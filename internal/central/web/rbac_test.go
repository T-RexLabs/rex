package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newRbacGatedServer wires both the session gate (Auth) and the
// per-org RBAC check (Orgs) so the test exercises both layers
// end-to-end. roles maps (orgID, fingerprint) → role; only those
// pairs pass requireOrgMember. tokens lists every token
// fingerprint pair the gate should accept.
func newRbacGatedServer(t *testing.T, tokens map[string]string, roles map[string]map[string]string) *httptest.Server {
	t.Helper()
	allowed := make(map[string]SessionInfo, len(tokens))
	for tok, fp := range tokens {
		allowed[tok] = SessionInfo{Fingerprint: fp, ExpiresAt: time.Now().Add(time.Minute)}
	}
	s, err := New(Options{
		Version:  "test",
		Auth:     &stubAuth{validTokens: allowed},
		Resolver: NewGitStoreResolver(stubGitStore{entries: map[string]string{}}, nil),
		Orgs:     &stubOrgs{roles: roles},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func gatedGet(t *testing.T, srv *httptest.Server, path, token string) *http.Response {
	t.Helper()
	c := noFollowClient()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.AddCookie(&http.Cookie{Name: "rex_session", Value: token})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// TestRBACAllowsMemberOnOwnOrg is the happy path: alice belongs
// to org "acme"; her session reads /orgs/acme/... cleanly.
func TestRBACAllowsMemberOnOwnOrg(t *testing.T) {
	t.Parallel()
	srv := newRbacGatedServer(t,
		map[string]string{"tok-alice": "fp-alice"},
		map[string]map[string]string{"acme": {"fp-alice": "member"}},
	)
	resp := gatedGet(t, srv, "/orgs/acme/workspaces/ws-1/specs", "tok-alice")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d (want 200)", resp.StatusCode)
	}
}

// TestRBACDeniesNonMember is the critical isolation property:
// alice belongs only to "acme"; her session reading
// /orgs/other/... gets 403, regardless of whether "other"
// exists. The body intentionally folds "no such org" into the
// same response so an attacker can't enumerate org ids.
func TestRBACDeniesNonMember(t *testing.T) {
	t.Parallel()
	srv := newRbacGatedServer(t,
		map[string]string{"tok-alice": "fp-alice"},
		map[string]map[string]string{"acme": {"fp-alice": "admin"}},
	)
	for _, path := range []string{
		"/orgs/other-org/workspaces/ws-1/specs",
		"/orgs/other-org/workspaces/ws-1/runs",
		"/orgs/other-org/workspaces/ws-1/audit",
		"/orgs/other-org/workspaces/ws-1/amendments",
		"/orgs/other-org/workspaces/ws-1/settings",
		"/orgs/other-org/workspaces/ws-1/remotes",
		"/orgs/other-org/workspaces",
		"/orgs/other-org/members",
		"/orgs/other-org/roles",
		"/orgs/other-org",
		"/orgs/other-org/idp",
		"/orgs/other-org/encryption-keys",
	} {
		resp := gatedGet(t, srv, path, "tok-alice")
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d (want 403)", path, resp.StatusCode)
		}
	}
}

// TestRBACWorkspaceRoutesEnforceOrg confirms the workspace-
// scoped routes use the {org} segment for the membership check
// (not just the admin org-only pages). Without this every
// authenticated user with knowledge of a workspace id could read
// any org's workspace by typing
// /orgs/<their-org>/workspaces/<some-other-ws>/specs.
func TestRBACWorkspaceRoutesEnforceOrg(t *testing.T) {
	t.Parallel()
	srv := newRbacGatedServer(t,
		map[string]string{"tok-bob": "fp-bob"},
		map[string]map[string]string{"bobs-org": {"fp-bob": "member"}},
	)
	resp := gatedGet(t, srv, "/orgs/alices-org/workspaces/ws-1/specs", "tok-bob")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: %d (want 403)", resp.StatusCode)
	}
}

// TestRBACPropagatesProjectionError surfaces a 500 with the
// underlying message when the Orgs projection itself fails
// (transient Postgres error, etc.) — not a silent 403 that the
// operator can't debug.
func TestRBACPropagatesProjectionError(t *testing.T) {
	t.Parallel()
	// Custom server with an Orgs that errors on every RoleFor
	// call.
	s, err := New(Options{
		Version: "test",
		Auth: &stubAuth{validTokens: map[string]SessionInfo{
			"tok-alice": {Fingerprint: "fp-alice", ExpiresAt: time.Now().Add(time.Minute)},
		}},
		Resolver: NewGitStoreResolver(stubGitStore{entries: map[string]string{}}, nil),
		Orgs:     &stubOrgs{err: errors.New("postgres down")},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	resp := gatedGet(t, hs, "/orgs/acme/members", "tok-alice")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: %d (want 500)", resp.StatusCode)
	}
	body := make([]byte, 1024)
	n, _ := resp.Body.Read(body)
	if !strings.Contains(string(body[:n]), "postgres down") {
		t.Errorf("error not surfaced: %s", body[:n])
	}
}

// TestRBACPassThroughWithoutOrgs documents the v1 carve-out: a
// shell with Auth wired but Orgs unbound (e.g. --keys without
// --db) lets authenticated requests through without the
// per-org check. Production deployments always wire Orgs.
func TestRBACPassThroughWithoutOrgs(t *testing.T) {
	t.Parallel()
	s, err := New(Options{
		Version: "test",
		Auth: &stubAuth{validTokens: map[string]SessionInfo{
			"tok-alice": {Fingerprint: "fp-alice", ExpiresAt: time.Now().Add(time.Minute)},
		}},
		Resolver: NewGitStoreResolver(stubGitStore{entries: map[string]string{}}, nil),
		// Orgs deliberately nil.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	resp := gatedGet(t, hs, "/orgs/acme/workspaces/ws-1/specs", "tok-alice")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d (want 200 — Orgs nil should pass through)", resp.StatusCode)
	}
}
