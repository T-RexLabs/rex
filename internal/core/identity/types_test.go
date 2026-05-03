package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestFingerprintOfRoundTrip(t *testing.T) {
	t.Parallel()

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	fp, err := FingerprintOf(pub)
	if err != nil {
		t.Fatalf("FingerprintOf: %v", err)
	}
	if fp.IsZero() {
		t.Fatal("fingerprint should be non-zero for a real key")
	}
	str := fp.String()
	if len(str) != 16 {
		t.Fatalf("fingerprint length: got %d want 16", len(str))
	}
	parsed, err := ParseFingerprint(str)
	if err != nil {
		t.Fatalf("ParseFingerprint: %v", err)
	}
	if parsed != fp {
		t.Fatalf("round-trip mismatch: got %v want %v", parsed, fp)
	}
}

func TestFingerprintOfRejectsBadKey(t *testing.T) {
	t.Parallel()

	if _, err := FingerprintOf([]byte("too short")); err == nil {
		t.Fatal("expected error for short public key")
	}
}

func TestParseFingerprintRejectsBadInput(t *testing.T) {
	t.Parallel()

	cases := []string{"", "abc", strings.Repeat("zz", 8), strings.Repeat("a", 17)}
	for _, s := range cases {
		if _, err := ParseFingerprint(s); err == nil {
			t.Errorf("ParseFingerprint(%q): expected error", s)
		}
	}
}

func TestIsValidHandle(t *testing.T) {
	t.Parallel()

	good := []string{"alice", "alice-1", "user-jane", "team-rex", "x"}
	for _, s := range good {
		if !IsValidHandle(s) {
			t.Errorf("IsValidHandle(%q) should be true", s)
		}
	}
	bad := []string{"", "Alice", "ALICE", "1alice", "alice-", "-alice", "alice--bob", "alice_bob"}
	for _, s := range bad {
		if IsValidHandle(s) {
			t.Errorf("IsValidHandle(%q) should be false", s)
		}
	}
}

func TestActorRoundTrip(t *testing.T) {
	t.Parallel()

	for _, role := range []Role{RoleLocal, RoleCentral} {
		fp, err := FingerprintOf(make(ed25519.PublicKey, ed25519.PublicKeySize))
		if err != nil {
			t.Fatalf("FingerprintOf: %v", err)
		}
		a := Actor{Role: role, Fingerprint: fp}
		got, err := ParseActor(a.String())
		if err != nil {
			t.Fatalf("ParseActor: %v", err)
		}
		if got != a {
			t.Fatalf("round-trip mismatch: got %+v want %+v", got, a)
		}
		if a.IsLocal() == a.IsCentral() {
			t.Fatalf("role classifier collision for %s", a)
		}
	}
}

func TestParseActorRejectsBadInput(t *testing.T) {
	t.Parallel()

	bad := []string{"", "x", "z-aaaaaaaaaaaaaaaa", "l-short", "lalice", "l_aaaaaaaaaaaaaaaa"}
	for _, s := range bad {
		if _, err := ParseActor(s); err == nil {
			t.Errorf("ParseActor(%q): expected error", s)
		}
	}
}

func TestActorStringSortsCentralFirst(t *testing.T) {
	t.Parallel()

	// Construct a central and a local actor with identical
	// fingerprints; the role prefix alone must be enough to put
	// central first under sync.ORDER.3.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	fp, _ := FingerprintOf(pub)
	central := Actor{Role: RoleCentral, Fingerprint: fp}
	local := Actor{Role: RoleLocal, Fingerprint: fp}
	if !LessActor(central, local) {
		t.Fatalf("central must sort before local: %s vs %s", central, local)
	}
	if LessActor(local, central) {
		t.Fatalf("local must not sort before central: %s vs %s", local, central)
	}
}
