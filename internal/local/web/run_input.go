package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// handleRunInput accepts user replies for an active interactive run.
// action=send enqueues text as the next user turn; action=end closes
// the interaction loop for this run.
func (s *Server) handleRunInput(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "web: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	action := strings.TrimSpace(r.FormValue("action"))
	text := strings.TrimSpace(r.FormValue("text"))
	end := action == "end"
	if !end && text == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	if err := s.interactions.submitInput(runID, text, end); err != nil {
		status := http.StatusConflict
		if errors.Is(err, errNoActiveInteraction) {
			status = http.StatusGone
		}
		http.Error(w, fmt.Sprintf("web: submit input: %v", err), status)
		return
	}
	http.Redirect(w, r, "/runs/"+runID, http.StatusSeeOther)
}
