package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/runner/adapter"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/local/recipe"
	"github.com/asabla/rex/internal/local/runtask"
)

// newSpecAskCmd implements `rex spec ask <id> [prompt]` per the
// spec-format.RECIPE.6 push: an ad-hoc convenience wrapper that
// drives a harness against a target spec without requiring a
// recipe-bearing task in any spec. The harness opens with the
// spec's full YAML preloaded; the user's prompt is the
// instruction. Use case: "look at this spec and tell me what's
// missing" — read-only review work.
func newSpecAskCmd() *cobra.Command {
	return newSpecActionAdHocCmd(specActionAdHocSpec{
		Use:    "ask <id> [prompt]",
		Short:  "Ask a harness about a spec (review-style; no mutations)",
		Action: specfmt.SpecActionReview,
		Long: `Opens a harness session preloaded with the named spec's full YAML
content and sends your prompt as the instruction. The harness sees
the spec, you ask the question, the response shows up on the run
detail page (and in the live event stream).

Read-only by default — the action is "review", which tells the
harness to produce Markdown commentary rather than a YAML rewrite.
Use ` + "`rex spec amend`" + ` when you want a YAML patch back.

The run records the target spec as a spec_ref so /specs/<id>
surfaces it alongside any recipe-driven runs.`,
		Example: `  rex spec ask overview "what's missing in the SCOPE component?"
  rex spec ask sync --harness claude-code "explain the rebase semantics"`,
	})
}

// specActionAdHocSpec carries the per-command differences for
// the shared ad-hoc spec_action runner. Three commands today
// (ask / amend) — only the action enum + prose differs.
type specActionAdHocSpec struct {
	Use     string
	Short   string
	Long    string
	Example string
	Action  specfmt.SpecAction
}

// newSpecActionAdHocCmd is the shared body of `rex spec ask`
// and `rex spec amend`. Cobra command construction differs only
// in the command name + action enum + help prose; the runtime
// is identical.
func newSpecActionAdHocCmd(spec specActionAdHocSpec) *cobra.Command {
	var (
		harnessFlag string
		modelFlag   string
		modeFlag    string
		runIDFlag   string
		nodeFlag    string
		quietFlag   bool
	)
	cmd := &cobra.Command{
		Use:               spec.Use,
		Short:             spec.Short,
		Long:              spec.Long,
		Example:           spec.Example,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeSpecIDs,
		RunE: func(cmd *cobra.Command, args []string) error {
			specID := args[0]
			if !specfmt.IsKebab(specID) {
				return fmt.Errorf("spec id %q is not kebab-case", specID)
			}
			prompt := ""
			if len(args) > 1 {
				prompt = args[1]
			}
			if prompt == "" {
				return errors.New("prompt is required (pass it as the second positional arg)")
			}

			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}

			// Pick a default harness when --harness wasn't passed —
			// the first registered adapter, alphabetised so the
			// pick is deterministic across invocations.
			if harnessFlag == "" {
				if h := pickDefaultHarness(); h != "" {
					harnessFlag = h
				}
			}
			if harnessFlag == "" {
				return errors.New("no harness specified and no adapters are registered (try --harness <name>)")
			}
			if _, ok := adapter.Default().Lookup(harnessFlag); !ok {
				return fmt.Errorf("unknown harness %q (registered: %v)", harnessFlag, adapter.Default().Names())
			}

			// Load the target spec so BuildSpecActionPrompt can
			// embed it verbatim.
			doc, err := loadSpecForAdHoc(root, specID)
			if err != nil {
				return err
			}

			// Synthesize an ad-hoc spec_action recipe + a fake
			// "host" task so PROMPT.1 token substitution still
			// works (the {{spec.*}} tokens resolve against the
			// target spec, {{task.*}} resolves to the synthetic
			// "ask"/"amend" id).
			synthRecipe := &specfmt.Recipe{
				Kind:    specfmt.RecipeKindSpecAction,
				Action:  spec.Action,
				Target:  specID,
				Harness: harnessFlag,
				Prompt:  prompt,
			}
			synthTask := &specfmt.Task{
				ID:          string(spec.Action),
				Description: prompt,
				State:       "in_progress",
			}

			fullPrompt, err := recipe.BuildSpecActionPrompt(root, synthRecipe, doc, synthTask)
			if err != nil {
				return err
			}

			ws, err := runtask.Open(root)
			if err != nil {
				return err
			}
			defer ws.Close()

			ctx := cmd.Context()
			onEvent := liveEventPrinter(cmd, quietFlag)

			node := nodeFlag
			if node == "" {
				node = "harness"
			}

			res, err := runtask.StartHarnessRun(ctx, ws, runtask.HarnessRunRequest{
				Harness:  harnessFlag,
				Prompt:   fullPrompt,
				Model:    modelFlag,
				Mode:     modeFlag,
				NodeID:   node,
				RunID:    runIDFlag,
				SpecRefs: []string{specID},
				OnEvent:  onEvent,
				OnStderr: harnessStderrPrinter(cmd, quietFlag),
			})
			if err != nil {
				return err
			}
			return reportHarnessRun(cmd, res, node)
		},
	}
	addWorkspacePersistentFlag(cmd)
	cmd.Flags().StringVar(&harnessFlag, "harness", "", "harness adapter to drive (default: first registered)")
	cmd.Flags().StringVar(&modelFlag, "model", "", "harness-specific model id")
	cmd.Flags().StringVar(&modeFlag, "mode", "", "harness-specific mode")
	cmd.Flags().StringVar(&runIDFlag, "run-id", "", "explicit run id (default: HLC-derived; useful for tests)")
	cmd.Flags().StringVar(&nodeFlag, "node-id", "harness", "id assigned to the only DAG node")
	cmd.Flags().BoolVar(&quietFlag, "quiet", false, "suppress the live event stream; print only the final summary")
	_ = cmd.RegisterFlagCompletionFunc("harness", completeHarnessNames)
	setRelated(cmd,
		"rex spec show <id>",
		"rex run start --harness <h> --prompt \"...\"",
		"rex run show <run-id>",
	)
	return cmd
}

// loadSpecForAdHoc resolves a spec id to its parsed Document for
// the ad-hoc flow. Falls back to the metadata.id walk when the
// file isn't named after the id (parallels the recipe loader's
// behaviour for `--from-task`).
func loadSpecForAdHoc(root, specID string) (*specfmt.Document, error) {
	dir := recipe.SpecsDir(root)
	path := filepath.Join(dir, specID+".yaml")
	if _, err := os.Stat(path); err != nil {
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			return nil, fmt.Errorf("read %s: %w", dir, rerr)
		}
		var matched string
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
				continue
			}
			candidate := filepath.Join(dir, e.Name())
			doc, perr := specfmt.ParseFile(candidate)
			if perr != nil {
				continue
			}
			if doc.Metadata.ID == specID {
				matched = candidate
				break
			}
		}
		if matched == "" {
			return nil, fmt.Errorf("no spec with metadata.id == %q in %s", specID, dir)
		}
		path = matched
	}
	return specfmt.ParseFile(path)
}

// pickDefaultHarness returns the first registered harness name
// alphabetically so the choice is deterministic across runs.
// Empty string when no adapters are registered.
func pickDefaultHarness() string {
	names := adapter.Default().Names()
	if len(names) == 0 {
		return ""
	}
	out := names[0]
	for _, n := range names[1:] {
		if n < out {
			out = n
		}
	}
	return out
}

// completeHarnessNames is the cobra ValidArgsFunction for the
// --harness flag on `rex spec ask` / `rex spec amend`. Lists
// every adapter in the global registry, sorted.
func completeHarnessNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	out := append([]string(nil), adapter.Default().Names()...)
	sortStrings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// sortStrings is a tiny helper duplicated here so we don't pull
// in the sort package for this single call site (the cli
// package's other completers use the same shape).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
