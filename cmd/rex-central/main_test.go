package main

import "testing"

func TestRunVersion(t *testing.T) {
	if err := run([]string{"--version"}); err != nil {
		t.Fatalf("run --version: %v", err)
	}
}
