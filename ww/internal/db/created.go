package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// restoreCreationTimes puts back each workspace's creation time where an
// acquire overwrote it.
//
// Acquiring a pooled workspace used to stamp created_at along with
// acquired_at, so a row's created_at was its latest acquire and the real
// creation was lost. The workspace's directory still dates it: jj makes the
// directory when it adds the workspace, just after the row is written, and
// nothing in the pool makes it again. So where the directory is older than the
// row's created_at, or the row has none, the directory's birth is the
// creation, and it is written back. A row whose directory is younger is
// honest; one whose directory is gone, or whose platform records no birth
// time, has nothing to recover from and is left as it is. Only created_at is
// written, and only if it has not changed since it was read.
//
// This runs on every open rather than once, for the reason pruneWorkspaceRepos
// does: every ww build on the machine shares the state database, so an older
// build can overwrite a creation time after this one has restored it, and the
// next open puts it back. Its cost is a stat per workspace.
func restoreCreationTimes(db *sql.DB, birth func(path string) (time.Time, bool)) error {
	rows, err := db.Query(`SELECT repo, name, path, created_at FROM workspaces;`)
	if err != nil {
		return fmt.Errorf("restore creation times: %w", err)
	}
	type workspace struct{ repo, name, path, createdAt string }
	var all []workspace
	for rows.Next() {
		var ws workspace
		if err := rows.Scan(&ws.repo, &ws.name, &ws.path, &ws.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("restore creation times: %w", err)
		}
		all = append(all, ws)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("restore creation times: %w", err)
	}

	for _, ws := range all {
		born, ok := birth(ws.path)
		if !ok {
			continue
		}
		if recorded, known := parseCreatedAt(ws.createdAt); known && !born.Before(recorded) {
			continue
		}
		if _, err := db.Exec(`UPDATE workspaces SET created_at = ?
WHERE repo = ? AND name = ? AND created_at = ?;`,
			born.UTC().Format(time.RFC3339Nano), ws.repo, ws.name, ws.createdAt); err != nil {
			return fmt.Errorf("restore creation time of %s: %w", ws.name, err)
		}
	}
	return nil
}

// parseCreatedAt reads a created_at as the store writes it. known is false for
// a row that records no creation: empty, the zero time the store writes for an
// unset one, or unreadable.
func parseCreatedAt(value string) (ts time.Time, known bool) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || ts.IsZero() {
		return time.Time{}, false
	}
	return ts, true
}
