package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/audit"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// TestHookDispatcherWritesAuditEvent installs a tiny shell hook
// that always succeeds, runs `rex spec create` (which fires the
// hook for spec.created), then asserts a hook.completed audit
// event lands in the workspace's events.log.
func TestHookDispatcherWritesAuditEvent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook script uses /bin/sh; skipping on windows")
	}
	t.Parallel()

	root := initWorkspace(t, t.TempDir())

	// Drop a hook for spec.created. Per the dispatcher's resolver,
	// it looks up <workspaceRoot>/.rex/hooks/<event-name> and
	// requires the path to be executable.
	hookDir := filepath.Join(root, ".rex", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookPath := filepath.Join(hookDir, "post-spec-created")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	if _, err := executeCommand(t, "spec", "create", "--workspace", root, "demo"); err != nil {
		t.Fatalf("spec create: %v", err)
	}

	// The hook fires asynchronously and the audit event lands
	// after the dispatcher's worker writes it. Drain happens on
	// emitAuditEvent return, so the event should be there by
	// the time spec create completes — but file-system latency
	// can put it just after EOF on slow CI. Poll briefly.
	deadline := time.After(2 * time.Second)
	for {
		events := readHookCompletedEvents(t, root)
		if len(events) > 0 {
			ev := events[0]
			if ev.HookName != "post-spec-created" || ev.HookScope != "workspace" {
				t.Fatalf("hook.completed payload: %+v", ev)
			}
			if ev.ExitCode != 0 || ev.Skipped {
				t.Fatalf("expected successful hook: %+v", ev)
			}
			return
		}
		select {
		case <-deadline:
			// Help debugging by dumping every event in the log.
			t.Logf("events.log dump:")
			r, err := eventlog.OpenReader(filepath.Join(root, ".rex", "events.log"))
			if err == nil {
				for {
					rec, err := r.Next()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Logf("  read err: %v", err)
						break
					}
					t.Logf("  type=%s id=%s", rec.Type, rec.ID)
				}
				r.Close()
			}
			t.Fatal("hook.completed event never landed")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestHookDispatcherSkipsRecursionOnWildcardHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook script uses /bin/sh; skipping on windows")
	}
	t.Parallel()

	root := initWorkspace(t, t.TempDir())
	hookDir := filepath.Join(root, ".rex", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Wildcard hook fires for every event including hook.completed.
	// The Logger's recursion guard should drop hook.completed
	// triggered by another hook.completed; non-trivial to assert
	// without timing flakiness, so we just confirm the system
	// doesn't deadlock or runaway-write infinite events.
	hookPath := filepath.Join(hookDir, "post-any")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	if _, err := executeCommand(t, "spec", "create", "--workspace", root, "demo2"); err != nil {
		t.Fatalf("spec create: %v", err)
	}

	// Give workers a moment, then count hook.completed events.
	// We expect a bounded number — at most a couple from the
	// initial fire — not unbounded growth.
	time.Sleep(300 * time.Millisecond)
	events := readHookCompletedEvents(t, root)
	if len(events) > 5 {
		t.Fatalf("expected bounded hook.completed count (recursion guard); got %d", len(events))
	}
}

func readHookCompletedEvents(t *testing.T, root string) []audit.HookCompletedEvent {
	t.Helper()
	r, err := eventlog.OpenReader(filepath.Join(root, ".rex", "events.log"))
	if err != nil {
		return nil
	}
	defer r.Close()
	var out []audit.HookCompletedEvent
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read events.log: %v", err)
		}
		if rec.Type != audit.EventTypeHookCompleted {
			continue
		}
		var p audit.HookCompletedEvent
		if err := json.Unmarshal(rec.Payload, &p); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}
