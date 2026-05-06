package web

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var errNoActiveInteraction = errors.New("web: no active interaction for run")

type permissionResolution struct {
	Granted bool
	Note    string
}

type inputMessage struct {
	Text string
	End  bool
}

type runInteraction struct {
	acceptsInput bool
	inputCh      chan inputMessage
	// initialPrompt is the user's first prompt for harness runs.
	// Stored so the run-detail page can render the user message
	// optimistically (before the harness echoes it back as a
	// user_message_chunk). Cleared as soon as the run unregisters.
	initialPrompt string

	mu          sync.Mutex
	permissions map[string]chan permissionResolution
}

type runInteractionHub struct {
	mu   sync.Mutex
	runs map[string]*runInteraction
}

func newRunInteractionHub() *runInteractionHub {
	return &runInteractionHub{runs: make(map[string]*runInteraction)}
}

func (h *runInteractionHub) register(runID string, acceptsInput bool) {
	h.registerWithPrompt(runID, acceptsInput, "")
}

func (h *runInteractionHub) registerWithPrompt(runID string, acceptsInput bool, initialPrompt string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs[runID] = &runInteraction{
		acceptsInput:  acceptsInput,
		inputCh:       make(chan inputMessage, 16),
		permissions:   make(map[string]chan permissionResolution),
		initialPrompt: initialPrompt,
	}
}

// initialPrompt returns the first prompt registered for a still-live
// harness run, or "" when none is recorded (run completed, never
// started, shell run, …). Used by the run-detail page to render an
// optimistic user message before the harness echoes the prompt back.
func (h *runInteractionHub) initialPrompt(runID string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	run, ok := h.runs[runID]
	if !ok {
		return ""
	}
	return run.initialPrompt
}

func (h *runInteractionHub) unregister(runID string) {
	h.mu.Lock()
	run, ok := h.runs[runID]
	if ok {
		delete(h.runs, runID)
	}
	h.mu.Unlock()
	if !ok {
		return
	}
	close(run.inputCh)
	run.mu.Lock()
	for id, ch := range run.permissions {
		close(ch)
		delete(run.permissions, id)
	}
	run.mu.Unlock()
}

func (h *runInteractionHub) has(runID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.runs[runID]
	return ok
}

func (h *runInteractionHub) acceptsInput(runID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	run, ok := h.runs[runID]
	if !ok {
		return false
	}
	return run.acceptsInput
}

func (h *runInteractionHub) submitInput(runID, text string, end bool) error {
	run, err := h.lookup(runID)
	if err != nil {
		return err
	}
	if !run.acceptsInput {
		return errNoActiveInteraction
	}
	msg := inputMessage{Text: text, End: end}
	select {
	case run.inputCh <- msg:
		return nil
	default:
		return fmt.Errorf("web: input queue full for run %q", runID)
	}
}

func (h *runInteractionHub) awaitInput(ctx context.Context, runID string) (string, error) {
	run, err := h.lookup(runID)
	if err != nil {
		return "", err
	}
	if !run.acceptsInput {
		return "", errNoActiveInteraction
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case msg, ok := <-run.inputCh:
		if !ok {
			return "", errNoActiveInteraction
		}
		if msg.End {
			return "", nil
		}
		return msg.Text, nil
	}
}

func (h *runInteractionHub) waitPermission(ctx context.Context, runID, requestID string) (permissionResolution, error) {
	run, err := h.lookup(runID)
	if err != nil {
		return permissionResolution{}, err
	}
	ch := make(chan permissionResolution, 1)
	run.mu.Lock()
	run.permissions[requestID] = ch
	run.mu.Unlock()

	defer func() {
		run.mu.Lock()
		delete(run.permissions, requestID)
		run.mu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return permissionResolution{}, ctx.Err()
	case res, ok := <-ch:
		if !ok {
			return permissionResolution{}, errNoActiveInteraction
		}
		return res, nil
	}
}

func (h *runInteractionHub) resolvePermission(runID, requestID string, res permissionResolution) bool {
	run, err := h.lookup(runID)
	if err != nil {
		return false
	}
	run.mu.Lock()
	ch, ok := run.permissions[requestID]
	if ok {
		delete(run.permissions, requestID)
	}
	run.mu.Unlock()
	if !ok {
		return false
	}
	ch <- res
	close(ch)
	return true
}

func (h *runInteractionHub) lookup(runID string) (*runInteraction, error) {
	h.mu.Lock()
	run, ok := h.runs[runID]
	h.mu.Unlock()
	if !ok {
		return nil, errNoActiveInteraction
	}
	return run, nil
}
