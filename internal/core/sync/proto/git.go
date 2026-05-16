package proto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// GitSigningVersion tags the canonical signing-input format clients
// use when pushing to /sync/git. Bump alongside any semantic change to
// GitSigningInput; readers refuse other versions per overview.SYS.4.
const GitSigningVersion = "rex-git-v1"

// GitEntity is one stored revision of a git-merged entity
// (sync.CAT.2 / sync.API.4). The wire format on GET responses and the
// shape persisted by the server.
type GitEntity struct {
	// Path is the entity's `.rex/`-relative path, e.g.
	// "specs/sync.yaml" or "workspace.yaml".
	Path string `json:"path"`
	// Revision is the server-assigned content hash for this
	// snapshot of the entity. Format: hex SHA-256 of Content.
	Revision string `json:"revision"`
	// Content is the UTF-8 file content. JSON-encoded as a string;
	// binary entities are not supported in v1 (sync.CAT.2's
	// listed entities are all text — yaml/toml).
	Content string `json:"content"`
	// Signature is the hex-encoded ed25519 signature the original
	// pusher produced over GitSigningInput. The server keeps it
	// alongside the content so peers can re-verify provenance
	// without re-asking the central node for the signer's key.
	Signature string `json:"signature"`
	// Actor is the canonical actor string of the identity that
	// pushed this revision. Set by the server on response from the
	// pusher's authenticated fingerprint.
	Actor string `json:"actor"`
	// UpdatedAt is when the server accepted this revision.
	UpdatedAt time.Time `json:"updated_at"`
}

// GitPushRequest is the body of POST /sync/git (sync.API.4).
//
// The wire shape is the {workspace_id, entity, base_revision,
// content, signature} tuple. `entity` is the path, not the full
// GitEntity — Path/Content/Signature are flat at the top level
// so the request stays small and human-readable.
//
// WorkspaceID scopes the entity to one workspace on the central
// node (a central holds content for many workspaces, and the
// per-workspace .rex/ trees never cross-contaminate). Required:
// pushes with an empty WorkspaceID are rejected with 400.
type GitPushRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	Entity       string `json:"entity"`
	BaseRevision string `json:"base_revision"`
	Content      string `json:"content"`
	Signature    string `json:"signature"`
}

// GitPushResponse is the 200 body of POST /sync/git on success.
type GitPushResponse struct {
	Entity   string `json:"entity"`
	Revision string `json:"revision"`
}

// GitConflictResponse is the 409 body returned when a push's
// BaseRevision does not match the server's current revision for the
// entity. Mirrors ConflictResponse on /sync/events: the client gets
// what it needs to do a three-way merge locally (sync.GIT.1, GIT.2)
// without a follow-up GET round-trip.
type GitConflictResponse struct {
	Entity          string    `json:"entity"`
	ServerRevision  string    `json:"server_revision"`
	ServerContent   string    `json:"server_content"`
	ServerSignature string    `json:"server_signature"`
	ServerActor     string    `json:"server_actor"`
	ServerUpdatedAt time.Time `json:"server_updated_at"`
}

// GitPullResponse is the body of GET /sync/git/<entity-path>.
// 404 if the entity has never been pushed; 200 with this shape
// otherwise.
type GitPullResponse struct {
	Entity GitEntity `json:"entity"`
}

// GitSigningInput is the canonical struct clients sign and the
// server reconstructs when verifying a push. JSON-encoded with
// struct field order; identity-and-trust.* signatures are over
// the hex-string rendering, not raw bytes.
//
// Fields:
//
//	Version       — locks the format; mismatched versions reject
//	WorkspaceID   — bound so a signature for one workspace cannot
//	                replay against another
//	Path          — bound so a signature for one entity cannot
//	                move to another
//	BaseRevision  — bound so a replay against a different parent
//	                fails
//	ContentSHA256 — bound by hash so we sign a fixed-size input
//	                regardless of payload size
type GitSigningInput struct {
	Version       string `json:"version"`
	WorkspaceID   string `json:"workspace_id"`
	Path          string `json:"path"`
	BaseRevision  string `json:"base_revision"`
	ContentSHA256 string `json:"content_sha256"`
}

// GitSigningBytes returns the canonical bytes to sign (or verify)
// for a push of (workspaceID, path, baseRevision, content). The
// hex SHA-256 of content is bound into the input so the signature
// commits to the content without growing with payload size.
func GitSigningBytes(workspaceID, path, baseRevision, content string) ([]byte, error) {
	sum := sha256.Sum256([]byte(content))
	in := GitSigningInput{
		Version:       GitSigningVersion,
		WorkspaceID:   workspaceID,
		Path:          path,
		BaseRevision:  baseRevision,
		ContentSHA256: hex.EncodeToString(sum[:]),
	}
	return json.Marshal(in)
}

// GitContentRevision returns the revision id assigned to a given
// content blob. The id is hex SHA-256 of the bytes — content-
// addressable and stable across nodes, so two clients that pushed
// the same content land on the same revision id.
func GitContentRevision(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Standardized error codes specific to the git surface.
const (
	ErrCodeGitConflict      = "git_conflict"
	ErrCodeGitUnknownEntity = "git_unknown_entity"
	ErrCodeWrongCategory    = "wrong_sync_category"
)
