package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Historical instances are actual schema-160 data, not current initialization
// with its manifest relabeled. No owning-start inventory exists for these items.
func currentLawUpgradeFixture(t *testing.T, id, step string, contract, boundary int) (*Store, WorkflowActor) {
	t.Helper()
	prefix := migrationPrefixBefore(t, "workflow_owning_start_law_baseline")
	path := filepath.Join(t.TempDir(), "before-law-attribution.db")
	s := &Store{db: openMigratedTo(t, path, prefix), path: path, Clock: func() time.Time { return time.Unix(100, 0).UTC() }}
	seedWork(t, s, id)
	owner := WorkflowActor{PrincipalRef: "principal/upgrade", ClientRef: "client/upgrade", AgentRef: "agent/main", SessionRef: "session/upgrade", ActorClass: ActorAgent}
	ref := ownerRefForTest(t, owner)
	entry := mustBuiltinDefinition(t, "workflow.research")
	execSixClassSQL(t, s.db, `INSERT INTO write_guard(active) VALUES(1)`)
	execSixClassSQL(t, s.db, `UPDATE work_items SET lifecycle='in_progress' WHERE id=?`, id)
	execSixClassSQL(t, s.db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','2026-10-01T00:00:00Z')`, ref, owner.PrincipalRef, owner.ClientRef, owner.AgentRef, owner.SessionRef)
	execSixClassSQL(t, s.db, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,started_at) VALUES(?,?,?,?,'running',?,'2026-10-01T00:00:00Z')`, id, entry.Definition.Ref, entry.Digest, step, ref)
	if contract != 0 {
		execSixClassSQL(t, s.db, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,decision_domains,definition_ref,definition_digest) VALUES(?,1,'Record the bounded research result','internal_sqlite','["artifact","verification"]','[]','2026-10-01T00:00:00Z',?,'[]','[]',?,'[]',?,?)`, id, ref, boundary, entry.Definition.Ref, entry.Digest)
		execSixClassSQL(t, s.db, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'outcome','{"kind":"outcome","allowed":["report_recorded"]}')`, id)
	}
	execSixClassSQL(t, s.db, `DELETE FROM write_guard`)
	if step == "complete" {
		currentLawUpgradeCompletionProof(t, s, id, owner)
	}
	tables := []string{"work_items", "workflow_contract_predicates", "workflow_actors", "effect_receipts", "workflow_evidence_bindings"}
	before := sixClassSnapshot(t, s.db, tables...)
	contracts := retainedWorkflowContractRows(t, s.db)
	const instanceQuery = `SELECT work_id,definition_ref,definition_digest,current_step,instance_state,execution_actor_ref,execution_model,started_at,completed_at,last_checkpoint_at,execution_started_at,completion_payload,completion_occurred_at FROM workflow_instances ORDER BY work_id`
	instances := retirementUpgradeRows(t, s.db, instanceQuery)
	if err := Migrate(t.Context(), s.db); err != nil {
		t.Fatal(err)
	}
	if got := sixClassSnapshot(t, s.db, tables...); !reflect.DeepEqual(got, before) {
		t.Fatalf("migration rewrote retained workflow facts: before=%v after=%v", before, got)
	}
	if got := retirementUpgradeRows(t, s.db, instanceQuery); !reflect.DeepEqual(got, instances) {
		t.Fatalf("migration rewrote current workflow instance facts: before=%v after=%v", instances, got)
	}
	if got := retainedWorkflowContractRows(t, s.db); !reflect.DeepEqual(got, contracts) {
		t.Fatal("migration rewrote current contract facts")
	}
	return s, owner
}

func currentLawUpgradeAction(t *testing.T, s *Store, id string, owner WorkflowActor, action string, fields map[string]any) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(t.Context(), s, id, action)
	if err != nil {
		return err
	}
	version := readWorkVersion(t, s, id)
	op := fmt.Sprintf("upgrade:%s:%s:%d", id, action, version)
	payload := mustJSONValue(fields)
	return s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: id, ExpectedVersion: version, ActionID: action, Payload: payload, Actor: owner,
			EvidenceRefs: actionEvidenceRefs(action, payload), AcceptedInputsDigest: testManifestDigest,
			IdempotencyIdentity: op, OperationID: op, PrincipalRef: owner.PrincipalRef,
			Tool: "concord_work_transition", IdempotencyKey: op, RequestID: "request:" + op,
			ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC(),
		})
		return err
	})
}

func currentLawUpgradeCompletionProof(t *testing.T, s *Store, id string, owner WorkflowActor) {
	t.Helper()
	seedWorkflowLaw(t, s)
	execSixClassSQL(t, s.db, `INSERT INTO write_guard(active) VALUES(1)`)
	execSixClassSQL(t, s.db, `UPDATE workflow_instances SET current_step='findings' WHERE work_id=?`, id)
	execSixClassSQL(t, s.db, `DELETE FROM write_guard`)
	for _, action := range []string{"record_report", "record_conclusion"} {
		fields := map[string]any{}
		if action == "record_report" {
			fields = map[string]any{"evidence_kind": "artifact", "immutable_subject_ref": "evidence:upgrade-report"}
		}
		if err := currentLawUpgradeAction(t, s, id, owner, action, fields); err != nil {
			t.Fatalf("ordinary %s before upgrade: %v", action, err)
		}
	}
	journey := pReturnClaim(t, workflowReturnRouteFixture{store: s, owner: owner}, id)
	journey.acceptor = owner
	run := journey.observeRun(t, "upgrade-proof")
	journey.bindVerification(t, run)
	qAcceptCriterionReview(t, journey.fixture, run.OperationRef)
	if err := currentLawUpgradeAction(t, s, id, owner, "record_verdict", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{run.OperationRef}}}}); err != nil {
		t.Fatal(err)
	}
	if err := currentLawUpgradeAction(t, s, id, owner, "confirm_premise", map[string]any{}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowLawUpgradeCurrentContractRoutes(t *testing.T) {
	for _, action := range []string{"approve_contract", "revise_intent"} {
		t.Run(action, func(t *testing.T) {
			id, step, contract := "upgrade-"+action, "frame", 0
			if action == "revise_intent" {
				step, contract = "conclude", 1
			}
			s, owner := currentLawUpgradeFixture(t, id, step, contract, 1)
			fields := map[string]any{
				"premise": "Record the bounded research result", "decision_domains": []string{},
				"outcome_predicates": []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}},
				"required_evidence":  []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{}, "rigor_class": "prototype_internal",
			}
			if action == "revise_intent" {
				fields["contract_version"] = 2
				if err := reviseContractRecord(t, s, id, owner, fields, readWorkVersion(t, s, id), nil); err != nil {
					t.Fatalf("ordinary upgraded contract update: %v", err)
				}
			} else if err := currentLawUpgradeAction(t, s, id, owner, action, fields); err != nil {
				t.Fatalf("ordinary upgraded approval: %v", err)
			}
			if err := currentLawUpgradeAction(t, s, id, owner, "checkpoint_context", map[string]any{
				"active_unit": "unit:research", "hypothesis": "Historical work stays usable", "diagnosis": "Use current law", "strategy": "Continue the ordinary workflow",
				"touched_refs": []string{"ref:research"}, "evidence_refs": []string{"evidence:upgrade"}, "pending_questions": []string{}, "pending_decisions": []string{},
			}); err != nil {
				t.Fatalf("ordinary upgraded context write: %v", err)
			}
			pin, err := ReadWorkPin(t.Context(), s, id)
			if err != nil || pin.Lifecycle != "in_progress" {
				t.Fatalf("upgraded work pin=%+v error=%v", pin, err)
			}
		})
	}
}

func TestWorkflowLawUpgradeCurrentCompletionRoutes(t *testing.T) {
	for _, boundary := range []int{0, 1} {
		for _, route := range []string{"ordered_completion", "complete_action"} {
			t.Run(fmt.Sprintf("boundary_%d/%s", boundary, route), func(t *testing.T) {
				id := fmt.Sprintf("upgrade-complete-%d-%s", boundary, route)
				s, owner := currentLawUpgradeFixture(t, id, "complete", 1, boundary)
				var err error
				if route == "complete_action" {
					err = currentLawUpgradeAction(t, s, id, owner, "complete", map[string]any{"impact_verdict": "non-breaking"})
				} else {
					version := readWorkVersion(t, s, id)
					var evidenceCount int
					if err := s.db.QueryRow(`SELECT count(*) FROM workflow_evidence_bindings WHERE work_id=?`, id).Scan(&evidenceCount); err != nil {
						t.Fatal(err)
					}
					actorRef := ownerRefForTest(t, owner)
					event := workflowEventWithActor("upgrade:ordered-completion", WorkflowCompleted, id, actorRef, map[string]any{
						"work_id": id, "expected_version": version, "resulting_version": version + 1,
						"terminal_state": "completed", "impact_verdict": "non-breaking", "final_verdict_kind": "ok",
						"verdict_actor_ref": actorRef, "premise_confirmed": true, "evidence_count": evidenceCount, "changed_refs_digest": testManifestDigest,
					})
					event.PayloadVersion = CurrentEventPayloadVersion(WorkflowCompleted)
					err = CompleteWorkflow(t.Context(), s, event)
				}
				if err != nil {
					t.Fatalf("ordinary upgraded completion: %v", err)
				}
				pin, err := ReadWorkPin(t.Context(), s, id)
				if err != nil || pin.Lifecycle != "completed" {
					t.Fatalf("upgraded completed pin=%+v error=%v", pin, err)
				}
			})
		}
	}
}
