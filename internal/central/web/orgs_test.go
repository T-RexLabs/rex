package web

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	internalweb "github.com/asabla/rex/internal/web"
)

// stubOrgs is the deterministic OrgsProjection used in tests.
// roles maps (orgID, fingerprint) → role string; when nil OR no
// entry exists for the pair, RoleFor returns "" (no membership)
// which the rbac helper treats as forbidden.
//
// changes / removes capture the changer/remover fingerprint
// each mutation was called with so audit-emission tests can
// assert the session gate threaded the right identity through.
type stubOrgs struct {
	orgs    map[string]internalweb.OrgSummary
	members map[string][]internalweb.MembershipRow
	roles   map[string]map[string]string
	invites map[string][]internalweb.InviteRow
	err     error

	changes []stubMutationCall
	removes []stubMutationCall
}

type stubMutationCall struct {
	OrgID       string
	Fingerprint string
	NewRole     string
	Changer     string
}

func (s *stubOrgs) ListOrgsForFingerprint(fingerprint string) ([]internalweb.OrgSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]internalweb.OrgSummary, 0, len(s.orgs))
	for _, o := range s.orgs {
		if s.roles[o.ID][fingerprint] != "" {
			out = append(out, o)
		}
	}
	return out, nil
}

func (s *stubOrgs) LookupOrg(orgID string) (internalweb.OrgSummary, bool, error) {
	if s.err != nil {
		return internalweb.OrgSummary{}, false, s.err
	}
	o, ok := s.orgs[orgID]
	return o, ok, nil
}

func (s *stubOrgs) ListMembers(orgID string) ([]internalweb.MembershipRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.members[orgID], nil
}

func (s *stubOrgs) RoleFor(orgID, fingerprint string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.roles[orgID][fingerprint], nil
}

func (s *stubOrgs) ChangeMemberRole(orgID, fingerprint, newRole, changer string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.roles[orgID] == nil {
		return "", internalweb.ErrUnknownMembership
	}
	prior, ok := s.roles[orgID][fingerprint]
	if !ok {
		return "", internalweb.ErrUnknownMembership
	}
	s.roles[orgID][fingerprint] = newRole
	s.changes = append(s.changes, stubMutationCall{
		OrgID: orgID, Fingerprint: fingerprint, NewRole: newRole, Changer: changer,
	})
	return prior, nil
}

func (s *stubOrgs) IssueInvite(orgID, inviter, role string) (internalweb.InviteRow, error) {
	if s.err != nil {
		return internalweb.InviteRow{}, s.err
	}
	if s.invites == nil {
		s.invites = make(map[string][]internalweb.InviteRow)
	}
	inv := internalweb.InviteRow{
		ID:        "inv-" + role,
		Token:     "tok-stub-" + role,
		Role:      role,
		InvitedBy: inviter,
	}
	s.invites[orgID] = append(s.invites[orgID], inv)
	return inv, nil
}

func (s *stubOrgs) ListPendingInvites(orgID string) ([]internalweb.InviteRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.invites[orgID], nil
}

func (s *stubOrgs) RemoveMember(orgID, fingerprint, remover string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.roles[orgID] == nil {
		return "", internalweb.ErrUnknownMembership
	}
	prior, ok := s.roles[orgID][fingerprint]
	if !ok {
		return "", internalweb.ErrUnknownMembership
	}
	delete(s.roles[orgID], fingerprint)
	s.removes = append(s.removes, stubMutationCall{
		OrgID: orgID, Fingerprint: fingerprint, Changer: remover,
	})
	return prior, nil
}

func newOrgsServer(t *testing.T, projection internalweb.OrgsProjection) *httptest.Server {
	t.Helper()
	s, err := New(Options{Version: "test", Orgs: projection})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestCentralOrgOverviewRendersOrg covers /orgs/<id>: page
// surfaces the org's id, name, click-through nav, and the
// CENTRAL ONLY banner.
func TestCentralOrgOverviewRendersOrg(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	projection := &stubOrgs{
		orgs: map[string]internalweb.OrgSummary{
			"acme": {ID: "acme", Name: "acme-co", DisplayName: "Acme Co", CreatedAt: created},
		},
	}
	srv := newOrgsServer(t, projection)

	resp, err := http.Get(srv.URL + "/orgs/acme")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "Acme Co") {
		t.Errorf("display name missing: %s", html)
	}
	if !strings.Contains(html, "<code>acme</code>") {
		t.Errorf("org id missing: %s", html)
	}
	if !strings.Contains(html, `href="/orgs/acme/members"`) ||
		!strings.Contains(html, `href="/orgs/acme/roles"`) ||
		!strings.Contains(html, `href="/orgs/acme/workspaces"`) {
		t.Errorf("quick-action links missing: %s", html)
	}
	if !strings.Contains(html, "CENTRAL ONLY") {
		t.Errorf("CENTRAL ONLY banner missing: %s", html)
	}
}

// TestCentralOrgOverview404 covers the missing-org branch.
func TestCentralOrgOverview404(t *testing.T) {
	t.Parallel()
	srv := newOrgsServer(t, &stubOrgs{orgs: map[string]internalweb.OrgSummary{}})
	resp, err := http.Get(srv.URL + "/orgs/ghost")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d (want 404)", resp.StatusCode)
	}
}

// TestCentralOrgMembersListsRows covers the basic members
// table render — fingerprints land in the page and the
// CENTRAL ONLY banner renders. The Auth-nil dev-mode path
// (this test) skips the gate + RBAC so the admin forms are
// hidden but the read view still works for inspection.
func TestCentralOrgMembersListsRows(t *testing.T) {
	t.Parallel()
	projection := &stubOrgs{
		members: map[string][]internalweb.MembershipRow{
			"acme": {
				{Fingerprint: "fp-alice", Role: "admin", JoinedAt: time.Now().UTC()},
				{Fingerprint: "fp-bob", Role: "member", JoinedAt: time.Now().UTC()},
			},
		},
	}
	srv := newOrgsServer(t, projection)
	resp, err := http.Get(srv.URL + "/orgs/acme/members")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "fp-alice") || !strings.Contains(html, "fp-bob") {
		t.Errorf("members missing: %s", html)
	}
	if !strings.Contains(html, "CENTRAL ONLY") {
		t.Errorf("CENTRAL ONLY banner missing: %s", html)
	}
}

// TestCentralOrgRolesRendersBuiltinCatalog covers /orgs/<id>/roles
// — the page renders the built-in role catalog from
// internal/core/rbac with each role's permissions.
func TestCentralOrgRolesRendersBuiltinCatalog(t *testing.T) {
	t.Parallel()
	srv := newOrgsServer(t, &stubOrgs{})
	resp, err := http.Get(srv.URL + "/orgs/acme/roles")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	for _, role := range []string{"admin", "member", "viewer"} {
		if !strings.Contains(html, role) {
			t.Errorf("role %q missing: %s", role, html)
		}
	}
	if !strings.Contains(html, "central-node.RBAC-SVR.1") {
		t.Errorf("pending-API hint missing on roles page: %s", html)
	}
}

// TestCentralOrgAdminWithoutProjectionReturns503 covers the
// misconfigured-deployment branch: when no Orgs projection is
// bound (e.g. MemoryStore dev mode), the overview + members
// surfaces respond 503. /roles still renders because it uses the
// in-binary rbac catalog directly.
func TestCentralOrgAdminWithoutProjectionReturns503(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, path := range []string{"/orgs/acme", "/orgs/acme/members"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d (want 503)", path, resp.StatusCode)
		}
	}
	// Roles page does not depend on Orgs.
	resp, err := http.Get(srv.URL + "/orgs/acme/roles")
	if err != nil {
		t.Fatalf("GET roles: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/orgs/<id>/roles: status %d (want 200)", resp.StatusCode)
	}
}

// TestCentralOrgMembersPropagatesProjectionError surfaces a
// 500 with the underlying error string so operators can debug.
func TestCentralOrgMembersPropagatesProjectionError(t *testing.T) {
	t.Parallel()
	srv := newOrgsServer(t, &stubOrgs{err: errors.New("postgres down")})
	resp, err := http.Get(srv.URL + "/orgs/acme/members")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: %d (want 500)", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "postgres down") {
		t.Errorf("error not surfaced: %s", body)
	}
}
