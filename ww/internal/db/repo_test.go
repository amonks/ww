package db

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	store, err := Open(path, OpenOptions{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store.SqlDB()
}

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func addWorkspace(t *testing.T, db *sql.DB, repo, name, path string) {
	t.Helper()
	exec(t, db, `INSERT INTO workspaces (repo, name, path, created_at, updated_at)
		VALUES (?, ?, ?, '', '')`, repo, name, path)
}

func repoNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM repos ORDER BY name")
	if err != nil {
		t.Fatalf("query repos: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, name)
	}
	return names
}

// symlinkedPool lays out a pool's workspaces directory reached through a
// symlink, the way macOS reaches every temp dir (/var is a link to
// /private/var), and returns the workspace path as the pool would record it
// and the same directory with symlinks resolved, as a source path is stored.
func symlinkedPool(t *testing.T) (recorded, resolved string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(realDir, "src", "ws-001"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return filepath.Join(link, "src", "ws-001"), filepath.Join(realDir, "src", "ws-001")
}

// A repo registered with one of the pool's own workspaces as its source was
// written by a ww that took a workspace for its repo. Opening the database
// clears every such row; nothing else goes.
func TestOpen_PrunesReposRegisteredAtAPoolWorkspace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	recorded, resolved := symlinkedPool(t)

	db := openTestDB(t, path)
	exec(t, db, "INSERT INTO repos (name, source_path) VALUES ('src', '/src')")
	addWorkspace(t, db, "src", "ws-001", recorded)
	addWorkspace(t, db, "src", "ws-002", "/pool/src/ws-002")
	// Bogus: registered at a workspace, by either spelling of its path.
	exec(t, db, "INSERT INTO repos (name, source_path) VALUES ('at-ws-001', ?)", resolved)
	exec(t, db, "INSERT INTO repos (name, source_path) VALUES ('at-ws-002', '/pool/src/ws-002')")
	// A repo with no workspaces whose directory merely shares a workspace's name.
	exec(t, db, "INSERT INTO repos (name, source_path) VALUES ('other', '/other/ws-001')")
	// Registered at a workspace, but holding workspaces of its own: those are
	// real jj workspaces, and the row is what keeps track of them.
	exec(t, db, "INSERT INTO repos (name, source_path) VALUES ('holder', '/pool/src/ws-003')")
	addWorkspace(t, db, "src", "ws-003", "/pool/src/ws-003")
	addWorkspace(t, db, "holder", "ws-004", "/pool/holder/ws-004")
	db.Close()

	db = openTestDB(t, path)
	if got, want := repoNames(t, db), []string{"holder", "other", "src"}; !slices.Equal(got, want) {
		t.Fatalf("repos after reopen: got %v, want %v", got, want)
	}
}

func TestGetOrCreateRepoName_RefusesAPoolWorkspace(t *testing.T) {
	recorded, resolved := symlinkedPool(t)
	db := openTestDB(t, filepath.Join(t.TempDir(), "state.db"))
	exec(t, db, "INSERT INTO repos (name, source_path) VALUES ('src', '/src')")
	addWorkspace(t, db, "src", "ws-001", recorded)

	for _, path := range []string{recorded, resolved, filepath.Join(recorded, ".")} {
		if _, err := GetOrCreateRepoName(db, path); !errors.Is(err, ErrNotSourceRepo) {
			t.Errorf("GetOrCreateRepoName(%s): expected ErrNotSourceRepo, got %v", path, err)
		}
	}
	if got, want := repoNames(t, db), []string{"src"}; !slices.Equal(got, want) {
		t.Fatalf("repos: got %v, want %v", got, want)
	}

	if name, err := GetOrCreateRepoName(db, "/src"); err != nil || name != "src" {
		t.Fatalf("GetOrCreateRepoName(/src): got %q, %v", name, err)
	}
	if _, err := GetOrCreateRepoName(db, "/other/ws-001"); err != nil {
		t.Fatalf("GetOrCreateRepoName(/other/ws-001): %v", err)
	}
}
