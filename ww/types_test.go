package ww

import (
	"testing"
	"time"
)

func TestWorkspaceTypesAliasModel(t *testing.T) {
	var status Status = StatusAvailable
	if status != StatusAvailable {
		t.Fatalf("expected workspace status alias to match model")
	}
}

func TestValidStatusesReturnsModelValues(t *testing.T) {
	statuses := ValidStatuses()
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
}

func TestWorkspaceStatusIsValid(t *testing.T) {
	if !StatusAvailable.IsValid() {
		t.Fatalf("expected status to be valid")
	}

	if Status("nope").IsValid() {
		t.Fatalf("expected status to be invalid")
	}
}

// Since is when the workspace entered its status: the acquire for a held
// workspace, the release for a parked one. Creation never stands in for
// either — a workspace created a week ago and acquired a minute ago has been
// held for a minute.
func TestInfoSince(t *testing.T) {
	created := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	acquired := created.Add(72 * time.Hour)
	released := created.Add(48 * time.Hour)

	held := Info{Status: StatusAcquired, CreatedAt: created, AcquiredAt: acquired, UpdatedAt: acquired.Add(time.Second)}
	if got := held.Since(); !got.Equal(acquired) {
		t.Errorf("held since %v, want the acquire %v", got, acquired)
	}

	parked := Info{Status: StatusAvailable, CreatedAt: created, UpdatedAt: released}
	if got := parked.Since(); !got.Equal(released) {
		t.Errorf("parked since %v, want the release %v", got, released)
	}

	// An acquired row with no acquire on record has no answer, rather than
	// the creation time, which would claim it has been held since then.
	unstamped := Info{Status: StatusAcquired, CreatedAt: created, UpdatedAt: released}
	if got := unstamped.Since(); !got.IsZero() {
		t.Errorf("unstamped held row since %v, want zero", got)
	}
}
