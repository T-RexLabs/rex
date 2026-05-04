package server

import (
	"io"
	"log/slog"
	"strings"
)

// LogConfig configures the central node's structured logger.
//
// HEALTH.3 says "Structured JSON logs are written to stdout; one
// log line per significant operation; no logs contain secrets".
// The "no secrets" half is enforced at the call site rather than
// via a redaction layer: the codebase never passes token values,
// signature hex, raw payload bytes, or the DB DSN's password
// component into a log.* call. What we DO log: fingerprints
// (already public-by-design), counts, error messages, durations,
// hostnames, scopes.
//
// Format defaults to "json" (the spec's requirement). "text"
// exists for local-dev readability; tests select "discard" via
// the Output sentinel.
type LogConfig struct {
	// Output is where log lines go. Defaults to os.Stdout in
	// the binary's main; tests typically pass a *bytes.Buffer
	// to inspect emitted records or io.Discard to silence
	// expected noise.
	Output io.Writer

	// Level is the minimum slog level to emit. Defaults to
	// slog.LevelInfo; --log-level on the binary sets it.
	Level slog.Level

	// Format is "json" (default) or "text".
	Format string
}

// NewLogger builds an slog.Logger from cfg, applying defaults
// for any zero-valued field. The returned logger is never nil so
// callers can use it without a nil check.
func NewLogger(cfg LogConfig) *slog.Logger {
	out := cfg.Output
	if out == nil {
		// Tests that don't supply Output get a silent logger by
		// default — keeping go test noiseless. The binary
		// always sets os.Stdout explicitly.
		out = io.Discard
	}
	level := cfg.Level
	// slog.LevelInfo is the zero value, so the field's zero
	// already does the right thing without explicit handling.

	var handler slog.Handler
	switch strings.ToLower(cfg.Format) {
	case "text":
		handler = slog.NewTextHandler(out, &slog.HandlerOptions{Level: level})
	default: // "json", "", anything-else → JSON
		handler = slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level})
	}
	return slog.New(handler).With("component", "rex-central")
}

// ParseLevel turns a "debug" / "info" / "warn" / "error" string
// into an slog.Level. Unknown strings default to LevelInfo so a
// typo in config doesn't silently disable observability.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
