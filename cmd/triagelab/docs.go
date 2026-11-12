package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/bondrenai/TriageLab/internal/investigation"
)

// The docs command regenerates the documentation site's technical reference
// pages from the real flag registrations, the investigation configuration
// struct, and the committed example files. `make check` runs a golden-file
// test over the same output, so the pages cannot drift from the code.
const docsGeneratedNote = "This page is generated from the CLI source by `triagelab docs`; run `make docs-generate` after changing commands, flags, or configuration. Do not edit it by hand."

type commandDoc struct {
	name    string
	usage   string
	purpose string
	flags   func() *flag.FlagSet
}

func newFlagSet[T any](name string, register func(*flag.FlagSet) T) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	register(flags)
	return flags
}

func cliCommandDocs() []commandDoc {
	runCommand := func(command string) func() *flag.FlagSet {
		return func() *flag.FlagSet {
			return newFlagSet("triagelab "+command, func(f *flag.FlagSet) *runOptions { return registerRunFlags(f, command) })
		}
	}
	return []commandDoc{
		{name: "investigate", usage: "triagelab investigate [options]", purpose: "Run one investigation: collect the bounded case, create a standalone sandbox, run the kagent investigator, and write JSON and Markdown reports.", flags: runCommand("investigate")},
		{name: "pause", usage: "triagelab pause --report <report.json> [options]", purpose: "Ask an active investigation to settle its turn, capture evidence, and suspend its sandbox. The report confirms when it is safely paused. Requires the TriageLab kagent controller fork; release controllers keep running without pausing.", flags: runCommand("pause")},
		{name: "resume", usage: "triagelab resume --report <report.json> --description <new-information> [options]", purpose: "Continue a paused manual investigation in its saved sandbox and session with a fresh bounded turn. Saved settings and tools are reused; ticket continuations require review in the signed-in workspace.", flags: runCommand("resume")},
		{name: "close", usage: "triagelab close --report <report.json> [options]", purpose: "Release an investigation's retained session and sandbox using its private lifecycle record. Keep the reports and previous rounds.", flags: runCommand("close")},
		{name: "serve", usage: "triagelab serve [options]", purpose: "Serve the authenticated browser workspace on loopback, sharing the CLI's investigation workflow and report directory.", flags: runCommand("serve")},
		{name: "doctor", usage: "triagelab doctor [options]", purpose: "Check that the kagent sandbox API is reachable with the configured credentials, and report whether durable pause retention is available or a release controller runs without it. A live investigation is still needed to verify runtime and model access.", flags: runCommand("doctor")},
		{name: "cleanup", usage: "triagelab cleanup [options]", purpose: "Finish sandbox and session cleanup from a saved report; the configuration must match the report's endpoint, user, and namespace.", flags: runCommand("cleanup")},
		{name: "manifest", usage: "triagelab manifest [options]", purpose: "Render the kagent workspace resources (investigator agent, sandbox template, tools service) as Kubernetes JSON.", flags: func() *flag.FlagSet {
			return newFlagSet("triagelab manifest", registerManifestFlags)
		}},
		{name: "agent-manifest", usage: "triagelab agent-manifest [options]", purpose: "Render a bundled agent definition — agent plus optional model and MCP server resources — as Kubernetes JSON for review and deployment.", flags: func() *flag.FlagSet {
			return newFlagSet("triagelab agent-manifest", registerAgentManifestFlags)
		}},
		{name: "user", usage: "triagelab user <add|list|remove|set-role|set-password> [options]", purpose: "Manage local sign-in accounts without contacting kagent. The first account defaults to admin; later accounts default to viewer.", flags: func() *flag.FlagSet {
			return newFlagSet("triagelab user", registerUserFlags)
		}},
	}
}

// exitCodeDocs mirrors the exit-code switches in run(); keep both in step.
var exitCodeDocs = []struct {
	code    int
	meaning string
}{
	{0, "Command succeeded; an investigation reproduced the reported symptom, paused, or closed"},
	{1, "Investigation completed; the symptom was not reproduced"},
	{2, "Invalid input, configuration, or usage"},
	{3, "Investigation finished inconclusive"},
	{4, "Infrastructure, agent, or protocol failure"},
	{5, "Cleanup incomplete or sandbox creation uncertain; see the report"},
	{124, "Investigation exceeded its time limit"},
	{130, "Interrupted (Ctrl-C or SIGTERM)"},
}

// configFieldDocs documents every JSON field of investigation.Config. The
// generator fails when a field is missing here or an entry goes stale, so
// configuration changes must update this table.
var configFieldDocs = map[string]string{
	"endpoint":              "kagent MCP API endpoint. HTTPS off-loopback; plain HTTP only on loopback or, with allow_cluster_http, on cluster-internal service names.",
	"namespace":             "Kubernetes namespace of the kagent installation.",
	"agent":                 "kagent agent invoked as the investigator.",
	"template":              "Standalone sandbox template used for the run's sandbox.",
	"user_id":               "kagent user identity that owns sessions and sandboxes.",
	"token_env":             "Environment variable holding the kagent invocation token; the token itself never goes in the file.",
	"investigation_timeout": "Budget per active investigation round in seconds (at most 3000). Resume starts a fresh round with the saved limit.",
	"process_timeout":       "Per-process watchdog in seconds; cannot exceed investigation_timeout.",
	"sandbox_ttl":           "Active sandbox TTL in seconds; must cover the investigation plus request and cleanup budgets (at most 3600). The pinned fork retains a fully suspended sandbox and refreshes this bounded TTL once on resume.",
	"max_experiments":       "Client-side cap on recorded experiments (at most 32). A watchdog observed between polls, not admission control.",
	"poll_interval":         "Seconds between task-history polls (at most 10).",
	"request_timeout":       "Per HTTP request timeout in seconds (at most 60).",
	"max_output_bytes":      "Cap on captured process output per read (at most 4 MiB).",
	"manual_agents":         "Optional map of agent name to extra MCP tool names for --agent runs; an empty tool list means the six investigation tools only.",
	"run_namespaces":        "Optional allowlist of up to eight additional Kubernetes namespaces where investigations may run. Entries must be unique valid namespace names and must not repeat namespace. Runs outside the default namespace and this list are refused; this setting does not create namespaces or grant access.",
	"allow_cluster_http":    "Permit plain-HTTP endpoints on *.svc or *.svc.cluster.local service names, for a dedicated in-cluster deployment whose pod network is the trust boundary.",
}

// exampleFileDocs embeds the committed example configurations verbatim. The
// generator fails when a listed file is missing from the repository root.
var exampleFileDocs = []struct {
	file        string
	title       string
	description string
}{
	{file: "triagelab.example.json", title: "Investigation configuration", description: "The core configuration passed to `--config`; defaults shown in the [configuration](/TriageLab/reference/configuration/) table."},
	{file: "auth.example.json", title: "Sign-in configuration", description: "Optional file passed to `serve --auth` configuring one OpenID Connect provider; see [sign-in and roles](/TriageLab/guides/sign-in/)."},
	{file: "triagelab.chat.example.json", title: "Results chat", description: "Optional file passed to `serve --chat` selecting a tool-free completion endpoint; see [results chat and MCP](/TriageLab/guides/chat-mcp/)."},
	{file: "triagelab.mcp.example.json", title: "HTTP MCP access", description: "Optional file passed to `serve --mcp` defining scoped machine credentials; see [results chat and MCP](/TriageLab/guides/chat-mcp/)."},
	{file: "tickets.example.json", title: "Zendesk ticket source", description: "Passed to `serve --tickets` for the Zendesk case source; see [Zendesk tickets](/TriageLab/guides/tickets-zendesk/)."},
	{file: "github.example.json", title: "GitHub issue source", description: "Passed to `serve --tickets` for one GitHub.com repository; see [GitHub issues](/TriageLab/guides/tickets-github/)."},
	{file: "gitlab.example.json", title: "GitLab issue source", description: "Passed to `serve --tickets` for one GitLab.com or self-managed project; see [GitLab issues](/TriageLab/guides/tickets-gitlab/)."},
	{file: "linear.example.json", title: "Linear issue source", description: "Passed to `serve --tickets` for one Linear team; see [Linear issues](/TriageLab/guides/tickets-linear/)."},
	{file: "bondren.example.json", title: "Bondren issue source", description: "Passed to `serve --tickets` for one Bondren workspace origin; see [Bondren issues](/TriageLab/guides/tickets-bondren/)."},
	{file: "jira.example.json", title: "Jira issue source", description: "Passed to `serve --tickets` for one Jira Cloud site; see [Jira issues](/TriageLab/guides/tickets-jira/)."},
	{file: "triagelab.telemetry.example.json", title: "Telemetry configuration", description: "Optional file passed to `--telemetry` for OTLP/HTTP export; see [telemetry](/TriageLab/guides/telemetry/)."},
}

func renderCLIPage() []byte {
	var b strings.Builder
	writeFrontmatter(&b, "CLI", "Every triagelab command, flag, and exit code, generated from the CLI source.")
	b.WriteString(docsGeneratedNote + "\n\n")
	for _, cmd := range cliCommandDocs() {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n```text\n%s\n```\n\n", cmd.name, cmd.purpose, cmd.usage)
		b.WriteString(flagTable(cmd.flags()))
		b.WriteString("\n")
	}
	b.WriteString("## version\n\nPrint the TriageLab and pinned upstream versions.\n\n```text\ntriagelab version\n```\n\n")
	b.WriteString("## Exit codes\n\nA nonzero exit never means the report should be discarded.\n\n| Code | Meaning |\n| --- | --- |\n")
	for _, e := range exitCodeDocs {
		fmt.Fprintf(&b, "| %d | %s |\n", e.code, e.meaning)
	}
	return []byte(b.String())
}

func flagTable(flags *flag.FlagSet) string {
	var b strings.Builder
	b.WriteString("| Flag | Default | Description |\n| --- | --- | --- |\n")
	flags.VisitAll(func(f *flag.Flag) {
		def := f.DefValue
		switch def {
		case "":
			def = "(none)"
		case "[]":
			def = "(repeatable)"
		}
		fmt.Fprintf(&b, "| `--%s` | %s | %s |\n", f.Name, def, markdownCell(f.Usage))
	})
	return b.String()
}

// flagReference matches an existing code span or a bare --flag. Markdown
// renderers with smart punctuation turn a bare double hyphen into a dash, so
// a flag named in running text must be a code span to stay copyable.
var flagReference = regexp.MustCompile("`[^`]*`|\\B--[a-z][a-z0-9-]*")

// markdownCell prepares text for a Markdown table cell: bare --flag
// references become code spans and pipes are escaped.
func markdownCell(s string) string {
	s = flagReference.ReplaceAllStringFunc(s, func(match string) string {
		if strings.HasPrefix(match, "`") {
			return match
		}
		return "`" + match + "`"
	})
	return strings.ReplaceAll(s, "|", "\\|")
}

func renderConfigurationPage() ([]byte, error) {
	configType := reflect.TypeOf(investigation.Config{})
	defaults := investigation.DefaultConfig()
	defaultValues := reflect.ValueOf(defaults)
	seen := make(map[string]bool)
	var rows []string
	for i := 0; i < configType.NumField(); i++ {
		field := configType.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		doc, ok := configFieldDocs[name]
		if !ok {
			return nil, fmt.Errorf("configuration field %q has no documentation entry in configFieldDocs", name)
		}
		seen[name] = true
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s | %s |", name, jsonTypeName(field.Type), defaultValue(defaultValues.Field(i)), markdownCell(doc)))
	}
	var stale []string
	for name := range configFieldDocs {
		if !seen[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		return nil, fmt.Errorf("stale configFieldDocs entries without matching Config fields: %s", strings.Join(stale, ", "))
	}
	var b strings.Builder
	writeFrontmatter(&b, "Configuration", "The investigation configuration file: every field, default, and bound, generated from the Go configuration struct.")
	b.WriteString(docsGeneratedNote + "\n\n")
	b.WriteString("One JSON object, decoded with unknown fields rejected. Omitted fields take the defaults below; every field is optional in the file but validated after loading.\n\n")
	b.WriteString("| Field | Type | Default | Description |\n| --- | --- | --- | --- |\n")
	b.WriteString(strings.Join(rows, "\n"))
	b.WriteString("\n\nSave it as `triagelab.json` and pass `--config triagelab.json`; the CLI and the workspace accept the same file. The committed [configuration examples](/TriageLab/reference/configuration-examples/) include this file plus the optional sign-in, ticket source, and telemetry configurations.\n")
	return []byte(b.String()), nil
}

func jsonTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int:
		return "integer"
	case reflect.Bool:
		return "boolean"
	case reflect.Map:
		return "object"
	case reflect.Slice:
		return "array"
	default:
		return t.Kind().String()
	}
}

func defaultValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		if v.String() == "" {
			return "(none)"
		}
		if strings.Contains(v.String(), "://") {
			return "`" + v.String() + "`"
		}
		return v.String()
	case reflect.Int:
		return fmt.Sprintf("%d", v.Int())
	case reflect.Bool:
		return fmt.Sprintf("%v", v.Bool())
	case reflect.Map:
		if v.Len() == 0 {
			return "(none)"
		}
		return fmt.Sprintf("%d entries", v.Len())
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}

func renderExamplesPage(root string) ([]byte, error) {
	var b strings.Builder
	writeFrontmatter(&b, "Configuration examples", "Every committed example configuration, embedded verbatim from the repository root.")
	b.WriteString(docsGeneratedNote + "\n\n")
	b.WriteString("Copy an example to a private file, fill in your values, and pass it to the matching flag. Credentials are always named by environment variables (`token_env`, `client_secret_env`, `headers_env`), never written in these files.\n\n")
	for _, example := range exampleFileDocs {
		data, err := os.ReadFile(filepath.Join(root, example.file))
		if err != nil {
			return nil, fmt.Errorf("example configuration %s: %w", example.file, err)
		}
		fmt.Fprintf(&b, "## %s\n\n`%s` — %s\n\n```json\n%s```\n\n", example.title, example.file, example.description, string(data))
	}
	return []byte(b.String()), nil
}

func writeFrontmatter(b *strings.Builder, title, description string) {
	quote := func(s string) string { return strings.ReplaceAll(s, "\"", "\\\"") }
	fmt.Fprintf(b, "---\ntitle: %q\ndescription: %q\n---\n\n", quote(title), quote(description))
}

// generateReferencePages renders the generated reference pages in a fixed
// order so repeated runs are byte-identical.
func generateReferencePages(root string) (map[string][]byte, error) {
	configuration, err := renderConfigurationPage()
	if err != nil {
		return nil, err
	}
	examples, err := renderExamplesPage(root)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"cli.md":                    renderCLIPage(),
		"configuration.md":          configuration,
		"configuration-examples.md": examples,
	}, nil
}

// runDocsCommand writes the generated reference pages (--out) or verifies the
// committed pages match the generator (--check). It needs no configuration
// and never contacts kagent.
func runDocsCommand(args []string, out, errout io.Writer) int {
	flags := flag.NewFlagSet("docs", flag.ContinueOnError)
	flags.SetOutput(errout)
	root := flags.String("root", ".", "repository root holding the example configuration files")
	outDir := flags.String("out", "", "write the generated pages to this directory")
	checkDir := flags.String("check", "", "verify the committed pages in this directory")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 || (*outDir == "" && *checkDir == "") {
		fmt.Fprintln(errout, "Use docs --out DIR or docs --check DIR [--root REPOSITORY_ROOT]")
		return 2
	}
	pages, err := generateReferencePages(*root)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	drift := false
	for _, dir := range []string{*outDir, *checkDir} {
		if dir == "" {
			continue
		}
		for _, name := range []string{"cli.md", "configuration.md", "configuration-examples.md"} {
			path := filepath.Join(dir, name)
			if dir == *outDir {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					fmt.Fprintln(errout, err)
					return 2
				}
				if err := os.WriteFile(path, pages[name], 0o644); err != nil {
					fmt.Fprintln(errout, err)
					return 2
				}
				fmt.Fprintf(out, "Wrote %s\n", path)
				continue
			}
			committed, err := os.ReadFile(path)
			if err != nil {
				fmt.Fprintln(errout, err)
				return 2
			}
			if !bytes.Equal(committed, pages[name]) {
				fmt.Fprintf(errout, "%s is stale; run: make docs-generate\n", path)
				drift = true
			}
		}
	}
	if drift {
		return 1
	}
	if *checkDir != "" {
		fmt.Fprintln(out, "Generated reference pages match the CLI source.")
	}
	return 0
}
