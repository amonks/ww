package ww

import (
	"errors"

	"monks.co/ww/ww/internal/db"
)

var (
	// ErrWorkspaceRootNotFound indicates a path is not in a jj workspace.
	ErrWorkspaceRootNotFound = errors.New("workspace root not found")
	// ErrRepoPathNotFound indicates a workspace is tracked but missing repo info.
	ErrRepoPathNotFound = db.ErrRepoPathNotFound
	// ErrNotSourceRepo indicates a path given as a source repo is one of the
	// pool's own workspaces; Locate resolves a workspace to its source repo.
	ErrNotSourceRepo = db.ErrNotSourceRepo
	// ErrWorkspaceNotAcquired indicates a path names no workspace the pool
	// has out: a parked one, or none at all.
	ErrWorkspaceNotAcquired = errors.New("workspace is not acquired")
	// ErrPurposeChanged indicates an acquired workspace is held for some
	// purpose other than the one its caller expected.
	ErrPurposeChanged = errors.New("workspace is held for a different purpose")
)
