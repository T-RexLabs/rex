package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIdentityShowDefaultCreatesAndPrints(t *testing.T) {
	t.Parallel()

	out, err := executeCommand(t, "identity", "show")
	if err != nil {
		t.Fatalf("identity show: %v\n%s", err, out)
	}
	for _, want := range []string{"handle:", "fingerprint:", "actor:", "default"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestIdentityShowPubReturnsPEM(t *testing.T) {
	t.Parallel()

	out, err := executeCommand(t, "identity", "show", "--pub")
	if err != nil {
		t.Fatalf("identity show --pub: %v\n%s", err, out)
	}
	if !strings.Contains(out, "BEGIN PUBLIC KEY") {
		t.Fatalf("output missing PEM header: %s", out)
	}
	if !strings.Contains(out, "END PUBLIC KEY") {
		t.Fatalf("output missing PEM footer: %s", out)
	}
}

func TestIdentityShowJSON(t *testing.T) {
	t.Parallel()

	out, err := executeCommand(t, "identity", "show", "--json")
	if err != nil {
		t.Fatalf("identity show --json: %v\n%s", err, out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if v["handle"] != "default" {
		t.Fatalf("handle: %v", v["handle"])
	}
	fp, _ := v["fingerprint"].(string)
	if len(fp) != 16 {
		t.Fatalf("fingerprint should be 16 hex chars: %q", fp)
	}
	actor, _ := v["actor"].(string)
	if !strings.HasPrefix(actor, "l-") {
		t.Fatalf("actor should start with l-: %q", actor)
	}
}

func TestIdentityShowNamedHandleFailsIfMissing(t *testing.T) {
	t.Parallel()

	_, err := executeCommand(t, "identity", "show", "ghost")
	if err == nil {
		t.Fatal("expected error for missing identity")
	}
}

func TestIdentityListAfterDefaultShow(t *testing.T) {
	t.Parallel()

	// Show creates the default identity; list should then surface it.
	if _, err := executeCommand(t, "identity", "show"); err != nil {
		t.Fatalf("show: %v", err)
	}
	out, err := executeCommand(t, "identity", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "default") {
		t.Fatalf("list missing default: %s", out)
	}
	if !strings.Contains(out, "HANDLE") {
		t.Fatalf("list missing header: %s", out)
	}
}

func TestIdentityListJSON(t *testing.T) {
	t.Parallel()

	if _, err := executeCommand(t, "identity", "show"); err != nil {
		t.Fatalf("show: %v", err)
	}
	out, err := executeCommand(t, "identity", "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v\n%s", err, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, out)
	}
	found := false
	for _, r := range rows {
		if r["handle"] == "default" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("default not in list: %v", rows)
	}
}
