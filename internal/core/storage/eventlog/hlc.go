package eventlog

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HLC is a hybrid logical clock value: a wall-clock time (Unix nanoseconds)
// fused with a monotonically-increasing logical counter.
//
// The wall component lets two nodes order events by approximate real time
// even with skewed clocks; the logical counter breaks ties when many
// events fall within the same wall instant or when wall time goes
// backwards. See storage.EVENTS.3 for the role HLC plays in cross-node
// merge.
//
// HLC values from a single Clock are strictly monotonic: every Now() call
// returns a value greater than every prior Now() from the same Clock.
type HLC struct {
	// Wall is Unix nanoseconds at the time the HLC was minted.
	Wall int64 `json:"wall"`
	// Logical is the per-wall-instant monotonic counter.
	Logical uint32 `json:"logical"`
}

// Less reports whether a is strictly before b in HLC order. Wall time
// dominates; Logical is the tiebreaker. This does NOT include node-id
// tiebreaking — that lives in the sync layer (sync.yaml) where the
// central node's view is authoritative.
func (a HLC) Less(b HLC) bool {
	if a.Wall != b.Wall {
		return a.Wall < b.Wall
	}
	return a.Logical < b.Logical
}

// String renders an HLC as "<wall-nanos>.<logical>" — sortable as a
// string only when Wall widths are equal, so do not rely on lexical sort
// for ordering. Use Less for comparisons.
func (h HLC) String() string {
	return fmt.Sprintf("%d.%d", h.Wall, h.Logical)
}

// ParseHLC parses the format produced by HLC.String.
func ParseHLC(s string) (HLC, error) {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return HLC{}, fmt.Errorf("hlc: missing '.' in %q", s)
	}
	wall, err := strconv.ParseInt(s[:dot], 10, 64)
	if err != nil {
		return HLC{}, fmt.Errorf("hlc: parse wall: %w", err)
	}
	logical, err := strconv.ParseUint(s[dot+1:], 10, 32)
	if err != nil {
		return HLC{}, fmt.Errorf("hlc: parse logical: %w", err)
	}
	return HLC{Wall: wall, Logical: uint32(logical)}, nil
}

// MarshalJSON / UnmarshalJSON encode HLC as the canonical string form so
// the on-disk representation matches what sync messages and audit log
// rows show to humans.
func (h HLC) MarshalJSON() ([]byte, error) {
	return json.Marshal(h.String())
}

// UnmarshalJSON decodes the canonical string form.
func (h *HLC) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := ParseHLC(s)
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}

// Clock issues monotonically increasing HLCs. The wall-clock source is
// injectable so executor, audit, and sync tests can be deterministic
// (overview.ENG.4).
//
// A zero Clock is not usable; use NewClock.
type Clock struct {
	now func() time.Time

	mu   sync.Mutex
	last HLC
}

// NewClock returns a Clock backed by time.Now.
func NewClock() *Clock {
	return NewClockWithSource(time.Now)
}

// NewClockWithSource returns a Clock that pulls wall time from now. The
// source is invoked under a mutex; it should be cheap and must never
// return the zero time.
func NewClockWithSource(now func() time.Time) *Clock {
	return &Clock{now: now}
}

// Now returns the next HLC. The Wall field is the larger of the source's
// reading and the previous Wall; if they match, Logical advances by one.
// This guarantees strict monotonicity even when the source goes backward
// (NTP correction, suspend/resume).
func (c *Clock) Now() HLC {
	c.mu.Lock()
	defer c.mu.Unlock()

	wall := c.now().UnixNano()
	switch {
	case wall > c.last.Wall:
		c.last = HLC{Wall: wall, Logical: 0}
	default:
		c.last.Logical++
	}
	return c.last
}

// Update folds an externally-observed HLC (e.g. from a sync peer) into
// this clock so future Now() values are strictly greater than both the
// local last and the observed remote. Returns the next local HLC after
// the merge — what the caller would write for the event that triggered
// the update.
//
// Implementation follows the canonical HLC receive rule (Kulkarni et al.,
// 2014): take the max of the three walls, then derive the logical
// counter from which one(s) won.
func (c *Clock) Update(remote HLC) HLC {
	c.mu.Lock()
	defer c.mu.Unlock()

	wall := c.now().UnixNano()
	maxWall := wall
	if c.last.Wall > maxWall {
		maxWall = c.last.Wall
	}
	if remote.Wall > maxWall {
		maxWall = remote.Wall
	}

	var logical uint32
	switch {
	case maxWall == c.last.Wall && maxWall == remote.Wall:
		logical = c.last.Logical
		if remote.Logical > logical {
			logical = remote.Logical
		}
		logical++
	case maxWall == c.last.Wall:
		logical = c.last.Logical + 1
	case maxWall == remote.Wall:
		logical = remote.Logical + 1
	default: // physical wall dominates
		logical = 0
	}

	c.last = HLC{Wall: maxWall, Logical: logical}
	return c.last
}
