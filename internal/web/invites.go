package web

import (
	"errors"
	"time"
)

// InviteSummary is the read-side projection of a pending invite
// the GET /invites/<token> handler renders above the redeem form
// (so the recipient knows which org / role they're about to
// accept before pasting their key). Mirrors the server-side
// Invite type without importing internal/central/server (keeps
// internal/web a leaf).
type InviteSummary struct {
	Token     string
	OrgID     string
	Role      string
	InvitedBy string
	ExpiresAt time.Time
}

// RedeemRequest is the parsed POST /invites/redeem payload.
// Handle is optional (empty is fine; it's a human-readable
// nicety stored on the authorized_keys row). PublicKeyPEM is the
// recipient's ed25519 public key in PEM form.
type RedeemRequest struct {
	Token        string
	Handle       string
	PublicKeyPEM string
}

// RedeemOutcome is the read-side projection of a successful
// RedeemInvite call. The handler uses Role + OrgID to build the
// confirmation flash; Fingerprint surfaces on the post-redeem
// page so the recipient can verify what key was registered.
type RedeemOutcome struct {
	OrgID       string
	Fingerprint string
	Role        string
}

// InviteRedeemer is the surface the central web shell calls
// to drive the redeem flow (identity-and-trust.AUTH.2.1 + ORG.5).
// cmd/rex-central satisfies it by wrapping
// PostgresStore.{PeekInvite, RedeemInvite}, plus an audit
// emission + in-memory Keystore overlay on the redeem-side so
// the new key works immediately without a central restart.
//
// Errors map to the package-level sentinels so handlers can
// errors.Is without importing internal/central/server.
type InviteRedeemer interface {
	// PeekInvite returns invite metadata without side effects.
	// Returns ErrInviteNotFound / ErrInviteExpired /
	// ErrInviteAlreadyRedeemed for the matching states; the
	// handler uses these to render the right error page before
	// the recipient ever pastes a key.
	PeekInvite(token string) (InviteSummary, error)
	// RedeemInvite consumes the invite, registers the supplied
	// public key (if new), inserts the membership row, and marks
	// the invite redeemed. The same sentinel set applies plus
	// any wrapped PEM-parse error the handler surfaces as 400.
	RedeemInvite(req RedeemRequest) (RedeemOutcome, error)
}

// Sentinels mirror PostgresStore's ErrInvite* set; the adapter
// translates them so the web layer doesn't import the server
// package directly.
var (
	ErrInviteNotFound        = errors.New("internal/web: invite not found")
	ErrInviteExpired         = errors.New("internal/web: invite expired")
	ErrInviteAlreadyRedeemed = errors.New("internal/web: invite already redeemed")
)
