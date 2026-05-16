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
