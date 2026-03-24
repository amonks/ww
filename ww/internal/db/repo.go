package db

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"monks.co/ww/ww/internal/paths"
)

// ErrRepoPathNotFound indicates a workspace is tracked but missing repo info.
var ErrRepoPathNotFound = errors.New("repo source path not found")

// SanitizeRepoName converts a file path to a safe repo name.
func SanitizeRepoName(path string) string {
	// Expand ~ if present
	if strings.HasPrefix(path, "~/") {
		home, err := paths.HomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		} else {
			path = path[2:]
		}
	}

	// Remove leading slash
	path = strings.TrimPrefix(path, "/")

	// Convert to lowercase
	path = strings.ToLower(path)

	// Replace path separators and spaces with hyphens
	path = strings.ReplaceAll(path, "/", "-")
	path = strings.ReplaceAll(path, " ", "-")

	// Remove any characters that aren't alphanumeric or hyphens
	reg := regexp.MustCompile(`[^a-z0-9-]`)
	path = reg.ReplaceAllString(path, "")

	// Collapse multiple hyphens
	reg = regexp.MustCompile(`-+`)
	path = reg.ReplaceAllString(path, "-")

	// Trim leading/trailing hyphens
	path = strings.Trim(path, "-")

	return path
}

// GetOrCreateRepoName returns the repo name for the given source path,
// creating a new entry if needed. Handles collisions by appending suffixes.
// The source path is normalized (symlinks resolved) for consistent matching.
func GetOrCreateRepoName(db *sql.DB, sourcePath string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("get repo name: db is nil")
	}

	normalizedPath := normalizeRepoPath(sourcePath)

	tx, err := db.Begin()
	if err != nil {
		return "", fmt.Errorf("get repo name: begin: %w", err)
	}

	var name string
	err = tx.QueryRow("SELECT name FROM repos WHERE source_path = ?;", normalizedPath).Scan(&name)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("get repo name: commit: %w", err)
		}
		return name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return "", fmt.Errorf("get repo name: select: %w", err)
	}

	baseName := SanitizeRepoName(normalizedPath)
	name = baseName

	suffix := 2
	for {
		var exists int
		err = tx.QueryRow("SELECT 1 FROM repos WHERE name = ?;", name).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			_ = tx.Rollback()
			return "", fmt.Errorf("get repo name: check name: %w", err)
		}
		name = fmt.Sprintf("%s-%d", baseName, suffix)
		suffix++
	}

	if _, err := tx.Exec("INSERT INTO repos (name, source_path) VALUES (?, ?);", name, normalizedPath); err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("get repo name: insert: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("get repo name: commit: %w", err)
	}

	return name, nil
}

// RepoNameForPath returns the repo name for the given source path.
// Returns empty string when no repo matches the path.
func RepoNameForPath(db *sql.DB, sourcePath string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("repo name for path: db is nil")
	}

	normalizedPath := normalizeRepoPath(sourcePath)

	var name string
	err := db.QueryRow("SELECT name FROM repos WHERE source_path = ?;", normalizedPath).Scan(&name)
	if err == nil {
		return name, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return "", fmt.Errorf("repo name for path: %w", err)
}

// RepoPathForWorkspace returns the source repo path for a workspace path.
func RepoPathForWorkspace(db *sql.DB, wsPath string) (string, bool, error) {
	if db == nil {
		return "", false, fmt.Errorf("repo path for workspace: db is nil")
	}

	wsPath = paths.NormalizePath(filepath.Clean(wsPath))

	var repoName string
	var sourcePath string
	err := db.QueryRow(`SELECT workspaces.repo, repos.source_path
FROM workspaces
JOIN repos ON repos.name = workspaces.repo
WHERE workspaces.path = ?;`, wsPath).Scan(&repoName, &sourcePath)
	if err == nil {
		if sourcePath == "" {
			return "", true, ErrRepoPathNotFound
		}
		return sourcePath, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("repo path for workspace: %w", err)
}

// normalizeRepoPath resolves symlinks and returns a clean absolute path.
// This ensures consistent path matching regardless of how the path was accessed.
func normalizeRepoPath(path string) string {
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}
