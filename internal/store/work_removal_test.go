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
	err := removeWorkForTest(t.Context(), s, producer)
	assertFailureKind(t, err, KindInvalidOperation)
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

func TestWorkRemovalLinearHandoffRequiresRecordedIdentity(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "remove-work-1")
	req := removalTestRequest()
	req.ProductID = "product"
	req.Linear = &LinearHandoffConfirmation{ProductID: "product", Destination: "linear", RemoteIssueUUID: "fabricated-issue"}
	err := validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req)
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
	assertFailureKind(t, err, KindInvalidOperation)
	if err == nil || !strings.Contains(err.Error(), "safety evidence is incomplete") {
		t.Fatalf("missing safety evidence refusal=%v", err)
	}
}

func TestWorkRemovalCancellationKeepsReasonDistinct(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "cancelled-removal-work")
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: "cancelled-removal-work", Definition: mustBuiltinDefinition(t, "workflow.implementation"), Actor: lawChangeConstructorActor})
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

func TestWorkRemovalRefusesRequiredResearchConsumer(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "research-owner")
	seedWork(t, s, "research-consumer")
	err := s.Transact(ctx, func(tx *Transaction) error {
		if err := enterWrite(ctx, tx.tx); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO active_research_packs(pack_id,owner_work_id,current_revision,freshness,expected_version,created_at,updated_at) VALUES('pack-owner','research-owner',1,'current',2,'now','now')`); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO active_research_revisions(pack_id,revision,question,scope_in_json,scope_out_json,done_when_json,method,created_at) VALUES('pack-owner',1,'question','{}','{}','{}','method','now')`); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO active_research_consumers(pack_id,revision,consumer_work_id,use_role,required,accepted_at) VALUES('pack-owner',1,'research-consumer','context',1,'now')`); err != nil {
			return err
		}
		return leaveWrite(ctx, tx.tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	refuseReq := func() WorkRemovalRequest {
		req := removalTestRequest()
		req.WorkID = "research-owner"
		return req
	}()
	refuseReq.Reason = "shelved"
	if err := removeWorkForTest(ctx, s, refuseReq); err == nil || !strings.Contains(err.Error(), "required research consumer") {
		t.Fatalf("required consumer removal error = %v", err)
	}
}
