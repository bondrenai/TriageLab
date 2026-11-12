package ticketflow

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

type bondrenTransport func(*http.Request) (*http.Response, error)

func (f bondrenTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func bondrenResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func bondrenJSON(value any) *http.Response {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return bondrenResponse(200, string(body))
}

const (
	bondrenTestOrigin = "https://app.bondren.example"
	bondrenIssueUUIDF = "00000000-0000-4000-8000-000000000042"
	bondrenAuthorUUID = "00000000-0000-4000-8000-000000000010"
	bondrenMemberUUID = "00000000-0000-4000-8000-000000000011"
	bondrenFileUUID   = "00000000-0000-4000-8000-0000000000aa"
	bondrenPendUUID   = "00000000-0000-4000-8000-0000000000bb"
	bondrenMessageA   = "00000000-0000-4000-8000-0000000000c1"
	bondrenMessageB   = "00000000-0000-4000-8000-0000000000c2"
	bondrenMessageC   = "00000000-0000-4000-8000-0000000000c3"
)

func bondrenIssueJSON() string {
	return `{"id":"` + bondrenIssueUUIDF + `","number":42,"title":"Timeout","state":"on_you",` +
		`"operational_state":"native","requester_id":"` + bondrenAuthorUUID + `",` +
		`"requester_name":"Reporter","requester_email":"reporter@example.com",` +
		`"updated_at":"2026-11-09T12:00:00Z","tags":["triagelab_python_timeout"]}`
}

func bondrenMessagesJSON() string {
	return `{"items":[` +
		`{"id":"` + bondrenMessageA + `","issue_id":"` + bondrenIssueUUIDF + `",` +
		`"body":"Python version: 3.12.4\nExpected: five seconds\nActual: five milliseconds",` +
		`"created_at":"2026-11-09T12:00:00Z","visibility":"public","author_kind":"end_user",` +
		`"author_end_user_id":"` + bondrenAuthorUUID + `",` +
		`"attachments":[{"id":"` + bondrenFileUUID + `","filename":"app.py","byte_size":17,"inspection_state":"clean"}]},` +
		`{"id":"` + bondrenMessageB + `","issue_id":"` + bondrenIssueUUIDF + `","body":"Taking a look.",` +
		`"created_at":"2026-11-09T12:01:00Z","visibility":"internal","author_kind":"user",` +
		`"author_user_id":"` + bondrenMemberUUID + `","attachments":[]},` +
		`{"id":"` + bondrenMessageC + `","issue_id":"` + bondrenIssueUUIDF + `","body":"Still failing.",` +
		`"created_at":"2026-11-09T12:02:00Z","visibility":"public","author_kind":"end_user",` +
		`"author_end_user_id":"` + bondrenAuthorUUID + `",` +
		`"attachments":[{"id":"` + bondrenPendUUID + `","filename":"notes.txt","byte_size":5,"inspection_state":"pending"}]}` +
		`],"page_info":{"has_more":false,"next_cursor":null}}`
}

type bondrenFixture struct {
	source   *bondrenSource
	issue    string
	messages string
	post     string
	postCode int
	// postInput records the message body and visibility the connector sent.
	postInput struct{ body, visibility string }
	download  string
	calls     int
	// numberLookups counts saved-view query requests; byIDLookups counts
	// requests to the by-ID endpoint.
	numberLookups, byIDLookups int
}

func newBondrenFixture(t *testing.T) *bondrenFixture {
	t.Helper()
	f := &bondrenFixture{source: newBondren(bondrenTestOrigin, "test-bondren-token"),
		issue: bondrenIssueJSON(), messages: bondrenMessagesJSON(), postCode: 201}
	f.source.client.Transport = bondrenTransport(func(r *http.Request) (*http.Response, error) {
		f.calls++
		if r.Header.Get("Authorization") != "Bearer test-bondren-token" || r.Header.Get("User-Agent") != "TriageLab" {
			t.Fatalf("unexpected Bondren authorization: %s %s", r.Method, r.URL.Redacted())
		}
		if r.URL.Scheme+"://"+r.URL.Host != bondrenTestOrigin {
			t.Fatalf("Bondren request left the configured origin: %s", r.URL.Redacted())
		}
		switch r.URL.Path {
		case "/api/v1/issues/query":
			f.numberLookups++
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("unexpected issue query: %s %s", r.Method, r.URL.Redacted())
			}
			body, _ := io.ReadAll(r.Body)
			var query struct {
				Limit       int `json:"limit"`
				FilterGroup struct {
					Op      string `json:"op"`
					Filters []struct {
						Field string `json:"field"`
						Op    string `json:"op"`
						Value string `json:"value"`
					} `json:"filters"`
				} `json:"filter_group"`
			}
			if json.Unmarshal(body, &query) != nil || query.Limit != 2 || query.FilterGroup.Op != "and" ||
				len(query.FilterGroup.Filters) != 1 || query.FilterGroup.Filters[0].Field != "number" ||
				query.FilterGroup.Filters[0].Op != "eq" || query.FilterGroup.Filters[0].Value != "42" {
				t.Fatalf("unexpected issue query filter: %s", body)
			}
			return bondrenJSON(map[string]any{"items": []any{json.RawMessage(f.issue)}, "page_info": map[string]any{"has_more": false}}), nil
		case "/api/v1/issues/" + bondrenIssueUUIDF:
			f.byIDLookups++
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected issue request: %s %s", r.Method, r.URL.Redacted())
			}
			return bondrenResponse(200, f.issue), nil
		case "/api/v1/issues/" + bondrenIssueUUIDF + "/messages":
			if r.Method == http.MethodGet {
				if r.URL.RawQuery != "limit=100" {
					t.Fatalf("unexpected message page query: %s", r.URL.Redacted())
				}
				return bondrenResponse(200, f.messages), nil
			}
			if f.post == "" {
				t.Fatal("unexpected Bondren message write")
			}
			body, _ := io.ReadAll(r.Body)
			var input struct {
				Body       string `json:"body"`
				Visibility string `json:"visibility"`
			}
			if json.Unmarshal(body, &input) == nil {
				f.postInput.body, f.postInput.visibility = input.Body, input.Visibility
			}
			return bondrenResponse(f.postCode, f.post), nil
		case "/api/v1/issues/" + bondrenIssueUUIDF + "/attachments/" + bondrenFileUUID + "/download":
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected attachment request: %s %s", r.Method, r.URL.Redacted())
			}
			if f.download == "" {
				f.download = "print('timeout')\n"
			}
			return bondrenResponse(200, f.download), nil
		}
		t.Fatalf("unexpected Bondren request: %s %s", r.Method, r.URL.Redacted())
		return nil, nil
	})
	return f
}

func TestBondrenReadsConversationAndProvenance(t *testing.T) {
	f := newBondrenFixture(t)
	s, err := f.source.read(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	issueURL := bondrenTestOrigin + "/work/support/" + bondrenIssueUUIDF
	if s.Ticket.ID != 42 || s.Ticket.Kind != "bondren" || s.Ticket.URL != issueURL ||
		s.Ticket.Status != "on_you" || s.Ticket.Requester != bondrenID(bondrenAuthorUUID) ||
		s.Ticket.RequesterProfile.Name != "Reporter" || s.Ticket.RequesterProfile.Email != "reporter@example.com" ||
		len(s.Ticket.Tags) != 1 || s.Ticket.Tags[0] != "triagelab_python_timeout" {
		t.Fatalf("incorrect issue snapshot: %+v", s.Ticket)
	}
	if len(s.Comments) != 3 || s.Comments[0].ID != bondrenID(bondrenMessageA) ||
		s.Comments[0].Author != s.Ticket.Requester || !s.Comments[0].Public ||
		s.Comments[1].Public || s.Comments[1].Author != bondrenID(bondrenMemberUUID) ||
		s.Comments[2].URL != issueURL+"#message-"+bondrenMessageC {
		t.Fatalf("incorrect message snapshot: %+v", s.Comments)
	}
	files := s.Comments[0].Files
	if len(files) != 1 || files[0].Name != "app.py" || files[0].Size != 17 ||
		files[0].URL != bondrenTestOrigin+"/api/v1/issues/"+bondrenIssueUUIDF+"/attachments/"+bondrenFileUUID+"/download" {
		t.Fatalf("missing attachment: %+v", files)
	}
	if len(s.Comments[2].Files) != 0 {
		t.Fatalf("pending inspection attachment surfaced: %+v", s.Comments[2].Files)
	}
	if s.Ticket.Updated == "" {
		t.Fatal("missing freshness receipt")
	}
}

func TestBondrenIdentifiersMapStably(t *testing.T) {
	issueID := bondrenID(bondrenIssueUUIDF)
	if issueID <= 0 || issueID != bondrenID(bondrenIssueUUIDF) ||
		issueID == bondrenID(bondrenAuthorUUID) || bondrenID("not-a-uuid") != 0 || bondrenID("") != 0 {
		t.Fatal("Bondren UUID mapping is not stable and distinct")
	}
}

func TestBondrenResolvesIssueIDToItsNumberWithoutTheNumberFilter(t *testing.T) {
	f := newBondrenFixture(t)
	for _, reference := range []string{bondrenIssueUUIDF, strings.ToUpper(bondrenIssueUUIDF), " " + bondrenIssueUUIDF + " "} {
		id, err := f.source.resolveReference(t.Context(), reference)
		if err != nil || id != 42 {
			t.Fatalf("issue ID %q resolved to %d: %v", reference, id, err)
		}
	}
	s, err := f.source.read(t.Context(), 42)
	if err != nil || s.Ticket.ID != 42 || s.Ticket.Kind != "bondren" ||
		s.Ticket.URL != bondrenTestOrigin+"/work/support/"+bondrenIssueUUIDF {
		t.Fatalf("snapshot after ID lookup: %+v, %v", s.Ticket, err)
	}
	if f.numberLookups != 0 || f.byIDLookups == 0 {
		t.Fatalf("ID lookup used the number filter: number=%d byID=%d", f.numberLookups, f.byIDLookups)
	}
}

func TestBondrenResolveReferenceKeepsNumbersAndRejectsOtherInput(t *testing.T) {
	f := newBondrenFixture(t)
	id, err := f.source.resolveReference(t.Context(), " 42 ")
	if err != nil || id != 42 || f.calls != 0 {
		t.Fatalf("number reference: %d, %v, calls=%d", id, err, f.calls)
	}
	for _, reference := range []string{"", "0", "-1", "042", "4 2", "forty-two", "42/43", bondrenIssueUUIDF + "x", "issues/" + bondrenIssueUUIDF} {
		if _, err := f.source.resolveReference(t.Context(), reference); err == nil || !errors.Is(err, ErrReference) {
			t.Fatalf("reference %q produced %v, want ErrReference", reference, err)
		}
	}
	if f.calls != 0 {
		t.Fatalf("rejected references reached Bondren: calls=%d", f.calls)
	}
}

func TestBondrenResolveReferenceRejectsUnusableIssues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(f *bondrenFixture)
	}{
		{"another issue returned", func(f *bondrenFixture) {
			f.issue = strings.Replace(f.issue, bondrenIssueUUIDF, "00000000-0000-4000-8000-000000000099", 1)
		}},
		{"missing number", func(f *bondrenFixture) {
			f.issue = strings.Replace(f.issue, `"number":42`, `"number":0`, 1)
		}},
		{"source-managed issue", func(f *bondrenFixture) {
			f.issue = strings.Replace(f.issue, `"operational_state":"native"`, `"operational_state":"source_owned"`, 1)
		}},
		{"unknown issue", func(f *bondrenFixture) {
			f.source.client.Transport = bondrenTransport(func(*http.Request) (*http.Response, error) {
				return bondrenResponse(404, `{"error":"missing"}`), nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBondrenFixture(t)
			tc.change(f)
			if _, err := f.source.resolveReference(t.Context(), bondrenIssueUUIDF); err == nil {
				t.Fatal("unusable issue was resolved")
			}
			if _, known := f.source.issueUUIDFor(42); known {
				t.Fatal("rejected issue was remembered")
			}
		})
	}
}

func TestBondrenKnownIssueIDMustStillMatchItsNumber(t *testing.T) {
	f := newBondrenFixture(t)
	if _, err := f.source.resolveReference(t.Context(), bondrenIssueUUIDF); err != nil {
		t.Fatal(err)
	}
	f.issue = strings.Replace(f.issue, `"number":42`, `"number":43`, 1)
	if _, err := f.source.read(t.Context(), 42); err == nil || errors.Is(err, ErrChanged) {
		t.Fatalf("renumbered issue admitted: %v", err)
	}
}

func TestBondrenSeedsIssueIDsFromStoredCaseURLs(t *testing.T) {
	b := newBondren(bondrenTestOrigin, "token")
	b.seed([]Case{
		{TicketID: 42, URL: bondrenTestOrigin + "/work/support/" + bondrenIssueUUIDF},
		{TicketID: 43, URL: "https://other.bondren.example/work/support/" + bondrenIssueUUIDF},
		{TicketID: 44, URL: bondrenTestOrigin + "/work/support/not-a-uuid"},
		{TicketID: 45},
	})
	if uuid, ok := b.issueUUIDFor(42); !ok || uuid != bondrenIssueUUIDF {
		t.Fatalf("stored case URL did not seed its issue ID: %q %v", uuid, ok)
	}
	for _, id := range []int64{43, 44, 45} {
		if _, ok := b.issueUUIDFor(id); ok {
			t.Fatalf("case %d was seeded from a foreign URL", id)
		}
	}
}

func TestBondrenRejectsInvalidIssueResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(f *bondrenFixture)
	}{
		{"zero matches", func(f *bondrenFixture) { f.issue = "null" }},
		{"mismatched number", func(f *bondrenFixture) {
			f.issue = strings.Replace(f.issue, `"number":42`, `"number":43`, 1)
		}},
		{"invalid issue UUID", func(f *bondrenFixture) { f.issue = strings.Replace(f.issue, bondrenIssueUUIDF, "42", 1) }},
		{"unknown state", func(f *bondrenFixture) { f.issue = strings.Replace(f.issue, `"state":"on_you"`, `"state":"wat"`, 1) }},
		{"invalid requester", func(f *bondrenFixture) { f.issue = strings.Replace(f.issue, bondrenAuthorUUID, "reporter", 1) }},
		{"invalid timestamp", func(f *bondrenFixture) {
			f.issue = strings.Replace(f.issue, `"updated_at":"2026-11-09T12:00:00Z"`, `"updated_at":"yesterday"`, 1)
		}},
		{"source-managed issue", func(f *bondrenFixture) {
			f.issue = strings.Replace(f.issue, `"operational_state":"native"`, `"operational_state":"source_owned"`, 1)
		}},
		{"replaying issue", func(f *bondrenFixture) {
			f.issue = strings.Replace(f.issue, `"operational_state":"native"`, `"operational_state":"replaying"`, 1)
		}},
		{"invalid message page", func(f *bondrenFixture) { f.messages = `{}` }},
		{"message page overflow", func(f *bondrenFixture) {
			f.messages = strings.Replace(f.messages, `"has_more":false`, `"has_more":true`, 1)
		}},
		{"message for another issue", func(f *bondrenFixture) {
			f.messages = strings.Replace(f.messages, `"issue_id":"`+bondrenIssueUUIDF+`","body":"Taking`, `"issue_id":"00000000-0000-4000-8000-000000000099","body":"Taking`, 1)
		}},
		{"duplicate message", func(f *bondrenFixture) {
			f.messages = strings.Replace(f.messages, bondrenMessageB, bondrenMessageA, 1)
		}},
		{"invalid message UUID", func(f *bondrenFixture) {
			f.messages = strings.Replace(f.messages, bondrenMessageA, "c1", 1)
		}},
		{"unknown author kind", func(f *bondrenFixture) {
			f.messages = strings.Replace(f.messages, `"author_kind":"user"`, `"author_kind":"external"`, 1)
		}},
		{"unknown visibility", func(f *bondrenFixture) {
			f.messages = strings.Replace(f.messages, `"visibility":"internal"`, `"visibility":"secret"`, 1)
		}},
		{"invalid message time", func(f *bondrenFixture) {
			f.messages = strings.Replace(f.messages, `"created_at":"2026-11-09T12:01:00Z"`, `"created_at":"soon"`, 1)
		}},
		{"unauthorized", func(f *bondrenFixture) {
			f.source.client.Transport = bondrenTransport(func(*http.Request) (*http.Response, error) {
				return bondrenResponse(401, `{"error":"nope"}`), nil
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBondrenFixture(t)
			tc.change(f)
			if _, err := f.source.read(t.Context(), 42); err == nil || errors.Is(err, ErrChanged) {
				t.Fatalf("invalid Bondren response admitted: %v", err)
			}
		})
	}
}

func TestBondrenConfirmDetectsIssueAndConversationChanges(t *testing.T) {
	f := newBondrenFixture(t)
	s, err := f.source.snapshot(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.source.confirm(t.Context(), s.Ticket); err != nil {
		t.Fatalf("unchanged issue did not confirm: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*bondrenFixture)
	}{
		{"state change", func(f *bondrenFixture) {
			f.issue = strings.Replace(f.issue, `"state":"on_you"`, `"state":"closed"`, 1)
		}},
		{"tag change", func(f *bondrenFixture) {
			f.issue = strings.Replace(f.issue, `"triagelab_python_timeout"`, `"other"`, 1)
		}},
		{"message edit", func(f *bondrenFixture) {
			f.messages = strings.Replace(f.messages, "Still failing.", "Still failing badly.", 1)
		}},
		{"message removed at source", func(f *bondrenFixture) {
			f.messages = strings.Replace(f.messages, `{"id":"`+bondrenMessageC+`"`, `{"source_removed":true,"id":"`+bondrenMessageC+`"`, 1)
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

func TestBondrenAttachmentSelectionSkipsUninspectedFiles(t *testing.T) {
	b := newBondren(bondrenTestOrigin, "token")
	files := b.attachments(bondrenIssueUUIDF, []bondrenAttachment{
		{ID: bondrenFileUUID, Filename: "app.py", ByteSize: 10, InspectionState: "clean"},
		{ID: bondrenPendUUID, Filename: "pending.log", ByteSize: 10, InspectionState: "pending"},
		{ID: "00000000-0000-4000-8000-0000000000cc", Filename: "rejected.log", ByteSize: 10, InspectionState: "rejected"},
		{ID: "00000000-0000-4000-8000-0000000000dd", Filename: "zero.py", ByteSize: 0, InspectionState: "clean"},
		{ID: "00000000-0000-4000-8000-0000000000ee", Filename: "huge.py", ByteSize: casefile.MaxFileBytes + 1, InspectionState: "clean"},
		{ID: "00000000-0000-4000-8000-0000000000ff", Filename: "script.sh", ByteSize: 10, InspectionState: "clean"},
		{ID: bondrenFileUUID, Filename: "app.py", ByteSize: 10, InspectionState: "clean"},
		{ID: "42", Filename: "odd.py", ByteSize: 10, InspectionState: "clean"},
	})
	if len(files) != 1 || files[0].Name != "app.py" ||
		files[0].URL != bondrenTestOrigin+"/api/v1/issues/"+bondrenIssueUUIDF+"/attachments/"+bondrenFileUUID+"/download" {
		t.Fatalf("unexpected attachment selection: %+v", files)
	}
}

func TestBondrenFileFetchIsAuthorizedAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		url        string
		size       int64
		body       string
		status     int
		authorized bool
	}{
		{name: "clean attachment", url: bondrenTestOrigin + "/api/v1/issues/" + bondrenIssueUUIDF + "/attachments/" + bondrenFileUUID + "/download", size: 17, body: "print('timeout')\n", status: 200, authorized: true},
		{name: "foreign origin", url: "https://evil.example/api/v1/issues/" + bondrenIssueUUIDF + "/attachments/" + bondrenFileUUID + "/download", size: 18, status: 200},
		{name: "query string", url: bondrenTestOrigin + "/api/v1/issues/" + bondrenIssueUUIDF + "/attachments/" + bondrenFileUUID + "/download?x=1", size: 18, status: 200},
		{name: "attachment listing", url: bondrenTestOrigin + "/api/v1/issues/" + bondrenIssueUUIDF + "/attachments", size: 18, status: 200},
		{name: "size changed", url: bondrenTestOrigin + "/api/v1/issues/" + bondrenIssueUUIDF + "/attachments/" + bondrenFileUUID + "/download", size: 3, body: "print('timeout')\n", status: 200, authorized: true},
		{name: "denied", url: bondrenTestOrigin + "/api/v1/issues/" + bondrenIssueUUIDF + "/attachments/" + bondrenFileUUID + "/download", size: 18, status: 403, authorized: true},
		{name: "NUL bytes", url: bondrenTestOrigin + "/api/v1/issues/" + bondrenIssueUUIDF + "/attachments/" + bondrenFileUUID + "/download", size: 2, body: "x\x00", status: 200, authorized: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBondren(bondrenTestOrigin, "test-bondren-token")
			var sentAuth bool
			b.client.Transport = bondrenTransport(func(r *http.Request) (*http.Response, error) {
				sentAuth = r.Header.Get("Authorization") == "Bearer test-bondren-token"
				return bondrenResponse(tc.status, tc.body), nil
			})
			_, err := b.file(t.Context(), attachment{Name: "app.py", URL: tc.url, Size: tc.size})
			if tc.authorized && !sentAuth {
				t.Fatal("Bondren attachment download was not authorized with the agent token")
			}
			if (err == nil) != (tc.name == "clean attachment") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestBondrenPostCommentValidatesRequestAndReceipt(t *testing.T) {
	const body = "Please add the missing Python version."
	for _, tc := range []struct {
		name       string
		public     bool
		visibility string
	}{
		{"public clarification", true, "public"},
		{"internal note", false, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBondrenFixture(t)
			f.source.writable = true
			f.post = `{"id":"` + bondrenMessageA + `","issue_id":"` + bondrenIssueUUIDF + `","body":"` + body + `","visibility":"` + tc.visibility + `"}`
			id, err := f.source.postComment(t.Context(), mustBondrenTicket(t, f), body, tc.public)
			if err != nil || id != bondrenID(bondrenMessageA) ||
				f.postInput.body != body || f.postInput.visibility != tc.visibility {
				t.Fatalf("message post: id=%d err=%v input=%+v", id, err, f.postInput)
			}
		})
	}
}

func mustBondrenTicket(t *testing.T, f *bondrenFixture) ticket {
	t.Helper()
	s, err := f.source.snapshot(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	return s.Ticket
}

func TestBondrenPostCommentRejectsInvalidReceipts(t *testing.T) {
	const body = "Please add the missing Python version."
	receipt := func(replacer func(string) string) string {
		return replacer(`{"id":"` + bondrenMessageA + `","issue_id":"` + bondrenIssueUUIDF + `","body":"` + body + `","visibility":"public"}`)
	}
	for _, tc := range []struct {
		name     string
		post     string
		postCode int
	}{
		{"edited body", receipt(func(s string) string { return strings.Replace(s, body, "edited", 1) }), 201},
		{"wrong issue", receipt(func(s string) string {
			return strings.Replace(s, bondrenIssueUUIDF, "00000000-0000-4000-8000-000000000099", 1)
		}), 201},
		{"wrong visibility", receipt(func(s string) string {
			return strings.Replace(s, `"visibility":"public"`, `"visibility":"internal"`, 1)
		}), 201},
		{"invalid message UUID", receipt(func(s string) string { return strings.Replace(s, bondrenMessageA, "c1", 1) }), 201},
		{"server rejection", `{"error":"finalized"}`, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBondrenFixture(t)
			f.source.writable = true
			f.post, f.postCode = tc.post, tc.postCode
			if _, err := f.source.postComment(t.Context(), mustBondrenTicket(t, f), body, true); err == nil {
				t.Fatal("invalid Bondren message receipt accepted")
			}
		})
	}
}

func TestBondrenPostCommentIsGated(t *testing.T) {
	const body = "Please add the missing Python version."
	for _, tc := range []struct {
		name   string
		change func(*bondrenFixture, *ticket)
	}{
		{"read-only source", func(f *bondrenFixture, _ *ticket) { f.source.writable = false }},
		{"no token", func(f *bondrenFixture, _ *ticket) { f.source.token = "" }},
		{"closed issue", func(_ *bondrenFixture, t *ticket) { t.Status = "closed" }},
		{"foreign URL", func(_ *bondrenFixture, t *ticket) {
			t.URL = bondrenTestOrigin + "/work/support/00000000-0000-4000-8000-000000000099"
		}},
		{"foreign kind", func(_ *bondrenFixture, t *ticket) { t.Kind = "github" }},
		{"changed issue", func(f *bondrenFixture, _ *ticket) {
			f.messages = strings.Replace(f.messages, "Still failing.", "racing edit", 1)
		}},
		{"oversized body", func(_ *bondrenFixture, _ *ticket) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBondrenFixture(t)
			f.source.writable = true
			message := body
			if tc.name == "oversized body" {
				message = strings.Repeat("x", maxBondrenCommentBytes+1)
			}
			ticket := mustBondrenTicket(t, f)
			tc.change(f, &ticket)
			if _, err := f.source.postComment(t.Context(), ticket, message, true); err == nil {
				t.Fatal("Bondren message write was not gated")
			}
		})
	}
}
