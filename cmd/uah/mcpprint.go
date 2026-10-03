package main

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/viktordanov/uah/internal/app"
	"github.com/viktordanov/uah/internal/mcp"
)

// printServers writes Codex's two tables: stdio servers, then HTTP ones.
func printServers(w io.Writer, entries []app.MCPEntry) error {
	var stdio, http []app.MCPEntry
	for _, e := range entries {
		if e.Config.URL != "" {
			http = append(http, e)
		} else {
			stdio = append(stdio, e)
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(stdio) > 0 {
		fmt.Fprintln(tw, "Name\tCommand\tArgs\tEnv\tCwd\tStatus\tAuth")
		for _, e := range stdio {
			c := e.Config
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.Name, c.Command, dash(strings.Join(c.Args, " ")),
				dash(masked(c.Env)), dash(c.Cwd), enabled(c), e.Auth.Text())
		}
	}
	if len(stdio) > 0 && len(http) > 0 {
		fmt.Fprintln(tw)
	}
	if len(http) > 0 {
		fmt.Fprintln(tw, "Name\tUrl\tBearer Token Env Var\tStatus\tAuth")
		for _, e := range http {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Name, e.Config.URL, dash(e.Config.BearerTokenEnvVar), enabled(e.Config), e.Auth.Text())
		}
	}

	return tw.Flush() // writing to stdout
}

// printServer writes `uah mcp get`: the name, then a line per set value.
func printServer(w io.Writer, e app.MCPEntry) {
	c := e.Config
	fmt.Fprintln(w, e.Name)
	line := func(key, value string) { fmt.Fprintf(w, "  %s: %s\n", key, value) }
	line("enabled", strconv.FormatBool(c.IsEnabled()))
	if c.EnabledTools != nil {
		line("enabled_tools", strings.Join(c.EnabledTools, ", "))
	}
	if len(c.DisabledTools) > 0 {
		line("disabled_tools", strings.Join(c.DisabledTools, ", "))
	}
	line("transport", c.Transport())
	if c.URL == "" {
		line("command", c.Command)
		line("args", dash(strings.Join(c.Args, " ")))
		line("cwd", dash(c.Cwd))
		line("env", dash(masked(c.Env)))
	} else {
		line("url", c.URL)
		line("bearer_token_env_var", dash(c.BearerTokenEnvVar))
		line("http_headers", dash(masked(c.HTTPHeaders)))
		line("env_http_headers", dash(pairs(c.EnvHTTPHeaders)))
	}
	line("auth", e.Auth.Text())
	if e.Error != "" {
		line("error", e.Error)
	}
	if c.StartupTimeoutSec != nil || c.StartupTimeoutMs != nil {
		line("startup_timeout_sec", strconv.FormatFloat(c.StartupTimeout().Seconds(), 'f', -1, 64))
	}
	if c.ToolTimeoutSec != nil {
		line("tool_timeout_sec", strconv.FormatFloat(*c.ToolTimeoutSec, 'f', -1, 64))
	}
	if c.DefaultToolsApprovalMode != "" {
		line("default_tools_approval_mode", string(c.DefaultToolsApprovalMode))
	}
	if e.Auth == mcp.AuthNotLoggedIn {
		line("login", "uah mcp login "+e.Name)
	}
	line("remove", "uah mcp remove "+e.Name)
}

// jsonServer is Codex's `mcp list --json` entry; get adds the tool lists.
type jsonServer struct {
	Name              string        `json:"name"`
	Enabled           bool          `json:"enabled"`
	Transport         jsonTransport `json:"transport"`
	StartupTimeoutSec *float64      `json:"startup_timeout_sec"`
	ToolTimeoutSec    *float64      `json:"tool_timeout_sec"`
	AuthStatus        string        `json:"auth_status"`
	Error             string        `json:"error,omitempty"`
	EnabledTools      []string      `json:"enabled_tools,omitempty"`
	DisabledTools     []string      `json:"disabled_tools,omitempty"`
}

type jsonTransport struct {
	Type              string            `json:"type"`
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	EnvVars           []string          `json:"env_vars,omitempty"`
	Cwd               string            `json:"cwd,omitempty"`
	URL               string            `json:"url,omitempty"`
	BearerTokenEnvVar string            `json:"bearer_token_env_var,omitempty"`
	HTTPHeaders       map[string]string `json:"http_headers,omitempty"`
	EnvHTTPHeaders    map[string]string `json:"env_http_headers,omitempty"`
}

func jsonEntries(entries []app.MCPEntry, tools bool) []jsonServer {
	out := make([]jsonServer, 0, len(entries))
	for _, e := range entries {
		c := e.Config
		s := jsonServer{
			Name: e.Name, Enabled: c.IsEnabled(), ToolTimeoutSec: c.ToolTimeoutSec, AuthStatus: string(e.Auth), Error: e.Error,
			Transport: jsonTransport{
				Type: c.Transport(), Command: c.Command, Args: c.Args, Env: c.Env, EnvVars: c.EnvVars, Cwd: c.Cwd,
				URL: c.URL, BearerTokenEnvVar: c.BearerTokenEnvVar, HTTPHeaders: c.HTTPHeaders, EnvHTTPHeaders: c.EnvHTTPHeaders,
			},
		}
		if c.StartupTimeoutSec != nil || c.StartupTimeoutMs != nil {
			secs := c.StartupTimeout().Seconds()
			s.StartupTimeoutSec = &secs
		}
		if tools {
			s.EnabledTools, s.DisabledTools = c.EnabledTools, c.DisabledTools
		}
		out = append(out, s)
	}

	return out
}

func enabled(c mcp.ServerConfig) string {
	if c.IsEnabled() {
		return "enabled"
	}

	return "disabled"
}

// masked shows keys, not values, as Codex does for env and headers.
func masked(m map[string]string) string {
	keys := slices.Sorted(maps.Keys(m))
	for i, k := range keys {
		keys[i] = k + "=*****"
	}

	return strings.Join(keys, ", ")
}

func pairs(m map[string]string) string {
	keys := slices.Sorted(maps.Keys(m))
	for i, k := range keys {
		keys[i] = k + "=" + m[k]
	}

	return strings.Join(keys, ", ")
}

func dash(s string) string {
	if s == "" {
		return "-"
	}

	return s
}
