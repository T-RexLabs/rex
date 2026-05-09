package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/mcpserver"
)

// newMCPCmd implements the `rex mcp` subcommand
// (tools.INTROSPECT.1). It runs Rex's built-in MCP server on
// stdio, exposing the read-mostly tool surface to whichever
// harness session attached it via session/new's mcpServers
// parameter.
//
// Lifecycle: the command is invoked by harness adapters as a
// subprocess. Rex's StartHarnessRun appends a `{name: "rex",
// command: ["rex", "mcp", "--workspace", <root>]}` entry to the
// session's mcpServer list (INTROSPECT.2); the harness spawns
// us, we run until stdin closes, the harness collects our
// reply.
func newMCPCmd() *cobra.Command {
	var workspaceFlag string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run Rex's built-in MCP server on stdio",
		Long: `Speaks JSON-RPC NDJSON on stdio per the Model Context Protocol;
exposes the rex.spec.* / rex.events.* / rex.workspace.brief tool
surface so a harness attached to this server can introspect the
workspace mid-session.

Normally invoked as a subprocess of a harness Rex spawned (the
runner auto-attaches it to every session/new). Run it manually
when debugging a tool definition or wiring up a new harness:

  rex mcp --workspace /path/to/workspace
  # then drive it from another process via JSON-RPC NDJSON

Per tools.INTROSPECT.3 the v1 surface is read-only; mutating
tools land once the permission integration ships.`,
		Hidden: true, // not user-facing; rendered in --help only intentionally
		RunE: func(cmd *cobra.Command, args []string) error {
			root := workspaceFlag
			if root == "" {
				if r, err := workspaceRootFor(""); err == nil {
					root = r
				}
			}
			if root == "" {
				return fmt.Errorf("rex mcp: --workspace is required (or run inside a workspace)")
			}
			info := mcpserver.ServerInfo{
				Name:    "rex",
				Version: rootVersion(cmd),
			}
			srv := mcpserver.New(info)
			mcpserver.WorkspaceTools(srv, root)
			return srv.Serve(cmd.Context(), os.Stdin, os.Stdout)
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root (default: walk up from cwd)")
	return cmd
}

// rootVersion looks up the version string the root command was
// initialised with so the MCP server's serverInfo carries the
// same value as `rex --version`.
func rootVersion(cmd *cobra.Command) string {
	r := cmd.Root()
	if r == nil || r.Version == "" {
		return "dev"
	}
	return r.Version
}
