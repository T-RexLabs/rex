package primparallel

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asabla/rex/internal/core/runner"
)

// stubPrimitive is the testing surface — each child invocation
// runs `do` and emits the returned (out, err). count tracks the
// number of times Run was called so concurrency assertions are
// possible.
type stubPrimitive struct {
	count int32
	do    func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error)
}

func (s *stubPrimitive) Run(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
	atomic.AddInt32(&s.count, 1)
	return s.do(ctx, in)
}

// makeRegistry registers the primitive under both possible child
// types and returns it for assertions.
func makeRegistry(t *testing.T, prims map[string]runner.Primitive) *runner.PrimitiveRegistry {
	t.Helper()
	reg := runner.NewPrimitiveRegistry()
	for k, p := range prims {
		reg.Register(k, p)
	}
	return reg
}

// runParallel is a tiny helper to build a Node with the given
// JSON config and dispatch the parallel primitive.
func runParallel(ctx context.Context, t *testing.T, reg *runner.PrimitiveRegistry, cfg Config) (runner.PrimitiveOutput, error) {
	t.Helper()
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}
	prim := New(Options{Registry: reg})
	return prim.Run(ctx, runner.PrimitiveInput{
		RunID: "r-1",
		Node:  runner.Node{ID: "fan", Type: PrimitiveType, Config: body},
	})
}

func TestParallelAllSucceed(t *testing.T) {
	t.Parallel()
	stub := &stubPrimitive{
		do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
			return runner.PrimitiveOutput{Output: json.RawMessage(`"ok"`)}, nil
		},
	}
	reg := makeRegistry(t, map[string]runner.Primitive{"echo": stub})

	out, err := runParallel(context.Background(), t, reg, Config{
		Children: []ChildSpec{
			{ID: "a", Type: "echo"},
			{ID: "b", Type: "echo"},
			{ID: "c", Type: "echo"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var got Output
	if err := json.Unmarshal(out.Output, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SucceededCount != 3 || got.FailedCount != 0 {
		t.Fatalf("counts: %+v", got)
	}
	if got.Policy != PolicyAny {
		t.Fatalf("default policy: %q", got.Policy)
	}
	if atomic.LoadInt32(&stub.count) != 3 {
		t.Fatalf("expected 3 child invocations, got %d", stub.count)
	}
}

func TestParallelAnyFailureFailsParent(t *testing.T) {
	t.Parallel()
	ok := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		return runner.PrimitiveOutput{Output: json.RawMessage(`"ok"`)}, nil
	}}
	bad := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		return runner.PrimitiveOutput{}, errors.New("boom")
	}}
	reg := makeRegistry(t, map[string]runner.Primitive{"ok": ok, "bad": bad})

	out, err := runParallel(context.Background(), t, reg, Config{
		Children: []ChildSpec{
			{ID: "a", Type: "ok"},
			{ID: "b", Type: "bad"},
			{ID: "c", Type: "ok"},
		},
	})
	if err == nil {
		t.Fatal("expected failure under PolicyAny")
	}
	if !strings.Contains(err.Error(), "1/3 children failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Output is still populated so the parent NodeFailedEvent
	// preserves child detail.
	var got Output
	if err := json.Unmarshal(out.Output, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SucceededCount != 2 || got.FailedCount != 1 {
		t.Fatalf("counts: %+v", got)
	}
}

func TestParallelMajorityPolicy(t *testing.T) {
	t.Parallel()
	ok := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		return runner.PrimitiveOutput{}, nil
	}}
	bad := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		return runner.PrimitiveOutput{}, errors.New("nope")
	}}
	reg := makeRegistry(t, map[string]runner.Primitive{"ok": ok, "bad": bad})

	// 2 of 3 fail — majority threshold (ceil(3/2)=2) met → fail.
	_, err := runParallel(context.Background(), t, reg, Config{
		FailurePolicy: PolicyMajority,
		Children: []ChildSpec{
			{ID: "a", Type: "ok"},
			{ID: "b", Type: "bad"},
			{ID: "c", Type: "bad"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "policy=majority") {
		t.Fatalf("expected majority failure; got %v", err)
	}

	// 1 of 3 fails — threshold not met → succeed.
	_, err = runParallel(context.Background(), t, reg, Config{
		FailurePolicy: PolicyMajority,
		Children: []ChildSpec{
			{ID: "a", Type: "ok"},
			{ID: "b", Type: "ok"},
			{ID: "c", Type: "bad"},
		},
	})
	if err != nil {
		t.Fatalf("expected majority success with 1/3 fail; got %v", err)
	}
}

func TestParallelAllPolicy(t *testing.T) {
	t.Parallel()
	ok := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		return runner.PrimitiveOutput{}, nil
	}}
	bad := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		return runner.PrimitiveOutput{}, errors.New("nope")
	}}
	reg := makeRegistry(t, map[string]runner.Primitive{"ok": ok, "bad": bad})

	// 1 ok, 1 bad — under PolicyAll the parent still succeeds.
	_, err := runParallel(context.Background(), t, reg, Config{
		FailurePolicy: PolicyAll,
		Children: []ChildSpec{
			{ID: "a", Type: "ok"},
			{ID: "b", Type: "bad"},
		},
	})
	if err != nil {
		t.Fatalf("PolicyAll: 1/2 fail should still succeed; got %v", err)
	}
	// All fail — parent fails.
	_, err = runParallel(context.Background(), t, reg, Config{
		FailurePolicy: PolicyAll,
		Children: []ChildSpec{
			{ID: "a", Type: "bad"},
			{ID: "b", Type: "bad"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "policy=all") {
		t.Fatalf("PolicyAll: all-fail should fail; got %v", err)
	}
}

// TestParallelChildrenRunConcurrently makes each child sleep
// 80ms; with 4 children, serial would take ~320ms. We give the
// run a 200ms budget — under concurrency it finishes well within
// that.
func TestParallelChildrenRunConcurrently(t *testing.T) {
	t.Parallel()
	stub := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		select {
		case <-time.After(80 * time.Millisecond):
			return runner.PrimitiveOutput{}, nil
		case <-ctx.Done():
			return runner.PrimitiveOutput{}, ctx.Err()
		}
	}}
	reg := makeRegistry(t, map[string]runner.Primitive{"sleep": stub})

	start := time.Now()
	_, err := runParallel(context.Background(), t, reg, Config{
		Children: []ChildSpec{
			{ID: "a", Type: "sleep"},
			{ID: "b", Type: "sleep"},
			{ID: "c", Type: "sleep"},
			{ID: "d", Type: "sleep"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("expected concurrent execution; took %s", elapsed)
	}
}

func TestParallelContextCancel(t *testing.T) {
	t.Parallel()
	stub := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		select {
		case <-time.After(500 * time.Millisecond):
			return runner.PrimitiveOutput{}, nil
		case <-ctx.Done():
			return runner.PrimitiveOutput{}, ctx.Err()
		}
	}}
	reg := makeRegistry(t, map[string]runner.Primitive{"slow": stub})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	_, err := runParallel(ctx, t, reg, Config{
		Children: []ChildSpec{
			{ID: "a", Type: "slow"},
			{ID: "b", Type: "slow"},
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected ctx canceled; got %v", err)
	}
}

func TestParallelRejectsEmptyChildren(t *testing.T) {
	t.Parallel()
	reg := makeRegistry(t, nil)
	_, err := runParallel(context.Background(), t, reg, Config{})
	if err == nil || !strings.Contains(err.Error(), "children is required") {
		t.Fatalf("expected empty-children rejection; got %v", err)
	}
}

func TestParallelRejectsDuplicateChildIDs(t *testing.T) {
	t.Parallel()
	stub := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		return runner.PrimitiveOutput{}, nil
	}}
	reg := makeRegistry(t, map[string]runner.Primitive{"x": stub})
	_, err := runParallel(context.Background(), t, reg, Config{
		Children: []ChildSpec{
			{ID: "dup", Type: "x"},
			{ID: "dup", Type: "x"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate child id") {
		t.Fatalf("expected duplicate-id rejection; got %v", err)
	}
}

func TestParallelRejectsUnknownChildType(t *testing.T) {
	t.Parallel()
	reg := makeRegistry(t, nil)
	_, err := runParallel(context.Background(), t, reg, Config{
		Children: []ChildSpec{
			{ID: "a", Type: "missing"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown primitive") {
		t.Fatalf("expected unknown-type rejection; got %v", err)
	}
}

func TestParallelRequiresRegistry(t *testing.T) {
	t.Parallel()
	prim := New(Options{})
	_, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "p", Type: PrimitiveType, Config: json.RawMessage(`{"children":[{"id":"a","type":"x"}]}`)},
	})
	if err == nil || !strings.Contains(err.Error(), "Registry is required") {
		t.Fatalf("expected Registry-required error; got %v", err)
	}
}

func TestParallelRejectsUnknownPolicy(t *testing.T) {
	t.Parallel()
	stub := &stubPrimitive{do: func(ctx context.Context, in runner.PrimitiveInput) (runner.PrimitiveOutput, error) {
		return runner.PrimitiveOutput{}, nil
	}}
	reg := makeRegistry(t, map[string]runner.Primitive{"x": stub})
	_, err := runParallel(context.Background(), t, reg, Config{
		FailurePolicy: "weighted",
		Children:      []ChildSpec{{ID: "a", Type: "x"}},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown failure_policy") {
		t.Fatalf("expected unknown-policy rejection; got %v", err)
	}
}
