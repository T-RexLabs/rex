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
	"github.com/asabla/rex/internal/core/search"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/local/registry"
)

// registryFlagName lets tests redirect the registry path to a
// tempdir without mutating $XDG_CONFIG_HOME / $HOME globally. Empty
// means use registry.DefaultPath().
const registryFlagName = "registry-file"

// addRegistryFlag installs --registry-file on a workspace
// subcommand so tests (and power users) can target an alternate
// registry.toml location.
func addRegistryFlag(cmd *cobra.Command) {
	cmd.Flags().String(registryFlagName, "", "override registry path (default: platform user-config dir)")
}

// resolveRegistryPath returns the effective registry path: explicit
// --registry-file wins, otherwise the platform default applies.
func resolveRegistryPath(cmd *cobra.Command) (string, error) {
	if v, _ := cmd.Flags().GetString(registryFlagName); v != "" {
		return v, nil
	}
	return registry.DefaultPath()
}

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
		Example: `  rex workspace init ./demo --id demo --name Demo
  rex workspace show
  rex workspace reindex --workspace /path/to/ws`,
	}
	setRelated(cmd,
		"rex workspace init",
		"rex workspace show",
		"rex workspace reindex",
	)
	cmd.AddCommand(newWorkspaceInitCmd())
	cmd.AddCommand(newWorkspaceShowCmd())
	cmd.AddCommand(newWorkspaceListCmd())
	cmd.AddCommand(newWorkspaceReindexCmd())
	cmd.AddCommand(newWorkspaceArchiveCmd())
	cmd.AddCommand(newWorkspaceUnarchiveCmd())
	cmd.AddCommand(newWorkspaceDeleteCmd())
	return cmd
}

// Workspace state values per workspace.LIFE.3.
const (
	workspaceStateActive   = "active"
	workspaceStateArchived = "archived"
	workspaceStateDeleted  = "deleted"
)

func newWorkspaceArchiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Mark the workspace as archived",
		Long: `Transitions the workspace to state=archived (workspace.LIFE.3).
Reversible via 'rex workspace unarchive'. The on-disk content stays
untouched; only workspace.yaml's state field flips and an audit
event is recorded.`,
		Example: `  rex workspace archive
  rex workspace --workspace /path/to/ws archive`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceTransition(cmd, transitionArchive)
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd, "rex workspace unarchive", "rex workspace delete", "rex workspace show")
	return cmd
}

func newWorkspaceUnarchiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "unarchive",
		Short:   "Restore an archived workspace to active",
		Long:    `Transitions the workspace from archived back to active (workspace.LIFE.3.1). Refuses on workspaces that are already active or have been deleted.`,
		Example: `  rex workspace unarchive`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceTransition(cmd, transitionUnarchive)
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd, "rex workspace archive", "rex workspace show")
	return cmd
}

func newWorkspaceDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Mark the workspace as deleted (reversible only via snapshot restore)",
		Long: `Transitions the workspace to state=deleted (workspace.LIFE.3 /
LIFE.3.1). Unlike 'archive', this is reversible only via 'rex
snapshot restore'. The on-disk files are NOT removed by this command
— the marker just hides the workspace from active surfaces and
records the intent in the audit log.`,
		Example: `  rex workspace delete`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkspaceTransition(cmd, transitionDelete)
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd, "rex snapshot list", "rex snapshot restore <id>", "rex workspace show")
	return cmd
}

// transition enumerates the three lifecycle moves. Kept tiny and
// declarative so runWorkspaceTransition can branch on it without
// re-implementing the rules in each command's RunE.
type transition int

const (
	transitionArchive transition = iota
	transitionUnarchive
	transitionDelete
)

// runWorkspaceTransition validates the requested move against the
// current state, updates workspace.yaml via yaml.Node round-trip
// (so unknown user-set fields like default_repo survive), and
// emits the matching audit-class workspace.* event.
func runWorkspaceTransition(cmd *cobra.Command, t transition) error {
	root, err := strictWorkspaceRoot(cmd)
	if err != nil {
		return err
	}
	settings, err := readWorkspaceSettings(root)
	if err != nil {
		return err
	}
	from := settings.State
	if from == "" {
		from = workspaceStateActive
	}

	to, eventType, err := resolveTransition(t, from)
	if err != nil {
		return err
	}
	if from == to {
		// idempotent no-op; communicate clearly.
		fmt.Fprintf(cmd.OutOrStdout(), "workspace %q is already %s\n", settings.ID, to)
		return nil
	}

	if err := setWorkspaceStateField(root, to); err != nil {
		return fmt.Errorf("update workspace.yaml: %w", err)
	}

	if err := emitWorkspaceTransition(cmd, root, eventType, audit.WorkspaceStateChangedEvent{
		Name: settings.Name,
		From: from,
		To:   to,
		At:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}

	if jsonOutput(cmd) {
		return writeJSON(cmd, map[string]any{
			"id":   settings.ID,
			"from": from,
			"to":   to,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(), "workspace %q: %s -> %s\n", settings.ID, from, to)
	return nil
}

// resolveTransition turns (current state, requested move) into
// (target state, audit-event type) or an error if the move isn't
// allowed. Rules per workspace.LIFE.3 / LIFE.3.1:
//
//	archive:    active -> archived
//	unarchive:  archived -> active
//	delete:     active|archived -> deleted (terminal under normal flow)
func resolveTransition(t transition, from string) (string, string, error) {
	switch t {
	case transitionArchive:
		switch from {
		case workspaceStateActive:
			return workspaceStateArchived, audit.EventTypeWorkspaceArchived, nil
		case workspaceStateArchived:
			return workspaceStateArchived, "", nil // idempotent
		case workspaceStateDeleted:
			return "", "", errors.New("workspace is deleted; restore via `rex snapshot restore` first")
		}
	case transitionUnarchive:
		switch from {
		case workspaceStateArchived:
			return workspaceStateActive, audit.EventTypeWorkspaceUnarchived, nil
		case workspaceStateActive:
			return workspaceStateActive, "", nil // idempotent
		case workspaceStateDeleted:
			return "", "", errors.New("workspace is deleted; restore via `rex snapshot restore` first")
		}
	case transitionDelete:
		switch from {
		case workspaceStateActive, workspaceStateArchived:
			return workspaceStateDeleted, audit.EventTypeWorkspaceDeleted, nil
		case workspaceStateDeleted:
			return workspaceStateDeleted, "", nil // idempotent
		}
	}
	return "", "", fmt.Errorf("workspace state %q does not allow this transition", from)
}

// setWorkspaceStateField does a yaml.Node round-trip to update the
// `state` key in .rex/workspace.yaml without disturbing other
// top-level keys (id, name, repos, default_repo, harness_defaults,
// etc.). Same shape as repo.go's saveRepoEntries.
func setWorkspaceStateField(root, state string) error {
	path := filepath.Join(root, metaDirName, "workspace.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("%s: not a YAML document", path)
	}
	root0 := doc.Content[0]
	if root0.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: top level is not a mapping", path)
	}
	value := &yaml.Node{Kind: yaml.ScalarNode, Value: state, Tag: "!!str"}
	setKey(root0, "state", value)

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	return os.WriteFile(path, out, 0o644)
}

// emitWorkspaceTransition appends an audit-class workspace.* event
// to the workspace event log, mirroring emitRepoEvent / emitScheduleEvent.
func emitWorkspaceTransition(cmd *cobra.Command, root, eventType string, payload audit.WorkspaceStateChangedEvent) error {
	settings, err := readWorkspaceSettings(root)
	if err != nil {
		return err
	}
	signer, err := loadOrCreateDefaultSigner(cmd)
	if err != nil {
		return err
	}

	global, _ := globalHooksDir()
	disp := hooks.New(hooks.Options{
		WorkspaceRoot:  root,
		GlobalHooksDir: global,
	})
	defer disp.Drain()

	searchIdx, idxErr := search.Open(root)
	if idxErr == nil {
		defer searchIdx.Close()
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: search index unavailable: %v\n", idxErr)
	}
	indexerCB := search.EventIndexer(searchIdx, func(err error) {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: index event: %v\n", err)
	})
	onAppend := func(rec eventlog.Record) {
		disp.OnAppend(rec)
		indexerCB(rec)
	}

	writer, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        eventLogPath(root),
		WorkspaceID: settings.ID,
		Actor:       signer.Actor().String(),
		Sign:        identity.SignFunc(signer),
		OnAppend:    onAppend,
	})
	if err != nil {
		return fmt.Errorf("open events.log: %w", err)
	}
	defer writer.Close()

	payload.WorkspaceID = settings.ID
	if _, err := audit.NewAppender(writer).Append(eventType, payload); err != nil {
		return fmt.Errorf("emit %s: %w", eventType, err)
	}
	return nil
}

func newWorkspaceReindexCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Drop and rebuild .rex/index.sqlite from events.log + specs/",
		Long: `Per storage.INDEX.2, reindex deterministically rebuilds the local
search index from the canonical event log and the workspace's
git-merged content. Safe to run while the workspace is otherwise
idle; not safe during concurrent writes.`,
		Example: `  rex workspace reindex
  rex workspace reindex --workspace /path/to/ws`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			idx, err := search.Open(root)
			if err != nil {
				return err
			}
			defer idx.Close()

			stats, err := idx.Rebuild(root)
			if err != nil {
				return err
			}

			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"events": stats.Events,
					"specs":  stats.Specs,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"reindexed: %d event(s), %d spec(s)\n",
				stats.Events, stats.Specs)
			return nil
		},
	}
	setRelated(cmd,
		"rex status",
		"rex search <query>",
		"rex workspace show",
	)
	cmd.Flags().String(workspaceFlagName, "", "workspace root (default: walk up from cwd)")
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
		Example: `  rex workspace init
  rex workspace init ./demo --id demo --name Demo
  rex workspace init ./demo --force`,
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

			// Open the search index now so the very first event
			// (workspace.created) lands in both events.log and
			// the FTS index. A nil index falls back to a no-op
			// callback in EventIndexer; we surface the open error
			// to the user but do not abort init — the workspace
			// is still usable, the user can `rex workspace
			// reindex` later.
			searchIdx, idxErr := search.Open(abs)
			if idxErr == nil {
				defer searchIdx.Close()
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: search index unavailable: %v\n", idxErr)
			}
			indexerCB := search.EventIndexer(searchIdx, func(err error) {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: index event: %v\n", err)
			})
			onAppend := func(rec eventlog.Record) {
				disp.OnAppend(rec)
				indexerCB(rec)
			}

			writer, err := eventlog.OpenWriter(eventlog.WriterConfig{
				Path:        eventLogPath(abs),
				WorkspaceID: id,
				Actor:       signer.Actor().String(),
				Sign:        identity.SignFunc(signer),
				OnAppend:    onAppend,
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

			// Register the new workspace in the global registry
			// (workspace.LIFE.4). Best-effort: a failure to write
			// the registry doesn't fail init — the workspace
			// itself is fully usable from cwd; the registry just
			// powers `rex workspace list` discovery.
			if err := registerWorkspace(cmd, id, abs, ""); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: register %s: %v\n", id, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Initialized rex workspace %q at %s (signed as %s)\n",
				id, abs, signer.Actor())
			return nil
		},
	}
	setRelated(cmd,
		"rex workspace show",
		"rex workspace reindex",
		"rex status",
	)
	cmd.Flags().StringVar(&idFlag, "id", "", "workspace id (default: kebab-cased basename of path)")
	cmd.Flags().StringVar(&nameFlag, "name", "", "human-readable workspace name (default: basename of path)")
	cmd.Flags().Bool("force", false, "overwrite an existing .rex/ directory at the target")
	cmd.Flags().String("identity-dir", "", "override identity store path (default: platform user-config dir/rex/identity/)")
	addRegistryFlag(cmd)
	return cmd
}

// registerWorkspace upserts a registry entry for the workspace at
// abs. Empty `remote` means a local-only workspace; clone will pass
// the originating remote alias when that command lands.
func registerWorkspace(cmd *cobra.Command, id, abs, remote string) error {
	path, err := resolveRegistryPath(cmd)
	if err != nil {
		return err
	}
	reg, err := registry.Load(path)
	if err != nil {
		return err
	}
	reg.Upsert(registry.Entry{ID: id, Path: abs, Remote: remote})
	return registry.Save(path, reg)
}

func newWorkspaceShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show [path]",
		Short: "Show the workspace at the given path (default: walk up from cwd)",
		Long: `Resolves a workspace from the supplied path (or the current working
directory when omitted) and prints its settings and content counts.`,
		Example: `  rex workspace show
  rex workspace show /path/to/ws
  rex workspace show .`,
		Args: cobra.MaximumNArgs(1),
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
	setRelated(cmd,
		"rex status",
		"rex spec list",
		"rex workspace reindex",
	)
	return cmd
}

func newWorkspaceListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List locally-known workspaces",
		Long: `Reads ~/.config/rex/registry.toml (workspace.LIFE.4 / storage.GLOBAL.4)
and prints one row per registered workspace. Entries whose on-disk
path no longer exists are flagged as "(missing)" but still listed —
removing them is a deliberate user action, not list's job.

When the registry is empty AND cwd is inside a workspace, falls back
to showing that workspace as a single row with the source column
"(cwd)" so a fresh user gets feedback before their first init/clone.`,
		Example: `  rex workspace list
  rex workspace list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveRegistryPath(cmd)
			if err != nil {
				return err
			}
			reg, err := registry.Load(path)
			if err != nil {
				return err
			}
			entries := reg.List()

			if len(entries) == 0 {
				return printWorkspaceListFallback(cmd)
			}

			rows := make([]workspaceListRow, 0, len(entries))
			for _, e := range entries {
				row := workspaceListRow{
					ID:     e.ID,
					Path:   e.Path,
					Remote: e.Remote,
					State:  workspaceListProbeState(e.Path),
					Source: "registry",
				}
				rows = append(rows, row)
			}
			return printWorkspaceList(cmd, rows)
		},
	}
	addRegistryFlag(cmd)
	return cmd
}

// workspaceListRow is the on-screen / on-JSON shape for one row of
// `rex workspace list`. State is sourced from disk (workspace.yaml)
// so a registry entry that survives a workspace deletion still
// surfaces something useful — "(missing)" in that case.
type workspaceListRow struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Remote string `json:"remote,omitempty"`
	State  string `json:"state"`
	Source string `json:"source"` // "registry" | "(cwd)"
}

func printWorkspaceList(cmd *cobra.Command, rows []workspaceListRow) error {
	if jsonOutput(cmd) {
		return writeJSON(cmd, rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no workspaces registered")
		return nil
	}
	for _, r := range rows {
		remote := r.Remote
		if remote == "" {
			remote = "-"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-12s %-10s %-10s %s\n",
			r.ID, r.State, remote, r.Source, r.Path)
	}
	return nil
}

// printWorkspaceListFallback handles the empty-registry case by
// surfacing the cwd workspace if one is resolvable, mirroring the
// previous behaviour but with explicit Source="(cwd)" labelling.
func printWorkspaceListFallback(cmd *cobra.Command) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, ferr := findWorkspaceRoot(cwd)
	if errors.Is(ferr, errNoWorkspace) {
		fmt.Fprintln(cmd.OutOrStdout(),
			"No workspaces registered; cwd is not inside a workspace either.")
		return nil
	}
	if ferr != nil {
		return ferr
	}
	settings, err := readWorkspaceSettings(root)
	if err != nil {
		return err
	}
	row := workspaceListRow{
		ID:     settings.ID,
		Path:   root,
		State:  settings.State,
		Source: "(cwd)",
	}
	return printWorkspaceList(cmd, []workspaceListRow{row})
}

// workspaceListProbeState reads .rex/workspace.yaml at the given
// path and returns the state string. Returns "(missing)" if the
// path or workspace.yaml is gone — a registry entry whose backing
// dir no longer exists.
func workspaceListProbeState(absPath string) string {
	yamlPath := filepath.Join(absPath, metaDirName, "workspace.yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		return "(missing)"
	}
	body, err := os.ReadFile(yamlPath)
	if err != nil {
		return "(missing)"
	}
	var s workspaceSettings
	if err := yaml.Unmarshal(body, &s); err != nil {
		return "(unreadable)"
	}
	if s.State == "" {
		return "active"
	}
	return s.State
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
