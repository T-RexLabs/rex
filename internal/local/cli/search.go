package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/search"
	"github.com/asabla/rex/internal/local/savedsearch"
)

// newSearchCmd returns the top-level `rex search` command per
// cli.SHAPE.2 (top-level shortcut for high-frequency operations).
// The default RunE runs an FTS query; the `saved` subcommand tree
// (cli.SEARCH.2 / search.SAVED.*) operates on named saved queries.
func newSearchCmd() *cobra.Command {
	var (
		limit int
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Full-text search the workspace's index",
		Long: `Runs an FTS5 query against .rex/index.sqlite (covering specs and
audit-log events). Workspace scope only in v1; --all-local /
--remote land alongside the global registry and remote-search work.

Tokens with hyphens, colons, or punctuation are auto-quoted so
identifier-shaped queries (kebab-case names, "type:event") behave
intuitively. AND / OR / NOT pass through unquoted.

Saved queries: ` + "`rex search saved <subcommand>`" + ` (search.SAVED.*).`,
		Example: `  rex search workspace.created
  rex search "type:event"
  rex search sync.ORDER.3 --workspace /path/to/ws
  rex search saved list`,
		Args: cobra.MinimumNArgs(1),
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

			query := joinArgs(args)
			results, err := idx.Search(query, search.SearchOptions{Limit: limit})
			if err != nil {
				return err
			}

			return renderSearchResults(cmd, results)
		},
	}
	setRelated(cmd,
		"rex status",
		"rex log tail",
		"rex spec list",
		"rex search saved list",
	)
	cmd.Flags().String(workspaceFlagName, "", "workspace root (default: walk up from cwd)")
	cmd.Flags().IntVar(&limit, "limit", 25, "max results to return")
	cmd.AddCommand(newSearchSavedCmd())
	return cmd
}

func renderSearchResults(cmd *cobra.Command, results []search.Result) error {
	if jsonOutput(cmd) {
		enc := json.NewEncoder(cmd.OutOrStdout())
		for _, r := range results {
			if err := enc.Encode(r); err != nil {
				return err
			}
		}
		return nil
	}
	if len(results) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no matches")
		return nil
	}
	out := cmd.OutOrStdout()
	color := useColor(cmd, out)
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tID\tTITLE\tSCORE\tSNIPPET")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.2f\t%s\n",
			r.EntityType, r.EntityID, r.Title, r.Score,
			renderSnippet(r.Snippet, color))
	}
	return tw.Flush()
}

// renderSnippet converts FTS5's `<<term>>` highlight markers into
// ANSI bold on a TTY, or strips them otherwise. The raw `<<...>>`
// form is fine for the JSON path (machine consumers want it
// stable); the human path benefits from highlights matching what
// every other CLI grep-like tool does.
func renderSnippet(s string, color bool) string {
	if color {
		s = strings.ReplaceAll(s, "<<", "\x1b[1m")
		s = strings.ReplaceAll(s, ">>", "\x1b[0m")
		return s
	}
	s = strings.ReplaceAll(s, "<<", "")
	s = strings.ReplaceAll(s, ">>", "")
	return s
}

// joinArgs concatenates positional arguments with single spaces so
// `rex search hello world` and `rex search "hello world"` behave the
// same way at the CLI boundary.
func joinArgs(args []string) string {
	out := ""
	for i, s := range args {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

// --- saved searches --------------------------------------------------

func newSearchSavedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "saved",
		Short: "Manage saved search queries (search.SAVED.*)",
		Long: `Saved searches give a name to a query for later re-use. Two on-disk
locations are recognized:

  workspace:  <workspaceRoot>/.rex/saved-searches.toml  (git-merged)
  user:       ~/.config/rex/saved-searches.toml        (per-machine)

Workspace entries shadow user entries on name collision. New saves
default to the workspace location; pass --global to write to the
user file instead.`,
		Example: `  rex search saved add recent-runs "type:run.completed"
  rex search saved list
  rex search saved run recent-runs
  rex search saved remove recent-runs`,
	}
	addWorkspacePersistentFlag(cmd)
	cmd.AddCommand(newSearchSavedAddCmd())
	cmd.AddCommand(newSearchSavedListCmd())
	cmd.AddCommand(newSearchSavedRunCmd())
	cmd.AddCommand(newSearchSavedRemoveCmd())
	setRelated(cmd,
		"rex search saved list",
		"rex search saved run <name>",
		"rex search <query>",
	)
	return cmd
}

func newSearchSavedAddCmd() *cobra.Command {
	var globalFlag bool
	cmd := &cobra.Command{
		Use:   "add <name> <query>",
		Short: "Save a search query under a name",
		Long: `Writes the query to the workspace's saved-searches.toml (default)
or the user-level file (--global). Refuses to overwrite an existing
entry; use 'rex search saved remove' first.`,
		Example: `  rex search saved add recent-runs "type:run.completed"
  rex search saved add my-fav "auth-flow" --global`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			query := joinArgs(args[1:])
			if !savedsearch.IsValidName(name) {
				return fmt.Errorf("saved-search name %q must be kebab-case", name)
			}
			path, err := resolveSavedSearchPath(cmd, globalFlag)
			if err != nil {
				return err
			}
			reg, err := savedsearch.Load(path)
			if err != nil {
				return err
			}
			if err := reg.Add(savedsearch.SavedSearch{Name: name, Query: query}); err != nil {
				return err
			}
			if err := savedsearch.Save(path, reg); err != nil {
				return err
			}
			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{
					"name": name, "query": query, "path": path,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "saved %q at %s\n", name, path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&globalFlag, "global", false, "write to ~/.config/rex/saved-searches.toml instead of the workspace file")
	setRelated(cmd, "rex search saved list", "rex search saved run <name>")
	return cmd
}

func newSearchSavedListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved searches (workspace + user)",
		Long: `Walks both saved-searches.toml locations and prints a merged view.
Workspace entries shadow user entries on name collision; the Source
column shows where each row resolved from.`,
		Example: `  rex search saved list
  rex search saved list --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, user, err := loadSavedSearchRegistries(cmd)
			if err != nil {
				return err
			}
			view := savedsearch.MergedView(ws, user)

			if jsonOutput(cmd) {
				return writeJSON(cmd, view)
			}
			if len(view) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no saved searches")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSOURCE\tQUERY")
			for _, v := range view {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", v.Name, v.Source, v.Query)
			}
			return tw.Flush()
		},
	}
	setRelated(cmd, "rex search saved add <name>", "rex search saved run <name>")
	return cmd
}

func newSearchSavedRunCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Look up a saved search by name and run it",
		Long: `Resolves <name> through the same workspace-shadows-user precedence
the list view uses, then runs the stored query against the workspace's
search index. Returns 'no matches' or the same result table 'rex search'
emits.`,
		Example: `  rex search saved run recent-runs
  rex search saved run recent-runs --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ws, user, err := loadSavedSearchRegistries(cmd)
			if err != nil {
				return err
			}
			merged := savedsearch.MergedView(ws, user)
			var hit *savedsearch.SavedSearchView
			for i := range merged {
				if merged[i].Name == args[0] {
					hit = &merged[i]
					break
				}
			}
			if hit == nil {
				return fmt.Errorf("no saved search named %q", args[0])
			}
			root, err := strictWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			idx, err := search.Open(root)
			if err != nil {
				return err
			}
			defer idx.Close()
			results, err := idx.Search(hit.Query, search.SearchOptions{Limit: limit})
			if err != nil {
				return err
			}
			return renderSearchResults(cmd, results)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 25, "max results to return")
	setRelated(cmd, "rex search saved list", "rex search saved add <name>")
	return cmd
}

func newSearchSavedRemoveCmd() *cobra.Command {
	var globalFlag bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a saved search",
		Long: `Removes the saved search from the workspace file by default; pass
--global to remove from ~/.config/rex/saved-searches.toml. Errors
when the named entry doesn't exist in the targeted location.`,
		Example: `  rex search saved remove recent-runs
  rex search saved remove my-fav --global`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveSavedSearchPath(cmd, globalFlag)
			if err != nil {
				return err
			}
			reg, err := savedsearch.Load(path)
			if err != nil {
				return err
			}
			if err := reg.Remove(args[0]); err != nil {
				return err
			}
			if err := savedsearch.Save(path, reg); err != nil {
				return err
			}
			if jsonOutput(cmd) {
				return writeJSON(cmd, map[string]any{"name": args[0], "path": path})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %q from %s\n", args[0], path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&globalFlag, "global", false, "remove from ~/.config/rex/saved-searches.toml")
	setRelated(cmd, "rex search saved list", "rex search saved add <name>")
	return cmd
}

// resolveSavedSearchPath picks the per-workspace or per-user file
// based on --global. The per-workspace path requires a resolvable
// workspace root; --global doesn't.
func resolveSavedSearchPath(cmd *cobra.Command, global bool) (string, error) {
	if global {
		return savedsearch.UserPath()
	}
	root, err := strictWorkspaceRoot(cmd)
	if err != nil {
		return "", err
	}
	return savedsearch.WorkspacePath(root), nil
}

// loadSavedSearchRegistries returns both registries, treating
// missing files as empty (the natural pre-first-save state).
// The workspace registry is nil when no workspace is resolvable;
// the user registry is nil only when the platform's config dir
// lookup fails.
func loadSavedSearchRegistries(cmd *cobra.Command) (*savedsearch.Registry, *savedsearch.Registry, error) {
	var ws *savedsearch.Registry
	root, err := currentWorkspaceRoot(cmd)
	if err == nil && root != "" {
		ws, err = savedsearch.Load(savedsearch.WorkspacePath(root))
		if err != nil {
			return nil, nil, err
		}
	}
	userPath, err := savedsearch.UserPath()
	if err != nil {
		return ws, nil, nil
	}
	user, err := savedsearch.Load(userPath)
	if err != nil {
		// Treat parse errors as fatal; missing-file is not (Load
		// already converted that to an empty registry).
		if !errors.Is(err, os.ErrNotExist) {
			return ws, nil, err
		}
	}
	return ws, user, nil
}
