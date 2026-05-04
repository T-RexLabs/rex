package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestRemotesPageEmpty(t *testing.T) {
	t.Parallel()

	// No registry file → empty list (the per-user registry is at
	// the platform user-config dir; tests don't write one).
	root := initWorkspace(t, "ws-remotes-empty")
	hs := newTestServer(t, root)
	resp, err := http.Get(hs.URL + "/remotes")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body := readBody(t, resp)
	// Heading + empty hint should both render.
	if !strings.Contains(body, "remotes") {
		t.Fatalf("missing heading: %s", body)
	}
}
