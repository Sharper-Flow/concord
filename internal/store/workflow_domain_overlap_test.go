package store

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func seedOverlapProjection(t *testing.T, left, right string, relation bool) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	s, _, _, _, _, hash := architectureValidationFixture(t, left)
	seedWork(t, s, right)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	actor := DeriveWorkflowActorRef("principal:operator", "client:overlap", "agent:operator", "session:overlap")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, []any{actor, "principal:operator", "client:overlap", "agent:operator", "session:overlap", "operator", "2026-08-19T00:00:00Z"}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	for _, workID := range []string{left, right} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, workID, 1, "overlap test", "internal_sqlite", `[]`, `[]`, "2026-08-19T00:00:00Z", actor, `[]`, `[]`, 1, "prototype_internal"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check',?)`, workID, `{"kind":"check","check_ref":"check:overlap","immutable_subject_ref":"commit:overlap","expected_result":"pass"}`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,?,?,?,?,?)`, workID, 1, "product", hash, "child", hash); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,?,?)`, workID, 1, "child"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES(?,?,?)`, workID, 1, "child"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if relation {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_domain_relation_modifications(work_id,contract_version,source_domain_id,kind,target_domain_id) VALUES(?,?,?,?,?)`, left, 1, "child", "depends_on", "root"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_domain_relation_modifications(work_id,contract_version,source_domain_id,kind,target_domain_id) VALUES(?,?,?,?,?)`, right, 1, "child", "depends_on", "root"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, actor
}

func TestWorkflowDomainOverlapDerivesTypedIntersectionsAndCompatibleResolution(t *testing.T) {
	ctx := context.Background()
	s, actor := seedOverlapProjection(t, "overlap-left", "overlap-right", true)
	err := CheckWorkflowDomainOverlap(ctx, s, "overlap-left")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap == nil || len(failure.DomainOverlap.Overlaps) != 1 {
		t.Fatalf("expected typed overlap refusal, got %v", err)
	}
	overlap := failure.DomainOverlap.Overlaps[0]
	if overlap.FromWorkID != "overlap-left" || overlap.ToWorkID != "overlap-right" || overlap.FromContractVersion != 1 || overlap.ToContractVersion != 1 || len(overlap.SharedAffectedDomainIDs) != 1 || len(overlap.SharedDomainModifications) != 1 || len(overlap.SharedRelationTuples) != 1 || overlap.OverlapClasses[0] != "architecture" {
		t.Fatalf("unexpected deterministic overlap detail: %#v", overlap)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{EventID: "overlap-compatible", FromWorkID: "overlap-left", ToWorkID: "overlap-right", FromExpectedVersion: 2, ToExpectedVersion: 2, FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: ResolutionCompatibleWith, Reason: "operator approved compatible change", ApprovalRef: "approval:overlap-test", Actor: actor, OccurredAt: time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)})
		return err
	}); err != nil {
		t.Fatalf("compatible resolution rejected: %v", err)
	}
	var relationCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE work_id_from='overlap-left' AND work_id_to='overlap-right' AND kind='compatible_with'`).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if relationCount != 1 {
		t.Fatalf("compatible resolution relation count = %d, want 1", relationCount)
	}
	if err := CheckWorkflowDomainOverlap(ctx, s, "overlap-left"); err != nil {
		t.Fatalf("compatible resolution did not permit leading side: %v", err)
	}
	if err := CheckWorkflowDomainOverlap(ctx, s, "overlap-right"); err != nil {
		t.Fatalf("compatible resolution did not permit symmetric side: %v", err)
	}
}

func TestWorkflowDomainOverlapCompatibleResolutionUsesCanonicalPair(t *testing.T) {
	ctx := context.Background()
	s, actor := seedOverlapProjection(t, "canonical-left", "canonical-right", false)
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
			EventID: "canonical-compatible", FromWorkID: "canonical-right", ToWorkID: "canonical-left",
			FromExpectedVersion: 2, ToExpectedVersion: 2, FromContractVersion: 1, ToContractVersion: 1,
			ResolutionKind: ResolutionCompatibleWith, Reason: "operator approved compatible change", ApprovalRef: "approval:canonical", Actor: actor,
		})
		return err
	}); err != nil {
		t.Fatalf("compatible resolution rejected: %v", err)
	}
	var resolutionFrom, resolutionTo, relationFrom, relationTo string
	if err := s.DatabaseForTesting().QueryRow(`SELECT from_work_id,to_work_id FROM workflow_overlap_resolutions WHERE resolution_id='canonical-compatible'`).Scan(&resolutionFrom, &resolutionTo); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT work_id_from,work_id_to FROM relations WHERE resolution_id='canonical-compatible'`).Scan(&relationFrom, &relationTo); err != nil {
		t.Fatal(err)
	}
	if resolutionFrom != "canonical-left" || resolutionTo != "canonical-right" || relationFrom != "canonical-left" || relationTo != "canonical-right" {
		t.Fatalf("compatible pair was not canonical: resolution=%s->%s relation=%s->%s", resolutionFrom, resolutionTo, relationFrom, relationTo)
	}
}

func TestWorkflowDomainOverlapSequencingUsesExplicitStateAndEventOrder(t *testing.T) {
	ctx := context.Background()
	s, actor := seedOverlapProjection(t, "sequence-left", "sequence-right", false)
	now := time.Date(2026, 8, 19, 2, 0, 0, 0, time.UTC)
	resolve := func(eventID, kind string, fromVersion, toVersion int64) error {
		return s.Transact(ctx, func(tx *Transaction) error {
			_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
				EventID: eventID, FromWorkID: "sequence-left", ToWorkID: "sequence-right",
				FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion,
				FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind,
				Reason: "operator sequence", ApprovalRef: "approval:" + eventID, Actor: actor, OccurredAt: now,
			})
			return err
		})
	}
	if err := resolve("sequence-depends", ResolutionDependsOn, 2, 2); err != nil {
		t.Fatalf("depends_on resolution: %v", err)
	}
	if err := CheckWorkflowDomainOverlap(ctx, s, "sequence-right"); err != nil {
		t.Fatalf("leading sequence side should be allowed: %v", err)
	}
	err := CheckWorkflowDomainOverlap(ctx, s, "sequence-left")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap == nil || len(failure.DomainOverlap.Overlaps) != 1 {
		t.Fatalf("follower should receive typed overlap refusal: %v", err)
	}
	detail := failure.DomainOverlap.Overlaps[0]
	if detail.ResolutionState != "sequenced" || detail.ResolutionKind != ResolutionDependsOn || !containsString(detail.RecoveryActions, "wait") || !containsString(detail.RecoveryActions, "resolve_overlap") || !containsString(detail.RecoveryActions, "terminal_work") {
		t.Fatalf("sequenced refusal lost closed recovery detail: %#v", detail)
	}
	if err := resolve("sequence-blocks", ResolutionBlocks, 3, 3); err != nil {
		t.Fatalf("same-timestamp newer resolution: %v", err)
	}
	if err := CheckWorkflowDomainOverlap(ctx, s, "sequence-left"); err != nil {
		t.Fatalf("newer blocks leading side should be allowed: %v", err)
	}
	err = CheckWorkflowDomainOverlap(ctx, s, "sequence-right")
	if !errors.As(err, &failure) || failure.DomainOverlap == nil || failure.DomainOverlap.Overlaps[0].ResolutionKind != ResolutionBlocks || failure.DomainOverlap.Overlaps[0].ResolutionState != "sequenced" {
		t.Fatalf("event sequence did not outrank equal timestamp: %v", err)
	}
}

func TestWorkflowDomainOverlapReopenInvalidatesSameVersionResolution(t *testing.T) {
	ctx := context.Background()
	s, actor := seedOverlapProjection(t, "reopen-left", "reopen-right", false)
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
			EventID: "reopen-resolution", FromWorkID: "reopen-left", ToWorkID: "reopen-right", FromExpectedVersion: 2, ToExpectedVersion: 2,
			FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: ResolutionCompatibleWith, Reason: "operator approved", ApprovalRef: "approval:reopen", Actor: actor,
		})
		return err
	}); err != nil {
		t.Fatalf("resolution: %v", err)
	}
	if err := applyWorkEvent(t, s, workTransitionEvent("reopen-terminal", "reopen-left", "needed", "completed", 3, 4), nil); err != nil {
		t.Fatalf("terminal transition: %v", err)
	}
	if err := applyWorkEvent(t, s, workReopenedEvent("reopen-again", "reopen-left", "completed", 4, 5), nil); err != nil {
		t.Fatalf("reopen transition: %v", err)
	}
	err := CheckWorkflowDomainOverlap(ctx, s, "reopen-left")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap == nil || failure.DomainOverlap.Overlaps[0].ResolutionState != "stale" {
		t.Fatalf("reopen must require a fresh resolution, got %v", err)
	}
	var invalidated int
	if err := s.DatabaseForTesting().QueryRow(`SELECT invalidated_seq FROM workflow_overlap_resolutions WHERE resolution_id='reopen-resolution'`).Scan(&invalidated); err != nil || invalidated <= 0 {
		t.Fatalf("reopen did not preserve and invalidate resolution history: seq=%d err=%v", invalidated, err)
	}
	var staleRelationCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE resolution_id='reopen-resolution'`).Scan(&staleRelationCount); err != nil {
		t.Fatal(err)
	}
	if staleRelationCount != 0 {
		t.Fatalf("reopen retained %d stale resolution relations", staleRelationCount)
	}
}

func TestWorkflowDomainOverlapClassifiesLawDomainAndRelationWrites(t *testing.T) {
	s, _ := seedOverlapProjection(t, "classes-left", "classes-right", true)
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflow_law_addition_reservations(product_id,law_id,owner_work_id,owner_contract_version,home_domain_id) VALUES('product','law:shared','classes-left',1,'child')`, nil},
		{`INSERT INTO workflow_contract_law_additions(work_id,contract_version,product_id,law_id,home_domain_id,reservation_owner_work_id,reservation_owner_contract_version) VALUES('classes-left',1,'product','law:shared','child','classes-left',1)`, nil},
		{`INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES('classes-right',1,'law:shared')`, nil},
		{`INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES('classes-left',1,'law:modified')`, nil},
		{`INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES('classes-right',1,'law:modified')`, nil},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	err = CheckWorkflowDomainOverlap(ctx, s, "classes-left")
	var failure *Failure
	if !errors.As(err, &failure) || failure.DomainOverlap == nil || len(failure.DomainOverlap.Overlaps) != 1 {
		t.Fatalf("expected typed overlap classes, got %v", err)
	}
	detail := failure.DomainOverlap.Overlaps[0]
	if got := strings.Join(detail.SharedLawIDs, ","); got != "law:modified,law:shared" {
		t.Fatalf("shared law writes = %q", got)
	}
	if got := strings.Join(detail.OverlapClasses, ","); got != "architecture,law_write,domain_write,domain_relation_write" {
		t.Fatalf("overlap classes = %q", got)
	}
	if detail.SharedAffectedDomainCount != 1 || detail.SharedLawCount != 2 || detail.SharedDomainModificationCount != 1 || detail.SharedRelationTupleCount != 1 || detail.DetailTruncated {
		t.Fatalf("overlap counts = %#v", detail)
	}
}

func TestWorkflowDomainOverlapOrdinaryRelationsCannotResolveOrForgeAuthority(t *testing.T) {
	ctx := context.Background()
	s, _ := seedOverlapProjection(t, "ordinary-left", "ordinary-right", false)
	if err := applyWorkEvent(t, s, relationAddedEvent("ordinary-blocks", "blocks", "ordinary-left", "ordinary-right", 2, 3), workVersion("ordinary-right", 2)); err != nil {
		t.Fatalf("ordinary blocks relation: %v", err)
	}
	err := CheckWorkflowDomainOverlap(ctx, s, "ordinary-left")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap.Overlaps[0].ResolutionState != "unresolved" {
		t.Fatalf("ordinary relation satisfied overlap authority: %v", err)
	}
	for _, kind := range []string{"compatible_with", "merged_into"} {
		err := applyWorkEvent(t, s, relationAddedEvent("ordinary-"+kind, kind, "ordinary-left", "ordinary-right", 3, 4), workVersion("ordinary-right", 3))
		assertFailureKind(t, err, KindRelationContractViolation)
	}
}

func TestWorkflowDomainOverlapSequenceTerminalUnblocksFollower(t *testing.T) {
	ctx := context.Background()
	s, actor := seedOverlapProjection(t, "terminal-left", "terminal-right", false)
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
			EventID: "terminal-sequence", FromWorkID: "terminal-left", ToWorkID: "terminal-right",
			FromExpectedVersion: 2, ToExpectedVersion: 2, FromContractVersion: 1, ToContractVersion: 1,
			ResolutionKind: ResolutionDependsOn, Reason: "right leads", ApprovalRef: "approval:terminal-sequence", Actor: actor,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := CheckWorkflowDomainOverlap(ctx, s, "terminal-left"); err == nil {
		t.Fatal("sequenced follower was allowed before predecessor became terminal")
	}
	if err := applyWorkEvent(t, s, workTransitionEvent("terminal-right-complete", "terminal-right", "needed", "completed", 3, 4), nil); err != nil {
		t.Fatalf("terminal predecessor: %v", err)
	}
	if err := CheckWorkflowDomainOverlap(ctx, s, "terminal-left"); err != nil {
		t.Fatalf("terminal predecessor did not resolve active pair: %v", err)
	}
}

func TestWorkflowDomainOverlapGuardsLifecycleExecutionEntry(t *testing.T) {
	ctx := context.Background()
	s, actor := seedOverlapProjection(t, "lifecycle-left", "lifecycle-right", false)
	err := applyWorkEvent(t, s, workTransitionEvent("lifecycle-blocked", "lifecycle-left", "needed", "in_progress", 2, 3), nil)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap {
		t.Fatalf("lifecycle execution entry error=%v, want domain_overlap", err)
	}
	if got := readWorkVersion(t, s, "lifecycle-left"); got != 2 {
		t.Fatalf("blocked lifecycle entry changed version=%d", got)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
			EventID: "lifecycle-compatible", FromWorkID: "lifecycle-left", ToWorkID: "lifecycle-right",
			FromExpectedVersion: 2, ToExpectedVersion: 2, FromContractVersion: 1, ToContractVersion: 1,
			ResolutionKind: ResolutionCompatibleWith, Reason: "operator approved lifecycle concurrency", ApprovalRef: "approval:lifecycle", Actor: actor,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := applyWorkEvent(t, s, workTransitionEvent("lifecycle-allowed", "lifecycle-left", "needed", "in_progress", 3, 4), nil); err != nil {
		t.Fatalf("resolved lifecycle entry: %v", err)
	}
}

func TestWorkflowDomainOverlapMergeAndSupersessionAreAtomicAndReopenStale(t *testing.T) {
	for _, testCase := range []struct {
		name, kind, terminalID, survivorID string
	}{
		{name: "merged into", kind: ResolutionMergedInto, terminalID: "terminal-source", survivorID: "terminal-target"},
		{name: "supersedes", kind: ResolutionSupersedes, terminalID: "terminal-target", survivorID: "terminal-source"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			s, actor := seedOverlapProjection(t, "terminal-source", "terminal-target", false)
			tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := enterFold(ctx, tx); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO resource_claims(resource_key,holder_work_id,holder_agent,holder_session,reason,state,claimed_at) VALUES(?,?,?,?,?,'held',?)`, "resource:"+testCase.kind, testCase.terminalID, "agent:test", "session:test", "terminal cleanup", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := leaveFold(ctx, tx); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := s.Transact(ctx, func(tx *Transaction) error {
				_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
					EventID: "terminal-" + testCase.kind, FromWorkID: "terminal-source", ToWorkID: "terminal-target",
					FromExpectedVersion: 2, ToExpectedVersion: 2, FromContractVersion: 1, ToContractVersion: 1,
					ResolutionKind: testCase.kind, Reason: "operator terminal resolution", ApprovalRef: "approval:" + testCase.kind, Actor: actor,
				})
				return err
			}); err != nil {
				t.Fatalf("terminal resolution: %v", err)
			}
			var lifecycle, claimState string
			if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, testCase.terminalID).Scan(&lifecycle); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT state FROM resource_claims WHERE resource_key=?`, "resource:"+testCase.kind).Scan(&claimState); err != nil {
				t.Fatal(err)
			}
			if lifecycle != "superseded" || claimState != "released" {
				t.Fatalf("terminal composite lifecycle=%q claim=%q", lifecycle, claimState)
			}
			if err := applyWorkEvent(t, s, workReopenedFromSupersededEvent("terminal-reopen-"+testCase.kind, testCase.terminalID, "", 3, 4), nil); err != nil {
				t.Fatalf("reopen terminal resolution: %v", err)
			}
			err = CheckWorkflowDomainOverlap(ctx, s, testCase.terminalID)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap.Overlaps[0].ResolutionState != "stale" {
				t.Fatalf("reopened terminal work inherited resolution: %v", err)
			}
		})
	}
}

func seedProductChangingContract(t *testing.T, s *Store, workID string, binding WorkflowArchitectureBinding) (WorkflowActor, int64) {
	t.Helper()
	ctx := context.Background()
	registered, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1)
	if !ok {
		t.Fatal("workflow.implementation is unavailable")
	}
	actor := WorkflowActor{PrincipalRef: "principal:overlap", ClientRef: "client:overlap", AgentRef: "agent:" + workID, SessionRef: "session:" + workID, ActorClass: ActorAgent}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	contract := workflowEventWithActor("overlap-contract-"+workID, WorkflowContractApproved, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": int64(1),
		"premise": "coordinate Product-changing overlap", "outcome_kind": "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:" + workID, "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		"required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": binding,
	})
	contract.PayloadVersion = 3
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{contract}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4}}); err != nil {
		t.Fatal(err)
	}
	return actor, 5
}

func TestWorkflowDomainOverlapContractRevisionStalesResolutionAndRebuilds(t *testing.T) {
	ctx := context.Background()
	s, _, binding, _, _, _ := architectureValidationFixture(t, "revision-left")
	seedWork(t, s, "revision-right")
	leftActor, leftVersion := seedProductChangingContract(t, s, "revision-left", binding)
	_, rightVersion := seedProductChangingContract(t, s, "revision-right", binding)
	leftActorRef, err := WorkflowActorRef(leftActor)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
			EventID: "revision-compatible", FromWorkID: "revision-left", ToWorkID: "revision-right",
			FromExpectedVersion: leftVersion, ToExpectedVersion: rightVersion, FromContractVersion: 1, ToContractVersion: 1,
			ResolutionKind: ResolutionCompatibleWith, Reason: "operator approved v1 pair", ApprovalRef: "approval:revision-v1", Actor: leftActorRef,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	successor := map[string]any{
		"contract_version": int64(2), "premise": "revised Product-changing overlap", "outcome_kind": "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:revision-v2", "immutable_subject_ref": "commit:revision-v2", "expected_result": "pass"},
		"required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": []string{"spec:one"}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": binding,
	}
	supersede := workflowEventWithActor("revision-contract-v2", WorkflowContractSuperseded, "revision-left", leftActorRef, map[string]any{
		"work_id": "revision-left", "expected_version": int64(6), "resulting_version": int64(7),
		"previous_contract_version": int64(1), "new_contract_version": int64(2), "supersede_reason": "scope revision",
		"audit_evidence": []string{"audit:revision-v2"}, "successor_contract": successor,
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{supersede}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "revision-left"): 6}}); err != nil {
		t.Fatalf("contract revision: %v", err)
	}
	err = CheckWorkflowDomainOverlap(ctx, s, "revision-left")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindDomainOverlap || failure.DomainOverlap.Overlaps[0].ResolutionState != "stale" || failure.DomainOverlap.Overlaps[0].FromContractVersion != 2 {
		t.Fatalf("contract revision did not stale v1 resolution: %v", err)
	}
	var invalidated int
	if err := s.DatabaseForTesting().QueryRow(`SELECT invalidated_seq FROM workflow_overlap_resolutions WHERE resolution_id='revision-compatible'`).Scan(&invalidated); err != nil || invalidated <= 0 {
		t.Fatalf("contract revision invalidation=%d err=%v", invalidated, err)
	}
	var relationCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE resolution_id='revision-compatible'`).Scan(&relationCount); err != nil || relationCount != 0 {
		t.Fatalf("stale resolution relation count=%d err=%v", relationCount, err)
	}
	before, err := WorkflowProjectionHash(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	after, err := WorkflowProjectionHash(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("overlap projection rebuild drift: before=%s after=%s", before, after)
	}
}

func seedCompletedWorkerOverlap(t *testing.T, workID, otherID string) (*Store, WorkflowActor, string, int64) {
	t.Helper()
	s, _, owner, attemptID := seedCompletedWorkerAtExecution(t, workID)
	seedWork(t, s, otherID)
	ctx := context.Background()
	hash := "sha256:" + strings.Repeat("d", 64)
	approvedBy, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product','project','workflow-law-locator','product','root','1.0',?,'test')`, []any{hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','product','root','Root','Product law','current',?,'test')`, []any{hash}},
		{`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'accept worker result','internal_sqlite','[]','[]','2026-08-19T00:00:00Z',?,'[]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:overlap","immutable_subject_ref":"commit:overlap","expected_result":"pass"}')`, []any{workID, approvedBy, workID}},
		{`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'concurrent work','internal_sqlite','[]','[]','2026-08-19T00:00:00Z',?,'[]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:overlap","immutable_subject_ref":"commit:overlap","expected_result":"pass"}')`, []any{otherID, approvedBy, otherID}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	for _, id := range []string{workID, otherID} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,1,'product',?,'root',?)`, id, hash, hash); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,'root')`, id); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, owner, attemptID, readWorkVersion(t, s, workID)
}

func TestWorkflowDomainOverlapBlocksWorkerResultAcceptanceWithoutKillingReads(t *testing.T) {
	const workID = "overlap-result-work"
	s, owner, attemptID, version := seedCompletedWorkerOverlap(t, workID, "overlap-result-other")
	ctx := context.Background()
	raw, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, raw); err != nil {
		raw.Rollback()
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(ctx, raw, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "accept_worker_result",
		Payload: mustJSONValue(map[string]any{"attempt_id": attemptID, "attempt_epoch": 1}), Actor: owner,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "overlap-result-accept", OperationID: "overlap-result-accept",
		PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "overlap-result-accept", RequestID: "request:overlap-result-accept", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC),
	})
	_ = leaveFold(ctx, raw)
	_ = raw.Rollback()
	var failure *Failure
	if !errors.As(actionErr, &failure) || failure.Kind != KindDomainOverlap {
		t.Fatalf("worker result acceptance error=%v, want domain_overlap", actionErr)
	}
	if got := readWorkVersion(t, s, workID); got != version {
		t.Fatalf("blocked result acceptance changed work version=%d, want %d", got, version)
	}
	if got := currentStep(t, s, workID); got != "execution" {
		t.Fatalf("blocked result acceptance advanced step=%q", got)
	}
	if _, err := ReadWorkflow(ctx, s, workID); err != nil {
		t.Fatalf("read-only workflow inspection failed while overlap blocked execution: %v", err)
	}
}

func TestWorkflowDomainOverlapResolutionCommitsBeforeCrossProcessResultAcceptance(t *testing.T) {
	const workID = "overlap-race-work"
	const otherID = "overlap-race-other"
	s, _, attemptID, version := seedCompletedWorkerOverlap(t, workID, otherID)

	resolver := exec.Command(os.Args[0], "-test.run=^TestWorkflowDomainOverlapCrossProcessWorker$", "-test.v=false")
	resolver.Env = append(os.Environ(), "CONCORD_OVERLAP_RACE_ROLE=resolve", "CONCORD_OVERLAP_RACE_DB="+s.Path(), "CONCORD_OVERLAP_RACE_WORK="+workID, "CONCORD_OVERLAP_RACE_OTHER="+otherID, fmt.Sprintf("CONCORD_OVERLAP_RACE_VERSION=%d", version))
	resolverOut, err := resolver.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	resolverIn, err := resolver.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	resolver.Stderr = os.Stderr
	if err := resolver.Start(); err != nil {
		t.Fatal(err)
	}
	resolverScanner := bufio.NewScanner(resolverOut)
	t.Cleanup(func() { _ = resolver.Process.Kill() })
	if !resolverScanner.Scan() || resolverScanner.Text() != "resolve=locked" {
		t.Fatalf("resolver did not establish transaction lock: %q", resolverScanner.Text())
	}

	acceptance := exec.Command(os.Args[0], "-test.run=^TestWorkflowDomainOverlapCrossProcessWorker$", "-test.v=false")
	acceptance.Env = append(os.Environ(), "CONCORD_OVERLAP_RACE_ROLE=accept", "CONCORD_OVERLAP_RACE_DB="+s.Path(), "CONCORD_OVERLAP_RACE_WORK="+workID, "CONCORD_OVERLAP_RACE_ATTEMPT="+attemptID, fmt.Sprintf("CONCORD_OVERLAP_RACE_VERSION=%d", version+1))
	acceptanceOut, err := acceptance.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	acceptance.Stderr = os.Stderr
	if err := acceptance.Start(); err != nil {
		t.Fatal(err)
	}
	acceptanceScanner := bufio.NewScanner(acceptanceOut)
	t.Cleanup(func() { _ = acceptance.Process.Kill() })
	if !acceptanceScanner.Scan() || acceptanceScanner.Text() != "accept=ready" {
		t.Fatalf("acceptance worker did not become ready: %q", acceptanceScanner.Text())
	}
	if _, err := fmt.Fprintln(resolverIn, "release"); err != nil {
		t.Fatal(err)
	}
	if !resolverScanner.Scan() || resolverScanner.Text() != "resolve=committed" {
		t.Fatalf("resolver result = %q", resolverScanner.Text())
	}
	if err := resolver.Wait(); err != nil {
		t.Fatal(err)
	}
	if !acceptanceScanner.Scan() || acceptanceScanner.Text() != "accept=committed" {
		t.Fatalf("acceptance result = %q", acceptanceScanner.Text())
	}
	if err := acceptance.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := readWorkVersion(t, s, workID); got != version+2 {
		t.Fatalf("result acceptance version=%d, want %d", got, version+2)
	}
}

func TestWorkflowDomainOverlapCrossProcessWorker(t *testing.T) {
	role := os.Getenv("CONCORD_OVERLAP_RACE_ROLE")
	if role == "" {
		return
	}
	if role == "accept" {
		fmt.Println("accept=ready")
	}
	s, err := Open(context.Background(), os.Getenv("CONCORD_OVERLAP_RACE_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	workID := os.Getenv("CONCORD_OVERLAP_RACE_WORK")
	version := mustEnvInt64(t, "CONCORD_OVERLAP_RACE_VERSION")
	switch role {
	case "resolve":
		raw, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		transaction := &Transaction{tx: raw}
		_, err = ResolveWorkflowDomainOverlapTx(context.Background(), transaction, WorkflowDomainOverlapResolutionRequest{
			EventID: "overlap-race-resolution", FromWorkID: workID, ToWorkID: os.Getenv("CONCORD_OVERLAP_RACE_OTHER"),
			FromExpectedVersion: version, ToExpectedVersion: 2, FromContractVersion: 1, ToContractVersion: 1,
			ResolutionKind: ResolutionCompatibleWith, Reason: "operator race resolution", ApprovalRef: "approval:overlap-race", Actor: "operator",
		})
		if err != nil {
			raw.Rollback()
			t.Fatal(err)
		}
		fmt.Println("resolve=locked")
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			raw.Rollback()
			t.Fatal(err)
		}
		if err := raw.Commit(); err != nil {
			t.Fatal(err)
		}
		fmt.Println("resolve=committed")
	case "accept":
		raw, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enterFold(context.Background(), raw); err != nil {
			raw.Rollback()
			t.Fatal(err)
		}
		owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
		_, actionErr := applyWorkflowActionRawTx(context.Background(), raw, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "accept_worker_result",
			Payload: mustJSONValue(map[string]any{"attempt_id": os.Getenv("CONCORD_OVERLAP_RACE_ATTEMPT"), "attempt_epoch": 1}), Actor: owner,
			AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), IdempotencyIdentity: "overlap-race-accept", OperationID: "overlap-race-accept",
			PrincipalRef: owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "overlap-race-accept", RequestID: "request:overlap-race-accept", ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC),
		})
		if actionErr != nil {
			raw.Rollback()
			t.Fatal(actionErr)
		}
		if err := leaveFold(context.Background(), raw); err != nil {
			raw.Rollback()
			t.Fatal(err)
		}
		if err := raw.Commit(); err != nil {
			t.Fatal(err)
		}
		fmt.Println("accept=committed")
	default:
		t.Fatalf("unknown overlap race role %q", role)
	}
}
