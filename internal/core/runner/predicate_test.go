package runner

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParsePredicateEmptyDefaultsToAlways(t *testing.T) {
	t.Parallel()
	p, err := ParsePredicate("")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.Kind != PredicateKindAlways {
		t.Fatalf("kind: %q", p.Kind)
	}
}

func TestParsePredicateRejectsUnknownKind(t *testing.T) {
	t.Parallel()
	_, err := ParsePredicate(`{"kind":"weird"}`)
	if err == nil || !strings.Contains(err.Error(), "unknown predicate kind") {
		t.Fatalf("err: %v", err)
	}
}

func TestParsePredicatePathEqRequiresPath(t *testing.T) {
	t.Parallel()
	_, err := ParsePredicate(`{"kind":"path_eq"}`)
	if err == nil || !strings.Contains(err.Error(), "requires path") {
		t.Fatalf("err: %v", err)
	}
}

func TestParsePredicatePathMatchRequiresRegex(t *testing.T) {
	t.Parallel()
	_, err := ParsePredicate(`{"kind":"path_match","path":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "requires regex") {
		t.Fatalf("err: %v", err)
	}
	_, err = ParsePredicate(`{"kind":"path_match","path":"x","regex":"["}`)
	if err == nil || !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("err: %v", err)
	}
}

func TestPredicateAlwaysAndNever(t *testing.T) {
	t.Parallel()
	always, _ := ParsePredicate(`{"kind":"always"}`)
	never, _ := ParsePredicate(`{"kind":"never"}`)
	if got, _ := always.Evaluate(nil); !got {
		t.Fatal("always should be true")
	}
	if got, _ := never.Evaluate(nil); got {
		t.Fatal("never should be false")
	}
}

func TestPredicateExitCodeEq(t *testing.T) {
	t.Parallel()
	p, _ := ParsePredicate(`{"kind":"exit_code_eq","value":0}`)
	cases := map[string]bool{
		`{"exit_code":0}`:  true,
		`{"exit_code":1}`:  false,
		`{"exit_code":42}`: false,
		`{}`:               false, // missing path → false (not error)
	}
	for payload, want := range cases {
		got, err := p.Evaluate(json.RawMessage(payload))
		if err != nil {
			t.Fatalf("evaluate %q: %v", payload, err)
		}
		if got != want {
			t.Errorf("evaluate %q = %v, want %v", payload, got, want)
		}
	}
}

func TestPredicatePathEq(t *testing.T) {
	t.Parallel()
	p, _ := ParsePredicate(`{"kind":"path_eq","path":"status","value":"ok"}`)
	cases := map[string]bool{
		`{"status":"ok"}`:            true,
		`{"status":"err"}`:           false,
		`{"status":"OK"}`:            false, // case-sensitive
		`{"other":"ok"}`:             false,
		`{"status":1}`:               false,
		`{"nested":{"status":"ok"}}`: false, // not at root path
	}
	for payload, want := range cases {
		got, err := p.Evaluate(json.RawMessage(payload))
		if err != nil {
			t.Fatalf("evaluate %q: %v", payload, err)
		}
		if got != want {
			t.Errorf("evaluate %q = %v, want %v", payload, got, want)
		}
	}
}

func TestPredicatePathEqDottedPath(t *testing.T) {
	t.Parallel()
	p, _ := ParsePredicate(`{"kind":"path_eq","path":"result.summary","value":"all-good"}`)
	got, err := p.Evaluate(json.RawMessage(`{"result":{"summary":"all-good"}}`))
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !got {
		t.Fatal("dotted path should match")
	}
}

func TestPredicatePathMatch(t *testing.T) {
	t.Parallel()
	p, _ := ParsePredicate(`{"kind":"path_match","path":"name","regex":"^foo"}`)
	cases := map[string]bool{
		`{"name":"foobar"}`: true,
		`{"name":"barfoo"}`: false,
		`{"name":""}`:       false,
		`{"other":"foo"}`:   false,
	}
	for payload, want := range cases {
		got, err := p.Evaluate(json.RawMessage(payload))
		if err != nil {
			t.Fatalf("evaluate %q: %v", payload, err)
		}
		if got != want {
			t.Errorf("evaluate %q = %v, want %v", payload, got, want)
		}
	}
}
