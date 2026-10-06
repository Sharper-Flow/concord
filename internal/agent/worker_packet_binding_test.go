package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// bindPacketToRecordedState sets a fixture packet's task and inputs.binding
// from recorded state, as the adapter builds them: the active contract premise
// and version when a contract is active, otherwise the work item's recorded
// question with a null contract version; the work version the dispatch is
// admitted at; and the lane's worker-scope assigned result. A job-executing
// lane also binds the work's one dispatch-ready worker-job revision, as the
// adapter selects it from the continuity projection (CD-0205). Call it
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
	if lane, ok := builtinLane(laneID); ok && (lane.CapabilityClass == "implementation" || lane.CapabilityClass == "design") {
		snapshot, err := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: workID})
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.ReadyWorkerJobs) == 1 {
			raw, err := json.Marshal(snapshot.ReadyWorkerJobs[0])
			if err != nil {
				t.Fatal(err)
			}
			inputs["worker_job"] = json.RawMessage(raw)
		}
	}
	return packet
}

func builtinLane(laneID string) (store.LaneDefinition, bool) {
	for _, lane := range store.BuiltinLaneDefinitions() {
		if lane.ID == laneID {
			return lane, true
		}
	}
	return store.LaneDefinition{}, false
}

// recordReadyRetryJob records one dispatch-ready worker-job revision through
// the public record_worker_job action, so a job-capable pin admits the
// implementation dispatches the fixture runs.
func recordReadyRetryJob(t *testing.T, s *store.Store, service *Service, env CallEnvelope, jobID string) {
	t.Helper()
	const workID = "work-1"
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "action_id": "record_worker_job", "idempotency_key": "record-ready-" + jobID,
		"fields": map[string]any{"job_id": jobID, "objective": "Carry out the approved retry objective.", "stopping_condition": "The approved objective's checks pass.", "ready": true, "readiness_evidence": []string{"contract:" + workID + ":1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if result.Outcome != OutcomeOK {
		t.Fatalf("record the ready retry job: %+v", result.Error)
	}
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

// authorizedWorkerJob reads the worker-job binding the attempt's
// dispatch_worker authorization recorded, so fixture worker evidence carries
// exactly the job the core authorized (CD-0205). Nil when the authorization
// bound none or no authorization exists.
func authorizedWorkerJob(t *testing.T, s *store.Store, workID, attemptID string) *store.WorkerJobBinding {
	t.Helper()
	var raw string
	err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(payload,'$.worker_job'),'') FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.action_id')='dispatch_worker' AND json_extract(payload,'$.worker_attempt_id')=? ORDER BY seq DESC LIMIT 1`, workID, store.WorkflowActionCompleted, attemptID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || raw == "" {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var job store.WorkerJobBinding
	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		t.Fatal(err)
	}
	return &job
}
