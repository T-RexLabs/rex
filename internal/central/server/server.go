package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/core/sync/proto"
)

// Options configure New.
type Options struct {
	// Keypair is the central node's signing keypair. Its
	// fingerprint surfaces on /sync/state. New generates one if
	// nil; production deployments should always supply persisted
	// material.
	Keypair *identity.Keypair
	// Store is the event store backing /sync/events. New creates
	// an in-memory Store if nil.
	Store *Store
}

// Server bundles the central-node HTTP surface and the state it
// serves. A Server is safe for concurrent use; the underlying Store
// owns its own mutex.
type Server struct {
	store    *Store
	keypair  identity.Keypair
	actor    identity.Actor
	mux      *http.ServeMux
	stateRes proto.StateResponse
}

// New returns a Server ready to serve via Handler().
func New(opts Options) (*Server, error) {
	store := opts.Store
	if store == nil {
		store = NewStore()
	}
	var kp identity.Keypair
	if opts.Keypair == nil {
		generated, err := identity.GenerateKeypair("rex-central", nil)
		if err != nil {
			return nil, fmt.Errorf("server: generate keypair: %w", err)
		}
		kp = generated
	} else {
		kp = *opts.Keypair
	}
	if !identity.IsValidHandle(string(kp.Handle)) {
		return nil, fmt.Errorf("server: invalid handle %q", kp.Handle)
	}

	central := identity.Actor{Role: identity.RoleCentral, Fingerprint: kp.Fingerprint()}
	s := &Server{
		store:   store,
		keypair: kp,
		actor:   central,
		mux:     http.NewServeMux(),
		stateRes: proto.StateResponse{
			Fingerprint:     kp.Fingerprint().String(),
			Actor:           central.String(),
			ProtocolVersion: proto.ProtocolVersion,
		},
	}
	s.mux.HandleFunc("/sync/state", s.handleState)
	s.mux.HandleFunc("/sync/events", s.handleEvents)
	return s, nil
}

// Handler returns the HTTP handler the server registered.
func (s *Server) Handler() http.Handler { return s.mux }

// Actor returns the central node's actor string. Useful when the
// caller (say, a test) wants to verify (HLC, actor) ordering with
// no second guess on what the server's actor looks like.
func (s *Server) Actor() identity.Actor { return s.actor }

// Store returns the underlying event store. Tests use this to
// pre-seed records.
func (s *Server) Store() *Store { return s.store }

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "GET only")
		return
	}
	res := s.stateRes
	res.HeadID = s.store.Head()
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleEventsGet(w, r)
	case http.MethodPost:
		s.handleEventsPost(w, r)
	default:
		writeError(w, http.StatusMethodNotAllowed, proto.ErrCodeBadRequest, "GET or POST only")
	}
}

// handleEventsPost implements sync.API.2 — accept a contiguous
// batch and acknowledge with the new HEAD. Idempotent: events the
// server already has are skipped (sync.API.6).
func (s *Server) handleEventsPost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "read body: "+err.Error())
		return
	}
	var req proto.PushRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "decode request: "+err.Error())
		return
	}

	// Conflict detection: the client's `since` must match our
	// current HEAD. If it does not, return 409 with the diverging
	// tail so the client can rebase.
	currentHead := s.store.Head()
	if req.Since != currentHead {
		tail, err := s.store.Since(req.Since)
		if err != nil {
			// The client's cursor is unknown to us; surface as a
			// conflict with empty tail so the client knows to
			// resync from scratch.
			writeJSON(w, http.StatusConflict, proto.ConflictResponse{
				ServerHead:    currentHead,
				DivergingTail: nil,
			})
			return
		}
		writeJSON(w, http.StatusConflict, proto.ConflictResponse{
			ServerHead:    currentHead,
			DivergingTail: tail,
		})
		return
	}

	res := proto.PushResponse{}
	for _, rec := range req.Events {
		if err := validatePushRecord(rec); err != nil {
			writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, err.Error())
			return
		}
		added, err := s.store.Append(rec)
		if err != nil {
			writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
			return
		}
		if added {
			res.Accepted++
		} else {
			res.Duplicates++
		}
	}
	res.HeadID = s.store.Head()
	writeJSON(w, http.StatusOK, res)
}

// handleEventsGet implements sync.API.3 — stream events past a
// cursor as Server-Sent Events.
func (s *Server) handleEventsGet(w http.ResponseWriter, r *http.Request) {
	cursor := r.URL.Query().Get("since")
	tail, err := s.store.Since(cursor)
	if err != nil {
		if errors.Is(err, ErrUnknownCursor) {
			writeError(w, http.StatusBadRequest, proto.ErrCodeBadRequest, "unknown cursor")
			return
		}
		writeError(w, http.StatusInternalServerError, proto.ErrCodeServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	for _, rec := range tail {
		body, err := json.Marshal(proto.SSEFrame{Record: rec})
		if err != nil {
			// Mid-stream: log via SSE comment and bail; cannot
			// write a non-2xx now.
			fmt.Fprintf(w, ": encode error %s\n\n", err.Error())
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", body)
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Mark end-of-stream with an SSE comment so clients can detect
	// it; standard SSE has no built-in EOF event, but a `: end`
	// comment is conventional.
	fmt.Fprintln(w, ": end")
	if flusher != nil {
		flusher.Flush()
	}
}

// validatePushRecord rejects records the server cannot store. For
// the in-process skeleton we only check the trivially-required
// fields; real signature verification (sync.SEC.1) lands once
// identity-and-trust has signed events.
func validatePushRecord(rec eventlog.Record) error {
	if rec.ID == "" {
		return errors.New("event record missing id")
	}
	if rec.Type == "" {
		return errors.New("event record missing type")
	}
	if rec.WorkspaceID == "" {
		return errors.New("event record missing workspace_id")
	}
	if rec.Actor == "" {
		return nil // tolerated until identity.Actor flows through writes
	}
	if !strings.HasPrefix(rec.Actor, "c-") && !strings.HasPrefix(rec.Actor, "l-") {
		return fmt.Errorf("event record actor %q lacks role prefix", rec.Actor)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	_ = enc.Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(proto.MarshalErrorResponse(code, message))
}
