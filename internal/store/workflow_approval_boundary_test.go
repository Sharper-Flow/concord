package store

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// claimRetryWorktree records the dispatch fixture's active worktree claim
// through the owning worktree_created fold, so the retry dispatch admits
// against a genuinely claimed session worktree instead of a projection write.
func claimRetryWorktree(t *testing.T, s *Store, workID string) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	payload := marshalWorktreeCreated(version, WorktreeSetID(workID), "project-1", "claim:"+workID,
		WorktreeLocation{Branch: "change/claim", BaseSHA: strings.Repeat("a", 40), Path: t.TempDir()},
		worktreeFacts{branch: "change/claim", headSHA: strings.Repeat("a", 40), repositoryID: "repo-1"}, "", 0, 0)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: "approval-boundary-worktree-" + workID, Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: workID,
		Actor: "worker:test", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: payload,
	}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("record the retry worktree claim: %v", err)
	}
}

// TestWorkflowApprovalBoundary pins the consent boundary between approval
// classification and engine-owned recovery discovery: an action the pin
// omits still classifies from the canonical declaration the engine's
// recovery owner returns, under both policies, and an action neither owner
// declares keeps its typed refusal. Classification proves policy only: no
// assertion here claims admission or any host effect.
func TestWorkflowApprovalBoundary(t *testing.T) {
	t.Parallel()

	t.Run("engine-owned recovery declarations classify under both policies", func(t *testing.T) {
		t.Parallel()
		legacy := workflowFixtureDefinition(t, 1).Definition
		newPolicy := routineRecoveryPolicyDefinition(workflowFixtureDefinition(t, 1))
		declared := func(definition WorkflowDefinition, actionID string) (WorkflowActionDefinition, bool) {
			for _, action := range definition.ActionDefinitions {
				if action.ID == actionID {
					return action, true
				}
			}
			return WorkflowActionDefinition{}, false
		}

		// Derive the engine-owned recovery set from the engine owner itself;
		// its returned definitions carry the canonical metadata, so no
		// expectation below copies a handwritten list.
		candidates := map[string]bool{}
		for _, action := range BuiltinWorkflowRecoveryActionDefinitions() {
			candidates[action.ID] = true
		}
		for _, version := range []int64{1, 2} {
			for _, action := range workflowFixtureDefinition(t, version).Definition.ActionDefinitions {
				candidates[action.ID] = true
			}
		}
		engineOwned := make([]string, 0, len(candidates))
		for actionID := range candidates {
			if _, ok := workflowRecoveryActionDefinition(actionID); ok {
				engineOwned = append(engineOwned, actionID)
			}
		}
		sort.Strings(engineOwned)
		ownedByEngine := func(actionID string) bool {
			for _, owned := range engineOwned {
				if owned == actionID {
					return true
				}
			}
			return false
		}
		for _, action := range BuiltinWorkflowRecoveryActionDefinitions() {
			if !ownedByEngine(action.ID) {
				t.Fatalf("engine recovery discovery does not return the builtin entry %s; derived set %v", action.ID, engineOwned)
			}
		}
		for _, actionID := range []string{"record_verdict", "record_worker_failure"} {
			if !ownedByEngine(actionID) {
				t.Fatalf("engine recovery discovery does not return %s; derived set %v", actionID, engineOwned)
			}
		}
		if ownedByEngine("dispatch_worker") {
			t.Fatalf("engine recovery discovery claims dispatch_worker, want it outside the recovery set %v", engineOwned)
		}

		// The declared control is the one engine-owned entry the v1 pin
		// declares; the rest are genuinely absent. Nothing is removed from
		// the pin to build these sets.
		controlID := ""
		var absent []string
		for _, actionID := range engineOwned {
			if _, isDeclared := declared(legacy, actionID); isDeclared {
				controlID = actionID
				continue
			}
			absent = append(absent, actionID)
		}
		if controlID != "record_verdict" {
			t.Fatalf("derived declared control %q, want record_verdict, the only engine-owned recovery action the v1 pin declares", controlID)
		}
		if len(absent) == 0 {
			t.Fatal("derived an empty absent recovery set; the boundary witness would be vacuous")
		}

		// Both policies answer every pin-absent engine-owned entry:
		// consequence_v1 classifies routine record-keeping, and legacy
		// echoes the consent the canonical engine owner declares.
		for _, actionID := range absent {
			result, err := ClassifyWorkflowApproval(newPolicy, actionID, WorkflowApprovalFacts{})
			if err != nil {
				t.Errorf("consequence_v1 classification of engine-owned recovery action %s refused: %v", actionID, err)
				continue
			}
			if result.Policy != WorkflowApprovalPolicyConsequenceV1 || result.Class != WorkflowApprovalClassRoutine || result.Consent != WorkflowConsentNone {
				t.Errorf("consequence_v1 engine recovery %s classified policy %q class %q consent %q, want consequence_v1 routine/none", actionID, result.Policy, result.Class, result.Consent)
			}
			owner, owned := workflowRecoveryActionDefinition(actionID)
			if !owned {
				t.Fatalf("engine recovery discovery lost %s", actionID)
			}
			legacyResult, err := ClassifyWorkflowApproval(legacy, actionID, WorkflowApprovalFacts{})
			if err != nil {
				t.Errorf("legacy classification of engine-owned recovery action %s refused: %v", actionID, err)
				continue
			}
			if legacyResult.Policy != "" || legacyResult.Class != WorkflowApprovalClassLegacy {
				t.Errorf("legacy engine recovery %s classified policy %q class %q, want absence/legacy", actionID, legacyResult.Policy, legacyResult.Class)
			}
			if legacyResult.Consent != WorkflowApprovalConsent(owner.Approval) {
				t.Errorf("legacy engine recovery %s consent = %q, want the canonical owner %q", actionID, legacyResult.Consent, owner.Approval)
			}
		}

		// Declared control: the same new-policy copy classifies the pinned
		// action through the ordinary declared lookup.
		control, err := ClassifyWorkflowApproval(newPolicy, controlID, WorkflowApprovalFacts{})
		if err != nil {
			t.Fatalf("consequence_v1 classification of declared %s refused: %v", controlID, err)
		}
		if control.Class != WorkflowApprovalClassRoutine || control.Consent != WorkflowConsentNone {
			t.Fatalf("consequence_v1 %s classified %s/%s, want routine/none", controlID, control.Class, control.Consent)
		}

		// Legacy parity on the declared control.
		engineOwner, ok := workflowRecoveryActionDefinition(controlID)
		if !ok {
			t.Fatalf("engine recovery discovery lost %s", controlID)
		}
		declaredControl, _ := declared(legacy, controlID)
		legacyResult, err := ClassifyWorkflowApproval(legacy, controlID, WorkflowApprovalFacts{})
		if err != nil {
			t.Fatalf("legacy classification of declared %s refused: %v", controlID, err)
		}
		if legacyResult.Class != WorkflowApprovalClassLegacy || legacyResult.Policy != "" {
			t.Fatalf("legacy %s classified policy %q class %q, want absence/legacy", controlID, legacyResult.Policy, legacyResult.Class)
		}
		if legacyResult.Consent != WorkflowApprovalConsent(declaredControl.Approval) || legacyResult.Consent != WorkflowApprovalConsent(engineOwner.Approval) {
			t.Fatalf("legacy %s consent = %q, want the declared %q the engine owner pins as %q", controlID, legacyResult.Consent, declaredControl.Approval, engineOwner.Approval)
		}
		override, err := ClassifyWorkflowApproval(legacy, controlID, WorkflowApprovalFacts{OperatorRequired: true})
		if err != nil {
			t.Fatalf("legacy operator-required classification of %s refused: %v", controlID, err)
		}
		if override.Consent != WorkflowConsentRequired {
			t.Fatalf("legacy operator-required %s consent = %q, want the executor override to required", controlID, override.Consent)
		}

		// The v2 pin declares record_worker_failure.
		legacyWorker := workflowFixtureDefinition(t, 2).Definition
		workerOwner, ok := workflowRecoveryActionDefinition("record_worker_failure")
		if !ok {
			t.Fatal("engine recovery discovery lost record_worker_failure")
		}
		workerResult, err := ClassifyWorkflowApproval(legacyWorker, "record_worker_failure", WorkflowApprovalFacts{})
		if err != nil {
			t.Fatalf("legacy classification of declared record_worker_failure refused: %v", err)
		}
		declaredWorker, _ := declared(legacyWorker, "record_worker_failure")
		if workerResult.Consent != WorkflowApprovalConsent(declaredWorker.Approval) || workerResult.Consent != WorkflowApprovalConsent(workerOwner.Approval) {
			t.Fatalf("legacy record_worker_failure consent = %q, want the declared %q the engine owner pins as %q", workerResult.Consent, declaredWorker.Approval, workerOwner.Approval)
		}

		// An action neither the pin declares nor the engine owns keeps the
		// typed refusal under both policies.
		const unknownAction = "record_alignment"
		if _, isOwned := workflowRecoveryActionDefinition(unknownAction); isOwned {
			t.Fatalf("engine recovery discovery claims %s, want it outside pin and engine", unknownAction)
		}
		if _, isDeclared := declared(legacy, unknownAction); isDeclared {
			t.Fatalf("the v1 pin declares %s, want it undeclared for the refusal control", unknownAction)
		}
		for name, definition := range map[string]WorkflowDefinition{"legacy": legacy, "consequence_v1": newPolicy} {
			_, err := ClassifyWorkflowApproval(definition, unknownAction, WorkflowApprovalFacts{})
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidInput || !strings.Contains(failure.Detail, "is not declared by definition") {
				t.Fatalf("%s classification of undeclared %s returned %v, want the typed undeclared refusal", name, unknownAction, err)
			}
		}
	})

	// The store boundary read builds typed facts from real store state and
	// classifies through the one pure classifier. It proves facts and policy
	// only: no assertion here claims admission, consent consumption, or any
	// host effect, and every read must leave the unchanged item unchanged.
	t.Run("boundary read pairs the pool and transaction forms on unchanged items", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		refusalKind := func(err error) FailureKind {
			var failure *Failure
			if failureAs(err, &failure) {
				return failure.Kind
			}
			return ""
		}

		const workID = "approval-boundary-read"
		s, registry, definition, owner, actors := seedApprovalItemAtAcceptance(t, workID, []string{})
		defer s.Close()
		eventsBefore := approvalEventCount(t, s, workID)
		confirmed := WorkflowActionPreflightRequest{WorkID: workID, ActionID: "confirm_premise", Actor: owner}
		state, err := ReadWorkflowApprovalBoundary(ctx, s, registry, confirmed)
		if err != nil {
			t.Fatalf("pool boundary read refused: %v", err)
		}
		registered, ok := registry.Lookup(definition.Ref, definition.Version)
		if !ok {
			t.Fatal("the seeded registry lost the pinned definition")
		}
		if state.Definition.Digest != registered.Digest || state.Definition.Definition.Ref != definition.Ref || state.Definition.Definition.Version != definition.Version {
			t.Fatalf("boundary pin = %s v%d digest %s, want the verified registered pin", state.Definition.Definition.Ref, state.Definition.Definition.Version, state.Definition.Digest)
		}
		active, err := s.ActiveWorkflowContract(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		var activeVersions []int64
		err = s.readSnapshot(ctx, func(q queryer) error {
			var innerErr error
			activeVersions, innerErr = activeWorkflowContractVersions(ctx, q, workID)
			return innerErr
		})
		if err != nil {
			t.Fatal(err)
		}
		workVersion := readWorkVersion(t, s, workID)
		if state.WorkVersion != workVersion || state.ContractVersion != active.Version || !reflect.DeepEqual(state.ActiveContractVersions, activeVersions) {
			t.Fatalf("boundary versions = work %d contract %d active %v, want work %d contract %d active %v", state.WorkVersion, state.ContractVersion, state.ActiveContractVersions, workVersion, active.Version, activeVersions)
		}
		if !state.DecisionDomainsPresent || state.DecisionDomains == nil || len(state.DecisionDomains) != 0 {
			t.Fatalf("engineering decision fact = present %v domains %v, want [] present", state.DecisionDomainsPresent, state.DecisionDomains)
		}
		// Executing the work requires no operator evaluation: no executing
		// actor distinctness exists beyond the worker's own result (CD-0242 D5).
		if state.EvaluatorAuthority != WorkflowIndependentEvaluator {
			t.Fatalf("executing coordinator authority = %q, want independent", state.EvaluatorAuthority)
		}
		if state.Policy.Policy != WorkflowApprovalPolicyConsequenceV1 || state.Policy.Class != WorkflowApprovalClassDecision || state.Policy.Consent != WorkflowConsentNone {
			t.Fatalf("engineering confirmation classified %s/%s/%s, want consequence_v1 decision/none", state.Policy.Policy, state.Policy.Class, state.Policy.Consent)
		}

		var txState WorkflowApprovalBoundaryState
		if err := s.Transact(ctx, func(tx *Transaction) error {
			var txErr error
			txState, txErr = ReadWorkflowApprovalBoundaryTx(ctx, tx, registry, confirmed)
			return txErr
		}); err != nil {
			t.Fatalf("transaction boundary read refused: %v", err)
		}
		if !reflect.DeepEqual(state, txState) {
			t.Fatalf("transaction boundary state = %+v, want the pool state %+v", txState, state)
		}

		reviewer, err := ReadWorkflowApprovalBoundary(ctx, s, registry, WorkflowActionPreflightRequest{WorkID: workID, ActionID: "confirm_premise", Actor: actors.reviewer})
		if err != nil {
			t.Fatalf("reviewer boundary read refused: %v", err)
		}
		if reviewer.EvaluatorAuthority != WorkflowIndependentEvaluator || reviewer.Policy.Consent != WorkflowConsentNone {
			t.Fatalf("independent evaluator read authority %q consent %q, want independent and no consent", reviewer.EvaluatorAuthority, reviewer.Policy.Consent)
		}

		proposed, err := ReadWorkflowApprovalBoundary(ctx, s, registry, WorkflowActionPreflightRequest{WorkID: workID, ActionID: "approve_contract", Actor: owner, Payload: json.RawMessage(`{"decision_domains":["product"]}`)})
		if err != nil {
			t.Fatalf("proposed-fact boundary read refused: %v", err)
		}
		if proposed.Policy.Class != WorkflowApprovalClassDecision || proposed.Policy.Consent != WorkflowConsentRequired {
			t.Fatalf("proposed product contract classified %s/%s, want decision/required", proposed.Policy.Class, proposed.Policy.Consent)
		}
		engineering, err := ReadWorkflowApprovalBoundary(ctx, s, registry, WorkflowActionPreflightRequest{WorkID: workID, ActionID: "approve_contract", Actor: owner, Payload: json.RawMessage(`{"decision_domains":[]}`)})
		if err != nil {
			t.Fatalf("engineering proposal read refused: %v", err)
		}
		if engineering.Policy.Consent != WorkflowConsentNone {
			t.Fatalf("engineering proposal consent = %q, want none", engineering.Policy.Consent)
		}
		if _, err := ReadWorkflowApprovalBoundary(ctx, s, registry, WorkflowActionPreflightRequest{WorkID: workID, ActionID: "approve_contract", Actor: owner, Payload: json.RawMessage(`{}`)}); refusalKind(err) != KindInvalidPayload {
			t.Fatalf("proposal without its submitted fact = %v, want the typed invalid_payload refusal", err)
		}

		// The nil registry resolves the builtin convention, so the builtin pin
		// reads the same boundary state; an empty registry resolves no pin and
		// refuses through the typed pin failure.
		builtin, err := ReadWorkflowApprovalBoundary(ctx, s, nil, confirmed)
		if err != nil {
			t.Fatalf("nil-registry read refused: %v", err)
		}
		if !reflect.DeepEqual(builtin, state) {
			t.Fatalf("nil-registry state = %+v, want the pool state %+v", builtin, state)
		}
		if _, err := ReadWorkflowApprovalBoundary(ctx, s, NewWorkflowDefinitionRegistry(), confirmed); refusalKind(err) != KindInvariantViolation {
			t.Fatalf("empty-registry read = %v, want the typed pin refusal", err)
		}

		const decisionID = "approval-boundary-decision"
		decisionStore, decisionRegistry, _, decisionOwner, _ := seedApprovalItemAtAcceptance(t, decisionID, []string{"product"})
		defer decisionStore.Close()
		decision, err := ReadWorkflowApprovalBoundary(ctx, decisionStore, decisionRegistry, WorkflowActionPreflightRequest{WorkID: decisionID, ActionID: "confirm_premise", Actor: decisionOwner})
		if err != nil {
			t.Fatalf("decision-bearing boundary read refused: %v", err)
		}
		if !decision.DecisionDomainsPresent || !reflect.DeepEqual(decision.DecisionDomains, []string{"product"}) {
			t.Fatalf("decision fact = present %v domains %v, want [product] present", decision.DecisionDomainsPresent, decision.DecisionDomains)
		}
		if decision.Policy.Consent != WorkflowConsentRequired {
			t.Fatalf("decision-bearing confirmation consent = %q, want required", decision.Policy.Consent)
		}

		// The carried-forward pin reads the legacy NULL contract as the
		// absent fact, and the confirmation classification refuses it.
		const absentID = "approval-boundary-absent"
		absentStore, absentRegistry, absentOwner := seedApprovalAbsentFactAtAcceptance(t, absentID)
		defer absentStore.Close()
		absentState, err := ReadWorkflowApprovalBoundary(ctx, absentStore, absentRegistry, WorkflowActionPreflightRequest{WorkID: absentID, ActionID: "record_verdict", Actor: absentOwner})
		if err != nil {
			t.Fatalf("absent-fact boundary read refused: %v", err)
		}
		if absentState.DecisionDomainsPresent || absentState.DecisionDomains != nil {
			t.Fatalf("legacy NULL absence = present %v domains %v, want absent nil", absentState.DecisionDomainsPresent, absentState.DecisionDomains)
		}
		if absentState.Policy.Class != WorkflowApprovalClassRoutine || absentState.Policy.Consent != WorkflowConsentNone {
			t.Fatalf("absent-fact record_verdict classified %s/%s, want routine/none", absentState.Policy.Class, absentState.Policy.Consent)
		}
		if _, err := ReadWorkflowApprovalBoundary(ctx, absentStore, absentRegistry, WorkflowActionPreflightRequest{WorkID: absentID, ActionID: "confirm_premise", Actor: absentOwner}); refusalKind(err) != KindInvalidInput {
			t.Fatalf("absent-fact confirmation = %v, want the typed absence refusal", err)
		}

		// A terminal failure does not add an approval requirement.
		const retryID = "approval-boundary-retry"
		retryFixture, attempt, _ := seedCurrentRepairWorker(t, retryID)
		retryStore := retryFixture.store
		defer retryStore.Close()
		worker := retryFixture.owner
		failed := s2WorkerFailedAttributedEvent(retryID, attempt, "failed-"+retryID, preferredModelForLane(laneForID(t)), WorkerFailureWorkerError, "the lane failed before a judgeable result existed", time.Unix(30, 0).UTC())
		if err := s2WorkerFailedLive(t, retryStore, failed); err != nil {
			t.Fatalf("failed-retry terminal: %v", err)
		}
		retry, err := ReadWorkflowApprovalBoundary(ctx, retryStore, nil, WorkflowActionPreflightRequest{WorkID: retryID, ActionID: "dispatch_worker", Actor: worker})
		if err != nil {
			t.Fatalf("failed-retry boundary read refused: %v", err)
		}
		if retry.EvaluatorAuthority != WorkflowIndependentEvaluator {
			t.Fatalf("dispatching coordinator authority = %q, want independent", retry.EvaluatorAuthority)
		}
		if retry.Policy.Consent != WorkflowConsentNone {
			t.Fatalf("failed retry added consent = %q, want none", retry.Policy.Consent)
		}

		if approvalEventCount(t, s, workID) != eventsBefore || readWorkVersion(t, s, workID) != workVersion {
			t.Fatal("the boundary reads moved the unchanged item")
		}
		if _, err := ReadWorkflowApprovalBoundary(ctx, s, registry, confirmed); err != nil {
			t.Fatalf("the store stopped serving after the balanced transaction read: %v", err)
		}
	})

	// The caller registry reaches the raw owners end to end: the public
	// initialization and repin facades verify the pinned entry through the
	// caller's registry and bind the same registry to the fold scope, the
	// selection facade and the opaque-Tx mutation pin verify through it, and
	// every missing, wrong, or drifted source refuses closed with the item
	// untouched. The nil default keeps the builtin convention on both
	// generations of the API.
	t.Run("caller registry reaches initialization, repin, selection, and the opaque work pin", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()

		definition := approvalRouteDefinition(t)
		// A family has one current definition (CD-0242 D14): the probe
		// initializes on the lower version while it is current, and the
		// higher version registers just before the repin onto it.
		registry, registered := newResolutionRegistry(t, definition)
		higher := definition
		higher.Version = definition.Version + 1

		foldGuardRows := func(t *testing.T, s *Store) int {
			t.Helper()
			var rows int
			if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			return rows
		}
		ownerFor := func(workID string) WorkflowActor {
			return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
		}
		initialize := func(t *testing.T, s *Store, workID string, entry RegisteredDefinition, reg DefinitionRegistry) error {
			t.Helper()
			return s.Transact(ctx, func(tx *Transaction) error {
				return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: entry, Actor: ownerFor(workID), Now: time.Unix(5, 0).UTC(), Registry: reg})
			})
		}

		// Fail-closed controls first: the same probe item refuses the
		// builtin (missing) registry, an empty (wrong) registry, and a
		// drifted digest, then initializes through the correct registry —
		// which proves every refusal left the item untouched.
		const probe = "approval-registry-probe"
		s := openTemp(t)
		seedWork(t, s, probe)
		seedWorkflowLaw(t, s)
		probeVersion := readWorkVersion(t, s, probe)
		probeEvents := approvalEventCount(t, s, probe)
		expectPinRefusal := func(name string, err error) {
			t.Helper()
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvariantViolation {
				t.Fatalf("%s initialization = %v, want the typed pin refusal", name, err)
			}
			if readWorkVersion(t, s, probe) != probeVersion || approvalEventCount(t, s, probe) != probeEvents {
				t.Fatalf("%s refusal moved the probe item", name)
			}
			if rows := foldGuardRows(t, s); rows != 0 {
				t.Fatalf("%s refusal left %d fold_guard rows", name, rows)
			}
		}
		expectPinRefusal("missing (builtin)", initialize(t, s, probe, registered, nil))
		expectPinRefusal("wrong (empty)", initialize(t, s, probe, registered, NewWorkflowDefinitionRegistry()))
		drifted := registered
		drifted.Digest = "sha256:" + strings.Repeat("0", 64)
		expectPinRefusal("drifted digest", initialize(t, s, probe, drifted, registry))
		if err := initialize(t, s, probe, registered, registry); err != nil {
			t.Fatalf("caller-registry initialization refused after the fail-closed controls: %v", err)
		}
		var probeRef, probeStep string
		var probeDefinitionVersion int64
		var probeDigest string
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest,current_step FROM workflow_instances WHERE work_id=?`, probe).Scan(&probeRef, &probeDefinitionVersion, &probeDigest, &probeStep); err != nil {
			t.Fatal(err)
		}
		if probeRef != registered.Definition.Ref || probeDefinitionVersion != registered.Definition.Version || probeDigest != registered.Digest {
			t.Fatalf("probe pin = %s v%d %s, want the verified registered entry", probeRef, probeDefinitionVersion, probeDigest)
		}
		if rows := foldGuardRows(t, s); rows != 0 {
			t.Fatalf("caller-registry initialization left %d fold_guard rows", rows)
		}

		higherRegistered, err := registry.Register(higher)
		if err != nil {
			t.Fatalf("the higher local definition refused registration: %v", err)
		}

		// Repin carries the instance forward onto the higher same-ref
		// version through the same registry, keeping the current step; the
		// builtin (missing) registry refuses the same repin closed.
		if err := s.Transact(ctx, func(tx *Transaction) error {
			return RepinWorkflowTx(ctx, tx, WorkflowRepinRequest{WorkID: probe, EventID: "approval-registry-repin:" + probe, Definition: higherRegistered, Actor: ownerFor(probe), Now: time.Unix(6, 0).UTC(), Registry: registry})
		}); err != nil {
			t.Fatalf("caller-registry repin refused: %v", err)
		}
		var repinVersion int64
		var repinRef, repinStep, repinDigest string
		var repinDefinitionVersion int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest,current_step,(SELECT version FROM work_items WHERE id=workflow_instances.work_id) FROM workflow_instances WHERE work_id=?`, probe).Scan(&repinRef, &repinDefinitionVersion, &repinDigest, &repinStep, &repinVersion); err != nil {
			t.Fatal(err)
		}
		if repinRef != higherRegistered.Definition.Ref || repinDefinitionVersion != higherRegistered.Definition.Version || repinDigest != higherRegistered.Digest {
			t.Fatalf("repinned pin = %s v%d %s, want the higher registered entry", repinRef, repinDefinitionVersion, repinDigest)
		}
		if repinStep != probeStep {
			t.Fatalf("carry-forward repin moved the step from %s to %s", probeStep, repinStep)
		}
		repinVersionBefore := repinVersion
		repinEvents := approvalEventCount(t, s, probe)
		if err := s.Transact(ctx, func(tx *Transaction) error {
			return RepinWorkflowTx(ctx, tx, WorkflowRepinRequest{WorkID: probe, EventID: "approval-registry-repin-missing:" + probe, Definition: higherRegistered, Actor: ownerFor(probe), Now: time.Unix(7, 0).UTC(), Registry: nil})
		}); err == nil {
			t.Fatal("missing-registry repin was accepted")
		} else {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation {
				t.Fatalf("missing-registry repin = %v, want the typed pin refusal", err)
			}
		}
		if readWorkVersion(t, s, probe) != repinVersionBefore || approvalEventCount(t, s, probe) != repinEvents || foldGuardRows(t, s) != 0 {
			t.Fatal("the refused repin moved the probe item")
		}

		// The route item drives the local pin to the acceptance step through
		// the genuine registry-threaded folds, then the selection facade and
		// the opaque-Tx mutation pin answer through the caller registry.
		// The route item runs on its own registry, where the lower version is
		// the family's one current definition.
		const route = "approval-registry-route"
		registry, registered = newResolutionRegistry(t, definition)
		seedWork(t, s, route)
		if err := initialize(t, s, route, registered, registry); err != nil {
			t.Fatalf("route initialization refused: %v", err)
		}
		recordResolutionActors(t, s, route)
		advanceResolutionToExecution(t, s, registry, route, ownerFor(route), []string{})
		advanceResolutionDelivery(t, s, registry, route, ownerFor(route))
		seedResolutionInvestigation(t, s, route)
		routeVersion := readWorkVersion(t, s, route)
		routeEvents := approvalEventCount(t, s, route)
		selection := WorkflowActionPreflightRequest{WorkID: route, ActionID: "confirm_premise", SelectedChoice: "confirm", ExpectedVersion: routeVersion}
		if err := ValidateWorkflowOperatorSelectionForRegistry(ctx, s, registry, selection); err != nil {
			t.Fatalf("engineering confirm selection refused under the caller registry: %v", err)
		}
		var selectionFailure *Failure
		if err := ValidateWorkflowOperatorSelectionForRegistry(ctx, s, NewWorkflowDefinitionRegistry(), selection); err == nil || !failureAs(err, &selectionFailure) || selectionFailure.Kind != KindInvariantViolation {
			t.Fatalf("wrong-registry selection = %v, want the typed pin refusal", err)
		}
		var wrapperRefusal *Failure
		if err := ValidateWorkflowOperatorSelectionForRegistry(ctx, s, nil, selection); err == nil || !failureAs(err, &wrapperRefusal) || wrapperRefusal.Kind != KindInvariantViolation {
			t.Fatalf("the nil-registry builtin selection = %v, want the typed pin refusal", err)
		}
		if readWorkVersion(t, s, route) != routeVersion || approvalEventCount(t, s, route) != routeEvents || foldGuardRows(t, s) != 0 {
			t.Fatal("the selection reads moved the route item")
		}

		readRoutePin := func(reg DefinitionRegistry) (WorkPin, bool, error) {
			var pin WorkPin
			var available bool
			txErr := s.Transact(ctx, func(tx *Transaction) error {
				var err error
				pin, available, err = ReadMutationWorkPinTxForRegistry(ctx, tx, reg, route, nil)
				return err
			})
			return pin, available, txErr
		}
		pin, available, err := readRoutePin(registry)
		if err != nil || !available {
			t.Fatalf("caller-registry mutation pin = available %v, %v, want the readable pin", available, err)
		}
		if pin.WorkflowType != registered.Definition.Ref || pin.WorkflowDefinitionVersion == nil || *pin.WorkflowDefinitionVersion != registered.Definition.Version || pin.WorkflowDefinitionDigest == nil || *pin.WorkflowDefinitionDigest != registered.Digest {
			t.Fatalf("caller-registry pin identity = %s v%v %v, want the verified registered entry", pin.WorkflowType, pin.WorkflowDefinitionVersion, pin.WorkflowDefinitionDigest)
		}
		parity, _, err := readRoutePin(registry)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pin, parity) {
			t.Fatalf("second mutation pin = %+v, want %+v", parity, pin)
		}
		var wrongPinRefusal *Failure
		_, _, wrongPinErr := readRoutePin(NewWorkflowDefinitionRegistry())
		if wrongPinErr == nil || !failureAs(wrongPinErr, &wrongPinRefusal) || wrongPinRefusal.Kind != KindInvariantViolation {
			t.Fatalf("wrong-registry mutation pin = %v, want the typed pin refusal", wrongPinErr)
		}
		var builtinConvention *Failure
		builtinConventionErr := s.Transact(ctx, func(tx *Transaction) error {
			_, _, err := ReadMutationWorkPinTxForRegistry(ctx, tx, nil, route, nil)
			return err
		})
		// The builtin registry holds no current definition of the local
		// family, so the pin refuses as unregistered (CD-0242 D14).
		if builtinConventionErr == nil || !failureAs(builtinConventionErr, &builtinConvention) || builtinConvention.Kind != KindInvariantViolation || !strings.Contains(builtinConvention.Detail, "current workflow definition is unavailable") {
			t.Fatalf("builtin-convenience mutation pin = %v, want the typed unregistered refusal", builtinConventionErr)
		}

		// The nil default keeps the builtin convention: the same facade
		// initializes a builtin pin exactly as production capture does, and
		// the old public mutation pin reads it.
		const builtin = "approval-registry-builtin"
		seedWork(t, s, builtin)
		builtinEntry, err := LiveWorkflowDefinitionForRef("workflow.implementation")
		if err != nil {
			t.Fatalf("the builtin implementation definition refused resolution: %v", err)
		}
		if err := initialize(t, s, builtin, builtinEntry, nil); err != nil {
			t.Fatalf("nil-registry builtin initialization refused: %v", err)
		}
		builtinPin, available, err := func() (WorkPin, bool, error) {
			var pin WorkPin
			var ready bool
			txErr := s.Transact(ctx, func(tx *Transaction) error {
				var err error
				pin, ready, err = ReadMutationWorkPinTxForRegistry(ctx, tx, nil, builtin, nil)
				return err
			})
			return pin, ready, txErr
		}()
		if err != nil || !available {
			t.Fatalf("builtin mutation pin = available %v, %v, want the readable pin", available, err)
		}
		if builtinPin.WorkflowDefinitionDigest == nil || *builtinPin.WorkflowDefinitionDigest != builtinEntry.Digest {
			t.Fatalf("builtin pin digest = %v, want the builtin entry %s", builtinPin.WorkflowDefinitionDigest, builtinEntry.Digest)
		}
	})

	// The duplicate-active state is real only when the fold created it: a
	// recorded approval rides the historical log and RebuildFromLog replays
	// it, exactly as the contract-authority recipe does. The boundary stays
	// typed on both read forms — the supersede recovery reads the ordered
	// plural set, an ordinary action keeps the singular refusal, and the
	// work pin stays readable with the one recovery route — and every read
	// leaves the item unchanged.
	t.Run("duplicate-active historical replay keeps the boundary and the pin typed", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		const workID = "approval-boundary-duplicate-active"
		s, owner, reviewer := seedItemAtAcceptance(t, workID)
		defer s.Close()
		ownerRef, err := WorkflowActorRef(owner)
		if err != nil {
			t.Fatal(err)
		}
		version := readWorkVersion(t, s, workID)
		legacyApproval, marshalErr := json.Marshal(map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"contract_version": 2, "premise": "recorded duplicate approval", "outcome_kind": "check",
			"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:duplicate", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
			"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
			"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
			"approval-boundary-duplicate-approval", WorkflowContractApproved, SubjectWorkItem, workID, ownerRef, "2026-09-10T00:00:00Z", 2, legacyApproval); err != nil {
			t.Fatal(err)
		}
		if err := RebuildFromLog(ctx, s); err != nil {
			t.Fatalf("rebuild the duplicate-active history: %v", err)
		}
		var activeVersions []int64
		err = s.readSnapshot(ctx, func(q queryer) error {
			var innerErr error
			activeVersions, innerErr = activeWorkflowContractVersions(ctx, q, workID)
			return innerErr
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(activeVersions, []int64{1, 2}) {
			t.Fatalf("active contract versions = %v, want [1 2] with highest 2", activeVersions)
		}
		replayedVersion := readWorkVersion(t, s, workID)
		eventsBefore := approvalEventCount(t, s, workID)

		request := WorkflowActionPreflightRequest{WorkID: workID, ActionID: "supersede_contract", Actor: owner}
		supersede, err := ReadWorkflowApprovalBoundary(ctx, s, nil, request)
		if err != nil {
			t.Fatalf("supersede boundary read refused: %v", err)
		}
		if supersede.ContractVersion != 2 || !reflect.DeepEqual(supersede.ActiveContractVersions, []int64{1, 2}) {
			t.Fatalf("supersede boundary contract %d active %v, want 2 over [1 2]", supersede.ContractVersion, supersede.ActiveContractVersions)
		}
		if supersede.Policy.Consent != WorkflowConsentRequired {
			t.Fatalf("supersede recovery consent = %q, want required", supersede.Policy.Consent)
		}
		var txSupersede WorkflowApprovalBoundaryState
		if err := s.Transact(ctx, func(tx *Transaction) error {
			var txErr error
			txSupersede, txErr = ReadWorkflowApprovalBoundaryTx(ctx, tx, nil, request)
			return txErr
		}); err != nil {
			t.Fatalf("supersede transaction boundary read refused: %v", err)
		}
		if !reflect.DeepEqual(supersede, txSupersede) {
			t.Fatalf("transaction boundary state = %+v, want the pool state %+v", txSupersede, supersede)
		}

		refusalKind := func(err error) FailureKind {
			var failure *Failure
			if failureAs(err, &failure) {
				return failure.Kind
			}
			return ""
		}
		verdict := WorkflowActionPreflightRequest{WorkID: workID, ActionID: "record_verdict", Actor: reviewer}
		var ambiguity *Failure
		if _, err := ReadWorkflowApprovalBoundary(ctx, s, nil, verdict); err == nil || !failureAs(err, &ambiguity) || ambiguity.Kind != KindInvariantViolation || !strings.Contains(ambiguity.Detail, "multiple active contracts") {
			t.Fatalf("ordinary boundary read = %v, want the typed duplicate-active refusal", err)
		}
		if err := s.Transact(ctx, func(tx *Transaction) error {
			_, txErr := ReadWorkflowApprovalBoundaryTx(ctx, tx, nil, verdict)
			return txErr
		}); refusalKind(err) != KindInvariantViolation {
			t.Fatalf("ordinary transaction boundary read = %v, want the typed duplicate-active refusal", err)
		}

		readPin := func() (WorkPin, bool, error) {
			var pin WorkPin
			var available bool
			txErr := s.Transact(ctx, func(tx *Transaction) error {
				var err error
				pin, available, err = ReadMutationWorkPinTxForRegistry(ctx, tx, nil, workID, nil)
				return err
			})
			return pin, available, txErr
		}
		pin, available, err := readPin()
		if err != nil || !available {
			t.Fatalf("duplicate-active mutation pin = available %v, %v, want the readable pin", available, err)
		}
		if len(pin.NextValidIntents) != 1 || pin.NextValidIntents[0].ActionID != "supersede_contract" || pin.NextValidIntents[0].ReasonCode != "operator_contract_correction" {
			t.Fatalf("duplicate-active intents = %+v, want only the operator_contract_correction recovery", pin.NextValidIntents)
		}
		poolPin, err := ReadWorkPin(ctx, s, workID)
		if err != nil {
			t.Fatalf("duplicate-active pool pin read refused: %v", err)
		}
		if !reflect.DeepEqual(pin, poolPin) {
			t.Fatalf("pool pin = %+v, want the opaque-Tx pin %+v", poolPin, pin)
		}
		parity, _, err := readPin()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pin, parity) {
			t.Fatalf("second mutation pin = %+v, want %+v", parity, pin)
		}
		if approvalEventCount(t, s, workID) != eventsBefore || readWorkVersion(t, s, workID) != replayedVersion {
			t.Fatal("the duplicate-active reads moved the unchanged item")
		}
	})
}
