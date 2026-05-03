// Package identity is the local-side identity primitive for Rex.
//
// One identity is an ed25519 keypair plus a human-readable handle
// (identity-and-trust.KEY.1). The signing operation is exposed via
// the Signer interface so passkey/HSM-backed implementations can land
// later without touching call sites (KEY.2). v1 ships the file-on-disk
// Signer only.
//
// The Fingerprint format is SHA-256 of the public key, hex-encoded,
// first 16 hex chars (KEY.4). The package's Actor type composes the
// fingerprint with a role prefix ("c-" central, "l-" local) so the
// resulting string sorts correctly under the (HLC, actor) tiebreaker
// from sync.ORDER.3.
//
// Cross-node authentication, tokens, and RBAC live in their own
// packages once central exists. This package is local-only.
package identity
