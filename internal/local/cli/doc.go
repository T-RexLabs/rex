// Package cli is the local-node command-line interface for rex.
//
// The shape is verb-noun, deeply nested, Git-inspired (cli.SHAPE.1).
// Top-level shortcuts exist only for the highest-frequency operations
// (cli.SHAPE.2). Every command supports --help with a synopsis,
// description, and examples (cli.SHAPE.3).
//
// This package is local-only: the central server has no shared CLI
// surface (per overview.SYS.1, the difference between local and
// central lives in the binary shell, not in core packages). Commands
// here reach into internal/core for their actual work.
package cli
