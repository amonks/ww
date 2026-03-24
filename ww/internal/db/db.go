package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps the sqlite database connection.
type DB struct {
	sql *sql.DB
}

// OpenOptions configures Open behavior.
type OpenOptions struct {
	// If true, skip the interactive confirmation prompt (for tests).
	SkipConfirm bool
}

// Open opens (or creates) the database at path, applies migrations, and configures pragmas.
func Open(path string, opts OpenOptions) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("open db: path is required")
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("open db: create dir %q: %w", dir, err)
	}

	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open db: ping: %w", err)
	}

	if err := applyPragmas(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	if err := runMigrations(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return &DB{sql: sqlDB}, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

// Tx runs fn within a transaction.
func (db *DB) Tx(fn func(tx *sql.Tx) error) error {
	if db == nil || db.sql == nil {
		return fmt.Errorf("transaction: db is nil")
	}

	tx, err := db.sql.Begin()
	if err != nil {
		return fmt.Errorf("transaction: begin: %w", err)
	}

	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			return fmt.Errorf("transaction: rollback after %v: %w", err, rollbackErr)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("transaction: commit: %w", err)
	}
	return nil
}

// SqlDB returns the underlying *sql.DB for use by package stores.
func (db *DB) SqlDB() *sql.DB {
	if db == nil {
		return nil
	}
	return db.sql
}

func applyPragmas(db *sql.DB) error {
	pragmas := []struct {
		name string
		sql  string
	}{
		{name: "journal_mode", sql: "PRAGMA journal_mode = WAL;"},
		{name: "busy_timeout", sql: "PRAGMA busy_timeout = 5000;"},
		{name: "foreign_keys", sql: "PRAGMA foreign_keys = ON;"},
		{name: "synchronous", sql: "PRAGMA synchronous = NORMAL;"},
		{name: "cache_size", sql: "PRAGMA cache_size = -2000;"},
	}

	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma.sql); err != nil {
			return fmt.Errorf("set pragma %s: %w", pragma.name, err)
		}
	}
	return nil
}
