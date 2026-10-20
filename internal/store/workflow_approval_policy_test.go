package store

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The approval classification owner is one pure policy reader over a verified
// definition, the action, declared decision domains, and typed consequence
// facts. The discriminator approval_policy=consequence_v1 is definition-owned:
// absence selects the released legacy policy, unknown values refuse, and no
// registered current definition carries the new value, so current authoring is
// unchanged.

// approvalPolicyDefinition builds a minimal definition value for the pure
// reader: the registry owns full definition validation, the reader owns policy
// classification.
func approvalPolicyDefinition(policy string, actions ...string) WorkflowDefinition {
	definitions := make([]WorkflowActionDefinition, 0, len(actions))
	for _, id := range actions {
		definitions = append(definitions, WorkflowActionDefinition{ID: id, Consequence: ActionInternalSQLite, Approval: ActionApprovalNone})
	}
	changes := false
	return WorkflowDefinition{Ref: "workflow.approval_policy_fixture", Version: 1, WorkKind: WorkKindStaticAnalysis, ChangesProductTruth: &changes, ApprovalPolicy: policy, AvailableActions: actions, ActionDefinitions: definitions}
}

// canonicalDecisionDomainSets enumerates all eight canonical decision-domain
// sets: engineering and every nonempty subset in canonical order.
func canonicalDecisionDomainSets() [][]string {
	return [][]string{
		{},
		{"preference"},
		{"product"},
		{"ui_ux"},
		{"preference", "product"},
		{"preference", "ui_ux"},
		{"product", "ui_ux"},
		{"preference", "product", "ui_ux"},
	}
}

func TestWorkflowApprovalPolicy(t *testing.T) {
	t.Parallel()

	t.Run("legacy policy preserves declared metadata and overrides", func(t *testing.T) {
		t.Parallel()
		for _, definition := range legacyApprovalPolicyHeads(t) {
			for _, action := range definition.ActionDefinitions {
				result, err := ClassifyWorkflowApproval(definition, action.ID, WorkflowApprovalFacts{})
				if err != nil {
					t.Fatalf("legacy classification refused %s %s: %v", definition.Ref, action.ID, err)
				}
				if result.Class != WorkflowApprovalClassLegacy {
					t.Errorf("legacy classification of %s %s read class %q, want legacy", definition.Ref, action.ID, result.Class)
				}
				if result.Policy != "" {
					t.Errorf("legacy classification of %s %s echoed policy %q, want absence", definition.Ref, action.ID, result.Policy)
				}
				want := WorkflowApprovalConsent(action.Approval)
				if result.Consent != want {
					t.Errorf("legacy consent for %s %s = %q, want the declared %q", definition.Ref, action.ID, result.Consent, want)
				}
			}
		}
		legacy := legacyApprovalPolicyHeads(t)[0]
		overrides := map[string]WorkflowApprovalConsent{}
		for _, action := range legacy.ActionDefinitions {
			result, err := ClassifyWorkflowApproval(legacy, action.ID, WorkflowApprovalFacts{OperatorRequired: true})
			if err != nil {
				t.Fatalf("legacy operator-required classification refused %s: %v", action.ID, err)
			}
			overrides[action.ID] = result.Consent
		}
		for _, actionID := range []string{"record_verdict", "complete", "request_correction", "confirm_premise"} {
			if overrides[actionID] != WorkflowConsentRequired {
				t.Errorf("legacy operator-required override on %s = %q, want required", actionID, overrides[actionID])
			}
		}
		for actionID, declared := range overrides {
			if workflowOperatorVerdictActions[actionID] {
				continue
			}
			if declared != WorkflowApprovalConsent(legacyActionApproval(t, legacy, actionID)) {
				t.Errorf("legacy operator-required override moved undeclared-gate action %s to %q, want its declared metadata", actionID, declared)
			}
		}
		retry, err := ClassifyWorkflowApproval(legacy, "dispatch_worker", WorkflowApprovalFacts{FailedRetryBelowWall: true})
		if err != nil {
			t.Fatalf("legacy failed-retry classification refused: %v", err)
		}
		if retry.Consent != WorkflowConsentRequired {
			t.Fatalf("legacy failed-retry-below-wall consent = %q, want required", retry.Consent)
		}
		plain, err := ClassifyWorkflowApproval(legacy, "dispatch_worker", WorkflowApprovalFacts{})
		if err != nil {
			t.Fatalf("legacy dispatch classification refused: %v", err)
		}
		if plain.Consent != WorkflowConsentNone {
			t.Fatalf("legacy dispatch consent = %q, want the declared none", plain.Consent)
		}
		if _, err := ClassifyWorkflowApproval(legacy, "approve_contract", WorkflowApprovalFacts{Destructive: WorkflowDestructiveCancel}); err == nil {
			t.Fatal("legacy policy accepted a destructive consequence fact it never declared, want refusal")
		}
	})

	t.Run("new policy classifies every gated action", func(t *testing.T) {
		t.Parallel()
		definition := approvalPolicyDefinition(WorkflowApprovalPolicyConsequenceV1,
			"approve_contract", "confirm_premise", "accept_decision", "approve_operation",
			"request_correction", "supersede_contract", "record_verdict", "complete", "dispatch_worker")
		for _, actionID := range []string{"approve_contract", "confirm_premise"} {
			result, err := ClassifyWorkflowApproval(definition, actionID, WorkflowApprovalFacts{ProposedDecisionDomains: []string{"product"}, ActiveDecisionDomains: []string{"product"}})
			if err != nil {
				t.Fatalf("decision classification refused %s: %v", actionID, err)
			}
			if result.Class != WorkflowApprovalClassDecision || result.Consent != WorkflowConsentRequired {
				t.Errorf("decision-bearing %s classified %s/%s, want decision/required", actionID, result.Class, result.Consent)
			}
		}
		for _, actionID := range []string{"accept_decision", "approve_operation", "request_correction", "supersede_contract", "record_verdict", "complete", "dispatch_worker"} {
			result, err := ClassifyWorkflowApproval(definition, actionID, WorkflowApprovalFacts{})
			if err != nil {
				t.Fatalf("routine classification refused %s: %v", actionID, err)
			}
			if result.Class != WorkflowApprovalClassRoutine || result.Consent != WorkflowConsentNone {
				t.Errorf("routine %s classified %s/%s, want routine/none", actionID, result.Class, result.Consent)
			}
		}
	})

	t.Run("all eight canonical domain sets gate decision consent", func(t *testing.T) {
		t.Parallel()
		definition := approvalPolicyDefinition(WorkflowApprovalPolicyConsequenceV1, "approve_contract", "confirm_premise")
		for index, domains := range canonicalDecisionDomainSets() {
			want := WorkflowConsentNone
			if len(domains) != 0 {
				want = WorkflowConsentRequired
			}
			proposed, err := ClassifyWorkflowApproval(definition, "approve_contract", WorkflowApprovalFacts{ProposedDecisionDomains: domains})
			if err != nil {
				t.Fatalf("set %d proposed classification refused: %v", index, err)
			}
			if proposed.Class != WorkflowApprovalClassDecision || proposed.Consent != want {
				t.Errorf("set %d proposed classification = %s/%s, want decision/%s", index, proposed.Class, proposed.Consent, want)
			}
			active, err := ClassifyWorkflowApproval(definition, "confirm_premise", WorkflowApprovalFacts{ActiveDecisionDomains: domains})
			if err != nil {
				t.Fatalf("set %d active classification refused: %v", index, err)
			}
			if active.Class != WorkflowApprovalClassDecision || active.Consent != want {
				t.Errorf("set %d active classification = %s/%s, want decision/%s", index, active.Class, active.Consent, want)
			}
		}
	})

	t.Run("new policy fails closed on missing unknown and noncanonical facts", func(t *testing.T) {
		t.Parallel()
		definition := approvalPolicyDefinition(WorkflowApprovalPolicyConsequenceV1, "approve_contract", "confirm_premise")
		refuseInvalidInput := func(name string, err error) {
			t.Helper()
			if err == nil {
				t.Fatalf("new-policy %s classification accepted the fact, want refusal", name)
			}
			var failure *Failure
			if !failureAs(err, &failure) {
				t.Fatalf("new-policy %s refusal is not a typed failure: %v", name, err)
			}
			if failure.Kind != KindInvalidInput {
				t.Fatalf("new-policy %s refusal kind = %q, want invalid_input at the classifier input boundary", name, failure.Kind)
			}
		}
		_, err := ClassifyWorkflowApproval(definition, "approve_contract", WorkflowApprovalFacts{})
		refuseInvalidInput("approve_contract with absent proposed domains", err)
		_, err = ClassifyWorkflowApproval(definition, "confirm_premise", WorkflowApprovalFacts{})
		refuseInvalidInput("confirm_premise with absent active domains", err)
		for name, domains := range map[string][]string{
			"unknown":   {"preference", "economics"},
			"duplicate": {"product", "product"},
			"unsorted":  {"product", "preference"},
			"oversized": {"preference", "product", "ui_ux", "preference"},
		} {
			_, err := ClassifyWorkflowApproval(definition, "approve_contract", WorkflowApprovalFacts{ProposedDecisionDomains: domains})
			refuseInvalidInput("approve_contract with the "+name+" domain set", err)
		}
	})

	t.Run("destructive consequence takes precedence", func(t *testing.T) {
		t.Parallel()
		definition := approvalPolicyDefinition(WorkflowApprovalPolicyConsequenceV1, "approve_contract", "record_verdict")
		result, err := ClassifyWorkflowApproval(definition, "approve_contract", WorkflowApprovalFacts{ProposedDecisionDomains: []string{"product"}, Destructive: WorkflowDestructiveCancel})
		if err != nil {
			t.Fatalf("destructive classification refused: %v", err)
		}
		if result.Class != WorkflowApprovalClassDestructive || result.Consent != WorkflowConsentRequired {
			t.Fatalf("destructive precedence on approve_contract = %s/%s, want destructive/required", result.Class, result.Consent)
		}
		routine, err := ClassifyWorkflowApproval(definition, "record_verdict", WorkflowApprovalFacts{Destructive: WorkflowDestructiveCancel})
		if err != nil {
			t.Fatalf("destructive routine classification refused: %v", err)
		}
		if routine.Class != WorkflowApprovalClassDestructive || routine.Consent != WorkflowConsentRequired {
			t.Fatalf("destructive precedence on record_verdict = %s/%s, want destructive/required", routine.Class, routine.Consent)
		}
		if _, err := ClassifyWorkflowApproval(definition, "approve_contract", WorkflowApprovalFacts{Destructive: WorkflowDestructiveConsequence("delete_everything")}); err == nil {
			t.Fatal("an unknown destructive consequence fact classified, want refusal")
		}
	})

	t.Run("unknown policy action and identity refuse", func(t *testing.T) {
		t.Parallel()
		unknownPolicy := approvalPolicyDefinition("consequence_v2", "approve_contract")
		if _, err := ClassifyWorkflowApproval(unknownPolicy, "approve_contract", WorkflowApprovalFacts{ProposedDecisionDomains: []string{}}); err == nil {
			t.Error("an unknown approval policy value classified, want refusal")
		}
		definition := approvalPolicyDefinition(WorkflowApprovalPolicyConsequenceV1, "approve_contract")
		if _, err := ClassifyWorkflowApproval(definition, "record_alignment", WorkflowApprovalFacts{}); err == nil {
			t.Error("an undeclared action classified, want refusal")
		}
		if _, err := ClassifyWorkflowApproval(definition, "", WorkflowApprovalFacts{}); err == nil {
			t.Error("an empty action ID classified, want refusal")
		}
	})

	t.Run("new policy never infers scope from product truth or override facts", func(t *testing.T) {
		t.Parallel()
		productChanging := true
		definition := approvalPolicyDefinition(WorkflowApprovalPolicyConsequenceV1, "approve_contract", "confirm_premise", "record_verdict", "dispatch_worker")
		definition.ChangesProductTruth = &productChanging
		engineering, err := ClassifyWorkflowApproval(definition, "approve_contract", WorkflowApprovalFacts{ProposedDecisionDomains: []string{}})
		if err != nil {
			t.Fatalf("product-changing engineering classification refused: %v", err)
		}
		if engineering.Class != WorkflowApprovalClassDecision || engineering.Consent != WorkflowConsentNone {
			t.Fatalf("product-changing engineering contract classified %s/%s, want decision/none with no product-truth inference", engineering.Class, engineering.Consent)
		}
		override, err := ClassifyWorkflowApproval(definition, "record_verdict", WorkflowApprovalFacts{OperatorRequired: true})
		if err != nil {
			t.Fatalf("operator-required routine classification refused: %v", err)
		}
		if override.Class != WorkflowApprovalClassRoutine || override.Consent != WorkflowConsentNone {
			t.Fatalf("operator-required record_verdict classified %s/%s, want routine/none with no operator substitution", override.Class, override.Consent)
		}
		confirmation, err := ClassifyWorkflowApproval(definition, "confirm_premise", WorkflowApprovalFacts{ActiveDecisionDomains: []string{"ui_ux"}, OperatorRequired: true})
		if err != nil {
			t.Fatalf("operator-required confirmation classification refused: %v", err)
		}
		if confirmation.Class != WorkflowApprovalClassDecision || confirmation.Consent != WorkflowConsentRequired {
			t.Fatalf("operator-required decision confirmation classified %s/%s, want decision/required", confirmation.Class, confirmation.Consent)
		}
		engineeringConfirmation, err := ClassifyWorkflowApproval(definition, "confirm_premise", WorkflowApprovalFacts{ActiveDecisionDomains: []string{}, OperatorRequired: true})
		if err != nil {
			t.Fatalf("operator-required engineering confirmation classification refused: %v", err)
		}
		if engineeringConfirmation.Class != WorkflowApprovalClassDecision || engineeringConfirmation.Consent != WorkflowConsentNone {
			t.Fatalf("operator-required engineering confirmation classified %s/%s, want decision/none", engineeringConfirmation.Class, engineeringConfirmation.Consent)
		}
		retry, err := ClassifyWorkflowApproval(definition, "dispatch_worker", WorkflowApprovalFacts{FailedRetryBelowWall: true})
		if err != nil {
			t.Fatalf("new-policy failed-retry classification refused: %v", err)
		}
		if retry.Class != WorkflowApprovalClassRoutine || retry.Consent != WorkflowConsentNone {
			t.Fatalf("new-policy failed retry classified %s/%s, want routine/none with binding revalidation", retry.Class, retry.Consent)
		}
	})

	t.Run("engineering consequence generations retain their declared policy", func(t *testing.T) {
		t.Parallel()
		generations := map[string][]int64{
			"workflow.implementation": {34, 35, 36},
			"workflow.break_fix":      {31, 32, 33},
			"workflow.research":       {21},
		}
		live := map[string]int64{}
		for _, head := range BuiltinWorkflowDefinitions() {
			live[head.Ref] = head.Version
		}
		for _, ref := range []string{"workflow.implementation", "workflow.break_fix", "workflow.research"} {
			if _, ok := live[ref]; !ok {
				t.Fatalf("%s is not a live authoring head", ref)
			}
			if !slices.Contains(generations[ref], live[ref]) {
				t.Fatalf("%s v%d is not a declared consequence generation", ref, live[ref])
			}
		}
		for _, definition := range builtinWorkflowDefinitionsWithHistory() {
			want := ""
			if slices.Contains(generations[definition.Ref], definition.Version) {
				want = WorkflowApprovalPolicyConsequenceV1
			}
			if definition.ApprovalPolicy != want {
				t.Errorf("registered %s v%d carries approval policy %q, want %q", definition.Ref, definition.Version, definition.ApprovalPolicy, want)
			}
		}
	})

	t.Run("registry refuses unknown policy and digests the declared value", func(t *testing.T) {
		t.Parallel()
		head := legacyApprovalPolicyHeads(t)[0]
		invalid := head
		invalid.ApprovalPolicy = "consequence_v2"
		if err := ValidateWorkflowDefinition(invalid); err == nil {
			t.Fatal("definition validation accepted an unknown approval policy, want refusal")
		}
		legacyDigest, err := WorkflowDefinitionDigest(head)
		if err != nil {
			t.Fatal(err)
		}
		if legacyDigest != headDigestForTest(t, head) {
			t.Fatalf("legacy head digest = %s, want the pinned %s", legacyDigest, headDigestForTest(t, head))
		}
		declared := head
		declared.ApprovalPolicy = WorkflowApprovalPolicyConsequenceV1
		declaredDigest, err := WorkflowDefinitionDigest(declared)
		if err != nil {
			t.Fatal(err)
		}
		if declaredDigest == legacyDigest {
			t.Fatal("a declared approval policy did not change the definition digest")
		}
		canonical, err := CanonicalWorkflowDefinition(declared)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(canonical), `"approval_policy":"consequence_v1"`) {
			t.Fatalf("canonical manifest omitted the declared policy: %s", canonical)
		}
		registry := NewWorkflowDefinitionRegistry()
		registered, err := registry.Register(declared)
		if err != nil {
			t.Fatalf("a consequence_v1 definition refused registration: %v", err)
		}
		if registered.Digest != declaredDigest {
			t.Fatalf("registered digest = %s, want %s", registered.Digest, declaredDigest)
		}
		if err := registry.Verify(declared.Ref, declared.Version, declaredDigest); err != nil {
			t.Fatalf("declared digest did not verify: %v", err)
		}
		encoded, err := json.Marshal(declared)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"approval_policy":"consequence_v1"`) {
			t.Fatal("the typed definition did not serialize the declared policy")
		}
	})
}

// headDigestForTest looks up the pinned digest of one builtin head through the
// builtin registry, so the digest-neutrality witness compares against the
// recorded pin rather than a recomputation.
// legacyApprovalPolicyHeads returns, for each live authoring family, the
// newest version that answers consent under the released legacy policy.
func legacyApprovalPolicyHeads(t *testing.T) []WorkflowDefinition {
	t.Helper()
	var heads []WorkflowDefinition
	for _, head := range BuiltinWorkflowDefinitions() {
		heads = append(heads, legacyApprovalPolicyDefinitionForRef(t, head.Ref).Definition)
	}
	return heads
}

func headDigestForTest(t *testing.T, definition WorkflowDefinition) string {
	t.Helper()
	registered, ok := BuiltinWorkflowRegistry().Lookup(definition.Ref, definition.Version)
	if !ok {
		t.Fatalf("builtin head %s v%d is not registered", definition.Ref, definition.Version)
	}
	return registered.Digest
}

// legacyActionApproval reads one declared action's approval metadata off a
// definition.
func legacyActionApproval(t *testing.T, definition WorkflowDefinition, actionID string) ActionApproval {
	t.Helper()
	for _, action := range definition.ActionDefinitions {
		if action.ID == actionID {
			return action.Approval
		}
	}
	t.Fatalf("definition %s does not declare action %s", definition.Ref, actionID)
	return ""
}
