package cli

import (
	"path/filepath"

	"github.com/asabla/rex/internal/local/recipe"
)

// resolveTaskRecipe loads the workspace's specs, finds the named
// `<spec-id>.<task-id>`, and renders its Recipe with PROMPT.1
// substitutions. Errors are user-facing.
//
// extraRefs are merged into the resolved SpecRefs ahead of the task's
// own references list.
func resolveTaskRecipe(workspaceRoot, ref string, extraRefs ...string) (*recipe.Resolved, error) {
	return recipe.LoadFromTaskRef(workspaceRoot, ref, extraRefs)
}

// splitTaskRef is a thin alias kept for the existing CLI tests; the
// authoritative implementation lives in the recipe package.
func splitTaskRef(ref string) (spec, task string, ok bool) {
	return recipe.SplitTaskRef(ref)
}

// qualifyTaskRefs is a thin alias kept for the existing CLI tests.
func qualifyTaskRefs(specID string, refs []string) []string {
	return recipe.QualifyTaskRefs(specID, refs)
}

// dedupeRefs is a thin alias kept for the existing CLI tests.
func dedupeRefs(in []string) []string {
	return recipe.DedupeRefs(in)
}

// recipeWorkspaceDir resolves a recipe-supplied `cwd` against the
// workspace root. Empty `cwd` resolves to the workspace root itself.
func recipeWorkspaceDir(workspaceRoot, dir string) string {
	if dir == "" {
		return workspaceRoot
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(workspaceRoot, dir)
}
