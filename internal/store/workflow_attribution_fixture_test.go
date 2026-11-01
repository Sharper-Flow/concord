package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func outsideRepairDatabaseSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	raw, err := json.Marshal(retirementUpgradeSnapshot(t, s.db))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func outsideRepairSettleAction(t *testing.T, s *Store, work string, actor WorkflowActor, kind string) {
	t.Helper()
	ref, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, work)
	fields := map[string]any{"work_id": work, "expected_version": version, "resulting_version": version + 1,
		"step_id": "proposal", "attempt_epoch": 1, "actor_ref": ref}
	if kind == WorkflowActionFailed {
		fields["failure_kind"], fields["recoverable"] = "timeout", false
	} else {
		fields["action_id"] = "checkpoint_context"
	}
	event := workflowEventWithActor(fmt.Sprintf("%s-settle-%d", work, version), kind, work, ref, fields)
	if err := applyWorkflowTestOperation(t.Context(), s, Operation{Events: []Event{event}, ExpectedVersions: workVersion(work, version)}); err != nil {
		t.Fatal(err)
	}
}

func terminalCleanupSQL(t *testing.T, s *Store, queries ...string) {
	t.Helper()
	if err := s.Transact(t.Context(), func(transaction *Transaction) error {
		scope := newWriteScope(transaction.tx)
		if err := scope.enter(t.Context()); err != nil {
			return err
		}
		for _, query := range queries {
			if _, err := transaction.tx.ExecContext(t.Context(), query); err != nil {
				return fmt.Errorf("fixture %s: %w", query, err)
			}
		}
		return scope.close(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}

func lawCleanupProducer(t *testing.T, s *Store, enacting bool) {
	t.Helper()
	laws := []string{}
	if enacting {
		laws = []string{"law-peer"}
	}
	attributionItem(t, s, "cleanup-producer", laws, []string{})
	if enacting {
		editLawChangeLaw(t, s, "law-peer")
	}
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, "cleanup-producer")); err != nil {
		t.Fatalf("complete the producer: %v", err)
	}
	var proofs int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_law_baselines WHERE work_id='cleanup-producer' AND enactment_json IS NOT NULL`).Scan(&proofs); err != nil || (proofs == 1) != enacting {
		t.Fatalf("material enactment count=%d, enacting=%t: %v", proofs, enacting, err)
	}
}

func lawCleanupStore(t *testing.T) *Store {
	t.Helper()
	s := seedLawChangeFixture(t, "inventory-seed")
	seedLawChangeLaw(t, s, "law-peer", "derived", "agent-surface")
	return s
}

func requireLawCleanupJoin(t *testing.T, s *Store, want int) {
	t.Helper()
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_law_baselines b JOIN effect_receipts r ON r.effect_seq=b.enactment_effect_seq JOIN workflow_instances i ON i.work_id=b.work_id JOIN work_items w ON w.id=b.work_id WHERE b.work_id='cleanup-producer' AND r.kind='workflow.completed' AND r.subject_type='work_item' AND r.subject_id=b.work_id AND i.instance_state='completed' AND w.lifecycle='completed'`).Scan(&count); err != nil || count != want {
		t.Fatalf("attribution join count=%d, want %d: %v", count, want, err)
	}
}

func requireLawCleanupAttribution(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		var start, enacted string
		var seq int64
		if err := tx.tx.QueryRow(`SELECT baseline_json,start_effect_seq FROM workflow_law_baselines WHERE work_id='cleanup-consumer'`).Scan(&start, &seq); err != nil {
			return err
		}
		if err := tx.tx.QueryRow(`SELECT enactment_json FROM workflow_law_baselines WHERE work_id='cleanup-producer'`).Scan(&enacted); err != nil {
			return err
		}
		var cursor workflowLawBaseline
		var proof workflowLawEnactment
		if err := json.Unmarshal([]byte(start), &cursor); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(enacted), &proof); err != nil {
			return err
		}
		if reflect.DeepEqual(cursor, proof.After) {
			return fmt.Errorf("consumer already has the enacted inventory without attribution")
		}
		if err := applyCompletedWorkflowLawEnactmentsTx(t.Context(), tx.tx, "cleanup-consumer", seq, &cursor); err != nil {
			return err
		}
		if !reflect.DeepEqual(cursor, proof.After) {
			return fmt.Errorf("retained completion did not advance the consumer to its enacted inventory")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
