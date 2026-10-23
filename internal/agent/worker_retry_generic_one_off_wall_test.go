package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// verifyWallChallenges counts the approval challenges the mutation boundary
// minted so far: the durable proof a journey minted none.
func verifyWallChallenges(t *testing.T, s *store.Store) int {
	t.Helper()
	return countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`)
}

// verifyWallDispatchCompletions counts the dispatch completions the fold
// recorded for one attempt.
func verifyWallDispatchCompletions(t *testing.T, s *store.Store, attemptID string) int {
	t.Helper()
	return countRows(t, s.DatabaseForTesting(), fmt.Sprintf(`SELECT count(*) FROM domain_events WHERE subject_id='work-1' AND kind='%s' AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')='%s'`, store.WorkflowActionCompleted, attemptID))
}

func assertDispatchHasNoRetryBudget(t *testing.T, s *store.Store, attemptID string) {
	t.Helper()
	var raw []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id='work-1' AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=?`, store.WorkflowActionCompleted, attemptID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["retry_convergence"]; ok {
		t.Fatalf("dispatch %s emitted retry budget authority: %s", attemptID, raw)
	}
}

// verifyWallPinCorrection reads the standing correction record from the work
// pin, failing when none stands.
func verifyWallPinCorrection(t *testing.T, s *store.Store) *store.WorkflowCorrectionContext {
	t.Helper()
	pin, err := store.ReadWorkPin(context.Background(), s, "work-1")
	if err != nil {
		t.Fatal(err)
	}
	if pin.Correction == nil {
		t.Fatal("no correction record stands on the work pin")
	}
	return pin.Correction
}

// mustScopeVersion reads the fixture's project scope version.
func mustScopeVersion(t *testing.T, s *store.Store) string {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return scopeVersion
}
