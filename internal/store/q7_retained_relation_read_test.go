package store

import (
	"testing"
)

// TestQ7HistoryServesWorkWithPreReceiptRelations covers relation rows the
// effect coordinate system never positioned: written before effect receipts
// existed, retained by migration 150 with record_version 0 and no
// effect_receipts row. The history read must still serve the work item's
// positioned records, and the relation-graph read must keep serving the
// retained edge.
func TestQ7HistoryServesWorkWithPreReceiptRelations(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "q7-retained")
	seedWork(t, s, "q7-peer")
	version := readWorkVersion(t, s, "q7-peer")
	positioned := relationAddedEvent("q7-positioned", "blocks", "q7-peer", "q7-retained", version, version+1)
	if err := applyWorkEvent(t, s, positioned, workVersion("q7-peer", version)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
	 INSERT INTO relations(work_id_from,work_id_to,kind,created_at,record_version) VALUES('q7-retained','q7-peer','depends_on','2026-10-07T02:30:41Z',0);
	 DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	out, err := s.QueryQ7(t.Context(), Q7Request{Work: "q7-retained"})
	if err != nil {
		t.Fatalf("history refused a work item that retains a pre-receipt relation: %v", err)
	}
	var positionedSeen bool
	for _, entry := range out.Entries {
		if entry.EntryKind != "relation" {
			continue
		}
		if entry.Version < 1 || entry.Position.EffectSeq < 1 {
			t.Fatalf("history served an unpositioned relation entry: %+v", entry)
		}
		if entry.WorkIDFrom == "q7-peer" && entry.WorkIDTo == "q7-retained" && entry.RelationKind == "blocks" {
			positionedSeen = true
		}
	}
	if !positionedSeen {
		t.Fatalf("history lost the positioned relation entry: %+v", out.Entries)
	}
	// The relation-graph read owns every live relation row, positioned or not.
	edges, err := s.QueryQ8(t.Context(), Q8Request{Work: "q7-retained", RelationKinds: []string{"depends_on"}, Direction: "outgoing"})
	if err != nil {
		t.Fatal(err)
	}
	var retainedSeen bool
	for _, edge := range edges.Edges {
		if edge.Kind == "depends_on" && edge.Source == "q7-retained" && edge.Target == "q7-peer" {
			retainedSeen = true
		}
	}
	if !retainedSeen {
		t.Fatalf("relation graph lost the retained pre-receipt edge: %+v", edges.Edges)
	}
}
