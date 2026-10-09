package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
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
	// A job-executing lane also binds the work's one dispatch-ready worker-job
	// revision, as the adapter selects it from the continuity projection
	// (CD-0205). Call it immediately before the dispatch the packet rides.
	if _, ok := builtinLane(laneID); ok {
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
			packet["schema_version"] = store.WorkerPacketSchemaVersion
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
	recordReadyRetryJobWithChecks(t, s, service, env, jobID, 1, nil)
}

// recordReadyRetryJobUnderContract records one dispatch-ready worker-job
// revision through the public record_worker_job action under the named active
// contract version. A revision recorded under a predecessor contract stays
// undispatchable until a successor records a fresh one (CD-0205); supersession
// paths re-record before the next dispatch.
func recordReadyRetryJobUnderContract(t *testing.T, s *store.Store, service *Service, env CallEnvelope, jobID string, contractVersion int64) {
	recordReadyRetryJobWithChecks(t, s, service, env, jobID, contractVersion, nil)
}

// oracleCapableAgentPin mirrors the store's declared-member derivation: the
// pinned definition's record_worker_job payload names acceptance_oracle.
// The store predicate is package-private, so the agent fixture restates the
// one derivation rule the registry owns.
func oracleCapableAgentPin(t *testing.T, s *store.Store) bool {
	t.Helper()
	var ref string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id='work-1'`).Scan(&ref, &version); err != nil {
		t.Fatal(err)
	}
	registered, ok := store.BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s@%d is not registered", ref, version)
	}
	for _, action := range registered.Definition.ActionDefinitions {
		if action.ID != "record_worker_job" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "acceptance_oracle" {
				return true
			}
		}
	}
	return false
}

// agentOracleFieldsForTest authors one valid, authority-joined oracle from
// work-1's live fixture state: the active contract's approved
// predicates, the primary Project, the Product registry's root Domain, and
// one retained evidence reference seeded when the fixture bound none.
func agentOracleFieldsForTest(t *testing.T, s *store.Store) map[string]any {
	t.Helper()
	db := s.DatabaseForTesting()
	var contractVersion int64
	if err := db.QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id='work-1' AND superseded_by IS NULL ORDER BY contract_version DESC LIMIT 1`).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT predicate_id FROM workflow_contract_predicates WHERE work_id='work-1' AND contract_version=? ORDER BY ordinal,predicate_id`, contractVersion)
	if err != nil {
		t.Fatal(err)
	}
	var predicates []string
	for rows.Next() {
		var predicate string
		if err := rows.Scan(&predicate); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		predicates = append(predicates, predicate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(predicates) == 0 {
		t.Fatal("the oracle fixture requires an approved contract predicate")
	}
	var project, product, rootDomain string
	if err := db.QueryRow(`SELECT project_id FROM work_projects WHERE work_id='work-1' AND role='primary'`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT pp.product_id FROM work_projects wp JOIN product_projects pp ON pp.project_id=wp.project_id WHERE wp.work_id='work-1' AND wp.role='primary' LIMIT 1`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT root_domain_id FROM domain_registries WHERE product_id=?`, product).Scan(&rootDomain); err != nil {
		t.Fatalf("read the fixture Domain registry for %s: %v", product, err)
	}
	readiness := ""
	err = db.QueryRow(`SELECT json_extract(payload,'$.immutable_subject_ref') FROM domain_events WHERE subject_type='work_item' AND subject_id='work-1' AND kind='workflow.evidence_bound' ORDER BY seq LIMIT 1`).Scan(&readiness)
	if err == sql.ErrNoRows {
		readiness = "evidence:oracle-agent-ready"
		op := "op:oracle-agent-ready"
		if _, err := db.Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,1,'work-1','workflow.test',1,'evidence','internal_sqlite','digest','{}','completed','{}',?,'[]','principal/oracle-ready','request/oracle-ready','2026-09-09T00:00:00Z','2026-09-09T00:00:01Z','sha256:`+strings.Repeat("9", 64)+`')`,
			op, `["`+readiness+`"]`); err != nil {
			t.Fatalf("seed oracle readiness authority: %v", err)
		}
		payload, marshalErr := json.Marshal(map[string]any{
			"work_id": "work-1", "expected_version": 1, "resulting_version": 2, "evidence_kind": "verification",
			"immutable_subject_ref": readiness, "producer_id": "principal/oracle-ready", "producer_run_ref": op,
			"producer_watermark": "request/oracle-ready", "observed_at": "2026-09-09T00:00:00Z",
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('oracle-agent-ready','workflow.evidence_bound','work_item','work-1','principal/oracle-ready','2026-09-09T00:00:00Z',1,?)`, string(payload)); err != nil {
			t.Fatalf("seed oracle readiness evidence: %v", err)
		}
		if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("1c9f", 10)
	return map[string]any{
		"owners": []map[string]any{{
			"owner_id": "owner:agent-fixture", "domain_id": rootDomain,
			"mechanism":     map[string]any{"project_id": project, "path": "internal/store/worker_jobs.go", "entry_point": "foldWorkerJobRecorded"},
			"obligation":    "the agent fixture oracle obligates the recorded revision",
			"predicate_ids": predicates,
			"law_bindings":  []map[string]any{},
		}},
		"cases": []map[string]any{{
			"case_id": "case:agent-fixture", "owner_id": "owner:agent-fixture", "entry_point": "record",
			"input_class": "agent fixture recording", "expected_state": "revision recorded",
			"control_ids": []string{"control:agent-fixture"},
		}},
		"controls": []map[string]any{{
			"control_id": "control:agent-fixture", "owner_id": "owner:agent-fixture",
			"predicate_ids": predicates, "case_ids": []string{"case:agent-fixture"},
			"recipe_source": map[string]any{"kind": "repository_file", "project_id": project, "path": "scripts/oracle_check.sh", "commit_oid": commit},
			"argv":          []string{"bash", "scripts/oracle_check.sh", "agent-fixture"}, "cwd": "internal/store",
			"expected_result": "pass", "required_evidence_role": "reported",
			"readiness_evidence_refs": []string{readiness},
		}},
	}
}

func TestOwnerOracleAgentFixtureTracksApprovedPredicatesAndRetainedReadiness(t *testing.T) {
	s, _, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	seedWorkerRetryMutationFixture(t, s, grant)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload)
		VALUES('work-1',1,'predicate:second-approved',1,'check','{"kind":"check","check_ref":"check:second","immutable_subject_ref":"commit:second","expected_result":"pass"}');
		DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	oracle := agentOracleFieldsForTest(t, s)
	owner := oracle["owners"].([]map[string]any)[0]
	control := oracle["controls"].([]map[string]any)[0]
	for _, entry := range []map[string]any{owner, control} {
		ids := entry["predicate_ids"].([]string)
		if len(ids) != 2 || ids[0] != "predicate:retry-objective" || ids[1] != "predicate:second-approved" {
			t.Fatalf("oracle fixture predicates = %v, want both active contract predicates", ids)
		}
	}
	refs := control["readiness_evidence_refs"].([]string)
	if len(refs) != 1 {
		t.Fatalf("readiness refs = %v, want one retained evidence binding", refs)
	}
	var retained int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events e JOIN durable_operations o
		ON o.op_id=json_extract(e.payload,'$.producer_run_ref') AND o.work_id=e.subject_id
		WHERE e.subject_id='work-1' AND e.kind=? AND json_extract(e.payload,'$.immutable_subject_ref')=?
		AND o.result_kind='completed' AND EXISTS(SELECT 1 FROM json_each(o.evidence_refs) WHERE value=?)`,
		store.WorkflowEvidenceBound, refs[0], refs[0]).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 1 {
		t.Fatalf("readiness ref %s has %d retained producer bindings, want 1", refs[0], retained)
	}
}

// recordReadyRetryJobWithChecks records one dispatch-ready worker-job
// revision. A verification worker job requires nonempty checks; the helper
// passes them through when the caller supplies them. On an oracle-capable
// pin the recording carries the fixture oracle the definition requires.
func recordReadyRetryJobWithChecks(t *testing.T, s *store.Store, service *Service, env CallEnvelope, jobID string, contractVersion int64, checks []string) {
	t.Helper()
	const workID = "work-1"
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{
		"job_id": jobID, "objective": "Carry out the approved retry objective.",
		"stopping_condition": "The approved objective's checks pass.",
		"ready":              true, "readiness_evidence": []string{"contract:" + workID + ":" + strconv.FormatInt(contractVersion, 10)},
	}
	if len(checks) > 0 {
		fields["checks"] = checks
	}
	if oracleCapableAgentPin(t, s) {
		fields["acceptance_oracle"] = agentOracleFieldsForTest(t, s)
	}
	raw, err := json.Marshal(map[string]any{
		"work_id": workID, "expected_version": version, "action_id": "record_worker_job", "idempotency_key": "record-ready-" + jobID,
		"fields": fields,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if result.Outcome != OutcomeOK {
		t.Fatalf("record the ready retry job: %+v", result.Error)
	}
}

// retryWorkVersion reads work-1's current version, the expected version a
// dispatch takes after the fixture records its ready worker job.
func retryWorkVersion(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
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
func authorizedWorkerJob(t *testing.T, s *store.Store, attemptID string) *store.WorkerJobBinding {
	t.Helper()
	const workID = "work-1"
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

// recordedAgentOracle reads the oracle under the dispatch's exact job binding.
func recordedAgentOracle(t *testing.T, s *store.Store, binding *store.WorkerJobBinding) *store.AcceptanceOracle {
	t.Helper()
	if binding == nil {
		return nil
	}
	views, err := s.WorkerJobRevisions(context.Background(), "work-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding == *binding {
			return view.AcceptanceOracle
		}
	}
	t.Fatalf("the authorized worker job has no recorded revision: %+v", binding)
	return nil
}
