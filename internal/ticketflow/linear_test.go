package ticketflow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type linearTransport func(*http.Request) (*http.Response, error)

func (f linearTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func linearResponse(data string) *http.Response {
	body, err := json.Marshal(map[string]any{"data": json.RawMessage(data)})
	if err != nil {
		panic(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

func linearErrorBody() *http.Response {
	body, err := json.Marshal(map[string]any{"errors": []map[string]string{{"message": "nope", "type": "BAD_USER_INPUT"}}})
	if err != nil {
		panic(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}

const (
	linearIssueUUID  = "00000000-0000-4000-8000-000000000042"
	linearAuthorUUID = "00000000-0000-4000-8000-000000000010"
	linearOtherUUID  = "00000000-0000-4000-8000-000000000011"
	linearFileUUID   = "00000000-0000-4000-8000-0000000000aa"
	linearFileUUID2  = "00000000-0000-4000-8000-0000000000bb"
	linearCommentA   = "00000000-0000-4000-8000-0000000000c1"
	linearCommentB   = "00000000-0000-4000-8000-0000000000c2"
)

const linearAttachmentURL = "https://uploads.linear.app/" + linearFileUUID + "/" + linearFileUUID2 + "?signature=fixture"

func linearIssueJSON() string {
	return `{"team":{"id":"00000000-0000-4000-8000-0000000000ee","key":"ENG","issues":{"nodes":[` +
		`{"id":"` + linearIssueUUID + `","identifier":"ENG-42","number":42,"title":"Timeout",` +
		`"description":"Python version: 3.12.4\nExpected: five seconds\nActual: five milliseconds\n[app.py.txt](` + linearAttachmentURL + `)",` +
		`"updatedAt":"2026-11-09T12:00:00Z","createdAt":"2026-11-09T11:00:00Z",` +
		`"state":{"name":"In Progress","type":"started"},` +
		`"creator":{"id":"` + linearAuthorUUID + `","name":"Reporter"},` +
		`"labels":{"nodes":[{"name":"reproduce"}]}}]}}}`
}

const linearCommentBElement = `{"id":"` + linearCommentB + `","body":"Actual: corrected description","createdAt":"2026-11-09T12:02:00Z","updatedAt":"2026-11-09T12:02:00Z","user":{"id":"` + linearAuthorUUID + `","name":"Reporter"}}`

func linearCommentsJSON() string {
	other := `{"id":"` + linearCommentA + `","body":"Expected: an unrelated person's request","createdAt":"2026-11-09T12:01:00Z","updatedAt":"2026-11-09T12:01:00Z","user":{"id":"` + linearOtherUUID + `","name":"Someone Else"}}`
	return `{"issue":{"id":"` + linearIssueUUID + `","identifier":"ENG-42","number":42,"comments":{"nodes":[` + other + `,` + linearCommentBElement + `],"pageInfo":{"hasNextPage":false}}}}`
}

type linearFixture struct {
	source   *linearSource
	issue    string
	comments string
	mutation string
	// mutationInput records the commentCreate input the connector sent.
	mutationInput struct{ issueID, body string }
	calls         int
	before        func(*http.Request)
}

func newLinearFixture(t *testing.T) *linearFixture {
	t.Helper()
	f := &linearFixture{
		source:   newLinear("acme", "ENG", "test-linear-secret"),
		issue:    linearIssueJSON(),
		comments: linearCommentsJSON(),
	}
	f.source.client.Transport = linearTransport(func(r *http.Request) (*http.Response, error) {
		f.calls++
		var payload struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		body, _ := io.ReadAll(r.Body)
		if json.Unmarshal(body, &payload) != nil {
			t.Fatal("invalid GraphQL request body")
		}
		if r.Method != http.MethodPost || r.URL.Scheme != "https" || r.URL.Host != "api.linear.app" || r.URL.Path != "/graphql" ||
			r.Header.Get("Authorization") != "test-linear-secret" || r.Header.Get("Accept") != "application/json" ||
			r.Header.Get("Content-Type") != "application/json" || r.Header.Get("User-Agent") != "TriageLab" {
			t.Fatalf("unexpected Linear API request: %s %s", r.Method, r.URL.Redacted())
		}
		if f.before != nil {
			f.before(r)
		}
		switch {
		case strings.Contains(payload.Query, "query TriageLabIssue"):
			if payload.Variables["team"] != "ENG" || payload.Variables["number"] != float64(42) ||
				r.Header.Get("public-file-urls-expire-in") != linearFileURLLifetime {
				t.Fatalf("unexpected issue query: %v %q", payload.Variables, r.Header.Get("public-file-urls-expire-in"))
			}
			return linearResponse(f.issue), nil
		case strings.Contains(payload.Query, "query TriageLabComments"):
			if payload.Variables["issue"] != linearIssueUUID {
				t.Fatalf("unexpected comments query: %v", payload.Variables)
			}
			return linearResponse(f.comments), nil
		case strings.Contains(payload.Query, "mutation TriageLabComment"):
			if f.mutation == "" {
				t.Fatal("unexpected comment mutation")
			}
			var input struct {
				Variables struct {
					Input struct {
						IssueID string `json:"issueId"`
						Body    string `json:"body"`
					} `json:"input"`
				} `json:"variables"`
			}
			if json.Unmarshal(body, &input) == nil {
				f.mutationInput.issueID, f.mutationInput.body = input.Variables.Input.IssueID, input.Variables.Input.Body
			}
			return linearResponse(f.mutation), nil
		}
		t.Fatalf("unexpected GraphQL document: %s", payload.Query)
		return nil, nil
	})
	return f
}

func TestLinearReadsIssueDescriptionCommentsAndProvenance(t *testing.T) {
	f := newLinearFixture(t)
	s, err := f.source.read(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	issueURL := "https://linear.app/acme/ENG/issue/ENG-42"
	if f.calls != 4 || s.Ticket.ID != 42 || s.Ticket.Kind != "linear" || s.Ticket.URL != issueURL ||
		s.Ticket.Status != "started" || s.Ticket.Requester != linearID(linearAuthorUUID) ||
		s.Ticket.RequesterProfile.Name != "Reporter" || len(s.Ticket.Tags) != 1 || s.Ticket.Tags[0] != "reproduce" {
		t.Fatalf("incorrect issue snapshot: %+v", s.Ticket)
	}
	if len(s.Comments) != 3 || s.Comments[0].ID != linearID(linearIssueUUID) || s.Comments[0].URL != issueURL ||
		s.Comments[0].Body != "Python version: 3.12.4\nExpected: five seconds\nActual: five milliseconds\n[app.py.txt]("+linearAttachmentURL+")" ||
		s.Comments[1].Author != linearID(linearOtherUUID) ||
		s.Comments[2].URL != issueURL+"#comment-"+linearCommentB || s.Comments[2].Author != s.Ticket.Requester {
		t.Fatalf("incorrect comment snapshot: %+v", s.Comments)
	}
	files := s.Comments[0].Files
	if len(files) != 1 || files[0].Name != "app.py.txt" || files[0].Size != -1 || files[0].URL != linearAttachmentURL {
		t.Fatalf("missing attachment: %+v", files)
	}
	if s.Ticket.Updated == "" {
		t.Fatal("missing freshness receipt")
	}
}

func TestLinearIdentifiersMapStably(t *testing.T) {
	if linearID(linearIssueUUID) <= 0 || linearID(linearIssueUUID) != linearID(linearIssueUUID) ||
		linearID(linearIssueUUID) == linearID(linearCommentA) || linearID("not-a-uuid") != 0 || linearID("") != 0 {
		t.Fatal("Linear UUID mapping is not stable and distinct")
	}
}

func TestLinearRejectsInvalidIssueResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(f *linearFixture)
	}{
		{"unknown team", func(f *linearFixture) { f.issue = `{"team":null}` }},
		{"zero or two matches", func(f *linearFixture) {
			f.issue = `{"team":{"id":"00000000-0000-4000-8000-0000000000ee","key":"ENG","issues":{"nodes":[]}}}`
		}},
		{"mismatched identifier", func(f *linearFixture) {
			f.issue = strings.Replace(f.issue, `"identifier":"ENG-42"`, `"identifier":"OTHER-42"`, 1)
		}},
		{"mismatched number", func(f *linearFixture) {
			f.issue = strings.Replace(f.issue, `"number":42`, `"number":43`, 1)
		}},
		{"wrong team key", func(f *linearFixture) {
			f.issue = strings.Replace(f.issue, `"key":"ENG"`, `"key":"OTHER"`, 1)
		}},
		{"invalid issue UUID", func(f *linearFixture) {
			f.issue = strings.Replace(f.issue, linearIssueUUID, "42", 1)
		}},
		{"missing state type", func(f *linearFixture) {
			f.issue = strings.Replace(f.issue, `"type":"started"`, `"type":""`, 1)
		}},
		{"invalid timestamp", func(f *linearFixture) {
			f.issue = strings.Replace(f.issue, `"updatedAt":"2026-11-09T12:00:00Z"`, `"updatedAt":"yesterday"`, 1)
		}},
		{"invalid creator UUID", func(f *linearFixture) {
			f.issue = strings.Replace(f.issue, linearAuthorUUID, "reporter", 1)
		}},
		{"comments for another issue", func(f *linearFixture) {
			f.comments = strings.Replace(f.comments, `"number":42`, `"number":43`, 1)
		}},
		{"duplicate comment", func(f *linearFixture) {
			f.comments = strings.Replace(f.comments, linearCommentB, linearCommentA, 1)
		}},
		{"invalid comment UUID", func(f *linearFixture) {
			f.comments = strings.Replace(f.comments, linearCommentA, "c1", 1)
		}},
		{"missing comment author", func(f *linearFixture) {
			f.comments = strings.Replace(f.comments, `,"user":{"id":"`+linearOtherUUID+`","name":"Someone Else"}`, ``, 1)
		}},
		{"comment page overflow", func(f *linearFixture) {
			f.comments = strings.Replace(f.comments, `"hasNextPage":false`, `"hasNextPage":true`, 1)
		}},
		{"GraphQL error", func(f *linearFixture) { f.issue = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinearFixture(t)
			if tc.name == "GraphQL error" {
				f.source.client.Transport = linearTransport(func(*http.Request) (*http.Response, error) {
					return linearErrorBody(), nil
				})
			} else {
				tc.change(f)
			}
			if _, err := f.source.read(t.Context(), 42); err == nil || errors.Is(err, ErrChanged) {
				t.Fatalf("invalid Linear response admitted: %v", err)
			}
		})
	}
}

func TestLinearConfirmDetectsIssueAndCommentChanges(t *testing.T) {
	f := newLinearFixture(t)
	s, err := f.source.snapshot(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.source.confirm(t.Context(), s.Ticket); err != nil {
		t.Fatalf("unchanged issue did not confirm: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*linearFixture)
	}{
		{"state change", func(f *linearFixture) {
			f.issue = strings.Replace(f.issue, `"type":"started"`, `"type":"completed"`, 1)
		}},
		{"description edit", func(f *linearFixture) {
			f.issue = strings.Replace(f.issue, "five milliseconds", "five centiseconds", 1)
		}},
		{"comment edit without new activity", func(f *linearFixture) {
			f.comments = strings.Replace(f.comments, "corrected description", "edited description", 1)
		}},
		{"comment deletion", func(f *linearFixture) {
			f.comments = strings.Replace(f.comments, ","+linearCommentBElement, "", 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.change(f)
			if err := f.source.confirm(t.Context(), s.Ticket); !errors.Is(err, ErrChanged) {
				t.Fatalf("changed issue confirmed: %v", err)
			}
		})
	}
}

func TestLinearAttachmentParsing(t *testing.T) {
	const file = "https://uploads.linear.app/" + linearFileUUID + "/" + linearFileUUID2
	for _, tc := range []struct {
		name string
		body string
		want []attachment
	}{
		{"link text name", "[app.py](" + file + ")", []attachment{{Name: "app.py", URL: file, Size: -1}}},
		{"path name", "[anything](" + file + "/notes.txt)", []attachment{{Name: "notes.txt", URL: file + "/notes.txt", Size: -1}}},
		{"image form", "![screenshot.log](" + file + ")", []attachment{{Name: "screenshot.log", URL: file, Size: -1}}},
		{"customer content host", "[app.py](https://cdn.linearusercontent.com/x/" + linearFileUUID + "/" + linearFileUUID2 + ")", nil},
		{"other host", "[app.py](https://github.com/user-attachments/files/1/app.py)", nil},
		{"http rejected", "[app.py](http://uploads.linear.app/" + linearFileUUID + "/" + linearFileUUID2 + ")", nil},
		{"bad path", "[app.py](https://uploads.linear.app/not-uuid)", nil},
		{"unsupported name", "[app.sh](https://uploads.linear.app/x/" + linearFileUUID + "/" + linearFileUUID2 + ")", nil},
		{"duplicate URL skipped", "[app.py](" + file + ") [app.py](" + file + ")", []attachment{{Name: "app.py", URL: file, Size: -1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := linearAttachments(tc.body)
			if len(files) != len(tc.want) {
				t.Fatalf("attachments %+v, want %+v", files, tc.want)
			}
			for i, want := range tc.want {
				if files[i] != want {
					t.Fatalf("attachment %+v, want %+v", files[i], want)
				}
			}
		})
	}
}

func TestLinearFileFetchAllowsOnlySignedUploads(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		ok   bool
	}{
		{"signed upload", "https://uploads.linear.app/" + linearFileUUID + "/" + linearFileUUID2 + "?signature=x", true},
		{"customer host", "https://x.linearusercontent.com/" + linearFileUUID + "/" + linearFileUUID2, true},
		{"other host", "https://example.com/" + linearFileUUID + "/" + linearFileUUID2, false},
		{"fragment", "https://uploads.linear.app/" + linearFileUUID + "/" + linearFileUUID2 + "#x", false},
		{"bad path", "https://uploads.linear.app/../../etc/passwd", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &linearFixture{source: newLinear("acme", "ENG", "test-linear-secret")}
			f.source.client.Transport = linearTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "" {
					t.Fatal("attachment download sent the API key to the storage host")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader("print('timeout')\n"))}, nil
			})
			_, err := f.source.file(t.Context(), attachment{Name: "app.py", URL: tc.url, Size: -1})
			if tc.ok != (err == nil) {
				t.Fatalf("err=%v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestLinearFileFetchEnforcesTextAndSizeBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		status int
		ok     bool
	}{
		{"empty", "", 200, false},
		{"NUL", "x\x00", 200, false},
		{"invalid UTF-8", "\xff", 200, false},
		{"denied", "print('timeout')", 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &linearFixture{source: newLinear("acme", "ENG", "test-linear-secret")}
			f.source.client.Transport = linearTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: make(http.Header),
					Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			_, err := f.source.file(t.Context(), attachment{Name: "app.py", URL: linearAttachmentURL, Size: -1})
			if tc.ok != (err == nil) {
				t.Fatalf("err=%v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestLinearPostCommentValidatesRequestAndReceipt(t *testing.T) {
	const body = "Please add the missing Python version."
	f := newLinearFixture(t)
	f.source.writable = true
	f.mutation = `{"commentCreate":{"success":true,"comment":{"id":"` + linearCommentA + `","body":"` + body + `",` +
		`"user":{"id":"` + linearOtherUUID + `","name":"Bot"},"issue":{"id":"` + linearIssueUUID + `","number":42,"identifier":"ENG-42"}}}}`
	id, err := f.source.postComment(t.Context(), mustLinearTicket(t, f), body, true)
	if err != nil || id != linearID(linearCommentA) ||
		f.mutationInput.issueID != linearIssueUUID || f.mutationInput.body != body {
		t.Fatalf("comment post: id=%d err=%v input=%+v", id, err, f.mutationInput)
	}
}

func mustLinearTicket(t *testing.T, f *linearFixture) ticket {
	t.Helper()
	s, err := f.source.snapshot(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	return s.Ticket
}

func TestLinearPostCommentRejectsInvalidReceipts(t *testing.T) {
	const body = "Please add the missing Python version."
	receipt := func(replacer func(string) string) string {
		mutation := `{"commentCreate":{"success":true,"comment":{"id":"` + linearCommentA + `","body":"` + body + `",` +
			`"user":{"id":"` + linearOtherUUID + `","name":"Bot"},"issue":{"id":"` + linearIssueUUID + `","number":42,"identifier":"ENG-42"}}}}`
		return replacer(mutation)
	}
	for _, tc := range []struct {
		name     string
		mutation string
	}{
		{"failed mutation", receipt(func(s string) string { return strings.Replace(s, `"success":true`, `"success":false`, 1) })},
		{"missing comment", receipt(func(s string) string { return `{"commentCreate":{"success":true}}` })},
		{"edited body", receipt(func(s string) string { return strings.Replace(s, body, "edited", 1) })},
		{"wrong issue", receipt(func(s string) string { return strings.Replace(s, `"number":42`, `"number":43`, 1) })},
		{"wrong identifier", receipt(func(s string) string { return strings.Replace(s, "ENG-42", "OTHER-42", 1) })},
		{"missing author", receipt(func(s string) string {
			return strings.Replace(s, `,"user":{"id":"`+linearOtherUUID+`","name":"Bot"}`, "", 1)
		})},
		{"invalid comment UUID", receipt(func(s string) string { return strings.Replace(s, linearCommentA, "c1", 1) })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinearFixture(t)
			f.source.writable = true
			f.mutation = tc.mutation
			if _, err := f.source.postComment(t.Context(), mustLinearTicket(t, f), body, true); err == nil {
				t.Fatal("invalid Linear comment receipt accepted")
			}
		})
	}
}

func TestLinearPostCommentIsGated(t *testing.T) {
	const body = "Please add the missing Python version."
	for _, tc := range []struct {
		name   string
		change func(*linearFixture, *ticket)
	}{
		{"read-only source", func(f *linearFixture, _ *ticket) { f.source.writable = false }},
		{"no token", func(f *linearFixture, _ *ticket) { f.source.token = "" }},
		{"completed issue", func(f *linearFixture, t *ticket) { t.Status = "completed" }},
		{"foreign URL", func(_ *linearFixture, t *ticket) { t.URL = "https://linear.app/acme/ENG/issue/OTHER-42" }},
		{"foreign kind", func(_ *linearFixture, t *ticket) { t.Kind = "github" }},
		{"changed issue", func(f *linearFixture, _ *ticket) {
			f.comments = strings.Replace(f.comments, "corrected description", "racing edit", 1)
		}},
		{"oversized body", func(_ *linearFixture, _ *ticket) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLinearFixture(t)
			f.source.writable = true
			message := body
			if tc.name == "oversized body" {
				message = strings.Repeat("x", maxLinearCommentBytes+1)
			}
			ticket := mustLinearTicket(t, f)
			tc.change(f, &ticket)
			if _, err := f.source.postComment(t.Context(), ticket, message, true); err == nil {
				t.Fatal("Linear comment write was not gated")
			}
		})
	}
}
