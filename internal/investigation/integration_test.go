package investigation

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
	"github.com/bondrenai/TriageLab/internal/report"
)

const (
	// corpusCleanupReserve keeps time after a case's run context ends for the
	// run's own bounded cleanup and the scorecard, before go test's -timeout panics.
	corpusCleanupReserve = 2 * time.Minute
	// corpusMinimumCaseTime is the least run time worth spending tokens on.
	corpusMinimumCaseTime = time.Minute
)

// TestLiveInvestigation scores the evaluation corpus in examples/corpus.json
// against a real deployment. It never falls back to a mock. Opt-in requires a
// dedicated real kagent/Substrate deployment and a working model credential in
// its gateway. Every case costs model tokens: TRIAGELAB_CASES=name,name selects
// a subset. All selected cases run; the test fails at the end if any missed.
func TestLiveInvestigation(t *testing.T) {
	if os.Getenv("TRIAGELAB_INTEGRATION") != "1" {
		t.Skip("set TRIAGELAB_INTEGRATION=1 for real kagent/Substrate/model integration")
	}
	corpus, err := loadCorpus(corpusManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	cases, err := corpus.selectCases(os.Getenv("TRIAGELAB_CASES"))
	if err != nil {
		t.Fatalf("TRIAGELAB_CASES: %v", err)
	}
	configPath := os.Getenv("TRIAGELAB_CONFIG")
	if configPath != "" && !filepath.IsAbs(configPath) {
		configPath = filepath.Join("..", "..", configPath)
	}
	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	token := os.Getenv(config.TokenEnv)
	if token == "" {
		t.Fatal("live integration requires a caller token")
	}
	client, err := kagent.New(config.Endpoint, config.UserID, token, time.Duration(config.RequestTimeout)*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Reports and the scorecard are kept only when TRIAGELAB_REPORT_DIR is set.
	// A relative directory is resolved from the repository root, like TRIAGELAB_CONFIG.
	root := os.Getenv("TRIAGELAB_REPORT_DIR")
	if root == "" {
		root = t.TempDir()
	} else if !filepath.IsAbs(root) {
		root = filepath.Join("..", "..", root)
	}
	if root, err = filepath.Abs(root); err != nil {
		t.Fatal(err)
	}
	scores := make([]corpusScore, 0, len(cases))
	for _, entry := range cases {
		t.Run(entry.Name, func(t *testing.T) {
			score := corpusScore{Case: entry.Name, Expected: entry.Outcomes, Status: "miss", Misses: []string{}}
			defer func() {
				if len(score.Misses) > 0 {
					where := score.Report
					if where == "" {
						where = "not written"
					}
					t.Errorf("%s %s (report %s):\n- %s", entry.Name, score.Status, where, strings.Join(score.Misses, "\n- "))
				}
				scores = append(scores, score)
			}()
			ctx := context.Background()
			if deadline, ok := t.Deadline(); ok {
				stop := deadline.Add(-corpusCleanupReserve)
				if time.Until(stop) < corpusMinimumCaseTime {
					score.Status = "not_run"
					score.Misses = append(score.Misses, "go test deadline too close to start; raise -timeout or select fewer cases")
					return
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, stop)
				defer cancel()
			}
			fixture, err := filepath.Abs(filepath.Join(corpusExamplesDir, entry.Name))
			if err != nil {
				score.Misses = append(score.Misses, err.Error())
				return
			}
			description, err := os.ReadFile(filepath.Join(fixture, "issue.md"))
			if err != nil {
				score.Misses = append(score.Misses, err.Error())
				return
			}
			c, err := casefile.Ingest(string(description), fixture, nil, nil)
			if err != nil {
				score.Misses = append(score.Misses, "ingest: "+err.Error())
				return
			}
			dir := filepath.Join(root, c.ID)
			score.Report = filepath.Join(dir, "report.json")
			investigator := Investigator{Client: client, Config: config, Secrets: []string{token}, Progress: func(s string) { t.Log(s) }}
			started := time.Now()
			result, runErr := investigator.Run(ctx, c, dir)
			score.DurationSeconds = math.Round(time.Since(started).Seconds()*10) / 10
			canonical, err := corpusCanonical(result)
			if err != nil {
				score.Misses = append(score.Misses, "encode report: "+err.Error())
				return
			}
			score.Outcome = corpusString(canonical["outcome"])
			score.RunStatus = corpusString(canonical["status"])
			score.Experiments = len(corpusList(canonical["experiments"]))
			for _, miss := range corpusScoreReport(entry, canonical, runErr) {
				miss, _ = report.Redacted(miss, token)
				score.Misses = append(score.Misses, miss)
			}
			if len(score.Misses) == 0 {
				score.Status = "pass"
				t.Logf("pass: real investigator, sandbox evidence and cleanup matched expectations; report %s", score.Report)
			}
		})
	}
	card := corpusScorecard{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339), Agent: config.Agent, Template: config.Template,
		KAgent: kagent.Version, Total: len(scores), Cases: scores,
	}
	for _, score := range scores {
		if score.Status == "pass" {
			card.Passed++
		}
	}
	t.Logf("corpus scorecard: %d of %d passed\n%s", card.Passed, card.Total, corpusTable(scores))
	path, err := writeCorpusScorecard(root, card, token)
	if err != nil {
		t.Errorf("write scorecard: %v", err)
	} else {
		t.Logf("scorecard: %s", path)
	}
	if card.Passed != card.Total {
		t.Errorf("%d of %d corpus cases missed", card.Total-card.Passed, card.Total)
	}
}
