package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// initWorkspace builds a TempDir with .rex/hooks/ ready for hook
// installation.
func initWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".rex", "hooks"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root
}

func writeShellHook(t *testing.T, dir, name, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, name)
	full := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(full), mode); err != nil {
		t.Fatalf("write hook %s: %v", name, err)
	}
}

func mkRecord(t *testing.T, id, eventType string) eventlog.Record {
	t.Helper()
	return eventlog.Record{
		ID:          id,
		Type:        eventType,
		Version:     1,
		Actor:       "l-aaaaaaaaaaaaaaaa",
		WorkspaceID: "ws-test",
		Payload:     json.RawMessage(`{"k":"v"}`),
	}
}

func TestDispatcherFiresExecutableHookAndCapturesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell hooks not supported on windows")
	}
	t.Parallel()

	root := initWorkspace(t)
	hookDir := filepath.Join(root, ".rex", "hooks")
	writeShellHook(t, hookDir, "post-spec-edit", `cat > "$REX_WORKSPACE_PATH/captured-stdin.json"
echo "exec OK"`, 0o755)

	d := New(Options{WorkspaceRoot: root})
	d.OnAppend(mkRecord(t, "ev-1", "spec.edit"))
	d.Drain()

	// Hook log captured the script's stdout.
	logPath := filepath.Join(root, ".rex", hookLogDirName, "ev-1.post-spec-edit.log")
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read hook log: %v", err)
	}
	if !strings.Contains(string(body), "exec OK") {
		t.Fatalf("hook log missing stdout: %q", body)
	}

	// JSON event payload reached the hook on stdin.
	stdinPath := filepath.Join(root, "captured-stdin.json")
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read captured stdin: %v", err)
	}
	var rec eventlog.Record
	if err := json.Unmarshal(stdin, &rec); err != nil {
		t.Fatalf("decode captured stdin: %v\n%s", err, stdin)
	}
	if rec.ID != "ev-1" || rec.Type != "spec.edit" {
		t.Fatalf("captured record drift: %+v", rec)
	}
}

func TestDispatcherSkipsNonExecutableHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix mode bits not supported on windows")
	}
	t.Parallel()

	root := initWorkspace(t)
	hookDir := filepath.Join(root, ".rex", "hooks")
	writeShellHook(t, hookDir, "post-spec-edit", `touch "$REX_WORKSPACE_PATH/should-not-exist"`, 0o644)

	var results []Result
	var mu sync.Mutex
	d := New(Options{
		WorkspaceRoot: root,
		Logger: func(r Result) {
			mu.Lock()
			results = append(results, r)
			mu.Unlock()
		},
	})
	d.OnAppend(mkRecord(t, "ev-2", "spec.edit"))
	d.Drain()

	if _, err := os.Stat(filepath.Join(root, "should-not-exist")); err == nil {
		t.Fatal("non-executable hook should not have run")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(results) != 0 {
		t.Fatalf("logger should not see non-executable hooks: %+v", results)
	}
}

func TestDispatcherSkipsSidecarConfigToml(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell hooks not supported on windows")
	}
	t.Parallel()

	root := initWorkspace(t)
	hookDir := filepath.Join(root, ".rex", "hooks")
	// A real hook plus a sidecar config — only the real one fires.
	writeShellHook(t, hookDir, "post-spec-edit", `echo real`, 0o755)
	writeShellHook(t, hookDir, "post-spec-edit.config.toml", `echo sidecar > "$REX_WORKSPACE_PATH/sidecar-marker"`, 0o755)

	d := New(Options{WorkspaceRoot: root})
	d.OnAppend(mkRecord(t, "ev-3", "spec.edit"))
	d.Drain()

	if _, err := os.Stat(filepath.Join(root, "sidecar-marker")); err == nil {
		t.Fatal("sidecar config.toml should not be invoked as a hook")
	}
}

func TestDispatcherHonorsTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix sleep not supported on windows")
	}
	t.Parallel()

	root := initWorkspace(t)
	hookDir := filepath.Join(root, ".rex", "hooks")
	writeShellHook(t, hookDir, "post-spec-edit", `sleep 5
touch "$REX_WORKSPACE_PATH/should-not-finish"`, 0o755)

	var resultCh = make(chan Result, 1)
	d := New(Options{
		WorkspaceRoot: root,
		Timeout:       150 * time.Millisecond,
		Logger:        func(r Result) { resultCh <- r },
	})
	d.OnAppend(mkRecord(t, "ev-4", "spec.edit"))
	d.Drain()

	select {
	case res := <-resultCh:
		if !strings.Contains(res.Reason, "timeout") {
			t.Fatalf("expected timeout reason, got %+v", res)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatcher did not surface a result for the timed-out hook")
	}
	if _, err := os.Stat(filepath.Join(root, "should-not-finish")); err == nil {
		t.Fatal("timed-out hook should not have completed")
	}
}

func TestDispatcherWildcardFiresForAnyEvent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell hooks not supported on windows")
	}
	t.Parallel()

	root := initWorkspace(t)
	hookDir := filepath.Join(root, ".rex", "hooks")
	writeShellHook(t, hookDir, "post-any", `echo wildcard`, 0o755)

	var ran []Result
	var mu sync.Mutex
	d := New(Options{
		WorkspaceRoot: root,
		Logger: func(r Result) {
			mu.Lock()
			ran = append(ran, r)
			mu.Unlock()
		},
	})
	d.OnAppend(mkRecord(t, "ev-5", "anything.different"))
	d.Drain()

	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 1 {
		t.Fatalf("wildcard should fire once: got %+v", ran)
	}
	if ran[0].HookName != "post-any" {
		t.Fatalf("expected post-any: %+v", ran[0])
	}
}

func TestDispatcherWorkspaceAndGlobalBothFire(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell hooks not supported on windows")
	}
	t.Parallel()

	root := initWorkspace(t)
	wsDir := filepath.Join(root, ".rex", "hooks")
	writeShellHook(t, wsDir, "post-spec-edit", `echo ws`, 0o755)

	globalDir := t.TempDir()
	writeShellHook(t, globalDir, "post-spec-edit", `echo global`, 0o755)

	var ran []Result
	var mu sync.Mutex
	d := New(Options{
		WorkspaceRoot:  root,
		GlobalHooksDir: globalDir,
		Logger: func(r Result) {
			mu.Lock()
			ran = append(ran, r)
			mu.Unlock()
		},
	})
	d.OnAppend(mkRecord(t, "ev-6", "spec.edit"))
	d.Drain()

	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 2 {
		t.Fatalf("ws + global should both fire: got %+v", ran)
	}
	scopes := map[string]int{}
	for _, r := range ran {
		scopes[r.Scope]++
	}
	if scopes["workspace"] != 1 || scopes["global"] != 1 {
		t.Fatalf("scope counts: %v", scopes)
	}
}

func TestDispatcherNoHooksIsSilent(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t)
	var ran []Result
	d := New(Options{
		WorkspaceRoot: root,
		Logger:        func(r Result) { ran = append(ran, r) },
	})
	d.OnAppend(mkRecord(t, "ev-7", "spec.edit"))
	d.Drain()

	if len(ran) != 0 {
		t.Fatalf("no hooks should mean no logger calls, got %+v", ran)
	}
}

func TestHookNameForEventMaps(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"run.completed":        "post-run-completed",
		"spec.edit":            "post-spec-edit",
		"workspace.created":    "post-workspace-created",
		"permission.requested": "post-permission-requested",
	}
	for in, want := range cases {
		if got := hookNameForEvent(in); got != want {
			t.Errorf("hookNameForEvent(%q) = %q want %q", in, got, want)
		}
	}
}
