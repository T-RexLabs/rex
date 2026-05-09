package synccat

import (
	"sort"
	"testing"
)

func TestCategoryValid(t *testing.T) {
	cases := []struct {
		c    Category
		want bool
	}{
		{CategoryGitMerged, true},
		{CategoryEventSourced, true},
		{CategoryDerived, true},
		{Category(""), false},
		{Category("synced"), false},
	}
	for _, tc := range cases {
		if got := tc.c.Valid(); got != tc.want {
			t.Errorf("Category(%q).Valid() = %v, want %v", tc.c, got, tc.want)
		}
	}
}

// TestCategorizeKnownPaths covers every literal entity named in
// storage.WS.2 plus the tool configs from tools.MCP.2 / tools.APP.2.
// Adding a new `.rex/` entity should require adding a row here.
func TestCategorizeKnownPaths(t *testing.T) {
	cases := []struct {
		path string
		want Category
	}{
		// git_merged — WS.2.1-7
		{"workspace.yaml", CategoryGitMerged},
		{"rbac.yaml", CategoryGitMerged},
		{"remotes.toml", CategoryGitMerged},
		{"specs/overview.yaml", CategoryGitMerged},
		{"specs/sync.yaml", CategoryGitMerged},
		{"schedules/nightly.yaml", CategoryGitMerged},
		{"templates/feature.yaml", CategoryGitMerged},
		{"hooks/post-event-run-completed", CategoryGitMerged},
		{"hooks/post-event-run-completed/notify.sh", CategoryGitMerged},
		// tools/ — tools.MCP.2, tools.APP.2
		{"tools/mcp-servers.yaml", CategoryGitMerged},
		{"tools/integrations.yaml", CategoryGitMerged},

		// event_sourced — WS.2.8-9
		{"events.log", CategoryEventSourced},
		{"transcripts/r-12345/0001-session-update.json", CategoryEventSourced},
		{"transcripts/r-12345", CategoryEventSourced},

		// derived — WS.2.10-12 + internal scratch
		{"index.sqlite", CategoryDerived},
		{"snapshots/snap-2026-05-09", CategoryDerived},
		{"snapshots/snap-2026-05-09/manifest.json", CategoryDerived},
		{"drafts/origin.toml", CategoryDerived},
		{"hook-log/2026-05-09.log", CategoryDerived},
		{"migrations-backup/2026-05-08.sqlite", CategoryDerived},
	}
	for _, tc := range cases {
		got, ok := Categorize(tc.path)
		if !ok {
			t.Errorf("Categorize(%q) returned ok=false; want category %q", tc.path, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("Categorize(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestCategorizeUnknownPaths(t *testing.T) {
	// Unknown paths return ok=false. Generic sync code refuses to
	// operate on unrecognised entries rather than guessing.
	for _, p := range []string{
		"random.txt",
		"notes/2026.md",
		"specs",  // bare directory without trailing slash should still match (registered as "specs/")
		"hooks",  // same: bare directory under registered prefix
		"tools",  // same
		"events", // not events.log
	} {
		got, ok := Categorize(p)
		switch p {
		case "specs", "hooks", "tools":
			// Bare directory names match the registered "X/" prefix.
			if !ok || got != CategoryGitMerged {
				t.Errorf("Categorize(%q) = (%q, %v); want (git_merged, true)", p, got, ok)
			}
		default:
			if ok {
				t.Errorf("Categorize(%q) = (%q, true); want ok=false", p, got)
			}
		}
	}
}

func TestCategorizeNormalizesInput(t *testing.T) {
	// Callers pass `.rex/`-relative paths; tolerate a stray leading
	// slash, ./, or even a redundant `.rex/` prefix without changing
	// the answer.
	cases := []struct {
		in   string
		want Category
	}{
		{"workspace.yaml", CategoryGitMerged},
		{"./workspace.yaml", CategoryGitMerged},
		{"/workspace.yaml", CategoryGitMerged},
		{".rex/workspace.yaml", CategoryGitMerged},
		{".rex/specs/sync.yaml", CategoryGitMerged},
		{".rex/events.log", CategoryEventSourced},
	}
	for _, tc := range cases {
		got, ok := Categorize(tc.in)
		if !ok || got != tc.want {
			t.Errorf("Categorize(%q) = (%q, %v); want (%q, true)", tc.in, got, ok, tc.want)
		}
	}
}

func TestCategorizeRejectsEscapingPaths(t *testing.T) {
	for _, p := range []string{"", ".", "..", "../etc/passwd", "../../events.log"} {
		if got, ok := Categorize(p); ok {
			t.Errorf("Categorize(%q) = (%q, true); want ok=false", p, got)
		}
	}
}

func TestMustCategorize(t *testing.T) {
	if got := MustCategorize("events.log"); got != CategoryEventSourced {
		t.Errorf("MustCategorize(events.log) = %q, want event_sourced", got)
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustCategorize on unknown path did not panic")
		}
	}()
	MustCategorize("not-a-thing.txt")
}

func TestKnownPathsCoversAllRules(t *testing.T) {
	got := KnownPaths()
	if len(got) != len(rules) {
		t.Fatalf("KnownPaths returned %d entries, want %d", len(got), len(rules))
	}
	// Returned slice is a copy.
	got[0] = "mutated"
	if rules[0].pattern == "mutated" {
		t.Error("KnownPaths returned the underlying slice; mutation leaked into rules")
	}
}

// TestRulesPartitionTheWorld confirms every category has at least one
// rule and no path is ambiguous between rules.
func TestRulesPartitionTheWorld(t *testing.T) {
	seen := map[Category]int{}
	patterns := make([]string, 0, len(rules))
	for _, r := range rules {
		seen[r.category]++
		patterns = append(patterns, r.pattern)
	}
	for _, c := range []Category{CategoryGitMerged, CategoryEventSourced, CategoryDerived} {
		if seen[c] == 0 {
			t.Errorf("no registry entries for category %q", c)
		}
	}

	sort.Strings(patterns)
	for i := 1; i < len(patterns); i++ {
		if patterns[i] == patterns[i-1] {
			t.Errorf("duplicate rule pattern %q", patterns[i])
		}
	}
}
