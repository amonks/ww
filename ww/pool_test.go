package ww_test

import (
	"errors"
	"os"
	"os/exec"
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

// runJJ runs a raw jj command. pkg/jj's Rebase wraps `-b` only, and the
// staleness tests need `-r` to rewrite one named change.
func runJJ(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("jj", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jj %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// setupPoolWithMain builds a repo whose main carries a file, so a change
// parked below it has a tree that a rewrite can change — which is what jj's
// staleness check keys on — plus a pool over fresh directories.
func setupPoolWithMain(t *testing.T) (string, *ww.Pool) {
	t.Helper()
	repoPath := setupTestRepo(t)
	if err := os.WriteFile(filepath.Join(repoPath, "f.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	client := jj.New()
	if err := client.Describe(repoPath, "add f"); err != nil {
		t.Fatalf("describe: %v", err)
	}
	ensureMainBookmark(t, repoPath)
	if _, err := client.NewChange(repoPath, "@"); err != nil {
		t.Fatalf("new change: %v", err)
	}

	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	return repoPath, openPool(t, ww.Options{StateDir: t.TempDir(), WorkspacesDir: workspacesDir})
}

// requireStale asserts a workspace really is in the state the staleness tests
// mean to put it in. Without it a test that stops reproducing the condition —
// a jj change, a drifted fixture — passes while proving nothing.
func requireStale(t *testing.T, wsPath string) {
	t.Helper()
	if _, err := jj.New().CurrentChangeID(wsPath); !jj.IsStaleWorkingCopy(err) {
		t.Fatalf("expected a stale working copy, got %v", err)
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

func TestPool_Release_RemovesGitignoredFiles(t *testing.T) {
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

	if err := os.WriteFile(filepath.Join(wsPath, ".gitignore"), []byte("build/\n*.log\n"), 0644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsPath, "README.md"), []byte("# hi"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wsPath, "build", "nested"), 0755); err != nil {
		t.Fatalf("mkdir build/nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsPath, "build", "nested", "out.txt"), []byte("out"), 0644); err != nil {
		t.Fatalf("write build/nested/out.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsPath, "test.log"), []byte("log"), 0644); err != nil {
		t.Fatalf("write test.log: %v", err)
	}

	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("failed to release workspace: %v", err)
	}

	if _, err := os.Stat(filepath.Join(wsPath, "build")); !os.IsNotExist(err) {
		t.Errorf("gitignored build/ should have been removed on release, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(wsPath, "test.log")); !os.IsNotExist(err) {
		t.Errorf("gitignored test.log should have been removed on release, got err=%v", err)
	}

	if _, err := os.Stat(filepath.Join(wsPath, ".jj")); err != nil {
		t.Errorf(".jj directory should be preserved after release: %v", err)
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

// writeOnCreateHook writes a project ww.toml at repoPath whose on-create
// script is the given shell body. A TOML multi-line literal string is used so
// the body needs no escaping.
func writeOnCreateHook(t *testing.T, repoPath, body string) {
	t.Helper()
	cfg := "[workspace]\non-create = '''\n" + body + "\n'''\n"
	if err := os.WriteFile(filepath.Join(repoPath, "ww.toml"), []byte(cfg), 0644); err != nil {
		t.Fatalf("write ww.toml: %v", err)
	}
}

// TestPool_Acquire_RunsOnCreateHookOnEveryAcquire pins the behavior the repo's
// own ww.toml depends on: the on-create hook regenerates per-workspace state
// (.envrc.local, .envrc.secrets) and must therefore run on reuse of an
// already-provisioned workspace, not only on first creation. A workspace that
// silently skipped the hook would serve stale decrypted secrets.
func TestPool_Acquire_RunsOnCreateHookOnEveryAcquire(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)

	// The marker lives outside the workspace: release deletes untracked files.
	marker := filepath.Join(t.TempDir(), "runs")
	t.Setenv("WW_TEST_MARKER", marker)
	writeOnCreateHook(t, repoPath, `printf '%s\n' "$PWD" >> "$WW_TEST_MARKER"`)

	pool := openPool(t, ww.Options{
		StateDir:      t.TempDir(),
		WorkspacesDir: workspacesDir,
	})

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("release: %v", err)
	}

	wsPath2, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if wsPath2 != wsPath {
		t.Fatalf("expected the pooled workspace to be reused, got %q then %q", wsPath, wsPath2)
	}
	if err := pool.Release(wsPath2); err != nil {
		t.Fatalf("release: %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected on-create to run once per acquire (2 runs), got %d: %q", len(lines), lines)
	}
	for i, line := range lines {
		if normalizePath(line) != wsPath {
			t.Errorf("run %d: expected hook cwd %q, got %q", i+1, wsPath, normalizePath(line))
		}
	}
}

// TestPool_Acquire_OnCreateHookFailureIsFatal ensures a failing hook aborts the
// acquire and returns the workspace to the pool. Handing back a workspace whose
// setup failed is how an agent would end up with stale or missing credentials.
func TestPool_Acquire_OnCreateHookFailureIsFatal(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)

	writeOnCreateHook(t, repoPath, "exit 1")

	pool := openPool(t, ww.Options{
		StateDir:      t.TempDir(),
		WorkspacesDir: workspacesDir,
	})

	if _, err := pool.Acquire(repoPath, acquireOptions()); err == nil {
		t.Fatal("expected acquire to fail when the on-create hook exits non-zero")
	} else if !strings.Contains(err.Error(), "on-create script") {
		t.Fatalf("expected an on-create script error, got %v", err)
	}

	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(list))
	}
	if list[0].Status != ww.StatusAvailable {
		t.Fatalf("expected the workspace to be released back to the pool, got status %s", list[0].Status)
	}
}

// TestPool_Acquire_SkipHooksSkipsOnCreate documents that SkipHooks callers opt
// out of per-workspace setup entirely.
func TestPool_Acquire_SkipHooksSkipsOnCreate(t *testing.T) {
	repoPath := setupTestRepo(t)
	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)

	marker := filepath.Join(t.TempDir(), "runs")
	t.Setenv("WW_TEST_MARKER", marker)
	writeOnCreateHook(t, repoPath, `printf 'ran\n' >> "$WW_TEST_MARKER"`)

	pool := openPool(t, ww.Options{
		StateDir:      t.TempDir(),
		WorkspacesDir: workspacesDir,
	})

	opts := acquireOptions()
	opts.SkipHooks = true
	wsPath, err := pool.Acquire(repoPath, opts)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("release: %v", err)
	}

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("expected the on-create hook not to run with SkipHooks, marker stat err = %v", err)
	}
}

// A workspace idling in the pool holds a parked change that the source repo
// can rewrite out from under it — a rebase, a revert inserted before it, an
// undo of either. jj then refuses to touch that workspace until someone runs
// update-stale, and the workspace it happens to is the next one Acquire hands
// out, so the failure lands on a caller that did nothing wrong.
func TestPool_Acquire_RecoversStaleWorkingCopy(t *testing.T) {
	repoPath, pool := setupPoolWithMain(t)

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	wsName, err := pool.WorkspaceNameForPath(wsPath)
	if err != nil {
		t.Fatalf("workspace name: %v", err)
	}
	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("release: %v", err)
	}

	// Rewrite the parked change from the source repo, leaving the
	// workspace's on-disk copy behind.
	runJJ(t, repoPath, "rebase", "-r", wsName+"@", "-d", "main")
	requireStale(t, wsPath)

	reacquired, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("acquire after the parked change was rewritten: %v", err)
	}
	if reacquired != wsPath {
		t.Fatalf("expected the pooled workspace %s, got %s", wsPath, reacquired)
	}
}

// The same rewrite can land on a workspace that is checked out and in use,
// where release does not paper over it: update-stale would drop whatever the
// session wrote and jj never snapshotted, so the workspace stays acquired for
// its owner to look at.
func TestPool_Release_RefusesStaleWorkingCopy(t *testing.T) {
	repoPath, pool := setupPoolWithMain(t)

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	wsName, err := pool.WorkspaceNameForPath(wsPath)
	if err != nil {
		t.Fatalf("workspace name: %v", err)
	}

	// Rebasing off main empties the change's tree, so the on-disk copy no
	// longer matches what the repo says the workspace is checked out to.
	runJJ(t, repoPath, "rebase", "-r", wsName+"@", "-d", "root()")
	requireStale(t, wsPath)

	err = pool.Release(wsPath)
	if err == nil {
		t.Fatal("expected release of a stale workspace to fail")
	}
	if !jj.IsStaleWorkingCopy(err) {
		t.Fatalf("expected a stale working copy error, got %v", err)
	}
	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Status != ww.StatusAcquired {
		t.Fatalf("expected the workspace to stay acquired, got %+v", list)
	}
}

// Acquire marks the state row before it does any jj work, so an acquire that
// fails partway has to hand the workspace back. Leaving the row acquired
// retires a workspace from the pool under a purpose that never ran, and
// nothing ever cleans it up.
func TestPool_Acquire_ReturnsWorkspaceWhenNewChangeFails(t *testing.T) {
	repoPath := setupTestRepo(t)
	ensureMainBookmark(t, repoPath)

	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	pool := openPool(t, ww.Options{StateDir: t.TempDir(), WorkspacesDir: workspacesDir})

	if _, err := pool.Acquire(repoPath, ww.AcquireOptions{
		Rev:     "no-such-rev",
		Purpose: "doomed",
	}); err == nil {
		t.Fatal("expected acquire at a missing revision to fail")
	}

	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(list))
	}
	if list[0].Status != ww.StatusAvailable {
		t.Fatalf("failed acquire leaked workspace %s in status %s", list[0].Name, list[0].Status)
	}
}

// The same has to hold for a failure after the jj work, and this one is the
// one that would actually be met: a broken config fails every acquire, so a
// leak here drains the pool a workspace at a time and manufactures new ones
// forever.
func TestPool_Acquire_ReturnsWorkspaceWhenConfigFails(t *testing.T) {
	repoPath := setupTestRepo(t)
	ensureMainBookmark(t, repoPath)
	// config.Load rejects a repo carrying both config locations.
	if err := os.WriteFile(filepath.Join(repoPath, "ww.toml"), []byte("[workspace]\n"), 0644); err != nil {
		t.Fatalf("write ww.toml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoPath, ".ww"), 0755); err != nil {
		t.Fatalf("mkdir .ww: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, ".ww", "config.toml"), []byte("[workspace]\n"), 0644); err != nil {
		t.Fatalf("write .ww/config.toml: %v", err)
	}

	workspacesDir := t.TempDir()
	workspacesDir, _ = filepath.EvalSymlinks(workspacesDir)
	pool := openPool(t, ww.Options{StateDir: t.TempDir(), WorkspacesDir: workspacesDir})

	if _, err := pool.Acquire(repoPath, acquireOptions()); err == nil {
		t.Fatal("expected acquire to fail on an unloadable config")
	}

	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(list))
	}
	if list[0].Status != ww.StatusAvailable {
		t.Fatalf("failed acquire leaked workspace %s in status %s", list[0].Name, list[0].Status)
	}
}
