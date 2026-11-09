package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
)

type fixture struct {
	mu              sync.Mutex
	server          *httptest.Server
	ticket          ticket
	comments        []comment
	contents        string
	attempts        int
	unknownDelivery bool
	more            bool
	redirect        string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		ticket: ticket{ID: 42, Requester: 10, Subject: "A timeout", Status: "open", Priority: "high", Tags: []string{"reproduce"},
			Updated: "2026-11-08T12:00:00Z", Fields: []field{{ID: 84, Value: "python"}}},
		comments: []comment{
			{ID: 1, Author: 10, Public: true, Body: "My script times out."},
			{ID: 2, Author: 10, Public: false, Body: "Python version: 3.12\nExpected: secret internal detail\nActual: error"},
		},
		contents: "print(1)\n",
	}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-ticket-credential" {
			t.Error("expected Zendesk OAuth bearer authorization")
			w.WriteHeader(401)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v2/tickets/") && r.Header.Get("Accept") != "application/json" {
			t.Error("ticket requests must accept JSON")
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v2/tickets/42.json":
			_ = json.NewEncoder(w).Encode(map[string]any{"ticket": f.ticket})
		case "GET /api/v2/tickets/42/comments":
			if r.URL.Query().Get("page[size]") != "100" || r.URL.Query().Get("sort") != "created_at" {
				t.Error("wrong bounded comment query")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"comments": f.comments, "meta": map[string]bool{"has_more": f.more}})
		case "GET /attachments/app.py", "GET /attachments/app.py.txt":
			if f.redirect != "" {
				http.Redirect(w, r, f.redirect, 302)
				return
			}
			_, _ = w.Write([]byte(f.contents))
		case "GET /attachments/config.json.txt":
			_, _ = w.Write([]byte("{}\n"))
		case "PUT /api/v2/tickets/42.json":
			f.attempts++
			var body struct {
				Ticket struct {
					Safe    bool    `json:"safe_update"`
					Updated string  `json:"updated_stamp"`
					Comment comment `json:"comment"`
				} `json:"ticket"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !body.Ticket.Safe ||
				body.Ticket.Updated != f.ticket.Updated || !body.Ticket.Comment.Public ||
				strings.Contains(body.Ticket.Comment.Body, "secret internal detail") || !strings.Contains(body.Ticket.Comment.Body, "Python version:") {
				t.Error("unexpected or unsafe clarification request")
				w.WriteHeader(400)
				return
			}
			if f.unknownDelivery {
				w.WriteHeader(503)
				return
			}
			message := body.Ticket.Comment
			message.ID, message.Author = 3, 11
			f.comments = append(f.comments, message)
			f.ticket.Updated = "2026-11-08T12:00:01Z"
			_ = json.NewEncoder(w).Encode(map[string]any{"ticket": f.ticket})
		default:
			t.Errorf("unexpected Zendesk call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) connector() *zendesk {
	client := f.server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &zendesk{origin: f.server.URL, client: client, oauth: &zendeskOAuth{
		scope: zendeskWriteScopes, now: time.Now, pair: &oauthPair{Access: "test-ticket-credential", Expires: time.Now().Add(time.Hour)},
	}}
}

func (f *fixture) reply() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments = append(f.comments, comment{ID: 4, Author: 10, Public: true,
		Body:  "Python version: 3.12.4\nExpected: 5 second timeout\nActual: timeout after 5 ms",
		Files: []attachment{{ID: 60, Name: "app.py", URL: f.server.URL + "/attachments/app.py", Size: int64(len(f.contents))}},
	})
	f.ticket.Updated = "2026-11-08T12:01:00Z"
}

func (f *fixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func openTestFlow(t *testing.T, dir string, f *fixture, auto bool) *Flow {
	t.Helper()
	store, err := openStore(dir, f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{Policy: Policy{Tag: "reproduce", Priorities: []string{"high"}, FieldID: 84, FieldValue: "python", AutoQuestions: auto, MaxQuestions: 2}}
	flow := newFlow(config, f.connector(), store, investigation.DefaultConfig(), []string{"test-ticket-credential"})
	t.Cleanup(func() {
		if !flow.closed {
			_ = flow.Close()
		}
	})
	return flow
}

func TestClarifyResumeAndApproveExactEvidence(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openTestFlow(t, dir, f, true)
	preview, err := flow.Preview(ctx, 42)
	if err != nil || preview.State != "ask" || len(preview.Facts) != 0 || f.count() != 0 || len(flow.Cases()) != 0 {
		t.Fatalf("preview wrote or included private evidence: %+v %v", preview, err)
	}
	if _, err := flow.Track(ctx, 42, "stale"); !errors.Is(err, ErrChanged) || f.count() != 0 {
		t.Fatalf("stale preview had an effect: %v", err)
	}
	tracked, err := flow.Track(ctx, 42, preview.Revision)
	if err != nil || tracked.State != "waiting" || f.count() != 1 {
		t.Fatalf("clarify: %+v %v", tracked, err)
	}
	if _, err := openStore(dir, f.server.URL); err == nil {
		t.Fatal("second process acquired the case store")
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openTestFlow(t, dir, f, true)
	waiting, err := flow.Refresh(ctx, 42)
	if err != nil || waiting.State != "waiting" || f.count() != 1 {
		t.Fatalf("duplicate refresh sent a question: %+v %v", waiting, err)
	}

	f.reply()
	ready, err := flow.Refresh(ctx, 42)
	if err != nil || ready.State != "approval" || len(ready.Files) != 1 || ready.Plan == "" || ready.RunID != "" || f.count() != 1 {
		t.Fatalf("expected a ready plan without a launch: %+v %v", ready, err)
	}
	f.mu.Lock()
	f.contents = "print(2)\n"
	f.mu.Unlock()
	var launchCount int
	launch := func(c casefile.Case, reserve func() error) error {
		launchCount++
		if string(c.Attachments[0].Content) != "print(2)\n" || c.Source["ticket_id"] != "42" || len(c.Evidence) != 4 {
			t.Fatalf("wrong evidence handed to investigator: %+v", c)
		}
		return reserve()
	}
	if _, err := flow.Approve(ctx, 42, ready.Plan, launch); !errors.Is(err, ErrChanged) || launchCount != 0 {
		t.Fatalf("approval with changed attachment bytes launched work: %v", err)
	}
	latest := flow.Cases()[0]
	if latest.Plan == "" || latest.Plan == ready.Plan {
		t.Fatal("updated evidence did not replace the old plan")
	}
	// A full workspace must not consume approval or the case's run allowance.
	if _, err := flow.Approve(ctx, 42, latest.Plan, func(casefile.Case, func() error) error { return ErrBusy }); !errors.Is(err, ErrBusy) || flow.Cases()[0].RunID != "" {
		t.Fatalf("busy workspace consumed the run: %v", err)
	}
	launched, err := flow.Approve(ctx, 42, latest.Plan, launch)
	if err != nil || launchCount != 1 || launched.RunID == "" || launched.ApprovedAt.IsZero() || flow.Cases()[0].RunID != launched.RunID {
		t.Fatalf("approval did not reserve the run: %+v %v", launched, err)
	}
	if _, err := flow.Approve(ctx, 42, latest.Plan, launch); !errors.Is(err, ErrBlocked) || launchCount != 1 {
		t.Fatalf("repeated approval launched work: %v", err)
	}
}

func TestUncertainClarificationAndPausedCaseDoNotSend(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.mu.Lock()
	f.unknownDelivery = true
	f.mu.Unlock()
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openTestFlow(t, dir, f, true)
	c, err := flow.Preview(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	c, err = flow.Track(ctx, 42, c.Revision)
	if err != nil || c.State != "review" || f.count() != 1 || len(c.Questions) != 1 || c.Questions[0].Status != "unconfirmed" {
		t.Fatalf("uncertain delivery not preserved: %+v %v", c, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openTestFlow(t, dir, f, true)
	c, err = flow.Refresh(ctx, 42)
	if err != nil || c.State != "review" || f.count() != 1 {
		t.Fatalf("unknown write was retried: %+v %v", c, err)
	}
	if _, err := flow.Pause(42, true); err != nil {
		t.Fatal(err)
	}
	f.reply()
	c, err = flow.Refresh(ctx, 42)
	if err != nil || !c.Paused || c.Plan != "" || f.count() != 1 {
		t.Fatalf("paused case advanced: %+v %v", c, err)
	}
}

func TestPolicyAndSourceLimitsFailClosed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	flow := openTestFlow(t, filepath.Join(t.TempDir(), "cases"), f, false)
	c, err := flow.Preview(ctx, 42)
	if err != nil || c.State != "review" || c.Draft == "" {
		t.Fatalf("expected a draft: %+v %v", c, err)
	}
	if _, err := flow.Track(ctx, 42, c.Revision); err != nil || f.count() != 0 {
		t.Fatalf("draft policy wrote to Zendesk: %v", err)
	}
	f.mu.Lock()
	f.ticket.Fields[0].Value = "different_product"
	f.mu.Unlock()
	if c, err = flow.Refresh(ctx, 42); err != nil || c.State != "excluded" || f.count() != 0 {
		t.Fatalf("wrong custom field matched: %+v %v", c, err)
	}
	f.mu.Lock()
	f.more = true
	f.mu.Unlock()
	if _, err := flow.Refresh(ctx, 42); err == nil || f.count() != 0 {
		t.Fatalf("incomplete comments were used: %v", err)
	}
}

func TestAttachmentRejectsUntrustedHosts(t *testing.T) {
	f := newFixture(t)
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("untrusted attachment endpoint was contacted")
	}))
	defer foreign.Close()
	f.mu.Lock()
	f.redirect = foreign.URL
	f.mu.Unlock()
	z := f.connector()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, url := range []string{foreign.URL + "/attachments/app.py", f.server.URL + "/attachments/app.py"} {
		if _, err := z.file(ctx, attachment{URL: url, Size: 9}); err == nil {
			t.Fatal("accepted untrusted attachment location")
		}
	}
}

type attachmentTransport func(*http.Request) (*http.Response, error)

func (f attachmentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAttachmentDownloadsFromZendeskHostWithoutCredentials(t *testing.T) {
	f := newFixture(t)
	z := f.connector()
	account := z.client.Transport
	for _, tc := range []struct {
		name, target string
		status       int
		wantDownload bool
	}{
		{"temporary URL", "https://p27.zdusercontent.com/attachment?download=temporary", 200, true},
		{"second redirect", "https://p27.zdusercontent.com/attachment?download=temporary", 302, true},
		{"foreign host", "https://p27.zdusercontent.com.example/attachment", 200, false},
		{"plain HTTP", "http://p27.zdusercontent.com/attachment", 200, false},
		{"embedded user", "https://user@p27.zdusercontent.com/attachment", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.mu.Lock()
			f.redirect = tc.target
			f.mu.Unlock()
			downloads := 0
			z.client.Transport = attachmentTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Scheme+"://"+r.URL.Host == f.server.URL {
					return account.RoundTrip(r)
				}
				downloads++
				if !tc.wantDownload || r.URL.String() != tc.target || r.Method != http.MethodGet || len(r.Header) != 0 {
					t.Fatal("unexpected or credentialed download request")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader("print(1)\n")), Header: http.Header{"Location": {"https://example.com/next"}}}, nil
			})
			data, err := z.file(t.Context(), attachment{URL: f.server.URL + "/attachments/app.py", Size: 9})
			if tc.wantDownload && downloads != 1 || !tc.wantDownload && downloads != 0 {
				t.Fatalf("download requests = %d", downloads)
			}
			if tc.wantDownload && tc.status == 200 {
				if err != nil || string(data) != "print(1)\n" {
					t.Fatalf("attachment download failed: %q %v", data, err)
				}
			} else if err == nil {
				t.Fatal("accepted an unsafe redirect")
			}
		})
	}
}

func TestTextWrappedReproductionFiles(t *testing.T) {
	f := newFixture(t)
	f.reply()
	f.mu.Lock()
	m := &f.comments[len(f.comments)-1]
	m.Files[0].Name = "app.py.txt"
	m.Files[0].URL += ".txt"
	m.Files = append(m.Files, attachment{ID: 61, Name: "config.json.txt", URL: f.server.URL + "/attachments/config.json.txt", Size: 3})
	f.mu.Unlock()
	flow := openTestFlow(t, filepath.Join(t.TempDir(), "cases"), f, false)
	c, err := flow.Preview(t.Context(), 42)
	if err != nil || c.State != "approval" || len(c.Files) != 2 || c.Files[0].Path != "app.py" || c.Files[1].Path != "config.json" {
		t.Fatalf("expected the original filenames in the approval plan: %+v %v", c, err)
	}

	f.mu.Lock()
	duplicate := m.Files[0]
	duplicate.ID, duplicate.Name = 62, "app.py"
	m.Files = append(m.Files, duplicate)
	f.mu.Unlock()
	c, err = flow.Preview(t.Context(), 42)
	if err != nil || c.State != "review" || c.Plan != "" {
		t.Fatalf("colliding filenames were accepted: %+v %v", c, err)
	}
}
