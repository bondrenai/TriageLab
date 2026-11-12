package ticketflow

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

func TestPolicyStartsReadyTicketOnce(t *testing.T) {
	source := newGitHubFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openGitHubFlowFixture(t, source, dir)
	flow.settings.Policy.AutoRuns = true
	launches := 0
	flow.AutoLaunch = func(input casefile.Case, target RunTarget, reserve func() error) error {
		if input.Source["unattended"] != true || input.Source["approval_plan"] == "" ||
			target.Agent != flow.runtime.Agent || len(input.Attachments) != 1 {
			t.Fatalf("launch lost evidence, policy provenance, or target: %+v, %+v", input.Source, target)
		}
		if err := reserve(); err != nil {
			return err
		}
		launches++
		return nil
	}
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || len(flow.Cases()) != 0 || launches != 0 {
		t.Fatalf("preview started or tracked work: %+v, %v", preview, err)
	}
	if _, err := flow.Track(t.Context(), 42, "stale"); !errors.Is(err, ErrChanged) || launches != 0 {
		t.Fatalf("stale enrollment launched work: %v", err)
	}
	tracked, err := flow.Track(t.Context(), 42, preview.Revision)
	if err != nil || tracked.State != "launched" || !tracked.Unattended || tracked.RunID == "" || launches != 1 {
		t.Fatalf("policy did not start the ready case: %+v, launches=%d, %v", tracked, launches, err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openGitHubFlowFixture(t, source, dir)
	reopened.settings.Policy.AutoRuns = true
	reopened.AutoLaunch = flow.AutoLaunch
	refreshed, err := reopened.Refresh(t.Context(), 42)
	if err != nil || refreshed.RunID != tracked.RunID || launches != 1 || source.writes != 0 {
		t.Fatalf("refresh repeated the launch or wrote to the source: %+v, launches=%d, %v", refreshed, launches, err)
	}
}

func TestPolicyWaitsForEvidenceFreshnessAndRunSlot(t *testing.T) {
	source := newGitHubFlowSource()
	comments := source.snapshot.Comments
	source.snapshot.Comments = comments[:1]
	flow := openGitHubFlowFixture(t, source, filepath.Join(t.TempDir(), "cases"))
	flow.settings.Policy.AutoRuns = true
	launchCalls := 0
	flow.AutoLaunch = func(casefile.Case, RunTarget, func() error) error {
		launchCalls++
		return ErrBusy
	}
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil {
		t.Fatal(err)
	}
	tracked, err := flow.Track(t.Context(), 42, preview.Revision)
	if err != nil || tracked.State != "review" || launchCalls != 0 {
		t.Fatalf("missing evidence did not block launch: %+v, %v", tracked, err)
	}
	source.snapshot.Comments = comments
	source.confirmErr = ErrChanged
	flow.poll(t.Context(), 0)
	if launchCalls != 0 || flow.Cases()[0].RunID != "" {
		t.Fatal("the case launched despite a failed final freshness check")
	}
	source.confirmErr = nil
	flow.poll(t.Context(), 0)
	if launchCalls != 1 || flow.Cases()[0].State != "approval" || flow.Cases()[0].RunID != "" {
		t.Fatalf("a busy workspace consumed the ticket attempt: %+v", flow.Cases()[0])
	}
	flow.AutoLaunch = func(_ casefile.Case, _ RunTarget, reserve func() error) error {
		launchCalls++
		return reserve()
	}
	flow.poll(t.Context(), 0)
	if launchCalls != 2 || flow.Cases()[0].State != "launched" {
		t.Fatalf("the next poll did not launch the ready case: %+v", flow.Cases()[0])
	}
}

func TestPolicyRunLimitSurvivesRestart(t *testing.T) {
	source := newGitHubFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openGitHubFlowFixture(t, source, dir)
	prior := Case{TicketID: 43, Account: source.account(), State: "launched", RunID: "prior-run",
		Unattended: true, ApprovedAt: time.Now().UTC().Add(-time.Hour)}
	if err := flow.save(prior); err != nil {
		t.Fatal(err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openGitHubFlowFixture(t, source, dir)
	flow.settings.Policy.AutoRuns = true
	flow.settings.MaxAutoRunsPerDay = 1
	flow.AutoLaunch = func(casefile.Case, RunTarget, func() error) error {
		t.Fatal("the policy exceeded its rolling run limit")
		return nil
	}
	tracked := trackGitHubFlowFixture(t, flow)
	if tracked.RunID != "" || !tracked.AutoRun {
		t.Fatalf("the limit should leave a reviewable plan: %+v", tracked)
	}
	manual, err := flow.Approve(t.Context(), 42, tracked.Plan, func(_ casefile.Case, _ RunTarget, reserve func() error) error {
		return reserve()
	})
	if err != nil || manual.State != "launched" || manual.Unattended {
		t.Fatalf("the automatic limit blocked manual approval: %+v, %v", manual, err)
	}
}

func TestPolicyRespectsSavedProcedureAndStartupOptOut(t *testing.T) {
	source := newGitHubFlowSource()
	dir := filepath.Join(t.TempDir(), "cases")
	flow := openGitHubFlowFixture(t, source, dir)
	flow.settings.Policy.AutoRuns = true
	flow.AutoLaunch = func(casefile.Case, RunTarget, func() error) error {
		t.Fatal("a procedure set to review started automatically")
		return nil
	}
	version := flow.Procedure()
	version.Procedure.Policy.AutoRuns = false
	saved, err := flow.SaveProcedure(version.Procedure, version.Revision)
	if err != nil {
		t.Fatal(err)
	}
	trackGitHubFlowFixture(t, flow)
	saved.Procedure.Policy.AutoRuns = true
	if _, err := flow.SaveProcedure(saved.Procedure, saved.Revision); err != nil {
		t.Fatal(err)
	}
	if err := flow.Close(); err != nil {
		t.Fatal(err)
	}
	flow = openGitHubFlowFixture(t, source, dir)
	if err := flow.checkSavedProcedure(); err != nil {
		t.Fatal(err)
	}
	review, err := flow.Refresh(t.Context(), 42)
	if err != nil || review.State != "approval" || review.AutoRun || review.RunID != "" {
		t.Fatalf("a saved procedure bypassed the startup opt-out: %+v, %v", review, err)
	}
}
