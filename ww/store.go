package ww

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"monks.co/ww/ww/internal/db"
	"monks.co/ww/ww/internal/paths"
)

type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) GetOrCreateRepoName(sourcePath string) (string, error) {
	return db.GetOrCreateRepoName(s.db, sourcePath)
}

func (s *Store) RepoPathForWorkspace(wsPath string) (string, bool, error) {
	return db.RepoPathForWorkspace(s.db, wsPath)
}

func (s *Store) FindAvailableWorkspace(repoName string) (*WorkspaceInfo, error) {
	row := s.db.QueryRow(`SELECT name, path, purpose, rev, status, acquired_by_pid,
		provisioned, created_at, updated_at, acquired_at
		FROM workspaces WHERE repo = ? AND status = ?
		ORDER BY name LIMIT 1;`, repoName, StatusAvailable)

	ws, err := scanWorkspaceRow(row, repoName)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("find available workspace: %w", err)
	}

	return ws, nil
}

func (s *Store) AcquireAvailableWorkspace(repoName string, ws WorkspaceInfo) (*WorkspaceInfo, error) {
	row := s.db.QueryRow(`UPDATE workspaces
		SET purpose = ?, rev = ?, status = ?, acquired_by_pid = ?,
			created_at = ?, updated_at = ?, acquired_at = ?
		WHERE repo = ? AND name = (
			SELECT name FROM workspaces WHERE repo = ? AND status = ?
			ORDER BY name LIMIT 1
		) AND status = ?
		RETURNING name, path, purpose, rev, status, acquired_by_pid,
			provisioned, created_at, updated_at, acquired_at;`,
		ws.Purpose,
		ws.Rev,
		ws.Status,
		sqlNullInt(ws.AcquiredByPID),
		formatWorkspaceTime(ws.CreatedAt),
		formatWorkspaceTime(ws.UpdatedAt),
		formatOptionalWorkspaceTime(ws.AcquiredAt),
		repoName,
		repoName,
		StatusAvailable,
		StatusAvailable,
	)

	updated, err := scanWorkspaceRow(row, repoName)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("acquire available workspace: %w", err)
	}

	return updated, nil
}

func (s *Store) InsertWorkspace(ws WorkspaceInfo) error {
	_, err := s.db.Exec(`INSERT INTO workspaces (
		repo, name, path, purpose, rev, status, acquired_by_pid, provisioned,
		created_at, updated_at, acquired_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);`,
		ws.Repo,
		ws.Name,
		ws.Path,
		ws.Purpose,
		ws.Rev,
		ws.Status,
		sqlNullInt(ws.AcquiredByPID),
		boolToSQLite(ws.Provisioned),
		formatWorkspaceTime(ws.CreatedAt),
		formatWorkspaceTime(ws.UpdatedAt),
		formatOptionalWorkspaceTime(ws.AcquiredAt),
	)
	if err != nil {
		return fmt.Errorf("insert workspace: %w", err)
	}
	return nil
}

func (s *Store) UpdateWorkspace(ws WorkspaceInfo) error {
	_, err := s.db.Exec(`UPDATE workspaces
		SET path = ?, purpose = ?, rev = ?, status = ?, acquired_by_pid = ?,
			provisioned = ?, created_at = ?, updated_at = ?, acquired_at = ?
		WHERE repo = ? AND name = ?;`,
		ws.Path,
		ws.Purpose,
		ws.Rev,
		ws.Status,
		sqlNullInt(ws.AcquiredByPID),
		boolToSQLite(ws.Provisioned),
		formatWorkspaceTime(ws.CreatedAt),
		formatWorkspaceTime(ws.UpdatedAt),
		formatOptionalWorkspaceTime(ws.AcquiredAt),
		ws.Repo,
		ws.Name,
	)
	if err != nil {
		return fmt.Errorf("update workspace: %w", err)
	}
	return nil
}

func (s *Store) ListWorkspaces(repoName string) ([]WorkspaceInfo, error) {
	rows, err := s.db.Query(`SELECT name, path, purpose, rev, status, acquired_by_pid,
		provisioned, created_at, updated_at, acquired_at
		FROM workspaces WHERE repo = ?;`, repoName)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()

	var items []WorkspaceInfo
	for rows.Next() {
		ws, err := scanWorkspaceRows(rows, repoName)
		if err != nil {
			return nil, err
		}
		items = append(items, *ws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	return items, nil
}

func (s *Store) UpdateWorkspaceRevision(repoName, name, rev string, updatedAt time.Time) error {
	_, err := s.db.Exec(`UPDATE workspaces
		SET rev = ?, updated_at = ?
		WHERE repo = ? AND name = ?;`,
		rev,
		formatWorkspaceTime(updatedAt),
		repoName,
		name,
	)
	if err != nil {
		return fmt.Errorf("update workspace rev: %w", err)
	}
	return nil
}

func (s *Store) MarkWorkspaceProvisioned(repoName, name string) error {
	_, err := s.db.Exec(`UPDATE workspaces
		SET provisioned = 1
		WHERE repo = ? AND name = ?;`, repoName, name)
	if err != nil {
		return fmt.Errorf("mark workspace provisioned: %w", err)
	}
	return nil
}

func (s *Store) ReleaseWorkspace(wsPath string, updatedAt time.Time) error {
	result, err := s.db.Exec(`UPDATE workspaces
		SET status = ?, purpose = '', rev = '', acquired_by_pid = NULL, acquired_at = '', updated_at = ?
		WHERE path = ?;`, StatusAvailable, formatWorkspaceTime(updatedAt), paths.NormalizePath(wsPath))
	if err != nil {
		return fmt.Errorf("release workspace: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("release workspace: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("workspace not found: %s", wsPath)
	}
	return nil
}

func (s *Store) GetWorkspaceByName(repoName, name string) (*WorkspaceInfo, error) {
	row := s.db.QueryRow(`SELECT name, path, purpose, rev, status, acquired_by_pid,
		provisioned, created_at, updated_at, acquired_at
		FROM workspaces WHERE repo = ? AND name = ?;`, repoName, name)

	ws, err := scanWorkspaceRow(row, repoName)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("workspace not found: %s", name)
		}
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	return ws, nil
}

func (s *Store) GetWorkspaceByPath(path string) (*WorkspaceInfo, error) {
	row := s.db.QueryRow(`SELECT repo, name, path, purpose, rev, status, acquired_by_pid,
		provisioned, created_at, updated_at, acquired_at
		FROM workspaces WHERE path = ?;`, paths.NormalizePath(path))

	var repoName string
	var name string
	var wsPath string
	var purpose string
	var rev string
	var status string
	var acquiredBy sql.NullInt64
	var provisioned int
	var createdAt string
	var updatedAt string
	var acquiredAt string
	if err := row.Scan(
		&repoName,
		&name,
		&wsPath,
		&purpose,
		&rev,
		&status,
		&acquiredBy,
		&provisioned,
		&createdAt,
		&updatedAt,
		&acquiredAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get workspace by path: %w", err)
	}

	return hydrateWorkspaceInfo(
		name,
		repoName,
		wsPath,
		purpose,
		rev,
		status,
		acquiredBy,
		provisioned,
		createdAt,
		updatedAt,
		acquiredAt,
	)
}

func (s *Store) DeleteWorkspace(repoName, name string) error {
	result, err := s.db.Exec("DELETE FROM workspaces WHERE repo = ? AND name = ?;", repoName, name)
	if err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete workspace: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("workspace not found: %s", name)
	}
	return nil
}

func (s *Store) DeleteWorkspaces(repoName string) ([]WorkspaceInfo, string, error) {
	rows, err := s.db.Query(`SELECT name, path, purpose, rev, status, acquired_by_pid,
		provisioned, created_at, updated_at, acquired_at
		FROM workspaces WHERE repo = ?;`, repoName)
	if err != nil {
		return nil, "", fmt.Errorf("load workspaces: %w", err)
	}
	defer rows.Close()

	var items []WorkspaceInfo
	for rows.Next() {
		ws, err := scanWorkspaceRows(rows, repoName)
		if err != nil {
			return nil, "", err
		}
		items = append(items, *ws)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("load workspaces: %w", err)
	}

	var sourcePath string
	row := s.db.QueryRow("SELECT source_path FROM repos WHERE name = ?;", repoName)
	if err := row.Scan(&sourcePath); err != nil {
		if err != sql.ErrNoRows {
			return nil, "", fmt.Errorf("load repo path: %w", err)
		}
	}

	if _, err := s.db.Exec("DELETE FROM workspaces WHERE repo = ?;", repoName); err != nil {
		return nil, "", fmt.Errorf("delete workspaces: %w", err)
	}

	return items, sourcePath, nil
}

func (s *Store) NextWorkspaceName(repoName string) (string, error) {
	rows, err := s.db.Query("SELECT name FROM workspaces WHERE repo = ?;", repoName)
	if err != nil {
		return "", fmt.Errorf("next workspace name: %w", err)
	}
	defer rows.Close()

	maxNum := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", fmt.Errorf("next workspace name: %w", err)
		}
		var num int
		if _, err := fmt.Sscanf(name, "ws-%d", &num); err == nil {
			if num > maxNum {
				maxNum = num
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("next workspace name: %w", err)
	}

	return fmt.Sprintf("ws-%03d", maxNum+1), nil
}

func scanWorkspaceRow(row *sql.Row, repoName string) (*WorkspaceInfo, error) {
	var ws WorkspaceInfo
	ws.Repo = repoName
	var status string
	var acquiredBy sql.NullInt64
	var provisioned int
	var createdAt string
	var updatedAt string
	var acquiredAt string

	err := row.Scan(
		&ws.Name,
		&ws.Path,
		&ws.Purpose,
		&ws.Rev,
		&status,
		&acquiredBy,
		&provisioned,
		&createdAt,
		&updatedAt,
		&acquiredAt,
	)
	if err != nil {
		return nil, err
	}

	parsed, err := hydrateWorkspaceInfo(
		ws.Name,
		repoName,
		ws.Path,
		ws.Purpose,
		ws.Rev,
		status,
		acquiredBy,
		provisioned,
		createdAt,
		updatedAt,
		acquiredAt,
	)
	if err != nil {
		return nil, err
	}

	return parsed, nil
}

func scanWorkspaceRows(rows *sql.Rows, repoName string) (*WorkspaceInfo, error) {
	var name string
	var path string
	var purpose string
	var rev string
	var status string
	var acquiredBy sql.NullInt64
	var provisioned int
	var createdAt string
	var updatedAt string
	var acquiredAt string
	if err := rows.Scan(
		&name,
		&path,
		&purpose,
		&rev,
		&status,
		&acquiredBy,
		&provisioned,
		&createdAt,
		&updatedAt,
		&acquiredAt,
	); err != nil {
		return nil, fmt.Errorf("scan workspace: %w", err)
	}

	parsed, err := hydrateWorkspaceInfo(
		name,
		repoName,
		path,
		purpose,
		rev,
		status,
		acquiredBy,
		provisioned,
		createdAt,
		updatedAt,
		acquiredAt,
	)
	if err != nil {
		return nil, err
	}

	return parsed, nil
}

func hydrateWorkspaceInfo(
	name string,
	repoName string,
	path string,
	purpose string,
	rev string,
	status string,
	acquiredBy sql.NullInt64,
	provisioned int,
	createdAt string,
	updatedAt string,
	acquiredAt string,
) (*WorkspaceInfo, error) {
	createdAtTime, err := parseWorkspaceTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("scan workspace created_at: %w", err)
	}
	updatedAtTime, err := parseWorkspaceTime(updatedAt)
	if err != nil {
		return nil, fmt.Errorf("scan workspace updated_at: %w", err)
	}
	acquiredAtTime, err := parseOptionalWorkspaceTime(acquiredAt)
	if err != nil {
		return nil, fmt.Errorf("scan workspace acquired_at: %w", err)
	}

	ws := &WorkspaceInfo{
		Name:        name,
		Repo:        repoName,
		Path:        paths.NormalizePath(path),
		Purpose:     purpose,
		Rev:         rev,
		Status:      Status(status),
		Provisioned: provisioned == 1,
		CreatedAt:   createdAtTime,
		UpdatedAt:   updatedAtTime,
		AcquiredAt:  acquiredAtTime,
	}
	if acquiredBy.Valid {
		ws.AcquiredByPID = int(acquiredBy.Int64)
	}
	if ws.Status == "" {
		ws.Status = StatusAvailable
	}
	return ws, nil
}

func parseWorkspaceTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}

func parseOptionalWorkspaceTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}

func formatWorkspaceTime(value time.Time) string {
	if value.IsZero() {
		return time.Time{}.UTC().Format(time.RFC3339Nano)
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func formatOptionalWorkspaceTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func boolToSQLite(value bool) int {
	if value {
		return 1
	}
	return 0
}

func sqlNullInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}
