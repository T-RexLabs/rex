package sync

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/asabla/rex/internal/core/sync/proto"
)

// GitConflictError is returned by GitPush when the server's
// current revision for the entity does not match the supplied
// baseRevision. ServerCurrent carries the server-side revision so
// the rebase engine can three-way merge against it without a
// follow-up GET.
type GitConflictError struct {
	Entity        string
	ServerCurrent proto.GitEntity
}

// Error reports the conflict in single-line form.
func (e *GitConflictError) Error() string {
	return fmt.Sprintf("sync: git revision conflict for %q (server has %s)",
		e.Entity, e.ServerCurrent.Revision)
}

// IsGitConflict reports whether err is a *GitConflictError.
func IsGitConflict(err error) bool {
	var ge *GitConflictError
	return errors.As(err, &ge)
}

// GitPull fetches the current revision of (workspaceID, entity)
// from the central node. Returns proto.GitEntity{} + a wrapped
// 404 error when the entity has never been pushed for the
// workspace (callers branch on errors.Is against
// ErrUnknownGitEntity).
func (c *Client) GitPull(ctx context.Context, workspaceID, entity string) (proto.GitEntity, error) {
	if workspaceID == "" {
		return proto.GitEntity{}, errors.New("sync: GitPull requires a non-empty workspace id")
	}
	if entity == "" {
		return proto.GitEntity{}, errors.New("sync: GitPull requires a non-empty entity path")
	}
	url := c.baseURL + "/sync/git/ws/" + url2PathEscape(workspaceID) + "/" + escapeEntity(entity)
	resp, err := c.doAuthorized(ctx, http.MethodGet, url, nil)
	if err != nil {
		return proto.GitEntity{}, fmt.Errorf("sync: GET /sync/git: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var pull proto.GitPullResponse
		if err := json.NewDecoder(resp.Body).Decode(&pull); err != nil {
			return proto.GitEntity{}, fmt.Errorf("sync: decode git pull: %w", err)
		}
		return pull.Entity, nil
	case http.StatusNotFound:
		body, _ := io.ReadAll(resp.Body)
		var er proto.ErrorResponse
		_ = json.Unmarshal(body, &er)
		return proto.GitEntity{}, fmt.Errorf("%w: %s", ErrUnknownGitEntity, er.Message)
	default:
		return proto.GitEntity{}, decodeError(resp)
	}
}

// ErrUnknownGitEntity is returned by GitPull when the server has no
// record of entity (404). Mirrors the central server-side error for
// type-symmetry across the API.
var ErrUnknownGitEntity = errors.New("sync: git entity not on remote")

// GitPush sends a (workspaceID, entity, baseRevision, content,
// signature) tuple to the central node. Returns the
// server-assigned revision on success, *GitConflictError on 409,
// or a generic error otherwise.
//
// The caller signs the canonical input via proto.GitSigningBytes
// (which now binds workspaceID into the signature) before passing
// the hex-encoded signature here.
func (c *Client) GitPush(ctx context.Context, workspaceID, entity, baseRevision, content, signatureHex string) (proto.GitPushResponse, error) {
	if workspaceID == "" {
		return proto.GitPushResponse{}, errors.New("sync: GitPush requires a non-empty workspace id")
	}
	body, err := json.Marshal(proto.GitPushRequest{
		WorkspaceID:  workspaceID,
		Entity:       entity,
		BaseRevision: baseRevision,
		Content:      content,
		Signature:    signatureHex,
	})
	if err != nil {
		return proto.GitPushResponse{}, fmt.Errorf("sync: marshal git push: %w", err)
	}
	resp, err := c.doAuthorized(ctx, http.MethodPost, c.baseURL+"/sync/git",
		func() (io.Reader, error) { return bytes.NewReader(body), nil })
	if err != nil {
		return proto.GitPushResponse{}, fmt.Errorf("sync: POST /sync/git: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var pr proto.GitPushResponse
		if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
			return proto.GitPushResponse{}, fmt.Errorf("sync: decode git push: %w", err)
		}
		return pr, nil
	case http.StatusConflict:
		var con proto.GitConflictResponse
		if err := json.NewDecoder(resp.Body).Decode(&con); err != nil {
			return proto.GitPushResponse{}, fmt.Errorf("sync: decode git conflict: %w", err)
		}
		return proto.GitPushResponse{}, &GitConflictError{
			Entity: entity,
			ServerCurrent: proto.GitEntity{
				Path:      entity,
				Revision:  con.ServerRevision,
				Content:   con.ServerContent,
				Signature: con.ServerSignature,
				Actor:     con.ServerActor,
				UpdatedAt: con.ServerUpdatedAt,
			},
		}
	default:
		return proto.GitPushResponse{}, decodeError(resp)
	}
}

// _ verifies hex.EncodeToString is referenced — used by callers that
// pass already-hex-encoded signatures, kept here for symmetry.
var _ = hex.EncodeToString

// GitSyncReport summarises a GitSyncOnly run: which entries
// were freshly pushed, which were already up to date, which
// conflicted. Conflict surfaces an entry list rather than
// aborting the whole sync so a partial batch isn't lost.
type GitSyncReport struct {
	Pushed     []string // entries the server accepted (created or updated)
	Unchanged  []string // entries whose local revision matched the server's
	Conflicted []string // entries the server rejected with a revision conflict
}

// GitSyncOnly walks the workspace's git-merged files and pushes
// each to the central via /sync/git. For each entry:
//
//   - GitPull to learn the server's current revision (404 = new).
//   - Skip when the local content's revision already matches.
//   - Otherwise GitPush with baseRevision = server's current
//     (or empty when the entry is new). Conflicts go into the
//     report rather than aborting — the user can fix the
//     diverged file and re-run.
//
// A configured Signer is required: each push canonical-signs
// the (workspaceID, path, baseRevision, content) tuple per
// sync.SEC.1.
func (c *Client) GitSyncOnly(ctx context.Context, workspaceID string, entries []GitEntry) (GitSyncReport, error) {
	if workspaceID == "" {
		return GitSyncReport{}, errors.New("sync: GitSyncOnly requires a workspace id")
	}
	if c.signer == nil {
		return GitSyncReport{}, errors.New("sync: GitSyncOnly requires a Signer (pass via WithSigner)")
	}
	var report GitSyncReport
	for _, entry := range entries {
		baseRev := ""
		newRev := proto.GitContentRevision(entry.Content)
		if current, err := c.GitPull(ctx, workspaceID, entry.Path); err == nil {
			if current.Revision == newRev {
				report.Unchanged = append(report.Unchanged, entry.Path)
				continue
			}
			baseRev = current.Revision
		} else if !errors.Is(err, ErrUnknownGitEntity) {
			return report, fmt.Errorf("git pull %q: %w", entry.Path, err)
		}
		canonical, err := proto.GitSigningBytes(workspaceID, entry.Path, baseRev, entry.Content)
		if err != nil {
			return report, fmt.Errorf("canonical %q: %w", entry.Path, err)
		}
		sig, err := c.signer.Sign(ctx, canonical)
		if err != nil {
			return report, fmt.Errorf("sign %q: %w", entry.Path, err)
		}
		if _, err := c.GitPush(ctx, workspaceID, entry.Path, baseRev, entry.Content, hex.EncodeToString(sig)); err != nil {
			if IsGitConflict(err) {
				report.Conflicted = append(report.Conflicted, entry.Path)
				continue
			}
			return report, fmt.Errorf("git push %q: %w", entry.Path, err)
		}
		report.Pushed = append(report.Pushed, entry.Path)
	}
	return report, nil
}

// url2PathEscape escapes a single path segment (no slashes). The
// alias keeps the import-renaming optional at the call site.
var url2PathEscape = url.PathEscape

// escapeEntity URL-encodes each path segment but preserves the slashes
// so /sync/git/specs/sync.yaml routes correctly through the Go 1.22
// {entity...} pattern. Same idea as path.Join but without collapsing.
func escapeEntity(entity string) string {
	parts := splitOnSlash(entity)
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return joinSlash(parts)
}

func splitOnSlash(s string) []string {
	out := []string{}
	cur := ""
	for _, r := range s {
		if r == '/' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}

func joinSlash(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "/"
		}
		out += p
	}
	return out
}
