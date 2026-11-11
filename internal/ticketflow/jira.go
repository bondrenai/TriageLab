package ticketflow

import (
	"bytes"
	"context"
	"encoding/base64"
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

// The issue, comment, and attachment shapes are pinned to the Jira Cloud
// REST v3 API reviewed on 2026-11-11 against
// https://developer.atlassian.com/cloud/jira/platform/rest/v3/:
// issues are numeric-id resources with a PROJECT-key, descriptions and
// comment bodies are Atlassian Document Format (ADF), comments carry
// Jira Service Management visibility through the sd.public.comment entity
// property, attachments download through /rest/api/3/attachment/content/{id}
// with a single redirect to a signed api.media.atlassian.com URL, and an
// Atlassian account email plus API token authorize as HTTP basic. Re-review
// the Atlassian REST v3 documentation before changing these shapes.
const maxJiraCommentBytes = 16 << 10
const maxJiraComments = 100

// Jira timestamps carry milliseconds and a colon-less offset, not RFC 3339.
const jiraTimeLayout = "2006-01-02T15:04:05.000-0700"

// ErrJiraReference rejects a workspace reference that is neither a Jira issue
// key, a browse URL of the configured site, nor a numeric issue id, before
// any request reaches Jira.
var ErrJiraReference = errors.New("use a Jira issue key (PROJ-123), the issue URL, or the numeric issue id")

var jiraIssueKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}-[1-9][0-9]{0,9}$`)
var jiraProjectKey = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,9}$`)
var jiraAccountID = regexp.MustCompile(`^[0-9A-Za-z:._-]{1,128}$`)
var jiraAttachmentPath = regexp.MustCompile(`^/rest/api/3/attachment/content/([1-9][0-9]{0,18})$`)
var jiraMediaHost = "api.media.atlassian.com"

// Go limits each counted repetition to 1000; the two identical ranges retain
// the original maximum of 1023 trailing path characters.
var jiraMediaPath = regexp.MustCompile(`^/[A-Za-z0-9][A-Za-z0-9._~:@!$&'()*+,;=%/-]{0,1000}[A-Za-z0-9._~:@!$&'()*+,;=%/-]{0,23}$`)

type jiraSource struct {
	origin        string
	authorization string
	token         string
	writable      bool
	client        *http.Client
}

func newJira(origin, email, token string) *jiraSource {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &jiraSource{
		origin:        strings.TrimSuffix(origin, "/"),
		authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+token)),
		token:         token,
		client: &http.Client{
			Transport: transport, Timeout: 8 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (j *jiraSource) account() string { return j.origin }

func (j *jiraSource) issueURL(key string) string { return j.origin + "/browse/" + key }

// Comment anchors use Jira's documented focusedCommentId deep link.
func (j *jiraSource) commentURL(key string, id int64) string {
	return j.issueURL(key) + "?focusedCommentId=" + strconv.FormatInt(id, 10)
}

// jiraIssueKeyFromURL recovers the issue key pinned by a stored case URL of
// the configured site, or "" when the URL belongs to another origin.
func jiraIssueKeyFromURL(caseURL, origin string) string {
	key := strings.TrimPrefix(caseURL, origin+"/browse/")
	if jiraIssueKey.MatchString(key) {
		return key
	}
	return ""
}

// jiraIssueCommentID keys the issue-description pseudo-comment. Jira assigns
// issue ids and comment ids from separate sequences that can collide
// numerically, so the description must not reuse the issue's own id: a real
// requester comment with that number would otherwise masquerade as the
// description in reply tracking. The live 2026-11-11 pilot observed exactly
// that collision on bondren.atlassian.net.
func jiraIssueCommentID(id int64) int64 {
	return jiraAccountHash("issue:" + strconv.FormatInt(id, 10))
}

// Jira entity ids are numeric strings while the shared case store keys
// requesters and comments by 64-bit values. jiraAccountHash maps an Atlassian
// account id to a stable positive value.
func jiraAccountHash(accountID string) int64 {
	if !jiraAccountID.MatchString(accountID) {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(accountID))
	return int64(h.Sum64() & (1<<63 - 1))
}

// stringOrNumber accepts Jira ids, which the API documents as strings but
// returns as JSON numbers in some responses.
type stringOrNumber string

func (s *stringOrNumber) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*s = stringOrNumber(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		*s = stringOrNumber(number.String())
		return nil
	}
	return errors.New("Jira id is neither a string nor a number")
}

func (s stringOrNumber) value() int64 {
	id, err := strconv.ParseInt(string(s), 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != string(s) {
		return 0
	}
	return id
}

type jiraUser struct {
	AccountID   stringOrNumber `json:"accountId"`
	DisplayName string         `json:"displayName"`
	Email       string         `json:"emailAddress"`
}

type jiraIssue struct {
	ID     stringOrNumber `json:"id"`
	Key    string         `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
		Updated     string          `json:"updated"`
		Status      struct {
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		} `json:"status"`
		Labels     []string             `json:"labels"`
		Reporter   *jiraUser            `json:"reporter"`
		Attachment []jiraAttachmentMeta `json:"attachment"`
	} `json:"fields"`
}

type jiraAttachmentMeta struct {
	ID       stringOrNumber `json:"id"`
	Filename string         `json:"filename"`
	Size     int64          `json:"size"`
}

type jiraComment struct {
	ID      stringOrNumber  `json:"id"`
	Self    string          `json:"self"`
	Author  *jiraUser       `json:"author"`
	Body    json.RawMessage `json:"body"`
	Created string          `json:"created"`
	Updated string          `json:"updated"`
	// jsdPublic is present on Jira Service Management sites: false marks an
	// internal note. Restricted comments (visibility) are never requester
	// facing either.
	JSDPublic  *bool `json:"jsdPublic"`
	Visibility *struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"visibility"`
}

// call performs one bounded request against the configured Jira site. Reads
// may retry nothing; writes are never retried here.
func (j *jiraSource) call(ctx context.Context, method, path string, body []byte, limit int64, contentType string) ([]byte, error) {
	target, err := url.Parse(j.origin + path)
	if err != nil || target.Scheme+"://"+target.Host != j.origin || target.User != nil || target.Fragment != "" || target.RawPath != "" {
		return nil, errors.New("Jira resource is outside the configured site")
	}
	var reader io.Reader
	if body != nil {
		// An opaque body leaves GetBody unset so the HTTP transport cannot
		// replay this request, even on a reused connection.
		reader = io.NopCloser(bytes.NewReader(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, errors.New("invalid Jira request")
	}
	if body != nil {
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TriageLab")
	req.Header.Set("Authorization", j.authorization)
	res, err := j.client.Do(req)
	if err != nil {
		return nil, errors.New("Jira request could not be completed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		// Never echo provider response bodies.
		return nil, fmt.Errorf("Jira returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("Jira response is incomplete or too large")
	}
	return data, nil
}

func validJiraTime(value string) bool {
	_, err := time.Parse(jiraTimeLayout, value)
	return err == nil
}

// jiraStatusCategory fails closed on categories outside the documented set.
func jiraStatusCategory(key string) bool {
	return key == "new" || key == "indeterminate" || key == "done"
}

// checkJiraIssue validates the parts of the issue the connector depends on.
func checkJiraIssue(issue jiraIssue) error {
	if issue.ID.value() <= 0 || !jiraIssueKey.MatchString(issue.Key) || !validJiraTime(issue.Fields.Updated) ||
		!jiraStatusCategory(issue.Fields.Status.StatusCategory.Key) || issue.Fields.Reporter == nil ||
		jiraAccountHash(string(issue.Fields.Reporter.AccountID)) == 0 {
		return errors.New("Jira returned an invalid issue")
	}
	return nil
}

// issueByIDOrKey reads one issue. The numeric id stays the durable case key;
// the key is only the human URL and may change when the issue moves.
func (j *jiraSource) issueByIDOrKey(ctx context.Context, idOrKey string) (jiraIssue, error) {
	data, err := j.call(ctx, http.MethodGet, "/rest/api/3/issue/"+idOrKey+
		"?fields=summary,description,updated,status,labels,reporter,attachment", nil, maxProviderBytes, "")
	if err != nil {
		return jiraIssue{}, err
	}
	var issue jiraIssue
	if json.Unmarshal(data, &issue) != nil {
		return jiraIssue{}, errors.New("Jira returned an invalid issue")
	}
	if err := checkJiraIssue(issue); err != nil {
		return jiraIssue{}, err
	}
	return issue, nil
}

// resolveReference accepts a workspace reference — an issue key like PROJ-123,
// the issue URL of the configured site, or the numeric issue id — and returns
// the numeric case key. Key and URL lookups read the issue once so unusable
// issues are rejected before they are enrolled.
func (j *jiraSource) resolveReference(ctx context.Context, reference string) (int64, error) {
	reference = strings.TrimSpace(reference)
	if strings.HasPrefix(reference, j.origin+"/browse/") {
		reference = strings.TrimPrefix(reference, j.origin+"/browse/")
	}
	if jiraIssueKey.MatchString(reference) {
		issue, err := j.issueByIDOrKey(ctx, reference)
		if err != nil {
			return 0, err
		}
		if issue.Key != reference {
			return 0, errors.New("Jira returned an invalid issue for this key")
		}
		return issue.ID.value(), nil
	}
	id, err := strconv.ParseInt(reference, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != reference {
		return 0, ErrJiraReference
	}
	return id, nil
}

func (j *jiraSource) snapshot(ctx context.Context, id int64) (snapshot, error) {
	if id <= 0 {
		return snapshot{}, errors.New("use a positive Jira issue id")
	}
	issue, err := j.issueByIDOrKey(ctx, strconv.FormatInt(id, 10))
	if err != nil {
		return snapshot{}, err
	}
	if issue.ID.value() != id {
		return snapshot{}, errors.New("Jira returned an invalid issue")
	}
	data, err := j.call(ctx, http.MethodGet, "/rest/api/3/issue/"+strconv.FormatInt(id, 10)+
		"/comment?maxResults=100&orderBy=created", nil, maxProviderBytes, "")
	if err != nil {
		return snapshot{}, err
	}
	var page struct {
		Comments   []jiraComment `json:"comments"`
		StartAt    int           `json:"startAt"`
		MaxResults int           `json:"maxResults"`
		Total      int           `json:"total"`
	}
	if json.Unmarshal(data, &page) != nil || page.Comments == nil || page.StartAt != 0 ||
		len(page.Comments) > maxJiraComments || page.MaxResults < len(page.Comments) {
		return snapshot{}, errors.New("Jira returned an invalid comment page")
	}
	// Do not follow provider-supplied pagination. The total detects truncated
	// pages and concurrent additions or deletions.
	if page.Total > maxJiraComments {
		return snapshot{}, errors.New("issue exceeds the 100-comment limit; review it in Jira")
	}
	if len(page.Comments) != page.Total {
		return snapshot{}, errors.New("Jira comments changed or exceed the 100-comment limit; refresh the issue")
	}
	requester := jiraAccountHash(string(issue.Fields.Reporter.AccountID))
	t := ticket{ID: id, Requester: requester, Subject: issue.Fields.Summary,
		Status: issue.Fields.Status.StatusCategory.Key, Kind: "jira", URL: j.issueURL(issue.Key),
		uuid: strconv.FormatInt(id, 10),
		RequesterProfile: Requester{ID: requester,
			Name:  bounded(issue.Fields.Reporter.DisplayName, 200),
			Email: bounded(issue.Fields.Reporter.Email, 320)}}
	for _, label := range issue.Fields.Labels {
		t.Tags = append(t.Tags, bounded(label, 100))
	}
	description := comment{ID: jiraIssueCommentID(id), Author: requester,
		Public: true, Body: jiraADFText(issue.Fields.Description), URL: j.issueURL(issue.Key),
		Files: j.attachments(issue.Fields.Attachment)}
	result := snapshot{Ticket: t, Comments: []comment{description}}
	seen := make(map[int64]bool, len(page.Comments)+1)
	seen[description.ID] = true
	for _, c := range page.Comments {
		commentID := c.ID.value()
		if commentID == 0 || seen[commentID] || c.Author == nil ||
			jiraAccountHash(string(c.Author.AccountID)) == 0 ||
			!validJiraTime(c.Created) || !validJiraTime(c.Updated) || !utf8.Valid(c.Body) {
			return snapshot{}, errors.New("Jira returned an invalid issue comment")
		}
		seen[commentID] = true
		result.Comments = append(result.Comments, comment{ID: commentID,
			Author: jiraAccountHash(string(c.Author.AccountID)),
			Public: c.Visibility == nil && (c.JSDPublic == nil || *c.JSDPublic),
			Body:   jiraADFText(c.Body), URL: j.commentURL(issue.Key, commentID)})
	}
	// Issue updated alone does not prove the comment bodies are unchanged.
	// Include the complete bounded snapshot in the freshness receipt, including
	// edit times and comment visibility, so confirm detects edits, deletions,
	// and visibility changes as well.
	result.Ticket.Updated = digest(struct {
		Issue    jiraIssue
		Comments []jiraComment
	}{issue, page.Comments})
	return result, nil
}

// Every attachment on the issue is offered with the connector's authorized
// download path; selection still enforces supported names and sizes.
func (j *jiraSource) attachments(files []jiraAttachmentMeta) []attachment {
	var result []attachment
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if f.ID.value() == 0 || f.Size <= 0 || f.Size > casefile.MaxFileBytes || seen[string(f.ID)] ||
			!fileName.MatchString(reproductionName(f.Filename)) {
			continue
		}
		seen[string(f.ID)] = true
		result = append(result, attachment{Name: f.Filename, Size: f.Size,
			ID:  f.ID.value(),
			URL: j.origin + "/rest/api/3/attachment/content/" + string(f.ID)})
	}
	return result
}

func (j *jiraSource) ticket(ctx context.Context, id int64) (ticket, error) {
	s, err := j.snapshot(ctx, id)
	return s.Ticket, err
}

func (j *jiraSource) read(ctx context.Context, id int64) (snapshot, error) {
	s, err := j.snapshot(ctx, id)
	if err != nil {
		return snapshot{}, err
	}
	if err := j.confirm(ctx, s.Ticket); err != nil {
		return snapshot{}, err
	}
	return s, nil
}

func (j *jiraSource) confirm(ctx context.Context, t ticket) error {
	current, err := j.ticket(ctx, t.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, t) {
		return ErrChanged
	}
	return nil
}

func (j *jiraSource) file(ctx context.Context, f attachment) ([]byte, error) {
	u, err := url.Parse(f.URL)
	if err != nil || u.Scheme+"://"+u.Host != j.origin || u.User != nil || u.Fragment != "" || u.RawPath != "" ||
		u.RawQuery != "" || u.ForceQuery || !jiraAttachmentPath.MatchString(u.Path) ||
		f.Name == "" || !fileName.MatchString(reproductionName(f.Name)) {
		return nil, errors.New("unsupported Jira attachment location")
	}
	parts := jiraAttachmentPath.FindStringSubmatch(u.Path)
	if parts[1] != strconv.FormatInt(f.ID, 10) {
		return nil, errors.New("unsupported Jira attachment location")
	}
	// The authorized content endpoint answers with one redirect to a signed
	// media URL. The redirect is followed manually so the site credential
	// never travels to the media host; the signed URL itself is the grant.
	res, err := j.fetch(ctx, u.Path)
	if err != nil {
		return nil, err
	}
	switch res.StatusCode {
	case http.StatusFound, http.StatusMovedPermanently, http.StatusSeeOther, http.StatusTemporaryRedirect:
		target, parseErr := url.Parse(res.Header.Get("Location"))
		res.Body.Close()
		if parseErr != nil || target.Scheme != "https" || target.Host != jiraMediaHost ||
			target.User != nil || target.Fragment != "" || target.RawPath != "" ||
			!jiraMediaPath.MatchString(target.Path) {
			return nil, errors.New("unsupported Jira attachment redirect")
		}
		res, err = j.fetchMedia(ctx, target.String())
		if err != nil {
			return nil, err
		}
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Jira attachment download returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, casefile.MaxFileBytes+1))
	if err != nil || int64(len(data)) > casefile.MaxFileBytes {
		return nil, errors.New("Jira response is incomplete or too large")
	}
	if int64(len(data)) != f.Size {
		return nil, errors.New("Jira attachment size changed")
	}
	if len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("Jira attachments must be nonempty UTF-8 text files")
	}
	return data, nil
}

// fetch performs the authorized GET for an API path on the site without
// reading the body, so callers can inspect the status first.
func (j *jiraSource) fetch(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.origin+path, nil)
	if err != nil {
		return nil, errors.New("invalid Jira request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "TriageLab")
	req.Header.Set("Authorization", j.authorization)
	res, err := j.client.Do(req)
	if err != nil {
		return nil, errors.New("Jira request could not be completed")
	}
	return res, nil
}

// fetchMedia downloads the signed media URL without any credential.
func (j *jiraSource) fetchMedia(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errors.New("invalid Jira attachment redirect request")
	}
	req.Header.Set("User-Agent", "TriageLab")
	res, err := j.client.Do(req)
	if err != nil {
		return nil, errors.New("Jira attachment download could not be completed")
	}
	return res, nil
}

// jiraADFBody encodes plain text as an ADF document with one paragraph whose
// line breaks are hardBreak nodes. hardBreak is the only valid ADF encoding
// of a newline, so the representation round-trips through jiraADFText.
func jiraADFBody(message string) []byte {
	type node struct {
		Type  string `json:"type"`
		Text  string `json:"text,omitempty"`
		Nodes []node `json:"content,omitempty"`
	}
	message = strings.ReplaceAll(strings.ReplaceAll(message, "\r\n", "\n"), "\r", "\n")
	var content []node
	var line strings.Builder
	for _, r := range message {
		switch r {
		case '\n':
			if line.Len() > 0 {
				content = append(content, node{Type: "text", Text: line.String()})
				line.Reset()
			}
			content = append(content, node{Type: "hardBreak"})
		case '\t':
			line.WriteRune(r)
		default:
			if r >= 0x20 || r == '\u00a0' {
				line.WriteRune(r)
			}
		}
	}
	if line.Len() > 0 {
		content = append(content, node{Type: "text", Text: line.String()})
	}
	if len(content) == 0 {
		content = []node{{Type: "text", Text: " "}}
	}
	body, err := json.Marshal(struct {
		Type    string `json:"type"`
		Version int    `json:"version"`
		Content []node `json:"content"`
	}{"doc", 1, []node{{Type: "paragraph", Nodes: content}}})
	if err != nil {
		panic(err)
	}
	return body
}

// jiraADFText renders the text content of an Atlassian Document Format
// body. Known layout blocks become line breaks; unhandled node types are
// still searched for nested text. Imported markup stays inert data.
func jiraADFText(doc []byte) string {
	if len(doc) == 0 || string(doc) == "null" {
		return ""
	}
	type node struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	var root struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(doc, &root) != nil {
		return ""
	}
	var out strings.Builder
	budget := 10000
	var walk func(raw json.RawMessage)
	walk = func(raw json.RawMessage) {
		if budget <= 0 || out.Len() > 64<<10 {
			budget = 0
			return
		}
		budget--
		var current node
		if json.Unmarshal(raw, &current) != nil {
			return
		}
		switch current.Type {
		case "text":
			out.WriteString(current.Text)
		case "hardBreak":
			out.WriteByte('\n')
		}
		var children []json.RawMessage
		if len(current.Content) != 0 && json.Unmarshal(current.Content, &children) == nil {
			for _, child := range children {
				walk(child)
			}
		}
		switch current.Type {
		case "paragraph", "heading", "codeBlock", "mediaSingle", "listItem", "tableRow", "tableCell", "blockquote", "panel":
			out.WriteByte('\n')
		}
	}
	var children []json.RawMessage
	if json.Unmarshal(root.Content, &children) == nil {
		for _, child := range children {
			walk(child)
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

// jiraCommentControl sets Jira Service Management comment visibility
// explicitly so delivery never depends on a project's default.
type jiraCommentControl struct {
	Key   string `json:"key"`
	Value struct {
		Internal bool `json:"internal"`
	} `json:"value"`
}

type jiraCommentWrite struct {
	Body       json.RawMessage      `json:"body"`
	Properties []jiraCommentControl `json:"properties"`
}

func (j *jiraSource) postComment(ctx context.Context, t ticket, message string, public bool) (int64, error) {
	// Clarifications are customer-visible replies on Jira Service Management
	// sites and ordinary comments elsewhere; investigation notes are internal.
	// Visibility is set explicitly through the sd.public.comment entity
	// property and never left to a project's default. Only an explicitly
	// enabled writer may post, and the flow journals its reservation before
	// calling.
	if !j.writable || j.token == "" || t.ID <= 0 || t.Kind != "jira" ||
		!openForInvestigation("jira", t.Status) || t.uuid != strconv.FormatInt(t.ID, 10) ||
		jiraIssueKeyFromURL(t.URL, j.origin) == "" {
		return 0, ErrBlocked
	}
	if strings.TrimSpace(message) == "" || len(message) > maxJiraCommentBytes || !utf8.ValidString(message) || strings.ContainsRune(message, '\x00') {
		return 0, errors.New("Jira comments must be nonempty UTF-8 text of at most 16 KiB")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Jira has no conditional writes for comments. Recheck immediately before
	// sending, but a concurrent edit can still race the POST.
	if err := j.confirm(ctx, t); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	control := jiraCommentControl{Key: "sd.public.comment"}
	control.Value.Internal = !public
	body, err := json.Marshal(jiraCommentWrite{Body: jiraADFBody(message), Properties: []jiraCommentControl{control}})
	if err != nil {
		return 0, errors.New("could not encode the Jira comment")
	}
	data, err := j.call(ctx, http.MethodPost, "/rest/api/3/issue/"+t.uuid+"/comment", body, maxProviderBytes, "application/json")
	if err != nil {
		return 0, err
	}
	var receipt jiraComment
	if json.Unmarshal(data, &receipt) != nil || receipt.ID.value() <= 0 ||
		jiraADFText(receipt.Body) != message ||
		receipt.Self != j.origin+"/rest/api/3/issue/"+t.uuid+"/comment/"+string(receipt.ID) {
		return 0, errors.New("Jira did not return a matching comment receipt")
	}
	return receipt.ID.value(), nil
}
