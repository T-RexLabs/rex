package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Predicate is the v1 edge-gating expression evaluated by the
// executor when deciding whether to schedule a downstream node.
// Implements execution.PRIM.5's "small expression language (path
// equality, exit-code check, regex match)" — five kinds in one
// closed set:
//
//	{"kind": "always"}                                       — true
//	{"kind": "never"}                                        — false (mostly for tests)
//	{"kind": "exit_code_eq", "value": 0}                     — upstream's output.exit_code == value
//	{"kind": "path_eq", "path": "status", "value": "ok"}     — upstream's output[path] == value
//	{"kind": "path_match", "path": "name", "regex": "^foo"}  — upstream's output[path] =~ regex
//
// `path` is a dotted-path lookup into the upstream node's output
// JSON (e.g. "result.summary"). Missing paths evaluate to false
// for both _eq and _match. Empty Predicate strings on edges are
// equivalent to {"kind":"always"}.
type Predicate struct {
	Kind  string          `json:"kind"`
	Path  string          `json:"path,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
	Regex string          `json:"regex,omitempty"`
}

// PredicateKind enumerates the recognised values of Predicate.Kind.
const (
	PredicateKindAlways     = "always"
	PredicateKindNever      = "never"
	PredicateKindExitCodeEq = "exit_code_eq"
	PredicateKindPathEq     = "path_eq"
	PredicateKindPathMatch  = "path_match"
)

// ParsePredicate turns the JSON Edge.Predicate string into a
// Predicate. Empty input yields the "always" predicate so existing
// edges (which were defined before predicates were enforced) keep
// firing unchanged.
func ParsePredicate(raw string) (Predicate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Predicate{Kind: PredicateKindAlways}, nil
	}
	var p Predicate
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Predicate{}, fmt.Errorf("runner: parse predicate %q: %w", raw, err)
	}
	switch p.Kind {
	case PredicateKindAlways, PredicateKindNever,
		PredicateKindExitCodeEq, PredicateKindPathEq, PredicateKindPathMatch:
	case "":
		return Predicate{}, errors.New("runner: predicate kind is required")
	default:
		return Predicate{}, fmt.Errorf("runner: unknown predicate kind %q", p.Kind)
	}
	if p.Kind == PredicateKindPathEq || p.Kind == PredicateKindPathMatch {
		if p.Path == "" {
			return Predicate{}, fmt.Errorf("runner: predicate kind %q requires path", p.Kind)
		}
	}
	if p.Kind == PredicateKindPathMatch {
		if p.Regex == "" {
			return Predicate{}, errors.New("runner: predicate kind path_match requires regex")
		}
		if _, err := regexp.Compile(p.Regex); err != nil {
			return Predicate{}, fmt.Errorf("runner: invalid regex %q: %w", p.Regex, err)
		}
	}
	return p, nil
}

// Evaluate runs p against the upstream node's recorded output.
// Returns false (with a nil error) when the path is missing or the
// upstream node has no output to inspect — those are normal "this
// branch isn't taken" outcomes, not bugs. Returns an error only on
// malformed inputs the parse step couldn't catch (e.g. corrupt
// upstream output JSON).
func (p Predicate) Evaluate(upstreamOutput json.RawMessage) (bool, error) {
	switch p.Kind {
	case PredicateKindAlways, "":
		return true, nil
	case PredicateKindNever:
		return false, nil
	case PredicateKindExitCodeEq:
		got, ok, err := lookupNumber(upstreamOutput, "exit_code")
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		want, err := numberFromValue(p.Value)
		if err != nil {
			return false, err
		}
		return got == want, nil
	case PredicateKindPathEq:
		got, ok, err := lookupValue(upstreamOutput, p.Path)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		return jsonValuesEqual(got, p.Value), nil
	case PredicateKindPathMatch:
		got, ok, err := lookupValue(upstreamOutput, p.Path)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
		s, ok := jsonValueAsString(got)
		if !ok {
			return false, nil
		}
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			return false, err
		}
		return re.MatchString(s), nil
	}
	return false, fmt.Errorf("runner: unknown predicate kind %q at evaluate", p.Kind)
}

// lookupValue traverses payload (a JSON object) along a dotted
// path. Returns (value, true, nil) on hit, (nil, false, nil) when
// any segment is missing, or (nil, false, err) on a malformed
// payload.
func lookupValue(payload json.RawMessage, path string) (json.RawMessage, bool, error) {
	if len(payload) == 0 {
		return nil, false, nil
	}
	segments := strings.Split(path, ".")
	cur := payload
	for _, seg := range segments {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false, err
		}
		next, ok := obj[seg]
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	return cur, true, nil
}

// lookupNumber is the float64 specialisation of lookupValue.
func lookupNumber(payload json.RawMessage, path string) (float64, bool, error) {
	v, ok, err := lookupValue(payload, path)
	if err != nil || !ok {
		return 0, ok, err
	}
	var n float64
	if err := json.Unmarshal(v, &n); err != nil {
		return 0, true, err
	}
	return n, true, nil
}

// numberFromValue decodes p.Value as a float64; the JSON encoder
// produces numbers as raw bytes that we need to coerce here.
func numberFromValue(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 {
		return 0, errors.New("runner: predicate value is empty")
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("runner: predicate value not a number: %w", err)
	}
	return n, nil
}

// jsonValuesEqual reports whether two raw JSON values represent
// the same scalar. Compares as strings after normalising via
// json.Unmarshal so 1 vs 1.0, "x" vs "x" all collapse correctly.
func jsonValuesEqual(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	// Normalise number/string/bool comparisons via fmt.Sprint —
	// JSON's `1.0` and `1` both decode to float64(1), strings are
	// compared verbatim, bools as their literal form. Map/slice
	// comparison via Sprint is order-sensitive but predicates
	// only target scalars in v1.
	return fmt.Sprint(av) == fmt.Sprint(bv)
}

// jsonValueAsString extracts a string from a scalar JSON value.
// Returns false for non-scalar shapes (objects, arrays, null) so
// path_match cleanly skips them rather than regex-matching weird
// printf forms.
func jsonValueAsString(raw json.RawMessage) (string, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		return fmt.Sprintf("%v", t), true
	case bool:
		if t {
			return "true", true
		}
		return "false", true
	case nil:
		return "", false
	default:
		return "", false
	}
}
