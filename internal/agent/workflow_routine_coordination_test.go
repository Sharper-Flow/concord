package agent

// The destructive overlap classes (merged_into, supersedes) settle only
// through the consumed one-use operator approval the real host mints. Every
// assertion here runs through Dispatch: the challenge the Service mints, the
// host assertion over the exact request, the consumption, and the effect.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest/neighbor"
)

// TestSQLiteNeighborProcess is the re-exec entry the neighbor fixture's Start
// targets in this package's own test binary. The fixture returns immediately
// outside a neighbor role, so a direct run of this package is unaffected.
func TestSQLiteNeighborProcess(t *testing.T) { neighbor.Child(t, store.SQLiteFileURI) }

// routineOverlapBinding is one destructive resolution request's complete
// identity: the covered from-to direction, both endpoint versions, and both
// contract versions. The challenge, the host assertion, and the consumed
// approval all bind this exact shape through the canonical mutation digest.
type routineOverlapBinding struct {
	kind, fromWorkID, toWorkID             string
	fromVersion, toVersion                 int64
	fromContractVersion, toContractVersion int64
}

// routineCoordinationFixture builds the unresolved-overlap pair the
// destructive classes settle. Peer enumeration counts only items whose
// workflow instance carries the durable execution start, while a subject is
// read from its prospective contract footprint, so the legal unresolved pair
// is one execution-claimed work plus one contender that holds its
// Product-changing contract and waits before its external-effect step.
// Entering that step while the overlap stands refuses by name.
func routineCoordinationFixture(t *testing.T) (*store.Store, *Service, ed25519.PrivateKey, CallEnvelope) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_relate"})
	seedRoutineCoordinationDomainFixture(t, s, ctx)
	events := []store.Event{
		{EventID: "routine-coordination-work-2", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"bug","title":"Routine coordination peer","priority":1}`)},
		{EventID: "routine-coordination-work-2-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-2"): 0}}); err != nil {
		t.Fatal(err)
	}
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	for _, workID := range []string{"work-1", "work-2"} {
		if err := s.Transact(ctx, func(tx *store.Transaction) error {
			return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: fixedTime()})
		}); err != nil {
			t.Fatalf("initialize %s: %v", workID, err)
		}
	}
	routineCoordinationDriveWorkflow(t, s, "work-1", actor, true)
	routineCoordinationDriveWorkflow(t, s, "work-2", actor, false)
	// The contender's prospective footprint against the execution-claimed
	// peer is the derived unresolved overlap, and both lifecycles come from
	// the real action folds, never a projection write.
	err = store.InspectWorkflowDomainOverlap(ctx, s, "work-2")
	failure := &store.Failure{}
	if !errors.As(err, &failure) || failure.Kind != store.KindDomainOverlap {
		t.Fatalf("fixture pair derives no unresolved overlap for the contender work-2: %v", err)
	}
	for _, workID := range []string{"work-1", "work-2"} {
		if got := workLifecycle(t, s, workID); got != "in_progress" {
			t.Fatalf("fixture %s lifecycle = %q, want the action-fold in_progress", workID, got)
		}
	}
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	return s, service, privateKey, env
}

// seedRoutineCoordinationDomainFixture seeds the fixture Product's current
// Domain law. The canonical-path locator the home pair references is applied
// as a project.locator_added event at the Project's current version, so the
// rebuild refolds the locator the restored registry and Domain rows point at.
// The registry, knowledge home, and root Domain rows are direct-authority
// state the rebuild snapshots and restores; their identity values match the
// shared current-Domain fixture the contract binding validation reads.
func seedRoutineCoordinationDomainFixture(t *testing.T, s *store.Store, ctx context.Context) {
	t.Helper()
	homeDir := t.TempDir()
	var projectVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM projects WHERE id=?`, "project-1").Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(ctx, "project-1", store.ProjectLocator{ID: "domain-locator-product-1", Kind: store.LocatorCanonicalPath, Value: homeDir}, projectVersion); err != nil {
		t.Fatalf("add the event-owned fixture locator: %v", err)
	}
	registry := fmt.Sprintf(`{"schema_version":"1.0","product_key":"fixture-%s","root_domain_id":%q,"domains":[]}`, "product-1", pm1fixture.FixtureRootDomainID)
	docsKnowledge := filepath.Join(homeDir, filepath.FromSlash(".concord/docs/knowledge"))
	if err := os.MkdirAll(docsKnowledge, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsKnowledge, "domain-registry.json"), []byte(registry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docsKnowledge, "manifest.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		name  string
		query string
		args  []any
	}{
		{"fold guard", `INSERT INTO fold_guard(active) VALUES(1)`, nil},
		{"knowledge home", `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-1','project-1','domain-locator-product-1')`, nil},
		{"registry", `INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product-1','project-1','domain-locator-product-1','fixture-product-1',?,'1.0',?,'fixture')`, []any{pm1fixture.FixtureRootDomainID, pm1fixture.FixtureDomainRegistryContentHash}},
		{"root domain", `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES('project-1','domain-locator-product-1','product-1',?,'Fixture root','Product law fixture',NULL,'current',?,'fixture')`, []any{pm1fixture.FixtureRootDomainID, pm1fixture.FixtureDomainRegistryContentHash}},
		{"leave fold", `DELETE FROM fold_guard`, nil},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatalf("seed %s: %v", statement.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// routineCoordinationDriveWorkflow advances one fixture work through the real
// engine: the typed diagnosis record, the Product-changing contract whose
// binding writes the shared root Domain, and — when the work is the
// execution-claimed side — the repair checkpoint whose external-effect start
// is the durable execution fact the overlap peer query reads. Every action
// claims a distinct durable identity and runs through the public boundary and
// action owners, so the whole prefix replays from the log.
func routineCoordinationDriveWorkflow(t *testing.T, s *store.Store, workID string, actor store.WorkflowActor, enterExecution bool) {
	t.Helper()
	ctx := context.Background()
	sequence := 0
	act := func(actionID string, payload map[string]any) {
		t.Helper()
		sequence++
		var version int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		identity := fmt.Sprintf("routine-coordination-%s-%s-%d", workID, actionID, sequence)
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request := store.WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: raw,
			Actor: actor, AcceptedInputsDigest: "sha256:" + strings.Repeat("2", 64), IdempotencyIdentity: identity,
			OperationID: "routine-coordination-op-" + identity, PrincipalRef: actor.PrincipalRef, Tool: "concord-test", IdempotencyKey: "routine-coordination-key-" + identity,
			RequestID: "routine-coordination-request-" + identity, AcceptedScope: `{"project":"project-1"}`, ContractDigest: ManifestDigest, Now: fixedTime(),
		}
		preflight := store.WorkflowActionPreflightRequest{WorkID: workID, ExpectedVersion: version, ActionID: actionID, Payload: raw, Actor: actor}
		if err := store.AuthorizeWorkflowActionAtBoundaryTx(ctx, s, store.BuiltinWorkflowRegistry(), preflight, nil, func(tx *store.Transaction) error {
			_, err := store.ApplyWorkflowActionTx(ctx, tx, store.BuiltinWorkflowRegistry(), request)
			return err
		}); err != nil {
			t.Fatalf("%s %s at v%d: %v", workID, actionID, version, err)
		}
	}
	act("record_diagnosis", map[string]any{
		"reproduction": "The bounded defect reproduction statement.",
		"searched":     "The bounded backlog search statement.",
		"outcome":      "none_found",
		"root_cause":   "The bounded root cause statement.",
	})
	fields := workflowContractFieldsFixture()
	binding := workflowArchitectureBindingForSeededRegistry(t, s)
	binding["domain_modifies"] = []string{pm1fixture.FixtureRootDomainID}
	fields["architecture_binding"] = binding
	fields["law_change_reason"] = "The bounded repair changes the declared Domain."
	fields["law_change_removes_user_visible_behavior"] = false
	fields["law_change_touches_security_or_data_safety"] = false
	fields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "absent", "outcome_payload": map[string]any{
		"kind": "absent", "surface": "behavior:defect", "subjects": []string{workID}, "distinguish_from": []string{"disabled"},
	}}}
	act("approve_contract", fields)
	if !enterExecution {
		return
	}
	act("checkpoint_repair", map[string]any{})
	var started any
	if err := s.DatabaseForTesting().QueryRow(`SELECT execution_started_at FROM workflow_instances WHERE work_id=?`, workID).Scan(&started); err != nil || started == nil {
		t.Fatalf("fixture %s carries no durable execution start: %v", workID, err)
	}
}

// routineOverlapExactBinding reads the pair's current versions and active
// contract versions, so a request never pins a stale value the fixture moved.
func routineOverlapExactBinding(t *testing.T, s *store.Store, kind string) routineOverlapBinding {
	t.Helper()
	binding := routineOverlapBinding{
		kind: kind, fromWorkID: "work-1", toWorkID: "work-2",
		fromVersion: workVersion(t, s, "work-1"), toVersion: workVersion(t, s, "work-2"),
	}
	for _, side := range []struct {
		workID  string
		version *int64
	}{
		{workID: "work-1", version: &binding.fromContractVersion},
		{workID: "work-2", version: &binding.toContractVersion},
	} {
		if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, side.workID).Scan(side.version); err != nil {
			t.Fatalf("active contract for %s: %v", side.workID, err)
		}
	}
	return binding
}

// routineOverlapApprovalBindings renders the scope and version maps the host
// assertion signs. The keys are exactly the bounded scope the planner carries:
// the directional and kind keys stay out of the signed scope, and the digest
// is what binds them.
func routineOverlapApprovalBindings(t *testing.T, s *store.Store, env CallEnvelope, binding routineOverlapBinding) (map[string]any, map[string]any) {
	t.Helper()
	if binding.fromVersion != workVersion(t, s, binding.fromWorkID) || binding.toVersion != workVersion(t, s, binding.toWorkID) {
		t.Fatal("stale binding: an endpoint version moved before the approval was signed")
	}
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{binding.fromWorkID, binding.toWorkID}, "scope_version": env.ScopeVersion}
	versions := map[string]any{"from": binding.fromVersion, "to": binding.toVersion, "from_contract": binding.fromContractVersion, "to_contract": binding.toContractVersion}
	return scope, versions
}

// routineOverlapResolveInput builds one resolve_overlap request body.
func routineOverlapResolveInput(t *testing.T, binding routineOverlapBinding, key string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"from_work_id": binding.fromWorkID, "to_work_id": binding.toWorkID,
		"from_expected_version": binding.fromVersion, "to_expected_version": binding.toVersion,
		"from_contract_version": binding.fromContractVersion, "to_contract_version": binding.toContractVersion,
		"resolution_kind": binding.kind, "reason": "the operator settles the destructive overlap",
		"idempotency_key": key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// routineOverlapChallenge drives the first, approval-less dispatch and
// asserts the genuine challenge: its typed refusal, its reference, its
// bound direction and kind, and its exact request digest.
func routineOverlapChallenge(t *testing.T, s *store.Store, service *Service, env CallEnvelope, binding routineOverlapBinding, input []byte) string {
	t.Helper()
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
	if first.Error == nil || first.Error.Kind != "approval_required" {
		t.Fatalf("destructive %s resolution without approval outcome=%s error=%+v, want the typed challenge", binding.kind, first.Outcome, first.Error)
	}
	challengeRef, ok := first.Error.Details["approval_ref"].(string)
	if !ok || challengeRef == "" {
		t.Fatalf("destructive %s challenge carries no approval ref: %+v", binding.kind, first.Error.Details)
	}
	if first.Error.Details["resolution_kind"] != binding.kind || first.Error.Details["from_work_id"] != binding.fromWorkID || first.Error.Details["to_work_id"] != binding.toWorkID {
		t.Fatalf("destructive %s challenge omitted the bound direction: %+v", binding.kind, first.Error.Details)
	}
	if digest, _ := first.Error.Details["operation_digest"].(string); digest != mutationDigest("concord_work_relate", "resolve_overlap", env, input) {
		t.Fatalf("destructive %s challenge digest %v does not bind the exact request", binding.kind, first.Error.Details["operation_digest"])
	}
	return challengeRef
}

// routineOverlapApprovedDispatch signs the host assertion over one digest and
// re-dispatches the input with the approval attached. The caller owns the
// digest, so a test can sign the minted request's exact digest and submit a
// different one.
func routineOverlapApprovedDispatch(t *testing.T, s *store.Store, service *Service, privateKey ed25519.PrivateKey, env CallEnvelope, approvalRef, digest string, scope, versions map[string]any, input []byte) Envelope {
	t.Helper()
	approvedEnv := env
	approvedEnv.HostApproval = signedHostApproval(privateKey, approvalRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(approvalRef))
	return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, approvedEnv)
}

// TestRoutineCoordinationDestructiveKindsSettleThroughAConsumedHostApproval:
// each destructive kind resolves only through a challenge the Service minted,
// a host assertion over the exact request, and one consumed one-use approval
// row. The terminal and survivor lifecycles, the one-step endpoint version
// bumps, the single consumed row, and the spent-row refusal are the settled
// effect.
func TestRoutineCoordinationDestructiveKindsSettleThroughAConsumedHostApproval(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ kind, terminalID, survivorID string }{
		{kind: store.ResolutionMergedInto, terminalID: "work-1", survivorID: "work-2"},
		{kind: store.ResolutionSupersedes, terminalID: "work-2", survivorID: "work-1"},
	} {
		t.Run(testCase.kind, func(t *testing.T) {
			t.Parallel()
			s, service, privateKey, env := routineCoordinationFixture(t)
			binding := routineOverlapExactBinding(t, s, testCase.kind)
			input := routineOverlapResolveInput(t, binding, "routine-destructive-"+testCase.kind)
			challengeRef := routineOverlapChallenge(t, s, service, env, binding, input)
			withApproval, err := injectApproval(input, challengeRef)
			if err != nil {
				t.Fatal(err)
			}
			scope, versions := routineOverlapApprovalBindings(t, s, env, binding)
			digest := mutationDigest("concord_work_relate", "resolve_overlap", env, withApproval)
			approved := routineOverlapApprovedDispatch(t, s, service, privateKey, env, challengeRef, digest, scope, versions, withApproval)
			if approved.Outcome != OutcomeOK {
				t.Fatalf("approved destructive %s outcome=%s error=%+v", testCase.kind, approved.Outcome, approved.Error)
			}
			if got := workLifecycle(t, s, testCase.terminalID); got != "superseded" {
				t.Fatalf("destructive %s left the terminal work %q, want superseded", testCase.kind, got)
			}
			if got := workLifecycle(t, s, testCase.survivorID); got != "in_progress" {
				t.Fatalf("destructive %s left the survivor %q, want in_progress", testCase.kind, got)
			}
			if got := workVersion(t, s, "work-1"); got != binding.fromVersion+1 {
				t.Fatalf("destructive %s left work-1 at v%d, want the single step to %d", testCase.kind, got, binding.fromVersion+1)
			}
			if got := workVersion(t, s, "work-2"); got != binding.toVersion+1 {
				t.Fatalf("destructive %s left work-2 at v%d, want the single step to %d", testCase.kind, got, binding.toVersion+1)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 1 {
				t.Fatalf("destructive %s left %d approval rows, want the single consumed approval", testCase.kind, got)
			}
			if used := countRows(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approvals`); used != 1 {
				t.Fatalf("destructive %s approval used_count=%d, want the consumed one-use row", testCase.kind, used)
			}
			var recordedRef, eventRef string
			if err := s.DatabaseForTesting().QueryRow(`SELECT approval_ref FROM workflow_overlap_resolutions`).Scan(&recordedRef); err != nil {
				t.Fatal(err)
			}
			if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.approval_ref') FROM domain_events WHERE kind='workflow.overlap_resolved'`).Scan(&eventRef); err != nil {
				t.Fatal(err)
			}
			if recordedRef == "" || recordedRef != eventRef || recordedRef == challengeRef {
				t.Fatalf("destructive %s persisted approval projection=%q event=%q challenge=%q", testCase.kind, recordedRef, eventRef, challengeRef)
			}

			// The cached replay of the same signed request answers with the
			// original result and consumes nothing again: the approval row
			// stays the single consumed one-use row, the settled resolution
			// and its event stay alone, and no second receipt exists.
			replayed := routineOverlapApprovedDispatch(t, s, service, privateKey, env, challengeRef, digest, scope, versions, withApproval)
			if replayed.Outcome != OutcomeOK || replayed.Error != nil {
				t.Fatalf("cached destructive %s replay outcome=%s error=%+v, want the original cached success", testCase.kind, replayed.Outcome, replayed.Error)
			}
			if !replayed.Replayed {
				t.Fatalf("cached destructive %s replay carries replayed=false, want the replayed marker", testCase.kind)
			}
			if !sameResultPayload(t, approved.Result, replayed.Result) {
				t.Fatalf("cached destructive %s replay result payload differs from the original: original=%s replayed=%s", testCase.kind, approved.Result, replayed.Result)
			}
			if !sameChangedRefs(t, approved.ChangedRefs, replayed.ChangedRefs) {
				t.Fatalf("cached destructive %s replay changed refs %+v differ from the original %+v", testCase.kind, replayed.ChangedRefs, approved.ChangedRefs)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 1 {
				t.Fatalf("cached destructive %s replay left %d approval rows, want the one consumed row", testCase.kind, got)
			}
			if used := countRows(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approvals`); used != 1 {
				t.Fatalf("cached destructive %s replay approval used_count=%d, want the single consumed use", testCase.kind, used)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
				t.Fatalf("cached destructive %s replay left %d resolutions, want the settled one", testCase.kind, got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE kind='workflow.overlap_resolved'`); got != 1 {
				t.Fatalf("cached destructive %s replay left %d resolution events, want the settled one", testCase.kind, got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`); got != 1 {
				t.Fatalf("cached destructive %s replay left %d idempotency receipts, want the original alone", testCase.kind, got)
			}

			// The spent row settles nothing again: the same approval, bound to
			// the exact settled request, presented for a fresh request refuses,
			// and nothing moves.
			spentInput := routineOverlapResolveInput(t, binding, "routine-destructive-spent-"+testCase.kind)
			spentWithApproval, err := injectApproval(spentInput, recordedRef)
			if err != nil {
				t.Fatal(err)
			}
			spentDigest := mutationDigest("concord_work_relate", "resolve_overlap", env, spentWithApproval)
			reuse := routineOverlapApprovedDispatch(t, s, service, privateKey, env, recordedRef, spentDigest, scope, versions, spentWithApproval)
			if reuse.Error == nil || reuse.Error.Kind != "approval_invalid" {
				t.Fatalf("spent destructive %s approval settled again: %+v", testCase.kind, reuse)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 1 {
				t.Fatalf("the refused reuse left %d approval rows, want the one spent row", got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
				t.Fatalf("the refused reuse left %d resolutions, want the settled one", got)
			}

		})
	}
}

// TestRoutineCoordinationDestructiveApprovalRefusesWhenBoundToAnotherRequest:
// the approval binds one exact destructive request, and presenting it for a
// request that differs in any bound field — kind, direction, endpoint
// version, or contract version — refuses by name and leaves no effect and no
// consumption.
func TestRoutineCoordinationDestructiveApprovalRefusesWhenBoundToAnotherRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(binding *routineOverlapBinding)
	}{
		{name: "kind", mutate: func(binding *routineOverlapBinding) { binding.kind = store.ResolutionSupersedes }},
		{name: "direction", mutate: func(binding *routineOverlapBinding) {
			binding.fromWorkID, binding.toWorkID = binding.toWorkID, binding.fromWorkID
			binding.fromVersion, binding.toVersion = binding.toVersion, binding.fromVersion
			binding.fromContractVersion, binding.toContractVersion = binding.toContractVersion, binding.fromContractVersion
		}},
		{name: "from endpoint version pin", mutate: func(binding *routineOverlapBinding) { binding.fromVersion-- }},
		{name: "to endpoint version pin", mutate: func(binding *routineOverlapBinding) { binding.toVersion-- }},
		{name: "from contract pin", mutate: func(binding *routineOverlapBinding) { binding.fromContractVersion++ }},
		{name: "to contract pin", mutate: func(binding *routineOverlapBinding) { binding.toContractVersion++ }},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Parallel()
			s, service, privateKey, env := routineCoordinationFixture(t)
			const requestKind = store.ResolutionMergedInto
			// One schema-valid request key, and only item.mutate varies the
			// tested request fields: the mutated request differs from the
			// minted one in the bound field alone.
			const requestKey = "routine-destructive-mismatch"
			minted := routineOverlapExactBinding(t, s, requestKind)
			input := routineOverlapResolveInput(t, minted, requestKey)
			challengeRef := routineOverlapChallenge(t, s, service, env, minted, input)
			mintedWithApproval, err := injectApproval(input, challengeRef)
			if err != nil {
				t.Fatal(err)
			}
			// The host signs the minted request's exact digest and bindings.
			mintedDigest := mutationDigest("concord_work_relate", "resolve_overlap", env, mintedWithApproval)
			mintedScope, mintedVersions := routineOverlapApprovalBindings(t, s, env, minted)

			mutated := minted
			item.mutate(&mutated)
			mutatedInput := routineOverlapResolveInput(t, mutated, requestKey)
			mutatedWithApproval, err := injectApproval(mutatedInput, challengeRef)
			if err != nil {
				t.Fatal(err)
			}
			response := routineOverlapApprovedDispatch(t, s, service, privateKey, env, challengeRef, mintedDigest, mintedScope, mintedVersions, mutatedWithApproval)
			if response.Error == nil || response.Error.Kind != "approval_invalid" {
				t.Fatalf("approval bound to another %s request outcome=%s error=%+v, want approval_invalid", item.name, response.Outcome, response.Error)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 0 {
				t.Fatalf("the refused mismatch consumed %d approval rows", got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 0 {
				t.Fatalf("the refused mismatch left %d resolutions", got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE kind='workflow.overlap_resolved'`); got != 0 {
				t.Fatalf("the refused mismatch left %d resolution events", got)
			}
			if got := workVersion(t, s, "work-1"); got != minted.fromVersion {
				t.Fatalf("the refused mismatch moved work-1 to v%d, want %d", got, minted.fromVersion)
			}
			if got := workVersion(t, s, "work-2"); got != minted.toVersion {
				t.Fatalf("the refused mismatch moved work-2 to v%d, want %d", got, minted.toVersion)
			}
		})
	}
}

// TestRoutineCoordinationDestructiveKindsRefuseWithoutAConsumableApproval:
// no approval at all mints the typed challenge, an approval reference no host
// exchange ever derived refuses, and a minted but unconsumed challenge
// presented without its host assertion refuses. None of them consumes
// authority or moves a version.
func TestRoutineCoordinationDestructiveKindsRefuseWithoutAConsumableApproval(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{store.ResolutionMergedInto, store.ResolutionSupersedes} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, service, privateKey, env := routineCoordinationFixture(t)
			binding := routineOverlapExactBinding(t, s, kind)
			input := routineOverlapResolveInput(t, binding, "routine-destructive-missing-"+kind)

			// Missing: the first dispatch is the typed challenge naming the
			// exact destructive resolution.
			challengeRef := routineOverlapChallenge(t, s, service, env, binding, input)

			// Fabricated: a well-shaped reference no challenge or approval row
			// ever carried, with a host assertion over the exact request.
			fabricatedRef := strings.Repeat("f", 64)
			fabricatedInput, err := injectApproval(input, fabricatedRef)
			if err != nil {
				t.Fatal(err)
			}
			scope, versions := routineOverlapApprovalBindings(t, s, env, binding)
			fabricatedDigest := mutationDigest("concord_work_relate", "resolve_overlap", env, fabricatedInput)
			fabricated := routineOverlapApprovedDispatch(t, s, service, privateKey, env, fabricatedRef, fabricatedDigest, scope, versions, fabricatedInput)
			if fabricated.Error == nil || fabricated.Error.Kind != "approval_invalid" {
				t.Fatalf("fabricated destructive %s approval outcome=%s error=%+v, want approval_invalid", kind, fabricated.Outcome, fabricated.Error)
			}

			// Unconsumed: the minted challenge presented with no host
			// assertion at all never settles.
			unconsumedInput, err := injectApproval(input, challengeRef)
			if err != nil {
				t.Fatal(err)
			}
			unconsumed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: unconsumedInput}, env)
			if unconsumed.Error == nil || unconsumed.Error.Kind != "approval_invalid" {
				t.Fatalf("unconsumed destructive %s challenge outcome=%s error=%+v, want approval_invalid", kind, unconsumed.Outcome, unconsumed.Error)
			}

			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 0 {
				t.Fatalf("the refused destructive %s claims consumed %d approval rows", kind, got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 0 {
				t.Fatalf("the refused destructive %s claims left %d resolutions", kind, got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE kind='workflow.overlap_resolved'`); got != 0 {
				t.Fatalf("the refused destructive %s claims left %d resolution events", kind, got)
			}
			if got := workVersion(t, s, "work-1"); got != binding.fromVersion {
				t.Fatalf("the refused destructive %s claims moved work-1 to v%d, want %d", kind, got, binding.fromVersion)
			}
			if got := workVersion(t, s, "work-2"); got != binding.toVersion {
				t.Fatalf("the refused destructive %s claims moved work-2 to v%d, want %d", kind, got, binding.toVersion)
			}
		})
	}
}

// TestRoutineCoordinationRoutineKindsRefuseASuppliedApprovalClaim: the
// routine kinds — compatible_with, depends_on, blocks — are coordination
// answers the authenticated caller records without operator consent. This
// pins the same-Product fresh refusal. The transaction derives the
// request's consent requirement, the tail consumes nothing, and the effect
// then refuses the unconsumed claim as invalid_input: no challenge, no
// consumption, no resolution row, no resolution event, and no endpoint
// version move. A cross-Product routine request whose tail consumes its
// claim settles instead. This test covers only the fresh same-Product
// call. The claim is genuine: the reference a real destructive-kind
// dispatch minted through the same Service on the same fixture, so the
// refusal is the planner's routine-branch decision and not an
// unknown-reference guess. No authority row is fabricated anywhere.
func TestRoutineCoordinationRoutineKindsRefuseASuppliedApprovalClaim(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{store.ResolutionCompatibleWith, store.ResolutionDependsOn, store.ResolutionBlocks} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, service, _, env := routineCoordinationFixture(t)
			// The genuine destructive-kind challenge this Service minted on
			// this fixture. The routine request below presents it without a
			// host assertion, and the planner must refuse on the routine
			// branch before any approval binding is inspected.
			destructive := routineOverlapExactBinding(t, s, store.ResolutionMergedInto)
			challengeRef := routineOverlapChallenge(t, s, service, env, destructive, routineOverlapResolveInput(t, destructive, "routine-supplied-claim-challenge"))
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 1 {
				t.Fatalf("the genuine destructive challenge left %d challenge rows, want exactly one", got)
			}
			// One fixed schema-valid request key, and the tested request
			// differs from a claimless routine request in the injected
			// approval claim alone.
			const requestKey = "routine-supplied-claim"
			binding := routineOverlapExactBinding(t, s, kind)
			input := routineOverlapResolveInput(t, binding, requestKey)
			withClaim, err := injectApproval(input, challengeRef)
			if err != nil {
				t.Fatal(err)
			}
			refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: withClaim}, env)
			if refused.Error == nil || refused.Error.Kind != "invalid_input" || !strings.Contains(refused.Error.Message, "approval") {
				t.Fatalf("supplied claim on routine %s outcome=%s error=%+v, want invalid_input naming the approval claim", kind, refused.Outcome, refused.Error)
			}
			// No challenge was minted for the routine request and the one
			// genuine challenge stays unconsumed; no approval row, no
			// resolution row, no resolution event, and no endpoint version
			// move exists.
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 1 {
				t.Fatalf("the refused routine %s claim left %d challenge rows, want the one genuine challenge untouched", kind, got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 0 {
				t.Fatalf("the refused routine %s claim consumed %d approval rows", kind, got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 0 {
				t.Fatalf("the refused routine %s claim left %d resolutions", kind, got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE kind='workflow.overlap_resolved'`); got != 0 {
				t.Fatalf("the refused routine %s claim left %d resolution events", kind, got)
			}
			if got := workVersion(t, s, "work-1"); got != binding.fromVersion {
				t.Fatalf("the refused routine %s claim moved work-1 to v%d, want %d", kind, got, binding.fromVersion)
			}
			if got := workVersion(t, s, "work-2"); got != binding.toVersion {
				t.Fatalf("the refused routine %s claim moved work-2 to v%d, want %d", kind, got, binding.toVersion)
			}
		})
	}
}

// sameResultPayload reports whether two mutation result payloads decode to
// equal objects, so byte order cannot mask or fake equality.
func sameResultPayload(t *testing.T, original, replayed json.RawMessage) bool {
	t.Helper()
	var originalObject, replayedObject map[string]any
	if err := json.Unmarshal(original, &originalObject); err != nil {
		t.Fatalf("original result is not a JSON object: %v", err)
	}
	if err := json.Unmarshal(replayed, &replayedObject); err != nil {
		t.Fatalf("replayed result is not a JSON object: %v", err)
	}
	return reflect.DeepEqual(originalObject, replayedObject)
}

// sameChangedRefs reports whether two changed-ref lists carry the same
// entities and versions.
func sameChangedRefs(t *testing.T, original, replayed *[]ChangedRef) bool {
	t.Helper()
	if original == nil || replayed == nil {
		return original == nil && replayed == nil
	}
	return reflect.DeepEqual(*original, *replayed)
}

// TestRoutineCoordinationCachedReplayPreservesOriginalResultWithoutNewEffects:
// a cached same-key replay that adds an unneeded approval claim refuses with
// the same invalid_input refusal the fresh routine call returns. The
// canonical mutation digest excludes the approval block by design, so the
// claim-bearing replay carries the stored digest of the unsigned success,
// and the routine overlap surface owns that refusal at every replay owner.
// The refused replay records no new durable effect: no domain event, no
// resolution row, no endpoint version move, no approval row consumed or
// minted, no idempotency row added, and no receipt touch. The unchanged
// unsigned replay stays the cached success: the ok outcome, the replayed
// marker, and the exact original result payload and changed refs.
func TestRoutineCoordinationCachedReplayPreservesOriginalResultWithoutNewEffects(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{store.ResolutionCompatibleWith, store.ResolutionDependsOn, store.ResolutionBlocks} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, service, _, env := routineCoordinationFixture(t)
			binding := routineOverlapExactBinding(t, s, kind)
			input := routineOverlapResolveInput(t, binding, "routine-cached-replay")
			unsigned := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
			if unsigned.Outcome != OutcomeOK || unsigned.Error != nil {
				t.Fatalf("unsigned routine %s outcome=%s error=%+v, want the plain success", kind, unsigned.Outcome, unsigned.Error)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
				t.Fatalf("the unsigned routine %s left %d resolutions, want the settled one", kind, got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 0 {
				t.Fatalf("the unsigned routine %s left %d approval rows", kind, got)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
				t.Fatalf("the unsigned routine %s minted %d challenge rows", kind, got)
			}
			// The baseline populations the replay is measured against: one
			// settled resolution and its two version bumps, and no approval
			// authority anywhere.
			baselineEvents := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events`)
			baselineResolutions := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`)
			baselineChallenges := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`)
			baselineApprovals := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`)
			baselineIdempotency := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`)
			baselineFromVersion := workVersion(t, s, "work-1")
			baselineToVersion := workVersion(t, s, "work-2")

			withClaim, err := injectApproval(input, "challenge:cached-replay-claim")
			if err != nil {
				t.Fatal(err)
			}
			claimReplay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: withClaim}, env)
			if claimReplay.Outcome != OutcomeError || claimReplay.Error == nil || claimReplay.Error.Kind != "invalid_input" || claimReplay.Error.Message != "a routine overlap resolution accepts no approval claim" {
				t.Errorf("cached %s replay with an unneeded claim outcome=%s error=%+v, want the same fresh invalid_input refusal the unsigned claim path returns", kind, claimReplay.Outcome, claimReplay.Error)
			}
			// The replay must settle nothing new: these probes measure the
			// reply against the baseline populations captured before it.
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events`); got != baselineEvents {
				t.Errorf("the %s claim replay left %d domain events, want the baseline %d", kind, got, baselineEvents)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != baselineResolutions {
				t.Errorf("the %s claim replay left %d resolutions, want the baseline %d", kind, got, baselineResolutions)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != baselineChallenges {
				t.Errorf("the %s claim replay left %d challenge rows, want the baseline %d", kind, got, baselineChallenges)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != baselineApprovals {
				t.Errorf("the %s claim replay left %d approval rows, want the baseline %d", kind, got, baselineApprovals)
			}
			if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`); got != baselineIdempotency {
				t.Errorf("the %s claim replay left %d idempotency rows, want the baseline %d", kind, got, baselineIdempotency)
			}
			if touched := countRows(t, s.DatabaseForTesting(), `SELECT replayed_count FROM idempotency_records WHERE idempotency_key='routine-cached-replay'`); touched != 0 {
				t.Errorf("the %s claim replay touched the receipt %d times, want the refused claim to leave it untouched", kind, touched)
			}
			if got := workVersion(t, s, "work-1"); got != baselineFromVersion {
				t.Errorf("the %s claim replay moved work-1 to v%d, want %d", kind, got, baselineFromVersion)
			}
			if got := workVersion(t, s, "work-2"); got != baselineToVersion {
				t.Errorf("the %s claim replay moved work-2 to v%d, want %d", kind, got, baselineToVersion)
			}

			// The unchanged unsigned replay stays the cached success: the ok
			// outcome, the replayed marker, and the exact original result
			// payload and changed refs, with the single receipt observation
			// the replay touch records.
			unchanged := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
			if unchanged.Outcome != OutcomeOK || unchanged.Error != nil {
				t.Fatalf("the unchanged unsigned %s replay outcome=%s error=%+v, want the cached success", kind, unchanged.Outcome, unchanged.Error)
			}
			if !unchanged.Replayed {
				t.Errorf("the unchanged unsigned %s replay carries replayed=false, want the replayed marker", kind)
			}
			if !sameResultPayload(t, unsigned.Result, unchanged.Result) {
				t.Errorf("the unchanged unsigned %s replay result payload differs from the original: original=%s replayed=%s", kind, unsigned.Result, unchanged.Result)
			}
			if !sameChangedRefs(t, unsigned.ChangedRefs, unchanged.ChangedRefs) {
				t.Errorf("the unchanged unsigned %s replay changed refs %+v differ from the original %+v", kind, unchanged.ChangedRefs, unsigned.ChangedRefs)
			}
			if touched := countRows(t, s.DatabaseForTesting(), `SELECT replayed_count FROM idempotency_records WHERE idempotency_key='routine-cached-replay'`); touched != 1 {
				t.Errorf("the unchanged unsigned %s replay touched the receipt %d times, want exactly the one replay observation", kind, touched)
			}
		})
	}
}

// TestRoutineCoordinationTransactionalCacheHitRefusesAnAddedRoutineClaim:
// the common transactional cache hit refuses an added routine claim exactly
// like the early replay. The nested unsigned original commits inside the
// outer dispatch's mutation host probe — after the outer's early replay
// lookup missed and before the mutation transaction opens — so the outer
// request reaches executeMutation's found-record branch carrying the stored
// digest of the unsigned success. The canonical digest excludes the
// approval block by design, so the claim-bearing outer carries that digest;
// the found-record branch must refuse it with the same fresh invalid_input
// refusal and leave the nested original alone: no second resolution, no
// resolution event, no approval row, no challenge, no receipt insert, no
// receipt touch, and no version move.
func TestRoutineCoordinationTransactionalCacheHitRefusesAnAddedRoutineClaim(t *testing.T) {
	t.Parallel()
	s, service, _, env := routineCoordinationFixture(t)
	binding := routineOverlapExactBinding(t, s, store.ResolutionCompatibleWith)
	input := routineOverlapResolveInput(t, binding, "routine-tx-cache-claim")
	withClaim, err := injectApproval(input, "challenge:transactional-cache-claim")
	if err != nil {
		t.Fatal(err)
	}
	// The mutation's host probe is the third probe of the dispatch (the two
	// dispatch identity authorizations precede it) and is the one position
	// between the early replay lookup and the transactional cache lookup.
	// The nested dispatch is the genuine unsigned original on the same key;
	// its own probes pass through untouched, and the guard restores the
	// original prober after the outer dispatch returns.
	originalProbe := service.ProjectHostProber
	probes := 0
	nestedDone := false
	inNested := false
	service.ProjectHostProber = func(ctx context.Context, directory, worktree string) (store.ResolvedProjectHost, error) {
		if inNested {
			return originalProbe(ctx, directory, worktree)
		}
		probes++
		if probes == 3 && !nestedDone {
			nestedDone = true
			inNested = true
			nested := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
			inNested = false
			if nested.Outcome != OutcomeOK || nested.Error != nil {
				t.Fatalf("nested unsigned original outcome=%s error=%+v, want the plain routine success", nested.Outcome, nested.Error)
			}
		}
		return originalProbe(ctx, directory, worktree)
	}
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: withClaim}, env)
	service.ProjectHostProber = originalProbe
	if probes != 3 || !nestedDone {
		t.Fatalf("host probes=%d nested original committed=%v, want the nested success at the mutation probe position 3", probes, nestedDone)
	}
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_input" || refused.Error.Message != "a routine overlap resolution accepts no approval claim" {
		t.Fatalf("transactional cache hit with an added claim outcome=%s error=%+v, want the same fresh invalid_input refusal", refused.Outcome, refused.Error)
	}
	// The nested original is the only effect. The refused outer added no
	// resolution, no approval authority, no receipt, and no receipt touch;
	// the stored digest names the unsigned original, which is what made the
	// outer request a same-key cache hit rather than a conflict.
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
		t.Fatalf("the refused transactional claim left %d resolutions, want the nested original alone", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE kind='workflow.overlap_resolved'`); got != 1 {
		t.Fatalf("the refused transactional claim left %d resolution events, want the nested original alone", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 0 {
		t.Fatalf("the refused transactional claim left %d approval rows", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
		t.Fatalf("the refused transactional claim minted %d challenge rows", got)
	}
	var storedDigest string
	var touched int
	if err := s.DatabaseForTesting().QueryRow(`SELECT canonical_digest,replayed_count FROM idempotency_records WHERE idempotency_key='routine-tx-cache-claim'`).Scan(&storedDigest, &touched); err != nil {
		t.Fatal(err)
	}
	if storedDigest != mutationDigest("concord_work_relate", "resolve_overlap", env, input) {
		t.Fatalf("stored digest %s does not name the unsigned original, so the outer request was not a same-key cache hit", storedDigest)
	}
	if touched != 0 {
		t.Fatalf("the refused transactional claim touched the receipt %d times, want the refused claim to leave it untouched", touched)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`); got != 1 {
		t.Fatalf("the refused transactional claim left %d idempotency receipts, want the nested original alone", got)
	}
	if got := workVersion(t, s, "work-1"); got != binding.fromVersion+1 {
		t.Fatalf("the refused transactional claim left work-1 at v%d, want the nested original's single step to %d", got, binding.fromVersion+1)
	}
	if got := workVersion(t, s, "work-2"); got != binding.toVersion+1 {
		t.Fatalf("the refused transactional claim left work-2 at v%d, want the nested original's single step to %d", got, binding.toVersion+1)
	}
}

// A refused cached claim needs no write lock. An unchanged success is not
// dispatched under the held writer because it legitimately touches its receipt.
func TestRoutineCoordinationCachedClaimRefusesBeforeWriterRelease(t *testing.T) {
	t.Parallel()
	s, service, _, env := routineCoordinationFixture(t)
	binding := routineOverlapExactBinding(t, s, store.ResolutionCompatibleWith)
	input := routineOverlapResolveInput(t, binding, "routine-cached-claim-writer")
	unsigned := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
	if unsigned.Outcome != OutcomeOK || unsigned.Error != nil {
		t.Fatalf("unsigned routine compatible_with outcome=%s error=%+v, want the plain success", unsigned.Outcome, unsigned.Error)
	}
	baselineEvents := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events`)
	baselineResolutions := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`)
	baselineChallenges := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`)
	baselineApprovals := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`)
	baselineIdempotency := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`)
	baselineFromVersion := workVersion(t, s, "work-1")
	baselineToVersion := workVersion(t, s, "work-2")
	withClaim, err := injectApproval(input, "challenge:cached-claim-writer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`PRAGMA busy_timeout=50`); err != nil {
		t.Fatal(err)
	}
	neighbor.Start(t, s.Path(), neighbor.Writer)
	started := time.Now()
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: withClaim}, env)
	elapsed := time.Since(started)
	if refused.Outcome != OutcomeError || refused.Error == nil || refused.Error.Kind != "invalid_input" || refused.Error.Message != "a routine overlap resolution accepts no approval claim" {
		t.Fatalf("held-writer cached claim outcome=%s error=%+v, want the same fresh invalid_input refusal the unlocked replay returns", refused.Outcome, refused.Error)
	}
	if elapsed >= neighbor.WriterHold {
		t.Fatalf("the held-writer claim replay answered after %s, want the refusal before the writer's release", elapsed)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events`); got != baselineEvents {
		t.Errorf("the held-writer claim replay left %d domain events, want the baseline %d", got, baselineEvents)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != baselineResolutions {
		t.Errorf("the held-writer claim replay left %d resolutions, want the baseline %d", got, baselineResolutions)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approval_challenges`); got != baselineChallenges {
		t.Errorf("the held-writer claim replay left %d challenge rows, want the baseline %d", got, baselineChallenges)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != baselineApprovals {
		t.Errorf("the held-writer claim replay left %d approval rows, want the baseline %d", got, baselineApprovals)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`); got != baselineIdempotency {
		t.Errorf("the held-writer claim replay left %d idempotency receipts, want the baseline %d", got, baselineIdempotency)
	}
	if touched := countRows(t, s.DatabaseForTesting(), `SELECT replayed_count FROM idempotency_records WHERE idempotency_key='routine-cached-claim-writer'`); touched != 0 {
		t.Errorf("the held-writer claim replay touched the receipt %d times, want the refused claim to leave it untouched", touched)
	}
	if got := workVersion(t, s, "work-1"); got != baselineFromVersion {
		t.Errorf("the held-writer claim replay moved work-1 to v%d, want %d", got, baselineFromVersion)
	}
	if got := workVersion(t, s, "work-2"); got != baselineToVersion {
		t.Errorf("the held-writer claim replay moved work-2 to v%d, want %d", got, baselineToVersion)
	}
}

// seedRoutineCoordinationPrimaryProjectFixture gives both works an
// event-owned primary Project that belongs to product-1 alone. The shared
// project-1 stays the ambient Project and demotes to a secondary work
// membership, so the contract binding resolves exactly one primary Product
// while the role-blind mutation scope derivation still crosses both Products
// through the shared Project. The Project joins product-1 as a secondary
// Product membership: product-1's one primary Project slot is the shared
// Project's, and the binding filter reads the work membership's role, never
// the Product membership's. Every seed is a real event pinned at its
// subject's current version, so the rebuild refolds it.
func seedRoutineCoordinationPrimaryProjectFixture(t *testing.T, s *store.Store, ctx context.Context) {
	t.Helper()
	var productVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM products WHERE id='product-1'`).Scan(&productVersion); err != nil {
		t.Fatal(err)
	}
	workVersions := map[string]int64{}
	for _, workID := range []string{"work-1", "work-2"} {
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		workVersions[workID] = version
	}
	memberships := func(workID string, version int64) store.Event {
		payload, err := json.Marshal(map[string]any{
			"memberships":       []map[string]any{{"project_id": "project-primary", "role": "primary"}, {"project_id": "project-1", "role": "secondary"}},
			"expected_version":  version,
			"resulting_version": version + 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return store.Event{EventID: "cross-product-" + workID + "-primary-memberships", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}
	}
	addedPayload, err := json.Marshal(map[string]any{
		"product_id": "product-1", "project_id": "project-primary", "role": "secondary", "reason": "fixture",
		"expected_version":  productVersion,
		"resulting_version": productVersion + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []store.Event{
		{EventID: "cross-product-primary-project", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-primary", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Primary Project"}`)},
		{EventID: "cross-product-primary-membership", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: addedPayload},
		memberships("work-1", workVersions["work-1"]),
		memberships("work-2", workVersions["work-2"]),
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): productVersion, store.VersionRef(store.SubjectProject, "project-primary"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): workVersions["work-1"], store.VersionRef(store.SubjectWorkItem, "work-2"): workVersions["work-2"]}}); err != nil {
		t.Fatal(err)
	}
}

// crossProductRoutineCoordinationFixture builds the unresolved-overlap pair
// under a two-Product grant. Each work's primary Project belongs to product-1
// alone, so both contract bindings resolve exactly one footprint Product; the
// shared project-1 stays the ambient Project, belongs to both Products, and
// holds a secondary work membership on both works, so the generic mutation
// scope derivation reports a cross-Product request for any envelope that
// selects one of the two. The workflow drives, the Domain law, and the
// derived unresolved overlap match routineCoordinationFixture exactly.
func crossProductRoutineCoordinationFixture(t *testing.T, capabilities []Capability) (*store.Store, *Service, Authority, ed25519.PrivateKey, CallEnvelope) {
	t.Helper()
	ctx := context.Background()
	s, service, grant, privateKey := crossProductDispatchFixture(t, capabilities)
	seedRoutineCoordinationPrimaryProjectFixture(t, s, ctx)
	seedRoutineCoordinationDomainFixture(t, s, ctx)
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	for _, workID := range []string{"work-1", "work-2"} {
		if err := s.Transact(ctx, func(tx *store.Transaction) error {
			return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: fixedTime()})
		}); err != nil {
			t.Fatalf("initialize %s: %v", workID, err)
		}
	}
	routineCoordinationDriveWorkflow(t, s, "work-1", actor, true)
	routineCoordinationDriveWorkflow(t, s, "work-2", actor, false)
	err = store.InspectWorkflowDomainOverlap(ctx, s, "work-2")
	failure := &store.Failure{}
	if !errors.As(err, &failure) || failure.Kind != store.KindDomainOverlap {
		t.Fatalf("cross-Product fixture pair derives no unresolved overlap for the contender work-2: %v", err)
	}
	for _, workID := range []string{"work-1", "work-2"} {
		if got := workLifecycle(t, s, workID); got != "in_progress" {
			t.Fatalf("cross-Product fixture %s lifecycle = %q, want the action-fold in_progress", workID, got)
		}
	}
	env := mutationEnvelope(grant, scopeVersionForProject(t, s, "project-1"))
	return s, service, grant, privateKey, env
}

// routineOverlapCrossScopeBindings renders the scope and version maps the
// host assertion signs for one cross-Product request. The scope is exactly
// the bounded planner scope: the envelope's selected Product, the derived
// Product set, the ambient Project, both endpoints, and the scope version.
// An empty selected Product drops out the way the bounded scope omits it, and
// no other key joins the signed bindings.
func routineOverlapCrossScopeBindings(t *testing.T, s *store.Store, env CallEnvelope, binding routineOverlapBinding, derivedProducts []string) (map[string]any, map[string]any) {
	t.Helper()
	if binding.fromVersion != workVersion(t, s, binding.fromWorkID) || binding.toVersion != workVersion(t, s, binding.toWorkID) {
		t.Fatal("stale binding: an endpoint version moved before the approval was signed")
	}
	scope := map[string]any{"product_ids": derivedProducts, "project_ids": []string{"project-1"}, "work_ids": []string{binding.fromWorkID, binding.toWorkID}, "scope_version": env.ScopeVersion}
	if env.SelectedProductID != "" {
		scope["product_id"] = env.SelectedProductID
	}
	versions := map[string]any{"from": binding.fromVersion, "to": binding.toVersion, "from_contract": binding.fromContractVersion, "to_contract": binding.toContractVersion}
	return scope, versions
}

// routineOverlapConsumedApprovalWitness asserts the approved retry left
// exactly one consumed one-use approval row.
func routineOverlapConsumedApprovalWitness(t *testing.T, s *store.Store, label string) {
	t.Helper()
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 1 {
		t.Fatalf("%s left %d approval rows, want the single consumed approval", label, got)
	}
	if used := countRows(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approvals`); used != 1 {
		t.Fatalf("%s approval used_count=%d, want the consumed one-use row", label, used)
	}
}

// TestRoutineCoordinationCrossProductRoutineKindSettlesThroughAConsumedHostApproval:
// compatible_with is a routine kind, yet the generic scope derivation reports
// the request cross-Product because the shared Project's second membership
// puts the endpoints' derived Product set outside the selected Product. The
// first dispatch therefore returns the typed challenge, and the host-approved
// retry consumes exactly one one-use approval and settles. The routine event
// carries no approval binding of any kind, the attribution is the
// authenticated caller, both endpoints advance one version, the commit is
// durable with the derived cross-Product set bound.
func TestRoutineCoordinationCrossProductRoutineKindSettlesThroughAConsumedHostApproval(t *testing.T) {
	t.Parallel()
	s, service, grant, privateKey, env := crossProductRoutineCoordinationFixture(t, []Capability{"work_relate", "cross_scope"})
	binding := routineOverlapExactBinding(t, s, store.ResolutionCompatibleWith)
	input := routineOverlapResolveInput(t, binding, "routine-cross-product-compatible")
	challengeRef := routineOverlapChallenge(t, s, service, env, binding, input)
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 0 {
		t.Fatalf("the cross-Product challenge minted %d approval rows", got)
	}
	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatal(err)
	}
	scope, versions := routineOverlapCrossScopeBindings(t, s, env, binding, []string{"product-1", "product-2"})
	digest := mutationDigest("concord_work_relate", "resolve_overlap", env, withApproval)
	approved := routineOverlapApprovedDispatch(t, s, service, privateKey, env, challengeRef, digest, scope, versions, withApproval)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved cross-Product compatible_with outcome=%s error=%+v, want the settled resolution", approved.Outcome, approved.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
		t.Fatalf("the approved cross-Product compatible_with left %d resolutions, want the settled one", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE kind='workflow.overlap_resolved'`); got != 1 {
		t.Fatalf("the approved cross-Product compatible_with left %d resolution events, want one", got)
	}
	if got := workVersion(t, s, "work-1"); got != binding.fromVersion+1 {
		t.Fatalf("cross-Product compatible_with left work-1 at v%d, want the single step to %d", got, binding.fromVersion+1)
	}
	if got := workVersion(t, s, "work-2"); got != binding.toVersion+1 {
		t.Fatalf("cross-Product compatible_with left work-2 at v%d, want the single step to %d", got, binding.toVersion+1)
	}
	routineOverlapConsumedApprovalWitness(t, s, "cross-Product compatible_with")
	var recordedRef, eventRef, eventScope, eventDigest, eventVersions, eventConsequence, eventActor string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(approval_ref,'') FROM workflow_overlap_resolutions`).Scan(&recordedRef); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(json_extract(payload,'$.approval_ref'),''),COALESCE(json_extract(payload,'$.approval_scope_json'),''),COALESCE(json_extract(payload,'$.approval_operation_digest'),''),COALESCE(json_extract(payload,'$.approval_versions_json'),''),COALESCE(json_extract(payload,'$.approval_consequence'),''),actor FROM domain_events WHERE kind='workflow.overlap_resolved'`).Scan(&eventRef, &eventScope, &eventDigest, &eventVersions, &eventConsequence, &eventActor); err != nil {
		t.Fatal(err)
	}
	if recordedRef != "" || eventRef != "" || eventScope != "" || eventDigest != "" || eventVersions != "" || eventConsequence != "" {
		t.Fatalf("the routine cross-Product event carries an approval binding: row=%q ref=%q scope=%q digest=%q versions=%q consequence=%q", recordedRef, eventRef, eventScope, eventDigest, eventVersions, eventConsequence)
	}
	if eventActor != grant.PrincipalRef {
		t.Fatalf("cross-Product compatible_with actor = %q, want the authenticated caller %q", eventActor, grant.PrincipalRef)
	}
	var snapshot string
	if err := s.DatabaseForTesting().QueryRow(`SELECT authorized_scope_snapshot FROM idempotency_records WHERE idempotency_key=?`, "routine-cross-product-compatible").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, `"durable_commit":true`) {
		t.Fatalf("cross-Product compatible_with committed a non-durable snapshot: %s", snapshot)
	}
	if !strings.Contains(snapshot, `"product_ids":["product-1","product-2"]`) {
		t.Fatalf("cross-Product compatible_with snapshot lost the derived Product set: %s", snapshot)
	}
	// The cached replay of the same signed cross-Product request answers
	// with the original result and consumes nothing again: the approval row
	// stays the single consumed one-use row, the routine event stays alone,
	// no endpoint version moves again, and no second receipt exists.
	replayed := routineOverlapApprovedDispatch(t, s, service, privateKey, env, challengeRef, digest, scope, versions, withApproval)
	if replayed.Outcome != OutcomeOK || replayed.Error != nil {
		t.Fatalf("cached cross-Product compatible_with replay outcome=%s error=%+v, want the original cached success", replayed.Outcome, replayed.Error)
	}
	if !replayed.Replayed {
		t.Fatalf("cached cross-Product compatible_with replay carries replayed=false, want the replayed marker")
	}
	if !sameResultPayload(t, approved.Result, replayed.Result) {
		t.Fatalf("cached cross-Product compatible_with replay result payload differs from the original: original=%s replayed=%s", approved.Result, replayed.Result)
	}
	if !sameChangedRefs(t, approved.ChangedRefs, replayed.ChangedRefs) {
		t.Fatalf("cached cross-Product compatible_with replay changed refs %+v differ from the original %+v", replayed.ChangedRefs, approved.ChangedRefs)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`); got != 1 {
		t.Fatalf("cached cross-Product compatible_with replay left %d approval rows, want the one consumed row", got)
	}
	if used := countRows(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approvals`); used != 1 {
		t.Fatalf("cached cross-Product compatible_with replay approval used_count=%d, want the single consumed use", used)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
		t.Fatalf("cached cross-Product compatible_with replay left %d resolutions, want the settled one", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM domain_events WHERE kind='workflow.overlap_resolved'`); got != 1 {
		t.Fatalf("cached cross-Product compatible_with replay left %d resolution events, want the settled one", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`); got != 1 {
		t.Fatalf("cached cross-Product compatible_with replay left %d idempotency receipts, want the original alone", got)
	}
	if got := workVersion(t, s, "work-1"); got != binding.fromVersion+1 {
		t.Fatalf("cached cross-Product compatible_with replay left work-1 at v%d, want the settled %d", got, binding.fromVersion+1)
	}
	if got := workVersion(t, s, "work-2"); got != binding.toVersion+1 {
		t.Fatalf("cached cross-Product compatible_with replay left work-2 at v%d, want the settled %d", got, binding.toVersion+1)
	}
}

// TestRoutineCoordinationCrossProductRoutineKindRefusesWithoutCrossScope:
// the same cross-Product routine request from a grant that covers both
// Products but holds no cross_scope capability refuses by name before any
// challenge, consumption, or Domain effect. No challenge row, approval row,
// resolution row, or resolution event exists, and no endpoint version moves.
func TestRoutineCoordinationCrossProductRoutineKindRefusesWithoutCrossScope(t *testing.T) {
	t.Parallel()
	s, service, _, _, env := crossProductRoutineCoordinationFixture(t, []Capability{"work_relate"})
	binding := routineOverlapExactBinding(t, s, store.ResolutionCompatibleWith)
	input := routineOverlapResolveInput(t, binding, "routine-cross-product-no-grant")
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
	if refused.Error == nil || refused.Error.Kind != "unauthorized" || !strings.Contains(refused.Error.Message, "cross_scope") {
		t.Fatalf("cross-Product routine request without cross_scope outcome=%s error=%+v, want unauthorized naming the capability", refused.Outcome, refused.Error)
	}
	for _, probe := range []struct{ label, query string }{
		{"challenge rows", `SELECT count(*) FROM agent_approval_challenges`},
		{"approval rows", `SELECT count(*) FROM agent_approvals`},
		{"resolutions", `SELECT count(*) FROM workflow_overlap_resolutions`},
		{"resolution events", `SELECT count(*) FROM domain_events WHERE kind='workflow.overlap_resolved'`},
	} {
		if got := countRows(t, s.DatabaseForTesting(), probe.query); got != 0 {
			t.Fatalf("the refused cross-Product request left %d %s", got, probe.label)
		}
	}
	if got := workVersion(t, s, "work-1"); got != binding.fromVersion {
		t.Fatalf("the refused request moved work-1 to v%d, want %d", got, binding.fromVersion)
	}
	if got := workVersion(t, s, "work-2"); got != binding.toVersion {
		t.Fatalf("the refused request moved work-2 to v%d, want %d", got, binding.toVersion)
	}
}

// TestRoutineCoordinationDestructiveKindWithEmptySelectedProductSettlesThroughAConsumedHostApproval:
// an ambient Project that belongs to exactly one Product admits an empty
// Product selection, and the bounded approval scope then omits product_id
// entirely. The destructive resolution still settles through the genuine
// challenge and the host-approved retry: exactly one consumed one-use
// approval, the exact binding preserved in the recorded row and event, the
// terminal and survivor lifecycles.
func TestRoutineCoordinationDestructiveKindWithEmptySelectedProductSettlesThroughAConsumedHostApproval(t *testing.T) {
	t.Parallel()
	s, service, privateKey, baseEnv := routineCoordinationFixture(t)
	env := baseEnv
	env.SelectedProductID = ""
	binding := routineOverlapExactBinding(t, s, store.ResolutionMergedInto)
	input := routineOverlapResolveInput(t, binding, "routine-destructive-empty-selection")
	challengeRef := routineOverlapChallenge(t, s, service, env, binding, input)
	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatal(err)
	}
	scope, versions := routineOverlapCrossScopeBindings(t, s, env, binding, []string{"product-1"})
	digest := mutationDigest("concord_work_relate", "resolve_overlap", env, withApproval)
	approved := routineOverlapApprovedDispatch(t, s, service, privateKey, env, challengeRef, digest, scope, versions, withApproval)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved destructive merged_into with an empty selected Product outcome=%s error=%+v, want the settled resolution", approved.Outcome, approved.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
		t.Fatalf("the approved empty-selection resolution left %d resolutions, want the settled one", got)
	}
	if got := workLifecycle(t, s, "work-1"); got != "superseded" {
		t.Fatalf("the destructive resolution left the terminal work %q, want superseded", got)
	}
	if got := workLifecycle(t, s, "work-2"); got != "in_progress" {
		t.Fatalf("the destructive resolution left the survivor %q, want in_progress", got)
	}
	if got := workVersion(t, s, "work-1"); got != binding.fromVersion+1 {
		t.Fatalf("the destructive resolution left work-1 at v%d, want the single step to %d", got, binding.fromVersion+1)
	}
	if got := workVersion(t, s, "work-2"); got != binding.toVersion+1 {
		t.Fatalf("the destructive resolution left work-2 at v%d, want the single step to %d", got, binding.toVersion+1)
	}
	routineOverlapConsumedApprovalWitness(t, s, "the empty-selection destructive resolution")
	var recordedRef, eventRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT approval_ref FROM workflow_overlap_resolutions`).Scan(&recordedRef); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.approval_ref') FROM domain_events WHERE kind='workflow.overlap_resolved'`).Scan(&eventRef); err != nil {
		t.Fatal(err)
	}
	if recordedRef == "" || recordedRef != eventRef || recordedRef == challengeRef {
		t.Fatalf("the empty-selection resolution persisted approval projection=%q event=%q challenge=%q", recordedRef, eventRef, challengeRef)
	}
}

// TestRoutineCoordinationDestructiveKindWithAnotherSelectedProductSettlesThroughAConsumedHostApproval:
// a destructive resolution whose envelope selects the Project's other
// Product is a legitimate cross-Product consent, not a Product-identity
// mismatch: the endpoints keep their shared footprint Product while the
// selected scope and the derived Product set cross it under the cross_scope
// grant. The genuine challenge and the host-approved retry settle with
// exactly one consumed one-use approval, the recorded binding carries the
// selected Product and no host scope field beyond the bounded keys.
func TestRoutineCoordinationDestructiveKindWithAnotherSelectedProductSettlesThroughAConsumedHostApproval(t *testing.T) {
	t.Parallel()
	s, service, _, privateKey, env := crossProductRoutineCoordinationFixture(t, []Capability{"work_relate", "cross_scope"})
	selectedEnv := env
	selectedEnv.SelectedProductID = "product-2"
	binding := routineOverlapExactBinding(t, s, store.ResolutionSupersedes)
	input := routineOverlapResolveInput(t, binding, "routine-destructive-selected-other")
	challengeRef := routineOverlapChallenge(t, s, service, selectedEnv, binding, input)
	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatal(err)
	}
	scope, versions := routineOverlapCrossScopeBindings(t, s, selectedEnv, binding, []string{"product-1", "product-2"})
	digest := mutationDigest("concord_work_relate", "resolve_overlap", selectedEnv, withApproval)
	approved := routineOverlapApprovedDispatch(t, s, service, privateKey, selectedEnv, challengeRef, digest, scope, versions, withApproval)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved destructive supersedes with another selected Product outcome=%s error=%+v, want the settled resolution", approved.Outcome, approved.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 1 {
		t.Fatalf("the approved selected-other resolution left %d resolutions, want the settled one", got)
	}
	if got := workLifecycle(t, s, "work-2"); got != "superseded" {
		t.Fatalf("the destructive resolution left the terminal work %q, want superseded", got)
	}
	if got := workLifecycle(t, s, "work-1"); got != "in_progress" {
		t.Fatalf("the destructive resolution left the survivor %q, want in_progress", got)
	}
	if got := workVersion(t, s, "work-1"); got != binding.fromVersion+1 {
		t.Fatalf("the destructive resolution left work-1 at v%d, want the single step to %d", got, binding.fromVersion+1)
	}
	if got := workVersion(t, s, "work-2"); got != binding.toVersion+1 {
		t.Fatalf("the destructive resolution left work-2 at v%d, want the single step to %d", got, binding.toVersion+1)
	}
	routineOverlapConsumedApprovalWitness(t, s, "the selected-other destructive resolution")
	var recordedScope string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(payload,'$.approval_scope_json') FROM domain_events WHERE kind='workflow.overlap_resolved'`).Scan(&recordedScope); err != nil {
		t.Fatal(err)
	}
	expectedScope, marshalErr := json.Marshal(scope)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if recordedScope != string(expectedScope) {
		t.Fatalf("recorded approval scope %s, want the exact bounded scope %s with no new host scope fields", recordedScope, expectedScope)
	}
}
