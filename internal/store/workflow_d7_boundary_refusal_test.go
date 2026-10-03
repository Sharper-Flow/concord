package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// d7BoundaryDomain, d7BoundaryLaw, and d7BoundaryProduct are the exact names a
// D7 refusal must report back to the caller.
const (
	d7BoundaryDomain  = "root"
	d7BoundaryLaw     = "spec:one"
	d7BoundaryProduct = "product"
)

// seedD7BoundaryOverlap positions one Product-changing workflow instance at the
// named definition step and gives a second active work item an identical
// architecture footprint, so every consequential boundary on the first item has
// an unresolved Domain overlap against the second.
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
		workflowEvent("d7-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": workflowFixtureRef, "version": 2, "digest": workflowFixtureDefinition(t, 2).Digest, "work_kind": workflowFixtureWorkKind}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	hash := "sha256:" + strings.Repeat("d", 64)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
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
				}{`INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_started_at) VALUES(?, 'workflow.break_fix', 9, ?, ?, 'running', '2026-08-19T00:00:00Z')`, []any{otherID, "sha256:" + strings.Repeat("e", 64), step}},
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
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// CD-0144: a contract holds its Domains from execution start. Every D7
	// boundary here sits at or after execution, so both items hold theirs and
	// the refusal stays mutual.
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

// assertD7TypedRefusal asserts the full D7 refusal shape: the exact Domains,
// laws, work items, contract versions, and closed recovery choices.
func assertD7TypedRefusal(t *testing.T, err error, workID, otherID string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("boundary error=%v, want a typed store failure", err)
	}
	if failure.Kind != KindDomainOverlap {
		t.Fatalf("boundary failure kind=%s, want %s (err=%v)", failure.Kind, KindDomainOverlap, err)
	}
	if failure.DomainOverlap == nil || len(failure.DomainOverlap.Overlaps) != 1 {
		t.Fatalf("boundary refusal carries no single typed overlap: %#v", failure.DomainOverlap)
	}
	overlap := failure.DomainOverlap.Overlaps[0]
	from, to := workID, otherID
	if to < from {
		from, to = to, from
	}
	if overlap.ProductID != d7BoundaryProduct || overlap.FromWorkID != from || overlap.ToWorkID != to {
		t.Fatalf("refusal work items product=%q from=%q to=%q, want %q %q %q", overlap.ProductID, overlap.FromWorkID, overlap.ToWorkID, d7BoundaryProduct, from, to)
	}
	if overlap.FromContractVersion != 1 || overlap.ToContractVersion != 1 {
		t.Fatalf("refusal contract versions from=%d to=%d, want 1 and 1", overlap.FromContractVersion, overlap.ToContractVersion)
	}
	if len(overlap.SharedAffectedDomainIDs) != 1 || overlap.SharedAffectedDomainIDs[0] != d7BoundaryDomain {
		t.Fatalf("refusal Domains=%v, want [%s]", overlap.SharedAffectedDomainIDs, d7BoundaryDomain)
	}
	if len(overlap.SharedLawIDs) != 1 || overlap.SharedLawIDs[0] != d7BoundaryLaw {
		t.Fatalf("refusal laws=%v, want [%s]", overlap.SharedLawIDs, d7BoundaryLaw)
	}
	if len(overlap.SharedDomainModifications) != 1 || overlap.SharedDomainModifications[0] != d7BoundaryDomain {
		t.Fatalf("refusal Domain writes=%v, want [%s]", overlap.SharedDomainModifications, d7BoundaryDomain)
	}
	if overlap.ResolutionState != "unresolved" {
		t.Fatalf("refusal resolution state=%q, want unresolved", overlap.ResolutionState)
	}
	wantRecovery := []string{"wait", "resolve_overlap", "terminal_work", "supersede_contract"}
	if strings.Join(overlap.RecoveryActions, ",") != strings.Join(wantRecovery, ",") {
		t.Fatalf("refusal recovery choices=%v, want %v", overlap.RecoveryActions, wantRecovery)
	}
	if strings.Join(overlap.OverlapClasses, ",") != "architecture,law_write,domain_write" {
		t.Fatalf("refusal overlap classes=%v", overlap.OverlapClasses)
	}
}

// attemptD7BoundaryAction drives one consequential action through the same
// owning-transaction coordinator the agent mutation surface uses.
func attemptD7BoundaryAction(ctx context.Context, s *Store, workID, actionID string, actor WorkflowActor, version int64) error {
	payload := mustJSONValue(map[string]any{})
	preflight := WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, Actor: actor}
	execution := WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("e", 64), IdempotencyIdentity: "d7-" + actionID + "-" + workID,
		OperationID: "d7-" + actionID + "-" + workID, PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: "d7-" + actionID + "-" + workID, RequestID: "request:d7-" + actionID + "-" + workID,
		ContractDigest: testManifestDigest, Now: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC),
	}
	return AuthorizeWorkflowActionAtBoundaryTx(ctx, s, BuiltinWorkflowRegistry(), preflight, nil, time.Time{}, nil, func(tx *Transaction) error {
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
	version := verdictItemVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operationID := "supersede-recovery-" + workID + "-" + strconv.FormatInt(version, 10)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
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

// runSupersedeDuplicateRecoveryFold drives the duplicate-contract recovery
// through the fold with the operator approval binding the recovery's
// operator-approved route records: the operator identity names the consumed
// approval, and the binding fields are the ones admission verified.
func runSupersedeDuplicateRecoveryFold(t *testing.T, s *Store, workID string, payload json.RawMessage, actor WorkflowActor) error {
	t.Helper()
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "approval:approval-duplicate-recovery", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	version := verdictItemVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	operationID := "supersede-duplicate-" + workID + "-" + strconv.FormatInt(version, 10)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "supersede_contract", Payload: payload,
		Actor: actor, OperatorActor: &operator, OperatorApprovalRef: "approval-duplicate-recovery",
		ApprovalOperationDigest: "sha256:" + strings.Repeat("b", 64),
		ApprovalScopeJSON:       `{"work_ids":["` + workID + `"]}`,
		ApprovalVersionsJSON:    `{"work":` + strconv.FormatInt(version, 10) + `}`,
		ApprovalConsequence:     "contract_recovery",
		AcceptedInputsDigest:    "sha256:" + strings.Repeat("f", 64), IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID, RequestID: "request:" + operationID,
		ContractDigest: testManifestDigest, Now: time.Unix(20, version).UTC(),
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

// TestWorkflowSupersedeOverlapRecoveryAgreesAcrossPinDiscoveryPreflightAndFold
// holds the single admission owner at the surfaces that answer
// supersede_contract: an unresolved Domain overlap at a step whose correction
// checkpoint refuses admits the recovery, and discovery, the work pin, the
// read-only preflight, and the dispatch fold answer identically for that
// state. Discovery once re-classified the overlap through the correction
// checkpoint and refused a recovery the fold admitted.
func TestWorkflowSupersedeOverlapRecoveryAgreesAcrossPinDiscoveryPreflightAndFold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workID := "d7-supersede-agreement"
	otherID := "d7-supersede-agreement-other"
	s, actor, _ := seedD7BoundaryOverlap(t, workID, otherID, "planning")

	// The contrast first, on the seeded state: the recovery admission widened
	// no other route, so the checkpoint advance still refuses with the typed
	// overlap refusal (CD-0186).
	if err := attemptD7BoundaryAction(ctx, s, workID, "approve_contract", actor, readWorkVersion(t, s, workID)); err == nil {
		t.Fatal("approve_contract passed while the overlap stands unresolved")
	} else {
		assertD7TypedRefusal(t, err, workID, otherID)
	}

	payload := supersedeRecoveryPayload(t, workID, 2, nil)
	_, action, err := WorkflowActionDefinitionFor(ctx, s, BuiltinWorkflowRegistry(), workID, "supersede_contract")
	if err != nil {
		t.Fatalf("discovery refused the overlap recovery the fold admits: %v", err)
	}
	if action.ID != "supersede_contract" || action.Approval != ActionApprovalRequired {
		t.Fatalf("recovery action = %+v, want the approval-required contract recovery", action)
	}
	pin, err := ReadWorkPin(ctx, s, workID)
	if err != nil {
		t.Fatalf("read work pin under the overlap: %v", err)
	}
	if !workPinContainsAction(pin.NextValidIntents, "supersede_contract") {
		t.Fatalf("pin omits supersede_contract under the overlap; intents = %v", intentActionIDs(pin.NextValidIntents))
	}
	if err := WorkflowActionPreflightWithRegistry(ctx, s, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
		WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "supersede_contract", Payload: payload, Actor: actor,
	}); err != nil {
		t.Fatalf("preflight refused the overlap recovery: %v", err)
	}
	if err := runSupersedeRecoveryFold(t, s, workID, payload, actor); err != nil {
		t.Fatalf("dispatch fold refused the overlap recovery: %v", err)
	}
	var superseded, active int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND contract_version=1 AND superseded_by IS NOT NULL`, workID).Scan(&superseded); err != nil || superseded != 1 {
		t.Fatalf("predecessor contract superseded=%d err=%v, want one", superseded, err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active contracts after recovery=%d err=%v, want one", active, err)
	}
}

// TestD7ConsequentialBoundariesRefuseUnresolvedOverlap covers the CD-0041 D7
// boundaries that are enacted as workflow actions: contract approval, execution
// dispatch, checkpoint and evidence binding, worker-result acceptance, verdict
// and premise confirmation, merge/ship successor linkage, and completion.
func TestD7ConsequentialBoundariesRefuseUnresolvedOverlap(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		boundary string
		step     string
		actionID string
	}{
		{boundary: "contract approval", step: "planning", actionID: "approve_contract"},
		{boundary: "execution dispatch", step: "execution", actionID: "start_execution"},
		{boundary: "checkpoint", step: "execution", actionID: "checkpoint_execution"},
		{boundary: "evidence binding", step: "execution", actionID: "bind_evidence"},
		{boundary: "worker result acceptance", step: "execution", actionID: "accept_worker_result"},
		{boundary: "merge ship successor", step: "execution", actionID: "link_successor"},
		{boundary: "verdict", step: "acceptance", actionID: "record_verdict"},
		{boundary: "premise confirmation", step: "acceptance", actionID: "confirm_premise"},
		{boundary: "completion", step: "release", actionID: "complete"},
	} {
		t.Run(testCase.boundary, func(t *testing.T) {
			ctx := context.Background()
			workID := "d7-" + testCase.actionID
			otherID := "d7-other-" + testCase.actionID
			s, actor, version := seedD7BoundaryOverlap(t, workID, otherID, testCase.step)
			var eventsBefore int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
				t.Fatal(err)
			}

			err := attemptD7BoundaryAction(ctx, s, workID, testCase.actionID, actor, version)
			assertD7TypedRefusal(t, err, workID, otherID)

			if got := readWorkVersion(t, s, workID); got != version {
				t.Fatalf("refused boundary changed work version=%d, want %d", got, version)
			}
			if got := currentStep(t, s, workID); got != testCase.step {
				t.Fatalf("refused boundary advanced step=%q, want %q", got, testCase.step)
			}
			assertTableCount(t, s, "domain_events", eventsBefore)

			// D7: read-only inspection remains available while the boundary refuses.
			if _, err := ReadWorkflow(ctx, s, workID); err != nil {
				t.Fatalf("read-only workflow inspection failed while the boundary refused: %v", err)
			}
			if err := InspectWorkflowDomainOverlap(ctx, s, otherID); err == nil {
				t.Fatal("the concurrent item reports no overlap, so the refusal was not mutual")
			}

			// Control: once the operator resolves the overlap, the same call no
			// longer refuses for this reason. Without it the assertions above
			// would also pass against a fixture that refused for any other cause.
			actorRef, err := WorkflowActorRef(actor)
			if err != nil {
				t.Fatal(err)
			}
			otherVersion := readWorkVersion(t, s, otherID)
			if err := s.Transact(ctx, func(tx *Transaction) error {
				_, resolveErr := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{
					EventID: "d7-resolve-" + testCase.actionID, FromWorkID: workID, ToWorkID: otherID,
					FromExpectedVersion: version, ToExpectedVersion: otherVersion, FromContractVersion: 1, ToContractVersion: 1,
					ResolutionKind: ResolutionCompatibleWith, Reason: "operator approved concurrent change",
					ApprovalRef: "approval:d7-" + testCase.actionID, Actor: actorRef,
					OccurredAt: time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC),
				})
				return resolveErr
			}); err != nil {
				t.Fatalf("overlap resolution: %v", err)
			}
			resolvedErr := attemptD7BoundaryAction(ctx, s, workID, testCase.actionID, actor, readWorkVersion(t, s, workID))
			var resolvedFailure *Failure
			if errors.As(resolvedErr, &resolvedFailure) && resolvedFailure.Kind == KindDomainOverlap {
				t.Fatalf("resolved overlap still refused as domain_overlap: %v", resolvedErr)
			}
		})
	}
}

// TestD7ExecutionClaimBoundaryRefusesUnresolvedOverlap covers the D7 execution
// claim boundary, which is owned by the durable fence transaction rather than
// by the workflow action coordinator.
func TestD7ExecutionClaimBoundaryRefusesUnresolvedOverlap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const workID = "d7-claim"
	const otherID = "d7-claim-other"
	s, _, _ := seedD7BoundaryOverlap(t, workID, otherID, "execution")
	var operationsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations`).Scan(&operationsBefore); err != nil {
		t.Fatal(err)
	}
	request := ClaimRequest{
		OpID: "op:d7-claim", WorkID: workID, WorkflowTypeRef: workflowFixtureRef, WorkflowTypeVersion: 2,
		StepID: "execution", StepKind: StepExternalEffect, AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64),
		AcceptedScopeSnapshot: `{"work_id":"` + workID + `"}`, PrincipalRef: "principal/operator", Tool: "concord_work_transition",
		IdempotencyKey: "d7-claim", RequestID: "request:d7-claim", ObservedAt: time.Unix(1, 0).UTC(), ContractDigest: testManifestDigest,
	}
	_, err := ClaimStep(ctx, s, request)
	assertD7TypedRefusal(t, err, workID, otherID)
	assertTableCount(t, s, "durable_operations", operationsBefore)
	if _, err := ReadWorkflow(ctx, s, workID); err != nil {
		t.Fatalf("read-only workflow inspection failed while the claim boundary refused: %v", err)
	}
}
