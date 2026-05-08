// Package cli — `rex repo` command tree (workspace.REPO.*).
//
// `rex repo add <git-url> [path]`   — clone + register
// `rex repo link <existing-path>`    — register without cloning
// `rex repo list`                    — list registered repos
// `rex repo remove <name>`           — unregister; --purge also deletes
//
// The shape of one entry in workspace.yaml's `repos` list is
// pinned by workspace.SETTINGS.3.1 (name + path + optional url).
// Audit events are emitted per workspace.REPO.4.1.
package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/search"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// repoEntry is one element of workspace.yaml's `repos` list per
// workspace.SETTINGS.3.1. URL is empty iff the repo was added via
// `rex repo link`.
type repoEntry struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
	URL  string `yaml:"url,omitempty"`
}

// newRepoCmd returns the `rex repo` parent. Wired in root.go.
func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Attach repositories to a Rex workspace",
		Long: `Manage the workspace's attached repositories. A workspace may have
zero, one, or many attached repos (workspace.REPO.1). v1 keeps repos
inside the workspace tree; paths in workspace.yaml are POSIX-relative
to the workspace root (workspace.SETTINGS.3.1).`,
		Example: `  rex repo add https://github.com/example/foo.git
  rex repo link ./vendored/bar
  rex repo list
  rex repo remove foo --purge`,
	}
	setRelated(cmd,
		"rex repo add",
		"rex repo link",
		"rex repo list",
		"rex workspace show",
	)
	cmd.AddCommand(newRepoAddCmd())
	cmd.AddCommand(newRepoLinkCmd())
	cmd.AddCommand(newRepoListCmd())
	cmd.AddCommand(newRepoRemoveCmd())
	return cmd
}

func newRepoAddCmd() *cobra.Command {
	var nameFlag string
	cmd := &cobra.Command{
		Use:   "add <git-url> [path]",
		Short: "Clone a repository into the workspace and register it",
		Long: `Clones <git-url> into the workspace tree and records the result in
workspace.yaml. [path] is the destination relative to the workspace
root (default: the URL's basename). Shells out to the system "git"
binary on PATH per workspace.REPO.2.1; fails up-front if git is not
available.`,
		Example: `  rex repo add https://github.com/example/foo.git
  rex repo add https://github.com/example/foo.git vendored/foo
  rex repo add https://github.com/example/foo.git --name foo-fork`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			url := strings.TrimSpace(args[0])
			if url == "" {
				return errors.New("git url is required")
			}
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			rel := ""
			if len(args) == 2 {
				rel = args[1]
			} else {
				rel = deriveRepoBasenameFromURL(url)
			}
			if rel == "" {
				return errors.New("could not derive a destination path from the url; pass an explicit [path]")
			}
			name := nameFlag
			if name == "" {
				name = filepath.Base(rel)
			}
			if err := validateRepoName(name); err != nil {
				return err
			}
			cleanRel, abs, err := resolveRepoPath(root, rel)
			if err != nil {
				return err
			}

			existing, err := loadRepoEntries(root)
			if err != nil {
				return err
			}
			if err := assertRepoSlotFree(existing, name, cleanRel); err != nil {
				return err
			}
			if _, err := os.Stat(abs); err == nil {
				return fmt.Errorf("destination %s already exists; remove it or pass a different [path]", abs)
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("stat %s: %w", abs, err)
			}

			gitBin, err := exec.LookPath("git")
			if err != nil {
				return errors.New("git: not found on PATH; rex repo add needs `git` (workspace.REPO.2.1)")
			}

			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return fmt.Errorf("create parent of %s: %w", abs, err)
			}
			clone := exec.CommandContext(commandContext(cmd), gitBin, "clone", url, abs)
			clone.Stdout = cmd.OutOrStdout()
			clone.Stderr = cmd.ErrOrStderr()
			if err := clone.Run(); err != nil {
				// Best-effort cleanup if clone left a partial dir.
				_ = os.RemoveAll(abs)
				return fmt.Errorf("git clone %s: %w", url, err)
			}

			entry := repoEntry{Name: name, Path: cleanRel, URL: url}
			if err := saveRepoEntries(root, append(existing, entry)); err != nil {
				// We cloned but failed to register; clean up the
				// clone so the next attempt is idempotent.
				_ = os.RemoveAll(abs)
				return fmt.Errorf("update workspace.yaml: %w", err)
			}

			if err := emitRepoEvent(cmd, root, audit.EventTypeRepoAdded, audit.RepoAddedEvent{
				Name: name, Path: cleanRel, URL: url,
			}); err != nil {
				return err
			}

			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"name": name, "path": cleanRel, "url": url,
				})
			}
			printConfirmation(cmd, "added repo %q at %s (cloned from %s)\n", name, cleanRel, url)
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	cmd.Flags().StringVar(&nameFlag, "name", "", "registered name (default: basename of the destination path)")
	setRelated(cmd, "rex repo link", "rex repo list", "rex repo remove")
	return cmd
}

func newRepoLinkCmd() *cobra.Command {
	var nameFlag string
	cmd := &cobra.Command{
		Use:   "link <existing-path>",
		Short: "Register an existing on-disk directory as a workspace repo",
		Long: `Records an existing directory as an attached repo without cloning
(workspace.REPO.3). The path must resolve to a directory under the
workspace tree; v1 stores it as a POSIX-relative path
(workspace.SETTINGS.3.1).`,
		Example: `  rex repo link ./vendored/bar
  rex repo link existing/checkout --name bar`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			cleanRel, abs, err := resolveRepoPath(root, args[0])
			if err != nil {
				return err
			}
			info, err := os.Stat(abs)
			if err != nil {
				return fmt.Errorf("stat %s: %w", abs, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s is not a directory", abs)
			}
			name := nameFlag
			if name == "" {
				name = filepath.Base(cleanRel)
			}
			if err := validateRepoName(name); err != nil {
				return err
			}
			existing, err := loadRepoEntries(root)
			if err != nil {
				return err
			}
			if err := assertRepoSlotFree(existing, name, cleanRel); err != nil {
				return err
			}

			entry := repoEntry{Name: name, Path: cleanRel}
			if err := saveRepoEntries(root, append(existing, entry)); err != nil {
				return fmt.Errorf("update workspace.yaml: %w", err)
			}
			if err := emitRepoEvent(cmd, root, audit.EventTypeRepoLinked, audit.RepoLinkedEvent{
				Name: name, Path: cleanRel,
			}); err != nil {
				return err
			}
			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"name": name, "path": cleanRel,
				})
			}
			printConfirmation(cmd, "linked repo %q at %s\n", name, cleanRel)
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	cmd.Flags().StringVar(&nameFlag, "name", "", "registered name (default: basename of the path)")
	setRelated(cmd, "rex repo add", "rex repo list", "rex repo remove")
	return cmd
}

func newRepoListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List repositories registered in workspace.yaml",
		Long: `Reads the ` + "`repos`" + ` list from .rex/workspace.yaml and prints one row
per entry with name, relative path, and origin (the clone URL for
added repos, the literal "linked" for linked ones).`,
		Example: `  rex repo list
  rex repo list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			entries, err := loadRepoEntries(root)
			if err != nil {
				return err
			}
			if jsonOutput(cmd) {
				out := make([]map[string]any, 0, len(entries))
				for _, e := range entries {
					row := map[string]any{"name": e.Name, "path": e.Path}
					if e.URL != "" {
						row["url"] = e.URL
					}
					out = append(out, row)
				}
				return writeJSON(cmd, out)
			}
			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no repos attached")
				return nil
			}
			for _, e := range entries {
				origin := "linked"
				if e.URL != "" {
					origin = e.URL
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-20s %-30s %s\n", e.Name, e.Path, origin)
			}
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd, "rex repo add", "rex repo link", "rex repo remove")
	return cmd
}

func newRepoRemoveCmd() *cobra.Command {
	var purgeFlag bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Unregister a repo from workspace.yaml",
		Long: `Removes the named repo from workspace.yaml. By default the working
copy stays on disk; --purge also deletes it (workspace.REPO.4).`,
		Example: `  rex repo remove foo
  rex repo remove foo --purge`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			entries, err := loadRepoEntries(root)
			if err != nil {
				return err
			}
			idx := -1
			for i, e := range entries {
				if e.Name == name {
					idx = i
					break
				}
			}
			if idx == -1 {
				return fmt.Errorf("no repo named %q in workspace.yaml", name)
			}
			gone := entries[idx]
			next := append(entries[:idx:idx], entries[idx+1:]...)
			if err := saveRepoEntries(root, next); err != nil {
				return fmt.Errorf("update workspace.yaml: %w", err)
			}

			purged := false
			if purgeFlag {
				abs := filepath.Join(root, filepath.FromSlash(gone.Path))
				// Defensive: re-check the path is under root before
				// the rm. resolveRepoPath already validated this on
				// add/link, but workspace.yaml is human-editable so
				// re-check at delete time too.
				if err := assertUnderRoot(root, abs); err != nil {
					return err
				}
				if err := os.RemoveAll(abs); err != nil {
					return fmt.Errorf("purge %s: %w", abs, err)
				}
				purged = true
			}

			if err := emitRepoEvent(cmd, root, audit.EventTypeRepoRemoved, audit.RepoRemovedEvent{
				Name:   gone.Name,
				Path:   gone.Path,
				Purged: purged,
			}); err != nil {
				return err
			}

			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"name":   gone.Name,
					"path":   gone.Path,
					"purged": purged,
				})
			}
			suffix := ""
			if purged {
				suffix = " (working copy deleted)"
			}
			printConfirmation(cmd, "removed repo %q%s\n", gone.Name, suffix)
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	cmd.Flags().BoolVar(&purgeFlag, "purge", false, "also delete the working copy on disk")
	setRelated(cmd, "rex repo add", "rex repo link", "rex repo list")
	return cmd
}

// --- helpers ---------------------------------------------------------

// validateRepoName enforces kebab-case identifiers per
// workspace.SETTINGS.3.1.
func validateRepoName(name string) error {
	if name == "" {
		return errors.New("repo name is required")
	}
	if !specfmt.IsKebab(name) {
		return fmt.Errorf("repo name %q is not kebab-case (lowercase letters, digits, hyphens)", name)
	}
	return nil
}

// resolveRepoPath turns a user-supplied path into the
// (POSIX-relative, absolute) pair, refusing anything that escapes
// the workspace root after normalization (workspace.SETTINGS.3.1).
func resolveRepoPath(root, raw string) (relPosix string, abs string, err error) {
	if strings.TrimSpace(raw) == "" {
		return "", "", errors.New("repo path is required")
	}
	cleaned := filepath.FromSlash(raw)
	var absPath string
	if filepath.IsAbs(cleaned) {
		absPath = filepath.Clean(cleaned)
	} else {
		absPath = filepath.Clean(filepath.Join(root, cleaned))
	}
	if err := assertUnderRoot(root, absPath); err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(root, absPath)
	if err != nil {
		return "", "", fmt.Errorf("compute relative path: %w", err)
	}
	if rel == "." || rel == "" {
		return "", "", errors.New("repo path cannot be the workspace root itself")
	}
	return filepath.ToSlash(rel), absPath, nil
}

// assertUnderRoot verifies abs resolves to a location under root.
// It tolerates root or abs not existing yet (so it's safe for both
// add-before-clone and remove-after-purge).
func assertUnderRoot(root, abs string) error {
	rootClean := filepath.Clean(root)
	absClean := filepath.Clean(abs)
	rel, err := filepath.Rel(rootClean, absClean)
	if err != nil {
		return fmt.Errorf("compute relative path: %w", err)
	}
	if rel == "." {
		return nil
	}
	if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("path %s escapes the workspace root %s", absClean, rootClean)
	}
	return nil
}

// assertRepoSlotFree returns an error if name or path collides
// with an existing entry. The path comparison uses POSIX-relative
// strings (the on-disk shape), case-sensitive — collisions on a
// case-insensitive filesystem are caught by the disk check
// elsewhere.
func assertRepoSlotFree(existing []repoEntry, name, relPosix string) error {
	for _, e := range existing {
		if e.Name == name {
			return fmt.Errorf("a repo named %q is already registered", name)
		}
		if e.Path == relPosix {
			return fmt.Errorf("path %s is already registered as repo %q", relPosix, e.Name)
		}
	}
	return nil
}

// deriveRepoBasenameFromURL extracts the repository basename from
// a git URL: "https://github.com/foo/bar.git" -> "bar".
func deriveRepoBasenameFromURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimSuffix(url, "/")
	if url == "" {
		return ""
	}
	// Take everything after the last separator, accepting both
	// posix slashes and the SCP-style "host:path" form.
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		url = url[i+1:]
	}
	return strings.TrimSuffix(url, ".git")
}

// loadRepoEntries reads .rex/workspace.yaml and returns its
// `repos` list. Returns an empty slice if the file lacks the key.
func loadRepoEntries(root string) ([]repoEntry, error) {
	body, err := os.ReadFile(workspaceYAMLPath(root))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", workspaceYAMLPath(root), err)
	}
	var doc struct {
		Repos []repoEntry `yaml:"repos"`
	}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", workspaceYAMLPath(root), err)
	}
	if doc.Repos == nil {
		return []repoEntry{}, nil
	}
	for i, e := range doc.Repos {
		if err := validateRepoName(e.Name); err != nil {
			return nil, fmt.Errorf("workspace.yaml repos[%d]: %w", i, err)
		}
		if e.Path == "" {
			return nil, fmt.Errorf("workspace.yaml repos[%d]: path is required", i)
		}
		if err := assertUnderRoot(root, filepath.Join(root, filepath.FromSlash(e.Path))); err != nil {
			return nil, fmt.Errorf("workspace.yaml repos[%d]: %w", i, err)
		}
	}
	return doc.Repos, nil
}

// saveRepoEntries does a yaml.Node round-trip to update the
// `repos` key in workspace.yaml without disturbing other top-level
// keys (id, name, default_repo, harness_defaults, etc.). Order of
// existing keys is preserved.
func saveRepoEntries(root string, entries []repoEntry) error {
	path := workspaceYAMLPath(root)
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

	// Build the new value node for `repos`.
	var newValue yaml.Node
	if len(entries) == 0 {
		// Drop the key entirely when the list is empty so the file
		// stays clean (omitempty semantics for the YAML round-trip).
		removeKey(root0, "repos")
		// Carry on to write the document below.
	} else {
		if err := newValue.Encode(entries); err != nil {
			return fmt.Errorf("encode repos: %w", err)
		}
		setKey(root0, "repos", &newValue)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// setKey replaces the value of `key` in mapping, or appends a new
// (key, value) pair when `key` is absent. mapping.Kind must be
// yaml.MappingNode.
func setKey(mapping *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = value
			return
		}
	}
	keyNode := &yaml.Node{Kind: yaml.ScalarNode, Value: key, Tag: "!!str"}
	mapping.Content = append(mapping.Content, keyNode, value)
}

// removeKey drops the (key, value) pair from mapping if present.
func removeKey(mapping *yaml.Node, key string) {
	for i := 0; i < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content = append(mapping.Content[:i], mapping.Content[i+2:]...)
			return
		}
	}
}

func workspaceYAMLPath(root string) string {
	return filepath.Join(root, metaDirName, "workspace.yaml")
}

// emitRepoEvent opens an event-log writer over the workspace, fires
// hooks + indexer the same way `rex workspace init` does, and
// appends the audit-class repo.* event.
func emitRepoEvent(cmd *cobra.Command, root, eventType string, payload any) error {
	settings, err := readWorkspaceSettings(root)
	if err != nil {
		return err
	}
	signer, err := loadOrCreateDefaultSigner(cmd)
	if err != nil {
		return err
	}

	disp := newAuditingHookDispatcher(cmd, root)
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

	// audit.Append rejects unknown event types; we set workspace_id
	// on the payload here so callers don't have to thread settings.
	payloadWithWS := withWorkspaceID(payload, settings.ID)
	if _, err := audit.NewAppender(writer).Append(eventType, payloadWithWS); err != nil {
		return fmt.Errorf("emit %s: %w", eventType, err)
	}
	return nil
}

// withWorkspaceID stamps the workspace_id field on each payload
// type. The audit package keeps the field public on every event
// struct so we set it here directly.
func withWorkspaceID(payload any, workspaceID string) any {
	switch p := payload.(type) {
	case audit.RepoAddedEvent:
		p.WorkspaceID = workspaceID
		return p
	case audit.RepoLinkedEvent:
		p.WorkspaceID = workspaceID
		return p
	case audit.RepoRemovedEvent:
		p.WorkspaceID = workspaceID
		return p
	default:
		return payload
	}
}
