package ww

import (
	"testing"
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
