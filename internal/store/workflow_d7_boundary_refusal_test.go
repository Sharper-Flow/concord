package store

import (
	"context"
	"errors"
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
		workflowEvent("d7-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": "workflow.implementation", "version": 2, "digest": workflowImplementationV2Digest(t), "work_kind": "implementation"}),
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
		{`UPDATE workflow_instances SET current_step=? WHERE work_id=?`, []any{step, workID}},
	}
	for _, id := range []string{workID, otherID} {
		statements = append(statements,
			struct {
				query string
				args  []any
			}{`INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'D7 boundary','check','{"kind":"check","check_ref":"check:d7","immutable_subject_ref":"commit:d7","expected_result":"pass"}','internal_sqlite','[]','[]','2026-08-19T00:00:00Z',?,'[]','[]',1,'prototype_internal')`, []any{id, actorRef}},
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

// TestD7ConsequentialBoundariesRefuseUnresolvedOverlap covers the CD-0041 D7
// boundaries that are enacted as workflow actions: contract approval, execution
// dispatch, checkpoint and evidence binding, worker-result acceptance, verdict
// and premise confirmation, merge/ship successor linkage, and completion.
func TestD7ConsequentialBoundariesRefuseUnresolvedOverlap(t *testing.T) {
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
			if err := CheckWorkflowDomainOverlap(ctx, s, otherID); err == nil {
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
	ctx := context.Background()
	const workID = "d7-claim"
	const otherID = "d7-claim-other"
	s, _, _ := seedD7BoundaryOverlap(t, workID, otherID, "execution")
	var operationsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations`).Scan(&operationsBefore); err != nil {
		t.Fatal(err)
	}
	request := ClaimRequest{
		OpID: "op:d7-claim", WorkID: workID, WorkflowTypeRef: "workflow.implementation", WorkflowTypeVersion: 2,
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
