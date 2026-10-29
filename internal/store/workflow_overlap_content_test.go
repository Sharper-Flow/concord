package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func overlapContentFixture(t *testing.T) (*Store, WorkflowArchitectureBinding, string) {
	t.Helper()
	s, _, binding, _, _, _ := architectureValidationFixture(t, "content-left")
	overlapFixtureExec(t, s, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) SELECT home_project_id,home_locator_id,product_id,'extra','Extra','Synthetic Domain',status,registry_content_hash,scanned_commit_oid FROM domains WHERE domain_id='child'`)
	seedWork(t, s, "content-right")
	overlapFixtureExec(t, s, `UPDATE work_items SET intent_json=json_set(intent_json,'$.governing_requirements',json('[]')) WHERE id IN ('content-left','content-right')`)
	actor, _ := seedProductChangingContract(t, s, "content-left", binding)
	seedProductChangingContract(t, s, "content-right", binding)
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return s, binding, ref
}

// resolveContentPair settles the pair through the live writer. Both kinds it
// serves are routine coordination answers, so the request carries no approval
// claim.
func resolveContentPair(t *testing.T, s *Store, from, to, kind string) {
	t.Helper()
	fromVersion, toVersion := readWorkVersion(t, s, from), readWorkVersion(t, s, to)
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(context.Background(), tx, WorkflowDomainOverlapResolutionRequest{
			EventID: "content-resolution-" + from + "-" + to + "-" + kind, FromWorkID: from, ToWorkID: to,
			FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
			FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind, SharedStep: sequencingSharedStep(kind),
			Reason: "synthetic coordination", Actor: "operator",
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func contentSuccessor(t *testing.T, s *Store, actor string, version int64, binding WorkflowArchitectureBinding, modifies []string) Event {
	t.Helper()
	return pOverlapContractFacts(t, s, contentSuccessorSource(t, s, actor, version, binding, modifies))
}

func contentSuccessorSource(t *testing.T, s *Store, actor string, version int64, binding WorkflowArchitectureBinding, modifies []string) Event {
	t.Helper()
	mandate := []string{"spec:one"}
	for _, addition := range binding.LawAdditions {
		mandate = append(mandate, addition.LawID)
	}
	expected := readWorkVersion(t, s, "content-left")
	return pOverlapContractFacts(t, s, workflowTypedEvent(fmt.Sprintf("content-successor-%d", version), WorkflowContractSuperseded, "content-left", actor, time.Unix(version, 0), expected, map[string]any{
		"previous_contract_version": version - 1, "new_contract_version": version,
		"supersede_reason": "synthetic revision", "audit_evidence": []string{"audit:content"},
		"successor_contract": map[string]any{
			"contract_version": version, "premise": "updated bounded outcome", "outcome_kind": "check",
			"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:content", "immutable_subject_ref": "commit:content", "expected_result": "pass"},
			"required_evidence": []string{"verification", "review"}, "route_conventions": []string{},
			"spec_mandate": mandate, "law_modifies": modifies, "law_boundary_version": 1,
			"law_revisions": []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}},
			"rigor_class":   "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": binding,
		},
	}))
}

func applyContentSuccessor(t *testing.T, s *Store, event Event) {
	t.Helper()
	expected := readWorkVersion(t, s, event.SubjectID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(event.SubjectID, expected)}); err != nil {
		t.Fatal(err)
	}
}

func assertContentCurrent(t *testing.T, s *Store) {
	t.Helper()
	for _, work := range []string{"content-left", "content-right"} {
		if err := InspectWorkflowDomainOverlap(context.Background(), s, work); err != nil {
			t.Fatalf("content-neutral resolution must admit %s: %v", work, err)
		}
	}
	read, err := s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product"})
	if err != nil || len(read.Pairs) != 1 || read.Pairs[0].ResolutionState != "current" {
		t.Fatalf("content-neutral read=%+v err=%v", read.Pairs, err)
	}
	var count, from, to int64
	var invalidated sql.NullInt64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*),from_contract_version,to_contract_version,invalidated_seq FROM workflow_overlap_resolutions`).Scan(&count, &from, &to, &invalidated); err != nil {
		t.Fatal(err)
	}
	if count != 1 || from != 1 || to != 1 || invalidated.Valid {
		t.Fatalf("judgment audit pins changed: count=%d versions=%d/%d invalidated=%v", count, from, to, invalidated)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='workflow.overlap_resolved'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("resolution events=%d err=%v", count, err)
	}
}

func assertContentStale(t *testing.T, s *Store) {
	t.Helper()
	err := InspectWorkflowDomainOverlap(context.Background(), s, "content-left")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap.Overlaps[0].ResolutionState != "stale" {
		t.Fatalf("changed judgment must be stale: %v", err)
	}
	read, err := s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product"})
	if err != nil || len(read.Pairs) != 1 || read.Pairs[0].ResolutionState != "stale" {
		t.Fatalf("stale read=%+v err=%v", read.Pairs, err)
	}
}

func TestOverlapContentNeutralSuccessorKeepsOriginalJudgment(t *testing.T) {
	s, binding, actor := overlapContentFixture(t)
	resolveContentPair(t, s, "content-left", "content-right", ResolutionCompatibleWith)
	applyContentSuccessor(t, s, contentSuccessor(t, s, actor, 2, binding, []string{}))
	applyContentSuccessor(t, s, contentSuccessor(t, s, actor, 3, binding, []string{}))
	assertContentCurrent(t, s)
}

func TestOverlapJudgedFieldsAndSetOrder(t *testing.T) {
	cases := []struct {
		name   string
		change func(*WorkflowArchitectureBinding) []string
		stale  bool
	}{
		{"affected", func(b *WorkflowArchitectureBinding) []string {
			b.AffectedDomainIDs = append(b.AffectedDomainIDs, "extra")
			return nil
		}, true},
		{"law addition", func(b *WorkflowArchitectureBinding) []string {
			b.LawAdditions = []WorkflowLawAddition{{LawID: "law:new", HomeDomainID: "child"}}
			return nil
		}, true},
		{"law modification", func(_ *WorkflowArchitectureBinding) []string { return []string{"spec:one"} }, true},
		{"Domain modification", func(b *WorkflowArchitectureBinding) []string {
			b.DomainModifies = []string{"root", "child"}
			return nil
		}, true},
		{"relation modification", func(b *WorkflowArchitectureBinding) []string {
			b.DomainRelationModifies = []WorkflowDomainRelationModification{}
			return nil
		}, true},
		{"set reorder", func(b *WorkflowArchitectureBinding) []string {
			b.AffectedDomainIDs = []string{"child", "root"}
			return nil
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, binding, actor := overlapContentFixture(t)
			resolveContentPair(t, s, "content-left", "content-right", ResolutionCompatibleWith)
			modifies := tc.change(&binding)
			if modifies == nil {
				modifies = []string{}
			}
			applyContentSuccessor(t, s, contentSuccessor(t, s, actor, 2, binding, modifies))
			if tc.stale {
				assertContentStale(t, s)
			} else {
				assertContentCurrent(t, s)
			}
		})
	}
}

func TestOverlapChangedThenRevertedDoesNotReviveJudgment(t *testing.T) {
	s, original, actor := overlapContentFixture(t)
	resolveContentPair(t, s, "content-left", "content-right", ResolutionCompatibleWith)
	changed := original
	changed.DomainModifies = []string{"child", "root"}
	applyContentSuccessor(t, s, contentSuccessor(t, s, actor, 2, changed, []string{}))
	applyContentSuccessor(t, s, contentSuccessor(t, s, actor, 3, original, []string{}))
	assertContentStale(t, s)
}

func TestOverlapSourceJudgedContentV3ChangedContentInvalidates(t *testing.T) {
	s, original, actor := overlapContentFixture(t)
	resolveContentPair(t, s, "content-left", "content-right", ResolutionCompatibleWith)
	changed := original
	changed.DomainModifies = []string{"child", "root"}
	event := contentSuccessorSource(t, s, actor, 2, changed, []string{})
	applyContentSuccessor(t, s, event)
	assertContentStale(t, s)
}

func overlapFixtureExec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestOverlapTwentyProximityPeersNeedZeroResolutions(t *testing.T) {
	s, _, binding, _, _, _ := architectureValidationFixture(t, "population-subject")
	binding.DomainModifies = []string{}
	binding.DomainRelationModifies = []WorkflowDomainRelationModification{}
	seedProductChangingContract(t, s, "population-subject", binding)
	population := []string{"population-subject"}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("population-peer-%02d", i)
		seedWork(t, s, id)
		seedProductChangingContract(t, s, id, binding)
		population = append(population, id)
	}
	for _, id := range population {
		if err := InspectWorkflowDomainOverlap(context.Background(), s, id); err != nil {
			t.Fatalf("proximity-only %s: %v", id, err)
		}
	}
	before, err := s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product"})
	if err != nil || len(before.Pairs) != 0 {
		t.Fatalf("proximity-only pairs=%+v err=%v", before.Pairs, err)
	}
	var resolutions int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_overlap_resolutions`).Scan(&resolutions); err != nil || resolutions != 0 {
		t.Fatalf("resolution count=%d err=%v", resolutions, err)
	}
	overlapFixtureExec(t, s, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES('population-subject',1,'child'),('population-peer-00',1,'child')`)
	err = InspectWorkflowDomainOverlap(context.Background(), s, "population-subject")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap.TotalOverlaps != 1 {
		t.Fatalf("added exact write must produce one typed refusal: %v", err)
	}
	read, err := s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product"})
	if err != nil || len(read.Pairs) != 1 {
		t.Fatalf("exact pair count=%d err=%v", len(read.Pairs), err)
	}
}

func TestOverlapAcrossProjectsPreservesDirectionalAdmission(t *testing.T) {
	for _, kind := range []string{ResolutionCompatibleWith, ResolutionDependsOn} {
		t.Run(kind, func(t *testing.T) {
			s, _, _ := overlapContentFixture(t)
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
				projectCreatedEvent("other-project", "other-project-created"),
				operationEvent("other-project-member", "product_project.added", SubjectProduct, "product", map[string]any{"product_id": "product", "project_id": "other-project", "role": "secondary", "reason": "synthetic coordination", "expected_version": 2, "resulting_version": 3}),
			}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "other-project"): 0, VersionRef(SubjectProduct, "product"): 2}}); err != nil {
				t.Fatal(err)
			}
			overlapFixtureExec(t, s, `UPDATE work_projects SET project_id='other-project' WHERE work_id='content-right'`)
			for _, id := range []string{"content-left", "content-right"} {
				var failure *Failure
				if err := InspectWorkflowDomainOverlap(context.Background(), s, id); !errors.As(err, &failure) || failure.Kind != KindDomainOverlap {
					t.Fatalf("unresolved cross-Project %s: %v", id, err)
				}
			}
			// Reverse the declarer to prove the read normalizes the stored direction.
			resolveContentPair(t, s, "content-right", "content-left", kind)
			if err := InspectWorkflowDomainOverlap(context.Background(), s, "content-left"); err != nil {
				t.Fatal(err)
			}
			err := InspectWorkflowDomainOverlap(context.Background(), s, "content-right")
			if kind == ResolutionCompatibleWith && err != nil {
				t.Fatal(err)
			}
			if kind == ResolutionDependsOn {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap {
					t.Fatalf("follower admitted: %v", err)
				}
				read, readErr := s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product"})
				if readErr != nil || len(read.Pairs) != 1 || read.Pairs[0].ResolutionKind != ResolutionBlocks {
					t.Fatalf("normalized read=%+v err=%v", read.Pairs, readErr)
				}
			}
		})
	}
}

func TestOverlapReadAndGuardPairPopulationAgree(t *testing.T) {
	s, binding, _ := overlapContentFixture(t)
	resolveContentPair(t, s, "content-left", "content-right", ResolutionCompatibleWith)
	for _, id := range []string{"proximity", "unstarted-a", "unstarted-b"} {
		seedWork(t, s, id)
		candidate := binding
		candidate.DomainModifies = []string{}
		candidate.DomainRelationModifies = []WorkflowDomainRelationModification{}
		seedProductChangingContract(t, s, id, candidate)
	}
	for _, id := range []string{"unstarted-a", "unstarted-b"} {
		setExecutionStartedForTesting(t, s, id, false)
		overlapFixtureExec(t, s, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES(?,1,'root')`, id)
	}
	// The two unstarted items share root writes, but neither is a live peer.
	read, err := s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product"})
	if err != nil || len(read.Pairs) != 1 || read.Pairs[0].ResolutionState != "current" {
		t.Fatalf("population read=%+v err=%v", read.Pairs, err)
	}
	for _, id := range []string{"content-left", "content-right", "proximity", "unstarted-a", "unstarted-b"} {
		if err := InspectWorkflowDomainOverlap(context.Background(), s, id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	setExecutionStartedForTesting(t, s, "unstarted-a", true)
	read, err = s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product"})
	if err != nil || len(read.Pairs) != 2 {
		t.Fatalf("started population=%+v err=%v", read.Pairs, err)
	}
	var absent DomainOverlapPair
	for _, pair := range read.Pairs {
		if pair.ResolutionState == "absent" {
			absent = pair
		}
	}
	if !reflect.DeepEqual([]string{absent.FromWorkID, absent.ToWorkID}, []string{"unstarted-a", "unstarted-b"}) {
		t.Fatalf("unresolved pair=%+v", absent)
	}
	// Prospective admission sees the started peer; the started side does not
	// yet see the unstarted peer. The read reports the pair once.
	var failure *Failure
	if err := InspectWorkflowDomainOverlap(context.Background(), s, "unstarted-b"); !errors.As(err, &failure) || failure.Kind != KindDomainOverlap {
		t.Fatalf("prospective claim=%v", err)
	}
	if err := InspectWorkflowDomainOverlap(context.Background(), s, "unstarted-a"); err != nil {
		t.Fatal(err)
	}
	setExecutionStartedForTesting(t, s, "unstarted-b", true)
	resolveContentPair(t, s, "unstarted-b", "unstarted-a", ResolutionDependsOn)
	read, err = s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product"})
	if err != nil || len(read.Pairs) != 2 || read.Pairs[0].ResolutionState != "current" || read.Pairs[1].ResolutionState != "sequenced" || read.Pairs[1].ResolutionKind != ResolutionBlocks {
		t.Fatalf("resolved/sequence population=%+v err=%v", read.Pairs, err)
	}
	if err := InspectWorkflowDomainOverlap(context.Background(), s, "unstarted-a"); err != nil {
		t.Fatal(err)
	}
	if err := InspectWorkflowDomainOverlap(context.Background(), s, "unstarted-b"); !errors.As(err, &failure) || failure.DomainOverlap.Overlaps[0].ResolutionState != "sequenced" {
		t.Fatalf("follower=%v", err)
	}
}

func TestOverlapLawAdditionHomeAndCanonicalOrder(t *testing.T) {
	for _, changeHome := range []bool{false, true} {
		t.Run(fmt.Sprintf("change-home-%t", changeHome), func(t *testing.T) {
			s, _, binding, _, _, _ := architectureValidationFixture(t, "content-left")
			seedWork(t, s, "content-right")
			seedProductChangingContract(t, s, "content-right", binding)
			binding.LawAdditions = []WorkflowLawAddition{{LawID: "law:new-a", HomeDomainID: "child"}, {LawID: "law:new-b", HomeDomainID: "root"}}
			actor, _ := seedProductChangingContract(t, s, "content-left", binding)
			ref, err := WorkflowActorRef(actor)
			if err != nil {
				t.Fatal(err)
			}
			resolveContentPair(t, s, "content-left", "content-right", ResolutionCompatibleWith)
			binding.LawAdditions[0], binding.LawAdditions[1] = binding.LawAdditions[1], binding.LawAdditions[0]
			if changeHome {
				// Reservations bind the home. Compare two valid synthetic
				// histories to verify the reader keeps that identity.
				before, err := readWorkflowOverlapJudgedContent(context.Background(), s.DatabaseForTesting(), "content-left", 1)
				if err != nil {
					t.Fatal(err)
				}
				other, _, _, _, _, _ := architectureValidationFixture(t, "content-left")
				binding.LawAdditions[1].HomeDomainID = "root"
				seedProductChangingContract(t, other, "content-left", binding)
				after, err := readWorkflowOverlapJudgedContent(context.Background(), other.DatabaseForTesting(), "content-left", 1)
				if err != nil || reflect.DeepEqual(before, after) || before.LawAdditions[0].LawID != after.LawAdditions[0].LawID {
					t.Fatalf("home identity lost: before=%+v after=%+v err=%v", before, after, err)
				}
				return
			}
			applyContentSuccessor(t, s, contentSuccessor(t, s, ref, 2, binding, []string{}))
			assertContentCurrent(t, s)
		})
	}
}

func TestOverlapReadBoundsAndCanonicalOrdering(t *testing.T) {
	s, binding, _ := overlapContentFixture(t)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("bounded-%02d", i)
		seedWork(t, s, id)
		seedProductChangingContract(t, s, id, binding)
		if i%2 == 0 {
			setExecutionStartedForTesting(t, s, id, false)
		}
	}
	read, err := s.QueryDomainOverlaps(context.Background(), DomainOverlapsRequest{Product: "product", Domain: "child"})
	if err != nil || !read.Truncated || len(read.Pairs) != domainOverlapPairLimit {
		t.Fatalf("bounded read=%+v err=%v", read, err)
	}
	// Twelve exact-write items, five unstarted: 66 total pairs minus the ten
	// both-unstarted pairs leaves 56 eligible pairs and the first 50 publish.
	ids := []string{"bounded-00", "bounded-01", "bounded-02", "bounded-03", "bounded-04", "bounded-05", "bounded-06", "bounded-07", "bounded-08", "bounded-09", "content-left", "content-right"}
	var expected [][2]string
	for i, left := range ids {
		for j := i + 1; j < len(ids); j++ {
			if i < 10 && j < 10 && i%2 == 0 && j%2 == 0 {
				continue
			}
			expected = append(expected, [2]string{left, ids[j]})
		}
	}
	for i, pair := range read.Pairs {
		if [2]string{pair.FromWorkID, pair.ToWorkID} != expected[i] {
			t.Fatalf("pair %d=%+v want=%v", i, pair, expected[i])
		}
	}
}
