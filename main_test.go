package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
	"monks.co/ww/ww"
)

// runMainEnv makes the test binary act as ww itself. Each CLI invocation in
// these tests is a fresh process in its own working directory, with flag
// state, cwd, and exit code exactly as a shell would give them.
const runMainEnv = "WW_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) != "" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// isolate points HOME, and with it the whole pool, at a scratch directory, and
// gives jj a config of its own there. A test must never touch the operator's
// real pool: a mistaken call writes into its state.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	jjConfig := filepath.Join(home, "jj.toml")
	if err := os.WriteFile(jjConfig, []byte("user.name = \"t\"\nuser.email = \"t@example.com\"\n"), 0o644); err != nil {
		t.Fatalf("write jj config: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("JJ_CONFIG", jjConfig)
	return home
}

func initRepo(t *testing.T, home string) string {
	t.Helper()
	repo := filepath.Join(home, "app")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runIn(t, repo, "jj", "git", "init", "--quiet")
	return repo
}

func runIn(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = exitErr.Stderr
		}
		t.Fatalf("%s %s in %s: %v: %s", name, strings.Join(args, " "), dir, err, stderr)
	}
	return strings.TrimSpace(string(out))
}

// wwIn runs the ww CLI in dir and returns its trimmed stdout and stderr.
func wwIn(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), runMainEnv+"=1")
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func mustWW(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := wwIn(t, dir, args...)
	if err != nil {
		t.Fatalf("ww %s in %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), dir, err, stdout, stderr)
	}
	return stdout
}

func listWorkspaces(t *testing.T, dir string) map[string]ww.Status {
	t.Helper()
	var items []ww.Info
	if err := json.Unmarshal([]byte(mustWW(t, dir, "list", "--json", "--all")), &items); err != nil {
		t.Fatalf("decode ww list: %v", err)
	}
	statuses := map[string]ww.Status{}
	for _, item := range items {
		statuses[item.Name] = item.Status
	}
	return statuses
}

// repoSources reads the source paths of every repo the pool has registered,
// straight from its state.
func repoSources(t *testing.T, home string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, ".local", "state", "ww", "state.db"))
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT source_path FROM repos ORDER BY source_path")
	if err != nil {
		t.Fatalf("query repos: %v", err)
	}
	defer rows.Close()
	var sources []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			t.Fatalf("scan repo: %v", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read repos: %v", err)
	}
	return sources
}

// Every command run from inside a pool workspace works on the pool of the repo
// that workspace came from — not on a pool keyed on the workspace itself.
func TestCLI_FromInsideAWorkspace(t *testing.T) {
	home := isolate(t)
	repo := initRepo(t, home)

	ws1 := mustWW(t, repo, "acquire", "--purpose", "first")
	sub := filepath.Join(ws1, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if got := listWorkspaces(t, sub); got["ws-001"] != ww.StatusAcquired || len(got) != 1 {
		t.Fatalf("list from inside ws-001: got %v, want ws-001 acquired", got)
	}

	ws2 := mustWW(t, sub, "acquire", "--purpose", "second")
	if filepath.Dir(ws2) != filepath.Dir(ws1) || filepath.Base(ws2) != "ws-002" {
		t.Fatalf("acquire from inside ws-001 gave %s, want ws-002 beside %s", ws2, ws1)
	}

	if out := mustWW(t, ws2, "release"); out != "released workspace ws-002" {
		t.Fatalf("release from inside ws-002 printed %q", out)
	}
	if out := mustWW(t, sub, "release"); out != "released workspace ws-001" {
		t.Fatalf("release from inside ws-001 printed %q", out)
	}
	if got := listWorkspaces(t, repo); got["ws-001"] != ww.StatusAvailable || got["ws-002"] != ww.StatusAvailable || len(got) != 2 {
		t.Fatalf("after releases: got %v, want ws-001 and ws-002 available", got)
	}

	if got := repoSources(t, home); len(got) != 1 || filepath.Base(got[0]) != "app" {
		t.Fatalf("registered repos: got %v, want only the source repo", got)
	}

	// destroy-all would delete the workspace it runs in, along with every
	// other session's, so from inside one it is refused.
	if _, stderr, err := wwIn(t, ws1, "destroy-all"); err == nil || !strings.Contains(stderr, "inside pool workspace ws-001") {
		t.Fatalf("destroy-all from inside ws-001: err %v, stderr %q", err, stderr)
	}
	if got := listWorkspaces(t, repo); len(got) != 2 {
		t.Fatalf("after refused destroy-all: got %v, want both workspaces", got)
	}
	mustWW(t, repo, "destroy-all")
	if got := listWorkspaces(t, repo); len(got) != 0 {
		t.Fatalf("after destroy-all from the source repo: got %v, want none", got)
	}
}

// A bare release outside any pool workspace has nothing to name, and says so.
func TestCLI_ReleaseOutsideAWorkspaceNeedsAName(t *testing.T) {
	home := isolate(t)
	repo := initRepo(t, home)
	mustWW(t, repo, "acquire", "--purpose", "held")

	_, stderr, err := wwIn(t, repo, "release")
	if err == nil {
		t.Fatal("bare release in the source repo succeeded")
	}
	if !strings.Contains(stderr, "not inside a pool workspace") {
		t.Fatalf("bare release in the source repo: stderr %q", stderr)
	}
	if got := listWorkspaces(t, repo); got["ws-001"] != ww.StatusAcquired {
		t.Fatalf("ws-001 should still be acquired, got %v", got)
	}
}

// --rev is a revset typed where the shell is, so inside a pool workspace @ is
// that workspace's working copy, as it is to jj there — not the source repo's.
func TestCLI_RevResolvesWhereItWasTyped(t *testing.T) {
	home := isolate(t)
	repo := initRepo(t, home)
	runIn(t, repo, "jj", "describe", "-m", "source work")

	ws1 := mustWW(t, repo, "acquire", "--purpose", "first", "--rev", "root()")
	runIn(t, ws1, "jj", "describe", "-m", "ws1 work")

	ws2 := mustWW(t, ws1, "acquire", "--purpose", "stacked")
	if got := runIn(t, ws2, "jj", "log", "--no-graph", "-r", "@-", "-T", "description"); got != "ws1 work" {
		t.Fatalf("acquire from ws-001 built on %q, want ws-001's change", got)
	}

	// exec runs its command in a pty, where jj would start a pager.
	out := mustWW(t, ws1, "exec", "--purpose", "probe", "--", "jj", "--no-pager", "log", "--no-graph", "-r", "@-", "-T", "description")
	if !strings.Contains(out, "ws1 work") {
		t.Fatalf("exec from ws-001 built on %q, want ws-001's change", out)
	}

	// One revision or none: a revset naming several is an error, not a merge.
	if _, stderr, err := wwIn(t, ws1, "acquire", "--purpose", "merge", "--rev", "@ | root()"); err == nil {
		t.Fatalf("acquire with a two-revision --rev from ws-001 succeeded; stderr %q", stderr)
	}

	ws3 := mustWW(t, repo, "acquire", "--purpose", "from source")
	if got := runIn(t, ws3, "jj", "log", "--no-graph", "-r", "@-", "-T", "description"); got != "source work" {
		t.Fatalf("acquire from the source repo built on %q, want the source repo's change", got)
	}
}

// The table answers how long each workspace has been in its status — held
// since its acquire, parked since its release — in one FOR column. A
// workspace's creation is not on it: a pooled workspace was created whenever
// the pool last grew, which says nothing about the one using it now.
func TestFormatWorkspaceTable(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	created := now.Add(-10 * 24 * time.Hour)
	items := []ww.Info{
		{Name: "ws-001", Status: ww.StatusAcquired, Rev: "kmqpxlzwvnrt", Purpose: "fix", Path: "/ws/ws-001",
			CreatedAt: created, AcquiredAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2*time.Hour + time.Second)},
		{Name: "ws-002", Status: ww.StatusAvailable, Path: "/ws/ws-002",
			CreatedAt: created, UpdatedAt: now.Add(-3 * 24 * time.Hour)},
		// Freshly created and held: its age and its time held are the same
		// instant, which is why the table shows only one of them.
		{Name: "ws-003", Status: ww.StatusAcquired, Rev: "tsuoyxlzwvnr", Purpose: "new", Path: "/ws/ws-003",
			CreatedAt: now.Add(-9 * time.Second), AcquiredAt: now.Add(-9 * time.Second), UpdatedAt: now.Add(-8 * time.Second)},
		{Name: "ws-004", Status: ww.StatusAvailable, Path: "/ws/ws-004"},
	}

	var got [][]string
	for line := range strings.Lines(formatWorkspaceTable(items, now)) {
		got = append(got, strings.Fields(line))
	}
	want := [][]string{
		{"NAME", "STATUS", "FOR", "REV", "PURPOSE", "PATH"},
		{"ws-001", "acquired", "2h", "kmqpxlzwvnrt", "fix", "/ws/ws-001"},
		{"ws-002", "available", "3d", "-", "-", "/ws/ws-002"},
		{"ws-003", "acquired", "9s", "tsuoyxlzwvnr", "new", "/ws/ws-003"},
		{"ws-004", "available", "-", "-", "-", "/ws/ws-004"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("table:\n%s\nwant rows %q", formatWorkspaceTable(items, now), want)
	}
}
