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
	s.render(w, r, "home.tmpl", data)
}

// handleSpecsList renders /specs.
func (s *Server) handleSpecsList(w http.ResponseWriter, r *http.Request) {
	data, err := loadSpecsList(s.opts)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "specs_list.tmpl", data)
}

// handleSpecDetail renders /specs/<id>.
func (s *Server) handleSpecDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tab := r.URL.Query().Get("tab")
	data, ok, err := loadSpecDetail(s.opts, id, tab)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, r, "spec_detail.tmpl", data)
}

// handleRunsList renders /runs.
func (s *Server) handleRunsList(w http.ResponseWriter, r *http.Request) {
	data, err := loadRunsList(s.opts)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, r, "runs_list.tmpl", data)
}

// handleRunDetail renders /runs/<id>.
func (s *Server) handleRunDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	data, ok, err := loadRunDetail(s.opts, id)
	if err != nil {
		http.Error(w, "web: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
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
