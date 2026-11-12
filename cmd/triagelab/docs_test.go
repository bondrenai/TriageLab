package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestGeneratedDocsMatchCommittedPages is the drift guard for the
// documentation site's generated reference pages: it renders the pages from
// the real flag registrations, the investigation configuration struct, and
// the committed example files, then compares byte-for-byte with what is
// checked in. Run `make docs-generate` after changing commands, flags, or
// configuration to refresh the pages.
func TestGeneratedDocsMatchCommittedPages(t *testing.T) {
	root := filepath.Join("..", "..")
	pages, err := generateReferencePages(root)
	if err != nil {
		t.Fatalf("generate reference pages: %v", err)
	}
	for _, name := range []string{"cli.md", "configuration.md", "configuration-examples.md"} {
		committed, err := os.ReadFile(filepath.Join(root, "docs-site", "src", "content", "docs", "reference", name))
		if err != nil {
			t.Fatalf("read committed page %s: %v (run: make docs-generate)", name, err)
		}
		if !bytes.Equal(committed, pages[name]) {
			t.Errorf("docs-site/src/content/docs/reference/%s is stale; run: make docs-generate", name)
		}
	}
}

// TestRunDocsCommandChecksDrift exercises the CLI form of the same guard.
func TestRunDocsCommandChecksDrift(t *testing.T) {
	root := filepath.Join("..", "..")
	dir := filepath.Join(root, "docs-site", "src", "content", "docs", "reference")
	var out, errout bytes.Buffer
	if code := runDocsCommand([]string{"--check", dir, "--root", root}, &out, &errout); code != 0 {
		t.Fatalf("docs --check exited %d: %s", code, errout.String())
	}
	if code := runDocsCommand([]string{}, &out, &errout); code != 2 {
		t.Fatalf("docs without a mode exited %d, want 2", code)
	}
}

// TestConfigFieldDocsRejectUnknownEntries pins the failure mode that forces
// documentation updates: an entry with no matching struct field must error.
func TestConfigFieldDocsRejectUnknownEntries(t *testing.T) {
	original, existed := configFieldDocs["no_such_field"]
	configFieldDocs["no_such_field"] = "stale"
	defer func() {
		delete(configFieldDocs, "no_such_field")
		if existed {
			configFieldDocs["no_such_field"] = original
		}
	}()
	if _, err := renderConfigurationPage(); err == nil {
		t.Fatal("renderConfigurationPage accepted a stale documentation entry")
	}
}

// TestMarkdownCellKeepsFlagsLiteral pins the rendering that keeps flags
// copyable: a bare --flag becomes a code span, an existing code span is left
// alone, and a hyphen pair inside a word is not a flag.
func TestMarkdownCellKeepsFlagsLiteral(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"initial password (prefer --password-stdin)", "initial password (prefer `--password-stdin`)"},
		{"--agent runs", "`--agent` runs"},
		{"pass `--config file.json` or --config", "pass `--config file.json` or `--config`"},
		{"a|b", "a\\|b"},
		{"well--known and plain text", "well--known and plain text"},
	} {
		if got := markdownCell(tc.in); got != tc.want {
			t.Errorf("markdownCell(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
