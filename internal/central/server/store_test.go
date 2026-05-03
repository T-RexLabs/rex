package server

import (
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

	s := NewStore()
	if s.Head() != "" {
		t.Fatalf("empty store head: got %q", s.Head())
	}
}

func TestStoreAppendIsIdempotent(t *testing.T) {
	t.Parallel()

	s := NewStore()
	r := mkRec("r1")
	added, err := s.Append(r)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !added {
		t.Fatal("first append should add")
	}
	added2, err := s.Append(r)
	if err != nil {
		t.Fatalf("Append duplicate: %v", err)
	}
	if added2 {
		t.Fatal("duplicate append should not add")
	}
	if s.Len() != 1 {
		t.Fatalf("len after duplicate: got %d want 1", s.Len())
	}
	if s.Head() != "r1" {
		t.Fatalf("head: got %q", s.Head())
	}
}

func TestStoreAppendRejectsEmptyID(t *testing.T) {
	t.Parallel()

	s := NewStore()
	if _, err := s.Append(eventlog.Record{}); err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestStoreSinceEmptyCursorReturnsAll(t *testing.T) {
	t.Parallel()

	s := NewStore()
	for _, id := range []string{"a", "b", "c"} {
		_, _ = s.Append(mkRec(id))
	}
	got, err := s.Since("")
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

	s := NewStore()
	for _, id := range []string{"a", "b", "c", "d"} {
		_, _ = s.Append(mkRec(id))
	}
	got, err := s.Since("b")
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

	s := NewStore()
	_, _ = s.Append(mkRec("a"))
	_, err := s.Since("ghost")
	if !errors.Is(err, ErrUnknownCursor) {
		t.Fatalf("got %v want ErrUnknownCursor", err)
	}
}

func TestStoreSinceLatestReturnsEmpty(t *testing.T) {
	t.Parallel()

	s := NewStore()
	for _, id := range []string{"a", "b"} {
		_, _ = s.Append(mkRec(id))
	}
	got, err := s.Since("b")
	if err != nil {
		t.Fatalf("Since: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty tail, got %d records", len(got))
	}
}
