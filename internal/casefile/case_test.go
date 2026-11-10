package casefile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	for _, test := range []struct {
		kind     string
		location bool
	}{
		{"file", false},
		{"directory", false},
		{"link under a linked ancestor", false},
		{"root", true},
		{"root with trailing slash", true},
		{"traversal", true},
	} {
		t.Run(test.kind, func(t *testing.T) {
			directory := temporary(t)
			repo := filepath.Join(directory, "repo")
			outside := filepath.Join(directory, "outside")
			put(t, filepath.Join(repo, "app"), []byte("safe"))
			put(t, filepath.Join(outside, "private"), []byte("private data"))
			var err error
			switch test.kind {
			case "file":
				err = os.Symlink(filepath.Join(outside, "private"), filepath.Join(repo, "link"))
			case "directory":
				err = os.Symlink(outside, filepath.Join(repo, "linked-directory"))
			case "link under a linked ancestor":
				// Resolving the named location must not relax checks below it.
				if err = os.Symlink(directory, filepath.Join(directory, "alias")); err == nil {
					err = os.Symlink(outside, filepath.Join(repo, "linked-directory"))
				}
				repo = filepath.Join(directory, "alias", "repo")
			case "root":
				repo = filepath.Join(directory, "linked-root")
				err = os.Symlink(outside, repo)
			case "root with trailing slash":
				// A trailing slash makes lstat follow the link; it must still be refused.
				err = os.Symlink(outside, filepath.Join(directory, "linked-root"))
				repo = filepath.Join(directory, "linked-root") + "/"
			case "traversal":
				repo = repo + "/missing/.."
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = Ingest("bug", repo, nil, nil)
			if err == nil {
				t.Fatal("unsafe path accepted")
			}
			if errors.Is(err, ErrLocation) != test.location {
				t.Fatalf("location classification %v for %v", !test.location, err)
			}
		})
	}
}

// The operator names a location; links in that path are resolved once.
// This changes the earlier policy, which refused a linked ancestor.
func TestNamedLocationMayCrossSymlinks(t *testing.T) {
	t.Run("repository ancestor", func(t *testing.T) {
		directory := temporary(t)
		put(t, filepath.Join(directory, "repo", "app.py"), []byte("pass"))
		if err := os.Symlink(directory, filepath.Join(directory, "alias")); err != nil {
			t.Fatal(err)
		}
		result, err := Ingest("bug", filepath.Join(directory, "alias", "repo"), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Attachments) != 1 || result.Attachments[0].Path != "app.py" ||
			result.Environment["repository_name"] != "repo" {
			t.Fatalf("unexpected case: %#v", result)
		}
	})
	t.Run("unresolved temporary directory", func(t *testing.T) {
		// On macOS this path crosses the /var link.
		directory := t.TempDir()
		put(t, filepath.Join(directory, "app.py"), []byte("pass"))
		if _, err := Ingest("bug", directory, nil, nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("working directory entered through a link", func(t *testing.T) {
		// The named input is ".", a real directory, even though PWD names a link.
		directory := temporary(t)
		put(t, filepath.Join(directory, "repo", "app.py"), []byte("pass"))
		link := filepath.Join(directory, "current")
		if err := os.Symlink(filepath.Join(directory, "repo"), link); err != nil {
			t.Fatal(err)
		}
		t.Chdir(link)
		result, err := Ingest("bug", ".", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Attachments) != 1 || result.Attachments[0].Path != "app.py" {
			t.Fatalf("unexpected attachments: %#v", result.Attachments)
		}
	})
	for _, group := range []string{"files", "logs"} {
		t.Run(group+" parent", func(t *testing.T) {
			directory := temporary(t)
			put(t, filepath.Join(directory, "evidence", "failure.log"), []byte("connection failed"))
			if err := os.Symlink(filepath.Join(directory, "evidence"), filepath.Join(directory, "alias")); err != nil {
				t.Fatal(err)
			}
			named := []string{filepath.Join(directory, "alias", "failure.log")}
			files, logs := named, []string(nil)
			if group == "logs" {
				files, logs = nil, named
			}
			result, err := Ingest("bug", "", files, logs)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Attachments) != 1 || result.Attachments[0].Path != group+"/failure.log" {
				t.Fatalf("unexpected attachments: %#v", result.Attachments)
			}
		})
	}
}

func TestNamedFileMustNotBeSymlink(t *testing.T) {
	for _, group := range []string{"files", "logs"} {
		for _, suffix := range []string{"", "/"} {
			name := group
			if suffix != "" {
				name += " with trailing slash"
			}
			t.Run(name, func(t *testing.T) {
				directory := temporary(t)
				put(t, filepath.Join(directory, "outside", "private"), []byte("private data"))
				link := filepath.Join(directory, "failure.log")
				if err := os.Symlink(filepath.Join(directory, "outside", "private"), link); err != nil {
					t.Fatal(err)
				}
				named := []string{link + suffix}
				files, logs := named, []string(nil)
				if group == "logs" {
					files, logs = nil, named
				}
				_, err := Ingest("bug", "", files, logs)
				if err == nil {
					t.Fatal("accepted a linked file")
				}
				if errors.Is(err, ErrLocation) {
					t.Fatalf("a linked file is material, not a location problem: %v", err)
				}
			})
		}
	}
	t.Run("traversal", func(t *testing.T) {
		directory := temporary(t)
		put(t, filepath.Join(directory, "failure.log"), []byte("connection failed"))
		_, err := Ingest("bug", "", []string{directory + "/missing/../failure.log"}, nil)
		if !errors.Is(err, ErrLocation) {
			t.Fatalf("traversal was not refused as a location problem: %v", err)
		}
	})
}

func TestLocationErrorsAreDistinctFromRefusals(t *testing.T) {
	directory := temporary(t)
	if _, err := Ingest("bug", filepath.Join(directory, "missing"), nil, nil); !errors.Is(err, ErrLocation) {
		t.Fatalf("missing repository: %v", err)
	}
	if _, err := Ingest("bug", "", []string{filepath.Join(directory, "missing", "x.log")}, nil); !errors.Is(err, ErrLocation) {
		t.Fatalf("missing file directory: %v", err)
	}
	put(t, filepath.Join(directory, "repo", "file"), nil)
	if _, err := Ingest("bug", filepath.Join(directory, "repo", "file"), nil, nil); !errors.Is(err, ErrLocation) {
		t.Fatalf("repository that is a file: %v", err)
	}
	put(t, filepath.Join(directory, "repo", "capture.log"), []byte("key=AKIA"+strings.Repeat("A", 16)))
	_, err := Ingest("bug", filepath.Join(directory, "repo"), nil, nil)
	if err == nil || errors.Is(err, ErrLocation) {
		t.Fatalf("content refusal classified as a location problem: %v", err)
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
		"token=github_pat_" + strings.Repeat("A", 40),
		"token=xoxb-" + strings.Repeat("A", 24),
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
