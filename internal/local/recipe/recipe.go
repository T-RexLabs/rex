// Package recipe resolves spec-format `tasks[].run` recipes into
// runtask requests. Used by both the CLI (`rex run start --from-task`)
// and the web UI (`POST /specs/<id>/tasks/<task-id>/run`) so the
// substitution and provenance rules live in one place.
package recipe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asabla/rex/internal/core/specfmt"
)

// Resolved is the output of LoadFromTaskRef: the spec doc, the task,
// the recipe, and the rendered fields ready to feed into a runtask
// request. SpecRefs is the deduplicated union of caller-supplied
// references and the task's own references list.
type Resolved struct {
	SpecID      string
	SpecName    string
	TaskID      string
	Description string
	Recipe      *specfmt.Recipe

	// Command is the rendered argv for shell recipes.
	Command []string
	// Prompt is the rendered prompt string for harness recipes.
	Prompt string
	// SpecRefs is the deduplicated union of (caller refs, task refs);
	// caller refs come first to preserve their order when both lists
	// share entries.
	SpecRefs []string
	// FromTask is the canonical `<spec-id>.<task-id>` reference.
	FromTask string
}

// ErrUnsupportedKind is returned when the recipe kind is recognized
// by the schema but the caller surface (CLI / web POST) hasn't wired
// it yet — currently `spec_validate`.
var ErrUnsupportedKind = errors.New("recipe kind not yet wired to this entry point")

// SpecsDir returns the canonical workspace specs directory.
func SpecsDir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".rex", "specs")
}

// LoadFromTaskRef parses `<spec-id>.<task-id>`, finds the spec under
// `<workspaceRoot>/.rex/specs/<spec-id>.yaml`, locates the task, and
// renders its recipe. extraRefs are merged in front of the task's own
// references in SpecRefs (and deduplicated).
func LoadFromTaskRef(workspaceRoot, ref string, extraRefs []string) (*Resolved, error) {
	specID, taskID, ok := SplitTaskRef(ref)
	if !ok {
		return nil, fmt.Errorf("--from-task %q: expected <spec-id>.<task-id>", ref)
	}

	dir := SpecsDir(workspaceRoot)
	specPath := filepath.Join(dir, specID+".yaml")
	if _, err := os.Stat(specPath); err != nil {
		// Fall back to a directory walk so specs whose filename
		// doesn't match the metadata.id (legacy or tests) still
		// resolve.
		matched, err := findSpecByID(dir, specID)
		if err != nil {
			return nil, err
		}
		if matched == "" {
			return nil, fmt.Errorf("--from-task %q: no spec with metadata.id == %q", ref, specID)
		}
		specPath = matched
	}

	doc, err := specfmt.ParseFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", specPath, err)
	}
	// metadata.id may differ from the filename when the file is an
	// alias for another spec; we still proceed and look up the task.

	var task *specfmt.Task
	for i := range doc.Tasks {
		if doc.Tasks[i].ID == taskID {
			task = &doc.Tasks[i]
			break
		}
	}
	if task == nil {
		return nil, fmt.Errorf("--from-task %q: spec %q has no task with id %q", ref, specID, taskID)
	}
	if task.Run == nil {
		return nil, fmt.Errorf("--from-task %q: task %q has no `run` recipe", ref, taskID)
	}

	out := &Resolved{
		SpecID:      doc.Metadata.ID,
		SpecName:    doc.Metadata.Name,
		TaskID:      task.ID,
		Description: task.Description,
		Recipe:      task.Run,
		FromTask:    doc.Metadata.ID + "." + task.ID,
		SpecRefs:    DedupeRefs(append(append([]string(nil), extraRefs...), QualifyTaskRefs(doc.Metadata.ID, task.References)...)),
	}

	switch task.Run.Kind {
	case specfmt.RecipeKindShell:
		out.Command = make([]string, len(task.Run.Command))
		for i, arg := range task.Run.Command {
			out.Command[i] = RenderTemplate(arg, doc, task)
		}
	case specfmt.RecipeKindHarness:
		out.Prompt = RenderTemplate(task.Run.Prompt, doc, task)
	case specfmt.RecipeKindSpecAction:
		// Pre-load the target spec's YAML and prepend it to the
		// rendered prompt so the harness opens with full context
		// (RECIPE.6). Also fold target into spec_refs so /specs/<target>
		// surfaces this run alongside any other citations
		// (RECIPE.6 — synthetic spec_ref for the target).
		body, err := buildSpecActionPrompt(workspaceRoot, task.Run, doc, task)
		if err != nil {
			return nil, err
		}
		out.Prompt = body
		// Synthesize a spec_ref pointing at the target so the
		// run's provenance is bidirectional. The dedupe pass
		// keeps it tidy if the task already references the target.
		out.SpecRefs = DedupeRefs(append(out.SpecRefs, task.Run.Target))
	case specfmt.RecipeKindSpecValidate:
		return nil, fmt.Errorf("%w: spec_validate", ErrUnsupportedKind)
	default:
		return nil, fmt.Errorf("recipe kind %q not supported by this build", task.Run.Kind)
	}
	return out, nil
}

// buildSpecActionPrompt assembles the harness prompt for a
// spec_action recipe: a clear preamble identifying the workspace
// and target, the target spec's current YAML body verbatim, the
// action enum, then the author's instruction.
func buildSpecActionPrompt(workspaceRoot string, recipe *specfmt.Recipe, doc *specfmt.Document, task *specfmt.Task) (string, error) {
	if recipe.Target == "" {
		return "", fmt.Errorf("recipe: spec_action target is empty (RECIPE.6.2)")
	}
	dir := SpecsDir(workspaceRoot)
	targetPath := filepath.Join(dir, recipe.Target+".yaml")
	body, err := os.ReadFile(targetPath)
	if err != nil {
		// Fall back to filename-or-id resolution like the main loader.
		matched, ferr := findSpecByID(dir, recipe.Target)
		if ferr != nil || matched == "" {
			return "", fmt.Errorf("recipe: spec_action target %q does not resolve in %s", recipe.Target, dir)
		}
		targetPath = matched
		body, err = os.ReadFile(targetPath)
		if err != nil {
			return "", fmt.Errorf("recipe: read spec_action target %s: %w", targetPath, err)
		}
	}
	action := string(recipe.Action)
	if action == "" {
		action = "amend"
	}
	rendered := RenderTemplate(recipe.Prompt, doc, task)
	var b strings.Builder
	fmt.Fprintf(&b, "You are working with the Rex spec at %s.\n", relTargetPath(workspaceRoot, targetPath))
	fmt.Fprintf(&b, "\nAction requested: %s\n", action)
	fmt.Fprintf(&b, "\nThe spec's current content:\n---\n%s\n---\n\n", strings.TrimRight(string(body), "\n"))
	fmt.Fprintf(&b, "Author's instruction:\n%s\n\n", strings.TrimSpace(rendered))
	switch specfmt.SpecAction(action) {
	case specfmt.SpecActionAmend:
		b.WriteString("Output a YAML body suitable for ")
		fmt.Fprintf(&b, "specs/_proposed/%s-amendment-<date>.yaml. ", recipe.Target)
		b.WriteString("Authors will review and fold the amendment manually; do not modify any other files.\n")
	case specfmt.SpecActionDraft:
		b.WriteString("Output a complete spec body. Authors will save it as a new spec file; do not modify any other files.\n")
	case specfmt.SpecActionReview:
		b.WriteString("Provide a Markdown review of the spec — gaps, contradictions, opportunities, and concrete suggestions. Do not output a YAML rewrite.\n")
	}
	return b.String(), nil
}

// relTargetPath formats the target spec's path workspace-relative
// when possible so the prompt's preamble is short. Falls back to
// the absolute path if filepath.Rel fails (e.g. cross-volume).
func relTargetPath(workspaceRoot, abs string) string {
	if rel, err := filepath.Rel(workspaceRoot, abs); err == nil {
		return rel
	}
	return abs
}

func findSpecByID(dir, specID string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		doc, err := specfmt.ParseFile(path)
		if err != nil {
			continue
		}
		if doc.Metadata.ID == specID {
			return path, nil
		}
	}
	return "", nil
}

// SplitTaskRef parses `<spec-id>.<task-id>`. The first dot wins.
func SplitTaskRef(ref string) (spec, task string, ok bool) {
	idx := strings.Index(ref, ".")
	if idx <= 0 || idx == len(ref)-1 {
		return "", "", false
	}
	return ref[:idx], ref[idx+1:], true
}

// QualifyTaskRefs converts a task.references list (which may use the
// short form per spec-format.ACID.1.1) into fully-qualified ACIDs by
// prefixing the enclosing spec id where the short form is detected.
//
// The discriminator is the first segment: a fully-qualified ACID
// starts with the spec's kebab-case id (lowercase + hyphens), while
// the short form starts with the component id (uppercase + hyphens
// per spec-format.COMP.1.1).
func QualifyTaskRefs(specID string, refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		if firstSegment := beforeFirstDot(r); specfmt.IsComponentID(firstSegment) {
			out = append(out, specID+"."+r)
			continue
		}
		out = append(out, r)
	}
	return out
}

func beforeFirstDot(s string) string {
	if i := strings.Index(s, "."); i >= 0 {
		return s[:i]
	}
	return s
}

// RenderTemplate performs spec-format.PROMPT.1 token substitution.
// Unknown tokens are left literal (the validator already flags them).
func RenderTemplate(s string, doc *specfmt.Document, task *specfmt.Task) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	tokens := map[string]string{
		"spec.id":          doc.Metadata.ID,
		"spec.name":        doc.Metadata.Name,
		"task.id":          task.ID,
		"task.description": task.Description,
		"task.references":  strings.Join(QualifyTaskRefs(doc.Metadata.ID, task.References), ", "),
	}

	var b strings.Builder
	for i := 0; i < len(s); {
		if i+1 < len(s) && s[i] == '{' && s[i+1] == '{' {
			end := strings.Index(s[i+2:], "}}")
			if end >= 0 {
				token := strings.TrimSpace(s[i+2 : i+2+end])
				if val, ok := tokens[token]; ok {
					b.WriteString(val)
					i += 2 + end + 2
					continue
				}
				b.WriteString(s[i : i+2+end+2])
				i += 2 + end + 2
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// DedupeRefs canonicalises a slice of ACIDs: trims, drops empties,
// removes duplicates, preserves first-seen order.
func DedupeRefs(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, r := range in {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}
