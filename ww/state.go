package ww

import (
	"slices"
	"time"
)

// Status represents the state of a workspace.
type Status string

const (
	// StatusAvailable indicates the workspace is free to be acquired.
	StatusAvailable Status = "available"
	// StatusAcquired indicates the workspace is currently in use.
	StatusAcquired Status = "acquired"
)

// ValidStatuses returns all valid workspace status values.
func ValidStatuses() []Status {
	return []Status{StatusAvailable, StatusAcquired}
}

// IsValid returns true if the status is a known value.
func (s Status) IsValid() bool {
	return slices.Contains(ValidStatuses(), s)
}

// WorkspaceInfo stores information about a workspace.
type WorkspaceInfo struct {
	Name          string
	Repo          string
	Path          string
	Purpose       string
	Rev           string
	Status        Status
	AcquiredByPID int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	AcquiredAt    time.Time
	Provisioned   bool
}
