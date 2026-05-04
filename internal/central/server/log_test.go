package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/asabla/rex/internal/core/storage/eventlog"
	"github.com/asabla/rex/internal/core/sync/proto"
)

func TestLoggerDefaultsToJSONOnDiscard(t *testing.T) {
	t.Parallel()

	// Default LogConfig: no Output → io.Discard, no Format →
	// json. The logger must still be non-nil and accept calls.
	logger := NewLogger(LogConfig{})
	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}
	logger.Info("smoke", "k", "v") // must not panic
}

func TestLoggerEmitsJSONLines(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := NewLogger(LogConfig{Output: &buf})
	logger.Info("hello", "key", "value", "n", 7)

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("logger emitted nothing")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("not valid JSON: %q (%v)", line, err)
	}
	if got["msg"] != "hello" {
		t.Errorf("msg: %v", got["msg"])
	}
	if got["key"] != "value" {
		t.Errorf("key: %v", got["key"])
	}
	if got["component"] != "rex-central" {
		t.Errorf("expected component prefix; got %v", got["component"])
	}
}

func TestLoggerLevelGate(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	logger := NewLogger(LogConfig{Output: &buf, Level: slog.LevelWarn})
	logger.Info("info-line")
	logger.Warn("warn-line")
	out := buf.String()
	if strings.Contains(out, "info-line") {
		t.Errorf("info should not pass at LevelWarn:\n%s", out)
	}
	if !strings.Contains(out, "warn-line") {
		t.Errorf("warn should pass at LevelWarn:\n%s", out)
	}
}

func TestParseLevelFallsBackToInfo(t *testing.T) {
	t.Parallel()
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo,
		"garbage": slog.LevelInfo,
	}
	for input, want := range cases {
		if got := ParseLevel(input); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", input, got, want)
		}
	}
}

// captureLogs returns a Server with an attached buffer logger so
// tests can assert what got emitted during a request lifecycle.
func captureLogs(t *testing.T) (*Server, *httptest.Server, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := NewLogger(LogConfig{Output: &buf})
	srv, err := New(Options{Logger: logger})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return srv, hs, &buf
}

func TestPushEmitsAcceptedLogLine(t *testing.T) {
	t.Parallel()

	_, hs, buf := captureLogs(t)
	body, _ := json.Marshal(proto.PushRequest{
		Since: "",
		Events: []eventlog.Record{
			{
				ID:          "log-test-1",
				Type:        "test.event",
				Version:     1,
				Actor:       "l-aaaaaaaaaaaaaaaa",
				WorkspaceID: "ws-log",
				Payload:     []byte(`{"k":"v"}`),
			},
		},
	})
	resp, err := http.Post(hs.URL+"/sync/events", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}

	if !strings.Contains(buf.String(), `"msg":"push accepted"`) {
		t.Fatalf("missing accepted log line:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `"events":1`) {
		t.Errorf("missing events count in log:\n%s", buf.String())
	}
}

func TestAuthVerifyDoesNotLogTokenValue(t *testing.T) {
	t.Parallel()

	// Build a server with a keystore so /auth/verify can
	// succeed end-to-end. We need a real ed25519 keypair so
	// the signature passes verification.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	ks := NewKeystore()
	fp, err := ks.Add("test-key", pub)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	var buf bytes.Buffer
	logger := NewLogger(LogConfig{Output: &buf})
	srv, err := New(Options{Logger: logger, Keystore: ks})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// Step 1: POST /auth/challenge to get a challenge id.
	chResp, err := http.Post(hs.URL+"/auth/challenge", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST challenge: %v", err)
	}
	defer chResp.Body.Close()
	var ch proto.AuthChallengeResponse
	if err := json.NewDecoder(chResp.Body).Decode(&ch); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}

	// Step 2: sign the canonical challenge bytes with the
	// private key, POST to /auth/verify.
	canonical, _ := json.Marshal(proto.ChallengeSigningInput{
		Version:  proto.AuthSigningVersion,
		Nonce:    ch.Nonce,
		Hostname: ch.Hostname,
		Scope:    "sync",
	})
	sig := ed25519.Sign(priv, canonical)
	verifyBody, _ := json.Marshal(proto.AuthVerifyRequest{
		ChallengeID: ch.ChallengeID,
		Fingerprint: fp.String(),
		Signature:   hex.EncodeToString(sig),
		Scope:       "sync",
	})
	vResp, err := http.Post(hs.URL+"/auth/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("POST verify: %v", err)
	}
	defer vResp.Body.Close()
	if vResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(vResp.Body)
		t.Fatalf("verify status: %d body=%s", vResp.StatusCode, body)
	}
	var verifyResp proto.AuthVerifyResponse
	if err := json.NewDecoder(vResp.Body).Decode(&verifyResp); err != nil {
		t.Fatalf("decode verify response: %v", err)
	}
	if verifyResp.AccessToken == "" {
		t.Fatal("expected an access token")
	}

	// HEALTH.3: the issued token VALUE must never appear in
	// any log line. Hex tokens are 64 chars; the search is
	// straightforward.
	logs := buf.String()
	if strings.Contains(logs, verifyResp.AccessToken) {
		t.Errorf("access token leaked into logs:\n%s", logs)
	}
	// And the request signature hex must not leak either.
	if strings.Contains(logs, hex.EncodeToString(sig)) {
		t.Errorf("signature hex leaked into logs:\n%s", logs)
	}
	// We DO want the fingerprint logged — that's not a secret.
	if !strings.Contains(logs, fp.String()) {
		t.Errorf("fingerprint should appear in audit logs: %s", logs)
	}
	// And the issuance must be recorded.
	if !strings.Contains(logs, `"msg":"auth verify: token issued"`) {
		t.Errorf("missing token-issued log:\n%s", logs)
	}
}
