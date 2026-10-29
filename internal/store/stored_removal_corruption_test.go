package store

import (
	"encoding/json"
	"testing"
)

// A durable removal receipt or recorded reclamation plan that no longer
// decodes is corrupt stored state, never caller input a request can correct.
func TestStoredRemovalRecordsRefuseCorruptionAsInvariant(t *testing.T) {
	t.Parallel()
	_, err := decodeRecordedRemovalPlan(json.RawMessage(`{`), "worktree_reclaim")
	assertFailureKind(t, err, KindInvariantViolation)

	s, _ := terminalCleanupFixture(t)
	terminalCleanupSQL(t, s, `INSERT INTO work_removal_operations(operation_id,idempotency_key,work_id,expected_version,reason,actor,handoff_json,handoff_digest,state,created_at,updated_at) VALUES('remove-corrupt','remove-key','work-done',3,'shelved','operator','{"findings":5}','sha256:1111111111111111111111111111111111111111111111111111111111111111','prepared','now','now')`)
	_, err = readWorkRemovalAuditQ(t.Context(), s.db, "remove-corrupt")
	assertFailureKind(t, err, KindInvariantViolation)
}
