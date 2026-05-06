package cli

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandContextFallsBackToBackground(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{}
	if got := commandContext(cmd); got == nil {
		t.Fatal("commandContext returned nil")
	}
}

func TestCommandContextUsesExplicitContext(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), struct{}{}, "x")
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	if got := commandContext(cmd); got != ctx {
		t.Fatal("commandContext should return the command's context")
	}
}

func TestJSONOutputReadsFlagValue(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json flag: %v", err)
	}
	if !jsonOutput(cmd) {
		t.Fatal("jsonOutput should report true when the flag is set")
	}
}
