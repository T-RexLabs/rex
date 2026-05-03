package eventlog

import (
	"encoding/json"
	"testing"
	"time"
)

// fakeClock returns successive Times from a slice; once exhausted it
// repeats the last entry. Lets tests drive HLC behaviour deterministically
// without touching the real wall clock (overview.ENG.4).
type fakeClock struct {
	times []time.Time
	i     int
}

func newFakeClock(times ...time.Time) *fakeClock { return &fakeClock{times: times} }

func (f *fakeClock) now() time.Time {
	if f.i >= len(f.times) {
		return f.times[len(f.times)-1]
	}
	t := f.times[f.i]
	f.i++
	return t
}

func TestHLCStringRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []HLC{
		{Wall: 0, Logical: 0},
		{Wall: 1700000000_000000000, Logical: 0},
		{Wall: 1700000000_000000001, Logical: 42},
		{Wall: -1, Logical: 7},
	}
	for _, want := range cases {
		got, err := ParseHLC(want.String())
		if err != nil {
			t.Fatalf("ParseHLC(%q): %v", want.String(), err)
		}
		if got != want {
			t.Fatalf("round-trip: got %+v want %+v", got, want)
		}
	}
}

func TestHLCJSONRoundTrip(t *testing.T) {
	t.Parallel()

	want := HLC{Wall: 1700000000_000000000, Logical: 3}
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != `"1700000000000000000.3"` {
		t.Fatalf("unexpected encoding: %s", encoded)
	}
	var got HLC
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip: got %+v want %+v", got, want)
	}
}

func TestParseHLCRejectsGarbage(t *testing.T) {
	t.Parallel()

	bad := []string{"", "no-dot", "abc.def", "1.abc", "abc.1"}
	for _, s := range bad {
		if _, err := ParseHLC(s); err == nil {
			t.Fatalf("ParseHLC(%q): want error, got nil", s)
		}
	}
}

func TestClockNowMonotonic(t *testing.T) {
	t.Parallel()

	base := time.Unix(1700000000, 0)
	fc := newFakeClock(base, base, base, base.Add(time.Nanosecond))
	clock := NewClockWithSource(fc.now)

	a := clock.Now()
	b := clock.Now()
	c := clock.Now()
	d := clock.Now()

	for i, pair := range [][2]HLC{{a, b}, {b, c}, {c, d}} {
		if !pair[0].Less(pair[1]) {
			t.Fatalf("pair %d not strictly increasing: %+v then %+v", i, pair[0], pair[1])
		}
	}

	if a.Wall != b.Wall || b.Wall != c.Wall {
		t.Fatalf("wall should stick: %+v %+v %+v", a, b, c)
	}
	if a.Logical != 0 || b.Logical != 1 || c.Logical != 2 {
		t.Fatalf("logical should advance 0,1,2: got %d %d %d", a.Logical, b.Logical, c.Logical)
	}
	if d.Wall <= c.Wall {
		t.Fatalf("d should advance wall: %+v vs %+v", c, d)
	}
	if d.Logical != 0 {
		t.Fatalf("d should reset logical to 0: got %d", d.Logical)
	}
}

func TestClockNowSurvivesBackwardsWall(t *testing.T) {
	t.Parallel()

	t0 := time.Unix(1700000000, 0)
	tBack := t0.Add(-time.Second)
	fc := newFakeClock(t0, tBack, tBack)
	clock := NewClockWithSource(fc.now)

	a := clock.Now()
	b := clock.Now()
	c := clock.Now()

	if !a.Less(b) || !b.Less(c) {
		t.Fatalf("monotonicity broken on backwards wall: %+v %+v %+v", a, b, c)
	}
	if b.Wall != a.Wall || c.Wall != a.Wall {
		t.Fatalf("wall should stick at the larger value: %+v %+v %+v", a, b, c)
	}
}

func TestClockUpdateAdvancesPastRemote(t *testing.T) {
	t.Parallel()

	local := time.Unix(1700000000, 0)
	fc := newFakeClock(local, local)
	clock := NewClockWithSource(fc.now)

	first := clock.Now()
	remote := HLC{Wall: first.Wall + 1_000_000, Logical: 99}

	merged := clock.Update(remote)
	if !remote.Less(merged) || !first.Less(merged) {
		t.Fatalf("merged HLC must dominate both: first=%+v remote=%+v merged=%+v", first, remote, merged)
	}

	next := clock.Now()
	if !merged.Less(next) {
		t.Fatalf("Now after Update must advance: merged=%+v next=%+v", merged, next)
	}
}
