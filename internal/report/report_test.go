package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bondrenai/TriageLab/internal/casefile"
)

func sample() map[string]any {
	return map[string]any{
		"schema_version": 1, "run_id": "run-1", "status": "completed", "outcome": "reproduced",
		"case": map[string]any{"description": "Request fails", "attachments": []any{map[string]any{"path": "app.py"}}},
		"experiments": []any{
			map[string]any{
				"process_id": "process-1", "command": []string{"python3", "app.py"},
				"state": "PROCESS_STATE_EXITED", "exit_code": 1,
				"stdout": "response latency 120ms", "stderr": "TimeoutError: deadline 5ms",
				"truncated": false, "timed_out": false,
			},
			map[string]any{
				"process_id": "process-2", "command": []string{"python3", "app.py", "--timeout-ms", "5000"},
				"state": "PROCESS_STATE_EXITED", "exit_code": 0,
				"stdout": "HTTP 200: healthy", "stderr": "",
			},
		},
		"analysis": map[string]any{
			"summary":            "The initial timeout was shorter than response latency",
			"root_cause":         map[string]any{"statement": "Possible units mismatch"},
			"reproduction_steps": []string{"python3 app.py", "python3 app.py --timeout-ms 5000"},
			"next_actions":       []string{"Audit configuration units"},
		},
		"errors": []string{}, "warnings": []string{},
		"cleanup": map[string]any{"sandbox_deleted": true, "session_deleted": true, "errors": []string{}},
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestRedactedReportsAppliedClasses(t *testing.T) {
	text, classes := Redacted("plain prose stays", "hunter2")
	if text != "plain prose stays" || len(classes) != 0 {
		t.Fatalf("clean text was changed: %q %v", text, classes)
	}
	text, classes = Redacted("send password=hunter2 as bearer abc123DEF456ghi789JKL", "hunter2")
	want := []string{"configured credential", "credential assignment", "bearer credential"}
	if strings.Join(classes, "|") != strings.Join(want, "|") {
		t.Fatalf("classes = %v, want %v", classes, want)
	}
	if strings.Contains(text, "hunter2") || strings.Contains(text, "abc123DEF456ghi789JKL") {
		t.Fatalf("credential text survived: %q", text)
	}
	if _, classes = Redacted("line\r\nbroken \x1b[31mred\x1b[0m and \x00nul", "hunter2"); strings.Join(classes, "|") != "terminal escape|control characters" {
		t.Fatalf("unsafe character classes = %v", classes)
	}
}

func TestPreservesFailureVerificationAndCleanup(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "run")
	data := sample()
	if err := Write(directory, data); err != nil {
		t.Fatal(err)
	}
	encoded := read(t, filepath.Join(directory, "report.json"))
	var saved map[string]any
	if err := json.Unmarshal([]byte(encoded), &saved); err != nil {
		t.Fatal(err)
	}
	experiments := saved["experiments"].([]any)
	if experiments[0].(map[string]any)["exit_code"] != float64(1) || experiments[1].(map[string]any)["exit_code"] != float64(0) {
		t.Fatal("failed or corrective experiment missing")
	}
	markdown := read(t, filepath.Join(directory, "report.md"))
	for _, expected := range []string{
		"deadline 5ms", "HTTP 200: healthy", "model-generated hypothesis", `"sandbox_deleted": true`,
		"Independent reproduction steps", "1. python3 app.py", "Audit configuration units",
	} {
		if !strings.Contains(markdown, expected) {
			t.Errorf("Markdown missing %q", expected)
		}
	}
	for name, permission := range map[string]os.FileMode{"": 0o700, "report.json": 0o600, "report.md": 0o600} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != permission {
			t.Fatalf("%s permission: %o", name, info.Mode().Perm())
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatal("temporary file left behind")
	}
}

func TestExistingReportsAreReplacedPrivately(t *testing.T) {
	directory := t.TempDir()
	name := filepath.Join(directory, "report.json")
	if err := os.WriteFile(name, []byte("old report"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(directory, sample()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || strings.Contains(read(t, name), "old report") {
		t.Fatal("old file retained")
	}
}

func TestNestedSecretsAndTerminalControlsAreRedacted(t *testing.T) {
	secret := "an-active-credential-with-an-unusual-shape"
	value := map[string]any{
		"Authorization": "Basic dXNlcjpwYXNzd29yZA==",
		"nested":        []any{map[string]any{"api_key": "secret-value", "text": "hello " + secret + "\x1b[31mred\x1b[0m"}},
		"command":       []string{"tool", "--password", "password-value"},
		"output":        "OPENAI_API_KEY=opaque-value\nBearer signed-token\rspoof\x00",
	}
	safe := Redact(value, secret).(map[string]any)
	encoded, err := json.Marshal(safe)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{secret, "secret-value", "password-value", "opaque-value", "signed-token"} {
		if strings.Contains(string(encoded), item) {
			t.Errorf("secret %q survived", item)
		}
	}
	if safe["nested"].([]any)[0].(map[string]any)["text"] != "hello [REDACTED]red" {
		t.Fatal("ANSI control survived")
	}
	if !strings.HasSuffix(safe["output"].(string), `\rspoof`) {
		t.Fatal("carriage return handling")
	}
	if value["nested"].([]any)[0].(map[string]any)["api_key"] != "secret-value" {
		t.Fatal("input mutated")
	}
}

func TestKnownTokenPrivateKeyAndRuntimeSecretRedaction(t *testing.T) {
	directory := t.TempDir()
	providerToken := "sk-proj-" + strings.Repeat("a", 40)
	privateMaterial := "-----BEGIN PRIVATE KEY-----\nprivate-material\n-----END PRIVATE KEY-----"
	runtimeCredential := "nonstandard-runtime-credential-value"
	data := sample()
	data["errors"] = []string{providerToken, privateMaterial, "backend echoed " + runtimeCredential}
	if err := Write(directory, data, runtimeCredential); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"report.json", "report.md"} {
		content := read(t, filepath.Join(directory, filename))
		for _, secret := range []string{providerToken, "private-material", runtimeCredential} {
			if strings.Contains(content, secret) {
				t.Errorf("%s contains secret", filename)
			}
		}
	}
}

func TestStructCaseHonorsJSONOmissionsAndRedaction(t *testing.T) {
	directory := t.TempDir()
	data := sample()
	data["case"] = casefile.Case{
		Description: "API_KEY=opaque-value",
		Attachments: []casefile.Attachment{{Path: "app.py", Content: []byte("private-file-body")}},
	}
	if err := Write(directory, data); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"report.json", "report.md"} {
		content := read(t, filepath.Join(directory, filename))
		if strings.Contains(content, "opaque-value") || strings.Contains(content, "private-file-body") {
			t.Fatal("struct content bypassed redaction")
		}
	}
}

func TestUntrustedMarkdownCannotEscapeFence(t *testing.T) {
	directory := t.TempDir()
	data := sample()
	attack := "```\n<script>alert(1)</script>\n[click](https://evil.invalid)\n````\n"
	data["experiments"].([]any)[0].(map[string]any)["stdout"] = attack
	if err := Write(directory, data); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, filepath.Join(directory, "report.md")), "`````text\n"+attack+"\n`````") {
		t.Fatal("untrusted output escaped fence")
	}
}

func TestInfrastructureFailureIsInconclusive(t *testing.T) {
	directory := t.TempDir()
	data := sample()
	data["status"], data["outcome"] = "error", "inconclusive"
	data["experiments"], data["analysis"] = []any{}, map[string]any{}
	data["errors"] = []string{"Cannot connect to kagent"}
	data["cleanup"] = map[string]any{"sandbox_deleted": false, "errors": []string{"Deletion unavailable; TTL remains"}}
	if err := Write(directory, data); err != nil {
		t.Fatal(err)
	}
	markdown := read(t, filepath.Join(directory, "report.md"))
	for _, text := range []string{"No experiment evidence was collected", "Cannot connect to kagent", "Deletion unavailable; TTL remains"} {
		if !strings.Contains(markdown, text) {
			t.Errorf("missing %q", text)
		}
	}
}

func TestSymlinkOutputRefusedAndInvalidDataDoesNotOverwrite(t *testing.T) {
	directory := t.TempDir()
	actual := filepath.Join(directory, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	if err := Write(link, sample()); err == nil {
		t.Fatal("symlink output accepted")
	}
	name := filepath.Join(actual, "report.json")
	if err := os.WriteFile(name, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(actual, map[string]any{"unsupported": make(chan int)}); err == nil {
		t.Fatal("unsupported report accepted")
	}
	if read(t, name) != "previous" {
		t.Fatal("invalid report destroyed previous record")
	}
}
