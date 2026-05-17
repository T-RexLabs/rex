package main

import (
	"github.com/asabla/rex/internal/central/server"
	centralweb "github.com/asabla/rex/internal/central/web"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// centralAuthAdapter bridges *server.Server's auth surface to
// centralweb.Auth. The two packages keep their types independent
// — server has ValidatedSession (string fingerprint, time.Time
// expiry), web has SessionInfo (same fields) — so the adapter
// translates at the cmd boundary rather than coupling the
// packages.
type centralAuthAdapter struct {
	s *server.Server
}

// IssueLoginChallenge forwards to the underlying server.
func (a centralAuthAdapter) IssueLoginChallenge(hostname string) (proto.LoginChallengePackage, error) {
	return a.s.IssueLoginChallenge(hostname)
}

// ValidateSession forwards to ResolveBearer via ValidateSession
// and shapes the ValidatedSession into centralweb.SessionInfo.
func (a centralAuthAdapter) ValidateSession(token string) (centralweb.SessionInfo, error) {
	vs, err := a.s.ValidateSession(token)
	if err != nil {
		return centralweb.SessionInfo{}, err
	}
	return centralweb.SessionInfo{
		Fingerprint: vs.Fingerprint,
		ExpiresAt:   vs.ExpiresAt,
	}, nil
}
