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
	version := verdictItemVersion(t, s, workID)
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
		newPolicy := routineRecoveryPolicyDefinition(t, workflowFixtureRef, 1)
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

		// The v2 pin declares record_worker_failure and dispatch_worker, so
		// legacy parity and the failed-retry upgrade extend there.
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
		retry, err := ClassifyWorkflowApproval(legacyWorker, "dispatch_worker", WorkflowApprovalFacts{FailedRetryBelowWall: true})
		if err != nil {
			t.Fatalf("legacy failed-retry classification of dispatch_worker refused: %v", err)
		}
		if retry.Consent != WorkflowConsentRequired {
			t.Fatalf("legacy failed-retry dispatch_worker consent = %q, want required", retry.Consent)
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
		activeVersions, err := s.ActiveWorkflowContractVersions(ctx, workID)
		if err != nil {
			t.Fatal(err)
		}
		workVersion := verdictItemVersion(t, s, workID)
		if state.WorkVersion != workVersion || state.ContractVersion != active.Version || !reflect.DeepEqual(state.ActiveContractVersions, activeVersions) {
			t.Fatalf("boundary versions = work %d contract %d active %v, want work %d contract %d active %v", state.WorkVersion, state.ContractVersion, state.ActiveContractVersions, workVersion, active.Version, activeVersions)
		}
		if !state.DecisionDomainsPresent || state.DecisionDomains == nil || len(state.DecisionDomains) != 0 {
			t.Fatalf("engineering decision fact = present %v domains %v, want [] present", state.DecisionDomainsPresent, state.DecisionDomains)
		}
		if state.EvaluatorAuthority != WorkflowOperatorRequired {
			t.Fatalf("executing coordinator authority = %q, want operator_required", state.EvaluatorAuthority)
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

		// The nil registry resolves the builtin convention, so the local pin
		// refuses through the typed pin failure; an empty registry refuses
		// the same way.
		if _, err := ReadWorkflowApprovalBoundary(ctx, s, nil, confirmed); refusalKind(err) != KindInvariantViolation {
			t.Fatalf("nil-registry read = %v, want the typed pin refusal", err)
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

		// A genuine failed retry below the wall binds its exact identity, and
		// the legacy pin upgrades the dispatch consent from it.
		const retryID = "approval-boundary-retry"
		retryStore, _, _, _ := seedOldDefinitionWorker(t, retryID)
		defer retryStore.Close()
		claimRetryWorktree(t, retryStore, retryID)
		worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + retryID, ActorClass: ActorAgent}
		attempt := "attempt:" + retryID + ":same:1"
		if err := dispatchSameStepAttempt(t, retryStore, retryID, "repair", attempt, worker, false, nil); err != nil {
			t.Fatalf("failed-retry dispatch: %v", err)
		}
		failWorkerAttemptWithKind(t, retryStore, retryID, attempt, "the lane failed before a judgeable result existed")
		retry, err := ReadWorkflowApprovalBoundary(ctx, retryStore, nil, WorkflowActionPreflightRequest{WorkID: retryID, ActionID: "dispatch_worker", Actor: worker})
		if err != nil {
			t.Fatalf("failed-retry boundary read refused: %v", err)
		}
		expected, err := WorkflowFailedWorkerRetryBinding(ctx, retryStore, nil, retryID)
		if err != nil || expected == nil {
			t.Fatalf("the exact binding reader returned %+v, %v, want a binding", expected, err)
		}
		if retry.RetryBinding == nil || *retry.RetryBinding != *expected || retry.RetryBinding.FailedAttemptID != attempt {
			t.Fatalf("boundary retry binding = %+v, want the exact reader's %+v for %s", retry.RetryBinding, *expected, attempt)
		}
		if retry.EvaluatorAuthority != WorkflowOperatorRequired {
			t.Fatalf("dispatching worker authority = %q, want operator_required", retry.EvaluatorAuthority)
		}
		if retry.Policy.Consent != WorkflowConsentRequired {
			t.Fatalf("legacy failed-retry consent = %q, want required", retry.Policy.Consent)
		}

		if approvalEventCount(t, s, workID) != eventsBefore || verdictItemVersion(t, s, workID) != workVersion {
			t.Fatal("the boundary reads moved the unchanged item")
		}
		if _, err := ReadWorkflowApprovalBoundary(ctx, s, registry, confirmed); err != nil {
			t.Fatalf("the store stopped serving after the balanced transaction read: %v", err)
		}
	})
}
