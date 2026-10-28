package ww_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"monks.co/pkg/jj"
	"monks.co/ww/ww"
)

// phaseRecorder is an AcquireOptions.Step that records each phase an acquire
// runs and the error fn ended it with. A phase that begins while another is
// still running fails the test: the phases are flat, so a caller timing them
// never has to untangle one from inside another.
type phaseRecorder struct {
	t       *testing.T
	phases  []ww.Phase
	errs    []error
	running bool
	// fail, when set, is returned in place of fn's outcome for that phase,
	// after fn has run.
	fail map[ww.Phase]error
}

func (r *phaseRecorder) step(phase ww.Phase, fn func() error) error {
	if r.running {
		r.t.Errorf("phase %s began inside another phase", phase)
	}
	r.running = true
	err := fn()
	r.running = false
	r.phases = append(r.phases, phase)
	r.errs = append(r.errs, err)
	if injected, ok := r.fail[phase]; ok {
		return injected
	}
	return err
}

func (r *phaseRecorder) options() ww.AcquireOptions {
	opts := acquireOptions()
	opts.Step = r.step
	return opts
}

func (r *phaseRecorder) requirePhases(want ...ww.Phase) {
	r.t.Helper()
	if !slices.Equal(r.phases, want) {
		r.t.Fatalf("phases = %v, want %v", r.phases, want)
	}
}

// An acquire that grows the pool runs every phase once, workspace_add among
// them, and none of them fails.
func TestPool_Acquire_StepsThroughACreate(t *testing.T) {
	repoPath, pool := setupPoolWithMain(t)
	rec := &phaseRecorder{t: t}

	wsPath, err := pool.Acquire(repoPath, rec.options())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(func() { _ = pool.Release(wsPath) })

	rec.requirePhases(ww.PhaseClaim, ww.PhaseWorkspaceAdd, ww.PhaseResolve, ww.PhaseNewChange, ww.PhaseOnCreate)
	for i, err := range rec.errs {
		if err != nil {
			t.Errorf("phase %s failed: %v", rec.phases[i], err)
		}
	}
}

// Reusing a parked workspace has nothing to add on disk, and SkipHooks drops
// the on-create phase along with the hook.
func TestPool_Acquire_StepsThroughAReuse(t *testing.T) {
	repoPath, pool := setupPoolWithMain(t)
	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("release: %v", err)
	}

	rec := &phaseRecorder{t: t}
	reacquired, err := pool.Acquire(repoPath, rec.options())
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if reacquired != wsPath {
		t.Fatalf("expected the pooled workspace %s, got %s", wsPath, reacquired)
	}
	rec.requirePhases(ww.PhaseClaim, ww.PhaseResolve, ww.PhaseNewChange, ww.PhaseOnCreate)
	if err := pool.Release(reacquired); err != nil {
		t.Fatalf("release: %v", err)
	}

	skipping := &phaseRecorder{t: t}
	opts := skipping.options()
	opts.SkipHooks = true
	again, err := pool.Acquire(repoPath, opts)
	if err != nil {
		t.Fatalf("acquire with SkipHooks: %v", err)
	}
	t.Cleanup(func() { _ = pool.Release(again) })
	skipping.requirePhases(ww.PhaseClaim, ww.PhaseResolve, ww.PhaseNewChange)
}

// A stale workspace's recovery is a phase of its own between the new change
// that failed and the one that succeeded, so a caller timing the acquire can
// see what the staleness cost.
func TestPool_Acquire_StepsThroughStaleRecovery(t *testing.T) {
	repoPath, pool := setupPoolWithMain(t)
	wsPath, err := pool.Acquire(repoPath, acquireOptions())
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	wsName := workspaceName(t, pool, wsPath)
	if err := pool.Release(wsPath); err != nil {
		t.Fatalf("release: %v", err)
	}
	runJJ(t, repoPath, "rebase", "-r", wsName+"@", "-d", "main")
	requireStale(t, wsPath)

	rec := &phaseRecorder{t: t}
	reacquired, err := pool.Acquire(repoPath, rec.options())
	if err != nil {
		t.Fatalf("acquire after the parked change was rewritten: %v", err)
	}
	t.Cleanup(func() { _ = pool.Release(reacquired) })

	rec.requirePhases(ww.PhaseClaim, ww.PhaseResolve, ww.PhaseNewChange, ww.PhaseUpdateStale, ww.PhaseNewChange, ww.PhaseOnCreate)
	if !jj.IsStaleWorkingCopy(rec.errs[2]) {
		t.Errorf("first new change ended with %v, want the stale working copy it recovered from", rec.errs[2])
	}
	for _, i := range []int{3, 4} {
		if rec.errs[i] != nil {
			t.Errorf("phase %s failed: %v", rec.phases[i], rec.errs[i])
		}
	}
}

// Step's return is the phase's outcome: an error it substitutes fails the
// acquire exactly as the phase's own would, workspace handed back included,
// and a phase's own failure reaches Step before it reaches the caller.
func TestPool_Acquire_StepErrorIsThePhaseError(t *testing.T) {
	t.Run("substituted", func(t *testing.T) {
		repoPath, pool := setupPoolWithMain(t)
		injected := errors.New("injected by the step")
		rec := &phaseRecorder{t: t, fail: map[ww.Phase]error{ww.PhaseNewChange: injected}}

		_, err := pool.Acquire(repoPath, rec.options())
		if !errors.Is(err, injected) {
			t.Fatalf("acquire = %v, want the step's error", err)
		}
		rec.requirePhases(ww.PhaseClaim, ww.PhaseWorkspaceAdd, ww.PhaseResolve, ww.PhaseNewChange)

		list, err := pool.List(repoPath)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) != 1 || list[0].Status != ww.StatusAvailable {
			t.Fatalf("failed acquire left %+v, want its one workspace handed back", list)
		}
	})

	// A Step can fail a phase whose work went through, and the acquire
	// takes the failure as the phase's own. What the work claimed goes back
	// all the same — the row deleted when there is nothing on disk behind
	// it, the workspace released when there is — since leaving either
	// acquired retires a workspace for good; a failed recovery fails the
	// acquire like any failed phase; and a failed resolve is the one the
	// acquire goes on past, with the revision as given.
	for _, tc := range []struct {
		name  string
		phase ww.Phase
		start string // the pool beforehand: empty, "idle" (one parked), or "stale" (one parked and stale)
		fails bool
		left  int // workspaces in the pool afterward
	}{
		{"claim of a new row", ww.PhaseClaim, "", true, 0},
		{"claim of a parked workspace", ww.PhaseClaim, "idle", true, 1},
		{"workspace add", ww.PhaseWorkspaceAdd, "", true, 1},
		{"stale recovery", ww.PhaseUpdateStale, "stale", true, 1},
		{"resolve", ww.PhaseResolve, "", false, 1},
	} {
		t.Run("after a successful "+tc.name, func(t *testing.T) {
			repoPath, pool := setupPoolWithMain(t)
			if tc.start != "" {
				wsPath, err := pool.Acquire(repoPath, acquireOptions())
				if err != nil {
					t.Fatalf("first acquire: %v", err)
				}
				wsName := workspaceName(t, pool, wsPath)
				if err := pool.Release(wsPath); err != nil {
					t.Fatalf("release: %v", err)
				}
				if tc.start == "stale" {
					runJJ(t, repoPath, "rebase", "-r", wsName+"@", "-d", "main")
					requireStale(t, wsPath)
				}
			}
			injected := errors.New("injected by the step")
			rec := &phaseRecorder{t: t, fail: map[ww.Phase]error{tc.phase: injected}}
			wsPath, err := pool.Acquire(repoPath, rec.options())
			if i := slices.Index(rec.phases, tc.phase); i < 0 {
				t.Fatalf("phases = %v, never reached %s", rec.phases, tc.phase)
			} else if rec.errs[i] != nil {
				t.Fatalf("%s's own work failed (%v); the case needs it to succeed", tc.phase, rec.errs[i])
			}

			want := ww.StatusAvailable
			if tc.fails {
				if !errors.Is(err, injected) {
					t.Fatalf("acquire = %v, want the step's error", err)
				}
			} else {
				if err != nil {
					t.Fatalf("acquire = %v, want it to go on past the step's error", err)
				}
				t.Cleanup(func() { _ = pool.Release(wsPath) })
				rec.requirePhases(ww.PhaseClaim, ww.PhaseWorkspaceAdd, ww.PhaseResolve, ww.PhaseNewChange, ww.PhaseOnCreate)
				want = ww.StatusAcquired
			}

			list, err := pool.List(repoPath)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(list) != tc.left {
				t.Fatalf("pool holds %+v, want %d workspace(s), all %s", list, tc.left, want)
			}
			for _, ws := range list {
				if ws.Status != want {
					t.Errorf("workspace %s is %s, want %s", ws.Name, ws.Status, want)
				}
			}
		})
	}

	t.Run("the phase's own", func(t *testing.T) {
		repoPath, pool := setupPoolWithMain(t)
		writeOnCreateHook(t, repoPath, "exit 1")
		rec := &phaseRecorder{t: t}

		_, err := pool.Acquire(repoPath, rec.options())
		if err == nil || !strings.Contains(err.Error(), "on-create script") {
			t.Fatalf("acquire = %v, want the on-create script's failure", err)
		}
		rec.requirePhases(ww.PhaseClaim, ww.PhaseWorkspaceAdd, ww.PhaseResolve, ww.PhaseNewChange, ww.PhaseOnCreate)
		if last := rec.errs[len(rec.errs)-1]; last == nil || !strings.Contains(last.Error(), "on-create script") {
			t.Errorf("on-create phase ended with %v, want the script's failure", last)
		}
	})
}
