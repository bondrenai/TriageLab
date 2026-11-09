package investigation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/kagent"
)

// TestLiveInvestigation never falls back to a mock. Opt-in requires a dedicated
// real KAgent/Substrate deployment and a working model credential in its gateway.
func TestLiveInvestigation(t *testing.T) {
	if os.Getenv("TRIAGELAB_INTEGRATION") != "1" {
		t.Skip("set TRIAGELAB_INTEGRATION=1 for real KAgent/Substrate/model integration")
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
	fixture, err := filepath.Abs(filepath.Join("..", "..", "examples", "config-bug"))
	if err != nil {
		t.Fatal(err)
	}
	description, err := os.ReadFile(filepath.Join(fixture, "issue.md"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := casefile.Ingest(string(description), fixture, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("TRIAGELAB_REPORT_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else {
		dir = filepath.Join(dir, c.ID)
	}
	investigator := Investigator{Client: client, Config: config, Secrets: []string{token}, Progress: func(s string) { t.Log(s) }}
	result, err := investigator.Run(context.Background(), c, dir)
	if err != nil {
		t.Fatalf("live investigation failed: %v; report %s", err, dir)
	}
	if result["outcome"] != "reproduced" {
		t.Fatalf("expected reproduced outcome; report %s", dir)
	}
	cleanup := obj(result["cleanup"])
	if cleanup["sandbox_deleted"] != true || cleanup["session_deleted"] != true {
		t.Fatalf("runtime cleanup incomplete; report %s", dir)
	}
	failure, correction := false, false
	for _, experiment := range result["experiments"].([]map[string]any) {
		exit, ok := integer(experiment["exit_code"])
		if !ok {
			continue
		}
		output := str(experiment["stdout"]) + str(experiment["stderr"])
		if exit != 0 && strings.Contains(strings.ToLower(output), "timeout") {
			failure = true
		}
		if exit == 0 && strings.Contains(output, "200") {
			correction = true
		}
	}
	if !failure || !correction {
		t.Fatalf("missing observed timeout failure or corrected success; report %s", dir)
	}
	root := obj(obj(result["analysis"])["root_cause"])
	if len(root["evidence_process_ids"].([]any)) == 0 {
		t.Fatal("diagnosis has no evidence references")
	}
	t.Logf("Real investigator, sandbox, failed/corrected experiments and cleanup verified: %s", dir)
}
