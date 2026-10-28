package store

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func currentConflictApplications(t *testing.T, s *Store, workID string) []HistoryEntry {
	t.Helper()
	page, err := s.QueryQ7(t.Context(), Q7Request{Work: workID, RecordKinds: []string{"conflict_application"}, Direction: "oldest_first", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if page.NextCursor != nil {
		t.Fatal("fixture exceeds one settlement page")
	}
	return page.Entries
}

func TestConflictApplicationCurrentRecordReadable(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "settlement-readable")
	request := conflictApplicationRequest("settlement-readable", "settlement-readable-1", nil)
	ids, err := recordConflictApplication(t, s, request)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.QueryQ7(t.Context(), Q7Request{Work: request.WorkID})
	if err != nil {
		t.Fatal(err)
	}
	var settlements []HistoryEntry
	for _, entry := range page.Entries {
		if entry.EntryKind == "conflict_application" {
			settlements = append(settlements, entry)
		}
	}
	if len(settlements) != 1 {
		t.Fatalf("history exposes %d current settlements, want one recorded settlement", len(settlements))
	}
	entry := settlements[0]
	var want ConflictApplication
	if err := json.Unmarshal(mustJSONValue(liveConflictPayload(request, WorkSizeSmall, 1)), &want); err != nil {
		t.Fatal(err)
	}
	if entry.ConflictApplication == nil || *entry.ConflictApplication != want {
		t.Fatalf("history payload = %+v, want all recorded fields %+v", entry.ConflictApplication, want)
	}
	var seq int64
	if err := s.db.QueryRow(`SELECT effect_seq FROM effect_receipts WHERE event_id=?`, ids[0]).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if entry.EntryID != "conflict_application:"+ids[0] || entry.Position != (HistoryPosition{EffectSeq: seq, Basis: "effect"}) || entry.Actor != request.Actor || entry.Version != 2 || entry.VersionWorkID != request.WorkID || len(entry.EvidenceRefs) != 0 {
		t.Fatalf("history lost receipt identity or recorded metadata: %+v", entry)
	}
	if selected := currentConflictApplications(t, s, request.WorkID); !reflect.DeepEqual(selected, settlements) {
		t.Fatalf("selected and default history disagree: %+v vs %+v", selected, settlements)
	}
	if readWorkVersion(t, s, request.WorkID) != 2 {
		t.Fatal("settlement advanced work version")
	}
	var observations int
	if err := s.db.QueryRow(`SELECT count(*) FROM work_observations WHERE work_id=?`, request.WorkID).Scan(&observations); err != nil || observations != 0 {
		t.Fatalf("settlement became an observation: count=%d err=%v", observations, err)
	}
}

func TestConflictApplicationCurrentRecordRetriesAndRollback(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "settlement-retry")
	request := conflictApplicationRequest("settlement-retry", "settlement-retry-1", nil)
	first, err := recordConflictApplication(t, s, request)
	if err != nil {
		t.Fatal(err)
	}
	want := currentConflictApplications(t, s, request.WorkID)
	again, err := recordConflictApplication(t, s, request)
	if err != nil || !slices.Equal(first, again) {
		t.Fatalf("retry identity = %v, %v; want %v", again, err, first)
	}
	divergent := request
	divergent.Outcome = "different outcome"
	_, err = recordConflictApplication(t, s, divergent)
	assertFailureKind(t, err, KindIdempotencyConflict)
	if got := currentConflictApplications(t, s, request.WorkID); !reflect.DeepEqual(got, want) {
		t.Fatalf("retry or divergence changed evidence: %+v, want %+v", got, want)
	}
	seedSizedWork(t, s, "settlement-rollback")
	request = conflictApplicationRequest("settlement-rollback", "rollback-first", nil)
	err = s.Transact(t.Context(), func(tx *Transaction) error {
		if _, err := RecordConflictApplicationTx(t.Context(), tx, request); err != nil {
			return err
		}
		invalid := request
		invalid.SettlementRef = "rollback-second"
		invalid.Outcome = " "
		_, err := RecordConflictApplicationTx(t.Context(), tx, invalid)
		return err
	})
	assertFailureKind(t, err, KindInvalidPayload)
	for _, change := range []func(*ConflictApplicationRequest){
		func(r *ConflictApplicationRequest) { r.RuleID = "unknown" },
		func(r *ConflictApplicationRequest) { r.SettlementRef = "" },
	} {
		invalid := request
		change(&invalid)
		_, err := recordConflictApplication(t, s, invalid)
		assertFailureKind(t, err, KindInvalidPayload)
	}
	if got := currentConflictApplications(t, s, request.WorkID); len(got) != 0 {
		t.Fatalf("refused transaction retained settlements: %+v", got)
	}
	if countConflictEvents(t, s) != 1 || readWorkVersion(t, s, request.WorkID) != 2 {
		t.Fatal("refusal or rollback retained an effect")
	}
	assertFoldGuardEmpty(t, s)
}

func TestConflictApplicationCurrentRecordSurvivesSizeAndReopen(t *testing.T) {
	path := copyTestDatabase(t)
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	seedSizedWork(t, s, "settlement-reopen")
	request := conflictApplicationRequest("settlement-reopen", "settlement-before", nil)
	if _, err := recordConflictApplication(t, s, request); err != nil {
		t.Fatal(err)
	}
	want := currentConflictApplications(t, s, request.WorkID)
	later := workSizeInputsFixture()
	later.Declared.EstimatedProductionLines = 61
	if err := applyWorkEvent(t, s, workSizedEvent("settlement-later-size", request.WorkID, WorkSizeModelVersion, WorkSizeLarger, later), nil); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, request.WorkID)
	if err := applyWorkEvent(t, s, workTransitionEvent("settlement-later-transition", request.WorkID, "needed", "in_progress", version, version+1), workVersion(request.WorkID, version)); err != nil {
		t.Fatal(err)
	}
	if got := currentConflictApplications(t, s, request.WorkID); !reflect.DeepEqual(got, want) {
		t.Fatalf("later size/version rewrote settlement: %+v, want %+v", got, want)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got := currentConflictApplications(t, s, request.WorkID); !reflect.DeepEqual(got, want) {
		t.Fatalf("reopen lost settlement: %+v, want %+v", got, want)
	}
	request.SettlementRef = "settlement-after"
	if _, err := recordConflictApplication(t, s, request); err != nil {
		t.Fatal(err)
	}
	got := currentConflictApplications(t, s, request.WorkID)
	if len(got) != 2 || !reflect.DeepEqual(got[0], want[0]) || got[1].ConflictApplication.Size != WorkSizeLarger || got[1].ConflictApplication.SizeAssessmentSequence != 2 || got[1].Version != version+1 {
		t.Fatalf("new settlement did not preserve the two applicability snapshots: %+v", got)
	}
}

func TestConflictApplicationCurrentRecordPagingAndCursor(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "settlement-pages")
	seedSizedWork(t, s, "settlement-other")
	for _, ref := range []string{"first", "second", "third"} {
		if _, err := recordConflictApplication(t, s, conflictApplicationRequest("settlement-pages", ref, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := recordConflictApplication(t, s, conflictApplicationRequest("settlement-other", "foreign", nil)); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"oldest_first", "newest_first"} {
		for _, kinds := range [][]string{nil, {"conflict_application"}} {
			req := Q7Request{Work: "settlement-pages", Direction: direction, RecordKinds: kinds, Limit: 100}
			full, err := s.QueryQ7(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			req.Limit = 1
			var entries []HistoryEntry
			for pageNumber := 0; ; pageNumber++ {
				if pageNumber > 4 {
					t.Fatal("pagination did not terminate")
				}
				page, err := s.QueryQ7(t.Context(), req)
				if err != nil || len(page.Entries) != 1 {
					t.Fatalf("page = %+v, %v", page, err)
				}
				entries = append(entries, page.Entries...)
				if page.NextCursor == nil {
					break
				}
				req.Cursor = *page.NextCursor
			}
			if !reflect.DeepEqual(entries, full.Entries) || len(entries) < 3 {
				t.Fatalf("paged %s %v = %+v, want %+v", direction, kinds, entries, full.Entries)
			}
			for _, entry := range entries {
				if entry.VersionWorkID != req.Work {
					t.Fatalf("another work leaked into history: %+v", entry)
				}
			}
		}
	}
	req := Q7Request{Work: "settlement-pages", RecordKinds: []string{"conflict_application"}, Limit: 1}
	page, err := s.QueryQ7(t.Context(), req)
	if err != nil || page.NextCursor == nil {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	req.Cursor = *page.NextCursor
	for _, change := range []func(*Q7Request){
		func(r *Q7Request) { r.Work = "settlement-other" },
		func(r *Q7Request) { r.RecordKinds = []string{"lifecycle"} },
		func(r *Q7Request) { r.Direction = "oldest_first" },
	} {
		changed := req
		change(&changed)
		_, err := s.QueryQ7(t.Context(), changed)
		assertFailureKind(t, err, KindInvalidCursor)
	}
	if _, err := recordConflictApplication(t, s, conflictApplicationRequest(req.Work, "fourth", nil)); err != nil {
		t.Fatal(err)
	}
	_, err = s.QueryQ7(t.Context(), req)
	assertFailureKind(t, err, KindInvalidCursor)
}

func TestConflictApplicationCurrentRecordMigrationDoesNotRestoreReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settlement-prefix.db")
	db := openMigratedTo(t, path, 161)
	s := &Store{db: db, path: path}
	seedSizedWork(t, s, "settlement-legacy")
	request := conflictApplicationRequest("settlement-legacy", "legacy", nil)
	event := conflictAppliedEvent(conflictApplicationEventID(request, 1), request.WorkID, liveConflictPayload(request, WorkSizeSmall, 1))
	event.Actor, event.OccurredAt = request.Actor, request.OccurredAt
	if err := applyWorkEvent(t, s, event, nil); err != nil {
		t.Fatal(err)
	}
	digest := conflictReceiptDigest(t, s, event.EventID)
	assertFailureKind(t, migrateAtOpen(t.Context(), db), KindUpgradeRequired)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	// Exact retries retain receipt identity, but never manufacture evidence
	// for a digest-only settlement accepted by a preceding binary.
	ids, err := recordConflictApplication(t, s, request)
	if err != nil || !slices.Equal(ids, []string{event.EventID}) {
		t.Fatalf("old receipt retry = %v, %v", ids, err)
	}
	if got := currentConflictApplications(t, s, request.WorkID); len(got) != 0 {
		t.Fatalf("migration/retry reconstructed old receipt: %+v", got)
	}
	if conflictReceiptDigest(t, s, event.EventID) != digest {
		t.Fatal("migration changed old receipt")
	}
	request.SettlementRef = "new-after-upgrade"
	if _, err := recordConflictApplication(t, s, request); err != nil {
		t.Fatal(err)
	}
	if got := currentConflictApplications(t, s, request.WorkID); len(got) != 1 || got[0].ConflictApplication.SettlementRef != request.SettlementRef {
		t.Fatalf("new settlement after upgrade = %+v", got)
	}
}

func TestConflictApplicationCurrentRecordCannotBeFabricatedByGenericAppend(t *testing.T) {
	s := openTemp(t)
	seedSizedWork(t, s, "settlement-generic")
	request := conflictApplicationRequest("settlement-generic", "generic", nil)
	for _, payload := range []map[string]any{nil, liveConflictPayload(request, WorkSizeSmall, 1)} {
		event := conflictAppliedEvent("generic-"+request.SettlementRef, request.WorkID, payload)
		if err := applyWorkEvent(t, s, event, nil); err != nil {
			t.Fatal(err)
		}
		request.SettlementRef += "-next"
	}
	if got := currentConflictApplications(t, s, request.WorkID); len(got) != 0 {
		t.Fatalf("generic append fabricated readable live-bound evidence: %+v", got)
	}
}

func TestConflictApplicationCurrentRecordGuardsAndCleanup(t *testing.T) {
	s, _ := terminalCleanupFixtureForKind(t, "task", func(s *Store) {
		if err := applyWorkEvent(t, s, workSizedEvent("cleanup-settlement-size", "work-done", WorkSizeModelVersion, WorkSizeSmall, workSizeInputsFixture()), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := recordConflictApplication(t, s, conflictApplicationRequest("work-done", "cleanup", nil)); err != nil {
			t.Fatal(err)
		}
	})
	if err := ApplyOperation(t.Context(), s, Operation{Events: []Event{
		workCreatedEvent("settlement-survivor", "create-settlement-survivor"),
		operationEvent("membership-settlement-survivor", "work_project.added", SubjectWorkItem, "settlement-survivor", map[string]any{
			"work_id": "settlement-survivor", "project_id": "proj-web", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
		}),
		workSizedEvent("size-settlement-survivor", "settlement-survivor", WorkSizeModelVersion, WorkSizeSmall, workSizeInputsFixture()),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordConflictApplication(t, s, conflictApplicationRequest("settlement-survivor", "survivor", nil)); err != nil {
		t.Fatal(err)
	}
	want := currentConflictApplications(t, s, "settlement-survivor")
	for _, query := range []string{
		`INSERT INTO work_conflict_applications SELECT * FROM work_conflict_applications WHERE work_id='work-done'`,
		`UPDATE work_conflict_applications SET payload='{}' WHERE work_id='work-done'`,
		`DELETE FROM work_conflict_applications WHERE work_id='work-done'`,
	} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatalf("unguarded mutation succeeded: %s", query)
		}
	}
	if got := currentConflictApplications(t, s, "work-done"); len(got) != 1 {
		t.Fatalf("guard tests lost settlement: %+v", got)
	}
	if err := s.CleanupPublishedTerminalWork(t.Context(), "work-done"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM work_conflict_applications WHERE work_id='work-done'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cleanup retained settlement: count=%d err=%v", count, err)
	}
	if got := currentConflictApplications(t, s, "settlement-survivor"); !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup changed another work's evidence: %+v, want %+v", got, want)
	}
}
