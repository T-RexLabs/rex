package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/hooks"
	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// envIdentityDir is the environment variable callers can set to
// override the platform-default identity store path. Tests use this
// for session-wide isolation; production users normally pass
// --identity-dir or rely on the platform default.
const envIdentityDir = "REX_IDENTITY_DIR"

// loadOrCreateDefaultSigner returns a Signer over the user's default
// identity, generating a fresh keypair on first call. Resolution
// order: --identity-dir flag, REX_IDENTITY_DIR env var, platform
// user-config-dir.
func loadOrCreateDefaultSigner(cmd *cobra.Command) (identity.Signer, error) {
	dir, _ := cmd.Flags().GetString("identity-dir")
	if dir == "" {
		dir = os.Getenv(envIdentityDir)
	}
	if dir == "" {
		def, err := identity.DefaultStoreDir()
		if err != nil {
			return nil, err
		}
		dir = def
	}
	store := identity.NewStore(dir)
	return identity.EnsureDefaultStoreSigner(store)
}

// newWorkspaceCmd returns the `rex workspace` parent and wires its
// leaves.
func newWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Manage Rex workspaces",
		Long: `A workspace is a container of intent — repositories, specs, scheduled
work, hooks, and connected tools that share one identity and one
event log. See specs/workspace.yaml for the data model.`,
	}
	cmd.AddCommand(newWorkspaceInitCmd())
	cmd.AddCommand(newWorkspaceShowCmd())
	cmd.AddCommand(newWorkspaceListCmd())
	return cmd
}

// workspaceSettings is the on-disk shape of .rex/workspace.yaml per
// workspace.SETTINGS.2. Only the v1 required + first-class optional
// fields are wired here; future fields (default_template_id,
// harness_defaults, repos) are additive (overview.SYS.4).
type workspaceSettings struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	State       string `yaml:"state"`
	CreatedAt   string `yaml:"created_at"`
	Description string `yaml:"description,omitempty"`
}

// initSubdirs are the directories `rex workspace init` creates inside
// .rex/. Files (events.log, index.sqlite) are created lazily by the
// subsystems that own them; init only ensures the directory skeleton
// exists per storage.WS.2.
var initSubdirs = []string{
	"specs",
	"schedules",
	"templates",
	"hooks",
}

func newWorkspaceInitCmd() *cobra.Command {
	var (
		idFlag   string
		nameFlag string
	)
	cmd := &cobra.Command{
		Use:   "init [path]",
		Short: "Create a new Rex workspace at the given path (default: cwd)",
		Long: `Creates .rex/ with the directory skeleton from storage.WS.2 plus
.rex/workspace.yaml seeded with id, name, state=active, and
created_at. The id defaults to the kebab-cased basename of the
target path; --id and --name override.

Refuses to clobber an existing .rex/ directory; use --force when you
mean it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return fmt.Errorf("resolve %q: %w", path, err)
			}
			rexDir := filepath.Join(abs, metaDirName)
			force, _ := cmd.Flags().GetBool("force")
			if _, err := os.Stat(rexDir); err == nil && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite", rexDir)
			}

			id := idFlag
			if id == "" {
				id = deriveIDFromPath(abs)
			}
			if !specfmt.IsKebab(id) {
				return fmt.Errorf("id %q is not kebab-case; pass --id <kebab>", id)
			}
			name := nameFlag
			if name == "" {
				name = filepath.Base(abs)
			}

			if err := os.MkdirAll(rexDir, 0o755); err != nil {
				return fmt.Errorf("create %s: %w", rexDir, err)
			}
			for _, sub := range initSubdirs {
				if err := os.MkdirAll(filepath.Join(rexDir, sub), 0o755); err != nil {
					return fmt.Errorf("create %s: %w", sub, err)
				}
			}

			createdAt := time.Now().UTC().Format(time.RFC3339)
			settings := workspaceSettings{
				ID:        id,
				Name:      name,
				State:     "active",
				CreatedAt: createdAt,
			}
			body, err := yaml.Marshal(settings)
			if err != nil {
				return fmt.Errorf("marshal workspace.yaml: %w", err)
			}
			settingsPath := filepath.Join(rexDir, "workspace.yaml")
			if err := os.WriteFile(settingsPath, body, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", settingsPath, err)
			}

			// Default identity: auto-create on first init so the
			// workspace.created event can be signed.
			signer, err := loadOrCreateDefaultSigner(cmd)
			if err != nil {
				return err
			}

			// First persistent audit entry: the workspace exists.
			// We open the events.log writer directly here rather
			// than through newWorkspaceWriter (which reads back
			// workspace.yaml) — the file we just wrote is the
			// reality we want to record.
			//
			// A hook dispatcher is wired so hooks installed under
			// .rex/hooks/ (per-workspace) and the global hooks
			// dir fire after each event. Drain ensures hooks
			// finish before init returns.
			global, _ := globalHooksDir()
			disp := hooks.New(hooks.Options{
				WorkspaceRoot:  abs,
				GlobalHooksDir: global,
			})
			defer disp.Drain()

			writer, err := eventlog.OpenWriter(eventlog.WriterConfig{
				Path:        eventLogPath(abs),
				WorkspaceID: id,
				Actor:       signer.Actor().String(),
				Sign:        identity.SignFunc(signer),
				OnAppend:    disp.OnAppend,
			})
			if err != nil {
				return fmt.Errorf("open events.log: %w", err)
			}
			defer writer.Close()
			appender := audit.NewAppender(writer)
			if _, err := appender.Append(audit.EventTypeWorkspaceCreated, audit.WorkspaceCreatedEvent{
				WorkspaceID: id,
				Name:        name,
				Path:        abs,
				CreatedAt:   createdAt,
				CreatedBy:   signer.Actor().String(),
			}); err != nil {
				return fmt.Errorf("emit workspace.created: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Initialized rex workspace %q at %s (signed as %s)\n",
				id, abs, signer.Actor())
			return nil
		},
	}
	cmd.Flags().StringVar(&idFlag, "id", "", "workspace id (default: kebab-cased basename of path)")
	cmd.Flags().StringVar(&nameFlag, "name", "", "human-readable workspace name (default: basename of path)")
	cmd.Flags().Bool("force", false, "overwrite an existing .rex/ directory at the target")
	cmd.Flags().String("identity-dir", "", "override identity store path (default: platform user-config dir/rex/identity/)")
	return cmd
}

func newWorkspaceShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show [path]",
		Short: "Show the workspace at the given path (default: walk up from cwd)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			start := "."
			if len(args) == 1 {
				start = args[0]
			}
			root, err := findWorkspaceRoot(start)
			if err != nil {
				return err
			}
			settings, err := readWorkspaceSettings(root)
			if err != nil {
				return err
			}
			specs, _ := listSpecFiles(specDir(root))
			hooks, _ := countDirEntries(filepath.Join(root, metaDirName, "hooks"))
			schedules, _ := countDirEntries(filepath.Join(root, metaDirName, "schedules"))

			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"path":       root,
					"id":         settings.ID,
					"name":       settings.Name,
					"state":      settings.State,
					"created_at": settings.CreatedAt,
					"specs":      len(specs),
					"hooks":      hooks,
					"schedules":  schedules,
				})
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "path:       %s\n", root)
			fmt.Fprintf(out, "id:         %s\n", settings.ID)
			fmt.Fprintf(out, "name:       %s\n", settings.Name)
			fmt.Fprintf(out, "state:      %s\n", settings.State)
			fmt.Fprintf(out, "created_at: %s\n", settings.CreatedAt)
			fmt.Fprintf(out, "specs:      %d\n", len(specs))
			fmt.Fprintf(out, "hooks:      %d\n", hooks)
			fmt.Fprintf(out, "schedules:  %d\n", schedules)
			return nil
		},
	}
	return cmd
}

func newWorkspaceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List locally-known workspaces",
		Long: `Reads ~/.config/rex/registry.toml when present (workspace.LIFE.4).
Until storage.global-config-layout lands, list falls back to
showing the current workspace if cwd is inside one, and a
"no registry yet" hint otherwise.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			root, ferr := findWorkspaceRoot(cwd)
			if errors.Is(ferr, errNoWorkspace) {
				fmt.Fprintln(cmd.OutOrStdout(),
					"No global registry yet (deferred to storage.global-config-layout) and cwd is not inside a workspace.")
				return nil
			}
			if ferr != nil {
				return ferr
			}
			settings, err := readWorkspaceSettings(root)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(),
				"Global registry not yet implemented; showing the workspace at cwd:")
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\t%s\t%s\n", settings.ID, settings.State, root)
			return nil
		},
	}
}

// readWorkspaceSettings loads .rex/workspace.yaml for the given
// workspace root.
func readWorkspaceSettings(root string) (*workspaceSettings, error) {
	path := filepath.Join(root, metaDirName, "workspace.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s workspaceSettings
	if err := yaml.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &s, nil
}

// countDirEntries counts non-directory entries in dir. Returns 0 with
// nil error if dir does not exist.
func countDirEntries(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			count++
		}
	}
	return count, nil
}

var nonKebabRE = regexp.MustCompile(`[^a-z0-9-]+`)

// deriveIDFromPath turns a path's basename into a kebab-case id.
// "Code/My Project!" -> "my-project"
func deriveIDFromPath(p string) string {
	base := strings.ToLower(filepath.Base(p))
	base = nonKebabRE.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" || !specfmt.IsKebab(base) {
		return "workspace"
	}
	return base
}
