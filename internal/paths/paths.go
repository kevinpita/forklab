// Package paths resolves forklab's on-disk locations.
package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// Home is $FORKLAB_HOME, else ~/.forklab, made absolute.
func Home() (string, error) {
	if d := os.Getenv("FORKLAB_HOME"); d != "" {
		return filepath.Abs(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".forklab"), nil
}

// UserPath expands ~/ and resolves relative paths against the working directory.
func UserPath(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	return filepath.Abs(path)
}
