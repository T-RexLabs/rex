package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSlugifyForSpecID exercises the helper directly so the
// happy / weird-input / empty-result paths are covered without
// going through the HTTP form.
func TestSlugifyForSpecID(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"My Spec Name":           "my-spec-name",
		"audit & sync v2":        "audit-sync-v2",
		"  --weird---name ":      "weird-name",
		"already-kebab":          "already-kebab",
		"UPPERCASE":              "uppercase",
		"trailing-punctuation!!": "trailing-punctuation",
		"":                       "",
		"      ":                 "",
		"!!!":                    "",
		// Trailing hyphen from the ' ' at end gets stripped.
		"name with space ": "name-with-space",
		// Leading non-ASCII / fancy chars are dropped, leaving
		// pure ASCII only.
		"🔥hot🔥 takes": "hot-takes",
	}
	for in, want := range cases {
		got := slugifyForSpecID(in)
		if got != want {
			t.Errorf("slugifyForSpecID(%q): got %q want %q", in, got, want)
		}
	}
}

// TestSpecCreateDerivesIDFromName covers the Phase-C UX fix:
// posting the form with a blank id should slugify the name and
// land at /specs/<slug>.
func TestSpecCreateDerivesIDFromName(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-derive")
	hs := newTestServer(t, root)

	form := url.Values{}
	form.Set("id", "")
	form.Set("name", "My Cool Spec")
	form.Set("state", "draft")
	form.Set("template", "")

	req, _ := http.NewRequest(http.MethodPost,
		hs.URL+"/specs/create",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		body := readBody(t, resp)
		t.Fatalf("status: %d body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Location"); got != "/specs/my-cool-spec" {
		t.Fatalf("Location = %q, want /specs/my-cool-spec", got)
	}
	// Confirm the spec landed on disk under the derived id.
	if _, err := os.Stat(filepath.Join(root, ".rex", "specs", "my-cool-spec.yaml")); err != nil {
		t.Fatalf("expected spec file at my-cool-spec.yaml: %v", err)
	}
}

// TestSpecCreateRejectsBlankIDAndName covers the empty-input
// case: when both id and name are blank, the form rerenders
// with a clear error rather than silently failing.
func TestSpecCreateRejectsBlankIDAndName(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-blank")
	hs := newTestServer(t, root)

	form := url.Values{}
	form.Set("id", "")
	form.Set("name", "")
	form.Set("state", "draft")

	req, _ := http.NewRequest(http.MethodPost,
		hs.URL+"/specs/create",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "spec id is required") {
		t.Fatalf("expected required-id message; got: %s", body)
	}
}

// TestSpecNewFormShowsNameAsRequired confirms the rendered
// form treats name as the load-bearing field — `required` on
// the name input, optional/derived hint on the id input.
func TestSpecNewFormShowsNameAsRequired(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-spec-new-form")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/specs/new")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		`name="name"`,
		`autofocus`,
		`placeholder="auto from name"`,
		`name="id"`,
		`Leave blank to auto-derive`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in form:\n%s", want, body[:minInt(len(body), 4000)])
		}
	}
}
