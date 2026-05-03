//go:build !unix

package eventlog

import (
	"errors"
	"os"
)

// errLockUnsupported is returned on non-unix platforms until a Windows
// implementation lands. Rex v1 targets macOS and Linux first; Windows
// support is a deliberate gap, surfaced loudly rather than silently
// degrading to no-locking (which would break storage.EVENTS.4).
var errLockUnsupported = errors.New("eventlog: file locking not implemented on this platform")

func acquireExclusiveLock(*os.File) error { return errLockUnsupported }
func releaseLock(*os.File) error          { return errLockUnsupported }
