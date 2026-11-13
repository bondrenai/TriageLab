package ticketflow

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

func TestProcedureNamespaceRoutesToApprovedRunTarget(t *testing.T) {
	f := newFixture(t)
	flow := openTestFlow(t, filepath.Join(t.TempDir(), "cases"), f, false)
	flow.runtime.RunNamespaces = []string{"triagelab-runs"}
	resolved := ""
	flow.ResolveTarget = func(_ context.Context, _, _, namespace string) (RunTarget, error) {
		resolved = namespace
		return RunTarget{Agent: "routed", Sandbox: "hardened", Namespace: namespace}, nil
	}
	ctx := t.Context()
	p := flow.Procedure().Procedure
	p.Namespace = "not-allowlisted"
	if _, err := flow.SaveProcedure(p, flow.Procedure().Revision); !errors.Is(err, ErrInvalidProcedure) {
		t.Fatalf("namespace outside the allowlist accepted: %v", err)
	}
	p.Namespace = "triagelab-runs"
	p.DefaultAgent, p.Sandbox = "routed", "hardened"
	if _, err := flow.SaveProcedure(p, flow.Procedure().Revision); err != nil {
		t.Fatal(err)
	}
	f.reply()
	preview, err := flow.Preview(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Track(ctx, 42, preview.Revision); err != nil {
		t.Fatal(err)
	}
	ready, err := flow.Refresh(ctx, 42)
	if err != nil || ready.State != "approval" || ready.Namespace != "triagelab-runs" {
		t.Fatalf("expected an approval in the procedure namespace: %+v %v", ready, err)
	}
	if resolved != "triagelab-runs" {
		t.Fatalf("resolver saw namespace %q", resolved)
	}
	if _, err := flow.Approve(ctx, 42, ready.Plan, func(_ casefile.Case, target RunTarget, reserve func() error) error {
		if target.Namespace != "triagelab-runs" {
			t.Fatalf("launch target missing namespace: %+v", target)
		}
		return reserve()
	}); err != nil {
		t.Fatal(err)
	}
}

func TestProcedureNamespaceRequiresResolver(t *testing.T) {
	f := newFixture(t)
	flow := openTestFlow(t, filepath.Join(t.TempDir(), "cases"), f, false)
	flow.runtime.RunNamespaces = []string{"triagelab-runs"}
	flow.ResolveTarget = nil
	ctx := t.Context()
	p := flow.Procedure().Procedure
	p.Namespace = "triagelab-runs"
	if _, err := flow.SaveProcedure(p, flow.Procedure().Revision); err != nil {
		t.Fatal(err)
	}
	f.reply()
	preview, err := flow.Preview(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := flow.Track(ctx, 42, preview.Revision); err != nil {
		t.Fatal(err)
	}
	ready, err := flow.Refresh(ctx, 42)
	if err != nil || ready.State != "review" || ready.Plan != "" {
		t.Fatalf("unresolved namespace produced a plan: %+v %v", ready, err)
	}
}

func TestContinuationBindsRunNamespaceToAllowlist(t *testing.T) {
	f := newFixture(t)
	dir := filepath.Join(t.TempDir(), "cases")
	flow := noteTestFlow(t, dir, f)
	id, saved := completedTicketReport(t, flow, f)
	saved["status"] = "paused"
	input, err := casefile.Ingest("The customer confirmed the timeout is in milliseconds.", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	before := flow.Cases()[0]
	config := flow.runtime
	config.Agent, config.Template, config.Namespace = before.Agent, before.Sandbox, "triagelab-runs"
	a := ContinuationApproval{RunID: id, Round: 1, Report: saved, Input: input, Config: config, Actor: "operator"}
	launch := func(func() error) error { return ErrBusy }
	if err := flow.ApproveContinuation(a, launch); !errors.Is(err, ErrChanged) {
		t.Fatalf("namespace outside the allowlist continued: %v", err)
	}
	flow.runtime.RunNamespaces = []string{"triagelab-runs"}
	if err := flow.ApproveContinuation(a, launch); !errors.Is(err, ErrBusy) {
		t.Fatalf("allowlisted namespace did not continue: %v", err)
	}
}
