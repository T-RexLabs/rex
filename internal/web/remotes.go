package web

// RemoteRow is one row on the /remotes page. Lifted from the
// local shell so both shells render against the same struct
// (web-ui.SHARED.1). Local resolvers populate every field;
// central resolvers populate the synced subset (Name + URL,
// optionally Fingerprint from the workspace.yaml-adjacent
// remotes.toml) and leave drafts / watermarks at their zero
// values — those signals are per-machine and have no central
// equivalent.
type RemoteRow struct {
	Name             string
	URL              string
	Fingerprint      string
	AddedAt          string
	LastSeen         string
	Drafts           int
	NeedsRebase      bool
	LastConflictHead string
}

// IndicatorView returns the partial-friendly view of r for the
// shared draft_indicator partial. Lifted so both shells' RemoteRow
// rendering goes through one place.
func (r RemoteRow) IndicatorView() DraftIndicator {
	return DraftIndicator{
		Name:             r.Name,
		Drafts:           r.Drafts,
		NeedsRebase:      r.NeedsRebase,
		LastConflictHead: r.LastConflictHead,
	}
}

// DraftIndicator is the shape the draft_indicator partial
// expects. Lifted from the local shell so both shells' rows can
// produce it (web-ui.SHARED.1 draft_indicator partial).
type DraftIndicator struct {
	Name             string
	Drafts           int
	NeedsRebase      bool
	LastConflictHead string
}

// RemotesProjection is the read-side surface the shared /remotes
// handler queries. Local resolvers wrap the per-machine
// `~/.config/rex/remotes.toml` registry plus per-remote sync
// watermarks; central resolvers wrap the workspace's
// `.rex/remotes.toml` from the GitStore (storage.WS.2.7) —
// read-only on central per the 2026-05-16 amendment, Decision C.
type RemotesProjection interface {
	ListRemotes() ([]RemoteRow, error)
}
