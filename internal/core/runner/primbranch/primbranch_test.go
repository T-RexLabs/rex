package primbranch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/asabla/rex/internal/core/runner"
)

func TestBranchEmitsEmptyOutputByDefault(t *testing.T) {
	t.Parallel()
	prim := New()
	out, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "br", Type: PrimitiveType},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out.Output) != "{}" {
		t.Fatalf("default output: %s", out.Output)
	}
}

func TestBranchPassesConfigOutputThrough(t *testing.T) {
	t.Parallel()
	cfg, _ := json.Marshal(Config{Output: json.RawMessage(`{"branch":"left"}`)})
	prim := New()
	out, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "br", Type: PrimitiveType, Config: cfg},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out.Output) != `{"branch":"left"}` {
		t.Fatalf("output: %s", out.Output)
	}
}

func TestBranchRejectsBadConfig(t *testing.T) {
	t.Parallel()
	prim := New()
	_, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "br", Type: PrimitiveType, Config: []byte("not json")},
	})
	if err == nil {
		t.Fatal("expected decode error")
	}
}
