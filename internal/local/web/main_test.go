package web

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asabla/rex/internal/local/remotes"
)

// TestMain isolates the package's tests from the dev machine's
// real ~/.config/rex/ state: identity store + remotes registry
// both point at a session-scoped tempdir so a test that triggers
// the sync code path doesn't see whichever "primary" remote a
// `make web-dev` flow left behind.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rex-web-cfg-*")
	if err != nil {
		panic("rex web tests: cannot mkdir temp config dir: " + err.Error())
	}
	if err := os.Setenv("REX_IDENTITY_DIR", filepath.Join(dir, "identity")); err != nil {
		panic("rex web tests: cannot set REX_IDENTITY_DIR: " + err.Error())
	}
	if err := os.Setenv(remotes.EnvPath, filepath.Join(dir, "remotes.toml")); err != nil {
		panic("rex web tests: cannot set " + remotes.EnvPath + ": " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
