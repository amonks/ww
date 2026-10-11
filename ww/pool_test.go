package ww_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"monks.co/pkg/jj"
	"monks.co/ww/ww"
)

// TestMain points HOME at a scratch directory for the whole package. Several
// tests open the pool at its default paths, which live under HOME, and a test
// run must never read or write the operator's real pool state. jj's user
// config lives under HOME too, so it gets one of its own.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ww-test-home-")
	if err != nil {
		panic(err)
	}
	jjConfig := filepath.Join(home, "jj.toml")
	if err := os.WriteFile(jjConfig, []byte("user.name = \"t\"\nuser.email = \"t@example.com\"\n"), 0o644); err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("JJ_CONFIG", jjConfig)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// workspaceName names the pool workspace at wsPath.
func workspaceName(t *testing.T, pool *ww.Pool, wsPath string) string {
	t.Helper()
	loc, err := pool.Locate(wsPath)
	if err != nil {
		t.Fatalf("locate %s: %v", wsPath, err)
	}
	if loc.Workspace == "" {
		t.Fatalf("%s is not a pool workspace", wsPath)
	}
	return loc.Workspace
}

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

// The fallback is for a bare change ID. A change asked for by revset —
// change_id(...), which is how the CLI hands on a --rev it pinned inside a
// workspace — is meant exactly, and a missing one is an error.
func TestPool_Acquire_MissingChangeIDRevsetDoesNotFallBack(t *testing.T) {
	repoPath := setupTestRepo(t)
	ensureMainBookmark(t, repoPath)
	workspacesDir, _ := filepath.EvalSymlinks(t.TempDir())
	pool := openPool(t, ww.Options{StateDir: t.TempDir(), WorkspacesDir: workspacesDir})

	if _, err := pool.Acquire(repoPath, ww.AcquireOptions{
		Purpose: "test purpose",
		Rev:     "change_id(kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk)",
	}); err == nil {
		t.Fatal("acquire on a missing change_id() revset succeeded")
	}

	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Status != ww.StatusAvailable {
		t.Fatalf("expected the workspace handed back, got %+v", list)
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

// A workspace's creation time is when it was created, and acquiring it again
// does not rewrite it: each acquire stamps its own instant, each release its
// own, and Since follows whichever the workspace is in now.
func TestPool_Acquire_KeepsCreationTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repoPath := setupTestRepo(t)
		workspacesDir, _ := filepath.EvalSymlinks(t.TempDir())
		pool := openPool(t, ww.Options{StateDir: t.TempDir(), WorkspacesDir: workspacesDir})

		only := func() ww.Info {
			t.Helper()
			list, err := pool.List(repoPath)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(list) != 1 {
				t.Fatalf("expected 1 workspace, got %d", len(list))
			}
			return list[0]
		}

		created := time.Now()
		wsPath, err := pool.Acquire(repoPath, acquireOptions())
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}

		time.Sleep(time.Hour)
		released := time.Now()
		if err := pool.Release(wsPath); err != nil {
			t.Fatalf("release: %v", err)
		}
		if got := only(); !got.CreatedAt.Equal(created) || !got.Since().Equal(released) {
			t.Errorf("parked: created %v since %v, want created %v since %v", got.CreatedAt, got.Since(), created, released)
		}

		time.Sleep(2 * time.Hour)
		reacquired := time.Now()
		again, err := pool.Acquire(repoPath, acquireOptions())
		if err != nil {
			t.Fatalf("reacquire: %v", err)
		}
		if again != wsPath {
			t.Fatalf("reacquire took %s, want the pooled %s", again, wsPath)
		}
		got := only()
		if !got.CreatedAt.Equal(created) {
			t.Errorf("reacquired created %v, want the creation %v", got.CreatedAt, created)
		}
		if !got.AcquiredAt.Equal(reacquired) || !got.Since().Equal(reacquired) {
			t.Errorf("reacquired acquired %v since %v, want %v", got.AcquiredAt, got.Since(), reacquired)
		}

		if err := pool.Release(wsPath); err != nil {
			t.Fatalf("release: %v", err)
		}
	})
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
	// RepoRootFromPath returns normalized paths (without macOS /private prefix).
	if expected := normalizePath(repoPath); root != expected {
		t.Fatalf("expected repo path %q, got %q", expected, root)
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

// workspacesDirs returns a fresh workspaces directory two ways: plainly, and
// reached through a symlink, as thor reaches /home through /usr/home. jj
// reports a workspace root with symlinks resolved, while the pool records the
// path it built, so the two spellings of a workspace differ in the second.
func workspacesDirs(t *testing.T) map[string]string {
	t.Helper()
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	base, _ := filepath.EvalSymlinks(t.TempDir())
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	return map[string]string{"plain": plain, "symlinked": link}
}

func TestPool_Locate(t *testing.T) {
	for kind, workspacesDir := range workspacesDirs(t) {
		t.Run(kind, func(t *testing.T) {
			repoPath := setupTestRepo(t)
			pool := openPool(t, ww.Options{StateDir: t.TempDir(), WorkspacesDir: workspacesDir})

			wsPath, err := pool.Acquire(repoPath, acquireOptions())
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			for _, dir := range []string{filepath.Join(repoPath, "sub"), filepath.Join(wsPath, "sub")} {
				if err := os.Mkdir(dir, 0755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			}

			source := ww.Location{Repo: normalizePath(repoPath)}
			inWorkspace := ww.Location{Repo: normalizePath(repoPath), Workspace: "ws-001"}
			for _, tc := range []struct {
				path string
				want ww.Location
			}{
				{repoPath, source},
				{filepath.Join(repoPath, "sub"), source},
				// jj's own answer here is the workspace root; the pool's is
				// the repo the workspace was made from.
				{wsPath, inWorkspace},
				{filepath.Join(wsPath, "sub"), inWorkspace},
			} {
				got, err := pool.Locate(tc.path)
				if err != nil {
					t.Fatalf("locate %s: %v", tc.path, err)
				}
				if got != tc.want {
					t.Errorf("locate %s: got %+v, want %+v", tc.path, got, tc.want)
				}
			}
		})
	}
}

func TestPool_Locate_NotARepo(t *testing.T) {
	pool := openPool(t, ww.Options{StateDir: t.TempDir(), WorkspacesDir: t.TempDir()})

	_, err := pool.Locate(t.TempDir())
	if !errors.Is(err, ww.ErrWorkspaceRootNotFound) {
		t.Fatalf("expected workspace root not found error, got %v", err)
	}
}

// A jj workspace sitting where the pool puts its own, with no row on record,
// is a pool workspace whose state was lost. Taking it for a source repo would
// register it as one; it is refused instead. A repo nested deeper is not in a
// workspace's place and is its own repo.
func TestPool_Locate_UnrecordedWorkspaceSlot(t *testing.T) {
	for kind, workspacesDir := range workspacesDirs(t) {
		t.Run(kind, func(t *testing.T) {
			repoPath := setupTestRepo(t)
			pool := openPool(t, ww.Options{StateDir: t.TempDir(), WorkspacesDir: workspacesDir})

			orphan := filepath.Join(workspacesDir, "some-repo", "ws-009")
			if err := os.MkdirAll(orphan, 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := jj.New().Init(orphan); err != nil {
				t.Fatalf("init orphan: %v", err)
			}
			if _, err := pool.Locate(orphan); !errors.Is(err, ww.ErrRepoPathNotFound) {
				t.Fatalf("locate unrecorded workspace slot: expected ErrRepoPathNotFound, got %v", err)
			}

			wsPath, err := pool.Acquire(repoPath, acquireOptions())
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			nested := filepath.Join(wsPath, "scratch")
			if err := os.Mkdir(nested, 0755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := jj.New().Init(nested); err != nil {
				t.Fatalf("init nested: %v", err)
			}
			got, err := pool.Locate(nested)
			if err != nil {
				t.Fatalf("locate nested repo: %v", err)
			}
			resolved, _ := filepath.EvalSymlinks(nested)
			if want := (ww.Location{Repo: normalizePath(resolved)}); got != want {
				t.Fatalf("locate nested repo: got %+v, want %+v", got, want)
			}
		})
	}
}

// Handing a pool workspace to a repo-keyed call in place of its source repo
// is refused, rather than registering the workspace as a repo with a pool of
// its own.
func TestPool_RefusesAWorkspaceAsASourceRepo(t *testing.T) {
	for kind, workspacesDir := range workspacesDirs(t) {
		t.Run(kind, func(t *testing.T) {
			repoPath := setupTestRepo(t)
			pool := openPool(t, ww.Options{StateDir: t.TempDir(), WorkspacesDir: workspacesDir})

			wsPath, err := pool.Acquire(repoPath, acquireOptions())
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}

			// Both spellings of the path: as the pool built it, and as jj
			// reports it, with symlinks resolved.
			resolved, _ := filepath.EvalSymlinks(wsPath)
			for _, path := range []string{wsPath, resolved} {
				if _, err := pool.Acquire(path, acquireOptions()); !errors.Is(err, ww.ErrNotSourceRepo) {
					t.Errorf("acquire with workspace %s as repo: expected ErrNotSourceRepo, got %v", path, err)
				}
				if _, err := pool.List(path); !errors.Is(err, ww.ErrNotSourceRepo) {
					t.Errorf("list with workspace %s as repo: expected ErrNotSourceRepo, got %v", path, err)
				}
				// What the refusal turns away, Locate resolves.
				if loc, err := pool.Locate(path); err != nil || loc.Workspace != "ws-001" {
					t.Errorf("locate %s: got %+v, %v; want ws-001", path, loc, err)
				}
			}

			list, err := pool.List(repoPath)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(list) != 1 {
				t.Fatalf("expected the source repo's one workspace, got %d", len(list))
			}
		})
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
	wsName := workspaceName(t, pool, wsPath)
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

// The same rewrite can land on a workspace that is checked out and in use.
// Release recovers that one too, and the reason it is safe to is that
// update-stale does not lose what the workspace was holding: it snapshots the
// disk into a divergent commit under the change's own id first. So a stale
// release preserves a session's last writes in the repo, where the row's Rev
// already points, rather than dropping them.
//
// Release refused this case for a long time, on the belief that the edits were
// destroyed. They are not, and the refusal cost a workspace stuck out of the
// pool until someone cleared it by hand.
func TestPool_Release_RecoversStaleWorkingCopyKeepingItsWork(t *testing.T) {
	repoPath, pool := setupPoolWithMain(t)

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	wsName := workspaceName(t, pool, wsPath)

	// Work jj has not snapshotted, of the kind a session would leave.
	const work = "PRECIOUS SESSION WORK\n"
	if err := os.WriteFile(filepath.Join(wsPath, "f.txt"), []byte(work), 0644); err != nil {
		t.Fatalf("write work: %v", err)
	}

	// Rebasing off main empties the change's tree, so the on-disk copy no
	// longer matches what the repo says the workspace is checked out to.
	runJJ(t, repoPath, "rebase", "-r", wsName+"@", "-d", "root()")
	requireStale(t, wsPath)

	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("release of a stale workspace: %v", err)
	}

	list, err := pool.List(repoPath)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Status != ww.StatusAvailable {
		t.Fatalf("expected the workspace back in the pool, got %+v", list)
	}
	if _, err := jj.New().CurrentChangeID(wsPath); err != nil {
		t.Fatalf("workspace still unusable after release: %v", err)
	}

	// The work is still in the repo, and says where it came from — an
	// undescribed head under a divergent id is not something anyone would
	// recognize as their own session's last writes.
	client := jj.New()
	commits, err := client.CommitIDsForRevset(repoPath, "all()")
	if err != nil {
		t.Fatalf("list commits: %v", err)
	}
	var found string
	for _, c := range commits {
		if b, err := client.FileShow(repoPath, c, "f.txt"); err == nil && string(b) == work {
			found = c
		}
	}
	if found == "" {
		t.Fatal("the unsnapshotted work did not survive the release")
	}
	desc, err := client.DescriptionAt(repoPath, found)
	if err != nil {
		t.Fatalf("description: %v", err)
	}
	if !strings.Contains(desc, wsName) || !strings.Contains(desc, "stale") {
		t.Errorf("recovered work described as %q, want it to name %s and say it went stale", desc, wsName)
	}
}

// The common case by far: staleness lands in bulk on idle workspaces, whose
// parked change is empty. There is nothing to recover, and the recovery must
// leave no sibling under that change's id — an empty one would be residue per
// stale release, and nameRecoveredWork declines to describe an empty commit, so
// only counting commits can see it.
func TestPool_Release_StaleButEmptyLeavesNothingBehind(t *testing.T) {
	repoPath, pool := setupPoolWithMain(t)

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	wsName := workspaceName(t, pool, wsPath)
	client := jj.New()
	// While the workspace is still readable: the rewrite below keeps the id.
	changeID, err := client.ChangeIDAt(wsPath, "@")
	if err != nil {
		t.Fatalf("change id: %v", err)
	}

	runJJ(t, repoPath, "rebase", "-r", wsName+"@", "-d", "root()")
	requireStale(t, wsPath)

	before, err := client.CommitIDsForRevset(repoPath, "change_id("+changeID+")")
	if err != nil {
		t.Fatalf("list commits: %v", err)
	}

	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("release: %v", err)
	}

	after, err := client.CommitIDsForRevset(repoPath, "change_id("+changeID+")")
	if err != nil {
		t.Fatalf("list commits: %v", err)
	}
	for _, c := range after {
		if !slices.Contains(before, c) {
			desc, _ := client.DescriptionAt(repoPath, c)
			t.Errorf("empty stale release left a sibling %s under the parked change: %q", c, desc)
		}
	}
}

// A change can be divergent before anything goes stale — two concurrent
// operations elsewhere rewriting it is enough — so the commits under its id
// after a recovery are not all the recovery's doing. Naming one of the others
// would put this workspace's name on an unrelated commit, and DescribeAt
// rewrites what it describes and rebases its descendants: a repair for one
// stale workspace would make another one stale.
func TestPool_Release_StaleAlreadyDivergentNamesOnlyWhatItRecovered(t *testing.T) {
	repoPath, pool := setupPoolWithMain(t)

	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	client := jj.New()
	changeID, err := client.ChangeIDAt(wsPath, "@")
	if err != nil {
		t.Fatalf("change id: %v", err)
	}

	// Give the change a second, non-empty, undescribed incarnation that has
	// nothing to do with any recovery — the shape nameRecoveredWork must not
	// touch. Two operations racing from the same parent op diverge it.
	if err := os.WriteFile(filepath.Join(wsPath, "unrelated.txt"), []byte("elsewhere\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runJJ(t, wsPath, "status")
	runJJ(t, repoPath, "--at-operation", "@", "rebase", "-r", changeID, "-d", "root()")
	runJJ(t, repoPath, "--at-operation", "@-", "rebase", "-r", changeID, "-d", "main")

	divergent, err := client.CommitIDsForRevset(repoPath, "change_id("+changeID+")")
	if err != nil {
		t.Fatalf("list divergent: %v", err)
	}
	if len(divergent) < 2 {
		t.Skipf("expected the change to be divergent before release, got %d commits", len(divergent))
	}
	// Nothing on disk is unsnapshotted here — the divergence came from the
	// concurrent rewrites, not from the workspace — so a correct recovery
	// creates no commit and names nothing. Assert on that rather than on the
	// pre-existing ids: describing a commit changes its id, so a check that
	// looked them up afterwards would miss exactly the damage it is for.
	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("release: %v", err)
	}

	commits, err := client.CommitIDsForRevset(repoPath, "all()")
	if err != nil {
		t.Fatalf("list commits: %v", err)
	}
	for _, c := range commits {
		if desc, err := client.DescriptionAt(repoPath, c); err == nil && strings.Contains(desc, "recovered from") {
			t.Errorf("release named pre-existing divergence as recovered work: %s %q", c, desc)
		}
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
