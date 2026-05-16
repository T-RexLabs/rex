package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/asabla/rex/internal/core/storage/synccat"
	"github.com/asabla/rex/internal/core/sync/conflict"
	"github.com/asabla/rex/internal/core/sync/merge3"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// RebaseOutcome categorises what happened to one entity during a
// rebase pass. Drives the CLI summary line and the audit-log event.
type RebaseOutcome int

const (
	// RebaseClean means the merge produced no conflicts and the
	// local file now contains the merged content. The base-content
	// cache has been updated to the new agreed revision.
	RebaseClean RebaseOutcome = iota

	// RebaseUnchanged means there was nothing to merge — local and
	// remote already agreed on the entity content.
	RebaseUnchanged

	// RebaseLocalOnly means remote has not seen this entity yet
	// (404 on /sync/git GET). Local is the only source; nothing to
	// rebase against. Not an error condition.
	RebaseLocalOnly

	// RebaseConflict means the merge surfaced unresolvable hunks.
	// The local file now carries conflict markers and the sidecar
	// describes the regions that need human resolution.
	RebaseConflict
)

// String renders the outcome for log lines.
func (o RebaseOutcome) String() string {
	switch o {
	case RebaseClean:
		return "clean"
	case RebaseUnchanged:
		return "unchanged"
	case RebaseLocalOnly:
		return "local-only"
	case RebaseConflict:
		return "conflict"
	}
	return "unknown"
}

// RebaseResult is the outcome of a single-entity rebase pass.
type RebaseResult struct {
	Entity         string
	Outcome        RebaseOutcome
	LocalRevision  string
	RemoteRevision string
	BaseRevision   string
	Hunks          int
}

// RebaseEntity rebases a single git-merged entity against the
// configured remote (sync.GIT.1, GIT.2). The full flow:
//
//  1. Validate entity is in the git_merged sync category.
//  2. Read the local file from <workspaceRoot>/.rex/<entity>.
//  3. Fetch the remote revision via /sync/git GET.
//  4. Read the cached base content for (remote, entity), if any.
//  5. Run merge3.
//  6. Write the merged (or conflict-marked) content to disk.
//  7. On a clean merge, refresh the base-content cache so the next
//     rebase has an accurate merge base.
//  8. On a conflict, write a sidecar (sync.GIT.3) and leave the
//     base-content cache unchanged so the next rebase still sees
//     the pre-conflict ancestor.
//
// The function never advances the events.log watermark — that is
// the events-side push/pull's job. RebaseEntity is purely about
// git-merged content.
func (c *Client) RebaseEntity(ctx context.Context, args RunArgs, entity string) (RebaseResult, error) {
	if args.WorkspaceRoot == "" || args.Remote == "" {
		return RebaseResult{}, errors.New("sync: RebaseEntity requires WorkspaceRoot + Remote")
	}
	if args.WorkspaceID == "" {
		return RebaseResult{}, errors.New("sync: RebaseEntity requires WorkspaceID for git-scoped pull/push")
	}
	if cat, ok := synccat.Categorize(entity); !ok || cat != synccat.CategoryGitMerged {
		return RebaseResult{}, fmt.Errorf("sync: %q is not a git_merged entity (sync.CAT.5)", entity)
	}

	localPath := filepath.Join(args.WorkspaceRoot, ".rex", entity)
	local, err := readFileOrEmpty(localPath)
	if err != nil {
		return RebaseResult{}, err
	}
	localRev := proto.GitContentRevision(string(local))

	res := RebaseResult{Entity: entity, LocalRevision: localRev}

	remote, err := c.GitPull(ctx, args.WorkspaceID, entity)
	if err != nil {
		if errors.Is(err, ErrUnknownGitEntity) {
			res.Outcome = RebaseLocalOnly
			return res, nil
		}
		return res, err
	}
	res.RemoteRevision = remote.Revision

	base, baseRev, err := loadBase(args.WorkspaceRoot, args.Remote, entity)
	if err != nil {
		return res, err
	}
	res.BaseRevision = baseRev

	// Both sides agree → nothing to merge.
	if remote.Revision == localRev {
		res.Outcome = RebaseUnchanged
		// Refresh base cache so a future rebase has the right
		// ancestor even if it was previously absent.
		if err := saveBase(args.WorkspaceRoot, args.Remote, entity, local); err != nil {
			return res, err
		}
		// Drop any stale sidecar from a prior failed rebase —
		// the entity is no longer in conflict.
		_ = conflict.Clear(conflict.SidecarPathFor(localPath))
		return res, nil
	}

	merged := merge3.Merge(base, local, []byte(remote.Content))

	conflictPath := conflict.SidecarPathFor(localPath)
	if merged.Clean() {
		if err := writeFile(localPath, merged.Merged); err != nil {
			return res, err
		}
		// Successful merge → refresh base cache to the merged
		// content. After this point local matches remote in
		// content but not yet in revision-id (revision is
		// content-addressable and changes if local edits beyond
		// the merge stay around). The next push will reconcile.
		if err := saveBase(args.WorkspaceRoot, args.Remote, entity, merged.Merged); err != nil {
			return res, err
		}
		// Best-effort cleanup: if a stale sidecar exists from a
		// prior conflict, drop it now that the merge is clean.
		_ = conflict.Clear(conflictPath)
		res.Outcome = RebaseClean
		return res, nil
	}

	// Conflict: write the marker-laden content to disk plus a
	// sidecar describing each unresolved region.
	if err := writeFile(localPath, merged.Merged); err != nil {
		return res, err
	}
	side := conflict.Sidecar{
		Entity:         entity,
		Remote:         args.Remote,
		BaseRevision:   baseRev,
		LocalRevision:  localRev,
		RemoteRevision: remote.Revision,
		CreatedAt:      timeNowUTC(),
		Hunks:          toSidecarHunks(merged.Conflicts),
	}
	if err := conflict.Write(conflictPath, side); err != nil {
		return res, err
	}
	res.Outcome = RebaseConflict
	res.Hunks = len(merged.Conflicts)
	return res, nil
}

// gitBaseDir returns the directory under .rex/drafts that caches
// per-remote agreed-upon base content for git-merged entities.
//
// Layout: .rex/drafts/<remote>.git/<entity-path>
//
// Mirrors the entity's relative path so re-locating the workspace
// keeps the cache consistent.
func gitBaseDir(workspaceRoot, remote string) string {
	return filepath.Join(workspaceRoot, ".rex", "drafts", remote+".git")
}

func gitBasePath(workspaceRoot, remote, entity string) string {
	return filepath.Join(gitBaseDir(workspaceRoot, remote), filepath.FromSlash(entity))
}

func loadBase(workspaceRoot, remote, entity string) ([]byte, string, error) {
	body, err := os.ReadFile(gitBasePath(workspaceRoot, remote, entity))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("sync: read git base: %w", err)
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:]), nil
}

func saveBase(workspaceRoot, remote, entity string, content []byte) error {
	path := gitBasePath(workspaceRoot, remote, entity)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sync: mkdir git base: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gitbase-*")
	if err != nil {
		return fmt.Errorf("sync: tempfile: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("sync: write git base: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("sync: rename git base: %w", err)
	}
	return nil
}

func readFileOrEmpty(path string) ([]byte, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("sync: read %s: %w", path, err)
	}
	return body, nil
}

func writeFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("sync: mkdir %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".rebase-*")
	if err != nil {
		return fmt.Errorf("sync: tempfile: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("sync: rename %s: %w", path, err)
	}
	return nil
}

func toSidecarHunks(in []merge3.ConflictHunk) []conflict.Hunk {
	out := make([]conflict.Hunk, len(in))
	for i, h := range in {
		out[i] = conflict.Hunk{
			BaseStart:   h.BaseStart,
			BaseEnd:     h.BaseEnd,
			LocalStart:  h.LocalStart,
			LocalEnd:    h.LocalEnd,
			RemoteStart: h.RemoteStart,
			RemoteEnd:   h.RemoteEnd,
			BaseLines:   h.BaseLines,
			LocalLines:  h.LocalLines,
			RemoteLines: h.RemoteLines,
		}
	}
	return out
}

// timeNowUTC is a function variable so tests can pin it. The default
// reads the real wall clock; tests that need determinism override.
var timeNowUTC = func() time.Time { return time.Now().UTC() }
