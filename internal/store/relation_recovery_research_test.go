package store

import (
	"encoding/json"
	"testing"
)

func researchPayloadDigest(t *testing.T, payload string) string {
	t.Helper()
	digest, err := effectPayloadDigest([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestRelationRecoveryResearchDuplicateCandidates(t *testing.T) {
	const first = `{"from":"a","to":"b","kind":"depends_on","reason":"initial","resulting_version":2}`
	const second = `{"from":"a","to":"b","kind":"depends_on","reason":"readded","resulting_version":4}`
	firstDigest := researchPayloadDigest(t, first)
	secondDigest := researchPayloadDigest(t, second)
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
