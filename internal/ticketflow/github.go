package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Review the issue and comment schemas before updating this API pin.
const githubAPIVersion = "2026-03-10"

var githubLogin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,99}(?:\[bot\])?$`)
var githubFilePath = regexp.MustCompile(`^/user-attachments/files/([1-9][0-9]*)/([A-Za-z0-9_][A-Za-z0-9_.-]{0,108})$`)
var githubFileRedirectPath = regexp.MustCompile(`^/github-production-repository-file-5c1aeb/[1-9][0-9]*/([1-9][0-9]*)$`)
var githubMarkdownLink = regexp.MustCompile(`\[[^\]\r\n]*\]\((https://[^\s<>\)]+)\)`)

type githubSource struct {
	repository string
	token      string
	client     *http.Client
}

func newGitHub(repository, token string) *githubSource {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &githubSource{repository: repository, token: token, client: &http.Client{
		Transport: transport, Timeout: 8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (g *githubSource) account() string { return "https://github.com/" + g.repository }

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

type githubIssue struct {
	ID          int64           `json:"id"`
	Number      int64           `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	User        githubUser      `json:"user"`
	Updated     string          `json:"updated_at"`
	Comments    *int            `json:"comments"`
	PullRequest json.RawMessage `json:"pull_request"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type githubComment struct {
	ID      int64      `json:"id"`
	Body    string     `json:"body"`
	User    githubUser `json:"user"`
	Updated string     `json:"updated_at"`
}

func (g *githubSource) api(ctx context.Context, path string) ([]byte, http.Header, error) {
	// Paths are constructed from validated startup configuration and numeric IDs.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+g.repository+path, nil)
	if err != nil {
		return nil, nil, errors.New("invalid GitHub request")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", "TriageLab")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, nil, errors.New("GitHub request could not be completed")
	}
	defer res.Body.Close()
	data, err := readGitHubResponse(res, maxProviderBytes)
	return data, res.Header, err
}

func readGitHubResponse(res *http.Response, limit int64) ([]byte, error) {
	if res.StatusCode != http.StatusOK {
		// Never echo provider response bodies or signed attachment URLs.
		return nil, fmt.Errorf("GitHub returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("GitHub response is incomplete or too large")
	}
	return data, nil
}

func validGitHubUser(user githubUser) bool {
	return user.ID > 0 && githubLogin.MatchString(user.Login)
}

func validGitHubTime(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func (g *githubSource) snapshot(ctx context.Context, id int64) (snapshot, error) {
	if id <= 0 {
		return snapshot{}, errors.New("use a positive GitHub issue number")
	}
	path := "/issues/" + strconv.FormatInt(id, 10)
	data, _, err := g.api(ctx, path)
	if err != nil {
		return snapshot{}, err
	}
	var issue githubIssue
	if json.Unmarshal(data, &issue) != nil || issue.ID <= 0 || issue.Number != id || !validGitHubUser(issue.User) ||
		!validGitHubTime(issue.Updated) || issue.Comments == nil || *issue.Comments < 0 ||
		(issue.State != "open" && issue.State != "closed") {
		return snapshot{}, errors.New("GitHub returned an invalid issue")
	}
	if len(issue.PullRequest) != 0 && string(issue.PullRequest) != "null" {
		return snapshot{}, errors.New("pull requests are not supported; select a GitHub issue")
	}
	if *issue.Comments > 100 {
		return snapshot{}, errors.New("issue exceeds the 100-comment limit; review it in GitHub")
	}
	data, headers, err := g.api(ctx, path+"/comments?per_page=100&page=1")
	if err != nil {
		return snapshot{}, err
	}
	var comments []githubComment
	if json.Unmarshal(data, &comments) != nil || comments == nil || len(comments) > 100 {
		return snapshot{}, errors.New("GitHub returned an invalid comment page")
	}
	// Do not follow provider-supplied pagination URLs. The issue count also
	// detects incomplete pages and concurrent additions or deletions.
	if headers.Get("Link") != "" || len(comments) != *issue.Comments {
		return snapshot{}, errors.New("GitHub comments changed or exceed the 100-comment limit; refresh the issue")
	}
	issueURL := g.account() + path
	t := ticket{ID: id, Requester: issue.User.ID, Subject: issue.Title, Status: issue.State,
		Kind: "github", URL: issueURL,
		RequesterProfile: Requester{ID: issue.User.ID, Name: issue.User.Login, URL: "https://github.com/" + url.PathEscape(issue.User.Login)},
	}
	for _, label := range issue.Labels {
		t.Tags = append(t.Tags, label.Name)
	}
	result := snapshot{Ticket: t, Comments: []comment{{ID: issue.ID, Author: issue.User.ID,
		Public: true, Body: issue.Body, URL: issueURL, Files: githubAttachments(issue.Body)}}}
	var previous int64
	for _, c := range comments {
		if c.ID <= previous || !validGitHubUser(c.User) || !validGitHubTime(c.Updated) {
			return snapshot{}, errors.New("GitHub returned an invalid issue comment")
		}
		previous = c.ID
		result.Comments = append(result.Comments, comment{ID: c.ID, Author: c.User.ID, Public: true,
			Body: c.Body, URL: issueURL + "#issuecomment-" + strconv.FormatInt(c.ID, 10), Files: githubAttachments(c.Body)})
	}
	// Issue updated_at alone does not prove the comment bodies are unchanged.
	// Include the complete bounded snapshot in the freshness receipt, including
	// edit times, so confirm detects comment edits and deletions as well.
	result.Ticket.Updated = digest(struct {
		Issue    githubIssue
		Comments []githubComment
	}{issue, comments})
	return result, nil
}

func (g *githubSource) ticket(ctx context.Context, id int64) (ticket, error) {
	s, err := g.snapshot(ctx, id)
	return s.Ticket, err
}

func (g *githubSource) read(ctx context.Context, id int64) (snapshot, error) {
	s, err := g.snapshot(ctx, id)
	if err != nil {
		return snapshot{}, err
	}
	if err := g.confirm(ctx, s.Ticket); err != nil {
		return snapshot{}, err
	}
	return s, nil
}

func (g *githubSource) confirm(ctx context.Context, t ticket) error {
	current, err := g.ticket(ctx, t.ID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, t) {
		return ErrChanged
	}
	return nil
}

func githubAttachments(body string) []attachment {
	var files []attachment
	seen := make(map[string]bool)
	for _, match := range githubMarkdownLink.FindAllStringSubmatch(body, -1) {
		u, err := url.Parse(match[1])
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
			u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			continue
		}
		parts := githubFilePath.FindStringSubmatch(u.Path)
		if len(parts) == 0 || seen[u.String()] {
			continue
		}
		seen[u.String()] = true
		// GitHub issue markdown does not include attachment sizes. -1 means
		// unknown; file() and the common preparation path enforce actual limits.
		files = append(files, attachment{Name: parts[2], URL: u.String(), Size: -1})
	}
	return files
}

func (g *githubSource) file(ctx context.Context, f attachment) ([]byte, error) {
	u, err := url.Parse(f.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
		u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("unsupported GitHub attachment location")
	}
	parts := githubFilePath.FindStringSubmatch(u.Path)
	if len(parts) == 0 || parts[2] != f.Name || !fileName.MatchString(reproductionName(f.Name)) {
		return nil, errors.New("unsupported GitHub attachment filename")
	}
	res, err := g.download(ctx, u.String())
	if err != nil {
		return nil, err
	}
	if res.StatusCode == http.StatusFound {
		target, parseErr := url.Parse(res.Header.Get("Location"))
		res.Body.Close()
		if parseErr != nil || target.Scheme != "https" || target.Host != "objects.githubusercontent.com" ||
			target.User != nil || target.Fragment != "" || target.RawPath != "" {
			return nil, errors.New("unsupported GitHub attachment redirect")
		}
		redirectParts := githubFileRedirectPath.FindStringSubmatch(target.Path)
		if len(redirectParts) == 0 || redirectParts[1] != parts[1] {
			return nil, errors.New("unsupported GitHub attachment redirect")
		}
		res, err = g.download(ctx, target.String())
		if err != nil {
			return nil, err
		}
	}
	defer res.Body.Close()
	data, err := readGitHubResponse(res, casefile.MaxFileBytes)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || !utf8.Valid(data) || strings.ContainsRune(string(data), '\x00') {
		return nil, errors.New("GitHub attachments must be nonempty UTF-8 text files")
	}
	return data, nil
}

func (g *githubSource) download(ctx context.Context, target string) (*http.Response, error) {
	// Public attachments only. Neither the initial request nor the one allowed
	// signed redirect receives the API token or browser cookies.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errors.New("invalid GitHub attachment request")
	}
	res, err := g.client.Do(req)
	if err != nil {
		return nil, errors.New("GitHub attachment download could not be completed")
	}
	return res, nil
}

func (g *githubSource) postComment(context.Context, ticket, string, bool) (int64, error) {
	return 0, ErrBlocked
}
