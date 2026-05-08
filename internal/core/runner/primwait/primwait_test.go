package primwait

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/runner"
)

func TestWaitSucceedsAfterDuration(t *testing.T) {
	t.Parallel()

	cfg, _ := json.Marshal(Config{Duration: 5 * time.Millisecond})
	prim := New(Options{})
	startedAt := time.Now()
	out, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "wait", Type: PrimitiveType, Config: cfg},
	})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed < 5*time.Millisecond {
		t.Fatalf("returned too early: %s", elapsed)
	}
	var got Output
	if err := json.Unmarshal(out.Output, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.WaitedNs <= 0 {
		t.Fatalf("waited_ns: %v", got.WaitedNs)
	}
}

func TestWaitCancelledMidway(t *testing.T) {
	t.Parallel()

	cfg, _ := json.Marshal(Config{Duration: 5 * time.Second})
	prim := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := prim.Run(ctx, runner.PrimitiveInput{
		Node: runner.Node{ID: "wait", Type: PrimitiveType, Config: cfg},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestWaitRejectsInvalidDuration(t *testing.T) {
	t.Parallel()

	cases := map[string]Config{
		"zero":     {Duration: 0},
		"negative": {Duration: -1 * time.Second},
		"too-long": {Duration: 48 * time.Hour},
	}
	prim := New(Options{})
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(cfg)
			_, err := prim.Run(context.Background(), runner.PrimitiveInput{
				Node: runner.Node{ID: "wait", Type: PrimitiveType, Config: body},
			})
			if err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestWaitDecodeError(t *testing.T) {
	t.Parallel()
	prim := New(Options{})
	_, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "wait", Type: PrimitiveType, Config: []byte("not json")},
	})
	if err == nil || !strings.Contains(err.Error(), "decode config") {
		t.Fatalf("expected decode error, got %v", err)
	}
}
