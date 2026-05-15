package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNewParsesSharedRenderer confirms the central shell's
// constructor reaches the shared internal/web package and parses
// every page (web-ui.CENTRAL-LAYOUT.1). If this fails the package
// boundary is broken — internal/web's embed.FS isn't visible from
// internal/central/web, the FuncMap is missing a func a page
// references, or a template body changed in a way that won't
// compile in central's binary.
func TestNewParsesSharedRenderer(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Spot-check three pages from different roles. If the renderer
	// parsed at all every page is loaded; the explicit names guard
	// against a silent rename / removal of any of these in
	// internal/web/templates/pages.
	for _, page := range []string{"home.tmpl", "specs_list.tmpl", "audit.tmpl"} {
		if !s.HasPage(page) {
			t.Errorf("renderer missing page %q", page)
		}
	}
}

// TestHealthPingProvesWiring exercises GET /_web/health end-to-end.
// Verifies three things in one request: the mux dispatches the
// path, the page renders, and /static/ is wired (the page links to
// /static/app.css, which the static handler must be able to serve).
func TestHealthPingProvesWiring(t *testing.T) {
	t.Parallel()
	s, err := New(Options{Version: "1.2.3"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/_web/health")
	if err != nil {
		t.Fatalf("GET /_web/health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "rex-central web UI") {
		t.Errorf("missing wiring-proof headline; body=%s", body)
	}
	if !strings.Contains(string(body), "1.2.3") {
		t.Errorf("version not surfaced; body=%s", body)
	}

	// /static/ must serve from internal/web's embed.FS.
	staticResp, err := http.Get(srv.URL + "/static/app.css")
	if err != nil {
		t.Fatalf("GET /static/app.css: %v", err)
	}
	defer staticResp.Body.Close()
	if staticResp.StatusCode != http.StatusOK {
		t.Fatalf("static status: %d", staticResp.StatusCode)
	}
	if ct := staticResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("static content-type=%q (want text/css)", ct)
	}
}

// TestUnknownPathReturns404 documents the catchall behaviour: a
// request to a path the web shell does not own returns 404, not a
// silent OK. When central-web is mounted as a fallback on the API
// mux, this is the boundary that keeps unknown API typos from
// rendering an HTML success page.
func TestUnknownPathReturns404(t *testing.T) {
	t.Parallel()
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d (want 404)", resp.StatusCode)
	}
}
