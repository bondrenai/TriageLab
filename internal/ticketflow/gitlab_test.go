package ticketflow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

const (
	gitlabTestOrigin    = "https://gitlab.example.com"
	gitlabTestProject   = "acme/tooling"
	gitlabTestToken     = "test-gitlab-token"
	gitlabTestIID       = 42
	gitlabTestReporter  = 7
	gitlabTestAgent     = 99
	gitlabUploadSecret  = "648d97c6eef5fc5df8d1004565b3ee5a"
	gitlabIssueAPIPath  = "/api/v4/projects/acme%2Ftooling/issues/42"
	gitlabNotesAPIPath  = "/api/v4/projects/acme%2Ftooling/issues/42/notes"
	gitlabUploadAPIPath = "/api/v4/projects/acme%2Ftooling/uploads/" + gitlabUploadSecret + "/app.py"
)

func gitlabUploadWebURL() string {
	return gitlabTestOrigin + "/" + gitlabTestProject + "/uploads/" + gitlabUploadSecret + "/app.py"
}

func gitlabIssueWebURL() string {
	return gitlabTestOrigin + "/" + gitlabTestProject + "/-/issues/" + "42"
}

func gitlabIssueJSON() string {
	return `{"iid":42,"type":"ISSUE","title":"Timeout is too short","state":"opened",` +
		`"description":"Python version: 3.12.4\nExpected: Wait five seconds.\nActual: Stops after five milliseconds.\n[app.py](/uploads/` + gitlabUploadSecret + `/app.py)",` +
		`"author":{"id":7,"username":"reporter","name":"Reporter"},` +
		`"updated_at":"2026-11-11T12:00:00.000Z","labels":["reproduce","python"]}`
}

func gitlabNotesJSON() string {
	return `[{"id":10010,"body":"The smallest reproduction. [app.log](` + gitlabIssueWebURL() + `/uploads/` + gitlabUploadSecret + `/app.log)",` +
		`"author":{"id":7,"username":"reporter","name":"Reporter"},"system":false,` +
		`"created_at":"2026-11-11T12:05:00Z","updated_at":"2026-11-11T12:05:00Z","noteable_id":1,"noteable_iid":42,"noteable_type":"Issue"},` +
		`{"id":10011,"body":"changed the description","author":{"id":1,"username":"root","name":"Root"},"system":true,` +
		`"created_at":"2026-11-11T12:06:00Z","updated_at":"2026-11-11T12:06:00Z","noteable_id":1,"noteable_iid":42,"noteable_type":"Issue"},` +
		`{"id":10012,"body":"Internal aside.","author":{"id":99,"username":"agent","name":"Agent"},"system":false,"internal":true,` +
		`"created_at":"2026-11-11T12:07:00Z","updated_at":"2026-11-11T12:07:00Z","noteable_id":1,"noteable_iid":42,"noteable_type":"Issue"}]`
}

type gitlabTransport func(*http.Request) (*http.Response, error)

func (f gitlabTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func gitlabResponse(status int, body string) *http.Response {
	res := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
	res.Header.Set("Content-Type", "application/json")
	return res
}

type gitlabFixture struct {
	source    *gitlabSource
	issue     string
	notes     string
	nextPage  string
	postCode  int
	postID    int64
	postIID   int64
	download  string
	postedURL string
}

func newGitLabFixture(t *testing.T) *gitlabFixture {
	t.Helper()
	f := &gitlabFixture{source: newGitLab(gitlabTestOrigin, gitlabTestProject, gitlabTestToken),
		issue: gitlabIssueJSON(), notes: gitlabNotesJSON(), postCode: http.StatusCreated,
		postID: 10050, postIID: gitlabTestIID}
	f.source.client.Transport = gitlabTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("PRIVATE-TOKEN") != gitlabTestToken || r.Header.Get("User-Agent") != "TriageLab" {
			t.Fatalf("unexpected GitLab authorization: %s %s", r.Method, r.URL.Redacted())
		}
		if r.URL.Scheme+"://"+r.URL.Host != gitlabTestOrigin {
			t.Fatalf("GitLab request left the configured instance: %s", r.URL.Redacted())
		}
		switch {
		case r.URL.EscapedPath() == gitlabIssueAPIPath && r.Method == http.MethodGet:
			return gitlabResponse(200, f.issue), nil
		case r.URL.EscapedPath() == gitlabNotesAPIPath:
			if r.Method == http.MethodGet {
				if r.URL.RawQuery != "order_by=created_at&sort=asc&per_page=100" {
					t.Fatalf("unexpected note page query: %s", r.URL.RawQuery)
				}
				res := gitlabResponse(200, f.notes)
				res.Header.Set("X-Page", "1")
				res.Header.Set("X-Next-Page", f.nextPage)
				return res, nil
			}
			if f.postCode != http.StatusCreated {
				return gitlabResponse(f.postCode, ""), nil
			}
			body, _ := io.ReadAll(r.Body)
			var sent struct {
				Body string `json:"body"`
			}
			if json.Unmarshal(body, &sent) != nil {
				t.Fatal("GitLab note write was not JSON")
			}
			f.postedURL = r.URL.Redacted()
			// Mirror GitLab's documented normalization before echoing.
			receipt := `{"id":` + strconv.FormatInt(f.postID, 10) + `,"body":` + jsonString(gitlabNoteBody(sent.Body)) +
				`,"author":{"id":99,"username":"agent","name":"Agent"},"system":false,` +
				`"created_at":"2026-11-11T12:10:00Z","updated_at":"2026-11-11T12:10:00Z",` +
				`"noteable_id":1,"noteable_iid":` + strconv.FormatInt(f.postIID, 10) + `,"noteable_type":"Issue"}`
			return gitlabResponse(201, receipt), nil
		case r.URL.EscapedPath() == gitlabUploadAPIPath && r.Method == http.MethodGet:
			if f.download == "" {
				f.download = "print('timeout')\n"
			}
			return gitlabResponse(200, f.download), nil
		}
		t.Fatalf("unexpected GitLab request: %s %s", r.Method, r.URL.Redacted())
		return nil, nil
	})
	return f
}

func jsonString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func TestGitLabReadsConversationAndProvenance(t *testing.T) {
	f := newGitLabFixture(t)
	s, err := f.source.read(t.Context(), gitlabTestIID)
	if err != nil {
		t.Fatal(err)
	}
	issueURL := gitlabIssueWebURL()
	if s.Ticket.ID != gitlabTestIID || s.Ticket.Kind != "gitlab" || s.Ticket.URL != issueURL ||
		s.Ticket.Status != "opened" || s.Ticket.Requester != gitlabTestReporter ||
		s.Ticket.RequesterProfile.Name != "Reporter" || s.Ticket.RequesterProfile.URL != gitlabTestOrigin+"/reporter" ||
		len(s.Ticket.Tags) != 2 || s.Ticket.Tags[0] != "reproduce" || s.Ticket.Tags[1] != "python" {
		t.Fatalf("incorrect issue snapshot: %+v", s.Ticket)
	}
	if len(s.Comments) != 3 {
		t.Fatalf("wrong comment count: %+v", s.Comments)
	}
	// The description is the first pseudo-comment and carries the relative
	// upload resolved against the project base.
	description := s.Comments[0]
	if description.ID != gitlabDescriptionCommentID(gitlabTestProject, gitlabTestIID) || description.ID == gitlabTestIID ||
		!description.Public || description.Author != gitlabTestReporter || description.URL != issueURL ||
		!strings.Contains(description.Body, "Python version: 3.12.4") {
		t.Fatalf("incorrect description comment: %+v", description)
	}
	if len(description.Files) != 1 || description.Files[0].Name != "app.py" ||
		description.Files[0].URL != gitlabUploadWebURL() || description.Files[0].Size != -1 {
		t.Fatalf("missing description attachment: %+v", description.Files)
	}
	// System notes are activity records, never conversation.
	public := s.Comments[1]
	if public.ID != 10010 || !public.Public || public.URL != issueURL+"#note_10010" ||
		len(public.Files) != 1 || public.Files[0].Name != "app.log" ||
		public.Files[0].URL != issueURL+"/uploads/"+gitlabUploadSecret+"/app.log" {
		t.Fatalf("incorrect public note: %+v", public)
	}
	if s.Comments[2].ID != 10012 || s.Comments[2].Public {
		t.Fatalf("internal note leaked as requester evidence: %+v", s.Comments[2])
	}
}

func TestGitLabRejectsInvalidIssues(t *testing.T) {
	for name, body := range map[string]string{
		"wrong iid":   strings.Replace(gitlabIssueJSON(), `"iid":42`, `"iid":43`, 1),
		"bad state":   strings.Replace(gitlabIssueJSON(), `"state":"opened"`, `"state":"locked"`, 1),
		"incident":    strings.Replace(gitlabIssueJSON(), `"type":"ISSUE"`, `"type":"INCIDENT"`, 1),
		"bad author":  strings.Replace(gitlabIssueJSON(), `"username":"reporter"`, `"username":"../escape"`, 1),
		"bad time":    strings.Replace(gitlabIssueJSON(), `"updated_at":"2026-11-11T12:00:00.000Z"`, `"updated_at":"yesterday"`, 1),
		"broken json": `{`,
		"empty body":  "",
	} {
		t.Run(name, func(t *testing.T) {
			f := newGitLabFixture(t)
			f.issue = body
			if _, err := f.source.read(t.Context(), gitlabTestIID); err == nil {
				t.Fatal("accepted an invalid GitLab issue")
			}
		})
	}
}

func TestGitLabRejectsInvalidNotePages(t *testing.T) {
	for name, change := range map[string]func(*gitlabFixture){
		"second page": func(f *gitlabFixture) { f.nextPage = "2" },
		"repeated id": func(f *gitlabFixture) { f.notes = strings.Replace(gitlabNotesJSON(), `"id":10012`, `"id":10010`, 1) },
		"descending":  func(f *gitlabFixture) { f.notes = strings.Replace(gitlabNotesJSON(), `"id":10012`, `"id":10005`, 1) },
		"bad author":  func(f *gitlabFixture) { f.notes = strings.Replace(gitlabNotesJSON(), `"id":7`, `"id":0`, 1) },
		"missing author": func(f *gitlabFixture) {
			f.notes = strings.Replace(gitlabNotesJSON(), `"author":{"id":7,"username":"reporter","name":"Reporter"},"system":false,"created_at"`, `"author":{"id":7},"system":false,"created_at"`, 1)
		},
		"broken page": func(f *gitlabFixture) { f.notes = `` },
	} {
		t.Run(name, func(t *testing.T) {
			f := newGitLabFixture(t)
			change(f)
			if _, err := f.source.read(t.Context(), gitlabTestIID); err == nil {
				t.Fatal("accepted an invalid GitLab note page")
			}
		})
	}
}

func TestGitLabAttachmentDownload(t *testing.T) {
	f := newGitLabFixture(t)
	data, err := f.source.file(t.Context(), attachment{Name: "app.py", URL: gitlabUploadWebURL(),
		Size: -1, ID: secretID(gitlabUploadSecret)})
	if err != nil || string(data) != "print('timeout')\n" {
		t.Fatalf("attachment download: %q %v", data, err)
	}
	for name, target := range map[string]attachment{
		"other instance": {Name: "app.py", URL: "https://gitlab.com/acme/tooling/uploads/" + gitlabUploadSecret + "/app.py", ID: secretID(gitlabUploadSecret)},
		"tampered name":  {Name: "app.log", URL: gitlabUploadWebURL(), ID: secretID(gitlabUploadSecret)},
		"missing secret": {Name: "app.py", URL: gitlabTestOrigin + "/" + gitlabTestProject + "/uploads/not-the-secret/app.py", ID: secretID(gitlabUploadSecret)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newGitLabFixture(t)
			if _, err := f.source.file(t.Context(), target); err == nil {
				t.Fatal("accepted an unsupported GitLab attachment")
			}
		})
	}
	f.download = string([]byte{0xff, 0xfe, 0xfd})
	if _, err := f.source.file(t.Context(), attachment{Name: "app.py", URL: gitlabUploadWebURL(),
		ID: secretID(gitlabUploadSecret)}); err == nil {
		t.Fatal("accepted a nontext GitLab attachment")
	}
}

func TestGitLabNoteWriteValidatesReceipt(t *testing.T) {
	f := newGitLabFixture(t)
	f.source.writable = true
	current, err := f.source.ticket(t.Context(), gitlabTestIID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.source.postComment(t.Context(), current, "line one\nline two\n\n", true)
	if err != nil || id != 10050 {
		t.Fatalf("note write: %d, %v", id, err)
	}
	// A receipt for another issue's note must not confirm delivery.
	f.postIID = 43
	if _, err := f.source.postComment(t.Context(), current, "line one", true); err == nil {
		t.Fatal("accepted a mismatching GitLab note receipt")
	}
	f.postIID = gitlabTestIID
	// Private notes are not part of the GitLab connector's contract.
	if _, err := f.source.postComment(t.Context(), current, "internal", false); err == nil {
		t.Fatal("posted a non-public GitLab note")
	}
	// Writes require an explicitly enabled writer and an open issue.
	blocked := newGitLabFixture(t)
	if _, err := blocked.source.postComment(t.Context(), current, "x", true); err == nil {
		t.Fatal("wrote through a read-only GitLab connector")
	}
	f.source.writable = true
	closed := current
	closed.Status = "closed"
	if _, err := f.source.postComment(t.Context(), closed, "x", true); err == nil {
		t.Fatal("wrote to a closed issue")
	}
}

func TestGitLabResolveReference(t *testing.T) {
	g := newGitLab(gitlabTestOrigin, gitlabTestProject, gitlabTestToken)
	for _, reference := range []string{
		"42",
		" 42 ",
		gitlabIssueWebURL(),
		gitlabIssueWebURL() + "#note_10010",
		gitlabTestOrigin + "/" + gitlabTestProject + "/issues/42",
		gitlabIssueWebURL() + "/",
	} {
		if id, err := g.resolveReference(reference); err != nil || id != 42 {
			t.Fatalf("reference %q resolved to %d: %v", reference, id, err)
		}
	}
	for _, reference := range []string{
		"",
		"0",
		"042",
		"OPS-42",
		"https://gitlab.com/acme/tooling/-/issues/42",
		gitlabTestOrigin + "/acme/other/-/issues/42",
		gitlabTestOrigin + "/" + gitlabTestProject + "/-/issues/42/app.py",
	} {
		if _, err := g.resolveReference(reference); err == nil {
			t.Fatalf("accepted invalid GitLab reference %q", reference)
		}
	}
}

func TestVerifyGitLabConnection(t *testing.T) {
	g := newGitLab(gitlabTestOrigin, gitlabTestProject, gitlabTestToken)
	g.client.Transport = gitlabTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.EscapedPath() != "/api/v4/projects/acme%2Ftooling" || r.Header.Get("PRIVATE-TOKEN") != gitlabTestToken {
			return nil, errors.New("unexpected GitLab verification request")
		}
		return gitlabResponse(200, `{"id":5,"path_with_namespace":"acme/tooling"}`), nil
	})
	if err := verifyGitLab(t.Context(), g); err != nil {
		t.Fatal(err)
	}
	g.client.Transport = gitlabTransport(func(*http.Request) (*http.Response, error) {
		return gitlabResponse(401, "{}"), nil
	})
	if err := verifyGitLab(t.Context(), g); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("rejected token not reported: %v", err)
	}
}
