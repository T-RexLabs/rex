package runner

import (
	"strings"
	"testing"
	"time"
)

func TestDAGValidateRejectsDuplicates(t *testing.T) {
	t.Parallel()

	d := DAG{
		Nodes: []Node{
			{ID: "a", Type: "noop"},
			{ID: "a", Type: "noop"},
		},
	}
	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "duplicate node") {
		t.Fatalf("Validate: got %v want duplicate node error", err)
	}
}

func TestDAGValidateRejectsEmptyNode(t *testing.T) {
	t.Parallel()

	d := DAG{Nodes: []Node{{ID: "", Type: "noop"}}}
	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "empty ID") {
		t.Fatalf("Validate: got %v want empty ID error", err)
	}
}

func TestDAGValidateRejectsUnknownEdges(t *testing.T) {
	t.Parallel()

	d := DAG{
		Nodes: []Node{{ID: "a", Type: "noop"}},
		Edges: []Edge{{From: "a", To: "b"}},
	}
	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), `"b"`) {
		t.Fatalf("Validate: got %v want unknown To error", err)
	}
}

func TestDAGValidateDetectsCycle(t *testing.T) {
	t.Parallel()

	d := DAG{
		Nodes: []Node{
			{ID: "a", Type: "noop"},
			{ID: "b", Type: "noop"},
			{ID: "c", Type: "noop"},
		},
		Edges: []Edge{
			{From: "a", To: "b"},
			{From: "b", To: "c"},
			{From: "c", To: "a"},
		},
	}
	err := d.Validate()
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Validate: got %v want cycle error", err)
	}
}

func TestDAGValidateAcceptsValid(t *testing.T) {
	t.Parallel()

	d := DAG{
		Nodes: []Node{
			{ID: "a", Type: "noop"},
			{ID: "b", Type: "noop"},
			{ID: "c", Type: "noop"},
		},
		Edges: []Edge{
			{From: "a", To: "b"},
			{From: "a", To: "c"},
		},
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("Validate on valid DAG: %v", err)
	}
}

func TestDAGRoots(t *testing.T) {
	t.Parallel()

	d := DAG{
		Nodes: []Node{
			{ID: "a", Type: "noop"},
			{ID: "b", Type: "noop"},
			{ID: "c", Type: "noop"},
		},
		Edges: []Edge{
			{From: "a", To: "c"},
			{From: "b", To: "c"},
		},
	}
	roots := d.roots()
	got := map[NodeID]bool{}
	for _, r := range roots {
		got[r] = true
	}
	if !got["a"] || !got["b"] || got["c"] {
		t.Fatalf("roots: got %v want {a,b}", roots)
	}
}

func TestRetryPolicyEffective(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		node RetryPolicy
		dag  RetryPolicy
		want RetryPolicy
	}{
		{
			name: "all defaults",
			want: DefaultRetryPolicy,
		},
		{
			name: "node overrides",
			node: RetryPolicy{MaxAttempts: 5, Backoff: 2 * time.Second},
			dag:  RetryPolicy{MaxAttempts: 7, Backoff: time.Minute},
			want: RetryPolicy{MaxAttempts: 5, Backoff: 2 * time.Second},
		},
		{
			name: "dag overrides default",
			dag:  RetryPolicy{MaxAttempts: 7, Backoff: time.Minute},
			want: RetryPolicy{MaxAttempts: 7, Backoff: time.Minute},
		},
		{
			name: "partial node overlay",
			node: RetryPolicy{MaxAttempts: 4},
			dag:  RetryPolicy{MaxAttempts: 7, Backoff: time.Minute},
			want: RetryPolicy{MaxAttempts: 4, Backoff: time.Minute},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.node.Effective(tc.dag)
			if got != tc.want {
				t.Fatalf("Effective: got %+v want %+v", got, tc.want)
			}
		})
	}
}
