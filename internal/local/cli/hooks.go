package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// hookInfo is one row reported by `rex hooks list`.
type hookInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Scope      string `json:"scope"` // "workspace" or "global"
	Executable bool   `json:"executable"`
	Timeout    string `json:"timeout"` // "default (30s)" until sidecar parsing lands
}

// newHooksCmd returns the `rex hooks` parent and wires its only v1 leaf.
func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Inspect installed hooks",
		Long: `Hooks are file-based observers that fire on workspace events. Per
specs/hooks.yaml, v1 supports only post-event observers; pre-event
gating is deferred.`,
	}
	cmd.AddCommand(newHooksListCmd())
	return cmd
}

func newHooksListCmd() *cobra.Command {
	var (
		workspaceFlag string
		globalFlag    string
		skipGlobal    bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed hooks (per-workspace and global)",
		Long: `Walks .rex/hooks/ in the current workspace plus
~/.config/rex/hooks/ globally, reporting each hook's name, scope,
executable status, and configured timeout. Per-hook timeout is
parsed from the sidecar <event-name>.config.toml — until TOML
reading lands, every hook is reported with the default 30s.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var hooks []hookInfo
			root, err := workspaceRootFor(workspaceFlag)
			if err != nil {
				return err
			}
			if root != "" {
				ws, err := scanHooksDir(filepath.Join(root, metaDirName, "hooks"), "workspace")
				if err != nil {
					return err
				}
				hooks = append(hooks, ws...)
			}

			if !skipGlobal {
				dir := globalFlag
				if dir == "" {
					if g, err := globalHooksDir(); err == nil {
						dir = g
					}
				}
				if dir != "" {
					gh, err := scanHooksDir(dir, "global")
					if err != nil {
						return err
					}
					hooks = append(hooks, gh...)
				}
			}

			sort.Slice(hooks, func(i, j int) bool {
				if hooks[i].Scope != hooks[j].Scope {
					return hooks[i].Scope < hooks[j].Scope
				}
				return hooks[i].Name < hooks[j].Name
			})

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				for _, h := range hooks {
					if err := enc.Encode(h); err != nil {
						return err
					}
				}
				return nil
			}
			if len(hooks) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no hooks installed")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSCOPE\tEXECUTABLE\tTIMEOUT\tPATH")
			for _, h := range hooks {
				exec := "yes"
				if !h.Executable {
					exec = "no (skipped)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", h.Name, h.Scope, exec, h.Timeout, h.Path)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "workspace root (default: walk up from cwd)")
	cmd.Flags().StringVar(&globalFlag, "global-dir", "", "override global hooks directory (default: platform user config)")
	cmd.Flags().BoolVar(&skipGlobal, "no-global", false, "skip the global hooks directory")
	return cmd
}

// scanHooksDir walks dir for hook files and returns one hookInfo per
// non-directory entry. Sidecar config files (`<event>.config.toml`)
// are filtered out so they do not appear as hooks themselves. Returns
// an empty slice if dir does not exist (the natural pre-init state).
func scanHooksDir(dir, scope string) ([]hookInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	out := make([]hookInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isSidecarConfig(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		out = append(out, hookInfo{
			Name:       e.Name(),
			Path:       path,
			Scope:      scope,
			Executable: info.Mode()&0o111 != 0,
			Timeout:    "default (30s)",
		})
	}
	return out, nil
}

func isSidecarConfig(name string) bool {
	const suffix = ".config.toml"
	return len(name) > len(suffix) && name[len(name)-len(suffix):] == suffix
}

// globalHooksDir returns ~/.config/rex/hooks following storage.GLOBAL.1.
// On macOS, follows the platform convention (Application Support); on
// other systems uses XDG / fallback. Until storage.global-config-layout
// lands this is the minimum needed by `rex hooks list`.
func globalHooksDir() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "rex", "hooks"), nil
}
