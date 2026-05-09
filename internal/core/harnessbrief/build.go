// Package harnessbrief renders the workspace-context primer
// that Rex injects into every harness session it spawns. The
// brief is a short Markdown passage telling the harness what
// kind of environment it's in (a Rex workspace), where things
// live (specs/, events.log), and what commands are available.
//
// Why injection rather than a workspace-discoverable file:
// writing AGENTS.md / CLAUDE.md collides with the user's own
// content (those files are conventionally human-managed). Rex
// owns runtime injection — every harness it spawns gets a fresh
// brief built from current workspace state, no on-disk file
// touched. Authors who want to override the body can drop a
// template at .rex/HARNESS.md.tmpl; absent that, the built-in
// default ships.
package harnessbrief

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/asabla/rex/internal/core/specfmt"
)

// TemplateFilename is the per-workspace override path. When
// present, Build renders that file instead of the built-in
// default. Lives inside .rex/ because it's Rex's own state, not
// content the harness needs to discover (the brief is injected,
// not file-discovered).
const TemplateFilename = "HARNESS.md.tmpl"

// Options configure Build. WorkspaceRoot is the only required
// field; everything else degrades gracefully when missing.
type Options struct {
	// WorkspaceRoot is the absolute path Rex is operating on.
	// Used for both the brief's preamble and to locate specs/.
	WorkspaceRoot string
	// FromTask, when set, anchors the brief in the run's
	// task: the brief calls out which task the run is for so
	// the harness knows what it's currently meant to be doing.
	FromTask string
	// SpecRefs, when non-empty, appears in the brief as the
	// list of ACIDs the run is satisfying.
	SpecRefs []string
	// WorkspaceID is the workspace's metadata.id for the
	// preamble. Optional — when empty Build derives it from
	// .rex/workspace.yaml; when that fails the path stands in.
	WorkspaceID string
	// WorkspaceName is the human-readable title. Same fallback
	// chain as WorkspaceID.
	WorkspaceName string
}

// Build returns the rendered brief for the supplied workspace
// state. Failures are tolerated: a missing specs/ dir, an
// unreadable workspace.yaml, or a parse error on a single spec
// degrade to a more terse brief rather than aborting the run.
// Returns ("", nil) only when WorkspaceRoot is empty (the
// caller should treat that as "skip the brief entirely").
func Build(opts Options) (string, error) {
	if opts.WorkspaceRoot == "" {
		return "", nil
	}

	id, name := opts.WorkspaceID, opts.WorkspaceName
	if id == "" || name == "" {
		dID, dName := loadWorkspaceMeta(opts.WorkspaceRoot)
		if id == "" {
			id = dID
		}
		if name == "" {
			name = dName
		}
	}

	specs := loadSpecsSummary(opts.WorkspaceRoot)

	// Honor the per-workspace template override first. It's a
	// raw Markdown body — Rex doesn't evaluate {{...}} tokens
	// in it (intentional simplicity; the brief is a primer, not
	// a full template engine). The override is for authors who
	// want a workspace-specific tone, not for branching content.
	if override := readOverride(opts.WorkspaceRoot); override != "" {
		var b strings.Builder
		b.WriteString(strings.TrimRight(override, "\n"))
		b.WriteString("\n\n")
		writeRunSection(&b, opts)
		return b.String(), nil
	}

	var b strings.Builder
	writeDefault(&b, id, name, opts.WorkspaceRoot, specs)
	writeRunSection(&b, opts)
	return b.String(), nil
}

// writeDefault is the built-in body — the one users see when
// they haven't written a .rex/HARNESS.md.tmpl. Kept short on
// purpose: every harness sees this on every run, so the
// signal-to-noise has to be high.
func writeDefault(b *strings.Builder, id, name, root string, specs []specSummary) {
	b.WriteString("# Rex workspace context\n\n")
	if name != "" || id != "" {
		fmt.Fprintf(b, "You are operating in a Rex workspace")
		if name != "" {
			fmt.Fprintf(b, ": **%s**", name)
		}
		if id != "" {
			fmt.Fprintf(b, " (`%s`)", id)
		}
		b.WriteString(".\n\n")
	} else {
		b.WriteString("You are operating in a Rex workspace.\n\n")
	}
	fmt.Fprintf(b, "Root: `%s`\n\n", root)

	if len(specs) > 0 {
		fmt.Fprintf(b, "## Active specs (%d)\n\n", len(specs))
		for _, s := range specs {
			fmt.Fprintf(b, "- `%s`", s.id)
			if s.name != "" && s.name != s.id {
				fmt.Fprintf(b, " — %s", s.name)
			}
			if s.state != "" {
				fmt.Fprintf(b, " · %s", s.state)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Conventions\n\n")
	b.WriteString("- Specs live at `.rex/specs/<id>.yaml`. Format described by `spec-format.yaml`.\n")
	b.WriteString("- ACIDs use the form `<spec-id>.<COMPONENT>.<requirement-id>` (e.g. `sync.ORDER.3`).\n")
	b.WriteString("- Tasks have kebab-case ids; cite ACIDs through their `references` list.\n")
	b.WriteString("- Audit log at `.rex/events.log`. Every run + harness frame writes there.\n\n")

	b.WriteString("## Useful commands\n\n")
	b.WriteString("- `rex spec validate` — strict schema check across all specs.\n")
	b.WriteString("- `rex spec verify` — exercise structured proof entries against on-disk evidence.\n")
	b.WriteString("- `rex spec ask <id> \"...\"` — ask a harness about a spec without writing a recipe.\n")
	b.WriteString("- `rex spec amend <id> \"...\"` — get a draft amendment back from a harness.\n")
	b.WriteString("- `rex run list --spec-ref <ACID>` / `--from-task <id>.<task>` — find related runs.\n\n")
}

// writeRunSection appends per-run context (the from_task /
// spec_refs the run is targeting, when set). Always last so the
// harness's eyes land on it after the workspace overview.
func writeRunSection(b *strings.Builder, opts Options) {
	if opts.FromTask == "" && len(opts.SpecRefs) == 0 {
		return
	}
	b.WriteString("## Current run\n\n")
	if opts.FromTask != "" {
		fmt.Fprintf(b, "- **From task**: `%s`\n", opts.FromTask)
	}
	if len(opts.SpecRefs) > 0 {
		b.WriteString("- **Cites**: ")
		for i, ref := range opts.SpecRefs {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "`%s`", ref)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

// readOverride returns the contents of .rex/HARNESS.md.tmpl
// when present, or the empty string when missing/unreadable.
// The file is loaded as-is (no token expansion), so authors get
// to write any prose they want without worrying about Rex
// rewriting it.
func readOverride(root string) string {
	body, err := os.ReadFile(filepath.Join(root, ".rex", TemplateFilename))
	if err != nil {
		return ""
	}
	return string(body)
}

// specSummary is one row in the brief's "Active specs" listing.
type specSummary struct {
	id    string
	name  string
	state string
}

// loadSpecsSummary returns one summary per parseable spec under
// .rex/specs/, sorted by id. Failures (missing dir, unparseable
// spec) skip silently — the brief stays renderable rather than
// erroring out the whole run.
func loadSpecsSummary(root string) []specSummary {
	dir := filepath.Join(root, ".rex", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []specSummary
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		doc, err := specfmt.ParseFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, specSummary{
			id:    doc.Metadata.ID,
			name:  doc.Metadata.Name,
			state: doc.Metadata.State,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// loadWorkspaceMeta returns (id, name) from .rex/workspace.yaml
// when readable, or empty strings when not. The brief uses these
// in the preamble; absence degrades to a generic "operating in a
// Rex workspace" intro.
func loadWorkspaceMeta(root string) (string, string) {
	body, err := os.ReadFile(filepath.Join(root, ".rex", "workspace.yaml"))
	if err != nil {
		return "", ""
	}
	// Hand-parse the two fields we need rather than pulling in
	// yaml.v3 just for this — both are top-level scalars in
	// every workspace.yaml on disk today.
	var id, name string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if id == "" && strings.HasPrefix(line, "id:") {
			id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
		if name == "" && strings.HasPrefix(line, "name:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		}
		if id != "" && name != "" {
			break
		}
	}
	id = strings.Trim(id, `"'`)
	name = strings.Trim(name, `"'`)
	return id, name
}

// Wrap returns the brief + a clear separator + the user prompt,
// formatted so the harness sees the brief as preliminary
// context rather than as part of the user's instruction. When
// brief is empty, returns userPrompt verbatim.
func Wrap(brief, userPrompt string) string {
	brief = strings.TrimRight(brief, "\n")
	userPrompt = strings.TrimSpace(userPrompt)
	if brief == "" {
		return userPrompt
	}
	if userPrompt == "" {
		return brief
	}
	var b strings.Builder
	b.WriteString(brief)
	b.WriteString("\n\n---\n\n")
	b.WriteString(userPrompt)
	b.WriteString("\n")
	return b.String()
}
