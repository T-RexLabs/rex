package cli

import (
	"context"
	"encoding/json"

	"github.com/asabla/rex/internal/cmdhelp"
	"github.com/spf13/cobra"
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
