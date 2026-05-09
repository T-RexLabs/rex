package web

import "net/http"

// handleHome renders the workspace overview at "/".
func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := loadHomeData(s.opts)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data.NavSection = "home"
	s.render(w, r, "home.tmpl", data)
}

// handleSpecsList renders /specs.
func (s *Server) handleSpecsList(w http.ResponseWriter, r *http.Request) {
	data, err := loadSpecsList(s.opts)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data.NavSection = "specs"
	s.render(w, r, "specs_list.tmpl", data)
}

// handleSpecDetail renders /specs/<id>.
func (s *Server) handleSpecDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tab := r.URL.Query().Get("tab")
	data, ok, err := loadSpecDetail(s.opts, id, tab, s.highlighter)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	data.Harnesses = s.harnesses.snapshot()
	data.NavSection = "specs"
	s.render(w, r, "spec_detail.tmpl", data)
}

// handleRunsList renders /runs. ?spec=<ACID-or-spec-id> filters the
// list to runs whose RunStartedEvent recorded a matching reference
// (execution.RUN.1.2).
func (s *Server) handleRunsList(w http.ResponseWriter, r *http.Request) {
	specFilter := r.URL.Query().Get("spec")
	data, err := loadRunsListFiltered(s.opts, specFilter)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data.NavSection = "runs"
	s.render(w, r, "runs_list.tmpl", data)
}

// handleRunDetail renders /runs/<id>. Honours ?debug=1 to surface
// raw frame JSON alongside the typed view.
func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	data, ok, err := loadRunDetail(s.opts, id, s.highlighter)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	data.NavSection = "runs"
	data.AcceptsInput = s.interactions.acceptsInput(id)
	data.Debug = r.URL.Query().Get("debug") == "1"
	if !data.HasUserMessage {
		data.OptimisticPrompt = s.interactions.initialPrompt(id)
	}
	s.render(w, r, "run_detail.tmpl", data)
}

// handleRunStream is the SSE endpoint used by the run detail page
// (web-ui.LIVE.1, .1-note).
func (s *Server) handleRunStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	s.streamRunEvents(w, r, id)
}

// handleAudit renders /audit.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := auditDefaultLimit
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, ok := parsePositiveInt(v); ok {
			limit = parsed
		}
	}
	data, err := loadAuditRows(s.opts, limit)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data.NavSection = "audit"
	s.render(w, r, "audit.tmpl", data)
}

// handleRemotes renders /remotes (read-only).
func (s *Server) handleRemotes(w http.ResponseWriter, r *http.Request) {
	data, err := loadRemotesData(s.opts)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	data.NavSection = "remotes"
	s.render(w, r, "remotes.tmpl", data)
}

// handleChromaCSS serves the chroma stylesheet generated at startup.
// Static-served because chroma's CSS is a function of the chosen
// style and the formatter options, not a static file we can ship
// alongside app.css. The headers tell browsers to cache it for the
// process lifetime — the bytes don't change without a binary
// rebuild.
func (s *Server) handleChromaCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write([]byte(s.highlighter.HighlightCSS()))
}

// parsePositiveInt is a small stdlib-free integer parser used by
// the audit page's ?n= query. Returns (n, true) for positive
// integers up to 9999; out-of-range and malformed inputs return
// false so the caller falls back to the default.
func parsePositiveInt(s string) (int, bool) {
	if s == "" || len(s) > 4 {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}
