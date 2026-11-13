package ticketflow

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

func TestProcedureUsesOnlyConfiguredEvidenceAndQuestions(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "state")
	flow := openTestFlow(t, dir, f, true)
	ctx := t.Context()
	version := flow.Procedure()
	p := version.Procedure
	if err := json.Unmarshal([]byte(`[
		{"kind":"field","match":"Request ID","pattern":"^req-[0-9]+$","required":true,"question":"Reply with Request ID: req-123."},
		{"kind":"attachment","match":".log","required":true,"question":"Attach the request trace as a .log file."}
	]`), &p.Evidence); err != nil {
		t.Fatalf("procedure does not accept user-defined evidence: %v", err)
	}
	p.Clarification = "Please help us find the failing request."
	want := []string{"Reply with Request ID: req-123.", "Attach the request trace as a .log file."}
	draft, err := flow.PreviewProcedure(ctx, 42, p)
	if err != nil || !slices.Equal(draft.Missing, want) ||
		draft.Draft != p.Clarification+"\n\n- "+strings.Join(want, "\n- ") || f.count() != 0 {
		t.Fatalf("wrong draft or unexpected send: %+v, %v", draft, err)
	}
	if _, err := flow.SaveProcedure(p, version.Revision); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.comments = append(f.comments, comment{ID: 3, Author: 10, Public: true, Body: "Request ID: wrong-format"},
		comment{ID: 4, Author: 11, Public: true, Body: "Request ID: req-666"})
	f.mu.Unlock()
	draft, err = flow.Preview(ctx, 42)
	if err != nil || !slices.Equal(draft.Missing, want) {
		t.Fatalf("an invalid or outsider value met the evidence rule: %+v, %v", draft, err)
	}
	f.mu.Lock()
	f.comments = append(f.comments, comment{ID: 5, Author: 10, Public: true, Body: "Request ID: req-123"},
		comment{ID: 6, Author: 10, Public: true, Files: []attachment{
			{ID: 60, Name: "trace.log", URL: f.server.URL + "/attachments/app.py", Size: int64(len(f.contents))},
		}})
	f.mu.Unlock()
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openTestFlow(t, dir, f, true)
	c, err := flow.Preview(ctx, 42)
	if err != nil || c.State != "approval" || len(c.Files) != 1 || c.Files[0].Path != "trace.log" ||
		len(c.Facts) != 1 || c.Facts[0].Value != "req-123" || c.Facts[0].CommentID != "5" {
		t.Fatalf("configured evidence did not reach approval without Python or JSON: %+v, %v", c, err)
	}
	c, err = flow.Track(ctx, 42, c.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = flow.Approve(ctx, 42, c.Plan, func(input casefile.Case, _ RunTarget, reserve func() error) error {
		if !strings.Contains(input.Description, "Request ID: req-123") ||
			!strings.Contains(strings.Join(input.Evidence, "\n"), "trace.log from Zendesk public requester comment 6") {
			t.Fatalf("the run lost evidence or its provenance: %+v", input)
		}
		return reserve()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcedureCanRemoveAllEvidenceRequirements(t *testing.T) {
	f := newFixture(t)
	flow := openTestFlow(t, filepath.Join(t.TempDir(), "state"), f, false)
	version := flow.Procedure()
	if err := json.Unmarshal([]byte("[]"), &version.Procedure.Evidence); err != nil {
		t.Fatalf("cannot clear evidence requirements: %v", err)
	}
	version.Procedure.Clarification = ""
	if _, err := flow.SaveProcedure(version.Procedure, version.Revision); err != nil {
		t.Fatal(err)
	}
	c, err := flow.Preview(t.Context(), 42)
	if err != nil || c.State != "approval" || len(c.Missing) != 0 || len(c.Files) != 0 || c.Draft != "" {
		t.Fatalf("procedure imposed hidden evidence or sent a draft: %+v, %v", c, err)
	}
}

func TestProductProceduresSelectEvidenceAndRecheckApprovals(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "state")
	flow := openTestFlow(t, dir, f, true)
	ctx := t.Context()
	f.mu.Lock()
	f.ticket.Tags = []string{"product:network", "product:platform"}
	f.questionPrefix = "Help us identify the affected release."
	f.mu.Unlock()
	version := flow.Procedure()
	version.Procedure.Policy.AutoQuestions = false
	network := version.Procedure
	network.Policy = Policy{Tag: "product:network", AutoQuestions: true, MaxQuestions: 1}
	network.Clarification = "Help us find the failing network request."
	network.Evidence = EvidenceRules{{Kind: "field", Match: "Trace ID", Required: true, Question: "Share the trace ID."}}
	platform := network
	platform.Policy.Tag = "product:platform"
	platform.Clarification = "Help us identify the affected release."
	platform.Evidence = EvidenceRules{{Kind: "field", Match: "Release", Required: true, Question: "Share the release version."}}

	version.Additional = []Procedure{network, platform}
	saved, err := flow.SaveProcedures(version)
	if err != nil {
		t.Fatal(err)
	}
	first, err := flow.Preview(ctx, 42)
	if err != nil || first.State != "ask" || first.MatchedTag != network.Policy.Tag ||
		!slices.Equal(first.Missing, []string{"Share the trace ID."}) {
		t.Fatalf("expected first matching product's questions: %+v, %v", first, err)
	}
	first, err = flow.Track(ctx, 42, first.Revision)
	if err != nil {
		t.Fatal(err)
	}
	saved.Additional = []Procedure{platform, network}
	saved, err = flow.SaveProcedures(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Clarify(ctx, 42, first.Revision); !errors.Is(err, ErrChanged) || f.count() != 0 {
		t.Fatalf("stale draft sent after priority change: %v, sends=%d", err, f.count())
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openTestFlow(t, dir, f, true)
	review, err := flow.Refresh(ctx, 42)
	if err != nil || review.MatchedTag != platform.Policy.Tag ||
		review.Draft != platform.Clarification+"\n\n- Share the release version." || f.count() != 0 {
		t.Fatalf("expected saved platform procedure after restart: %+v, %v", review, err)
	}
	waiting, err := flow.Clarify(ctx, 42, review.Revision)
	if err != nil || waiting.State != "waiting" || f.count() != 1 {
		t.Fatalf("product-specific clarification did not send: %+v, %v", waiting, err)
	}
	f.mu.Lock()
	f.comments = append(f.comments, comment{ID: 4, Author: 10, Public: true, Body: "Release: 1.4.2"})
	f.mu.Unlock()
	ready, err := flow.Refresh(ctx, 42)
	if err != nil || ready.State != "approval" || ready.Plan == "" || len(ready.Facts) != 1 {
		t.Fatalf("expected approval using only platform evidence: %+v, %v", ready, err)
	}
	f.mu.Lock()
	f.ticket.Tags = []string{network.Policy.Tag}
	f.mu.Unlock()
	if _, err := flow.Approve(ctx, 42, ready.Plan, func(casefile.Case, RunTarget, func() error) error {
		t.Fatal("approval from the platform procedure launched a network case")
		return nil
	}); !errors.Is(err, ErrChanged) {
		t.Fatalf("expected new approval after product changed: %v", err)
	}
	if c := flow.Cases()[0]; c.MatchedTag != network.Policy.Tag || c.Plan != "" || c.ClarificationApproval != "" {
		t.Fatalf("previous approval leaked to new procedure: %+v", c)
	}
	saved.Additional = []Procedure{network, network}
	if _, err := flow.SaveProcedures(saved); !errors.Is(err, ErrInvalidProcedure) {
		t.Fatalf("expected duplicate match tag to be rejected: %v", err)
	}
}

func TestProcedureDraftRoutingAndApproval(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "state")
	flow := openTestFlow(t, dir, f, true)
	flow.ResolveTarget = func(_ context.Context, agent, sandbox, _ string) (RunTarget, error) {
		if agent == "billing" {
			return RunTarget{}, errors.New("not ready")
		}
		return RunTarget{Agent: agent, Sandbox: sandbox, AgentRevision: "revision", Tools: []string{"lookup"}}, nil
	}
	ctx := t.Context()
	f.mu.Lock()
	f.ticket.Tags = append(f.ticket.Tags, "network", "billing")
	f.mu.Unlock()
	p := flow.Procedure().Procedure
	p.Routes = []RouteRule{{Tag: "network", Agent: "network"}, {Tag: "billing", Agent: "billing"}}
	p.Clarification = "Please provide these details."
	draft, err := flow.PreviewProcedure(ctx, 42, p)
	if err != nil || len(draft.Matches) != 2 || len(draft.Missing) == 0 || len(flow.Cases()) != 0 || f.count() != 0 {
		t.Fatalf("draft preview: %+v, %v", draft, err)
	}
	if _, err := flow.SaveProcedure(p, flow.Procedure().Revision); err != nil {
		t.Fatal(err)
	}
	f.reply()
	preview, err := flow.Preview(ctx, 42)
	if err != nil || preview.State != "review" || preview.Plan != "" {
		t.Fatalf("ambiguous route produced a plan: %+v, %v", preview, err)
	}
	c, err := flow.Track(ctx, 42, preview.Revision)
	if err != nil {
		t.Fatal(err)
	}
	c, err = flow.ChooseRoute(ctx, 42, c.Revision, "billing")
	if err != nil || c.Agent != "billing" || c.Plan != "" {
		t.Fatalf("unavailable route produced a plan: %+v, %v", c, err)
	}
	c, err = flow.ChooseRoute(ctx, 42, c.Revision, "network")
	if err != nil || c.Agent != "network" || c.Plan == "" {
		t.Fatalf("selected route missing approval: %+v, %v", c, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openTestFlow(t, dir, f, true)
	flow.ResolveTarget = func(_ context.Context, agent, sandbox, _ string) (RunTarget, error) {
		return RunTarget{Agent: agent, Sandbox: sandbox, AgentRevision: "revision", Tools: []string{"lookup"}}, nil
	}
	if len(flow.Procedure().Procedure.Routes) != 2 {
		t.Fatal("saved procedure did not survive restart")
	}
	f.mu.Lock()
	f.ticket.Tags = []string{"reproduce", "billing"}
	f.mu.Unlock()
	launch := func(_ casefile.Case, target RunTarget, reserve func() error) error {
		if target.Agent != "billing" || len(target.Tools) != 1 || target.Tools[0] != "lookup" {
			t.Fatalf("wrong launch target: %+v", target)
		}
		return reserve()
	}
	if _, err := flow.Approve(ctx, 42, c.Plan, launch); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed route accepted old approval: %v", err)
	}
	latest := flow.Cases()[0]
	if latest.Agent != "billing" || latest.Plan == "" || latest.RunID != "" {
		t.Fatalf("expected a new billing approval: %+v", latest)
	}
	version := flow.Procedure()
	version.Procedure.Evidence = append(slices.Clone(version.Procedure.Evidence),
		EvidenceRule{Kind: "attachment", Match: ".log", Required: true, Question: "Please attach the execution log."})
	if _, err := flow.SaveProcedure(version.Procedure, version.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Approve(ctx, 42, latest.Plan, launch); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed evidence requirements accepted old approval: %v", err)
	}
	if updated := flow.Cases()[0]; updated.Plan != "" || len(updated.Missing) != 1 {
		t.Fatalf("required log was not reflected in the new plan: %+v", updated)
	}
	version.Procedure.Evidence = version.Procedure.Evidence[:len(version.Procedure.Evidence)-1]
	if _, err := flow.SaveProcedure(version.Procedure, flow.Procedure().Revision); err != nil {
		t.Fatal(err)
	}
	latest, err = flow.Refresh(ctx, 42)
	if err != nil || latest.Plan == "" {
		t.Fatalf("expected refreshed plan: %+v, %v", latest, err)
	}
	if _, err := flow.Approve(ctx, 42, latest.Plan, launch); err != nil {
		t.Fatal(err)
	}
}
