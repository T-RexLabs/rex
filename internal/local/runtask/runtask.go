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
	"time"

	"gopkg.in/yaml.v3"

	"github.com/asabla/rex/internal/core/acp"
	"github.com/asabla/rex/internal/core/hooks"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/runner/adapter"
	"github.com/asabla/rex/internal/core/runner/primharness"
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
	// OnEvent is invoked with every event the run emits, AFTER it
	// has been written to the event log. Used by `rex run start`
	// in attached mode (the default) to render events live as
	// they happen. Nil = silent during execution.
	OnEvent func(eventlog.Record)
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
		Sink:     &writerSink{w: ws.Writer, onEvent: req.OnEvent},
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

// HarnessRunRequest configures a single-harness-node DAG run
// (execution.PRIM.1). The harness is resolved via the adapter
// registry — supply Adapters explicitly to share a registry with
// the test, or leave nil to use adapter.Default() (the production
// path; cmd/rex registers every bundled adapter at startup).
type HarnessRunRequest struct {
	// Harness names a registered adapter. Required.
	Harness string
	// Prompt is the initial user message handed to session/new.
	// Required.
	Prompt string
	// Model and Mode pass through to the adapter; empty means
	// "harness default."
	Model string
	Mode  string
	// MCPServers are forwarded to the harness via session/new
	// (execution.ACP.5).
	MCPServers []acp.MCPServer
	// Dir overrides the harness's working directory. Default is
	// the workspace root.
	Dir string
	// Env merges into the harness process environment.
	Env map[string]string
	// Timeout bounds the entire invocation. Zero = no timeout
	// (harness exits on its own).
	Timeout time.Duration
	// NodeID is the id assigned to the harness node in the DAG.
	// Defaults to "harness" when empty.
	NodeID string
	// RunID is the explicit run id. Defaults to clock.Now() when
	// empty.
	RunID string
	// Adapters is the registry consulted for Harness; nil =
	// adapter.Default().
	Adapters *adapter.Registry
	// OnEvent is invoked with every event the run emits, AFTER it
	// has been written to the event log. Used by `rex run start`
	// in attached mode (the default) to render events live as
	// they happen. Nil = silent during execution.
	OnEvent func(eventlog.Record)
	// OnStderr is invoked once per line written to the harness's
	// stderr (npx output, bridge diagnostics, crashes). Nil
	// silently drops; the CLI typically wires this to its own
	// os.Stderr so the user sees what the bridge is doing.
	OnStderr func(line string)
}

// StartHarnessRun executes a one-node harness DAG synchronously
// against ws. Returns when the harness exits; events stream to
// the workspace event log throughout. Single-shot interactive
// shape — the CLI process is the run's lifetime.
func StartHarnessRun(ctx context.Context, ws *Workspace, req HarnessRunRequest) (*ShellRunResult, error) {
	if ws == nil {
		return nil, fmt.Errorf("runtask: nil workspace")
	}
	if req.Harness == "" {
		return nil, fmt.Errorf("runtask: harness is required")
	}
	if req.Prompt == "" {
		return nil, fmt.Errorf("runtask: prompt is required")
	}
	nodeID := req.NodeID
	if nodeID == "" {
		nodeID = "harness"
	}
	dir := req.Dir
	if dir == "" {
		dir = ws.Root
	}

	cfg, err := json.Marshal(primharness.Config{
		Harness:    req.Harness,
		Prompt:     req.Prompt,
		Model:      req.Model,
		Mode:       req.Mode,
		Dir:        dir,
		Env:        req.Env,
		MCPServers: req.MCPServers,
		Timeout:    req.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal harness config: %w", err)
	}
	dag := runner.DAG{
		Nodes: []runner.Node{
			{ID: runner.NodeID(nodeID), Type: primharness.PrimitiveType, Config: cfg},
		},
	}

	runID := req.RunID
	if runID == "" {
		runID = ws.Clock.Now().String()
	}

	// frameWriter persists each ACP frame as a harness.frame event
	// in the workspace log so `rex run watch`, `rex run show`, and
	// the web run-detail page all see the actual transcript content
	// — not just lifecycle events. Routes through the same sink
	// the executor uses so OnEvent (the CLI's live stream) fires
	// for harness.frame events too.
	sink := &writerSink{w: ws.Writer, onEvent: req.OnEvent}
	frameWriter := buildHarnessFrameWriter(sink, runID, runner.NodeID(nodeID))

	reg := runner.NewPrimitiveRegistry()
	reg.Register(primharness.PrimitiveType, primharness.New(primharness.Options{
		WorkspaceID: ws.ID,
		Adapters:    req.Adapters,
		OnStderr:    req.OnStderr,
		OnFrame:     frameWriter,
	}))

	exec, err := runner.NewExecutor(runner.ExecConfig{
		RunID:    runID,
		DAG:      dag,
		Sink:     sink,
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

// buildHarnessFrameWriter returns a primharness.OnFrame callback
// that translates each received ACP frame into a HarnessFrameEvent
// and writes it through sink — the same sink the executor uses for
// run/node lifecycle events, so OnEvent fires for harness frames
// too and the CLI's live stream sees them.
//
// Append errors are swallowed: a stdout-write failure must not
// abort an in-flight harness session. The frame count tracked by
// primharness still increments either way, so the diagnostic in
// node.succeeded.output stays accurate.
func buildHarnessFrameWriter(sink *writerSink, runID string, nodeID runner.NodeID) func(acp.RawMessage) {
	return func(raw acp.RawMessage) {
		ev := runner.HarnessFrameEvent{
			RunID:     runID,
			NodeID:    nodeID,
			Method:    raw.Message.Method,
			RequestID: rawMessageID(raw),
			SessionID: extractSessionID(raw),
			Frame:     append(json.RawMessage{}, raw.Raw...),
			At:        time.Now().UTC(),
		}
		payload, err := json.Marshal(ev)
		if err != nil {
			return
		}
		_ = sink.Append(runner.EventTypeHarnessFrame, runner.EventVersion, payload)
	}
}

// rawMessageID stringifies the JSON-RPC id field. ACP uses both
// numeric and string ids; coalescing to a string keeps the event
// payload simple downstream.
func rawMessageID(raw acp.RawMessage) string {
	if len(raw.Message.ID) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Trim(string(raw.Message.ID), `"`))
}

// extractSessionID best-effort pulls the sessionId out of an ACP
// frame's params (notifications) or result (responses). When the
// frame doesn't carry one, returns "" — frames produced before
// session/new completes are the obvious case. We do this rather
// than typed decoding because the params shape varies per method
// and we don't want each new method to require a code change.
func extractSessionID(raw acp.RawMessage) string {
	for _, body := range [][]byte{raw.Message.Params, raw.Message.Result} {
		if len(body) == 0 {
			continue
		}
		var probe struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(body, &probe); err == nil && probe.SessionID != "" {
			return probe.SessionID
		}
	}
	return ""
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

// writerSink adapts an eventlog.Writer to runner.EventSink. When
// OnEvent is non-nil it fires after the underlying append so the
// caller (e.g. attached `rex run start`) can render the event in
// real time. The hook receives the populated Record (id +
// timestamp filled in by the writer); errors are not propagated
// because a stdout-print failure must not abort a run.
type writerSink struct {
	w       *eventlog.Writer
	onEvent func(eventlog.Record)
}

func (s *writerSink) Append(eventType string, version uint32, payload json.RawMessage) error {
	rec, err := s.w.Append(eventType, version, payload)
	if err != nil {
		return err
	}
	if s.onEvent != nil {
		s.onEvent(rec)
	}
	return nil
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
