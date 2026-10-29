package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestWorkRemovalRetainsConsumedLawEnactment(t *testing.T) {
	s := lawCleanupStore(t)
	attributionItem(t, s, "cleanup-consumer", []string{}, []string{})
	lawCleanupProducer(t, s, true)
	producer := removalTestRequest()
	producer.WorkID, producer.ExpectedVersion = "cleanup-producer", readWorkVersion(t, s, "cleanup-producer")
	// Explicit removal cannot remove completed work, even after consumers leave.
	_, err := s.RemoveWork(t.Context(), producer)
	assertFailureKind(t, err, KindInvalidOperation)
	// The deletion owner must also retain the full join if called directly.
	err = s.Transact(t.Context(), func(tx *Transaction) error {
		scope, err := beginWrite(t.Context(), tx.tx)
		if err != nil {
			return err
		}
		defer func() { _ = scope.close(t.Context()) }()
		if err := deleteWorkOwnedProjections(t.Context(), tx.tx, "cleanup-producer"); err != nil {
			return err
		}
		return errors.New("deletion owner discarded a consumed law enactment")
	})
	assertFailureKind(t, err, KindOperationConflict)
	requireLawCleanupJoin(t, s, 1)
	requireLawCleanupAttribution(t, s)
	consumer := removalTestRequest()
	consumer.OperationID, consumer.IdempotencyKey = "remove-consumer", "remove-consumer-key"
	consumer.WorkID, consumer.ExpectedVersion = "cleanup-consumer", readWorkVersion(t, s, "cleanup-consumer")
	if _, err := s.RemoveWork(t.Context(), consumer); err != nil {
		t.Fatal(err)
	}
	_, err = s.RemoveWork(t.Context(), producer)
	assertFailureKind(t, err, KindInvalidOperation)
	if err := s.CleanupPublishedTerminalWork(t.Context(), "cleanup-producer"); err != nil {
		t.Fatal(err)
	}
	requireLawCleanupJoin(t, s, 0)
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

func TestWorkRemovalDeletesExecutionAndRetriesIdempotently(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "remove-work-1")
	req := removalTestRequest()
	req.Reason = "shelved"
	receipt, err := s.RemoveWork(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != "committed" || receipt.EventID == "" {
		t.Fatalf("receipt = %+v", receipt)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE id=?`, "remove-work-1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("removed work rows = %d, want 0", count)
	}
	audit, err := s.readWorkRemovalAuditByOperation(ctx, req.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if audit.WorkID != "remove-work-1" || audit.Reason != "shelved" || audit.State != "committed" {
		t.Fatalf("audit = %+v", audit)
	}
	shelveReq := removalTestRequest()
	shelveReq.Reason = "shelved"
	second, err := s.RemoveWork(ctx, shelveReq)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed {
		t.Fatalf("retry was not replayed: %+v", second)
	}
}

func TestWorkRemovalRefusesMissingSafetyEvidence(t *testing.T) {
	req := removalTestRequest()
	req.Reason = "shelved"
	req.ArtifactsVerified = false
	if _, err := (&Store{}).RemoveWork(context.Background(), req); err == nil {
		t.Fatal("missing safety evidence was accepted")
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
	var baselines int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_law_baselines WHERE work_id='cancelled-removal-work'`).Scan(&baselines); err != nil || baselines != 1 {
		t.Fatalf("owning start baseline count=%d: %v", baselines, err)
	}
	req := removalTestRequest()
	req.OperationID = "cancel-op-1"
	req.IdempotencyKey = "cancel-key-1"
	req.WorkID = "cancelled-removal-work"
	req.ExpectedVersion = readWorkVersion(t, s, req.WorkID)
	req.Reason = "cancelled"
	receipt, err := s.RemoveWork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Reason != "cancelled" || receipt.State != "committed" {
		t.Fatalf("receipt = %+v", receipt)
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
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_law_baselines WHERE work_id='cancelled-removal-work'`).Scan(&baselines); err != nil || baselines != 0 {
		t.Fatalf("removed work retained baseline count=%d: %v", baselines, err)
	}
}

func TestWorkLivenessKeepsWaitAndDecisionEvidence(t *testing.T) {
	for _, bounded := range []int64{0, 1} {
		liveness := workLivenessFromCounts("work", bounded, 1, 2, sql.NullString{String: "progress", Valid: true})
		wantState := "unknown"
		if bounded > 0 {
			wantState = "waiting"
		}
		if liveness.State != wantState || liveness.OpenWaits != int(bounded) || liveness.LastProgress != "progress" {
			t.Fatalf("liveness = %+v, want state=%s bounded=%d and last progress", liveness, wantState, bounded)
		}
		joined := strings.Join(liveness.Evidence, " ")
		for _, want := range []string{"no declared bound", "operator decision"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("evidence %q does not contain %q", joined, want)
			}
		}
		if bounded > 0 && !strings.Contains(joined, "bounded external wait") {
			t.Fatalf("evidence %q omits the bounded wait", joined)
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
	if _, err := s.RemoveWork(ctx, refuseReq); err == nil || !strings.Contains(err.Error(), "required research consumer") {
		t.Fatalf("required consumer removal error = %v", err)
	}
}
