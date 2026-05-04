package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// Auth-time constants (identity-and-trust.AUTH.1.1, TOKEN.1). The
// access-token TTL is short by design; refresh-token rotation lands
// later (TOKEN.3).
const (
	challengeNonceLen   = 32
	challengeTTL        = 60 * time.Second
	accessTokenTTL      = 15 * time.Minute
	authChallengePath   = "/auth/challenge"
	authVerifyPath      = "/auth/verify"
	bearerPrefix        = "Bearer "
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

// token is one issued access token bound to an identity fingerprint.
type token struct {
	value       string
	fingerprint identity.Fingerprint
	scope       string
	expiresAt   time.Time
	revoked     bool
}

// authState owns the in-flight challenges and issued tokens. All
// access is mutex-guarded; expiry is best-effort (we sweep on
// access rather than via a separate goroutine to keep the surface
// small).
type authState struct {
	mu         sync.Mutex
	challenges map[string]*challenge
	tokens     map[string]*token

	// now and randRead are injectable for deterministic tests.
	now      func() time.Time
	randRead func([]byte) (int, error)
}

func newAuthState() *authState {
	return &authState{
		challenges: make(map[string]*challenge),
		tokens:     make(map[string]*token),
		now:        time.Now,
		randRead:   rand.Read,
	}
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

// issueToken stores a new opaque access token bound to fp.
func (a *authState) issueToken(fp identity.Fingerprint, scope string) (*token, error) {
	raw := make([]byte, 32)
	if _, err := a.randRead(raw); err != nil {
		return nil, fmt.Errorf("server: read token: %w", err)
	}
	t := &token{
		value:       hex.EncodeToString(raw),
		fingerprint: fp,
		scope:       scope,
		expiresAt:   a.now().Add(accessTokenTTL),
	}
	a.mu.Lock()
	a.tokens[t.value] = t
	a.mu.Unlock()
	return t, nil
}

// activeSessions returns the current count of issued tokens
// that are not expired and not revoked. Used by /metrics to
// surface HEALTH.2's active-sessions gauge as a snapshot — no
// drift from increment/decrement pairing because the count is
// derived at scrape time. Expired tokens are swept lazily here
// so the metric also acts as a periodic gc trigger.
func (a *authState) activeSessions() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	n := 0
	for k, t := range a.tokens {
		if t.revoked || now.After(t.expiresAt) {
			delete(a.tokens, k)
			continue
		}
		n++
	}
	return n
}

// resolveToken looks up a token string. Returns ErrTokenInvalid for
// missing, expired, or revoked tokens.
func (a *authState) resolveToken(s string) (*token, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.tokens[s]
	if !ok {
		return nil, ErrTokenInvalid
	}
	if t.revoked {
		return nil, ErrTokenInvalid
	}
	if a.now().After(t.expiresAt) {
		delete(a.tokens, s)
		return nil, ErrTokenInvalid
	}
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
// keystore and issues an access token on success.
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
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "challenge invalid")
		return
	}

	fp, err := identity.ParseFingerprint(req.Fingerprint)
	if err != nil {
		s.log.Warn("auth verify: fingerprint parse failed",
			"op", "auth.verify",
			"challenge_id", req.ChallengeID,
		)
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
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "challenge invalid")
		return
	}
	if !ed25519.Verify(key.publicKey, canonical, sig) {
		s.log.Warn("auth verify: signature invalid",
			"op", "auth.verify",
			"challenge_id", req.ChallengeID,
			"fingerprint", fp.String(),
		)
		writeError(w, http.StatusUnauthorized, proto.ErrCodeUnauthorized, "challenge invalid")
		return
	}

	tok, err := s.auth.issueToken(fp, req.Scope)
	if err != nil {
		s.log.Error("auth verify: token issue failed", "op", "auth.verify", "err", err)
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}
	// Note: token VALUE never reaches the logger (HEALTH.3 "no
	// secrets"). We log only fingerprint + scope + expires_at.
	s.log.Info("auth verify: token issued",
		"op", "auth.verify",
		"fingerprint", fp.String(),
		"scope", req.Scope,
		"expires_at", tok.expiresAt.UTC().Format(time.RFC3339Nano),
	)
	writeJSON(w, http.StatusOK, proto.AuthVerifyResponse{
		AccessToken: tok.value,
		ExpiresAt:   tok.expiresAt,
	})
}

// requireToken extracts and validates a Bearer token from the
// Authorization header. Returns the token's bound fingerprint or an
// error describing why authorization failed. The caller writes the
// HTTP error response based on this error.
func (s *Server) requireToken(r *http.Request) (identity.Fingerprint, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return identity.Fingerprint{}, errors.New("missing Authorization header")
	}
	if !strings.HasPrefix(header, bearerPrefix) {
		return identity.Fingerprint{}, errors.New("Authorization must be Bearer")
	}
	value := strings.TrimPrefix(header, bearerPrefix)
	tok, err := s.auth.resolveToken(value)
	if err != nil {
		return identity.Fingerprint{}, err
	}
	return tok.fingerprint, nil
}
