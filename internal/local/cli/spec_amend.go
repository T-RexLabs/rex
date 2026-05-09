package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/specamend"
	"github.com/asabla/rex/internal/core/specfmt"
)

// newSpecAmendCmd is the parent for the amendment-lifecycle
// subcommands (cli.SPEC.7-11). Subcommand layout:
//
//	rex spec amend list   — enumerate amendments under .rex/specs/_proposed/
//	rex spec amend show   — print a single amendment's frontmatter + body
//	rex spec amend accept — move proposed → accepted (audit-emitting)
//	rex spec amend reject — delete proposed (audit-emitting)
//	rex spec amend draft  — drive a harness to draft a new amendment
//
// The previous bare form `rex spec amend <id> [prompt]` is kept as
// a deprecated alias that prints a warning and forwards to draft.
func newSpecAmendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "amend",
		Short: "Manage spec amendments — list, show, accept, reject, draft",
		Long: `Spec amendments live under ` + "`.rex/specs/_proposed/`" + ` and graduate
into ` + "`.rex/specs/_proposed/_accepted/`" + ` once approved (per
spec-format.AMEND.*). Acceptance does not modify the target spec —
folding the amendment's edits into the target is a separate manual
or harness-driven step.

Use ` + "`rex spec amend draft <id>`" + ` to ask a harness to draft a new
amendment. The bare form ` + "`rex spec amend <id> [prompt]`" + ` is kept
as a deprecated alias for one minor version.`,
		Example: `  rex spec amend list
  rex spec amend list --state proposed --for sync
  rex spec amend show cli-amendment-2026-05-10
  rex spec amend accept cli-amendment-2026-05-10
  rex spec amend reject stale-amendment-2026-04-01
  rex spec amend draft overview "tighten SYS.4 and add a SYS.7 about offline mode"`,
		Args: cobra.ArbitraryArgs,
		// DisableFlagParsing on the parent so the deprecated bare
		// form passes flags (--harness, --model, --workspace, etc.)
		// through to the draft subcommand untouched. Subcommands
		// re-enable their own flag parsing — this only affects
		// dispatch at the parent level. Without this, `rex spec
		// amend <id> --harness foo` would fail with "unknown flag"
		// before reaching the alias-fallback RunE.
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			if args[0] == "-h" || args[0] == "--help" {
				return cmd.Help()
			}
			// Bare form fallback (deprecated alias). cobra reaches
			// this branch when args[0] doesn't match any registered
			// subcommand name. We forward to draft after warning.
			fmt.Fprintln(cmd.ErrOrStderr(),
				"warning: `rex spec amend <id> [prompt]` is deprecated; use `rex spec amend draft <id> [prompt]` instead.")
			draft := newSpecAmendDraftCmd()
			draft.SetArgs(args)
			draft.SetIn(cmd.InOrStdin())
			draft.SetOut(cmd.OutOrStdout())
			draft.SetErr(cmd.ErrOrStderr())
			draft.SetContext(cmd.Context())
			return draft.Execute()
		},
	}
	addWorkspacePersistentFlag(cmd)
	cmd.AddCommand(newSpecAmendListCmd())
	cmd.AddCommand(newSpecAmendShowCmd())
	cmd.AddCommand(newSpecAmendAcceptCmd())
	cmd.AddCommand(newSpecAmendRejectCmd())
	cmd.AddCommand(newSpecAmendDraftCmd())
	return cmd
}

// newSpecAmendListCmd implements `rex spec amend list` (SPEC.7).
// Lists amendments under .rex/specs/_proposed/ (and _accepted/),
// optionally filtered by state and target spec id.
func newSpecAmendListCmd() *cobra.Command {
	var (
		stateFlag string
		forFlag   string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List amendments under .rex/specs/_proposed/",
		Long: `Lists every amendment file under ` + "`.rex/specs/_proposed/`" + ` and
` + "`.rex/specs/_proposed/_accepted/`" + ` in date-descending order. The
state column shows whether the file currently sits in proposed or
accepted; the directory location is the source of truth, not the
in-file ` + "`state:`" + ` field.`,
		Example: `  rex spec amend list
  rex spec amend list --state proposed
  rex spec amend list --for sync`,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			state, err := parseStateFlag(stateFlag)
			if err != nil {
				return err
			}
			amendments, err := specamend.List(root, specamend.ListOptions{
				State: state,
				For:   forFlag,
			})
			if err != nil {
				return err
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return writeAmendmentListJSON(cmd, amendments)
			}
			writeAmendmentListText(cmd, amendments)
			return nil
		},
	}
	cmd.Flags().StringVar(&stateFlag, "state", "", "filter by lifecycle state: proposed | accepted")
	cmd.Flags().StringVar(&forFlag, "for", "", "filter by target spec id")
	_ = cmd.RegisterFlagCompletionFunc("for", completeSpecIDs)
	setRelated(cmd,
		"rex spec amend show <stem>",
		"rex spec amend accept <stem>",
		"rex spec amend reject <stem>",
	)
	return cmd
}

// newSpecAmendShowCmd implements `rex spec amend show <stem>`
// (SPEC.8). Prints the amendment's frontmatter (target, date,
// state, summary, kind) and the full YAML body.
func newSpecAmendShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <file-or-stem>",
		Short: "Show an amendment's frontmatter and body",
		Long: `Resolves the amendment by stem (filename without the .yaml
extension), looking under ` + "`.rex/specs/_proposed/`" + ` first and
` + "`.rex/specs/_proposed/_accepted/`" + ` second. Prints the parsed
frontmatter followed by the raw YAML body.`,
		Example: `  rex spec amend show cli-amendment-2026-05-10
  rex spec amend show cli-amendment-2026-05-10.yaml`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeAmendmentStems,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			a, err := specamend.Load(root, args[0])
			if err != nil {
				return err
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"stem":           a.Stem,
					"path":           a.Path,
					"state":          a.State,
					"amendment_for":  a.AmendmentFor,
					"amendment_date": a.AmendmentDate,
					"amendment_kind": a.AmendmentKind,
					"multi":          a.Multi,
					"summary":        a.Summary,
					"body":           string(a.Body),
				})
			}
			writeAmendmentShowText(cmd, a)
			return nil
		},
	}
	setRelated(cmd,
		"rex spec amend list",
		"rex spec amend accept <stem>",
		"rex spec amend reject <stem>",
	)
	return cmd
}

// newSpecAmendAcceptCmd implements `rex spec amend accept <stem>`
// (SPEC.9). Moves _proposed/<stem>.yaml to
// _proposed/_accepted/<stem>.yaml, rewrites `state: proposed` to
// `state: accepted`, and emits spec.amendment.accepted. Does not
// modify the target spec.
func newSpecAmendAcceptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accept <file-or-stem>",
		Short: "Accept a proposed amendment (move + audit, no spec edit)",
		Long: `Moves the amendment from ` + "`_proposed/`" + ` to ` + "`_proposed/_accepted/`" + `,
rewrites the in-file ` + "`state:`" + ` field to ` + "`accepted`" + `, and writes a
spec.amendment.accepted audit row.

Per spec-format.AMEND.5: acceptance does NOT modify the target
spec. Folding the amendment's edits into the target spec is a
separate manual or harness-driven step.`,
		Example:           `  rex spec amend accept cli-amendment-2026-05-10`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProposedAmendmentStems,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			res, err := specamend.Accept(root, args[0])
			if err != nil {
				return err
			}
			wsID, err := workspaceID(root)
			if err != nil {
				return err
			}
			if err := emitAuditEvent(cmd, root, audit.EventTypeSpecAmendmentAccepted, audit.SpecAmendmentEvent{
				WorkspaceID:   wsID,
				Stem:          res.Stem,
				AmendmentFor:  res.AmendmentFor,
				AmendmentDate: res.AmendmentDate,
				FromPath:      res.FromPath,
				ToPath:        res.ToPath,
			}); err != nil {
				return err
			}
			printConfirmation(cmd, "accepted %s\n  moved: %s -> %s\n", res.Stem, res.FromPath, res.ToPath)
			return nil
		},
	}
	setRelated(cmd,
		"rex spec amend list --state proposed",
		"rex spec amend show <stem>",
		"rex spec amend reject <stem>",
	)
	return cmd
}

// newSpecAmendRejectCmd implements `rex spec amend reject <stem>`
// (SPEC.10). Deletes the proposed amendment file and emits
// spec.amendment.rejected. Refuses to delete accepted amendments.
func newSpecAmendRejectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reject <file-or-stem>",
		Short: "Reject a proposed amendment (delete + audit)",
		Long: `Deletes the proposed amendment file and writes a
spec.amendment.rejected audit row. Refuses to delete amendments
already under ` + "`_proposed/_accepted/`" + ` — accepted amendments are
part of the audit trail.

Git history preserves the rejected proposal; there is no
` + "`_rejected/`" + ` directory.`,
		Example:           `  rex spec amend reject stale-amendment-2026-04-01`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeProposedAmendmentStems,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			res, err := specamend.Reject(root, args[0])
			if err != nil {
				return err
			}
			wsID, err := workspaceID(root)
			if err != nil {
				return err
			}
			if err := emitAuditEvent(cmd, root, audit.EventTypeSpecAmendmentRejected, audit.SpecAmendmentEvent{
				WorkspaceID:   wsID,
				Stem:          res.Stem,
				AmendmentFor:  res.AmendmentFor,
				AmendmentDate: res.AmendmentDate,
				FromPath:      res.FromPath,
			}); err != nil {
				return err
			}
			printConfirmation(cmd, "rejected %s\n  deleted: %s\n", res.Stem, res.FromPath)
			return nil
		},
	}
	setRelated(cmd,
		"rex spec amend list --state proposed",
		"rex spec amend show <stem>",
		"rex spec amend accept <stem>",
	)
	return cmd
}

// newSpecAmendDraftCmd implements `rex spec amend draft <id>`
// (SPEC.11). Drives a harness to produce a YAML amendment body
// for the named target spec — the same machinery as the previous
// bare `rex spec amend <id>` form.
func newSpecAmendDraftCmd() *cobra.Command {
	return newSpecActionAdHocCmd(specActionAdHocSpec{
		Use:    "draft <id> [prompt]",
		Short:  "Ask a harness to draft an amendment to a spec",
		Action: specfmt.SpecActionAmend,
		Long: `Opens a harness session preloaded with the named spec's full YAML
content and sends your prompt as the amendment instruction. The
harness is asked to produce a YAML body suitable for
` + "`.rex/specs/_proposed/<id>-amendment-<date>.yaml`" + `.

Per spec-format.AMEND.5: v1 does not auto-write the response.
Review the harness output on /runs/<id>, save the YAML under
` + "`.rex/specs/_proposed/`" + `, then accept it via ` + "`rex spec amend accept`" + `.`,
		Example: `  rex spec amend draft overview "tighten SYS.4 and add a SYS.7 about offline mode"
  rex spec amend draft cli --harness claude-code "extend rex run list with --since"`,
	})
}

// parseStateFlag converts the --state flag value into a
// specamend.State. Empty string means "no filter".
func parseStateFlag(s string) (specamend.State, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", nil
	case "proposed":
		return specamend.StateProposed, nil
	case "accepted":
		return specamend.StateAccepted, nil
	default:
		return "", fmt.Errorf("invalid --state %q (want proposed or accepted)", s)
	}
}

func writeAmendmentListText(cmd *cobra.Command, amendments []*specamend.Amendment) {
	if len(amendments) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no amendments found")
		return
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tDATE\tFOR\tSTEM\tSUMMARY")
	for _, a := range amendments {
		target := a.AmendmentFor
		if a.Multi {
			target = "(multi)"
		}
		summary := firstLine(a.Summary)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			a.State, a.AmendmentDate, target, a.Stem, summary)
	}
	_ = tw.Flush()
}

func writeAmendmentListJSON(cmd *cobra.Command, amendments []*specamend.Amendment) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	for _, a := range amendments {
		if err := enc.Encode(map[string]any{
			"stem":           a.Stem,
			"path":           a.Path,
			"state":          a.State,
			"amendment_for":  a.AmendmentFor,
			"amendment_date": a.AmendmentDate,
			"amendment_kind": a.AmendmentKind,
			"multi":          a.Multi,
			"summary":        firstLine(a.Summary),
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeAmendmentShowText(cmd *cobra.Command, a *specamend.Amendment) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "stem:           %s\n", a.Stem)
	fmt.Fprintf(out, "path:           %s\n", a.Path)
	fmt.Fprintf(out, "state:          %s\n", a.State)
	target := a.AmendmentFor
	if a.Multi {
		target = "(multi)"
	}
	fmt.Fprintf(out, "amendment_for:  %s\n", target)
	if a.AmendmentDate != "" {
		fmt.Fprintf(out, "amendment_date: %s\n", a.AmendmentDate)
	}
	if a.AmendmentKind != "" {
		fmt.Fprintf(out, "amendment_kind: %s\n", a.AmendmentKind)
	}
	if a.Summary != "" {
		fmt.Fprintln(out, "summary:")
		for _, line := range strings.Split(a.Summary, "\n") {
			fmt.Fprintf(out, "  %s\n", line)
		}
	}
	fmt.Fprintln(out, "---")
	out.Write(a.Body)
	if len(a.Body) == 0 || a.Body[len(a.Body)-1] != '\n' {
		fmt.Fprintln(out)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// completeAmendmentStems returns every amendment stem (proposed +
// accepted) for shell completion. completeProposedAmendmentStems
// narrows to proposed-only — used by accept/reject which can only
// act on proposed amendments.
func completeAmendmentStems(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	root, err := requiredWorkspaceRoot(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	amendments, err := specamend.List(root, specamend.ListOptions{})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(amendments))
	for _, a := range amendments {
		out = append(out, a.Stem)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func completeProposedAmendmentStems(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	root, err := requiredWorkspaceRoot(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	amendments, err := specamend.List(root, specamend.ListOptions{State: specamend.StateProposed})
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(amendments))
	for _, a := range amendments {
		out = append(out, a.Stem)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}
