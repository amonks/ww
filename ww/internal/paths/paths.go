package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkingDir returns the current working directory.
func WorkingDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	return normalizeWorkingDir(cwd), nil
}

// NormalizePath normalizes a file path by removing macOS-specific prefixes
// like /private when the trimmed path refers to the same location.
// This handles the common case where /var -> /private/var on macOS.
//
// The function works even if the path doesn't exist yet by checking if the
// parent directories refer to the same location.
func NormalizePath(path string) string {
	trimmed := strings.TrimPrefix(path, "/private")
	if trimmed == path {
		return path
	}

	// Find the longest existing prefix to verify the paths are equivalent
	original := path
	for original != "/" && original != "." {
		originalInfo, err := os.Stat(original)
		if err != nil {
			original = filepath.Dir(original)
			trimmed = filepath.Dir(trimmed)
			continue
		}
		trimmedInfo, err := os.Stat(trimmed)
		if err != nil {
			// Trimmed path doesn't exist at this level, can't verify
			return path
		}
		if os.SameFile(originalInfo, trimmedInfo) {
			// Parent paths are equivalent, so we can safely use trimmed version
			return strings.TrimPrefix(path, "/private")
		}
		return path
	}
	return path
}

func normalizeWorkingDir(cwd string) string {
	return NormalizePath(cwd)
}

// DefaultStateDir returns the default ww state directory.
func DefaultStateDir() (string, error) {
	return defaultHomeDirPath(".local", "state", "ww")
}

// DefaultDBPath returns the default SQLite state database path.
func DefaultDBPath() (string, error) {
	stateDir, err := DefaultStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "state.db"), nil
}

// DefaultWorkspacesDir returns the default ww workspaces directory.
func DefaultWorkspacesDir() (string, error) {
	return defaultHomeDirPath(".local", "share", "ww", "workspaces")
}

// HomeDir returns the current user's home directory.
func HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home directory: %w", err)
	}
	return home, nil
}

func defaultHomeDirPath(parts ...string) (string, error) {
	home, err := HomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(append([]string{home}, parts...)...), nil
}

// ResolveWithDefault returns the override value if non-empty, otherwise calls
// the default function to get a fallback value.
func ResolveWithDefault(override string, defaultFn func() (string, error)) (string, error) {
	if override != "" {
		return override, nil
	}
	return defaultFn()
}
