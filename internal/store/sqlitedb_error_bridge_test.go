package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestSQLiteMechanismErrorBridgePreservesJoinedFailures(t *testing.T) {
	driverCause := errors.New("injected driver cause")
	rollbackCause := errors.New("injected rollback cause")
	read := &sqlitedb.Error{Kind: sqlitedb.Unavailable, Op: "store_revision", Detail: "cannot advance the store revision", RetrySafe: true, RecoveryAction: "confirm the migrated store is writable", Err: driverCause}
	rollback := &sqlitedb.Error{Kind: sqlitedb.Unavailable, Op: "transaction_rollback", Detail: "cannot prove the transaction rolled back", RecoveryAction: "reconcile the requested effect before retrying", EffectPossible: true, Err: rollbackCause}
	commit := &sqlitedb.Error{Kind: sqlitedb.Unavailable, Op: "durable_transaction", Detail: "cannot finish the durable commit", RetrySafe: true, RecoveryAction: "reconcile the requested effect before retrying", EffectPossible: true, Err: errors.Join(read, rollback)}
	for _, input := range []error{read, rollback, errors.Join(read, rollback), commit} {
		t.Run(input.Error(), func(t *testing.T) {
			got := mechanismFailure(input)
			if got.Error() != input.Error() {
				t.Fatalf("error text changed: got %q want %q", got, input)
			}
			var failure *Failure
			if !errors.As(got, &failure) {
				t.Fatalf("mechanism error escaped the store boundary: %T", got)
			}
			var check func(error, error)
			check = func(before, after error) {
				switch e := before.(type) {
				case *sqlitedb.Error:
					f, ok := after.(*Failure)
					if !ok || f.Kind != FailureKind(e.Kind) || f.Op != e.Op || f.Detail != e.Detail || f.RetrySafe != e.RetrySafe || f.RecoveryAction != e.RecoveryAction || f.EffectPossible != e.EffectPossible {
						t.Fatalf("lost mechanism fields: before=%+v after=%+v", e, after)
					}
					check(e.Err, f.Err)
				case interface{ Unwrap() []error }:
					children := after.(interface{ Unwrap() []error }).Unwrap()
					for i, cause := range e.Unwrap() {
						check(cause, children[i])
					}
				default:
					if before != after {
						t.Fatalf("driver cause changed: %v -> %v", before, after)
					}
				}
			}
			check(input, got)
			for _, cause := range []error{driverCause, rollbackCause} {
				if errors.Is(input, cause) != errors.Is(got, cause) {
					t.Fatalf("lost driver cause %v", cause)
				}
			}
			outer := wrapFailure(KindUnavailable, "transaction", "cannot begin transaction", true, "retry once the database is writable", got)
			if errors.Is(input, rollbackCause) && !outer.EffectPossible {
				t.Fatal("joined rollback lost possible-effect metadata")
			}
		})
	}
	if mechanismFailure(nil) != nil {
		t.Fatal("nil acquired an error")
	}
	if mechanismFailure(driverCause) != driverCause {
		t.Fatal("unclassified driver error changed")
	}
	for _, unchanged := range []error{errors.Join(driverCause, rollbackCause), fmt.Errorf("driver failures: %w and %w", driverCause, rollbackCause)} {
		if got := mechanismFailure(unchanged); got != unchanged {
			t.Fatalf("unclassified multi-cause error changed: %T %q -> %T %q", unchanged, unchanged, got, got)
		}
	}
}
