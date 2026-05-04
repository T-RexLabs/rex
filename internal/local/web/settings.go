package web

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"

	"github.com/asabla/rex/internal/core/identity"
	"github.com/asabla/rex/internal/local/remotes"
)

// settingsData backs settings.tmpl. Read-only by design: the
// settings surface in v1 surfaces state for inspection plus
// explicit "edit via X" hints. Mutating from a single web form
// would mean writing to .rex/workspace.yaml, ~/.config/rex/*,
// and per-workspace config files from inside an HTTP request —
// possible but the failure modes (partial writes, schema
// mismatches with future CLI versions) aren't worth the speed
// of one extra mouse click. Edits go through the CLI / a text
// editor; this page tells you where.
type settingsData struct {
	pageData

	// Workspace
	WorkspaceID        string
	WorkspaceName      string
	WorkspaceState     string
	WorkspacePath      string
	WorkspaceCreatedAt string
	WorkspaceYAMLPath  string

	// Identity
	IdentityHandle      string
	IdentityFingerprint string
	IdentityPubPEM      string
	IdentityStoreDir    string
	IdentityCount       int
	IdentityErr         string // surfaced when the store can't be opened

	// Remotes
	RemotesPath  string
	RemotesCount int

	// Hooks
	WorkspaceHooksDir   string
	WorkspaceHookCount  int
	GlobalHooksDir      string
	GlobalHookCount     int
}

// handleSettings renders /settings.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	d := settingsData{pageData: s.basePageData()}
	d.NavSection = "settings"

	// Workspace metadata.
	if d.Workspace != nil {
		d.WorkspaceID = d.Workspace.ID
		d.WorkspaceName = d.Workspace.Name
		d.WorkspaceState = d.Workspace.State
		d.WorkspaceCreatedAt = d.Workspace.CreatedAt
	}
	d.WorkspacePath = s.opts.WorkspaceRoot
	d.WorkspaceYAMLPath = filepath.Join(s.opts.WorkspaceRoot, ".rex", "workspace.yaml")

	// Identity. EnsureDefaultStoreSigner mints a keypair on first
	// call so the page never fails on a fresh install.
	storeDir, err := identity.DefaultStoreDir()
	if err != nil {
		d.IdentityErr = "resolve identity store: " + err.Error()
	} else {
		d.IdentityStoreDir = storeDir
		store := identity.NewStore(storeDir)
		signer, err := identity.EnsureDefaultStoreSigner(store)
		if err != nil {
			d.IdentityErr = "open identity store: " + err.Error()
		} else {
			d.IdentityHandle = string(signer.Handle())
			d.IdentityFingerprint = hexFingerprint(signer.PublicKey())
			d.IdentityPubPEM = pubKeyPEM(signer.PublicKey())
		}
		if handles, herr := store.List(); herr == nil {
			d.IdentityCount = len(handles)
		}
	}

	// Remotes.
	if rp, err := remotes.DefaultPath(); err == nil {
		d.RemotesPath = rp
		if reg, err := remotes.Load(rp); err == nil {
			d.RemotesCount = len(reg.List())
		}
	}

	// Hooks.
	d.WorkspaceHooksDir = filepath.Join(s.opts.WorkspaceRoot, ".rex", "hooks")
	d.WorkspaceHookCount = countExecutableFiles(d.WorkspaceHooksDir)
	if cfg, err := os.UserConfigDir(); err == nil {
		d.GlobalHooksDir = filepath.Join(cfg, "rex", "hooks")
		d.GlobalHookCount = countExecutableFiles(d.GlobalHooksDir)
	}

	s.render(w, r, "settings.tmpl", d)
}

// hexFingerprint returns the first 8 bytes of the public key in
// hex — same shape the CLI prints for `rex identity show`. Lets
// the user paste the value into a remote-side authorized-keys
// or config without having to look up the format.
func hexFingerprint(pub ed25519.PublicKey) string {
	if len(pub) < 8 {
		return ""
	}
	return hex.EncodeToString(pub[:8])
}

// pubKeyPEM returns the public key in PEM form so the user can
// copy-paste it into a remote node's identity registry.
func pubKeyPEM(pub ed25519.PublicKey) string {
	if len(pub) == 0 {
		return ""
	}
	block := &pem.Block{Type: "ED25519 PUBLIC KEY", Bytes: pub}
	return string(pem.EncodeToMemory(block))
}

// countExecutableFiles counts non-directory entries in dir whose
// owner-execute bit is set. Mirrors what hooks.Dispatcher's
// resolveHooks treats as runnable. Returns 0 when dir doesn't
// exist (the natural pre-init state).
func countExecutableFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode().Perm()&0o100 != 0 {
			n++
		}
	}
	return n
}
