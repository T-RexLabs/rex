// Command rex-central is the Rex central-node server.
//
// This is the multi-tenant central binary deployed via Docker Compose
// (see specs/central-node.yaml). It is the thin shell over the shared
// core; the shared core does the work (overview.SYS.1).
package main

import (
	"fmt"
	"os"
)

// version is set at build time via -ldflags. Defaults to "dev" for local builds.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rex-central:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "version") {
		fmt.Println("rex-central", version)
		return nil
	}
	fmt.Println("rex-central", version)
	fmt.Println("Central server not yet implemented; see specs/central-node.yaml")
	return nil
}
