package ww

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"monks.co/pkg/jj"
	"monks.co/ww/ww/internal/config"
	"monks.co/ww/ww/internal/db"
	"monks.co/ww/ww/internal/paths"
)

// Pool manages a pool of jujutsu workspaces.
//
// A Pool maintains workspaces in a shared location and tracks which workspaces
// are currently acquired. Multiple processes can safely use the same Pool
// concurrently through SQLite coordination.
type Pool struct {
	store         *Store
	workspacesDir string
	jj            *jj.Client
	close         func() error
}

// Options configures a workspace pool.
type Options struct {
	// StateDir is the directory where the SQLite database is stored.
	// Defaults to ~/.local/state/ww if empty.
	StateDir string

	// WorkspacesDir is the directory where workspaces are created.
	// Defaults to ~/.local/share/ww/workspaces if empty.
	WorkspacesDir string
}

// Open creates a new Pool with default options.
// State is stored in ~/.local/state/ww and workspaces in
// ~/.local/share/ww/workspaces.
func Open() (*Pool, error) {
	return OpenWithOptions(Options{})
}

// OpenWithOptions creates a new Pool with custom options.
func OpenWithOptions(opts Options) (*Pool, error) {
	stateDir, err := paths.ResolveWithDefault(opts.StateDir, paths.DefaultStateDir)
	if err != nil {
		return nil, err
	}

	workspacesDir, err := paths.ResolveWithDefault(opts.WorkspacesDir, paths.DefaultWorkspacesDir)
	if err != nil {
		return nil, err
	}

	dbPath, err := resolveDBPath(stateDir)
	if err != nil {
		return nil, err
	}

	dbStore, err := db.Open(dbPath, db.OpenOptions{})
	if err != nil {
		return nil, err
	}

	// Attempt auto-migration from incrementum's state database,
	// but only when using default paths (not custom test dirs).
	if opts.StateDir == "" && opts.WorkspacesDir == "" {
		if err := migrateFromIncrementum(dbStore.SqlDB(), workspacesDir); err != nil {
			fmt.Fprintf(os.Stderr, "ww: warning: incrementum migration failed: %v\n", err)
		}
	}

	pool := NewPool(dbStore.SqlDB(), workspacesDir)
	pool.SetCloseFunc(dbStore.Close)
	return pool, nil
}

// NewPool constructs a pool using an existing database connection.
func NewPool(sqlDB *sql.DB, workspacesDir string) *Pool {
	return &Pool{
		store:         NewStore(sqlDB),
		workspacesDir: workspacesDir,
		jj:            jj.New(),
	}
}

// SetCloseFunc configures the close callback for a pool.
func (p *Pool) SetCloseFunc(closeFn func() error) {
	if p == nil {
		return
	}
	p.close = closeFn
}

// RepoSlug returns the repo slug used for state storage.
func (p *Pool) RepoSlug(repoPath string) (string, error) {
	repoName, err := p.store.GetOrCreateRepoName(repoPath)
	if err != nil {
		return "", fmt.Errorf("get repo name: %w", err)
	}
	return repoName, nil
}

// AcquireOptions configures a workspace acquire operation.
type AcquireOptions struct {
	// Rev is the jj revision to base a new change on. Defaults to "@" if empty.
	Rev string

	// Purpose describes why the workspace is being acquired.
	// It must be a single-line string.
	Purpose string

	// NewChangeMessage is an optional description to apply when a new change
	// is created because the requested revision is immutable.
	NewChangeMessage string

	// SkipHooks suppresses on-create hook execution and provisioning marking.
	// Use this when the workspace content will differ from the main tree
	// (e.g. the todo store edits to an orphan bookmark immediately after acquire).
	SkipHooks bool

	// Step, when set, runs each phase of the acquire: Acquire hands it the
	// phase's name and the work, and Step must call fn exactly once and
	// return its error, or an error of its own in place of it. Whatever it
	// returns is the phase's outcome, and the acquire handles it as it
	// would fn's — a failure hands the workspace back, a resolve failure
	// falls back, a stale working copy is recovered. Nil runs every phase
	// directly.
	//
	// It exists so a caller can time the phases — trace them, count them —
	// without the pool taking on a dependency of its own for it. Release
	// has no phases; it is one step as far as a caller can see.
	Step func(phase Phase, fn func() error) error
}

// ValidateAcquirePurpose ensures the purpose is present and single-line.
func ValidateAcquirePurpose(purpose string) error {
	if strings.TrimSpace(purpose) == "" {
		return fmt.Errorf("purpose is required")
	}
	if strings.ContainsAny(purpose, "\r\n") {
		return fmt.Errorf("purpose must be a single line")
	}
	return nil
}

// Acquire obtains a workspace from the pool for the given repository.
//
// If an available workspace exists, it will be reused. Otherwise, a new
// workspace is created. The workspace is checked out to a new change based on
// the specified revision (or @ by default).
//
// The returned path is the root directory of the acquired workspace.
// Call Release when done to return the workspace to the pool.
//
// If the repository contains a ww.toml or .ww/config.toml
// configuration file, the on-create hooks run on every acquire.
//
// Each step of the work runs as a Phase, through opts.Step when one is set.
func (p *Pool) Acquire(repoPath string, opts AcquireOptions) (string, error) {
	// Apply defaults
	if opts.Rev == "" {
		opts.Rev = "@"
	}
	if err := ValidateAcquirePurpose(opts.Purpose); err != nil {
		return "", err
	}
	run := step(opts.Step)

	var repoName string
	var wsPath string
	var wsName string
	var needsCreate bool
	var needsProvision bool

	// What a failure hands back depends on how far the acquire got: nothing
	// before the claim writes its row, the row alone while a new workspace
	// has nothing on disk behind it, and the workspace itself after that.
	// Each flag is set the moment it becomes true rather than when its phase
	// returns, because a Step can fail a phase whose work succeeded, and the
	// row that work claimed still has to go back.
	var claimed, onDisk bool
	handBack := func(err error) error {
		switch {
		case onDisk:
			p.returnAfterFailedAcquire(wsPath)
		case claimed:
			// Nothing exists on disk to hand back, so the row goes away
			// entirely, as it does when jj declines to add the workspace.
			if deleteErr := p.store.DeleteWorkspace(repoName, wsName); deleteErr != nil {
				return fmt.Errorf("%w; cleanup failed: %v", err, deleteErr)
			}
		}
		return err
	}

	if err := run.run(PhaseClaim, func() error {
		var err error
		repoName, err = p.store.GetOrCreateRepoName(repoPath)
		if err != nil {
			return fmt.Errorf("get repo name: %w", err)
		}

		now := time.Now()
		available, err := p.store.AcquireAvailableWorkspace(repoName, WorkspaceInfo{
			Repo:          repoName,
			Purpose:       opts.Purpose,
			Rev:           opts.Rev,
			Status:        StatusAcquired,
			AcquiredByPID: os.Getpid(),
			AcquiredAt:    now,
			UpdatedAt:     now,
		})
		if err != nil {
			return err
		}
		if available != nil {
			wsPath = paths.NormalizePath(available.Path)
			wsName = available.Name
			needsProvision = !available.Provisioned
			claimed, onDisk = true, true
			return nil
		}
		wsName, err = p.store.NextWorkspaceName(repoName)
		if err != nil {
			return err
		}
		wsPath = paths.NormalizePath(filepath.Join(p.workspacesDir, repoName, wsName))
		needsCreate = true
		needsProvision = true
		if err := p.store.InsertWorkspace(WorkspaceInfo{
			Name:          wsName,
			Repo:          repoName,
			Path:          wsPath,
			Purpose:       opts.Purpose,
			Rev:           opts.Rev,
			Status:        StatusAcquired,
			AcquiredByPID: os.Getpid(),
			AcquiredAt:    now,
			CreatedAt:     now,
			UpdatedAt:     now,
			Provisioned:   false,
		}); err != nil {
			return err
		}
		claimed = true
		return nil
	}); err != nil {
		return "", handBack(err)
	}

	if needsCreate {
		if err := run.run(PhaseWorkspaceAdd, func() error {
			if err := os.MkdirAll(filepath.Dir(wsPath), 0755); err != nil {
				return fmt.Errorf("create workspace parent dir: %w", err)
			}
			if err := p.jj.WorkspaceAdd(repoPath, wsName, wsPath); err != nil {
				return fmt.Errorf("jj workspace add: %w", err)
			}
			onDisk = true
			return nil
		}); err != nil {
			return "", handBack(err)
		}
	}

	// Resolve the revision in the source repo first. This is necessary because
	// symbolic refs like "@" have different meanings in the workspace vs source.
	//
	// The source repo's own working copy can be stale — a rewrite issued from
	// a workspace does that to it — and then this resolution fails and a
	// symbolic rev falls through to be resolved in the workspace instead,
	// which is the mis-parenting the paragraph above warns about. Recovering
	// the way a pool workspace is recovered is not open to us here. Not
	// because update-stale would lose their edits — it would not, it keeps
	// them in a divergent commit — but because it wipes the working copy on
	// the way, taking the untracked files with it. In a pool workspace that
	// is the point, since a release deletes them anyway; in the operator's
	// own checkout it is their build output, their editor state, their
	// .envrc.secrets. The fallback is knowingly accepted.
	resolve := func(rev string) string {
		var resolved string
		if err := run.run(PhaseResolve, func() error {
			var err error
			resolved, err = p.jj.ChangeIDAt(repoPath, rev)
			return err
		}); err != nil {
			return rev // Fall back to original if resolution fails
		}
		return resolved
	}

	newChange := func(parentRev string) (string, error) {
		var rev string
		err := p.withFreshWorkingCopy(wsPath, run, PhaseNewChange, func() error {
			var err error
			if strings.TrimSpace(opts.NewChangeMessage) != "" {
				rev, err = p.jj.NewChangeWithMessage(wsPath, parentRev, opts.NewChangeMessage)
			} else {
				rev, err = p.jj.NewChange(wsPath, parentRev)
			}
			return err
		})
		return rev, err
	}

	actualRev, err := newChange(resolve(opts.Rev))
	if err != nil {
		if isMissingRevisionError(err) && looksLikeChangeID(opts.Rev) {
			// Fall back to @ resolved in the source repo
			actualRev, err = newChange(resolve("@"))
		}
		if err != nil {
			return "", handBack(fmt.Errorf("jj new: %w", err))
		}
	}

	if actualRev != opts.Rev {
		if err := p.store.UpdateWorkspaceRevision(repoName, wsName, actualRev, time.Now()); err != nil {
			return "", handBack(fmt.Errorf("update workspace rev: %w", err))
		}
	}

	// Load config and run hooks (skipped for non-main-tree acquires like todo store)
	if !opts.SkipHooks {
		if err := run.run(PhaseOnCreate, func() error {
			cfg, err := config.Load(repoPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			// Run on-create script for every acquire
			if err := config.RunScript(wsPath, cfg.Workspace.OnCreate); err != nil {
				return fmt.Errorf("on-create script: %w", err)
			}
			return nil
		}); err != nil {
			// A repo-level config fault fails every acquire, so leaking
			// here empties the pool one workspace per attempt.
			return "", handBack(err)
		}

		// Mark as provisioned if needed
		if needsProvision {
			if err := p.store.MarkWorkspaceProvisioned(repoName, wsName); err != nil {
				return "", handBack(fmt.Errorf("mark workspace provisioned: %w", err))
			}
		}
	}

	return wsPath, nil
}

// withFreshWorkingCopy runs a jj operation in a workspace, recovering once
// from a working copy the repo has moved on from.
//
// A pooled workspace holds a parked change nobody is watching, and any
// operation elsewhere that rewrites it — a rebase, a revert inserted ahead of
// it, an undo of either — carries the repo's idea of the workspace away from
// what is on disk. jj then refuses every command that touches the working copy
// there until someone runs update-stale. Nothing about that is the caller's
// doing, and the recovery is mechanical, so the pool does it rather than
// handing the hint out as an error.
//
// update-stale wipes the working copy, but it does not lose what was on it:
// it snapshots the disk into a divergent commit sharing the workspace change's
// id, then moves the working copy to the rewritten one. On a parked empty
// change — the common case, since staleness lands in bulk on idle workspaces —
// the divergent commit is empty too and the caller's `jj new` abandons it,
// leaving nothing behind. Where the workspace really was holding unsnapshotted
// work, that commit is the only record of it, so nameRecoveredWork describes it
// on the way past.
//
// An acquire runs each attempt at op as phase and the recovery between them as
// PhaseUpdateStale, all through its Step, so a caller timing it sees the
// failed attempt, the recovery, and the retry as three phases rather than one
// slow one. Release passes a nil run, under which phase means nothing.
func (p *Pool) withFreshWorkingCopy(wsPath string, run step, phase Phase, op func() error) error {
	err := run.run(phase, op)
	if !jj.IsStaleWorkingCopy(err) {
		return err
	}
	if updateErr := run.run(PhaseUpdateStale, func() error {
		// Read before recovering, so nameRecoveredWork can tell the commit
		// this update-stale creates from any the change already had.
		before, beforeErr := p.jj.WorkingCopyChangeCommitIDs(wsPath)
		if err := p.jj.WorkspaceUpdateStale(wsPath); err != nil {
			return err
		}
		if beforeErr == nil {
			p.nameRecoveredWork(wsPath, before)
		}
		return nil
	}); updateErr != nil {
		// Both causes wrapped: the stale error says what went wrong, and
		// the recovery's (or the Step's own) says why it stayed wrong.
		return fmt.Errorf("%w (update-stale: %w)", err, updateErr)
	}
	return run.run(phase, op)
}

// nameRecoveredWork describes the commit update-stale just snapshotted a stale
// working copy into, when there was anything on it to snapshot. before is the
// change's commits as they stood before the recovery.
//
// Without this the work survives as an undescribed head under a change id that
// now has two commits — findable only by someone who already knows to look for
// it, and indistinguishable in `jj log` from junk worth abandoning. Naming it
// is what turns "your edits are somewhere" into a change anyone can read.
//
// Only a commit the recovery actually created is named. A change can be
// divergent for reasons of its own — two concurrent operations elsewhere
// rewriting it — and describing one of those would be worse than saying
// nothing: it would put this workspace's name on an unrelated commit, and
// DescribeAt rewrites what it describes and rebases its descendants, which is
// how a repair for one stale workspace would make another one stale.
//
// A commit that already carries a description is left alone too. That is a
// session's own change, rewritten rather than recovered; overwriting what its
// author wrote to announce the recovery would lose more than it explains.
//
// Best-effort throughout: this runs inside a recovery the caller did not ask
// for, and failing to label the commit is not a reason to fail the release
// that saved it. The empty case is the common one and does nothing at all.
func (p *Pool) nameRecoveredWork(wsPath string, before []string) {
	after, err := p.jj.WorkingCopyChangeCommitIDs(wsPath)
	if err != nil {
		return
	}
	for _, commit := range after {
		if slices.Contains(before, commit) {
			continue
		}
		if empty, err := p.jj.ChangeEmptyAt(wsPath, commit); err != nil || empty {
			continue
		}
		if desc, err := p.jj.DescriptionAt(wsPath, commit); err != nil || strings.TrimSpace(desc) != "" {
			continue
		}
		_ = p.jj.DescribeAt(wsPath, commit, fmt.Sprintf(
			"ww: work recovered from %s, which went stale before jj snapshotted it",
			filepath.Base(wsPath)))
	}
}

// returnAfterFailedAcquire hands a workspace back after an acquire that
// claimed the state row and then failed. Without it a single failure retires
// the workspace from the pool for good, holding a purpose that never ran and
// a PID that has exited, and nothing ever cleans that up.
//
// The full release does jj work of its own, which can fail for whatever
// reason the acquire did; the state row still has to be freed, so a failure
// there falls back to flipping it directly.
func (p *Pool) returnAfterFailedAcquire(wsPath string) {
	if err := p.Release(wsPath); err == nil {
		return
	}
	_ = p.store.ReleaseWorkspace(paths.NormalizePath(wsPath), time.Now())
}

// Release returns a workspace to the pool, making it available for reuse.
//
// After releasing, the workspace path should no longer be used. The workspace
// directory remains on disk and may be acquired again later.
func (p *Pool) Release(wsPath string) error {
	return p.releaseToAvailable(wsPath)
}

// releaseToAvailable cleans the workspace, parks it on a fresh change at
// trunk(), and frees its state row.
//
// It recovers a stale working copy the same way an acquire does, because
// update-stale does not lose the work it looks like it loses: it snapshots
// whatever the disk holds into a divergent commit under the workspace change's
// own id before moving the working copy, so a release preserves a session's
// last writes rather than discarding them. What it does drop — untracked,
// ignored files — a release deletes anyway (see cleanUntracked), so a stale
// release costs nothing a plain one does not.
//
// Recovery is therefore not a judgment call, and the pool does not put one to
// anyone. It used to: release refused a stale workspace and left the operator
// to clear it by hand, on the belief that update-stale destroyed unsnapshotted
// edits. It does not, and the refusal bought nothing — while costing a
// workspace stuck out of the pool, and a fault reported to whoever pressed the
// button.
func (p *Pool) releaseToAvailable(wsPath string) error {
	err := p.withFreshWorkingCopy(wsPath, nil, "", func() error {
		if err := cleanUntracked(p.jj, wsPath); err != nil {
			return fmt.Errorf("clean untracked: %w", err)
		}
		if _, err := p.jj.NewChange(wsPath, "trunk()"); err != nil {
			return fmt.Errorf("jj new trunk(): %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	wsPath = paths.NormalizePath(wsPath)
	if err := p.store.ReleaseWorkspace(wsPath, time.Now()); err != nil {
		return fmt.Errorf("release workspace: %w", err)
	}
	return nil
}

// ReleaseByName returns a workspace to the pool by name.
func (p *Pool) ReleaseByName(repoPath, wsName string) error {
	repoName, err := p.store.GetOrCreateRepoName(repoPath)
	if err != nil {
		return fmt.Errorf("get repo name: %w", err)
	}

	ws, err := p.store.GetWorkspaceByName(repoName, wsName)
	if err != nil {
		return fmt.Errorf("get workspace: %w", err)
	}

	return p.releaseToAvailable(ws.Path)
}

// Info contains information about a workspace.
type Info struct {
	// Name is the workspace identifier (e.g., "ws-001").
	Name string

	// Path is the absolute path to the workspace directory.
	Path string

	// Purpose describes why the workspace was acquired.
	Purpose string

	// Rev is the jj revision the workspace was opened to.
	Rev string

	// Status indicates whether the workspace is available or acquired.
	Status Status

	// AcquiredByPID is the process ID that acquired this workspace.
	// Zero if not acquired.
	AcquiredByPID int

	// AcquiredAt is when the workspace was acquired.
	// Zero if not acquired.
	AcquiredAt time.Time

	// CreatedAt is when the workspace was created. Acquiring it again does
	// not change it.
	CreatedAt time.Time

	// UpdatedAt is when the row was last written, which for an available
	// workspace is its release: nothing else writes a parked row.
	UpdatedAt time.Time
}

// Since is when the workspace entered its status: the acquire for an acquired
// workspace, the release for an available one. Zero when the pool recorded
// neither. It is the instant a listing measures how long a workspace has been
// held, or parked, from.
func (i Info) Since() time.Time {
	if i.Status == StatusAcquired {
		return i.AcquiredAt
	}
	return i.UpdatedAt
}

// List returns information about all workspaces for the given repository.
//
// The returned slice includes both available and acquired workspaces.
func (p *Pool) List(repoPath string) ([]Info, error) {
	repoName, err := p.store.GetOrCreateRepoName(repoPath)
	if err != nil {
		return nil, fmt.Errorf("get repo name: %w", err)
	}

	workspaces, err := p.store.ListWorkspaces(repoName)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	var items []Info

	for _, ws := range workspaces {
		item := Info{
			Name:          ws.Name,
			Path:          ws.Path,
			Purpose:       ws.Purpose,
			Rev:           ws.Rev,
			Status:        ws.Status,
			AcquiredByPID: ws.AcquiredByPID,
			AcquiredAt:    ws.AcquiredAt,
			CreatedAt:     ws.CreatedAt,
			UpdatedAt:     ws.UpdatedAt,
		}

		items = append(items, item)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Status != items[j].Status {
			return workspaceStatusRank(items[i].Status) < workspaceStatusRank(items[j].Status)
		}
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].Path < items[j].Path
	})

	return items, nil
}

func workspaceStatusRank(status Status) int {
	switch status {
	case StatusAcquired:
		return 0
	case StatusAvailable:
		return 1
	default:
		return 2
	}
}

func isMissingRevisionError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "doesn't exist") || strings.Contains(lower, "does not exist")
}

func looksLikeChangeID(rev string) bool {
	if len(rev) < 12 {
		return false
	}
	for _, r := range rev {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return true
}

// workspaceRoot returns the root of the jj workspace containing path,
// normalized to handle macOS symlinks like /private/var.
//
// Inside a pool workspace that is the workspace itself, not the repo it was
// made from; Locate is the answer for anything keyed on the repo.
func workspaceRoot(path string) (string, error) {
	root, err := jj.New().WorkspaceRoot(path)
	if err != nil {
		return "", err
	}
	return paths.NormalizePath(root), nil
}

func resolveDBPath(stateDir string) (string, error) {
	if stateDir != "" {
		return filepath.Join(stateDir, "state.db"), nil
	}
	return paths.DefaultDBPath()
}

// RepoRootFromPath returns the source repo for a path, as Locate does, opening
// the default pool to answer it.
func RepoRootFromPath(path string) (string, error) {
	return RepoRootFromPathWithOptions(path, Options{})
}

// RepoRootFromPathWithOptions is like RepoRootFromPath with custom options.
func RepoRootFromPathWithOptions(path string, opts Options) (string, error) {
	// Outside any jj repo there is nothing to ask the pool, so that is
	// settled before opening, and perhaps creating, its state.
	root, err := workspaceRoot(path)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrWorkspaceRootNotFound, err)
	}

	pool, err := OpenWithOptions(opts)
	if err != nil {
		return "", fmt.Errorf("open workspace pool: %w", err)
	}
	defer pool.Close()

	loc, err := pool.locateRoot(root)
	if err != nil {
		return "", err
	}
	return loc.Repo, nil
}

// Location is where a path sits relative to the pool.
type Location struct {
	// Repo is the source repo the path belongs to: the repo every
	// repo-keyed call (Acquire, List, ReleaseByName, DestroyAll) takes.
	// Inside a pool workspace it is the repo the workspace was created from.
	Repo string

	// Workspace names the pool workspace containing the path, or is empty
	// when the path is not inside one.
	Workspace string
}

// Locate resolves a path against the pool: the source repo it belongs to, and
// the pool workspace it is in, if any.
//
// jj alone cannot answer this. Inside a pool workspace `jj workspace root`
// names the workspace, and a workspace passed where a source repo belongs is
// refused with ErrNotSourceRepo. So a path's workspace root is looked up in
// the pool's state first, and only taken for the repo when it is not a pool
// workspace.
//
// Returns ErrWorkspaceRootNotFound when path is not in a jj workspace at all,
// and ErrRepoPathNotFound when its workspace root sits where the pool puts its
// workspaces but nothing is on record there — a workspace whose state was
// lost, which taking for a source repo would register as one.
func (p *Pool) Locate(path string) (Location, error) {
	root, err := workspaceRoot(path)
	if err != nil {
		return Location{}, fmt.Errorf("%w: %v", ErrWorkspaceRootNotFound, err)
	}
	return p.locateRoot(root)
}

// locateRoot is Locate for a path already resolved to its jj workspace root.
func (p *Pool) locateRoot(root string) (Location, error) {
	name, sourcePath, found, err := p.store.WorkspaceAt(root)
	if err != nil {
		return Location{}, err
	}
	if found {
		return Location{Repo: paths.NormalizePath(sourcePath), Workspace: name}, nil
	}

	if p.isWorkspaceSlot(root) {
		return Location{}, fmt.Errorf("%w: no workspace is on record at %s", ErrRepoPathNotFound, root)
	}
	return Location{Repo: root}, nil
}

// isWorkspaceSlot reports whether dir is where the pool would put a workspace:
// <workspaces dir>/<repo>/<name>. A repo nested deeper, inside a workspace's
// checkout, is not in a workspace's place and is a repo of its own.
//
// Both sides are compared with symlinks resolved, since dir comes from jj,
// which resolves them, and the workspaces directory may be reached through one.
func (p *Pool) isWorkspaceSlot(dir string) bool {
	workspacesDir := p.workspacesDir
	if resolved, err := filepath.EvalSymlinks(workspacesDir); err == nil {
		workspacesDir = resolved
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	rel, err := filepath.Rel(workspacesDir, dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return len(strings.Split(rel, string(filepath.Separator))) == 2
}

// DestroyAll removes all workspaces for the given repository.
//
// This deletes both the state entries and the workspace directories on disk.
// It also runs "jj workspace forget" to unregister each workspace from the
// source repository.
func (p *Pool) DestroyAll(repoPath string) error {
	repoName, err := p.store.GetOrCreateRepoName(repoPath)
	if err != nil {
		return fmt.Errorf("get repo name: %w", err)
	}

	workspaces, repoSourcePath, err := p.store.DeleteWorkspaces(repoName)
	if err != nil {
		return err
	}

	// Forget workspaces from jj and delete directories
	var errs []error
	for _, ws := range workspaces {
		// Try to forget from jj (may fail if source repo is gone)
		if repoSourcePath != "" {
			if err := p.jj.WorkspaceForget(repoSourcePath, ws.Name); err != nil {
				// Non-fatal - the workspace might already be forgotten or the repo gone
				errs = append(errs, fmt.Errorf("forget workspace %s: %w", ws.Name, err))
			}
		}

		// Delete the workspace directory
		if err := os.RemoveAll(ws.Path); err != nil {
			errs = append(errs, fmt.Errorf("remove workspace %s: %w", ws.Path, err))
		}
	}

	// Also try to remove the repo's workspace directory if empty
	repoWorkspacesDir := filepath.Join(p.workspacesDir, repoName)
	os.Remove(repoWorkspacesDir) // Ignore error - may not be empty or exist

	if len(errs) > 0 {
		// Return first error but log intent that some cleanup failed
		return errs[0]
	}

	return nil
}

// migrateFromIncrementum migrates workspace state from the incrementum database
// if it exists and the ww database is fresh (has 0 repos).
func migrateFromIncrementum(wwDB *sql.DB, wwWorkspacesDir string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil // silently skip
	}
	incrDBPath := filepath.Join(home, ".local", "state", "incrementum", "state.db")
	if _, err := os.Stat(incrDBPath); os.IsNotExist(err) {
		return nil
	}

	// Check if ww DB already has data
	var count int
	if err := wwDB.QueryRow("SELECT COUNT(*) FROM repos").Scan(&count); err != nil {
		return nil
	}
	if count > 0 {
		return nil // already has data, skip migration
	}

	incrDB, err := sql.Open("sqlite", incrDBPath)
	if err != nil {
		return fmt.Errorf("open incrementum db: %w", err)
	}
	defer incrDB.Close()

	// Copy repos
	rows, err := incrDB.Query("SELECT name, source_path FROM repos")
	if err != nil {
		return fmt.Errorf("read incrementum repos: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, sourcePath string
		if err := rows.Scan(&name, &sourcePath); err != nil {
			return fmt.Errorf("scan repo: %w", err)
		}
		if _, err := wwDB.Exec("INSERT OR IGNORE INTO repos (name, source_path) VALUES (?, ?)", name, sourcePath); err != nil {
			return fmt.Errorf("insert repo: %w", err)
		}
	}

	// Copy workspaces, updating paths
	incrWorkspacesDir := filepath.Join(home, ".local", "share", "incrementum", "workspaces")
	wsRows, err := incrDB.Query("SELECT repo, name, path, purpose, rev, status, acquired_by_pid, provisioned, created_at, updated_at, acquired_at FROM workspaces")
	if err != nil {
		return fmt.Errorf("read incrementum workspaces: %w", err)
	}
	defer wsRows.Close()
	for wsRows.Next() {
		var repo, name, path, purpose, rev, status, createdAt, updatedAt, acquiredAt string
		var acquiredByPID sql.NullInt64
		var provisioned int
		if err := wsRows.Scan(&repo, &name, &path, &purpose, &rev, &status, &acquiredByPID, &provisioned, &createdAt, &updatedAt, &acquiredAt); err != nil {
			return fmt.Errorf("scan workspace: %w", err)
		}
		// Update path from incrementum dir to ww dir
		if strings.HasPrefix(path, incrWorkspacesDir) {
			path = wwWorkspacesDir + path[len(incrWorkspacesDir):]
		}
		if _, err := wwDB.Exec("INSERT OR IGNORE INTO workspaces (repo, name, path, purpose, rev, status, acquired_by_pid, provisioned, created_at, updated_at, acquired_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			repo, name, path, purpose, rev, status, acquiredByPID, provisioned, createdAt, updatedAt, acquiredAt); err != nil {
			return fmt.Errorf("insert workspace: %w", err)
		}
	}

	// Move workspace directories
	if _, err := os.Stat(incrWorkspacesDir); err == nil {
		if err := os.MkdirAll(filepath.Dir(wwWorkspacesDir), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "ww: warning: could not create workspace dir: %v\n", err)
		} else if err := os.Rename(incrWorkspacesDir, wwWorkspacesDir); err != nil {
			fmt.Fprintf(os.Stderr, "ww: warning: could not move workspace directories: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "ww: migrated workspaces from %s to %s\n", incrWorkspacesDir, wwWorkspacesDir)
		}
	}

	fmt.Fprintf(os.Stderr, "ww: migrated workspace state from incrementum\n")
	return nil
}
