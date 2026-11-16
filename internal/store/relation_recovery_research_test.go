package store

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRelationRecoveryResearchShiftAndWithholding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "research.db")
	s := &Store{db: openMigratedTo(t, path, 144), path: path}
	corpusWorkParents(t, s, "fold_guard", "research-a", "research-b", "research-c")
	// A synthetic gap: relation ID 1 was consumed without a selected event, so
	// first-event inserted the surviving edge as ID 2. Second-event's edge was
	// later removed; its removal is not a selected event and is omitted.
	const first = `{"from":"research-a","to":"research-c","kind":"depends_on","reason":"first reason","resulting_version":2}`
	const second = `{"from":"research-a","to":"research-b","kind":"depends_on","reason":"second reason","resulting_version":3}`
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at) VALUES(2,'research-a','research-c','depends_on','2026-01-01T00:00:00Z');
INSERT INTO domain_events(seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES
(1,'first-event','relation.added','work_item','research-a','first-actor','2026-01-01T00:00:00Z',1,?),
(2,'second-event','relation.added','work_item','research-a','second-actor','2026-01-01T00:00:00Z',1,?);
DELETE FROM fold_guard`, first, second); err != nil {
		t.Fatal(err)
	}
	advanceHistoricalPrefix(t, s.db, 150)
	var reason string
	var version int
	if err := s.db.QueryRow(`SELECT reason,record_version FROM relations WHERE id=2`).Scan(&reason, &version); err != nil || reason != "second reason" || version != 3 {
		t.Fatalf("v150 metadata = %q/%d, %v", reason, version, err)
	}
	ctx := outcomeSnapshotContext(t, t.Context(), s.db)
	if err := Migrate(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	var firstDigest, secondDigest string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id='first-event'`).Scan(&firstDigest); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id='second-event'`).Scan(&secondDigest); err != nil {
		t.Fatal(err)
	}
	if firstDigest != historicalFixturePayloadDigest(t, first) || secondDigest != historicalFixturePayloadDigest(t, second) || firstDigest == secondDigest {
		t.Fatalf("artifact/receipt digest mismatch: %s %s", firstDigest, secondDigest)
	}
	// Receipts retain kind and subject but no time; that header admits both
	// events, not an identity.
	var candidates int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='relation.added' AND subject_type='work_item' AND subject_id='research-a'`).Scan(&candidates); err != nil || candidates != 2 {
		t.Fatalf("header candidates = %d, %v", candidates, err)
	}
	history := func() []HistoryEntry {
		t.Helper()
		got, err := s.QueryQ7(t.Context(), Q7Request{Work: "research-a", RecordKinds: []string{"relation"}})
		if err != nil {
			t.Fatal(err)
		}
		return got.Entries
	}
	graph := func() {
		t.Helper()
		got, err := s.QueryQ8(t.Context(), Q8Request{Work: "research-a", Direction: "outgoing"})
		if err != nil || len(got.Edges) != 1 || got.Edges[0].Target != "research-c" {
			t.Fatalf("graph = %+v, %v", got.Edges, err)
		}
	}
	before := history()
	if len(before) != 1 || before[0].Actor != "second-actor" || before[0].Reason != "second reason" || before[0].Version != 3 || before[0].WorkIDTo != "research-c" {
		t.Fatalf("misattribution = %+v", before)
	}
	graph()
	receiptsBefore := retirementUpgradeRows(t, s.db, `SELECT * FROM effect_receipts ORDER BY event_id`)
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET reason='',record_version=0 WHERE id=2; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if got := history(); len(got) != 0 {
		t.Fatalf("withheld history = %+v", got)
	}
	graph()
	var afterFirst, afterSecond string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id='first-event'`).Scan(&afterFirst); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id='second-event'`).Scan(&afterSecond); err != nil {
		t.Fatal(err)
	}
	if firstDigest != afterFirst || secondDigest != afterSecond {
		t.Fatal("receipt digests changed")
	}
	if got := retirementUpgradeRows(t, s.db, `SELECT * FROM effect_receipts ORDER BY event_id`); !reflect.DeepEqual(got, receiptsBefore) {
		t.Fatalf("receipts changed: got %v, want %v", got, receiptsBefore)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE relations SET reason=?,record_version=? WHERE id=2; DELETE FROM write_guard`, reason, version); err != nil {
		t.Fatal(err)
	}
	if got := history(); !reflect.DeepEqual(got, before) {
		t.Fatalf("restored history = %+v, want %+v", got, before)
	}
	graph()
}

func TestRelationRecoveryResearchDuplicateCandidates(t *testing.T) {
	const first = `{"from":"a","to":"b","kind":"depends_on","reason":"initial","resulting_version":2}`
	const second = `{"from":"a","to":"b","kind":"depends_on","reason":"readded","resulting_version":4}`
	firstDigest := historicalFixturePayloadDigest(t, first)
	secondDigest := historicalFixturePayloadDigest(t, second)
	s := openTemp(t)
	seedWork(t, s, "a")
	seedWork(t, s, "b")
	// Hand-inserted current-schema rows: the digests bind by construction.
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,reason,record_version) VALUES(98765,'a','b','depends_on','2026-01-01T00:00:00Z','initial',2);
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,relation_seq,payload_digest)
VALUES('candidate-one',(SELECT max(effect_seq)+1 FROM effect_receipts),'relation.added','work_item','a','actor-one',1,98765,?),
('candidate-two',(SELECT max(effect_seq)+2 FROM effect_receipts),'relation.added','work_item','a','actor-two',1,98766,?);
DELETE FROM write_guard`, firstDigest, secondDigest); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE event_id IN ('candidate-one','candidate-two') AND ((event_id='candidate-one' AND payload_digest=?) OR (event_id='candidate-two' AND payload_digest=?))`, firstDigest, secondDigest).Scan(&count); err != nil || count != 2 || firstDigest == secondDigest {
		t.Fatalf("verified candidates = %d, %v", count, err)
	}
	// Both candidate payloads name the retained edge's endpoints and kind, so
	// that agreement and the digest bindings do not identify the insertion.
	var from, to, kind string
	if err := s.db.QueryRow(`SELECT work_id_from,work_id_to,kind FROM relations WHERE id=98765`).Scan(&from, &to, &kind); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{first, second} {
		var p struct{ From, To, Kind string }
		if err := json.Unmarshal([]byte(payload), &p); err != nil || p.From != from || p.To != to || p.Kind != kind {
			t.Fatalf("candidate %s does not agree with retained edge %s->%s %s: %v", payload, from, to, kind, err)
		}
	}
	// Q7 selects the candidate only by numeric position.
	got, err := s.QueryQ7(t.Context(), Q7Request{Work: "a", RecordKinds: []string{"relation"}})
	if err != nil || len(got.Entries) != 1 || got.Entries[0].Actor != "actor-one" || got.Entries[0].Reason != "initial" || got.Entries[0].Version != 2 {
		t.Fatalf("numeric candidate selection = %+v, %v", got.Entries, err)
	}
}
