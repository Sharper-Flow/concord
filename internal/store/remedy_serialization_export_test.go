package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// BootstrapOriginRefusalFixture drives the real bootstrap origin gate against
// a path no active Concord worktree occupies and returns its genuine refusal.
func BootstrapOriginRefusalFixture(t *testing.T) *Failure {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "bootstrap-remedy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.ValidateBootstrapOrigin(context.Background(), "concord", filepath.Join(t.TempDir(), "not-an-active-worktree"), nil)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("origin gate did not refuse: %v", err)
	}
	return failure
}
