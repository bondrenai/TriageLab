package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/report"
)

func ingestRunOptions(opts *runOptions) (casefile.Case, error) {
	if opts.description != "" && opts.descriptionFile != "" {
		return casefile.Case{}, fmt.Errorf("Use either --description or --description-file")
	}
	description := opts.description
	if opts.descriptionFile != "" {
		f, err := os.Open(opts.descriptionFile)
		if err != nil {
			return casefile.Case{}, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, casefile.MaxDescriptionBytes+1))
		if err != nil || len(data) > casefile.MaxDescriptionBytes {
			return casefile.Case{}, fmt.Errorf("Cannot read description, or it exceeds %d KiB", casefile.MaxDescriptionBytes>>10)
		}
		description = string(data)
	}
	return casefile.Ingest(description, opts.repo, opts.files, opts.logs)
}

func runSavedInvestigation(ctx context.Context, command string, opts *runOptions, runner *investigation.Investigator, out, errout io.Writer) int {
	if opts.reportPath == "" {
		fmt.Fprintf(errout, "%s requires --report <report.json>\n", command)
		return 2
	}
	dir, err := filepath.Abs(filepath.Dir(opts.reportPath))
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	state, err := investigation.LoadState(dir)
	if err != nil {
		fmt.Fprintln(errout, "Cannot load the private investigation lifecycle record:", report.Redact(err.Error(), runner.Secrets...))
		return 2
	}
	current := runner.Config
	if state.Config.Endpoint != current.Endpoint || state.Config.Namespace != current.Namespace || state.Config.UserID != current.UserID || state.Config.AllowClusterHTTP != current.AllowClusterHTTP {
		fmt.Fprintln(errout, "Configuration must match the saved endpoint, user, namespace, and transport policy")
		return 2
	}
	// A refreshed credential comes from the current client. Execution settings
	// and tool access stay bound to the original investigation.
	runner.Config = state.Config
	runner.AdditionalTools = state.AdditionalTools
	runner.ExpectedRound = state.Round
	runner.ExpectedPhase = state.Phase
	if command == "pause" {
		if err := investigation.RequestPause(dir); err != nil {
			fmt.Fprintln(errout, report.Redact(err.Error(), runner.Secrets...))
			return 4
		}
		fmt.Fprintln(out, "Pause requested. The report will say paused after evidence collection and sandbox suspension finish.")
		return 0
	}
	var result *investigation.Report
	if command == "resume" {
		if state.Trigger == "ticket" {
			fmt.Fprintln(errout, "Resume ticket investigations in the signed-in workspace to approve the new evidence and saved run settings.")
			return 2
		}
		input, inputErr := ingestRunOptions(opts)
		if inputErr != nil {
			fmt.Fprintln(errout, inputErr)
			return 2
		}
		result, err = runner.Resume(ctx, input, dir)
	} else {
		result, err = runner.Close(ctx, dir)
		if err == nil {
			fmt.Fprintf(out, "Report: %s\nInvestigation closed; saved evidence is retained.\n", filepath.Join(dir, "report.md"))
			return 0
		}
	}
	return printInvestigationResult(result, err, dir, out, errout, runner.Secrets...)
}

func printInvestigationResult(result *investigation.Report, runErr error, dir string, out, errout io.Writer, secrets ...string) int {
	if _, err := os.Stat(filepath.Join(dir, "report.md")); err == nil {
		fmt.Fprintf(out, "Report: %s\n", filepath.Join(dir, "report.md"))
	} else {
		fmt.Fprintln(errout, "Report could not be saved to", dir)
	}
	if runErr != nil {
		fmt.Fprintln(errout, report.Redact(runErr.Error(), secrets...))
	}
	if result == nil {
		return 4
	}
	fmt.Fprintf(out, "Outcome: %v (%v)\n", result.Outcome, result.Status)
	if result.Cleanup != nil && len(result.Cleanup.Errors) > 0 {
		fmt.Fprintln(errout, "Cleanup incomplete; see report and use triagelab close.")
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
	switch result.Status {
	case investigation.StatusPaused, investigation.StatusClosed:
		return 0
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
