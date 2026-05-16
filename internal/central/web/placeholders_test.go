package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newPlaceholderServer(t *testing.T) *httptest.Server {
	t.Helper()
	s, err := New(Options{Version: "test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// TestCentralIdPPlaceholderShowsBannerAndDeferral covers
// /orgs/<id>/idp: the page renders the CENTRAL ONLY banner from
// the shared base layout (web-ui.CENTRAL.2) and explains the
// deferral with a tracking hint.
func TestCentralIdPPlaceholderShowsBannerAndDeferral(t *testing.T) {
	t.Parallel()
	srv := newPlaceholderServer(t)
	resp, err := http.Get(srv.URL + "/orgs/acme/idp")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "CENTRAL ONLY") {
		t.Errorf("CENTRAL ONLY banner missing: %s", html)
	}
	if !strings.Contains(html, "identity provider") {
		t.Errorf("page title missing: %s", html)
	}
	if !strings.Contains(html, "deferred") {
		t.Errorf("deferral copy missing: %s", html)
	}
	if !strings.Contains(html, "IDP-CENTRAL") {
		t.Errorf("tracking hint missing: %s", html)
	}
}

// TestCentralEncryptionKeysPlaceholderShowsBannerAndDeferral
// mirrors the IdP test for /orgs/<id>/encryption-keys.
func TestCentralEncryptionKeysPlaceholderShowsBannerAndDeferral(t *testing.T) {
	t.Parallel()
	srv := newPlaceholderServer(t)
	resp, err := http.Get(srv.URL + "/orgs/acme/encryption-keys")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status: %d body: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, "CENTRAL ONLY") {
		t.Errorf("CENTRAL ONLY banner missing: %s", html)
	}
	if !strings.Contains(html, "encryption keys") {
		t.Errorf("page title missing: %s", html)
	}
	if !strings.Contains(html, "deferred to v1.5") {
		t.Errorf("v1.5 deferral copy missing: %s", html)
	}
	if !strings.Contains(html, "encryption-opt-in") {
		t.Errorf("tracking hint missing: %s", html)
	}
}

// TestCentralOnlyBannerDoesNotLeakIntoWorkspacePages confirms the
// banner is absent on workspace-scoped pages (per CENTRAL.2,
// only org-scoped admin surfaces get the marker).
func TestCentralOnlyBannerDoesNotLeakIntoWorkspacePages(t *testing.T) {
	t.Parallel()
	srv := newAmendmentsServer(t, map[string]string{
		"workspace.yaml": workspaceYAML("ws-acme", "Acme", "active"),
	})
	resp, err := http.Get(srv.URL + "/orgs/acme/workspaces/ws-acme/settings")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "CENTRAL ONLY") {
		t.Errorf("banner leaked into workspace-scoped settings page: %s", body)
	}
}
