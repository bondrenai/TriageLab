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
	"strings"
	"syscall"
	"time"

	"github.com/bondrenai/TriageLab/internal/agent"
	"github.com/bondrenai/TriageLab/internal/auth"
	"github.com/bondrenai/TriageLab/internal/casefile"
	"github.com/bondrenai/TriageLab/internal/investigation"
	"github.com/bondrenai/TriageLab/internal/kagent"
	"github.com/bondrenai/TriageLab/internal/report"
	"github.com/bondrenai/TriageLab/internal/telemetry"
	"github.com/bondrenai/TriageLab/internal/ticketflow"
	"github.com/bondrenai/TriageLab/internal/webserver"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

type stringsFlag []string

func (v *stringsFlag) String() string     { return fmt.Sprint([]string(*v)) }
func (v *stringsFlag) Set(s string) error { *v = append(*v, s); return nil }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, out, errout io.Writer) (exitCode int) {
	if len(args) == 0 {
		fmt.Fprintln(errout, "Usage: triagelab <investigate|serve|doctor|cleanup|manifest|agent-manifest|user|version> [options]")
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
	if args[0] == "agent-manifest" {
		definitionPath := flags.String("definition", "", "local agent definition JSON file")
		namespace := flags.String("namespace", investigation.DefaultConfig().Namespace, "KAgent namespace")
		if err := flags.Parse(args[1:]); err != nil {
			return 2
		}
		if *definitionPath == "" || flags.NArg() != 0 {
			fmt.Fprintln(errout, "Use agent-manifest --definition FILE [--namespace NAMESPACE]")
			return 2
		}
		f, err := os.Open(*definitionPath)
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
		data, err := agent.RenderDefinition(*namespace, definition)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		if _, err := fmt.Fprintln(out, string(data)); err != nil {
			fmt.Fprintln(errout, "Could not write agent manifest")
			return 4
		}
		return 0
	}
	if args[0] == "user" {
		return runUserCommand(args[1:], out, errout, os.Stdin)
	}
	configPath := flags.String("config", "", "JSON configuration (default: built-in localhost settings)")
	description := flags.String("description", "", "local issue description")
	descriptionFile := flags.String("description-file", "", "UTF-8 issue description file")
	repo := flags.String("repo", "", "small local repository or fixture directory")
	output := flags.String("output", "reports", "parent directory for private reports")
	reportPath := flags.String("report", "", "saved report.json for cleanup")
	listen := flags.String("listen", "127.0.0.1:8080", "loopback address for the private web UI")
	assets := flags.String("assets", "web/dist", "built frontend directory")
	tickets := flags.String("tickets", "", "optional Zendesk, GitHub, or Linear case configuration (serve only)")
	telemetryPath := flags.String("telemetry", "", "optional OTLP telemetry configuration exporting logs, metrics, and traces")
	var statusKubeconfig, statusContext, authConfigPath, usersPath string
	var allowResourceWrites bool
	if args[0] == "serve" {
		flags.StringVar(&statusKubeconfig, "status-kubeconfig", "", "explicit Kubernetes config for live deployment status and optional saves")
		flags.StringVar(&statusContext, "status-context", "", "Kubernetes context for live deployment status")
		flags.BoolVar(&allowResourceWrites, "allow-resource-writes", false, "allow the workspace to save KAgent definitions in the configured namespace")
		flags.StringVar(&authConfigPath, "auth", "", "optional sign-in configuration JSON with an oidc section")
		flags.StringVar(&usersPath, "users", "", "account file for web sign-in (default <output>/.auth/users.json)")
	}
	var manualAgent string
	if args[0] == "investigate" {
		flags.StringVar(&manualAgent, "agent", "", "agent name configured under manual_agents")
	}
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
	if *telemetryPath != "" {
		telemetryConfig, err := telemetry.LoadConfig(*telemetryPath)
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
	config, err := investigation.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	token := os.Getenv(config.TokenEnv)
	var clientOpts []kagent.Option
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
	ctx, commandSpan := otel.Tracer("github.com/bondrenai/TriageLab/cmd/triagelab").Start(ctx, "triagelab "+args[0])
	defer func() {
		commandSpan.SetAttributes(attribute.Int("exit_code", exitCode))
		if exitCode != 0 {
			commandSpan.SetStatus(codes.Error, fmt.Sprintf("command exited %d", exitCode))
		}
		commandSpan.End()
	}()
	switch args[0] {
	case "serve":
		workspace, err := webserver.New(ctx, config, *output, *assets, client, token)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		if err := enableSignIn(ctx, workspace, authConfigPath, resolveUsersPath(*output, usersPath), *listen); err != nil {
			_ = workspace.Close(ctx)
			fmt.Fprintln(errout, err)
			return 2
		}
		if statusKubeconfig != "" || statusContext != "" {
			if err := workspace.EnableDeploymentStatus(statusKubeconfig, statusContext); err != nil {
				_ = workspace.Close(ctx)
				fmt.Fprintln(errout, err)
				return 2
			}
		}
		if allowResourceWrites {
			if err := workspace.EnableResourceWrites(); err != nil {
				_ = workspace.Close(ctx)
				fmt.Fprintln(errout, err)
				return 2
			}
		}
		if *tickets != "" {
			ticketConfig, err := ticketflow.LoadConfig(*tickets)
			if err == nil {
				err = workspace.EnableTickets(ticketConfig)
			}
			if err != nil {
				_ = workspace.Close(ctx)
				fmt.Fprintln(errout, err)
				return 2
			}
		}
		if err = webserver.Serve(ctx, *listen, workspace, out); err != nil {
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
		fmt.Fprintln(out, "KAgent sandbox API reachable. A live investigation is needed to verify runtime and model access.")
		return 0
	case "investigate":
		runner.Config, runner.AdditionalTools, err = config.SelectManualAgent(manualAgent)
		if err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
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

// resolveUsersPath keeps local accounts beside the reports so they persist on
// the same volume in clustered deployments.
func resolveUsersPath(output, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return filepath.Join(output, ".auth", "users.json")
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

// runUserCommand manages local sign-in accounts without contacting KAgent.
func runUserCommand(args []string, out, errout io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		fmt.Fprintln(errout, "Usage: triagelab user <add|list|remove|set-role|set-password> [options]")
		return 2
	}
	flags := flag.NewFlagSet("user "+args[0], flag.ContinueOnError)
	flags.SetOutput(errout)
	output := flags.String("output", "reports", "parent directory for the default account file")
	usersFile := flags.String("users", "", "account file (default <output>/.auth/users.json)")
	username := flags.String("username", "", "account name")
	role := flags.String("role", "", "viewer, user, or admin")
	password := flags.String("password", "", "initial or replacement password (prefer --password-stdin)")
	passwordStdin := flags.Bool("password-stdin", false, "read the password from standard input")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	rest := flags.Args()
	store, err := auth.OpenStore(resolveUsersPath(*output, *usersFile))
	if err != nil {
		fmt.Fprintln(errout, err)
		return 2
	}
	readPassword := func(prompt string) (string, error) {
		switch {
		case *passwordStdin:
			content, err := io.ReadAll(stdin)
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(content)), nil
		case *password != "":
			return *password, nil
		}
		return "", fmt.Errorf("%s; pass --password-stdin (recommended) or --password", prompt)
	}
	switch args[0] {
	case "add":
		if *username == "" || len(rest) != 0 {
			fmt.Fprintln(errout, "Use user add --username NAME --role ROLE [--password-stdin]")
			return 2
		}
		accountRole := *role
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
		if err := store.Create(*username, secret, parsed); err != nil {
			fmt.Fprintln(errout, err)
			return 2
		}
		fmt.Fprintf(out, "Added %s (%s) to %s\n", *username, parsed, store.Path())
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
		name := *username
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
		name, newRole := *username, ""
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
		name := *username
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
