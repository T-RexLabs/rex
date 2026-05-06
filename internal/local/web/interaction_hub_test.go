package web

import (
	"context"
	"testing"
	"time"
)

func TestRunInteractionHubInputRoundTrip(t *testing.T) {
	t.Parallel()

	h := newRunInteractionHub()
	h.register("r1", true)
	t.Cleanup(func() { h.unregister("r1") })

	if err := h.submitInput("r1", "hello", false); err != nil {
		t.Fatalf("submitInput: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := h.awaitInput(ctx, "r1")
	if err != nil {
		t.Fatalf("awaitInput: %v", err)
	}
	if got != "hello" {
		t.Fatalf("input: got %q want %q", got, "hello")
	}
}

func TestRunInteractionHubPermissionRoundTrip(t *testing.T) {
	t.Parallel()

	h := newRunInteractionHub()
	h.register("r2", false)
	t.Cleanup(func() { h.unregister("r2") })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resCh := make(chan permissionResolution, 1)
	errCh := make(chan error, 1)
	go func() {
		res, err := h.waitPermission(ctx, "r2", "req-1")
		if err != nil {
			errCh <- err
			return
		}
		resCh <- res
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if h.resolvePermission("r2", "req-1", permissionResolution{Granted: true, Note: "ok"}) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case err := <-errCh:
		t.Fatalf("waitPermission: %v", err)
	case res := <-resCh:
		if !res.Granted || res.Note != "ok" {
			t.Fatalf("resolution: %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for permission resolution")
	}
}
