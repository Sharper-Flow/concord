package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// d7BoundaryDomain, d7BoundaryLaw, and d7BoundaryProduct are the Domain, law,
// and Product two concurrent contracts share in the peer fixture.
const (
	d7BoundaryDomain  = "root"
	d7BoundaryLaw     = "spec:one"
	d7BoundaryProduct = "product"
)

// seedD7BoundaryOverlap positions one Product-changing workflow instance at the
// named definition step and gives a second active work item an identical
// architecture footprint: both modify the same Domain and the same law, and no
// resolution relates them.
func seedD7BoundaryOverlap(t *testing.T, workID, otherID, step string) (*Store, WorkflowActor, int64) {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWork(t, s, otherID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	setup := []Event{
		workflowEvent("d7-actor-"+workID, WorkflowActorRecorded, workID, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef, "agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": "agent"}),
		workflowEvent("d7-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowWorkerFixtureRef, "version": 2, "digest": workflowFixtureDefinition(t, 2).Digest, "work_kind": workflowFixtureWorkKind}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	if workID == "d7-accept_worker_result" {
		// Acceptance now joins the requested terminal result before the
		// consequential-boundary check. Give that boundary an actual result
		// to consume, before introducing the overlapping contracts.
		attempt := "attempt:" + workID
		recordWorkerFixtureDispatchAuthorization(t, s, workID, attempt, "execution", actorRef)
		lane := reviewGateLane(t, "implementation")
		dispatch := workerDispatchEvent(workID, attempt+":dispatched", lane, map[string]any{"attempt_id": attempt, "packet_digest": "sha256:" + strings.Repeat("b", 64)})
		completed := workerCompleteEventForLane(workID, attempt+":completed", attempt, lane, time.Unix(3, 0).UTC(), workerFixtureDirectory(t, s, workID))
		completed = workerCompletionAtClaim(t, s, completed)
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{dispatch, completed}}); err != nil {
			t.Fatal(err)
		}
	}
	hash := "sha256:" + strings.Repeat("d", 64)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterWrite(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,'project','workflow-law-locator','product',?,'1.0',?,'test')`, []any{d7BoundaryProduct, d7BoundaryDomain, hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator',?,?,'Root','Product law','current',?,'test')`, []any{d7BoundaryProduct, d7BoundaryDomain, hash}},
		{`UPDATE workflow_instances SET current_step=?,execution_started_at='2026-08-19T00:00:00Z' WHERE work_id=?`, []any{step, workID}},
	}
	for _, id := range []string{workID, otherID} {
		if id == otherID {
			// CD-0183: the peer claims its Domains through the instance's
			// execution-start fact, so the peer fixture carries a started
			// instance on the same step.
			statements = append(statements,
				struct {
					query string
					args  []any
				}{`INSERT INTO workflow_instances(work_id,definition_ref,definition_digest,current_step,instance_state,execution_started_at) VALUES(?, 'workflow.break_fix', ?, ?, 'running', '2026-08-19T00:00:00Z')`, []any{otherID, "sha256:" + strings.Repeat("e", 64), step}},
			)
		}
		statements = append(statements,
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'D7 boundary','internal_sqlite','[]','[]','2026-08-19T00:00:00Z',?,'[]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:d7","immutable_subject_ref":"commit:d7","expected_result":"pass"}')`, []any{id, actorRef, id}},
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) VALUES(?,1,?,?,?,?)`, []any{id, d7BoundaryProduct, hash, d7BoundaryDomain, hash}},
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,?)`, []any{id, d7BoundaryDomain}},
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES(?,1,?)`, []any{id, d7BoundaryLaw}},
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_contract_domain_modifications(work_id,contract_version,domain_id) VALUES(?,1,?)`, []any{id, d7BoundaryDomain}},
		)
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
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
	setWorkLifecycleForTesting(t, s, workID, "in_progress")
	setWorkLifecycleForTesting(t, s, otherID, "in_progress")
	if got := currentStep(t, s, workID); got != step {
		t.Fatalf("fixture step=%q, want %q", got, step)
	}
	assertD7ConflictEstablished(t, s, workID, otherID)
	return s, actor, readWorkVersion(t, s, workID)
}

// assertD7ConflictEstablished proves the fixture actually created the
// conflicting condition: two active Product-changing contracts share one
// current Domain and one law, and no overlap resolution exists between them.
func assertD7ConflictEstablished(t *testing.T, s *Store, workID, otherID string) {
	t.Helper()
	var activeContracts, sharedDomains, sharedLaws, resolutions int
	db := s.DatabaseForTesting()
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contracts c JOIN workflow_architecture_bindings b ON b.work_id=c.work_id AND b.contract_version=c.contract_version JOIN work_items w ON w.id=c.work_id WHERE c.superseded_by IS NULL AND w.lifecycle NOT IN ('completed','cancelled','superseded') AND c.work_id IN (?,?)`, workID, otherID).Scan(&activeContracts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contract_affected_domains a JOIN workflow_contract_affected_domains b ON a.domain_id=b.domain_id WHERE a.work_id=? AND b.work_id=?`, workID, otherID).Scan(&sharedDomains); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM workflow_contract_law_modifications a JOIN workflow_contract_law_modifications b ON a.law_id=b.law_id WHERE a.work_id=? AND b.work_id=?`, workID, otherID).Scan(&sharedLaws); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM workflow_overlap_resolutions WHERE invalidated_seq IS NULL AND ((from_work_id=? AND to_work_id=?) OR (from_work_id=? AND to_work_id=?))`, workID, otherID, otherID, workID).Scan(&resolutions); err != nil {
		t.Fatal(err)
	}
	if activeContracts != 2 || sharedDomains != 1 || sharedLaws != 1 || resolutions != 0 {
		t.Fatalf("conflict not established: contracts=%d domains=%d laws=%d resolutions=%d", activeContracts, sharedDomains, sharedLaws, resolutions)
	}
}

// attemptD7BoundaryAction drives one consequential action through the same
// owning-transaction coordinator the agent mutation surface uses.
func attemptD7BoundaryAction(ctx context.Context, s *Store, workID, actionID string, actor WorkflowActor, version int64) error {
	payload := mustJSONValue(map[string]any{})
	if actionID == "accept_worker_result" {
		payload = mustJSONValue(map[string]any{"attempt_id": "attempt:" + workID, "attempt_epoch": 1})
	}
	preflight := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, Actor: actor}
	execution := WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "d7-" + actionID + "-" + workID,
		OperationID: "d7-" + actionID + "-" + workID, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: "d7-" + actionID + "-" + workID, RequestID: "request:d7-" + actionID + "-" + workID,
		ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC),
	}
	return AuthorizeWorkflowActionAtBoundaryTx(ctx, s, BuiltinWorkflowRegistry(), preflight, nil, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), execution)
		return err
	})
}

// supersedeRecoveryPayload is the typed successor contract the recovery
// payload checks require, shaped for the non-Product-changing fixture family.
// A Product-changing pinned definition requires the successor architecture
// binding, and a duplicated active-contract projection must name the exact
// active versions the supersession retires; the successor version follows the
// highest one.
func supersedeRecoveryPayload(t *testing.T, workID string, successor int64, binding map[string]any, predecessors ...int64) json.RawMessage {
	t.Helper()
	fields := map[string]any{
		"contract_version": successor,
		"premise":          "The successor contract carries the work past the refused state.",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:" + workID, "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"},
		"route_conventions": []string{},
		"spec_mandate":      []string{},
		"law_modifies":      []string{},
		"rigor_class":       "prototype_internal",
		"supersede_reason":  "the operator recovered the refused state",
		"audit_evidence":    []string{"evidence:" + workID},
	}
	if len(predecessors) != 0 {
		fields["predecessor_contract_versions"] = predecessors
	}
	if binding != nil {
		fields["architecture_binding"] = binding
	}
	return mustJSONValue(fields)
}

// runSupersedeRecoveryFold drives one supersede_contract request through the
// dispatch fold the owning boundary runs. The recovery's operator approval is
// the mutation boundary's wall, not the fold's, so the request carries the
// work actor alone.
func runSupersedeRecoveryFold(t *testing.T, s *Store, workID string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operationID := "supersede-recovery-" + workID + "-" + strconv.FormatInt(version, 10)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "supersede_contract", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: testManifestDigest, Now: time.Unix(20, version).UTC(),
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

// Two executing items that modify the same Domain and the same law do not block
// each other at any consequential boundary (CD-0222 D6): the later one merges
// the earlier one's changes, and the boundary check admits both.
func TestConsequentialBoundariesAdmitASharedDomainPeer(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"planning", "execution", "acceptance", "release"} {
		t.Run(step, func(t *testing.T) {
			ctx := context.Background()
			workID, otherID := "shared-peer-"+step, "shared-peer-other-"+step
			s, _, _ := seedD7BoundaryOverlap(t, workID, otherID, step)
			for _, id := range []string{workID, otherID} {
				if err := s.Transact(ctx, func(tx *Transaction) error {
					return CheckWorkflowConsequentialBoundaryTx(ctx, tx, id)
				}); err != nil {
					t.Fatalf("boundary for %s beside a shared-Domain peer = %v, want admitted", id, err)
				}
			}
		})
	}
}

// The execution claim, owned by the durable fence transaction, admits a step
// claim beside a shared-Domain peer (CD-0222 D6).
func TestExecutionClaimAdmitsASharedDomainPeer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "shared-peer-claim"
	const otherID = "shared-peer-claim-other"
	s, _, _ := seedD7BoundaryOverlap(t, workID, otherID, "execution")
	request := ClaimRequest{
		OpID: "op:shared-peer-claim", WorkID: workID, WorkflowTypeRef: workflowFixtureRef, WorkflowTypeDigest: mustBuiltinDefinition(t, workflowFixtureRef).Digest,
		StepID: "execution", StepKind: StepExternalEffect, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64),
		AcceptedScopeSnapshot: `{"work_id":"` + workID + `"}`, PrincipalRef: "principal/operator", Tool: "concord_work_transition",
		IdempotencyKey: "shared-peer-claim", RequestID: "request:shared-peer-claim", ObservedAt: time.Unix(1, 0).UTC(), ContractDigest: testManifestDigest,
	}
	if _, err := ClaimStepAuthorized(ctx, s, request, nil); err != nil {
		t.Fatalf("execution claim beside a shared-Domain peer = %v, want admitted", err)
	}
}
