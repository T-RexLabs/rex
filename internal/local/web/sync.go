package web

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/local/remotes"
	syncclient "github.com/asabla/rex/internal/local/sync"
)

// syncResultData backs sync.tmpl. It's reused for both the GET
// landing form (Remote/Result/Error empty) and the POST result
// rerender (one or more populated).
type syncResultData struct {
	pageData

	Remote     string // remote we tried to sync against
	Pushed     int    // events accepted by the remote
	Duplicates int    // events the remote already had
	Pulled     int    // events streamed back from the remote
	HeadID     string // server head after the push
	Error      string // human-readable failure surface

	Available []syncRemoteRow // for the form's remote-select
}

// syncRemoteRow is one option in the remote select on the sync
// page. Mirrors the minimal view the form needs without dragging
// the full remoteDetailRow shape from the /remotes page.
type syncRemoteRow struct {
	Name string
	URL  string
}

// handleSyncPage renders GET /sync — a small page that lists the
// registered remotes and offers a per-remote "sync now" form.
// Without remotes the page surfaces the rex remote add hint
// rather than rendering an empty form.
func (s *Server) handleSyncPage(w http.ResponseWriter, r *http.Request) {
	d := s.loadSyncBase()
	d.NavSection = "settings"
	s.render(w, r, "sync.tmpl", d)
}

// handleSyncRun is the POST endpoint behind every "sync now"
// button. Synchronous: the response returns when sync finishes
// (push then pull). On success the result fields populate; on
// failure Error populates and we 200 the rerender so the user
// sees what happened. ConflictError is surfaced verbatim because
// rebase support isn't built yet (sync.GIT.* still pending).
func (s *Server) handleSyncRun(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "web: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	remote := strings.TrimSpace(r.FormValue("remote"))
	if remote == "" {
		remote = "primary"
	}

	d := s.loadSyncBase()
	d.NavSection = "settings"
	d.Remote = remote

	rerender := func(msg string) {
		d.Error = msg
		s.render(w, r, "sync.tmpl", d)
	}

	// Resolve URL from the registry. Web v1 has no --url override
	// equivalent; if a remote isn't registered, point the user at
	// /remotes (or the CLI) rather than letting them type a URL
	// from the browser.
	regPath, err := remotes.DefaultPath()
	if err != nil {
		rerender("resolve remotes registry: " + err.Error())
		return
	}
	reg, err := remotes.Load(regPath)
	if err != nil {
		rerender("load remotes registry: " + err.Error())
		return
	}
	rec, ok := reg.Get(remote)
	if !ok {
		rerender(fmt.Sprintf("remote %q not registered. Add via `rex remote add %s <url>` from the CLI.", remote, remote))
		return
	}

	// Identity (signer) for the handshake. Same path the CLI
	// follows: env override → platform default; mints on first
	// use so a fresh install never fails here.
	storeDir, err := identity.DefaultStoreDir()
	if err != nil {
		rerender("resolve identity store: " + err.Error())
		return
	}
	signer, err := identity.EnsureDefaultStoreSigner(identity.NewStore(storeDir))
	if err != nil {
		rerender("open identity store: " + err.Error())
		return
	}

	client := syncclient.NewClient(rec.URL).WithSigner(signer)
	logPath := filepath.Join(s.opts.WorkspaceRoot, ".rex", "events.log")

	res, err := client.Sync(r.Context(), s.opts.WorkspaceRoot, remote, logPath)
	if err != nil {
		var ce *syncclient.ConflictError
		if errors.As(err, &ce) {
			rerender(fmt.Sprintf(
				"diverged from %s (server head=%s; %d events to rebase). Rebase support is not yet implemented (sync.GIT.*).",
				remote, ce.ServerHead, len(ce.DivergingTail)))
			return
		}
		rerender("sync failed: " + err.Error())
		return
	}

	d.Pushed = res.Push.Accepted
	d.Duplicates = res.Push.Duplicates
	d.Pulled = res.Pulled
	d.HeadID = res.Push.HeadID
	s.render(w, r, "sync.tmpl", d)
}

// loadSyncBase fills the parts of syncResultData that don't
// depend on POST state — the available-remotes list, basic page
// frame.
func (s *Server) loadSyncBase() syncResultData {
	d := syncResultData{pageData: s.basePageData()}
	regPath, err := remotes.DefaultPath()
	if err != nil {
		return d
	}
	reg, err := remotes.Load(regPath)
	if err != nil {
		return d
	}
	for _, r := range reg.List() {
		d.Available = append(d.Available, syncRemoteRow{Name: r.Name, URL: r.URL})
	}
	return d
}
