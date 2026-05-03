package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/asabla/rex/internal/core/specfmt"
)

// metaDirName is the per-workspace metadata directory (overview.NAME.1.2).
const metaDirName = ".rex"

// errNoWorkspace surfaces from findWorkspaceRoot when no .rex/ exists
// at CWD or any ancestor.
var errNoWorkspace = errors.New("no rex workspace found in current directory or any parent")

// findWorkspaceRoot walks up from start looking for a directory that
// contains .rex/. Returns the workspace root (the parent of the .rex
// directory) or errNoWorkspace.
func findWorkspaceRoot(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", start, err)
	}
	dir := abs
	for {
		candidate := filepath.Join(dir, metaDirName)
		info, err := os.Stat(candidate)
		if err == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errNoWorkspace
		}
		dir = parent
	}
}

// specDir returns the canonical specs directory for a workspace root.
func specDir(workspaceRoot string) string {
	return filepath.Join(workspaceRoot, metaDirName, "specs")
}

// listSpecFiles returns every *.yaml file in dir (non-recursive).
// Returns an empty slice + nil error if dir does not exist; that is
// the natural pre-init state of a workspace.
func listSpecFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out, nil
}

// loadWorkspace parses every spec file in the given paths and registers
// each Document with a fresh specfmt.Workspace. Returns the workspace
// plus the list of paths-with-parse-errors so the caller can decide
// how to surface them.
func loadWorkspace(paths []string) (*specfmt.Workspace, []parseError, error) {
	ws := specfmt.NewWorkspace()
	var failures []parseError
	for _, path := range paths {
		doc, err := specfmt.ParseFile(path)
		if err != nil {
			failures = append(failures, parseError{Path: path, Err: err})
			continue
		}
		if err := ws.Add(doc); err != nil {
			failures = append(failures, parseError{Path: path, Err: err})
		}
	}
	return ws, failures, nil
}

type parseError struct {
	Path string
	Err  error
}

// resolveSpecArg interprets a positional argument: a literal path with
// a .yaml suffix is taken as-is; anything else is resolved to
// <workspaceRoot>/.rex/specs/<arg>.yaml. Returns the resolved path or
// an error if the argument shape is ambiguous.
func resolveSpecArg(workspaceRoot, arg string) (string, error) {
	if strings.HasSuffix(arg, ".yaml") || strings.ContainsRune(arg, os.PathSeparator) {
		return arg, nil
	}
	if workspaceRoot == "" {
		return "", fmt.Errorf("cannot resolve %q without a workspace (try `--workspace <dir>` or pass a path)", arg)
	}
	return filepath.Join(specDir(workspaceRoot), arg+".yaml"), nil
}
