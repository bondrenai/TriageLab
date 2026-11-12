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
	"strings"
	"syscall"
	"time"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/auth"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/kagent"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/resultchat"
	"github.com/bondrenai/TriageLab/internal/telemetry"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
	"github.com/bondrenai/TriageLab/internal/webserver"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// version is this build's TriageLab version. Release builds set it with
// -ldflags "-X main.version=v0.2.0"; run passes it explicitly to the kagent
// client and to investigation reports.
var version = "dev"

type stringsFlag []string

func (v *stringsFlag) String() string     { return fmt.Sprint([]string(*v)) }
func (v *stringsFlag) Set(s string) error { *v = append(*v, s); return nil }

// runOptions holds the flag-defined inputs of the investigation commands.
// Registration lives in one place so the docs generator renders the real
// command surface instead of a copy that can drift.
type runOptions struct {
	configPath          string
	description         string
	descriptionFile     string
	repo                string
	output              string
	reportPath          string
	listen              string
	assets              string
	tickets             string
	telemetryPath       string
	statusKubeconfig    string
	statusContext       string
	authConfigPath      string
	usersPath           string
	chatConfigPath      string
	mcpConfigPath       string
	clientsPath         string
	allowResourceWrites bool
	manualAgent         string
	files               stringsFlag
	logs                stringsFlag
}

// registerRunFlags declares the flags of investigate, serve, doctor, or
// cleanup. Each command defines only the options it uses, so another
// command's flags are rejected instead of silently ignored.
func registerRunFlags(flags *flag.FlagSet, command string) *runOptions {
	opts := &runOptions{}
	flags.StringVar(&opts.configPath, "config", "", "JSON configuration (default: built-in localhost settings)")
	flags.StringVar(&opts.telemetryPath, "telemetry", "", "optional OTLP telemetry configuration exporting logs, metrics, and traces")
	switch command {
	case "investigate":
		flags.StringVar(&opts.description, "description", "", "local issue description")
		flags.StringVar(&opts.descriptionFile, "description-file", "", "UTF-8 issue description file")
		flags.StringVar(&opts.repo, "repo", "", "small local repository or fixture directory")
		flags.StringVar(&opts.output, "output", "reports", "parent directory for private reports")
		flags.StringVar(&opts.manualAgent, "agent", "", "agent name configured under manual_agents")
		flags.Var(&opts.files, "file", "attachment (repeatable)")
		flags.Var(&opts.logs, "log", "log file (repeatable)")
	case "serve":
		flags.StringVar(&opts.output, "output", "reports", "parent directory for private reports")
		flags.StringVar(&opts.listen, "listen", "127.0.0.1:8080", "loopback address for the private web UI")
		flags.StringVar(&opts.assets, "assets", "web/dist", "built frontend directory")
		flags.StringVar(&opts.tickets, "tickets", "", "optional Zendesk, GitHub, Linear, Bondren, or Jira case configuration (serve only)")
		flags.StringVar(&opts.statusKubeconfig, "status-kubeconfig", "", "explicit Kubernetes config for live deployment status and optional saves")
		flags.StringVar(&opts.statusContext, "status-context", "", "Kubernetes context for live deployment status")
		flags.BoolVar(&opts.allowResourceWrites, "allow-resource-writes", false, "allow the workspace to save kagent definitions in the configured namespace")
		flags.StringVar(&opts.authConfigPath, "auth", "", "optional sign-in configuration JSON with an oidc section")
		flags.StringVar(&opts.usersPath, "users", "", "account file for web sign-in (default <output>/.auth/users.json)")
		flags.StringVar(&opts.chatConfigPath, "chat", "", "optional results chat model configuration JSON")
		flags.StringVar(&opts.mcpConfigPath, "mcp", "", "optional HTTP MCP credentials and origins configuration JSON")
		flags.StringVar(&opts.clientsPath, "clients", "", "API client file for OAuth access (default <output>/.auth/clients.json)")
	case "pause", "close", "cleanup":
		flags.StringVar(&opts.reportPath, "report", "", "saved report.json for pause, resume, close, or cleanup")
	case "resume":
		flags.StringVar(&opts.reportPath, "report", "", "saved report.json for pause, resume, close, or cleanup")
		flags.StringVar(&opts.description, "description", "", "local issue description")
		flags.StringVar(&opts.descriptionFile, "description-file", "", "UTF-8 issue description file")
		flags.StringVar(&opts.repo, "repo", "", "small local repository or fixture directory")
		flags.Var(&opts.files, "file", "attachment (repeatable)")
		flags.Var(&opts.logs, "log", "log file (repeatable)")
	}
	return opts
}

type manifestOptions struct {
	workloadImage string
}

func registerManifestFlags(flags *flag.FlagSet) *manifestOptions {
	opts := &manifestOptions{}
	flags.StringVar(&opts.workloadImage, "workload-image", "", "digest-pinned replacement tools image")
	return opts
}

type agentManifestOptions struct {
	definition string
	namespace  string
}

func registerAgentManifestFlags(flags *flag.FlagSet) *agentManifestOptions {
	opts := &agentManifestOptions{}
	flags.StringVar(&opts.definition, "definition", "", "local agent definition JSON file")
	flags.StringVar(&opts.namespace, "namespace", investigation.DefaultConfig().Namespace, "kagent namespace")
	return opts
}

type userOptions struct {
	output        string
	usersFile     string
	username      string
	role          string
	password      string
	passwordStdin bool
}

func registerUserFlags(flags *flag.FlagSet) *userOptions {
	opts := &userOptions{}
	flags.StringVar(&opts.output, "output", "reports", "parent directory for the default account file")
	flags.StringVar(&opts.usersFile, "users", "", "account file (default <output>/.auth/users.json)")
	flags.StringVar(&opts.username, "username", "", "account name")
	flags.StringVar(&opts.role, "role", "", "viewer, user, or admin")
	flags.StringVar(&opts.password, "password", "", "initial or replacement password (prefer --password-stdin)")
	flags.BoolVar(&opts.passwordStdin, "password-stdin", false, "read the password from standard input")
	return opts
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, out, errout io.Writer) (exitCode int) {
	if len(args) == 0 {
		fmt.Fprintln(errout, "Usage: triagelab <investigate|pause|resume|close|serve|doctor|cleanup|manifest|agent-manifest|user|docs|version> [options]")
		return 2
	}
	command, args := args[0], args[1:]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(errout)
	switch command {
	case "version":
		if !parseFlags(flags, args, errout) {
			return 2
		}
		fmt.Fprintf(out, "triagelab %s (kagent %s / Substrate v1.0.0)\n", version, kagent.Version)
		return 0
	case "manifest":
		manifestOpts := registerManifestFlags(flags)
		if !parseFlags(flags, args, errout) {
			return 2
		}
		data, err := agent.Manifest(manifestOpts.workloadImage)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		fmt.Fprintln(out, string(data))
		return 0
	case "agent-manifest":
		agentOpts := registerAgentManifestFlags(flags)
		if err := flags.Parse(args); err != nil {
			return 2
		}
		if agentOpts.definition == "" || flags.NArg() != 0 {
			fmt.Fprintln(errout, "Use agent-manifest --definition FILE [--namespace NAMESPACE]")
			return 2
		}
		f, err := os.Open(agentOpts.definition)
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
		data, err := agent.RenderDefinition(agentOpts.namespace, definition)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		if _, err := fmt.Fprintln(out, string(data)); err != nil {
			fmt.Fprintln(errout, "Could not write agent manifest")
			return 4
		}
		return 0
	case "user":
		return runUserCommand(args, out, errout, os.Stdin)
	case "docs":
		return runDocsCommand(args, out, errout)
	case "investigate", "pause", "resume", "close", "serve", "doctor", "cleanup":
	default:
		fmt.Fprintln(errout, "Unknown command:", command)
		return 2
	}
	opts := registerRunFlags(flags, command)
	if !parseFlags(flags, args, errout) {
		return 2
	}
	if opts.telemetryPath != "" {
		telemetryConfig, err := telemetry.LoadConfig(opts.telemetryPath)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		providers, err := telemetry.Setup(ctx, telemetryConfig)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		defer func() {
			// Flush with a bounded budget before the process exits; export
			// failures are reported but never change the command outcome.
			flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := providers.Shutdown(flush); err != nil {
				fmt.Fprintln(errout, "telemetry export did not flush cleanly:", err)
			}
		}()
	}
	config, err := investigation.LoadConfig(opts.configPath)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	config.Version = version
	// The token is read from the configured environment variable, never a flag.
	token := os.Getenv(config.TokenEnv)
	clientOpts := []kagent.Option{kagent.ClientVersion(version)}
	if config.AllowClusterHTTP {
		clientOpts = append(clientOpts, kagent.AllowClusterHTTP())
	}
	client, err := kagent.New(config.Endpoint, config.UserID, token, time.Duration(config.RequestTimeout)*time.Second, clientOpts...)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	defer client.Close()
	runner := investigation.Investigator{Client: client, Config: config, Trigger: "cli", Secrets: []string{token}, Progress: func(s string) { fmt.Fprintln(out, s) }}
	ctx, commandSpan := otel.Tracer("github.com/bondrenai/TriageLab/cmd/triagelab").Start(ctx, "triagelab "+command)
	defer func() {
		commandSpan.SetAttributes(attribute.Int("exit_code", exitCode))
		if exitCode != 0 {
			commandSpan.SetStatus(codes.Error, fmt.Sprintf("command exited %d", exitCode))
		}
		commandSpan.End()
	}()
	switch command {
	case "pause", "resume", "close":
		return runSavedInvestigation(ctx, command, opts, &runner, out, errout)
	case "serve":
		workspace, err := webserver.New(ctx, config, opts.output, opts.assets, client, token)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		if err := enableSignIn(ctx, workspace, opts.authConfigPath, resolveUsersPath(opts.output, opts.usersPath), opts.listen); err != nil {
			_ = workspace.Close(ctx)
			fmt.Fprintln(errout, err)
			return 2
		}
		if opts.chatConfigPath != "" {
			chatConfig, err := resultchat.Load(opts.chatConfigPath)
			if err == nil {
				err = workspace.EnableChat(chatConfig)
			}
			if err != nil {
				_ = workspace.Close(ctx)
				fmt.Fprintln(errout, err)
				return 2
			}
		}
		if opts.mcpConfigPath != "" {
			mcpConfig, err := webserver.LoadMCPConfig(opts.mcpConfigPath)
			if err == nil {
				err = workspace.EnableMCP(mcpConfig)
			}
			if err != nil {
				_ = workspace.Close(ctx)
				fmt.Fprintln(errout, err)
				return 2
			}
		}
		clients, err := auth.OpenClientStore(resolveClientsPath(opts.output, opts.clientsPath))
		if err != nil {
			_ = workspace.Close(ctx)
			fmt.Fprintln(errout, err)
			return 2
		}
		if err := workspace.EnableAPIClients(clients); err != nil {
			_ = workspace.Close(ctx)
			fmt.Fprintln(errout, err)
			return 2
		}
		if opts.statusKubeconfig != "" || opts.statusContext != "" {
			if err := workspace.EnableDeploymentStatus(opts.statusKubeconfig, opts.statusContext); err != nil {
				_ = workspace.Close(ctx)
				fmt.Fprintln(errout, err)
				return 2
			}
		}
		if opts.allowResourceWrites {
			if err := workspace.EnableResourceWrites(); err != nil {
				_ = workspace.Close(ctx)
				fmt.Fprintln(errout, err)
				return 2
			}
		}
		var bootstraps []ticketflow.Config
		if opts.tickets != "" {
			bootstraps, err = ticketflow.LoadConfigs(opts.tickets)
			if err != nil {
				_ = workspace.Close(ctx)
				fmt.Fprintln(errout, err)
				return 2
			}
		}
		opened, seeded, err := workspace.EnableTicketConnections(bootstraps)
		if err != nil {
			_ = workspace.Close(ctx)
			fmt.Fprintln(errout, err)
			return 2
		}
		if opts.tickets != "" && opened > 0 && !seeded {
			fmt.Fprintln(out, "Using the ticket connections saved in this workspace; manage them in the UI or remove "+
				filepath.Join(opts.output, ".tickets", "registry.json")+" to bootstrap from --tickets again.")
		}
		if err = webserver.Serve(ctx, opts.listen, workspace, out); err != nil {
			fmt.Fprintln(errout, report.Redact(err.Error(), token))
			return 4
		}
		return 0
	case "doctor":
		probe, cancel := context.WithTimeout(ctx, time.Duration(config.RequestTimeout)*time.Second)
		defer cancel()
		_, err = client.CallTool(probe, "list_sandbox_templates", map[string]any{"namespace": config.Namespace})
		if err != nil {
			fmt.Fprintln(errout, err)
			return 4
		}
		fmt.Fprintln(out, "kagent sandbox API reachable. A live investigation is needed to verify runtime and model access.")
		return 0
	case "investigate":
		runner.Config, runner.AdditionalTools, err = config.SelectManualAgent(opts.manualAgent)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		c, err := ingestRunOptions(opts)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		dir, err := filepath.Abs(filepath.Join(opts.output, c.ID))
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		result, runErr := runner.Run(ctx, c, dir)
		return printInvestigationResult(result, runErr, dir, out, errout, token)
	case "cleanup":
		if opts.reportPath == "" {
			fmt.Fprintln(errout, "cleanup requires --report <report.json>")
			return 2
		}
		if _, stateErr := os.Lstat(filepath.Join(filepath.Dir(opts.reportPath), "lifecycle.json")); stateErr == nil {
			return runSavedInvestigation(ctx, "close", opts, &runner, out, errout)
		} else if !os.IsNotExist(stateErr) {
			fmt.Fprintln(errout, "Cannot read the investigation lifecycle record")
			return 4
		}
		f, err := os.Open(opts.reportPath)
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
		cleanupCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		runner.Cleanup(cleanupCtx, saved)
		if err = report.Write(filepath.Dir(opts.reportPath), saved, token); err != nil {
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
	default:
		fmt.Fprintln(errout, "Unknown command:", command)
		return 2
	}
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

// resolveUsersPath keeps local accounts beside the reports so they persist on
// the same volume in clustered deployments.
func resolveUsersPath(output, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(output, ".auth", "users.json")
}

// resolveClientsPath keeps OAuth API clients beside the account file so both
// credentials persist on the same volume in clustered deployments.
func resolveClientsPath(output, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(output, ".auth", "clients.json")
}

// enableSignIn wires local accounts and the optional identity provider. It
// fails fast when no login method is usable so an open workspace never starts.
func enableSignIn(ctx context.Context, workspace *webserver.Server, authConfigPath, usersFile, listen string) error {
	signInConfig := auth.AuthConfig{}
	if authConfigPath != "" {
		loaded, err := auth.LoadAuthConfig(authConfigPath)
		if err != nil {
			return fmt.Errorf("auth configuration: %w", err)
		}
		signInConfig = loaded
	}
	signInConfig, err := signInConfig.Validate("http://" + listen)
	if err != nil {
		return fmt.Errorf("auth configuration: %w", err)
	}
	users, err := auth.OpenStore(usersFile)
	if err != nil {
		return fmt.Errorf("user accounts: %w", err)
	}
	var provider *auth.OIDCProvider
	if signInConfig.OIDC != nil {
		if provider, err = auth.NewOIDCProvider(ctx, *signInConfig.OIDC); err != nil {
			return fmt.Errorf("identity provider: %w", err)
		}
	}
	if err := workspace.EnableAuth(users, provider); err != nil {
		return err
	}
	if provider == nil && !users.EnabledLogin() {
		return fmt.Errorf("no usable account in %s; create one with: triagelab user add --username NAME --role admin --password-stdin", usersFile)
	}
	return nil
}

// runUserCommand manages local sign-in accounts without contacting kagent.
func runUserCommand(args []string, out, errout io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		fmt.Fprintln(errout, "Usage: triagelab user <add|list|remove|set-role|set-password> [options]")
		return 2
	}
	flags := flag.NewFlagSet("user "+args[0], flag.ContinueOnError)
	flags.SetOutput(errout)
	opts := registerUserFlags(flags)
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	rest := flags.Args()
	store, err := auth.OpenStore(resolveUsersPath(opts.output, opts.usersFile))
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	readPassword := func(prompt string) (string, error) {
		switch {
		case opts.passwordStdin:
			content, err := io.ReadAll(stdin)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(content)), nil
		case opts.password != "":
			return opts.password, nil
		}
		return "", fmt.Errorf("%s; pass --password-stdin (recommended) or --password", prompt)
	}
	switch args[0] {
	case "add":
		if opts.username == "" || len(rest) != 0 {
			fmt.Fprintln(errout, "Use user add --username NAME --role ROLE [--password-stdin]")
			return 2
		}
		accountRole := opts.role
		if accountRole == "" {
			// The first account bootstraps the workspace and needs full
			// control; after that the default is least privilege.
			if len(store.Accounts()) == 0 {
				accountRole = string(auth.RoleAdmin)
			} else {
				accountRole = string(auth.RoleViewer)
			}
		}
		parsed, err := auth.ParseRole(accountRole)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		secret, err := readPassword("an initial password is required")
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		if err := store.Create(opts.username, secret, parsed); err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		fmt.Fprintf(out, "Added %s (%s) to %s\n", opts.username, parsed, store.Path())
		return 0
	case "list":
		if len(rest) != 0 {
			fmt.Fprintln(errout, "user list takes no positional arguments")
			return 2
		}
		accounts := store.Accounts()
		if len(accounts) == 0 {
			fmt.Fprintf(out, "No accounts in %s; start with triagelab user add.\n", store.Path())
			return 0
		}
		fmt.Fprintf(out, "%-24s %-8s %-9s %s\n", "USERNAME", "ROLE", "STATUS", "LOCAL SIGN-IN")
		for _, account := range accounts {
			status, login := "enabled", "password"
			if account.Disabled {
				status = "disabled"
			}
			if !account.Password {
				login = "none"
			}
			fmt.Fprintf(out, "%-24s %-8s %-9s %s\n", account.Username, account.Role, status, login)
		}
		return 0
	case "remove":
		name := opts.username
		if name == "" && len(rest) == 1 {
			name = rest[0]
		}
		if name == "" || len(rest) > 1 {
			fmt.Fprintln(errout, "Use user remove NAME")
			return 2
		}
		if err := store.Remove(name); err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		fmt.Fprintf(out, "Removed %s from %s\n", name, store.Path())
		return 0
	case "set-role":
		name, newRole := opts.username, ""
		if name == "" && len(rest) == 2 {
			name, newRole = rest[0], rest[1]
		}
		if name == "" || newRole == "" || len(rest) > 2 {
			fmt.Fprintln(errout, "Use user set-role NAME ROLE")
			return 2
		}
		parsed, err := auth.ParseRole(newRole)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		if err := store.SetRole(name, parsed); err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		fmt.Fprintf(out, "%s is now %s\n", name, parsed)
		return 0
	case "set-password":
		name := opts.username
		if name == "" && len(rest) == 1 {
			name = rest[0]
		}
		if name == "" || len(rest) > 1 {
			fmt.Fprintln(errout, "Use user set-password NAME [--password-stdin]")
			return 2
		}
		secret, err := readPassword("a replacement password is required")
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		if err := store.SetPassword(name, secret); err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		fmt.Fprintf(out, "Updated the password for %s\n", name)
		return 0
	}
	fmt.Fprintln(errout, "Unknown user command:", args[0])
	return 2
}
