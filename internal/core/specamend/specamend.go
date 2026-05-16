// Package specamend implements the on-disk lifecycle for spec
// amendments per spec-format.AMEND.*: locating proposed and
// accepted amendments under .rex/specs/_proposed/ (and its
// _accepted/ subdirectory), parsing their frontmatter, and
// performing the proposed→accepted move + state rewrite and the
// proposed→rejected delete.
//
// Acceptance does NOT modify the target spec (AMEND.5) — folding
// the amendment's edits into the target is a separate manual or
// harness-driven step. This package owns the lifecycle bookkeeping
// only: file location, state rewrite, and audit-payload synthesis.
package specamend

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// State is the lifecycle position of an amendment file. Two
// observable values: Proposed (file under _proposed/) and Accepted
// (file under _proposed/_accepted/). Rejection deletes the file
// rather than transitioning to a state.
type State string

const (
	StateProposed State = "proposed"
	StateAccepted State = "accepted"
)

// Amendment is the parsed view of an on-disk amendment file. The
// fields capture spec-format.AMEND.2 frontmatter (required) plus
// AMEND.3 optional fields. Body is the raw YAML bytes for surfaces
// that want to render the file verbatim.
type Amendment struct {
	// Stem is the filename without the .yaml extension.
	Stem string
	// Path is the absolute path to the file.
	Path string
	// State is derived from the directory the file sits in
	// (_proposed/ → Proposed, _proposed/_accepted/ → Accepted),
	// not from the in-file `state:` field. The two should agree
	// for a well-formed amendment; the directory wins.
	State State
	// AmendmentFor is the target spec id from the frontmatter.
	// Empty when the amendment uses `target: multi`.
	AmendmentFor string
	// AmendmentDate is the YYYY-MM-DD string from the frontmatter.
	AmendmentDate string
	// Summary is the free-form summary string from the frontmatter.
	Summary string
	// AmendmentKind is the optional `amendment_kind` field
	// (additive | breaking | clarifying) — empty when omitted.
	AmendmentKind string
	// Multi is true when frontmatter has `target: multi` — the
	// amendment touches more than one spec (AMEND.6).
	Multi bool
	// Body is the raw file content; used by surfaces that render
	// the amendment YAML verbatim.
	Body []byte
}

// frontmatter is the subset of YAML keys this package parses out
// of an amendment file. Fields not listed here pass through
// untouched in Body. yaml.v3 is forgiving about unknown keys, so
// authors can add `changes:` / `verification:` / `extra:` blocks
// without this package needing to model them.
type frontmatter struct {
	AmendmentFor  string `yaml:"amendment_for"`
	AmendmentDate string `yaml:"amendment_date"`
	State         string `yaml:"state"`
	Summary       string `yaml:"summary"`
	AmendmentKind string `yaml:"amendment_kind"`
	Target        string `yaml:"target"`
}

// Dir returns the proposed-amendments directory under a workspace
// root. It is unconditional — the directory may not exist on disk
// for a workspace that has never authored an amendment, and List
// handles that case as "empty result".
func Dir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, ".rex", "specs", "_proposed")
}

// AcceptedDir returns the accepted-amendments subdirectory under a
// workspace root.
func AcceptedDir(workspaceRoot string) string {
	return filepath.Join(Dir(workspaceRoot), "_accepted")
}

// stemPattern is the compiled form of the AMEND.1 filename rule —
// `<target-spec-id>-amendment-<YYYY-MM-DD>[-<slug>]`. Used only as
// a soft validator when surfacing parse errors; it does not gate
// loading (a file with an unrecognised stem is still readable).
var stemPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*-amendment-\d{4}-\d{2}-\d{2}(?:-[a-z0-9]+(?:-[a-z0-9]+)*)?$`)

// IsValidStem reports whether s matches the AMEND.1 filename rule.
// Surfaces use this only for warnings; loading a file with a stem
// that doesn't match still returns the parsed amendment.
func IsValidStem(s string) bool {
	return stemPattern.MatchString(s)
}

// ListOptions filters the result of List. Empty fields mean "no
// filter on this dimension".
type ListOptions struct {
	// State, when non-empty, restricts the result to a single
	// lifecycle state. The zero value lists both.
	State State
	// For, when non-empty, restricts the result to amendments
	// targeting the given spec id. `target: multi` amendments are
	// included only when For is empty.
	For string
}

// List walks the proposed and accepted amendment directories under
// workspaceRoot, parses each .yaml file, and returns the matches
// sorted by AmendmentDate descending then by Stem.
//
// Missing directories are not an error — a workspace that has
// never authored an amendment yields an empty slice.
func List(workspaceRoot string, opts ListOptions) ([]*Amendment, error) {
	out := make([]*Amendment, 0, 8)

	if opts.State == "" || opts.State == StateProposed {
		proposed, err := listDir(Dir(workspaceRoot), StateProposed)
		if err != nil {
			return nil, err
		}
		out = append(out, proposed...)
	}
	if opts.State == "" || opts.State == StateAccepted {
		accepted, err := listDir(AcceptedDir(workspaceRoot), StateAccepted)
		if err != nil {
			return nil, err
		}
		out = append(out, accepted...)
	}

	if opts.For != "" {
		filtered := out[:0]
		for _, a := range out {
			if a.AmendmentFor == opts.For {
				filtered = append(filtered, a)
			}
		}
		out = filtered
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].AmendmentDate != out[j].AmendmentDate {
			return out[i].AmendmentDate > out[j].AmendmentDate
		}
		return out[i].Stem < out[j].Stem
	})
	return out, nil
}

func listDir(dir string, state State) ([]*Amendment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("specamend: read %s: %w", dir, err)
	}
	out := make([]*Amendment, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		a, err := parseFile(path, state)
		if err != nil {
			// A parse failure on one amendment does not block the
			// listing of others — surface a placeholder with the
			// error in the Summary so the UI can render it.
			out = append(out, &Amendment{
				Stem:    strings.TrimSuffix(e.Name(), ".yaml"),
				Path:    path,
				State:   state,
				Summary: "parse error: " + err.Error(),
			})
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// Load resolves a stem (filename without .yaml) to an amendment.
// Searches _proposed/ first, then _proposed/_accepted/. Returns
// fs.ErrNotExist when neither contains the file.
func Load(workspaceRoot, stem string) (*Amendment, error) {
	if stem == "" {
		return nil, errors.New("specamend: empty stem")
	}
	stem = strings.TrimSuffix(stem, ".yaml")
	for _, candidate := range []struct {
		dir   string
		state State
	}{
		{Dir(workspaceRoot), StateProposed},
		{AcceptedDir(workspaceRoot), StateAccepted},
	} {
		path := filepath.Join(candidate.dir, stem+".yaml")
		if _, err := os.Stat(path); err == nil {
			return parseFile(path, candidate.state)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("specamend: stat %s: %w", path, err)
		}
	}
	return nil, fmt.Errorf("specamend: amendment %q not found: %w", stem, fs.ErrNotExist)
}

func parseFile(path string, state State) (*Amendment, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".yaml")
	a, err := ParseAmendmentBytes(stem, body, state)
	if err != nil {
		return nil, fmt.Errorf("yaml %s: %w", path, err)
	}
	a.Path = path
	return a, nil
}

// ParseAmendmentBytes is the filesystem-agnostic parse path: it
// takes the amendment's stem + raw YAML bytes + the lifecycle
// state implied by the source (proposed / accepted) and returns
// the projected Amendment. Used by callers that don't have a
// filesystem path — notably the central web shell, which projects
// amendments from the GitStore.
//
// On success Amendment.Path is left empty; callers with a path
// should set it after the call. Returns a non-nil error only on
// YAML decode failure.
func ParseAmendmentBytes(stem string, body []byte, state State) (*Amendment, error) {
	var fm frontmatter
	if err := yaml.Unmarshal(body, &fm); err != nil {
		return nil, fmt.Errorf("yaml %s: %w", stem, err)
	}
	return &Amendment{
		Stem:          stem,
		State:         state,
		AmendmentFor:  fm.AmendmentFor,
		AmendmentDate: fm.AmendmentDate,
		Summary:       strings.TrimSpace(fm.Summary),
		AmendmentKind: fm.AmendmentKind,
		Multi:         fm.Target == "multi",
		Body:          body,
	}, nil
}

// AcceptResult captures the file movement performed by Accept and
// is the input the caller uses to synthesise a SpecAmendmentEvent
// audit row.
type AcceptResult struct {
	Stem          string
	AmendmentFor  string
	AmendmentDate string
	FromPath      string
	ToPath        string
}

// Accept moves the named amendment from _proposed/ to
// _proposed/_accepted/, rewrites its in-file `state: proposed`
// field to `state: accepted` (adding the field if it is missing),
// and returns the file movement so the caller can emit
// spec.amendment.accepted.
//
// The function refuses to operate on an amendment already in the
// accepted directory (idempotent guard) and refuses to overwrite
// an existing destination. It does not modify the target spec —
// folding the amendment's edits into the target is the author's
// next step (AMEND.5).
func Accept(workspaceRoot, stem string) (*AcceptResult, error) {
	if stem == "" {
		return nil, errors.New("specamend: empty stem")
	}
	stem = strings.TrimSuffix(stem, ".yaml")
	srcPath := filepath.Join(Dir(workspaceRoot), stem+".yaml")
	dstDir := AcceptedDir(workspaceRoot)
	dstPath := filepath.Join(dstDir, stem+".yaml")

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if _, accepted := os.Stat(dstPath); accepted == nil {
				return nil, fmt.Errorf("specamend: %s is already accepted", stem)
			}
			return nil, fmt.Errorf("specamend: amendment %q not found in _proposed/", stem)
		}
		return nil, fmt.Errorf("specamend: stat src: %w", err)
	}
	if srcInfo.IsDir() {
		return nil, fmt.Errorf("specamend: %s is a directory, not an amendment file", srcPath)
	}
	if _, err := os.Stat(dstPath); err == nil {
		return nil, fmt.Errorf("specamend: destination %s already exists; refusing to overwrite", dstPath)
	}

	body, err := os.ReadFile(srcPath)
	if err != nil {
		return nil, fmt.Errorf("specamend: read src: %w", err)
	}
	var fm frontmatter
	_ = yaml.Unmarshal(body, &fm)

	rewritten := rewriteState(body, StateAccepted)

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return nil, fmt.Errorf("specamend: mkdir %s: %w", dstDir, err)
	}
	if err := os.WriteFile(dstPath, rewritten, 0o644); err != nil {
		return nil, fmt.Errorf("specamend: write dst: %w", err)
	}
	if err := os.Remove(srcPath); err != nil {
		// Best-effort cleanup: the file is already at the
		// destination, but removing the source failed. Roll
		// back the destination write so the user doesn't end
		// up with two copies.
		_ = os.Remove(dstPath)
		return nil, fmt.Errorf("specamend: remove src: %w", err)
	}

	return &AcceptResult{
		Stem:          stem,
		AmendmentFor:  fm.AmendmentFor,
		AmendmentDate: fm.AmendmentDate,
		FromPath:      srcPath,
		ToPath:        dstPath,
	}, nil
}

// RejectResult captures the file removal performed by Reject and
// is the input the caller uses to synthesise a SpecAmendmentEvent
// audit row.
type RejectResult struct {
	Stem          string
	AmendmentFor  string
	AmendmentDate string
	FromPath      string
}

// Reject deletes the named proposed amendment and returns the file
// metadata so the caller can emit spec.amendment.rejected.
//
// The function refuses to delete files under _proposed/_accepted/
// — accepted amendments are part of the audit trail and must not
// be retroactively rejected.
func Reject(workspaceRoot, stem string) (*RejectResult, error) {
	if stem == "" {
		return nil, errors.New("specamend: empty stem")
	}
	stem = strings.TrimSuffix(stem, ".yaml")
	srcPath := filepath.Join(Dir(workspaceRoot), stem+".yaml")

	if _, err := os.Stat(srcPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			acceptedPath := filepath.Join(AcceptedDir(workspaceRoot), stem+".yaml")
			if _, accepted := os.Stat(acceptedPath); accepted == nil {
				return nil, fmt.Errorf("specamend: %s is accepted; cannot reject (delete the file by hand if you must)", stem)
			}
			return nil, fmt.Errorf("specamend: amendment %q not found", stem)
		}
		return nil, fmt.Errorf("specamend: stat src: %w", err)
	}

	body, _ := os.ReadFile(srcPath)
	var fm frontmatter
	_ = yaml.Unmarshal(body, &fm)

	if err := os.Remove(srcPath); err != nil {
		return nil, fmt.Errorf("specamend: remove src: %w", err)
	}

	return &RejectResult{
		Stem:          stem,
		AmendmentFor:  fm.AmendmentFor,
		AmendmentDate: fm.AmendmentDate,
		FromPath:      srcPath,
	}, nil
}

// stateLineRe matches a top-level `state:` line in an amendment
// file, capturing leading whitespace (always empty for top-level)
// and the value. We deliberately keep the regex line-anchored and
// avoid a structural YAML rewrite: amendments contain
// author-written comments and we want the rewrite to preserve
// them verbatim. yaml.Marshal would strip comments.
var stateLineRe = regexp.MustCompile(`(?m)^state:[ \t]+\S+[ \t]*$`)

// rewriteState updates the top-level `state:` line in body to the
// given value, preserving every other byte. If no `state:` line
// exists, one is appended after the last frontmatter line (just
// before the first comment-only or blank line that follows the
// initial frontmatter block) — concretely, after the last
// `^[a-z_]+:` line at column 0 before any nested content.
//
// In practice every amendment authored after AMEND.2 has a
// `state:` line; the append branch is a safety net.
func rewriteState(body []byte, to State) []byte {
	target := []byte("state: " + string(to))
	if stateLineRe.Match(body) {
		return stateLineRe.ReplaceAll(body, target)
	}
	// No state line — append at end with a leading newline if the
	// file doesn't end in one.
	out := make([]byte, 0, len(body)+len(target)+2)
	out = append(out, body...)
	if len(out) == 0 || out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, target...)
	out = append(out, '\n')
	return out
}
