package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/sync/proto"
)

func TestPostgresMigrationSeedsDefaultOrg(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	got, err := s.LookupOrg(ctx, DefaultOrgName)
	if err != nil {
		t.Fatalf("LookupOrg(default): %v", err)
	}
	if got.Name != "default" {
		t.Errorf("name: %q", got.Name)
	}
	if got.DisplayName != "Default organization" {
		t.Errorf("display: %q", got.DisplayName)
	}
	if got.ID == "" {
		t.Errorf("id: empty")
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("created_at: zero")
	}
}

func TestPostgresMigrationIsIdempotentForDefaultOrg(t *testing.T) {
	t.Parallel()

	// Run twice via the per-test schema. The second call must
	// not duplicate the default org row — the seed insert uses
	// WHERE NOT EXISTS so reruns are no-ops.
	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	if err := migrate(ctx, s.pool); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	orgs, err := s.ListOrgs(ctx)
	if err != nil {
		t.Fatalf("ListOrgs: %v", err)
	}
	if len(orgs) != 1 {
		t.Fatalf("orgs after re-migrate: got %d want 1", len(orgs))
	}
	if orgs[0].Name != "default" {
		t.Errorf("first org: %q", orgs[0].Name)
	}
}

func TestEnsureDefaultMembershipUpsertsAndIsIdempotent(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	const fp = "aabbccddeeff0011"
	if err := s.EnsureDefaultMembership(ctx, fp); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := s.EnsureDefaultMembership(ctx, fp); err != nil {
		t.Fatalf("second call: %v", err)
	}

	got, err := s.ListMemberships(ctx, fp)
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("memberships: got %d want 1", len(got))
	}
	if got[0].OrgName != "default" || got[0].Role != "member" {
		t.Errorf("got %+v", got[0])
	}
	if got[0].Fingerprint != fp {
		t.Errorf("fingerprint round trip: got %q", got[0].Fingerprint)
	}
}

func TestEnsureDefaultMembershipRejectsEmptyFingerprint(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	if err := s.EnsureDefaultMembership(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty fingerprint")
	}
}

func TestListMembershipsForUnknownFingerprintIsEmpty(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	got, err := s.ListMemberships(context.Background(), "deadbeefcafebabe")
	if err != nil {
		t.Fatalf("ListMemberships: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}

// TestAuthVerifyAutoJoinsDefaultOrg drives the end-to-end auth
// path against a Postgres-backed central and asserts the
// just-verified fingerprint shows up in org_memberships.
func TestAuthVerifyAutoJoinsDefaultOrg(t *testing.T) {
	t.Parallel()

	s, _ := freshPostgresStore(t)
	ctx := context.Background()

	// Build a server backed by the per-test PostgresStore so
	// the MembershipEnsurer hook fires.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	ks := NewKeystore()
	fp, err := ks.Add("test-key", pub)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	srv, err := New(Options{Store: s, Keystore: ks})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// Pre-condition: no membership for fp.
	pre, err := s.ListMemberships(ctx, fp.String())
	if err != nil {
		t.Fatalf("pre ListMemberships: %v", err)
	}
	if len(pre) != 0 {
		t.Fatalf("pre memberships: got %d want 0", len(pre))
	}

	// Challenge + verify.
	chResp, err := http.Post(hs.URL+"/auth/challenge", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST challenge: %v", err)
	}
	defer chResp.Body.Close()
	var ch proto.AuthChallengeResponse
	if err := json.NewDecoder(chResp.Body).Decode(&ch); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version:  proto.AuthSigningVersion,
		Nonce:    ch.Nonce,
		Hostname: ch.Hostname,
		Scope:    "sync",
	})
	verifyBody, _ := json.Marshal(proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: fp.String(),
		Signature:   hex.EncodeToString(ed25519.Sign(priv, canonical)),
		Scope:       "sync",
	})
	vResp, err := http.Post(hs.URL+"/auth/verify", "application/json", strings.NewReader(string(verifyBody)))
	if err != nil {
		t.Fatalf("POST verify: %v", err)
	}
	defer vResp.Body.Close()
	if vResp.StatusCode != http.StatusOK {
		t.Fatalf("verify status: %d", vResp.StatusCode)
	}

	// Post-condition: fp is a member of the default org.
	post, err := s.ListMemberships(ctx, fp.String())
	if err != nil {
		t.Fatalf("post ListMemberships: %v", err)
	}
	if len(post) != 1 {
		t.Fatalf("post memberships: got %d want 1", len(post))
	}
	if post[0].OrgName != "default" {
		t.Errorf("auto-joined to %q, want default", post[0].OrgName)
	}
}
