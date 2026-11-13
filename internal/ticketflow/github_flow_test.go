package ticketflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
)

// This source fixture exercises preparation and approval only. Connector HTTP
// behavior and live integration are deliberately outside these tests.
type githubFlowSource struct {
	origin     string
	snapshot   snapshot
	contents   map[string][]byte
	reads      []string
	writes     int
	confirmErr error
}

func (s *githubFlowSource) account() string { return s.origin }
func (s *githubFlowSource) ticket(context.Context, int64) (ticket, error) {
	return s.snapshot.Ticket, nil
}
func (s *githubFlowSource) read(context.Context, int64) (snapshot, error) {
	return s.snapshot, nil
}
func (s *githubFlowSource) confirm(context.Context, ticket) error { return s.confirmErr }
func (s *githubFlowSource) file(_ context.Context, a attachment) ([]byte, error) {
	s.reads = append(s.reads, a.URL)
	data, found := s.contents[a.URL]
	if !found {
		return nil, errors.New("unknown fixture attachment")
	}
	return data, nil
}
func (s *githubFlowSource) postComment(context.Context, ticket, string, bool) (int64, error) {
	s.writes++
	return 0, ErrBlocked
}

func newGitHubFlowSource() *githubFlowSource {
	const origin = "https://github.com/example/reproduction"
	const issueURL = origin + "/issues/42"
	const attachmentURL = "https://github.com/user-attachments/files/123/app.py.txt"
	return &githubFlowSource{
		origin: origin,
		snapshot: snapshot{
			Ticket: ticket{ID: 42, Kind: "github", URL: issueURL, Requester: 10,
				Subject: "Timeout is too short", Status: "open", Tags: []string{"reproduce"},
				Updated:          "2026-11-09T12:00:00Z",
				RequesterProfile: Requester{ID: 10, Name: "reporter", URL: "https://github.com/reporter"}},
			Comments: []comment{
				{ID: 100, Author: 10, Public: true, URL: issueURL,
					Body: "Python version: 3.12.4\nExpected: Wait five seconds.\nActual: Stops after five milliseconds."},
				{ID: 200, Author: 10, Public: true, URL: issueURL + "#issuecomment-200", Body: "The smallest reproduction.",
					Files: []attachment{{ID: 123, Name: "app.py.txt", Size: -1, URL: attachmentURL}}},
			},
		},
		contents: map[string][]byte{attachmentURL: []byte("print('timeout')\n")},
	}
}

func openGitHubFlowFixture(t *testing.T, s *githubFlowSource, dir string, secrets ...string) *Flow {
	t.Helper()
	store, err := openStore(dir, s.account())
	if err != nil {
		t.Fatal(err)
	}
	flow := newFlow(Config{Source: "github", Repository: strings.TrimPrefix(s.origin, "https://github.com/"),
		Policy: Policy{Tag: "reproduce"}}, s, store, investigation.DefaultConfig(), secrets)
	t.Cleanup(func() {
		if !flow.closed {
			if err := flow.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return flow
}

func trackGitHubFlowFixture(t *testing.T, flow *Flow) Case {
	t.Helper()
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || preview.State != "approval" || preview.Plan == "" {
		t.Fatalf("ready preview: %+v, %v", preview, err)
	}
	tracked, err := flow.Track(t.Context(), 42, preview.Revision)
	if err != nil || tracked.State != "approval" {
		t.Fatalf("track ready issue: %+v, %v", tracked, err)
	}
	return tracked
}

func TestGitHubFlowPreservesAuthorEvidenceAndDurableRun(t *testing.T) {
	s := newGitHubFlowSource()
	issueURL := s.snapshot.Ticket.URL
	const configURL = "https://github.com/user-attachments/files/124/config.json.txt"
	s.snapshot.Comments[0].Files = []attachment{{ID: 124, Name: "config.json.txt", Size: -1, URL: configURL}}
	s.contents[configURL] = []byte("{\"timeout\":5}\n")
	s.snapshot.Comments = append(s.snapshot.Comments, comment{
		ID: 300, Author: 99, Public: true, URL: issueURL + "#issuecomment-300",
		Body:  "Python version: 9.99\nExpected: Outsider claim.\nActual: Outsider result.",
		Files: []attachment{{Name: "outsider.py", Size: -1, URL: "https://example.invalid/outsider.py"}},
	})
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openGitHubFlowFixture(t, s, dir)
	version := flow.Procedure()
	version.Procedure.Evidence = append(version.Procedure.Evidence,
		EvidenceRule{Kind: "attachment", Match: ".json", Required: true, Question: "Attach a sanitized config file."})
	if _, err := flow.SaveProcedure(version.Procedure, version.Revision); err != nil {
		t.Fatal(err)
	}
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || preview.State != "approval" || preview.Source != "github" || preview.URL != issueURL || len(flow.Cases()) != 0 {
		t.Fatalf("preview identity or state: %+v, %v", preview, err)
	}
	if len(preview.Facts) != 3 || len(preview.Files) != 2 {
		t.Fatalf("wrong selected facts/files: %+v", preview)
	}
	for _, fact := range preview.Facts {
		if fact.CommentID != "100" || fact.URL != issueURL || strings.Contains(fact.Value, "Outsider") {
			t.Fatalf("fact attributed to wrong author: %+v", fact)
		}
	}
	sum := sha256.Sum256([]byte("print('timeout')\n"))
	if preview.Files[0].Path != "app.py" || preview.Files[0].SHA256 != hex.EncodeToString(sum[:]) || preview.Files[0].Content != nil || preview.Files[1].Path != "config.json" || preview.Files[1].SourceURL != configURL {
		t.Fatalf("sanitized file inventory: %+v", preview.Files)
	}
	tracked, err := flow.Track(t.Context(), 42, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	launches := 0
	launch := func(input casefile.Case, _ RunTarget, reserve func() error) error {
		launches++
		if input.Source["kind"] != "github" || input.Source["repository"] != "example/reproduction" ||
			input.Source["issue_number"] != "42" || input.Source["url"] != issueURL || input.Source["approval_plan"] != tracked.Plan {
			t.Fatalf("incorrect investigation source: %+v", input.Source)
		}
		evidence := strings.Join(input.Evidence, "\n")
		if !strings.Contains(evidence, issueURL+"#issuecomment-200") || !strings.Contains(evidence, configURL) ||
			!strings.Contains(evidence, "config.json from GitHub issue author at "+issueURL) || strings.Contains(evidence, "issuecomment-300") {
			t.Fatalf("incorrect evidence provenance: %s", evidence)
		}
		if len(input.Attachments) != 2 || string(input.Attachments[0].Content) != "print('timeout')\n" ||
			!strings.Contains(input.Description, "reported in GitHub") || strings.Contains(input.Description, "Outsider") {
			t.Fatalf("wrong approved inputs: %+v", input)
		}
		return reserve()
	}
	launched, err := flow.Approve(t.Context(), 42, tracked.Plan, launch)
	if err != nil || launches != 1 || launched.RunID == "" || launched.ApprovedAt.IsZero() || s.writes != 0 {
		t.Fatalf("launch: %+v, writes=%d, %v", launched, s.writes, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openGitHubFlowFixture(t, s, dir)
	if _, err := flow.Approve(t.Context(), 42, tracked.Plan, launch); !errors.Is(err, ErrBlocked) || launches != 1 || flow.Cases()[0].RunID != launched.RunID {
		t.Fatalf("restarted flow admitted another run: launches=%d, %v", launches, err)
	}
	for _, target := range s.reads {
		if strings.Contains(target, "example.invalid") {
			t.Fatal("downloaded a non-author attachment")
		}
	}
}

func TestGitHubFlowApprovalRejectsChangedEvidenceAndProvenance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*githubFlowSource)
	}{
		{"attachment bytes", func(s *githubFlowSource) {
			s.contents[s.snapshot.Comments[1].Files[0].URL] = []byte("print('changed')\n")
		}},
		{"attachment URL with identical bytes", func(s *githubFlowSource) {
			a := &s.snapshot.Comments[1].Files[0]
			target := "https://github.com/user-attachments/files/999/app.py.txt"
			s.contents[target] = s.contents[a.URL]
			a.URL = target
		}},
		{"comment edit without issue timestamp change", func(s *githubFlowSource) {
			s.snapshot.Comments[1].Body = "Edited reproduction explanation."
		}},
		{"comment source reference", func(s *githubFlowSource) {
			s.snapshot.Comments[1].URL = s.snapshot.Ticket.URL + "#issuecomment-201"
		}},
		{"comment deletion", func(s *githubFlowSource) {
			s.snapshot.Comments = s.snapshot.Comments[:1]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGitHubFlowSource()
			flow := openGitHubFlowFixture(t, s, filepath.Join(t.TempDir(), "cases"))
			tracked := trackGitHubFlowFixture(t, flow)
			tc.change(s)
			launched := false
			_, err := flow.Approve(t.Context(), 42, tracked.Plan, func(casefile.Case, RunTarget, func() error) error {
				launched = true
				return nil
			})
			if !errors.Is(err, ErrChanged) || launched || flow.Cases()[0].RunID != "" || flow.Cases()[0].Plan == tracked.Plan {
				t.Fatalf("changed evidence retained approval: launched=%v, %v", launched, err)
			}
		})
	}
}

func TestGitHubFlowFinalConfirmationBlocksLaunch(t *testing.T) {
	s := newGitHubFlowSource()
	flow := openGitHubFlowFixture(t, s, filepath.Join(t.TempDir(), "cases"))
	tracked := trackGitHubFlowFixture(t, flow)
	s.confirmErr = ErrChanged
	_, err := flow.Approve(t.Context(), 42, tracked.Plan, func(casefile.Case, RunTarget, func() error) error {
		t.Fatal("launched after final freshness check failed")
		return nil
	})
	if !errors.Is(err, ErrChanged) || flow.Cases()[0].Plan != "" || flow.Cases()[0].State != "review" {
		t.Fatalf("failed freshness check did not revoke approval: %v", err)
	}
}

func TestGitHubFlowMissingEvidenceNeverWritesClarification(t *testing.T) {
	s := newGitHubFlowSource()
	s.snapshot.Comments = s.snapshot.Comments[:1]
	flow := openGitHubFlowFixture(t, s, filepath.Join(t.TempDir(), "cases"))
	c, err := flow.Preview(t.Context(), 42)
	if err != nil || c.State != "review" || c.Draft == "" {
		t.Fatalf("missing evidence review: %+v, %v", c, err)
	}
	c, err = flow.Track(t.Context(), 42, c.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Clarify(t.Context(), 42, c.Revision); !errors.Is(err, ErrBlocked) {
		t.Fatalf("read-only source admitted clarification: %v", err)
	}
	if _, err := flow.Refresh(t.Context(), 42); err != nil || s.writes != 0 || len(flow.Cases()[0].Questions) != 0 {
		t.Fatalf("read-only refresh attempted a write: writes=%d, %v", s.writes, err)
	}
}

func TestGitHubFlowRejectsConfiguredCredentialInEvidence(t *testing.T) {
	const secret = "fixture-github-access-credential"
	for _, location := range []string{"fact", "file", "filename", "attachment URL"} {
		t.Run(location, func(t *testing.T) {
			s := newGitHubFlowSource()
			switch location {
			case "fact":
				s.snapshot.Comments[0].Body += "\nActual: " + secret
			case "file":
				s.contents[s.snapshot.Comments[1].Files[0].URL] = []byte("print('" + secret + "')\n")
			case "filename":
				s.snapshot.Comments[1].Files[0].Name = secret + ".py"
			case "attachment URL":
				s.snapshot.Comments[1].Files[0].URL += "?token=" + secret
			}
			flow := openGitHubFlowFixture(t, s, filepath.Join(t.TempDir(), "cases"), secret)
			c, err := flow.Preview(t.Context(), 42)
			if err != nil || c.State != "review" || c.Plan != "" || len(c.Files) != 0 {
				t.Fatalf("credential evidence was admitted: %+v, %v", c, err)
			}
			data, err := json.Marshal(c)
			if err != nil || strings.Contains(string(data), secret) {
				t.Fatal("configured credential appeared in the case response")
			}
		})
	}
}

func TestGitHubFlowRejectsUnsafeOrOversizedAttachments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*githubFlowSource)
	}{
		{"empty", func(s *githubFlowSource) { s.contents[s.snapshot.Comments[1].Files[0].URL] = nil }},
		{"NUL", func(s *githubFlowSource) { s.contents[s.snapshot.Comments[1].Files[0].URL] = []byte{'x', 0} }},
		{"invalid UTF-8", func(s *githubFlowSource) { s.contents[s.snapshot.Comments[1].Files[0].URL] = []byte{0xff} }},
		{"unknown-size overflow", func(s *githubFlowSource) {
			s.contents[s.snapshot.Comments[1].Files[0].URL] = []byte(strings.Repeat("x", casefile.MaxFileBytes+1))
		}},
		{"path traversal", func(s *githubFlowSource) { s.snapshot.Comments[1].Files[0].Name = "../app.py" }},
		{"sanitized name collision", func(s *githubFlowSource) {
			a := s.snapshot.Comments[1].Files[0]
			a.Name = "app.py"
			s.snapshot.Comments[1].Files = append(s.snapshot.Comments[1].Files, a)
		}},
		{"five files", func(s *githubFlowSource) {
			a := s.snapshot.Comments[1].Files[0]
			for _, name := range []string{"one.txt", "two.txt", "three.txt", "four.txt"} {
				a.Name = name
				s.snapshot.Comments[1].Files = append(s.snapshot.Comments[1].Files, a)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGitHubFlowSource()
			tc.change(s)
			flow := openGitHubFlowFixture(t, s, filepath.Join(t.TempDir(), "cases"))
			c, err := flow.Preview(t.Context(), 42)
			if err != nil || c.State != "review" || c.Plan != "" || len(c.Files) != 0 {
				t.Fatalf("unsafe evidence admitted: %+v, %v", c, err)
			}
		})
	}
}

// A content refusal and an unusable evidence location reach the operator as
// different reasons, and neither reason carries the refused content.
func TestGitHubFlowSeparatesIngestionRefusalFromLocationFailure(t *testing.T) {
	token := "ghp_" + strings.Repeat("A", 36)
	for _, tc := range []struct {
		name   string
		dir    func(*testing.T) string
		change func(*githubFlowSource)
		want   string
	}{
		{"credential-shaped attachment", func(t *testing.T) string { return filepath.Join(t.TempDir(), "cases") },
			func(s *githubFlowSource) {
				s.contents[s.snapshot.Comments[1].Files[0].URL] = []byte("print('" + token + "')\n")
			}, "failed ingestion checks"},
		{"unusable evidence location", func(t *testing.T) string {
			// The store opens through this path, but ingestion refuses the
			// parent reference in the evidence directory built under it.
			base := t.TempDir()
			if err := os.Mkdir(filepath.Join(base, "x"), 0o700); err != nil {
				t.Fatal(err)
			}
			return base + "/x/../cases"
		}, func(*githubFlowSource) {}, "ticket state directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newGitHubFlowSource()
			tc.change(s)
			flow := openGitHubFlowFixture(t, s, tc.dir(t))
			c, err := flow.Preview(t.Context(), 42)
			if err != nil || c.State != "review" || c.Plan != "" || !strings.Contains(c.Reason, tc.want) {
				t.Fatalf("unexpected preview: %+v, %v", c, err)
			}
			data, err := json.Marshal(c)
			if err != nil || strings.Contains(string(data), token) {
				t.Fatal("refused content appeared in the case response")
			}
		})
	}
}

func TestGitHubFlowStoreCannotBeReusedForAnotherRepository(t *testing.T) {
	s := newGitHubFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openGitHubFlowFixture(t, s, dir)
	trackGitHubFlowFixture(t, flow)
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := openStore(dir, "https://github.com/example/other"); err == nil {
		store.lock.Close()
		t.Fatal("another repository reused the same issue number's stored approval")
	}
}
