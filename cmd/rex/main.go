// Command rex is the Rex local-node CLI.
//
// This is the local-first binary that runs on a developer's machine. It is
// the thin shell over the shared core (overview.SYS.1); the work happens
// inside internal/local/cli, which composes packages from internal/core.
package main

import (
	"os"

	"github.com/asabla/rex/internal/local/cli"
)

// version is set at build time via -ldflags. Defaults to "dev" for local builds.
var version = "dev"

func main() {
	os.Exit(cli.Execute(version))
}
