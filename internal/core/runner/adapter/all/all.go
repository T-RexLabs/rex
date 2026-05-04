// Package all is the import-side-effect bundle that registers every
// harness adapter shipped with the local rex binary. Importing this
// package (typically from cmd/rex via the cli wiring) loads each
// adapter's init() block, which in turn calls Register on
// adapter.Default(). Tests that want isolated registries do NOT
// import this package; they Register adapters explicitly into their
// own *adapter.Registry.
//
// Add a new bundled adapter here as a single blank-import line.
package all

import (
	// claude-code (execution.ADAPT.2): the upstream
	// @agentclientprotocol/claude-agent-acp bridge spawned via npx.
	_ "github.com/asabla/rex/internal/core/runner/adapter/claudecode"
)
