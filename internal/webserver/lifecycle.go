package webserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

// lifecycleView is the public action projection, never the private resume record.
type lifecycleView struct {
	Phase          string `json:"phase"`
	Round          int    `json:"round"`
	CanPause       bool   `json:"can_pause"`
	CanResume      bool   `json:"can_resume"`
	CanClose       bool   `json:"can_close"`
	ResumeSettings string `json:"resume_settings"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	MaxExperiments int    `json:"max_experiments"`
	Message        string `json:"message,omitempty"`
}

func (s *Server) loadRunState(id string) (investigation.State, error) {
	root, err := s.reportRoot(id)
	if err != nil {
		return investigation.State{}, err
	}
	defer root.Close()
	state, err := investigation.LoadState(filepath.Join(s.directory, id))
	if err == nil && state.RunID != id {
		return investigation.State{}, errors.New("investigation state does not match its directory")
	}
	return state, err
}

func resumeSettings(state investigation.State) string {
	encoded, _ := json.Marshal(struct {
		RunID  string
		Round  int
		Phase  string
		Config investigation.Config
		Tools  []string
	}{state.RunID, state.Round, state.Phase, state.Config, state.AdditionalTools})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func (s *Server) connectionMatches(config investigation.Config) bool {
	return config.Endpoint == s.config.Endpoint && s.config.AllowsNamespace(config.Namespace) &&
		config.UserID == s.config.UserID && config.AllowClusterHTTP == s.config.AllowClusterHTTP
}

func (s *Server) lifecycle(id string, state investigation.State, active bool) lifecycleView {
	view := lifecycleView{Phase: state.Phase, Round: state.Round, ResumeSettings: resumeSettings(state),
		TimeoutSeconds: state.Config.InvestigationTimeout, MaxExperiments: state.Config.MaxExperiments}
	if !s.connectionMatches(state.Config) {
		view.Message = "Restore this investigation's upstream connection and identity before continuing or closing it."
		return view
	}
	view.CanPause = active && state.Phase == "running"
	view.CanResume = !active && state.SandboxID != "" && state.SessionID != "" &&
		((state.Round < 32 && (state.Phase == "paused" || state.Phase == "pausing")) || state.Phase == "resuming")
	view.CanClose = !active && state.Phase != "closed" && (state.SandboxID != "" || state.SessionID != "" || state.SandboxCreationPending || state.SessionCreationPending)
	if state.Trigger == "ticket" && s.ticketOwner(id) == nil {
		view.CanResume = false
		view.Message = "Reconnect the original ticket source before resuming this investigation."
	}
	return view
}

func (s *Server) projectLifecycle(id string, saved map[string]any, active bool) {
	delete(saved, "lifecycle")
	state, err := s.loadRunState(id)
	if err != nil {
		return
	}
	saved["lifecycle"] = s.lifecycle(id, state, active)
	switch state.Phase {
	case "pausing", "paused", "resuming", "invoking", "closing":
		saved["status"] = state.Phase
	case "closed":
		switch saved["status"] {
		case "queued", "running", "pausing", "paused", "resuming", "invoking", "closing":
			saved["status"] = "closed"
		}
	}
}

func (s *Server) pauseRun(w http.ResponseWriter, _ *http.Request, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.activeID != id {
		fail(w, http.StatusConflict, "This investigation is not active")
		return
	}
	if err := investigation.RequestPause(filepath.Join(s.directory, id)); err != nil {
		fail(w, http.StatusConflict, "The investigation cannot pause at this stage. Refresh its status and try again.")
		return
	}
	jsonResponse(w, http.StatusAccepted, map[string]string{"id": id})
}

func (s *Server) continueRun(w http.ResponseWriter, r *http.Request, id, action string) {
	// Read the review binding before accepting input, then recheck it under the
	// active-slot lock after ingestion. The workflow also locks the durable run.
	state, err := s.loadRunState(id)
	if err != nil {
		fail(w, http.StatusConflict, "No resumable investigation state is available")
		return
	}
	if r.Header.Get("X-TriageLab-Resume-Round") != strconv.Itoa(state.Round) ||
		r.Header.Get("X-TriageLab-Resume-Settings") != resumeSettings(state) {
		fail(w, http.StatusConflict, "The investigation changed. Refresh it and review the current round and limits.")
		return
	}
	var added casefile.Case
	if action == "resume" {
		added, err = s.ingestUpload(w, r)
		if err != nil {
			fail(w, http.StatusBadRequest, fmt.Sprint(report.Redact(err.Error(), s.secretList()...)))
			return
		}
	}
	if r.Context().Err() != nil {
		return
	}
	actor := actorFrom(r.Context()).Username
	config := state.Config
	config.TokenEnv = s.config.TokenEnv
	launch := func(reserve func() error) error {
		return s.launchContinuation(id, state, config, added, actor, action, reserve)
	}
	if action == "resume" && state.Trigger == "ticket" {
		saved, readErr := s.readReport(id)
		owner := s.ticketOwner(id)
		if readErr != nil || owner == nil {
			fail(w, http.StatusConflict, "The original ticket approval is unavailable")
			return
		}
		// The journal is authoritative across a crash between state and report
		// writes; approval still binds the report's original ticket provenance.
		saved["status"] = state.Phase
		round := state.Round
		if state.Phase == "resuming" && state.ResumeFromRound > 0 {
			round = state.ResumeFromRound
		}
		err = owner.flow.ApproveContinuation(ticketflow.ContinuationApproval{RunID: id, Round: round, Report: saved, Input: added, Config: config, Tools: state.AdditionalTools, Actor: actor}, launch)
	} else {
		err = launch(nil)
	}
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, errWorkspaceClosed) {
			status = http.StatusServiceUnavailable
		}
		fail(w, status, fmt.Sprint(report.Redact(err.Error(), s.secretList()...)))
		return
	}
	jsonResponse(w, http.StatusAccepted, map[string]string{"id": id})
}

func (s *Server) launchContinuation(id string, state investigation.State, config investigation.Config, added casefile.Case, actor, action string, reserve func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ctx.Err() != nil {
		return errWorkspaceClosed
	}
	if s.active != nil {
		return ticketflow.ErrBusy
	}
	current, err := s.loadRunState(id)
	if err != nil || resumeSettings(current) != resumeSettings(state) || current.Phase != state.Phase {
		return errors.New("the investigation changed; refresh it before continuing")
	}
	view := s.lifecycle(id, current, false)
	if action == "resume" && !view.CanResume || action == "close" && !view.CanClose {
		if view.Message != "" {
			return errors.New(view.Message)
		}
		return errors.New("this action is not available for the current investigation state")
	}
	if reserve != nil {
		if err := reserve(); err != nil {
			return err
		}
	}
	dir := filepath.Join(s.directory, id)
	message := "Resuming investigation"
	if action == "close" {
		message = "Closing investigation and deleting execution resources"
	}
	s.startOperationLocked(id, config, current.AdditionalTools, actor, current.Trigger, message, func(ctx context.Context, runner *investigation.Investigator) error {
		runner.ExpectedRound = current.Round
		runner.ExpectedPhase = current.Phase
		if action == "close" {
			_, err := runner.Close(ctx, dir)
			return err
		}
		_, err := runner.Resume(ctx, added, dir)
		return err
	})
	return nil
}

// startOperationLocked is shared by new runs, continuation, and explicit cleanup.
// The caller owns s.mu and has already persisted any ticket reservation.
func (s *Server) startOperationLocked(id string, config investigation.Config, tools []string, actor, trigger, message string, operation func(context.Context, *investigation.Investigator) error) {
	ctx, cancel := context.WithCancel(s.ctx)
	active := &runningCase{cancel: cancel, done: make(chan struct{}), progress: []string{message}}
	delete(s.actionErrors, id)
	s.activeID, s.active = id, active
	go func() {
		defer cancel()
		runner := investigation.Investigator{Client: s.backend, Config: config, AdditionalTools: tools, Secrets: s.secretList(), Actor: actor, Trigger: trigger, Progress: func(message string) {
			safe := fmt.Sprint(report.Redact(message, s.secretList()...))
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(active.progress) >= 100 {
				active.progress = active.progress[1:]
			}
			active.progress = append(active.progress, safe)
		}}
		if err := operation(ctx, &runner); err != nil {
			s.mu.Lock()
			if s.actionErrors == nil {
				s.actionErrors = make(map[string]string)
			}
			s.actionErrors[id] = fmt.Sprint(report.Redact(err.Error(), s.secretList()...))
			active.progress = append(active.progress, "Investigation ended; inspect the report for errors")
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.activeID, s.active = "", nil
		close(active.done)
		s.mu.Unlock()
	}()
}
