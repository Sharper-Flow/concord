package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// CD-0030 R1: the paged observations read keeps the legacy bounds, orders
// recorded_at descending with observation_id ascending as the tie-breaker,
// drains each row exactly once behind a work-bound cursor, and refuses
// foreign or tampered cursors and corrupted stored lists.

// observationCorpusEntry is one seeded observation with its exact ordering
// keys.
type observationCorpusEntry struct {
	ID         string
	RecordedAt time.Time
}

// seedObservationCorpus records forty observations on one work item. Time
// advances one second per index, except indices 15 through 20 inclusive,
// which share one timestamp so the observation_id tie-breaker is exercised
// across page boundaries.
func seedObservationCorpus(t *testing.T, s *Store, workID string, base time.Time) []observationCorpusEntry {
	t.Helper()
	entries := make([]observationCorpusEntry, 0, 40)
	for i := 0; i < 40; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		if i >= 15 && i <= 20 {
			at = base.Add(15 * time.Second)
		}
		id := fmt.Sprintf("obs:%016x", i)
		payload, err := json.Marshal(map[string]any{
			"observation_id": id,
			"statement":      fmt.Sprintf("observation %d", i),
			"refs":           []string{fmt.Sprintf("ref-%d", i)},
			"tags":           []string{"corpus"},
		})
		if err != nil {
			t.Fatal(err)
		}
		event := Event{EventID: fmt.Sprintf("%s-obs-%d", workID, i), Kind: WorkObservationRecorded, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "principal/agent", OccurredAt: at, PayloadVersion: 1, Payload: payload}
		if err := recordObservation(t, s, event); err != nil {
			t.Fatalf("seed observation %d: %v", i, err)
		}
		entries = append(entries, observationCorpusEntry{ID: id, RecordedAt: at})
	}
	return entries
}

// expectedObservationOrder sorts corpus entries by the page contract:
// recorded_at descending, then observation_id ascending.
func expectedObservationOrder(entries []observationCorpusEntry) []observationCorpusEntry {
	out := append([]observationCorpusEntry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].RecordedAt.Equal(out[j].RecordedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].RecordedAt.After(out[j].RecordedAt)
	})
	return out
}

func TestWorkObservationPageDrainsNewestFirstWithTieBreaker(t *testing.T) {
	t.Parallel()
	s := observationFixture(t)
	base := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	entries := seedObservationCorpus(t, s, "work-99", base)
	expected := expectedObservationOrder(entries)

	var seen []string
	cursor := ""
	pages := 0
	for {
		page, err := s.ReadWorkObservations(context.Background(), WorkObservationsRequest{WorkID: "work-99", Limit: 7, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 40 {
			t.Fatalf("page total=%d want 40", page.Total)
		}
		if page.QueryID != "CD-0030.R1" {
			t.Fatalf("query id=%q want CD-0030.R1", page.QueryID)
		}
		if page.ResolvedScope.WorkID != "work-99" {
			t.Fatalf("resolved scope=%+v want work-99", page.ResolvedScope)
		}
		if len(page.Observations) == 0 || len(page.Observations) > 7 {
			t.Fatalf("page size=%d want 1..7", len(page.Observations))
		}
		for _, o := range page.Observations {
			if o.WorkID != "work-99" {
				t.Fatalf("foreign work id %q in page", o.WorkID)
			}
			if len(o.Refs) != 1 || len(o.Tags) != 1 {
				t.Fatalf("observation %s decoded refs=%v tags=%v", o.ObservationID, o.Refs, o.Tags)
			}
			seen = append(seen, o.ObservationID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		pages++
		if pages > 40 {
			t.Fatal("cursor drain does not terminate")
		}
	}
	if len(seen) != 40 {
		t.Fatalf("drained %d observations want 40", len(seen))
	}
	emitted := map[string]bool{}
	for _, id := range seen {
		if emitted[id] {
			t.Fatalf("observation %s emitted twice", id)
		}
		emitted[id] = true
	}
	want := make([]string, len(expected))
	for i, e := range expected {
		want[i] = e.ID
	}
	if strings.Join(seen, ",") != strings.Join(want, ",") {
		t.Fatalf("drain order mismatch:\n got %s\nwant %s", strings.Join(seen, ","), strings.Join(want, ","))
	}
	// The shared-timestamp cluster must arrive contiguously, ascending by
	// identity, so the tie-breaker holds across page boundaries.
	cluster := make([]string, 0, 6)
	for _, e := range expected {
		if e.RecordedAt.Equal(base.Add(15 * time.Second)) {
			cluster = append(cluster, e.ID)
		}
	}
	if len(cluster) != 6 {
		t.Fatalf("cluster size=%d want 6", len(cluster))
	}
	joined := strings.Join(seen, ",")
	if !strings.Contains(joined, strings.Join(cluster, ",")) {
		t.Fatalf("tie cluster %v not contiguous ascending in drain", cluster)
	}
}

func TestWorkObservationPageRejectsForeignAndTamperedCursors(t *testing.T) {
	t.Parallel()
	s := observationFixture(t)
	base := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	seedObservationCorpus(t, s, "work-99", base)
	page, err := s.ReadWorkObservations(context.Background(), WorkObservationsRequest{WorkID: "work-99", Limit: 7})
	if err != nil || page.NextCursor == nil {
		t.Fatalf("first page err=%v cursor=%v", err, page.NextCursor)
	}
	valid := *page.NextCursor

	craft := func(mutate func(c *workCollectionCursor)) string {
		c := workCollectionCursor{Version: 1, WorkID: "work-99", Collection: workObservationsCollection, RecordedAt: base.Add(10 * time.Second).Format(time.RFC3339Nano), ID: "obs:000000000000000a"}
		if raw, err := base64.RawURLEncoding.DecodeString(valid); err == nil {
			_ = json.Unmarshal(raw, &c)
		}
		mutate(&c)
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	cases := map[string]string{
		"garbage":           "%%%not-a-cursor",
		"wrong work":        craft(func(c *workCollectionCursor) { c.WorkID = "work-other" }),
		"wrong collection":  craft(func(c *workCollectionCursor) { c.Collection = "messages" }),
		"unknown version":   craft(func(c *workCollectionCursor) { c.Version = 2 }),
		"invalid timestamp": craft(func(c *workCollectionCursor) { c.RecordedAt = "yesterday" }),
		"missing identity":  craft(func(c *workCollectionCursor) { c.ID = "" }),
	}
	for name, cursor := range cases {
		_, err := s.ReadWorkObservations(context.Background(), WorkObservationsRequest{WorkID: "work-99", Limit: 7, Cursor: cursor})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidCursor {
			t.Fatalf("%s: err=%v want typed %s", name, err, KindInvalidCursor)
		}
	}
}

func TestWorkObservationPageLimitBounds(t *testing.T) {
	t.Parallel()
	s := observationFixture(t)
	base := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	entries := seedObservationCorpus(t, s, "work-99", base)
	expected := expectedObservationOrder(entries)

	one, err := s.ReadWorkObservations(context.Background(), WorkObservationsRequest{WorkID: "work-99", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Observations) != 1 || one.Total != 40 || one.NextCursor == nil {
		t.Fatalf("limit 1: len=%d total=%d cursor=%v", len(one.Observations), one.Total, one.NextCursor)
	}
	if one.Observations[0].ObservationID != expected[0].ID {
		t.Fatalf("limit 1 returned %s want newest %s", one.Observations[0].ObservationID, expected[0].ID)
	}

	def, err := s.ReadWorkObservations(context.Background(), WorkObservationsRequest{WorkID: "work-99"})
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Observations) != 10 || def.NextCursor == nil {
		t.Fatalf("default limit: len=%d cursor=%v want 10 rows with cursor", len(def.Observations), def.NextCursor)
	}

	capped, err := s.ReadWorkObservations(context.Background(), WorkObservationsRequest{WorkID: "work-99", Limit: 65})
	if err != nil {
		t.Fatal(err)
	}
	if len(capped.Observations) != 40 || capped.Total != 40 || capped.NextCursor != nil {
		t.Fatalf("limit 65 clamped: len=%d cursor=%v want whole population without cursor", len(capped.Observations), capped.NextCursor)
	}
}

func TestWorkObservationPageLimitPagePrefixPreservesEnvelope(t *testing.T) {
	t.Parallel()
	s := observationFixture(t)
	base := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	seedObservationCorpus(t, s, "work-99", base)
	req := WorkObservationsRequest{WorkID: "work-99", Limit: 10}
	source, err := s.ReadWorkObservations(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Observations) != 10 || source.NextCursor == nil {
		t.Fatalf("source page len=%d cursor=%v", len(source.Observations), source.NextCursor)
	}

	limited, err := source.LimitPage(req, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Observations) != 4 {
		t.Fatalf("limited len=%d want 4", len(limited.Observations))
	}
	for i := range limited.Observations {
		if limited.Observations[i].ObservationID != source.Observations[i].ObservationID {
			t.Fatalf("limited row %d is %s want prefix row %s", i, limited.Observations[i].ObservationID, source.Observations[i].ObservationID)
		}
	}
	if limited.Total != source.Total {
		t.Fatalf("limited total=%d want %d", limited.Total, source.Total)
	}
	if limited.QueryID != source.QueryID || limited.ContractVersion != source.ContractVersion || limited.SourceVersionWatermark != source.SourceVersionWatermark {
		t.Fatalf("limited meta drifted: %+v vs %+v", limited.ResultMeta, source.ResultMeta)
	}
	if limited.ResolvedScope.WorkID != source.ResolvedScope.WorkID || !equalStringSlices(limited.OrderingKeys, source.OrderingKeys) {
		t.Fatalf("limited scope or ordering keys drifted")
	}
	if limited.NextCursor == nil {
		t.Fatal("truncating LimitPage must derive a continuation cursor")
	}

	// The derived cursor resumes at the first dropped source row.
	resumed, err := s.ReadWorkObservations(context.Background(), WorkObservationsRequest{WorkID: "work-99", Limit: 6, Cursor: *limited.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Observations) == 0 || resumed.Observations[0].ObservationID != source.Observations[4].ObservationID {
		t.Fatalf("resumed at %+v want %s", resumed.Observations, source.Observations[4].ObservationID)
	}

	// A prefix that already fits returns the page unchanged, source cursor
	// included.
	kept, err := source.LimitPage(req, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept.Observations) != 10 || kept.Total != source.Total || kept.NextCursor == nil || *kept.NextCursor != *source.NextCursor {
		t.Fatalf("non-truncating LimitPage changed the page: len=%d total=%d cursor=%v", len(kept.Observations), kept.Total, kept.NextCursor)
	}
}

func TestWorkObservationPageRejectsMalformedStoredLists(t *testing.T) {
	t.Parallel()
	s := observationFixture(t)
	base := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	entries := seedObservationCorpus(t, s, "work-99", base)
	newest := expectedObservationOrder(entries)[0]
	// The schema admits any JSON array, so store wrong-shape arrays: they
	// pass every column CHECK but cannot decode into the string lists the
	// fold writes. Each column is corrupted alone on the first row the page
	// reads, so one run proves the refs decoder and one the tags decoder.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_observations SET refs='[1,2]' WHERE observation_id=?; DELETE FROM fold_guard`, newest.ID); err != nil {
		t.Fatal(err)
	}
	_, err := s.ReadWorkObservations(context.Background(), WorkObservationsRequest{WorkID: "work-99", Limit: 10})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvariantViolation {
		t.Fatalf("paged read err=%v want typed %s", err, KindInvariantViolation)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE work_observations SET refs='["ref-ok"]', tags='[true]' WHERE observation_id=?; DELETE FROM fold_guard`, newest.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadWorkObservations(context.Background(), WorkObservationsRequest{WorkID: "work-99", Limit: 10}); !errors.As(err, &failure) || failure.Kind != KindInvariantViolation {
		t.Fatalf("paged read after tags corruption err=%v want typed %s", err, KindInvariantViolation)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
