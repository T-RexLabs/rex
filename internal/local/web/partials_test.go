package web

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestSharedPartialsLoaded confirms loadPages parsed every partial
// alongside base + the pages — a missing partial would otherwise
// only surface as a runtime template-execute error on the matching
// page. Done by asking each page template for a partial by name.
func TestSharedPartialsLoaded(t *testing.T) {
	t.Parallel()

	pages, err := loadPages()
	if err != nil {
		t.Fatalf("loadPages: %v", err)
	}
	want := []string{
		"acid_badge", "draft_indicator", "scope_picker",
		"spec_row", "run_row", "audit_row", "workspace_card",
	}
	// Pick any page; partials live in its template tree.
	tmpl := pages["home.tmpl"]
	if tmpl == nil {
		t.Fatal("home.tmpl missing from loaded pages")
	}
	for _, name := range want {
		if tmpl.Lookup(name) == nil {
			t.Errorf("partial %q not registered on home.tmpl", name)
		}
	}
}

// TestScopePickerRendersOnEveryReadPage covers the topbar component:
// every page that renders base.tmpl gets the scope picker's HTML in
// the response. The picker itself only has the "current workspace"
// option when the registry is empty, which is the test scenario.
func TestScopePickerRendersOnEveryReadPage(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "spw")
	hs := newTestServer(t, root)
	for _, path := range []string{"/", "/specs", "/runs", "/audit", "/remotes", "/settings"} {
		resp, err := http.Get(hs.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status: %d body: %s", path, resp.StatusCode, body)
		}
		if !strings.Contains(string(body), `class="scope-picker"`) {
			t.Errorf("%s missing scope-picker markup", path)
		}
		if !strings.Contains(string(body), `name="scope"`) {
			t.Errorf("%s missing scope select element", path)
		}
		if !strings.Contains(string(body), `>current workspace</option>`) {
			t.Errorf("%s missing current-workspace default option", path)
		}
	}
}

// TestDraftIndicatorBranches covers the three partial branches:
// rebase pill, drafts pill, and synced pill. Renders the partial
// directly with controlled input rather than spinning a full server
// up — the integration with /remotes is covered by the page tests.
func TestDraftIndicatorBranches(t *testing.T) {
	t.Parallel()

	pages, err := loadPages()
	if err != nil {
		t.Fatalf("loadPages: %v", err)
	}
	tmpl := pages["home.tmpl"].Lookup("draft_indicator")
	if tmpl == nil {
		t.Fatal("draft_indicator partial missing")
	}

	cases := []struct {
		name string
		in   DraftIndicator
		want []string
		not  []string
	}{
		{
			name: "synced",
			in:   DraftIndicator{Name: "primary", Drafts: 0, NeedsRebase: false},
			want: []string{"pill-synced", ">synced<"},
			not:  []string{"pill-rebase", "pill-drafts"},
		},
		{
			name: "drafts only",
			in:   DraftIndicator{Name: "primary", Drafts: 3, NeedsRebase: false},
			want: []string{"pill-drafts", ">3 drafts<"},
			not:  []string{"pill-rebase", "pill-synced"},
		},
		{
			name: "rebase",
			in:   DraftIndicator{Name: "primary", Drafts: 2, NeedsRebase: true},
			want: []string{"pill-rebase", ">rebase<", "pill-drafts", ">2 drafts<"},
			not:  []string{"pill-synced"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf strings.Builder
			if err := tmpl.Execute(&buf, tc.in); err != nil {
				t.Fatalf("execute: %v", err)
			}
			html := buf.String()
			for _, w := range tc.want {
				if !strings.Contains(html, w) {
					t.Errorf("missing %q in rendered:\n%s", w, html)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(html, w) {
					t.Errorf("unexpected %q in rendered:\n%s", w, html)
				}
			}
		})
	}
}

// TestAcidBadgePartialRendersStringInput covers the bare-string
// argument path (used by recipe panels and run-detail SpecRefs lists).
func TestAcidBadgePartialRendersStringInput(t *testing.T) {
	t.Parallel()

	pages, err := loadPages()
	if err != nil {
		t.Fatalf("loadPages: %v", err)
	}
	tmpl := pages["home.tmpl"].Lookup("acid_badge")
	if tmpl == nil {
		t.Fatal("acid_badge partial missing")
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, "sync.GIT.3"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `data-acid="sync.GIT.3"`) {
		t.Errorf("missing data-acid attribute: %s", html)
	}
	if !strings.Contains(html, `>sync.GIT.3<`) {
		t.Errorf("ACID body not present: %s", html)
	}
}
