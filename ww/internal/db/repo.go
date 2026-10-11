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

// ErrNotSourceRepo indicates a path offered as a source repo is one of the
// pool's own workspaces.
var ErrNotSourceRepo = errors.New("path is a pool workspace, not a source repo")

// queryer is what *sql.DB and *sql.Tx have in common for reads.
type queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

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
//
// A pool workspace is never a source repo, and is refused with
// ErrNotSourceRepo: registering one gives the workspace a pool of its own,
// whose first `jj workspace add` collides with the real pool's names.
func GetOrCreateRepoName(db *sql.DB, sourcePath string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("get repo name: db is nil")
	}

	normalizedPath := normalizeRepoPath(sourcePath)

	tx, err := db.Begin()
	if err != nil {
		return "", fmt.Errorf("get repo name: begin: %w", err)
	}

	_, _, isWorkspace, err := workspaceAt(tx, normalizedPath)
	if err != nil {
		_ = tx.Rollback()
		return "", fmt.Errorf("get repo name: %w", err)
	}
	if isWorkspace {
		_ = tx.Rollback()
		return "", fmt.Errorf("%w: %s", ErrNotSourceRepo, normalizedPath)
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

// WorkspaceAt returns the name of the pool workspace at wsPath and the source
// repo it was created from. found is false when no workspace is recorded there.
func WorkspaceAt(db *sql.DB, wsPath string) (name, sourcePath string, found bool, err error) {
	if db == nil {
		return "", "", false, fmt.Errorf("workspace at: db is nil")
	}
	name, sourcePath, found, err = workspaceAt(db, wsPath)
	if err == nil && found && sourcePath == "" {
		err = ErrRepoPathNotFound
	}
	return name, sourcePath, found, err
}

// workspaceAt finds the pool workspace at dir, comparing paths with symlinks
// resolved on both sides.
//
// The two sides spell a path differently. A row holds its workspace's path as
// the pool built it, under the workspaces directory as configured, while a
// path asked about usually comes from jj, which resolves symlinks: macOS
// reaches every temp dir through /var, a link to /private/var, and FreeBSD's
// /home is a link to /usr/home. A workspace's directory is named for the
// workspace, which narrows the candidates to the rows of that name.
//
// Locating a path and refusing it as a source repo both go through here, so
// anything the refusal turns away, Locate resolves to its repo.
func workspaceAt(q queryer, dir string) (name, sourcePath string, found bool, err error) {
	dir = normalizeRepoPath(dir)
	rows, err := q.Query(`SELECT workspaces.name, workspaces.path, COALESCE(repos.source_path, '')
FROM workspaces
LEFT JOIN repos ON repos.name = workspaces.repo
WHERE workspaces.name = ?;`, filepath.Base(dir))
	if err != nil {
		return "", "", false, fmt.Errorf("find workspaces named %s: %w", filepath.Base(dir), err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&name, &path, &sourcePath); err != nil {
			return "", "", false, fmt.Errorf("find workspaces named %s: %w", filepath.Base(dir), err)
		}
		if normalizeRepoPath(path) == dir {
			return name, sourcePath, true, nil
		}
	}
	return "", "", false, rows.Err()
}

// pruneWorkspaceRepos deletes every repos row registered with one of the
// pool's own workspaces as its source path.
//
// Such rows came from a ww that took the workspace it was run in for its
// repo — `jj workspace root` names the workspace there, not the repo it was
// made from — and GetOrCreateRepoName refuses to write them now. They key a
// pool that cannot work, since its first `jj workspace add` collides with
// the real pool's names in the jj repo they share, so in practice they hold
// no workspaces. One that does is left: its workspaces are real jj
// workspaces, and the row is what keeps track of them.
//
// This runs on every open rather than as a migration, because the state
// database is shared by every ww build on the machine at once, and a
// migration an older build does not know makes that build refuse to open it.
// That also sweeps up rows an older build writes after this one first ran.
func pruneWorkspaceRepos(db *sql.DB) error {
	rows, err := db.Query(`SELECT name, source_path FROM repos
WHERE NOT EXISTS (SELECT 1 FROM workspaces WHERE workspaces.repo = repos.name);`)
	if err != nil {
		return fmt.Errorf("prune workspace repos: %w", err)
	}
	type repo struct{ name, sourcePath string }
	var candidates []repo
	for rows.Next() {
		var r repo
		if err := rows.Scan(&r.name, &r.sourcePath); err != nil {
			rows.Close()
			return fmt.Errorf("prune workspace repos: %w", err)
		}
		candidates = append(candidates, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("prune workspace repos: %w", err)
	}

	for _, r := range candidates {
		_, _, isWorkspace, err := workspaceAt(db, r.sourcePath)
		if err != nil {
			return fmt.Errorf("prune workspace repos: %w", err)
		}
		if !isWorkspace {
			continue
		}
		if _, err := db.Exec(`DELETE FROM repos WHERE name = ?
AND NOT EXISTS (SELECT 1 FROM workspaces WHERE workspaces.repo = repos.name);`, r.name); err != nil {
			return fmt.Errorf("prune workspace repo %s: %w", r.name, err)
		}
	}
	return nil
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
