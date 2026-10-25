package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func seedResearchWork(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	if _, err := s.CreateProductWithProject(context.Background(), ProductCreation{
		ProductID: "product", DisplayName: "Product", StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: "project", ProjectDisplayName: "Project", Role: "primary", Reason: "test",
	}); err != nil {
		t.Fatal(err)
	}
	var events []Event
	for i, id := range ids {
		events = append(events,
			Event{EventID: "research-work-" + id, Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "test", OccurredAt: time.Unix(2, int64(i)).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: mustJSONValue(map[string]any{"work_kind": "research", "title": id, "priority": 1})},
			Event{EventID: "research-work-project-" + id, Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "test", OccurredAt: time.Unix(3, int64(i)).UTC(), PayloadVersion: 1, Payload: mustJSONValue(map[string]any{"work_id": id, "project_id": "project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2})},
		)
	}
	expected := make(map[SubjectRef]int64, len(ids))
	for _, id := range ids {
		expected[VersionRef(SubjectWorkItem, id)] = 0
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: events, ExpectedVersions: expected}); err != nil {
		t.Fatal(err)
	}
}

func TestActiveResearchRevisionBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	create := CreateResearchPackRequest{OwnerWorkID: "owner", Revision: ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`{"in":true}`), ScopeOut: json.RawMessage(`{"out":false}`), DoneWhen: json.RawMessage(`{"done":true}`), Method: "docs"}}
	pack, err := createResearchPack(ctx, s, create)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 1, Revision: ResearchRevisionInput{Question: "q2", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "source"}})
	if err != nil || rev.Revision != 2 {
		t.Fatalf("append = %+v, %v", rev, err)
	}
	if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 1, Revision: ResearchRevisionInput{Question: "q3", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "source"}}); err == nil {
		t.Fatal("stale append succeeded")
	} else {
		assertFailureKind(t, err, KindVersionConflict)
	}
	beforeEvents := currentEffectRows(t, s)
	if _, err := recordResearchSource(ctx, s, ResearchSourceRequest{PackID: pack.PackID, ExpectedVersion: 2, Source: ResearchSource{SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com", Title: "Example", PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 3, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"s1"}}}); err != nil {
		t.Fatal(err)
	}
	complete, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
	if err != nil || len(complete.Revisions) != 2 || len(complete.Revisions[1].Sources) != 1 || len(complete.Revisions[1].Findings) != 1 || len(complete.Revisions[1].Findings[0].SourceIDs) != 1 {
		t.Fatalf("complete research pack = %+v, %v", complete, err)
	}
	if got := currentEffectRows(t, s); !reflect.DeepEqual(got, beforeEvents) {
		t.Fatalf("research content changed effect receipts: %v -> %v", beforeEvents, got)
	}
	pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 2, UseRole: UseContext, Required: true})
	pinned, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
	if err != nil || len(pinned.Consumers) != 1 || pinned.Consumers[0].Revision != 2 {
		t.Fatalf("consumer pins = %+v, %v", pinned.Consumers, err)
	}
	if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, Revision: 2, ExpectedVersion: 5, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "updated", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}}); err == nil {
		t.Fatal("consumed revision update succeeded")
	} else {
		assertFailureKind(t, err, KindResearchRevisionImmutable)
	}
	if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 5, Revision: ResearchRevisionInput{Question: "q3", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "source"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 6, Finding: ResearchFinding{FindingID: "f3", Kind: FindingConclusion, Statement: "new current", Confidence: ConfidenceMedium, Freshness: ResearchCurrent, Status: FindingActive}}); err != nil {
		t.Fatal(err)
	}
	if err := bindResearchReliance(ctx, s, "consumer"); err != nil {
		t.Fatalf("required current pin refused: %v", err)
	}
	// Issue #122: freshness targets the revision the consumer pins, not the
	// pack. A pack-level set does not poison unchanged revisions, and an
	// unrelated append does not un-stale pinned content.
	if err := setResearchFreshness(ctx, s, SetResearchFreshnessRequest{PackID: pack.PackID, ExpectedVersion: 7, Freshness: ResearchStale}); err != nil {
		t.Fatal(err)
	}
	// That set targeted the CURRENT revision (3); the consumer pinned to
	// revision 2 is unaffected by another revision's staleness.
	if err := bindResearchReliance(ctx, s, "consumer"); err != nil {
		t.Fatalf("consumer pinned to unchanged revision poisoned: %v", err)
	}
	// Staling the pinned revision blocks the consumer...
	if err := setResearchFreshness(ctx, s, SetResearchFreshnessRequest{PackID: pack.PackID, ExpectedVersion: 8, Freshness: ResearchStale, Revision: 2}); err != nil {
		t.Fatal(err)
	}
	assertRequiredPinBlocked(t, bindResearchReliance(ctx, s, "consumer"), ResearchStale)
	// ...and stays blocked across an unrelated append (two revisions of one
	// pack disagree: current 4 is fresh, pinned 2 is stale).
	if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 9, Revision: ResearchRevisionInput{Question: "q4", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "docs"}}); err != nil {
		t.Fatal(err)
	}
	assertRequiredPinBlocked(t, bindResearchReliance(ctx, s, "consumer"), ResearchStale)
	// Retention cutover: deletion is retire-only. The owner is still needed
	// here, so terminalize it to isolate the pin protection this assertion
	// owns: the active required pin keeps the pack protected.
	terminalizeResearchOwner(t, s, "owner")
	out, err := retireResearchForTest(t, s, RetireResearchPacksRequest{ProductID: "product", Candidates: []ResearchRetirementCandidate{{PackID: pack.PackID, ExpectedVersion: 10}}})
	if err != nil || len(out.Candidates) != 1 || out.Candidates[0].Classification != ResearchRetirementProtected || out.Candidates[0].ProtectionReason != "active_pin" {
		t.Fatalf("retirement with required active consumer=%+v err=%v", out, err)
	}
	if countRows(t, s, "active_research_packs") != 1 {
		t.Fatal("active required pin did not protect the pack")
	}
}

func TestResearchRecordUpdatesAnExistingFindingAndSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner")
	pack := createSimplePack(t, s, "write-modes", "owner")
	finding := ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "Original finding", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}
	source := ResearchSource{SourceID: "s1", Kind: SourceCode, Locator: "synthetic/source.go", Title: "Original source", PublisherOrAuthor: "Synthetic fixture", AccessedAt: "2026-08-07T00:00:00Z"}
	if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 1, Finding: finding}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordResearchSource(ctx, s, ResearchSourceRequest{PackID: pack.PackID, ExpectedVersion: 2, Source: source}); err != nil {
		t.Fatal(err)
	}
	source.Title = "Updated source"
	updatedSource, err := recordResearchSource(ctx, s, ResearchSourceRequest{PackID: pack.PackID, ExpectedVersion: 3, Source: source})
	if err != nil || updatedSource.Title != source.Title {
		t.Fatalf("source update=%+v err=%v", updatedSource, err)
	}
	finding.Statement = "Updated finding"
	updatedFinding, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 4, Finding: finding})
	if err != nil || updatedFinding.Statement != finding.Statement {
		t.Fatalf("finding update=%+v err=%v", updatedFinding, err)
	}
}

func TestActiveResearchNonrequiredConsumerDoesNotBlock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "optional")
	pack, err := createResearchPack(ctx, s, CreateResearchPackRequest{OwnerWorkID: "owner", Revision: ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "docs"}})
	if err != nil {
		t.Fatal(err)
	}
	pinResearch(t, s, "optional", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext})
	if err := setResearchFreshness(ctx, s, SetResearchFreshnessRequest{PackID: pack.PackID, ExpectedVersion: 2, Freshness: ResearchUnknown}); err != nil {
		t.Fatal(err)
	}
	if err := bindResearchReliance(ctx, s, "optional"); err != nil {
		t.Fatalf("optional pin on unknown freshness blocked: %v", err)
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

func simpleResearchRevision() ResearchRevisionInput {
	return ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "docs"}
}

func createSimplePack(t *testing.T, s *Store, key, owner string) ResearchPack {
	t.Helper()
	pack, err := createResearchPack(context.Background(), s, CreateResearchPackRequest{PackID: key + "-pack", OwnerWorkID: owner, Revision: simpleResearchRevision()})
	if err != nil {
		t.Fatal(err)
	}
	return pack
}

// bindResearchReliance records consumer pins through the reliance boundary a
// consuming workflow action applies inside its transaction.
func bindResearchReliance(ctx context.Context, s *Store, consumerWorkID string, declarations ...ResearchBindingDeclaration) error {
	return s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "research_reliance")
		if err != nil {
			return err
		}
		return BindResearchRelianceTx(ctx, tx, consumerWorkID, declarations, transaction.now())
	})
}

// assertRequiredPinBlocked asserts the fail-closed refusal a required pin on
// non-current freshness produces.
func assertRequiredPinBlocked(t *testing.T, err error, freshness ResearchFreshness) {
	t.Helper()
	assertFailureKind(t, err, KindResearchConsumerBlocked)
	if !strings.Contains(err.Error(), "on "+string(freshness)+" freshness") {
		t.Fatalf("required pin refusal=%v, want %s freshness", err, freshness)
	}
}

func pinResearch(t *testing.T, s *Store, consumerWorkID string, declarations ...ResearchBindingDeclaration) {
	t.Helper()
	if err := bindResearchReliance(context.Background(), s, consumerWorkID, declarations...); err != nil {
		t.Fatal(err)
	}
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
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES(?, 'work_note', ?, '2026-08-07T00:00:00Z', 'completed', '[]', 'completed', 1, 'durable summary', 'home', 'locator', 'notes/`+id+`.md', 'commit', 'hash'); DELETE FROM write_guard`, id, id); err != nil {
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
	pack, err := reopened.ReadResearchPack(ctx, ResearchReadRequest{PackID: "persist-pack", Limit: 1000})
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
	if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 1, Revision: simpleResearchRevision()}); err != nil {
		t.Fatal(err)
	}
	pinResearch(t, s, "consumer-a", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true})
	pinResearch(t, s, "consumer-b", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 2, UseRole: UseDesignInput, Required: true})
	got, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
	if err != nil || len(got.Consumers) != 2 || got.Consumers[0].Revision == got.Consumers[1].Revision {
		t.Fatalf("pinned consumers=%+v err=%v", got.Consumers, err)
	}
}

func TestRetainedOwnerContentIsReadOnlyExceptFreshness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	pack := createSimplePack(t, s, "retained-readonly", "owner")
	pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext})
	terminalizeResearchOwner(t, s, "owner")
	// A freshness review stays available: staling a retained revision is the
	// one retained-owner write the retention cutover keeps.
	if err := setResearchFreshness(ctx, s, SetResearchFreshnessRequest{PackID: pack.PackID, ExpectedVersion: 2, Freshness: ResearchStale}); err != nil {
		t.Fatalf("retained freshness review refused: %v", err)
	}
	authoring := []struct {
		name string
		run  func() error
	}{
		{"append", func() error {
			_, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 3, Revision: simpleResearchRevision()})
			return err
		}},
		{"finding", func() error {
			_, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 3, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive}})
			return err
		}},
		{"source", func() error {
			_, err := recordResearchSource(ctx, s, ResearchSourceRequest{PackID: pack.PackID, ExpectedVersion: 3, Source: ResearchSource{SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com", Title: "Source", PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}})
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
	// Releasing a pin is not authoring: the consumer's terminal transition
	// still releases its optional pin on the retained pack.
	terminalizeResearchOwner(t, s, "consumer")
	got, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
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
	if got, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000}); err != nil || got.PackID != pack.PackID {
		t.Fatalf("unlinked terminal pack=%+v err=%v", got, err)
	}
	other := createSimplePack(t, s, "unrelated-active", "other")
	if err := setResearchFreshness(ctx, s, SetResearchFreshnessRequest{PackID: other.PackID, ExpectedVersion: 1, Freshness: ResearchStale}); err != nil {
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
	if got, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000}); err != nil || got.PackID != pack.PackID {
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
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "consumer")
	activePack := createSimplePack(t, s, "guard-active", "owner")
	retainedPack := createSimplePack(t, s, "guard-retained", "owner")
	pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: retainedPack.PackID, Revision: 1, UseRole: UseContext})
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
	req.Reason = "shelved"
	if _, err := s.RemoveWork(ctx, req); err == nil {
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
	if _, err := s.RemoveWork(ctx, req); err != nil {
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
	pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext})
	req := removalTestRequest()
	req.OperationID = "remove-research-consumer"
	req.IdempotencyKey = "remove-research-consumer-key"
	req.WorkID = "consumer"
	req.ExpectedVersion = 2
	req.Reason = "shelved"
	if _, err := s.RemoveWork(ctx, req); err != nil {
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
	pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext})
	terminalizeResearchOwner(t, s, "owner")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
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
	if version != CurrentSchemaVersion() {
		t.Fatalf("schema version=%d, want %d", version, CurrentSchemaVersion())
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
	synthExecPath(t, path, "fold_guard", `DELETE FROM work_items WHERE id='owner'`)
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
	pinResearch(t, s, "consumer-a", ResearchBindingDeclaration{PackID: packA.PackID, Revision: 1, UseRole: UseContext, Required: true})
	terminalizeResearchOwner(t, s, "owner-a")
	linkArchivedResearchOwner(t, s, "owner-a")
	packB := createSimplePack(t, s, "unrelated-owner", "owner-b")
	if err := setResearchFreshness(ctx, s, SetResearchFreshnessRequest{PackID: packB.PackID, ExpectedVersion: 1, Freshness: ResearchStale}); err != nil {
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
			for _, pack := range []ResearchPack{packA, packB} {
				pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true})
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
		pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true})
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='compaction_link.published'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("compaction events=%d, want 1", events)
	}
	if countRows(t, s, "active_research_packs") != 1 || countRows(t, s, "active_research_consumers") != 1 {
		t.Fatal("publication destroyed retained research")
	}
	if got, err := s.ReadResearchPack(context.Background(), ResearchReadRequest{PackID: pack.PackID, Limit: 1000}); err != nil || got.PackID != pack.PackID {
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
	payload, err := json.Marshal(compactionLinkPayload{ID: "owner", Type: "work_note", Title: note.Title, CompletedAt: note.CompletedAt, OutcomeTag: note.OutcomeTag, LessonTags: note.LessonTags, TerminalState: note.TerminalState, Priority: note.Priority, Summary: note.Summary, ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs, DomainIDs: note.DomainIDs, TagIDs: note.TagIDs, HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID, NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash, Reason: "direct fold test", ExpectedVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "direct-retained-compaction", Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: "owner", Actor: "test", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), PayloadVersion: eventKindRegistry["compaction_link.published"].CurrentVersion, Payload: payload}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "owner"): 3}}); err != nil {
		t.Fatalf("compaction fold refused a required active consumer: %v", err)
	}
	if countRows(t, s, "archived_work") != 1 {
		t.Fatal("fold did not record the archive")
	}
	if countRows(t, s, "active_research_packs") != 1 || countRows(t, s, "active_research_consumers") != 1 {
		t.Fatalf("fold destroyed retained research: packs=%d consumers=%d", countRows(t, s, "active_research_packs"), countRows(t, s, "active_research_consumers"))
	}
	if _, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000}); err != nil {
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
	pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true})
	secret := "SECRET-RESEARCH-PACK-BODY"
	if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 2, Revision: ResearchRevisionInput{Question: secret, ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`), Method: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordResearchSource(ctx, s, ResearchSourceRequest{PackID: pack.PackID, Revision: 2, ExpectedVersion: 3, Source: ResearchSource{SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.com", Title: "Source", PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, Revision: 2, ExpectedVersion: 4, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"s1"}}}); err != nil {
		t.Fatal(err)
	}
	terminalizeResearchOwner(t, s, "owner")
	home := KnowledgeHome{HomeProjectID: "home", HomeLocatorID: "owner-repo", RepoPath: repo, HeadRef: "HEAD"}
	request := compactionRequest(home, commit, path, "retained-proof-compaction")
	if err := PublishCompactionLink(ctx, s, request); err != nil {
		t.Fatal(err)
	}
	// Publication is proof-backed archive, not destruction: the pack body
	// stays in direct-table authority with its required consumer.
	for _, table := range []string{"active_research_packs", "active_research_revisions", "active_research_findings", "active_research_sources", "active_research_finding_sources", "active_research_consumers"} {
		if countRows(t, s, table) == 0 {
			t.Fatalf("%s lost rows after proof-backed compaction", table)
		}
	}
	if got, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000}); err != nil || len(got.Revisions) != 2 {
		t.Fatalf("retained pack after publication=%+v err=%v", got, err)
	}
	var bodySummary int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM archived_work WHERE summary LIKE ?`, "%"+secret+"%").Scan(&bodySummary); err != nil {
		t.Fatal(err)
	}
	if bodySummary != 0 {
		t.Fatalf("retained pack body leaked to %d archive summaries", bodySummary)
	}
	note, err := VerifyCommittedNote(ctx, home.RepoPath, commit, path, "")
	if err != nil || strings.Contains(string(note.Content), secret) {
		t.Fatalf("Git authority contains retained pack body: err=%v", err)
	}
	expected := compactionLinkPayload{
		ID: note.ID, Type: note.Kind, Title: note.Title, CompletedAt: note.CompletedAt,
		OutcomeTag: note.OutcomeTag, LessonTags: note.LessonTags, TerminalState: note.TerminalState,
		Priority: note.Priority, Summary: note.Summary, SuccessorID: note.SuccessorID,
		ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs, DomainIDs: []string{"auth"}, TagIDs: note.TagIDs,
		HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID,
		NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash,
		Reason: request.Reason, ExpectedVersion: request.ExpectedVersion,
	}
	wantDigest, err := effectPayloadDigest(mustJSONValue(expected))
	if err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=? AND kind='compaction_link.published' AND subject_type='work_item' AND subject_id=?`, request.EventID, request.WorkID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != wantDigest {
		t.Fatalf("publication digest=%q, want the exact bounded note/request with no research body: %q", digest, wantDigest)
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
	payload, err := json.Marshal(compactionLinkPayload{ID: "owner", Type: "work_note", Title: note.Title, CompletedAt: note.CompletedAt, OutcomeTag: note.OutcomeTag, LessonTags: note.LessonTags, TerminalState: note.TerminalState, Priority: note.Priority, Summary: note.Summary, ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs, DomainIDs: note.DomainIDs, TagIDs: note.TagIDs, HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID, NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash, Reason: "proof-backed archive", ExpectedVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "crash-window-link", Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: "owner", Actor: "test", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), PayloadVersion: eventKindRegistry["compaction_link.published"].CurrentVersion, Payload: payload}
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='compaction_link.published'`).Scan(&events); err != nil {
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
	for i, sourceID := range []string{"s1", "s2"} {
		if _, err := recordResearchSource(ctx, s, ResearchSourceRequest{PackID: pack.PackID, ExpectedVersion: int64(1 + i), Source: ResearchSource{SourceID: sourceID, Kind: SourceOfficialDoc, Locator: "https://example.com/" + sourceID, Title: sourceID, PublisherOrAuthor: "Example", AccessedAt: "2026-08-07T00:00:00Z"}}); err != nil {
			t.Fatal(err)
		}
	}
	for i, cited := range []struct {
		finding string
		sources []string
	}{{"f1", []string{"s1", "s2"}}, {"f2", []string{"s1"}}} {
		if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: int64(3 + i), Finding: ResearchFinding{FindingID: cited.finding, Kind: FindingObservation, Statement: cited.finding, Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: cited.sources}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 2}); err == nil {
		t.Fatal("bounded research read silently truncated finding-source links")
	} else {
		assertFailureKind(t, err, KindInvalidOperation)
		var failure *Failure
		if !failureAs(err, &failure) || failure.Op != "research_read" {
			t.Fatalf("overflow failure=%v, want research_read operation", err)
		}
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

func TestObsoleteEpicEventsAreNotRegistered(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{EventID: "obsolete-work", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "obsolete-work", Actor: "test", OccurredAt: time.Unix(4, 3).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: mustJSONValue(map[string]any{"work_kind": "epic", "title": "obsolete", "priority": 1})}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "obsolete-work"): 0}})
	if err == nil {
		t.Fatal("obsolete Epic work kind was accepted by the store")
	}
}

func operationEventForResearch(id, kind string, subjectID string, payload map[string]any) Event {
	return Event{EventID: id, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: subjectID, Actor: "test", OccurredAt: time.Unix(5, 0).UTC(), PayloadVersion: 1, Payload: mustJSONValue(payload)}
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
		pack, err := createResearchPack(ctx, s, CreateResearchPackRequest{
			OwnerWorkID: "owner",
			Freshness:   ResearchCurrent,
			Revision:    ResearchRevisionInput{Question: "does it hold?", ScopeIn: json.RawMessage(`["a"]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`["proven"]`), Method: "read the source"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := recordResearchSource(ctx, s, ResearchSourceRequest{PackID: pack.PackID, ExpectedVersion: 1, Source: ResearchSource{SourceID: "s1", Kind: SourceOfficialDoc, Locator: "https://example.invalid/doc", Title: "Doc", PublisherOrAuthor: "Example", AccessedAt: "2026-01-01T00:00:00Z"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 2, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "it holds", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"s1"}}}); err != nil {
			t.Fatal(err)
		}
		return s, pack
	}

	sameBrief := ResearchRevisionInput{Question: "does it hold?", ScopeIn: json.RawMessage(`["a"]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`["proven"]`), Method: "read the source"}

	t.Run("unchanged brief preserves assessed freshness", func(t *testing.T) {
		s, pack := seed(t)
		if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 3, Revision: sameBrief}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
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
		if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 3, Revision: restated}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
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
		pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseContext, Required: true})
		restated := sameBrief
		restated.Question = "a different question entirely"
		if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 4, Revision: restated}); err != nil {
			t.Fatal(err)
		}
		got, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
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
	pack, err := createResearchPack(ctx, s, CreateResearchPackRequest{
		OwnerWorkID: "owner", Freshness: ResearchCurrent,
		Revision: ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`[]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`[]`), Method: "m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	explicit := ResearchScopes{Mode: "explicit", ProductIDs: []string{"product"}, ProjectIDs: []string{"project"}, DomainIDs: []string{"api"}, TagIDs: []string{"security"}}
	if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 1, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "scoped", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, Scopes: explicit}}); err != nil {
		t.Fatal(err)
	}
	if _, err := appendResearchRevision(ctx, s, AppendResearchRevisionRequest{PackID: pack.PackID, ExpectedVersion: 2, Revision: ResearchRevisionInput{Question: "q", ScopeIn: json.RawMessage(`[]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`[]`), Method: "m"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
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
	if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, Revision: 2, ExpectedVersion: 3, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "scoped", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, Scopes: ResearchScopes{Mode: "home"}}}); err != nil {
		t.Fatal(err)
	}
	updated, err := s.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
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
			_, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 4, Finding: ResearchFinding{FindingID: "bad-" + tc.name, Kind: FindingObservation, Statement: "bad", Confidence: ConfidenceLow, Freshness: ResearchCurrent, Status: FindingActive, Scopes: tc.scope}})
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
	if _, err := recordResearchSource(ctx, s, ResearchSourceRequest{PackID: pack.PackID, ExpectedVersion: 1, Source: recorded}); err != nil {
		t.Fatal(err)
	}
	if _, err := recordResearchFinding(ctx, s, ResearchFindingRequest{PackID: pack.PackID, ExpectedVersion: 2, Finding: ResearchFinding{FindingID: "f1", Kind: FindingObservation, Statement: "observed", Confidence: ConfidenceHigh, Freshness: ResearchCurrent, Status: FindingActive, SourceIDs: []string{"s1"}}}); err != nil {
		t.Fatal(err)
	}
	pinResearch(t, s, "consumer", ResearchBindingDeclaration{PackID: pack.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true})
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
		got, err := reopened.ReadResearchPack(ctx, ResearchReadRequest{PackID: pack.PackID, Limit: 1000})
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
	if _, err := recordResearchSource(ctx, reopened, ResearchSourceRequest{PackID: pack.PackID, Revision: 1, ExpectedVersion: 4, Source: rewritten}); err == nil {
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
// stale or missing revision. A retained terminal-owner pack stays pinnable
// (CD-0216). A refusal writes nothing. A current required binding and a
// non-required stale binding each record exactly one pin.
func TestResearchRelianceRefusesUnprovableBindingsAndPinsProvableOnes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openTemp(t)
	seedResearchWork(t, s, "owner", "retired-owner", "consumer")
	current := createSimplePack(t, s, "reliance-current", "owner")
	stale := createSimplePack(t, s, "reliance-stale", "owner")
	if err := setResearchFreshness(ctx, s, SetResearchFreshnessRequest{PackID: stale.PackID, ExpectedVersion: 1, Freshness: ResearchStale}); err != nil {
		t.Fatal(err)
	}
	retained := createSimplePack(t, s, "reliance-retained", "retired-owner")
	terminalizeResearchOwner(t, s, "retired-owner")
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		declaration ResearchBindingDeclaration
		refusal     FailureKind
		detail      string
	}{
		{"missing pack", ResearchBindingDeclaration{PackID: "no-such-pack", Revision: 1, UseRole: UseDecisionBasis, Required: true}, KindProjectionNotFound, ""},
		{"missing revision", ResearchBindingDeclaration{PackID: current.PackID, Revision: 2, UseRole: UseDecisionBasis, Required: true}, KindProjectionNotFound, "declared research revision does not exist"},
		{"required stale", ResearchBindingDeclaration{PackID: stale.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}, KindResearchConsumerBlocked, ""},
		{"required current", ResearchBindingDeclaration{PackID: current.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}, "", ""},
		{"optional stale", ResearchBindingDeclaration{PackID: stale.PackID, Revision: 1, UseRole: UseContext, Required: false}, "", ""},
		{"retained terminal owner", ResearchBindingDeclaration{PackID: retained.PackID, Revision: 1, UseRole: UseDecisionBasis, Required: true}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var beforeChanges, beforeVersion int64
			if err := tx.QueryRowContext(ctx, `SELECT total_changes(), COALESCE((SELECT expected_version FROM active_research_packs WHERE pack_id=?),0)`, tc.declaration.PackID).Scan(&beforeChanges, &beforeVersion); err != nil {
				t.Fatal(err)
			}
			err = BindResearchRelianceTx(ctx, tx, "consumer", []ResearchBindingDeclaration{tc.declaration}, now)
			if tc.refusal != "" {
				if err == nil {
					t.Fatalf("binding %+v was admitted, want %s", tc.declaration, tc.refusal)
				}
				assertFailureKind(t, err, tc.refusal)
				if tc.detail != "" {
					var failure *Failure
					if !errors.As(err, &failure) || failure.Detail != tc.detail {
						t.Fatalf("binding error=%v, want detail %q", err, tc.detail)
					}
				}
				var afterChanges, afterVersion int64
				if err := tx.QueryRowContext(ctx, `SELECT total_changes(), COALESCE((SELECT expected_version FROM active_research_packs WHERE pack_id=?),0)`, tc.declaration.PackID).Scan(&afterChanges, &afterVersion); err != nil {
					t.Fatal(err)
				}
				if afterVersion != beforeVersion || afterChanges != beforeChanges {
					t.Fatalf("refused binding changed version %d -> %d or database changes %d -> %d", beforeVersion, afterVersion, beforeChanges, afterChanges)
				}
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
