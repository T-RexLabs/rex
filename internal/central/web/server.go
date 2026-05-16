package web

import (
	"fmt"
	"html"
	"io/fs"
	"net/http"

	"github.com/asabla/rex/internal/core/sync/proto"
	internalweb "github.com/asabla/rex/internal/web"
)

// Auth is the subset of central-node auth surface the web shell
// needs to power /login (web-ui.CENTRAL-AUTH.2). cmd/rex-central
// satisfies it by passing the *server.Server through; tests inject
// a stub.
type Auth interface {
	// IssueLoginChallenge mints a fresh challenge for browser
	// login. The returned package carries the challenge id, the
	// signing-input nonce, the canonical hostname, and the absolute
	// expiry. The web handler stamps Redirect on top before
	// rendering.
	IssueLoginChallenge(hostname string) (proto.LoginChallengePackage, error)
}

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
}

// NewGitStoreResolver builds an internalweb.WorkspaceResolver
// backed by the central GitStore (specs) and Event store (runs +
// audit). v1 single-workspace limitation per
// centralWorkspaceResolver — the resolver returns the same
// projections regardless of workspaceID until the multi-workspace
// store refactor lands. Either argument may be nil; the
// corresponding projection on the returned Workspace will then
// be nil and handlers respond 503 for that surface.
func NewGitStoreResolver(git GitEntityReader, events EventReader) internalweb.WorkspaceResolver {
	return centralWorkspaceResolver{git: git, events: events}
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
// the fallback for non-API paths.
func (s *Server) Handler() http.Handler { return s.mux }

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
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/specs", s.handleSpecsList)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/specs/{id}", s.handleSpecDetail)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/runs", s.handleRunsList)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/runs/{id}", s.handleRunDetail)
	s.mux.HandleFunc("GET /orgs/{org}/workspaces/{ws}/audit", s.handleAudit)
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
