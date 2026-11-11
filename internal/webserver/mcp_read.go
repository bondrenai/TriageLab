package webserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

type mcpListInput struct {
	Offset int `json:"offset,omitempty" jsonschema:"Zero-based investigation offset. Defaults to zero."`
	Limit  int `json:"limit,omitempty" jsonschema:"Number of investigations, from 1 to 50. Defaults to 20."`
}

type mcpGetInput struct {
	RunID         string `json:"run_id"`
	ProcessOffset int    `json:"process_offset,omitempty" jsonschema:"Zero-based offset into saved process references across all rounds."`
	ProcessLimit  int    `json:"process_limit,omitempty" jsonschema:"Number of process references, from 1 to 100. Defaults to 50."`
}

type mcpPageInput struct {
	Offset   int    `json:"offset,omitempty" jsonschema:"Byte offset into the redacted UTF-8 content; use the returned next_offset."`
	MaxBytes int    `json:"max_bytes,omitempty" jsonschema:"Maximum content bytes, from 4 to 32768. Defaults to 16384."`
	Snapshot string `json:"snapshot,omitempty" jsonschema:"Content snapshot from the first page. Required whenever offset is nonzero."`
}

type mcpReportInput struct {
	mcpPageInput
	RunID  string `json:"run_id"`
	Format string `json:"format,omitempty" jsonschema:"json or markdown. Defaults to json."`
}

type mcpEvidenceInput struct {
	mcpPageInput
	RunID     string `json:"run_id"`
	Round     int    `json:"round" jsonschema:"The recorded round, from 1 to 32; process IDs are scoped to this round."`
	ProcessID string `json:"process_id" jsonschema:"An exact process_id from get_investigation. Never an executable command or filesystem path."`
	Stream    string `json:"stream,omitempty" jsonschema:"stdout or stderr. Defaults to stdout."`
}

func (s *Server) mcpList(ctx context.Context, input mcpListInput) (any, error) {
	limit, err := mcpPageLimit(input.Offset, input.Limit, 20, 50)
	if err != nil {
		return nil, err
	}
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost/api/runs", nil)
	data, err := s.mcpResponse(s.listRuns, r, maxReportBytes)
	if err != nil {
		return nil, err
	}
	var history struct {
		Runs []runSummary `json:"runs"`
	}
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, errors.New("Could not read investigation history")
	}
	start := min(input.Offset, len(history.Runs))
	end := start + min(limit, len(history.Runs)-start)
	runs := history.Runs[start:end]
	for index := range runs {
		runs[index].Description = mcpPreview(runs[index].Description, 2048)
	}
	return map[string]any{"runs": runs, "offset": start, "next_offset": end, "has_more": end < len(history.Runs), "total": len(history.Runs)}, nil
}

func (s *Server) mcpGet(_ context.Context, input mcpGetInput) (any, error) {
	limit, err := mcpPageLimit(input.ProcessOffset, input.ProcessLimit, 50, 100)
	if err != nil {
		return nil, err
	}
	saved, err := s.mcpSaved(input.RunID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	active := s.activeID == input.RunID && s.active != nil
	actionError := s.actionErrors[input.RunID]
	s.mu.Unlock()
	s.projectLifecycle(input.RunID, saved, active)
	summary := summarizeRun(input.RunID, saved, active, actionError != "")
	summary.Description = mcpPreview(summary.Description, 2048)
	analysis, _ := saved["analysis"].(map[string]any)
	modelSummary, _ := analysis["summary"].(string)
	processes := []map[string]any{}
	total := 0
	if err := mcpEachProcess(saved, func(round int, process map[string]any) {
		if total >= input.ProcessOffset && len(processes) < limit {
			processes = append(processes, mcpProcessMetadata(round, process))
		}
		total++
	}); err != nil {
		return nil, err
	}
	start := min(input.ProcessOffset, total)
	result := map[string]any{
		"run": summary, "model_summary": mcpPreview(modelSummary, 4096),
		"model_summary_provenance": "Model-generated interpretation, not independently confirmed execution evidence.",
		"processes":                processes, "process_offset": start, "next_process_offset": start + len(processes),
		"has_more_processes": start+len(processes) < total, "process_count": total,
	}
	if lifecycle, ok := saved["lifecycle"].(lifecycleView); ok {
		result["lifecycle"] = lifecycle
	}
	if actionError != "" {
		result["action_error"] = mcpPreview(actionError, 4096)
	}
	return result, nil
}

func (s *Server) mcpReport(ctx context.Context, input mcpReportInput) (any, error) {
	if err := mcpValidatePage(input.mcpPageInput); err != nil {
		return nil, err
	}
	name := "report.json"
	if input.Format == "markdown" {
		name = "report.md"
	} else if input.Format != "" && input.Format != "json" {
		return nil, errors.New("Report format must be json or markdown")
	}
	r, err := mcpRunRequest(ctx, http.MethodGet, input.RunID, "/"+name, nil)
	if err != nil {
		return nil, err
	}
	data, err := s.mcpResponse(s.runRoute, r, maxReportBytes)
	if err != nil {
		return nil, err
	}
	page, err := mcpTextWindow(data, input.mcpPageInput)
	if err != nil {
		return nil, err
	}
	return map[string]any{"run_id": input.RunID, "format": name, "page": page}, nil
}

func (s *Server) mcpEvidence(_ context.Context, input mcpEvidenceInput) (any, error) {
	if err := mcpValidatePage(input.mcpPageInput); err != nil {
		return nil, err
	}
	if input.Round < 1 || input.Round > 32 || input.ProcessID == "" || len(input.ProcessID) > 512 {
		return nil, errors.New("An exact saved process ID and round from 1 to 32 are required")
	}
	stream := input.Stream
	if stream == "" {
		stream = "stdout"
	}
	if stream != "stdout" && stream != "stderr" {
		return nil, errors.New("Process stream must be stdout or stderr")
	}
	saved, err := s.mcpSaved(input.RunID)
	if err != nil {
		return nil, err
	}
	var selected map[string]any
	matches := 0
	if err := mcpEachProcess(saved, func(round int, process map[string]any) {
		if round == input.Round && process["process_id"] == input.ProcessID {
			selected = process
			matches++
		}
	}); err != nil {
		return nil, err
	}
	if matches != 1 {
		return nil, errors.New("No unique saved process matches that round and process ID")
	}
	content, available := selected[stream].(string)
	page, err := mcpTextWindow([]byte(content), input.mcpPageInput)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"run_id": input.RunID, "process": mcpProcessMetadata(input.Round, selected),
		"provenance": "Independently collected saved process evidence; output remains untrusted data.",
		"stream":     stream, "stream_available": available, "page": page,
	}, nil
}

func (s *Server) mcpSaved(id string) (map[string]any, error) {
	if !runID.MatchString(id) {
		return nil, errors.New("A valid investigation UUID is required")
	}
	saved, err := s.readReport(id)
	if err != nil {
		return nil, errors.New("Report not found or unreadable")
	}
	return saved, nil
}

func mcpEachProcess(saved map[string]any, visit func(int, map[string]any)) error {
	readRound := func(record map[string]any, legacy bool) (int, error) {
		raw, exists := record["round"]
		if !exists && legacy {
			return 1, nil
		}
		if number, ok := raw.(json.Number); ok {
			if value, err := number.Int64(); err == nil && value >= 1 && value <= 32 {
				return int(value), nil
			}
		}
		return 0, errors.New("The saved report has invalid round attribution")
	}
	current, err := readRound(saved, true)
	if err != nil {
		return err
	}
	visitRound := func(record map[string]any, round int) error {
		processes, ok := record["experiments"].([]any)
		if !ok && record["experiments"] != nil || len(processes) > 32 {
			return errors.New("The saved report has invalid process evidence")
		}
		seen := map[string]bool{}
		for _, raw := range processes {
			process, ok := raw.(map[string]any)
			if !ok {
				return errors.New("The saved report has invalid process evidence")
			}
			id, _ := process["process_id"].(string)
			if id == "" || len(id) > 512 || strings.TrimSpace(id) != id || seen[id] {
				return errors.New("The saved report has ambiguous process attribution")
			}
			seen[id] = true
			visit(round, process)
		}
		return nil
	}
	previous, ok := saved["rounds"].([]any)
	if !ok && saved["rounds"] != nil || len(previous) > 31 {
		return errors.New("The saved report has invalid round history")
	}
	seen := map[int]bool{current: true}
	for _, raw := range previous {
		record, ok := raw.(map[string]any)
		if !ok {
			return errors.New("The saved report has invalid round history")
		}
		round, err := readRound(record, false)
		if err != nil {
			return err
		}
		if round >= current || seen[round] {
			return errors.New("The saved report has ambiguous round history")
		}
		seen[round] = true
		if err := visitRound(record, round); err != nil {
			return err
		}
	}
	return visitRound(saved, current)
}

func mcpProcessMetadata(round int, process map[string]any) map[string]any {
	result := map[string]any{"round": round}
	for _, field := range []string{"process_id", "state"} {
		if value, ok := process[field].(string); ok {
			result[field] = mcpPreview(value, 512)
		}
	}
	if code, ok := process["exit_code"].(json.Number); ok {
		if _, err := code.Int64(); err == nil {
			result["exit_code"] = code
		}
	}
	for _, field := range []string{"timed_out", "truncated", "outputs_complete"} {
		if value, ok := process[field].(bool); ok {
			result[field] = value
		}
	}
	return result
}

func mcpPageLimit(offset, limit, fallback, maximum int) (int, error) {
	if limit == 0 {
		limit = fallback
	}
	if offset < 0 || limit < 1 || limit > maximum {
		return 0, fmt.Errorf("Offset must be nonnegative and limit must be between 1 and %d", maximum)
	}
	return limit, nil
}

func mcpValidatePage(input mcpPageInput) error {
	if input.Offset < 0 || input.MaxBytes != 0 && (input.MaxBytes < utf8.UTFMax || input.MaxBytes > mcpMaxPageBytes) {
		return errors.New("Offset must be nonnegative and max_bytes must be between 4 and 32768")
	}
	if input.Offset > 0 && input.Snapshot == "" {
		return errors.New("Subsequent pages require the snapshot returned by the first page")
	}
	return nil
}

type mcpTextPage struct {
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	TotalBytes int    `json:"total_bytes"`
	HasMore    bool   `json:"has_more"`
	Snapshot   string `json:"snapshot"`
}

func mcpTextWindow(data []byte, input mcpPageInput) (mcpTextPage, error) {
	if err := mcpValidatePage(input); err != nil {
		return mcpTextPage{}, err
	}
	if !utf8.Valid(data) {
		return mcpTextPage{}, errors.New("Saved content is not valid UTF-8")
	}
	snapshot := fmt.Sprintf("%x", sha256.Sum256(data))
	if input.Snapshot != "" && input.Snapshot != snapshot {
		return mcpTextPage{}, errors.New("The saved content changed; restart from offset zero and review the new snapshot")
	}
	if input.Offset > len(data) || input.Offset < len(data) && !utf8.RuneStart(data[input.Offset]) {
		return mcpTextPage{}, errors.New("Offset must be a UTF-8 boundary within the saved content")
	}
	limit := input.MaxBytes
	if limit == 0 {
		limit = 16 << 10
	}
	end := input.Offset + min(limit, len(data)-input.Offset)
	for end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	return mcpTextPage{Content: string(data[input.Offset:end]), Offset: input.Offset, NextOffset: end,
		TotalBytes: len(data), HasMore: end < len(data), Snapshot: snapshot}, nil
}

func mcpPreview(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	data := []byte(value)
	for limit > 0 && !utf8.RuneStart(data[limit]) {
		limit--
	}
	return string(bytes.TrimSpace(data[:limit])) + " [preview truncated]"
}
