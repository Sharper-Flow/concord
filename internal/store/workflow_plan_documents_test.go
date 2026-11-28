package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWorkflowPlanDocumentsMigrationFlow(t *testing.T) {
	for _, tc := range []struct{ typ, action, table, document string }{
		{"change", "record_plan", "workflow_plan_records", `{"outcomes":[{"id":"outcome:one","statement":"working outcome","done_condition":"works","evidence":"test","depends_on":[]}],"approach":"bounded change","decisions":[],"touches":[{"path":"file:one","role":"change"}],"merge_split":"one item","risks":"bounded risk"}`},
		{"removal", "record_removal_scope", "workflow_removal_scopes", `{"removed":["file:one"],"retiring_clauses":[],"note":"retire this"}`},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			seedWork(t, s, "work-document")
			seedWorkflowLaw(t, s)
			ctx := t.Context()
			if err := s.Transact(ctx, func(tx *Transaction) error {
				scope := newWriteScope(tx.tx)
				if err := scope.enter(ctx); err != nil {
					return err
				}
				if _, err := tx.tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,instance_state) VALUES('work-document','intake')`); err != nil {
					return err
				}
				return scope.close(ctx)
			}); err != nil {
				t.Fatal(err)
			}
			var version int64
			if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-document'`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			seq := 0
			apply := func(action, payload string) error {
				seq++
				actionCtx, err := s.PrepareWorkflowActionProof(ctx, action, "work-document", json.RawMessage(payload))
				if err != nil {
					return err
				}
				r := WorkflowActionExecutionRequest{WorkID: "work-document", ExpectedVersion: version, ActionID: action, Payload: json.RawMessage(payload), Actor: stepFixtureActor(), OperationID: fmt.Sprintf("doc-op-%d", seq), IdempotencyIdentity: fmt.Sprintf("doc-key-%d", seq), PrincipalRef: "principal-1", RequestID: fmt.Sprintf("doc-request-%d", seq), AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()}
				var result WorkflowActionExecutionResult
				err = s.Transact(actionCtx, func(tx *Transaction) error {
					var err error
					result, err = ApplyWorkflowActionTx(actionCtx, tx, r)
					return err
				})
				if err == nil {
					version = result.ResultingVersion
				}
				return err
			}
			if err := apply("record_intake", `{"outcome":"proceed"}`); err != nil {
				t.Fatal(err)
			}
			if err := apply("pick_type", fmt.Sprintf(`{"type":%q,"reason":"initial choice"}`, tc.typ)); err != nil {
				t.Fatal(err)
			}
			if err := apply("record_clarify", `{"resolved_questions":[],"note":"No operator questions remain."}`); err != nil {
				t.Fatal(err)
			}
			if err := apply("complete_step", `{"step_id":"clarify","note":"Clarification complete."}`); err != nil {
				t.Fatal(err)
			}
			var before string
			if err := s.db.QueryRowContext(ctx, `SELECT step_plan FROM workflow_instances WHERE work_id='work-document'`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := s.Transact(ctx, func(tx *Transaction) error {
				plan, err := readWorkflowPlanTx(ctx, tx.tx, "work-document")
				if err != nil {
					return err
				}
				step := "plan"
				if tc.typ == "removal" {
					step = "list"
				}
				if requireWorkflowPlanDocumentTx(ctx, tx.tx, "work-document", plan, step) == nil {
					return fmt.Errorf("missing document admitted")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := apply(tc.action, tc.document); err != nil {
				t.Fatal(err)
			}
			var stored, eventRef, actor, after string
			var docVersion int64
			if err := s.db.QueryRowContext(ctx, `SELECT payload,event_ref,actor,work_version FROM `+tc.table+` WHERE work_id='work-document'`).Scan(&stored, &eventRef, &actor, &docVersion); err != nil {
				t.Fatal(err)
			}
			if !json.Valid([]byte(stored)) || eventRef == "" || actor == "" || docVersion > version {
				t.Fatalf("bad projection %s %q %q %d", stored, eventRef, actor, docVersion)
			}
			if err := s.Transact(ctx, func(tx *Transaction) error {
				plan, err := readWorkflowPlanTx(ctx, tx.tx, "work-document")
				if err != nil {
					return err
				}
				step := "plan"
				if tc.typ == "removal" {
					step = "list"
				}
				return requireWorkflowPlanDocumentTx(ctx, tx.tx, "work-document", plan, step)
			}); err != nil {
				t.Fatal(err)
			}
			if err := apply(tc.action, `{}`); err == nil {
				t.Fatal("invalid document accepted")
			}
			var unchanged string
			var persistedVersion int64
			if err := s.db.QueryRowContext(ctx, `SELECT payload FROM `+tc.table+` WHERE work_id='work-document'`).Scan(&unchanged); err != nil || unchanged != stored {
				t.Fatalf("invalid document changed projection: %q %v", unchanged, err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-document'`).Scan(&persistedVersion); err != nil || persistedVersion != version {
				t.Fatalf("invalid document changed version: %d %v", persistedVersion, err)
			}
			if tc.typ == "change" {
				if err := apply(tc.action, tc.document); err == nil {
					t.Fatal("revision without reason accepted")
				}
				if err := s.db.QueryRowContext(ctx, `SELECT payload FROM workflow_plan_records WHERE work_id='work-document'`).Scan(&unchanged); err != nil || unchanged != stored {
					t.Fatalf("refused revision changed projection: %q %v", unchanged, err)
				}
				if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work-document'`).Scan(&persistedVersion); err != nil || persistedVersion != version {
					t.Fatalf("refused revision changed version: %d %v", persistedVersion, err)
				}
				revision := strings.TrimSuffix(tc.document, "}") + `,"reason":"new evidence"}`
				if err := apply(tc.action, revision); err != nil {
					t.Fatal(err)
				}
				if err := s.db.QueryRowContext(ctx, `SELECT payload FROM workflow_plan_records WHERE work_id='work-document'`).Scan(&stored); err != nil || !strings.Contains(stored, `"reason":"new evidence"`) {
					t.Fatalf("revision %q: %v", stored, err)
				}
			}
			if err := s.db.QueryRowContext(ctx, `SELECT step_plan FROM workflow_instances WHERE work_id='work-document'`).Scan(&after); err != nil || after != before {
				t.Fatalf("document action changed step plan: %v", err)
			}
			step := "plan"
			if tc.typ == "removal" {
				step = "list"
			}
			// approve first: CD-0203 D1 forbids closing the contract step without a contract
			if err := apply("approve_contract", contractStepApprovalPayload); err != nil {
				t.Fatal(err)
			}
			if err := apply("complete_step", fmt.Sprintf(`{"step_id":%q,"note":"document recorded"}`, step)); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRowContext(ctx, `SELECT payload FROM `+tc.table+` WHERE work_id='work-document'`).Scan(&after); err != nil || after != stored {
				t.Fatalf("later action lost document %q: %v", after, err)
			}
		})
	}
}

func TestWorkflowPlanDocumentGateAcceptsStoredObject(t *testing.T) {
	const workID = "work-document-object"
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "plan")
	defer f.store.Close()
	ctx := t.Context()
	err := f.store.Transact(ctx, func(tx *Transaction) error {
		scope, err := beginWrite(ctx, tx.tx)
		if err != nil {
			return err
		}
		_, err = tx.tx.ExecContext(ctx, `INSERT INTO workflow_plan_records(work_id,work_version,payload,recorded_at,actor,event_ref) SELECT ?,1,?,'now',actor_ref,'event:fixture' FROM workflow_actors LIMIT 1`, workID, `{"outcomes":"previously recorded"}`)
		if err != nil {
			return err
		}
		plan, err := readWorkflowPlanTx(ctx, tx.tx, workID)
		if err != nil {
			return err
		}
		if err := requireWorkflowPlanDocumentTx(ctx, tx.tx, workID, plan, "plan"); err != nil {
			return err
		}
		return scope.close(ctx)
	})
	if err != nil {
		t.Fatalf("stored object refused: %v", err)
	}
	documents, err := ReadWorkflowDocuments(ctx, f.store, workID)
	if err != nil {
		t.Fatal(err)
	}
	if documents.Documents.Plan == nil || documents.Documents.Plan.Document != `{"outcomes":"previously recorded"}` {
		t.Fatalf("resume did not return the stored document: %+v", documents.Documents.Plan)
	}
}

func TestWorkflowPlanDocumentGateCompletesEarlierPlan(t *testing.T) {
	const workID = "work-document-earlier"
	const earlier = `{"outcomes":"working outcome","approach":"bounded change","decisions":[{"id":"decision:one","question":"which?","choice":"one","rationale":"fits","rejected":[]}],"touches":["file:one"],"merge_split":"one item","risks":"bounded risk"}`
	f := seedWorkflowReturnRouteFixture(t, workID, WorkflowStepPlanChange, "plan")
	defer f.store.Close()
	ctx := t.Context()
	complete := func(key string) error {
		version := readWorkVersion(t, f.store, workID)
		return f.store.Transact(ctx, func(tx *Transaction) error {
			_, err := ApplyWorkflowActionTx(ctx, tx, WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: "complete_step", Payload: json.RawMessage(`{"step_id":"plan","note":"plan complete"}`), Actor: f.owner, OperationID: key, IdempotencyIdentity: key, PrincipalRef: f.owner.PrincipalRef, RequestID: key, AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()})
			return err
		})
	}
	assertFailureKind(t, complete("missing-document"), KindMissingEvidence)
	db := f.store.DatabaseForTesting()
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON; INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_plan_records(work_id,work_version,payload,recorded_at,actor,event_ref) SELECT ?,1,'[]','now',actor_ref,'event:earlier' FROM workflow_actors LIMIT 1`, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard; PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, complete("non-object-document"), KindInvariantViolation)
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workflow_plan_records SET payload=? WHERE work_id=?`, earlier, workID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := complete("earlier-document"); err != nil {
		t.Fatalf("earlier plan prevented completion: %v", err)
	}
	if got := workflowPlanFixtureFrontier(t, f.store, workID); got != "implement" {
		t.Fatalf("frontier after completion = %q", got)
	}
}
