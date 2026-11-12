package ticketflow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

func TestAutoRunCELAttributesAndFailures(t *testing.T) {
	input := ticket{ID: 42, Requester: 10, Subject: "Timeout", Status: "open", Priority: "high", Tags: []string{"reproduce"}, Fields: []field{{ID: 84, Value: "python"}}}
	for _, tc := range []struct {
		expression      string
		allowed, failed bool
	}{
		{`ticket.priority == "high" && "reproduce" in ticket.tags`, true, false},
		{`ticket.source == "zendesk" && ticket.id == 42 && ticket.requester_id == 10`, true, false},
		{`"84" in ticket.custom_fields && ticket.custom_fields["84"] == "python"`, true, false},
		{`ticket.subject.startsWith("Time") && ticket.status == "open"`, true, false},
		{`ticket.priority == "low"`, false, false},
		{`"missing" in ticket.custom_fields && ticket.custom_fields["missing"] == true`, false, false},
		{`ticket.custom_fields["missing"] == true`, false, true},
		{`ticket.unknown == true`, false, true},
		{`ticket.id / 0 > 0`, false, true},
		{`ticket.tags.exists(tag, tag == "reproduce")`, true, false},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			allowed, err := evaluateTicketCondition(t.Context(), tc.expression, "zendesk", input)
			if allowed != tc.allowed || (err != nil) != tc.failed {
				t.Fatalf("allowed=%v, err=%v", allowed, err)
			}
		})
	}
	for _, expression := range []string{`ticket.status ==`, `unknown == true`, `42`, `ticket`, " ", strings.Repeat("x", 4097)} {
		if _, err := compileTicketCondition(expression); err == nil {
			t.Errorf("accepted invalid condition %q", expression)
		}
	}
	tags := make([]string, 200)
	input.Tags = tags
	if allowed, err := evaluateTicketCondition(t.Context(), `ticket.tags.all(a, ticket.tags.all(b, a == b))`, "zendesk", input); err == nil || allowed {
		t.Fatal("unbounded CEL evaluation succeeded")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if allowed, err := evaluateTicketCondition(ctx, `ticket.tags.all(a, a == "")`, "zendesk", input); err == nil || allowed {
		t.Fatal("canceled evaluation succeeded")
	}
}

func TestCELPolicyRechecksAttributesBeforeAutomaticLaunch(t *testing.T) {
	source := newGitHubFlowSource()
	flow := openGitHubFlowFixture(t, source, filepath.Join(t.TempDir(), "cases"))
	flow.settings.Policy.AutoRuns = true
	flow.settings.Policy.AutoRunCondition = `"automatic" in ticket.tags`
	launches := 0
	flow.AutoLaunch = func(input casefile.Case, _ RunTarget, reserve func() error) error {
		if input.Source["auto_run_condition"] != flow.settings.Policy.AutoRunCondition {
			t.Fatal("condition missing from provenance")
		}
		if err := reserve(); err != nil {
			return err
		}
		launches++
		return nil
	}
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || preview.AutoRun {
		t.Fatalf("nonmatching preview: %+v %v", preview, err)
	}
	tracked, err := flow.Track(t.Context(), 42, preview.Revision)
	if err != nil || tracked.RunID != "" || launches != 0 {
		t.Fatalf("nonmatching track: %+v %v", tracked, err)
	}
	source.snapshot.Ticket.Tags = append(source.snapshot.Ticket.Tags, "automatic")
	preview, err = flow.Preview(t.Context(), 42)
	if err != nil || !preview.AutoRun || launches != 0 {
		t.Fatalf("matching preview: %+v %v", preview, err)
	}
	source.snapshot.Ticket.Tags = []string{"reproduce"}
	flow.poll(t.Context(), 0)
	if launches != 0 {
		t.Fatal("stale matching attributes authorized launch")
	}
	source.snapshot.Ticket.Tags = append(source.snapshot.Ticket.Tags, "automatic")
	flow.poll(t.Context(), 0)
	if launches != 1 {
		t.Fatalf("matching attributes did not launch: %d", launches)
	}
}

func TestInvalidCELRejectedByConfigurationAndProcedure(t *testing.T) {
	config := Config{Source: "github", Repository: "owner/repo", Policy: Policy{Tag: "reproduce", AutoRunCondition: `ticket.id`}}
	if err := config.validate(); err == nil {
		t.Fatal("configuration accepted nonboolean CEL")
	}
	flow := openGitHubFlowFixture(t, newGitHubFlowSource(), filepath.Join(t.TempDir(), "cases"))
	version := flow.Procedure()
	version.Procedure.Policy.AutoRunCondition = `ticket.status ==`
	if _, err := flow.SaveProcedure(version.Procedure, version.Revision); !errors.Is(err, ErrInvalidProcedure) {
		t.Fatalf("save: %v", err)
	}
	if _, err := flow.PreviewProcedure(t.Context(), 42, version.Procedure); !errors.Is(err, ErrInvalidProcedure) {
		t.Fatalf("preview: %v", err)
	}
}

func TestCELRuntimeFailureStillAllowsManualApproval(t *testing.T) {
	flow := openGitHubFlowFixture(t, newGitHubFlowSource(), filepath.Join(t.TempDir(), "cases"))
	flow.settings.Policy.AutoRuns = true
	flow.settings.Policy.AutoRunCondition = `ticket.custom_fields["absent"] == true`
	flow.AutoLaunch = func(casefile.Case, RunTarget, func() error) error {
		t.Fatal("a CEL error authorized an automatic run")
		return nil
	}
	ready := trackGitHubFlowFixture(t, flow)
	if ready.AutoRun || !strings.Contains(ready.Reason, "could not be evaluated") {
		t.Fatalf("runtime failure: %+v", ready)
	}
	launched, err := flow.Approve(t.Context(), 42, ready.Plan, func(_ casefile.Case, _ RunTarget, reserve func() error) error { return reserve() })
	if err != nil || launched.RunID == "" || launched.Unattended {
		t.Fatalf("manual approval: %+v %v", launched, err)
	}
}

func TestCELSelectsRunTargetAndConditionalEvidence(t *testing.T) {
	source := newGitHubFlowSource()
	flow := openGitHubFlowFixture(t, source, filepath.Join(t.TempDir(), "cases"))
	flow.ResolveTarget = func(_ context.Context, agent, sandbox, _ string) (RunTarget, error) {
		return RunTarget{Agent: agent, Sandbox: sandbox}, nil
	}
	version := flow.Procedure()
	special := cloneProcedure(version.Procedure)
	special.Policy.Tag = "network-procedure"
	special.Policy.MatchCondition = `"network" in ticket.tags`
	special.Sandbox = "triagelab-network"
	special.Evidence = append(special.Evidence, EvidenceRule{
		Kind: "field", Match: "Trace ID", Required: true, Question: "Provide a trace ID.",
		RequiredWhen: `"needs-trace" in ticket.tags`,
	})
	version.Additional = []Procedure{special}
	if _, err := flow.SaveProcedures(version); err != nil {
		t.Fatal(err)
	}
	base, err := flow.Preview(t.Context(), 42)
	if err != nil || base.Sandbox == special.Sandbox || base.State != "approval" {
		t.Fatalf("ordinary ticket: %+v, %v", base, err)
	}
	source.snapshot.Ticket.Tags = append(source.snapshot.Ticket.Tags, "network")
	network, err := flow.Preview(t.Context(), 42)
	if err != nil || network.Sandbox != special.Sandbox || network.State != "approval" {
		t.Fatalf("network ticket: %+v, %v", network, err)
	}
	source.snapshot.Ticket.Tags = append(source.snapshot.Ticket.Tags, "needs-trace")
	missing, err := flow.Preview(t.Context(), 42)
	if err != nil || missing.Sandbox != special.Sandbox || missing.State != "review" ||
		len(missing.Missing) != 1 || missing.Missing[0] != "Provide a trace ID." || missing.Plan != "" {
		t.Fatalf("conditional requirement: %+v, %v", missing, err)
	}
}

func TestCELRoutingErrorDoesNotFallThrough(t *testing.T) {
	source := newGitHubFlowSource()
	flow := openGitHubFlowFixture(t, source, filepath.Join(t.TempDir(), "cases"))
	version := flow.Procedure()
	broken := cloneProcedure(version.Procedure)
	broken.Policy.Tag = "broken"
	broken.Policy.MatchCondition = `ticket.custom_fields["missing"] == true`
	version.Additional = []Procedure{broken}
	if _, err := flow.SaveProcedures(version); err != nil {
		t.Fatal(err)
	}
	result, err := flow.Preview(t.Context(), 42)
	if err != nil || result.State != "review" || result.Plan != "" {
		t.Fatalf("routing error: %+v, %v", result, err)
	}
}
