// Package webserver serves the private, single-user investigation workspace.
package webserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/auth"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/kagent"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/telemetry"
	"github.com/bondrenai/TriageLab/internal/ticketflow"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const maxReportBytes = 64 << 20

var runID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type runningCase struct {
	cancel   context.CancelFunc
	done     chan struct{}
	progress []string
}

// Server has one active investigation. Completed reports are read from disk.
type Server struct {
	ctx            context.Context
	config         investigation.Config
	backend        investigation.Backend
	secrets        []string
	directory      string
	reports        *os.Root
	assets         *os.Root
	mu             sync.Mutex
	activeID       string
	active         *runningCase
	actionErrors   map[string]string
	closed         bool
	tickets        *ticketflow.Flow
	deployments    *kagent.DeploymentReader
	resourceWrites bool
	auth           *gate
	mcp            *mcpAccess
	chat           chatGenerator
	chatClose      func()
	chatActive     *chatJob
	chatErrors     map[string]string
}

// New requires a built UI and a backend; it does not contact the model or cluster.
func New(ctx context.Context, config investigation.Config, directory, assets string, backend investigation.Backend, secrets ...string) (*Server, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if backend == nil {
		return nil, fmt.Errorf("an investigation backend is required")
	}
	assetRoot, err := os.OpenRoot(assets)
	if err != nil {
		return nil, fmt.Errorf("built UI not found at %s; run make build-ui: %w", assets, err)
	}
	index, err := assetRoot.Stat("index.html")
	if err != nil || !index.Mode().IsRegular() {
		assetRoot.Close()
		return nil, fmt.Errorf("built UI is missing index.html; run make build-ui")
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		assetRoot.Close()
		return nil, err
	}
	if err = os.MkdirAll(directory, 0o700); err != nil {
		assetRoot.Close()
		return nil, fmt.Errorf("create report directory: %w", err)
	}
	// The operator chooses the parent path. Resolve it once; individual report
	// entries below it must never be links.
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		assetRoot.Close()
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		assetRoot.Close()
		return nil, err
	}
	return &Server{ctx: ctx, config: config, backend: backend, secrets: append([]string(nil), secrets...), directory: directory, reports: root, assets: assetRoot}, nil
}

// Close rejects new work, cancels the active investigation, and waits for its
// separate cleanup deadline before closing report handles.
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	active := s.active
	if active != nil {
		active.cancel()
	}
	chat := s.chatActive
	if chat != nil {
		chat.cancel()
	}
	s.mu.Unlock()
	if active != nil {
		select {
		case <-active.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if chat != nil {
		select {
		case <-chat.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.chatClose != nil {
		s.chatClose()
	}
	var ticketErr error
	if s.tickets != nil {
		ticketErr = s.tickets.Close()
	}
	return errors.Join(ticketErr, s.reports.Close(), s.assets.Close())
}

// Serve runs a loopback-only HTTP server and waits for investigation cleanup on shutdown.
func Serve(ctx context.Context, listen string, s *Server, out io.Writer) (resultErr error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, s.Close(cleanup))
	}()
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("listen address must include a loopback host and port")
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("the UI must listen on a loopback address; use an SSH tunnel for remote access")
	}
	if s.auth == nil {
		return fmt.Errorf("the UI requires sign-in; create a local account (triagelab user add) or configure an identity provider")
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	if s.tickets != nil {
		s.tickets.Start(ctx)
	}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	fmt.Fprintf(out, "TriageLab UI: http://%s\n", listener.Addr())
	telemetry.Logger().Info("server started", "addr", listener.Addr().String())
	select {
	case err = <-result:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(shutdown)
		cancel()
		err = <-result
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	telemetry.Logger().Info("server stopped")
	return err
}

func allowedHost(hostport string) bool {
	host := hostport
	if parsed, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsed
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	route := routePattern(r.URL.Path)
	recorder := &statusRecorder{ResponseWriter: w}
	w = recorder
	start := time.Now()
	var span trace.Span
	if strings.HasPrefix(route, "/api/") || strings.HasPrefix(route, "/oauth/") {
		var spanCtx context.Context
		spanCtx, span = tracer().Start(r.Context(), "http "+route, trace.WithAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("route", route),
		))
		r = r.WithContext(spanCtx)
	}
	defer func() {
		status := recorder.statusCode()
		if span != nil {
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			span.End()
		}
		loadHTTPInstruments().request(r.Method, route, status, time.Since(start).Seconds())
	}()
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Monaco uses runtime styles for layout and syntax colors. Scripts and workers
	// remain same-origin; no inline script, eval, blob worker, or CDN is allowed.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; worker-src 'self'; font-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if !allowedHost(r.Host) {
		fail(w, http.StatusForbidden, "Only a loopback host is allowed")
		return
	}
	if r.URL.Path == "/mcp" {
		s.serveMCP(w, r)
		return
	}
	// OAuth is a top-level navigation from Zendesk. The callback has its
	// own exact host, one-time state and browser cookie checks.
	if r.URL.Path == ticketflow.OAuthCallbackPath {
		s.zendeskCallback(w, r)
		return
	}
	if r.URL.Path == "/healthz" {
		healthRoute(w, r)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "http" || u.Host != r.Host || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			fail(w, http.StatusForbidden, "Cross-origin requests are not allowed")
			return
		}
	}
	// The browser can carry Zendesk's cross-site classification through
	// the callback redirect to the ticket workspace document.
	ticketNavigation := r.Method == http.MethodGet && r.URL.Path == "/tickets" &&
		r.Header.Get("Sec-Fetch-Mode") == "navigate" && r.Header.Get("Sec-Fetch-Dest") == "document"
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" && !ticketNavigation {
		fail(w, http.StatusForbidden, "Cross-origin requests are not allowed")
		return
	}
	// The OAuth endpoints authenticate the caller in the request itself, so
	// they need no session and no cross-site request marker.
	if r.URL.Path == auth.OAuthTokenPath || r.URL.Path == auth.OAuthRevocationPath {
		if s.auth == nil || s.auth.oauth == nil {
			fail(w, http.StatusServiceUnavailable, "API clients are not configured for this workspace")
			return
		}
		s.auth.oauth.ServeHTTP(w, r)
		return
	}
	// A Bearer token is a credential in the request itself, so a cross-site
	// page cannot supply one and the request marker that protects session
	// cookies is not needed. Session routes under /api/auth/ keep the marker.
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !bearerRequest(r) &&
		r.Header.Get("X-TriageLab-Request") != "1" {
		fail(w, http.StatusForbidden, "Missing request header")
		return
	}
	// Sign-in routes manage their own credentials; account management below
	// /api/auth/admin/ still requires an admin session. Everything else under
	// /api/ requires an authenticated session with the route's minimum role.
	if strings.HasPrefix(r.URL.Path, "/api/auth/admin/") {
		if !s.authorize(w, r, auth.RoleAdmin) {
			return
		}
	}
	if s.authRoute(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if !s.authorize(w, r, requiredRole(r.Method, r.URL.Path)) {
			return
		}
	}
	switch r.URL.Path {
	case "/api/agents/status":
		s.deploymentStatus(w, r, "agents")
		return
	case "/api/sandboxes/status":
		s.deploymentStatus(w, r, "sandboxtemplates")
		return
	case "/api/models/status":
		s.deploymentStatus(w, r, "modelconfigs")
		return
	case "/api/mcp-servers/status":
		s.deploymentStatus(w, r, "remotemcpservers")
		return
	case "/api/egress-policies/status":
		s.deploymentStatus(w, r, "egresspolicies")
		return
	case "/api/agents/manifest":
		renderManifest(w, r, s.config.Namespace, agent.RenderWorkspaceDefinition)
		return
	case "/api/models/manifest":
		renderManifest(w, r, s.config.Namespace, agent.RenderModelDefinition)
		return
	case "/api/mcp-servers/manifest":
		renderManifest(w, r, s.config.Namespace, agent.RenderWorkspaceMCPServer)
		return
	case "/api/sandboxes/manifest":
		renderManifest(w, r, s.config.Namespace, agent.RenderSandboxTemplateDefinition)
		return
	case "/api/egress-policies/manifest":
		renderManifest(w, r, s.config.Namespace, agent.RenderEgressPolicyDefinition)
		return
	case "/api/agents/definition":
		resourceDefinition(s, w, r, "agents", agent.RenderWorkspaceDefinition, func(d agent.Definition) string { return d.Name })
		return
	case "/api/models/definition":
		resourceDefinition(s, w, r, "modelconfigs", agent.RenderModelDefinition, func(d agent.ModelDefinition) string { return d.Name })
		return
	case "/api/mcp-servers/definition":
		resourceDefinition(s, w, r, "remotemcpservers", agent.RenderWorkspaceMCPServer, func(d agent.WorkspaceMCPServer) string { return d.Name })
		return
	case "/api/sandboxes/definition":
		resourceDefinition(s, w, r, "sandboxtemplates", agent.RenderSandboxTemplateDefinition, func(d agent.SandboxTemplateDefinition) string { return d.Name })
		return
	case "/api/egress-policies/definition":
		resourceDefinition(s, w, r, "egresspolicies", agent.RenderEgressPolicyDefinition, func(d agent.EgressPolicyDefinition) string { return d.Name })
		return
	}
	if r.URL.Path == "/api/settings" && r.Method == http.MethodGet {
		jsonResponse(w, http.StatusOK, map[string]any{"agent": s.config.Agent, "sandbox_template": s.config.Template, "namespace": s.config.Namespace, "resource_writes": s.resourceWrites, "manual_agents": s.manualAgentNames(), "max_file_bytes": casefile.MaxFileBytes, "max_total_bytes": casefile.MaxTotalBytes, "max_files": casefile.MaxFiles, "max_description_bytes": casefile.MaxDescriptionBytes, "max_experiments": s.config.MaxExperiments, "timeout_seconds": s.config.InvestigationTimeout})
		return
	}
	if r.URL.Path == "/api/runs" {
		switch r.Method {
		case http.MethodGet:
			s.listRuns(w, r)
		case http.MethodPost:
			s.startRun(w, r)
		default:
			fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/runs/") {
		s.runRoute(w, r)
		return
	}
	if r.URL.Path == "/api/tickets" || strings.HasPrefix(r.URL.Path, "/api/tickets/") {
		s.ticketRoute(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, http.StatusNotFound, "API route not found")
		return
	}
	s.static(w, r)
}

func openRegular(root *os.Root, name string) (*os.File, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, fmt.Errorf("file changed while opening")
	}
	return file, nil
}

func (s *Server) reportRoot(id string) (*os.Root, error) {
	if !runID.MatchString(id) {
		return nil, fs.ErrNotExist
	}
	before, err := s.reports.Lstat(id)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fs.ErrNotExist
	}
	root, err := s.reports.OpenRoot(id)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, fs.ErrNotExist
	}
	return root, nil
}

func (s *Server) readReport(id string) (map[string]any, error) {
	root, err := s.reportRoot(id)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openRegular(root, "report.json")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, maxReportBytes+1))
	if err != nil || len(content) > maxReportBytes {
		return nil, fmt.Errorf("cannot read report")
	}
	return s.decodeReport(content, id)
}

func (s *Server) decodeReport(content []byte, id string) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var saved map[string]any
	if err := decoder.Decode(&saved); err != nil || saved == nil || saved["run_id"] != id {
		return nil, fmt.Errorf("invalid report")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("invalid report")
	}
	return report.Redact(saved, s.secrets...).(map[string]any), nil
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	directory, err := s.reports.Open(".")
	if err != nil {
		fail(w, 500, "Could not read report history")
		return
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		fail(w, 500, "Could not read report history")
		return
	}
	s.mu.Lock()
	activeID := s.activeID
	failedActions := make(map[string]bool, len(s.actionErrors))
	for id := range s.actionErrors {
		failedActions[id] = true
	}
	s.mu.Unlock()
	runs := []runSummary{}
	for _, entry := range entries {
		if !entry.IsDir() || !runID.MatchString(entry.Name()) {
			continue
		}
		saved, err := s.readReport(entry.Name())
		if err != nil {
			continue
		}
		// A client without read scope sees only the runs it started.
		if grant, client := clientFrom(r.Context()); client && !grant.Allows(auth.ScopeRead) &&
			saved["started_by"] != auth.ClientActor(grant.ClientID) {
			continue
		}
		s.projectLifecycle(entry.Name(), saved, entry.Name() == activeID)
		runs = append(runs, summarizeRun(entry.Name(), saved, entry.Name() == activeID, failedActions[entry.Name()]))
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt > runs[j].StartedAt })
	jsonResponse(w, 200, map[string]any{"runs": runs})
}

func (s *Server) runRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/runs/"), "/")
	if len(parts) > 2 || !runID.MatchString(parts[0]) {
		fail(w, 404, "Run not found")
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "chat" {
		s.chatRoute(w, r, id)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		switch parts[1] {
		case "pause":
			s.pauseRun(w, r, id)
			return
		case "resume", "close":
			s.continueRun(w, r, id, parts[1])
			return
		}
	}
	if len(parts) == 2 && parts[1] == "ticket-note" && r.Method == http.MethodPost {
		s.postTicketNote(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		s.mu.Lock()
		if s.activeID != id || s.active == nil {
			s.mu.Unlock()
			fail(w, 409, "This investigation is not active")
			return
		}
		s.active.cancel()
		s.mu.Unlock()
		jsonResponse(w, 202, map[string]string{"id": id})
		return
	}
	if r.Method != http.MethodGet {
		fail(w, 405, "Method not allowed")
		return
	}
	if len(parts) == 2 {
		if parts[1] != "report.json" && parts[1] != "report.md" {
			fail(w, 404, "Report not found")
			return
		}
		s.download(w, r, id, parts[1])
		return
	}
	saved, err := s.readReport(id)
	if err != nil {
		fail(w, 404, "Report not found or unreadable")
		return
	}
	s.mu.Lock()
	saved["active"] = s.activeID == id
	saved["progress"] = []string{}
	delete(saved, "action_error")
	if message := s.actionErrors[id]; message != "" {
		saved["action_error"] = message
	}
	if s.activeID == id && s.active != nil {
		saved["progress"] = append([]string{}, s.active.progress...)
	}
	s.mu.Unlock()
	s.projectLifecycle(id, saved, saved["active"] == true)
	if s.tickets != nil && saved["active"] == false {
		if note := s.tickets.NotePreview(id, saved); note != nil {
			saved["ticket_note"] = note
		}
	}
	jsonResponse(w, 200, saved)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request, id, name string) {
	root, err := s.reportRoot(id)
	if err != nil {
		fail(w, 404, "Report not found")
		return
	}
	defer root.Close()
	file, err := openRegular(root, name)
	if err != nil {
		fail(w, 404, "Report not found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maxReportBytes {
		fail(w, 500, "Report is too large to download")
		return
	}
	content, err := io.ReadAll(io.LimitReader(file, maxReportBytes+1))
	if err != nil || len(content) > maxReportBytes {
		fail(w, 500, "Could not read report for download")
		return
	}
	// Reapply the current server's secret filter to historical files too. JSON
	// is decoded with UseNumber so redaction does not round canonical values.
	if name == "report.json" {
		saved, decodeErr := s.decodeReport(content, id)
		if decodeErr != nil {
			fail(w, 404, "Report not found or unreadable")
			return
		}
		content, err = json.MarshalIndent(saved, "", "  ")
		if err != nil {
			fail(w, 500, "Could not encode report for download")
			return
		}
		content = append(content, '\n')
		w.Header().Set("Content-Type", "application/json")
	} else {
		content = []byte(report.Redact(string(content), s.secrets...).(string))
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="triagelab-%s-%s"`, id, name))
	// The response also depends on current secrets, not only the file's mtime.
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(content))
}

func safeUploadName(name string) bool {
	return name != "." && fs.ValidPath(name) && !strings.Contains(name, `\`) && strings.IndexFunc(name, unicode.IsControl) < 0 && len(name) <= 512 && strings.Count(name, "/") < 16
}

func (s *Server) ingestUpload(w http.ResponseWriter, r *http.Request) (casefile.Case, error) {
	// Multipart headers count towards this limit as well as bounded file bodies.
	r.Body = http.MaxBytesReader(w, r.Body, casefile.MaxTotalBytes+casefile.MaxDescriptionBytes+(128<<10))
	reader, err := r.MultipartReader()
	if err != nil {
		return casefile.Case{}, fmt.Errorf("Use a multipart form with a description and optional files")
	}
	temp, err := os.MkdirTemp(s.directory, ".upload-")
	if err != nil {
		return casefile.Case{}, fmt.Errorf("Cannot prepare attachments")
	}
	defer os.RemoveAll(temp)
	seen := map[string]bool{}
	description := ""
	hasDescription := false
	total := 0
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return casefile.Case{}, fmt.Errorf("Could not read upload or upload exceeds the size limit")
		}
		_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			part.Close()
			return casefile.Case{}, fmt.Errorf("Invalid attachment header")
		}
		if params["name"] == "description" && params["filename"] == "" {
			content, readErr := io.ReadAll(io.LimitReader(part, casefile.MaxDescriptionBytes+1))
			part.Close()
			if hasDescription || readErr != nil || len(content) > casefile.MaxDescriptionBytes {
				return casefile.Case{}, fmt.Errorf("Description is duplicated or exceeds 64 KiB")
			}
			description = string(content)
			hasDescription = true
			continue
		}
		name := params["filename"]
		if params["name"] != "files" || !safeUploadName(name) {
			part.Close()
			return casefile.Case{}, fmt.Errorf("Unsafe attachment path or unexpected form field")
		}
		if seen[name] || len(seen) >= casefile.MaxFiles {
			part.Close()
			return casefile.Case{}, fmt.Errorf("Duplicate attachment path or more than 64 files")
		}
		content, readErr := io.ReadAll(io.LimitReader(part, casefile.MaxFileBytes+1))
		part.Close()
		total += len(content)
		if readErr != nil || len(content) > casefile.MaxFileBytes || total > casefile.MaxTotalBytes {
			return casefile.Case{}, fmt.Errorf("Attachments exceed 1 MiB per file or 4 MiB total")
		}
		destination := filepath.Join(temp, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return casefile.Case{}, fmt.Errorf("Conflicting attachment paths")
		}
		if err = os.WriteFile(destination, content, 0o600); err != nil {
			return casefile.Case{}, fmt.Errorf("Could not save attachment")
		}
		seen[name] = true
	}
	c, err := casefile.Ingest(description, temp, nil, nil)
	if err != nil {
		return casefile.Case{}, err
	}
	if len(c.Attachments) != len(seen) {
		return casefile.Case{}, fmt.Errorf("Attachments inside ignored build or dependency directories are not accepted")
	}
	c.Source = map[string]any{"kind": "upload"}
	c.Environment = map[string]any{}
	return c, nil
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	busy, closed := s.active != nil, s.closed
	s.mu.Unlock()
	if closed {
		fail(w, 503, "The workspace is shutting down")
		return
	}
	if busy {
		fail(w, 409, "An investigation is already running")
		return
	}
	var name string
	if names := r.Header.Values("X-TriageLab-Agent"); len(names) > 0 {
		if len(names) != 1 || names[0] == "" {
			fail(w, http.StatusBadRequest, "Choose exactly one configured agent")
			return
		}
		name = names[0]
	}
	config, extraTools, err := s.selectInvestigator(r.Context(), name)
	if err != nil {
		switch {
		case errors.Is(err, kagent.ErrAgentNotReady):
			fail(w, http.StatusConflict, err.Error())
		case errors.Is(err, kagent.ErrResourceNotFound):
			fail(w, http.StatusBadRequest, "Choose a saved agent or an agent configured for manual investigations.")
		default:
			fail(w, http.StatusBadRequest, "Could not verify the selected agent. Check the agent picker or Deployment status.")
		}
		return
	}
	var sandbox string
	if sandboxes := r.Header.Values("X-TriageLab-Sandbox"); len(sandboxes) > 0 {
		if len(sandboxes) != 1 || sandboxes[0] == "" {
			fail(w, http.StatusBadRequest, "Choose exactly one sandbox template")
			return
		}
		sandbox = sandboxes[0]
	}
	template, err := s.selectSandbox(r.Context(), sandbox)
	if err != nil {
		switch {
		case errors.Is(err, errSandboxForbidden):
			fail(w, http.StatusForbidden, err.Error())
		case errors.Is(err, kagent.ErrResourceNotFound):
			fail(w, http.StatusBadRequest, "Choose a saved sandbox template or the workspace default.")
		default:
			fail(w, http.StatusBadRequest, "Could not verify the selected sandbox template. Check the sandbox picker or Deployment status.")
		}
		return
	}
	config.Template = template
	c, err := s.ingestUpload(w, r)
	if err != nil {
		fail(w, 400, fmt.Sprint(report.Redact(err.Error(), s.secrets...)))
		return
	}
	if r.Context().Err() != nil {
		return
	}
	actor := actorFrom(r.Context())
	trigger := "ui"
	if _, client := clientFrom(r.Context()); client {
		trigger = "api"
	} else if actor.Via == "mcp" {
		trigger = "mcp"
	}
	if err := s.launchRun(c, nil, config, extraTools, actor.Username, trigger); err != nil {
		writeLaunchError(w, err)
		return
	}
	jsonResponse(w, 202, map[string]string{"id": c.ID})
}

var errWorkspaceClosed = errors.New("the workspace is shutting down")
var errRunStore = errors.New("could not save the initial report")

func writeLaunchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ticketflow.ErrBusy):
		fail(w, 409, "An investigation is already running")
	case errors.Is(err, errWorkspaceClosed):
		fail(w, 503, err.Error())
	default:
		fail(w, 500, "Could not save the initial report")
	}
}

func (s *Server) launchTicketAs(actor string) ticketflow.Launch {
	return func(c casefile.Case, target ticketflow.RunTarget, reserve func() error) error {
		config := s.config
		config.Agent, config.Template = target.Agent, target.Sandbox
		return s.launchRun(c, reserve, config, target.Tools, actor, "ticket")
	}
}

func (s *Server) launchRun(c casefile.Case, reserve func() error, config investigation.Config, extraTools []string, actor, trigger string) error {
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return errWorkspaceClosed
	}
	if s.active != nil {
		s.mu.Unlock()
		return ticketflow.ErrBusy
	}
	dir := filepath.Join(s.directory, c.ID)
	if err := s.reports.Mkdir(c.ID, 0o700); err != nil {
		s.mu.Unlock()
		return errRunStore
	}
	initial := map[string]any{"schema_version": 1, "run_id": c.ID, "case": c, "status": "queued", "agent": map[string]string{"namespace": config.Namespace, "name": config.Agent}, "sandbox_template": config.Template, "started_by": actor, "outcome": "inconclusive", "started_at": time.Now().UTC().Format(time.RFC3339Nano), "finished_at": nil, "experiments": []any{}, "errors": []string{}, "warnings": []string{}}
	if err := report.Write(dir, initial, s.secrets...); err != nil {
		_ = s.reports.RemoveAll(c.ID)
		s.mu.Unlock()
		return errRunStore
	}
	if reserve != nil {
		if err := reserve(); err != nil {
			// Keep the report if the durable ticket reservation was uncertain.
			s.mu.Unlock()
			return err
		}
	}
	s.startOperationLocked(c.ID, config, extraTools, actor, trigger, "Investigation queued", func(ctx context.Context, runner *investigation.Investigator) error {
		_, err := runner.Run(ctx, c, dir)
		return err
	})
	s.mu.Unlock()
	return nil
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		fail(w, 405, "Method not allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) || strings.Contains(name, `\`) {
		http.NotFound(w, r)
		return
	}
	file, err := s.assets.Open(name)
	if errors.Is(err, fs.ErrNotExist) && path.Ext(name) == "" {
		name = "index.html"
		file, err = s.assets.Open(name)
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}
