package investigation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/report"
)

func missingOrdersExpectation() corpusCase {
	return corpusCase{
		Name:              "missing-orders",
		Purpose:           "Genuine missing-orders demonstration: reproduce the 11-row timestamp-tie loss without lock retries and verify cursor_columns=created_at,id.",
		Outcomes:          []string{"reproduced"},
		Rationale:         "Replaying examples/missing-orders/repro with sync.json skips 11 orders (614 rows, 0 lock retries, 2493520 cents short); cursor_columns=created_at,id delivers all 625 rows.",
		RootCauseEvidence: true,
		Experiments: []corpusMatcher{
			{
				Label:           "baseline replay reproduces missing orders",
				CommandContains: "rowferry.py",
				Exit:            "nonzero",
				OutputContains:  []string{"614 rows", "0 lock retries", "2493520"},
			},
			{
				Label:           "composite cursor delivers all 625 orders",
				CommandContains: "rowferry.py",
				Exit:            "zero",
				OutputContains:  []string{"625 rows", "112396738"},
			},
		},
	}
}

func ingestMissingOrdersDemo(t *testing.T) casefile.Case {
	t.Helper()
	demoDir, err := filepath.Abs(filepath.Join("..", "..", "examples", "missing-orders"))
	if err != nil {
		t.Fatal(err)
	}
	description, err := os.ReadFile(filepath.Join(demoDir, "ticket.md"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := casefile.Ingest(string(description), filepath.Join(demoDir, "repro"), nil, nil)
	if err != nil {
		t.Fatalf("ingest missing-orders demo: %v", err)
	}
	return c
}

// Offline safeguard: ensures the preserved November 12 demonstration report and
// examples/missing-orders fixture remain consistent with the demo scorer.
func TestMissingOrdersScorerMatchesRecordedDemo(t *testing.T) {
	c := ingestMissingOrdersDemo(t)
	if len(c.Attachments) != 6 {
		t.Fatalf("expected 6 repro files in missing-orders demo, got %d", len(c.Attachments))
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "verification", "missing-orders-demo.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded map[string]any
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatal(err)
	}
	if misses := corpusScoreReport(missingOrdersExpectation(), recorded, nil); len(misses) != 0 {
		t.Fatalf("recorded missing-orders demo report missed expectations: %q", misses)
	}
}

// TestLiveMissingOrders runs examples/missing-orders end to end against a real
// kagent/Substrate deployment and model provider. It never falls back to a mock.
func TestLiveMissingOrders(t *testing.T) {
	if os.Getenv("TRIAGELAB_DEMO_INTEGRATION") != "1" {
		t.Skip("set TRIAGELAB_DEMO_INTEGRATION=1 for live missing-orders demonstration")
	}
	config, token := livePauseConfig(t)
	client := livePauseClient(t, config, token)
	root := os.Getenv("TRIAGELAB_REPORT_DIR")
	if root == "" {
		root = t.TempDir()
	} else if !filepath.IsAbs(root) {
		root = filepath.Join("..", "..", root)
	}
	var err error
	if root, err = filepath.Abs(root); err != nil {
		t.Fatal(err)
	}
	c := ingestMissingOrdersDemo(t)
	dir := filepath.Join(root, "demo-"+c.ID)

	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		stop := deadline.Add(-corpusCleanupReserve)
		if time.Until(stop) < corpusMinimumCaseTime {
			t.Fatal("go test deadline too close to start; raise -timeout")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, stop)
		defer cancel()
	}

	investigator := Investigator{
		Client:   client,
		Config:   config,
		Secrets:  []string{token},
		Progress: func(s string) { t.Log(s) },
	}
	result, runErr := investigator.Run(ctx, c, dir)
	canonical, encErr := corpusCanonical(result)
	if result != nil && (result.Status == StatusPaused || result.Status == StatusPausing) {
		closeCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		closed, closeErr := investigator.Close(closeCtx, dir)
		cancel()
		runErr = errors.Join(runErr, closeErr)
		if closed != nil && closed.Cleanup != nil && canonical != nil {
			canonical["cleanup"] = map[string]any{
				"sandbox_deleted": closed.Cleanup.SandboxDeleted,
				"session_deleted": closed.Cleanup.SessionDeleted,
			}
		}
	}
	if encErr != nil {
		t.Fatalf("encode report: %v (run error: %v)", encErr, runErr)
	}
	misses := corpusScoreReport(missingOrdersExpectation(), canonical, runErr)
	for index, miss := range misses {
		misses[index], _ = report.Redacted(miss, token)
	}
	if len(misses) > 0 {
		t.Fatalf("missing-orders live demo missed (report %s):\n- %s", filepath.Join(dir, "report.json"), strings.Join(misses, "\n- "))
	}
	t.Logf("Real missing-orders demo reproduced loss, verified composite cursor, and cleaned up: %s", filepath.Join(dir, "report.json"))
}
