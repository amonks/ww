package ww

import (
	"errors"
	"testing"

	"monks.co/ww/ww/internal/db"
)

func TestWorkspaceErrorsAliasModel(t *testing.T) {
	if !errors.Is(ErrRepoPathNotFound, db.ErrRepoPathNotFound) {
		t.Fatalf("expected ErrRepoPathNotFound to wrap the state error")
	}
}
