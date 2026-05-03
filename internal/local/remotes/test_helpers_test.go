package remotes

import "os"

func readBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}
