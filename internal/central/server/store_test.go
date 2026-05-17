package server

import (
	"context"
	"errors"
	"testing"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

func mkRec(id string) eventlog.Record {
	return eventlog.Record{
		ID:          id,
		Type:        "test.event",
		WorkspaceID: "ws-test",
		Actor:       "l-aaaaaaaaaaaaaaaa",
	}
}

func TestStoreEmptyHead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != "" {
		t.Fatalf("empty store head: got %q", head)
	}
}

func TestStoreAppendIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	r := mkRec("r1")
	added, err := s.Append(ctx, r)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !added {
		t.Fatal("first append should add")
	}
	added2, err := s.Append(ctx, r)
	if err != nil {
		t.Fatalf("Append duplicate: %v", err)
	}
	if added2 {
		t.Fatal("duplicate append should not add")
	}
	n, err := s.Len(ctx)
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if n != 1 {
		t.Fatalf("len after duplicate: got %d want 1", n)
	}
	head, err := s.Head(ctx)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != "r1" {
		t.Fatalf("head: got %q", head)
	}
}

func TestStoreAppendRejectsEmptyID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	if _, err := s.Append(ctx, eventlog.Record{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestStoreSinceEmptyCursorReturnsAll(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	for _, id := range []string{"a", "b", "c"} {
		_, _ = s.Append(ctx, mkRec(id))
	}
	got, err := s.Since(ctx, "")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len: got %d want 3", len(got))
	}
	for i, want := range []string{"a", "b", "c"} {
		if got[i].ID != want {
			t.Fatalf("at %d: got %q want %q", i, got[i].ID, want)
		}
	}
}

func TestStoreSinceCursorReturnsTail(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	for _, id := range []string{"a", "b", "c", "d"} {
		_, _ = s.Append(ctx, mkRec(id))
	}
	got, err := s.Since(ctx, "b")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: got %d want 2", len(got))
	}
	if got[0].ID != "c" || got[1].ID != "d" {
		t.Fatalf("ids: %+v", got)
	}
}

func TestStoreSinceUnknownCursorErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	_, _ = s.Append(ctx, mkRec("a"))
	_, err := s.Since(ctx, "ghost")
	if !errors.Is(err, ErrUnknownCursor) {
		t.Fatalf("got %v want ErrUnknownCursor", err)
	}
}

func TestStoreSinceLatestReturnsEmpty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	for _, id := range []string{"a", "b"} {
		_, _ = s.Append(ctx, mkRec(id))
	}
	got, err := s.Since(ctx, "b")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty tail, got %d records", len(got))
	}
}

func TestStoreAppendBatchEmptyIsNoop(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	added, err := s.AppendBatch(ctx, nil)
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if len(added) != 0 {
		t.Fatalf("empty input should add nothing: %v", added)
	}
}

func TestStoreAppendBatchHappyPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	in := []eventlog.Record{mkRec("a"), mkRec("b"), mkRec("c")}
	added, err := s.AppendBatch(ctx, in)
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if len(added) != 3 {
		t.Fatalf("added: got %d want 3", len(added))
	}
	for i, want := range []string{"a", "b", "c"} {
		if added[i] != want {
			t.Errorf("added[%d]: got %q want %q", i, added[i], want)
		}
	}
	n, _ := s.Len(ctx)
	if n != 3 {
		t.Fatalf("len after batch: got %d want 3", n)
	}
}

func TestStoreAppendBatchSkipsDuplicates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	if _, err := s.AppendBatch(ctx, []eventlog.Record{mkRec("a"), mkRec("b")}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Second batch has one new ("c") + two dupes ("a", "b").
	added, err := s.AppendBatch(ctx, []eventlog.Record{mkRec("a"), mkRec("c"), mkRec("b")})
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if len(added) != 1 || added[0] != "c" {
		t.Fatalf("added: got %v want [c]", added)
	}
	n, _ := s.Len(ctx)
	if n != 3 {
		t.Fatalf("len after dup batch: got %d want 3", n)
	}
}

func TestStoreAppendBatchRejectsEmptyID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewStore()
	in := []eventlog.Record{mkRec("a"), {}, mkRec("c")}
	if _, err := s.AppendBatch(ctx, in); err == nil {
		t.Fatal("expected error for empty id in batch")
	}
	// All-or-nothing: nothing should have landed.
	n, _ := s.Len(ctx)
	if n != 0 {
		t.Fatalf("partial write on validation failure: len=%d", n)
	}
}
