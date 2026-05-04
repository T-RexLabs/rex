package snapshot

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// copyTree recursively copies src into dst. Both must be regular
// paths (no symlinks); copyTree refuses to traverse through symlinks
// to avoid following an unexpected reference out of the workspace.
//
// dst is created if it does not exist; existing entries inside dst
// are not removed before copying — callers needing replace semantics
// should remove dst first.
func copyTree(src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("snapshot: lstat %s: %w", src, err)
	}
	if srcInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("snapshot: refuse to traverse symlink %s", src)
	}
	if !srcInfo.IsDir() {
		return copyFile(src, dst, srcInfo.Mode().Perm())
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("snapshot: mkdir %s: %w", dst, err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("snapshot: read %s: %w", src, err)
	}
	for _, e := range entries {
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies one regular file. perm sets the destination mode.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("snapshot: open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("snapshot: create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("snapshot: copy %s -> %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("snapshot: close %s: %w", dst, err)
	}
	return nil
}
