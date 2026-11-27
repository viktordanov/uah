package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/config"
	"github.com/viktordanov/uah/internal/home"
	"github.com/viktordanov/uah/internal/mcp"
)

const (
	nameArg        = "<name>"
	usageConfig    = "user configuration file"
	usageCurrent   = "the current directory"
	usagePrintJSON = "print JSON"
)

// mcpCommand is `uah mcp`, Codex's `codex mcp`: list, get, add, remove,
// login, and logout, and approve.
func mcpCommand() *cli.Command {
	common := func(extra ...cli.Flag) []cli.Flag {
		return append([]cli.Flag{
			&cli.StringFlag{Name: flagWorkspace, Aliases: []string{"C"}, Usage: "the workspace whose servers to use (a trusted project file adds servers)", DefaultText: usageCurrent, TakesFile: true},
			&cli.StringFlag{Name: flagConfig, Usage: usageConfig, Value: config.UserFile(), Sources: cli.NewValueSourceChain(nonEmptyEnv(home.EnvConfig)), TakesFile: true},
		}, extra...)
	}
	jsonFlag := &cli.BoolFlag{Name: flagJSON, Usage: usagePrintJSON}

	return &cli.Command{
		Name:  "mcp",
		Usage: "manage MCP servers: list, get, add, remove, approve, login, logout",
		Description: "Servers are [mcp_servers.<name>] tables in Codex's format. add and remove edit the user\n" +
			"configuration file and keep the rest of it as it was. login runs OAuth for an HTTP server\n" +
			"that asks for it and keeps the tokens in the OS keyring or " + app.MCPCredentialsFile() + ".",
		Commands: []*cli.Command{
			{Name: "list", Usage: "list the configured servers", Flags: common(jsonFlag), OnUsageError: onUsageError, Action: mcpList},
			{Name: "get", Usage: "show one server", ArgsUsage: nameArg, Flags: common(jsonFlag), OnUsageError: onUsageError, Action: mcpGet},
			{
				Name: "add", Usage: "add a server to the user configuration, or replace it",
				ArgsUsage: nameArg + " (--url <url> | -- <command> [args...])", Flags: common(addFlags()...), OnUsageError: onUsageError, Action: mcpAdd,
			},
			{Name: "remove", Usage: "remove a server from the user configuration", ArgsUsage: nameArg, Flags: common(), OnUsageError: onUsageError, Action: mcpRemove},
			mcpApproveCommand(common),
			{
				Name: "login", Usage: "log in to an HTTP server with OAuth", ArgsUsage: nameArg, OnUsageError: onUsageError, Action: mcpLogin,
				Flags: common(
					&cli.StringFlag{Name: "scopes", Usage: "comma-separated scopes to ask for", DefaultText: "the configured scopes, else the server's"},
					&cli.BoolFlag{Name: "no-browser", Usage: "print the URL instead of opening a browser"},
				),
			},
			{Name: "logout", Usage: "forget a server's OAuth login", ArgsUsage: nameArg, Flags: common(), OnUsageError: onUsageError, Action: mcpLogout},
		},
	}
}

func addFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "url", Usage: "a streamable HTTP server's URL"},
		&cli.StringSliceFlag{Name: "env", Usage: "KEY=VALUE for a stdio server (repeatable)"},
		&cli.StringFlag{Name: "bearer-token-env-var", Usage: "the variable holding an HTTP server's bearer token"},
		&cli.StringFlag{Name: "oauth-client-id", Usage: "an OAuth client registered ahead of time"},
		&cli.StringFlag{Name: "oauth-resource", Usage: "the RFC 8707 resource to ask tokens for"},
		&cli.BoolFlag{Name: "approve", Usage: "run the server's tools without asking (default_tools_approval_mode = \"approve\")"},
	}
}

func mcpWorkspace(cmd *cli.Command) (string, error) {
	dir := cmd.String(flagWorkspace)
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("failed to resolve workspace: %w", err)
	}

	return abs, nil
}

// oneName is the single <name> argument.
func oneName(cmd *cli.Command) (string, error) {
	if cmd.Args().Len() != 1 {
		return "", cli.Exit("expected one server name (see --help)", exitUsage)
	}

	return cmd.Args().First(), nil
}

func mcpEntries(ctx context.Context, cmd *cli.Command) ([]app.MCPEntry, error) {
	ws, err := mcpWorkspace(cmd)
	if err != nil {
		return nil, err
	}
	entries, err := app.MCPServers(ctx, cmd.String(flagConfig), ws)

	return entries, exitError(err)
}

func mcpList(ctx context.Context, cmd *cli.Command) error {
	entries, err := mcpEntries(ctx, cmd)
	if err != nil {
		return err
	}
	if cmd.Bool(flagJSON) {
		return writeJSON(os.Stdout, jsonEntries(entries, false))
	}
	if len(entries) == 0 {
		fmt.Println("No MCP servers configured yet. Try `uah mcp add my-tool -- my-command`.")

		return nil
	}

	return printServers(os.Stdout, entries)
}

func mcpGet(ctx context.Context, cmd *cli.Command) error {
	name, err := oneName(cmd)
	if err != nil {
		return err
	}
	entries, err := mcpEntries(ctx, cmd)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name == name {
			if cmd.Bool(flagJSON) {
				return writeJSON(os.Stdout, jsonEntries([]app.MCPEntry{e}, true)[0])
			}
			printServer(os.Stdout, e)

			return nil
		}
	}

	return cli.Exit(fmt.Sprintf("No MCP server named '%s' found.", name), exitUsage)
}

func mcpAdd(ctx context.Context, cmd *cli.Command) error {
	args := cmd.Args().Slice()
	if len(args) == 0 {
		return cli.Exit("expected a server name (see --help)", exitUsage)
	}
	name := args[0]
	server, err := serverFromFlags(cmd, args[1:])
	if err != nil {
		return cli.Exit(err.Error(), exitUsage)
	}
	path := cmd.String(flagConfig)
	if err := mcp.AddServer(path, name, server); err != nil {
		return cli.Exit(err.Error(), exitUsage)
	}
	fmt.Printf("Added MCP server '%s' to %s.\n", name, path)
	if server.URL == "" || server.BearerTokenEnvVar != "" || !mcp.DiscoverOAuth(ctx, server.URL, nil) {
		return nil
	}
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		fmt.Printf("The server supports OAuth. Run `uah mcp login %s` to log in.\n", name)

		return nil
	}
	fmt.Println("Detected OAuth support. Starting OAuth flow…")

	return runLogin(ctx, cmd, name, false)
}

// serverFromFlags builds a server from --url or the command after the name.
func serverFromFlags(cmd *cli.Command, command []string) (mcp.ServerConfig, error) {
	url := cmd.String("url")
	var s mcp.ServerConfig
	switch {
	case url != "" && len(command) > 0:
		return s, errors.New("give --url or a command after --, not both")
	case url != "":
		if len(cmd.StringSlice("env")) > 0 {
			return s, errors.New("--env is only for stdio servers")
		}
		s.URL, s.BearerTokenEnvVar, s.OAuthResource = url, cmd.String("bearer-token-env-var"), cmd.String("oauth-resource")
		if id := cmd.String("oauth-client-id"); id != "" {
			s.OAuth = &mcp.OAuthConfig{ClientID: id}
		}
	case len(command) > 0:
		for _, f := range []string{"bearer-token-env-var", "oauth-client-id", "oauth-resource"} {
			if cmd.String(f) != "" {
				return s, fmt.Errorf("--%s is only for HTTP servers (--url)", f)
			}
		}
		s.Command, s.Args = command[0], command[1:]
		for _, kv := range cmd.StringSlice("env") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return s, fmt.Errorf("invalid --env %q (want KEY=VALUE)", kv)
			}
			if s.Env == nil {
				s.Env = map[string]string{}
			}
			s.Env[k] = v
		}
	default:
		return s, errors.New("give --url <url> or -- <command> [args...]")
	}
	if cmd.Bool("approve") {
		s.DefaultToolsApprovalMode = mcp.ApprovalApprove
	}

	return s, nil
}

func mcpRemove(_ context.Context, cmd *cli.Command) error {
	name, err := oneName(cmd)
	if err != nil {
		return err
	}
	ok, err := mcp.RemoveServer(cmd.String(flagConfig), name)
	if err != nil {
		return cli.Exit(err.Error(), exitUsage)
	}
	if !ok {
		fmt.Printf("No MCP server named '%s' found.\n", name)

		return nil
	}
	fmt.Printf("Removed MCP server '%s'.\n", name)

	return nil
}

func mcpLogin(ctx context.Context, cmd *cli.Command) error {
	name, err := oneName(cmd)
	if err != nil {
		return err
	}

	return runLogin(ctx, cmd, name, cmd.Bool("no-browser"))
}

func runLogin(ctx context.Context, cmd *cli.Command, name string, noBrowser bool) error {
	ws, err := mcpWorkspace(cmd)
	if err != nil {
		return err
	}
	opts := mcp.LoginOptions{Out: os.Stdout, OpenBrowser: openBrowser}
	if noBrowser {
		opts.OpenBrowser = nil
	}
	if scopes := cmd.String("scopes"); scopes != "" {
		opts.Scopes = strings.Split(scopes, ",")
	}
	if err := app.MCPLogin(ctx, cmd.String(flagConfig), ws, name, opts); err != nil {
		return exitError(err)
	}
	fmt.Printf("Successfully logged in to MCP server '%s'.\n", name)

	return nil
}

func mcpLogout(_ context.Context, cmd *cli.Command) error {
	name, err := oneName(cmd)
	if err != nil {
		return err
	}
	ws, err := mcpWorkspace(cmd)
	if err != nil {
		return err
	}
	ok, err := app.MCPLogout(cmd.String(flagConfig), ws, name)
	if err != nil {
		return exitError(err)
	}
	if !ok {
		fmt.Printf("No OAuth credentials stored for '%s'.\n", name)

		return nil
	}
	fmt.Printf("Removed OAuth credentials for '%s'.\n", name)

	return nil
}

// openBrowser opens a URL with $BROWSER, else the system's opener.
func openBrowser(url string) error {
	name, args := "xdg-open", []string{url}
	switch {
	case os.Getenv("BROWSER") != "":
		name = os.Getenv("BROWSER")
	case runtime.GOOS == "darwin":
		name = "open"
	case runtime.GOOS == "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	}
	c := exec.Command(name, args...) //nolint:noctx // the user's browser; it returns at once
	if err := c.Start(); err != nil {
		return fmt.Errorf("failed to open a browser: %w", err)
	}
	go func() { _ = c.Wait() }()

	return nil
}
