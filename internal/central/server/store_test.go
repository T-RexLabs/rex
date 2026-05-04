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
