package store

import (
	"slices"
	"strings"
	"testing"
)

// History must admit every kind the relation.added fold accepts: the fold's
// kinds less its link refusals.
func TestQ7RelationAddedKindsMatchFold(t *testing.T) {
	var kinds []string
	for kind := range relationKinds {
		if _, refused := relationLinkRefusals[kind]; !refused {
			kinds = append(kinds, "'"+kind+"'")
		}
	}
	slices.Sort(kinds)
	if want := "f.kind='relation.added' AND f.subject_id=r.work_id_from AND r.resolution_id IS NULL AND r.kind IN (" + strings.Join(kinds, ",") + "))"; !strings.Contains(currentQ7EntriesSQL, want) {
		t.Fatalf("history relation.added predicate does not admit exactly the fold's kinds; want %s", want)
	}
}

func TestQ7RelationReceiptDiscriminants(t *testing.T) {
	for _, tc := range []struct {
		name, kind, subjectType, subject, relationKind, resolution, resolutionKind string
		duplicate, overlap, inverted, zeroVersion, want                            bool
	}{
		{name: "valid", kind: "relation.added", subjectType: "work_item", subject: "disc-a", relationKind: "depends_on", want: true},
		{name: "successor", kind: "workflow.successor_linked", subjectType: "work_item", subject: "disc-a", relationKind: "forward_link", want: true},
		{name: "capture", kind: "work.created", subjectType: "work_item", subject: "disc-a", relationKind: "raised_from", want: true},
		{name: "overlap", kind: "workflow.overlap_resolved", subjectType: "work_item", subject: "disc-a", relationKind: "depends_on", resolution: "disc-event", overlap: true, want: true},
		{name: "overlap pair order", kind: "workflow.overlap_resolved", subjectType: "work_item", subject: "disc-a", relationKind: "compatible_with", resolution: "disc-event", resolutionKind: "compatible_with", overlap: true, inverted: true, want: true},
		{name: "overlap wrong kind", kind: "workflow.overlap_resolved", subjectType: "work_item", subject: "disc-a", relationKind: "blocks", resolution: "disc-event", overlap: true},
		{name: "wrong subject type", kind: "relation.added", subjectType: "project", subject: "disc-a", relationKind: "depends_on"},
		{name: "wrong endpoint", kind: "relation.added", subjectType: "work_item", subject: "disc-b", relationKind: "depends_on"},
		{name: "capture target endpoint", kind: "work.created", subjectType: "work_item", subject: "disc-b", relationKind: "raised_from"},
		{name: "wrong event", kind: "work.updated", subjectType: "work_item", subject: "disc-a", relationKind: "depends_on"},
		{name: "unpositioned version", kind: "relation.added", subjectType: "work_item", subject: "disc-a", relationKind: "depends_on", zeroVersion: true},
		{name: "wrong relation kind", kind: "workflow.successor_linked", subjectType: "work_item", subject: "disc-a", relationKind: "depends_on"},
		{name: "added internal kind", kind: "relation.added", subjectType: "work_item", subject: "disc-a", relationKind: "forward_link"},
		{name: "wrong resolution", kind: "workflow.overlap_resolved", subjectType: "work_item", subject: "disc-a", relationKind: "depends_on", resolution: "other-resolution"},
		{name: "unexpected resolution", kind: "relation.added", subjectType: "work_item", subject: "disc-a", relationKind: "depends_on", resolution: "other-resolution"},
		{name: "ambiguous", kind: "relation.added", subjectType: "work_item", subject: "disc-a", relationKind: "depends_on", duplicate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, to := "disc-a", "disc-b"
			if tc.inverted {
				from, to = to, from
			}
			if tc.resolutionKind == "" {
				tc.resolutionKind = "depends_on"
			}
			s := openTemp(t)
			seedWork(t, s, "disc-a")
			seedWork(t, s, "disc-b")
			_, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1)`)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.db.Exec(`INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,relation_seq)
VALUES('disc-event',(SELECT MAX(effect_seq)+1 FROM effect_receipts),?,?,?,'operator',1,98765)`, tc.kind, tc.subjectType, tc.subject)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.db.Exec(`INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,resolution_id,reason,record_version)
VALUES(98765,?,?,?,'2026-08-07T12:00:00Z',NULLIF(?,''),'reason',CASE WHEN ?='work.created' THEN 1 ELSE 2 END)`, from, to, tc.relationKind, tc.resolution, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if tc.zeroVersion {
				if _, err := s.db.Exec(`UPDATE relations SET record_version=0 WHERE id=98765`); err != nil {
					t.Fatal(err)
				}
			}
			if tc.overlap {
				_, err = s.db.Exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES('actor:'||lower(hex(zeroblob(32))),'principal','client','agent','session','operator','now');
INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate) VALUES
('disc-a',1,'internal_sqlite','now','actor:'||lower(hex(zeroblob(32))),'[]'),('disc-b',1,'internal_sqlite','now','actor:'||lower(hex(zeroblob(32))),'[]')`)
				if err != nil {
					t.Fatal(err)
				}
				_, err = s.db.Exec(`INSERT INTO workflow_overlap_resolutions(resolution_id,event_seq,product_id,from_work_id,to_work_id,from_contract_version,to_contract_version,resolution_kind,reason,created_at)
VALUES('disc-event',(SELECT effect_seq FROM effect_receipts WHERE event_id='disc-event'),'product',?,?,1,1,?,'reason','2026-08-07T12:00:00Z')`, from, to, tc.resolutionKind)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			if tc.duplicate {
				_, err = s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,relation_seq)
VALUES('disc-duplicate',(SELECT MAX(effect_seq)+1 FROM effect_receipts),'relation.added','work_item','disc-a','operator',1,98765);
DELETE FROM write_guard`)
				if err != nil {
					t.Fatal(err)
				}
			}
			history, err := s.QueryQ7(t.Context(), Q7Request{Work: "disc-a", RecordKinds: []string{"relation"}})
			if err != nil || (len(history.Entries) == 1) != tc.want || len(history.Entries) > 1 {
				t.Fatalf("history = %+v, err = %v; want relation = %v", history.Entries, err, tc.want)
			}
		})
	}
}

func TestQ7RejectsUnrelatedRelationReceipt(t *testing.T) {
	s := openTemp(t)
	for _, id := range []string{"attr-a", "attr-b", "attr-c"} {
		seedWork(t, s, id)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,relation_seq)
VALUES('attr-receipt',(SELECT MAX(effect_seq)+1 FROM effect_receipts),'relation.added','work_item','attr-c','operator',1,98765);
INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,reason,record_version)
VALUES(98765,'attr-a','attr-b','depends_on','2026-08-07T12:00:00Z','unrelated reason',2);
DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	history, err := s.QueryQ7(t.Context(), Q7Request{Work: "attr-a", RecordKinds: []string{"relation"}})
	if err != nil || len(history.Entries) != 0 {
		t.Fatalf("unrelated receipt attributed to relation: %+v, %v", history.Entries, err)
	}
	graph, err := s.QueryQ8(t.Context(), Q8Request{Work: "attr-a", Direction: "outgoing"})
	if err != nil || len(graph.Edges) != 1 || graph.Edges[0].Target != "attr-b" {
		t.Fatalf("retained relation missing from graph: %+v, %v", graph.Edges, err)
	}
}
