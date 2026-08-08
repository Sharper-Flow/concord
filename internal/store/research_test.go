package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func researchIdentity(key string) ResearchMutationIdentity {
	return ResearchMutationIdentity{PrincipalRef: "operator", Tool: "research-test", OperationKind: "test", IdempotencyKey: key}
}

func seedResearchWork(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	events := []Event{
		{EventID: "research-product", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product", Actor: "test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "research-project", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project", Actor: "test", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Project"}`)},
		{EventID: "research-product-project", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product", Actor: "test", OccurredAt: time.Unix(1, 2).UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"product","project_id":"project","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
	}
	for i, id := range ids {
		events = append(events,
			Event{EventID: "research-work-" + id, Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "test", OccurredAt: time.Unix(2, int64(i)).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "research", "title": id, "priority": 1})},
			Event{EventID: "research-work-project-" + id, Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "test", OccurredAt: time.Unix(3, int64(i)).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": id, "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		)
	}
	expected := map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 0, VersionRef(SubjectProject, "project"): 0}
	for _, id := range ids {
		expected[VersionRef(SubjectWorkItem, id)] = 0
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: expected}); err != nil {
		t.Fatal(err)
	}
}

func mustJSONBytes(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func TestActiveResearchRevisionAndIdempotencyBoundary(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	create := CreateResearchPackRequest{Identity: researchIdentity("create"), OwnerWorkID: "owner", Revision: ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`{"in":true}`), ScopeOut: json.RawMessage(`{"out":false}`), DoneWhen: json.RawMessage(`{"done":true}`), Method: "docs"}}
	pack, err := CreateResearchPack(ctx, s, create)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := CreateResearchPack(ctx, s, create)
	if err != nil || replayed.PackID != pack.PackID {
		t.Fatalf("idempotent create = %+v, %v", replayed, err)
	}
	conflict := create
	conflict.Revision.Question = "different"
	if _, err := CreateResearchPack(ctx, s, conflict); err == nil {
		t.Fatal("same key/different request succeeded")
	} else {
		assertFailureKind(t, err, KindIdempotencyConflict)
	}
	rev, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("append"), PackID: pack.PackID, ExpectedVersion: 1, Revision: ResearchRevisionInput{Question: "q2", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "source"}})
	if err != nil || rev.Revision != 2 {
		t.Fatalf("append = %+v, %v", rev, err)
	}
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("stale"), PackID: pack.PackID, ExpectedVersion: 1, Revision: ResearchRevisionInput{Question: "q3", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "source"}}); err == nil {
		t.Fatal("stale append succeeded")
	} else {
		assertFailureKind(t, err, KindVersionConflict)
	}
	beforeEvents := countRows(t, s, "domain_events")
	if _, err := AddResearchFinding(ctx, s, ResearchFindingRequest{Identity: researchIdentity("finding"), PackID: pack.PackID, ExpectedVersion: 2, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddResearchSource(ctx, s, ResearchSourceRequest{Identity: researchIdentity("source"), PackID: pack.PackID, ExpectedVersion: 3, Source: ResearchSource{SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com", Title: "Example", PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if err := BindResearchFindingSource(ctx, s, ResearchFindingSourceRequest{Identity: researchIdentity("finding-source"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 4, FindingID: "f1", SourceID: "s1"}); err != nil {
		t.Fatal(err)
	}
	complete, err := ReadCompleteResearchPack(ctx, s, pack.PackID)
	if err != nil || len(complete.Revisions) != 2 || len(complete.Revisions[1].Sources) != 1 || len(complete.Revisions[1].Findings) != 1 || len(complete.Revisions[1].Findings[0].SourceIDs) != 1 {
		t.Fatalf("complete research pack = %+v, %v", complete, err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	retained, err := ReadCompleteResearchPack(ctx, s, pack.PackID)
	if err != nil || len(retained.Revisions) != 2 {
		t.Fatalf("research pack was not preserved across projection rebuild: %+v, %v", retained, err)
	}
	if got := countRows(t, s, "domain_events"); got != beforeEvents {
		t.Fatalf("research content entered domain_events: %d -> %d", beforeEvents, got)
	}
	consumer, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("bind"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 5, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: true}})
	if err != nil {
		t.Fatal(err)
	}
	if consumer.Revision != 2 {
		t.Fatalf("consumer revision = %d", consumer.Revision)
	}
	if _, err := UpdateResearchFinding(ctx, s, ResearchFindingRequest{Identity: researchIdentity("consumed-update"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 6, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "updated", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err == nil {
		t.Fatal("consumed revision update succeeded")
	} else {
		assertFailureKind(t, err, KindResearchRevisionImmutable)
	}
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("append-after-consume"), PackID: pack.PackID, ExpectedVersion: 6, Revision: ResearchRevisionInput{Question: "q3", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "source"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := AddResearchFinding(ctx, s, ResearchFindingRequest{Identity: researchIdentity("new-current-finding"), PackID: pack.PackID, ExpectedVersion: 7, Finding: ResearchFinding{FindingID: "f3", Kind: FindingConclusion, Statement: "new current", Confidence: ConfidenceMedium, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RequiredResearchFreshness(ctx, pack.PackID, "consumer"); err != nil || got != ResearchCurrent {
		t.Fatalf("required current freshness = %q, %v", got, err)
	}
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("stale"), PackID: pack.PackID, ExpectedVersion: 8, Freshness: ResearchStale}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RequiredResearchFreshness(ctx, pack.PackID, "consumer"); err != nil || got != ResearchStale {
		t.Fatalf("required stale freshness = %q, %v", got, err)
	}
	freshness, err := ResearchFreshnessForPack(ctx, s, pack.PackID)
	if err != nil || freshness.Status != ResearchStale || !freshness.Blocked {
		t.Fatalf("freshness = %+v, %v", freshness, err)
	}
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("unknown"), PackID: pack.PackID, ExpectedVersion: 9, Freshness: ResearchUnknown}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RequiredResearchFreshness(ctx, pack.PackID, "consumer"); err != nil || got != ResearchUnknown {
		t.Fatalf("required unknown freshness = %q, %v", got, err)
	}
	if err := DeleteResearchPack(ctx, s, ResearchPackMutationRequest{Identity: researchIdentity("delete-blocked"), PackID: pack.PackID, ExpectedVersion: 10}); err == nil {
		t.Fatal("delete with required active consumer succeeded")
	} else {
		assertFailureKind(t, err, KindResearchConsumerBlocked)
	}
}

func TestActiveResearchNonrequiredConsumerDoesNotBlock(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "optional")
	pack, err := CreateResearchPack(ctx, s, CreateResearchPackRequest{Identity: researchIdentity("create-nonrequired"), OwnerWorkID: "owner", Revision: ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("bind-optional"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "optional", UseRole: UseContext, Required: false}}); err != nil {
		t.Fatal(err)
	}
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("unknown-optional"), PackID: pack.PackID, ExpectedVersion: 2, Freshness: ResearchUnknown}); err != nil {
		t.Fatal(err)
	}
	got, err := ResearchFreshnessForPack(ctx, s, pack.PackID)
	if err != nil || got.Blocked {
		t.Fatalf("optional freshness = %+v, %v", got, err)
	}
	if err := DeleteResearchPack(ctx, s, ResearchPackMutationRequest{Identity: researchIdentity("delete-optional"), PackID: pack.PackID, ExpectedVersion: 3}); err != nil {
		t.Fatal(err)
	}
}

func TestResearchFreshnessReturnsOnlyFirstFailingConsumer(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer-c", "consumer-a", "consumer-b")
	pack := createSimplePack(t, s, "bounded-freshness", "owner")
	for i, consumerID := range []string{"consumer-c", "consumer-a", "consumer-b"} {
		if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("bounded-freshness-bind-" + consumerID), PackID: pack.PackID, Revision: 1, ExpectedVersion: int64(1 + i), Consumer: ResearchConsumer{ConsumerWorkID: consumerID, UseRole: UseContext, Required: true}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("bounded-freshness-stale"), PackID: pack.PackID, ExpectedVersion: 4, Freshness: ResearchStale}); err != nil {
		t.Fatal(err)
	}
	result, err := ResearchFreshnessForPack(ctx, s, pack.PackID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Blocked || result.Status != ResearchStale || len(result.Reasons) != 1 || result.Reasons[0] != "consumer-a:stale" {
		t.Fatalf("bounded freshness result=%+v", result)
	}
}

func simpleResearchRevision() ResearchRevisionInput {
	return ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "docs"}
}

func createSimplePack(t *testing.T, s *Store, key, owner string) ResearchPack {
	t.Helper()
	pack, err := CreateResearchPack(context.Background(), s, CreateResearchPackRequest{Identity: researchIdentity(key), PackID: key + "-pack", OwnerWorkID: owner, Revision: simpleResearchRevision()})
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

func terminalizeResearchOwner(t *testing.T, s *Store, id string) {
	t.Helper()
	event, _ := operationEventForResearch("terminal-"+id, "work.transitioned", SubjectWorkItem, id, map[string]any{"from": "needed", "to": "completed", "reason": "archive", "expected_version": 2, "resulting_version": 3})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 2}}); err != nil {
		t.Fatal(err)
	}
}

func linkArchivedResearchOwner(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.DB().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES(?, 'work_note', ?, '2026-08-07T00:00:00Z', 'completed', '[]', 'completed', 1, 'durable summary', 'home', 'locator', 'notes/`+id+`.md', 'commit', 'hash'); DELETE FROM fold_guard`, id, id); err != nil {
		t.Fatal(err)
	}
}

func TestActiveResearchPersistsAcrossCloseAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedResearchWork(t, s, "owner")
	createSimplePack(t, s, "persist", "owner")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	pack, err := ReadCompleteResearchPack(ctx, reopened, "persist-pack")
	if err != nil || len(pack.Revisions) != 1 || pack.OwnerWorkID != "owner" {
		t.Fatalf("reopened pack=%+v err=%v", pack, err)
	}
}

func TestResearchConsumersPinDifferentRevisions(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer-a", "consumer-b")
	pack := createSimplePack(t, s, "pins", "owner")
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("pins-revision"), PackID: pack.PackID, ExpectedVersion: 1, Revision: simpleResearchRevision()}); err != nil {
		t.Fatal(err)
	}
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("pin-a"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 2, Consumer: ResearchConsumer{ConsumerWorkID: "consumer-a", UseRole: UseContext, Required: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("pin-b"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 3, Consumer: ResearchConsumer{ConsumerWorkID: "consumer-b", UseRole: UseDesignInput, Required: true}}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadCompleteResearchPack(ctx, s, pack.PackID)
	if err != nil || len(got.Consumers) != 2 || got.Consumers[0].Revision == got.Consumers[1].Revision {
		t.Fatalf("pinned consumers=%+v err=%v", got.Consumers, err)
	}
}

func TestResearchPruneKeepsCurrentAndConsumedRevisions(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "prune", "owner")
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("prune-r2"), PackID: pack.PackID, ExpectedVersion: 1, Revision: simpleResearchRevision()}); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("prune-r3"), PackID: pack.PackID, ExpectedVersion: 2, Revision: simpleResearchRevision()}); err != nil {
		t.Fatal(err)
	}
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("prune-pin"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 3, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: false}}); err != nil {
		t.Fatal(err)
	}
	if count, err := PruneResearchRevisions(ctx, s, ResearchPackMutationRequest{Identity: researchIdentity("prune-operation"), PackID: pack.PackID, ExpectedVersion: 4}); err != nil || count != 1 {
		t.Fatalf("prune count=%d err=%v", count, err)
	}
	got, err := ReadCompleteResearchPack(ctx, s, pack.PackID)
	if err != nil || len(got.Revisions) != 2 || got.Revisions[0].Revision != 1 || got.Revisions[1].Revision != 3 {
		t.Fatalf("pruned revisions=%+v err=%v", got.Revisions, err)
	}
}

func TestTerminalResearchCleanupRefusesRequiredCompaction(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "blocked-compaction", "owner")
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("blocked-bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: true}}); err != nil {
		t.Fatal(err)
	}
	terminalizeResearchOwner(t, s, "owner")
	linkArchivedResearchOwner(t, s, "owner")
	if err := cleanupTerminalResearch(ctx, s, "owner"); err == nil {
		t.Fatal("compaction cleanup succeeded with required consumer")
	} else {
		assertFailureKind(t, err, KindResearchConsumerBlocked)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("blocked pack count=%d err=%v", count, err)
	}
}

func TestTerminalResearchCleanupDeletesUnblockedPack(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "successful-compaction", "owner")
	terminalizeResearchOwner(t, s, "owner")
	linkArchivedResearchOwner(t, s, "owner")
	if err := cleanupTerminalResearch(ctx, s, "owner"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted pack count=%d err=%v", count, err)
	}
}

func TestInterruptedTerminalCleanupFinishesAtNextPackMutation(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "interrupted-compaction", "owner")
	terminalizeResearchOwner(t, s, "owner")
	linkArchivedResearchOwner(t, s, "owner")
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("resume-cleanup"), PackID: pack.PackID, ExpectedVersion: 1, Freshness: ResearchStale}); err == nil {
		t.Fatal("mutation unexpectedly wrote a terminal-owner pack")
	}
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reconciled pack count=%d err=%v", count, err)
	}
}

func TestTerminalUnlinkedPackRemainsReadable(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "other")
	pack := createSimplePack(t, s, "unlinked-terminal", "owner")
	terminalizeResearchOwner(t, s, "owner")
	if got, err := ReadCompleteResearchPack(ctx, s, pack.PackID); err != nil || got.PackID != pack.PackID {
		t.Fatalf("unlinked terminal pack=%+v err=%v", got, err)
	}
	other := createSimplePack(t, s, "unrelated-active", "other")
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("unrelated-write"), PackID: other.PackID, ExpectedVersion: 1, Freshness: ResearchStale}); err != nil {
		t.Fatal(err)
	}
}

func TestBlockedLinkedOwnerDoesNotBlockUnrelatedPackMutation(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner-a", "consumer-a", "owner-b")
	packA := createSimplePack(t, s, "blocked-owner", "owner-a")
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("blocked-owner-bind"), PackID: packA.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer-a", UseRole: UseContext, Required: true}}); err != nil {
		t.Fatal(err)
	}
	terminalizeResearchOwner(t, s, "owner-a")
	linkArchivedResearchOwner(t, s, "owner-a")
	packB := createSimplePack(t, s, "unrelated-owner", "owner-b")
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("unrelated-owner-write"), PackID: packB.PackID, ExpectedVersion: 1, Freshness: ResearchStale}); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalConsumerTransitionRemovesBindingAndAdvancesPack(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		to   string
	}{
		{name: "completed", kind: "work.transitioned", to: "completed"},
		{name: "cancelled", kind: "work.transitioned", to: "cancelled"},
		{name: "superseded", kind: "work.superseded", to: "superseded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTemp(t)
			seedResearchWork(t, s, "owner", "consumer", "successor")
			packA := createSimplePack(t, s, "terminal-consumer-a-"+tc.name, "owner")
			packB := createSimplePack(t, s, "terminal-consumer-b-"+tc.name, "owner")
			for i, pack := range []ResearchPack{packA, packB} {
				if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity(tc.name + "-terminal-bind-" + string(rune('a'+i))), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: true}}); err != nil {
					t.Fatal(err)
				}
			}
			var event Event
			var err error
			if tc.kind == "work.superseded" {
				event, err = operationEventForResearch(tc.name+"-consumer-terminal", tc.kind, SubjectWorkItem, "consumer", map[string]any{"successor": "successor", "superseded": "consumer", "reason": "done", "expected_version": 2, "resulting_version": 3})
			} else {
				event, err = operationEventForResearch(tc.name+"-consumer-terminal", tc.kind, SubjectWorkItem, "consumer", map[string]any{"from": "needed", "to": tc.to, "reason": "done", "expected_version": 2, "resulting_version": 3})
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "consumer"): 2}}); err != nil {
				t.Fatal(err)
			}
			var bindings int
			if err := s.DB().QueryRow(`SELECT count(*) FROM active_research_consumers WHERE consumer_work_id=?`, "consumer").Scan(&bindings); err != nil {
				t.Fatal(err)
			}
			if bindings != 0 {
				t.Fatalf("terminal consumer bindings=%d", bindings)
			}
			for _, pack := range []ResearchPack{packA, packB} {
				var version int
				if err := s.DB().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				if version != 3 {
					t.Fatalf("terminal consumer pack %s version=%d, want one bump from binding", pack.PackID, version)
				}
			}
		})
	}
}

func compactionFixture(t *testing.T, required bool) (*Store, KnowledgeHome, ResearchPack, string, string) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	path := "docs/work/owner.md"
	writeKnowledgeFile(t, repo, path, canonicalWorkNote("owner", "2026-08-07T00:00:00Z"))
	commit := commitKnowledgeRepo(t, repo, "owner proof")
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "compaction-fixture", "owner")
	if required {
		if _, err := BindResearchConsumer(context.Background(), s, BindResearchConsumerRequest{Identity: researchIdentity("compaction-bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: true}}); err != nil {
			t.Fatal(err)
		}
	}
	terminalizeResearchOwner(t, s, "owner")
	home := KnowledgeHome{HomeProjectID: "home", HomeLocatorID: "owner-repo", RepoPath: repo, HeadRef: "HEAD"}
	return s, home, pack, commit, path
}

func compactionRequest(home KnowledgeHome, commit, path, eventID string, expected int64) CompactionLinkRequest {
	return CompactionLinkRequest{EventID: eventID, WorkID: "owner", ExpectedVersion: expected, Actor: "test", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), Home: home, CommitOID: commit, NotePath: path, Reason: "proof-backed archive"}
}

func TestPublishCompactionLinkPreflightsRequiredResearchConsumer(t *testing.T) {
	s, home, pack, commit, path := compactionFixture(t, true)
	beforeEvents := countRows(t, s, "domain_events")
	if err := PublishCompactionLink(context.Background(), s, compactionRequest(home, commit, path, "blocked-compaction", 3)); err == nil {
		t.Fatal("compaction succeeded with required active consumer")
	} else {
		assertFailureKind(t, err, KindResearchConsumerBlocked)
	}
	var archived, events, active int
	if err := s.DB().QueryRow(`SELECT count(*) FROM archived_work WHERE id='owner'`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='compaction_link.published'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if archived != 0 || events != 0 || active != 1 || countRows(t, s, "domain_events") != beforeEvents {
		t.Fatalf("blocked compaction mutated archived/events/pack=%d/%d/%d", archived, events, active)
	}
}

func TestCompactionFoldRejectsRequiredConsumerAtomically(t *testing.T) {
	ctx := context.Background()
	s, home, _, commit, path := compactionFixture(t, true)
	note, err := VerifyCommittedNote(ctx, home.RepoPath, commit, path, "")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(compactionLinkPayload{ID: "owner", Type: "work_note", Title: note.Title, CompletedAt: note.CompletedAt, OutcomeTag: note.OutcomeTag, LessonTags: note.LessonTags, TerminalState: note.TerminalState, Priority: note.Priority, Summary: note.Summary, ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs, ComponentIDs: note.ComponentIDs, TagIDs: note.TagIDs, HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID, NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash, Reason: "direct fold test", ExpectedVersion: 3, ResultingVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	beforeEvents := countRows(t, s, "domain_events")
	event := Event{EventID: "direct-blocked-compaction", Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: "owner", Actor: "test", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: payload}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "owner"): 3}}); err == nil {
		t.Fatal("direct compaction fold succeeded with required active consumer")
	} else {
		assertFailureKind(t, err, KindResearchConsumerBlocked)
	}
	if countRows(t, s, "domain_events") != beforeEvents || countRows(t, s, "archived_work") != 0 || countRows(t, s, "active_research_packs") != 1 {
		t.Fatalf("direct compaction fold committed event/archive/pack rows: events=%d archive=%d packs=%d", countRows(t, s, "domain_events"), countRows(t, s, "archived_work"), countRows(t, s, "active_research_packs"))
	}
}

func TestProofBackedCompactionDeletesResearchAndNeverStoresBody(t *testing.T) {
	ctx := context.Background()
	s, home, pack, commit, path := compactionFixture(t, true)
	secret := "SECRET-RESEARCH-PACK-BODY"
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("secret-revision"), PackID: pack.PackID, ExpectedVersion: 2, Revision: ResearchRevisionInput{Question: secret, ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "test"}}); err != nil {
		t.Fatal(err)
	}
	consumerDone, _ := operationEventForResearch("consumer-terminal", "work.transitioned", SubjectWorkItem, "consumer", map[string]any{"from": "needed", "to": "completed", "reason": "done", "expected_version": 2, "resulting_version": 3})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{consumerDone}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "consumer"): 2}}); err != nil {
		t.Fatal(err)
	}
	if err := PublishCompactionLink(ctx, s, compactionRequest(home, commit, path, "successful-compaction", 3)); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"active_research_packs", "active_research_revisions", "active_research_findings", "active_research_sources", "active_research_finding_sources", "active_research_consumers"} {
		if countRows(t, s, table) != 0 {
			t.Fatalf("%s retained rows after proof-backed compaction", table)
		}
	}
	var bodyEvents, bodySummary int
	if err := s.DB().QueryRow(`SELECT count(*) FROM domain_events WHERE payload LIKE ?`, "%"+secret+"%").Scan(&bodyEvents); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM archived_work WHERE summary LIKE ?`, "%"+secret+"%").Scan(&bodySummary); err != nil {
		t.Fatal(err)
	}
	if bodyEvents != 0 || bodySummary != 0 {
		t.Fatalf("deleted pack body leaked to events/summary=%d/%d", bodyEvents, bodySummary)
	}
	note, err := VerifyCommittedNote(ctx, home.RepoPath, commit, path, "")
	if err != nil || strings.Contains(string(note.Content), secret) {
		t.Fatalf("Git authority contains deleted pack body: err=%v", err)
	}
}

func TestCompactionRetryReconcilesCrashWindow(t *testing.T) {
	ctx := context.Background()
	s, home, pack, commit, path := compactionFixture(t, false)
	note, err := VerifyCommittedNote(ctx, home.RepoPath, commit, path, "")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(compactionLinkPayload{ID: "owner", Type: "work_note", Title: note.Title, CompletedAt: note.CompletedAt, OutcomeTag: note.OutcomeTag, LessonTags: note.LessonTags, TerminalState: note.TerminalState, Priority: note.Priority, Summary: note.Summary, ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs, ComponentIDs: note.ComponentIDs, TagIDs: note.TagIDs, HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID, NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash, Reason: "proof-backed archive", ExpectedVersion: 3, ResultingVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "crash-window-link", Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: "owner", Actor: "test", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: payload}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "owner"): 3}}); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "active_research_packs") != 1 {
		t.Fatal("crash-window setup did not retain pack before cleanup")
	}
	if err := PublishCompactionLink(ctx, s, compactionRequest(home, commit, path, "crash-window-link", 3)); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "active_research_packs") != 0 || countRows(t, s, "active_research_revisions") != 0 || pack.PackID == "" {
		t.Fatal("idempotent compaction retry did not finish cleanup")
	}
}

func TestArchiveFailureBeforeGitProofLeavesPackIntact(t *testing.T) {
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "proof-failure", "owner")
	terminalizeResearchOwner(t, s, "owner")
	home := KnowledgeHome{HomeProjectID: "home", HomeLocatorID: "missing", RepoPath: t.TempDir(), HeadRef: "HEAD"}
	if err := PublishCompactionLink(context.Background(), s, compactionRequest(home, strings.Repeat("a", 40), "docs/work/missing.md", "proof-failure-link", 3)); err == nil {
		t.Fatal("compaction without Git proof succeeded")
	}
	if countRows(t, s, "active_research_packs") != 1 || countRows(t, s, "archived_work") != 0 {
		t.Fatal("archive proof failure changed active research or archive projection")
	}
	_ = pack
}

func TestResearchFindingSourceReadRejectsGlobalOverflow(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "finding-source-overflow", "owner")
	for i, findingID := range []string{"f1", "f2"} {
		if _, err := AddResearchFinding(ctx, s, ResearchFindingRequest{Identity: researchIdentity("overflow-finding-" + findingID), PackID: pack.PackID, ExpectedVersion: int64(1 + i), Finding: ResearchFinding{FindingID: findingID, Kind: FindingObservation, Statement: findingID, Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, sourceID := range []string{"s1", "s2"} {
		if _, err := AddResearchSource(ctx, s, ResearchSourceRequest{Identity: researchIdentity("overflow-source-" + sourceID), PackID: pack.PackID, ExpectedVersion: int64(3 + i), Source: ResearchSource{SourceID: sourceID, Kind: SourceOfficialDoc, Locator: "https://example.com/" + sourceID, Title: sourceID, PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, link := range []struct{ finding, source string }{{"f1", "s1"}, {"f1", "s2"}, {"f2", "s1"}} {
		if err := BindResearchFindingSource(ctx, s, ResearchFindingSourceRequest{Identity: researchIdentity("overflow-link-" + string(rune('a'+i))), PackID: pack.PackID, Revision: 1, ExpectedVersion: int64(5 + i), FindingID: link.finding, SourceID: link.source}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadResearchPack(ctx, s, pack.PackID, 2); err == nil {
		t.Fatal("bounded research read silently truncated finding-source links")
	} else {
		assertFailureKind(t, err, KindInvalidOperation)
		var failure *Failure
		if !failureAs(err, &failure) || failure.Op != "research_read" {
			t.Fatalf("overflow failure=%v, want research_read operation", err)
		}
	}
}

func TestArchitectureSpikeCompletionFailsClosedBeforeDecisionWorkflow(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	events := []Event{
		{EventID: "architecture-product", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product", Actor: "test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "architecture-project", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project", Actor: "test", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Project"}`)},
		{EventID: "architecture-product-project", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product", Actor: "test", OccurredAt: time.Unix(1, 2).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"product_id": "product", "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		{EventID: "architecture-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "spike", Actor: "test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "architecture_spike", "title": "Spike", "priority": 1})},
		{EventID: "architecture-work-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "spike", Actor: "test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "spike", "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 0, VersionRef(SubjectProject, "project"): 0, VersionRef(SubjectWorkItem, "spike"): 0}}); err != nil {
		t.Fatal(err)
	}
	pack := createSimplePack(t, s, "spike-research", "spike")
	if _, err := AddResearchFinding(ctx, s, ResearchFindingRequest{Identity: researchIdentity("spike-finding"), PackID: pack.PackID, ExpectedVersion: 1, Finding: ResearchFinding{FindingID: "f1", Kind: FindingConclusion, Statement: "research alone is not accepted decision proof", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
		t.Fatal(err)
	}
	beforeEvents := countRows(t, s, "domain_events")
	event, _ := operationEventForResearch("spike-complete", "work.transitioned", SubjectWorkItem, "spike", map[string]any{"from": "needed", "to": "completed", "reason": "research complete", "evidence_refs": []string{"research:f1"}, "expected_version": 2, "resulting_version": 3})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "spike"): 2}}); err == nil {
		t.Fatal("architecture_spike completed without accepted decision proof")
	} else {
		assertFailureKind(t, err, KindDecisionRecordRequired)
	}
	var lifecycle string
	if err := s.DB().QueryRow(`SELECT lifecycle FROM work_items WHERE id='spike'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "needed" || countRows(t, s, "domain_events") != beforeEvents {
		t.Fatalf("fail-closed spike completion changed lifecycle/events=%s/%d", lifecycle, countRows(t, s, "domain_events"))
	}
}

func TestActiveResearchSchemaEnforcesForeignKeysAndChecks(t *testing.T) {
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	if _, err := s.DB().Exec(`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('bad-owner','missing',1,'current',1,'now','now')`); err == nil {
		t.Fatal("missing owner FK accepted")
	}
	if _, err := s.DB().Exec(`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('schema-pack','owner',1,'current',1,'now','now'); INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES('schema-pack',1,'q','{}','{}','{}','m','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status) VALUES('schema-pack',1,'f','not-an-enum','x','high','current','active')`); err == nil {
		t.Fatal("finding enum CHECK accepted")
	}
	if _, err := s.DB().Exec(`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES('schema-pack',2,'q','not-json','{}','{}','m','now')`); err == nil {
		t.Fatal("JSON CHECK accepted invalid scope")
	}
	if _, err := s.DB().Exec(`INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES('schema-pack',1,'missing','context',1,'now')`); err == nil {
		t.Fatal("consumer FK accepted")
	}
}

func TestEpicEntriesFoldAndCompletionGate(t *testing.T) {
	ctx := context.Background()
	guarded := openTemp(t)
	seedResearchWork(t, guarded, "guarded")
	if _, err := guarded.DB().Exec(`UPDATE work_items SET kind='epic' WHERE id='guarded'`); err == nil {
		t.Fatal("direct work kind mutation bypassed fold-only authority")
	}
	// The operation below establishes the Epic kind through work.created.
	s := openTemp(t)
	events := []Event{
		{EventID: "p", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "p", Actor: "test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"P","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "pr", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "pr", Actor: "test", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"PR"}`)},
		{EventID: "pp", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "p", Actor: "test", OccurredAt: time.Unix(1, 2).UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"p","project_id":"pr","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
		{EventID: "epic", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "epic", Actor: "test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "epic", "title": "Epic", "priority": 1})},
		{EventID: "child", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "child", Actor: "test", OccurredAt: time.Unix(2, 1).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "task", "title": "Child", "priority": 1})},
		{EventID: "child2", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "child2", Actor: "test", OccurredAt: time.Unix(2, 2).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "task", "title": "Child 2", "priority": 1})},
		{EventID: "epic-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "epic", Actor: "test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "epic", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		{EventID: "child-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "child", Actor: "test", OccurredAt: time.Unix(3, 1).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "child", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		{EventID: "child2-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "child2", Actor: "test", OccurredAt: time.Unix(3, 2).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "child2", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "p"): 0, VersionRef(SubjectProject, "pr"): 0, VersionRef(SubjectWorkItem, "epic"): 0, VersionRef(SubjectWorkItem, "child"): 0, VersionRef(SubjectWorkItem, "child2"): 0}}); err != nil {
		t.Fatal(err)
	}
	entry, _ := EpicEntryEvent("add", "epic_entry.added", "epic", EpicEntry{ChildWorkID: "child", Position: 0, Required: true}, "test", time.Unix(4, 0).UTC(), 2)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{entry}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 2}}); err != nil {
		t.Fatal(err)
	}
	entry2, _ := EpicEntryEvent("add-2", "epic_entry.added", "epic", EpicEntry{ChildWorkID: "child2", Position: 1, Required: false}, "test", time.Unix(4, 0).UTC(), 3)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{entry2}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 3}}); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEpicEntries(ctx, s, "epic")
	if err != nil || len(entries) != 2 || entries[0].ChildWorkID != "child" || entries[1].ChildWorkID != "child2" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	var parentDirection, reverseDirection int
	if err := s.DB().QueryRow(`SELECT count(*) FROM relations WHERE kind='parent' AND work_id_from='epic' AND work_id_to='child2'`).Scan(&parentDirection); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT count(*) FROM relations WHERE kind='parent' AND work_id_from='child2' AND work_id_to='epic'`).Scan(&reverseDirection); err != nil {
		t.Fatal(err)
	}
	if parentDirection != 1 || reverseDirection != 0 {
		t.Fatalf("Epic parent direction = %d/%d", parentDirection, reverseDirection)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	entries, err = ReadEpicEntries(ctx, s, "epic")
	if err != nil || len(entries) != 2 || entries[0].Position != 0 || entries[1].Position != 1 {
		t.Fatalf("rebuilt entries=%+v err=%v", entries, err)
	}
	blocked, _ := operationEventForResearch("blocked-complete", "work.transitioned", SubjectWorkItem, "epic", map[string]any{"from": "needed", "to": "completed", "reason": "test", "expected_version": 4, "resulting_version": 5})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{blocked}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 4}}); err == nil {
		t.Fatal("Epic completed with required nonterminal child")
	} else {
		assertFailureKind(t, err, KindEpicCompletionBlocked)
	}
	reorder, _ := EpicEntryEvent("reorder", "epic_entry.reordered", "epic", EpicEntry{ChildWorkID: "child2", Position: 0}, "test", time.Unix(4, 1).UTC(), 4)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{reorder}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 4}}); err != nil {
		t.Fatal(err)
	}
	requiredness, _ := EpicEntryEvent("optional", "epic_entry.requiredness_changed", "epic", EpicEntry{ChildWorkID: "child", Required: false}, "test", time.Unix(4, 2).UTC(), 5)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{requiredness}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 5}}); err != nil {
		t.Fatal(err)
	}
	complete, _ := operationEventForResearch("complete-epic", "work.transitioned", SubjectWorkItem, "epic", map[string]any{"from": "needed", "to": "completed", "reason": "test", "expected_version": 6, "resulting_version": 7})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{complete}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 6}}); err != nil {
		t.Fatal(err)
	}
}

func TestReadEpicEntriesRejectsOverflow(t *testing.T) {
	s := openTemp(t)
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	insertWork := `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?, 'task', ?, 'needed', 1, 1, 'now', 'now')`
	if _, err := tx.Exec(insertWork, "epic", "Epic"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	for i := 0; i <= maxEpicEntriesRead; i++ {
		childID := fmt.Sprintf("child-%04d", i)
		if _, err := tx.Exec(insertWork, childID, childID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO epic_entries(epic_work_id,child_work_id,position,required) VALUES(?,?,?,0)`, "epic", childID, i); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM fold_guard`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEpicEntries(context.Background(), s, "epic"); err == nil {
		t.Fatal("Epic entry read silently truncated overflow")
	} else {
		assertFailureKind(t, err, KindInvalidOperation)
		var failure *Failure
		if !failureAs(err, &failure) || failure.RetrySafe {
			t.Fatalf("overflow failure=%v, want non-retryable typed failure", err)
		}
	}
}

func TestEpicRejectsNestedAndCrossProductEntries(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	events := []Event{
		{EventID: "p1", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "p1", Actor: "test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"P1","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "pr1", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "pr1", Actor: "test", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"PR1"}`)},
		{EventID: "p2", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "p2", Actor: "test", OccurredAt: time.Unix(1, 2).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"P2","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "pr2", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "pr2", Actor: "test", OccurredAt: time.Unix(1, 3).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"PR2"}`)},
		{EventID: "p1-pr1", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "p1", Actor: "test", OccurredAt: time.Unix(1, 4).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"product_id": "p1", "project_id": "pr1", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		{EventID: "p2-pr2", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "p2", Actor: "test", OccurredAt: time.Unix(1, 5).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"product_id": "p2", "project_id": "pr2", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
	}
	for _, item := range []struct{ id, kind, project string }{{"epic", "epic", "pr1"}, {"nested", "epic", "pr1"}, {"cross", "task", "pr2"}} {
		events = append(events,
			Event{EventID: item.id + "-created", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: item.id, Actor: "test", OccurredAt: time.Unix(2, int64(len(events))).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": item.kind, "title": item.id, "priority": 1})},
			Event{EventID: item.id + "-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: item.id, Actor: "test", OccurredAt: time.Unix(3, int64(len(events))).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": item.id, "project_id": item.project, "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		)
	}
	expected := map[SubjectRef]int64{VersionRef(SubjectProduct, "p1"): 0, VersionRef(SubjectProject, "pr1"): 0, VersionRef(SubjectProduct, "p2"): 0, VersionRef(SubjectProject, "pr2"): 0}
	for _, id := range []string{"epic", "nested", "cross"} {
		expected[VersionRef(SubjectWorkItem, id)] = 0
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: expected}); err != nil {
		t.Fatal(err)
	}
	nested, _ := EpicEntryEvent("nested-entry", "epic_entry.added", "epic", EpicEntry{ChildWorkID: "nested", Position: 0}, "test", time.Unix(4, 0).UTC(), 2)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{nested}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 2}}); err == nil {
		t.Fatal("nested Epic entry succeeded")
	} else {
		assertFailureKind(t, err, KindEpicScopeViolation)
	}
	cross, _ := EpicEntryEvent("cross-entry", "epic_entry.added", "epic", EpicEntry{ChildWorkID: "cross", Position: 0}, "test", time.Unix(4, 1).UTC(), 2)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{cross}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "epic"): 2}}); err == nil {
		t.Fatal("cross-Product Epic entry succeeded")
	} else {
		assertFailureKind(t, err, KindEpicScopeViolation)
	}
}

func operationEventForResearch(id, kind string, subject SubjectType, subjectID string, payload map[string]any) (Event, error) {
	return Event{EventID: id, Kind: kind, SubjectType: subject, SubjectID: subjectID, Actor: "test", OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(payload)}, nil
}
