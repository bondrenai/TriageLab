package webserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/report"
)

// cleanupTimeout matches the CLI's bound for `triagelab cleanup`.
const cleanupTimeout = 90 * time.Second

// runCleanup is the server's single cleanup slot. Cleanup is asynchronous so
// the request is not tied to the HTTP write timeout; the report page polls the
// run and sees cleanup_running until the saved report has been rewritten.
type runCleanup struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
}

// readSavedReport returns the saved record exactly as stored, without the
// redaction applied to browser responses. The record is written back by
// cleanup, so a redacted copy must never reach report.Write. A report with a
// schema version this build does not rewrite is refused with
// investigation.ErrUnsupportedSchema.
func (s *Server) readSavedReport(id string) (*investigation.Report, error) {
	content, err := s.readReportFile(id)
	if err != nil {
		return nil, err
	}
	saved, err := investigation.DecodeReport(content)
	if err != nil {
		return nil, err
	}
	if saved.RunID != id {
		return nil, fmt.Errorf("invalid report")
	}
	return saved, nil
}

// startCleanup retries recorded runtime cleanup for an inactive saved run,
// with the same checks as `triagelab cleanup --report`. A cleanup of an older
// run may proceed while a different investigation is active: it reads and
// writes only its own report directory, and the active run owns its own.
func (s *Server) startCleanup(w http.ResponseWriter, id string) {
	s.mu.Lock()
	switch {
	case s.closed || s.ctx.Err() != nil:
		s.mu.Unlock()
		fail(w, http.StatusServiceUnavailable, "The workspace is shutting down")
		return
	case s.activeID == id:
		s.mu.Unlock()
		fail(w, http.StatusConflict, "This investigation is still active; stop it and wait for its own cleanup")
		return
	case s.cleaning != nil && s.cleaning.id == id:
		s.mu.Unlock()
		fail(w, http.StatusConflict, "Cleanup is already running for this investigation")
		return
	case s.cleaning != nil:
		s.mu.Unlock()
		fail(w, http.StatusConflict, "Another cleanup is running; wait for it to finish")
		return
	}
	// Hold the slot while reading so no second request can start between the
	// checks below and the write at the end.
	ctx, cancel := context.WithTimeout(s.ctx, cleanupTimeout)
	cleaning := &runCleanup{id: id, cancel: cancel, done: make(chan struct{})}
	s.cleaning = cleaning
	delete(s.cleanupFailure, id)
	s.mu.Unlock()
	release := func() {
		cancel()
		s.mu.Lock()
		s.cleaning = nil
		close(cleaning.done)
		s.mu.Unlock()
	}

	saved, err := s.readSavedReport(id)
	if errors.Is(err, investigation.ErrUnsupportedSchema) {
		release()
		fail(w, http.StatusUnprocessableEntity, "This report's schema version is missing or is not one this TriageLab build rewrites, so cleanup was not attempted. Use the TriageLab build that wrote it.")
		return
	}
	if err != nil {
		release()
		fail(w, http.StatusNotFound, "Report not found or unreadable")
		return
	}
	connection := saved.Connection
	if saved.Cleanup == nil || connection == nil {
		release()
		fail(w, http.StatusUnprocessableEntity, "This report has no recorded sandbox or session to clean up. Runs that stopped before creating resources have no cleanup record.")
		return
	}
	if connection.Endpoint != s.config.Endpoint || connection.UserID != s.config.UserID || connection.Namespace != s.config.Namespace {
		release()
		fail(w, http.StatusConflict, "This workspace is configured for a different kagent endpoint, user or namespace than the one that created the report")
		return
	}
	dir := filepath.Join(s.directory, id)
	go func() {
		defer release()
		runner := investigation.Investigator{Client: s.backend, Config: s.config, Secrets: s.secrets}
		runner.Cleanup(ctx, saved)
		if err := report.Write(dir, saved, s.secrets...); err != nil {
			s.mu.Lock()
			s.cleanupFailure = map[string]string{id: "Cleanup finished but the report could not be saved. Retry, or use the CLI cleanup command."}
			s.mu.Unlock()
		}
	}()
	jsonResponse(w, http.StatusAccepted, map[string]string{"id": id})
}
