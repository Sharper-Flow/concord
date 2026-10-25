package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"slices"
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
