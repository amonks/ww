package ww

// Phase names one step of an Acquire, as AcquireOptions.Step sees it. The
// names are stable: a caller can build event or metric names from them.
//
// The phases are flat — none runs inside another — and an acquire runs them
// in the order listed, skipping those that do not apply. A stale working copy
// adds PhaseUpdateStale after the PhaseNewChange it failed and a second
// PhaseNewChange after that; a change id missing from the workspace adds a
// second PhaseResolve and PhaseNewChange for the fallback to the source
// repo's @ (see the spec's Revision Resolution). The state database writes
// between phases, recording the revision and the provisioning mark, run
// outside any.
type Phase string

const (
	// PhaseClaim claims a state row: an available workspace, or a new name
	// and row when none is free. SQLite only.
	PhaseClaim Phase = "claim"

	// PhaseWorkspaceAdd creates the workspace on disk with `jj workspace
	// add`. It runs only when the claim found nothing available to reuse.
	PhaseWorkspaceAdd Phase = "workspace_add"

	// PhaseResolve resolves the requested revision in the source repo.
	// That is a `jj log` there, which snapshots the source repo's working
	// copy first. A failure is not fatal: the acquire goes on with the
	// revision as given.
	PhaseResolve Phase = "resolve"

	// PhaseNewChange runs `jj new` in the workspace, and `jj describe` when
	// the acquire carries a NewChangeMessage.
	PhaseNewChange Phase = "new_change"

	// PhaseUpdateStale recovers a stale working copy with `jj workspace
	// update-stale`, naming any work it preserved. It runs only after a
	// PhaseNewChange that failed because the working copy was stale.
	PhaseUpdateStale Phase = "update_stale"

	// PhaseOnCreate loads the configuration and runs its on-create script.
	// It runs on every acquire but one with SkipHooks set.
	PhaseOnCreate Phase = "on_create"
)

// step runs one phase of an acquire through AcquireOptions.Step, or directly
// when there is none.
type step func(phase Phase, fn func() error) error

func (s step) run(phase Phase, fn func() error) error {
	if s == nil {
		return fn()
	}
	return s(phase, fn)
}
