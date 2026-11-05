package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

// removeWorkForTest drives removal the way the only production caller does:
// prepare the operation and apply the typed work.removed event inside one
// transaction, exactly as internal/agent planWorkRemoval's effect does.
func removeWorkForTest(ctx context.Context, s *Store, req WorkRemovalRequest) error {
	return s.Transact(ctx, func(tx *Transaction) error {
		prepared, err := PrepareWorkRemovalTx(ctx, tx, req)
		if err != nil {
			return err
		}
		payload, err := MarshalWorkRemovedPayload(req, prepared.HandoffDigest)
		if err != nil {
			return err
		}
		_, err = ApplyOperationTx(ctx, tx, Operation{Events: []Event{{EventID: "work.removed:" + req.OperationID, Kind: WorkRemoved, SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.Actor, OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion}})
		return err
	})
}

func TestWorkRemovalRefusesCompletedWork(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	attributionItem(t, s, "cleanup-producer", []string{}, []string{})
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, "cleanup-producer")); err != nil {
		t.Fatal(err)
	}
	producer := removalTestRequest()
	producer.WorkID, producer.ExpectedVersion = "cleanup-producer", readWorkVersion(t, s, "cleanup-producer")
	confirmRemovalHandoff(t, s, &producer)
	err := removeWorkForTest(t.Context(), s, producer)
	assertFailureKind(t, err, KindInvalidOperation)
	if err == nil || !strings.Contains(err.Error(), "completed and superseded work cannot be removed") {
		t.Fatalf("completed work refusal=%v", err)
	}
}

func removalTestRequest() WorkRemovalRequest {
	return WorkRemovalRequest{
		OperationID: "remove-op-1", IdempotencyKey: "remove-key-1", WorkID: "remove-work-1",
		ExpectedVersion: 2, Reason: "shelved", Actor: "operator:one",
		Handoff: WorkRemovalHandoff{
			Findings: []string{"finding"}, RemainingScope: []string{"scope"}, Blockers: []string{"blocker"},
			Artifacts: []string{"artifact"}, RenewalConditions: []string{"new approval"},
		}, ExecutionRelinquished: true, WritesReconciled: true, EffectsReconciled: true,
		DependenciesResolved: true, ArtifactsVerified: true,
	}
}

// confirmRemovalHandoff records a Linear issue identity for the work item and
// attaches the matching Linear handoff confirmation, so a removal fixture
// exercises its own gate instead of the CD-0142 D3 destination admission.
func confirmRemovalHandoff(t *testing.T, s *Store, req *WorkRemovalRequest) {
	t.Helper()
	remote := "remote-" + req.WorkID
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := RecordLinearIssueLinkTx(context.Background(), tx, LinearIssueLink{WorkID: req.WorkID, RemoteIssueUUID: remote, HumanKey: "EX-1", URL: "https://linear.app/example/issue/EX-1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	req.ProductID = "product"
	req.Linear = &LinearHandoffConfirmation{ProductID: "product", Destination: "linear", RemoteIssueUUID: remote, HandoffDigest: removalHandoffDigest(req.Handoff)}
}

func TestWorkRemovalLinearHandoffRequiresRecordedIdentity(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "remove-work-1")
	req := removalTestRequest()
	req.ProductID = "product"
	// CD-0142 D3: unlinked work with no Linear handoff confirmation refuses;
	// Linear is the only planning authority (CD-0213 D1), so no unconfirmed
	// removal path remains.
	err := validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req)
	assertFailureKind(t, err, KindInvalidPayload)
	if !strings.Contains(err.Error(), "no recorded Linear issue identity") || !strings.Contains(err.Error(), "carries no Linear handoff confirmation") {
		t.Fatalf("unlinked unconfirmed refusal=%v", err)
	}
	req.Linear = &LinearHandoffConfirmation{ProductID: "product", Destination: "linear", RemoteIssueUUID: "fabricated-issue"}
	err = validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req)
	assertFailureKind(t, err, KindInvalidRelation)
	if !strings.Contains(err.Error(), "no recorded Linear issue identity") {
		t.Fatalf("unrecorded handoff refusal=%v", err)
	}
	if err := s.Transact(ctx, func(tr *Transaction) error {
		_, err := RecordLinearIssueLinkTx(ctx, tr, LinearIssueLink{WorkID: req.WorkID, RemoteIssueUUID: "recorded-issue", HumanKey: "EX-1", URL: "https://linear.app/example/issue/EX-1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req), KindInvalidRelation)
	req.Linear.RemoteIssueUUID = "recorded-issue"
	if err := validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req); err != nil {
		t.Fatalf("recorded identity refusal=%v", err)
	}
	req.ProductID = "unrelated-product"
	req.Linear.ProductID = req.ProductID
	assertFailureKind(t, validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req), KindInvalidRelation)
}

func TestWorkRemovalRefusesMissingSafetyEvidence(t *testing.T) {
	req := removalTestRequest()
	req.Reason = "shelved"
	req.ArtifactsVerified = false
	err := validateRemovalRequest(req)
	assertFailureKind(t, err, KindInvalidPayload)
	if err == nil || !strings.Contains(err.Error(), "safety evidence is incomplete") {
		t.Fatalf("missing safety evidence refusal=%v", err)
	}
}

func TestWorkRemovalCancellationKeepsReasonDistinct(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "cancelled-removal-work")
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: "cancelled-removal-work", Actor: lawChangeConstructorActor})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard VALUES(1);
INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES('removal-op',1,'cancelled-removal-work','workflow.test','digest','step','internal_sqlite','digest','{}','completed','{}','[]','[]','operator','request','t','t','digest');
DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	req := removalTestRequest()
	req.OperationID = "cancel-op-1"
	req.IdempotencyKey = "cancel-key-1"
	req.WorkID = "cancelled-removal-work"
	req.ExpectedVersion = readWorkVersion(t, s, req.WorkID)
	req.Reason = "cancelled"
	confirmRemovalHandoff(t, s, &req)
	if err := removeWorkForTest(context.Background(), s, req); err != nil {
		t.Fatal(err)
	}
	committed, err := readWorkRemovalAuditQ(context.Background(), s.DatabaseForTesting(), req.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Reason != "cancelled" || committed.State != "committed" || committed.EventID != "work.removed:"+req.OperationID {
		t.Fatalf("committed receipt = %+v", committed)
	}
	var removed, transitioned int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, req.WorkID, WorkRemoved).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind='work.transitioned'`, req.WorkID).Scan(&transitioned); err != nil {
		t.Fatal(err)
	}
	if removed != 1 || transitioned != 0 {
		t.Fatalf("removal events = %d, transition events = %d", removed, transitioned)
	}
	var operations int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE work_id=?`, req.WorkID).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("removal retained %d durable operations: %v", operations, err)
	}
}

func TestWorkLivenessKeepsDecisionAndProgressEvidence(t *testing.T) {
	for _, decisions := range []int64{0, 2} {
		liveness := workLivenessFromCounts("work", decisions, sql.NullString{String: "progress", Valid: true})
		if liveness.State != "unknown" || liveness.LastProgress != "progress" {
			t.Fatalf("liveness = %+v, want unknown host state and last progress", liveness)
		}
		joined := strings.Join(liveness.Evidence, " ")
		if !strings.Contains(joined, "no verified active or waiting evidence") || strings.Contains(joined, "operator decision") != (decisions > 0) {
			t.Fatalf("evidence %q does not preserve decision count %d and unknown host liveness", joined, decisions)
		}
	}
}
