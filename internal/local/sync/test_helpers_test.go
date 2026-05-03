package sync

import (
	"os"
	"path/filepath"
)

// mkdirAll is a thin wrapper used by tests to fabricate a path
// collision (a directory where a file is expected).
func mkdirAll(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(path, 0o755)
}
