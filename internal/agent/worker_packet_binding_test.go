package agent

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// bindPacketToRecordedState sets a fixture packet's task and inputs.binding
// from recorded state, as the adapter builds them: the active contract premise
// and version when a contract is active, otherwise the work item's recorded
// question with a null contract version; the work version the dispatch is
// admitted at; and the lane's worker-scope assigned result. Call it
// immediately before the dispatch the packet rides.
func bindPacketToRecordedState(t *testing.T, s *store.Store, packet map[string]any) map[string]any {
	t.Helper()
	workID := packet["work_id"].(string)
	laneID := packet["lane_id"].(string)
	assigned, ok := store.WorkerScopeAssignedResult(laneID)
	if !ok {
		t.Fatalf("lane %s carries no worker-scope assignment", laneID)
	}
	db := s.DatabaseForTesting()
	var workVersion int64
	if err := db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&workVersion); err != nil {
		t.Fatal(err)
	}
	inputs := packet["inputs"].(map[string]any)
	binding := map[string]any{"objective_source": "work_question", "work_version": workVersion, "contract_version": nil, "assigned_result": assigned}
	var contractVersion int64
	var premise string
	err := db.QueryRow(`SELECT contract_version,premise FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&contractVersion, &premise)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		inputs["task"] = recordedWorkQuestion(t, s, workID)
	case err != nil:
		t.Fatal(err)
	default:
		binding["objective_source"] = "contract_premise"
		binding["contract_version"] = contractVersion
		inputs["task"] = premise
	}
	inputs["binding"] = binding
	return packet
}

// recordedWorkQuestion reads the question the store binds a read-only packet
// to before contract approval: the recorded task when it holds visible text,
// else the title, else the narrative. The fixtures here carry ASCII
// whitespace only; the store owns the full selection rule.
func recordedWorkQuestion(t *testing.T, s *store.Store, workID string) string {
	t.Helper()
	var title, narrative, task string
	if err := s.DatabaseForTesting().QueryRow(`SELECT title, narrative, coalesce(json_extract(intent_json, '$.task'), '') FROM work_items WHERE id=?`, workID).Scan(&title, &narrative, &task); err != nil {
		t.Fatal(err)
	}
	switch {
	case strings.TrimSpace(task) != "":
		return task
	case strings.TrimSpace(title) != "":
		return title
	default:
		return narrative
	}
}
