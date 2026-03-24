package ww_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"monks.co/pkg/jj"
	"monks.co/ww/ww"
)

// normalizePath resolves symlinks and strips macOS /private prefix,
// matching the behavior of ww's internal paths.NormalizePath.
func normalizePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return strings.TrimPrefix(path, "/private")
}

func requirePool(t *testing.T, pool *ww.Pool) *ww.Pool {
	t.Helper()
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Fatalf("close pool: %v", err)
		}
	})
	return pool
}

func openPool(t *testing.T, opts ww.Options) *ww.Pool {
	t.Helper()
	pool, err := ww.OpenWithOptions(opts)
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}
	return requirePool(t, pool)
}

func openDefaultPool(t *testing.T) *ww.Pool {
	t.Helper()
	pool, err := ww.Open()
	if err != nil {
		t.Fatalf("failed to open pool: %v", err)
	}
	return requirePool(t, pool)
}

func setupTestRepo(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	tmpDir, _ = filepath.EvalSymlinks(tmpDir)

	// Init a jj repo
	client := jj.New()
	if err := client.Init(tmpDir); err != nil {
		t.Fatalf("failed to init jj repo: %v", err)
	}

	return tmpDir
}

func ensureMainBookmark(t *testing.T, repoPath string) {
	t.Helper()
	client := jj.New()
	bookmarks, err := client.BookmarkList(repoPath)
	if err != nil {
		t.Fatalf("list bookmarks: %v", err)
	}
	if slices.Contains(bookmarks, "main") {
		return
	}
	if err := client.BookmarkCreate(repoPath, "main", "@"); err != nil {
		t.Fatalf("create main bookmark: %v", err)
	}
}

func acquireOptions() ww.AcquireOptions {
	return ww.AcquireOptions{Purpose: "test purpose"}
}

func TestPool_Acquire_CreatesNewWorkspace(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to acquire workspace: %v", err)
	}

	// Verify workspace path exists
	if _, err := os.Stat(wsPath); os.IsNotExist(err) {
		t.Error("workspace directory was not created")
	}

	// Verify it's a jj workspace
	if _, err := os.Stat(filepath.Join(wsPath, ".jj")); os.IsNotExist(err) {
		t.Error("workspace does not have .jj directory")
	}

	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("failed to release workspace: %v", err)
	}

	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("failed to list after release: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 workspace after release, got %d", len(list))
	}
	if list[0].Status != ww.StatusAvailable {
		t.Fatalf("expected status available after release, got %s", list[0].Status)
	}
	if list[0].Purpose != "" {
		t.Fatalf("expected purpose to be cleared on release, got %q", list[0].Purpose)
	}
}

func TestPool_RepoSlug(t *testing.T) {
	pool := openPool(t, ww.Options{
		StateDir:      t.TempDir(),
		WorkspacesDir: t.TempDir(),
	})

	repoPath := "/tmp/my-repo"
	slug, err := pool.RepoSlug(repoPath)
	if err != nil {
		t.Fatalf("get repo slug: %v", err)
	}

	// The slug should be a sanitized version of the repo path.
	if slug == "" {
		t.Fatal("expected non-empty slug")
	}
}

func TestPool_Acquire_RequiresPurpose(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	_, err := pool.Acquire(repoPath, ww.AcquireOptions{Purpose: ""})
	if err == nil {
		t.Fatal("expected error for empty purpose")
	}
}

func TestPool_Acquire_RejectsMultilinePurpose(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	_, err := pool.Acquire(repoPath, ww.AcquireOptions{Purpose: "line 1\nline 2"})
	if err == nil {
		t.Fatal("expected error for multiline purpose")
	}
}

func TestPool_Acquire_MissingChangeIDFallsBackToMain(t *testing.T) {
	repoPath := setupTestRepo(t)
	ensureMainBookmark(t, repoPath)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	wsPath, err := pool.Acquire(repoPath, ww.AcquireOptions{
		Purpose: "test purpose",
		Rev:     "deadbeefdead",
	})
	if err != nil {
		t.Fatalf("failed to acquire workspace: %v", err)
	}

	client := jj.New()
	currentChangeID, err := client.CurrentChangeID(wsPath)
	if err != nil {
		t.Fatalf("get current change id: %v", err)
	}
	mainChangeID, err := client.ChangeIDAt(wsPath, "main")
	if err != nil {
		t.Fatalf("get main change id: %v", err)
	}
	if currentChangeID == mainChangeID {
		t.Fatalf("expected change to differ from main, got %q", currentChangeID)
	}

	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("failed to list workspaces: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(list))
	}
	if list[0].Rev != currentChangeID {
		t.Fatalf("expected stored rev %q, got %q", currentChangeID, list[0].Rev)
	}

	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("failed to release workspace: %v", err)
	}
}

func TestPool_Acquire_ReusesAvailableWorkspace(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	// Claim and release a workspace
	wsPath1, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to claim workspace: %v", err)
	}

	if err := pool.Release(wsPath1); err != nil {
		t.Fatalf("failed to release workspace: %v", err)
	}

	// Claim again - should reuse the same workspace
	wsPath2, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to claim workspace second time: %v", err)
	}

	if wsPath1 != wsPath2 {
		t.Errorf("expected to reuse workspace %q, got %q", wsPath1, wsPath2)
	}

	if err := pool.Release(wsPath2); err != nil {
		t.Fatalf("failed to release workspace: %v", err)
	}

	wsPath3, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to claim workspace third time: %v", err)
	}

	if wsPath1 != wsPath3 {
		t.Errorf("expected to reuse workspace %q after second release, got %q", wsPath1, wsPath3)
	}

	if err := pool.Release(wsPath3); err != nil {
		t.Fatalf("failed to release workspace third time: %v", err)
	}
}

func TestPool_Acquire_ImmutableRevisionCreatesNewChange(t *testing.T) {
	repoPath := setupTestRepo(t)
	ensureMainBookmark(t, repoPath)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	message := "staging for todo test"
	wsPath, err := pool.Acquire(repoPath, ww.AcquireOptions{
		Purpose:          "test purpose",
		Rev:              "main",
		NewChangeMessage: message,
	})
	if err != nil {
		t.Fatalf("failed to acquire workspace: %v", err)
	}

	client := jj.New()
	currentChangeID, err := client.CurrentChangeID(wsPath)
	if err != nil {
		t.Fatalf("get current change id: %v", err)
	}
	mainChangeID, err := client.ChangeIDAt(wsPath, "main")
	if err != nil {
		t.Fatalf("get main change id: %v", err)
	}
	if currentChangeID == mainChangeID {
		t.Fatalf("expected change to differ from main, got %q", currentChangeID)
	}

	description, err := client.DescriptionAt(wsPath, "@")
	if err != nil {
		t.Fatalf("get change description: %v", err)
	}
	trimmedDescription := strings.TrimSpace(description)
	if trimmedDescription != message {
		t.Fatalf("expected change description %q, got %q", message, trimmedDescription)
	}

	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("failed to list workspaces: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(list))
	}
	if list[0].Rev != currentChangeID {
		t.Fatalf("expected stored rev %q, got %q", currentChangeID, list[0].Rev)
	}

	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("failed to release workspace: %v", err)
	}
}

func TestPool_Acquire_RevAtResolvesInSourceRepo(t *testing.T) {
	// Test that --rev=@ resolves the @ symbol in the source repo, not the workspace.
	// This is important because workspaces start with their own @ at root, but when
	// the user requests @ they mean the source repo's current revision.
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	client := jj.New()

	// Create a commit in the source repo so @ is distinct from root
	if _, err := client.NewChange(repoPath, "root()"); err != nil {
		t.Fatalf("failed to create new change in repo: %v", err)
	}
	if err := client.Describe(repoPath, "test commit in source repo"); err != nil {
		t.Fatalf("failed to describe change: %v", err)
	}

	// Get the source repo's @ change ID before acquiring
	sourceAtChangeID, err := client.CurrentChangeID(repoPath)
	if err != nil {
		t.Fatalf("get source @ change id: %v", err)
	}

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	// Acquire with --rev=@ (the default)
	wsPath, err := pool.Acquire(repoPath, ww.AcquireOptions{
		Purpose: "test purpose",
		Rev:     "@",
	})
	if err != nil {
		t.Fatalf("failed to acquire workspace: %v", err)
	}

	// The workspace's current change should have the source repo's @ as its parent
	parentChangeID, err := client.ChangeIDAt(wsPath, "@-")
	if err != nil {
		t.Fatalf("get parent change id: %v", err)
	}

	if parentChangeID != sourceAtChangeID {
		t.Errorf("expected workspace parent to be source repo @ (%s), got %s", sourceAtChangeID, parentChangeID)
	}

	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("failed to release workspace: %v", err)
	}
}

func TestPool_Acquire_CreatesMultipleWorkspaces(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	// Claim two workspaces without releasing
	wsPath1, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to claim workspace 1: %v", err)
	}

	wsPath2, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to claim workspace 2: %v", err)
	}

	if wsPath1 == wsPath2 {
		t.Error("expected different workspaces, got same path")
	}

	// Both should contain ws- prefix and be numbered
	if !strings.Contains(wsPath1, "ws-") {
		t.Errorf("expected ws- prefix in %q", wsPath1)
	}
	if !strings.Contains(wsPath2, "ws-") {
		t.Errorf("expected ws- prefix in %q", wsPath2)
	}

	if err := pool.Release(wsPath1); err != nil {
		t.Fatalf("failed to release workspace 1: %v", err)
	}
	if err := pool.Release(wsPath2); err != nil {
		t.Fatalf("failed to release workspace 2: %v", err)
	}

	wsPath3, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to claim workspace 3: %v", err)
	}

	if err := pool.Release(wsPath3); err != nil {
		t.Fatalf("failed to release workspace 3: %v", err)
	}
}

func TestPool_Release(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to claim workspace: %v", err)
	}

	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("failed to release workspace: %v", err)
	}

	wsPath2, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to acquire workspace after release: %v", err)
	}

	if err := pool.Release(wsPath2); err != nil {
		t.Fatalf("failed to release workspace again: %v", err)
	}
}

func TestPool_List(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	// Initially empty
	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("failed to list: %v", err)
	}

	if len(list) != 0 {
		t.Errorf("expected 0 workspaces, got %d", len(list))
	}

	// Claim one
	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to claim: %v", err)
	}

	list, err = pool.List(repoPath)
	if err != nil {
		t.Fatalf("failed to list: %v", err)
	}

	if len(list) != 1 {
		t.Errorf("expected 1 workspace, got %d", len(list))
	}

	if list[0].Path != wsPath {
		t.Errorf("expected path %q, got %q", wsPath, list[0].Path)
	}

	if list[0].Status != ww.StatusAcquired {
		t.Errorf("expected status claimed, got %s", list[0].Status)
	}
	if list[0].Purpose != "test purpose" {
		t.Errorf("expected purpose to be set, got %q", list[0].Purpose)
	}

	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("failed to release workspace: %v", err)
	}

}

func TestPool_List_SortsByStatusThenName(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	wsPath1, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to acquire workspace 1: %v", err)
	}

	wsPath2, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to acquire workspace 2: %v", err)
	}

	wsPath3, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to acquire workspace 3: %v", err)
	}

	if err := pool.Release(wsPath2); err != nil {
		t.Fatalf("failed to release workspace 2: %v", err)
	}

	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("failed to list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 workspaces, got %d", len(list))
	}

	if list[0].Name != filepath.Base(wsPath1) {
		t.Fatalf("expected first workspace %q, got %q", filepath.Base(wsPath1), list[0].Name)
	}
	if list[1].Name != filepath.Base(wsPath3) {
		t.Fatalf("expected second workspace %q, got %q", filepath.Base(wsPath3), list[1].Name)
	}
	if list[2].Name != filepath.Base(wsPath2) {
		t.Fatalf("expected third workspace %q, got %q", filepath.Base(wsPath2), list[2].Name)
	}

	if list[0].Status != ww.StatusAcquired {
		t.Fatalf("expected first workspace status acquired, got %s", list[0].Status)
	}
	if list[1].Status != ww.StatusAcquired {
		t.Fatalf("expected second workspace status acquired, got %s", list[1].Status)
	}
	if list[2].Status != ww.StatusAvailable {
		t.Fatalf("expected third workspace status available, got %s", list[2].Status)
	}
}

func TestPool_DefaultOptions(t *testing.T) {
	// Just verify Open() doesn't error
	pool := openDefaultPool(t)
	if pool == nil {
		t.Error("expected non-nil pool")
	}
}

func TestRepoRoot(t *testing.T) {
	repoPath := setupTestRepo(t)

	root, err := ww.RepoRoot(repoPath)
	if err != nil {
		t.Fatalf("failed to get repo root: %v", err)
	}

	// RepoRoot returns normalized paths (without macOS /private prefix).
	expected := normalizePath(repoPath)
	if root != expected {
		t.Errorf("expected %q, got %q", expected, root)
	}
}

func TestRepoRoot_NotARepo(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ww.RepoRoot(tmpDir)
	if err == nil {
		t.Error("expected error for non-repo directory")
	}
}

func TestRepoRootFromPath_Workspace(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to acquire workspace: %v", err)
	}

	root, err := ww.RepoRootFromPathWithOptions(wsPath, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})
	if err != nil {
		t.Fatalf("failed to resolve repo root: %v", err)
	}
	if root != repoPath {
		t.Fatalf("expected repo path %q, got %q", repoPath, root)
	}
}

func TestRepoRootFromPath_Repo(t *testing.T) {
	repoPath := setupTestRepo(t)

	root, err := ww.RepoRootFromPathWithOptions(repoPath, ww.Options{
		StateDir:      "",
		WorkspacesDir: "",
	})
	if err != nil {
		t.Fatalf("failed to resolve repo root: %v", err)
	}
	// RepoRootFromPath returns normalized paths (without macOS /private prefix).
	expected := normalizePath(repoPath)
	if root != expected {
		t.Fatalf("expected repo path %q, got %q", expected, root)
	}
}

func TestRepoRootFromPath_NotARepo(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ww.RepoRootFromPath(tmpDir)
	if err == nil {
		t.Fatal("expected error for non-repo directory")
	}
}

func TestPool_DestroyAll(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	// Acquire two workspaces
	wsPath1, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to acquire workspace 1: %v", err)
	}

	wsPath2, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to acquire workspace 2: %v", err)
	}

	// Verify workspaces exist
	if _, err := os.Stat(wsPath1); os.IsNotExist(err) {
		t.Fatalf("workspace 1 does not exist: %s", wsPath1)
	}
	if _, err := os.Stat(wsPath2); os.IsNotExist(err) {
		t.Fatalf("workspace 2 does not exist: %s", wsPath2)
	}

	// Destroy all
	if err := pool.DestroyAll(repoPath); err != nil {
		t.Fatalf("failed to destroy all: %v", err)
	}

	// Verify workspaces are gone
	if _, err := os.Stat(wsPath1); !os.IsNotExist(err) {
		t.Error("workspace 1 should have been deleted")
	}
	if _, err := os.Stat(wsPath2); !os.IsNotExist(err) {
		t.Error("workspace 2 should have been deleted")
	}

	// List should return empty
	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("failed to list: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 workspaces after destroy-all, got %d", len(list))
	}
}

func TestPool_DestroyAll_NoWorkspaces(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	// Destroy all when there are no workspaces should not error
	if err := pool.DestroyAll(repoPath); err != nil {
		t.Fatalf("destroy-all with no workspaces should not error: %v", err)
	}
}

func TestPool_WorkspaceNameForPath(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	stateDir := t.TempDir()

	pool := openPool(t, ww.Options{
		StateDir:      stateDir,
		WorkspacesDir: workspacesDir,
	})

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("failed to acquire workspace: %v", err)
	}

	name, err := pool.WorkspaceNameForPath(wsPath)
	if err != nil {
		t.Fatalf("failed to resolve workspace name: %v", err)
	}
	if name == "" {
		t.Fatal("expected workspace name")
	}
}

func TestPool_WorkspaceNameForPath_NotInWorkspace(t *testing.T) {
	pool := openDefaultPool(t)

	_, err := pool.WorkspaceNameForPath(t.TempDir())
	if err == nil {
		t.Fatal("expected error for non-workspace directory")
	}
	if !errors.Is(err, ww.ErrWorkspaceRootNotFound) {
		t.Fatalf("expected workspace root not found error, got %v", err)
	}
}
