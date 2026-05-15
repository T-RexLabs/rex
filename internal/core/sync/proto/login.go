package proto

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// LoginChallengePackageVersion tags the wire shape of the
// browser-login challenge package. Bump alongside any change to
// LoginChallengePackage's field set so older CLIs fail loudly
// rather than silently mis-sign.
const LoginChallengePackageVersion = "rex-login-v1"

// LoginChallengePackage is the self-contained payload the central
// node's /login page renders for the user to copy into
// `rex remote login --challenge <s>` (web-ui.CENTRAL-AUTH.2). It
// holds everything the CLI needs to sign and POST /auth/verify
// without a follow-up GET against the server:
//
//   - ChallengeID: consumed by /auth/verify (single-use)
//   - Nonce: hex-encoded; signed alongside Hostname + scope
//   - Hostname: server's view of itself; bound into the signing
//     input so a signature for server A cannot replay against B
//     (identity-and-trust.AUTH.1.2)
//   - ExpiresAt: absolute server-clock expiry so the CLI can
//     refuse stale packages locally before bothering the server
//   - Redirect: same-origin path the /auth/redeem handler will
//     navigate to after the cookie is set (default "/" when empty)
//
// Wire form is base64(URL-safe, no padding) of the JSON encoding.
// Field order is locked by JSON object semantics; the Version
// guard is the upgrade lever.
type LoginChallengePackage struct {
	Version     string    `json:"v"`
	ChallengeID string    `json:"id"`
	Nonce       string    `json:"nonce"` // hex-encoded
	Hostname    string    `json:"hostname"`
	ExpiresAt   time.Time `json:"exp"`
	Redirect    string    `json:"r,omitempty"`
}

// EncodeLoginChallengePackage turns a LoginChallengePackage into
// its wire form. Returns an error only on json.Marshal failure,
// which the fixed struct shape rules out — the signature exists so
// callers can compose the encode with other fallible work cleanly.
func EncodeLoginChallengePackage(p LoginChallengePackage) (string, error) {
	if p.Version == "" {
		p.Version = LoginChallengePackageVersion
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode login package: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeLoginChallengePackage reverses EncodeLoginChallengePackage.
// Rejects packages whose Version field is not LoginChallengePackageVersion
// — a silently mis-decoded package would lead to a malformed
// signing input and an opaque /auth/verify failure later.
func DecodeLoginChallengePackage(s string) (LoginChallengePackage, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return LoginChallengePackage{}, fmt.Errorf("decode login package: %w", err)
	}
	var p LoginChallengePackage
	if err := json.Unmarshal(raw, &p); err != nil {
		return LoginChallengePackage{}, fmt.Errorf("unmarshal login package: %w", err)
	}
	if p.Version != LoginChallengePackageVersion {
		return LoginChallengePackage{}, fmt.Errorf("login package version %q (want %q)", p.Version, LoginChallengePackageVersion)
	}
	return p, nil
}
