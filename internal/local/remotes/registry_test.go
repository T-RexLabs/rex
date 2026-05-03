package remotes

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsValidName(t *testing.T) {
	t.Parallel()

	good := []string{"primary", "alpha-2", "team-rex", "x"}
	for _, s := range good {
		if !IsValidName(s) {
			t.Errorf("IsValidName(%q) should be true", s)
		}
	}
	bad := []string{"", "Primary", "1main", "alpha-", "-alpha", "alpha--beta", "snake_case"}
	for _, s := range bad {
		if IsValidName(s) {
			t.Errorf("IsValidName(%q) should be false", s)
		}
	}
}

func TestLoadMissingPathReturnsEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "remotes.toml")
	r, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(r.Remotes) != 0 {
		t.Fatalf("expected empty registry, got %v", r.Remotes)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "remotes.toml")

	original := &Registry{Remotes: map[string]Remote{
		"primary": {
			Name:        "primary",
			URL:         "http://127.0.0.1:8080",
			Fingerprint: "abc1234567890def",
			AddedAt:     time.Date(2026, 5, 4, 1, 0, 0, 0, time.UTC),
			LastSeen:    time.Date(2026, 5, 4, 2, 0, 0, 0, time.UTC),
		},
		"team": {
			Name:    "team",
			URL:     "https://central.example.com",
			AddedAt: time.Date(2026, 5, 4, 1, 30, 0, 0, time.UTC),
		},
	}}
	if err := Save(path, original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for name, want := range original.Remotes {
		got, ok := loaded.Remotes[name]
		if !ok {
			t.Fatalf("remote %q missing after reload", name)
		}
		if got.URL != want.URL {
			t.Fatalf("%q url: got %q want %q", name, got.URL, want.URL)
		}
		if got.Fingerprint != want.Fingerprint {
			t.Fatalf("%q fingerprint: got %q want %q", name, got.Fingerprint, want.Fingerprint)
		}
		if !got.AddedAt.Equal(want.AddedAt) {
			t.Fatalf("%q added_at: got %v want %v", name, got.AddedAt, want.AddedAt)
		}
		if !got.LastSeen.Equal(want.LastSeen) {
			t.Fatalf("%q last_seen: got %v want %v", name, got.LastSeen, want.LastSeen)
		}
	}
}

func TestSaveProducesDeterministicOutput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "remotes.toml")
	r := &Registry{Remotes: map[string]Remote{
		"zeta":  {Name: "zeta", URL: "https://z", AddedAt: time.Unix(1, 0).UTC()},
		"alpha": {Name: "alpha", URL: "https://a", AddedAt: time.Unix(2, 0).UTC()},
	}}
	if err := Save(path, r); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body, _ := readFile(t, path)
	// alpha must come before zeta in the file.
	if i, j := strings.Index(body, "[alpha]"), strings.Index(body, "[zeta]"); i < 0 || j < 0 || i > j {
		t.Fatalf("section order: alpha=%d zeta=%d in:\n%s", i, j, body)
	}
}

func TestAddRejectsDuplicate(t *testing.T) {
	t.Parallel()

	r := &Registry{Remotes: map[string]Remote{}}
	if err := r.Add(Remote{Name: "primary", URL: "http://x"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := r.Add(Remote{Name: "primary", URL: "http://y"}); err == nil {
		t.Fatal("duplicate Add should error")
	}
}

func TestAddRejectsBadName(t *testing.T) {
	t.Parallel()

	r := &Registry{Remotes: map[string]Remote{}}
	if err := r.Add(Remote{Name: "Bad Name", URL: "http://x"}); err == nil {
		t.Fatal("bad name should error")
	}
	if err := r.Add(Remote{Name: "valid", URL: ""}); err == nil {
		t.Fatal("missing URL should error")
	}
}

func TestSetIsUpsert(t *testing.T) {
	t.Parallel()

	r := &Registry{Remotes: map[string]Remote{}}
	if err := r.Set(Remote{Name: "primary", URL: "http://x"}); err != nil {
		t.Fatalf("first Set: %v", err)
	}
	if err := r.Set(Remote{Name: "primary", URL: "http://y", Fingerprint: "abc1234567890def"}); err != nil {
		t.Fatalf("second Set: %v", err)
	}
	got, _ := r.Get("primary")
	if got.URL != "http://y" || got.Fingerprint != "abc1234567890def" {
		t.Fatalf("upsert: %+v", got)
	}
}

func TestRemoveAndGet(t *testing.T) {
	t.Parallel()

	r := &Registry{Remotes: map[string]Remote{}}
	if err := r.Add(Remote{Name: "primary", URL: "http://x"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok := r.Get("primary"); !ok {
		t.Fatal("Get after Add should return true")
	}
	if err := r.Remove("primary"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok := r.Get("primary"); ok {
		t.Fatal("Get after Remove should return false")
	}
	if err := r.Remove("primary"); err == nil {
		t.Fatal("second Remove should error")
	}
}

func TestList(t *testing.T) {
	t.Parallel()

	r := &Registry{Remotes: map[string]Remote{}}
	for _, name := range []string{"zeta", "alpha", "mid"} {
		_ = r.Add(Remote{Name: name, URL: "http://" + name})
	}
	got := r.List()
	if len(got) != 3 || got[0].Name != "alpha" || got[1].Name != "mid" || got[2].Name != "zeta" {
		t.Fatalf("List: %+v", got)
	}
}

func TestDefaultPath(t *testing.T) {
	t.Parallel()

	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("DefaultPath not absolute: %q", got)
	}
	if !strings.HasSuffix(got, FileName) {
		t.Fatalf("DefaultPath should end in %q: %q", FileName, got)
	}
}

func readFile(t *testing.T, path string) (string, error) {
	t.Helper()
	body, err := readBytes(path)
	return string(body), err
}
