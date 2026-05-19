package web

import (
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/local/savedsearch"
)

// TestSearchPageHasSavedSearchSidebar covers web-ui.SEARCH.3: the
// search page renders the saved-searches sidebar even when empty.
func TestSearchPageHasSavedSearchSidebar(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ssidebar")
	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/search")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body := mustReadBody(t, resp)
	for _, want := range []string{
		`class="search-sidebar"`,
		`saved searches`,
		"none yet",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in /search:\n%s", want, body[:minIntLocal(len(body), 2000)])
		}
	}
}

// TestSearchPageListsSavedFromWorkspace covers SEARCH.3: a saved
// entry in .rex/saved-searches.toml renders in the sidebar with a
// click-to-run link that points back at /search?q=<encoded>.
func TestSearchPageListsSavedFromWorkspace(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "slist")
	regPath := filepath.Join(root, ".rex", "saved-searches.toml")
	reg := &savedsearch.Registry{}
	if err := reg.Set(savedsearch.SavedSearch{Name: "my-rebases", Query: "type:sync.git.rebased"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := savedsearch.Save(regPath, reg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/search")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body := mustReadBody(t, resp)
	if !strings.Contains(body, ">my-rebases<") {
		t.Errorf("saved-search name missing from sidebar:\n%s", body[:minIntLocal(len(body), 2000)])
	}
	wantHref := `href="/search?q=` + url.QueryEscape("type:sync.git.rebased") + `"`
	if !strings.Contains(body, wantHref) {
		t.Errorf("expected %q in body:\n%s", wantHref, body[:minIntLocal(len(body), 2000)])
	}
}

// TestSearchSavePersistsToWorkspace covers SEARCH.3 write: POST /search
// with name+query writes to .rex/saved-searches.toml.
func TestSearchSavePersistsToWorkspace(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ssave")
	hs := newTestServer(t, root)

	form := url.Values{}
	form.Set("name", "needs-rebase")
	form.Set("query", "type:sync.git.conflicted")
	resp, err := http.PostForm(hs.URL+"/search", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body := mustReadBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "saved.") {
		t.Errorf("expected save confirmation:\n%s", body[:minIntLocal(len(body), 2000)])
	}

	reg, err := savedsearch.Load(filepath.Join(root, ".rex", "saved-searches.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, ok := reg.Get("needs-rebase")
	if !ok {
		t.Fatalf("registry missing needs-rebase: %+v", reg)
	}
	if got.Query != "type:sync.git.conflicted" {
		t.Fatalf("query: got %q", got.Query)
	}
}

// TestSearchSaveRejectsInvalidName covers the kebab-name guard:
// invalid names surface in the page banner without persisting
// anything.
func TestSearchSaveRejectsInvalidName(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "sname")
	hs := newTestServer(t, root)

	form := url.Values{}
	form.Set("name", "Bad Name") // spaces + capitals
	form.Set("query", "x")
	resp, err := http.PostForm(hs.URL+"/search", form)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	body := mustReadBody(t, resp)
	if !strings.Contains(body, "save failed") {
		t.Errorf("expected error banner:\n%s", body[:minIntLocal(len(body), 2000)])
	}
	// Nothing should have persisted.
	reg, _ := savedsearch.Load(filepath.Join(root, ".rex", "saved-searches.toml"))
	if reg != nil && len(reg.Searches) > 0 {
		t.Errorf("registry should be empty: %+v", reg.Searches)
	}
}

// TestSearchScopeHiddenWithoutRemotes covers the new behaviour:
// the scope picker only appears when at least one remote is
// registered. The /search page form should still POST/GET cleanly
// even when no picker is rendered (?scope= echoes as a no-op).
func TestSearchScopeHiddenWithoutRemotes(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "scope")
	hs := newTestServer(t, root)

	resp, err := http.Get(hs.URL + "/search?q=x&scope=*")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body := mustReadBody(t, resp)
	if strings.Contains(body, `class="scope-picker"`) {
		t.Errorf("scope picker unexpectedly rendered without remotes:\n%s", body[:minIntLocal(len(body), 2000)])
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func mustReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(body)
}

func minIntLocal(a, b int) int {
	if a < b {
		return a
	}
	return b
}
