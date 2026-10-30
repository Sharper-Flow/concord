package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinWorkflowRegistryHasTheThreeLiveContractFamilies(t *testing.T) {
	t.Parallel()
	live := BuiltinWorkflowDefinitions()
	liveRefs := make([]string, len(live))
	for index, definition := range live {
		liveRefs[index] = definition.Ref
	}
	if !reflect.DeepEqual(liveRefs, []string{"workflow.implementation", "workflow.break_fix", "workflow.research"}) {
		t.Fatalf("live authoring heads = %v, want the three retained families in chain order", liveRefs)
	}
	defs := BuiltinWorkflowDefinitions()
	if len(defs) != 3 {
		t.Fatalf("current definition count = %d, want 3", len(defs))
	}
	registry := NewWorkflowDefinitionRegistry()
	wantSteps := map[string][]string{
		"workflow.implementation": {"proposal", "alignment", "planning", "design", "execution", "refine", "delivery", "acceptance", "release"},
		"workflow.break_fix":      {"diagnosis", "planning", "repair", "refine", "delivery", "verify", "complete"},
		"workflow.research":       {"frame", "investigate", "findings", "conclude", "complete"},
	}
	// A composed head lists each action once, in first appearance over its
	// compiled steps.
	wantActions := map[string][]string{
		"workflow.implementation": {"record_proposal", "checkpoint_context", "cross_context_boundary", "accept_worker_result", "dispatch_worker", "record_work_context", "record_alignment", "approve_contract", "accept_worker_evidence", "record_design", "start_execution", "checkpoint_execution", "bind_evidence", "link_successor", "record_delivery", "request_correction", "record_worker_job", "checkpoint_refine", "record_verdict", "confirm_premise", "complete"},
		"workflow.break_fix":      {"record_diagnosis", "checkpoint_context", "cross_context_boundary", "record_work_context", "approve_contract", "dispatch_worker", "accept_worker_evidence", "checkpoint_repair", "bind_evidence", "link_successor", "record_delivery", "request_correction", "accept_worker_result", "record_worker_job", "checkpoint_refine", "record_verdict", "confirm_premise", "complete"},
		"workflow.research":       {"frame_research", "approve_contract", "checkpoint_context", "cross_context_boundary", "dispatch_worker", "accept_worker_evidence", "record_work_context", "record_finding", "revise_candidates", "bind_evidence", "accept_worker_result", "record_report", "link_successor", "record_conclusion", "record_verdict", "confirm_premise", "record_worker_job", "complete"},
	}
	wantTerminals := map[string]string{
		"workflow.implementation": "release", "workflow.break_fix": "complete", "workflow.research": "complete",
	}
	wantEdges := map[string][]WorkflowEdge{
		"workflow.implementation": {{"proposal", "alignment", WorkflowEdgeForward}, {"alignment", "planning", WorkflowEdgeForward}, {"planning", "design", WorkflowEdgeForward}, {"design", "execution", WorkflowEdgeForward}, {"execution", "refine", WorkflowEdgeForward}, {"refine", "delivery", WorkflowEdgeForward}, {"delivery", "acceptance", WorkflowEdgeForward}, {"acceptance", "release", WorkflowEdgeForward}, {"execution", "execution", WorkflowEdgeRetry}, {"acceptance", "refine", WorkflowEdgeFailure}},
		"workflow.break_fix":      {{"diagnosis", "planning", WorkflowEdgeForward}, {"planning", "repair", WorkflowEdgeForward}, {"repair", "refine", WorkflowEdgeForward}, {"refine", "delivery", WorkflowEdgeForward}, {"delivery", "verify", WorkflowEdgeForward}, {"verify", "complete", WorkflowEdgeForward}, {"repair", "repair", WorkflowEdgeRetry}, {"verify", "refine", WorkflowEdgeFailure}},
		"workflow.research":       {{"frame", "investigate", WorkflowEdgeForward}, {"investigate", "findings", WorkflowEdgeForward}, {"findings", "conclude", WorkflowEdgeForward}, {"conclude", "complete", WorkflowEdgeForward}},
	}
	for _, definition := range defs {
		registered, err := registry.Register(definition)
		if err != nil {
			t.Fatalf("register %s: %v", definition.Ref, err)
		}
		if registered.Digest == "" || !strings.HasPrefix(registered.Digest, "sha256:") {
			t.Fatalf("%s has no computed digest", definition.Ref)
		}
		if err := registry.Verify(definition.Ref, registered.Digest); err != nil {
			t.Fatalf("verify %s: %v", definition.Ref, err)
		}
		stored, ok := registry.Current(definition.Ref)
		if !ok || !reflect.DeepEqual(stored.Definition, definition) {
			t.Fatalf("registry changed the full definition for %s", definition.Ref)
		}
		gotSteps := make([]string, len(definition.StepGraph.Steps))
		for i, step := range definition.StepGraph.Steps {
			gotSteps[i] = step.ID
		}
		if !reflect.DeepEqual(gotSteps, wantSteps[definition.Ref]) {
			t.Fatalf("%s step graph = %v, want %v", definition.Ref, gotSteps, wantSteps[definition.Ref])
		}
		if !reflect.DeepEqual(definition.AvailableActions, wantActions[definition.Ref]) {
			t.Fatalf("%s actions = %v, want %v", definition.Ref, definition.AvailableActions, wantActions[definition.Ref])
		}
		for _, step := range definition.StepGraph.Steps {
			if !slices.Contains(step.Actions, "checkpoint_context") || !slices.Contains(step.Actions, "cross_context_boundary") {
				t.Fatalf("%s step %s does not expose both continuity actions", definition.Ref, step.ID)
			}
		}
		for _, action := range definition.ActionDefinitions {
			if action.ID != "checkpoint_context" && action.ID != "cross_context_boundary" {
				continue
			}
			if len(action.Payload.Fields) == 0 {
				t.Fatalf("%s continuity action is not closed: %+v", definition.Ref, action)
			}
		}
		if !reflect.DeepEqual(definition.StepGraph.TerminalSteps, []string{wantTerminals[definition.Ref]}) {
			t.Fatalf("%s terminal steps = %v, want %q", definition.Ref, definition.StepGraph.TerminalSteps, wantTerminals[definition.Ref])
		}
		if !reflect.DeepEqual(definition.StepGraph.Edges, wantEdges[definition.Ref]) {
			t.Fatalf("%s edges = %v, want %v", definition.Ref, definition.StepGraph.Edges, wantEdges[definition.Ref])
		}
		terminal := definition.StepGraph.Steps[len(definition.StepGraph.Steps)-1]
		if terminal.ID != wantTerminals[definition.Ref] || !slices.Contains(terminal.Actions, "complete") {
			t.Fatalf("%s terminal completion verb is not declared: %+v", definition.Ref, terminal)
		}
		if len(definition.RequiredEvidenceKinds) == 0 || len(definition.RigorRules) == 0 || !definition.CompositionRules.ForwardLinkOnly {
			t.Fatalf("%s omitted artifact/rigor/composition requirements", definition.Ref)
		}
		for _, action := range definition.ActionDefinitions {
			if action.ID == "" || action.Consequence == "" || action.Payload.Fields == nil {
				t.Fatalf("%s has incomplete action metadata: %+v", definition.Ref, action)
			}
		}
		for _, edge := range definition.StepGraph.Edges {
			if edge.From == "" || edge.To == "" || edge.Kind == "" {
				t.Fatalf("%s has incomplete edge metadata: %+v", definition.Ref, edge)
			}
		}
	}
	if _, ok := BuiltinWorkflowRegistry().Current("workflow.implementation"); !ok {
		t.Fatal("implementation definition was not registered")
	}
}

func TestRefinementStepsRequireArtifactEvidenceAndHaveNoSkipEdge(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions()[:2] {
		var refine WorkflowStep
		for _, candidate := range definition.StepGraph.Steps {
			if candidate.ID == "refine" {
				refine = candidate
				break
			}
		}
		if refine.ID == "" || refine.Kind != WorkflowStepExternalEffect {
			t.Fatalf("%s refine step = %+v, want external_effect", definition.Ref, refine)
		}
		if !slices.Contains(evidenceStrings(definition.RequiredEvidenceKinds), string(EvidenceArtifact)) {
			t.Fatalf("%s definition evidence = %v, want artifact", definition.Ref, definition.RequiredEvidenceKinds)
		}
		// The derived-start shape: the qualifying checkpoint opens the fence,
		// and the evidence, delivery, correction, and continuity actions follow it.
		if !reflect.DeepEqual(refine.Actions[:5], []string{"checkpoint_refine", "bind_evidence", "record_delivery", "request_correction", "checkpoint_context"}) {
			t.Fatalf("%s refine actions = %v, want derived-start checkpoint, evidence, delivery, correction, continuity", definition.Ref, refine.Actions)
		}
		var delivery WorkflowStep
		for _, candidate := range definition.StepGraph.Steps {
			if candidate.ID == "delivery" {
				delivery = candidate
			}
		}
		if !reflect.DeepEqual(delivery.Actions, []string{"record_delivery", "request_correction", "checkpoint_context", "cross_context_boundary"}) {
			t.Fatalf("%s delivery actions = %v, want the delivery exit, the corrective return, and the continuity holds", definition.Ref, delivery.Actions)
		}
		if bindingStep := workflowEvidenceRecoveryBindingStep(definition, refine.ID); bindingStep != "" {
			t.Fatalf("%s refine recovery binding step = %q, want none", definition.Ref, bindingStep)
		}
		for _, edge := range definition.StepGraph.Edges {
			if edge.From == refine.ID && edge.Kind == WorkflowEdgeOptional {
				t.Fatalf("%s refine has an optional edge: %+v", definition.Ref, edge)
			}
		}
	}
}

func TestBuiltinWorkflowDefinitionsMatchExactPhaseBContractMetadata(t *testing.T) {
	t.Parallel()
	type expected struct {
		evidence []EvidenceKind
		outcome  WorkflowOutcomeSchema
		success  []WorkKind
		external map[string]ActionConsequence
		cross    map[string]ActionConsequence
	}
	want := map[string]expected{
		"workflow.implementation":     {evidence: []EvidenceKind{EvidenceVerification, EvidenceReview, EvidenceArtifact}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}}, success: []WorkKind{WorkKindBreakFix, WorkKindResearch}, external: map[string]ActionConsequence{"start_execution": ActionExternalEffect, "checkpoint_execution": ActionExternalEffect}},
		"workflow.break_fix":          {evidence: []EvidenceKind{EvidenceVerification, EvidenceArtifact}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateAbsent, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}}, success: []WorkKind{WorkKindImplementation, WorkKindResearch}, external: map[string]ActionConsequence{"start_repair": ActionExternalEffect, "checkpoint_repair": ActionExternalEffect}},
		"workflow.research":           {evidence: []EvidenceKind{EvidenceArtifact}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"no_change", "resolved", "report_recorded"}}, success: []WorkKind{WorkKindBreakFix, WorkKindArchitectureSpike, WorkKindStaticAnalysis}, cross: map[string]ActionConsequence{"record_finding": ActionCrossAuthority}},
		"workflow.architecture_spike": {evidence: []EvidenceKind{EvidenceReview, EvidenceApproval, EvidenceArtifact}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"accepted_decision", "insufficient_evidence"}, DecisionRecordRequired: true}, success: []WorkKind{WorkKindImplementation, WorkKindResearch, WorkKindStaticAnalysis}, external: map[string]ActionConsequence{"start_poc": ActionExternalEffect, "checkpoint_poc": ActionExternalEffect}, cross: map[string]ActionConsequence{"record_research": ActionCrossAuthority, "record_option": ActionCrossAuthority}},
		"workflow.ops_runbook":        {evidence: []EvidenceKind{EvidenceApproval, EvidenceNativeRun}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}}, success: []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch}, external: map[string]ActionConsequence{"start_run": ActionExternalEffect, "checkpoint_run": ActionExternalEffect, "rollback_run": ActionExternalEffect}, cross: map[string]ActionConsequence{"record_health": ActionCrossAuthority}},
		"workflow.static_analysis":    {evidence: []EvidenceKind{EvidenceArtifact, EvidenceReview}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}}, success: []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch}, external: map[string]ActionConsequence{"run_analysis": ActionExternalEffect, "checkpoint_analysis": ActionExternalEffect}},
		"workflow.generic_one_off":    {evidence: []EvidenceKind{EvidenceArtifact}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateOutcome, PredicateCheck}, AllowedOutcomeTokens: []string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}}, success: []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch, WorkKindArchitectureSpike, WorkKindOpsRunbook, WorkKindStaticAnalysis, WorkKindGenericOneOff}, external: map[string]ActionConsequence{"start_action": ActionExternalEffect, "checkpoint_action": ActionExternalEffect}},
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		wantDefinition, ok := want[definition.Ref]
		if !ok {
			t.Fatalf("missing exact metadata expectation for %s", definition.Ref)
		}
		if !reflect.DeepEqual(definition.RequiredEvidenceKinds, wantDefinition.evidence) || !reflect.DeepEqual(definition.OutcomeSchema, wantDefinition.outcome) || !reflect.DeepEqual(definition.CompositionRules.AllowedSuccessorWorkKinds, wantDefinition.success) {
			t.Fatalf("%s metadata mismatch: evidence=%v outcome=%+v successors=%v", definition.Ref, definition.RequiredEvidenceKinds, definition.OutcomeSchema, definition.CompositionRules.AllowedSuccessorWorkKinds)
		}
		if len(definition.RigorRules) != 1 || definition.RigorRules[0].Maturity != "prototype" || definition.RigorRules[0].AudienceBand != "internal" || !reflect.DeepEqual(definition.RigorRules[0].RequiredEvidenceKinds, []EvidenceKind{EvidenceVerification}) {
			t.Fatalf("%s rigor metadata mismatch: %+v", definition.Ref, definition.RigorRules)
		}
		for _, action := range definition.ActionDefinitions {
			if expectedConsequence, ok := wantDefinition.external[action.ID]; ok && action.Consequence != expectedConsequence {
				t.Fatalf("%s action %s consequence=%s want %s", definition.Ref, action.ID, action.Consequence, expectedConsequence)
			}
			if expectedConsequence, ok := wantDefinition.cross[action.ID]; ok && action.Consequence != expectedConsequence {
				t.Fatalf("%s action %s consequence=%s want %s", definition.Ref, action.ID, action.Consequence, expectedConsequence)
			}
		}
	}
}

func TestBuiltinWorkflowResolverReturnsTheShippedDefinition(t *testing.T) {
	t.Parallel()
	registry := NewBuiltinWorkflowRegistry()
	for _, latest := range BuiltinWorkflowDefinitions() {
		registered, ok := registry.Current(latest.Ref)
		if !ok {
			t.Fatalf("%s definition is not registered", latest.Ref)
		}
		resolved, err := BuiltinWorkflowDefinitionForRef(latest.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Definition.Ref != registered.Definition.Ref || resolved.Digest != registered.Digest {
			t.Fatalf("resolver for %s = %s, want %s", latest.Ref, resolved.Digest, registered.Digest)
		}
		// TestCurrentJoinComposesTheWorkerFailureRecordWithTheDispatchPair
		// owns which steps compose the worker pair (CD-0242 D5).
		for _, action := range latest.ActionDefinitions {
			if action.ID != "accept_worker_result" {
				continue
			}
			// CD-0198 D4: the delivery-accept versions add the optional
			// delivery pair after the required attempt identity. Size-capable
			// heads also admit supplemental path classifications.
			fields := action.Payload.Fields
			want := []WorkflowPayloadField{
				{Name: "attempt_id", ValueType: PayloadRef, Required: true},
				{Name: "attempt_epoch", ValueType: PayloadInteger, Required: true},
			}
			if len(fields) >= 4 {
				want = append(want,
					WorkflowPayloadField{Name: "delivery_artifact", ValueType: PayloadRef},
					WorkflowPayloadField{Name: "delivery_state", ValueType: PayloadString, Enum: []string{"asserted"}},
				)
			}
			if len(fields) != len(want) {
				t.Fatalf("%s acceptance payload has %d fields, want %d", latest.Ref, len(fields), len(want))
			}
			for index, field := range want {
				got := fields[index]
				if got.Name != field.Name || got.ValueType != field.ValueType || got.Required != field.Required || got.ItemRef != field.ItemRef || strings.Join(got.Enum, ",") != strings.Join(field.Enum, ",") {
					t.Fatalf("%s acceptance payload field %d = %+v, want %+v", latest.Ref, index, got, field)
				}
			}
		}
	}
}

func TestWorkflowDefinitionValidationRejectsMalformedGraphsAndMetadataDrift(t *testing.T) {
	t.Parallel()
	base := BuiltinWorkflowDefinitions()[0]
	cases := []struct {
		name   string
		mutate func(*WorkflowDefinition)
	}{
		{"missing-start", func(definition *WorkflowDefinition) { definition.StepGraph.StartStep = "missing" }},
		{"missing-terminal", func(definition *WorkflowDefinition) { definition.StepGraph.TerminalSteps = []string{"missing"} }},
		{"missing-edge-endpoint", func(definition *WorkflowDefinition) { definition.StepGraph.Edges[0].To = "missing" }},
		{"non-retry-cycle", func(definition *WorkflowDefinition) {
			definition.StepGraph.Edges = append(definition.StepGraph.Edges, WorkflowEdge{From: "release", To: "proposal", Kind: WorkflowEdgeForward})
		}},
		{"unknown-step-action", func(definition *WorkflowDefinition) {
			definition.StepGraph.Steps[0].Actions = []string{"unknown_action"}
		}},
		{"missing-action-definition", func(definition *WorkflowDefinition) {
			definition.ActionDefinitions = definition.ActionDefinitions[:len(definition.ActionDefinitions)-1]
		}},
		{"invalid-evidence", func(definition *WorkflowDefinition) {
			definition.RequiredEvidenceKinds = []EvidenceKind{"unregistered"}
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			definition := cloneWorkflowDefinition(base)
			testCase.mutate(&definition)
			if err := ValidateWorkflowDefinition(definition); err == nil {
				t.Fatal("malformed definition was accepted")
			}
		})
	}
}

func TestWorkflowDefinitionCanonicalDigestAndDriftProtection(t *testing.T) {
	t.Parallel()
	definition := BuiltinWorkflowDefinitions()[0]
	first, err := CanonicalWorkflowDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalWorkflowDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("canonical definition encoding is not deterministic")
	}
	digest, err := WorkflowDefinitionDigest(definition)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewWorkflowDefinitionRegistry()
	registered, err := registry.Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	if registered.Digest != digest {
		t.Fatalf("registered digest = %q, computed = %q", registered.Digest, digest)
	}
	if err := registry.Verify(definition.Ref, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("stored digest drift was accepted")
	}
}

func TestWorkflowDefinitionCanonicalEncodingMatchesPublicFixture(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "workflow-engine.fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Schema   string          `json:"schema"`
			Valid    bool            `json:"valid"`
			Instance json.RawMessage `json:"instance"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 || fixture.Cases[0].Schema != "workflow-definition.schema.json" || !fixture.Cases[0].Valid {
		t.Fatal("definition fixture is missing")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(fixture.Cases[0].Instance, &object); err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := json.Unmarshal(object["digest"], &digest); err != nil {
		t.Fatal(err)
	}
	wantDigest := digest
	delete(object, "digest")
	delete(object, "schema_version")
	definitionJSON, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	var definition WorkflowDefinition
	if err := json.Unmarshal(definitionJSON, &definition); err != nil {
		t.Fatal(err)
	}
	gotDigest, err := WorkflowDefinitionDigest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("fixture digest = %s, computed = %s", wantDigest, gotDigest)
	}
}

func TestWorkflowCapabilityVocabularyMatchesPublicSchema(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "workflow-definition.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Capabilities struct {
				UniqueItems bool `json:"uniqueItems"`
				MaxItems    int  `json:"maxItems"`
				Items       struct {
					Enum []WorkflowCapability `json:"enum"`
				} `json:"items"`
			} `json:"capabilities"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	capabilities := schema.Properties.Capabilities
	if !slices.Equal(capabilities.Items.Enum, workflowCapabilityNames) || !capabilities.UniqueItems || capabilities.MaxItems != len(workflowCapabilityNames) {
		t.Fatalf("public capability vocabulary/bounds differ from the canonical registry: %+v", capabilities)
	}
}

func TestWorkerPacketSchemaMatchesCurrentDispatchFormat(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-tool-surface-payloads.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var packet struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema.Defs["worker_packet"], &packet); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(packet.Properties["schema_version"].Enum, []string{WorkerPacketSchemaVersion}) {
		t.Fatalf("current packet schema differs from dispatch format %q", WorkerPacketSchemaVersion)
	}
	if _, exists := packet.Properties["lane_version"]; exists {
		t.Fatal("current packet carries a retired lane counter")
	}
}

func TestWorkflowPredicateDecoderIsClosedAndRejectsTrailingData(t *testing.T) {
	t.Parallel()
	valid := []byte(`{"kind":"exists","surface":"git:tree","subjects":["path:cmd/concord"]}`)
	predicate, err := DecodeWorkflowPredicate(valid)
	if err != nil {
		t.Fatal(err)
	}
	if predicate.Kind != PredicateExists {
		t.Fatalf("predicate kind = %q, want %q", predicate.Kind, PredicateExists)
	}
	for _, input := range []string{
		`{"kind":"exists","surface":"git:tree","subjects":["path:x"],"op":"not"}`,
		`{"kind":"expression","surface":"git:tree","subjects":["path:x"]}`,
		`{"kind":"exists","surface":"git:tree","subjects":["path:x"]} {}`,
		`{"kind":"exists","surface":"git:tree","subjects":["path:x"],"predicate":{"kind":"absent"}}`,
		`{"kind":"exists","surface":"git:tree","subjects":"path:x"}`,
		`{"kind":1,"surface":"git:tree","subjects":["path:x"]}`,
		`{"kind":"exists","surface":"git:tree","subjects":["path:x"],"kind":"absent"}`,
		`{"kind":"outcome","allowed":["no_change"],"decision_record":{"question":"q","question":"q","options_considered":["a"]}}`,
		`{"kind":"outcome","allowed":["accepted_decision"],"decision_record":{"question":"q","options_considered":["a"],"decision":"accepted_decision","rationale":"r","consequences":["c"],"inputs":["i"],"poc_findings":"none","supersedes":null,"superseded_by":null,"unknowns":[],"required_to_decide":[],"reviewer_actor_ref":"actor:` + strings.Repeat("a", 64) + `","extra":true}}`,
	} {
		if _, err := DecodeWorkflowPredicate([]byte(input)); err == nil {
			t.Fatalf("invalid predicate accepted: %s", input)
		}
	}
	if _, err := DecodeWorkflowPredicate([]byte(`{"kind":"exists","surface":"git:tree","subjects":["` + strings.Repeat("a", maxWorkflowPredicateBytes) + `"]}`)); err == nil {
		t.Fatal("oversized predicate was accepted")
	}
}

func TestWorkflowActorValidation(t *testing.T) {
	t.Parallel()
	if err := ValidateWorkflowActor(WorkflowActor{PrincipalRef: "p1", ClientRef: "c1", AgentRef: "a1", SessionRef: "s1", ActorClass: ActorAgent}); err != nil {
		t.Fatal(err)
	}
}

// The pinned definition, not the generic outcome union, decides which outcome
// tokens a predicate may carry: live research refuses the retired
// architecture-spike token.
func TestResearchPredicateRefusesTheArchitectureSpikeToken(t *testing.T) {
	t.Parallel()
	research, err := BuiltinWorkflowDefinitionForRef("workflow.research")
	if err != nil {
		t.Fatal(err)
	}
	spikeToken := OutcomePredicate{Kind: PredicateOutcome, Allowed: []string{"accepted_decision"}}
	if err := ValidateWorkflowPredicateForDefinition(research.Definition, spikeToken); err == nil {
		t.Fatal("research definition accepted an architecture-spike token")
	}
}

func TestWorkflowPredicateRoundTripUsesStrictShape(t *testing.T) {
	t.Parallel()
	predicate := OutcomePredicate{Kind: PredicateOutcome, Allowed: []string{"no_change"}}
	raw, err := json.Marshal(predicate)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeWorkflowPredicate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != PredicateOutcome || len(decoded.Allowed) != 1 || decoded.Allowed[0] != "no_change" {
		t.Fatalf("round trip = %+v", decoded)
	}
}

func TestWorkflowDecisionRecordKeepsReviewerAndOutcomeGuards(t *testing.T) {
	t.Parallel()
	valid := `{"kind":"outcome","allowed":["accepted_decision"],"decision_record":{"question":"q","options_considered":["a"],"decision":"accepted_decision","rationale":"r","consequences":["c"],"inputs":["i"],"poc_findings":"none","supersedes":null,"superseded_by":null,"unknowns":[],"required_to_decide":[],"reviewer_actor_ref":"actor:` + strings.Repeat("a", 64) + `"}}`
	predicate, err := DecodeWorkflowPredicate([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(predicate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeWorkflowPredicate(raw); err != nil {
		t.Fatalf("decision record round trip: %v", err)
	}
	for _, invalid := range []string{
		strings.Replace(valid, `"reviewer_actor_ref":"actor:`+strings.Repeat("a", 64)+`"`, `"reviewer_actor_ref":"unverified"`, 1),
		strings.Replace(valid, `"allowed":["accepted_decision"]`, `"allowed":["insufficient_evidence"]`, 1),
		strings.Replace(valid, `"poc_findings":"none"`, `"poc_findings":""`, 1),
		strings.Replace(valid, `"options_considered":["a"]`, `"options_considered":["a","a"]`, 1),
	} {
		if _, err := DecodeWorkflowPredicate([]byte(invalid)); err == nil {
			t.Fatalf("invalid decision record accepted: %s", invalid)
		}
	}
}

func TestWorkflowDefinitionPinPreflightFailsClosedOnDrift(t *testing.T) {
	t.Parallel()
	registry := NewWorkflowDefinitionRegistry()
	definition := BuiltinWorkflowDefinitions()[0]
	registered, err := registry.Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: definition.Ref, Digest: registered.Digest}); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: definition.Ref, Digest: "sha256:" + strings.Repeat("f", 64)}); err == nil {
		t.Fatal("drifted workflow pin was accepted")
	}
	s := openTemp(t)
	seedWork(t, s, "drift-work")
	badEvent := workflowEvent("drift-definition", WorkflowDefinitionSelected, "drift-work", map[string]any{
		"work_id": "drift-work", "expected_version": 2, "resulting_version": 3, "ref": definition.Ref,
		"digest": "sha256:" + strings.Repeat("f", 64), "work_kind": string(definition.WorkKind),
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{badEvent}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "drift-work"): 2}}); err == nil {
		t.Fatal("drifted definition selection was accepted")
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='drift-work'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("drifted selection changed work version to %d", version)
	}
	var instances int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id='drift-work'`).Scan(&instances); err != nil {
		t.Fatal(err)
	}
	if instances != 0 {
		t.Fatalf("drifted selection created %d workflow instances", instances)
	}
}

func TestWorkflowActionAuthorizationPreflightsBeforeCallbackOnRegistryDrift(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "preflight-work")
	definition := BuiltinWorkflowDefinitions()[0]
	registered, err := BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	event := workflowEvent("preflight-definition", WorkflowDefinitionSelected, "preflight-work", map[string]any{
		"work_id": "preflight-work", "expected_version": 2, "resulting_version": 3,
		"ref": definition.Ref, "digest": registered.Digest, "work_kind": string(definition.WorkKind),
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "preflight-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	called := false
	err = AuthorizeWorkflowActionAtBoundaryTx(context.Background(), s, NewWorkflowDefinitionRegistry(), WorkflowActionPreflightRequest{WorkID: "preflight-work", StepID: "proposal", ActionID: "record_proposal"}, func(*Transaction) error {
		called = true
		return nil
	}, func(*Transaction) error { return nil })
	if err == nil || called {
		t.Fatalf("registry drift did not fail before authorization: err=%v called=%v", err, called)
	}
}
