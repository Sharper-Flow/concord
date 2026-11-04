package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The predecessor is written with the schema-182 layout, not current writers.
func currentLawUpgradeFixture(t *testing.T, contract, boundary int) (*Store, WorkflowActor) {
	t.Helper()
	useStampedBuild(t)
	path := filepath.Join(t.TempDir(), "before-step-plan.db")
	db := openMigratedTo(t, path, migrationPrefixBefore(t, "workflow_step_plan_cutover"))
	owner := WorkflowActor{PrincipalRef: "principal/upgrade", ClientRef: "client/upgrade", AgentRef: "agent/main", SessionRef: "session/upgrade", ActorClass: ActorAgent}
	actor := ownerRefForTest(t, owner)
	digest := "sha256:" + strings.Repeat("a", 64)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product','Product','prototype','operator_only',1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project','Project',1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO product_projects VALUES('product','project','primary')`)
	retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('upgrade-law','research','Historical research','in_progress',0,2,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO work_projects VALUES('upgrade-law','project','primary')`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent',?)`, actor, owner.PrincipalRef, owner.ClientRef, owner.AgentRef, owner.SessionRef, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at,last_checkpoint_at) VALUES('upgrade-law','workflow.research',?,'conclude','running',?,?,?)`, digest, actor, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO linear_issue_links VALUES('upgrade-law','synthetic-issue','TEST-1574','https://example.com/issues/TEST-1574',?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES('upgrade-delivered','task','Delivered research','in_progress',0,1,?,?)`, retirementUpgradeTime, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO work_projects VALUES('upgrade-delivered','project','primary')`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES('upgrade-delivered','workflow.research',?,'conclude','running',?,?)`, digest, actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload) VALUES('upgrade-delivered',1,'delivery:historical',?,?,1,'{ "delivery_artifact": "https://example.com/change/1", "delivery_state": "asserted" }')`, actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_review_state(work_id,state) VALUES('upgrade-law','{ "accepted": { "report_ref": "review:historical", "report": { "disposition": "pass" } } }')`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_context_checkpoints(work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES('upgrade-law',1,1,'checkpoint:history','conclude',1,'unit:research','Historical hypothesis','Historical diagnosis','Historical strategy','[ "ref:research" ]','[ "evidence:history" ]','[]','[]','workflow.research',?,?,'request:history',?)`, digest, actor, retirementUpgradeTime)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES('upgrade-law',2,1,1,'boundary:history','summary','checkpoint:history',1,1,'Historical summary','workflow.research',?,?,'request:history',?)`, digest, actor, retirementUpgradeTime)
	if contract != 0 {
		retirementUpgradeExec(t, db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,decision_domains,definition_ref,definition_digest) VALUES('upgrade-law',1,'Record the bounded research result','internal_sqlite','[ "artifact" ]','[]',?,?,'[]','[]',?,'[]','workflow.research',?)`, retirementUpgradeTime, actor, boundary, digest)
		retirementUpgradeExec(t, db, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('upgrade-law',1,'predicate:primary',0,'outcome','{ "kind": "outcome", "allowed": ["report_recorded"] }')`)
	}
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	queries := map[string]string{
		"work":        `SELECT * FROM work_items WHERE id='upgrade-law'`,
		"actors":      `SELECT * FROM workflow_actors ORDER BY actor_ref`,
		"contracts":   `SELECT work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,superseded_by,spec_mandate,rigor_class,law_modifies,law_boundary_version,self_repair_json,supersession_seq,last_healthy_seq,decision_domains FROM workflow_contracts ORDER BY work_id,contract_version`,
		"checkpoints": `SELECT work_id,work_version,checkpoint_sequence,checkpoint_id,step_id,attempt_epoch,active_unit,hypothesis,diagnosis,strategy,touched_refs,evidence_refs,pending_questions,pending_decisions,workflow_ref,actor_ref,request_id,recorded_at FROM workflow_context_checkpoints ORDER BY work_id,checkpoint_sequence`,
		"boundaries":  `SELECT work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,actor_ref,request_id,recorded_at FROM workflow_context_boundaries ORDER BY work_id,boundary_sequence`,
		"membership":  `SELECT * FROM work_projects ORDER BY work_id`,
		"identity":    `SELECT * FROM linear_issue_links ORDER BY work_id`,
		"delivery":    `SELECT * FROM workflow_delivery_state ORDER BY work_id`,
		"receipts":    `SELECT * FROM effect_receipts ORDER BY effect_seq`,
		"bindings":    `SELECT * FROM workflow_evidence_bindings ORDER BY work_id,binding_seq`,
	}
	before := make(map[string][][]any, len(queries))
	for name, query := range queries {
		before[name] = retirementUpgradeRows(t, db, query)
	}
	if got := retirementUpgradeRows(t, db, `SELECT state FROM workflow_review_state WHERE work_id='upgrade-law'`); len(got) != 1 {
		t.Fatalf("historical Review missing before upgrade: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT predicate_id FROM workflow_contract_predicates WHERE work_id='upgrade-law'`); len(got) != contract {
		t.Fatalf("historical predicate count before upgrade=%v, want %d", got, contract)
	}
	snapshot := filepath.Join(t.TempDir(), "pre-step-plan.db")
	if _, err := BackupDatabase(t.Context(), path, snapshot); err != nil {
		t.Fatal(err)
	}
	report, err := Upgrade(t.Context(), path, nil, UpgradeOptions{SnapshotPath: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	var wantApplied []int
	for _, m := range migrations {
		if m.Version > 182 {
			wantApplied = append(wantApplied, m.Version)
		}
	}
	if !reflect.DeepEqual(report.Applied, wantApplied) {
		t.Fatalf("applied migrations=%v, want %v", report.Applied, wantApplied)
	}
	for name, query := range queries {
		if got := retirementUpgradeRows(t, db, query); !reflect.DeepEqual(got, before[name]) {
			t.Errorf("migration changed %s: before=%v after=%v", name, before[name], got)
		}
	}
	if got := retirementUpgradeRows(t, db, `SELECT instance_state,workflow_type,step_plan,execution_actor_ref,started_at,last_checkpoint_at FROM workflow_instances WHERE work_id='upgrade-law'`); !reflect.DeepEqual(got, [][]any{{"intake", nil, nil, nil, retirementUpgradeTime, retirementUpgradeTime}}) {
		t.Fatalf("undelivered predecessor did not park at intake: %v", got)
	}
	if got := retirementUpgradeRows(t, db, `SELECT w.lifecycle,i.instance_state,i.workflow_type,i.step_plan,i.execution_actor_ref,w.terminal_time FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id='upgrade-delivered'`); !reflect.DeepEqual(got, [][]any{{"completed", "completed", nil, nil, actor, retirementUpgradeTime}}) {
		t.Fatalf("delivered predecessor did not complete: %v", got)
	}
	retirementUpgradeHealthy(t, db)
	return &Store{db: db, path: path, Clock: func() time.Time { return time.Unix(100, 0).UTC() }}, owner
}

func currentLawUpgradeAction(t *testing.T, s *Store, owner WorkflowActor, action string, fields map[string]any) error {
	t.Helper()
	const id = "upgrade-law"
	ctx, err := prepareTerminalTestContext(t.Context(), s, id, action)
	if err != nil {
		return err
	}
	version := readWorkVersion(t, s, id)
	op := fmt.Sprintf("upgrade:%s:%d", action, version)
	payload := mustJSONValue(fields)
	return s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, WorkflowActionExecutionRequest{
			WorkID: id, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
			EvidenceRefs: actionEvidenceRefs(action, payload), AcceptedInputsDigest: testManifestDigest,
			IdempotencyIdentity: op, OperationID: op, PrincipalRef: owner.PrincipalRef,
			Tool: "concord_work_transition", IdempotencyKey: op, RequestID: "request:" + op,
			ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC(),
		})
		return err
	})
}

func TestWorkflowLawUpgradeCurrentContractRoutes(t *testing.T) {
	for _, action := range []string{"approve_contract", "revise_intent"} {
		t.Run(action, func(t *testing.T) {
			contract := 0
			if action == "revise_intent" {
				contract = 1
			}
			s, owner := currentLawUpgradeFixture(t, contract, 1)
			seedWorkflowLaw(t, s)
			seedWorkflowPlanFrontier(t, s, "upgrade-law", WorkflowStepPlanResearch, firstCallApprovalFrontier(t, WorkflowStepPlanResearch))
			fields := map[string]any{
				"premise": "Record the bounded research result", "decision_domains": []string{},
				"legislative_subjects": []string{}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal",
			}
			if action == "revise_intent" {
				fields["contract_version"] = 2
				if err := reviseContractRecord(t, s, "upgrade-law", owner, fields, readWorkVersion(t, s, "upgrade-law")); err != nil {
					t.Fatalf("upgraded contract update: %v", err)
				}
			} else if err := currentLawUpgradeAction(t, s, owner, action, fields); err != nil {
				t.Fatalf("upgraded contract approval: %v", err)
			}
			contractData, err := readWorkflowContractVersion(t.Context(), s.db, "upgrade-law", int64(contract+1))
			if err != nil || contractData.Version != int64(contract+1) {
				t.Fatalf("current contract=%+v error=%v", contractData, err)
			}
		})
	}
}

func TestWorkflowLawUpgradeCurrentCompletionRoutes(t *testing.T) {
	for _, boundary := range []int{0, 1} {
		t.Run(fmt.Sprintf("boundary_%d", boundary), func(t *testing.T) {
			s, owner := currentLawUpgradeFixture(t, 1, boundary)
			seedWorkflowPlanFrontier(t, s, "upgrade-law", WorkflowStepPlanResearch, currentWorkflowPlanFixture(t, WorkflowStepPlanResearch).Steps[0].ID)
			payload := mustJSONValue(map[string]any{"impact_verdict": "non-breaking"})
			request := WorkflowActionPreflightRequest{WorkID: "upgrade-law", ExpectedVersion: readWorkVersion(t, s, "upgrade-law"), ActionID: "complete", Payload: payload, Actor: owner}
			if _, err := WorkflowActionDefinitionFor(t.Context(), s, request); err == nil {
				t.Fatal("completion admitted with unfinished steps")
			}
			completeWorkflowPlanFixture(t, s, "upgrade-law")
			if _, err := WorkflowActionDefinitionFor(t.Context(), s, request); err != nil {
				t.Fatalf("completion preflight after plan: %v", err)
			}
			stale := request
			stale.ExpectedVersion--
			if _, err := WorkflowActionDefinitionFor(t.Context(), s, stale); err == nil {
				t.Fatal("completion admitted a stale work version")
			}
			invalidActor := request
			invalidActor.Actor = WorkflowActor{}
			if _, err := WorkflowActionDefinitionFor(t.Context(), s, invalidActor); err == nil {
				t.Fatal("completion admitted an invalid actor")
			}
			if err := currentLawUpgradeAction(t, s, owner, "complete", map[string]any{"impact_verdict": "non-breaking"}); err != nil {
				t.Fatalf("upgraded completion: %v", err)
			}
			pin, err := ReadWorkPin(t.Context(), s, "upgrade-law")
			if err != nil || pin.Lifecycle != "completed" {
				t.Fatalf("completed pin=%+v error=%v", pin, err)
			}
		})
	}
}

func TestWorkflowLawUpgradeCompletionRequiresActiveContract(t *testing.T) {
	s, owner := currentLawUpgradeFixture(t, 0, 0)
	plan := currentWorkflowPlanFixture(t, WorkflowStepPlanResearch)
	seedWorkflowPlanFrontier(t, s, "upgrade-law", WorkflowStepPlanResearch, plan.Steps[0].ID)
	completeWorkflowPlanFixture(t, s, "upgrade-law")
	if err := currentLawUpgradeAction(t, s, owner, "complete", map[string]any{"impact_verdict": "non-breaking"}); err == nil {
		t.Fatal("completion without an active contract succeeded")
	}
	pin, err := ReadWorkPin(t.Context(), s, "upgrade-law")
	if err != nil || pin.Lifecycle != "in_progress" {
		t.Fatalf("work changed after refused completion: pin=%+v error=%v", pin, err)
	}
}
