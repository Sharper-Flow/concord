package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventLogRetirementFreshStore(t *testing.T) {
	db, err := sql.Open(driverName, dataSourceName(filepath.Join(t.TempDir(), "fresh.db")))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	var obsolete int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name IN ('domain_events','fold_guard') OR instr(sql,'fold_guard')>0 OR instr(sql,'fold-only')>0 OR instr(sql,'domain_events')>0`).Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatalf("obsolete schema objects = %d, err = %v", obsolete, err)
	}
	var guards int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND instr(sql,'write_guard')>0`).Scan(&guards); err != nil || guards == 0 {
		t.Fatalf("write guard triggers = %d, err = %v", guards, err)
	}
	s := &Store{db: db}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	e := testEvent()
	seq, err := AppendEvent(t.Context(), tx, e)
	if err != nil || seq <= 0 {
		t.Fatalf("receipt-only append = %d, %v", seq, err)
	}
	e.Seq = seq
	scope, err := beginWrite(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyEffect(t.Context(), tx, e, scope); err != nil {
		t.Fatal(err)
	}
	if err := scope.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := s.db.QueryRow(`SELECT display_name FROM products WHERE id=?`, e.SubjectID).Scan(&name); err != nil || name == "" {
		t.Fatalf("current row = %q, %v", name, err)
	}
	if _, err := s.db.Exec(`UPDATE products SET display_name='unguarded' WHERE id=?`, e.SubjectID); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
		t.Fatalf("unguarded update = %v", err)
	}
}

func TestEventLogRetirementReceiptProtection(t *testing.T) {
	for _, mode := range []string{"fresh", "upgraded"} {
		t.Run(mode, func(t *testing.T) {
			var db *sql.DB
			if mode == "upgraded" {
				db, _ = retirementUpgradeFixture(t)
				retirementUpgradeMigrate(t, db)
			} else {
				db = openTemp(t).db
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			e := testEvent()
			retirementUpgradeApplyProduct(t, tx, e)
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			for _, guarded := range []bool{false, true} {
				probe, err := db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer probe.Rollback()
				if guarded {
					if _, err := probe.Exec(`INSERT INTO write_guard VALUES(1)`); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := probe.Exec(`UPDATE effect_receipts SET actor='tampered' WHERE event_id=?`, e.EventID); err == nil || !strings.Contains(err.Error(), "immutable") {
					t.Fatalf("receipt update (guarded=%t) = %v", guarded, err)
				}
				_, deleteErr := probe.Exec(`DELETE FROM effect_receipts WHERE event_id=?`, e.EventID)
				if guarded && deleteErr != nil {
					t.Fatalf("guarded cleanup = %v", deleteErr)
				}
				if !guarded && (deleteErr == nil || !strings.Contains(deleteErr.Error(), "write-seam-only")) {
					t.Fatalf("unguarded cleanup = %v", deleteErr)
				}
				if err := probe.Rollback(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
