package casefile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func temporary(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func put(t *testing.T, name string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIngestInventory(t *testing.T) {
	directory := temporary(t)
	repo := filepath.Join(directory, "repo")
	put(t, filepath.Join(repo, "src", "app.py"), []byte("print('hello')\n"))
	log := filepath.Join(directory, "failure.log")
	put(t, log, []byte("connection failed"))
	binary := filepath.Join(directory, "trace.bin")
	put(t, binary, []byte{0, 1, 255})
	result, err := Ingest("  Cannot connect  ", repo, []string{binary}, []string{log})
	if err != nil {
		t.Fatal(err)
	}
	if result.Description != "Cannot connect" || result.ID == "" {
		t.Fatalf("bad case: %#v", result)
	}
	want := []string{"src/app.py", "files/trace.bin", "logs/failure.log"}
	for index, attachment := range result.Attachments {
		if attachment.Path != want[index] {
			t.Fatalf("path %q, want %q", attachment.Path, want[index])
		}
	}
	sum := sha256.Sum256([]byte("print('hello')\n"))
	if result.Attachments[0].SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("incorrect content digest")
	}
	if result.Attachments[0].SizeBytes != 15 {
		t.Fatal("incorrect manifest size")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "print('hello')") || strings.Contains(string(encoded), directory) {
		t.Fatal("report contains file bodies or host paths")
	}
	if len(result.Evidence) != 1 || result.Evidence[0] != "User-supplied log: logs/failure.log" {
		t.Fatal("missing log provenance")
	}
}

func TestDescriptionValidation(t *testing.T) {
	for _, description := range []string{"", " \n ", strings.Repeat("x", MaxDescriptionBytes+1)} {
		if _, err := Ingest(description, "", nil, nil); err == nil {
			t.Fatal("accepted invalid description")
		}
	}
}

func TestGeneratedDirectoriesExcluded(t *testing.T) {
	directory := temporary(t)
	for name := range excludedDirectories {
		put(t, filepath.Join(directory, name, ".env"), []byte("do not copy"))
	}
	put(t, filepath.Join(directory, "app.py"), []byte("pass"))
	result, err := Ingest("bug", directory, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Attachments) != 1 || result.Attachments[0].Path != "app.py" {
		t.Fatal("copied excluded files")
	}
}

func TestSymlinksAndParentTraversalRefused(t *testing.T) {
	for _, kind := range []string{"file", "directory", "root", "ancestor", "traversal"} {
		t.Run(kind, func(t *testing.T) {
			directory := temporary(t)
			repo := filepath.Join(directory, "repo")
			outside := filepath.Join(directory, "outside")
			put(t, filepath.Join(repo, "app"), []byte("safe"))
			put(t, filepath.Join(outside, "private"), []byte("private data"))
			var err error
			switch kind {
			case "file":
				err = os.Symlink(filepath.Join(outside, "private"), filepath.Join(repo, "link"))
			case "directory":
				err = os.Symlink(outside, filepath.Join(repo, "linked-directory"))
			case "root":
				repo = filepath.Join(directory, "linked-root")
				err = os.Symlink(outside, repo)
			case "ancestor":
				err = os.Symlink(directory, filepath.Join(directory, "alias"))
				repo = filepath.Join(directory, "alias", "repo")
			case "traversal":
				repo = repo + "/missing/.."
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Ingest("bug", repo, nil, nil); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
}

func TestFIFORefusedWithoutOpening(t *testing.T) {
	pipe := filepath.Join(temporary(t), "pipe")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Ingest("bug", "", []string{pipe}, nil); err == nil {
		t.Fatal("accepted FIFO")
	}
}

func TestCredentialInputsRefused(t *testing.T) {
	for _, name := range []string{".env", ".env.production", "id_rsa", "credentials.json", "client.key", "service-account.json"} {
		t.Run(name, func(t *testing.T) {
			directory := temporary(t)
			put(t, filepath.Join(directory, name), []byte("do not transfer"))
			if _, err := Ingest("bug", directory, nil, nil); err == nil {
				t.Fatal("accepted credential file")
			}
		})
	}
	t.Run("credential directory", func(t *testing.T) {
		directory := temporary(t)
		if err := os.Mkdir(filepath.Join(directory, ".aws"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Ingest("bug", directory, nil, nil); err == nil {
			t.Fatal("accepted credential directory")
		}
	})
	for _, content := range []string{
		"-----BEGIN RSA PRIVATE KEY-----\nfake-test-value",
		"token=ghp_" + strings.Repeat("A", 36),
		"key=AKIA" + strings.Repeat("A", 16),
		"token=sk-proj-" + strings.Repeat("A", 40),
	} {
		directory := temporary(t)
		put(t, filepath.Join(directory, "capture.log"), []byte(content))
		_, err := Ingest("bug", directory, nil, nil)
		if err == nil {
			t.Fatal("accepted secret content")
		}
		if strings.Contains(err.Error(), content) {
			t.Fatal("error exposed secret")
		}
		if _, err := Ingest(content, "", nil, nil); err == nil {
			t.Fatal("accepted secret description")
		}
	}
}

func TestNonsecretEnvExampleAccepted(t *testing.T) {
	directory := temporary(t)
	put(t, filepath.Join(directory, ".env.example"), []byte("OPENAI_API_KEY=replace-me"))
	if _, err := Ingest("bug", directory, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestInputLimits(t *testing.T) {
	t.Run("file size", func(t *testing.T) {
		directory := temporary(t)
		put(t, filepath.Join(directory, "large"), make([]byte, MaxFileBytes+1))
		if _, err := Ingest("bug", directory, nil, nil); err == nil {
			t.Fatal("accepted oversized file")
		}
	})
	t.Run("total size", func(t *testing.T) {
		directory := temporary(t)
		for i := 0; i < 5; i++ {
			put(t, filepath.Join(directory, fmt.Sprint(i)), make([]byte, MaxFileBytes))
		}
		if _, err := Ingest("bug", directory, nil, nil); err == nil {
			t.Fatal("accepted oversized case")
		}
	})
	t.Run("file count", func(t *testing.T) {
		directory := temporary(t)
		for i := 0; i < MaxFiles+1; i++ {
			put(t, filepath.Join(directory, fmt.Sprint(i)), nil)
		}
		if _, err := Ingest("bug", directory, nil, nil); err == nil {
			t.Fatal("accepted excessive files")
		}
	})
	t.Run("depth", func(t *testing.T) {
		directory := temporary(t)
		name := directory + strings.Repeat("/a", maxDirectoryDepth+3)
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := Ingest("bug", directory, nil, nil); err == nil {
			t.Fatal("accepted excessive depth")
		}
	})
}

func TestDuplicatesAndUnsafeNamesRefused(t *testing.T) {
	directory := temporary(t)
	file := filepath.Join(directory, "log.txt")
	put(t, file, nil)
	if _, err := Ingest("bug", "", []string{file, file}, nil); err == nil {
		t.Fatal("accepted duplicate destination")
	}
	for _, name := range []string{"line\nfeed.txt", `..\outside.txt`} {
		file := filepath.Join(directory, name)
		put(t, file, nil)
		if _, err := Ingest("bug", "", []string{file}, nil); err == nil {
			t.Fatal("accepted unsafe name")
		}
	}
}

func TestDeterministicFixture(t *testing.T) {
	// This verifies only the fixture, not KAgent or sandbox integration.
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the fixture-only check")
	}
	fixture := filepath.Join("..", "..", "examples", "config-bug")
	for _, test := range []struct {
		args   []string
		exit   int
		output string
	}{
		{[]string{"app.py"}, 1, "deadline 5ms"},
		{[]string{"app.py", "--timeout-ms", "5000"}, 0, "HTTP 200: healthy"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		command := exec.CommandContext(ctx, python, test.args...)
		command.Dir = fixture
		output, err := command.CombinedOutput()
		cancel()
		if err != nil && command.ProcessState == nil {
			t.Fatal(err)
		}
		if command.ProcessState.ExitCode() != test.exit || !strings.Contains(string(output), test.output) {
			t.Fatalf("fixture output: %s (error: %v)", output, err)
		}
		if !strings.Contains(string(output), "downstream_response_ms=120") {
			t.Fatal("verification changed latency")
		}
	}
}
