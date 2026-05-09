package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/rbac"
	"github.com/asabla/rex/internal/core/storage/synccat"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// handleGitPush implements POST /sync/git (sync.API.4).
//
// Wire body: proto.GitPushRequest = {entity, base_revision, content,
// signature}. Path category is enforced (sync.CAT.5) before any other
// work — derived or event-sourced paths are rejected with 400; only
// git_merged paths are accepted on this endpoint.
//
// Concurrency: GitStore.Put owns conflict detection; the handler
// only translates the *GitRevisionConflictError into a 409 body.
func (s *Server) handleGitPush(w http.ResponseWriter, r *http.Request) {
	// Auth gate (sync.SEC.1 / API.5). When the keystore is empty
	// we run in dev mode and skip; production --keys configurations
	// always require a Bearer token.
	var pusher identity.Fingerprint
	if !s.keystore.Empty() {
		fp, err := s.requireToken(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, err.Error())
			return
		}
		pusher = fp
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "read body: "+err.Error())
		return
	}
	var req proto.GitPushRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "decode request: "+err.Error())
		return
	}
	if req.Entity == "" {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "missing entity")
		return
	}

	// CAT.5 enforcement: reject any path that is not git_merged.
	// The synccat registry is the single source of truth for which
	// `.rex/` paths sync via this endpoint.
	if err := requireGitMergedPath(req.Entity); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeWrongCategory, err.Error())
		return
	}

	// RBAC gate (identity-and-trust.RBAC.1). Skipped in dev mode
	// (no keystore → no fingerprint to authorize against).
	if pusher != (identity.Fingerprint{}) {
		orgID := OrgIDFromContext(r.Context())
		if err := s.requirePermission(r.Context(), pusher.String(), orgID, rbac.PermGitPush, "", "", ""); err != nil {
			s.writeRBACDenied(w, r, err)
			return
		}
	}

	// Signature verification (sync.SEC.1). Skipped only when the
	// keystore is empty (dev mode).
	if !s.keystore.Empty() {
		canonical, err := proto.GitSigningBytes(req.Entity, req.BaseRevision, req.Content)
		if err != nil {
			writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "canonical signing input: "+err.Error())
			return
		}
		sig, err := hex.DecodeString(req.Signature)
		if err != nil {
			writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "decode signature: "+err.Error())
			return
		}
		if err := s.keystore.Verify(pusher, canonical, sig); err != nil {
			if errors.Is(err, ErrUnknownIdentity) {
				writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "signed by unregistered identity")
				return
			}
			writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "signature does not verify")
			return
		}
	}

	rec := proto.GitEntity{
		Path:      req.Entity,
		Revision:  proto.GitContentRevision(req.Content),
		Content:   req.Content,
		Signature: req.Signature,
		Actor:     gitActorString(pusher),
		UpdatedAt: time.Now().UTC(),
	}

	if err := s.gitStore.Put(r.Context(), rec, req.BaseRevision); err != nil {
		var conflict *GitRevisionConflictError
		if errors.As(err, &conflict) {
			s.log.Info("git push conflict",
				"op", "git_push",
				"entity", req.Entity,
				"client_base", req.BaseRevision,
				"server_revision", conflict.ServerCurrent.Revision,
			)
			writeJSON(w, http.StatusConflict, proto.GitConflictResponse{
				Entity:          req.Entity,
				ServerRevision:  conflict.ServerCurrent.Revision,
				ServerContent:   conflict.ServerCurrent.Content,
				ServerSignature: conflict.ServerCurrent.Signature,
				ServerActor:     conflict.ServerCurrent.Actor,
				ServerUpdatedAt: conflict.ServerCurrent.UpdatedAt,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}

	s.log.Info("git push accepted",
		"op", "git_push",
		"entity", req.Entity,
		"revision", rec.Revision,
		"base", req.BaseRevision,
	)
	writeJSON(w, http.StatusOK, proto.GitPushResponse{
		Entity:   req.Entity,
		Revision: rec.Revision,
	})
}

// handleGitPull implements GET /sync/git/<entity-path> (sync.API.4).
// Returns proto.GitPullResponse on success or 404 when the entity has
// never been pushed.
func (s *Server) handleGitPull(w http.ResponseWriter, r *http.Request) {
	var puller identity.Fingerprint
	if !s.keystore.Empty() {
		fp, err := s.requireToken(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, err.Error())
			return
		}
		puller = fp
	}

	path := r.PathValue("entity")
	if path == "" {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "missing entity path")
		return
	}

	if err := requireGitMergedPath(path); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeWrongCategory, err.Error())
		return
	}

	// RBAC gate (RBAC.1). The pull surface needs git.pull; the gate
	// short-circuits in dev mode (no keystore → no fingerprint).
	if puller != (identity.Fingerprint{}) {
		// /sync/git/{entity...} doesn't carry an org segment; the
		// pull's org context comes from the puller's single-org
		// membership (or X-Rex-Org for multi-org identities, set
		// by the same middleware that handles /sync/events).
		orgID, _, err := s.resolveOrgForRequest(r, puller.String())
		if err == nil && orgID != "" {
			if err := s.requirePermission(r.Context(), puller.String(), orgID, rbac.PermGitPull, "", "", ""); err != nil {
				s.writeRBACDenied(w, r, err)
				return
			}
		}
	}

	rec, err := s.gitStore.Get(r.Context(), path)
	if err != nil {
		if errors.Is(err, ErrUnknownGitEntity) {
			writeError(w, http.StatusNotFound, proto.ErrCodeGitUnknownEntity, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, proto.GitPullResponse{Entity: rec})
}

// requireGitMergedPath enforces sync.CAT.5: only paths in the
// git_merged category may be pushed to or fetched from /sync/git.
// Returns a descriptive error suitable for inclusion in a 400 body.
func requireGitMergedPath(path string) error {
	if strings.Contains(path, "..") {
		return errors.New("entity path may not contain parent-directory segments")
	}
	cat, ok := synccat.Categorize(path)
	if !ok {
		return errors.New("entity path is not a registered .rex/ entity")
	}
	if cat != synccat.CategoryGitMerged {
		return errors.New("entity path is not a git_merged category; refusing per sync.CAT.5")
	}
	return nil
}

// gitActorString returns the canonical actor string for a fingerprint
// pushing through /sync/git. The local-prefixed form matches the actor
// scheme used elsewhere (sync.ORDER.3 — local actors carry "l-").
// Empty fp (dev mode, no keystore) returns "" so logs and stored
// records do not falsely attribute pushes to a real identity.
func gitActorString(fp identity.Fingerprint) string {
	zero := identity.Fingerprint{}
	if fp == zero {
		return ""
	}
	return identity.Actor{Role: identity.RoleLocal, Fingerprint: fp}.String()
}
