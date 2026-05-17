package sync

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/asabla/rex/internal/core/storage/synccat"
)

// GitEntry is one file in the workspace's git-merged content set,
// ready for shipment to the central via /sync/git. Path is the
// .rex/-relative path the central node indexes by (e.g.
// "workspace.yaml", "specs/foo.yaml", "specs/_proposed/bar.yaml").
type GitEntry struct {
	Path    string
	Content string
}

// WalkWorkspaceGit returns every git-merged file under the
// workspace's .rex/ tree, suitable for bulk-pushing via
// /sync/git. Hidden files (dotfiles) are skipped; non-regular
// entries (sockets, symlinks pointing outside .rex/) are skipped.
// Derived and event-sourced paths are skipped via synccat.
//
// Returns an empty slice (not an error) when the workspace has
// no .rex/ directory.
func WalkWorkspaceGit(workspaceRoot string) ([]GitEntry, error) {
	if workspaceRoot == "" {
		return nil, errors.New("sync: WalkWorkspaceGit requires workspace root")
	}
	rexDir := filepath.Join(workspaceRoot, ".rex")
	info, err := os.Stat(rexDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("sync: stat %s: %w", rexDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("sync: %s is not a directory", rexDir)
	}
	var out []GitEntry
	walkErr := filepath.WalkDir(rexDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(rexDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Skip hidden files (.DS_Store etc) and anything the
		// synccat registry doesn't recognise as a git-merged path.
		base := filepath.Base(rel)
		if strings.HasPrefix(base, ".") {
			return nil
		}
		cat, ok := synccat.Categorize(rel)
		if !ok || cat != synccat.CategoryGitMerged {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("sync: read %s: %w", path, err)
		}
		out = append(out, GitEntry{Path: rel, Content: string(body)})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return out, nil
}
