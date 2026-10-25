package main

import (
	"errors"
	"testing"
)

func TestPredecessorImportFailureUsesSnapshotRecovery(t *testing.T) {
	cause := errors.New("unreadable current rows")
	failure := wrapOperatorFailure("cannot inspect the imported work", cause)
	if failure.RecoveryAction != "restore the current rows from a verified snapshot" {
		t.Fatalf("recovery = %q; want verified snapshot restoration", failure.RecoveryAction)
	}
	if !errors.Is(failure, cause) || failure.RetrySafe {
		t.Fatalf("failure lost its cause or became retry-safe: %+v", failure)
	}
}
