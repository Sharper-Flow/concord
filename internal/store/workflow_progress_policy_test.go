package store

import (
	"encoding/json"
	"slices"
	"testing"
)

// workflowObligationSatisfactionRefs names the live authoring families whose
// fresh heads declare the CD-0164 D2a obligation-satisfaction progress
// policy. The four retired families freeze at their released behavior, so
// they never appear here.
var workflowObligationSatisfactionRefs = []string{"workflow.implementation", "workflow.break_fix", "workflow.research"}

// TestObligationSatisfactionHeadsDeclareThePolicy pins the definition-level
// policy discriminator: each live family's current head carries
// progress_policy obligation_satisfaction_v1, the retired families stay
// unpinned, and a policy declaration without the taxonomy is impossible at
// registration.
func TestObligationSatisfactionHeadsDeclareThePolicy(t *testing.T) {
	t.Parallel()
	for _, ref := range workflowObligationSatisfactionRefs {
		registered, err := BuiltinWorkflowDefinitionForRef(ref)
		if err != nil {
			t.Fatalf("%s does not resolve: %v", ref, err)
		}
		if registered.Definition.ProgressPolicy != WorkflowProgressPolicyObligationSatisfactionV1 {
			t.Fatalf("%s v%d progress policy = %q, want %q", ref, registered.Definition.Version, registered.Definition.ProgressPolicy, WorkflowProgressPolicyObligationSatisfactionV1)
		}
	}
	for _, ref := range []string{"workflow.architecture_spike", "workflow.ops_runbook", "workflow.static_analysis", "workflow.generic_one_off"} {
		registered, err := BuiltinWorkflowDefinitionForRef(ref)
		if err != nil {
			t.Fatalf("%s does not resolve: %v", ref, err)
		}
		if registered.Definition.ProgressPolicy != "" {
			t.Fatalf("frozen %s declares progress policy %q; retired families keep their released behavior", ref, registered.Definition.ProgressPolicy)
		}
	}
}

// TestObligationSatisfactionTaxonomyMatchesTheCanonicalMapping holds the
// closed action taxonomy on every fresh head: each declared action carries
// exactly the canonical satisfaction kind for its ID, every satisfying
// action present declares its kind, and the non-satisfying machinery
// (records, fences, checkpoints, corrections) stays empty.
func TestObligationSatisfactionTaxonomyMatchesTheCanonicalMapping(t *testing.T) {
	t.Parallel()
	for _, ref := range workflowObligationSatisfactionRefs {
		registered, err := BuiltinWorkflowDefinitionForRef(ref)
		if err != nil {
			t.Fatalf("%s does not resolve: %v", ref, err)
		}
		satisfying := 0
		classes := map[string]bool{}
		for _, action := range registered.Definition.ActionDefinitions {
			want := workflowActionSatisfactionKind(action.ID)
			if action.SatisfactionKind != want {
				t.Errorf("%s v%d action %s satisfaction kind = %q, want %q", ref, registered.Definition.Version, action.ID, action.SatisfactionKind, want)
			}
			if want != "" {
				satisfying++
				classes[want] = true
			}
		}
		if satisfying == 0 {
			t.Errorf("%s v%d declares the progress policy without a satisfying action", ref, registered.Definition.Version)
		}
		for _, class := range []string{WorkflowSatisfactionEvidence, WorkflowSatisfactionPredicate, WorkflowSatisfactionPremise, WorkflowSatisfactionWorkerJob} {
			if !classes[class] {
				t.Errorf("%s v%d declares no %s satisfaction; the fresh heads carry every class their actions ship", ref, registered.Definition.Version, class)
			}
		}
	}
}

// TestObligationSatisfactionClassMembership spot-checks the closed mapping
// against the exact action classes the approved taxonomy names.
func TestObligationSatisfactionClassMembership(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		actionID string
		want     string
	}{
		{"bind_evidence", WorkflowSatisfactionEvidence},
		{"record_report", WorkflowSatisfactionEvidence},
		{"record_research", WorkflowSatisfactionEvidence},
		{"accept_decision", WorkflowSatisfactionEvidence},
		{"approve_operation", WorkflowSatisfactionEvidence},
		{"accept_worker_evidence", WorkflowSatisfactionEvidence},
		{"record_verdict", WorkflowSatisfactionPredicate},
		{"confirm_premise", WorkflowSatisfactionPremise},
		{"accept_worker_result", WorkflowSatisfactionWorkerJob},
		// The machinery that creates, records, or fences work never
		// satisfies an obligation.
		{"record_proposal", ""},
		{"approve_contract", ""},
		{"record_worker_failure", ""},
		{"reject_worker_result", ""},
		{"request_correction", ""},
		{"dispatch_worker", ""},
		{"record_worker_job", ""},
		{"complete", ""},
		{"record_delivery", ""},
		{"checkpoint_context", ""},
		{"cross_context_boundary", ""},
		{"record_work_context", ""},
		{"supersede_contract", ""},
	} {
		if got := workflowActionSatisfactionKind(testCase.actionID); got != testCase.want {
			t.Errorf("workflowActionSatisfactionKind(%q) = %q, want %q", testCase.actionID, got, testCase.want)
		}
	}
}

// TestReleasedDefinitionVersionsCarryNoProgressPolicyOrTaxonomy walks every
// registered version: the policy and its taxonomy ride only the fresh heads
// of the live authoring families. Every released pin, and every retired
// family at any version, keeps the legacy empty fields its digest covers.
func TestReleasedDefinitionVersionsCarryNoProgressPolicyOrTaxonomy(t *testing.T) {
	t.Parallel()
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		fresh := slices.Contains(workflowObligationSatisfactionRefs, definition.Ref) && definition.Version == builtinWorkflowCurrentVersions[definition.Ref]
		if fresh {
			continue
		}
		if definition.ProgressPolicy != "" {
			t.Fatalf("%s v%d declares progress policy %q; only the fresh live-family heads carry it", definition.Ref, definition.Version, definition.ProgressPolicy)
		}
		for _, action := range definition.ActionDefinitions {
			if action.SatisfactionKind != "" {
				t.Fatalf("%s v%d declares satisfaction kind %q on %s; only the fresh live-family heads carry the taxonomy", definition.Ref, definition.Version, action.SatisfactionKind, action.ID)
			}
		}
	}
}

// TestObligationSatisfactionValidationClosesTheFields proves the validator
// refuses every open shape: an unknown policy, an unknown satisfaction kind,
// taxonomy without the policy, a kind that contradicts the canonical
// mapping, and a policy declared with no satisfying action.
func TestObligationSatisfactionValidationClosesTheFields(t *testing.T) {
	t.Parallel()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatalf("workflow.implementation does not resolve: %v", err)
	}
	base := registered.Definition

	// The fresh head's own declaration validates.
	if err := ValidateWorkflowDefinition(base); err != nil {
		t.Fatalf("the fresh head does not validate: %v", err)
	}

	unknownPolicy := base
	unknownPolicy.ProgressPolicy = "obligation_satisfaction_v2"
	if err := ValidateWorkflowDefinition(unknownPolicy); err == nil {
		t.Fatal("validation accepted an unknown progress policy")
	}

	unknownKind := base
	for index := range unknownKind.ActionDefinitions {
		if unknownKind.ActionDefinitions[index].ID == "complete" {
			unknownKind.ActionDefinitions[index].SatisfactionKind = "worker_output"
		}
	}
	if err := ValidateWorkflowDefinition(unknownKind); err == nil {
		t.Fatal("validation accepted an unknown satisfaction kind")
	}

	mismatchedKind := base
	for index := range mismatchedKind.ActionDefinitions {
		if mismatchedKind.ActionDefinitions[index].ID == "record_verdict" {
			mismatchedKind.ActionDefinitions[index].SatisfactionKind = WorkflowSatisfactionEvidence
		}
	}
	if err := ValidateWorkflowDefinition(mismatchedKind); err == nil {
		t.Fatal("validation accepted a satisfaction kind that contradicts the action's obligation class")
	}

	nonSatisfyingKind := base
	for index := range nonSatisfyingKind.ActionDefinitions {
		if nonSatisfyingKind.ActionDefinitions[index].ID == "record_proposal" {
			nonSatisfyingKind.ActionDefinitions[index].SatisfactionKind = WorkflowSatisfactionEvidence
		}
	}
	if err := ValidateWorkflowDefinition(nonSatisfyingKind); err == nil {
		t.Fatal("validation accepted a satisfaction kind on a non-satisfying action")
	}

	// A released legacy pin gains the taxonomy only with the policy.
	legacy, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 35)
	if !ok {
		t.Fatal("workflow.implementation v35 is not registered")
	}
	taxonomyWithoutPolicy := legacy.Definition
	for index := range taxonomyWithoutPolicy.ActionDefinitions {
		if taxonomyWithoutPolicy.ActionDefinitions[index].ID == "accept_worker_result" {
			taxonomyWithoutPolicy.ActionDefinitions[index].SatisfactionKind = WorkflowSatisfactionWorkerJob
		}
	}
	if err := ValidateWorkflowDefinition(taxonomyWithoutPolicy); err == nil {
		t.Fatal("validation accepted satisfaction taxonomy without the progress policy")
	}

	policyWithoutTaxonomy := base
	for index := range policyWithoutTaxonomy.ActionDefinitions {
		policyWithoutTaxonomy.ActionDefinitions[index].SatisfactionKind = ""
	}
	if err := ValidateWorkflowDefinition(policyWithoutTaxonomy); err == nil {
		t.Fatal("validation accepted the progress policy without a satisfying action")
	}
}

// TestObligationSatisfactionManifestCarriesTheDeclaredFields holds the
// digest-covered manifest shape: a fresh head serializes progress_policy and
// per-action satisfaction_kind, while a released legacy manifest serializes
// neither key, so its digest stays byte-identical.
func TestObligationSatisfactionManifestCarriesTheDeclaredFields(t *testing.T) {
	t.Parallel()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatalf("workflow.implementation does not resolve: %v", err)
	}
	canonical, err := CanonicalWorkflowDefinition(registered.Definition)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(canonical, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["progress_policy"] != WorkflowProgressPolicyObligationSatisfactionV1 {
		t.Fatalf("manifest progress_policy = %v, want %q", manifest["progress_policy"], WorkflowProgressPolicyObligationSatisfactionV1)
	}
	actions, ok := manifest["action_definitions"].([]any)
	if !ok {
		t.Fatal("manifest action_definitions is not an array")
	}
	seen := map[string]any{}
	for _, raw := range actions {
		action, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("manifest action entry is not an object")
		}
		id, _ := action["id"].(string)
		seen[id] = action["satisfaction_kind"]
	}
	want := map[string]string{
		"bind_evidence":        WorkflowSatisfactionEvidence,
		"record_verdict":       WorkflowSatisfactionPredicate,
		"confirm_premise":      WorkflowSatisfactionPremise,
		"accept_worker_result": WorkflowSatisfactionWorkerJob,
		"record_proposal":      "",
		"dispatch_worker":      "",
	}
	for id, kind := range want {
		value, declared := seen[id]
		if !declared {
			t.Fatalf("manifest action %s is missing", id)
		}
		if kind == "" {
			if _, present := value.(string); present && value != "" {
				t.Fatalf("manifest action %s satisfaction kind = %v, want absent", id, value)
			}
			continue
		}
		if value != kind {
			t.Fatalf("manifest action %s satisfaction kind = %v, want %q", id, value, kind)
		}
	}

	legacy, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 35)
	if !ok {
		t.Fatal("workflow.implementation v35 is not registered")
	}
	legacyCanonical, err := CanonicalWorkflowDefinition(legacy.Definition)
	if err != nil {
		t.Fatal(err)
	}
	var legacyManifest map[string]any
	if err := json.Unmarshal(legacyCanonical, &legacyManifest); err != nil {
		t.Fatal(err)
	}
	if _, present := legacyManifest["progress_policy"]; present {
		t.Fatal("released manifest carries progress_policy; its digest would move")
	}
	legacyActions, ok := legacyManifest["action_definitions"].([]any)
	if !ok {
		t.Fatal("legacy manifest action_definitions is not an array")
	}
	for _, raw := range legacyActions {
		action, ok := raw.(map[string]any)
		if !ok {
			t.Fatal("legacy manifest action entry is not an object")
		}
		if _, present := action["satisfaction_kind"]; present {
			t.Fatalf("released manifest action %v carries satisfaction_kind; its digest would move", action["id"])
		}
	}
}
