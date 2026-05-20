package cli

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/central/server"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

func startCentral(t *testing.T) (*server.Server, *httptest.Server) {
	t.Helper()
	srv, err := server.New(server.Options{})
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs
}

// initSyncWorkspace wires `rex workspace init` so subsequent
// commands have a real .rex/ to operate on, including the
// workspace.created audit event in events.log.
func initSyncWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := executeCommand(t, "init", dir, "--id", "demo", "--name", "Demo"); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	return dir
}

func TestPushRequiresURL(t *testing.T) {
	t.Parallel()

	dir := initSyncWorkspace(t)
	// Point at an empty per-test remotes file so the test doesn't
	// read the dev machine's actual ~/.config/rex/remotes.toml —
	// which on a `make web-dev` host now carries a "primary"
	// entry that the test would happily try to push to.
	reg := tempRegistry(t)
	_, err := executeCommand(t, "push", "--workspace", dir, "--remotes-file", reg)
	if err == nil {
		t.Fatal("missing --url should error")
	}
	if !strings.Contains(err.Error(), "--url") {
		t.Fatalf("error should mention --url: %v", err)
	}
}

func TestPushAdvancesWatermark(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	dir := initSyncWorkspace(t)

	out, err := executeCommand(t, "push",
		"--workspace", dir,
		"--url", hs.URL,
		"--remote", "primary",
	)
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pushed 1") {
		t.Fatalf("output should report 1 pushed: %s", out)
	}

	// Watermark file present and points at the workspace.created
	// event id (whatever HLC was minted).
	wmPath := filepath.Join(dir, ".rex", "drafts", "primary.toml")
	body, err := readFile(wmPath)
	if err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if !strings.Contains(body, "last_acked_event_id") {
		t.Fatalf("watermark missing key: %s", body)
	}
}

func TestPushNothingToPush(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	dir := initSyncWorkspace(t)

	// First push covers the workspace.created event.
	if _, err := executeCommand(t, "push",
		"--workspace", dir, "--url", hs.URL,
	); err != nil {
		t.Fatalf("first push: %v", err)
	}
	out, err := executeCommand(t, "push",
		"--workspace", dir, "--url", hs.URL,
	)
	if err != nil {
		t.Fatalf("second push: %v", err)
	}
	if !strings.Contains(out, "nothing to push") {
		t.Fatalf("output should say nothing to push: %s", out)
	}
}

func TestPullEmpty(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	dir := initSyncWorkspace(t)

	out, err := executeCommand(t, "pull",
		"--workspace", dir, "--url", hs.URL,
	)
	if err != nil {
		t.Fatalf("pull: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no new events") {
		t.Fatalf("expected 'no new events': %s", out)
	}
}

func TestPullReceivesServerEvent(t *testing.T) {
	t.Parallel()

	srv, hs := startCentral(t)
	_, _ = srv.Store().Append(context.Background(), eventlog.Record{
		ID: "srv-1", Type: "test.event", Version: 1,
		Actor: "l-aaaaaaaaaaaaaaaa", WorkspaceID: "ws", Payload: json.RawMessage(`{}`),
	})

	dir := initSyncWorkspace(t)
	// Skip pushing local events — pull then push to make pull
	// see the server-only record despite server having more head
	// than local watermark.
	_, err := executeCommand(t, "pull",
		"--workspace", dir, "--url", hs.URL,
	)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
}

func TestSyncDivergenceRendersConflict(t *testing.T) {
	t.Parallel()

	srv, hs := startCentral(t)
	_, _ = srv.Store().Append(context.Background(), eventlog.Record{
		ID: "srv-1", Type: "test.event", Version: 1,
		Actor: "l-aaaaaaaaaaaaaaaa", WorkspaceID: "ws", Payload: json.RawMessage(`{}`),
	})
	dir := initSyncWorkspace(t)

	_, err := executeCommand(t, "sync",
		"--workspace", dir, "--url", hs.URL,
	)
	if err == nil {
		t.Fatal("sync against divergent server should error")
	}
	if !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("error wording: %v", err)
	}
}

func TestSyncRoundTrip(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	dir := initSyncWorkspace(t)

	out, err := executeCommand(t, "sync",
		"--workspace", dir, "--url", hs.URL,
	)
	if err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	if !strings.Contains(out, "sync ok") {
		t.Fatalf("expected 'sync ok': %s", out)
	}
}

func TestPushJSONFlag(t *testing.T) {
	t.Parallel()

	_, hs := startCentral(t)
	dir := initSyncWorkspace(t)

	out, err := executeCommand(t, "push",
		"--workspace", dir, "--url", hs.URL,
		"--json",
	)
	if err != nil {
		t.Fatalf("push --json: %v\n%s", err, out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if v["accepted"].(float64) != 1 {
		t.Fatalf("accepted: %v", v["accepted"])
	}
}

func readFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	return string(body), err
}
