package webserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bondrenai/TriageLab/internal/gitops"
	"github.com/bondrenai/TriageLab/internal/telemetry"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
)

type gitopsSource interface {
	Snapshot(context.Context) (gitops.Snapshot, error)
	Token(context.Context, *gitops.SecretRef) (string, error)
	SetStatus(context.Context, gitops.Connection, gitops.Status) error
}

type gitopsSync struct {
	source   gitopsSource
	config   gitops.Config
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	statuses []connectionStatus
}

type managedTicket struct {
	owner       string
	key         string
	fingerprint [32]byte
}

// EnableGitOps adds a bounded configuration poller to this workspace process.
// Only explicitly selected resources are read; kagent still owns sandboxes.
func (s *Server) EnableGitOps(config gitops.Config) error {
	if s.gitops != nil {
		return errors.New("GitOps is already enabled")
	}
	client, err := gitops.NewClient(config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(s.ctx)
	g := &gitopsSync{source: client, config: config, cancel: cancel, done: make(chan struct{})}
	s.gitops = g
	go func() {
		defer close(g.done)
		defer client.Close()
		ticker := time.NewTicker(time.Duration(config.PollSeconds) * time.Second)
		defer ticker.Stop()
		failed := false
		for {
			cycle, finish := context.WithTimeout(ctx, 2*time.Minute)
			err := s.reconcileGitOps(cycle)
			finish()
			if err != nil && !failed && ctx.Err() == nil {
				telemetry.Logger().Warn("GitOps reconciliation failed; managed connections are stopped")
			}
			failed = err != nil
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}

func (s *Server) gitopsStatuses() []connectionStatus {
	if s.gitops == nil {
		return nil
	}
	s.gitops.mu.Lock()
	defer s.gitops.mu.Unlock()
	return append([]connectionStatus(nil), s.gitops.statuses...)
}

func (s *Server) gitopsOwns(id string) bool {
	if conn := s.ticketConnection(id); conn != nil {
		return conn.managed != nil
	}
	for _, status := range s.gitopsStatuses() {
		if status.ID == id {
			return true
		}
	}
	return false
}

func (s *Server) stopManaged(conn *ticketConn) {
	if conn == nil || conn.managed == nil {
		return
	}
	s.replaceTicketConnection(conn.id, nil)
	_ = conn.flow.Close()
}

func (s *Server) reconcileGitOps(ctx context.Context) error {
	g := s.gitops
	snapshot, err := g.source.Snapshot(ctx)
	var locals []ticketflow.Connection
	if err == nil {
		locals, err = ticketflow.LoadConnections(s.ticketRoot())
	}
	if err != nil {
		// Never interpret a failed/partial list as deletion or keep automatic
		// work running with an unobservable desired configuration.
		for _, conn := range s.ticketConnections() {
			s.stopManaged(conn)
		}
		g.mu.Lock()
		for i := range g.statuses {
			g.statuses[i].Running, g.statuses[i].Connected = false, false
			g.statuses[i].Error = "GitOps configuration unavailable; polling stopped"
		}
		g.mu.Unlock()
		return err
	}
	// Reserve every desired UI ID before opening flows. Collisions with local
	// records or another CR never adopt or overwrite their state.
	ids := map[string]int{}
	localIDs := map[string]bool{}
	for _, record := range locals {
		ids[record.ID]++
		localIDs[record.ID] = true
	}
	desired := map[string]bool{}
	for _, object := range snapshot.Connections {
		key := gitops.StateKey(g.config.Server, g.config.Namespace, g.config.Workspace, object.Metadata.Name)
		ids[key[:8]]++
		desired[key] = true
	}
	for _, conn := range s.ticketConnections() {
		if conn.managed != nil && !desired[conn.managed.key] {
			s.stopManaged(conn)
		}
	}
	statuses := make([]connectionStatus, 0, len(snapshot.Connections))
	var statusErr error
	for _, object := range snapshot.Connections {
		key := gitops.StateKey(g.config.Server, g.config.Namespace, g.config.Workspace, object.Metadata.Name)
		id := key[:8]
		owner := object.Metadata.Namespace + "/" + object.Metadata.Name
		view := connectionStatus{ID: id, Label: object.Spec.Label, ManagedBy: owner,
			Source: object.Spec.Config.Provider(), Account: ticketflow.SourceAccount(object.Spec.Config), Config: object.Spec.Config}
		view.Credential.Mode = "none"
		if object.Spec.TokenSecretRef != nil {
			view.Credential.Mode = "secret"
		}
		reason, message := "Reconciled", "Configuration applied; source connectivity is checked when used."
		var version ticketflow.ProcedureVersion
		var token string
		switch {
		case ids[id] != 1:
			reason, message = "IdentityConflict", "Connection ID conflicts with another connection. Use a different resource name."
		case object.Spec.Config.TokenEnv != "" || utf8.RuneCountInString(object.Spec.Label) > 60 || strings.IndexFunc(object.Spec.Label, unicode.IsControl) >= 0:
			reason, message = "InvalidConfiguration", "Use a plain label and tokenSecretRef instead of token_env."
		case object.Spec.Config.Provider() == "zendesk" && object.Spec.TokenSecretRef != nil:
			reason, message = "InvalidConfiguration", "Zendesk uses its browser OAuth grant; omit tokenSecretRef."
		default:
			version, err = object.Procedures(snapshot.Procedures)
			if err != nil {
				reason, message = "ProcedureUnavailable", "A procedure reference is invalid, duplicated, missing, or outside this workspace."
			} else if token, err = g.source.Token(ctx, object.Spec.TokenSecretRef); err != nil {
				reason, message = "SecretUnavailable", "The referenced Secret key could not be read or is not allowed."
			}
		}
		conn := s.ticketConnection(id)
		if reason == "Reconciled" {
			// Hash stays in memory; neither credentials nor their hash are
			// persisted or exposed in Kubernetes status or workspace responses.
			data, _ := json.Marshal([]any{object.Metadata.UID, object.Spec, version, token})
			fingerprint := sha256.Sum256(data)
			if conn == nil || conn.managed == nil || conn.managed.fingerprint != fingerprint {
				s.stopManaged(conn)
				conn = nil
				record := ticketflow.Connection{ID: id, Label: object.Spec.Label, Config: object.Spec.Config, Token: token}
				config, prepareErr := record.Prepared()
				if _, manual := s.config.ManualAgents[s.config.Agent]; manual {
					prepareErr = errors.New("ticket default agent cannot be a manual agent")
				}
				dir := filepath.Join(s.ticketRoot(), "gitops", key)
				if prepareErr == nil {
					prepareErr = bindManagedSource(dir, config)
				}
				if prepareErr == nil {
					var flow *ticketflow.Flow
					flow, prepareErr = ticketflow.OpenManaged(config, dir, s.config, version, append(s.secretList(), token)...)
					if prepareErr == nil {
						flow.ResolveTarget = s.resolveTicketTarget
						if config.Policy.AutoRuns {
							flow.AutoLaunch = s.launchTicketAs("ticket policy")
						}
						s.addSecret(token)
						conn = &ticketConn{id: id, label: object.Spec.Label, flow: flow,
							managed: &managedTicket{owner: owner, key: key, fingerprint: fingerprint}}
						s.replaceTicketConnection(id, conn)
						flow.Start(s.ctx)
					}
				}
				if prepareErr != nil {
					reason, message = "ApplyFailed", "Configuration, procedures, or retained source identity are invalid; check the manifests and private state."
				}
			}
		}
		if reason != "Reconciled" {
			s.stopManaged(conn)
			view.Error = message
		} else {
			view.Running, view.Connected = true, true
			if conn.flow.Provider() == "zendesk" {
				oauth := conn.flow.OAuthStatus()
				view.OAuth, view.Connected = &oauth, oauth.Connected
			}
		}
		if !localIDs[id] {
			statuses = append(statuses, view)
		}
		status := gitopsStatus(object, id, reason, message)
		if !reflect.DeepEqual(status, object.Status) {
			statusErr = errors.Join(statusErr, g.source.SetStatus(ctx, object, status))
		}
	}
	g.mu.Lock()
	g.statuses = statuses
	g.mu.Unlock()
	// A status write failure does not undo a successfully applied config.
	if statusErr != nil {
		telemetry.Logger().Warn("GitOps status update failed; retrying on the next poll")
	}
	return nil
}

func gitopsStatus(object gitops.Connection, id, reason, message string) gitops.Status {
	condition := gitops.Condition{Type: "Ready", Status: "False", Reason: reason, Message: message,
		ObservedGeneration: object.Metadata.Generation, LastTransitionTime: time.Now().UTC().Format(time.RFC3339)}
	if reason == "Reconciled" {
		condition.Status = "True"
	}
	for _, old := range object.Status.Conditions {
		if old.Type == condition.Type && old.Status == condition.Status {
			condition.LastTransitionTime = old.LastTransitionTime
		}
	}
	return gitops.Status{ObservedGeneration: object.Metadata.Generation, ConnectionID: id, Conditions: []gitops.Condition{condition}}
}

// Bind even an empty case store to its source. Changing a CR's account must
// not reuse clarification reservations or silently discard retained history.
func bindManagedSource(dir string, config ticketflow.Config) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("invalid managed state directory")
	}
	identity, _ := json.Marshal([]string{config.Provider(), ticketflow.SourceAccount(config)})
	path := filepath.Join(dir, ".source")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != int64(len(identity)) {
			return errors.New("invalid managed source identity")
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(data, identity) {
			return errors.New("managed source identity changed")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(identity)
	return errors.Join(writeErr, f.Sync(), f.Close())
}
