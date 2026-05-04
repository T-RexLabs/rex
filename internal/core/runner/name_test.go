package runner

import (
	"strings"
	"testing"
)

func TestFriendlyNameDeterministic(t *testing.T) {
	t.Parallel()
	a := FriendlyName("1777924237683504000.0")
	b := FriendlyName("1777924237683504000.0")
	if a != b {
		t.Errorf("expected same name twice, got %q and %q", a, b)
	}
}

func TestFriendlyNameVariesByInput(t *testing.T) {
	t.Parallel()
	a := FriendlyName("1777924237683504000.0")
	b := FriendlyName("1777924237683504001.0")
	if a == b {
		t.Errorf("expected different names for different ids: both %q", a)
	}
}

func TestFriendlyNameShape(t *testing.T) {
	t.Parallel()
	got := FriendlyName("anything")
	if !strings.Contains(got, "-") {
		t.Errorf("expected hyphenated, got %q", got)
	}
	parts := strings.Split(got, "-")
	if len(parts) != 2 {
		t.Errorf("expected exactly two tokens, got %v", parts)
	}
	for _, p := range parts {
		if p == "" {
			t.Errorf("empty token in %q", got)
		}
	}
}

func TestFriendlyNameEmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	if got := FriendlyName(""); got != "" {
		t.Errorf("expected empty for empty input, got %q", got)
	}
}

func TestIsFriendlyName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"brave-otter", true},
		{"calm-deer", true},
		{"1777924237683504000.0", false},
		{"", false},
		{"-otter", false},
		{"brave-", false},
		{"brave_otter", false},
		{"Brave-otter", false},
	}
	for _, c := range cases {
		if got := IsFriendlyName(c.in); got != c.want {
			t.Errorf("IsFriendlyName(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
