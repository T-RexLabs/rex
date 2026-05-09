// Package merge3 is the line-based three-way merge engine used by
// the rebase pipeline (sync.GIT.1, sync.GIT.2). Given a base, a
// local, and a remote version of a text entity, it returns either
// merged content or a structured conflict description.
//
// The algorithm follows the standard diff3 shape:
//
//  1. Split each input into lines.
//
//  2. Compute LCS-derived matches: for each base line, the local
//     index it matches (or -1) and the remote index (or -1).
//
//  3. Walk base, splitting it into "runs" where each run is a maximal
//     contiguous range of base indices sharing the same per-side
//     status. The four statuses are: both kept, only-local kept,
//     only-remote kept, both modified.
//
//  4. For each run determine the local slice and remote slice that
//     map to it, then resolve:
//
//     - both kept           → emit base lines verbatim
//     - only one kept       → emit the OTHER side's slice (it was
//     modified by the side that didn't keep)
//     - both modified, equal slices → emit either
//     - both modified, different    → CONFLICT (markers + sidecar
//     entry)
//
// The implementation is deliberately small and pure: no I/O, no
// external state, no goroutines. All inputs are []byte; the caller
// owns reading from disk and writing the merged output back.
package merge3

import (
	"bytes"
	"strings"
)

// Markers emitted into the merged output for conflict regions
// (RCS-compatible so editors and CI tools recognise them).
const (
	MarkerLocal     = "<<<<<<< local"
	MarkerSeparator = "======="
	MarkerRemote    = ">>>>>>> remote"
)

// ConflictHunk records one unresolvable region for the conflict
// sidecar (sync.GIT.3). Line numbers are 1-based, exclusive on End,
// and refer to the corresponding input.
type ConflictHunk struct {
	BaseStart   int
	BaseEnd     int
	LocalStart  int
	LocalEnd    int
	RemoteStart int
	RemoteEnd   int
	BaseLines   []string
	LocalLines  []string
	RemoteLines []string
}

// Result is the outcome of a 3-way merge.
//
// When Conflicts is empty, Merged contains a clean merge ready to
// write to disk. When Conflicts is non-empty, Merged contains the
// conflict-markered content (also suitable to write so the user can
// edit it) and Conflicts records each unresolved region for the
// sidecar.
type Result struct {
	Merged    []byte
	Conflicts []ConflictHunk
}

// Clean reports whether the merge produced no conflicts.
func (r Result) Clean() bool { return len(r.Conflicts) == 0 }

// runStatus categorises one run of base indices by which sides kept
// the base lines in that run unchanged.
type runStatus int

const (
	statusBothKept runStatus = iota
	statusOnlyLocalKept
	statusOnlyRemoteKept
	statusBothModified
)

type run struct {
	baseStart, baseEnd int // [start, end) on base
	status             runStatus
}

// Merge performs a 3-way line-based merge.
//
// Trailing-newline policy: if any of base/local/remote ended with
// a newline, the output ends with a newline; otherwise it does not.
// (Editors generally prefer text files to end with a newline, so the
// "any" rule is the safer default.)
func Merge(base, local, remote []byte) Result {
	baseLines := splitLines(base)
	localLines := splitLines(local)
	remoteLines := splitLines(remote)

	// Cheap shortcuts for the common cases.
	switch {
	case bytes.Equal(local, base):
		return Result{Merged: append([]byte(nil), remote...)}
	case bytes.Equal(remote, base):
		return Result{Merged: append([]byte(nil), local...)}
	case bytes.Equal(local, remote):
		return Result{Merged: append([]byte(nil), local...)}
	}

	localMatch := lcsMatch(baseLines, localLines)
	remoteMatch := lcsMatch(baseLines, remoteLines)

	runs := groupIntoRuns(baseLines, localMatch, remoteMatch)

	var (
		out       bytes.Buffer
		conflicts []ConflictHunk
	)
	li, ri := 0, 0
	for idx, r := range runs {
		le, re := boundsForRun(idx, runs, localMatch, remoteMatch, len(localLines), len(remoteLines))

		baseSlice := baseLines[r.baseStart:r.baseEnd]
		localSlice := localLines[li:le]
		remoteSlice := remoteLines[ri:re]

		emitRun(&out, &conflicts, baseSlice, localSlice, remoteSlice, r, li, ri)

		li = le
		ri = re
	}

	// Anything past the last base-anchored run is a pure trailing
	// insertion on one or both sides. Treat it as a final unstable
	// region with no base context.
	if li < len(localLines) || ri < len(remoteLines) {
		emitRun(&out, &conflicts,
			nil,
			localLines[li:],
			remoteLines[ri:],
			run{baseStart: len(baseLines), baseEnd: len(baseLines), status: statusBothModified},
			li, ri)
	}

	merged := out.Bytes()
	merged = preserveTrailingNewline(merged, base, local, remote)
	return Result{Merged: merged, Conflicts: conflicts}
}

// groupIntoRuns walks base and produces a slice of runs whose per-
// index status is constant within each run. Empty base produces an
// empty slice.
func groupIntoRuns(baseLines []string, localMatch, remoteMatch []int) []run {
	if len(baseLines) == 0 {
		return nil
	}
	var runs []run
	curStart := 0
	curStatus := statusFor(0, localMatch, remoteMatch)
	for bi := 1; bi <= len(baseLines); bi++ {
		var nextStatus runStatus
		if bi == len(baseLines) {
			// Sentinel: force closing the last run.
			runs = append(runs, run{baseStart: curStart, baseEnd: bi, status: curStatus})
			break
		}
		nextStatus = statusFor(bi, localMatch, remoteMatch)
		if nextStatus != curStatus {
			runs = append(runs, run{baseStart: curStart, baseEnd: bi, status: curStatus})
			curStart = bi
			curStatus = nextStatus
		}
	}
	return runs
}

func statusFor(bi int, localMatch, remoteMatch []int) runStatus {
	lkept := localMatch[bi] >= 0
	rkept := remoteMatch[bi] >= 0
	switch {
	case lkept && rkept:
		return statusBothKept
	case lkept:
		return statusOnlyLocalKept
	case rkept:
		return statusOnlyRemoteKept
	default:
		return statusBothModified
	}
}

// boundsForRun computes the [li, le) and [ri, re) on the local and
// remote sides that correspond to the given run. The lo end (li, ri)
// is implicit (continues from the previous run); this returns le and
// re only.
//
// For runs where a side kept the base lines, the side's slice is
// determined by its match indices in this run. For runs where a side
// did NOT keep, the slice extends until the side's NEXT stable index
// in any subsequent run, falling back to len(side) when no future
// stable match exists.
func boundsForRun(idx int, runs []run, localMatch, remoteMatch []int, localLen, remoteLen int) (int, int) {
	r := runs[idx]
	var le, re int

	if r.status == statusBothKept || r.status == statusOnlyLocalKept {
		// Local kept; use the highest local index matched within
		// this run + 1 to capture any local insertions between
		// matches.
		le = localMatch[r.baseEnd-1] + 1
	} else {
		le = nextLocalAnchor(idx, runs, localMatch, localLen)
	}

	if r.status == statusBothKept || r.status == statusOnlyRemoteKept {
		re = remoteMatch[r.baseEnd-1] + 1
	} else {
		re = nextRemoteAnchor(idx, runs, remoteMatch, remoteLen)
	}
	return le, re
}

// nextLocalAnchor returns the local index of the next local-stable
// base position after run idx, or localLen if none exists. Used when
// the current run is locally unstable so we don't know its le from
// match indices.
func nextLocalAnchor(idx int, runs []run, localMatch []int, localLen int) int {
	for k := idx + 1; k < len(runs); k++ {
		rk := runs[k]
		if rk.status == statusBothKept || rk.status == statusOnlyLocalKept {
			for bi := rk.baseStart; bi < rk.baseEnd; bi++ {
				if localMatch[bi] >= 0 {
					return localMatch[bi]
				}
			}
		}
	}
	return localLen
}

func nextRemoteAnchor(idx int, runs []run, remoteMatch []int, remoteLen int) int {
	for k := idx + 1; k < len(runs); k++ {
		rk := runs[k]
		if rk.status == statusBothKept || rk.status == statusOnlyRemoteKept {
			for bi := rk.baseStart; bi < rk.baseEnd; bi++ {
				if remoteMatch[bi] >= 0 {
					return remoteMatch[bi]
				}
			}
		}
	}
	return remoteLen
}

// emitRun appends merged output for one run plus records any conflict
// in the conflicts slice. li and ri are the local/remote starting
// indices for the run, used for conflict-hunk line numbers.
func emitRun(out *bytes.Buffer, conflicts *[]ConflictHunk, baseSlice, localSlice, remoteSlice []string, r run, li, ri int) {
	switch r.status {
	case statusBothKept:
		// Both sides kept the base lines verbatim. Local insertions
		// and remote insertions, if any, live before the next run's
		// processing — but since both kept, there can't be any here
		// (consecutive matches would have absorbed them).
		writeLines(out, baseSlice)
	case statusOnlyLocalKept:
		// Local kept; remote modified. The remote slice is what
		// remote has in place of the base lines.
		writeLines(out, remoteSlice)
	case statusOnlyRemoteKept:
		writeLines(out, localSlice)
	case statusBothModified:
		switch {
		case linesEqual(baseSlice, localSlice):
			writeLines(out, remoteSlice)
		case linesEqual(baseSlice, remoteSlice):
			writeLines(out, localSlice)
		case linesEqual(localSlice, remoteSlice):
			writeLines(out, localSlice)
		default:
			writeLines(out, []string{MarkerLocal})
			writeLines(out, localSlice)
			writeLines(out, []string{MarkerSeparator})
			writeLines(out, remoteSlice)
			writeLines(out, []string{MarkerRemote})
			*conflicts = append(*conflicts, ConflictHunk{
				BaseStart:   r.baseStart + 1,
				BaseEnd:     r.baseEnd + 1,
				LocalStart:  li + 1,
				LocalEnd:    li + len(localSlice) + 1,
				RemoteStart: ri + 1,
				RemoteEnd:   ri + len(remoteSlice) + 1,
				BaseLines:   append([]string(nil), baseSlice...),
				LocalLines:  append([]string(nil), localSlice...),
				RemoteLines: append([]string(nil), remoteSlice...),
			})
		}
	}
}

// lcsMatch returns a slice of length len(a) where entry i is the
// index in b that a[i] matches under the chosen LCS, or -1 when a[i]
// is not part of the LCS. Standard O(n*m) DP — sufficient for the
// YAML/TOML payload sizes Rex sees.
func lcsMatch(a, b []string) []int {
	n, m := len(a), len(b)
	match := make([]int, n)
	for i := range match {
		match[i] = -1
	}
	if n == 0 || m == 0 {
		return match
	}

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 0; i < n; i++ {
		for j := 0; j < m; j++ {
			if a[i] == b[j] {
				dp[i+1][j+1] = dp[i][j] + 1
			} else if dp[i][j+1] >= dp[i+1][j] {
				dp[i+1][j+1] = dp[i][j+1]
			} else {
				dp[i+1][j+1] = dp[i+1][j]
			}
		}
	}

	i, j := n, m
	for i > 0 && j > 0 {
		switch {
		case a[i-1] == b[j-1]:
			match[i-1] = j - 1
			i--
			j--
		case dp[i-1][j] >= dp[i][j-1]:
			i--
		default:
			j--
		}
	}
	return match
}

// splitLines returns lines without trailing newlines. An empty input
// returns an empty slice; a trailing newline does NOT produce an
// extra empty line.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	trimmed := strings.TrimSuffix(s, "\n")
	if trimmed == "" {
		return []string{""}
	}
	return strings.Split(trimmed, "\n")
}

func linesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeLines(buf *bytes.Buffer, lines []string) {
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
}

// preserveTrailingNewline strips the trailing newline from merged
// when none of base/local/remote ended with one.
func preserveTrailingNewline(merged, base, local, remote []byte) []byte {
	anyNL := endsWithNewline(base) || endsWithNewline(local) || endsWithNewline(remote)
	if !anyNL && len(merged) > 0 && merged[len(merged)-1] == '\n' {
		return merged[:len(merged)-1]
	}
	return merged
}

func endsWithNewline(b []byte) bool {
	return len(b) > 0 && b[len(b)-1] == '\n'
}
