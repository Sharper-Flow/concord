package store

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestQ7CurrentStateWithoutLog(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "q7-current")
	ctx := context.Background()
	before, err := s.QueryQ7(ctx, Q7Request{Work: "q7-current"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE domain_events RENAME TO hidden_domain_events`); err != nil {
		t.Fatal(err)
	}
	after, err := s.QueryQ7(ctx, Q7Request{Work: "q7-current"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before.Result)
	b, _ := json.Marshal(after.Result)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("current result changed without writes: before=%s after=%s", a, b)
	}
}

func TestQ7CurrentTransitionAndTieCursor(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "q7-prior")
	seedWork(t, s, "q7-next")
	version := readWorkVersion(t, s, "q7-prior")
	event := workSupersededEvent("q7-superseded", "q7-next", "q7-prior", version, version+1)
	if err := applyWorkEvent(t, s, event, workVersion("q7-prior", version)); err != nil {
		t.Fatal(err)
	}
	peer, err := s.QueryQ7(t.Context(), Q7Request{Work: "q7-next", RecordKinds: []string{"relation"}})
	if err != nil || len(peer.Entries) != 1 {
		t.Fatalf("successor relation: %+v, %v", peer.Entries, err)
	}
	if entry := peer.Entries[0]; entry.Version != version+1 || entry.VersionWorkID != "q7-prior" || entry.WorkIDFrom != "q7-next" {
		t.Fatalf("relation version must name its writing work, not its source endpoint: %+v", entry)
	}
	for _, direction := range []string{"oldest_first", "newest_first"} {
		first, err := s.QueryQ7(t.Context(), Q7Request{Work: "q7-prior", Direction: direction, Limit: 1})
		if err != nil || first.NextCursor == nil {
			t.Fatalf("first tie page: %+v %v", first, err)
		}
		second, err := s.QueryQ7(t.Context(), Q7Request{Work: "q7-prior", Direction: direction, Limit: 1, Cursor: *first.NextCursor})
		if err != nil || second.NextCursor != nil || len(second.Entries) != 1 {
			t.Fatalf("second tie page: %+v %v", second, err)
		}
		if first.Entries[0].Position.EffectSeq != second.Entries[0].Position.EffectSeq || first.Entries[0].EntryKind == second.Entries[0].EntryKind {
			t.Fatalf("fixture must exercise the same effect's two current rows")
		}
		for _, entry := range append(first.Entries, second.Entries...) {
			if entry.Version != version+1 || entry.VersionWorkID != "q7-prior" || entry.Reason != "test" || entry.Actor != event.Actor {
				t.Fatalf("lost writing metadata: %+v", entry)
			}
			if entry.EntryKind == "lifecycle" && (entry.Lifecycle != "superseded" || entry.From == nil || *entry.From != "needed") {
				t.Fatalf("lost lifecycle transition: %+v", entry)
			}
		}
		for _, changed := range []Q7Request{
			{Work: "q7-next", Direction: direction, Cursor: *first.NextCursor},
			{Work: "q7-prior", Direction: direction, RecordKinds: []string{"lifecycle"}, Cursor: *first.NextCursor},
		} {
			_, err := s.QueryQ7(t.Context(), changed)
			assertFailureKind(t, err, KindInvalidCursor)
		}
		old := base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"seq":1}`))
		_, err = s.QueryQ7(t.Context(), Q7Request{Work: "q7-prior", Cursor: old})
		assertFailureKind(t, err, KindInvalidCursor)
		if err := s.Transact(t.Context(), func(tx *Transaction) error { return advanceStoreRevision(t.Context(), tx.tx) }); err != nil {
			t.Fatal(err)
		}
		_, err = s.QueryQ7(t.Context(), Q7Request{Work: "q7-prior", Direction: direction, Cursor: *first.NextCursor})
		assertFailureKind(t, err, KindInvalidCursor)
	}
}

func TestQ7CurrentLifecycleOverwritesTrace(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const work = "q7-overwritten"
	seedWork(t, s, work)
	version := readWorkVersion(t, s, work)
	for _, transition := range [][2]string{{"needed", "in_progress"}, {"in_progress", "cancelled"}} {
		event := workTransitionEvent("q7-overwrite-"+transition[1], work, transition[0], transition[1], version, version+1)
		var fields map[string]any
		if err := json.Unmarshal(event.Payload, &fields); err != nil {
			t.Fatal(err)
		}
		fields["evidence_refs"] = []string{"proof:" + transition[1]}
		event.Payload, _ = json.Marshal(fields)
		if err := applyWorkEvent(t, s, event, workVersion(work, version)); err != nil {
			t.Fatal(err)
		}
		version++
	}
	out, err := s.QueryQ7(t.Context(), Q7Request{Work: work})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 1 || out.Entries[0].Version != version || out.Entries[0].Lifecycle != "cancelled" || !slices.Equal(out.Entries[0].EvidenceRefs, []string{"proof:cancelled"}) {
		t.Fatalf("current lifecycle retained an overwritten transition: %+v", out.Entries)
	}
}

func TestQ7RelationReceiptJoinIsIndexed(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	rows, err := s.db.QueryContext(t.Context(), `EXPLAIN QUERY PLAN `+currentQ7EntriesSQL+` SELECT entry_id FROM entries`, "q7-plan")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(detail, "SCAN f") {
			t.Fatalf("relation receipts are scanned per relation: %s", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestQ7DirectionsOrderAcrossPages(t *testing.T) {
	t.Parallel()
	s, work := seedQ7CurrentRows(t)
	compare := func(a, b HistoryEntry) int {
		if order := cmp.Compare(a.Position.EffectSeq, b.Position.EffectSeq); order != 0 {
			return order
		}
		if order := strings.Compare(a.EntryKind, b.EntryKind); order != 0 {
			return order
		}
		return strings.Compare(a.EntryID, b.EntryID)
	}
	var oldest []HistoryEntry
	for _, direction := range []string{"oldest_first", "newest_first"} {
		t.Run(direction, func(t *testing.T) {
			var entries []HistoryEntry
			cursor, pages := "", 0
			for {
				page, err := s.QueryQ7(t.Context(), Q7Request{Work: work, Direction: direction, Limit: 2, Cursor: cursor})
				if err != nil {
					t.Fatal(err)
				}
				pages++
				entries = append(entries, page.Entries...)
				if pages > 100 {
					t.Fatal("pagination did not terminate")
				}
				if page.NextCursor == nil {
					break
				}
				cursor = *page.NextCursor
			}
			if pages < 2 || len(entries) <= 2 {
				t.Fatalf("ordering fixture must span pages: %d pages, %d entries", pages, len(entries))
			}
			for i := 1; i < len(entries); i++ {
				order := compare(entries[i-1], entries[i])
				if direction == "newest_first" {
					order = -order
				}
				if order >= 0 {
					t.Fatalf("%s lost strict tuple ordering at entry %d: %+v then %+v", direction, i, entries[i-1], entries[i])
				}
			}
			if direction == "oldest_first" {
				oldest = entries
			} else {
				slices.Reverse(entries)
				if !reflect.DeepEqual(entries, oldest) {
					t.Fatal("directions enumerate different retained entries")
				}
			}
		})
	}
}
