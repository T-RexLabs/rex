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
	if doc.Metadata.ID != specID {
		// Filename matched but metadata.id didn't — probably an
		// alias. Still serviceable.
	}

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
	case specfmt.RecipeKindSpecValidate:
		return nil, fmt.Errorf("%w: spec_validate", ErrUnsupportedKind)
	default:
		return nil, fmt.Errorf("recipe kind %q not supported by this build", task.Run.Kind)
	}
	return out, nil
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
