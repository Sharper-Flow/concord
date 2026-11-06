package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// removeWorkForTest drives removal the way the only production caller does:
// prepare the operation and apply the typed work.removed event inside one
// transaction, exactly as internal/agent planWorkRemoval's effect does.
func removeWorkForTest(ctx context.Context, s *Store, req WorkRemovalRequest) error {
	return s.Transact(ctx, func(tx *Transaction) error {
		prepared, err := PrepareWorkRemovalTx(ctx, tx, req)
		if err != nil {
			return err
		}
		payload, err := MarshalWorkRemovedPayload(req, prepared.HandoffDigest)
		if err != nil {
			return err
		}
		_, err = ApplyOperationTx(ctx, tx, Operation{Events: []Event{{EventID: "work.removed:" + req.OperationID, Kind: WorkRemoved, SubjectType: SubjectWorkItem, SubjectID: req.WorkID, Actor: req.Actor, OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, req.WorkID): req.ExpectedVersion}})
		return err
	})
}

func TestWorkRemovalRefusesCompletedWork(t *testing.T) {
	s := seedLawChangeFixture(t, "inventory-seed")
	attributionItem(t, s, "cleanup-producer", []string{}, []string{})
	if err := CompleteWorkflow(t.Context(), s, attributionCompletion(t, s, "cleanup-producer")); err != nil {
		t.Fatal(err)
	}
	producer := removalTestRequest()
	producer.WorkID, producer.ExpectedVersion = "cleanup-producer", readWorkVersion(t, s, "cleanup-producer")
	confirmRemovalHandoff(t, s, &producer)
	err := removeWorkForTest(t.Context(), s, producer)
	assertFailureKind(t, err, KindInvalidOperation)
	if err == nil || !strings.Contains(err.Error(), "completed work cannot be removed") {
		t.Fatalf("completed work refusal=%v", err)
	}
}

func removalTestRequest() WorkRemovalRequest {
	return WorkRemovalRequest{
		OperationID: "remove-op-1", IdempotencyKey: "remove-key-1", WorkID: "remove-work-1",
		ExpectedVersion: 2, Reason: "shelved", Actor: "operator:one",
		Handoff: WorkRemovalHandoff{
			Findings: []string{"finding"}, RemainingScope: []string{"scope"}, Blockers: []string{"blocker"},
			Artifacts: []string{"artifact"}, RenewalConditions: []string{"new approval"},
		}, ExecutionRelinquished: true, WritesReconciled: true, EffectsReconciled: true,
		DependenciesResolved: true, ArtifactsVerified: true,
	}
}

// confirmRemovalHandoff records a Linear issue identity for the work item and
// attaches the matching Linear handoff confirmation, so a removal fixture
// exercises its own gate instead of the CD-0142 D3 destination admission.
func confirmRemovalHandoff(t *testing.T, s *Store, req *WorkRemovalRequest) {
	t.Helper()
	remote := "remote-" + req.WorkID
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := RecordLinearIssueLinkTx(context.Background(), tx, LinearIssueLink{WorkID: req.WorkID, RemoteIssueUUID: remote, HumanKey: "EX-1", URL: "https://linear.app/example/issue/EX-1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	req.ProductID = "product"
	req.Linear = &LinearHandoffConfirmation{ProductID: "product", Destination: "linear", RemoteIssueUUID: remote, HandoffDigest: removalHandoffDigest(req.Handoff)}
}

func TestWorkRemovalLinearHandoffRequiresRecordedIdentity(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "remove-work-1")
	req := removalTestRequest()
	req.ProductID = "product"
	// CD-0142 D3: unlinked work with no Linear handoff confirmation refuses;
	// Linear is the only planning authority (CD-0213 D1), so no unconfirmed
	// removal path remains.
	err := validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req)
	assertFailureKind(t, err, KindInvalidPayload)
	if !strings.Contains(err.Error(), "no recorded Linear issue identity") || !strings.Contains(err.Error(), "carries no Linear handoff confirmation") {
		t.Fatalf("unlinked unconfirmed refusal=%v", err)
	}
	req.Linear = &LinearHandoffConfirmation{ProductID: "product", Destination: "linear", RemoteIssueUUID: "fabricated-issue"}
	err = validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req)
	assertFailureKind(t, err, KindInvalidRelation)
	if !strings.Contains(err.Error(), "no recorded Linear issue identity") {
		t.Fatalf("unrecorded handoff refusal=%v", err)
	}
	if err := s.Transact(ctx, func(tr *Transaction) error {
		_, err := RecordLinearIssueLinkTx(ctx, tr, LinearIssueLink{WorkID: req.WorkID, RemoteIssueUUID: "recorded-issue", HumanKey: "EX-1", URL: "https://linear.app/example/issue/EX-1"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertFailureKind(t, validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req), KindInvalidRelation)
	req.Linear.RemoteIssueUUID = "recorded-issue"
	if err := validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req); err != nil {
		t.Fatalf("recorded identity refusal=%v", err)
	}
	req.ProductID = "unrelated-product"
	req.Linear.ProductID = req.ProductID
	assertFailureKind(t, validateRemovalDestinationQ(ctx, s.DatabaseForTesting(), req), KindInvalidRelation)
}

func TestWorkRemovalRefusesMissingSafetyEvidence(t *testing.T) {
	req := removalTestRequest()
	req.Reason = "shelved"
	req.ArtifactsVerified = false
	err := validateRemovalRequest(req)
	assertFailureKind(t, err, KindInvalidPayload)
	if err == nil || !strings.Contains(err.Error(), "safety evidence is incomplete") {
		t.Fatalf("missing safety evidence refusal=%v", err)
	}
}

func TestWorkRemovalCancellationKeepsReasonDistinct(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "cancelled-removal-work")
	if err := s.Transact(t.Context(), func(tx *Transaction) error {
		return InitializeWorkflowTx(t.Context(), tx, WorkflowInitializationRequest{WorkID: "cancelled-removal-work", Actor: lawChangeConstructorActor})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard VALUES(1);
INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,changed_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES('removal-op',1,'cancelled-removal-work','workflow.test','digest','step','internal_sqlite','digest','{}','completed','{}','[]','[]','operator','request','t','t','digest');
DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	req := removalTestRequest()
	req.OperationID = "cancel-op-1"
	req.IdempotencyKey = "cancel-key-1"
	req.WorkID = "cancelled-removal-work"
	req.ExpectedVersion = readWorkVersion(t, s, req.WorkID)
	req.Reason = "cancelled"
	confirmRemovalHandoff(t, s, &req)
	if err := removeWorkForTest(context.Background(), s, req); err != nil {
		t.Fatal(err)
	}
	committed, err := readWorkRemovalAuditQ(context.Background(), s.DatabaseForTesting(), req.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Reason != "cancelled" || committed.State != "committed" || committed.EventID != "work.removed:"+req.OperationID {
		t.Fatalf("committed receipt = %+v", committed)
	}
	var removed, transitioned int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, req.WorkID, WorkRemoved).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind='work.transitioned'`, req.WorkID).Scan(&transitioned); err != nil {
		t.Fatal(err)
	}
	if removed != 1 || transitioned != 0 {
		t.Fatalf("removal events = %d, transition events = %d", removed, transitioned)
	}
	var operations int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations WHERE work_id=?`, req.WorkID).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("removal retained %d durable operations: %v", operations, err)
	}
}

func TestWorkRemovalDeletesCurrentAndMigratedClarification(t *testing.T) {
	useStampedBuild(t)
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "removal.db"), 188)
	ctx := t.Context()
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project','Project',1,'now','now')`)
	retirementUpgradeExec(t, db, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product','Product','prototype','operator_only',1,'now','now')`)
	retirementUpgradeExec(t, db, `INSERT INTO product_projects(product_id,project_id,role) VALUES('product','project','primary')`)
	retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES('migrated','task','Migrated','cancelled',1,'now','now')`)
	retirementUpgradeExec(t, db, `INSERT INTO work_projects(work_id,project_id,role) VALUES('migrated','project','primary')`)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_instances(work_id,instance_state,prelude_state) VALUES('migrated','cancelled','{"intake":{"outcome":"clarify","open_questions_found":true},"clarify":{"resolved_questions":["é"],"note":"Resolved."}}')`)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, Clock: time.Now}
	seedWork(t, s, "current")
	retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
	retirementUpgradeExec(t, db, `UPDATE work_items SET lifecycle='cancelled' WHERE id='current'`)
	actor := "actor:" + strings.Repeat("a", 64)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal','client','agent','session','agent','now')`, actor)
	retirementUpgradeExec(t, db, `INSERT INTO workflow_clarify_records(work_id,work_version,payload,recorded_at,actor,event_ref) VALUES('current',1,'{"resolved_questions":[],"note":"Resolved."}','now',?,'event:current')`, actor)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	for _, work := range []string{"current", "migrated"} {
		t.Run(work, func(t *testing.T) {
			var count int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM workflow_clarify_records WHERE work_id=?`, work).Scan(&count); err != nil || count != 1 {
				t.Fatalf("clarify count=%d err=%v", count, err)
			}
			req := removalTestRequest()
			req.WorkID, req.OperationID, req.IdempotencyKey = work, "remove-"+work, "key-"+work
			req.ExpectedVersion, req.Reason = readWorkVersion(t, s, work), "cancelled"
			confirmRemovalHandoff(t, s, &req)
			if err := removeWorkForTest(ctx, s, req); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM work_items WHERE id=?) + (SELECT count(*) FROM workflow_clarify_records WHERE work_id=?)`, work, work).Scan(&count); err != nil || count != 0 {
				t.Fatalf("remaining rows=%d err=%v", count, err)
			}
		})
	}
}

// drivePlanDocumentForTest advances one work item through the real workflow
// folds until the named document action records its row, the way the workflow
// dispatcher does in production.
func drivePlanDocumentForTest(t *testing.T, s *Store, workID, typ, action, document string) {
	t.Helper()
	ctx := t.Context()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		scope := newWriteScope(tx.tx)
		if err := scope.enter(ctx); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,instance_state) VALUES(?,'intake')`, workID); err != nil {
			return err
		}
		return scope.close(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	seq := 0
	apply := func(action, payload string) {
		t.Helper()
		seq++
		request := WorkflowActionExecutionRequest{WorkID: workID, ExpectedVersion: version, ActionID: action, Payload: json.RawMessage(payload), Actor: stepFixtureActor(), OperationID: fmt.Sprintf("removal-doc-op-%d", seq), IdempotencyIdentity: fmt.Sprintf("removal-doc-key-%d", seq), PrincipalRef: "principal-1", RequestID: fmt.Sprintf("removal-doc-request-%d", seq), AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()}
		var result WorkflowActionExecutionResult
		err := s.Transact(ctx, func(tx *Transaction) error {
			var err error
			result, err = ApplyWorkflowActionTx(ctx, tx, request)
			return err
		})
		if err != nil {
			t.Fatalf("apply %s: %v", action, err)
		}
		version = result.ResultingVersion
	}
	apply("record_intake", `{"outcome":"proceed"}`)
	apply("pick_type", fmt.Sprintf(`{"type":%q,"reason":"initial choice"}`, typ))
	apply("record_clarify", `{"resolved_questions":[],"note":"No operator questions remain."}`)
	apply("complete_step", `{"step_id":"clarify","note":"Clarification complete."}`)
	apply(action, document)
}

func TestWorkRemovalDeletesPlanAndRemovalScopeRecords(t *testing.T) {
	for _, tc := range []struct{ typ, action, table, document string }{
		{"change", "record_plan", "workflow_plan_records", `{"outcomes":"working outcome","approach":"bounded change","decisions":[{"id":"decision:one","question":"which?","choice":"one","rationale":"fits","rejected":[]}],"touches":["file:one"],"merge_split":"one item","risks":"bounded risk"}`},
		{"removal", "record_removal_scope", "workflow_removal_scopes", `{"removed":["file:one"],"retiring_clauses":[],"note":"retire this"}`},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			work := "plan-removal-" + tc.typ
			seedWork(t, s, work)
			drivePlanDocumentForTest(t, s, work, tc.typ, tc.action, tc.document)
			// A cancelled item that already recorded its plan document must
			// still be removable.
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard VALUES(1); UPDATE work_items SET lifecycle='cancelled' WHERE id=?; DELETE FROM write_guard`, work); err != nil {
				t.Fatal(err)
			}
			var recorded int
			if err := s.db.QueryRow(`SELECT count(*) FROM `+tc.table+` WHERE work_id=?`, work).Scan(&recorded); err != nil || recorded != 1 {
				t.Fatalf("%s rows=%d err=%v", tc.table, recorded, err)
			}
			req := removalTestRequest()
			req.OperationID, req.IdempotencyKey = "remove-"+tc.typ, "key-"+tc.typ
			req.WorkID = work
			req.ExpectedVersion, req.Reason = readWorkVersion(t, s, work), "cancelled"
			confirmRemovalHandoff(t, s, &req)
			if err := removeWorkForTest(t.Context(), s, req); err != nil {
				t.Fatalf("remove: %v", err)
			}
			var remaining int
			if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM work_items WHERE id=?) + (SELECT count(*) FROM `+tc.table+` WHERE work_id=?)`, work, work).Scan(&remaining); err != nil || remaining != 0 {
				t.Fatalf("remaining rows=%d err=%v", remaining, err)
			}
		})
	}
	t.Run("migrated", func(t *testing.T) {
		// Rows written by an earlier binary survive the upgrade; removal must
		// clear them in the same transaction as the work item.
		useStampedBuild(t)
		db := openMigratedTo(t, filepath.Join(t.TempDir(), "removal.db"), 188)
		ctx := t.Context()
		retirementUpgradeExec(t, db, `INSERT INTO write_guard VALUES(1)`)
		retirementUpgradeExec(t, db, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('project','Project',1,'now','now')`)
		retirementUpgradeExec(t, db, `INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('product','Product','prototype','operator_only',1,'now','now')`)
		retirementUpgradeExec(t, db, `INSERT INTO product_projects(product_id,project_id,role) VALUES('product','project','primary')`)
		retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at) VALUES('migrated-plan','task','Migrated','cancelled',1,'now','now')`)
		retirementUpgradeExec(t, db, `INSERT INTO work_projects(work_id,project_id,role) VALUES('migrated-plan','project','primary')`)
		actor := "actor:" + strings.Repeat("a", 64)
		retirementUpgradeExec(t, db, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal','client','agent','session','agent','now')`, actor)
		retirementUpgradeExec(t, db, `INSERT INTO workflow_plan_records(work_id,work_version,payload,recorded_at,actor,event_ref) VALUES('migrated-plan',1,'{"outcomes":"recorded before the fix"}','now',?,'event:migrated-plan')`, actor)
		retirementUpgradeExec(t, db, `INSERT INTO workflow_removal_scopes(work_id,work_version,payload,recorded_at,actor,event_ref) VALUES('migrated-plan',1,'{"removed":["file:one"]}','now',?,'event:migrated-plan')`, actor)
		retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
		s := &Store{db: db, Clock: time.Now}
		for _, table := range []string{"workflow_plan_records", "workflow_removal_scopes"} {
			var count int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE work_id='migrated-plan'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("%s count=%d err=%v", table, count, err)
			}
		}
		req := removalTestRequest()
		req.WorkID, req.OperationID, req.IdempotencyKey = "migrated-plan", "remove-migrated-plan", "key-migrated-plan"
		req.ExpectedVersion, req.Reason = readWorkVersion(t, s, "migrated-plan"), "cancelled"
		confirmRemovalHandoff(t, s, &req)
		if err := removeWorkForTest(ctx, s, req); err != nil {
			t.Fatalf("remove: %v", err)
		}
		var remaining int
		if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM work_items WHERE id='migrated-plan') + (SELECT count(*) FROM workflow_plan_records WHERE work_id='migrated-plan') + (SELECT count(*) FROM workflow_removal_scopes WHERE work_id='migrated-plan')`).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("remaining rows=%d err=%v", remaining, err)
		}
	})
}

func TestWorkLivenessKeepsDecisionAndProgressEvidence(t *testing.T) {
	for _, decisions := range []int64{0, 2} {
		liveness := workLivenessFromCounts("work", decisions, sql.NullString{String: "progress", Valid: true})
		if liveness.State != "unknown" || liveness.LastProgress != "progress" {
			t.Fatalf("liveness = %+v, want unknown host state and last progress", liveness)
		}
		joined := strings.Join(liveness.Evidence, " ")
		if !strings.Contains(joined, "no verified active or waiting evidence") || strings.Contains(joined, "operator decision") != (decisions > 0) {
			t.Fatalf("evidence %q does not preserve decision count %d and unknown host liveness", joined, decisions)
		}
	}
}
