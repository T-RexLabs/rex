package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/asabla/rex/internal/core/event"
	"github.com/asabla/rex/internal/core/runner"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/local/runtask"
)

// handleRunPermission writes a permission.granted or
// permission.denied event in response to a user pressing
// approve / deny on the run-detail page (web-ui.LIVE.3).
//
// Form fields:
//
//	request_id  (required) — matches the request_id on the
//	                         corresponding permission.requested.
//	decision    (required) — "grant" | "deny".
//	note        (optional) — operator's note; for deny this maps
//	                         to the resolution event's Reason
//	                         field, for grant it maps to Note.
//
// Idempotency: a request_id that already has a resolution event
// returns 409 Conflict — the runner only honours the first
// resolution and any later ones would be confusing.
//
// Wired-into-runner: interactive harness runs resolve
// session/request_permission through this endpoint. Tests also seed
// permission events directly to exercise the UI in isolation.
func (s *Server) handleRunPermission(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if runID == "" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "web: parse form: "+err.Error(), http.StatusBadRequest)
		return
	}
	reqID := strings.TrimSpace(r.FormValue("request_id"))
	decision := strings.TrimSpace(r.FormValue("decision"))
	note := strings.TrimSpace(r.FormValue("note"))
	if reqID == "" {
		http.Error(w, "request_id is required", http.StatusBadRequest)
		return
	}
	if decision != "grant" && decision != "deny" {
		http.Error(w, "decision must be 'grant' or 'deny'", http.StatusBadRequest)
		return
	}

	// Locate the matching permission.requested event so we can
	// stamp the response with the same node_id and refuse a
	// double-resolve.
	req, alreadyResolved, err := findPermissionRequest(s.opts.WorkspaceRoot, runID, reqID)
	if err != nil {
		http.Error(w, "web: scan events.log: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if req == nil {
		http.Error(w, fmt.Sprintf("no permission.requested with request_id=%q on run %q", reqID, runID), http.StatusNotFound)
		return
	}
	if alreadyResolved {
		http.Error(w, fmt.Sprintf("request_id=%q is already resolved", reqID), http.StatusConflict)
		return
	}

	// Open the workspace via runtask so the writer/hooks/index
	// fan-out matches the rest of the runner's writes — a
	// permission resolution is a real event and downstream
	// observers (search, hooks) should see it.
	ws, err := runtask.Open(s.opts.WorkspaceRoot)
	if err != nil {
		http.Error(w, "web: open workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer ws.Close()

	now := time.Now().UTC()
	approver := "" // v1 has no auth context — leave blank; the runner treats empty as "local operator".

	var (
		evType  string
		payload []byte
	)
	switch decision {
	case "grant":
		ev := runner.PermissionGrantedEvent{
			RunID:     runID,
			NodeID:    req.NodeID,
			RequestID: reqID,
			Approver:  approver,
			GrantedAt: now,
			Note:      note,
		}
		payload, _ = json.Marshal(ev)
		evType = runner.EventTypePermissionGranted
	case "deny":
		ev := runner.PermissionDeniedEvent{
			RunID:     runID,
			NodeID:    req.NodeID,
			RequestID: reqID,
			Approver:  approver,
			DeniedAt:  now,
			Reason:    note,
		}
		payload, _ = json.Marshal(ev)
		evType = runner.EventTypePermissionDenied
	}

	if _, err := ws.Writer.Append(evType, runner.EventVersion, payload); err != nil {
		http.Error(w, "web: append event: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if decision == "grant" {
		s.interactions.resolvePermission(runID, reqID, permissionResolution{Granted: true, Note: note})
	} else {
		s.interactions.resolvePermission(runID, reqID, permissionResolution{Granted: false, Note: note})
	}
	http.Redirect(w, r, "/runs/"+runID+"#"+reqID, http.StatusSeeOther)
}

// findPermissionRequest walks events.log looking for the
// permission.requested event matching (runID, requestID). Also
// reports whether a matching .granted or .denied event already
// exists, so the caller can refuse to double-resolve.
func findPermissionRequest(workspaceRoot, runID, requestID string) (*runner.PermissionRequestedEvent, bool, error) {
	logPath := filepath.Join(workspaceRoot, ".rex", "events.log")
	r, err := eventlog.OpenReader(logPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer r.Close()

	reg := event.NewRegistry()
	runner.RegisterEvents(reg)

	var req *runner.PermissionRequestedEvent
	resolved := false
	for {
		rec, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, false, err
		}
		decoded, derr := reg.Decode(event.Envelope{
			Type: rec.Type, Version: rec.Version, Payload: rec.Payload,
		})
		if derr != nil {
			continue
		}
		switch ev := decoded.(type) {
		case runner.PermissionRequestedEvent:
			if ev.RunID == runID && ev.RequestID == requestID {
				copy := ev
				req = &copy
			}
		case runner.PermissionGrantedEvent:
			if ev.RunID == runID && ev.RequestID == requestID {
				resolved = true
			}
		case runner.PermissionDeniedEvent:
			if ev.RunID == runID && ev.RequestID == requestID {
				resolved = true
			}
		}
	}
	return req, resolved, nil
}
