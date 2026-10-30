package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

func seedOverlapProjection(t *testing.T, left, right string, relation bool) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	s, _, _, _, _, hash := architectureValidationFixture(t, left)
	seedWork(t, s, right)
	pinned, ok := BuiltinWorkflowRegistry().Current("workflow.break_fix")
	if !ok {
		t.Fatal("workflow.break_fix v9 is unavailable")
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	actor := DeriveWorkflowActorRef("principal:operator", "client:overlap", "agent:operator", "session:overlap")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,?,?)`, []any{actor, "principal:operator", "client:overlap", "agent:operator", "session:overlap", "operator", "2026-08-19T00:00:00Z"}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	for _, workID := range []string{left, right} {
		// CD-0183: a contract holds its Domains once its workflow instance
		// carries the execution-start fact, so an overlap fixture puts both
		// items there with the fact set.
		if _, err := tx.ExecContext(ctx, `UPDATE work_items SET lifecycle='in_progress',intent_json=json_set(intent_json,'$.governing_requirements',json('[]')) WHERE id=?`, workID); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_started_at) VALUES(?, 'workflow.break_fix', ?, 'repair', 'running', '2026-08-19T00:00:00Z')`, workID, pinned.Digest); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, workID, 1, "overlap test", "internal_sqlite", `[]`, `[]`, "2026-08-19T00:00:00Z", actor, `[]`, `[]`, 1, "prototype_internal"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check',?)`, workID, `{"kind":"check","check_ref":"check:overlap","immutable_subject_ref":"commit:overlap","expected_result":"pass"}`); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,?,?,?,?,?)`, workID, 1, "product", hash, "child", hash); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,?,?)`, workID, 1, "child"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES(?,?,?)`, workID, 1, "child"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if relation {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_domain_relation_modifications(work_id,contract_version,source_domain_id,kind,target_domain_id) VALUES(?,?,?,?,?)`, left, 1, "child", "depends_on", "root"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_contract_domain_relation_modifications(work_id,contract_version,source_domain_id,kind,target_domain_id) VALUES(?,?,?,?,?)`, right, 1, "child", "depends_on", "root"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, actor
}

// resolveApprovedOverlap drives the live writer's graph and terminal-effect guards.
func resolveApprovedOverlap(t *testing.T, s *Store, eventID, fromWorkID, toWorkID string, fromExpected, toExpected int64, kind, reason, actor string) error {
	t.Helper()
	return s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := ResolveWorkflowDomainOverlapTx(context.Background(), tx, WorkflowDomainOverlapResolutionRequest{
			EventID: eventID, FromWorkID: fromWorkID, ToWorkID: toWorkID,
			FromExpectedVersion: fromExpected, ToExpectedVersion: toExpected,
			FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind, Reason: reason, Actor: actor,
			OccurredAt: time.Unix(41, 0).UTC(),
		})
		return err
	})
}

func seedProductChangingContract(t *testing.T, s *Store, workID string, binding WorkflowArchitectureBinding) WorkflowActor {
	t.Helper()
	ctx := context.Background()
	registered, ok := BuiltinWorkflowRegistry().Current("workflow.break_fix")
	if !ok {
		t.Fatal("workflow.break_fix v9 is unavailable")
	}
	actor := WorkflowActor{PrincipalRef: "principal:overlap", ClientRef: "client:overlap", AgentRef: "agent:" + workID, SessionRef: "session:" + workID, ActorClass: ActorAgent}
	ctx, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	// The current graph records diagnosis and the derived execution start in
	// the log, so rebuilding derives the same active overlap claim.
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "repair", actorRef); err != nil {
		t.Fatalf("advance onto repair: %v", err)
	}
	version := readWorkVersion(t, s, workID)
	started := workflowEventWithActor("overlap-start-execution-"+workID, WorkflowActionStarted, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"step_id": "repair", "action_id": "checkpoint_repair", "attempt_epoch": int64(1),
		"accepted_inputs_digest": "sha256:" + strings.Repeat("a", 64), "idempotency_identity": "overlap-start-" + workID,
		"actor_ref": actorRef,
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{started}, ExpectedVersions: workVersion(workID, version)}); err != nil {
		t.Fatalf("record the execution start: %v", err)
	}
	version = readWorkVersion(t, s, workID)
	mandate := []string{"spec:one"}
	for _, addition := range binding.LawAdditions {
		mandate = append(mandate, addition.LawID)
	}
	contract := workflowEventWithActor("overlap-contract-"+workID, WorkflowContractApproved, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": int64(1),
		"premise": "coordinate Product-changing overlap", "outcome_kind": "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:" + workID, "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		"required_evidence": []string{"verification", "review"}, "route_conventions": []string{}, "spec_mandate": mandate, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{{LawID: "spec:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "architecture_binding": binding,
	})
	contract = pOverlapContractFacts(t, s, contract)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{contract}, ExpectedVersions: workVersion(workID, version)}); err != nil {
		t.Fatal(err)
	}
	return actor
}

// setWorkLifecycleForTesting places a work item in a lifecycle without moving
// its version. It opens its own transaction and must never run inside one,
// because the store pools a single connection.
func setWorkLifecycleForTesting(t *testing.T, s *Store, workID, lifecycle string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET lifecycle=? WHERE id=?`, lifecycle, workID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// setExecutionStartedForTesting gives a fixture work item the workflow
// instance and durable execution-start fact (CD-0183) the overlap peer query
// reads. A fixture that wants a live claim sets the fact, and a test that
// exercises the claim boundary clears it. It opens its own transaction and
// must never run inside one, because the store pools a single connection.
func setExecutionStartedForTesting(t *testing.T, s *Store, workID string, started bool) {
	t.Helper()
	ctx := context.Background()
	var value any
	if started {
		value = "2026-08-19T00:00:00Z"
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_started_at) VALUES(?, 'workflow.break_fix', ?, 'repair', 'running', ?) ON CONFLICT(work_id) DO UPDATE SET execution_started_at=excluded.execution_started_at`, workID, "sha256:"+strings.Repeat("e", 64), value); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// setWorkTerminalForTesting places a work item and its workflow instance in
// the completed terminal state without appending events. CD-0183 requires a
// live workflow to complete through the workflow completion action, so a
// fixture that needs a terminal peer seeds both projections directly. It
// opens its own transaction and must never run inside one, because the store
// pools a single connection.
func setWorkTerminalForTesting(t *testing.T, s *Store, workID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET lifecycle='completed',terminal_time='2026-08-19T00:00:00Z' WHERE id=?`, workID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_instances SET instance_state='completed',completed_at='2026-08-19T00:00:00Z' WHERE work_id=? AND instance_state NOT IN ('completed','cancelled','superseded')`, workID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// CD-0183 D2: the first workflow action moves the lifecycle, and the
// external-effect start records the execution-start fact, not arrival at repair.
func TestExecutionStartRecordsTheInstanceFact(t *testing.T) {
	const workID = "claim-at-execution-start"
	f := seedCurrentWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := f.store
	if startedAt, _ := instanceExecutionStart(t, s, workID); startedAt != "" {
		t.Fatal("arrival at repair recorded execution before its qualifying start")
	}
	reviewGateStartStep(t, s, workID, "repair", "checkpoint_repair", f.owner)
	var lifecycle string
	var startedAt any
	if err := s.DatabaseForTesting().QueryRow(`SELECT w.lifecycle, i.execution_started_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, workID).Scan(&lifecycle, &startedAt); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "in_progress" {
		t.Fatalf("workflow action left lifecycle=%q, want in_progress", lifecycle)
	}
	if startedAt == nil {
		t.Fatal("the external-effect step left execution_started_at unset")
	}
}

// resolve_overlap records the routine coordination relations without consent
// and refuses the retired compatible_with kind.
func TestResolveOverlapRecordsRoutineRelations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	resolve := func(s *Store, actor, eventID, kind, fromWorkID, toWorkID string) error {
		return s.Transact(ctx, func(tx *Transaction) error {
			_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
				EventID: eventID, FromWorkID: fromWorkID, ToWorkID: toWorkID,
				FromExpectedVersion: 2, ToExpectedVersion: 2,
				FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind,
				Reason: "agent sequencing", Actor: actor, OccurredAt: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC),
			})
			return err
		})
	}
	for _, kind := range []string{ResolutionDependsOn, ResolutionBlocks} {
		s, actor := seedOverlapProjection(t, "routine-"+kind+"-left", "routine-"+kind+"-right", false)
		if err := resolve(s, actor, "routine-"+kind, kind, "routine-"+kind+"-left", "routine-"+kind+"-right"); err != nil {
			t.Fatalf("routine %s resolution: %v", kind, err)
		}
		var relations int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE work_id_from=? AND work_id_to=? AND kind=?`, "routine-"+kind+"-left", "routine-"+kind+"-right", kind).Scan(&relations); err != nil || relations != 1 {
			t.Fatalf("%s relation count=%d err=%v, want 1", kind, relations, err)
		}
		for _, workID := range []string{"routine-" + kind + "-left", "routine-" + kind + "-right"} {
			if got := readWorkVersion(t, s, workID); got != 3 {
				t.Fatalf("%s endpoint %s version=%d, want 3", kind, workID, got)
			}
		}
	}
	s, actor := seedOverlapProjection(t, "retired-compatible-left", "retired-compatible-right", false)
	err := resolve(s, actor, "retired-compatible", "compatible_with", "retired-compatible-left", "retired-compatible-right")
	assertFailureKind(t, err, KindInvalidPayload)
}

// merged_into and supersedes terminalize one endpoint atomically;
// reopening it retires the resolution and its relation.
func TestResolveOverlapMergeAndSupersessionAreAtomic(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name, kind, terminalID string
	}{
		{name: "merged into", kind: ResolutionMergedInto, terminalID: "terminal-source"},
		{name: "supersedes", kind: ResolutionSupersedes, terminalID: "terminal-target"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s, actor := seedOverlapProjection(t, "terminal-source", "terminal-target", false)
			if err := resolveApprovedOverlap(t, s, "terminal-"+testCase.kind, "terminal-source", "terminal-target", 2, 2, testCase.kind, "operator terminal resolution", actor); err != nil {
				t.Fatalf("terminal resolution: %v", err)
			}
			var lifecycle string
			if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id=?`, testCase.terminalID).Scan(&lifecycle); err != nil {
				t.Fatal(err)
			}
			if lifecycle != "superseded" {
				t.Fatalf("terminal composite lifecycle=%q", lifecycle)
			}
			if err := applyWorkEvent(t, s, workReopenedFromSupersededEvent("terminal-reopen-"+testCase.kind, testCase.terminalID, "", 3, 4), nil); err != nil {
				t.Fatalf("reopen terminal resolution: %v", err)
			}
			var current, relations int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_overlap_resolutions WHERE invalidated_seq IS NULL`).Scan(&current); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE kind=?`, testCase.kind).Scan(&relations); err != nil {
				t.Fatal(err)
			}
			if current != 0 || relations != 0 {
				t.Fatalf("reopened work kept resolution=%d relation=%d, want 0/0", current, relations)
			}
		})
	}
}

// Ordinary relation writes cannot create resolution-owned relations.
func TestOrdinaryRelationsCannotWriteResolutionRelations(t *testing.T) {
	t.Parallel()
	s, _ := seedOverlapProjection(t, "ordinary-left", "ordinary-right", false)
	for _, kind := range []string{"compatible_with", "merged_into"} {
		err := applyWorkEvent(t, s, relationAddedEvent("ordinary-"+kind, kind, "ordinary-left", "ordinary-right", 2, 3), workVersion("ordinary-right", 2))
		assertFailureKind(t, err, KindRelationContractViolation)
	}
}

func overlapFixtureExec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := enterWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
