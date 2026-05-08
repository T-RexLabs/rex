package cli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/cmdhelp"
)

const workspaceFlagName = "workspace"

func addWorkspacePersistentFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().String(workspaceFlagName, "", "workspace root (default: walk up from cwd)")
}

func workspaceFlagValue(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString(workspaceFlagName)
	return v
}

func currentWorkspaceRoot(cmd *cobra.Command) (string, error) {
	return workspaceRootFor(workspaceFlagValue(cmd))
}

func requiredWorkspaceRoot(cmd *cobra.Command) (string, error) {
	root, err := currentWorkspaceRoot(cmd)
	if err != nil {
		return "", err
	}
	if root == "" {
		return "", errNoWorkspace
	}
	return root, nil
}

func strictWorkspaceRoot(cmd *cobra.Command) (string, error) {
	return workspaceRootForOrError(workspaceFlagValue(cmd))
}

func jsonOutput(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// quietOutput reports whether --quiet is set on cmd or any parent.
// Wired alongside the root persistent flag (root.go) per cli.FMT.3
// so any leaf can suppress non-essential confirmation output for
// script callers without per-command boilerplate.
//
// Confirmation prints (cli.UX.4) check this and skip when true; the
// caller still gets the same info via exit code + --json output.
func quietOutput(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("quiet")
	return v
}

// printConfirmation is the small helper for "Commands that modify
// state print a one-line confirmation" (cli.UX.4). Honors --quiet
// (cli.FMT.3) and --json: under --json the JSON payload is the
// only stdout output anyway, so no confirmation text fires.
func printConfirmation(cmd *cobra.Command, format string, args ...any) {
	if quietOutput(cmd) || jsonOutput(cmd) {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), format, args...)
}

func writeJSON(cmd *cobra.Command, v any) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
}

func commandContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func installRelatedHelp(root *cobra.Command) {
	cmdhelp.InstallRelatedHelp(root)
}

func setRelated(cmd *cobra.Command, commands ...string) {
	cmdhelp.SetRelated(cmd, commands...)
}
