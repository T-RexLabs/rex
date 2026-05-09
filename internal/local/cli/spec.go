package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/specfmt"
	"github.com/asabla/rex/internal/core/specverify"
	"github.com/asabla/rex/internal/core/sync/conflict"
)

// conflictedSpecPaths returns the subset of paths that are flagged
// as in-conflict — either via a `.conflict` sidecar or via in-file
// merge markers — per sync.GIT.3.
func conflictedSpecPaths(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		flagged, err := conflict.IsConflicted(p)
		if err != nil {
			return nil, err
		}
		if flagged {
			out = append(out, p)
		}
	}
	return out, nil
}

// newSpecCmd returns the `rex spec` parent and wires its leaves.
func newSpecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spec",
		Short: "Author, validate, and inspect specs",
		Long: `Specs are the contract for work done in a workspace. Each spec is a
YAML file under .rex/specs/<id>.yaml; the format is described by
specs/spec-format.yaml.`,
		Example: `  rex spec list
  rex spec create my-feature
  rex spec validate
  rex spec acid overview.SYS.1`,
	}
	setRelated(cmd,
		"rex spec list",
		"rex spec create <id>",
		"rex spec validate",
	)
	addWorkspacePersistentFlag(cmd)
	cmd.AddCommand(newSpecCreateCmd())
	cmd.AddCommand(newSpecValidateCmd())
	cmd.AddCommand(newSpecVerifyCmd())
	cmd.AddCommand(newSpecListCmd())
	cmd.AddCommand(newSpecShowCmd())
	cmd.AddCommand(newSpecEditCmd())
	cmd.AddCommand(newSpecACIDCmd())
	cmd.AddCommand(newSpecRunsCmd())
	cmd.AddCommand(newSpecAskCmd())
	cmd.AddCommand(newSpecAmendCmd())
	return cmd
}

func newSpecCreateCmd() *cobra.Command {
	var (
		templateFlag string
		nameFlag     string
		stateFlag    string
		force        bool
	)
	cmd := &cobra.Command{
		Use:   "create <id>",
		Short: "Create a new spec from the workspace's default template (or named template)",
		Long: `Writes .rex/specs/<id>.yaml. With --template <id> the new spec
inherits the named template's tasks/components/constraints/extra
(template-marker keys stripped). Without --template, falls back to
the workspace's default template (extra.default_template_id in
.rex/workspace.yaml) if set, then to a minimal v1-shaped skeleton.

Refuses to overwrite an existing spec unless --force is passed.`,
		Example: `  rex spec create my-feature
  rex spec --workspace /path/to/ws create my-feature --name "My feature"
  rex spec create my-feature --template service`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !specfmt.IsKebab(id) {
				return fmt.Errorf("spec id %q is not kebab-case", id)
			}
			root, err := workspaceRootForOrError(workspaceFlagValue(cmd))
			if err != nil {
				return err
			}

			template, err := resolveTemplateForCreate(root, templateFlag)
			if err != nil {
				return err
			}

			path := filepath.Join(specDir(root), id+".yaml")
			if !force {
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("%s already exists; pass --force to overwrite", path)
				}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("create specs dir: %w", err)
			}

			// Two emit paths: with-template uses the typed
			// Document + yaml.Marshal so inherited tasks /
			// components round-trip cleanly. No-template uses
			// the hand-rolled MinimalSkeletonYAML so the new
			// spec ships with placeholder fields + comments
			// that yaml.Marshal of an empty Document would
			// strip.
			scaffoldOpts := specfmt.ScaffoldOptions{
				ID:       id,
				Name:     nameFlag,
				State:    stateFlag,
				Template: template,
			}
			var body []byte
			if template != nil {
				doc, err := specfmt.NewSpecFromTemplate(scaffoldOpts)
				if err != nil {
					return err
				}
				body, err = yaml.Marshal(doc)
				if err != nil {
					return fmt.Errorf("marshal spec: %w", err)
				}
			} else {
				body, err = specfmt.MinimalSkeletonYAML(scaffoldOpts)
				if err != nil {
					return err
				}
			}
			if err := os.WriteFile(path, body, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			from := "minimal skeleton"
			templateID := ""
			if template != nil {
				from = fmt.Sprintf("template %q", template.Metadata.ID)
				templateID = template.Metadata.ID
			}

			wsID, err := workspaceID(root)
			if err != nil {
				return err
			}
			if err := emitAuditEvent(cmd, root, audit.EventTypeSpecCreated, audit.SpecCreatedEvent{
				WorkspaceID: wsID,
				SpecID:      id,
				Path:        path,
				Template:    templateID,
			}); err != nil {
				return err
			}

			printConfirmation(cmd, "created %s (from %s)\n", path, from)
			return nil
		},
	}
	setRelated(cmd,
		"rex spec list",
		"rex spec validate",
		"rex spec show <id>",
	)
	cmd.Flags().StringVar(&templateFlag, "template", "", "template spec id to inherit shape from (overrides workspace default)")
	cmd.Flags().StringVar(&nameFlag, "name", "", "human-readable name (default: spec id)")
	cmd.Flags().StringVar(&stateFlag, "state", "draft", "metadata.state for the new spec")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing spec at .rex/specs/<id>.yaml")
	return cmd
}

// resolveTemplateForCreate picks the template a `spec create` should
// scaffold from. Precedence:
//   - explicit --template flag (must exist or error)
//   - workspace.yaml's extra.default_template_id (must exist or error)
//   - none (returns nil; scaffolds a minimal skeleton)
func resolveTemplateForCreate(root, explicit string) (*specfmt.Document, error) {
	id := explicit
	if id == "" {
		id = workspaceDefaultTemplateID(root)
	}
	if id == "" {
		return nil, nil
	}
	paths, err := listSpecFiles(specDir(root))
	if err != nil {
		return nil, err
	}
	ws, _, err := loadWorkspace(paths)
	if err != nil {
		return nil, err
	}
	t := ws.Template(id)
	if t == nil {
		return nil, fmt.Errorf("template %q not found in workspace (or extra.template != true)", id)
	}
	return t, nil
}

// workspaceDefaultTemplateID reads .rex/workspace.yaml and returns
// extra.default_template_id when set. Best-effort; never blocks.
func workspaceDefaultTemplateID(root string) string {
	body, err := os.ReadFile(filepath.Join(root, metaDirName, "workspace.yaml"))
	if err != nil {
		return ""
	}
	// Minimal extraction — workspace.yaml is small and we only
	// need this one key. Avoid pulling in a structured decoder.
	var raw map[string]any
	if err := yaml.Unmarshal(body, &raw); err != nil {
		return ""
	}
	extra, ok := raw[ExtraKey].(map[string]any)
	if !ok {
		return ""
	}
	v, _ := extra[specfmt.ExtraDefaultTemplateID].(string)
	return v
}

// ExtraKey is the workspace.yaml top-level key that mirrors a
// spec's extra block. Same name; different document type.
const ExtraKey = "extra"

func newSpecValidateCmd() *cobra.Command {
	var lenient bool
	cmd := &cobra.Command{
		Use:   "validate [<id-or-path>...]",
		Short: "Validate one or more specs",
		Long: `Validate specs against the v1 schema (specs/spec-format.yaml). With no
arguments, every spec in the current workspace is validated. Arguments
ending in .yaml or containing a path separator are treated as file
paths; bare identifiers are resolved to .rex/specs/<id>.yaml in the
workspace.

Per spec-format.VAL.5: exit 0 on success, 1 on any validation error,
2 on internal validator failure.`,
		Example: `  rex spec validate
  rex spec validate execution
  rex spec validate ./specs/execution.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := specfmt.ModeStrict
			if lenient {
				mode = specfmt.ModeLenient
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			workspaceFlag := workspaceFlagValue(cmd)
			paths, err := pathsFromArgs(args, workspaceFlag)
			if err != nil {
				return err
			}
			if len(paths) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "rex spec validate: no specs found")
				return nil
			}
			// sync.GIT.3: refuse to operate on conflicted specs.
			// A `.conflict` sidecar OR in-file merge markers count
			// as conflicted; either path means the user has not
			// yet run `rex sync resolve`.
			if blocked, err := conflictedSpecPaths(paths); err != nil {
				return err
			} else if len(blocked) > 0 {
				cmd.SilenceErrors = true
				for _, p := range blocked {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"rex spec validate: %s is conflicted; resolve with `rex sync resolve` first (sync.GIT.3)\n", p)
				}
				return fmt.Errorf("%d spec(s) conflicted", len(blocked))
			}
			ws, parseFailures, _ := loadWorkspace(paths)
			// Wire the workspace's default template id (if any)
			// so ValidateWorkspace's second pass enforces
			// template conformance.
			if root, _ := workspaceRootFor(workspaceFlag); root != "" {
				ws.SetDefaultTemplateID(workspaceDefaultTemplateID(root))
			}
			res := specfmt.ValidateWorkspace(ws, mode)

			// Surface parse failures alongside validate issues.
			for _, pf := range parseFailures {
				res.Issues = append(res.Issues, specfmt.Issue{
					File:     pf.Path,
					Path:     "",
					Category: "parse",
					Message:  pf.Err.Error(),
					Severity: specfmt.SeverityError,
				})
			}

			if jsonOut {
				return writeIssuesJSON(cmd.OutOrStdout(), res.Issues)
			}
			writeIssuesTextW(cmd.OutOrStdout(), res.Issues)
			fmt.Fprintf(cmd.OutOrStdout(), "\n%d spec(s) validated, %d error(s), %d warning(s)\n",
				len(paths), len(res.Errors()), len(res.Warnings()))

			if res.HasErrors() {
				// Cobra would otherwise print the error; suppress that
				// because we already produced human-readable output.
				cmd.SilenceErrors = true
				return errors.New("validation failed")
			}
			return nil
		},
	}
	setRelated(cmd,
		"rex spec list",
		"rex spec show <id>",
		"rex spec acid <ACID>",
	)
	cmd.Flags().BoolVar(&lenient, "lenient", false, "treat unknown top-level keys and dangling ACIDs as warnings")
	return cmd
}

// newSpecVerifyCmd is the disk-checking counterpart to validate.
// validate enforces schema; verify exercises every structured
// proof entry against the workspace (file exists, test func
// present, run id in events.log, commit reachable, ACID
// resolves) per spec-format.PROOF.* / VAL.7.
func newSpecVerifyCmd() *cobra.Command {
	var lenient bool
	cmd := &cobra.Command{
		Use:   "verify [<id-or-path>...]",
		Short: "Exercise structured proof entries against on-disk evidence",
		Long: `Walks every task with a structured proof block (state: done
tasks always have one per spec-format.VAL.7) and exercises each
entry against the workspace:

  - kind: code / test  → path exists on disk
  - kind: test         → optional name greps as ` + "`func <name>(`" + ` in the file
  - kind: run          → run_id appears in .rex/events.log (warning if missing)
  - kind: commit       → ref reachable via ` + "`git cat-file -e <ref>^{commit}`" + `
  - kind: spec         → ACID resolves through the workspace's specs

Distinct from ` + "`rex spec validate`" + `: validate is pure schema and
side-effect-free; verify reaches out to the filesystem, the
events.log, and the local git repo. Per spec-format.VAL.5: exit
0 on success, 1 on any verification error, 2 on internal failure.`,
		Example: `  rex spec verify
  rex spec verify execution
  rex spec verify --lenient`,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := specfmt.ModeStrict
			if lenient {
				mode = specfmt.ModeLenient
			}
			jsonOut, _ := cmd.Flags().GetBool("json")
			workspaceFlag := workspaceFlagValue(cmd)
			// verify is a read-only operation: events.log and gitDir
			// are both handled gracefully when absent (FromCLI nils
			// them; the per-proof checks emit warnings, not errors).
			// requiring .rex/ at the workspace root would block
			// running verify against a fresh clone of a repo that
			// hasn't been `rex workspace init`'d, which is exactly
			// the regression-guard use case this command exists for.
			root, err := requiredWorkspaceRoot(cmd)
			if err != nil {
				return err
			}
			paths, err := pathsFromArgs(args, workspaceFlag)
			if err != nil {
				return err
			}
			if len(paths) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "rex spec verify: no specs found")
				return nil
			}
			ws, parseFailures, _ := loadWorkspace(paths)
			ws.SetDefaultTemplateID(workspaceDefaultTemplateID(root))

			res := specverify.Verify(ws, specverify.FromCLI(root, mode))

			// Parse failures still surface — a spec we can't read is
			// a verify failure too.
			for _, pf := range parseFailures {
				res.Issues = append(res.Issues, specfmt.Issue{
					File:     pf.Path,
					Path:     "",
					Category: "parse",
					Message:  pf.Err.Error(),
					Severity: specfmt.SeverityError,
				})
			}

			if jsonOut {
				return writeIssuesJSON(cmd.OutOrStdout(), res.Issues)
			}
			writeIssuesTextW(cmd.OutOrStdout(), res.Issues)
			fmt.Fprintf(cmd.OutOrStdout(), "\n%d spec(s) verified, %d error(s), %d warning(s)\n",
				len(paths), len(res.Errors()), len(res.Warnings()))

			if res.HasErrors() {
				cmd.SilenceErrors = true
				return errors.New("verification failed")
			}
			return nil
		},
	}
	setRelated(cmd,
		"rex spec validate",
		"rex spec list",
		"rex spec show <id>",
	)
	cmd.Flags().BoolVar(&lenient, "lenient", false, "downgrade verification failures to warnings")
	return cmd
}

func newSpecListCmd() *cobra.Command {
	var stateFilter string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List specs in the workspace",
		Long: `Lists every spec known in the current workspace, optionally filtered
by metadata.state.`,
		Example: `  rex spec list
  rex spec --workspace /path/to/ws list --state active`,
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			paths, err := pathsFromArgs(nil, workspaceFlagValue(cmd))
			if err != nil {
				return err
			}
			if len(paths) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(), "rex spec list: no specs found")
				return nil
			}
			docs := make([]*specfmt.Document, 0, len(paths))
			for _, p := range paths {
				doc, err := specfmt.ParseFile(p)
				if err != nil {
					return fmt.Errorf("parse %s: %w", p, err)
				}
				if stateFilter != "" && doc.Metadata.State != stateFilter {
					continue
				}
				docs = append(docs, doc)
			}
			sort.Slice(docs, func(i, j int) bool {
				return docs[i].Metadata.ID < docs[j].Metadata.ID
			})
			if jsonOut {
				return writeSpecListJSON(cmd.OutOrStdout(), docs)
			}
			writeSpecListText(cmd.OutOrStdout(), docs)
			return nil
		},
	}
	setRelated(cmd,
		"rex spec create <id>",
		"rex spec validate",
		"rex spec show <id>",
	)
	cmd.Flags().StringVar(&stateFilter, "state", "", "only show specs with the given metadata.state")
	return cmd
}

func newSpecShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id-or-path>",
		Short: "Show a spec's metadata, components, and tasks",
		Long: `Loads one spec by workspace id or explicit file path and prints its
metadata, tasks, components, and constraints.`,
		Example: `  rex spec show execution
  rex spec show ./specs/execution.yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jsonOut, _ := cmd.Flags().GetBool("json")
			paths, err := pathsFromArgs(args, workspaceFlagValue(cmd))
			if err != nil {
				return err
			}
			if len(paths) != 1 {
				return fmt.Errorf("expected exactly one spec, got %d", len(paths))
			}
			doc, err := specfmt.ParseFile(paths[0])
			if err != nil {
				return fmt.Errorf("parse %s: %w", paths[0], err)
			}
			if jsonOut {
				return writeSpecShowJSON(cmd.OutOrStdout(), doc)
			}
			writeSpecShowText(cmd.OutOrStdout(), doc)
			return nil
		},
	}
	setRelated(cmd,
		"rex spec validate",
		"rex spec acid <ACID>",
		"rex spec list",
	)
	return cmd
}

func newSpecEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <id-or-path>",
		Short: "Open a spec in $EDITOR and re-validate on save",
		Long: `Resolves the spec by id or path and opens it in $EDITOR (default: vi).
After the editor exits, the file is re-validated; a non-zero exit on
validation surfaces the same issues 'rex spec validate' would. The
file is left edited even when validation fails — the user is expected
to re-run 'rex spec edit' or 'rex spec validate' to iterate.`,
		Example: `  rex spec edit execution
  rex spec edit ./specs/execution.yaml
  EDITOR="code --wait" rex spec edit execution`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := pathsFromArgs(args, workspaceFlagValue(cmd))
			if err != nil {
				return err
			}
			if len(paths) != 1 {
				return fmt.Errorf("expected exactly one spec, got %d", len(paths))
			}
			path := paths[0]
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("stat %s: %w", path, err)
			}

			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vi"
			}
			ed := exec.CommandContext(commandContext(cmd), editor, path)
			ed.Stdin = os.Stdin
			ed.Stdout = cmd.OutOrStdout()
			ed.Stderr = cmd.ErrOrStderr()
			if err := ed.Run(); err != nil {
				return fmt.Errorf("editor: %w", err)
			}

			// Re-validate after the edit. Mirrors `rex spec validate
			// <path>` strict mode (the bar set by CLAUDE.md is "0
			// errors, 0 warnings on every spec"). Issues surface as
			// the command's exit error so scripts wrapping this can
			// tell pass from fail.
			doc, err := specfmt.ParseFile(path)
			if err != nil {
				return fmt.Errorf("re-parse %s after edit: %w", path, err)
			}
			res := specfmt.Validate(doc, specfmt.ModeStrict)
			writeIssuesTextW(cmd.OutOrStdout(), res.Issues)
			hasErrors := res.HasErrors()

			// Emit spec.edited regardless of validation outcome —
			// the audit log records the act of editing; the
			// HasErrors flag preserves the validation verdict so
			// downstream readers can correlate. Best-effort: a
			// failure to emit is logged but doesn't shadow the
			// validation error, which is the user's primary
			// signal.
			root, rootErr := strictWorkspaceRoot(cmd)
			if rootErr == nil {
				wsID, err := workspaceID(root)
				if err == nil {
					_ = emitAuditEvent(cmd, root, audit.EventTypeSpecEdited, audit.SpecEditedEvent{
						WorkspaceID: wsID,
						SpecID:      doc.Metadata.ID,
						Path:        path,
						HasErrors:   hasErrors,
					})
				}
			}

			if hasErrors {
				return fmt.Errorf("%s has validation errors after edit", path)
			}
			return nil
		},
	}
	addWorkspacePersistentFlag(cmd)
	setRelated(cmd,
		"rex spec validate",
		"rex spec show <id>",
		"rex spec list",
	)
	return cmd
}

func newSpecACIDCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "acid <ACID>",
		Short: "Resolve an ACID reference and show the requirement",
		Long: `Accepts both fully-qualified (overview.SYS.1) and short-form
(SYS.1) ACIDs. Short-form references resolve against every spec in
the workspace; if exactly one match exists, it is printed.`,
		Example: `  rex spec acid overview.SYS.1
  rex spec --workspace /path/to/ws acid SYS.1`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := specfmt.ParseACID(args[0])
			if err != nil {
				return fmt.Errorf("malformed ACID: %w", err)
			}
			paths, err := pathsFromArgs(nil, workspaceFlagValue(cmd))
			if err != nil {
				return err
			}
			ws, parseFailures, _ := loadWorkspace(paths)
			if len(parseFailures) > 0 && len(ws.Specs()) == 0 {
				return fmt.Errorf("workspace has no parseable specs (%d failed to parse)", len(parseFailures))
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			if ref.Short {
				return resolveShortACIDAcrossWorkspace(cmd, ws, ref, jsonOut)
			}
			res := ws.Resolve(ref, "")
			if !res.Found {
				return fmt.Errorf("dangling: %s does not resolve to any known requirement", res.FullACID)
			}
			return printResolution(cmd, res, jsonOut)
		},
	}
	setRelated(cmd,
		"rex spec show <id>",
		"rex spec validate",
		"rex spec list",
	)
	return cmd
}

// resolveShortACIDAcrossWorkspace tries every spec in ws as the
// "from" spec for a short ACID. Returns success if exactly one spec
// resolves; otherwise reports the ambiguity (or the dangling state).
func resolveShortACIDAcrossWorkspace(cmd *cobra.Command, ws *specfmt.Workspace, ref specfmt.ACIDRef, jsonOut bool) error {
	var hits []specfmt.Resolution
	for _, doc := range ws.Specs() {
		r := ws.Resolve(ref, doc.Metadata.ID)
		if r.Found {
			hits = append(hits, r)
		}
	}
	switch len(hits) {
	case 0:
		return fmt.Errorf("dangling: short-form %s.%s did not resolve in any workspace spec",
			ref.Component, ref.RequirementID)
	case 1:
		return printResolution(cmd, hits[0], jsonOut)
	default:
		var ids []string
		for _, h := range hits {
			ids = append(ids, h.Doc.Metadata.ID)
		}
		sort.Strings(ids)
		return fmt.Errorf("ambiguous short-form %s.%s — resolves in multiple specs: %v",
			ref.Component, ref.RequirementID, ids)
	}
}

// pathsFromArgs maps positional arguments to spec file paths. When
// args is empty, walks the workspace's specs/ directory. workspaceFlag
// overrides the CWD-walk. Errors are returned for unresolvable args
// — including errNoWorkspace when args is empty and neither --workspace
// nor a CWD-anchored .rex/ exists, so the caller surfaces the same
// "no rex workspace" message every other top-level command does.
func pathsFromArgs(args []string, workspaceFlag string) ([]string, error) {
	if len(args) == 0 {
		root, err := workspaceRootFor(workspaceFlag)
		if err != nil {
			return nil, err
		}
		if root == "" {
			return nil, errNoWorkspace
		}
		return listSpecFiles(specDir(root))
	}
	root, _ := workspaceRootFor(workspaceFlag) // may be empty; only needed for id args
	out := make([]string, 0, len(args))
	for _, arg := range args {
		path, err := resolveSpecArg(root, arg)
		if err != nil {
			return nil, err
		}
		out = append(out, path)
	}
	return out, nil
}

// workspaceRootFor returns the explicit flag value when set, else
// walks up from cwd looking for .rex/. Returns "" with no error when
// neither source yields a workspace — leaf commands handle that with
// their own friendlier message.
func workspaceRootFor(flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root, err := findWorkspaceRoot(cwd)
	if err != nil {
		if errors.Is(err, errNoWorkspace) {
			return "", nil
		}
		return "", err
	}
	return root, nil
}

// --- text output helpers ---

// writeIssuesTextW prints validator issues in a stable human form.
// The function takes io.Writer rather than *cobra.Command so it can
// be reused by other surfaces (tests, alternate frontends).
func writeIssuesTextW(w writerLike, issues []specfmt.Issue) {
	if len(issues) == 0 {
		fmt.Fprintln(w, "ok")
		return
	}
	for _, issue := range specfmt.SortIssues(issues) {
		fmt.Fprintln(w, issue)
	}
}

// writerLike is the io.Writer surface we actually use; declared here
// so non-cobra callers can supply *bytes.Buffer in tests.
type writerLike interface {
	Write([]byte) (int, error)
}

func writeIssuesJSON(w writerLike, issues []specfmt.Issue) error {
	enc := json.NewEncoder(w)
	for _, issue := range specfmt.SortIssues(issues) {
		if err := enc.Encode(map[string]any{
			"file":     issue.File,
			"path":     issue.Path,
			"category": issue.Category,
			"message":  issue.Message,
			"severity": issue.Severity.String(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeSpecListText(w writerLike, docs []*specfmt.Document) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tNAME\tTASKS\tCOMPONENTS")
	for _, d := range docs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n",
			d.Metadata.ID, d.Metadata.State, d.Metadata.Name,
			len(d.Tasks), len(d.Components))
	}
	_ = tw.Flush()
}

func writeSpecListJSON(w writerLike, docs []*specfmt.Document) error {
	enc := json.NewEncoder(w)
	for _, d := range docs {
		if err := enc.Encode(map[string]any{
			"id":         d.Metadata.ID,
			"state":      d.Metadata.State,
			"name":       d.Metadata.Name,
			"tasks":      len(d.Tasks),
			"components": len(d.Components),
			"path":       d.Path,
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeSpecShowText(w writerLike, d *specfmt.Document) {
	fmt.Fprintf(w, "id:    %s\n", d.Metadata.ID)
	fmt.Fprintf(w, "name:  %s\n", d.Metadata.Name)
	fmt.Fprintf(w, "state: %s\n", d.Metadata.State)
	fmt.Fprintf(w, "tasks: %d\n", len(d.Tasks))
	if len(d.Tasks) > 0 {
		for _, t := range d.Tasks {
			fmt.Fprintf(w, "  - [%s] %s — %s\n", t.State, t.ID, t.Description)
		}
	}
	fmt.Fprintf(w, "components: %d\n", len(d.Components))
	for _, id := range d.ComponentOrder() {
		c := d.Components[id]
		fmt.Fprintf(w, "  %s — %s (%d req)\n", id, c.Name, len(c.Requirements))
	}
	if len(d.Constraints) > 0 {
		fmt.Fprintf(w, "constraints: %d\n", len(d.Constraints))
		for _, id := range d.ConstraintOrder() {
			c := d.Constraints[id]
			fmt.Fprintf(w, "  %s — %s (%d req)\n", id, c.Description, len(c.Requirements))
		}
	}
}

func writeSpecShowJSON(w writerLike, d *specfmt.Document) error {
	out := map[string]any{
		"id":          d.Metadata.ID,
		"name":        d.Metadata.Name,
		"state":       d.Metadata.State,
		"tasks":       d.Tasks,
		"components":  d.ComponentOrder(),
		"constraints": d.ConstraintOrder(),
		"path":        d.Path,
	}
	return json.NewEncoder(w).Encode(out)
}

func printResolution(cmd *cobra.Command, r specfmt.Resolution, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
			"acid":         r.FullACID,
			"in_component": r.InComponent,
			"group":        r.GroupID,
			"text":         r.Requirement.Text,
			"deprecated":   r.Requirement.Deprecated,
			"replaced_by":  r.Requirement.ReplacedBy,
			"notes":        r.Requirement.Notes,
		})
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s\n", r.FullACID)
	fmt.Fprintf(out, "  group: %s (%s)\n", r.GroupID, groupKind(r.InComponent))
	fmt.Fprintf(out, "  text:  %s\n", r.Requirement.Text)
	if r.Requirement.Deprecated {
		fmt.Fprintln(out, "  status: DEPRECATED")
	}
	if r.Requirement.ReplacedBy != "" {
		fmt.Fprintf(out, "  replaced_by: %s\n", r.Requirement.ReplacedBy)
	}
	if r.Requirement.Notes != "" {
		fmt.Fprintf(out, "  notes: %s\n", r.Requirement.Notes)
	}
	return nil
}

func groupKind(inComponent bool) string {
	if inComponent {
		return "component"
	}
	return "constraint"
}
