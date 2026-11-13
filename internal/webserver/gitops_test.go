package webserver

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/gitops"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

type fixtureGitOps struct {
	snapshot          gitops.Snapshot
	token             string
	readErr, tokenErr error
	statuses          []gitops.Status
}

func (f *fixtureGitOps) Snapshot(context.Context) (gitops.Snapshot, error) {
	return f.snapshot, f.readErr
}
func (f *fixtureGitOps) Token(context.Context, *gitops.SecretRef) (string, error) {
	return f.token, f.tokenErr
}
func (f *fixtureGitOps) SetStatus(_ context.Context, _ gitops.Connection, status gitops.Status) error {
	f.statuses = append(f.statuses, status)
	return nil
}

func managedFixture(t *testing.T) (*Server, *fixtureGitOps, string) {
	t.Helper()
	s := newTestServer(t, nil)
	f := &fixtureGitOps{token: "fixture-gitops-private-token"}
	conn := gitops.Connection{APIVersion: gitops.APIVersion, Kind: "TicketConnection", Metadata: gitops.Metadata{Name: "issues", Namespace: "support", UID: "uid", ResourceVersion: "1", Generation: 1},
		Spec: gitops.ConnectionSpec{Workspace: "lab", Label: "Support", Config: ticketflow.Config{Source: "github", Repository: "acme/demo", Policy: ticketflow.Policy{Tag: "reproduce"}}, TokenSecretRef: &gitops.SecretRef{Name: "source", Key: "token"}, ProcedureRefs: []string{"timeout"}}}
	var p gitops.Procedure
	p.Metadata = gitops.Metadata{Name: "timeout", Namespace: "support"}
	p.Spec.Workspace = "lab"
	p.Spec.Procedure = ticketflow.Procedure{Policy: ticketflow.Policy{Tag: "reproduce"}, Evidence: ticketflow.EvidenceRules{}, DefaultAgent: s.config.Agent, Sandbox: s.config.Template}
	f.snapshot = gitops.Snapshot{Connections: []gitops.Connection{conn}, Procedures: []gitops.Procedure{p}}
	done := make(chan struct{})
	close(done)
	s.gitops = &gitopsSync{source: f, config: gitops.Config{Server: "https://cluster", Namespace: "support", Workspace: "lab"}, cancel: func() {}, done: done}
	key := gitops.StateKey("https://cluster", "support", "lab", "issues")
	return s, f, key
}

func reconcile(t *testing.T, s *Server) {
	t.Helper()
	if err := s.reconcileGitOps(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestGitOpsReconcileIdempotenceRotationDeletionAndRetainedState(t *testing.T) {
	s, f, key := managedFixture(t)
	reconcile(t, s)
	first := s.ticketConnection(key[:8])
	if first == nil || first.flow.Procedure().Procedure.Policy.Tag != "reproduce" {
		t.Fatal("managed connection not opened")
	}
	reconcile(t, s)
	if s.ticketConnection(key[:8]) != first {
		t.Fatal("unchanged config reopened flow")
	}
	f.token = "rotated-fixture-gitops-token"
	reconcile(t, s)
	if s.ticketConnection(key[:8]) == first {
		t.Fatal("Secret rotation was not applied")
	}
	state := filepath.Join(s.ticketRoot(), "gitops", key)
	marker := filepath.Join(state, ".retained-test")
	if err := os.WriteFile(marker, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	saved := f.snapshot.Connections
	f.snapshot.Connections = nil
	reconcile(t, s)
	if s.ticketConnection(key[:8]) != nil {
		t.Fatal("deleted connection still active")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("deletion removed private case state")
	}
	f.snapshot.Connections = saved
	reconcile(t, s)
	if s.ticketConnection(key[:8]) == nil {
		t.Fatal("recreated connection did not reopen retained state")
	}
	f.snapshot.Connections[0].Spec.Config.Repository = "acme/another"
	reconcile(t, s)
	if s.ticketConnection(key[:8]) != nil || f.statuses[len(f.statuses)-1].Conditions[0].Status != "False" {
		t.Fatal("changed source adopted retained state")
	}
}

func TestGitOpsFailClosedAndRecover(t *testing.T) {
	s, f, key := managedFixture(t)
	reconcile(t, s)
	f.tokenErr = errors.New("private-source-error")
	reconcile(t, s)
	if s.ticketConnection(key[:8]) != nil {
		t.Fatal("missing Secret left flow running")
	}
	f.tokenErr = nil
	reconcile(t, s)
	f.snapshot.Procedures[0].Spec.Procedure.Policy.AutoQuestions = true
	f.snapshot.Procedures[0].Spec.Procedure.Policy.MaxQuestions = 1
	reconcile(t, s)
	if s.ticketConnection(key[:8]) != nil {
		t.Fatal("procedure exceeded connection send controls")
	}
	f.snapshot.Procedures[0].Spec.Procedure.Policy.AutoQuestions = false
	reconcile(t, s)
	f.readErr = errors.New("API unavailable")
	if s.reconcileGitOps(context.Background()) == nil || s.ticketConnection(key[:8]) != nil {
		t.Fatal("failed list left flow running")
	}
	if _, err := os.Stat(filepath.Join(s.ticketRoot(), "gitops", key)); err != nil {
		t.Fatal("failed list deleted state")
	}
	f.readErr = nil
	reconcile(t, s)
	if s.ticketConnection(key[:8]) == nil {
		t.Fatal("failed to recover")
	}
	if err := os.WriteFile(filepath.Join(s.ticketRoot(), "registry.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if s.reconcileGitOps(context.Background()) == nil || s.ticketConnection(key[:8]) != nil {
		t.Fatal("unreadable ownership registry left managed flow active")
	}
	if statuses := s.gitopsStatuses(); len(statuses) != 1 || statuses[0].Running || statuses[0].Error == "" {
		t.Fatal("stopped flow still reported as running")
	}
}

func TestGitOpsProtectsDefinitionsAndKeepsSecretsPrivate(t *testing.T) {
	s, f, key := managedFixture(t)
	reconcile(t, s)
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/tickets/connections/" + key[:8], `{}`},
		{http.MethodDelete, "/api/tickets/connections/" + key[:8], ``},
		{http.MethodPost, "/api/tickets/c/" + key[:8] + "/procedure", `{}`},
	} {
		w := request(s, call.method, call.path, strings.NewReader(call.body))
		if w.Code != http.StatusConflict {
			t.Fatalf("managed mutation returned %d: %s", w.Code, w.Body)
		}
	}
	for _, path := range []string{"/api/tickets/connections", "/api/tickets"} {
		w := request(s, http.MethodGet, path, nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"managed_by":"support/issues"`) || strings.Contains(w.Body.String(), f.token) {
			t.Fatalf("unsafe managed view: %d %s", w.Code, w.Body)
		}
	}
	err := filepath.WalkDir(s.ticketRoot(), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if strings.Contains(string(data), f.token) {
			t.Error("credential persisted in private state")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.ticketRoot(), "gitops", key, ".procedure")); !os.IsNotExist(err) {
		t.Fatal("GitOps procedure duplicated on disk")
	}
}

func TestGitOpsNeverAdoptsLocalConnection(t *testing.T) {
	s, f, key := managedFixture(t)
	record := ticketflow.Connection{ID: key[:8], Config: f.snapshot.Connections[0].Spec.Config}
	if err := ticketflow.SaveConnections(s.ticketRoot(), []ticketflow.Connection{record}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnableTicketConnections(nil); err != nil {
		t.Fatal(err)
	}
	local := s.ticketConnection(key[:8])
	reconcile(t, s)
	if s.ticketConnection(key[:8]) != local || local.managed != nil {
		t.Fatal("GitOps replaced local connection")
	}
	if s.gitopsOwns(key[:8]) {
		t.Fatal("GitOps claimed a local connection's edit permissions")
	}
	if f.statuses[len(f.statuses)-1].Conditions[0].Reason != "IdentityConflict" {
		t.Fatal("collision not reported")
	}
}
