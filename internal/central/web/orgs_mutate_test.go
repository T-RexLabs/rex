package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	internalweb "github.com/asabla/rex/internal/web"
)

// newMutateServer wires Auth + Orgs so the handlers go through
// the full gate + RBAC path. roles seeds the (orgID, fp) → role
// map; tokens authenticate a single test fingerprint per token.
func newMutateServer(t *testing.T, tokens map[string]string, roles map[string]map[string]string) (*httptest.Server, *stubOrgs) {
	t.Helper()
	allowed := make(map[string]SessionInfo, len(tokens))
	for tok, fp := range tokens {
		allowed[tok] = SessionInfo{Fingerprint: fp, ExpiresAt: time.Now().Add(time.Minute)}
	}
	// Mirror roles into members so the page renders rows; the
	// stub's ListMembers reads from members, not roles.
	members := make(map[string][]internalweb.MembershipRow)
	for orgID, fps := range roles {
		for fp, role := range fps {
			members[orgID] = append(members[orgID], internalweb.MembershipRow{
				Fingerprint: fp, Role: role, JoinedAt: time.Now().UTC(),
			})
		}
	}
	orgs := &stubOrgs{
		orgs:    map[string]internalweb.OrgSummary{"acme": {ID: "acme", Name: "acme"}},
		members: members,
		roles:   roles,
	}
	s, err := New(Options{
		Version: "test",
		Auth:    &stubAuth{validTokens: allowed},
		Orgs:    orgs,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, orgs
}

func postForm(t *testing.T, srv *httptest.Server, path, token string, form url.Values) *http.Response {
	t.Helper()
	c := noFollowClient()
	body := strings.NewReader(form.Encode())
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "rex_session", Value: token})
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// TestChangeMemberRoleHappyPath covers the admin-only mutation
// end-to-end: alice (admin) flips bob's role from member to
// viewer; the handler 303s back to /members with a flash; the
// stub state reflects the change.
func TestChangeMemberRoleHappyPath(t *testing.T) {
	t.Parallel()
	srv, orgs := newMutateServer(t,
		map[string]string{"tok-alice": "fp-alice"},
		map[string]map[string]string{"acme": {"fp-alice": "admin", "fp-bob": "member"}},
	)
	resp := postForm(t, srv, "/orgs/acme/members/fp-bob/role", "tok-alice", url.Values{"role": {"viewer"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status: %d (want 303)", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, "/orgs/acme/members?flash=") {
		t.Errorf("Location: %q", loc)
	}
	if got := orgs.roles["acme"]["fp-bob"]; got != "viewer" {
		t.Errorf("role: got %q want viewer", got)
	}
	if len(orgs.changes) != 1 {
		t.Fatalf("changes recorded: %d (want 1)", len(orgs.changes))
	}
	call := orgs.changes[0]
	if call.OrgID != "acme" || call.Fingerprint != "fp-bob" || call.NewRole != "viewer" {
		t.Errorf("change call: %+v", call)
	}
	if call.Changer != "fp-alice" {
		t.Errorf("changer not threaded from session: %q (want fp-alice)", call.Changer)
	}
}

// TestChangeMemberRoleRejectsNonAdmin keeps the admin gate
// strict: bob (member) cannot promote himself even with a
// valid session.
func TestChangeMemberRoleRejectsNonAdmin(t *testing.T) {
	t.Parallel()
	srv, orgs := newMutateServer(t,
		map[string]string{"tok-bob": "fp-bob"},
		map[string]map[string]string{"acme": {"fp-bob": "member"}},
	)
	resp := postForm(t, srv, "/orgs/acme/members/fp-bob/role", "tok-bob", url.Values{"role": {"admin"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: %d (want 403)", resp.StatusCode)
	}
	if got := orgs.roles["acme"]["fp-bob"]; got != "member" {
		t.Errorf("role unexpectedly changed: %q", got)
	}
}

// TestChangeMemberRoleUnknownMembershipIs404 covers the
// not-found branch — admin tries to flip a fingerprint that has
// no membership row.
func TestChangeMemberRoleUnknownMembershipIs404(t *testing.T) {
	t.Parallel()
	srv, _ := newMutateServer(t,
		map[string]string{"tok-alice": "fp-alice"},
		map[string]map[string]string{"acme": {"fp-alice": "admin"}},
	)
	resp := postForm(t, srv, "/orgs/acme/members/fp-ghost/role", "tok-alice", url.Values{"role": {"viewer"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d (want 404)", resp.StatusCode)
	}
}

// TestChangeMemberRoleRejectsEmptyRole keeps the form gate
// honest: a POST without a `role` field 400s rather than
// silently storing "".
func TestChangeMemberRoleRejectsEmptyRole(t *testing.T) {
	t.Parallel()
	srv, _ := newMutateServer(t,
		map[string]string{"tok-alice": "fp-alice"},
		map[string]map[string]string{"acme": {"fp-alice": "admin", "fp-bob": "member"}},
	)
	resp := postForm(t, srv, "/orgs/acme/members/fp-bob/role", "tok-alice", url.Values{})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d (want 400)", resp.StatusCode)
	}
}

// TestRemoveMemberHappyPath covers the admin-only remove flow:
// the membership row goes away and the page 303s back with a
// flash carrying the prior role.
func TestRemoveMemberHappyPath(t *testing.T) {
	t.Parallel()
	srv, orgs := newMutateServer(t,
		map[string]string{"tok-alice": "fp-alice"},
		map[string]map[string]string{"acme": {"fp-alice": "admin", "fp-bob": "member"}},
	)
	resp := postForm(t, srv, "/orgs/acme/members/fp-bob/remove", "tok-alice", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status: %d (want 303)", resp.StatusCode)
	}
	if _, present := orgs.roles["acme"]["fp-bob"]; present {
		t.Error("fp-bob still in roles after remove")
	}
	if len(orgs.removes) != 1 {
		t.Fatalf("removes recorded: %d (want 1)", len(orgs.removes))
	}
	if orgs.removes[0].Changer != "fp-alice" {
		t.Errorf("remover not threaded from session: %q (want fp-alice)", orgs.removes[0].Changer)
	}
}

// TestRemoveMemberRejectsNonAdmin: same admin gate as
// role-change, asserted independently so future refactors that
// split the gate notice the regression.
func TestRemoveMemberRejectsNonAdmin(t *testing.T) {
	t.Parallel()
	srv, _ := newMutateServer(t,
		map[string]string{"tok-bob": "fp-bob"},
		map[string]map[string]string{"acme": {"fp-bob": "member", "fp-alice": "admin"}},
	)
	resp := postForm(t, srv, "/orgs/acme/members/fp-alice/remove", "tok-bob", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status: %d (want 403)", resp.StatusCode)
	}
}

// TestOrgMembersRendersAdminFormsForAdminViewer confirms the
// /orgs/<id>/members page renders the role-change dropdown +
// remove button when the viewer is admin.
func TestOrgMembersRendersAdminFormsForAdminViewer(t *testing.T) {
	t.Parallel()
	srv, _ := newMutateServer(t,
		map[string]string{"tok-alice": "fp-alice"},
		map[string]map[string]string{"acme": {"fp-alice": "admin", "fp-bob": "member"}},
	)
	c := noFollowClient()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/orgs/acme/members", nil)
	req.AddCookie(&http.Cookie{Name: "rex_session", Value: "tok-alice"})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, `action="/orgs/acme/members/fp-bob/role"`) {
		t.Errorf("role-change form missing")
	}
	if !strings.Contains(html, `action="/orgs/acme/members/fp-bob/remove"`) {
		t.Errorf("remove form missing")
	}
	if !strings.Contains(html, `<option value="viewer"`) {
		t.Errorf("role dropdown missing")
	}
	if strings.Contains(html, "Read-only for this role") {
		t.Errorf("non-admin notice leaked into admin view: %s", html)
	}
}

// TestOrgMembersHidesAdminFormsForNonAdminViewer is the
// negative of the above: bob (member) gets the read-only notice
// and no per-row forms.
func TestOrgMembersHidesAdminFormsForNonAdminViewer(t *testing.T) {
	t.Parallel()
	srv, _ := newMutateServer(t,
		map[string]string{"tok-bob": "fp-bob"},
		map[string]map[string]string{"acme": {"fp-alice": "admin", "fp-bob": "member"}},
	)
	c := noFollowClient()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/orgs/acme/members", nil)
	req.AddCookie(&http.Cookie{Name: "rex_session", Value: "tok-bob"})
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if strings.Contains(html, `action="/orgs/acme/members/`) {
		t.Errorf("admin form leaked into non-admin view: %s", html)
	}
	if !strings.Contains(html, "Read-only for this role") {
		t.Errorf("read-only notice missing: %s", html)
	}
}
