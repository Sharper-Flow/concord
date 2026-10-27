package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
)

func TestInstallationKeyFailureFieldsAndCauses(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing row", "invalid length", "canceled inspection", "missing revision", "insert refusal", "ignored insert rollback failure"} {
		t.Run(mode, func(t *testing.T) {
			s := openTemp(t)
			ctx := t.Context()
			var err error
			want := Failure{Kind: KindUnavailable, Op: "cursor_key", Detail: "cannot read the installation cursor key", RetrySafe: true, RecoveryAction: "open a migrated authority database"}
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
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				err = ensureInstallationKey(canceled, s.db)
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation cause: %v", err)
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
			case "insert refusal":
				if _, err := s.db.Exec(`DELETE FROM agent_installation_keys; CREATE TRIGGER refuse_key BEFORE INSERT ON agent_installation_keys BEGIN SELECT RAISE(ABORT,'fixture refusal'); END`); err != nil {
					t.Fatal(err)
				}
				before := revisionValue(t, s.db)
				err = ensureInstallationKey(ctx, s.db)
				if !sqlitedb.IsConstraintViolation(err) || revisionValue(t, s.db) != before {
					t.Fatalf("insert refusal lost cause or committed revision: %v", err)
				}
				want = Failure{Kind: KindUnavailable, Op: "open", Detail: "cannot persist the installation cursor key", RetrySafe: true, RecoveryAction: "retry once the database is writable"}
			case "ignored insert rollback failure":
				if _, err := s.db.Exec(`DELETE FROM agent_installation_keys; CREATE TRIGGER ignore_key BEFORE INSERT ON agent_installation_keys BEGIN SELECT RAISE(IGNORE); END`); err != nil {
					t.Fatal(err)
				}
				connector := &restoreFaultConnector{dsn: dataSourceName(s.Path())}
				connector.failRollback.Store(true)
				db := sql.OpenDB(connector)
				db.SetMaxOpenConns(1)
				defer db.Close()
				err = ensureInstallationKey(ctx, db)
				var outer *Failure
				if !errors.As(err, &outer) || outer.Err == nil || outer.Err.Error() != "injected uncertain rollback outcome" {
					t.Fatalf("ignored insert rollback cause changed: %v", err)
				}
				want = Failure{Kind: KindUnavailable, Op: "open", Detail: "cannot roll back ignored installation cursor key creation", RetrySafe: true, RecoveryAction: "retry once the database is writable"}
			}
			var got *Failure
			if !errors.As(err, &got) || got.Kind != want.Kind || got.Op != want.Op || got.Detail != want.Detail || got.RetrySafe != want.RetrySafe || got.RecoveryAction != want.RecoveryAction || got.EffectPossible {
				t.Fatalf("failure fields: got %+v, want %+v", got, want)
			}
			var leaked *sqlitedb.Error
			if errors.As(err, &leaked) {
				t.Fatalf("mechanism error escaped the store envelope: %v", err)
			}
		})
	}
}
