package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/sync/proto"
	internalweb "github.com/asabla/rex/internal/web"
)

// stubWorkspaceID is the canonical workspace id every central
// web test fixture implicitly populates. The stubGitStore treats
// its `entries` map as belonging to this one workspace; URLs
// across the test suite use it via `/orgs/<org>/workspaces/ws-1/`.
// Tests that need multi-workspace behaviour construct a
// stubGitStore variant via stubGitStoreMulti below.
const stubWorkspaceID = "ws-1"

// stubGitStore is the minimal GitEntityReader the central
// projections need in tests. Backed by a map[path]content scoped
// to stubWorkspaceID so the test data shape stays in-test and
// self-evident.
type stubGitStore struct {
	entries map[string]string
}

func (s stubGitStore) Get(_ context.Context, workspaceID, path string) (proto.GitEntity, error) {
	if workspaceID != stubWorkspaceID {
		return proto.GitEntity{}, fmt.Errorf("stub: %w: %q", errUnknownEntity, path)
	}
	body, ok := s.entries[path]
	if !ok {
		return proto.GitEntity{}, fmt.Errorf("stub: %w: %q", errUnknownEntity, path)
	}
	return proto.GitEntity{Path: path, Revision: "rev-" + path, Content: body}, nil
}

func (s stubGitStore) List(_ context.Context, workspaceID string) ([]string, error) {
	if workspaceID != stubWorkspaceID {
		return nil, nil
	}
	paths := make([]string, 0, len(s.entries))
	for p := range s.entries {
		paths = append(paths, p)
	}
	return paths, nil
}

// ListWorkspaces satisfies GitWorkspacesLister so the central
// workspaces-index handler can enumerate workspaces backed by
// this stub. Returns the canonical stubWorkspaceID when the
// stub has any entries; empty otherwise so the empty-state
// tests can drive the no-workspaces branch.
func (s stubGitStore) ListWorkspaces() []string {
	if len(s.entries) == 0 {
		return nil
	}
	return []string{stubWorkspaceID}
}

// stubGitStoreMulti scopes content by an explicit per-workspace
// map. Used by the multi-workspace isolation tests below.
type stubGitStoreMulti struct {
	workspaces map[string]map[string]string // wsID → path → content
}

func (s stubGitStoreMulti) Get(_ context.Context, workspaceID, path string) (proto.GitEntity, error) {
	ws, ok := s.workspaces[workspaceID]
	if !ok {
		return proto.GitEntity{}, fmt.Errorf("stub: %w: %q", errUnknownEntity, path)
	}
	body, ok := ws[path]
	if !ok {
		return proto.GitEntity{}, fmt.Errorf("stub: %w: %q", errUnknownEntity, path)
	}
	return proto.GitEntity{Path: path, Revision: "rev-" + path, Content: body}, nil
}

func (s stubGitStoreMulti) List(_ context.Context, workspaceID string) ([]string, error) {
	ws, ok := s.workspaces[workspaceID]
	if !ok {
		return nil, nil
	}
	paths := make([]string, 0, len(ws))
	for p := range ws {
		paths = append(paths, p)
	}
	return paths, nil
}

func (s stubGitStoreMulti) ListWorkspaces() []string {
	ids := make([]string, 0, len(s.workspaces))
	for id := range s.workspaces {
		ids = append(ids, id)
	}
	return ids
}

// validSpecYAML returns a minimal but spec-format-valid YAML
// document with the supplied id. Used to populate the stub
// GitStore so the central projection's parsing path is exercised
// end-to-end (parse failures must surface as skipped/404 rather
// than 500).
func validSpecYAML(id, name string) string {
	return `spec_version: 1
metadata:
  id: ` + id + `
  name: ` + name + `
  state: draft
  owners: []
  related_specs: []
  created_at: 2026-01-01T00:00:00Z
  updated_at: 2026-01-01T00:00:00Z
description: |
  Sample spec body for ` + name + `.
tasks: []
components: {}
constraints: {}
`
}

func newSpecsServer(t *testing.T, store GitEntityReader) *httptest.Server {
	t.Helper()
	s, err := New(Options{
		Version:  "test",
		Resolver: newCentralWorkspaceResolver(store),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestCentralSpecsListRendersGitStoreEntries exercises the happy
// path for /orgs/<org>/workspaces/<ws>/specs: every parseable
// `specs/<id>.yaml` in the GitStore appears in the list, sorted
// by id, and the page renders with the shared template
// (web-ui.SHARED.2).
func TestCentralSpecsListRendersGitStoreEntries(t *testing.T) {
	t.Parallel()
	store := stubGitStore{entries: map[string]string{
		"specs/alpha.yaml":                   validSpecYAML("alpha", "Alpha Spec"),
		"specs/beta.yaml":                    validSpecYAML("beta", "Beta Spec"),
		"workspace.yaml":                     "id: ws-1\nname: Test\nstate: draft\n",
		"specs/_proposed/foo.yaml":           validSpecYAML("foo", "Should be skipped"),
		"specs/_proposed/_accepted/bar.yaml": validSpecYAML("bar", "Should be skipped"),
	}}
	srv := newSpecsServer(t, store)

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/specs")
	if err != nil {
		t.Fatalf("GET specs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	// Both root specs are present; proposed-amendments are not.
	if !strings.Contains(html, "Alpha Spec") {
		t.Errorf("missing alpha spec; body=%s", html)
	}
	if !strings.Contains(html, "Beta Spec") {
		t.Errorf("missing beta spec; body=%s", html)
	}
	if strings.Contains(html, "Should be skipped") {
		t.Errorf("proposed amendment leaked into specs list; body=%s", html)
	}
	// Sort order — alpha must appear before beta in the rendered
	// HTML so the page is deterministic regardless of map iteration.
	if i, j := strings.Index(html, "Alpha Spec"), strings.Index(html, "Beta Spec"); i < 0 || j < 0 || i >= j {
		t.Errorf("specs not sorted by id: alpha@%d beta@%d", i, j)
	}
}

// TestCentralSpecDetailRendersFromGitStore confirms
// /orgs/.../specs/<id> reads the parsed doc + raw YAML from the
// store and renders spec_detail.tmpl. Exercising the chroma
// highlighter at the same time proves the syntax-css cache hook
// is live on central.
func TestCentralSpecDetailRendersFromGitStore(t *testing.T) {
	t.Parallel()
	yaml := validSpecYAML("alpha", "Alpha Spec")
	store := stubGitStore{entries: map[string]string{
		"specs/alpha.yaml": yaml,
	}}
	srv := newSpecsServer(t, store)

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/specs/alpha")
	if err != nil {
		t.Fatalf("GET detail: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "Alpha Spec") {
		t.Errorf("spec name not rendered; body=%s", html)
	}
	if !strings.Contains(html, "Sample spec body") {
		t.Errorf("description prose not rendered; body=%s", html)
	}
}

// TestCentralSpecDetail404ForMissingSpec confirms an unknown id
// surfaces as 404 (not 500). Critical for browser UX — a typo on
// a deep link should land on the not-found page.
func TestCentralSpecDetail404ForMissingSpec(t *testing.T) {
	t.Parallel()
	srv := newSpecsServer(t, stubGitStore{entries: map[string]string{}})

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/specs/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d (want 404)", resp.StatusCode)
	}
}

// TestCentralSpecDetail404ForBadID confirms a non-kebab id is
// treated as not-found rather than reaching the projection.
func TestCentralSpecDetail404ForBadID(t *testing.T) {
	t.Parallel()
	srv := newSpecsServer(t, stubGitStore{entries: map[string]string{}})

	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/specs/BadId")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: %d (want 404)", resp.StatusCode)
	}
}

// TestCentralSpecsListWithoutResolverReturns503 documents the
// misconfigured-deployment branch: --web is on but no Resolver
// was supplied. We surface 503 so an operator notices.
func TestCentralSpecsListWithoutResolverReturns503(t *testing.T) {
	t.Parallel()
	s, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-1/specs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: %d (want 503)", resp.StatusCode)
	}
}

// TestIsSpecPath documents the filter that keeps amendments and
// nested files out of the specs list.
func TestIsSpecPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"specs/alpha.yaml", true},
		{"specs/beta.yaml", true},
		{"specs/_proposed/foo.yaml", false},
		{"specs/_proposed/_accepted/bar.yaml", false},
		{"workspace.yaml", false},
		{"specs/", false},
		{"other/spec.yaml", false},
		{"specs/alpha.txt", false},
	}
	for _, tc := range cases {
		if got := isSpecPath(tc.in); got != tc.want {
			t.Errorf("isSpecPath(%q) = %t (want %t)", tc.in, got, tc.want)
		}
	}
}

// TestCentralSpecProjectionListPropagatesStoreError exercises the
// error path on ListSpecs — a store that errors on List must
// surface that to the handler (no silent empty-list).
func TestCentralSpecProjectionListPropagatesStoreError(t *testing.T) {
	t.Parallel()
	p := newCentralSpecProjection(context.Background(), errStore{}, stubWorkspaceID)
	if _, err := p.ListSpecs(); err == nil {
		t.Fatal("ListSpecs: expected store error to surface")
	}
}

type errStore struct{}

func (errStore) Get(context.Context, string, string) (proto.GitEntity, error) {
	return proto.GitEntity{}, errors.New("store down")
}

func (errStore) List(context.Context, string) ([]string, error) {
	return nil, errors.New("store down")
}

// TestCentralResolverScopesByWorkspaceID is the post-multi-workspace
// invariant: each Resolve call yields a Workspace whose projections
// are bound to the supplied workspaceID. Spec lookups for ws-1 see
// only ws-1's content; lookups for ws-2 see only ws-2's; an empty
// workspaceID yields nil projections.
func TestCentralResolverScopesByWorkspaceID(t *testing.T) {
	t.Parallel()
	store := stubGitStoreMulti{workspaces: map[string]map[string]string{
		"ws-1": {"specs/alpha.yaml": validSpecYAML("alpha", "Alpha")},
		"ws-2": {"specs/beta.yaml": validSpecYAML("beta", "Beta")},
	}}
	r := newCentralWorkspaceResolver(store)

	ws1, err := r.Resolve("ws-1")
	if err != nil {
		t.Fatalf("Resolve(ws-1): %v", err)
	}
	rows1, _ := ws1.Specs.ListSpecs()
	if len(rows1) != 1 || rows1[0].ID != "alpha" {
		t.Errorf("ws-1 rows: %+v (want [alpha])", rows1)
	}

	ws2, err := r.Resolve("ws-2")
	if err != nil {
		t.Fatalf("Resolve(ws-2): %v", err)
	}
	rows2, _ := ws2.Specs.ListSpecs()
	if len(rows2) != 1 || rows2[0].ID != "beta" {
		t.Errorf("ws-2 rows: %+v (want [beta])", rows2)
	}

	empty, err := r.Resolve("")
	if err != nil {
		t.Fatalf("Resolve(\"\"): %v", err)
	}
	if empty.Specs != nil {
		t.Error("empty workspaceID: Specs should be nil")
	}
}

// keep the legacy roundtrip test for the single-workspace stub,
// renamed to reflect what it actually proves now.
func TestCentralResolverRoundtripsWorkspaceID(t *testing.T) {
	t.Parallel()
	store := stubGitStore{entries: map[string]string{
		"specs/alpha.yaml": validSpecYAML("alpha", "Alpha"),
	}}
	r := newCentralWorkspaceResolver(store)
	for _, id := range []string{stubWorkspaceID} {
		ws, err := r.Resolve(id)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", id, err)
		}
		if ws.ID != id {
			t.Errorf("Resolve(%q): ws.ID=%q (want roundtrip)", id, ws.ID)
		}
		if ws.Specs == nil {
			t.Errorf("Resolve(%q): nil Specs projection", id)
		}
		rows, _ := ws.Specs.ListSpecs()
		if len(rows) != 1 || rows[0].ID != "alpha" {
			t.Errorf("Resolve(%q): rows=%+v", id, rows)
		}
	}
}

// Compile-time assertion that the central projection satisfies
// the shared interface. Saves a future class of "the interface
// drifted but the impl didn't" silent breakages.
var _ internalweb.SpecProjection = centralSpecProjection{}
var _ internalweb.WorkspaceResolver = centralWorkspaceResolver{}
