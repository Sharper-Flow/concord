package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

const workSizeOverrideApprovalRef = "c2f1d9e8b7a6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c1d0"

// sizedWork seeds a work item whose current model record is small.
func sizedWork(t *testing.T, s *Store, workID string) {
	t.Helper()
	seedWork(t, s, workID)
	inputs := workSizeInputsFixture()
	size, err := DecideWorkSize(WorkSizeModelVersion, inputs, nil)
	if err != nil || size != WorkSizeSmall {
		t.Fatalf("fixture inputs decide %q, %v; want small", size, err)
	}
	if err := applyWorkEvent(t, s, workSizedEvent(workID+"-sized-1", workID, WorkSizeModelVersion, size, inputs), nil); err != nil {
		t.Fatal(err)
	}
}

func workSizeOverrideRequest(t *testing.T, s *Store, workID, eventID string, size WorkSize) WorkSizeOverrideRequest {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	return WorkSizeOverrideRequest{
		WorkID: workID, ExpectedVersion: version, Size: size,
		Reason:      "the operator judges the change wider than its declared footprint",
		EventID:     eventID,
		OccurredAt:  time.Unix(30, 0).UTC(),
		ApprovalRef: workSizeOverrideApprovalRef, ApprovalOperationDigest: deliveryCorrectionDigest,
		ApprovalScopeJSON: deliveryCorrectionScopeJSON(workID), ApprovalVersionsJSON: deliveryCorrectionVersionsJSON(version),
		ApprovalConsequence: deliveryCorrectionConsequence,
	}
}

func runWorkSizeOverride(t *testing.T, s *Store, request WorkSizeOverrideRequest) error {
	t.Helper()
	return s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := OverrideWorkSizeTx(context.Background(), tx, request)
		return err
	})
}

func readEffectiveWorkSizeForTest(t *testing.T, s *Store, workID string) (WorkSizeDecision, bool) {
	t.Helper()
	decision, sized, err := readEffectiveWorkSize(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	return decision, sized
}

func TestWorkSizeOverrideGovernsBesideTheModelResult(t *testing.T) {
	s := openTemp(t)
	sizedWork(t, s, "override")
	insertWorkSizeOverrideApproval(t, s, "override", 1)
	if err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, "override", "override-up", WorkSizeCrucial)); err != nil {
		t.Fatalf("override: %v", err)
	}
	decision, sized := readEffectiveWorkSizeForTest(t, s, "override")
	if !sized || decision.Effective != WorkSizeCrucial || decision.Model.Size != WorkSizeSmall || decision.OperatorSize == nil || *decision.OperatorSize != WorkSizeCrucial {
		t.Fatalf("decision %+v sized=%v; want the operator's crucial beside the model's small", decision, sized)
	}

	// A later model run does not erase the operator's size.
	larger := workSizeInputsFixture()
	larger.Declared.EstimatedProductionLines = 61
	if err := applyWorkEvent(t, s, workSizedEvent("override-sized-2", "override", WorkSizeModelVersion, WorkSizeLarger, larger), nil); err != nil {
		t.Fatal(err)
	}
	decision, _ = readEffectiveWorkSizeForTest(t, s, "override")
	if decision.Effective != WorkSizeCrucial || decision.Model.Size != WorkSizeLarger || decision.Model.Sequence != 2 {
		t.Fatalf("after a rerun: %+v; want the operator's size to govern over the model's larger", decision)
	}
}

func TestWorkSizeOverrideMovesTheSizeDown(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "down")
	larger := workSizeInputsFixture()
	larger.Declared.EstimatedProductionLines = 61
	if err := applyWorkEvent(t, s, workSizedEvent("down-sized-1", "down", WorkSizeModelVersion, WorkSizeLarger, larger), nil); err != nil {
		t.Fatal(err)
	}
	insertWorkSizeOverrideApproval(t, s, "down", 1)
	if err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, "down", "override-down", WorkSizeSmall)); err != nil {
		t.Fatalf("override: %v", err)
	}
	decision, _ := readEffectiveWorkSizeForTest(t, s, "down")
	if decision.Effective != WorkSizeSmall || decision.Model.Size != WorkSizeLarger {
		t.Fatalf("decision %+v; want the operator's small beside the model's larger", decision)
	}
}

func TestWorkSizeOverrideRefusesWithoutAConsumedApproval(t *testing.T) {
	s := openTemp(t)
	sizedWork(t, s, "unapproved")
	err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, "unapproved", "override-none", WorkSizeLarger))
	assertFailureKind(t, err, KindApprovalRequired)

	insertWorkSizeOverrideApproval(t, s, "unapproved", 0)
	err = runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, "unapproved", "override-unconsumed", WorkSizeLarger))
	assertFailureKind(t, err, KindApprovalRequired)

	assertNoWorkSizeOverride(t, s, "unapproved")

	forgedStore := openTemp(t)
	sizedWork(t, forgedStore, "forged")
	insertWorkSizeOverrideApproval(t, forgedStore, "forged", 1)
	forged := workSizeOverrideRequest(t, forgedStore, "forged", "override-forged", WorkSizeLarger)
	forged.ApprovalConsequence = "a different consequence"
	err = runWorkSizeOverride(t, forgedStore, forged)
	assertFailureKind(t, err, KindUnauthorized)
	assertNoWorkSizeOverride(t, forgedStore, "forged")
}

func TestWorkSizeOverrideRefusesAReusedApproval(t *testing.T) {
	s := openTemp(t)
	sizedWork(t, s, "reused")
	insertWorkSizeOverrideApproval(t, s, "reused", 1)
	if err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, "reused", "override-first", WorkSizeLarger)); err != nil {
		t.Fatal(err)
	}
	// The work version moved, so the same approval no longer binds the request.
	err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, "reused", "override-again", WorkSizeCrucial))
	if err == nil {
		t.Fatal("a reused approval recorded a second override")
	}
	decision, _ := readEffectiveWorkSizeForTest(t, s, "reused")
	if decision.Effective != WorkSizeLarger {
		t.Fatalf("decision %+v; the refused override changed the size", decision)
	}
}

func TestWorkSizeOverrideRefusesAnUnsizedItem(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "unsized")
	insertWorkSizeOverrideApproval(t, s, "unsized", 1)
	err := runWorkSizeOverride(t, s, workSizeOverrideRequest(t, s, "unsized", "override-unsized", WorkSizeLarger))
	assertFailureKind(t, err, KindInvalidOperation)
	if _, sized := readEffectiveWorkSizeForTest(t, s, "unsized"); sized {
		t.Fatal("an unsized item reads as sized")
	}
}

// A store append of the override event by a non-operator actor refuses at
// the fold, so no route can record an operator size without the operator.
func TestWorkSizeOverrideEventRefusesANonOperatorActor(t *testing.T) {
	s := openTemp(t)
	sizedWork(t, s, "agent")
	event := operationEvent("agent-override", WorkSizeOverridden, SubjectWorkItem, "agent", workSizeOverriddenPayload{
		ModelSequence: 1, Size: WorkSizeCrucial, Reason: "an agent wants a bigger size",
		ApprovalRef: workSizeOverrideApprovalRef, ApprovalOperationDigest: deliveryCorrectionDigest,
		ApprovalScopeJSON: deliveryCorrectionScopeJSON("agent"), ApprovalVersionsJSON: deliveryCorrectionVersionsJSON(1),
		ApprovalConsequence: deliveryCorrectionConsequence,
	})
	err := applyWorkEvent(t, s, event, nil)
	assertFailureKind(t, err, KindUnauthorized)
	assertNoWorkSizeOverride(t, s, "agent")
}

func TestWorkSizeOverrideRefusesLiveCarriedHistory(t *testing.T) {
	s := openTemp(t)
	sizedWork(t, s, "carried")
	event := operationEvent("carried-override", WorkSizeOverridden, SubjectWorkItem, "carried", workSizeOverriddenPayload{ModelSequence: 1, Size: WorkSizeCrucial, Reason: "historical carry", CarriedFromEventID: "other-override"})
	assertFailureKind(t, applyWorkEvent(t, s, event, nil), KindInvalidOperation)
	assertNoWorkSizeOverride(t, s, "carried")
}

func TestWorkSizeOverrideTableIsFoldOnly(t *testing.T) {
	s := openTemp(t)
	sizedWork(t, s, "direct")
	_, err := s.DatabaseForTesting().Exec(`INSERT INTO work_size_overrides(work_id,event_id,model_sequence,size_class,recorded_at) VALUES('direct','direct-sized-1',1,'crucial','2026-10-18T00:00:00Z')`)
	if err == nil || !strings.Contains(err.Error(), "fold-only") {
		t.Fatalf("direct insert err=%v; want the fold-only guard", err)
	}
}

// insertWorkSizeOverrideApproval records the trusted client and an operator
// approval row bound to the item's current version, in the given use state.
func insertWorkSizeOverrideApproval(t *testing.T, s *Store, workID string, usedCount int) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT OR IGNORE INTO agent_clients(client_ref,status,principal_ref,capabilities_json,product_scope_json,project_scope_json,created_at) VALUES('client/concord-1','active','principal/operator','[]','[]','[]','2026-09-10T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	insertConsumedApprovalState(t, s, workSizeOverrideApprovalRef, workID, readWorkVersion(t, s, workID), usedCount)
}

func assertNoWorkSizeOverride(t *testing.T, s *Store, workID string) {
	t.Helper()
	var rows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_size_overrides WHERE work_id=?`, workID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("override rows=%d err=%v; want none", rows, err)
	}
}
