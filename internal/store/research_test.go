package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
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
	t.Parallel()
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
	if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("finding"), PackID: pack.PackID, ExpectedVersion: 2, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchSource(ctx, ResearchSourceRequest{Identity: researchIdentity("source"), PackID: pack.PackID, ExpectedVersion: 3, Source: ResearchSource{SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com", Title: "Example", PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if err := BindResearchFindingSource(ctx, s, ResearchFindingSourceRequest{Identity: researchIdentity("finding-source"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 4, FindingID: "f1", SourceID: "s1"}); err != nil {
		t.Fatal(err)
	}
	complete, err := GetResearchPack(ctx, s, pack.PackID, 1000)
	if err != nil || len(complete.Revisions) != 2 || len(complete.Revisions[1].Sources) != 1 || len(complete.Revisions[1].Findings) != 1 || len(complete.Revisions[1].Findings[0].SourceIDs) != 1 {
		t.Fatalf("complete research pack = %+v, %v", complete, err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	retained, err := GetResearchPack(ctx, s, pack.PackID, 1000)
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
	if _, err := s.UpdateResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("consumed-update"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 6, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "updated", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err == nil {
		t.Fatal("consumed revision update succeeded")
	} else {
		assertFailureKind(t, err, KindResearchRevisionImmutable)
	}
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("append-after-consume"), PackID: pack.PackID, ExpectedVersion: 6, Revision: ResearchRevisionInput{Question: "q3", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "source"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("new-current-finding"), PackID: pack.PackID, ExpectedVersion: 7, Finding: ResearchFinding{FindingID: "f3", Kind: FindingConclusion, Statement: "new current", Confidence: ConfidenceMedium, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RequiredResearchFreshness(ctx, pack.PackID, "consumer"); err != nil || got != ResearchCurrent {
		t.Fatalf("required current freshness = %q, %v", got, err)
	}
	// Issue #122: freshness targets the revision the consumer pins, not the
	// pack. A pack-level set does not poison unchanged revisions, and an
	// unrelated append does not un-stale pinned content.
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("stale-other"), PackID: pack.PackID, ExpectedVersion: 8, Freshness: ResearchStale}); err != nil {
		t.Fatal(err)
	}
	// That set targeted the CURRENT revision (3); the consumer pinned to
	// revision 2 is unaffected by another revision's staleness.
	if got, err := s.RequiredResearchFreshness(ctx, pack.PackID, "consumer"); err != nil || got != ResearchCurrent {
		t.Fatalf("consumer pinned to unchanged revision poisoned: %q, %v", got, err)
	}
	// Staling the pinned revision blocks the consumer...
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("stale-pinned"), PackID: pack.PackID, ExpectedVersion: 9, Freshness: ResearchStale, Revision: 2}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RequiredResearchFreshness(ctx, pack.PackID, "consumer"); err != nil || got != ResearchStale {
		t.Fatalf("required stale freshness = %q, %v", got, err)
	}
	freshness, err := ResearchFreshnessForPack(ctx, s, pack.PackID)
	if err != nil || freshness.Status != ResearchStale || !freshness.Blocked {
		t.Fatalf("freshness = %+v, %v", freshness, err)
	}
	// ...and stays blocked across an unrelated append (two revisions of one
	// pack disagree: current 4 is fresh, pinned 2 is stale).
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("append-while-stale"), PackID: pack.PackID, ExpectedVersion: 10, Revision: ResearchRevisionInput{Question: "q4", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "docs"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.RequiredResearchFreshness(ctx, pack.PackID, "consumer"); err != nil || got != ResearchStale {
		t.Fatalf("unrelated append un-staled pinned content: %q, %v", got, err)
	}
	// Retention cutover: deletion is retire-only. The owner is still needed
	// here, so terminalize it to isolate the pin protection this assertion
	// owns: the active required pin keeps the pack protected.
	terminalizeResearchOwner(t, s, "owner")
	out, err := retireResearchForTest(t, s, RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{{PackID: pack.PackID, ExpectedVersion: 11}}})
	if err != nil || len(out.Candidates) != 1 || out.Candidates[0].Classification != ResearchRetirementProtected || out.Candidates[0].ProtectionReason != "active_pin" {
		t.Fatalf("retirement with required active consumer=%+v err=%v", out, err)
	}
	if countRows(t, s, "active_research_packs") != 1 {
		t.Fatal("active required pin did not protect the pack")
	}
}

func TestActiveResearchNonrequiredConsumerDoesNotBlock(t *testing.T) {
	t.Parallel()
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
	// An optional pin protects the pack too: after the owner terminalizes, the
	// pack stays protected until the optional consumer releases.
	terminalizeResearchOwner(t, s, "owner")
	out, err := retireResearchForTest(t, s, RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{{PackID: pack.PackID, ExpectedVersion: 3}}})
	if err != nil || len(out.Candidates) != 1 || out.Candidates[0].Classification != ResearchRetirementProtected || out.Candidates[0].ProtectionReason != "active_pin" {
		t.Fatalf("optional-pin retirement=%+v err=%v", out, err)
	}
	if countRows(t, s, "active_research_packs") != 1 {
		t.Fatal("optional pin did not protect the pack")
	}
}

func TestResearchFreshnessReturnsOnlyFirstFailingConsumer(t *testing.T) {
	t.Parallel()
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
	event := operationEventForResearch("terminal-"+id, "work.transitioned", id, map[string]any{"from": "needed", "to": "completed", "reason": "archive", "expected_version": 2, "resulting_version": 3})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 2}}); err != nil {
		t.Fatal(err)
	}
}

func linkArchivedResearchOwner(t *testing.T, s *Store, id string) {
	t.Helper()
	anchorHomePair(t, s, "home", "locator")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES(?, 'work_note', ?, '2026-08-07T00:00:00Z', 'completed', '[]', 'completed', 1, 'durable summary', 'home', 'locator', 'notes/`+id+`.md', 'commit', 'hash'); DELETE FROM fold_guard`, id, id); err != nil {
		t.Fatal(err)
	}
}

func TestActiveResearchPersistsAcrossCloseAndReopen(t *testing.T) {
	t.Parallel()
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
	pack, err := GetResearchPack(ctx, reopened, "persist-pack", 1000)
	if err != nil || len(pack.Revisions) != 1 || pack.OwnerWorkID != "owner" {
		t.Fatalf("reopened pack=%+v err=%v", pack, err)
	}
}

func TestResearchConsumersPinDifferentRevisions(t *testing.T) {
	t.Parallel()
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
	got, err := GetResearchPack(ctx, s, pack.PackID, 1000)
	if err != nil || len(got.Consumers) != 2 || got.Consumers[0].Revision == got.Consumers[1].Revision {
		t.Fatalf("pinned consumers=%+v err=%v", got.Consumers, err)
	}
}

func TestResearchPruneKeepsCurrentAndConsumedRevisions(t *testing.T) {
	t.Parallel()
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
	got, err := GetResearchPack(ctx, s, pack.PackID, 1000)
	if err != nil || len(got.Revisions) != 2 || got.Revisions[0].Revision != 1 || got.Revisions[1].Revision != 3 {
		t.Fatalf("pruned revisions=%+v err=%v", got.Revisions, err)
	}
}

func TestRetainedOwnerContentIsReadOnlyExceptFreshness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "retained-readonly", "owner")
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("retained-bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: false}}); err != nil {
		t.Fatal(err)
	}
	terminalizeResearchOwner(t, s, "owner")
	// A freshness review stays available: staling a retained revision is the
	// one retained-owner write the retention cutover keeps.
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("retained-freshness"), PackID: pack.PackID, ExpectedVersion: 2, Freshness: ResearchStale}); err != nil {
		t.Fatalf("retained freshness review refused: %v", err)
	}
	authoring := []struct {
		name string
		run  func() error
	}{
		{"append", func() error {
			_, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("retained-append"), PackID: pack.PackID, ExpectedVersion: 3, Revision: simpleResearchRevision()})
			return err
		}},
		{"finding", func() error {
			_, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("retained-finding"), PackID: pack.PackID, ExpectedVersion: 3, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}})
			return err
		}},
		{"source", func() error {
			_, err := s.AddResearchSource(ctx, ResearchSourceRequest{Identity: researchIdentity("retained-source"), PackID: pack.PackID, ExpectedVersion: 3, Source: ResearchSource{SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com", Title: "Source", PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}})
			return err
		}},
		{"citation", func() error {
			return BindResearchFindingSource(ctx, s, ResearchFindingSourceRequest{Identity: researchIdentity("retained-citation"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 3, FindingID: "f1", SourceID: "s1"})
		}},
		{"prune", func() error {
			_, err := PruneResearchRevisions(ctx, s, ResearchPackMutationRequest{Identity: researchIdentity("retained-prune"), PackID: pack.PackID, ExpectedVersion: 3})
			return err
		}},
	}
	for _, write := range authoring {
		if err := write.run(); err == nil {
			t.Fatalf("retained pack accepted %s", write.name)
		} else if !hasFailureKind(err, KindInvalidOperation) || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("retained pack %s refusal=%v", write.name, err)
		}
	}
	// Releasing a pin is not authoring: the optional consumer may still unbind.
	if _, err := UnbindResearchConsumer(ctx, s, UnbindResearchConsumerRequest{Identity: researchIdentity("retained-unbind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 3, ConsumerWorkID: "consumer"}); err != nil {
		t.Fatalf("retained pack refused a pin release: %v", err)
	}
	got, err := GetResearchPack(ctx, s, pack.PackID, 1000)
	if err != nil || len(got.Revisions) != 1 || got.Revisions[0].Freshness != ResearchStale || len(got.Consumers) != 0 {
		t.Fatalf("retained pack after read-only proofs=%+v err=%v", got, err)
	}
}

func TestTerminalUnlinkedPackRemainsReadable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "other")
	pack := createSimplePack(t, s, "unlinked-terminal", "owner")
	terminalizeResearchOwner(t, s, "owner")
	if got, err := GetResearchPack(ctx, s, pack.PackID, 1000); err != nil || got.PackID != pack.PackID {
		t.Fatalf("unlinked terminal pack=%+v err=%v", got, err)
	}
	other := createSimplePack(t, s, "unrelated-active", "other")
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("unrelated-write"), PackID: other.PackID, ExpectedVersion: 1, Freshness: ResearchStale}); err != nil {
		t.Fatal(err)
	}
}

func TestResearchRetentionLinkedReadDoesNotDelete(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "retained-linked-read", "owner")
	terminalizeResearchOwner(t, s, "owner")
	linkArchivedResearchOwner(t, s, "owner")
	if got, err := GetResearchPack(ctx, s, pack.PackID, 1000); err != nil || got.PackID != pack.PackID {
		t.Fatalf("read deleted retained research: pack=%+v err=%v", got, err)
	}
}

func TestResearchRetentionTerminalOwnerAllowsReliance(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "retained-terminal-reliance", "owner")
	terminalizeResearchOwner(t, s, "owner")
	err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "research_reliance")
		if err != nil {
			return err
		}
		return BindResearchRelianceTx(ctx, tx, "consumer", []ResearchBindingDeclaration{{PackID: pack.PackID, Revision: 1, UseRole: UseContext}}, time.Unix(10, 0))
	})
	if err != nil {
		t.Fatalf("retained terminal-owner reliance refused: %v", err)
	}
}

func TestRetirementDeleteGuardRefusesIneligibleDeletes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	activePack := createSimplePack(t, s, "guard-active", "owner")
	retainedPack := createSimplePack(t, s, "guard-retained", "owner")
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("guard-bind"), PackID: retainedPack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: false}}); err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	// The guard applies retirement authority to every delete, so a raw or
	// legacy binary delete of an active-owner pack is refused.
	if _, err := db.Exec(`DELETE FROM active_research_packs WHERE pack_id=?`, activePack.PackID); err == nil {
		t.Fatal("raw delete of an active-owner pack passed the retirement guard")
	} else if !strings.Contains(err.Error(), "retire-eligible") {
		t.Fatalf("guard refusal=%v", err)
	}
	terminalizeResearchOwner(t, s, "owner")
	// An optional pin protects the retained pack from every delete too.
	if _, err := db.Exec(`DELETE FROM active_research_packs WHERE pack_id=?`, retainedPack.PackID); err == nil {
		t.Fatal("raw delete with an active optional pin passed the retirement guard")
	}
	terminalizeResearchOwner(t, s, "consumer")
	if _, err := db.Exec(`DELETE FROM active_research_packs WHERE pack_id=?`, retainedPack.PackID); err != nil {
		t.Fatalf("retire-eligible pack delete refused: %v", err)
	}
}

func TestWorkRemovalRefusesUntilOwnedPacksRetire(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "removal-owned", "owner")
	cancel := operationEventForResearch("removal-cancel", "work.transitioned", "owner", map[string]any{"from": "needed", "to": "cancelled", "reason": "done", "expected_version": 2, "resulting_version": 3})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{cancel}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "owner"): 2}}); err != nil {
		t.Fatal(err)
	}
	req := removalTestRequest()
	req.OperationID = "remove-research-owner"
	req.IdempotencyKey = "remove-research-owner-key"
	req.WorkID = "owner"
	req.ExpectedVersion = 3
	if _, err := s.ShelveWork(ctx, req); err == nil {
		t.Fatal("removal succeeded while the work still owned a research pack")
	} else if !hasFailureKind(err, KindResourceClaimHeld) || !strings.Contains(err.Error(), "research pack") {
		t.Fatalf("removal refusal=%v", err)
	}
	if countRows(t, s, "active_research_packs") != 1 {
		t.Fatal("refused removal deleted the owned pack")
	}
	out, err := retireResearchForTest(t, s, RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{{PackID: pack.PackID, ExpectedVersion: 1}}})
	if err != nil || len(out.Candidates) != 1 || out.Candidates[0].Classification != ResearchRetirementRetired {
		t.Fatalf("retirement before removal=%+v err=%v", out, err)
	}
	if _, err := s.ShelveWork(ctx, req); err != nil {
		t.Fatalf("removal after retirement refused: %v", err)
	}
	var work int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id='owner'`).Scan(&work); err != nil {
		t.Fatal(err)
	}
	if work != 0 || countRows(t, s, "active_research_packs") != 0 {
		t.Fatalf("removal after retirement left work=%d packs=%d", work, countRows(t, s, "active_research_packs"))
	}
}

func TestWorkRemovalReleasesOptionalConsumerPin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "removal-pin", "owner")
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("removal-pin-bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: false}}); err != nil {
		t.Fatal(err)
	}
	req := removalTestRequest()
	req.OperationID = "remove-research-consumer"
	req.IdempotencyKey = "remove-research-consumer-key"
	req.WorkID = "consumer"
	req.ExpectedVersion = 2
	if _, err := s.ShelveWork(ctx, req); err != nil {
		t.Fatalf("removal of an optional-pin consumer refused: %v", err)
	}
	if countRows(t, s, "active_research_consumers") != 0 {
		t.Fatal("removal left the consumer pin behind")
	}
	if countRows(t, s, "active_research_packs") != 1 {
		t.Fatal("removal destroyed the pinned pack")
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	// Create, bind, then one bump from the atomic pin release.
	if version != 3 {
		t.Fatalf("released pack version=%d, want one bump from the pin release", version)
	}
	terminalizeResearchOwner(t, s, "owner")
	out, err := retireResearchForTest(t, s, RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{{PackID: pack.PackID, ExpectedVersion: version}}})
	if err != nil || len(out.Candidates) != 1 || out.Candidates[0].Classification != ResearchRetirementRetired {
		t.Fatalf("retirement after pin release=%+v err=%v", out, err)
	}
}

func TestRetainedPackSurvivesProjectionRebuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "rebuild-retained", "owner")
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("rebuild-retained-bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: false}}); err != nil {
		t.Fatal(err)
	}
	terminalizeResearchOwner(t, s, "owner")
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := GetResearchPack(ctx, s, pack.PackID, 1000)
	if err != nil || len(got.Revisions) != 1 || len(got.Consumers) != 1 {
		t.Fatalf("rebuilt retained pack=%+v err=%v", got, err)
	}
}

func TestRetainedPackSurvivesCloseAndReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-retained.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "reopen-retained", "owner")
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("reopen-retained-bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: false}}); err != nil {
		t.Fatal(err)
	}
	terminalizeResearchOwner(t, s, "owner")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := GetResearchPack(ctx, reopened, pack.PackID, 1000)
	if err != nil || len(got.Revisions) != 1 || len(got.Consumers) != 1 || got.OwnerWorkID != "owner" {
		t.Fatalf("reopened retained pack=%+v err=%v", got, err)
	}
}

func TestMigrationInstallsRetirementDeleteGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "concord-retention-v122.db"), 122)
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('owner','task','Owner','needed',1,1,'t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	packInsert := `INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES(?,'owner',1,'current',1,'t','t')`
	revisionInsert := `INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at,freshness) VALUES(?,1,'q','{}','{}','{}','m','t','current')`
	if _, err := db.ExecContext(ctx, packInsert, "migration-pack"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, revisionInsert, "migration-pack"); err != nil {
		t.Fatal(err)
	}
	// The pre-cutover schema still lets a legacy binary delete the pack.
	if _, err := db.ExecContext(ctx, `DELETE FROM active_research_packs WHERE pack_id='migration-pack'`); err != nil {
		t.Fatalf("v122 schema refused a legacy deletion: %v", err)
	}
	if _, err := db.ExecContext(ctx, packInsert, "migration-pack"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, revisionInsert, "migration-pack"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 123 {
		t.Fatalf("schema version=%d, want 123", version)
	}
	for _, m := range migrations {
		if m.Version == 123 && !m.Breaking {
			t.Fatal("retention cutover migration must declare Breaking")
		}
	}
	var guards int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type='trigger' AND name='active_research_packs_retirement_delete_guard'`).Scan(&guards); err != nil {
		t.Fatal(err)
	}
	if guards != 1 {
		t.Fatal("retention migration did not install the retirement delete guard")
	}
	// The migration is data-preserving: the pack and its revision survive.
	var packs, revisions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM active_research_packs`).Scan(&packs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM active_research_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if packs != 1 || revisions != 1 {
		t.Fatalf("migration changed research content: packs=%d revisions=%d", packs, revisions)
	}
	// The legacy deletion shape is refused after the migration.
	if _, err := db.ExecContext(ctx, `DELETE FROM active_research_packs WHERE pack_id='migration-pack'`); err == nil {
		t.Fatal("migrated schema admitted a legacy deletion")
	}
}

// TestRetirementDeleteGuardRefusesMissingOwner witnesses the guard's
// fail-closed reading of a pack whose owner work row is missing: SQL NULL
// from the owner lookup must count as unmet eligibility, not as silence the
// delete passes through. No production path creates this state, so the
// corruption is synthesized on a foreign connection with foreign keys off —
// the kind of outside-the-store write the guard exists to refuse.
func TestRetirementDeleteGuardRefusesMissingOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord-retention-missing-owner.db")
	db := openMigratedTo(t, path, 123)
	if _, err := db.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('owner','task','Owner','needed',1,1,'t','t')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('missing-owner-pack','owner',1,'current',1,'t','t')`); err != nil {
		t.Fatal(err)
	}
	// A present, non-terminal owner refuses before the corruption too.
	if _, err := db.ExecContext(ctx, `DELETE FROM active_research_packs WHERE pack_id='missing-owner-pack'`); err == nil {
		t.Fatal("guard admitted a delete whose owner work is non-terminal")
	}
	synthExecPath(t, path, `DELETE FROM work_items WHERE id='owner'`)
	if _, err := db.ExecContext(ctx, `DELETE FROM active_research_packs WHERE pack_id='missing-owner-pack'`); err == nil {
		t.Fatal("guard admitted a delete whose owner work row is missing")
	}
}

func TestBlockedLinkedOwnerDoesNotBlockUnrelatedPackMutation(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
			if tc.kind == "work.superseded" {
				event = operationEventForResearch(tc.name+"-consumer-terminal", tc.kind, "consumer", map[string]any{"successor": "successor", "superseded": "consumer", "reason": "done", "expected_version": 2, "resulting_version": 3})
			} else {
				event = operationEventForResearch(tc.name+"-consumer-terminal", tc.kind, "consumer", map[string]any{"from": "needed", "to": tc.to, "reason": "done", "expected_version": 2, "resulting_version": 3})
			}
			if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "consumer"): 2}}); err != nil {
				t.Fatal(err)
			}
			var bindings int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_consumers WHERE consumer_work_id=?`, "consumer").Scan(&bindings); err != nil {
				t.Fatal(err)
			}
			if bindings != 0 {
				t.Fatalf("terminal consumer bindings=%d", bindings)
			}
			for _, pack := range []ResearchPack{packA, packB} {
				var version int
				if err := s.DatabaseForTesting().QueryRow(`SELECT expected_version FROM active_research_packs WHERE pack_id=?`, pack.PackID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				if version != 3 {
					t.Fatalf("terminal consumer pack %s version=%d, want 3: created at 1, one bump from the consumer binding, one from its release when the consumer went terminal", pack.PackID, version)
				}
			}
		})
	}
}

func compactionFixture(t *testing.T, required bool) (*Store, KnowledgeHome, ResearchPack, string, string) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	path := ".concord/docs/work/owner.md"
	writeKnowledgeFile(t, repo, path, canonicalWorkNote("owner", "2026-08-07T00:00:00Z"))
	commit := commitKnowledgeRepo(t, repo, "owner proof")
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	seedEventDerivedLocator(t, s, "home", "owner-repo", repo)
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

func compactionRequest(home KnowledgeHome, commit, path, eventID string) CompactionLinkRequest {
	return CompactionLinkRequest{EventID: eventID, WorkID: "owner", ExpectedVersion: 3, Actor: "test", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), Home: home, CommitOID: commit, NotePath: path, Reason: "proof-backed archive"}
}

func TestPublishCompactionLinkRetainsResearchWithRequiredConsumer(t *testing.T) {
	t.Parallel()
	s, home, pack, commit, path := compactionFixture(t, true)
	if err := PublishCompactionLink(context.Background(), s, compactionRequest(home, commit, path, "retained-compaction")); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "archived_work") != 1 {
		t.Fatal("compaction did not record the archive")
	}
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='compaction_link.published'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("compaction events=%d, want 1", events)
	}
	if countRows(t, s, "active_research_packs") != 1 || countRows(t, s, "active_research_consumers") != 1 {
		t.Fatal("publication destroyed retained research")
	}
	if got, err := GetResearchPack(context.Background(), s, pack.PackID, 1000); err != nil || got.PackID != pack.PackID {
		t.Fatalf("retained pack unreadable after publication: %+v err=%v", got, err)
	}
}

func TestCompactionFoldAcceptsRequiredConsumer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, pack, commit, path := compactionFixture(t, true)
	note, err := VerifyCommittedNote(ctx, home.RepoPath, commit, path, "")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(compactionLinkPayloadV1{ID: "owner", Type: "work_note", Title: note.Title, CompletedAt: note.CompletedAt, OutcomeTag: note.OutcomeTag, LessonTags: note.LessonTags, TerminalState: note.TerminalState, Priority: note.Priority, Summary: note.Summary, ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs, ComponentIDs: note.DomainIDs, TagIDs: note.TagIDs, HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID, NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash, Reason: "direct fold test", ExpectedVersion: 3, ResultingVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "direct-retained-compaction", Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: "owner", Actor: "test", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: payload}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "owner"): 3}}); err != nil {
		t.Fatalf("compaction fold refused a required active consumer: %v", err)
	}
	if countRows(t, s, "archived_work") != 1 {
		t.Fatal("fold did not record the archive")
	}
	if countRows(t, s, "active_research_packs") != 1 || countRows(t, s, "active_research_consumers") != 1 {
		t.Fatalf("fold destroyed retained research: packs=%d consumers=%d", countRows(t, s, "active_research_packs"), countRows(t, s, "active_research_consumers"))
	}
	if _, err := GetResearchPack(ctx, s, pack.PackID, 1000); err != nil {
		t.Fatalf("retained pack unreadable after fold: %v", err)
	}
}

func TestProofBackedCompactionRetainsResearchAndNeverStoresBody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	path := ".concord/docs/work/owner.md"
	writeKnowledgeFile(t, repo, path, canonicalWorkNote("owner", "2026-08-07T00:00:00Z"))
	commit := commitKnowledgeRepo(t, repo, "owner proof")
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	seedEventDerivedLocator(t, s, "home", "owner-repo", repo)
	pack := createSimplePack(t, s, "compaction-fixture", "owner")
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("compaction-bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 1, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: true}}); err != nil {
		t.Fatal(err)
	}
	secret := "SECRET-RESEARCH-PACK-BODY"
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("secret-revision"), PackID: pack.PackID, ExpectedVersion: 2, Revision: ResearchRevisionInput{Question: secret, ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("secret-finding"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 3, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchSource(ctx, ResearchSourceRequest{Identity: researchIdentity("secret-source"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 4, Source: ResearchSource{SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com", Title: "Source", PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if err := BindResearchFindingSource(ctx, s, ResearchFindingSourceRequest{Identity: researchIdentity("secret-citation"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 5, FindingID: "f1", SourceID: "s1"}); err != nil {
		t.Fatal(err)
	}
	terminalizeResearchOwner(t, s, "owner")
	home := KnowledgeHome{HomeProjectID: "home", HomeLocatorID: "owner-repo", RepoPath: repo, HeadRef: "HEAD"}
	if err := PublishCompactionLink(ctx, s, compactionRequest(home, commit, path, "retained-proof-compaction")); err != nil {
		t.Fatal(err)
	}
	// Publication is proof-backed archive, not destruction: the pack body
	// stays in direct-table authority with its required consumer.
	for _, table := range []string{"active_research_packs", "active_research_revisions", "active_research_findings", "active_research_sources", "active_research_finding_sources", "active_research_consumers"} {
		if countRows(t, s, table) == 0 {
			t.Fatalf("%s lost rows after proof-backed compaction", table)
		}
	}
	if got, err := GetResearchPack(ctx, s, pack.PackID, 1000); err != nil || len(got.Revisions) != 2 {
		t.Fatalf("retained pack after publication=%+v err=%v", got, err)
	}
	var bodyEvents, bodySummary int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE payload LIKE ?`, "%"+secret+"%").Scan(&bodyEvents); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM archived_work WHERE summary LIKE ?`, "%"+secret+"%").Scan(&bodySummary); err != nil {
		t.Fatal(err)
	}
	if bodyEvents != 0 || bodySummary != 0 {
		t.Fatalf("retained pack body leaked to events/summary=%d/%d", bodyEvents, bodySummary)
	}
	note, err := VerifyCommittedNote(ctx, home.RepoPath, commit, path, "")
	if err != nil || strings.Contains(string(note.Content), secret) {
		t.Fatalf("Git authority contains retained pack body: err=%v", err)
	}
}

func TestCompactionRetryAfterCrashWindowRetainsResearch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, pack, commit, path := compactionFixture(t, false)
	note, err := VerifyCommittedNote(ctx, home.RepoPath, commit, path, "")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(compactionLinkPayloadV1{ID: "owner", Type: "work_note", Title: note.Title, CompletedAt: note.CompletedAt, OutcomeTag: note.OutcomeTag, LessonTags: note.LessonTags, TerminalState: note.TerminalState, Priority: note.Priority, Summary: note.Summary, ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs, ComponentIDs: note.DomainIDs, TagIDs: note.TagIDs, HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID, NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash, Reason: "proof-backed archive", ExpectedVersion: 3, ResultingVersion: 4})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "crash-window-link", Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: "owner", Actor: "test", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), PayloadVersion: 1, Payload: payload}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "owner"): 3}}); err != nil {
		t.Fatal(err)
	}
	if countRows(t, s, "active_research_packs") != 1 {
		t.Fatal("crash-window link destroyed the retained pack")
	}
	if err := PublishCompactionLink(ctx, s, compactionRequest(home, commit, path, "crash-window-link")); err != nil {
		t.Fatalf("idempotent compaction retry refused: %v", err)
	}
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='compaction_link.published'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("retry duplicated the compaction event: %d", events)
	}
	if countRows(t, s, "active_research_packs") != 1 || countRows(t, s, "active_research_revisions") != 1 || pack.PackID == "" {
		t.Fatal("idempotent compaction retry did not retain the research")
	}
}

func TestArchiveFailureBeforeGitProofLeavesPackIntact(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "proof-failure", "owner")
	terminalizeResearchOwner(t, s, "owner")
	home := KnowledgeHome{HomeProjectID: "home", HomeLocatorID: "missing", RepoPath: t.TempDir(), HeadRef: "HEAD"}
	if err := PublishCompactionLink(context.Background(), s, compactionRequest(home, strings.Repeat("a", 40), ".concord/docs/work/missing.md", "proof-failure-link")); err == nil {
		t.Fatal("compaction without Git proof succeeded")
	}
	if countRows(t, s, "active_research_packs") != 1 || countRows(t, s, "archived_work") != 0 {
		t.Fatal("archive proof failure changed active research or archive projection")
	}
	_ = pack
}

func TestResearchFindingSourceReadRejectsGlobalOverflow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "finding-source-overflow", "owner")
	for i, findingID := range []string{"f1", "f2"} {
		if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("overflow-finding-" + findingID), PackID: pack.PackID, ExpectedVersion: int64(1 + i), Finding: ResearchFinding{FindingID: findingID, Kind: FindingObservation, Statement: findingID, Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, sourceID := range []string{"s1", "s2"} {
		if _, err := s.AddResearchSource(ctx, ResearchSourceRequest{Identity: researchIdentity("overflow-source-" + sourceID), PackID: pack.PackID, ExpectedVersion: int64(3 + i), Source: ResearchSource{SourceID: sourceID, Kind: SourceOfficialDoc, Locator: "https://example.com/" + sourceID, Title: sourceID, PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}}); err != nil {
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
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	events := []Event{
		{EventID: "architecture-product", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "product", Actor: "test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "architecture-project", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "project", Actor: "test", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Project"}`)},
		{EventID: "architecture-product-project", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "product", Actor: "test", OccurredAt: time.Unix(1, 2).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"product_id": "product", "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		{EventID: "architecture-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "spike", Actor: "test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "task", "title": "Spike", "priority": 1})},
		{EventID: "architecture-work-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "spike", Actor: "test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "spike", "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product"): 0, VersionRef(SubjectProject, "project"): 0, VersionRef(SubjectWorkItem, "spike"): 0}}); err != nil {
		t.Fatal(err)
	}
	initializeCompositionWorkflow(t, s, "spike", "workflow.architecture_spike", WorkflowActor{PrincipalRef: "principal:architecture", ClientRef: "client:architecture", AgentRef: "agent:architecture", SessionRef: "session:architecture", ActorClass: ActorAgent})
	// CD-0183 D4: a live instance refuses the lifecycle completion outright,
	// so the fail-closed decision-record case runs against a cancelled
	// instance, where the workflow gate never ran and the lifecycle-side
	// decision gate stays in force.
	seedTerminalInstanceForTesting(t, s, "spike", "cancelled")
	pack := createSimplePack(t, s, "spike-research", "spike")
	if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("spike-finding"), PackID: pack.PackID, ExpectedVersion: 1, Finding: ResearchFinding{FindingID: "f1", Kind: FindingConclusion, Statement: "research alone is not accepted decision proof", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
		t.Fatal(err)
	}
	beforeEvents := countRows(t, s, "domain_events")
	event := operationEventForResearch("spike-complete", "work.transitioned", "spike", map[string]any{"from": "needed", "to": "completed", "reason": "research complete", "evidence_refs": []string{"research:f1"}, "expected_version": 4, "resulting_version": 5})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "spike"): 4}}); err == nil {
		t.Fatal("architecture_spike completed without accepted decision proof")
	} else {
		assertFailureKind(t, err, KindDecisionRecordRequired)
	}
	var lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id='spike'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "needed" || countRows(t, s, "domain_events") != beforeEvents {
		t.Fatalf("fail-closed spike completion changed lifecycle/events=%s/%d", lifecycle, countRows(t, s, "domain_events"))
	}
}

func TestActiveResearchSchemaEnforcesForeignKeysAndChecks(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('bad-owner','missing',1,'current',1,'now','now')`); err == nil {
		t.Fatal("missing owner FK accepted")
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('schema-pack','owner',1,'current',1,'now','now'); INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES('schema-pack',1,'q','{}','{}','{}','m','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO active_research_findings(pack_id,revision,finding_id,kind,statement,confidence,freshness,status) VALUES('schema-pack',1,'f','not-an-enum','x','high','current','active')`); err == nil {
		t.Fatal("finding enum CHECK accepted")
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES('schema-pack',2,'q','not-json','{}','{}','m','now')`); err == nil {
		t.Fatal("JSON CHECK accepted invalid scope")
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES('schema-pack',1,'missing','context',1,'now')`); err == nil {
		t.Fatal("consumer FK accepted")
	}
}

func TestInitiativeEntriesFoldAndCompletionGate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	guarded := openTemp(t)
	seedResearchWork(t, guarded, "guarded")
	if _, err := guarded.DatabaseForTesting().Exec(`UPDATE work_items SET kind='initiative' WHERE id='guarded'`); err == nil {
		t.Fatal("direct work kind mutation bypassed fold-only authority")
	}
	// The operation below establishes the Initiative kind through work.created.
	s := openTemp(t)
	events := []Event{
		{EventID: "p", Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "p", Actor: "test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"P","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "pr", Kind: "project.created", SubjectType: SubjectProject, SubjectID: "pr", Actor: "test", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"PR"}`)},
		{EventID: "pp", Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "p", Actor: "test", OccurredAt: time.Unix(1, 2).UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"p","project_id":"pr","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)},
		{EventID: "initiative", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "initiative", Actor: "test", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "initiative", "title": "Initiative", "priority": 1})},
		{EventID: "child", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "child", Actor: "test", OccurredAt: time.Unix(2, 1).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "task", "title": "Child", "priority": 1})},
		{EventID: "child2", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "child2", Actor: "test", OccurredAt: time.Unix(2, 2).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "task", "title": "Child 2", "priority": 1})},
		{EventID: "initiative-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "initiative", Actor: "test", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "initiative", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		{EventID: "child-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "child", Actor: "test", OccurredAt: time.Unix(3, 1).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "child", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		{EventID: "child2-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "child2", Actor: "test", OccurredAt: time.Unix(3, 2).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": "child2", "project_id": "pr", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "p"): 0, VersionRef(SubjectProject, "pr"): 0, VersionRef(SubjectWorkItem, "initiative"): 0, VersionRef(SubjectWorkItem, "child"): 0, VersionRef(SubjectWorkItem, "child2"): 0}}); err != nil {
		t.Fatal(err)
	}
	entry, _ := InitiativeEntryEvent("add", "initiative_entry.added", "initiative", InitiativeEntry{ChildWorkID: "child", Position: 0, Required: true}, "test", time.Unix(4, 0).UTC(), 2)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{entry}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}}); err != nil {
		t.Fatal(err)
	}
	entry2, _ := InitiativeEntryEvent("add-2", "initiative_entry.added", "initiative", InitiativeEntry{ChildWorkID: "child2", Position: 1, Required: false}, "test", time.Unix(4, 0).UTC(), 3)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{entry2}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 3}}); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadInitiativeEntries(ctx, s, "initiative")
	if err != nil || len(entries) != 2 || entries[0].ChildWorkID != "child" || entries[1].ChildWorkID != "child2" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	var includesDirection, reverseDirection int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE kind='includes' AND work_id_from='initiative' AND work_id_to='child2'`).Scan(&includesDirection); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE kind='includes' AND work_id_from='child2' AND work_id_to='initiative'`).Scan(&reverseDirection); err != nil {
		t.Fatal(err)
	}
	if includesDirection != 1 || reverseDirection != 0 {
		t.Fatalf("Initiative includes direction = %d/%d", includesDirection, reverseDirection)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	entries, err = ReadInitiativeEntries(ctx, s, "initiative")
	if err != nil || len(entries) != 2 || entries[0].Position != 0 || entries[1].Position != 1 {
		t.Fatalf("rebuilt entries=%+v err=%v", entries, err)
	}
	for _, relationKind := range []string{"parent", "includes"} {
		blockedRelation := operationEventForResearch("generic-"+relationKind, "relation.added", "initiative", map[string]any{"from": "initiative", "to": "child2", "kind": relationKind, "reason": "generic relation must not own membership", "expected_version": 4, "resulting_version": 5})
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{blockedRelation}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 4}}); err == nil {
			t.Fatalf("generic %s relation touching Initiative was accepted", relationKind)
		} else {
			assertFailureKind(t, err, KindRelationContractViolation)
		}
	}
	blocked := operationEventForResearch("blocked-complete", "work.transitioned", "initiative", map[string]any{"from": "needed", "to": "completed", "reason": "test", "expected_version": 4, "resulting_version": 5})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{blocked}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 4}}); err == nil {
		t.Fatal("Initiative completed with required nonterminal child")
	} else {
		assertFailureKind(t, err, KindInitiativeCompletionBlocked)
	}
	reorder, _ := InitiativeEntryEvent("reorder", "initiative_entry.reordered", "initiative", InitiativeEntry{ChildWorkID: "child2", Position: 0}, "test", time.Unix(4, 1).UTC(), 4)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{reorder}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 4}}); err != nil {
		t.Fatal(err)
	}
	requiredness, _ := InitiativeEntryEvent("optional", "initiative_entry.requiredness_changed", "initiative", InitiativeEntry{ChildWorkID: "child", Required: false}, "test", time.Unix(4, 2).UTC(), 5)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{requiredness}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 5}}); err != nil {
		t.Fatal(err)
	}
	complete := operationEventForResearch("complete-initiative", "work.transitioned", "initiative", map[string]any{"from": "needed", "to": "completed", "reason": "test", "expected_version": 6, "resulting_version": 7})
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{complete}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 6}}); err != nil {
		t.Fatal(err)
	}
}

func TestReadInitiativeEntriesRejectsOverflow(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	tx, err := s.DatabaseForTesting().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	insertWork := `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?, 'task', ?, 'needed', 1, 1, 'now', 'now')`
	if _, err := tx.Exec(insertWork, "initiative", "Initiative"); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	for i := 0; i <= maxInitiativeEntriesRead; i++ {
		childID := fmt.Sprintf("child-%04d", i)
		if _, err := tx.Exec(insertWork, childID, childID); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO initiative_entries(initiative_work_id,child_work_id,position,required) VALUES(?,?,?,0)`, "initiative", childID, i); err != nil {
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
	if _, err := ReadInitiativeEntries(context.Background(), s, "initiative"); err == nil {
		t.Fatal("Initiative entry read silently truncated overflow")
	} else {
		assertFailureKind(t, err, KindInvalidOperation)
		var failure *Failure
		if !failureAs(err, &failure) || failure.RetrySafe {
			t.Fatalf("overflow failure=%v, want non-retryable typed failure", err)
		}
	}
}

func TestInitiativeRejectsNestedAndCrossProductEntries(t *testing.T) {
	t.Parallel()
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
	for _, item := range []struct{ id, kind, project string }{{"initiative", "initiative", "pr1"}, {"nested", "initiative", "pr1"}, {"cross", "task", "pr2"}} {
		events = append(events,
			Event{EventID: item.id + "-created", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: item.id, Actor: "test", OccurredAt: time.Unix(2, int64(len(events))).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": item.kind, "title": item.id, "priority": 1})},
			Event{EventID: item.id + "-project", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: item.id, Actor: "test", OccurredAt: time.Unix(3, int64(len(events))).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(map[string]any{"work_id": item.id, "project_id": item.project, "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		)
	}
	expected := map[SubjectRef]int64{VersionRef(SubjectProduct, "p1"): 0, VersionRef(SubjectProject, "pr1"): 0, VersionRef(SubjectProduct, "p2"): 0, VersionRef(SubjectProject, "pr2"): 0}
	for _, id := range []string{"initiative", "nested", "cross"} {
		expected[VersionRef(SubjectWorkItem, id)] = 0
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: expected}); err != nil {
		t.Fatal(err)
	}
	nested, _ := InitiativeEntryEvent("nested-entry", "initiative_entry.added", "initiative", InitiativeEntry{ChildWorkID: "nested", Position: 0}, "test", time.Unix(4, 0).UTC(), 2)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{nested}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}}); err == nil {
		t.Fatal("nested Initiative entry succeeded")
	} else {
		assertFailureKind(t, err, KindInitiativeScopeViolation)
	}
	cross, _ := InitiativeEntryEvent("cross-entry", "initiative_entry.added", "initiative", InitiativeEntry{ChildWorkID: "cross", Position: 0}, "test", time.Unix(4, 1).UTC(), 2)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{cross}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "initiative"): 2}}); err == nil {
		t.Fatal("cross-Product Initiative entry succeeded")
	} else {
		assertFailureKind(t, err, KindInitiativeScopeViolation)
	}
}

func TestObsoleteEpicEventsAreNotRegistered(t *testing.T) {
	t.Parallel()
	if _, err := InitiativeEntryEvent("obsolete", "epic_entry.added", "initiative", InitiativeEntry{ChildWorkID: "child"}, "test", time.Unix(4, 2).UTC(), 2); err == nil {
		t.Fatal("obsolete Epic event remained registered")
	} else {
		assertFailureKind(t, err, KindInvalidOperation)
	}
	s := openTemp(t)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{EventID: "obsolete-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "obsolete-work", Actor: "test", OccurredAt: time.Unix(4, 3).UTC(), PayloadVersion: 2, Payload: mustJSONBytes(map[string]any{"work_kind": "epic", "title": "obsolete", "priority": 1})}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "obsolete-work"): 0}})
	if err == nil {
		t.Fatal("obsolete Epic work kind was accepted by the store")
	}
}

func operationEventForResearch(id, kind string, subjectID string, payload map[string]any) Event {
	return Event{EventID: id, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: subjectID, Actor: "test", OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 1, Payload: mustJSONBytes(payload)}
}

// A consumed revision is immutable, so a researcher who learns one more thing must
// append a successor. Content carries forward into the successor, so adding one
// finding never needs a re-entry of every finding already gathered, and
// freshness follows whether the brief was actually restated.
func TestAppendRevisionCarriesContentForward(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	seed := func(t *testing.T) (*Store, ResearchPack) {
		t.Helper()
		s := openTemp(t)
		seedResearchWork(t, s, "owner", "consumer")
		pack, err := CreateResearchPack(ctx, s, CreateResearchPackRequest{
			Identity:    researchIdentity("seed-pack"),
			OwnerWorkID: "owner",
			Freshness:   ResearchCurrent,
			Revision:    ResearchRevisionInput{Question: "does it hold?", ScopeIn: json.RawMessage(`["a"]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`["proven"]`), Method: "read the source"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddResearchSource(ctx, ResearchSourceRequest{Identity: researchIdentity("seed-source"), PackID: pack.PackID, ExpectedVersion: 1, Source: ResearchSource{SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.invalid/doc", Title: "Doc", PublisherOrAuthor: "Example", AccessedAt: "2026-01-01T00:00:00Z"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("seed-finding"), PackID: pack.PackID, ExpectedVersion: 2, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "it holds", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"s1"}}}); err != nil {
			t.Fatal(err)
		}
		return s, pack
	}

	sameBrief := ResearchRevisionInput{Question: "does it hold?", ScopeIn: json.RawMessage(`["a"]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`["proven"]`), Method: "read the source"}

	t.Run("unchanged brief preserves assessed freshness", func(t *testing.T) {
		s, pack := seed(t)
		if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("append-same"), PackID: pack.PackID, ExpectedVersion: 3, Revision: sameBrief}); err != nil {
			t.Fatal(err)
		}
		got, err := GetResearchPack(ctx, s, pack.PackID, 1000)
		if err != nil {
			t.Fatal(err)
		}
		rev := revisionByNumber(t, got, 2)
		if len(rev.Findings) != 1 || rev.Findings[0].FindingID != "f1" {
			t.Fatalf("findings=%+v, want the prior finding carried forward", rev.Findings)
		}
		if rev.Findings[0].Freshness != ResearchCurrent {
			t.Fatalf("freshness=%q, want the assessed freshness preserved", rev.Findings[0].Freshness)
		}
		if len(rev.Findings[0].SourceIDs) != 1 || rev.Findings[0].SourceIDs[0] != "s1" {
			t.Fatalf("citations=%v, want the citation link carried forward", rev.Findings[0].SourceIDs)
		}
		if len(rev.Sources) != 1 || rev.Sources[0].SourceID != "s1" {
			t.Fatalf("sources=%+v, want the prior source carried forward", rev.Sources)
		}

	})

	t.Run("restated brief degrades copied findings to unknown", func(t *testing.T) {
		s, pack := seed(t)
		restated := sameBrief
		restated.Question = "does it still hold after the rewrite?"
		if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("append-restated"), PackID: pack.PackID, ExpectedVersion: 3, Revision: restated}); err != nil {
			t.Fatal(err)
		}
		got, err := GetResearchPack(ctx, s, pack.PackID, 1000)
		if err != nil {
			t.Fatal(err)
		}
		rev := revisionByNumber(t, got, 2)
		if len(rev.Findings) != 1 {
			t.Fatalf("findings=%+v, want the prior finding carried forward", rev.Findings)
		}
		if rev.Findings[0].Freshness != ResearchUnknown {
			t.Fatalf("freshness=%q, want unknown under a restated brief", rev.Findings[0].Freshness)
		}
		if rev.Findings[0].Statement != "it holds" || rev.Findings[0].Confidence != ConfidenceHigh {
			t.Fatalf("finding=%+v, want content preserved apart from freshness", rev.Findings[0])
		}
		// Pack freshness stays untouched: it is pack-scoped while consumers pin
		// exact revisions, so a restatement must not reach consumers of older ones.
		if got.Freshness != ResearchCurrent {
			t.Fatalf("pack freshness=%q, want the pinned-consumer guarantee preserved", got.Freshness)
		}
	})

	t.Run("the pinned revision is untouched", func(t *testing.T) {
		s, pack := seed(t)
		if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 3, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseContext, Required: true, AcceptedAt: "2026-01-01T00:00:00Z"}}); err != nil {
			t.Fatal(err)
		}
		restated := sameBrief
		restated.Question = "a different question entirely"
		if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("append-after-bind"), PackID: pack.PackID, ExpectedVersion: 4, Revision: restated}); err != nil {
			t.Fatal(err)
		}
		got, err := GetResearchPack(ctx, s, pack.PackID, 1000)
		if err != nil {
			t.Fatal(err)
		}
		pinned := revisionByNumber(t, got, 1)
		if len(pinned.Findings) != 1 || pinned.Findings[0].Freshness != ResearchCurrent {
			t.Fatalf("pinned revision=%+v, want the consumer's content unchanged", pinned.Findings)
		}
	})
}

func revisionByNumber(t *testing.T, pack ResearchPack, revision int64) ResearchRevision {
	t.Helper()
	for _, r := range pack.Revisions {
		if r.Revision == revision {
			return r
		}
	}
	t.Fatalf("revision %d absent from pack", revision)
	return ResearchRevision{}
}

// Active research findings use the same applies-to vocabulary as durable knowledge.
// Owner work says where the research was found; this scope says what one finding
// applies to, which can be a different component, project, or tag.
func TestResearchFindingScopesAreValidatedReadBackAndCopied(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack, err := CreateResearchPack(ctx, s, CreateResearchPackRequest{
		Identity: researchIdentity("scope-pack"), OwnerWorkID: "owner", Freshness: ResearchCurrent,
		Revision: ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`[]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`[]`), Method: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	explicit := ResearchScopes{Mode: "explicit", ProductIDs: []string{"product"}, ProjectIDs: []string{"project"}, DomainIDs: []string{"api"}, TagIDs: []string{"security"}}
	if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("scoped-finding"), PackID: pack.PackID, ExpectedVersion: 1, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "scoped", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, Scopes: explicit}}); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendResearchRevision(ctx, s, AppendResearchRevisionRequest{Identity: researchIdentity("scope-append"), PackID: pack.PackID, ExpectedVersion: 2, Revision: ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`[]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`[]`), Method: "m"}}); err != nil {
		t.Fatal(err)
	}
	got, err := GetResearchPack(ctx, s, pack.PackID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range got.Revisions {
		if len(revision.Findings) != 1 || !reflect.DeepEqual(revision.Findings[0].Scopes, explicit) {
			t.Fatalf("revision %d scope=%+v, want %+v", revision.Revision, revision.Findings, explicit)
		}
	}

	// Switching an explicit finding back to home must delete its old rows before
	// the structural home guard permits the mode change.
	if _, err := s.UpdateResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("scope-home-update"), PackID: pack.PackID, Revision: 2, ExpectedVersion: 3, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "scoped", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, Scopes: ResearchScopes{Mode: "home"}}}); err != nil {
		t.Fatal(err)
	}
	updated, err := GetResearchPack(ctx, s, pack.PackID, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if scopes := revisionByNumber(t, updated, 2).Findings[0].Scopes; scopes.Mode != "home" || len(scopes.ProductIDs)+len(scopes.ProjectIDs)+len(scopes.DomainIDs)+len(scopes.TagIDs) != 0 {
		t.Fatalf("updated scope=%+v, want empty home scope", scopes)
	}

	for _, tc := range []struct {
		name  string
		scope ResearchScopes
	}{
		{"home with IDs", ResearchScopes{Mode: "home", ProductIDs: []string{"product"}}},
		{"explicit empty", ResearchScopes{Mode: "explicit"}},
		{"unknown product", ResearchScopes{Mode: "explicit", ProductIDs: []string{"missing"}}},
		{"duplicate component", ResearchScopes{Mode: "explicit", DomainIDs: []string{"api", "api"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("invalid-scope-" + tc.name), PackID: pack.PackID, ExpectedVersion: 4, Finding: ResearchFinding{FindingID: "bad-" + tc.name, Kind: FindingObservation, Statement: "bad", Confidence: ConfidenceLow, Freshness: ResearchCurrent, Status: FindingActive, Scopes: tc.scope}})
			if err == nil {
				t.Fatal("scope validation accepted invalid scope")
			}
		})
	}
}

// A research source keeps its full provenance (kind, locator, title, author,
// publication and access times) across a store close and reopen, so a later
// session reads exactly what the authoring session recorded. Once a consumer
// pins the revision, the source refuses a rewrite and its provenance stays as
// recorded.
func TestResearchSourceProvenanceSurvivesReopenAndConsumption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concord.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "provenance", "owner")
	recorded := ResearchSource{PackID: pack.PackID, Revision: 1, SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com/spec", Title: "Example specification", PublisherOrAuthor: "Example Org", PublishedAt: "2026-07-01T00:00:00Z", AccessedAt: "2026-08-07T00:00:00Z"}
	if _, err := s.AddResearchSource(ctx, ResearchSourceRequest{Identity: researchIdentity("provenance-source"), PackID: pack.PackID, ExpectedVersion: 1, Source: recorded}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddResearchFinding(ctx, ResearchFindingRequest{Identity: researchIdentity("provenance-finding"), PackID: pack.PackID, ExpectedVersion: 2, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"s1"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := BindResearchConsumer(ctx, s, BindResearchConsumerRequest{Identity: researchIdentity("provenance-bind"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 3, Consumer: ResearchConsumer{ConsumerWorkID: "consumer", UseRole: UseDecisionBasis, Required: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	readSource := func() ResearchSource {
		t.Helper()
		got, err := GetResearchPack(ctx, reopened, pack.PackID, 1000)
		if err != nil || len(got.Revisions) != 1 || len(got.Revisions[0].Sources) != 1 || len(got.Revisions[0].Findings) != 1 {
			t.Fatalf("reopened pack=%+v err=%v, want one revision with one source and one finding", got, err)
		}
		if ids := got.Revisions[0].Findings[0].SourceIDs; len(ids) != 1 || ids[0] != "s1" {
			t.Fatalf("finding source ids=%v, want the finding to cite s1", ids)
		}
		return got.Revisions[0].Sources[0]
	}
	if got := readSource(); !reflect.DeepEqual(got, recorded) {
		t.Fatalf("reopened source=%+v, want the recorded provenance %+v", got, recorded)
	}
	rewritten := recorded
	rewritten.Locator = "https://example.com/other"
	rewritten.PublisherOrAuthor = "Someone Else"
	if _, err := reopened.UpdateResearchSource(ctx, ResearchSourceRequest{Identity: researchIdentity("provenance-rewrite"), PackID: pack.PackID, Revision: 1, ExpectedVersion: 4, Source: rewritten}); err == nil {
		t.Fatal("a consumed revision accepted a source rewrite")
	} else {
		assertFailureKind(t, err, KindResearchRevisionImmutable)
	}
	if got := readSource(); !reflect.DeepEqual(got, recorded) {
		t.Fatalf("source after the refused rewrite=%+v, want the recorded provenance %+v", got, recorded)
	}
}

// A workflow action that declares research reliance is refused at the
// declaration boundary when the pack is missing or a required binding pins a
// stale revision. A retained terminal-owner pack stays pinnable (CD-0216). A
// refusal records no consumer pin. A current required binding and a
// non-required stale binding each record exactly one pin.
func TestResearchRelianceRefusesUnprovableBindingsAndPinsProvableOnes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "retired-owner", "consumer")
	current := createSimplePack(t, s, "reliance-current", "owner")
	stale := createSimplePack(t, s, "reliance-stale", "owner")
	if err := SetResearchFreshness(ctx, s, SetResearchFreshnessRequest{Identity: researchIdentity("reliance-stale-set"), PackID: stale.PackID, ExpectedVersion: 1, Freshness: ResearchStale}); err != nil {
		t.Fatal(err)
	}
	retained := createSimplePack(t, s, "reliance-retained", "retired-owner")
	terminalizeResearchOwner(t, s, "retired-owner")
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		declaration ResearchBindingDeclaration
		refusal     FailureKind
	}{
		{"missing pack", ResearchBindingDeclaration{PackID: "no-such-pack", Revision: 1, UseRole: UseDecisionBasis, Required: true}, KindProjectionNotFound},
		{"required stale", ResearchBindingDeclaration{PackID: stale.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}, KindResearchConsumerBlocked},
		{"required current", ResearchBindingDeclaration{PackID: current.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}, ""},
		{"optional stale", ResearchBindingDeclaration{PackID: stale.PackID, Revision: 1, UseRole: UseContext, Required: false}, ""},
		{"retained terminal owner", ResearchBindingDeclaration{PackID: retained.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = BindResearchRelianceTx(ctx, tx, "consumer", []ResearchBindingDeclaration{tc.declaration}, now)
			if tc.refusal != "" {
				if err == nil {
					t.Fatalf("binding %+v was admitted, want %s", tc.declaration, tc.refusal)
				}
				assertFailureKind(t, err, tc.refusal)
			} else if err != nil {
				t.Fatalf("binding %+v was refused: %v", tc.declaration, err)
			}
			var pins int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM active_research_consumers WHERE consumer_work_id=?`, "consumer").Scan(&pins); err != nil {
				t.Fatal(err)
			}
			want := 1
			if tc.refusal != "" {
				want = 0
			}
			if pins != want {
				t.Fatalf("consumer pins=%d, want %d", pins, want)
			}
		})
	}
}
