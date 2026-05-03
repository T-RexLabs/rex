// Command rex is the Rex local-node CLI.
//
// This is the local-first binary that runs on a developer's machine. It is
// the thin shell over the shared core; the shared core does the work
// (overview.SYS.1).
package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags. Defaults to "dev" for local builds.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rex:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Println("rex", version)
		return nil
	}
	fmt.Println("rex", version)
	fmt.Println("CLI surface not yet implemented; see specs/cli.yaml")
	return nil
}
