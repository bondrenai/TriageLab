package webserver

import (
	"encoding/json"
	"strings"
)

type runAttention struct {
	Reason string `json:"reason"`
	Action string `json:"action"`
}

type runSummary struct {
	ID              string        `json:"id"`
	Description     string        `json:"description"`
	Status          string        `json:"status"`
	Outcome         string        `json:"outcome"`
	StartedAt       string        `json:"started_at"`
	FinishedAt      string        `json:"finished_at,omitempty"`
	ExperimentCount int           `json:"experiment_count"`
	Active          bool          `json:"active"`
	Round           int           `json:"round"`
	ResourceState   string        `json:"resource_state"`
	Attention       *runAttention `json:"attention,omitempty"`
}

// summarizeRun exposes operational facts without copying model conclusions,
// blocking-question text, credentials, or private lifecycle settings into the queue.
// projectLifecycle must run first so saved in-flight statuses reflect the journal.
func summarizeRun(id string, saved map[string]any, active, actionFailed bool) runSummary {
	c, _ := saved["case"].(map[string]any)
	description, _ := c["description"].(string)
	status, _ := saved["status"].(string)
	outcome, _ := saved["outcome"].(string)
	started, _ := saved["started_at"].(string)
	finished, _ := saved["finished_at"].(string)
	experiments, _ := saved["experiments"].([]any)
	result := runSummary{ID: id, Description: description, Status: status, Outcome: outcome,
		StartedAt: started, FinishedAt: finished, ExperimentCount: len(experiments), Active: active,
		Round: 1, ResourceState: "unconfirmed"}
	if number, ok := saved["round"].(json.Number); ok {
		if round, err := number.Int64(); err == nil && round >= 1 && round <= 32 {
			result.Round = int(round)
		}
	}
	if rounds, ok := saved["rounds"].([]any); ok {
		for _, raw := range rounds {
			if prior, ok := raw.(map[string]any); ok {
				previous, _ := prior["experiments"].([]any)
				result.ExperimentCount += len(previous)
			}
		}
	}
	lifecycle, _ := saved["lifecycle"].(lifecycleView)
	if lifecycle.Round > 0 {
		result.Round = lifecycle.Round
	}
	cleanup, _ := saved["cleanup"].(map[string]any)
	cleanupErrors, _ := cleanup["errors"].([]any)
	switch {
	case active:
		result.ResourceState = "active"
	case lifecycle.Phase == "paused":
		result.ResourceState = "retained"
	case cleanup["sandbox_deleted"] == true && cleanup["session_deleted"] == true:
		result.ResourceState = "deleted"
	}
	if active {
		return result
	}
	attention := func(reason, action string) {
		result.Attention = &runAttention{Reason: reason, Action: action}
	}
	switch {
	case lifecycle.Message != "" && lifecycle.Phase != "closed" && result.ResourceState != "deleted":
		attention(lifecycle.Message, "Review run")
	case actionFailed:
		attention("The last workspace action failed", "Review run")
	case lifecycle.Phase == "invoking":
		attention("The continuation outcome is uncertain", "Review saved evidence")
	case status == "pausing" || status == "resuming":
		attention("A workspace operation needs to finish", "Review continuation")
	case status == "closing":
		attention("Resource cleanup needs to finish", "Review cleanup")
	case status == "paused":
		action := "Review paused investigation"
		if lifecycle.CanResume {
			action = "Resume or close"
		} else if lifecycle.CanClose {
			action = "Review and close"
		}
		if hasBlockingQuestions(saved) {
			if lifecycle.CanResume {
				action = "Add information and resume"
			}
			attention("Waiting for additional information", action)
		} else {
			attention("Investigation is paused", action)
		}
	case status == "running" || status == "queued" || status == "interrupted":
		if result.ResourceState == "unconfirmed" {
			attention("Interrupted; resource cleanup is not confirmed", "Review cleanup")
		} else {
			attention("Investigation was interrupted", "Review saved evidence")
		}
	case len(cleanupErrors) > 0:
		attention("Cleanup reported an error", "Review cleanup")
	case result.ResourceState == "unconfirmed":
		attention("Resource cleanup is not confirmed", "Review cleanup")
	case status == "error" || status == "timed_out":
		attention("Investigation ended before completing", "Review report")
	case status != "closed" && hasBlockingQuestions(saved):
		attention("Additional information is needed", "Review report")
	}
	return result
}

func hasBlockingQuestions(saved map[string]any) bool {
	analysis, _ := saved["analysis"].(map[string]any)
	questions, _ := analysis["missing_information"].([]any)
	for _, raw := range questions {
		if question, ok := raw.(string); ok && strings.TrimSpace(question) != "" {
			return true
		}
	}
	return false
}
