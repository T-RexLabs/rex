package cli

import (
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/specfmt"
)

// completionSource exposes the inputs spec/task/ACID flag
// completers need. Centralised so all completion entry points
// resolve workspace root + load specs the same way and stay in
// sync as that surface evolves.
//
// Failures degrade silently: cobra's completion contract is "no
// suggestions" rather than "error" since the user is mid-keystroke.
type completionSource struct {
	specs []*specfmt.Document
}

func loadCompletionSource(cmd *cobra.Command) *completionSource {
	root, err := workspaceRootFor(workspaceFlagValue(cmd))
	if err != nil || root == "" {
		return &completionSource{}
	}
	paths, err := listSpecFiles(specDir(root))
	if err != nil {
		return &completionSource{}
	}
	ws, _, _ := loadWorkspace(paths)
	if ws == nil {
		return &completionSource{}
	}
	return &completionSource{specs: ws.Specs()}
}

// completeSpecIDs returns every kebab-case spec id in the
// workspace, sorted. Used as the first-arg completer on
// `rex spec runs <id>`.
func completeSpecIDs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	src := loadCompletionSource(cmd)
	out := make([]string, 0, len(src.specs))
	for _, doc := range src.specs {
		out = append(out, doc.Metadata.ID)
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeTaskIDsForSpec returns every task id declared in the
// supplied spec. Used by `rex spec runs <id> --task <Tab>`. We
// pass the spec id explicitly because the completer doesn't see
// positional args until they're committed to the command.
func completeTaskIDsForSpec(src *completionSource, specID string) []string {
	for _, doc := range src.specs {
		if doc.Metadata.ID != specID {
			continue
		}
		out := make([]string, 0, len(doc.Tasks))
		for _, t := range doc.Tasks {
			out = append(out, t.ID)
		}
		sort.Strings(out)
		return out
	}
	return nil
}

// completeFromTaskRefs enumerates every `<spec-id>.<task-id>`
// pair in the workspace as one flat list. Used as the
// --from-task completer on `rex run start` and `rex run list`,
// and for the first positional arg of any future task-targeted
// command. Filters by prefix so users typing `execution.<Tab>`
// see only that spec's tasks.
func completeFromTaskRefs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	src := loadCompletionSource(cmd)
	out := make([]string, 0)
	for _, doc := range src.specs {
		for _, t := range doc.Tasks {
			ref := doc.Metadata.ID + "." + t.ID
			if toComplete == "" || strings.HasPrefix(ref, toComplete) {
				out = append(out, ref)
			}
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeSpecRefs enumerates every fully-qualified ACID in the
// workspace as `<spec>.<COMP>.<req>`. Used for --spec-ref
// completion. Suggests both the bare spec id (so a user can
// type `execution<Tab>` and get `execution.PRIM.6` etc.) and
// every individual ACID.
func completeSpecRefs(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	src := loadCompletionSource(cmd)
	out := make([]string, 0)
	for _, doc := range src.specs {
		specPrefix := doc.Metadata.ID + "."
		// Components first (preferred citation), then constraints.
		for cid, comp := range doc.Components {
			for rid := range comp.Requirements {
				acid := specPrefix + cid + "." + rid
				if toComplete == "" || strings.HasPrefix(acid, toComplete) {
					out = append(out, acid)
				}
			}
		}
		for cid, cons := range doc.Constraints {
			for rid := range cons.Requirements {
				acid := specPrefix + cid + "." + rid
				if toComplete == "" || strings.HasPrefix(acid, toComplete) {
					out = append(out, acid)
				}
			}
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeTaskFlagForSpecRunsCmd is the --task completer on
// `rex spec runs <spec-id>`. It pulls the spec id off the
// already-typed positional arg and narrows to that spec's
// tasks.
func completeTaskFlagForSpecRunsCmd(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) == 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	src := loadCompletionSource(cmd)
	tasks := completeTaskIDsForSpec(src, args[0])
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if toComplete == "" || strings.HasPrefix(t, toComplete) {
			out = append(out, t)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
