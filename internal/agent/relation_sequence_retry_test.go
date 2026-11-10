package agent

import (
	"context"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestRelationMutationRetryAfterSequenceUpgrade(t *testing.T) {
	s, service, _, _, env, input := seedAgentOverlapFixture(t)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO relations(id,work_id_from,work_id_to,kind,created_at,record_version)
		VALUES(1,'work-1','work-2','compatible_with','2026-01-01T00:00:00Z',0);
		DELETE FROM write_guard;
		DELETE FROM schema_migrations WHERE version=196`); err != nil {
		t.Fatal(err)
	}
	var frontier int64
	if err := db.QueryRow(`SELECT last FROM store_sequences WHERE name='relation'`).Scan(&frontier); err != nil || frontier != 0 {
		t.Fatalf("fixture frontier=%d err=%v, want 0", frontier, err)
	}
	request := InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}
	before := agentCurrentEffects(t, s)
	failed := dispatchMutation(t, s, service, request, env)
	if failed.Error == nil || failed.Error.Kind != "invariant_violation" || failed.Error.Message != "allocated relation identity already exists" {
		t.Fatalf("expected relation identity collision, got %+v", failed)
	}
	if agentCurrentEffects(t, s) != before {
		t.Fatal("failed mutation changed durable effects")
	}
	if got := countRows(t, db, `SELECT count(*) FROM idempotency_records WHERE idempotency_key='resolve-overlap-1'`); got != 0 {
		t.Fatalf("failed mutation left %d idempotency records", got)
	}
	if got := countRows(t, db, `SELECT count(*) FROM effect_receipts WHERE kind='workflow.overlap_resolved'`); got != 0 {
		t.Fatalf("failed mutation left %d resolution receipts", got)
	}
	if err := store.Migrate(context.Background(), db); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if err := db.QueryRow(`SELECT last FROM store_sequences WHERE name='relation'`).Scan(&frontier); err != nil || frontier != 1 {
		t.Fatalf("upgraded frontier=%d err=%v, want 1", frontier, err)
	}
	success := dispatchMutation(t, s, service, request, env)
	if success.Outcome != agentcontract.OutcomeOK || success.Error != nil || success.Replayed {
		t.Fatalf("same-key retry failed: %+v", success)
	}
	if got := countRows(t, db, `SELECT count(*) FROM relations WHERE id=2 AND work_id_from='work-1' AND work_id_to='work-2' AND kind='depends_on'`); got != 1 {
		t.Fatalf("successful relation count=%d, want 1", got)
	}
	if got := countRows(t, db, `SELECT count(*) FROM effect_receipts WHERE kind='workflow.overlap_resolved' AND relation_seq=2`); got != 1 {
		t.Fatalf("successful resolution receipts=%d, want 1", got)
	}
	baseline := agentCurrentEffects(t, s)
	replay := dispatchMutation(t, s, service, request, env)
	if replay.Outcome != agentcontract.OutcomeOK || replay.Error != nil || !replay.Replayed || !sameResultPayload(t, success.Result, replay.Result) || !sameChangedRefs(t, success.ChangedRefs, replay.ChangedRefs) {
		t.Fatalf("replay did not return the retained success: %+v", replay)
	}
	if agentCurrentEffects(t, s) != baseline {
		t.Fatal("replay changed durable effects")
	}
	if got := countRows(t, db, `SELECT count(*) FROM idempotency_records WHERE idempotency_key='resolve-overlap-1' AND replayed_count=1`); got != 1 {
		t.Fatalf("replayed idempotency records=%d, want 1", got)
	}
}
