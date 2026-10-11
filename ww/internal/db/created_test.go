package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type workspaceTimes struct{ created, updated, acquired string }

func timesOf(t *testing.T, db *sql.DB, name string) workspaceTimes {
	t.Helper()
	var got workspaceTimes
	if err := db.QueryRow(`SELECT created_at, updated_at, acquired_at FROM workspaces WHERE name = ?`, name).
		Scan(&got.created, &got.updated, &got.acquired); err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return got
}

func stamp(ts time.Time) string { return ts.UTC().Format(time.RFC3339Nano) }

// A row's created_at went to the latest acquire until acquires stopped
// writing it. A workspace's directory is born when jj adds the workspace, just
// after its row is written, so a directory older than its row's created_at
// dates the creation the overwrite lost, and the sweep puts it back. Every
// other row is left as it is.
func TestRestoreCreationTimes(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "state.db"))
	exec(t, db, "INSERT INTO repos (name, source_path) VALUES ('repo', '/src/repo')")

	born := time.Date(2026, 8, 4, 6, 39, 0, 123456789, time.UTC)
	lastAcquire := time.Date(2026, 10, 11, 17, 24, 9, 0, time.UTC)
	honest := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	insert := func(name, created, updated, acquired string) {
		exec(t, db, `INSERT INTO workspaces (repo, name, path, status, created_at, updated_at, acquired_at)
			VALUES ('repo', ?, ?, 'acquired', ?, ?, ?)`, name, "/ws/"+name, created, updated, acquired)
	}
	insert("overwritten", stamp(lastAcquire), stamp(lastAcquire.Add(time.Second)), stamp(lastAcquire))
	insert("honest", stamp(honest), stamp(honest), stamp(honest))
	insert("gone", stamp(lastAcquire), stamp(lastAcquire), stamp(lastAcquire))
	insert("unrecorded", "", "", "")
	insert("zero", stamp(time.Time{}), "", "")

	births := map[string]time.Time{
		"/ws/overwritten": born,
		// jj creates the directory after the row is written.
		"/ws/honest":     honest.Add(500 * time.Millisecond),
		"/ws/unrecorded": born,
		"/ws/zero":       born,
	}
	birth := func(path string) (time.Time, bool) {
		ts, ok := births[path]
		return ts, ok
	}

	for range 2 { // and running again changes nothing
		if err := restoreCreationTimes(db, birth); err != nil {
			t.Fatalf("restore: %v", err)
		}
		want := map[string]workspaceTimes{
			"overwritten": {stamp(born), stamp(lastAcquire.Add(time.Second)), stamp(lastAcquire)},
			"honest":      {stamp(honest), stamp(honest), stamp(honest)},
			"gone":        {stamp(lastAcquire), stamp(lastAcquire), stamp(lastAcquire)},
			"unrecorded":  {stamp(born), "", ""},
			"zero":        {stamp(born), "", ""},
		}
		for name, w := range want {
			if got := timesOf(t, db, name); got != w {
				t.Errorf("%s: got %+v, want %+v", name, got, w)
			}
		}
	}
}

// The sweep writes a creation time only over the value it read. A row
// rewritten between the read and the write — the workspace re-created under
// the same name, say — is the newer writer's, and an older directory's birth
// time does not belong on it.
func TestRestoreCreationTimes_LeavesARowChangedSinceItWasRead(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "state.db"))
	exec(t, db, "INSERT INTO repos (name, source_path) VALUES ('repo', '/src/repo')")
	lastAcquire := time.Date(2026, 10, 11, 17, 24, 9, 0, time.UTC)
	exec(t, db, `INSERT INTO workspaces (repo, name, path, status, created_at, updated_at, acquired_at)
		VALUES ('repo', 'ws-001', '/ws/ws-001', 'acquired', ?, ?, ?)`, stamp(lastAcquire), stamp(lastAcquire), stamp(lastAcquire))

	recreated := stamp(lastAcquire.Add(time.Hour))
	birth := func(string) (time.Time, bool) {
		exec(t, db, "UPDATE workspaces SET created_at = ? WHERE name = 'ws-001'", recreated)
		return time.Date(2026, 8, 4, 6, 39, 0, 0, time.UTC), true
	}
	if err := restoreCreationTimes(db, birth); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := timesOf(t, db, "ws-001").created; got != recreated {
		t.Errorf("created %s, want the concurrent writer's %s", got, recreated)
	}
}

// Opening the database runs the sweep against the real filesystem.
func TestOpen_RestoresCreationTimes(t *testing.T) {
	dir := t.TempDir()
	wsPath := filepath.Join(dir, "ws-001")
	if err := os.Mkdir(wsPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	born, ok := birthTime(wsPath)
	if !ok {
		t.Skip("this platform or filesystem records no birth time")
	}

	dbPath := filepath.Join(dir, "state.db")
	db := openTestDB(t, dbPath)
	exec(t, db, "INSERT INTO repos (name, source_path) VALUES ('repo', '/src/repo')")
	future := stamp(time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC))
	exec(t, db, `INSERT INTO workspaces (repo, name, path, status, created_at, updated_at, acquired_at)
		VALUES ('repo', 'ws-001', ?, 'available', ?, ?, '')`, wsPath, future, future)

	reopened := openTestDB(t, dbPath)
	if got := timesOf(t, reopened, "ws-001"); got.created != stamp(born) || got.updated != future {
		t.Errorf("after open: got %+v, want created %s and updated untouched", got, stamp(born))
	}
}

// A directory's birth time is when it was made.
func TestBirthTime(t *testing.T) {
	before := time.Now()
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	after := time.Now()

	born, ok := birthTime(dir)
	if !ok {
		t.Skip("this platform or filesystem records no birth time")
	}
	// Filesystem timestamps can be coarser than the clock.
	if born.Before(before.Add(-time.Second)) || born.After(after.Add(time.Second)) {
		t.Errorf("birth %v, want between %v and %v", born, before, after)
	}

	if _, ok := birthTime(filepath.Join(dir, "missing")); ok {
		t.Error("a missing path reported a birth time")
	}
}
