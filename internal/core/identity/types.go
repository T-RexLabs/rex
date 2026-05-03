package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// FingerprintLen is the byte length of a fingerprint. The hex
// representation is 2*FingerprintLen characters; identity-and-trust.KEY.4
// pins this at 16 hex chars (8 bytes).
const FingerprintLen = 8

// Fingerprint is the 16-hex-char prefix of SHA-256 over the public
// key. It is the canonical short reference for an identity in CLI
// output, audit logs, and the actor field on event records
// (storage.EVENTS.2).
type Fingerprint [FingerprintLen]byte

// String renders the fingerprint as 16 lowercase hex characters.
func (f Fingerprint) String() string {
	return hex.EncodeToString(f[:])
}

// IsZero reports whether the fingerprint is all zeros.
func (f Fingerprint) IsZero() bool {
	for _, b := range f {
		if b != 0 {
			return false
		}
	}
	return true
}

// FingerprintOf computes the fingerprint of an ed25519 public key.
func FingerprintOf(pub ed25519.PublicKey) (Fingerprint, error) {
	if len(pub) != ed25519.PublicKeySize {
		return Fingerprint{}, fmt.Errorf("identity: public key length %d, want %d", len(pub), ed25519.PublicKeySize)
	}
	sum := sha256.Sum256(pub)
	var fp Fingerprint
	copy(fp[:], sum[:FingerprintLen])
	return fp, nil
}

// ParseFingerprint parses a 16-hex-char fingerprint.
func ParseFingerprint(s string) (Fingerprint, error) {
	if len(s) != 2*FingerprintLen {
		return Fingerprint{}, fmt.Errorf("identity: fingerprint length %d, want %d", len(s), 2*FingerprintLen)
	}
	var fp Fingerprint
	if _, err := hex.Decode(fp[:], []byte(s)); err != nil {
		return Fingerprint{}, fmt.Errorf("identity: parse fingerprint: %w", err)
	}
	return fp, nil
}

// Handle is the human-readable name bound to a keypair. Handles are
// kebab-case strings stored alongside the key file.
type Handle string

// handleRE encodes the same kebab-case rule the spec format enforces
// (specfmt.IsKebab) without taking a dependency on that package —
// identity is self-contained so anything in the codebase can validate.
var handleRE = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// IsValidHandle reports whether s is well-formed.
func IsValidHandle(s string) bool { return handleRE.MatchString(s) }

// Role marks an identity as a local-node identity or a central-node
// identity. The role is encoded as the prefix of an Actor string so
// lex comparison under sync.ORDER.3 puts central before local.
type Role string

const (
	RoleLocal   Role = "l"
	RoleCentral Role = "c"
)

// Actor is the (role, fingerprint) pair Rex stamps on every event
// (storage.EVENTS.2 + sync.ORDER.3). String form is "<role>-<fp>"
// (e.g. "l-a1b2c3d4e5f6a7b8"); 18 characters total.
type Actor struct {
	Role        Role
	Fingerprint Fingerprint
}

// String renders an Actor as "<role>-<fingerprint>".
func (a Actor) String() string {
	return string(a.Role) + "-" + a.Fingerprint.String()
}

// ParseActor parses the canonical "<role>-<fingerprint>" form.
func ParseActor(s string) (Actor, error) {
	if len(s) < 2 || s[1] != '-' {
		return Actor{}, fmt.Errorf("identity: actor %q missing role prefix", s)
	}
	role := Role(s[:1])
	switch role {
	case RoleLocal, RoleCentral:
	default:
		return Actor{}, fmt.Errorf("identity: actor %q has unknown role %q", s, role)
	}
	fp, err := ParseFingerprint(s[2:])
	if err != nil {
		return Actor{}, err
	}
	return Actor{Role: role, Fingerprint: fp}, nil
}

// IsLocal reports whether the actor is a local-node identity.
func (a Actor) IsLocal() bool { return a.Role == RoleLocal }

// IsCentral reports whether the actor is a central-node identity.
func (a Actor) IsCentral() bool { return a.Role == RoleCentral }

// LessActor returns true when a sorts before b under sync.ORDER.3.
// Byte-lex comparison of the string form gives the right answer for
// free since "c-..." < "l-..." in ASCII; this helper exists for
// callsite clarity.
func LessActor(a, b Actor) bool {
	return strings.Compare(a.String(), b.String()) < 0
}
