package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRelationSequenceUpgradePreservesRowsAndRetries(t *testing.T) {
	useStampedBuild(t)
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "upgrade.db"), 195)
	s := &Store{db: db}
	for _, id := range []string{"a", "b", "c", "d"} {
		seedWork(t, s, id)
	}
	va := readWorkVersion(t, s, "a")
	if err := applyWorkEvent(t, s, relationAddedEvent("positioned", "blocks", "a", "b", va, va+1), workVersion("a", va)); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,record_version)
		VALUES(2,'a','c','depends_on','2026-01-01T00:00:00Z',0),
		      (10,'a','d','depends_on','2026-01-01T00:00:00Z',0);
		DELETE FROM relations WHERE id=10;
		DELETE FROM write_guard`)
	vc := readWorkVersion(t, s, "c")
	op := relationAddedEvent("retry", "depends_on", "c", "d", vc, vc+1)
	err := applyWorkEvent(t, s, op, workVersion("c", vc))
	if err == nil || !strings.Contains(err.Error(), "relations.id") {
		t.Fatalf("expected primary key collision before upgrade, got %v", err)
	}
	var failedReceipts int
	if err := db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE event_id='retry'`).Scan(&failedReceipts); err != nil || failedReceipts != 0 {
		t.Fatalf("failed operation receipts=%d err=%v", failedReceipts, err)
	}
	beforeRelations := retirementUpgradeRows(t, db, `SELECT * FROM relations ORDER BY id`)
	beforeReceipts := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	beforeQ8, err := s.QueryQ8(context.Background(), Q8Request{Work: "a", RelationKinds: []string{"depends_on"}, Direction: "outgoing"})
	if err != nil {
		t.Fatal(err)
	}
	beforeQ7, err := s.QueryQ7(t.Context(), Q7Request{Work: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	var last int64
	if err := db.QueryRow(`SELECT last FROM store_sequences WHERE name='relation'`).Scan(&last); err != nil || last != 10 {
		t.Fatalf("upgraded relation frontier = %d, err %v; want 10", last, err)
	}
	var breaking int
	if err := db.QueryRow(`SELECT breaking FROM schema_migrations WHERE version=196`).Scan(&breaking); err != nil || breaking != 0 {
		t.Fatalf("migration 196 breaking=%d err=%v", breaking, err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM relations ORDER BY id`); !reflect.DeepEqual(got, beforeRelations) {
		t.Fatalf("migration changed relations: %v -> %v", beforeRelations, got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, beforeReceipts) {
		t.Fatalf("migration changed receipts: %v -> %v", beforeReceipts, got)
	}
	afterQ8, err := s.QueryQ8(context.Background(), Q8Request{Work: "a", RelationKinds: []string{"depends_on"}, Direction: "outgoing"})
	if err != nil || !reflect.DeepEqual(beforeQ8.Edges, afterQ8.Edges) {
		t.Fatalf("Q8 changed: before=%+v after=%+v err=%v", beforeQ8, afterQ8, err)
	}
	afterQ7, err := s.QueryQ7(t.Context(), Q7Request{Work: "a"})
	if err != nil || !reflect.DeepEqual(beforeQ7.Entries, afterQ7.Entries) {
		t.Fatalf("Q7 entries changed: before=%+v after=%+v err=%v", beforeQ7.Entries, afterQ7.Entries, err)
	}
	vb := readWorkVersion(t, s, "b")
	if err := applyWorkEvent(t, s, relationAddedEvent("fresh", "blocks", "b", "c", vb, vb+1), workVersion("b", vb)); err != nil {
		t.Fatalf("fresh relation: %v", err)
	}
	var freshID, freshReceipt int64
	if err := db.QueryRow(`SELECT id FROM relations WHERE work_id_from='b' AND work_id_to='c' AND kind='blocks'`).Scan(&freshID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT relation_seq FROM effect_receipts WHERE event_id='fresh'`).Scan(&freshReceipt); err != nil || freshID != 11 || freshReceipt != 11 {
		t.Fatalf("fresh relation id=%d receipt=%d err=%v", freshID, freshReceipt, err)
	}
	if err := applyWorkEvent(t, s, op, workVersion("c", vc)); err != nil {
		t.Fatalf("same-key retry: %v", err)
	}
	if err := applyWorkEvent(t, s, op, nil); !hasFailureKind(err, KindDuplicateEvent) {
		t.Fatalf("event replay: %v", err)
	}
	var id, count int64
	if err := db.QueryRow(`SELECT max(id),count(*) FROM relations WHERE work_id_from='c' AND work_id_to='d'`).Scan(&id, &count); err != nil || id != 12 || count != 1 {
		t.Fatalf("retry id=%d count=%d err=%v", id, count, err)
	}
	var retryReceipts int
	if err := db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE event_id='retry' AND relation_seq=12`).Scan(&retryReceipts); err != nil || retryReceipts != 1 {
		t.Fatalf("retry receipts=%d err=%v", retryReceipts, err)
	}
	vb = readWorkVersion(t, s, "b")
	err = applyWorkEvent(t, s, relationAddedEvent("duplicate", "blocks", "b", "c", vb, vb+1), workVersion("b", vb))
	if !hasFailureKind(err, KindRelationConflict) {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestRelationSequenceUpgradeHighWaterSources(t *testing.T) {
	useStampedBuild(t)
	for _, tc := range []struct {
		name  string
		setup string
		want  int64
	}{
		{"empty", ``, 0},
		{"current sequence", `UPDATE store_sequences SET last=30 WHERE name='relation'`, 30},
		{"standing row", `INSERT INTO write_guard(active) VALUES(1);
			INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,record_version) VALUES(20,'a','b','blocks','time',0);
			DELETE FROM write_guard; UPDATE sqlite_sequence SET seq=1 WHERE name='relations'`, 20},
		{"deleted high water", `INSERT INTO write_guard(active) VALUES(1);
			INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,record_version) VALUES(20,'a','b','blocks','time',0);
			DELETE FROM relations WHERE id=20; DELETE FROM write_guard`, 20},
		{"receipt position", `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,relation_seq)
			VALUES('receipt',100,'relation.added','work_item','a','operator',1,20)`, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigratedTo(t, filepath.Join(t.TempDir(), "upgrade.db"), 195)
			retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1);
				INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES('a','task','A','needed',1,'time','time'),('b','task','B','needed',1,'time','time');
				DELETE FROM write_guard`)
			if tc.setup != "" {
				retirementUpgradeExec(t, db, tc.setup)
			}
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			var got int64
			if err := db.QueryRow(`SELECT last FROM store_sequences WHERE name='relation'`).Scan(&got); err != nil || got != tc.want {
				t.Fatalf("frontier=%d want=%d err=%v", got, tc.want, err)
			}
		})
	}
}

func TestRelationIdentityCollisionIsInvariantViolation(t *testing.T) {
	s := openTemp(t)
	for _, id := range []string{"a", "b", "c"} {
		seedWork(t, s, id)
	}
	va := readWorkVersion(t, s, "a")
	if err := applyWorkEvent(t, s, relationAddedEvent("one", "blocks", "a", "b", va, va+1), workVersion("a", va)); err != nil {
		t.Fatal(err)
	}
	retirementUpgradeExec(t, s.db, `UPDATE store_sequences SET last=0 WHERE name='relation'`)
	vb := readWorkVersion(t, s, "b")
	err := applyWorkEvent(t, s, relationAddedEvent("two", "blocks", "b", "c", vb, vb+1), workVersion("b", vb))
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation || !strings.Contains(err.Error(), "relations.id") {
		t.Fatalf("identity collision must report invariant violation: %v", err)
	}
	if strings.Contains(failure.RecoveryAction, "duplicate") || strings.Contains(failure.RecoveryAction, "self-edge") {
		t.Fatalf("misleading recovery: %+v", failure)
	}
}
