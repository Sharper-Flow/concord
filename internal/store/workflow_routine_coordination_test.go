package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func routineResolve(t *testing.T, s *Store, from, to, kind, actor, eventID string) error {
	t.Helper()
	fromVersion, toVersion := readWorkVersion(t, s, from), readWorkVersion(t, s, to)
	return s.Transact(t.Context(), func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(t.Context(), tx, WorkflowDomainOverlapResolutionRequest{EventID: eventID, FromWorkID: from, ToWorkID: to, FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion, FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind, Reason: "routine coordination settlement", Actor: actor})
		return err
	})
}

func routineOverlapBoundaryEvent(id, actor string, fromExpected, toExpected int64, kind string, payloadVersion int) Event {
	fields := map[string]any{"work_id": "content-left", "expected_version": fromExpected, "resulting_version": fromExpected + 1, "to_work_id": "content-right", "to_expected_version": toExpected, "to_resulting_version": toExpected + 1, "from_contract_version": int64(1), "to_contract_version": int64(1), "resolution_kind": kind, "reason": "routine coordination settlement"}
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return Event{EventID: id, Kind: WorkflowOverlapResolved, SubjectType: SubjectWorkItem, SubjectID: "content-left", Actor: actor, OccurredAt: time.Unix(41, 0).UTC(), PayloadVersion: payloadVersion, Payload: raw}
}

func routineFailure(t *testing.T, err error) *Failure {
	t.Helper()
	if err == nil {
		t.Fatal("coordination operation was admitted, want a typed refusal")
	}
	failure := &Failure{}
	if !errors.As(err, &failure) {
		t.Fatalf("error=%v, want *Failure", err)
	}
	return failure
}

func routinePairVersions(t *testing.T, s *Store) (int64, int64) {
	t.Helper()
	return readWorkVersion(t, s, "content-left"), readWorkVersion(t, s, "content-right")
}

func routineOverlapEventCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=? AND (subject_id='content-left' OR subject_id='content-right')`, WorkflowOverlapResolved).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func routineApprovalActorRows(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_actors WHERE agent_ref LIKE 'approval:%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func overlapContentFixture(t *testing.T) (*Store, WorkflowArchitectureBinding, string) {
	t.Helper()
	s, _, binding, _ := architectureValidationFixture(t, "content-left")
	commitWorkflowFixtureRegistry(t, KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}, overlapFixtureRegistry(t))
	seedWork(t, s, "content-right")
	overlapFixtureExec(t, s, `UPDATE work_items SET intent_json=json_set(intent_json,'$.governing_requirements',json('[]')) WHERE id IN ('content-left','content-right')`)
	actor := seedProductChangingContract(t, s, "content-left", binding)
	seedProductChangingContract(t, s, "content-right", binding)
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	return s, binding, ref
}

func overlapFixtureRegistry(t *testing.T) string {
	t.Helper()
	var registry KnowledgeDomainRegistry
	if err := json.Unmarshal([]byte(workflowArchitectureFixtureRegistry), &registry); err != nil {
		t.Fatal(err)
	}
	registry.Domains = append(registry.Domains, KnowledgeDomain{DomainID: "extra", Name: "Extra", Purpose: "Synthetic extra Domain.", Status: "current", ParentDomainID: registry.RootDomainID, ArchitectureRelations: []KnowledgeArchitectureRelation{}})
	raw, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + "\n"
}

func TestRoutineCoordination(t *testing.T) {
	t.Parallel()
	t.Run("routine overlap kinds resolve unsigned", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []string{ResolutionDependsOn, ResolutionBlocks} {
			t.Run(kind, func(t *testing.T) {
				t.Parallel()
				s, _, actor := overlapContentFixture(t)
				beforeFrom, beforeTo := routinePairVersions(t, s)
				if err := routineResolve(t, s, "content-left", "content-right", kind, actor, "routine-"+kind); err != nil {
					t.Fatal(err)
				}
				var count int
				if err := s.db.QueryRow(`SELECT count(*) FROM relations WHERE resolution_id=? AND kind=?`, "routine-"+kind, kind).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("relations=%d, want 1", count)
				}
				from, to := routinePairVersions(t, s)
				if from != beforeFrom+1 || to != beforeTo+1 {
					t.Fatalf("versions=%d/%d want %d/%d", from, to, beforeFrom+1, beforeTo+1)
				}
				var version int
				if err := s.db.QueryRow(`SELECT payload_version FROM effect_receipts WHERE event_id=?`, "routine-"+kind).Scan(&version); err != nil {
					t.Fatal(err)
				}
				if version != eventKindRegistry[WorkflowOverlapResolved].CurrentVersion {
					t.Fatalf("payload version=%d", version)
				}
			})
		}
	})
	t.Run("a v2 governing scope cut without cross scope refuses", func(t *testing.T) {
		t.Parallel()
		s, _, actor := overlapContentFixture(t)
		overlapFixtureExec(t, s, `UPDATE work_items SET intent_json=json_set(intent_json,'$.governing_requirements',json('["audit_required"]')) WHERE id='content-left'`)
		from, to := routinePairVersions(t, s)
		event := routineOverlapBoundaryEvent("routine-v2-scope-cut", actor, from, to, ResolutionMergedInto, 2)
		err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion("content-left", from)})
		failure := routineFailure(t, err)
		if failure.Kind != KindUnauthorized || len(failure.Violations) != 1 || failure.Violations[0] != "audit_required" {
			t.Fatalf("scope cut=%v", err)
		}
	})
}
