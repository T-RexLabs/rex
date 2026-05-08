package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/specfmt"
)

// recognizedWorkspaceYAMLKeys mirrors workspace.SETTINGS.2 (required)
// and SETTINGS.3 (optional). Unknown keys land as warnings rather
// than errors so additive future-spec evolution doesn't break
// pre-spec workspace.yaml files (overview.SYS.4 / SYS.3 spirit).
var recognizedWorkspaceYAMLKeys = map[string]struct{}{
	"id":                  {}, // SETTINGS.2
	"name":                {}, // SETTINGS.2
	"state":               {}, // SETTINGS.2
	"created_at":          {}, // SETTINGS.2
	"description":         {}, // SETTINGS.3
	"default_repo":        {}, // SETTINGS.3
	"repos":               {}, // SETTINGS.3 / SETTINGS.3.1
	"harness_defaults":    {}, // SETTINGS.3
	"default_template_id": {}, // SETTINGS.3
	"multi_repo_mode":     {}, // SETTINGS.3
	"extra":               {}, // catch-all bucket consistent with spec-format
}

// recognizedWorkspaceStates is the closed set from workspace.LIFE.3.
var recognizedWorkspaceStates = map[string]struct{}{
	workspaceStateActive:   {},
	workspaceStateArchived: {},
	workspaceStateDeleted:  {},
}

// recognizedMultiRepoModes is the closed set from workspace.SETTINGS.3
// (paren'd "one of `all`, `primary`").
var recognizedMultiRepoModes = map[string]struct{}{
	"all":     {},
	"primary": {},
}

// WorkspaceIssueSeverity classifies a validation finding.
type WorkspaceIssueSeverity string

const (
	wsIssueError   WorkspaceIssueSeverity = "error"
	wsIssueWarning WorkspaceIssueSeverity = "warning"
)

// WorkspaceIssue is one validation finding against workspace.yaml.
type WorkspaceIssue struct {
	Severity WorkspaceIssueSeverity `json:"severity"`
	Path     string                 `json:"path"`
	Message  string                 `json:"message"`
}

func (i WorkspaceIssue) String() string {
	return fmt.Sprintf("[%s] %s: %s", i.Severity, i.Path, i.Message)
}

// validateWorkspaceYAML walks .rex/workspace.yaml and returns every
// shape issue it finds. workspace.SETTINGS.2 required-key omissions
// become errors; unknown-top-level keys and out-of-set state /
// multi_repo_mode values become warnings + errors per their nature
// (SETTINGS.2 is normative for the four required keys; SETTINGS.3
// pin-the-shape values surface as errors when invalid).
//
// repos[] entries delegate to loadRepoEntries (which already enforces
// SETTINGS.3.1) — failures bubble as errors.
func validateWorkspaceYAML(root string) ([]WorkspaceIssue, error) {
	path := filepath.Join(root, metaDirName, "workspace.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return []WorkspaceIssue{{
			Severity: wsIssueError,
			Path:     "workspace.yaml",
			Message:  fmt.Sprintf("parse: %v", err),
		}}, nil
	}

	var issues []WorkspaceIssue

	// Required-key presence (SETTINGS.2).
	for _, k := range []string{"id", "name", "state", "created_at"} {
		if _, ok := raw[k]; !ok {
			issues = append(issues, WorkspaceIssue{
				Severity: wsIssueError,
				Path:     k,
				Message:  fmt.Sprintf("required key %q is missing (workspace.SETTINGS.2)", k),
			})
		}
	}

	// id is kebab-case.
	if id, ok := raw["id"].(string); ok && id != "" && !specfmt.IsKebab(id) {
		issues = append(issues, WorkspaceIssue{
			Severity: wsIssueError,
			Path:     "id",
			Message:  fmt.Sprintf("id %q is not kebab-case", id),
		})
	}

	// state ∈ {active, archived, deleted} when set.
	if state, ok := raw["state"].(string); ok && state != "" {
		if _, valid := recognizedWorkspaceStates[state]; !valid {
			issues = append(issues, WorkspaceIssue{
				Severity: wsIssueError,
				Path:     "state",
				Message:  fmt.Sprintf("state %q must be one of active/archived/deleted (workspace.LIFE.3)", state),
			})
		}
	}

	// multi_repo_mode ∈ {all, primary} when set.
	if mode, ok := raw["multi_repo_mode"].(string); ok && mode != "" {
		if _, valid := recognizedMultiRepoModes[mode]; !valid {
			issues = append(issues, WorkspaceIssue{
				Severity: wsIssueError,
				Path:     "multi_repo_mode",
				Message:  fmt.Sprintf("multi_repo_mode %q must be one of all/primary (workspace.SETTINGS.3)", mode),
			})
		}
	}

	// Unknown top-level keys → warnings (additive future-spec).
	var unknown []string
	for k := range raw {
		if _, ok := recognizedWorkspaceYAMLKeys[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		issues = append(issues, WorkspaceIssue{
			Severity: wsIssueWarning,
			Path:     k,
			Message:  fmt.Sprintf("unknown top-level key %q (recognized: %s)", k, recognizedKeyList()),
		})
	}

	// repos[] entry shape (delegates to existing loader).
	if _, ok := raw["repos"]; ok {
		if _, err := loadRepoEntries(root); err != nil {
			issues = append(issues, WorkspaceIssue{
				Severity: wsIssueError,
				Path:     "repos",
				Message:  err.Error(),
			})
		}
	}

	return issues, nil
}

// recognizedKeyList renders the recognized-keys set as a sorted
// comma-list for the warning message.
func recognizedKeyList() string {
	keys := make([]string, 0, len(recognizedWorkspaceYAMLKeys))
	for k := range recognizedWorkspaceYAMLKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// hasErrorIssue reports whether issues contains at least one error.
func hasErrorIssue(issues []WorkspaceIssue) bool {
	for _, i := range issues {
		if i.Severity == wsIssueError {
			return true
		}
	}
	return false
}
