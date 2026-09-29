package cli

import (
	"os"
	"path/filepath"
)

// forklabHome is $FORKLAB_HOME, else ~/.forklab, made absolute.
func forklabHome() (string, error) {
	if d := os.Getenv("FORKLAB_HOME"); d != "" {
		return filepath.Abs(d)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".forklab"), nil
}
