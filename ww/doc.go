// Package ww manages a pool of jujutsu workspaces.
//
// This package provides functionality to acquire, release, and manage jujutsu
// workspaces from a shared pool. It's designed for scenarios where multiple
// processes need concurrent access to independent working copies of the same
// repository.
//
// # Basic Usage
//
// Create a pool and acquire a workspace:
//
//	pool, err := ww.Open()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer pool.Close()
//
//	wsPath, err := pool.Acquire("/path/to/repo", ww.AcquireOptions{
//	    Rev: "main",
//	    Purpose: "feature work",
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer pool.Release(wsPath)
//
//	// Use the workspace at wsPath...
//
// Open a pool with an existing SQLite connection:
//
//	stateDir, err := paths.ResolveWithDefault("", paths.DefaultStateDir)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	workspacesDir, err := paths.DefaultWorkspacesDir()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	dbPath := filepath.Join(stateDir, "state.db")
//
//	dbStore, err := db.Open(dbPath, db.OpenOptions{})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer dbStore.Close()
//
//	pool := ww.NewPool(dbStore.SqlDB(), workspacesDir)
//
// # Configuration
//
// Repositories can include a ww.toml or .ww/config.toml file to
// configure workspace behavior:
//
//	[workspace]
//	on-create = ["npm install"]  # Run every time workspace is acquired
//
// # Storage
//
// By default, workspaces are stored in ~/.local/share/ww/workspaces/ and
// state is stored in ~/.local/state/ww/. These locations follow the XDG
// Base Directory Specification.
//
// The pool coordinates concurrent access through SQLite state rather than lock files.
package ww
