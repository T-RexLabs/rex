package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRunInputEndpointEnqueuesReply(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-input")
	s, err := New(Options{WorkspaceRoot: root})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	s.interactions.register("run-1", true)
	t.Cleanup(func() { s.interactions.unregister("run-1") })

	hs := httptest.NewServer(s.Handler())
	defer hs.Close()

	form := url.Values{"text": {"hello from web"}, "action": {"send"}}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/runs/run-1/input", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /runs/run-1/input: %v", err)
	}
	if resp.StatusCode != http.StatusSeeOther {
		body := readBody(t, resp)
		t.Fatalf("status: got %d body=%s", resp.StatusCode, body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := s.interactions.awaitInput(ctx, "run-1")
	if err != nil {
		t.Fatalf("awaitInput: %v", err)
	}
	if got != "hello from web" {
		t.Fatalf("input: got %q", got)
	}
}

func TestRunInputEndpointRequiresActiveInteraction(t *testing.T) {
	t.Parallel()

	root := initWorkspace(t, "ws-input-gone")
	hs := newTestServer(t, root)

	form := url.Values{"text": {"hello"}, "action": {"send"}}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/runs/missing/input", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /runs/missing/input: %v", err)
	}
	if resp.StatusCode != http.StatusGone {
		body := readBody(t, resp)
		t.Fatalf("status: got %d body=%s", resp.StatusCode, body)
	}
}
