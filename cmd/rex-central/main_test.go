package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootHelpListsServe(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd("test")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--help: %v", err)
	}
	if !strings.Contains(buf.String(), "serve") {
		t.Fatalf("help output missing serve: %s", buf.String())
	}
}

func TestRootVersionPrintsVersion(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd("v9-test")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if !strings.Contains(buf.String(), "v9-test") {
		t.Fatalf("--version output: %s", buf.String())
	}
}

func TestServeHelpListsFlags(t *testing.T) {
	t.Parallel()

	cmd := newRootCmd("test")
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"serve", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("serve --help: %v", err)
	}
	if !strings.Contains(buf.String(), "--addr") {
		t.Fatalf("serve help missing --addr: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "--shutdown-timeout") {
		t.Fatalf("serve help missing --shutdown-timeout: %s", buf.String())
	}
}
