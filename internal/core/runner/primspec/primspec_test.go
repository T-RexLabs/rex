package primspec

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/asabla/rex/internal/core/runner"
)

func TestStubReturnsNotImplemented(t *testing.T) {
	t.Parallel()

	prim := New()
	res, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{ID: "x", Type: PrimitiveType, Config: json.RawMessage(`{"specs":["overview.yaml"]}`)},
	})
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("err: got %v want ErrNotImplemented", err)
	}
	var out Output
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Implemented {
		t.Fatal("Implemented should be false in stub")
	}
	if out.Note == "" {
		t.Fatal("Note should explain stub state")
	}
}

func TestStubRejectsBadConfig(t *testing.T) {
	t.Parallel()

	prim := New()
	_, err := prim.Run(context.Background(), runner.PrimitiveInput{
		Node: runner.Node{Type: PrimitiveType, Config: json.RawMessage(`{not json`)},
	})
	if err == nil {
		t.Fatal("expected decode error")
	}
}
