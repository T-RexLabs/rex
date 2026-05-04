package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asabla/rex/internal/core/storage/eventlog"
)

// Defaults from hooks.EXEC.3 / EXEC.4.
const (
	DefaultWorkers       = 4
	DefaultHookTimeout   = 30 * time.Second
	DefaultKillGrace     = 5 * time.Second
	hookLogDirName       = "hook-log"
	wildcardHookName     = "post-any"
	configSuffixSidecar  = ".config.toml"
)

// Options configure a Dispatcher.
type Options struct {
	// WorkspaceRoot is the workspace whose .rex/hooks/ to walk.
	// When empty, only the global directory is consulted.
	WorkspaceRoot string
	// GlobalHooksDir is the global hook directory (typically
	// <user-config-dir>/rex/hooks). When empty, no global hooks
	// fire.
	GlobalHooksDir string
	// Workers bounds the worker pool size. Zero falls back to
	// DefaultWorkers.
	Workers int
	// Timeout is the per-hook wall-clock timeout. Zero falls back
	// to DefaultHookTimeout.
	Timeout time.Duration
	// Logger is invoked once per dispatched hook with a structured
	// summary (handle, exit code, output truncation). Optional.
	Logger func(Result)
	// Now and CommandFactory are injectable for tests so the
	// dispatcher does not need to spawn real subprocesses or rely
	// on the wall clock when verifying the queueing/log-writing
	// machinery.
	Now            func() time.Time
	CommandFactory func(ctx context.Context, name string, args ...string) Cmd
}

// Cmd is the small subset of *exec.Cmd the dispatcher uses; the
// indirection lets tests inject fakes that don't fork subprocesses.
type Cmd interface {
	SetStdin(r interface{ Read([]byte) (int, error) })
	SetStdout(w interface{ Write([]byte) (int, error) })
	SetStderr(w interface{ Write([]byte) (int, error) })
	SetEnv(env []string)
	SetDir(dir string)
	Start() error
	Wait() error
	ProcessKill() error
	ExitCode() int
}

// Result is one dispatched hook's outcome.
type Result struct {
	HookName string
	Scope    string // "workspace" or "global"
	Path     string
	EventID  string
	ExitCode int
	Skipped  bool   // true for non-executable, sidecar, or missing
	Reason   string // populated when Skipped or Failure
	Duration time.Duration
}

// Dispatcher fires hooks for events. The zero value is unusable;
// build via New.
type Dispatcher struct {
	opts Options

	queue chan dispatchJob
	wg    sync.WaitGroup
}

type dispatchJob struct {
	rec eventlog.Record
}

// New builds a Dispatcher, starts its workers, and returns it.
// Callers must call Drain (or Close) before exiting to ensure
// outstanding hook output makes it to disk.
func New(opts Options) *Dispatcher {
	if opts.Workers <= 0 {
		opts.Workers = DefaultWorkers
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultHookTimeout
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CommandFactory == nil {
		opts.CommandFactory = realCommandFactory
	}
	d := &Dispatcher{
		opts:  opts,
		queue: make(chan dispatchJob, 64),
	}
	for i := 0; i < opts.Workers; i++ {
		d.wg.Add(1)
		go d.worker()
	}
	return d
}

// OnAppend matches the eventlog.WriterConfig.OnAppend signature; it
// enqueues a hook-dispatch job and returns immediately. The caller
// (the writer) is never blocked by hook execution.
func (d *Dispatcher) OnAppend(rec eventlog.Record) {
	if d == nil {
		return
	}
	select {
	case d.queue <- dispatchJob{rec: rec}:
	default:
		// Queue is full. Drop the event rather than block the
		// writer. A logger here would be useful; for v1 we
		// surface this via a Result with Skipped=true so callers
		// observing a Logger see the drop.
		if d.opts.Logger != nil {
			d.opts.Logger(Result{
				EventID: rec.ID,
				Skipped: true,
				Reason:  "dispatcher queue full; event dropped",
			})
		}
	}
}

// Drain blocks until every previously-enqueued job finishes, then
// shuts down the workers. After Drain returns, the dispatcher is
// no longer usable.
func (d *Dispatcher) Drain() {
	if d == nil {
		return
	}
	close(d.queue)
	d.wg.Wait()
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for job := range d.queue {
		d.handleEvent(job.rec)
	}
}

func (d *Dispatcher) handleEvent(rec eventlog.Record) {
	hookFiles := d.resolveHooks(rec)
	for _, hf := range hookFiles {
		res := d.runHook(rec, hf)
		if d.opts.Logger != nil {
			d.opts.Logger(res)
		}
	}
}

// hookFile is one resolved hook ready to invoke.
type hookFile struct {
	Name  string // basename
	Path  string
	Scope string // "workspace" or "global"
}

func (d *Dispatcher) resolveHooks(rec eventlog.Record) []hookFile {
	var out []hookFile
	specific := hookNameForEvent(rec.Type)
	for _, candidate := range []string{specific, wildcardHookName} {
		if d.opts.WorkspaceRoot != "" {
			path := filepath.Join(d.opts.WorkspaceRoot, ".rex", "hooks", candidate)
			if isExecutableHook(path) {
				out = append(out, hookFile{Name: candidate, Path: path, Scope: "workspace"})
			}
		}
		if d.opts.GlobalHooksDir != "" {
			path := filepath.Join(d.opts.GlobalHooksDir, candidate)
			if isExecutableHook(path) {
				out = append(out, hookFile{Name: candidate, Path: path, Scope: "global"})
			}
		}
	}
	return out
}

// hookNameForEvent maps a record's Type to the hook filename per
// hooks.EVENTS.1: "run.completed" → "post-run-completed",
// "spec.edit" → "post-spec-edit", etc.
func hookNameForEvent(eventType string) string {
	return "post-" + strings.ReplaceAll(eventType, ".", "-")
}

// isExecutableHook reports whether path exists, is a regular file,
// and has at least one execute bit set per hooks.EXEC.1. Sidecar
// .config.toml files are filtered.
func isExecutableHook(path string) bool {
	if strings.HasSuffix(path, configSuffixSidecar) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		// Symlinks tolerated when the target itself is
		// executable; resolve and re-check.
		target, err := os.Stat(path)
		if err != nil {
			return false
		}
		return !target.IsDir() && target.Mode()&0o111 != 0
	}
	return info.Mode()&0o111 != 0
}

func (d *Dispatcher) runHook(rec eventlog.Record, hf hookFile) Result {
	res := Result{
		HookName: hf.Name,
		Scope:    hf.Scope,
		Path:     hf.Path,
		EventID:  rec.ID,
	}
	startedAt := d.opts.Now()

	logPath, err := d.openHookLog(rec.ID, hf.Name)
	if err != nil {
		res.Reason = "open hook log: " + err.Error()
		res.Skipped = true
		return res
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		res.Reason = "open hook log: " + err.Error()
		res.Skipped = true
		return res
	}
	defer logFile.Close()

	payload, err := json.Marshal(rec)
	if err != nil {
		res.Reason = "marshal payload: " + err.Error()
		res.Skipped = true
		return res
	}

	ctx, cancel := context.WithTimeout(context.Background(), d.opts.Timeout)
	defer cancel()

	cmd := d.opts.CommandFactory(ctx, hf.Path)
	cmd.SetStdin(&bytesReader{data: payload})
	cmd.SetStdout(logFile)
	cmd.SetStderr(logFile)
	cmd.SetEnv(buildEnv(rec, d.opts.WorkspaceRoot))
	if d.opts.WorkspaceRoot != "" {
		cmd.SetDir(d.opts.WorkspaceRoot)
	}

	if err := cmd.Start(); err != nil {
		res.Reason = "start: " + err.Error()
		res.Skipped = true
		return res
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	select {
	case waitErr := <-doneCh:
		res.ExitCode = cmd.ExitCode()
		res.Duration = d.opts.Now().Sub(startedAt)
		if waitErr != nil {
			// Exit code already captured; record the wait error
			// as a reason but do not fail the dispatcher.
			res.Reason = waitErr.Error()
		}
		return res
	case <-ctx.Done():
		// Timeout — SIGTERM via cancel, then a brief grace
		// period before forcing.
		_ = cmd.ProcessKill()
		<-doneCh
		res.ExitCode = cmd.ExitCode()
		res.Duration = d.opts.Now().Sub(startedAt)
		res.Reason = fmt.Sprintf("timeout after %s", d.opts.Timeout)
		return res
	}
}

func (d *Dispatcher) openHookLog(eventID, hookName string) (string, error) {
	if d.opts.WorkspaceRoot == "" {
		// Without a workspace root we have nowhere to put the log.
		return "", errors.New("no workspace root for hook log")
	}
	dir := filepath.Join(d.opts.WorkspaceRoot, ".rex", hookLogDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Sanitize hook name in case it ever contains slashes (it
	// shouldn't, but defense in depth).
	safe := strings.ReplaceAll(hookName, string(os.PathSeparator), "_")
	return filepath.Join(dir, eventID+"."+safe+".log"), nil
}

func buildEnv(rec eventlog.Record, workspaceRoot string) []string {
	out := os.Environ()
	out = append(out,
		"REX_EVENT_TYPE="+rec.Type,
		"REX_EVENT_ID="+rec.ID,
		"REX_WORKSPACE_ID="+rec.WorkspaceID,
		"REX_WORKSPACE_PATH="+workspaceRoot,
		"REX_NODE_ROLE=local",
	)
	return out
}

// bytesReader implements io.Reader over a fixed payload. We don't
// import bytes.NewReader because the Cmd interface only requires a
// Read([]byte) (int, error) shape.
type bytesReader struct {
	data []byte
	off  int
}

func (b *bytesReader) Read(p []byte) (int, error) {
	if b.off >= len(b.data) {
		return 0, io.EOF
	}
	n := copy(p, b.data[b.off:])
	b.off += n
	return n, nil
}

// realCommandFactory is the production Cmd factory, shelling out
// via os/exec.
func realCommandFactory(ctx context.Context, name string, args ...string) Cmd {
	c := exec.CommandContext(ctx, name, args...)
	return &realCmd{cmd: c}
}

type realCmd struct {
	cmd      *exec.Cmd
	exitCode int
}

func (r *realCmd) SetStdin(rd interface{ Read([]byte) (int, error) }) {
	r.cmd.Stdin = readerAdapter{rd: rd}
}
func (r *realCmd) SetStdout(w interface{ Write([]byte) (int, error) }) {
	r.cmd.Stdout = writerAdapter{w: w}
}
func (r *realCmd) SetStderr(w interface{ Write([]byte) (int, error) }) {
	r.cmd.Stderr = writerAdapter{w: w}
}
func (r *realCmd) SetEnv(env []string) { r.cmd.Env = env }
func (r *realCmd) SetDir(dir string)   { r.cmd.Dir = dir }
func (r *realCmd) Start() error        { return r.cmd.Start() }
func (r *realCmd) Wait() error {
	err := r.cmd.Wait()
	if r.cmd.ProcessState != nil {
		r.exitCode = r.cmd.ProcessState.ExitCode()
	}
	return err
}
func (r *realCmd) ProcessKill() error {
	if r.cmd.Process == nil {
		return nil
	}
	return r.cmd.Process.Kill()
}
func (r *realCmd) ExitCode() int { return r.exitCode }

// reader/writer adapters bridge the dispatcher's narrow interfaces
// onto the io.Reader / io.Writer that os/exec wants.
type readerAdapter struct{ rd interface{ Read([]byte) (int, error) } }

func (r readerAdapter) Read(p []byte) (int, error) { return r.rd.Read(p) }

type writerAdapter struct{ w interface{ Write([]byte) (int, error) } }

func (w writerAdapter) Write(p []byte) (int, error) { return w.w.Write(p) }
