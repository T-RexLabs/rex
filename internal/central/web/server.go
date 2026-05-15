package web

import (
	"fmt"
	"html"
	"io/fs"
	"net/http"

	internalweb "github.com/asabla/rex/internal/web"
)

// Options configure New.
type Options struct {
	// Version surfaces in the wiring-proof page so an operator
	// hitting /_web/health can confirm they're talking to the
	// expected binary. Empty defaults to "dev".
	Version string
}

// Server is the central node's web UI handler. It owns a small
// http.ServeMux and a shared *internalweb.Renderer; routes register
// on the mux. Construction is the wiring test — if internal/web is
// importable and parses cleanly, New succeeds.
type Server struct {
	opts     Options
	renderer *internalweb.Renderer
	mux      *http.ServeMux
}

// New constructs a Server, parses the shared template tree, and
// registers the wiring-proof routes. Returns an error when the
// shared renderer fails to parse (a packaging bug that should
// surface loudly at startup).
func New(opts Options) (*Server, error) {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	r, err := internalweb.NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("central web: build renderer: %w", err)
	}
	s := &Server{opts: opts, renderer: r, mux: http.NewServeMux()}
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
	s.mux.HandleFunc("GET /_web/health", s.handleHealthPing)
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
