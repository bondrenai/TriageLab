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

// The GraphQL endpoint and issue shapes are pinned to the documented Linear
// API; review https://linear.app/developers/graphql before changing them.
const linearAPIOrigin = "https://api.linear.app"
const maxLinearCommentBytes = 16 << 10
const maxLinearComments = 100

// Request signed upload URLs so attachments download without the API key.
const linearFileURLLifetime = "3600"

var linearUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var linearUploadHost = regexp.MustCompile(`^(?:uploads\.linear\.app|[a-z0-9-]{1,63}\.linearusercontent\.com)$`)

// Upload paths are two UUID segments plus an optional original file name.
var linearUploadPath = regexp.MustCompile(`^(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}){2}(?:/([A-Za-z0-9_][A-Za-z0-9_.-]{0,108}))?$`)
var linearMarkdownLink = regexp.MustCompile(`(!?\[[^\]\r\n]*\])\((https://[^\s<>\)]+)\)`)

type linearSource struct {
	workspace string
	team      string
	token     string
	writable  bool
	client    *http.Client
}

func newLinear(workspace, team, token string) *linearSource {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &linearSource{workspace: workspace, team: team, token: token, client: &http.Client{
		Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (l *linearSource) account() string { return "https://linear.app/" + l.workspace + "/" + l.team }

// Linear identifiers have the form TEAMKEY-number, so the configured team key
// and the issue number reconstruct the canonical, slug-free issue URL.
func (l *linearSource) identifier(number int64) string {
	return l.team + "-" + strconv.FormatInt(number, 10)
}

func (l *linearSource) issueURL(number int64) string {
	return l.account() + "/issue/" + l.identifier(number)
}

// Linear entity IDs are UUIDs while the shared case store keys authors and
// comments by 64-bit values. linearID maps a UUID to a stable positive value.
func linearID(uuid string) int64 {
	if !linearUUID.MatchString(uuid) {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(uuid))
	return int64(h.Sum64() & (1<<63 - 1))
}

type linearUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type linearIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Number      int64  `json:"number"`
	Title       string `json:"title"`
	Description string `json:"description"`
	UpdatedAt   string `json:"updatedAt"`
	CreatedAt   string `json:"createdAt"`
	State       struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Creator *linearUser `json:"creator"`
	Labels  *struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

type linearComment struct {
	ID        string      `json:"id"`
	Body      string      `json:"body"`
	User      *linearUser `json:"user"`
	CreatedAt string      `json:"createdAt"`
	UpdatedAt string      `json:"updatedAt"`
}

const linearIssueDocument = `query TriageLabIssue($team: String!, $number: Int!) {
  team(id: $team) {
    id key
    issues(first: 2, filter: {number: {eq: $number}}, orderBy: number) {
      nodes { id identifier number title description updatedAt createdAt
        state { name type } creator { id name } labels(first: 25) { nodes { name } } }
    }
  }
}`

const linearCommentsDocument = `query TriageLabComments($issue: String!) {
  issue(id: $issue) {
    id identifier number
    comments(first: 100, orderBy: createdAt) {
      nodes { id body createdAt updatedAt user { id name } }
      pageInfo { hasNextPage }
    }
  }
}`

// graphQL performs one bounded POST against the pinned Linear endpoint. Reads
// request signed attachment URLs; writes are never retried here.
func (l *linearSource) graphQL(ctx context.Context, document string, variables map[string]any, signedFiles bool) ([]byte, error) {
	payload, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{document, variables})
	if err != nil || len(payload) > 16<<10 {
		return nil, errors.New("invalid Linear request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearAPIOrigin+"/graphql", io.NopCloser(bytes.NewReader(payload)))
	if err != nil {
		return nil, errors.New("invalid Linear request")
	}
	// Supplying an opaque body leaves GetBody unset so the HTTP transport
	// cannot replay this request, even on a reused connection.
	req.ContentLength = int64(len(payload))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TriageLab")
	// Linear API keys are sent as the raw Authorization value.
	req.Header.Set("Authorization", l.token)
	if signedFiles {
		req.Header.Set("public-file-urls-expire-in", linearFileURLLifetime)
	}
	res, err := l.client.Do(req)
	if err != nil {
		return nil, errors.New("Linear request could not be completed")
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxProviderBytes+1))
	if err != nil || len(data) > maxProviderBytes {
		return nil, errors.New("Linear response is incomplete or too large")
	}
	if res.StatusCode != http.StatusOK {
		// Never echo provider response bodies or signed attachment URLs.
		return nil, fmt.Errorf("Linear returned HTTP %d", res.StatusCode)
	}
	var envelope struct {
		Errors []struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"errors"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, errors.New("Linear returned an invalid response")
	}
	if len(envelope.Errors) != 0 {
		code := envelope.Errors[0].Type
		if code == "AUTHENTICATION_ERROR" || code == "UNAUTHENTICATED" || code == "INVALID_TOKEN" {
			return nil, ErrOAuthRequired
		}
		return nil, errors.New("Linear rejected the request")
	}
	return envelope.Data, nil
}

func validLinearTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func (l *linearSource) snapshot(ctx context.Context, id int64) (snapshot, error) {
	if id <= 0 {
		return snapshot{}, errors.New("use a positive Linear issue number")
	}
	data, err := l.graphQL(ctx, linearIssueDocument, map[string]any{"team": l.team, "number": id}, true)
	if err != nil {
		return snapshot{}, err
	}
	var query struct {
		Team *struct {
			Key    string `json:"key"`
			Issues *struct {
				Nodes []linearIssue `json:"nodes"`
			} `json:"issues"`
		} `json:"team"`
	}
	if json.Unmarshal(data, &query) != nil || query.Team == nil || query.Team.Issues == nil || len(query.Team.Issues.Nodes) != 1 {
		return snapshot{}, errors.New("Linear returned an invalid issue lookup; use the issue number within the configured team")
	}
	issue := query.Team.Issues.Nodes[0]
	if !linearUUID.MatchString(issue.ID) || issue.Number != id || query.Team.Key != l.team ||
		issue.Identifier != l.identifier(id) || issue.State.Type == "" ||
		!validLinearTime(issue.UpdatedAt) || !validLinearTime(issue.CreatedAt) ||
		(issue.Creator != nil && linearID(issue.Creator.ID) == 0) {
		return snapshot{}, errors.New("Linear returned an invalid issue")
	}
	data, err = l.graphQL(ctx, linearCommentsDocument, map[string]any{"issue": issue.ID}, true)
	if err != nil {
		return snapshot{}, err
	}
	var commentsQuery struct {
		Issue *struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
			Number     int64  `json:"number"`
			Comments   *struct {
				Nodes    []linearComment `json:"nodes"`
				PageInfo struct {
					HasNextPage bool `json:"hasNextPage"`
				} `json:"pageInfo"`
			} `json:"comments"`
		} `json:"issue"`
	}
	if json.Unmarshal(data, &commentsQuery) != nil || commentsQuery.Issue == nil ||
		commentsQuery.Issue.ID != issue.ID || commentsQuery.Issue.Identifier != issue.Identifier ||
		commentsQuery.Issue.Number != id || commentsQuery.Issue.Comments == nil ||
		len(commentsQuery.Issue.Comments.Nodes) > maxLinearComments {
		return snapshot{}, errors.New("Linear returned an invalid comment page")
	}
	// Do not follow provider-supplied pagination. More than 100 comments, or a
	// page that reports more, requires review at the source.
	if commentsQuery.Issue.Comments.PageInfo.HasNextPage {
		return snapshot{}, errors.New("issue exceeds the 100-comment limit; review it in Linear")
	}
	issueURL := l.issueURL(id)
	t := ticket{ID: id, Requester: linearID(creatorID(issue.Creator)), Subject: issue.Title,
		Status: issue.State.Type, Kind: "linear", URL: issueURL, uuid: issue.ID,
		RequesterProfile: Requester{ID: linearID(creatorID(issue.Creator))},
	}
	if issue.Creator != nil {
		t.RequesterProfile.Name = bounded(issue.Creator.Name, 200)
	}
	if issue.Labels != nil {
		for _, label := range issue.Labels.Nodes {
			t.Tags = append(t.Tags, bounded(label.Name, 100))
		}
	}
	result := snapshot{Ticket: t, Comments: []comment{{ID: linearID(issue.ID), Author: t.Requester,
		Public: true, Body: issue.Description, URL: issueURL, Files: linearAttachments(issue.Description)}}}
	seen := make(map[int64]bool, len(commentsQuery.Issue.Comments.Nodes))
	for _, c := range commentsQuery.Issue.Comments.Nodes {
		id := linearID(c.ID)
		if id == 0 || seen[id] || c.User == nil || linearID(c.User.ID) == 0 ||
			!validLinearTime(c.CreatedAt) || !validLinearTime(c.UpdatedAt) {
			return snapshot{}, errors.New("Linear returned an invalid issue comment")
		}
		seen[id] = true
		result.Comments = append(result.Comments, comment{ID: id, Author: linearID(c.User.ID), Public: true,
			Body: c.Body, URL: issueURL + "#comment-" + c.ID, Files: linearAttachments(c.Body)})
	}
	// Issue updatedAt alone does not prove the comment bodies are unchanged.
	// Include the complete bounded snapshot in the freshness receipt, including
	// edit times, so confirm detects comment edits and deletions as well.
	result.Ticket.Updated = digest(struct {
		Issue    linearIssue
		Comments []linearComment
	}{issue, commentsQuery.Issue.Comments.Nodes})
	return result, nil
}

func creatorID(user *linearUser) string {
	if user == nil {
		return ""
	}
	return user.ID
}

func (l *linearSource) ticket(ctx context.Context, id int64) (ticket, error) {
	s, err := l.snapshot(ctx, id)
	return s.Ticket, err
}

func (l *linearSource) read(ctx context.Context, id int64) (snapshot, error) {
	s, err := l.snapshot(ctx, id)
	if err != nil {
		return snapshot{}, err
	}
	if err := l.confirm(ctx, s.Ticket); err != nil {
		return snapshot{}, err
	}
	return s, nil
}

func (l *linearSource) confirm(ctx context.Context, t ticket) error {
	current, err := l.ticket(ctx, t.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, t) {
		return ErrChanged
	}
	return nil
}

// linearAttachments collects Linear upload links from markdown bodies. File
// uploads render as links to Linear's signed private storage; the link text
// carries the original file name when the path does not.
func linearAttachments(body string) []attachment {
	var files []attachment
	seen := make(map[string]bool)
	for _, match := range linearMarkdownLink.FindAllStringSubmatch(body, -1) {
		u, err := url.Parse(match[2])
		if err != nil || u.Scheme != "https" || u.User != nil || u.RawPath != "" ||
			u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || !linearUploadHost.MatchString(u.Host) {
			continue
		}
		parts := linearUploadPath.FindStringSubmatch(u.Path)
		if parts == nil || seen[u.String()] {
			continue
		}
		name := parts[2]
		if name == "" {
			// Upload links carry the original file name as the link text.
			name = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(match[1], "!"), "["), "]")
		}
		if !fileName.MatchString(reproductionName(name)) {
			continue
		}
		seen[u.String()] = true
		// Linear upload URLs do not state a size. -1 means unknown; file() and
		// the common preparation path enforce actual limits.
		files = append(files, attachment{Name: name, URL: u.String(), Size: -1})
	}
	return files
}

func (l *linearSource) file(ctx context.Context, f attachment) ([]byte, error) {
	u, err := url.Parse(f.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawPath != "" ||
		u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || !linearUploadHost.MatchString(u.Host) {
		return nil, errors.New("unsupported Linear attachment location")
	}
	parts := linearUploadPath.FindStringSubmatch(u.Path)
	if parts == nil || (parts[2] != "" && parts[2] != f.Name) || f.Name == "" ||
		!fileName.MatchString(reproductionName(f.Name)) {
		return nil, errors.New("unsupported Linear attachment filename")
	}
	// Signed upload URLs already grant access; the request carries neither the
	// API key nor browser cookies.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid Linear attachment request")
	}
	req.Header.Set("User-Agent", "TriageLab")
	res, err := l.client.Do(req)
	if err != nil {
		return nil, errors.New("Linear attachment download could not be completed")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Linear attachment download returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, casefile.MaxFileBytes+1))
	if err != nil || len(data) > casefile.MaxFileBytes {
		return nil, errors.New("Linear response is incomplete or too large")
	}
	if len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("Linear attachments must be nonempty UTF-8 text files")
	}
	return data, nil
}

func (l *linearSource) postComment(ctx context.Context, t ticket, message string, _ bool) (int64, error) {
	// Linear issue comments are visible to the workspace. Only an explicitly
	// enabled writer may post, and the flow journals its reservation before
	// calling.
	if !l.writable || l.token == "" || t.ID <= 0 || t.Kind != "linear" ||
		!openForInvestigation("linear", t.Status) || t.URL != l.issueURL(t.ID) {
		return 0, ErrBlocked
	}
	if strings.TrimSpace(message) == "" || len(message) > maxLinearCommentBytes || !utf8.ValidString(message) || strings.ContainsRune(message, '\x00') {
		return 0, errors.New("Linear comments must be nonempty UTF-8 text of at most 16 KiB")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Linear does not support conditional writes for comments. Recheck
	// immediately before sending, but a concurrent edit can still race.
	if err := l.confirm(ctx, t); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	const document = `mutation TriageLabComment($input: CommentCreateInput!) {
  commentCreate(input: $input) { success comment { id body user { id } issue { id number identifier } } }
}`
	data, err := l.graphQL(ctx, document, map[string]any{
		"input": map[string]any{"issueId": t.uuid, "body": message},
	}, false)
	if err != nil {
		return 0, err
	}
	var mutation struct {
		CommentCreate *struct {
			Success bool `json:"success"`
			Comment *struct {
				ID    string      `json:"id"`
				Body  string      `json:"body"`
				User  *linearUser `json:"user"`
				Issue *struct {
					ID         string `json:"id"`
					Number     int64  `json:"number"`
					Identifier string `json:"identifier"`
				} `json:"issue"`
			} `json:"comment"`
		} `json:"commentCreate"`
	}
	if json.Unmarshal(data, &mutation) != nil || mutation.CommentCreate == nil || !mutation.CommentCreate.Success ||
		mutation.CommentCreate.Comment == nil || linearID(mutation.CommentCreate.Comment.ID) == 0 ||
		mutation.CommentCreate.Comment.Body != message || mutation.CommentCreate.Comment.User == nil ||
		linearID(mutation.CommentCreate.Comment.User.ID) == 0 || mutation.CommentCreate.Comment.Issue == nil ||
		mutation.CommentCreate.Comment.Issue.ID != t.uuid || mutation.CommentCreate.Comment.Issue.Number != t.ID ||
		mutation.CommentCreate.Comment.Issue.Identifier != l.identifier(t.ID) {
		return 0, errors.New("Linear did not return a matching comment receipt")
	}
	return linearID(mutation.CommentCreate.Comment.ID), nil
}
