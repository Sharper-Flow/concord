package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestReadSnapshotLifecycle(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	wantErr := errors.New("read callback failed")
	for _, callbackErr := range []error{nil, wantErr} {
		var retained queryer
		err := s.readSnapshot(ctx, func(q queryer) error {
			retained = q
			var count int
			if err := q.QueryRowContext(ctx, "SELECT count(*) FROM effect_receipts").Scan(&count); err != nil {
				return err
			}
			return callbackErr
		})
		if !errors.Is(err, callbackErr) {
			t.Fatalf("callback error=%v want %v", err, callbackErr)
		}
		var count int
		if err := retained.QueryRowContext(ctx, "SELECT 1").Scan(&count); !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("closed snapshot query error=%v", err)
		}
	}
	t.Run("panic", func(t *testing.T) {
		var retained queryer
		func() {
			defer func() {
				if got := recover(); got != "read panic" {
					t.Fatalf("panic=%v", got)
				}
			}()
			_ = s.readSnapshot(ctx, func(q queryer) error {
				retained = q
				panic("read panic")
			})
		}()
		var value int
		if err := retained.QueryRowContext(ctx, "SELECT 1").Scan(&value); !errors.Is(err, sql.ErrTxDone) {
			t.Fatalf("panic retained an open snapshot: %v", err)
		}
		if err := s.db.PingContext(ctx); err != nil {
			t.Fatal(err)
		}
	})
	if err := s.readSnapshot(ctx, nil); failureKind(err) != KindInvalidOperation {
		t.Fatalf("nil callback=%v", err)
	}
	var absent *Store
	if err := absent.readSnapshot(ctx, func(queryer) error { return nil }); failureKind(err) != KindUnavailable {
		t.Fatalf("absent store=%v", err)
	}
}
