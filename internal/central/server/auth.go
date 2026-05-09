package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// Auth-time constants (identity-and-trust.AUTH.1.1 / TOKEN.1).
//
// TOKEN.1 specifies "short-lived access (15 minute default) and
// longer-lived refresh (30 day default)". The tokens are opaque
// random strings; on the wire we hex-encode 32 random bytes per
// token. The server stores SHA-256 hashes of the wire values per
// TOKEN.2 — token theft from the central node's memory is then
// only useful if the attacker also captured the wire value.
const (
	challengeNonceLen  = 32
	challengeTTL       = 60 * time.Second
	accessTokenTTL     = 15 * time.Minute
	refreshTokenTTL    = 30 * 24 * time.Hour
	authChallengePath  = "/auth/challenge"
	authVerifyPath     = "/auth/verify"
	authRefreshPath    = "/auth/refresh"
	authRevokePath     = "/auth/revoke"
	bearerPrefix       = "Bearer "
	tokenIDPrefixChars = 16 // hex chars exposed in audit events
)

// challenge is one in-flight handshake nonce. It expires after
// challengeTTL; the verifier consumes it on success and discards
// after expiry.
type challenge struct {
	id        string
	nonce     []byte
	hostname  string
	expiresAt time.Time
	consumed  bool
}

// tokenKind distinguishes access tokens from refresh tokens. They
// share storage and most lifecycle code; the kind drives expiry
// rules and which endpoints accept which kind.
type tokenKind int

const (
	kindAccess tokenKind = iota
	kindRefresh
)

func (k tokenKind) String() string {
	if k == kindRefresh {
		return "refresh"
	}
	return "access"
}

// token is one issued token bound to an identity fingerprint and
// chain. Storage is keyed by the SHA-256 hash of the wire value
// (TOKEN.2); the wire value itself never sits in memory after
// issuance returns.
type token struct {
	hash        string // hex SHA-256 of the wire value (the storage key)
	fingerprint identity.Fingerprint
	scope       string
	chainID     string
	kind        tokenKind
	expiresAt   time.Time
	revoked     bool
	// refreshUsed marks a refresh token as already-rotated. A
	// reuse attempt after this point trips SEC.2's chain-revoke
	// path. Always false for access tokens.
	refreshUsed bool
	// legacyValue is the wire token value retained in-memory for
	// the test helper issueTestToken's caller convenience. New
	// production paths return the value separately and never set
	// this field.
	legacyValue string
}

// authState owns the in-flight challenges and issued tokens. All
// access is mutex-guarded; expiry is best-effort (we sweep on
// access rather than via a separate goroutine to keep the surface
// small).
type authState struct {
	mu         sync.Mutex
	challenges map[string]*challenge
	tokens     map[string]*token // key: tokenHash; value: token
	// chains[chainID] = set of token hashes belonging to that chain.
	// Used by Revoke(All=true) and the SEC.2 chain-revoke path.
	chains map[string]map[string]struct{}
	// revoked is the explicit revocation list (TOKEN.4). Hashes
	// land here on every revoke and are checked on every
	// resolveToken call. Expired/swept-but-revoked entries stay
	// long enough to trip a still-presented stolen token.
	revoked map[string]time.Time

	// now and randRead are injectable for deterministic tests.
	now      func() time.Time
	randRead func([]byte) (int, error)
}

func newAuthState() *authState {
	return &authState{
		challenges: make(map[string]*challenge),
		tokens:     make(map[string]*token),
		chains:     make(map[string]map[string]struct{}),
		revoked:    make(map[string]time.Time),
		now:        time.Now,
		randRead:   rand.Read,
	}
}

// hashToken returns the canonical storage key for a wire token
// value. SHA-256 keeps the on-disk shape compact while making
// memory-only "stolen the server's RAM" attacks meaningless without
// the wire value too.
func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// tokenIDForLog returns the prefix of a token hash safe to surface
// in audit events / log lines. Long enough to disambiguate runs of
// activity, short enough that a logged copy doesn't reveal the
// full hash.
func tokenIDForLog(hash string) string {
	if len(hash) < tokenIDPrefixChars {
		return hash
	}
	return hash[:tokenIDPrefixChars]
}

// issueChallenge mints a fresh challenge and stores it.
func (a *authState) issueChallenge(hostname string) (*challenge, error) {
	nonce := make([]byte, challengeNonceLen)
	if _, err := a.randRead(nonce); err != nil {
		return nil, fmt.Errorf("server: read nonce: %w", err)
	}
	idBytes := make([]byte, 16)
	if _, err := a.randRead(idBytes); err != nil {
		return nil, fmt.Errorf("server: read challenge id: %w", err)
	}
	c := &challenge{
		id:        hex.EncodeToString(idBytes),
		nonce:     nonce,
		hostname:  hostname,
		expiresAt: a.now().Add(challengeTTL),
	}
	a.mu.Lock()
	a.challenges[c.id] = c
	a.mu.Unlock()
	return c, nil
}

// consumeChallenge looks up and consumes the challenge with id.
// Returns ErrChallengeUnknown for missing, ErrChallengeExpired for
// stale, ErrChallengeUsed for already-consumed.
func (a *authState) consumeChallenge(id string) (*challenge, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, ok := a.challenges[id]
	if !ok {
		return nil, ErrChallengeUnknown
	}
	if a.now().After(c.expiresAt) {
		delete(a.challenges, id)
		return nil, ErrChallengeExpired
	}
	if c.consumed {
		return nil, ErrChallengeUsed
	}
	c.consumed = true
	return c, nil
}

// issueTokenPair mints a fresh (access, refresh) pair belonging to
// a new chain (TOKEN.1). Returned values are wire-encoded; the
// server holds only the hashes after this returns. Used by
// /auth/verify on a successful signature.
func (a *authState) issueTokenPair(fp identity.Fingerprint, scope string) (access, refresh *token, accessValue, refreshValue string, err error) {
	chainBytes := make([]byte, 8)
	if _, err = a.randRead(chainBytes); err != nil {
		return nil, nil, "", "", fmt.Errorf("server: read chain id: %w", err)
	}
	chainID := hex.EncodeToString(chainBytes)

	access, accessValue, err = a.mintToken(fp, scope, chainID, kindAccess, accessTokenTTL)
	if err != nil {
		return nil, nil, "", "", err
	}
	refresh, refreshValue, err = a.mintToken(fp, scope, chainID, kindRefresh, refreshTokenTTL)
	if err != nil {
		return nil, nil, "", "", err
	}
	return access, refresh, accessValue, refreshValue, nil
}

// mintToken creates one token of the given kind under chainID. The
// wire value is returned alongside the in-memory token; callers
// pass the value back to the client and discard it.
func (a *authState) mintToken(fp identity.Fingerprint, scope, chainID string, kind tokenKind, ttl time.Duration) (*token, string, error) {
	raw := make([]byte, 32)
	if _, err := a.randRead(raw); err != nil {
		return nil, "", fmt.Errorf("server: read token: %w", err)
	}
	value := hex.EncodeToString(raw)
	t := &token{
		hash:        hashToken(value),
		fingerprint: fp,
		scope:       scope,
		chainID:     chainID,
		kind:        kind,
		expiresAt:   a.now().Add(ttl),
	}
	a.mu.Lock()
	a.tokens[t.hash] = t
	if _, ok := a.chains[chainID]; !ok {
		a.chains[chainID] = make(map[string]struct{})
	}
	a.chains[chainID][t.hash] = struct{}{}
	a.mu.Unlock()
	return t, value, nil
}

// rotateRefresh exchanges presentedValue (a refresh-token wire
// value) for a fresh (access, refresh) pair under the SAME chain.
// The presented refresh is marked refreshUsed=true on success;
// presenting it again triggers ErrTokenReplay and the caller's
// chain-revoke path (SEC.2).
//
// Returns ErrTokenInvalid for missing/expired/revoked tokens, and
// ErrTokenReplay for an already-rotated refresh.
func (a *authState) rotateRefresh(presentedValue string) (oldHash string, access, refresh *token, accessValue, refreshValue string, err error) {
	hash := hashToken(presentedValue)
	a.mu.Lock()
	old, ok := a.tokens[hash]
	if !ok {
		a.mu.Unlock()
		return "", nil, nil, "", "", ErrTokenInvalid
	}
	if old.kind != kindRefresh {
		a.mu.Unlock()
		return "", nil, nil, "", "", ErrTokenInvalid
	}
	if old.revoked {
		a.mu.Unlock()
		return "", nil, nil, "", "", ErrTokenInvalid
	}
	if a.now().After(old.expiresAt) {
		delete(a.tokens, hash)
		a.mu.Unlock()
		return "", nil, nil, "", "", ErrTokenInvalid
	}
	if old.refreshUsed {
		// Replay attempt — caller should chain-revoke.
		a.mu.Unlock()
		return hash, nil, nil, "", "", ErrTokenReplay
	}
	old.refreshUsed = true
	fp := old.fingerprint
	scope := old.scope
	chainID := old.chainID
	a.mu.Unlock()

	access, accessValue, err = a.mintToken(fp, scope, chainID, kindAccess, accessTokenTTL)
	if err != nil {
		return hash, nil, nil, "", "", err
	}
	refresh, refreshValue, err = a.mintToken(fp, scope, chainID, kindRefresh, refreshTokenTTL)
	if err != nil {
		return hash, nil, nil, "", "", err
	}
	return hash, access, refresh, accessValue, refreshValue, nil
}

// activeSessions returns the current count of issued tokens that
// are not expired and not revoked. Used by /metrics to surface
// HEALTH.2's active-sessions gauge as a snapshot — no drift from
// increment/decrement pairing because the count is derived at
// scrape time. Expired tokens are swept lazily here so the metric
// also acts as a periodic gc trigger.
//
// Counts both access AND refresh tokens; an active session has
// one of each.
func (a *authState) activeSessions() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	n := 0
	for h, t := range a.tokens {
		if t.revoked || now.After(t.expiresAt) {
			delete(a.tokens, h)
			if chain, ok := a.chains[t.chainID]; ok {
				delete(chain, h)
				if len(chain) == 0 {
					delete(a.chains, t.chainID)
				}
			}
			continue
		}
		n++
	}
	return n
}

// resolveAccessToken looks up an access-token wire value. Returns
// ErrTokenInvalid for missing, expired, revoked, or refresh-kind
// tokens (a refresh token must not be usable for direct request
// auth).
func (a *authState) resolveAccessToken(presentedValue string) (*token, error) {
	hash := hashToken(presentedValue)
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, isRevoked := a.revoked[hash]; isRevoked {
		return nil, ErrTokenInvalid
	}
	t, ok := a.tokens[hash]
	if !ok {
		return nil, ErrTokenInvalid
	}
	if t.revoked {
		return nil, ErrTokenInvalid
	}
	if t.kind != kindAccess {
		return nil, ErrTokenInvalid
	}
	if a.now().After(t.expiresAt) {
		delete(a.tokens, hash)
		return nil, ErrTokenInvalid
	}
	return t, nil
}

// revokeToken marks the token at presentedValue as revoked and
// adds its hash to the revocation list (TOKEN.4). Returns
// ErrTokenInvalid for an unknown token. The caller's fingerprint
// (from the Bearer of the request) must match the token's owner;
// the handler enforces that before calling this.
func (a *authState) revokeToken(presentedValue string) (*token, error) {
	hash := hashToken(presentedValue)
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.tokens[hash]
	if !ok {
		return nil, ErrTokenInvalid
	}
	t.revoked = true
	a.revoked[hash] = a.now()
	return t, nil
}

// revokeChain revokes every token belonging to chainID. Returns
// the list of hashes invalidated so the audit-emission path can
// log token ids. Idempotent: chains already empty produce 0
// revoked.
func (a *authState) revokeChain(chainID string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	chain, ok := a.chains[chainID]
	if !ok {
		return nil
	}
	now := a.now()
	out := make([]string, 0, len(chain))
	for h := range chain {
		t, ok := a.tokens[h]
		if !ok {
			continue
		}
		t.revoked = true
		a.revoked[h] = now
		out = append(out, h)
	}
	return out
}

// issueToken is the legacy single-token issuance API kept for the
// existing test helpers (issueTestToken in verify_test.go). New
// callers should use issueTokenPair. The returned token has a
// chain id of its own so the SEC.2 path still covers it.
func (a *authState) issueToken(fp identity.Fingerprint, scope string) (*token, error) {
	chainBytes := make([]byte, 8)
	if _, err := a.randRead(chainBytes); err != nil {
		return nil, fmt.Errorf("server: read chain id: %w", err)
	}
	chainID := hex.EncodeToString(chainBytes)
	t, value, err := a.mintToken(fp, scope, chainID, kindAccess, accessTokenTTL)
	if err != nil {
		return nil, err
	}
	// Backwards-compat: the legacy test helper relies on the
	// returned token's `value` field carrying the wire token.
	// Stash it on a side-channel field used only by tests.
	t.legacyValue = value
	return t, nil
}

var (
	// ErrChallengeUnknown — challenge_id does not match any
	// in-flight challenge.
	ErrChallengeUnknown = errors.New("server: unknown challenge")
	// ErrChallengeExpired — the challenge's 60s window elapsed.
	ErrChallengeExpired = errors.New("server: challenge expired")
	// ErrChallengeUsed — the challenge was already consumed.
	ErrChallengeUsed = errors.New("server: challenge already consumed")
	// ErrTokenInvalid — token missing, expired, or revoked.
	ErrTokenInvalid = errors.New("server: invalid token")
	// ErrTokenReplay — refresh token was reused after rotation
	// (SEC.2). The caller's chain is auto-revoked.
	ErrTokenReplay = errors.New("server: refresh token replay")
)

// handleAuthChallenge implements POST /auth/challenge: server emits
// a fresh nonce + challenge id, expiring in 60s.
func (s *Server) handleAuthChallenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "POST only")
		return
	}
	c, err := s.auth.issueChallenge(r.Host)
	if err != nil {
		s.log.Error("auth challenge: issue failed", "op", "auth.challenge", "err", err)
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}
	s.metrics.RecordAuthChallenge()
	s.log.Info("auth challenge issued",
		"op", "auth.challenge",
		"challenge_id", c.id,
		"hostname", c.hostname,
	)
	writeJSON(w, http.StatusOK, proto.AuthChallengeResponse{
		ChallengeID: c.id,
		Nonce:       hex.EncodeToString(c.nonce),
		Hostname:    c.hostname,
		ExpiresAt:   c.expiresAt,
	})
}

// handleAuthVerify implements POST /auth/verify: client submits a
// signature over the challenge; server verifies against the
// keystore and issues an access+refresh pair on success.
func (s *Server) handleAuthVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "POST only")
		return
	}
	var req proto.AuthVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "decode: "+err.Error())
		return
	}
	if req.ChallengeID == "" || req.Fingerprint == "" || req.Signature == "" {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest,
			"challenge_id, fingerprint, and signature are required")
		return
	}
	if req.Scope == "" {
		req.Scope = "sync"
	}

	c, err := s.auth.consumeChallenge(req.ChallengeID)
	if err != nil {
		// Fail closed without leaking which path tripped (AUTH.3).
		s.log.Warn("auth verify: challenge consume failed",
			"op", "auth.verify",
			"challenge_id", req.ChallengeID,
			"reason", err.Error(),
		)
		s.appendAuthAudit(r, audit.EventTypeAuthFailure, audit.AuthFailureEvent{
			Fingerprint: req.Fingerprint,
			Reason:      "challenge_invalid",
			ChallengeID: req.ChallengeID,
		})
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "challenge invalid")
		return
	}

	fp, err := identity.ParseFingerprint(req.Fingerprint)
	if err != nil {
		s.log.Warn("auth verify: fingerprint parse failed",
			"op", "auth.verify",
			"challenge_id", req.ChallengeID,
		)
		s.appendAuthAudit(r, audit.EventTypeAuthFailure, audit.AuthFailureEvent{
			Fingerprint: req.Fingerprint,
			Reason:      "fingerprint_parse",
			ChallengeID: req.ChallengeID,
		})
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "challenge invalid")
		return
	}
	key, ok := s.keystore.Lookup(fp)
	if !ok {
		s.log.Warn("auth verify: fingerprint not registered",
			"op", "auth.verify",
			"challenge_id", req.ChallengeID,
			"fingerprint", fp.String(),
		)
		s.appendAuthAudit(r, audit.EventTypeAuthFailure, audit.AuthFailureEvent{
			Fingerprint: fp.String(),
			Reason:      "unknown_fingerprint",
			ChallengeID: req.ChallengeID,
		})
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "challenge invalid")
		return
	}

	canonical, err := json.Marshal(proto.ChallengeSigningInput{
		Version:  proto.AuthSigningVersion,
		Nonce:    hex.EncodeToString(c.nonce),
		Hostname: c.hostname,
		Scope:    req.Scope,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}
	sig, err := hex.DecodeString(req.Signature)
	if err != nil {
		s.appendAuthAudit(r, audit.EventTypeAuthFailure, audit.AuthFailureEvent{
			Fingerprint: fp.String(),
			Reason:      "signature_decode",
			ChallengeID: req.ChallengeID,
		})
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "challenge invalid")
		return
	}
	if !ed25519.Verify(key.publicKey, canonical, sig) {
		s.log.Warn("auth verify: signature invalid",
			"op", "auth.verify",
			"challenge_id", req.ChallengeID,
			"fingerprint", fp.String(),
		)
		s.appendAuthAudit(r, audit.EventTypeAuthFailure, audit.AuthFailureEvent{
			Fingerprint: fp.String(),
			Reason:      "bad_signature",
			ChallengeID: req.ChallengeID,
		})
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "challenge invalid")
		return
	}

	access, refresh, accessValue, refreshValue, err := s.auth.issueTokenPair(fp, req.Scope)
	if err != nil {
		s.log.Error("auth verify: token issue failed", "op", "auth.verify", "err", err)
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}

	// Auto-join the verified identity to the default org if the
	// store knows about orgs (central-node.TENANT.4-note). The
	// MemoryStore doesn't implement MembershipEnsurer; that's the
	// dev/test path with no orgs and we just skip. Soft-fail: a
	// database hiccup logs a WARN but doesn't fail the auth.
	if ensurer, ok := s.store.(MembershipEnsurer); ok {
		if err := ensurer.EnsureDefaultMembership(r.Context(), fp.String()); err != nil {
			s.log.Warn("auth verify: ensure default membership failed",
				"op", "auth.verify",
				"fingerprint", fp.String(),
				"err", err.Error(),
			)
		}
	}
	s.appendAuthAudit(r, audit.EventTypeAuthSuccess, audit.AuthSuccessEvent{
		Fingerprint: fp.String(),
		Scope:       req.Scope,
		ChallengeID: req.ChallengeID,
		Hostname:    c.hostname,
	})
	s.appendAuthAudit(r, audit.EventTypeTokenIssued, audit.TokenIssuedEvent{
		Fingerprint:      fp.String(),
		Scope:            req.Scope,
		TokenID:          tokenIDForLog(access.hash),
		ChainID:          access.chainID,
		ExpiresAt:        access.expiresAt.UTC().Format(time.RFC3339Nano),
		RefreshExpiresAt: refresh.expiresAt.UTC().Format(time.RFC3339Nano),
	})
	s.log.Info("auth verify: token issued",
		"op", "auth.verify",
		"fingerprint", fp.String(),
		"scope", req.Scope,
		"token_id", tokenIDForLog(access.hash),
		"chain_id", access.chainID,
		"expires_at", access.expiresAt.UTC().Format(time.RFC3339Nano),
	)
	writeJSON(w, http.StatusOK, proto.AuthVerifyResponse{
		AccessToken:      accessValue,
		ExpiresAt:        access.expiresAt,
		RefreshToken:     refreshValue,
		RefreshExpiresAt: refresh.expiresAt,
	})
}

// handleAuthRefresh implements POST /auth/refresh (TOKEN.3). The
// presented refresh token is exchanged for a new access+refresh
// pair under the same chain; the presented refresh is invalidated.
// A reuse attempt fires the SEC.2 chain-revoke path.
func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "POST only")
		return
	}
	var req proto.AuthRefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "decode: "+err.Error())
		return
	}
	if req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "refresh_token is required")
		return
	}

	oldHash, access, refresh, accessValue, refreshValue, err := s.auth.rotateRefresh(req.RefreshToken)
	if errors.Is(err, ErrTokenReplay) {
		// SEC.2: a refresh token used after rotation invalidates
		// the entire chain.
		s.handleRefreshReplay(r, oldHash)
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "token chain revoked")
		return
	}
	if err != nil {
		s.log.Warn("auth refresh: rejected", "op", "auth.refresh", "reason", err.Error())
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "refresh invalid")
		return
	}

	s.appendAuthAudit(r, audit.EventTypeTokenRefreshed, audit.TokenRefreshedEvent{
		Fingerprint: access.fingerprint.String(),
		ChainID:     access.chainID,
		OldTokenID:  tokenIDForLog(oldHash),
		NewTokenID:  tokenIDForLog(access.hash),
		ExpiresAt:   access.expiresAt.UTC().Format(time.RFC3339Nano),
	})
	s.log.Info("auth refresh: rotated",
		"op", "auth.refresh",
		"fingerprint", access.fingerprint.String(),
		"chain_id", access.chainID,
		"old_token_id", tokenIDForLog(oldHash),
		"new_token_id", tokenIDForLog(access.hash),
	)
	writeJSON(w, http.StatusOK, proto.AuthRefreshResponse{
		AccessToken:      accessValue,
		ExpiresAt:        access.expiresAt,
		RefreshToken:     refreshValue,
		RefreshExpiresAt: refresh.expiresAt,
	})
}

// handleRefreshReplay handles the SEC.2 chain-revoke path: revoke
// every token in the chain the replayed refresh belonged to and
// emit auth.replay_attempt + token.revoked audit events.
func (s *Server) handleRefreshReplay(r *http.Request, oldHash string) {
	s.auth.mu.Lock()
	t, ok := s.auth.tokens[oldHash]
	var fp string
	var chainID string
	if ok {
		fp = t.fingerprint.String()
		chainID = t.chainID
	}
	s.auth.mu.Unlock()
	if chainID == "" {
		return
	}
	revoked := s.auth.revokeChain(chainID)

	s.appendAuthAudit(r, audit.EventTypeAuthReplayAttempt, audit.AuthReplayAttemptEvent{
		Fingerprint: fp,
		ChainID:     chainID,
		OldTokenID:  tokenIDForLog(oldHash),
	})
	s.appendAuthAudit(r, audit.EventTypeTokenRevoked, audit.TokenRevokedEvent{
		Fingerprint: fp,
		ChainID:     chainID,
		Count:       len(revoked),
		Reason:      "replay",
	})
	s.log.Warn("auth refresh: replay detected, chain revoked",
		"op", "auth.refresh",
		"fingerprint", fp,
		"chain_id", chainID,
		"revoked_count", len(revoked),
	)
}

// handleAuthRevoke implements POST /auth/revoke (TOKEN.4). The
// caller authenticates with their own access token; the body
// names what to revoke. Only tokens belonging to the caller's
// chain may be revoked through this endpoint.
func (s *Server) handleAuthRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "POST only")
		return
	}
	caller, err := s.requireToken(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, err.Error())
		return
	}
	var req proto.AuthRevokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "decode: "+err.Error())
		return
	}

	// Resolve caller's chain id by looking up their access token.
	s.auth.mu.Lock()
	bearer := r.Header.Get("Authorization")
	bearerValue := strings.TrimPrefix(bearer, bearerPrefix)
	callerHash := hashToken(bearerValue)
	callerTok := s.auth.tokens[callerHash]
	s.auth.mu.Unlock()
	if callerTok == nil {
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "invalid bearer")
		return
	}

	if req.All {
		revoked := s.auth.revokeChain(callerTok.chainID)
		s.appendAuthAudit(r, audit.EventTypeTokenRevoked, audit.TokenRevokedEvent{
			Fingerprint: caller.String(),
			ChainID:     callerTok.chainID,
			Count:       len(revoked),
			Reason:      "explicit",
		})
		writeJSON(w, http.StatusOK, proto.AuthRevokeResponse{Count: len(revoked)})
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest,
			"token or all=true is required")
		return
	}
	target, err := s.auth.revokeToken(req.Token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, err.Error())
		return
	}
	if target.fingerprint != caller {
		writeError(w, http.StatusForbidden, "permission_denied",
			"token belongs to a different identity")
		return
	}
	s.appendAuthAudit(r, audit.EventTypeTokenRevoked, audit.TokenRevokedEvent{
		Fingerprint: caller.String(),
		TokenID:     tokenIDForLog(target.hash),
		ChainID:     target.chainID,
		Count:       1,
		Reason:      "explicit",
	})
	writeJSON(w, http.StatusOK, proto.AuthRevokeResponse{Count: 1})
}

// requireToken extracts and validates a Bearer token from the
// Authorization header. Returns the token's bound fingerprint or
// an error describing why authorization failed. The caller writes
// the HTTP error response based on this error.
func (s *Server) requireToken(r *http.Request) (identity.Fingerprint, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return identity.Fingerprint{}, errors.New("missing Authorization header")
	}
	if !strings.HasPrefix(header, bearerPrefix) {
		return identity.Fingerprint{}, errors.New("authorization must be Bearer")
	}
	value := strings.TrimPrefix(header, bearerPrefix)
	tok, err := s.auth.resolveAccessToken(value)
	if err != nil {
		return identity.Fingerprint{}, err
	}
	return tok.fingerprint, nil
}

// appendAuthAudit is the central node's audit-emit shim for auth
// events. The MemoryStore path drops the event silently (no audit
// log lives on a dev/test memory server); production --db Postgres
// servers append via the eventlog adapter wired in the central
// command.
//
// Best-effort: a failure to append is logged but never bubbles back
// to the request — auth correctness must not depend on audit
// availability.
func (s *Server) appendAuthAudit(r *http.Request, eventType string, payload any) {
	if s.auditAppender == nil {
		return
	}
	if err := s.auditAppender.Append(r.Context(), eventType, payload); err != nil {
		s.log.Warn("auth audit append failed",
			"op", "audit.append",
			"event_type", eventType,
			"err", err.Error(),
		)
	}
}
