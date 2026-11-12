package ticketflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

// The issue, note, and project shapes are pinned to the GitLab REST v4 API
// reviewed on 2026-11-11 against https://docs.gitlab.com/ee/api/:
// projects are addressed by URL-encoded path, issues by per-project iid,
// conversation arrives as notes (system notes record activity, internal
// notes are team-only), note writes normalize the body (quick actions,
// carriage returns, trailing whitespace), and project Markdown uploads
// download through GET /projects/:id/uploads/:secret/:filename. Re-review
// the GitLab REST documentation before changing these shapes.
const maxGitLabNoteBytes = 16 << 10
const maxGitLabNotes = 100
const gitLabAPI = "/api/v4"

// ErrGitLabReference rejects a workspace reference that is neither the
// numeric issue iid nor an issue URL of the configured project, before any
// request reaches GitLab.
var ErrGitLabReference = errors.New("use a GitLab issue number or the issue URL")

var gitLabUsername = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$`)
var gitLabUploadPath = regexp.MustCompile(`^/uploads/([0-9a-f]{32})/([A-Za-z0-9_][A-Za-z0-9_.-]{0,100}\.[A-Za-z0-9]{1,8})$`)
var gitLabIssueUploadPath = regexp.MustCompile(`^/-/issues/([1-9][0-9]{0,18})/uploads/([0-9a-f]{32})/([A-Za-z0-9_][A-Za-z0-9_.-]{0,100}\.[A-Za-z0-9]{1,8})$`)
var gitLabMarkdownLink = regexp.MustCompile(`\[[^\]\r\n]*\]\(([^()\s]+)\)`)

type gitlabSource struct {
	origin   string
	project  string
	token    string
	writable bool
	client   *http.Client
}

func newGitLab(origin, project, token string) *gitlabSource {
	if origin == "" {
		origin = "https://gitlab.com"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &gitlabSource{origin: strings.TrimSuffix(origin, "/"), project: project, token: token, client: &http.Client{
		Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (g *gitlabSource) account() string { return g.origin + "/" + g.project }

// gitlabProjectFromAccount recovers the project path from a GitLab account
// identifier, which is always the origin followed by the project path.
func gitlabProjectFromAccount(account string) string {
	u, err := url.Parse(account)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

func (g *gitlabSource) issueURL(iid int64) string {
	return g.account() + "/-/issues/" + strconv.FormatInt(iid, 10)
}

// gitlabProjectPath URL-encodes the project path for API requests. GitLab
// addresses projects by the full namespace path as a single path segment,
// with "/" encoded as %2F.
func gitlabProjectPath(project string) string {
	return url.PathEscape(project)
}

// gitlabDescriptionCommentID keys the issue-description pseudo-comment.
// GitLab assigns issue iids and note ids from unrelated sequences, so the
// description must not reuse the issue's own iid: a real note with that
// number would otherwise masquerade as the description in reply tracking.
func gitlabDescriptionCommentID(project string, iid int64) int64 {
	return gitLabHash("issue:" + project + ":" + strconv.FormatInt(iid, 10))
}

// gitLabHash maps a stable identifier to a positive 64-bit value for the
// shared case store, which keys comments by 64-bit ids.
func gitLabHash(value string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return int64(h.Sum64() & (1<<63 - 1))
}

type gitlabUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type gitlabIssue struct {
	IID         int64      `json:"iid"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       string     `json:"state"`
	Author      gitlabUser `json:"author"`
	Updated     string     `json:"updated_at"`
	Labels      []string   `json:"labels"`
}

type gitlabNote struct {
	ID     int64      `json:"id"`
	Body   string     `json:"body"`
	Author gitlabUser `json:"author"`
	System bool       `json:"system"`
	// Internal (formerly confidential) notes are team-only and never supply
	// requester evidence.
	Internal     bool   `json:"internal"`
	Confidential bool   `json:"confidential"`
	Created      string `json:"created_at"`
	Updated      string `json:"updated_at"`
	NoteableIID  int64  `json:"noteable_iid"`
	NoteableType string `json:"noteable_type"`
}

func validGitLabUser(user gitlabUser) bool {
	return user.ID > 0 && gitLabUsername.MatchString(user.Username)
}

func validGitLabTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

// request performs one bounded request against the configured GitLab
// instance. Paths are constructed from validated startup configuration,
// numeric ids, and validated upload names.
func (g *gitlabSource) request(ctx context.Context, method, path string, body []byte, limit int64) ([]byte, http.Header, error) {
	var reader io.Reader
	if body != nil {
		// An opaque body leaves GetBody unset so the HTTP transport cannot
		// replay this write, even on a reused connection.
		reader = io.NopCloser(bytes.NewReader(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, g.origin+gitLabAPI+path, reader)
	if err != nil {
		return nil, nil, errors.New("invalid GitLab request")
	}
	if body != nil {
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TriageLab")
	if g.token != "" {
		req.Header.Set("PRIVATE-TOKEN", g.token)
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, nil, errors.New("GitLab request could not be completed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		// Never echo provider response bodies.
		return nil, nil, fmt.Errorf("GitLab returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, nil, errors.New("GitLab response is incomplete or too large")
	}
	return data, res.Header, nil
}

// resolveReference accepts a workspace reference — the numeric issue iid or
// the issue URL of the configured project — and returns the numeric case
// key. The iid is per-project and the connector is pinned to one project,
// so no request is needed.
func (g *gitlabSource) resolveReference(reference string) (int64, error) {
	reference = strings.TrimSpace(reference)
	if i := strings.IndexAny(reference, "#?"); i >= 0 {
		reference = reference[:i]
	}
	reference = strings.TrimSuffix(reference, "/")
	switch {
	case strings.HasPrefix(reference, g.account()+"/-/issues/"):
		reference = strings.TrimPrefix(reference, g.account()+"/-/issues/")
	case strings.HasPrefix(reference, g.account()+"/issues/"):
		reference = strings.TrimPrefix(reference, g.account()+"/issues/")
	}
	id, err := strconv.ParseInt(reference, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != reference {
		return 0, ErrGitLabReference
	}
	return id, nil
}

func (g *gitlabSource) issue(ctx context.Context, iid int64) (gitlabIssue, error) {
	data, _, err := g.request(ctx, http.MethodGet, "/projects/"+gitlabProjectPath(g.project)+
		"/issues/"+strconv.FormatInt(iid, 10), nil, maxProviderBytes)
	if err != nil {
		return gitlabIssue{}, err
	}
	var issue gitlabIssue
	if json.Unmarshal(data, &issue) != nil {
		return gitlabIssue{}, errors.New("GitLab returned an invalid issue")
	}
	return issue, nil
}

func (g *gitlabSource) snapshot(ctx context.Context, id int64) (snapshot, error) {
	if id <= 0 {
		return snapshot{}, errors.New("use a positive GitLab issue number")
	}
	issue, err := g.issue(ctx, id)
	if err != nil {
		return snapshot{}, err
	}
	if issue.IID != id || !validGitLabUser(issue.Author) || !validGitLabTime(issue.Updated) ||
		(issue.State != "opened" && issue.State != "closed") {
		return snapshot{}, errors.New("GitLab returned an invalid issue")
	}
	if issue.Type != "" && issue.Type != "ISSUE" {
		return snapshot{}, errors.New("work item types other than issues are not supported; select a GitLab issue")
	}
	data, headers, err := g.request(ctx, http.MethodGet, "/projects/"+gitlabProjectPath(g.project)+
		"/issues/"+strconv.FormatInt(id, 10)+"/notes?order_by=created_at&sort=asc&per_page=100", nil, maxProviderBytes)
	if err != nil {
		return snapshot{}, err
	}
	var notes []gitlabNote
	if json.Unmarshal(data, &notes) != nil || notes == nil {
		return snapshot{}, errors.New("GitLab returned an invalid note page")
	}
	// Do not follow provider-supplied pagination. An empty X-Next-Page
	// proves the single bounded page holds every note, including the system
	// notes filtered below.
	if len(notes) > maxGitLabNotes || headers.Get("X-Next-Page") != "" {
		return snapshot{}, errors.New("issue exceeds the 100-note limit; review it in GitLab")
	}
	issueURL := g.issueURL(id)
	requester := issue.Author.ID
	t := ticket{ID: id, Requester: requester, Subject: issue.Title, Status: issue.State,
		Kind: "gitlab", URL: issueURL,
		RequesterProfile: Requester{ID: requester, Name: bounded(issue.Author.Name, 200),
			URL: g.origin + "/" + issue.Author.Username}}
	for _, label := range issue.Labels {
		t.Tags = append(t.Tags, bounded(label, 100))
	}
	result := snapshot{Ticket: t, Comments: []comment{{
		ID: gitlabDescriptionCommentID(g.project, id), Author: requester, Public: true,
		Body: issue.Description, URL: issueURL, Files: g.attachments(issue.Description, ""),
	}}}
	var previous int64
	for _, n := range notes {
		if n.ID <= previous || !validGitLabUser(n.Author) || !validGitLabTime(n.Created) || !validGitLabTime(n.Updated) {
			return snapshot{}, errors.New("GitLab returned an invalid issue note")
		}
		previous = n.ID
		if n.System {
			continue
		}
		result.Comments = append(result.Comments, comment{ID: n.ID, Author: n.Author.ID,
			Public: !n.Internal && !n.Confidential, Body: n.Body,
			URL:   issueURL + "#note_" + strconv.FormatInt(n.ID, 10),
			Files: g.attachments(n.Body, "/-/issues/"+strconv.FormatInt(id, 10))})
	}
	// Issue updated_at alone does not prove the note bodies are unchanged.
	// Include the complete bounded snapshot in the freshness receipt,
	// including edit times and visibility, so confirm detects note edits,
	// deletions, and visibility changes as well.
	result.Ticket.Updated = digest(struct {
		Issue gitlabIssue
		Notes []gitlabNote
	}{issue, notes})
	return result, nil
}

// attachments extracts project Markdown uploads from an issue description or
// note body. Relative /uploads links resolve against the base path they
// appeared under: the project for descriptions, the issue for notes.
func (g *gitlabSource) attachments(body, base string) []attachment {
	var files []attachment
	seen := make(map[string]bool)
	for _, match := range gitLabMarkdownLink.FindAllStringSubmatch(body, -1) {
		link := match[1]
		var relative string
		if strings.HasPrefix(link, "/uploads/") {
			relative = base + link
		} else if strings.HasPrefix(link, g.account()+"/") {
			// The account carries the origin and the project path; what
			// follows is already the project-relative upload path.
			relative = strings.TrimPrefix(link, g.account())
		} else {
			continue
		}
		secret, name, ok := g.uploadParts(relative)
		if !ok {
			continue
		}
		// The account already carries the project path.
		url := g.account() + relative
		if seen[url] {
			continue
		}
		seen[url] = true
		// Upload markdown does not include a size. -1 means unknown; file()
		// and the common preparation path enforce actual limits.
		files = append(files, attachment{Name: name, URL: url, Size: -1, ID: secretID(secret)})
	}
	return files
}

// uploadParts validates an upload path relative to the project and returns
// its secret and filename.
func (g *gitlabSource) uploadParts(path string) (string, string, bool) {
	if parts := gitLabIssueUploadPath.FindStringSubmatch(path); len(parts) != 0 {
		if iid, err := strconv.ParseInt(parts[1], 10, 64); err != nil || iid <= 0 {
			return "", "", false
		}
		return parts[2], parts[3], true
	}
	if parts := gitLabUploadPath.FindStringSubmatch(path); len(parts) != 0 {
		return parts[1], parts[2], true
	}
	return "", "", false
}

// secretID maps an upload secret to a stable numeric attachment id for case
// state.
func secretID(secret string) int64 {
	return gitLabHash("upload:" + secret)
}

func (g *gitlabSource) ticket(ctx context.Context, id int64) (ticket, error) {
	s, err := g.snapshot(ctx, id)
	return s.Ticket, err
}

func (g *gitlabSource) read(ctx context.Context, id int64) (snapshot, error) {
	s, err := g.snapshot(ctx, id)
	if err != nil {
		return snapshot{}, err
	}
	if err := g.confirm(ctx, s.Ticket); err != nil {
		return snapshot{}, err
	}
	return s, nil
}

func (g *gitlabSource) confirm(ctx context.Context, t ticket) error {
	current, err := g.ticket(ctx, t.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, t) {
		return ErrChanged
	}
	return nil
}

func (g *gitlabSource) file(ctx context.Context, f attachment) ([]byte, error) {
	u, err := url.Parse(f.URL)
	if err != nil || u.Scheme+"://"+u.Host != g.origin || u.User != nil ||
		u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("unsupported GitLab attachment location")
	}
	prefix := "/" + g.project
	if !strings.HasPrefix(u.Path, prefix+"/") {
		return nil, errors.New("unsupported GitLab attachment location")
	}
	secret, name, ok := g.uploadParts(strings.TrimPrefix(u.Path, prefix))
	if !ok || name != f.Name || !fileName.MatchString(reproductionName(f.Name)) ||
		secretID(secret) != f.ID {
		return nil, errors.New("unsupported GitLab attachment filename")
	}
	// Project uploads download through the authorized same-origin API path.
	data, _, err := g.request(ctx, http.MethodGet, "/projects/"+gitlabProjectPath(g.project)+
		"/uploads/"+secret+"/"+url.PathEscape(name), nil, casefile.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("GitLab attachments must be nonempty UTF-8 text files")
	}
	return data, nil
}

// gitlabNoteBody mirrors GitLab's documented note normalization: carriage
// returns are removed and trailing whitespace, including trailing newlines,
// is trimmed. A receipt body must round-trip to the sent message.
func gitlabNoteBody(message string) string {
	message = strings.ReplaceAll(message, "\r", "")
	return strings.TrimRight(message, " \t\n")
}

func (g *gitlabSource) postComment(ctx context.Context, t ticket, message string, public bool) (int64, error) {
	// GitLab issue notes are visible to everyone with access to the issue
	// and its project; internal notes are a newer team-only feature this
	// connector does not send. Only an explicitly enabled writer may post a
	// note, and the flow journals its reservation before calling.
	if !public || !g.writable || g.token == "" || t.ID <= 0 || t.Kind != "gitlab" || t.Status != "opened" ||
		t.URL != g.issueURL(t.ID) {
		return 0, ErrBlocked
	}
	if strings.TrimSpace(message) == "" || len(message) > maxGitLabNoteBytes || !utf8.ValidString(message) || strings.ContainsRune(message, '\x00') {
		return 0, errors.New("GitLab notes must be nonempty UTF-8 text of at most 16 KiB")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// GitLab does not support conditional writes for notes. Recheck
	// immediately before sending, but a concurrent edit can still race the
	// POST.
	if err := g.confirm(ctx, t); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	body, err := json.Marshal(struct {
		Body string `json:"body"`
	}{message})
	if err != nil {
		return 0, errors.New("could not encode the GitLab note")
	}
	data, _, err := g.request(ctx, http.MethodPost, "/projects/"+gitlabProjectPath(g.project)+
		"/issues/"+strconv.FormatInt(t.ID, 10)+"/notes", body, maxProviderBytes)
	if err != nil {
		return 0, err
	}
	var receipt gitlabNote
	if json.Unmarshal(data, &receipt) != nil || receipt.ID <= 0 ||
		receipt.NoteableType != "Issue" || receipt.NoteableIID != t.ID ||
		receipt.System || receipt.Internal || receipt.Confidential ||
		gitlabNoteBody(receipt.Body) != gitlabNoteBody(message) {
		return 0, errors.New("GitLab did not return a matching note receipt")
	}
	return receipt.ID, nil
}
