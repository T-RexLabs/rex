package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/sync/proto"
)

// stubRedeemSrv stands up a minimal httptest server that serves
// just the three endpoints `rex remote join` exercises:
//   - GET  /sync/state           -> proto.StateResponse
//   - GET  /invites/<token>      -> proto.PeekInviteResponse (or status)
//   - POST /invites/redeem       -> proto.RedeemInviteResponse (or status)
//
// peekStatus / redeemStatus default to 200; setting them to a
// non-2xx code skips the body decode and returns the status.
// The redeem handler captures the request body so tests can
// assert the CLI forwarded the right payload.
type stubRedeemSrv struct {
	stateFingerprint string
	stateProtoVer    int
	peekResp         proto.PeekInviteResponse
	peekStatus       int
	redeemResp       proto.RedeemInviteResponse
	redeemStatus     int
	lastRedeem       proto.RedeemInviteRequest
}

func startStubRedeemSrv(t *testing.T, s *stubRedeemSrv) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sync/state", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(proto.StateResponse{
			Fingerprint:     s.stateFingerprint,
			ProtocolVersion: s.stateProtoVer,
			Actor:           "c-stub",
		})
	})
	mux.HandleFunc("GET /invites/{token}", func(w http.ResponseWriter, r *http.Request) {
		if s.peekStatus != 0 && s.peekStatus != http.StatusOK {
			w.WriteHeader(s.peekStatus)
			_, _ = w.Write([]byte(`{"error":"stub"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := s.peekResp
		if resp.Token == "" {
			resp.Token = r.PathValue("token")
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("POST /invites/redeem", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&s.lastRedeem)
		if s.redeemStatus != 0 && s.redeemStatus != http.StatusOK {
			w.WriteHeader(s.redeemStatus)
			_, _ = w.Write([]byte(`{"error":"stub"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.redeemResp)
	})
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs
}

// TestRemoteJoinHappyPath covers the end-to-end success path:
// stub central returns expected state + peek + redeem; the local
// registry gains the new entry stamped with the observed
// fingerprint, and the redeem request body carries the local
// public key.
func TestRemoteJoinHappyPath(t *testing.T) {
	t.Parallel()
	stub := &stubRedeemSrv{
		stateFingerprint: "fp-server",
		stateProtoVer:    1,
		peekResp: proto.PeekInviteResponse{
			OrgID:     "acme",
			Role:      "member",
			InvitedBy: "fp-alice",
			ExpiresAt: time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
		},
		redeemResp: proto.RedeemInviteResponse{
			OrgID: "acme", Fingerprint: "fp-local", Role: "member",
		},
	}
	hs := startStubRedeemSrv(t, stub)
	reg := tempRegistry(t)

	out, err := executeCommand(t, "remote", "join", "primary", hs.URL,
		"--invite", "tok-xyz",
		"--remotes-file", reg,
		"--yes",
	)
	if err != nil {
		t.Fatalf("join: %v\n%s", err, out)
	}
	if !strings.Contains(out, "joined") {
		t.Errorf("output missing join confirmation: %s", out)
	}
	if stub.lastRedeem.Token != "tok-xyz" {
		t.Errorf("redeem request: %+v", stub.lastRedeem)
	}
	if !strings.HasPrefix(stub.lastRedeem.PublicKeyPEM, "-----BEGIN PUBLIC KEY-----") {
		t.Errorf("redeem PEM missing: %q", stub.lastRedeem.PublicKeyPEM)
	}
	show, err := executeCommand(t, "remote", "show", "primary", "--remotes-file", reg)
	if err != nil {
		t.Fatalf("show: %v\n%s", err, show)
	}
	if !strings.Contains(show, "fp-server") {
		t.Errorf("show missing observed fingerprint: %s", show)
	}
}

// TestRemoteJoinRequiresInvite covers the cobra flag gate:
// without --invite the command refuses with a clear error.
func TestRemoteJoinRequiresInvite(t *testing.T) {
	t.Parallel()
	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "join", "primary", "https://example.invalid",
		"--remotes-file", reg,
	)
	if err == nil {
		t.Fatal("join without --invite should error")
	}
	if !strings.Contains(err.Error(), "required flag") || !strings.Contains(err.Error(), "invite") {
		t.Fatalf("error wording: %v", err)
	}
}

// TestRemoteJoinRejectsExistingName covers the early gate that
// stops a join from clobbering a registered remote, before any
// network call fires.
func TestRemoteJoinRejectsExistingName(t *testing.T) {
	t.Parallel()
	reg := tempRegistry(t)
	if _, err := executeCommand(t, "remote", "add", "primary", "http://x",
		"--remotes-file", reg, "--skip-handshake",
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := executeCommand(t, "remote", "join", "primary", "http://y",
		"--invite", "tok-xyz",
		"--remotes-file", reg, "--yes",
	)
	if err == nil {
		t.Fatal("join over existing name should error")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("error wording: %v", err)
	}
}

// TestRemoteJoinBadTokenDoesNotRegister covers the
// fail-fast-on-peek branch: 404 from /invites/<token> bubbles
// up as ErrInviteNotFound (with friendly wording) and the
// registry stays empty.
func TestRemoteJoinBadTokenDoesNotRegister(t *testing.T) {
	t.Parallel()
	stub := &stubRedeemSrv{
		stateFingerprint: "fp-server",
		stateProtoVer:    1,
		peekStatus:       http.StatusNotFound,
	}
	hs := startStubRedeemSrv(t, stub)
	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "join", "primary", hs.URL,
		"--invite", "tok-bad",
		"--remotes-file", reg, "--yes",
	)
	if err == nil {
		t.Fatal("join with unknown token should error")
	}
	if !strings.Contains(err.Error(), "not recognised") {
		t.Fatalf("error wording: %v", err)
	}
	list, _ := executeCommand(t, "remote", "list", "--remotes-file", reg)
	if !strings.Contains(list, "no remotes registered") {
		t.Errorf("registry should be empty after bad token: %s", list)
	}
}

// TestRemoteJoinExpiredInviteFriendlyMessage covers the 410
// branch: the central returns Gone, the CLI surfaces "expired"
// wording with a hint about the 7-day TTL.
func TestRemoteJoinExpiredInviteFriendlyMessage(t *testing.T) {
	t.Parallel()
	stub := &stubRedeemSrv{
		stateFingerprint: "fp-server",
		stateProtoVer:    1,
		peekStatus:       http.StatusGone,
	}
	hs := startStubRedeemSrv(t, stub)
	reg := tempRegistry(t)
	_, err := executeCommand(t, "remote", "join", "primary", hs.URL,
		"--invite", "tok-old",
		"--remotes-file", reg, "--yes",
	)
	if err == nil {
		t.Fatal("expired invite should error")
	}
	if !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "7 days") {
		t.Fatalf("error wording: %v", err)
	}
}
