// Package schedule implements rex's v1 scheduled-work surface
// (execution.SCHED.*). One Schedule is the parsed shape of a
// .rex/schedules/<name>.yaml file (execution.SCHED.2.1); the
// daemon (Daemon, in daemon.go) walks the schedules dir, sets up
// cron tickers and fsnotify watchers, and fires runs through the
// caller-supplied dispatch function whenever a trigger matches.
//
// v1 ships cron and file-watch only; webhook is deferred to v1.5
// alongside central-side execution (execution.SCHED.1.4).
package schedule

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/specfmt"
)

// Dirname is the directory under .rex/ where schedule YAMLs live.
const Dirname = "schedules"

// TriggerKind enumerates the v1 trigger kinds (execution.SCHED.1.4).
type TriggerKind string

const (
	TriggerKindCron      TriggerKind = "cron"
	TriggerKindFileWatch TriggerKind = "file_watch"
)

// DefaultDebounce is the minimum quiet-period the file_watch
// trigger waits between fsnotify events before firing the schedule
// (execution.SCHED.2.1). Schedules may override via debounce_ms.
const DefaultDebounce = 500 * time.Millisecond

// Schedule is the parsed shape of one .rex/schedules/<name>.yaml.
type Schedule struct {
	// Name is the schedule's stable identifier — kebab-case,
	// matches the filename basename (without .yaml).
	Name string `yaml:"name"`
	// Trigger holds exactly one trigger block. Multi-trigger
	// schedules are post-v1 (execution.SCHED.2.1).
	Trigger Trigger `yaml:"trigger"`
	// Run is the recipe to execute on each fire — same shape as
	// spec-format.RECIPE.* so harness/shell/spec_validate runs
	// work without inventing a parallel "what to run" type.
	Run *specfmt.Recipe `yaml:"run"`

	// Path records where the schedule was loaded from. Set by
	// LoadFile / LoadDir; empty for in-memory test fixtures.
	Path string `yaml:"-"`
}

// Trigger holds the kind-specific trigger configuration. Exactly
// one of Cron / Paths is populated; the validator enforces this.
type Trigger struct {
	Kind TriggerKind `yaml:"kind"`
	// Cron is required when Kind == TriggerKindCron — a 5-field
	// standard cron expression (execution.SCHED.1.1).
	Cron string `yaml:"cron,omitempty"`
	// Paths is required when Kind == TriggerKindFileWatch — one
	// or more globs matched against paths under the workspace
	// root (execution.SCHED.1.3).
	Paths []string `yaml:"paths,omitempty"`
	// DebounceMs overrides DefaultDebounce for file_watch
	// schedules. Ignored for cron.
	DebounceMs int `yaml:"debounce_ms,omitempty"`
}

// Debounce returns the configured debounce window or the package
// default when DebounceMs is zero.
func (t Trigger) Debounce() time.Duration {
	if t.DebounceMs <= 0 {
		return DefaultDebounce
	}
	return time.Duration(t.DebounceMs) * time.Millisecond
}

// Errors surfaced by Validate.
var (
	ErrNoTrigger          = errors.New("schedule: trigger block is required")
	ErrUnknownTriggerKind = errors.New("schedule: unknown trigger kind")
	ErrCronExpressionMiss = errors.New("schedule: trigger.kind=cron requires cron")
	ErrPathsMiss          = errors.New("schedule: trigger.kind=file_watch requires paths")
	ErrEmptyRun           = errors.New("schedule: run block is required")
	ErrPromptToken        = errors.New("schedule: schedule recipes do not support PROMPT.1 templating tokens")
	ErrNameMismatch       = errors.New("schedule: name does not match filename basename")
)

var nameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate checks the Schedule against execution.SCHED.2.1 and
// the spec-format.RECIPE.* schema. Returns nil on success.
func (s *Schedule) Validate() error {
	if s == nil {
		return errors.New("schedule: nil schedule")
	}
	if s.Name == "" {
		return errors.New("schedule: name is required")
	}
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("schedule: name %q is not kebab-case", s.Name)
	}
	if s.Trigger.Kind == "" {
		return ErrNoTrigger
	}
	switch s.Trigger.Kind {
	case TriggerKindCron:
		if strings.TrimSpace(s.Trigger.Cron) == "" {
			return ErrCronExpressionMiss
		}
		if len(s.Trigger.Paths) > 0 {
			return errors.New("schedule: cron trigger must not set paths")
		}
	case TriggerKindFileWatch:
		if len(s.Trigger.Paths) == 0 {
			return ErrPathsMiss
		}
		if strings.TrimSpace(s.Trigger.Cron) != "" {
			return errors.New("schedule: file_watch trigger must not set cron")
		}
	default:
		return fmt.Errorf("%w: %q", ErrUnknownTriggerKind, s.Trigger.Kind)
	}
	if s.Run == nil {
		return ErrEmptyRun
	}
	if err := validateRecipeShape(s.Run); err != nil {
		return err
	}
	if err := assertNoPromptTokens(s.Run); err != nil {
		return err
	}
	return nil
}

// validateRecipeShape mirrors the kind-specific checks in
// internal/core/specfmt/validate.go (checkRecipe / checkShellRecipe /
// checkHarnessRecipe / checkSpecValidateRecipe) without dragging the
// full validator's issue-collection plumbing into the schedule path.
// Schedules need a synchronous yes/no answer at load time, not a
// list of issue lines.
func validateRecipeShape(r *specfmt.Recipe) error {
	if r == nil {
		return ErrEmptyRun
	}
	switch r.Kind {
	case specfmt.RecipeKindShell:
		if len(r.Command) == 0 {
			return errors.New("schedule: run.kind=shell requires command")
		}
	case specfmt.RecipeKindHarness:
		if strings.TrimSpace(r.Harness) == "" {
			return errors.New("schedule: run.kind=harness requires harness")
		}
		if strings.TrimSpace(r.Prompt) == "" {
			return errors.New("schedule: run.kind=harness requires prompt")
		}
	case specfmt.RecipeKindSpecValidate:
		// spec_validate has no required fields — paths defaults to
		// the workspace's specs/ directory.
	case "":
		return errors.New("schedule: run.kind is required")
	default:
		return fmt.Errorf("schedule: unknown run.kind %q", r.Kind)
	}
	return nil
}

// LoadFile parses one schedule YAML and validates it. The
// filename basename (minus .yaml) is enforced to equal Schedule.Name
// so `rex schedule remove <name>` works without a separate index.
func LoadFile(path string) (*Schedule, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("schedule: read %s: %w", path, err)
	}
	var s Schedule
	if err := yaml.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("schedule: parse %s: %w", path, err)
	}
	s.Path = path
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	base := strings.TrimSuffix(filepath.Base(path), ".yaml")
	if base != s.Name {
		return nil, fmt.Errorf("%s: %w (file basename %q)", path, ErrNameMismatch, base)
	}
	return &s, nil
}

// LoadDir scans dir (typically `<workspaceRoot>/.rex/schedules/`)
// and returns every valid Schedule it finds. Returns an empty
// slice and a nil error when the directory is missing — the
// natural pre-first-schedule state.
func LoadDir(dir string) ([]*Schedule, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("schedule: read dir %s: %w", dir, err)
	}
	var out []*Schedule
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		s, err := LoadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Dir returns the canonical schedules directory for a workspace.
func Dir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".rex", Dirname)
}

// FilePath returns the canonical schedule-yaml path for a name
// inside a workspace.
func FilePath(workspaceRoot, name string) string {
	return filepath.Join(Dir(workspaceRoot), name+".yaml")
}

// promptTokenRE matches the {{ task.id }}-style tokens
// spec-format.PROMPT.1 defines for task-bound recipes. Schedule
// recipes have no enclosing task, so any token is rejected.
var promptTokenRE = regexp.MustCompile(`\{\{[^}]+\}\}`)

func assertNoPromptTokens(r *specfmt.Recipe) error {
	if r == nil {
		return nil
	}
	if promptTokenRE.MatchString(r.Prompt) {
		return ErrPromptToken
	}
	for _, c := range r.Command {
		if promptTokenRE.MatchString(c) {
			return ErrPromptToken
		}
	}
	for _, v := range r.Env {
		if promptTokenRE.MatchString(v) {
			return ErrPromptToken
		}
	}
	return nil
}
