package schedule

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/robfig/cron/v3"
)

// Fire is the dispatch payload the daemon hands the caller every
// time a trigger matches. The caller (typically the CLI's
// `rex schedule run` or `rex schedule trigger`) is responsible for
// turning the schedule's recipe into an actual run.
type Fire struct {
	Schedule *Schedule
	// At is the wall-clock instant the trigger matched, sourced
	// from the daemon's now() so tests can drive deterministic
	// fire times (overview.ENG.4).
	At time.Time
	// Reason is a free-form human-readable explanation suitable
	// for the run.started.trigger.reason field (execution.RUN.1.3).
	// e.g. "cron 0 3 * * *" or "file_watch matched src/foo.go".
	Reason string
}

// Dispatcher is the function the daemon invokes for every fire.
// Implementations should respect ctx (the daemon's parent context
// is cancelled on Stop) and apply EXEC-CONC.1 workspace-serial
// semantics — fires that arrive while a previous run is still
// going queue rather than overlap.
type Dispatcher func(ctx context.Context, fire Fire) error

// DaemonOptions configures a Daemon.
type DaemonOptions struct {
	// WorkspaceRoot is the workspace whose `.rex/schedules/` we
	// watch. Required.
	WorkspaceRoot string
	// Dispatch is invoked once per matched trigger.
	Dispatch Dispatcher
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
	// OnError is invoked for non-fatal errors during the daemon's
	// lifetime (a fired dispatch returned an error, an fsnotify
	// event arrived for a stale watcher, etc.). Nil drops them.
	OnError func(error)
	// Schedules optionally pre-populates the daemon with already-
	// loaded schedules instead of having Run() walk the directory.
	// Empty triggers a directory walk on Run.
	Schedules []*Schedule
}

// Daemon is the v1 schedule engine. One Daemon owns one workspace's
// schedules; concurrent workspaces would each have their own.
type Daemon struct {
	opts DaemonOptions

	mu      sync.Mutex
	cron    *cron.Cron
	watcher *fsnotify.Watcher

	// fileWatchSchedules maps an absolute filesystem path that
	// matched at least one schedule's globs to the schedules that
	// claimed it. fsnotify events arrive per-path; we look up
	// schedules here and dispatch a fire for each match.
	fileWatchSchedules map[string][]*Schedule

	// debounceTimers per schedule name; reset on every fsnotify
	// event so a burst of saves only fires once.
	debounceTimers map[string]*time.Timer
}

// NewDaemon builds a Daemon. Run starts it; Stop tears it down.
func NewDaemon(opts DaemonOptions) (*Daemon, error) {
	if opts.WorkspaceRoot == "" {
		return nil, errors.New("schedule: WorkspaceRoot is required")
	}
	if opts.Dispatch == nil {
		return nil, errors.New("schedule: Dispatch is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Daemon{
		opts:               opts,
		fileWatchSchedules: map[string][]*Schedule{},
		debounceTimers:     map[string]*time.Timer{},
	}, nil
}

// Run blocks until ctx is cancelled or a fatal error trips. The
// daemon registers cron tickers (one per cron schedule) and
// fsnotify watchers (one per file_watch schedule) up-front, then
// waits.
func (d *Daemon) Run(ctx context.Context) error {
	scheds := d.opts.Schedules
	if len(scheds) == 0 {
		loaded, err := LoadDir(Dir(d.opts.WorkspaceRoot))
		if err != nil {
			return err
		}
		scheds = loaded
	}

	if err := d.start(ctx, scheds); err != nil {
		return err
	}
	defer d.stop()

	<-ctx.Done()
	return nil
}

func (d *Daemon) start(ctx context.Context, scheds []*Schedule) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.cron = cron.New(cron.WithLocation(time.Local))

	for _, s := range scheds {
		switch s.Trigger.Kind {
		case TriggerKindCron:
			if err := d.addCron(ctx, s); err != nil {
				return err
			}
		case TriggerKindFileWatch:
			if err := d.addFileWatch(ctx, s); err != nil {
				return err
			}
		}
	}

	d.cron.Start()
	if d.watcher != nil {
		go d.runWatcher(ctx)
	}
	return nil
}

func (d *Daemon) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.cron != nil {
		<-d.cron.Stop().Done()
		d.cron = nil
	}
	if d.watcher != nil {
		_ = d.watcher.Close()
		d.watcher = nil
	}
	for name, t := range d.debounceTimers {
		t.Stop()
		delete(d.debounceTimers, name)
	}
}

// addCron parses the schedule's cron expression and registers a
// callback that dispatches a Fire on each tick. Robfig's cron
// parser accepts the standard 5-field format by default.
func (d *Daemon) addCron(ctx context.Context, s *Schedule) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	schedule, err := parser.Parse(s.Trigger.Cron)
	if err != nil {
		return fmt.Errorf("schedule %q: cron parse: %w", s.Name, err)
	}
	wrapper := &cronEntry{
		next: schedule,
		fire: func() {
			d.dispatchFire(ctx, Fire{
				Schedule: s,
				At:       d.opts.Now(),
				Reason:   fmt.Sprintf("cron %s", s.Trigger.Cron),
			})
		},
	}
	d.cron.Schedule(wrapper, wrapper)
	return nil
}

// cronEntry adapts our schedule.Schedule to the cron.Job +
// cron.Schedule interfaces robfig's library expects.
type cronEntry struct {
	next cron.Schedule
	fire func()
}

func (c *cronEntry) Next(t time.Time) time.Time { return c.next.Next(t) }
func (c *cronEntry) Run()                       { c.fire() }

// addFileWatch creates the fsnotify Watcher (lazily, on first
// file_watch schedule) and registers the schedule's globs against
// the directories that contain them. fsnotify watches directories,
// not glob patterns directly, so we add a watcher for each parent
// directory and filter at event time.
func (d *Daemon) addFileWatch(ctx context.Context, s *Schedule) error {
	if d.watcher == nil {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			return fmt.Errorf("fsnotify: %w", err)
		}
		d.watcher = w
	}
	for _, glob := range s.Trigger.Paths {
		// Resolve the glob against the workspace root so a
		// schedule with relative paths watches the right place.
		abs := glob
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(d.opts.WorkspaceRoot, abs)
		}
		// fsnotify watches paths, not patterns. Walk to the
		// fixed prefix (everything before the first glob meta-
		// character) and watch that as a directory if it exists.
		prefix := fixedPrefix(abs)
		if prefix == "" {
			continue
		}
		if err := d.watcher.Add(prefix); err != nil {
			// A schedule may name a directory that doesn't exist
			// yet — record the error but keep going so other
			// schedules still load.
			d.reportError(fmt.Errorf("schedule %q: add watch %s: %w", s.Name, prefix, err))
			continue
		}
	}
	// Track the schedule so events on any watched path can fan
	// out to it during runWatcher.
	d.fileWatchSchedules[s.Name] = append(d.fileWatchSchedules[s.Name], s)
	return nil
}

// fixedPrefix returns the longest leading substring of p that has
// no glob meta-characters. e.g. "src/**/*.go" -> "src".
func fixedPrefix(p string) string {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '*', '?', '[':
			// Walk back to the last separator so we watch a
			// directory, not a partial name.
			cut := strings.LastIndex(p[:i], string(filepath.Separator))
			if cut < 0 {
				return "."
			}
			return p[:cut]
		}
	}
	return p
}

// runWatcher pumps fsnotify events. For each event whose path
// matches any registered schedule's globs, schedule a debounced
// fire.
func (d *Daemon) runWatcher(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-d.watcher.Events:
			if !ok {
				return
			}
			d.handleFSEvent(ctx, ev)
		case err, ok := <-d.watcher.Errors:
			if !ok {
				return
			}
			d.reportError(fmt.Errorf("fsnotify: %w", err))
		}
	}
}

func (d *Daemon) handleFSEvent(ctx context.Context, ev fsnotify.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for name, list := range d.fileWatchSchedules {
		for _, s := range list {
			if !pathMatchesAny(s.Trigger.Paths, ev.Name, d.opts.WorkspaceRoot) {
				continue
			}
			// Reset / arm the debounce timer for this schedule.
			if t := d.debounceTimers[name]; t != nil {
				t.Stop()
			}
			capturedSched := s
			capturedReason := fmt.Sprintf("file_watch matched %s", ev.Name)
			d.debounceTimers[name] = time.AfterFunc(s.Trigger.Debounce(), func() {
				d.dispatchFire(ctx, Fire{
					Schedule: capturedSched,
					At:       d.opts.Now(),
					Reason:   capturedReason,
				})
			})
			break // one fire per schedule per event
		}
	}
}

func pathMatchesAny(globs []string, path, workspaceRoot string) bool {
	rel, err := filepath.Rel(workspaceRoot, path)
	if err != nil {
		rel = path
	}
	for _, g := range globs {
		if ok, _ := filepath.Match(g, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(g, filepath.Base(path)); ok {
			return true
		}
	}
	return false
}

func (d *Daemon) dispatchFire(ctx context.Context, fire Fire) {
	if err := d.opts.Dispatch(ctx, fire); err != nil {
		d.reportError(fmt.Errorf("schedule %q dispatch: %w", fire.Schedule.Name, err))
	}
}

func (d *Daemon) reportError(err error) {
	if d.opts.OnError == nil {
		return
	}
	d.opts.OnError(err)
}

// FireOnce is a test-shape helper that runs the daemon's dispatch
// path for one schedule without starting tickers or watchers.
// `rex schedule trigger <name>` uses this to produce a single
// reproducible fire from the CLI.
func FireOnce(ctx context.Context, dispatch Dispatcher, s *Schedule, now func() time.Time) error {
	if dispatch == nil {
		return errors.New("schedule: Dispatch is required")
	}
	if s == nil {
		return errors.New("schedule: nil schedule")
	}
	if now == nil {
		now = time.Now
	}
	return dispatch(ctx, Fire{
		Schedule: s,
		At:       now(),
		Reason:   "manual trigger",
	})
}
