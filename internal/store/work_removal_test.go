package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

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

func TestWorkRemovalDeletesExecutionAndReplaysAbsence(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "remove-work-1")
	receipt, err := s.ShelveWork(ctx, removalTestRequest())
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
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE id=?`, "remove-work-1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("replayed work rows = %d, want 0", count)
	}
	audit, err := s.ReadWorkRemovalAudit(ctx, "remove-work-1")
	if err != nil {
		t.Fatal(err)
	}
	if audit.Reason != "shelved" || audit.State != "committed" {
		t.Fatalf("audit = %+v", audit)
	}
	second, err := s.ShelveWork(ctx, removalTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed {
		t.Fatalf("retry was not replayed: %+v", second)
	}
}

func TestWorkRemovalRefusesMissingSafetyEvidence(t *testing.T) {
	req := removalTestRequest()
	req.ArtifactsVerified = false
	if _, err := (&Store{}).ShelveWork(context.Background(), req); err == nil {
		t.Fatal("missing safety evidence was accepted")
	}
}

func TestWorkRemovalCancellationKeepsReasonDistinct(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "cancelled-removal-work")
	req := removalTestRequest()
	req.OperationID = "cancel-op-1"
	req.IdempotencyKey = "cancel-key-1"
	req.WorkID = "cancelled-removal-work"
	receipt, err := s.CancelWork(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Reason != "cancelled" || receipt.State != "committed" {
		t.Fatalf("receipt = %+v", receipt)
	}
	var removed, transitioned int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, req.WorkID, WorkRemoved).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind='work.transitioned'`, req.WorkID).Scan(&transitioned); err != nil {
		t.Fatal(err)
	}
	if removed != 1 || transitioned != 0 {
		t.Fatalf("removal events = %d, transition events = %d", removed, transitioned)
	}
}

func TestWorkLivenessKeepsUnverifiedWorkersUnknown(t *testing.T) {
	liveness := workLivenessFromCounts("work", 2, 1, 1, 1, 1, 2, sql.NullString{String: "progress", Valid: true})
	if liveness.State != "unknown" {
		t.Fatalf("state = %q, want unknown", liveness.State)
	}
	if liveness.Attempts != 2 || liveness.OpenWaits != 1 || liveness.LastProgress != "progress" {
		t.Fatalf("liveness = %+v", liveness)
	}
	joined := strings.Join(liveness.Evidence, " ")
	for _, want := range []string{"unknown host state", "no declared bound", "operator decision"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("evidence %q does not contain %q", joined, want)
		}
	}
}

func TestWorkRemovalRefusesRequiredResearchConsumer(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "research-owner")
	seedWork(t, s, "research-consumer")
	err := s.Transact(ctx, func(tx *Transaction) error {
		if err := enterFold(ctx, tx.tx); err != nil {
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
		return leaveFold(ctx, tx.tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ShelveWork(ctx, func() WorkRemovalRequest {
		req := removalTestRequest()
		req.WorkID = "research-owner"
		return req
	}()); err == nil || !strings.Contains(err.Error(), "required research consumer") {
		t.Fatalf("required consumer removal error = %v", err)
	}
}
