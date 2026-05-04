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
