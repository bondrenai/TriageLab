package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/bondrenai/TriageLab/internal/auth"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/kagent"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

const (
	defaultAuditLimit     = 100
	maxAuditLimit         = 500
	maxAuditFieldRunes    = 256
	maxAuditTailReadBytes = 2 << 20
	maxAuditFileBytes     = 8 << 20
)

// auditEntry records one security-relevant or state-changing workspace action
// using opaque identifiers and fixed outcome labels—never ticket prose,
// prompts, file contents, raw error text, or credentials. The log rotates to
// one previous generation at maxAuditFileBytes.
type auditEntry struct {
	At      string `json:"at"`
	Actor   string `json:"actor"`
	Via     string `json:"via,omitempty"`
	Action  string `json:"action"`
	Target  string `json:"target,omitempty"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

func (s *Server) auditPath() string {
	return filepath.Join(s.directory, ".auth", "audit.jsonl")
}

func cleanAuditField(value string, secrets []string) string {
	if value == "" {
		return ""
	}
	redacted, _ := report.Redacted(value, secrets...)
	var b strings.Builder
	count := 0
	for _, r := range redacted {
		if count >= maxAuditFieldRunes {
			break
		}
		if unicode.IsControl(r) {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
		count++
	}
	return strings.TrimSpace(b.String())
}

// auditError reduces an error to a fixed, known classification. Raw error
// text can quote ticket titles, provider response bodies, or kagent payloads,
// so unknown errors are recorded only as "failed".
func auditError(err error) string {
	if err == nil {
		return ""
	}
	known := []struct {
		target error
		label  string
	}{
		{auth.ErrUserNotFound, "account not found"},
		{auth.ErrUserExists, "account exists"},
		{auth.ErrLastAdmin, "last enabled admin"},
		{errAccountGuard, "account guard"},
		{auth.ErrClientNotFound, "client not found"},
		{auth.ErrClientExists, "client exists"},
		{ticketflow.ErrBusy, "busy"},
		{ticketflow.ErrNotFound, "case not found"},
		{ticketflow.ErrBlocked, "blocked"},
		{ticketflow.ErrChanged, "ticket changed"},
		{ticketflow.ErrStorage, "storage"},
		{ticketflow.ErrInvalidProcedure, "invalid procedure"},
		{ticketflow.ErrOAuthRequired, "oauth required"},
		{ticketflow.ErrOAuthCallback, "oauth callback"},
		{ticketflow.ErrOAuthDenied, "oauth denied"},
		{ticketflow.ErrOAuthExchange, "oauth exchange"},
		{kagent.ErrAgentNotReady, "agent not ready"},
		{kagent.ErrResourceNotFound, "resource not found"},
		{kagent.ErrResourceConflict, "resource conflict"},
		{kagent.ErrResourceForbidden, "resource forbidden"},
		{kagent.ErrResourceInvalid, "resource invalid"},
		{investigation.ErrBusy, "investigation busy"},
		{investigation.ErrRetentionUnsupported, "retention unsupported"},
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "timeout"},
	}
	for _, k := range known {
		if errors.Is(err, k.target) {
			return k.label
		}
	}
	return "failed"
}

func (s *Server) recordAudit(actor auth.Identity, action, target, outcome, detail string) {
	if s.directory == "" {
		return
	}
	secrets := s.secretList()
	actorName := strings.TrimSpace(actor.Username)
	if actorName == "" {
		actorName = "anonymous"
	}
	entry := auditEntry{
		At:      time.Now().UTC().Format(time.RFC3339Nano),
		Actor:   cleanAuditField(actorName, secrets),
		Via:     cleanAuditField(actor.Via, secrets),
		Action:  cleanAuditField(action, secrets),
		Target:  cleanAuditField(target, secrets),
		Outcome: cleanAuditField(outcome, secrets),
		Detail:  cleanAuditField(detail, secrets),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	line = append(line, '\n')

	path := s.auditPath()
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	// Rotate to one previous generation so the log stays bounded even when
	// unauthenticated failed logins append entries.
	if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size()+int64(len(line)) > maxAuditFileBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return
	}
	_, _ = f.Write(line)
}

// readAuditTail returns up to maxAuditTailReadBytes from the end of one log
// generation, starting at a line boundary. A missing file yields nothing.
func readAuditTail(path string, budget int64) ([]byte, error) {
	if budget <= 0 {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("audit log must be a regular file")
	}
	var offset int64
	if info.Size() > budget {
		offset = info.Size() - budget
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, budget))
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		// Discard the partial first line.
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			return nil, nil
		}
		data = data[idx+1:]
	}
	return data, nil
}

func (s *Server) readAuditEntries(limit int) ([]auditEntry, error) {
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	if limit > maxAuditLimit {
		limit = maxAuditLimit
	}
	path := s.auditPath()
	s.auditMu.Lock()
	current, err := readAuditTail(path, maxAuditTailReadBytes)
	var previous []byte
	if err == nil && int64(len(current)) < maxAuditTailReadBytes {
		previous, err = readAuditTail(path+".1", maxAuditTailReadBytes-int64(len(current)))
	}
	s.auditMu.Unlock()
	if err != nil {
		return nil, err
	}
	// Walk lines newest first and clean only the entries that are returned.
	// Splitting on bytes has no per-line limit, so one oversized or corrupt
	// line is skipped instead of hiding everything after it.
	secrets := s.secretList()
	entries := make([]auditEntry, 0, limit)
	for _, data := range [][]byte{current, previous} {
		lines := bytes.Split(data, []byte{'\n'})
		for i := len(lines) - 1; i >= 0 && len(entries) < limit; i-- {
			text := bytes.TrimSpace(lines[i])
			if len(text) == 0 {
				continue
			}
			var entry auditEntry
			if json.Unmarshal(text, &entry) != nil {
				continue
			}
			entry.Actor = cleanAuditField(entry.Actor, secrets)
			entry.Via = cleanAuditField(entry.Via, secrets)
			entry.Action = cleanAuditField(entry.Action, secrets)
			entry.Target = cleanAuditField(entry.Target, secrets)
			entry.Outcome = cleanAuditField(entry.Outcome, secrets)
			entry.Detail = cleanAuditField(entry.Detail, secrets)
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (s *Server) adminAuditRoute(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		fail(w, http.StatusServiceUnavailable, "Sign-in is not configured for this workspace")
		return
	}
	if !actorFrom(r.Context()).Role.AtLeast(auth.RoleAdmin) {
		fail(w, http.StatusForbidden, "Viewing the audit log requires the admin role")
		return
	}
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	limit := defaultAuditLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxAuditLimit {
			fail(w, http.StatusBadRequest, "Use a limit between 1 and 500")
			return
		}
		limit = parsed
	}
	entries, err := s.readAuditEntries(limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not read the audit log")
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"entries": entries})
}
