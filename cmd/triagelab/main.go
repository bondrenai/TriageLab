package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/kagent"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
	"github.com/bondrenai/TriageLab/internal/webserver"
)

// version is this build's TriageLab version. Release builds set it with
// -ldflags "-X main.version=v0.2.0"; it reaches the KAgent client and
// investigation reports explicitly through connect.
var version = "dev"

const configUsage = "JSON configuration (default: built-in localhost settings)"

type stringsFlag []string

func (v *stringsFlag) String() string     { return fmt.Sprint([]string(*v)) }
func (v *stringsFlag) Set(s string) error { *v = append(*v, s); return nil }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errout, "Usage: triagelab <investigate|serve|doctor|cleanup|manifest|agent-manifest|version> [options]")
		return 2
	}
	command, args := args[0], args[1:]
	switch command {
	case "version":
		return runVersion(args, out, errout)
	case "manifest":
		return runManifest(args, out, errout)
	case "agent-manifest":
		return runAgentManifest(args, out, errout)
	case "doctor":
		return runDoctor(ctx, args, out, errout)
	case "investigate":
		return runInvestigate(ctx, args, out, errout)
	case "serve":
		return runServe(ctx, args, out, errout)
	case "cleanup":
		return runCleanup(ctx, args, out, errout)
	default:
		fmt.Fprintln(errout, "Unknown command:", command)
		return 2
	}
}

// commandFlags returns a flag set for one subcommand; each subcommand defines
// only the options it uses, so another command's flags are rejected.
func commandFlags(name string, errout io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(errout)
	return flags
}

func parseFlags(flags *flag.FlagSet, args []string, errout io.Writer) bool {
	if err := flags.Parse(args); err != nil {
		return false
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errout, "Unexpected positional arguments")
		return false
	}
	return true
}

// connect loads the configuration and creates the KAgent client, passing this
// build's version explicitly to both. The token is read from the configured
// environment variable and never from a flag.
func connect(configPath string) (investigation.Config, *kagent.Client, string, error) {
	config, err := investigation.LoadConfig(configPath)
	if err != nil {
		return config, nil, "", err
	}
	config.Version = version
	token := os.Getenv(config.TokenEnv)
	options := []kagent.Option{kagent.ClientVersion(version)}
	if config.AllowClusterHTTP {
		options = append(options, kagent.AllowClusterHTTP())
	}
	client, err := kagent.New(config.Endpoint, config.UserID, token, time.Duration(config.RequestTimeout)*time.Second, options...)
	if err != nil {
		return config, nil, "", err
	}
	return config, client, token, nil
}

func runVersion(args []string, out, errout io.Writer) int {
	if !parseFlags(commandFlags("version", errout), args, errout) {
		return 2
	}
	fmt.Fprintf(out, "triagelab %s (KAgent %s / Substrate v1.0.0)\n", version, kagent.Version)
	return 0
}

func runManifest(args []string, out, errout io.Writer) int {
	flags := commandFlags("manifest", errout)
	image := flags.String("workload-image", "", "digest-pinned replacement tools image")
	if !parseFlags(flags, args, errout) {
		return 2
	}
	data, err := agent.Manifest(*image)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	fmt.Fprintln(out, string(data))
	return 0
}

func runAgentManifest(args []string, out, errout io.Writer) int {
	flags := commandFlags("agent-manifest", errout)
	definitionPath := flags.String("definition", "", "local agent definition JSON file")
	namespace := flags.String("namespace", investigation.DefaultConfig().Namespace, "KAgent namespace")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *definitionPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(errout, "Use agent-manifest --definition FILE [--namespace NAMESPACE]")
		return 2
	}
	f, err := os.Open(*definitionPath)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	defer f.Close()
	definition, err := agent.DecodeDefinition[agent.Definition](f)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	data, err := agent.RenderDefinition(*namespace, definition)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	if _, err := fmt.Fprintln(out, string(data)); err != nil {
		fmt.Fprintln(errout, "Could not write agent manifest")
		return 4
	}
	return 0
}

func runDoctor(ctx context.Context, args []string, out, errout io.Writer) int {
	flags := commandFlags("doctor", errout)
	configPath := flags.String("config", "", configUsage)
	if !parseFlags(flags, args, errout) {
		return 2
	}
	config, client, _, err := connect(*configPath)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	defer client.Close()
	probe, cancel := context.WithTimeout(ctx, time.Duration(config.RequestTimeout)*time.Second)
	defer cancel()
	if _, err = client.CallTool(probe, "list_sandbox_templates", map[string]any{"namespace": config.Namespace}); err != nil {
		fmt.Fprintln(errout, err)
		return 4
	}
	fmt.Fprintln(out, "KAgent sandbox API reachable. A live investigation is needed to verify runtime and model access.")
	return 0
}

func runServe(ctx context.Context, args []string, out, errout io.Writer) int {
	flags := commandFlags("serve", errout)
	configPath := flags.String("config", "", configUsage)
	output := flags.String("output", "reports", "parent directory for private reports")
	listen := flags.String("listen", "127.0.0.1:8080", "loopback address for the private web UI")
	assets := flags.String("assets", "web/dist", "built frontend directory")
	tickets := flags.String("tickets", "", "optional Zendesk ticket or GitHub issue procedure configuration")
	statusKubeconfig := flags.String("status-kubeconfig", "", "explicit Kubernetes config for live deployment status and optional saves")
	statusContext := flags.String("status-context", "", "Kubernetes context for live deployment status")
	allowResourceWrites := flags.Bool("allow-resource-writes", false, "allow the workspace to save KAgent definitions in the configured namespace")
	if !parseFlags(flags, args, errout) {
		return 2
	}
	config, client, token, err := connect(*configPath)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	defer client.Close()
	workspace, err := webserver.New(ctx, config, *output, *assets, client, token)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	if *statusKubeconfig != "" || *statusContext != "" {
		if err := workspace.EnableDeploymentStatus(*statusKubeconfig, *statusContext); err != nil {
			_ = workspace.Close(ctx)
			fmt.Fprintln(errout, err)
			return 2
		}
	}
	if *allowResourceWrites {
		if err := workspace.EnableResourceWrites(); err != nil {
			_ = workspace.Close(ctx)
			fmt.Fprintln(errout, err)
			return 2
		}
	}
	if *tickets != "" {
		ticketConfig, err := ticketflow.LoadConfig(*tickets)
		if err == nil {
			err = workspace.EnableTickets(ticketConfig)
		}
		if err != nil {
			_ = workspace.Close(ctx)
			fmt.Fprintln(errout, err)
			return 2
		}
	}
	if err = webserver.Serve(ctx, *listen, workspace, out); err != nil {
		fmt.Fprintln(errout, report.Redact(err.Error(), token))
		return 4
	}
	return 0
}

func runInvestigate(ctx context.Context, args []string, out, errout io.Writer) int {
	flags := commandFlags("investigate", errout)
	configPath := flags.String("config", "", configUsage)
	description := flags.String("description", "", "local issue description")
	descriptionFile := flags.String("description-file", "", "UTF-8 issue description file")
	repo := flags.String("repo", "", "small local repository or fixture directory")
	output := flags.String("output", "reports", "parent directory for private reports")
	manualAgent := flags.String("agent", "", "agent name configured under manual_agents")
	var files, logs stringsFlag
	flags.Var(&files, "file", "attachment (repeatable)")
	flags.Var(&logs, "log", "log file (repeatable)")
	if !parseFlags(flags, args, errout) {
		return 2
	}
	config, client, token, err := connect(*configPath)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	defer client.Close()
	runner := investigation.Investigator{Client: client, Secrets: []string{token}, Progress: func(s string) { fmt.Fprintln(out, s) }}
	runner.Config, runner.AdditionalTools, err = config.SelectManualAgent(*manualAgent)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	if *description != "" && *descriptionFile != "" {
		fmt.Fprintln(errout, "Use either --description or --description-file")
		return 2
	}
	if *descriptionFile != "" {
		f, err := os.Open(*descriptionFile)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		data, err := io.ReadAll(io.LimitReader(f, casefile.MaxDescriptionBytes+1))
		f.Close()
		if err != nil || len(data) > casefile.MaxDescriptionBytes {
			fmt.Fprintf(errout, "Cannot read description, or it exceeds %d KiB\n", casefile.MaxDescriptionBytes>>10)
			return 2
		}
		*description = string(data)
	}
	c, err := casefile.Ingest(*description, *repo, files, logs)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	dir, err := filepath.Abs(filepath.Join(*output, c.ID))
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	result, runErr := runner.Run(ctx, c, dir)
	if _, err := os.Stat(filepath.Join(dir, "report.md")); err == nil {
		fmt.Fprintf(out, "Report: %s\n", filepath.Join(dir, "report.md"))
	} else {
		fmt.Fprintln(errout, "Report could not be saved to", dir)
	}
	if runErr != nil {
		fmt.Fprintln(errout, report.Redact(runErr.Error(), token))
	}
	if result == nil {
		return 4
	}
	fmt.Fprintf(out, "Outcome: %v (%v)\n", result.Outcome, result.Status)
	if result.Cleanup != nil && len(result.Cleanup.Errors) > 0 {
		fmt.Fprintln(errout, "Cleanup incomplete; see report and use triagelab cleanup.")
		return 5
	}
	switch result.Status {
	case investigation.StatusInterrupted:
		return 130
	case investigation.StatusTimedOut:
		return 124
	case investigation.StatusError:
		return 4
	}
	if runErr != nil {
		return 4
	}
	switch result.Outcome {
	case investigation.OutcomeReproduced:
		return 0
	case investigation.OutcomeNotReproduced:
		return 1
	default:
		return 3
	}
}

func runCleanup(ctx context.Context, args []string, out, errout io.Writer) int {
	flags := commandFlags("cleanup", errout)
	configPath := flags.String("config", "", configUsage)
	reportPath := flags.String("report", "", "saved report.json for cleanup")
	if !parseFlags(flags, args, errout) {
		return 2
	}
	if *reportPath == "" {
		fmt.Fprintln(errout, "cleanup requires --report <report.json>")
		return 2
	}
	config, client, token, err := connect(*configPath)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	defer client.Close()
	f, err := os.Open(*reportPath)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	defer f.Close()
	// Only the first JSON value is read. The typed decoder refuses a schema
	// version this build does not write and keeps unmodelled members for the rewrite.
	var raw json.RawMessage
	if err = json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&raw); err != nil {
		fmt.Fprintln(errout, "Invalid report JSON")
		return 2
	}
	saved, err := investigation.DecodeReport(raw)
	if errors.Is(err, investigation.ErrUnsupportedSchema) {
		fmt.Fprintln(errout, err)
		return 2
	}
	if err != nil {
		fmt.Fprintln(errout, "Invalid report JSON")
		return 2
	}
	if conn := saved.Connection; conn == nil || conn.Endpoint != config.Endpoint || conn.UserID != config.UserID || conn.Namespace != config.Namespace {
		fmt.Fprintln(errout, "Configuration must match the report endpoint, user and namespace")
		return 2
	}
	if saved.Cleanup == nil {
		fmt.Fprintln(errout, "Report has no cleanup record")
		return 2
	}
	runner := investigation.Investigator{Client: client, Config: config, Secrets: []string{token}}
	cleanupCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	runner.Cleanup(cleanupCtx, saved)
	if err = report.Write(filepath.Dir(*reportPath), saved, token); err != nil {
		fmt.Fprintln(errout, err)
		return 4
	}
	if len(saved.Cleanup.Errors) > 0 {
		fmt.Fprintln(errout, "Cleanup remains incomplete; see report.")
		return 5
	}
	if saved.CreationUncertain != nil && *saved.CreationUncertain {
		fmt.Fprintln(errout, "Sandbox creation outcome is unknown; use saved request ID and upstream TTL to recover.")
		return 5
	}
	fmt.Fprintln(out, "Recorded runtime cleanup completed.")
	return 0
}
