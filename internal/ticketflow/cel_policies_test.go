package ticketflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

// The CEL demonstration hands an audience the procedure set stored under
// examples/cel-policies. Load that file exactly as saved and run the
// demonstration's decisions through the GitHub fixture, so the asset and the
// script in docs/cel-demo.md cannot drift apart silently.
func TestCELDemoProcedureSetRoutesConditionsAndAutoRun(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "cel-policies", "procedures.github.json"))
	if err != nil {
		t.Fatal(err)
	}
	var version ProcedureVersion
	if err := json.Unmarshal(data, &version); err != nil {
		t.Fatalf("demo procedure set is not valid JSON: %v", err)
	}
	source := newGitHubFlowSource()
	flow := openGitHubFlowFixture(t, source, filepath.Join(t.TempDir(), "cases"))
	flow.settings.Policy.AutoRuns = true
	// The workspace resolves each procedure's agent and sandbox against what
	// is deployed; the demonstration deploys these two pairs.
	flow.ResolveTarget = func(_ context.Context, agent, sandbox, _ string) (RunTarget, error) {
		if (agent == "triagelab-investigator" && sandbox == "triagelab-python") ||
			(agent == "data-loss-investigator" && sandbox == "triagelab-python-replay") {
			return RunTarget{Agent: agent, Sandbox: sandbox}, nil
		}
		return RunTarget{}, ErrBlocked
	}
	if err := flow.validateProcedureSet(version); err != nil {
		t.Fatalf("demo procedure set does not validate: %v", err)
	}
	if _, err := flow.SaveProcedures(ProcedureVersion{Procedure: version.Procedure,
		Additional: version.Additional, Revision: flow.Procedure().Revision}); err != nil {
		t.Fatal(err)
	}

	// The final procedure keeps the plain label check for ordinary timeouts.
	source.snapshot.Ticket.Tags = []string{"triagelab"}
	preview, err := flow.Preview(t.Context(), 42)
	if err != nil || preview.MatchedTag != "triagelab" || preview.State != "approval" {
		t.Fatalf("label fallback: %+v %v", preview, err)
	}

	// A title prefix routes the issue with no matching label at all.
	source.snapshot.Ticket.Subject = "[edge] Health checks time out at the edge gateway"
	source.snapshot.Ticket.Tags = nil
	preview, err = flow.Preview(t.Context(), 42)
	if err != nil || preview.MatchedTag != "edge" || preview.State != "approval" || preview.Sandbox != "triagelab-python" {
		t.Fatalf("subject routing: %+v %v", preview, err)
	}

	// The edge issue had no labels, so it keeps the general investigator.
	if preview.Agent != "triagelab-investigator" {
		t.Fatalf("edge agent: %+v", preview)
	}

	// The data-loss procedure holds a labelled issue for manual approval and
	// starts the investigation itself once the brake label is gone, after a
	// fresh read of the issue.
	dataLoss, ok := version.byTag("data-loss")
	if !ok {
		t.Fatal("data-loss procedure missing")
	}
	launches := 0
	flow.AutoLaunch = func(input casefile.Case, _ RunTarget, reserve func() error) error {
		if input.Source["auto_run_condition"] != dataLoss.Policy.AutoRunCondition {
			t.Errorf("condition missing from provenance: %v", input.Source["auto_run_condition"])
		}
		if err := reserve(); err != nil {
			return err
		}
		launches++
		return nil
	}
	// The same report labelled data-loss is matched by a different procedure,
	// which supplies its own investigator and sandbox template.
	source.snapshot.Ticket.Subject = "Nightly order sync is silently dropping orders"
	source.snapshot.Ticket.Tags = []string{"data-loss", "needs-review"}
	source.snapshot.Comments = []comment{{ID: 500, Author: 10, Public: true,
		URL:  source.snapshot.Ticket.URL + "#issuecomment-500",
		Body: "Attached: the sync tool and its configuration.",
		Files: []attachment{
			{Name: "rowferry.py.txt", Size: -1, URL: "https://github.com/user-attachments/files/501/rowferry.py.txt"},
			{Name: "sync.json", Size: -1, URL: "https://github.com/user-attachments/files/502/sync.json"},
		}}}
	for name, body := range map[string]string{
		"https://github.com/user-attachments/files/501/rowferry.py.txt":       "print('sync')\n",
		"https://github.com/user-attachments/files/502/sync.json":             "{\"page_size\":100}\n",
		"https://github.com/user-attachments/files/503/schema.sql.txt":        "CREATE TABLE orders;\n",
		"https://github.com/user-attachments/files/504/orders_export.csv.txt": "418306,2026-10-28\n",
	} {
		source.contents[name] = []byte(body)
	}
	preview, err = flow.Preview(t.Context(), 42)
	if err != nil || preview.MatchedTag != "data-loss" || preview.State != "approval" || preview.AutoRun ||
		!strings.Contains(preview.Reason, "did not match") ||
		preview.Agent != "data-loss-investigator" || preview.Sandbox != "triagelab-python-replay" {
		t.Fatalf("held automatic start: %+v %v", preview, err)
	}

	// One label makes the customer's data required evidence; the requester's
	// reply supplies it without touching the procedure.
	source.snapshot.Ticket.Tags = []string{"data-loss", "needs-review", "needs-replay"}
	preview, err = flow.Preview(t.Context(), 42)
	if err != nil || preview.MatchedTag != "data-loss" || preview.State != "review" ||
		len(preview.Missing) != 1 || !strings.Contains(preview.Missing[0], "replay") {
		t.Fatalf("conditional requirement: %+v %v", preview, err)
	}
	source.snapshot.Comments = append(source.snapshot.Comments, comment{ID: 501, Author: 10, Public: true,
		URL:  source.snapshot.Ticket.URL + "#issuecomment-501",
		Body: "The schema and the export of the affected night.",
		Files: []attachment{
			{Name: "schema.sql.txt", Size: -1, URL: "https://github.com/user-attachments/files/503/schema.sql.txt"},
			{Name: "orders_export.csv.txt", Size: -1, URL: "https://github.com/user-attachments/files/504/orders_export.csv.txt"},
		}})
	preview, err = flow.Preview(t.Context(), 42)
	if err != nil || preview.State != "approval" || preview.AutoRun || len(preview.Files) != 4 {
		t.Fatalf("replay data supplied: %+v %v", preview, err)
	}
	tracked, err := flow.Track(t.Context(), 42, preview.Revision)
	if err != nil || tracked.AutoRun || launches != 0 {
		t.Fatalf("brake label held: %+v %v", tracked, err)
	}
	source.snapshot.Ticket.Tags = []string{"data-loss", "needs-replay"}
	flow.poll(t.Context(), 0)
	if launches != 1 {
		t.Fatalf("automatic launch after fresh read: %d", launches)
	}
	flow.poll(t.Context(), 0)
	if launches != 1 {
		t.Fatalf("repeated launch: %d", launches)
	}
}
