package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/rbac"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// hexDecodeString is a thin wrapper over encoding/hex so the
// verifier surface stays self-contained.
func hexDecodeString(s string) ([]byte, error) {
	return hex.DecodeString(s)
}

// Options configure New.
type Options struct {
	// Keypair is the central node's signing keypair. Its
	// fingerprint surfaces on /sync/state. New generates one if
	// nil; production deployments should always supply persisted
	// material.
	Keypair *identity.Keypair
	// Store is the event store backing /sync/events. New creates
	// an in-memory Store if nil. Implementations: MemoryStore (the
	// default, dev/test), PostgresStore (durable production
	// deployments — central-node.DB.*).
	Store Store
	// GitStore is the git-merged content store backing /sync/git
	// (sync.API.4). New creates an in-memory GitStore if nil.
	// Postgres durability is the follow-up under central-node.DB.*;
	// in the meantime an operator running with --db Postgres still
	// gets a working /sync/git surface but loses git revisions on
	// restart.
	GitStore GitStore
	// Keystore holds the public keys the server trusts for
	// signature verification (sync.SEC.1). When nil or empty,
	// verification is skipped — useful for dev/test. In production
	// the operator passes `--keys <file>` to `rex-central serve`,
	// which loads the keystore from a TOML file and supplies it
	// here.
	Keystore *Keystore
	// Logger is the structured logger every handler shares
	// (HEALTH.3). New defaults to a discard logger when nil so
	// existing tests don't need a logger fixture; cmd/rex-central
	// supplies an os.Stdout JSON handler in production.
	Logger *slog.Logger
	// AuthAudit is the optional appender for token-lifecycle audit
	// events (identity-and-trust.TOKEN.5). Nil disables emission;
	// production deployments wire one up against the central event
	// log.
	AuthAudit AuthAuditAppender
}

// Server bundles the central-node HTTP surface and the state it
// serves. A Server is safe for concurrent use; the underlying Store
// and GitStore each own their own synchronization.
type Server struct {
	store         Store
	gitStore      GitStore
	keypair       identity.Keypair
	actor         identity.Actor
	keystore      *Keystore
	auth          *authState
	auditAppender AuthAuditAppender
	mux           *http.ServeMux
	stateRes      proto.StateResponse
	metrics       *Metrics
	log           *slog.Logger
}

// AuthAuditAppender is the optional interface a Server consumer
// supplies so auth.go can route token-lifecycle events to the
// central audit log. The MemoryStore dev/test path leaves it nil
// (no audit-log file lives in memory); production --db Postgres
// configurations supply an appender that writes to events.log via
// the eventlog package.
type AuthAuditAppender interface {
	Append(ctx context.Context, eventType string, payload any) error
}

// New returns a Server ready to serve via Handler().
func New(opts Options) (*Server, error) {
	store := opts.Store
	if store == nil {
		store = NewStore()
	}
	gitStore := opts.GitStore
	if gitStore == nil {
		gitStore = NewMemoryGitStore()
	}
	var kp identity.Keypair
	if opts.Keypair == nil {
		generated, err := identity.GenerateKeypair("rex-central", nil)
		if err != nil {
			return nil, fmt.Errorf("server: generate keypair: %w", err)
		}
		kp = generated
	} else {
		kp = *opts.Keypair
	}
	if !identity.IsValidHandle(string(kp.Handle)) {
		return nil, fmt.Errorf("server: invalid handle %q", kp.Handle)
	}

	central := identity.Actor{Role: identity.RoleCentral, Fingerprint: kp.Fingerprint()}
	keystore := opts.Keystore
	if keystore == nil {
		keystore = NewKeystore()
	}
	logger := opts.Logger
	if logger == nil {
		logger = NewLogger(LogConfig{}) // discards by default
	}
	s := &Server{
		store:         store,
		gitStore:      gitStore,
		keypair:       kp,
		actor:         central,
		keystore:      keystore,
		auth:          newAuthState(),
		auditAppender: opts.AuthAudit,
		mux:           http.NewServeMux(),
		stateRes: proto.StateResponse{
			Fingerprint:     kp.Fingerprint().String(),
			Actor:           central.String(),
			ProtocolVersion: proto.ProtocolVersion,
		},
		metrics: NewMetrics(),
		log:     logger.With("actor", central.String()),
	}
	s.metrics.SetActiveSessionsSource(s.auth.activeSessions)
	s.mux.HandleFunc("/sync/state", s.handleState)
	s.mux.HandleFunc("/sync/events", s.handleEvents)
	// /sync/git uses Go 1.22+ method-and-wildcard patterns: POST on
	// the bare path is the push surface, GET with a multi-segment
	// {entity...} captures `.rex/`-relative paths like
	// "specs/sync.yaml" via r.PathValue("entity").
	s.mux.HandleFunc("POST /sync/git", s.handleGitPush)
	s.mux.HandleFunc("GET /sync/git/ws/{ws}/{entity...}", s.handleGitPull)
	s.mux.HandleFunc(authChallengePath, s.handleAuthChallenge)
	s.mux.HandleFunc(authVerifyPath, s.handleAuthVerify)
	s.mux.HandleFunc(authRefreshPath, s.handleAuthRefresh)
	s.mux.HandleFunc(authRevokePath, s.handleAuthRevoke)
	s.mux.HandleFunc(authRedeemPath, s.handleAuthRedeem)
	s.mux.HandleFunc(authLogoutPath, s.handleAuthLogout)
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/ready", s.handleReady)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
	s.mux.HandleFunc(adminBootstrapPath, s.handleAdminBootstrap)
	return s, nil
}

// AnnounceBootstrap is called once at startup (after the store
// is open, before the listener accepts traffic) to log + persist
// the admin claim token when bootstrap mode is active.
// tokenPath is the host-filesystem destination (bundled compose:
// /var/lib/rex/bootstrap.token); empty means logs-only.
func (s *Server) AnnounceBootstrap(ctx context.Context, tokenPath string) {
	announceBootstrapToken(ctx, s.store, tokenPath, s.log)
}

// Metrics returns the server's in-process metric registry. Tests
// use it to assert recording behavior without scraping /metrics.
func (s *Server) Metrics() *Metrics { return s.metrics }

// Handler returns the HTTP handler the server registered.
func (s *Server) Handler() http.Handler { return s.mux }

// MountWeb registers h as the fallback handler on the server's mux,
// rooted at "/". API routes (the specific patterns registered in
// New) win against the "/" catchall via http.ServeMux's
// longest-match rule, so this is safe to call after construction:
// h only sees requests whose paths don't match any API route.
//
// Called by cmd/rex-central when --web is enabled. Idempotency is
// not guaranteed — calling twice will panic with
// "multiple registrations for /", matching ServeMux semantics.
// MountWeb is what wires the central web shell
// (internal/central/web) into the same listener the API uses, so
// browsers and `rex remote …` clients share one TLS endpoint
// (web-ui.CENTRAL-LAYOUT.1).
func (s *Server) MountWeb(h http.Handler) {
	s.mux.Handle("/", h)
}

// Actor returns the central node's actor string. Useful when the
// caller (say, a test) wants to verify (HLC, actor) ordering with
// no second guess on what the server's actor looks like.
func (s *Server) Actor() identity.Actor { return s.actor }

// Store returns the underlying event store. Tests use this to
// pre-seed records or assert counts.
func (s *Server) Store() Store { return s.store }

// GitStore returns the underlying git-merged content store. Tests
// use this to pre-seed entities or assert post-conditions.
func (s *Server) GitStore() GitStore { return s.gitStore }

// Keystore returns the server's keystore for tests and admin
// surfaces.
func (s *Server) Keystore() *Keystore { return s.keystore }

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "GET only")
		return
	}
	res := s.stateRes
	headID, err := s.store.Head(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}
	res.HeadID = headID
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Auth gate: when the keystore is configured, every /sync/events
	// request requires a Bearer token issued via the handshake
	// (sync.API.5). When unset, the server runs in dev mode and
	// passes the token check.
	var fingerprint string
	if !s.keystore.Empty() {
		fp, err := s.requireToken(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, err.Error())
			return
		}
		fingerprint = fp.String()
	}

	// Tenant routing: resolve the org for this request and stamp
	// it on the request context (TENANT.1, TENANT.4). The
	// PostgresStore reads OrgIDFromContext on every Append/Since/
	// Head/Len; without it those calls fail rather than silently
	// query unscoped (defense in depth at the application layer
	// until tenant-rls adds Postgres-level RLS).
	if fingerprint != "" {
		orgID, _, err := s.resolveOrgForRequest(r, fingerprint)
		if err != nil {
			var te *tenantStatusError
			if errors.As(err, &te) {
				writeError(w, te.status, te.code, te.msg)
			} else {
				writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
			}
			return
		}
		if orgID != "" {
			r = r.WithContext(WithOrgID(r.Context(), orgID))
		}
	}

	// RBAC gate (identity-and-trust.RBAC.1). Pulls the role for
	// (orgID, fingerprint) from the resolver and asks rbac.Allow.
	// Bypassed when the store has no RoleResolver (MemoryStore /
	// dev mode); in production --db Postgres + --keys both load
	// and the gate fires.
	if fingerprint != "" {
		orgID := OrgIDFromContext(r.Context())
		var action rbac.Permission
		switch r.Method {
		case http.MethodGet:
			action = rbac.PermSyncPull
		case http.MethodPost:
			action = rbac.PermSyncPush
		}
		if action != "" {
			if err := s.requirePermission(r.Context(), fingerprint, orgID, action, "", "", ""); err != nil {
				s.writeRBACDenied(w, r, err)
				return
			}
		}
	}

	switch r.Method {
	case http.MethodGet:
		s.handleEventsGet(w, r)
	case http.MethodPost:
		s.handleEventsPost(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "GET or POST only")
	}
}

// handleEventsPost implements sync.API.2 — accept a contiguous
// batch and acknowledge with the new HEAD. Idempotent: events the
// server already has are skipped (sync.API.6).
//
// Logs one line per request at INFO with the structured fields
// `op=push events=N accepted=A duplicates=D head=<id>` (or
// `op=push conflict=true server_head=...` on the divergence
// path). Never logs payload bytes or signature hex (HEALTH.3
// "no logs contain secrets").
func (s *Server) handleEventsPost(w http.ResponseWriter, r *http.Request) {
	s.metrics.RecordPushRequest()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "read body: "+err.Error())
		return
	}
	var req proto.PushRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "decode request: "+err.Error())
		return
	}

	// Authentication first (sync.SEC.1): every event in the batch
	// must be signed by a registered identity. Auth precedes any
	// business-logic check so a divergence-conflict response never
	// leaks state to an unauthorized client. When the keystore is
	// empty we skip — that's the dev/test path with verification
	// off.
	if !s.keystore.Empty() {
		for _, rec := range req.Events {
			if err := s.verifyRecord(rec); err != nil {
				if errors.Is(err, ErrUnknownIdentity) {
					writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized,
						"signed by unregistered identity")
					return
				}
				if errors.Is(err, ErrInvalidSignature) {
					writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized,
						"signature does not verify")
					return
				}
				writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, err.Error())
				return
			}
		}
	}

	// Workspace binding (ORG.6-note "first-push-wins"). When
	// the request has an org context (PostgresStore + tenant
	// routing path), reject if any pushed record references a
	// workspace already bound to a different org.
	if orgID := OrgIDFromContext(r.Context()); orgID != "" {
		recs := make([]recordWithWorkspace, len(req.Events))
		for i, e := range req.Events {
			recs[i] = recordWithWorkspace{WorkspaceID: e.WorkspaceID}
		}
		if err := s.enforceWorkspaceBinding(r.Context(), orgID, recs); err != nil {
			s.log.Warn("push: workspace binding mismatch",
				"op", "push",
				"err", err.Error(),
			)
			writeError(w, http.StatusForbidden, "workspace_org_mismatch", err.Error())
			return
		}
	}

	// Conflict detection: the client's `since` must match our
	// current HEAD. If it does not, return 409 with the diverging
	// tail so the client can rebase.
	currentHead, err := s.store.Head(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}
	if req.Since != currentHead {
		s.metrics.RecordPushConflict()
		s.log.Info("push conflict",
			"op", "push",
			"events", len(req.Events),
			"client_since", req.Since,
			"server_head", currentHead,
		)
		tail, err := s.store.Since(r.Context(), req.Since)
		if err != nil {
			// The client's cursor is unknown to us; surface as a
			// conflict with empty tail so the client knows to
			// resync from scratch.
			writeJSON(w, http.StatusConflict, proto.ConflictResponse{
				ServerHead:    currentHead,
				DivergingTail: nil,
			})
			return
		}
		writeJSON(w, http.StatusConflict, proto.ConflictResponse{
			ServerHead:    currentHead,
			DivergingTail: tail,
		})
		return
	}

	// Validation pre-pass: a malformed record poisons the whole
	// batch (atomic insert below), so reject 400 before we ever
	// touch the database.
	for _, rec := range req.Events {
		if err := validatePushRecord(rec); err != nil {
			writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, err.Error())
			return
		}
	}

	// One transaction collapses N×(BEGIN + INSERT + COMMIT) into
	// one workspace upsert + one multi-row event INSERT + one
	// commit, independent of batch size — the dominant cost of a
	// large push at v1's central scale.
	addedIDs, err := s.store.AppendBatch(r.Context(), req.Events)
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}
	addedSet := make(map[string]struct{}, len(addedIDs))
	for _, id := range addedIDs {
		addedSet[id] = struct{}{}
	}
	res := proto.PushResponse{}
	for _, rec := range req.Events {
		_, was := addedSet[rec.ID]
		s.recordEvent(rec.Type, was)
		if was {
			res.Accepted++
		} else {
			res.Duplicates++
		}
	}
	headID, err := s.store.Head(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}
	res.HeadID = headID
	s.log.Info("push accepted",
		"op", "push",
		"events", len(req.Events),
		"accepted", res.Accepted,
		"duplicates", res.Duplicates,
		"head", headID,
	)
	writeJSON(w, http.StatusOK, res)
}

// verifyRecord checks rec's signature against the server's keystore.
// Returns nil on success, ErrUnknownIdentity when the actor's
// fingerprint is not registered, ErrInvalidSignature when the
// signature is missing or does not verify, or another error for
// malformed inputs (which surface as 400, not 401, because the
// client made a structural mistake unrelated to authentication).
func (s *Server) verifyRecord(rec eventlog.Record) error {
	if rec.Actor == "" {
		return fmt.Errorf("record has no actor")
	}
	actor, err := identity.ParseActor(rec.Actor)
	if err != nil {
		return fmt.Errorf("parse actor: %w", err)
	}
	if rec.Signature == "" {
		// Missing signature is structurally indistinguishable
		// from an invalid one — the client failed to authenticate
		// the record. Surface as ErrInvalidSignature so the
		// handler returns 401.
		return fmt.Errorf("%w: record %q has no signature", ErrInvalidSignature, rec.ID)
	}
	canonical, err := eventlog.SigningBytes(rec)
	if err != nil {
		return err
	}
	sig, err := hexDecodeString(rec.Signature)
	if err != nil {
		return fmt.Errorf("%w: decode signature: %w", ErrInvalidSignature, err)
	}
	return s.keystore.Verify(actor.Fingerprint, canonical, sig)
}

// handleEventsGet implements sync.API.3 — stream events past a
// cursor as Server-Sent Events.
func (s *Server) handleEventsGet(w http.ResponseWriter, r *http.Request) {
	cursor := r.URL.Query().Get("since")
	tail, err := s.store.Since(r.Context(), cursor)
	if err != nil {
		if errors.Is(err, ErrUnknownCursor) {
			writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "unknown cursor")
			return
		}
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	for _, rec := range tail {
		body, err := json.Marshal(proto.SSEFrame{Record: rec})
		if err != nil {
			// Mid-stream: log via SSE comment and bail; cannot
			// write a non-2xx now.
			fmt.Fprintf(w, ": encode error %s\n\n", err.Error())
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", body)
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Mark end-of-stream with an SSE comment so clients can detect
	// it; standard SSE has no built-in EOF event, but a `: end`
	// comment is conventional.
	fmt.Fprintln(w, ": end")
	if flusher != nil {
		flusher.Flush()
	}
}

// validatePushRecord rejects records the server cannot store. For
// the in-process skeleton we only check the trivially-required
// fields; real signature verification (sync.SEC.1) lands once
// identity-and-trust has signed events.
func validatePushRecord(rec eventlog.Record) error {
	if rec.ID == "" {
		return errors.New("event record missing id")
	}
	if rec.Type == "" {
		return errors.New("event record missing type")
	}
	if rec.WorkspaceID == "" {
		return errors.New("event record missing workspace_id")
	}
	if rec.Actor == "" {
		return nil // tolerated until identity.Actor flows through writes
	}
	if !strings.HasPrefix(rec.Actor, "c-") && !strings.HasPrefix(rec.Actor, "l-") {
		return fmt.Errorf("event record actor %q lacks role prefix", rec.Actor)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(proto.MarshalErrorResponse(code, message))
}
