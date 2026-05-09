package specamend

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeAmendment is a test helper that drops a minimal valid
// amendment file under the named state directory.
func writeAmendment(t *testing.T, root, stem, target, date, state, summary string) string {
	t.Helper()
	dir := Dir(root)
	if state == "accepted" {
		dir = AcceptedDir(root)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	body := strings.Join([]string{
		"# test amendment",
		"amendment_for: " + target,
		"amendment_date: " + date,
		"state: " + state,
		"summary: |",
		"  " + summary,
		"",
	}, "\n")
	path := filepath.Join(dir, stem+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestListEmptyWorkspace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	got, err := List(root, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %d", len(got))
	}
}

func TestListSortAndFilter(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAmendment(t, root, "audit-amendment-2026-05-08", "audit", "2026-05-08", "accepted", "older accepted")
	writeAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "newer cli")
	writeAmendment(t, root, "audit-amendment-2026-05-10", "audit", "2026-05-10", "proposed", "newer audit")

	all, err := List(root, ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 amendments, got %d", len(all))
	}
	// Date desc, then stem asc.
	wantOrder := []string{"audit-amendment-2026-05-10", "cli-amendment-2026-05-10", "audit-amendment-2026-05-08"}
	for i, a := range all {
		if a.Stem != wantOrder[i] {
			t.Errorf("position %d: want %q, got %q", i, wantOrder[i], a.Stem)
		}
	}

	proposedOnly, err := List(root, ListOptions{State: StateProposed})
	if err != nil {
		t.Fatalf("List(proposed): %v", err)
	}
	if len(proposedOnly) != 2 {
		t.Fatalf("want 2 proposed, got %d", len(proposedOnly))
	}
	for _, a := range proposedOnly {
		if a.State != StateProposed {
			t.Errorf("got %q in proposed result", a.State)
		}
	}

	auditOnly, err := List(root, ListOptions{For: "audit"})
	if err != nil {
		t.Fatalf("List(for=audit): %v", err)
	}
	if len(auditOnly) != 2 {
		t.Fatalf("want 2 for audit, got %d", len(auditOnly))
	}
	for _, a := range auditOnly {
		if a.AmendmentFor != "audit" {
			t.Errorf("got AmendmentFor=%q in for=audit result", a.AmendmentFor)
		}
	}
}

func TestLoadProposedThenAccepted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "in proposed")
	got, err := Load(root, "cli-amendment-2026-05-10")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.State != StateProposed {
		t.Fatalf("want proposed, got %q", got.State)
	}
	if got.AmendmentFor != "cli" {
		t.Errorf("AmendmentFor: %q", got.AmendmentFor)
	}
}

func TestLoadMissing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, err := Load(root, "nope")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
}

func TestAcceptHappyPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := writeAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "moves")

	res, err := Accept(root, "cli-amendment-2026-05-10")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if res.AmendmentFor != "cli" || res.AmendmentDate != "2026-05-10" {
		t.Errorf("unexpected metadata: %+v", res)
	}
	if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source still exists: %v", err)
	}
	dst := filepath.Join(AcceptedDir(root), "cli-amendment-2026-05-10.yaml")
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dst missing: %v", err)
	}
	body, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !strings.Contains(string(body), "state: accepted") {
		t.Errorf("state not rewritten: %s", body)
	}
	if strings.Contains(string(body), "state: proposed") {
		t.Errorf("proposed line still present: %s", body)
	}
}

func TestAcceptIdempotentGuard(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "accepted", "already done")

	_, err := Accept(root, "cli-amendment-2026-05-10")
	if err == nil {
		t.Fatal("Accept should refuse on already-accepted amendment")
	}
	if !strings.Contains(err.Error(), "already accepted") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAcceptRefusesOverwrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAmendment(t, root, "dup", "x", "2026-05-10", "proposed", "p")
	writeAmendment(t, root, "dup", "x", "2026-05-10", "accepted", "a")

	_, err := Accept(root, "dup")
	if err == nil {
		t.Fatal("Accept should refuse to overwrite existing destination")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRejectHappyPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src := writeAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "proposed", "rejecting")

	res, err := Reject(root, "cli-amendment-2026-05-10")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if res.AmendmentFor != "cli" {
		t.Errorf("AmendmentFor: %q", res.AmendmentFor)
	}
	if _, err := os.Stat(src); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source still exists: %v", err)
	}
}

func TestRejectRefusesAccepted(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAmendment(t, root, "cli-amendment-2026-05-10", "cli", "2026-05-10", "accepted", "already in")

	_, err := Reject(root, "cli-amendment-2026-05-10")
	if err == nil {
		t.Fatal("Reject should refuse on accepted amendment")
	}
	if !strings.Contains(err.Error(), "accepted") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIsValidStem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stem string
		ok   bool
	}{
		{"cli-amendment-2026-05-10", true},
		{"cli-amendment-2026-05-10-output-formatting", true},
		{"identity-and-trust-amendment-2026-05-10", true},
		{"missing-date", false},
		{"cli-amendment-2026-13-99", true}, // regex doesn't validate calendar
		{"cli-amendment-2026-5-10", false}, // requires zero-pad
		{"CLI-amendment-2026-05-10", false},
	}
	for _, c := range cases {
		if got := IsValidStem(c.stem); got != c.ok {
			t.Errorf("IsValidStem(%q) = %v, want %v", c.stem, got, c.ok)
		}
	}
}

func TestRewriteStateAddsWhenMissing(t *testing.T) {
	t.Parallel()
	body := []byte("amendment_for: foo\namendment_date: 2026-05-10\nsummary: x\n")
	out := rewriteState(body, StateAccepted)
	if !strings.Contains(string(out), "state: accepted") {
		t.Errorf("state line not appended: %s", out)
	}
}
