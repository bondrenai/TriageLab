package main

import (
	"context"
	"encoding/json"
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
)

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
		fmt.Fprintln(errout, "Usage: triagelab <investigate|doctor|cleanup|manifest|version> [options]")
		return 2
	}
	if args[0] == "version" {
		fmt.Fprintln(out, "triagelab 0.1.0 (KAgent v1.0.0-rc3 / Substrate v1.0.0)")
		return 0
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(errout)
	if args[0] == "manifest" {
		image := flags.String("workload-image", "", "digest-pinned replacement tools image")
		if err := flags.Parse(args[1:]); err != nil {
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
	configPath := flags.String("config", "", "JSON configuration (default: built-in localhost settings)")
	description := flags.String("description", "", "local issue description")
	descriptionFile := flags.String("description-file", "", "UTF-8 issue description file")
	repo := flags.String("repo", "", "small local repository or fixture directory")
	output := flags.String("output", "reports", "parent directory for private reports")
	reportPath := flags.String("report", "", "saved report.json for cleanup")
	var files, logs stringsFlag
	flags.Var(&files, "file", "attachment (repeatable)")
	flags.Var(&logs, "log", "log file (repeatable)")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errout, "Unexpected positional arguments")
		return 2
	}
	config, err := investigation.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	token := os.Getenv(config.TokenEnv)
	client, err := kagent.New(config.Endpoint, config.UserID, token, time.Duration(config.RequestTimeout)*time.Second)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	defer client.Close()
	runner := investigation.Investigator{Client: client, Config: config, Secrets: []string{token}, Progress: func(s string) { fmt.Fprintln(out, s) }}
	switch args[0] {
	case "doctor":
		probe, cancel := context.WithTimeout(ctx, time.Duration(config.RequestTimeout)*time.Second)
		defer cancel()
		_, err = client.CallTool(probe, "list_sandbox_templates", map[string]any{"namespace": config.Namespace})
		if err != nil {
			fmt.Fprintln(errout, err)
			return 4
		}
		fmt.Fprintln(out, "KAgent sandbox API reachable. A live investigation is needed to verify runtime and model access.")
		return 0
	case "investigate":
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
			data, err := io.ReadAll(io.LimitReader(f, 32769))
			f.Close()
			if err != nil || len(data) > 32768 {
				fmt.Fprintln(errout, "Cannot read description, or it exceeds 32 KiB")
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
		fmt.Fprintf(out, "Outcome: %v (%v)\n", result["outcome"], result["status"])
		cleanup, _ := result["cleanup"].(map[string]any)
		if problems, ok := cleanup["errors"].([]string); ok && len(problems) > 0 {
			fmt.Fprintln(errout, "Cleanup incomplete; see report and use triagelab cleanup.")
			return 5
		}
		switch result["status"] {
		case "interrupted":
			return 130
		case "timed_out":
			return 124
		case "error":
			return 4
		}
		if runErr != nil {
			return 4
		}
		switch result["outcome"] {
		case "reproduced":
			return 0
		case "not_reproduced":
			return 1
		default:
			return 3
		}
	case "cleanup":
		if *reportPath == "" {
			fmt.Fprintln(errout, "cleanup requires --report <report.json>")
			return 2
		}
		f, err := os.Open(*reportPath)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		defer f.Close()
		var saved map[string]any
		if err = json.NewDecoder(io.LimitReader(f, 32<<20)).Decode(&saved); err != nil {
			fmt.Fprintln(errout, "Invalid report JSON")
			return 2
		}
		conn, _ := saved["connection"].(map[string]any)
		if conn["endpoint"] != config.Endpoint || conn["user_id"] != config.UserID || conn["namespace"] != config.Namespace {
			fmt.Fprintln(errout, "Configuration must match the report endpoint, user and namespace")
			return 2
		}
		if _, ok := saved["cleanup"].(map[string]any); !ok {
			fmt.Fprintln(errout, "Report has no cleanup record")
			return 2
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		runner.Cleanup(cleanupCtx, saved)
		if err = report.Write(filepath.Dir(*reportPath), saved, token); err != nil {
			fmt.Fprintln(errout, err)
			return 4
		}
		cleanup := saved["cleanup"].(map[string]any)
		if len(cleanup["errors"].([]string)) > 0 {
			fmt.Fprintln(errout, "Cleanup remains incomplete; see report.")
			return 5
		}
		if saved["creation_uncertain"] == true {
			fmt.Fprintln(errout, "Sandbox creation outcome is unknown; use saved request ID and upstream TTL to recover.")
			return 5
		}
		fmt.Fprintln(out, "Recorded runtime cleanup completed.")
		return 0
	default:
		fmt.Fprintln(errout, "Unknown command:", args[0])
		return 2
	}
}
