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
		// Main dispatches the lanes and executes none of them.
		if state.EvaluatorAuthority != WorkflowIndependentEvaluator {
			t.Fatalf("Main's authority = %q, want independent", state.EvaluatorAuthority)
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

		// The nil registry resolves the builtin convention the pin names; an
		// empty registry refuses through the typed pin failure.
		if nilState, err := ReadWorkflowApprovalBoundary(ctx, s, nil, confirmed); err != nil || !reflect.DeepEqual(nilState, state) {
			t.Fatalf("nil-registry read = %+v, %v, want the builtin pin state", nilState, err)
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
		failed := s2WorkerFailedAttributedEvent(retryID, attempt, "failed-"+retryID, preferredModelForLane(laneForID(t)), WorkerFailureWorkerError, "the lane failed before a judgeable result existed", time.Unix(30, 0).UTC())
		if err := s2WorkerFailedLive(t, retryFixture.store, failed); err != nil {
			t.Fatalf("live failed terminal: %v", err)
		}
		retry, err := ReadWorkflowApprovalBoundary(ctx, retryFixture.store, nil, WorkflowActionPreflightRequest{WorkID: retryID, ActionID: "dispatch_worker", Actor: retryFixture.owner})
		if err != nil {
			t.Fatalf("failed-retry boundary read refused: %v", err)
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
		s, owner, reviewer := seedItemAtAcceptance(t, workID, false)
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
