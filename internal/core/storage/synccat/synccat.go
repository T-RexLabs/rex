// Package synccat carries the sync-category typing for every persistent
// entity Rex stores under .rex/. Per sync.CAT.1 every entity belongs to
// exactly one of three categories — git_merged, event_sourced, or derived
// — and the category is encoded at the type level so generic sync-engine
// code can ask "what is this?" without depending on every storage
// subsystem.
//
// Two surfaces ship here:
//
//   - The Category enum (CategoryGitMerged, CategoryEventSourced,
//     CategoryDerived). Storage subpackages declare their canonical
//     category as a typed package-level constant alongside their primary
//     types — see eventlog.SyncCategory, snapshot.SyncCategory,
//     specfmt.SyncCategory — so the type-level marking required by
//     sync-categorization travels with the package itself.
//
//   - A path-based registry (Categorize, MustCategorize) that maps any
//     `.rex/`-relative path to its category. This is the lookup the
//     sync engine uses when it has a path but not a Go type — e.g.
//     when iterating files staged for a push, or when refusing to pull
//     a derived path from a remote.
//
// Repo checkouts and caches (sync.CAT.4) live OUTSIDE `.rex/` at
// workspace-configured locations, so they never appear in the registry;
// they are categorized as derived through the workspace's repo metadata
// directly, which references CategoryDerived from this package.
package synccat

import (
	"fmt"
	"path"
	"strings"
)

// Category names one of the three sync categories from sync.CAT.1.
// The string values are the on-the-wire form used in events and
// configuration; do not change them without a major version bump
// (overview.SYS.4).
type Category string

const (
	// CategoryGitMerged covers human-authored content that syncs via
	// three-way merge with auto-rebase. Per sync.CAT.2 the set is:
	// workspace.yaml, specs/*, schedules/*, rbac.yaml, templates/*,
	// hooks/*, tool configs, and remote refs.
	CategoryGitMerged Category = "git_merged"

	// CategoryEventSourced covers append-only streams of facts. Per
	// sync.CAT.3 the set is: events.log, transcripts/*, run records,
	// audit log entries, and comments/annotations. Run records,
	// audit entries, and annotations live inside events.log itself,
	// so they share its category transitively.
	CategoryEventSourced Category = "event_sourced"

	// CategoryDerived covers state rebuilt from the canonical sources.
	// Per sync.CAT.4 the set is: index.sqlite, snapshots/*, repo
	// checkouts, and caches. Per-remote draft watermarks
	// (storage.WS.2.12) are also derived — they are local-only
	// bookkeeping over events.log and are reconstructable from a
	// remote acknowledgement round-trip.
	CategoryDerived Category = "derived"
)

// Valid reports whether c is one of the three known categories.
func (c Category) Valid() bool {
	switch c {
	case CategoryGitMerged, CategoryEventSourced, CategoryDerived:
		return true
	}
	return false
}

// String returns the on-the-wire name of the category.
func (c Category) String() string { return string(c) }

// rule encodes a single registry entry. A rule with a trailing slash
// in pattern matches any path under that prefix (including the bare
// directory itself); without a trailing slash it matches the exact
// file path.
type rule struct {
	pattern  string
	category Category
}

// rules enumerates every persistent entity Rex stores under `.rex/`.
// Order does not matter: every concrete `.rex/`-relative path matches
// at most one rule.
//
// Keep this list in sync with storage.WS.2 and the per-spec entities
// listed in tools.MCP.2 / tools.APP.2 / hooks.* / etc. Adding a new
// `.rex/` entity REQUIRES adding it here — Categorize returns ok=false
// for paths the registry does not recognise, and the sync engine will
// refuse to handle entities of unknown category.
var rules = []rule{
	// git_merged — sync.CAT.2 + storage.WS.2.1-7
	{pattern: "workspace.yaml", category: CategoryGitMerged},
	{pattern: "rbac.yaml", category: CategoryGitMerged},
	{pattern: "remotes.toml", category: CategoryGitMerged},
	{pattern: "specs/", category: CategoryGitMerged},
	{pattern: "schedules/", category: CategoryGitMerged},
	{pattern: "templates/", category: CategoryGitMerged},
	{pattern: "hooks/", category: CategoryGitMerged},
	{pattern: "tools/", category: CategoryGitMerged},

	// event_sourced — sync.CAT.3 + storage.WS.2.8-9
	{pattern: "events.log", category: CategoryEventSourced},
	{pattern: "transcripts/", category: CategoryEventSourced},

	// derived — sync.CAT.4 + storage.WS.2.10-12 + WS.2.11
	{pattern: "index.sqlite", category: CategoryDerived},
	{pattern: "snapshots/", category: CategoryDerived},
	{pattern: "drafts/", category: CategoryDerived},
	{pattern: "hook-log/", category: CategoryDerived},
	{pattern: "migrations-backup/", category: CategoryDerived},
}

// Categorize returns the sync category for a path expressed RELATIVE to
// the workspace's `.rex/` root. The path may name a directory, a file
// directly under `.rex/`, or any nested entry under a registered
// directory prefix.
//
// Returns ok=false when the path does not match any known entity. The
// caller decides what to do with that — generic sync code should
// refuse to operate on unrecognised paths rather than guessing.
func Categorize(rexRelPath string) (Category, bool) {
	clean := normalizeRel(rexRelPath)
	if clean == "" {
		return "", false
	}
	for _, r := range rules {
		if matchRule(r, clean) {
			return r.category, true
		}
	}
	return "", false
}

// MustCategorize is Categorize that panics on unknown paths. Use this
// only where the caller statically owns the entity and a missing rule
// is a programming error.
func MustCategorize(rexRelPath string) Category {
	c, ok := Categorize(rexRelPath)
	if !ok {
		panic(fmt.Sprintf("synccat: %q is not a registered .rex/ entity", rexRelPath))
	}
	return c
}

// KnownPaths returns the registered top-level entity names, sorted
// alphabetically. Callers that enumerate `.rex/` (e.g. the sync engine
// when bundling a push, or `rex workspace status`) can use this to
// confirm an entry is recognised before deciding what to do with it.
//
// The returned slice is a copy; mutation is safe.
func KnownPaths() []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.pattern)
	}
	return out
}

// matchRule reports whether clean (already normalized) matches r.
//
// A rule whose pattern ends in "/" is a directory prefix: any path
// starting with that prefix, OR the bare directory name itself,
// matches. A rule without a trailing slash is an exact filename match.
func matchRule(r rule, clean string) bool {
	if strings.HasSuffix(r.pattern, "/") {
		bare := strings.TrimSuffix(r.pattern, "/")
		if clean == bare {
			return true
		}
		return strings.HasPrefix(clean, r.pattern)
	}
	return clean == r.pattern
}

// normalizeRel cleans a `.rex/`-relative path: collapses `.`/`..`,
// trims a leading `/` or `./`, and strips a leading `.rex/` prefix
// if the caller passed it accidentally. Returns "" for paths that
// escape the root after cleaning.
func normalizeRel(rel string) string {
	if rel == "" {
		return ""
	}
	clean := path.Clean(strings.TrimPrefix(rel, "./"))
	clean = strings.TrimPrefix(clean, "/")
	clean = strings.TrimPrefix(clean, ".rex/")
	if clean == "" || clean == "." {
		return ""
	}
	if strings.HasPrefix(clean, "../") || clean == ".." {
		return ""
	}
	return clean
}
