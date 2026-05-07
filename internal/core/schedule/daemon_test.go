package schedule

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/specfmt"
)

// TestDaemonFileWatchFires writes a file under a watched glob and
// asserts the dispatcher sees a fire within the debounce window.
// Uses a real fsnotify watcher against a temp dir; deterministic
// because the test creates the file itself.
func TestDaemonFileWatchFires(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".rex", "schedules"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	sched := &Schedule{
		Name: "on-save",
		Trigger: Trigger{
			Kind:       TriggerKindFileWatch,
			Paths:      []string{"src/*.go"},
			DebounceMs: 50,
		},
		Run: &specfmt.Recipe{
			Kind:    specfmt.RecipeKindShell,
			Command: []string{"true"},
		},
	}
	if err := sched.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var (
		mu    sync.Mutex
		fires []Fire
	)
	dispatch := func(ctx context.Context, fire Fire) error {
		mu.Lock()
		defer mu.Unlock()
		fires = append(fires, fire)
		return nil
	}

	d, err := NewDaemon(DaemonOptions{
		WorkspaceRoot: root,
		Dispatch:      dispatch,
		Schedules:     []*Schedule{sched},
	})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneRun := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(doneRun)
	}()

	// Give the watcher a moment to register before writing the
	// triggering file. fsnotify is asynchronous w.r.t. setup;
	// 100ms is generous on every OS we care about.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(srcDir, "foo.go"), []byte("package src\n"), 0o644); err != nil {
		t.Fatalf("write src/foo.go: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		got := len(fires)
		mu.Unlock()
		if got > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for file_watch fire")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	<-doneRun

	mu.Lock()
	defer mu.Unlock()
	if got := fires[0].Schedule.Name; got != "on-save" {
		t.Fatalf("schedule name: %q", got)
	}
	if !contains(fires[0].Reason, "src/foo.go") {
		t.Fatalf("reason: %q", fires[0].Reason)
	}
}

// TestDaemonFileWatchDebouncesBurst checks that a burst of writes
// inside the debounce window collapses to a single fire.
func TestDaemonFileWatchDebouncesBurst(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".rex", "schedules"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}

	sched := &Schedule{
		Name: "on-save",
		Trigger: Trigger{
			Kind:       TriggerKindFileWatch,
			Paths:      []string{"src/*.go"},
			DebounceMs: 200,
		},
		Run: &specfmt.Recipe{Kind: specfmt.RecipeKindShell, Command: []string{"true"}},
	}

	var (
		mu    sync.Mutex
		fires int
	)
	d, err := NewDaemon(DaemonOptions{
		WorkspaceRoot: root,
		Schedules:     []*Schedule{sched},
		Dispatch: func(ctx context.Context, _ Fire) error {
			mu.Lock()
			fires++
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = d.Run(ctx) }()

	time.Sleep(100 * time.Millisecond)
	for i := 0; i < 5; i++ {
		_ = os.WriteFile(filepath.Join(srcDir, "f.go"), []byte("x"), 0o644)
		time.Sleep(20 * time.Millisecond)
	}
	// Give the debounce timer (200ms) plus margin to fire once.
	time.Sleep(500 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if fires == 0 {
		t.Fatal("expected at least one fire after debounce")
	}
	if fires > 2 {
		// Allow up to 2 to absorb pathological event orderings on
		// macOS where rename + chmod can produce multiple events
		// outside the original debounce window.
		t.Fatalf("expected 1-2 debounced fires from a 5-event burst, got %d", fires)
	}
}

// TestFireOnceCallsDispatch covers the `rex schedule trigger` test
// path — FireOnce should invoke the dispatcher exactly once with
// a manual-trigger reason.
func TestFireOnceCallsDispatch(t *testing.T) {
	t.Parallel()
	sched := &Schedule{
		Name:    "x",
		Trigger: Trigger{Kind: TriggerKindCron, Cron: "0 0 * * *"},
		Run:     &specfmt.Recipe{Kind: specfmt.RecipeKindShell, Command: []string{"true"}},
	}
	var got Fire
	count := 0
	err := FireOnce(context.Background(), func(ctx context.Context, f Fire) error {
		got = f
		count++
		return nil
	}, sched, func() time.Time { return time.Unix(1700000000, 0) })
	if err != nil {
		t.Fatalf("FireOnce: %v", err)
	}
	if count != 1 {
		t.Fatalf("count: %d", count)
	}
	if got.Schedule != sched {
		t.Fatal("schedule mismatch")
	}
	if got.Reason != "manual trigger" {
		t.Fatalf("reason: %q", got.Reason)
	}
	if got.At.Unix() != 1700000000 {
		t.Fatalf("now: %v", got.At)
	}
}

func TestPathMatchesAny(t *testing.T) {
	t.Parallel()
	root := "/ws"
	cases := []struct {
		globs []string
		path  string
		want  bool
	}{
		{[]string{"src/*.go"}, "/ws/src/foo.go", true},
		{[]string{"src/*.go"}, "/ws/other/foo.go", false},
		{[]string{"*.go"}, "/ws/foo.go", true},
		{[]string{"*.go"}, "/ws/src/foo.go", true}, // basename match
		{[]string{"node_modules/*"}, "/ws/src/foo.go", false},
	}
	for _, tc := range cases {
		got := pathMatchesAny(tc.globs, tc.path, root)
		if got != tc.want {
			t.Errorf("pathMatchesAny(%v, %q) = %v, want %v", tc.globs, tc.path, got, tc.want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
