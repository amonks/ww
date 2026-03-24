package db

import (
	"context"
	"database/sql"
	"embed"

	"monks.co/pkg/migrate"
)

//go:embed migrations/*.sql
var migrations embed.FS

func runMigrations(sqlDB *sql.DB) error {
	return migrate.Run(context.Background(), migrate.Config{
		DB: sqlDB, FS: migrations, Dir: "migrations",
		Baseline: []string{"001_initial.sql"},
	})
}
