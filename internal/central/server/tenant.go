package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

// XRexOrgHeader is the canonical name for the explicit-org
// disambiguation header sync clients send when their authed
// identity is a member of multiple orgs (TENANT.1, TENANT.2,
// TENANT.1-note).
const XRexOrgHeader = "X-Rex-Org"

// MembershipLister is the optional Store interface tenant
// resolution depends on. PostgresStore implements it via
// ListMemberships; the in-memory MemoryStore does not — that
// path skips tenant scoping (dev/test paths run unauthenticated
// and one-tenant). Same opt-in shape as Pinger /
// MembershipEnsurer.
type MembershipLister interface {
	ListMemberships(ctx context.Context, fingerprint string) ([]Membership, error)
	WorkspaceOrg(ctx context.Context, workspaceID string) (string, bool, error)
}

// resolveOrg picks the request's org based on the authenticated
// fingerprint, the optional X-Rex-Org header, and the central's
// membership records. Per TENANT.1-note:
//
//   - 0 memberships → 401-shaped error (the identity is in no
//     org). Caller surfaces with the right status code.
//   - 1 membership and no X-Rex-Org header (or header matches)
//     → infer that org.
//   - 2+ memberships and no X-Rex-Org header → 400, the API
//     never picks (TENANT.2).
//   - X-Rex-Org header set → must match one of the identity's
//     memberships; otherwise 403.
//
// Returns the resolved org id, the org name (for log lines),
// and an error tagged with the appropriate HTTP status code via
// the tenantStatusError type.
type tenantStatusError struct {
	status int
	code   string
	msg    string
}

func (e *tenantStatusError) Error() string { return e.msg }

func (s *Server) resolveOrgForRequest(r *http.Request, fingerprint string) (orgID, orgName string, err error) {
	lister, ok := s.store.(MembershipLister)
	if !ok {
		// MemoryStore — no orgs known, no scoping. Dev/test
		// paths run this way; production with PostgresStore
		// always implements MembershipLister so this branch
		// stays a dev affordance.
		return "", "", nil
	}
	memberships, lerr := lister.ListMemberships(r.Context(), fingerprint)
	if lerr != nil {
		return "", "", &tenantStatusError{
			status: http.StatusInternalServerError,
			code:   "server_error",
			msg:    lerr.Error(),
		}
	}
	if len(memberships) == 0 {
		return "", "", &tenantStatusError{
			status: http.StatusForbidden,
			code:   "no_org_membership",
			msg:    "identity is not a member of any org",
		}
	}
	hdr := strings.TrimSpace(r.Header.Get(XRexOrgHeader))
	if hdr == "" {
		if len(memberships) > 1 {
			return "", "", &tenantStatusError{
				status: http.StatusBadRequest,
				code:   "ambiguous_org",
				msg:    "identity is a member of multiple orgs; set X-Rex-Org header",
			}
		}
		return memberships[0].OrgID, memberships[0].OrgName, nil
	}
	for _, m := range memberships {
		if m.OrgName == hdr || m.OrgID == hdr {
			return m.OrgID, m.OrgName, nil
		}
	}
	return "", "", &tenantStatusError{
		status: http.StatusForbidden,
		code:   "not_a_member",
		msg:    "identity is not a member of the requested org",
	}
}

// enforceWorkspaceBinding checks that every record in the push
// references a workspace_id whose stored org matches the
// resolved org. The first push for a workspace_id wins
// (ORG.6-note "first-push-wins"); subsequent pushes from a
// different org are rejected here with 403 before Append can
// silently keep the prior binding.
//
// Records with empty workspace_id are tolerated — they're
// non-runner audit events the central writes itself or a
// future surface emits without a workspace anchor.
func (s *Server) enforceWorkspaceBinding(ctx context.Context, orgID string, recs []recordWithWorkspace) error {
	lister, ok := s.store.(MembershipLister)
	if !ok {
		return nil
	}
	for _, rec := range recs {
		if rec.WorkspaceID == "" {
			continue
		}
		bound, exists, err := lister.WorkspaceOrg(ctx, rec.WorkspaceID)
		if err != nil {
			return err
		}
		if !exists {
			continue // Append will create it bound to orgID.
		}
		if bound != orgID {
			return errors.New("workspace " + rec.WorkspaceID + " is bound to a different org")
		}
	}
	return nil
}

// recordWithWorkspace is the minimal shape enforceWorkspaceBinding
// reads from each pushed record. Defined as a tiny struct so the
// helper doesn't pull eventlog into its imports.
type recordWithWorkspace struct {
	WorkspaceID string
}
