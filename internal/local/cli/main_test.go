package cli

import (
	"os"
	"testing"
)

// TestMain points REX_IDENTITY_DIR at a session-scoped temp dir so
// tests in this package never write into the user's real
// ~/.config/rex/identity/. Tests that explicitly set --identity-dir
// override this via flag precedence in loadOrCreateDefaultSigner.
//
// Cleanup runs after m.Run; a panic during a test still calls
// os.Exit via t.Fatal, so the deferred RemoveAll won't run on
// catastrophic failure — but the temp dir is under TMPDIR and the
// OS reaps it eventually. Acceptable for a test fixture.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rex-cli-id-*")
	if err != nil {
		panic("rex cli tests: cannot mkdir temp identity dir: " + err.Error())
	}
	if err := os.Setenv(envIdentityDir, dir); err != nil {
		panic("rex cli tests: cannot set " + envIdentityDir + ": " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
