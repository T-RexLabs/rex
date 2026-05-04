// Package runtask is the shared run-execution helper used by both
// the local CLI (cmd/rex run start) and the local web UI (POST
// /runs/start) so they share one event-log writer composition,
// hooks dispatcher, and search indexer wiring. v1 ships shell
// runs only — see ShellRun.
//
// Why a separate package: cmd/rex/cli and internal/local/web are
// peer surfaces onto the same model. Duplicating the writer +
// indexer + dispatcher composition across both invites drift; one
// surface gaining a side effect (e.g. hooks fan-out) and the other
// silently missing it is a bug class we should not enable.
package runtask

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/hooks"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/runner/primshell"
	"github.com/asabla/rex/internal/core/search"
	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// Workspace is an opened, write-ready workspace handle composed of
// every side effect a run produces: events.log writer, HLC clock,
// hooks dispatcher, search indexer. Open returns it; the caller
// invokes Close to drain hooks and close the search index.
type Workspace struct {
	Root     string
	ID       string
	Writer   *eventlog.Writer
	Clock    *eventlog.Clock
	indexer  *search.Index
	hooks    *hooks.Dispatcher
}

// Open builds the writer + clock + dispatcher + indexer for the
// workspace at root. Mirrors what the CLI's newWorkspaceWriter
// helper used to do internally. Caller MUST call ws.Close() when
// done (typically via defer); skipping it leaks hook workers and
// the SQLite handle.
func Open(root string) (*Workspace, error) {
	settings, err := readWorkspaceSettings(root)
	if err != nil {
		return nil, err
	}
	clock := eventlog.NewClock()

	global, _ := globalHooksDir()
	disp := hooks.New(hooks.Options{
		WorkspaceRoot:  root,
		GlobalHooksDir: global,
	})

	idx, _ := search.Open(root)
	indexerCB := search.EventIndexer(idx, nil)

	onAppend := func(rec eventlog.Record) {
		disp.OnAppend(rec)
		indexerCB(rec)
	}

	w, err := eventlog.OpenWriter(eventlog.WriterConfig{
		Path:        EventLogPath(root),
		WorkspaceID: settings.ID,
		Clock:       clock,
		OnAppend:    onAppend,
	})
	if err != nil {
		disp.Drain()
		if idx != nil {
			_ = idx.Close()
		}
		return nil, err
	}
	return &Workspace{
		Root:    root,
		ID:      settings.ID,
		Writer:  w,
		Clock:   clock,
		indexer: idx,
		hooks:   disp,
	}, nil
}

// Close flushes the event-log writer, drains hook workers, and
// closes the search index handle. Idempotent.
func (ws *Workspace) Close() error {
	if ws == nil {
		return nil
	}
	if ws.Writer != nil {
		_ = ws.Writer.Close()
		ws.Writer = nil
	}
	if ws.hooks != nil {
		ws.hooks.Drain()
		ws.hooks = nil
	}
	if ws.indexer != nil {
		_ = ws.indexer.Close()
		ws.indexer = nil
	}
	return nil
}

// EventLogPath returns the canonical events.log path for a workspace.
func EventLogPath(root string) string {
	return filepath.Join(root, ".rex", "events.log")
}

// ShellRunRequest configures a single-shell-node DAG run.
type ShellRunRequest struct {
	// Command is the argv to exec. Use SplitShellCommand to derive
	// it from a single shell-style string when the surface only
	// has a string field (e.g. CLI --shell flag, web form input).
	Command []string
	// NodeID is the id assigned to the shell node in the DAG.
	// Defaults to "shell" when empty.
	NodeID string
	// RunID is the explicit run id. Defaults to clock.Now() when
	// empty.
	RunID string
}

// ShellRunResult is the outcome of StartShellRun: the assigned run
// id and the engine's final state.
type ShellRunResult struct {
	RunID string
	State *runner.RunState
}

// StartShellRun executes a one-node shell DAG synchronously against
// ws. Returns when the run terminates; events were written to the
// log throughout. The synchronous semantics match v1 where there
// is no daemon — the caller (CLI process or HTTP request goroutine)
// is the run's lifetime.
func StartShellRun(ctx context.Context, ws *Workspace, req ShellRunRequest) (*ShellRunResult, error) {
	if ws == nil {
		return nil, fmt.Errorf("runtask: nil workspace")
	}
	if len(req.Command) == 0 {
		return nil, fmt.Errorf("runtask: command is empty")
	}
	nodeID := req.NodeID
	if nodeID == "" {
		nodeID = "shell"
	}

	cfg, err := json.Marshal(primshell.Config{Command: req.Command})
	if err != nil {
		return nil, fmt.Errorf("marshal shell config: %w", err)
	}
	dag := runner.DAG{
		Nodes: []runner.Node{
			{ID: runner.NodeID(nodeID), Type: primshell.PrimitiveType, Config: cfg},
		},
	}

	runID := req.RunID
	if runID == "" {
		runID = ws.Clock.Now().String()
	}

	reg := runner.NewPrimitiveRegistry()
	reg.Register(primshell.PrimitiveType, primshell.New(primshell.Options{WorkspaceDir: ws.Root}))

	exec, err := runner.NewExecutor(runner.ExecConfig{
		RunID:    runID,
		DAG:      dag,
		Sink:     &writerSink{w: ws.Writer},
		Registry: reg,
	})
	if err != nil {
		return nil, err
	}
	state, err := exec.Run(ctx)
	if err != nil {
		return nil, err
	}
	return &ShellRunResult{RunID: runID, State: state}, nil
}

// SplitShellCommand parses a POSIX-ish quoted shell string into
// argv. Bare words and double-quoted runs are supported; single
// quotes and backslash escapes can land later if a real surface
// demands them. Mirrors the CLI splitShellCommand helper, exposed
// here so the web form can use the same parser.
func SplitShellCommand(cmd string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range cmd {
		switch {
		case r == '"':
			inQuote = !inQuote
		case !inQuote && (r == ' ' || r == '\t'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("unbalanced quote in shell command")
	}
	flush()
	if len(out) == 0 {
		return nil, fmt.Errorf("shell command is empty")
	}
	return out, nil
}

// writerSink adapts an eventlog.Writer to runner.EventSink.
type writerSink struct {
	w *eventlog.Writer
}

func (s *writerSink) Append(eventType string, version uint32, payload json.RawMessage) error {
	_, err := s.w.Append(eventType, version, payload)
	return err
}

// settings is the minimal subset of workspace.yaml runtask needs to
// stamp the WorkspaceID on every event. Read directly here so
// runtask doesn't depend on internal/local/cli (which would create
// a cycle once cli/run.go imports this package).
type settings struct {
	ID string `yaml:"id"`
}

func readWorkspaceSettings(root string) (*settings, error) {
	path := filepath.Join(root, ".rex", "workspace.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s settings
	if err := yaml.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &s, nil
}

func globalHooksDir() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "rex", "hooks"), nil
}
