package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

// cursorKeyCount reports how many installation cursor keys the database holds.
func cursorKeyCount(t *testing.T, q queryer) int64 {
	t.Helper()
	var count int64
	if err := q.QueryRowContext(context.Background(), `SELECT count(*) FROM agent_installation_keys WHERE key_name='cursor'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestInstallationKeyFailureFieldsAndCauses(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing row", "invalid length", "canceled inspection", "missing revision", "insert refusal", "refused insert with uncertain cleanup", "ignored insert", "ignored insert rollback failure"} {
		t.Run(mode, func(t *testing.T) {
			s := openTemp(t)
			ctx := t.Context()
			var err error
			want := Failure{Kind: KindUnavailable, Op: "cursor_key", Detail: "cannot read the installation cursor key", RetrySafe: true, RecoveryAction: "open a migrated authority database"}
			effectPossible := false
			// fixtureCommits marks cases whose fault connector commits the real
			// transaction while reporting the injected rollback outcome, so the
			// persisted revision advances and no key appears.
			fixtureCommits := false
			switch mode {
			case "missing row":
				if _, err := s.db.Exec(`DELETE FROM agent_installation_keys`); err != nil {
					t.Fatal(err)
				}
				_, err = InstallationKey(ctx, s.db)
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("missing row cause: %v", err)
				}
			case "invalid length":
				if _, err := s.db.Exec(`PRAGMA ignore_check_constraints=ON; UPDATE agent_installation_keys SET key_bytes=zeroblob(31)`); err != nil {
					t.Fatal(err)
				}
				_, err = InstallationKey(ctx, s.db)
				want = Failure{Kind: KindInvariantViolation, Op: "cursor_key", Detail: "installation cursor key has an invalid length", RecoveryAction: "rebuild the authority from its migration"}
			case "canceled inspection":
				before := revisionValue(t, s.db)
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				err = ensureInstallationKey(canceled, s.db)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation cause: %v", err)
				}
				if after := revisionValue(t, s.db); after != before {
					t.Fatalf("cancellation persisted a revision: %d -> %d", before, after)
				}
				want = Failure{Kind: KindUnavailable, Op: "open", Detail: "cannot inspect the installation cursor key", RetrySafe: true, RecoveryAction: "confirm the database is readable"}
			case "missing revision":
				if _, err := s.db.Exec(`DELETE FROM agent_installation_keys; DELETE FROM store_revision`); err != nil {
					t.Fatal(err)
				}
				err = ensureInstallationKey(ctx, s.db)
				var outer *Failure
				if !errors.As(err, &outer) {
					t.Fatalf("creation envelope: %v", err)
				}
				var cause *Failure
				if !errors.As(outer.Unwrap(), &cause) || cause.Kind != KindInvariantViolation || cause.Op != "store_revision" {
					t.Fatalf("nested revision envelope: %v", err)
				}
				want = Failure{Kind: KindUnavailable, Op: "open", Detail: "cannot begin installation cursor key creation", RetrySafe: true, RecoveryAction: "retry once the database is writable"}
			case "insert refusal", "refused insert with uncertain cleanup":
				if _, err := s.db.Exec(`DELETE FROM agent_installation_keys; CREATE TRIGGER refuse_key BEFORE INSERT ON agent_installation_keys BEGIN SELECT RAISE(ABORT,'fixture refusal'); END`); err != nil {
					t.Fatal(err)
				}
				before := revisionValue(t, s.db)
				if mode == "refused insert with uncertain cleanup" {
					connector := &restoreFaultConnector{dsn: dataSourceName(s.Path())}
					connector.failRollback.Store(true)
					db := sql.OpenDB(connector)
					db.SetMaxOpenConns(1)
					defer db.Close()
					err = ensureInstallationKey(ctx, db)
					fixtureCommits = true
					want = Failure{Kind: KindUnavailable, Op: "open", Detail: "cannot clean up installation cursor key creation", RecoveryAction: "reconcile database state before retrying"}
					effectPossible = true
				} else {
					err = ensureInstallationKey(ctx, s.db)
					want = Failure{Kind: KindUnavailable, Op: "open", Detail: "cannot persist the installation cursor key", RetrySafe: true, RecoveryAction: "retry once the database is writable"}
				}
				if !sqlitedb.IsConstraintViolation(err) {
					t.Fatalf("insert refusal lost cause: %v", err)
				}
				after := revisionValue(t, s.db)
				if fixtureCommits {
					if after != before+1 {
						t.Fatalf("uncertain cleanup reported no possible effect but revision stayed %d -> %d", before, after)
					}
					if !strings.Contains(err.Error(), "injected uncertain rollback outcome") || !strings.Contains(err.Error(), "cannot prove the transaction rolled back") {
						t.Fatalf("uncertain cleanup lost its rollback outcome: %v", err)
					}
				} else if after != before {
					t.Fatalf("insert refusal committed revision: %d -> %d", before, after)
				}
				if count := cursorKeyCount(t, s.db); count != 0 {
					t.Fatalf("refused insert created %d keys", count)
				}
			case "ignored insert":
				if _, err := s.db.Exec(`DELETE FROM agent_installation_keys; CREATE TRIGGER ignore_key BEFORE INSERT ON agent_installation_keys BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
				before := revisionValue(t, s.db)
				if err = ensureInstallationKey(ctx, s.db); err != nil {
					t.Fatalf("ignored insert failed: %v", err)
				}
				if after := revisionValue(t, s.db); after != before {
					t.Fatalf("ignored insert persisted a revision: %d -> %d", before, after)
				}
				if count := cursorKeyCount(t, s.db); count != 0 {
					t.Fatalf("ignored insert created %d keys", count)
				}
				return
			case "ignored insert rollback failure":
				if _, err := s.db.Exec(`DELETE FROM agent_installation_keys; CREATE TRIGGER ignore_key BEFORE INSERT ON agent_installation_keys BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
				before := revisionValue(t, s.db)
				connector := &restoreFaultConnector{dsn: dataSourceName(s.Path())}
				connector.failRollback.Store(true)
				db := sql.OpenDB(connector)
				db.SetMaxOpenConns(1)
				defer db.Close()
				err = ensureInstallationKey(ctx, db)
				want = Failure{Kind: KindUnavailable, Op: "open", Detail: "cannot roll back ignored installation cursor key creation", RecoveryAction: "reconcile database state before retrying"}
				effectPossible = true
				if after := revisionValue(t, s.db); after != before+1 {
					t.Fatalf("uncertain rollback reported no possible effect but revision stayed %d -> %d", before, after)
				}
				if !strings.Contains(err.Error(), "injected uncertain rollback outcome") || !strings.Contains(err.Error(), "cannot prove the transaction rolled back") {
					t.Fatalf("uncertain rollback lost its typed outcome: %v", err)
				}
				if count := cursorKeyCount(t, s.db); count != 0 {
					t.Fatalf("uncertain rollback created %d keys", count)
				}
			}
			var got *Failure
			if !errors.As(err, &got) || got.Kind != want.Kind || got.Op != want.Op || got.Detail != want.Detail || got.RetrySafe != want.RetrySafe || got.RecoveryAction != want.RecoveryAction || got.EffectPossible != effectPossible {
				t.Fatalf("failure fields: got %+v, want %+v, effect_possible=%t", got, want, effectPossible)
			}
			var leaked *sqlitedb.Error
			if errors.As(err, &leaked) {
				t.Fatalf("mechanism error escaped the store envelope: %v", err)
			}
		})
	}
}
