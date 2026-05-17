package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/asabla/rex/internal/core/sync/proto"
	internalweb "github.com/asabla/rex/internal/web"
)

// sessionContextKey is the unexported context key the session
// gate stores the validated SessionInfo under. Per-handler RBAC
// checks pull it back via SessionFromContext.
type sessionContextKey struct{}

// withSession returns ctx augmented with info — used by the gate
// after a successful ValidateSession.
func withSession(ctx context.Context, info SessionInfo) context.Context {
	return context.WithValue(ctx, sessionContextKey{}, info)
}

// SessionFromContext returns the SessionInfo the gate stored,
// or false when no session is on the context (dev-mode
// pass-through, or a request handled outside the gate).
func SessionFromContext(ctx context.Context) (SessionInfo, bool) {
	info, ok := ctx.Value(sessionContextKey{}).(SessionInfo)
	return info, ok
}

// Auth is the subset of central-node auth surface the web shell
// needs to power /login + the session gate (web-ui.CENTRAL-AUTH.*).
// cmd/rex-central satisfies it by passing the *server.Server
// through; tests inject a stub.
type Auth interface {
	// IssueLoginChallenge mints a fresh challenge for browser
	// login. The returned package carries the challenge id, the
	// signing-input nonce, the canonical hostname, and the absolute
	// expiry. The web handler stamps Redirect on top before
	// rendering.
	IssueLoginChallenge(hostname string) (proto.LoginChallengePackage, error)

	// ValidateSession resolves a bearer/cookie token. Returns a
	// non-nil SessionInfo on success; a non-nil error means the
	// token is unknown, expired, or revoked. The middleware uses
	// only the existence of a successful return — fingerprint
	// rides through SessionInfo for future RBAC hooks the
	// per-page handlers can call.
	ValidateSession(token string) (SessionInfo, error)
}

// SessionInfo is the minimal shape ValidateSession returns. Lets
// the web shell stay free of an internal/core/identity import.
type SessionInfo struct {
	Fingerprint string
	ExpiresAt   time.Time
}

// ErrSessionRequired is the sentinel ValidateSession returns when
// no token was presented at all (as distinct from "token was
// presented but doesn't resolve"). Both branches gate the same
// middleware response; surfacing the difference lets future logs
// distinguish a missing-cookie request from a stolen-token one.
var ErrSessionRequired = errors.New("central web: no session token presented")

// Options configure New.
type Options struct {
	// Version surfaces in the wiring-proof page so an operator
	// hitting /_web/health can confirm they're talking to the
	// expected binary. Empty defaults to "dev".
	Version string
	// BindAddr is the central binary's listen address; surfaces in
	// the footer the shared base.tmpl renders. Empty falls back to
	// "(unspecified)" so the page still renders.
	BindAddr string
	// Auth supplies the challenge-issuing surface /login uses.
	// Optional: when nil, /login responds 503; the rest of the
	// shell still works (useful for the read-side-only deployment
	// path that lands with central-read-side-pages before auth is
	// fully wired in dev).
	Auth Auth
	// Resolver maps a workspace id (from the
	// /orgs/<org-id>/workspaces/<ws-id>/... URL) to its content
	// projection. Optional: when nil, every workspace-scoped read
	// route responds 503. The v1 central wireup binds a
	// GitStore-backed resolver; tests inject stubs.
	Resolver internalweb.WorkspaceResolver
	// Orgs powers the central org-admin surfaces (/orgs/<id>,
	// /orgs/<id>/members, /orgs/<id>/roles). Optional; the v1
	// wireup binds a PostgresStore-backed adapter from
	// cmd/rex-central. When nil, the read-side admin handlers
	// respond 503 with a pointer to central-node.RBAC-SVR.1
	// (admin REST API pending).
	Orgs internalweb.OrgsProjection
	// Redeemer powers the unauthenticated /invites/<token> +
	// /invites/redeem surface (identity-and-trust.AUTH.2.1).
	// Optional; the v1 wireup binds a PostgresStore-backed
	// adapter (plus in-memory Keystore overlay + audit
	// emission) from cmd/rex-central when --db is on. When nil,
	// both routes respond 503.
	Redeemer internalweb.InviteRedeemer
	// OrgAudit powers /orgs/<id>/audit, the org-scoped
	// audit-log tail. Optional; the v1 wireup binds a
	// PostgresStore-backed adapter from cmd/rex-central when
	// --db is on. When nil, the route responds 503.
	OrgAudit internalweb.OrgAuditProjection
}

// NewGitStoreResolver builds an internalweb.WorkspaceResolver
// backed by the central GitStore (specs / amendments / remotes /
// workspace.yaml) and Event store (runs / audit). Each Resolve
// call scopes the returned projections to the supplied
// workspaceID; entities pushed for one workspace are invisible
// to projections for another. Either argument may be nil; the
// corresponding projection on the returned Workspace will then
// be nil and handlers respond 503 for that surface.
func NewGitStoreResolver(git GitEntityReader, events EventReader) internalweb.WorkspaceResolver {
	return centralWorkspaceResolver{git: git, events: events}
}

// NewGitStoreResolverWithSearch is NewGitStoreResolver plus a
// Postgres-backed search surface (central-node.DB.4). Wired by
// cmd/rex-central when --db is set so the /search page can
// dispatch real queries instead of rendering the v1 "not yet
// wired" notice. Any of git / events / search may be nil; the
// corresponding projection on the returned Workspace will then
// be nil and the page falls back to its respective empty / 503
// behaviour.
func NewGitStoreResolverWithSearch(git GitEntityReader, events EventReader, search SearchHitReader) internalweb.WorkspaceResolver {
	return centralWorkspaceResolver{git: git, events: events, search: search}
}

// Server is the central node's web UI handler. It owns a small
// http.ServeMux and a shared *internalweb.Renderer; routes register
// on the mux. Construction is the wiring test — if internal/web is
// importable and parses cleanly, New succeeds.
type Server struct {
	opts        Options
	renderer    *internalweb.Renderer
	highlighter *internalweb.Highlighter
	mux         *http.ServeMux
}

// New constructs a Server, parses the shared template tree, and
// registers the wiring-proof routes. Returns an error when the
// shared renderer fails to parse (a packaging bug that should
// surface loudly at startup).
func New(opts Options) (*Server, error) {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.BindAddr == "" {
		opts.BindAddr = "(unspecified)"
	}
	r, err := internalweb.NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("central web: build renderer: %w", err)
	}
	s := &Server{
		opts:        opts,
		renderer:    r,
		highlighter: internalweb.NewHighlighter(),
		mux:         http.NewServeMux(),
	}
	s.registerRoutes()
	return s, nil
}

// Handler returns the http.Handler the central binary mounts as
// the fallback for non-API paths. Wrapped in the session gate so
// the auth check fires before the mux's path/method routing —
// unauthed POSTs against a GET-only route therefore 401 (or
// 303 → /login) instead of 405-on-method-mismatch.
func (s *Server) Handler() http.Handler {
	return s.requireSession(s.mux)
}

// HasPage reports whether the shared renderer parsed the named
// page. Surfaced so the central-web-shell wiring test can confirm
// the shared template tree is reachable without spinning up a
// full server; production code rarely calls it.
func (s *Server) HasPage(name string) bool { return s.renderer.HasPage(name) }

func (s *Server) registerRoutes() {
	staticSub, _ := fs.Sub(internalweb.StaticFS, "static")
	s.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	s.mux.HandleFunc("GET /static/chroma.css", s.handleChromaCSS)
	s.mux.HandleFunc("GET /_web/health", s.handleHealthPing)
	s.mux.HandleFunc("GET /login", s.handleLogin)
	s.mux.HandleFunc("GET /{$}", s.handleHome)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/specs", s.handleSpecsList)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/specs/{id}", s.handleSpecDetail)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/runs", s.handleRunsList)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/runs/{id}", s.handleRunDetail)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/audit", s.handleAudit)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/amendments", s.handleAmendmentsList)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/amendments/{stem}", s.handleAmendmentDetail)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/search", s.handleSearch)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/remotes", s.handleRemotes)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/settings", s.handleSettings)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces", s.handleWorkspacesIndex)
	s.mux.HandleFunc("GET /orgs/{org}/idp", s.handleOrgIdP)
	s.mux.HandleFunc("GET /orgs/{org}/encryption-keys", s.handleOrgEncryptionKeys)
	s.mux.HandleFunc("GET /orgs/{org}", s.handleOrgOverview)
	s.mux.HandleFunc("GET /orgs/{org}/members", s.handleOrgMembers)
	s.mux.HandleFunc("GET /orgs/{org}/roles", s.handleOrgRoles)
	s.mux.HandleFunc("GET /orgs/{org}/audit", s.handleOrgAudit)
	s.mux.HandleFunc("POST /orgs/{org}/members/{fp}/role", s.handleOrgMemberRoleChange)
	s.mux.HandleFunc("POST /orgs/{org}/members/{fp}/remove", s.handleOrgMemberRemove)
	s.mux.HandleFunc("POST /orgs/{org}/members/invite", s.handleOrgMemberInvite)
	s.mux.HandleFunc("GET /invites/{token}", s.handleInvitePeek)
	s.mux.HandleFunc("POST /invites/redeem", s.handleInviteRedeem)
}

// requireSession wraps the whole central web mux with a session
// check that fires before path/method routing — so unauthed
// requests get a uniform 303-to-/login (GET) or 401 (other
// methods) regardless of whether the underlying route exists or
// matches the request method. The publicly-reachable surfaces
// (/static/, /login, /_web/health) are listed in isPublicWebPath
// and pass straight through.
//
// When Auth is nil the wrapper is a no-op pass-through. This is
// the dev-mode path: `rex-central serve --web` without --keys /
// --db never validates anything anyway. Production deployments
// always set --keys + --db so Auth is wired and the gate fires.
func (s *Server) requireSession(next http.Handler) http.Handler {
	if s.opts.Auth == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicWebPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := cookieOrBearer(r)
		if !ok {
			s.failSession(w, r)
			return
		}
		info, err := s.opts.Auth.ValidateSession(token)
		if err != nil {
			s.failSession(w, r)
			return
		}
		// Stash the validated session on the request context so
		// downstream RBAC checks (requireOrgMember on every
		// /orgs/<id>/... handler) can read the caller's
		// fingerprint without re-validating the token.
		next.ServeHTTP(w, r.WithContext(withSession(r.Context(), info)))
	})
}

// isPublicWebPath reports whether a request path is reachable
// without a session. Kept exhaustive on purpose — every entry
// here is a deliberate carve-out from CENTRAL-AUTH.3's gate.
//
// /invites/<token> + /invites/redeem are public because the
// invite token IS the credential (identity-and-trust.AUTH.2.1);
// recipients reach the redeem form before they have a key
// registered, so a session check would be circular.
func isPublicWebPath(path string) bool {
	switch {
	case path == "/login":
		return true
	case path == "/_web/health":
		return true
	case path == "/invites/redeem":
		return true
	case strings.HasPrefix(path, "/static/"):
		return true
	case strings.HasPrefix(path, "/invites/"):
		return true
	}
	return false
}

// cookieOrBearer pulls the bearer value from either an
// Authorization header or the rex_session cookie, mirroring the
// API server's tokenFromRequest. Defined here so the web shell
// doesn't import internal/central/server.
func cookieOrBearer(r *http.Request) (string, bool) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer "), true
	}
	if c, err := r.Cookie("rex_session"); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}

// failSession is the unauthenticated-request response. GET
// requests bounce to /login with the original request URI
// preserved so /auth/redeem can land them back on the page they
// asked for after the cookie is set. Mutations (POST / PUT / etc)
// 401 because a redirect would silently drop the request body —
// the browser would resubmit only after the user manually replays
// the action, which is worse than a clean failure.
func (s *Server) failSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	target := "/login?redirect=" + url.QueryEscape(r.URL.RequestURI())
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// handleChromaCSS serves the chroma stylesheet generated at
// startup. Mirrored from the local shell so spec_detail.tmpl's
// source tab styles the highlighted YAML the same way on both
// shells (web-ui.SHARED.2).
func (s *Server) handleChromaCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(s.highlighter.HighlightCSS()))
}

// handleLogin renders the browser-side of the login flow
// (web-ui.CENTRAL-AUTH.2): the user lands here, gets a one-time
// challenge string, and is instructed to redeem it via
// `rex remote login`. The path the user originally wanted is
// preserved via ?redirect=<path> on the URL and rolled into the
// challenge package so /auth/redeem lands them there after the
// cookie is set.
//
// 503 when Options.Auth is nil — that's the misconfigured
// deployment ("--web is on but the server didn't pass an Auth")
// rather than a server-side error.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.opts.Auth == nil {
		http.Error(w, "central web: login not configured (Auth missing)", http.StatusServiceUnavailable)
		return
	}
	redirect := r.URL.Query().Get("redirect")
	if redirect == "" {
		redirect = "/"
	}
	pkg, err := s.opts.Auth.IssueLoginChallenge(r.Host)
	if err != nil {
		http.Error(w, "central web: issue challenge: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pkg.Redirect = redirect
	wire, err := proto.EncodeLoginChallengePackage(pkg)
	if err != nil {
		http.Error(w, "central web: encode package: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>rex-central — sign in</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
  <main class="login">
    <h1>Sign in to rex-central</h1>
    <p>This browser is not authenticated. To sign in, open a terminal on
       a machine that holds your keypair and run:</p>
    <pre class="login-cmd"><code>rex remote login &lt;remote-name&gt; --challenge &quot;` + html.EscapeString(wire) + `&quot;</code></pre>
    <p class="login-meta">The challenge expires at <time>` + html.EscapeString(pkg.ExpiresAt.Format("2006-01-02 15:04:05 MST")) + `</time>.
       After signing, your terminal will either open this browser at
       <code>/auth/redeem</code> (desktop default) or print the URL
       for you to paste (headless/SSH fallback).</p>
    <p class="login-meta">You'll land on <code>` + html.EscapeString(redirect) + `</code> after sign-in.</p>
  </main>
</body>
</html>
`
	_, _ = fmt.Fprint(w, body)
}

// handleHome is GET / — the post-login landing page. /auth/redeem
// 303s here by default; without a registered handler the request
// fell through to ServeMux's 404. Lists the orgs the
// authenticated identity belongs to; with exactly one membership
// the page 303s straight on to /orgs/<id>. Falls back to a
// gentle "no orgs" message when the projection is missing
// (dev-mode without --db) or when the caller has no
// memberships yet.
//
// Multi-org rendering is deliberately small in v1: a single
// <ul> of clickable org names. A richer "recent activity"
// surface lands when CENTRAL.* gets to it.
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	// Without Orgs bound (e.g. dev-mode in-memory store) we can't
	// list memberships. Land users on /orgs/default so the existing
	// dev-mode admin can still click around.
	if s.opts.Orgs == nil {
		http.Redirect(w, r, "/orgs/default", http.StatusSeeOther)
		return
	}
	info, ok := SessionFromContext(r.Context())
	if !ok {
		// requireSession should have caught this; defensive.
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	memberships, err := s.opts.Orgs.ListOrgsForFingerprint(info.Fingerprint)
	if err != nil {
		http.Error(w, "central web: list memberships: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(memberships) == 1 {
		http.Redirect(w, r, "/orgs/"+memberships[0].ID, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light dark">
  <title>rex-central</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
  <main class="login">
    <h1>rex-central</h1>
`
	if len(memberships) == 0 {
		body += `    <p>You're signed in as <code>` + html.EscapeString(info.Fingerprint) + `</code>
       but you don't belong to any org yet. Ask an admin to issue you an invite
       (<code>POST /orgs/&lt;id&gt;/members/invite</code>), then redeem with
       <code>rex remote join</code>.</p>
`
	} else {
		body += `    <p>Signed in as <code>` + html.EscapeString(info.Fingerprint) + `</code>. Your orgs:</p>
    <ul>
`
		for _, m := range memberships {
			label := m.DisplayName
			if label == "" {
				label = m.Name
			}
			body += `      <li><a href="/orgs/` + html.EscapeString(m.ID) + `">` +
				html.EscapeString(label) + `</a></li>
`
		}
		body += `    </ul>
`
	}
	body += `  </main>
</body>
</html>
`
	_, _ = fmt.Fprint(w, body)
}

// handleHealthPing is the wiring-proof page. It serves a static
// HTML response that confirms three things: the --web flag wired
// through, the shared internal/web package parsed at startup, and
// HTTP routing reaches the central web mux. Real pages land with
// central-read-side-pages.
func (s *Server) handleHealthPing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	body := `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>rex-central web — wiring ok</title>
  <link rel="stylesheet" href="/static/app.css">
</head>
<body>
  <main>
    <h1>rex-central web UI</h1>
    <p>Web shell is up. Read-side pages land with central-read-side-pages.</p>
    <dl>
      <dt>Version</dt><dd>` + html.EscapeString(s.opts.Version) + `</dd>
    </dl>
  </main>
</body>
</html>
`
	_, _ = fmt.Fprint(w, body)
}
