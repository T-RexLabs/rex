package primshell

import "os"

// osEnviron exists in its own file so the test build can stub it if
// ever needed; today it is a thin wrapper.
func osEnviron() []string {
	return os.Environ()
}
