package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBuiltinWorkflowRegistryHasTheSevenContractFamilies(t *testing.T) {
	defs := BuiltinWorkflowDefinitions()
	if len(defs) != 7 {
		t.Fatalf("built-in definition count = %d, want 7", len(defs))
	}
	registry := NewWorkflowDefinitionRegistry()
	wantSteps := map[string][]string{
		"workflow.implementation":     {"proposal", "discovery", "design", "planning", "execution", "acceptance", "release"},
		"workflow.break_fix":          {"reproduce", "diagnose", "repair", "verify", "complete"},
		"workflow.research":           {"frame", "investigate", "findings", "conclude", "complete"},
		"workflow.architecture_spike": {"frame", "research", "options", "poc_optional", "decision_record", "review", "acceptance", "complete"},
		"workflow.ops_runbook":        {"plan", "approval", "execute", "health", "rollback_optional", "cleanup", "complete"},
		"workflow.static_analysis":    {"scope", "analyze", "report", "review", "complete"},
		"workflow.generic_one_off":    {"define", "execute", "verify", "complete"},
	}
	wantActions := map[string][]string{
		"workflow.implementation":     {"record_proposal", "record_discovery", "record_design", "approve_contract", "start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor", "record_verdict", "confirm_premise", "complete", "checkpoint_context", "cross_context_boundary", "accept_worker_result"},
		"workflow.break_fix":          {"record_reproduction", "record_root_cause", "start_repair", "checkpoint_repair", "bind_evidence", "link_successor", "record_verdict", "confirm_premise", "complete", "checkpoint_context", "cross_context_boundary", "accept_worker_result"},
		"workflow.research":           {"frame_research", "approve_contract", "record_finding", "revise_candidates", "bind_evidence", "record_report", "link_successor", "record_conclusion", "record_verdict", "confirm_premise", "complete", "checkpoint_context", "cross_context_boundary"},
		"workflow.architecture_spike": {"frame_question", "approve_contract", "record_research", "bind_evidence", "record_option", "start_poc", "checkpoint_poc", "discard_poc", "record_decision", "record_verdict", "accept_decision", "confirm_premise", "complete", "checkpoint_context", "cross_context_boundary", "accept_worker_result"},
		"workflow.ops_runbook":        {"approve_contract", "approve_operation", "start_run", "checkpoint_run", "bind_evidence", "add_condition", "resolve_condition", "cancel_condition", "record_health", "record_verdict", "rollback_run", "cleanup_run", "confirm_premise", "complete", "checkpoint_context", "cross_context_boundary", "accept_worker_result"},
		"workflow.static_analysis":    {"approve_contract", "declare_scope", "run_analysis", "checkpoint_analysis", "record_report", "bind_evidence", "record_verdict", "confirm_premise", "complete", "checkpoint_context", "cross_context_boundary", "accept_worker_result"},
		"workflow.generic_one_off":    {"approve_contract", "start_action", "checkpoint_action", "bind_evidence", "link_successor", "record_verdict", "confirm_premise", "complete", "checkpoint_context", "cross_context_boundary", "accept_worker_result"},
	}
	wantTerminals := map[string]string{
		"workflow.implementation": "release", "workflow.break_fix": "complete", "workflow.research": "complete", "workflow.architecture_spike": "complete", "workflow.ops_runbook": "complete", "workflow.static_analysis": "complete", "workflow.generic_one_off": "complete",
	}
	wantEdges := map[string][]WorkflowEdge{
		"workflow.implementation":     {{"proposal", "discovery", WorkflowEdgeForward}, {"discovery", "design", WorkflowEdgeForward}, {"design", "planning", WorkflowEdgeForward}, {"planning", "execution", WorkflowEdgeForward}, {"execution", "acceptance", WorkflowEdgeForward}, {"acceptance", "release", WorkflowEdgeForward}, {"execution", "execution", WorkflowEdgeRetry}},
		"workflow.break_fix":          {{"reproduce", "diagnose", WorkflowEdgeForward}, {"diagnose", "repair", WorkflowEdgeForward}, {"repair", "verify", WorkflowEdgeForward}, {"verify", "complete", WorkflowEdgeForward}, {"repair", "repair", WorkflowEdgeRetry}},
		"workflow.research":           {{"frame", "investigate", WorkflowEdgeForward}, {"investigate", "findings", WorkflowEdgeForward}, {"findings", "conclude", WorkflowEdgeForward}, {"conclude", "complete", WorkflowEdgeForward}},
		"workflow.architecture_spike": {{"frame", "research", WorkflowEdgeForward}, {"research", "options", WorkflowEdgeForward}, {"options", "poc_optional", WorkflowEdgeForward}, {"poc_optional", "decision_record", WorkflowEdgeForward}, {"decision_record", "review", WorkflowEdgeForward}, {"review", "acceptance", WorkflowEdgeForward}, {"acceptance", "complete", WorkflowEdgeForward}, {"options", "decision_record", WorkflowEdgeOptional}, {"poc_optional", "poc_optional", WorkflowEdgeRetry}},
		"workflow.ops_runbook":        {{"plan", "approval", WorkflowEdgeForward}, {"approval", "execute", WorkflowEdgeForward}, {"execute", "health", WorkflowEdgeForward}, {"health", "rollback_optional", WorkflowEdgeForward}, {"rollback_optional", "cleanup", WorkflowEdgeForward}, {"cleanup", "complete", WorkflowEdgeForward}, {"health", "cleanup", WorkflowEdgeOptional}, {"execute", "execute", WorkflowEdgeRetry}},
		"workflow.static_analysis":    {{"scope", "analyze", WorkflowEdgeForward}, {"analyze", "report", WorkflowEdgeForward}, {"report", "review", WorkflowEdgeForward}, {"review", "complete", WorkflowEdgeForward}, {"analyze", "analyze", WorkflowEdgeRetry}},
		"workflow.generic_one_off":    {{"define", "execute", WorkflowEdgeForward}, {"execute", "verify", WorkflowEdgeForward}, {"verify", "complete", WorkflowEdgeForward}, {"execute", "execute", WorkflowEdgeRetry}},
	}
	for _, definition := range defs {
		registered, err := registry.Register(definition)
		if err != nil {
			t.Fatalf("register %s: %v", definition.Ref, err)
		}
		if registered.Digest == "" || !strings.HasPrefix(registered.Digest, "sha256:") {
			t.Fatalf("%s has no computed digest", definition.Ref)
		}
		if err := registry.Verify(definition.Ref, definition.Version, registered.Digest); err != nil {
			t.Fatalf("verify %s: %v", definition.Ref, err)
		}
		stored, ok := registry.Lookup(definition.Ref, definition.Version)
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
			if !containsString(step.Actions, "checkpoint_context") || !containsString(step.Actions, "cross_context_boundary") {
				t.Fatalf("%s step %s does not expose both continuity actions", definition.Ref, step.ID)
			}
		}
		for _, action := range definition.ActionDefinitions {
			if action.ID != "checkpoint_context" && action.ID != "cross_context_boundary" {
				continue
			}
			if action.Approval != ActionApprovalNone || len(action.Payload.Fields) == 0 {
				t.Fatalf("%s continuity action is not closed and unapproved: %+v", definition.Ref, action)
			}
		}
		if !reflect.DeepEqual(definition.StepGraph.TerminalSteps, []string{wantTerminals[definition.Ref]}) {
			t.Fatalf("%s terminal steps = %v, want %q", definition.Ref, definition.StepGraph.TerminalSteps, wantTerminals[definition.Ref])
		}
		if !reflect.DeepEqual(definition.StepGraph.Edges, wantEdges[definition.Ref]) {
			t.Fatalf("%s edges = %v, want %v", definition.Ref, definition.StepGraph.Edges, wantEdges[definition.Ref])
		}
		terminal := definition.StepGraph.Steps[len(definition.StepGraph.Steps)-1]
		if terminal.ID != wantTerminals[definition.Ref] || !containsString(terminal.Actions, "complete") {
			t.Fatalf("%s terminal completion verb is not declared: %+v", definition.Ref, terminal)
		}
		if len(definition.RequiredEvidenceKinds) == 0 || len(definition.RigorRules) == 0 || !definition.CompositionRules.ForwardLinkOnly {
			t.Fatalf("%s omitted artifact/rigor/composition requirements", definition.Ref)
		}
		for _, action := range definition.ActionDefinitions {
			if action.ID == "" || action.Consequence == "" || action.Approval == "" || action.Payload.Fields == nil {
				t.Fatalf("%s has incomplete action metadata: %+v", definition.Ref, action)
			}
		}
		for _, edge := range definition.StepGraph.Edges {
			if edge.From == "" || edge.To == "" || edge.Kind == "" {
				t.Fatalf("%s has incomplete edge metadata: %+v", definition.Ref, edge)
			}
		}
	}
	if _, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 1); !ok {
		t.Fatal("legacy implementation definition was not registered")
	}
	if _, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 3); !ok {
		t.Fatal("latest implementation definition was not registered")
	}
}

func TestBuiltinWorkflowDefinitionsMatchExactPhaseBContractMetadata(t *testing.T) {
	type expected struct {
		evidence []EvidenceKind
		outcome  WorkflowOutcomeSchema
		success  []WorkKind
		approval map[string]ActionApproval
		external map[string]ActionConsequence
		cross    map[string]ActionConsequence
	}
	want := map[string]expected{
		"workflow.implementation":     {evidence: []EvidenceKind{EvidenceVerification, EvidenceReview}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}}, success: []WorkKind{WorkKindBreakFix, WorkKindResearch}, approval: map[string]ActionApproval{"approve_contract": ActionApprovalRequired, "confirm_premise": ActionApprovalRequired}, external: map[string]ActionConsequence{"start_execution": ActionExternalEffect, "checkpoint_execution": ActionExternalEffect}},
		"workflow.break_fix":          {evidence: []EvidenceKind{EvidenceVerification}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateAbsent, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}}, success: []WorkKind{WorkKindImplementation, WorkKindResearch}, approval: map[string]ActionApproval{"confirm_premise": ActionApprovalRequired}, external: map[string]ActionConsequence{"start_repair": ActionExternalEffect, "checkpoint_repair": ActionExternalEffect}},
		"workflow.research":           {evidence: []EvidenceKind{EvidenceArtifact}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"no_change", "resolved", "report_recorded"}}, success: []WorkKind{WorkKindBreakFix, WorkKindArchitectureSpike, WorkKindStaticAnalysis}, approval: map[string]ActionApproval{"approve_contract": ActionApprovalRequired, "confirm_premise": ActionApprovalRequired}, cross: map[string]ActionConsequence{"record_finding": ActionCrossAuthority}},
		"workflow.architecture_spike": {evidence: []EvidenceKind{EvidenceReview, EvidenceApproval, EvidenceArtifact}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"accepted_decision", "insufficient_evidence"}, DecisionRecordRequired: true}, success: []WorkKind{WorkKindImplementation, WorkKindResearch, WorkKindStaticAnalysis}, approval: map[string]ActionApproval{"approve_contract": ActionApprovalRequired, "accept_decision": ActionApprovalRequired, "confirm_premise": ActionApprovalRequired}, external: map[string]ActionConsequence{"start_poc": ActionExternalEffect, "checkpoint_poc": ActionExternalEffect}, cross: map[string]ActionConsequence{"record_research": ActionCrossAuthority, "record_option": ActionCrossAuthority}},
		"workflow.ops_runbook":        {evidence: []EvidenceKind{EvidenceApproval, EvidenceNativeRun}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}}, success: []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch}, approval: map[string]ActionApproval{"approve_contract": ActionApprovalRequired, "approve_operation": ActionApprovalRequired, "confirm_premise": ActionApprovalRequired}, external: map[string]ActionConsequence{"start_run": ActionExternalEffect, "checkpoint_run": ActionExternalEffect, "rollback_run": ActionExternalEffect}, cross: map[string]ActionConsequence{"record_health": ActionCrossAuthority}},
		"workflow.static_analysis":    {evidence: []EvidenceKind{EvidenceArtifact, EvidenceReview}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}}, success: []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch}, approval: map[string]ActionApproval{"approve_contract": ActionApprovalRequired, "confirm_premise": ActionApprovalRequired}, external: map[string]ActionConsequence{"run_analysis": ActionExternalEffect, "checkpoint_analysis": ActionExternalEffect}},
		"workflow.generic_one_off":    {evidence: []EvidenceKind{EvidenceArtifact}, outcome: WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateOutcome, PredicateCheck}, AllowedOutcomeTokens: []string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}}, success: []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch, WorkKindArchitectureSpike, WorkKindOpsRunbook, WorkKindStaticAnalysis, WorkKindGenericOneOff}, approval: map[string]ActionApproval{"approve_contract": ActionApprovalRequired, "confirm_premise": ActionApprovalRequired}, external: map[string]ActionConsequence{"start_action": ActionExternalEffect, "checkpoint_action": ActionExternalEffect}},
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
			if expectedApproval, ok := wantDefinition.approval[action.ID]; ok && action.Approval != expectedApproval {
				t.Fatalf("%s action %s approval=%s want %s", definition.Ref, action.ID, action.Approval, expectedApproval)
			}
			if expectedConsequence, ok := wantDefinition.external[action.ID]; ok && action.Consequence != expectedConsequence {
				t.Fatalf("%s action %s consequence=%s want %s", definition.Ref, action.ID, action.Consequence, expectedConsequence)
			}
			if expectedConsequence, ok := wantDefinition.cross[action.ID]; ok && action.Consequence != expectedConsequence {
				t.Fatalf("%s action %s consequence=%s want %s", definition.Ref, action.ID, action.Consequence, expectedConsequence)
			}
		}
	}
}

func TestBuiltinWorkflowHistoryPinsV1V2AndLatestV3WorkerAcceptance(t *testing.T) {
	registry := NewBuiltinWorkflowRegistry()
	for _, latest := range BuiltinWorkflowDefinitions() {
		if latest.Version != 3 {
			t.Fatalf("latest %s version=%d, want 3", latest.Ref, latest.Version)
		}
		v1, ok := registry.Lookup(latest.Ref, 1)
		if !ok {
			t.Fatalf("%s v1 definition is not registered", latest.Ref)
		}
		v2, ok := registry.Lookup(latest.Ref, 2)
		if !ok {
			t.Fatalf("%s v2 definition is not registered", latest.Ref)
		}
		if v1.Digest == v2.Digest {
			t.Fatalf("%s v1 and v2 unexpectedly share digest %q", latest.Ref, v1.Digest)
		}
		v3, ok := registry.Lookup(latest.Ref, 3)
		if !ok {
			t.Fatalf("%s v3 definition is not registered", latest.Ref)
		}
		if v2.Digest == v3.Digest {
			t.Fatalf("%s v2 and v3 unexpectedly share digest %q", latest.Ref, v2.Digest)
		}
		resolved, err := BuiltinWorkflowDefinitionForRef(latest.Ref)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.Definition.Version != 3 || resolved.Digest != v3.Digest {
			t.Fatalf("latest resolver for %s = v%d %s, want v3 %s", latest.Ref, resolved.Definition.Version, resolved.Digest, v3.Digest)
		}
		for _, step := range latest.StepGraph.Steps {
			hasAcceptance := containsString(step.Actions, "accept_worker_result")
			if (step.Kind == WorkflowStepExternalEffect) != hasAcceptance {
				t.Fatalf("%s step %s acceptance declaration=%t for kind %s", latest.Ref, step.ID, hasAcceptance, step.Kind)
			}
		}
		for _, action := range latest.ActionDefinitions {
			if action.ID != "accept_worker_result" {
				continue
			}
			if len(action.Payload.Fields) != 2 || !action.Payload.Fields[0].Required || action.Payload.Fields[0].Name != "attempt_id" || action.Payload.Fields[0].ValueType != PayloadRef || !action.Payload.Fields[1].Required || action.Payload.Fields[1].Name != "attempt_epoch" || action.Payload.Fields[1].ValueType != PayloadInteger {
				t.Fatalf("%s acceptance payload is not closed: %+v", latest.Ref, action.Payload.Fields)
			}
		}
	}
}

func TestWorkflowDefinitionValidationRejectsMalformedGraphsAndMetadataDrift(t *testing.T) {
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
	if err := registry.Verify(definition.Ref, definition.Version, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("stored digest drift was accepted")
	}
}

func TestWorkflowDefinitionCanonicalEncodingMatchesPublicFixture(t *testing.T) {
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
	wantDigest := string(object["digest"])
	var digest string
	if err := json.Unmarshal(object["digest"], &digest); err != nil {
		t.Fatal(err)
	}
	wantDigest = digest
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

func TestWorkflowPredicateDecoderIsClosedAndRejectsTrailingData(t *testing.T) {
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
		`{"kind":"outcome","allowed":["accepted_decision"],"decision_record":{"question":"q","options_considered":["a"],"decision":"accepted_decision","rationale":"r","consequences":["c"],"inputs":["i"],"poc_findings":"none","supersedes":null,"superseded_by":null,"unknowns":[],"required_to_decide":[],"reviewer_actor_ref":"actor:` + strings.Repeat("a", 64) + `","operator_approval_ref":"approval:a","extra":true}}`,
	} {
		if _, err := DecodeWorkflowPredicate([]byte(input)); err == nil {
			t.Fatalf("invalid predicate accepted: %s", input)
		}
	}
	if _, err := DecodeWorkflowPredicate([]byte(`{"kind":"exists","surface":"git:tree","subjects":["` + strings.Repeat("a", maxWorkflowPredicateBytes) + `"]}`)); err == nil {
		t.Fatal("oversized predicate was accepted")
	}
}

func TestWorkflowOutcomeStrengthAndActorDistinctness(t *testing.T) {
	approved := OutcomePredicate{Kind: PredicateExists, Surface: "git:tree", Subjects: []string{"path:a"}}
	equal, err := CompareWorkflowPredicates(approved, approved)
	if err != nil || equal != StrengthStrongerOrEqual {
		t.Fatalf("equal exists strength = %q, err=%v", equal, err)
	}
	delivered := OutcomePredicate{Kind: PredicateExists, Surface: "git:tree", Subjects: []string{"path:a", "path:b"}}
	strength, err := CompareWorkflowPredicates(approved, delivered)
	if err != nil || strength != StrengthStrongerOrEqual {
		t.Fatalf("exists strength = %q, err=%v", strength, err)
	}
	weak := OutcomePredicate{Kind: PredicateExists, Surface: "git:tree", Subjects: []string{"path:a"}}
	weak.Subjects = []string{"path:b"}
	strength, err = CompareWorkflowPredicates(approved, weak)
	if err != nil || strength != StrengthIncomparable {
		t.Fatalf("different exists identity strength = %q, err=%v", strength, err)
	}
	weaker := OutcomePredicate{Kind: PredicateExists, Surface: "git:tree", Subjects: []string{"path:a", "path:b"}}
	strength, err = CompareWorkflowPredicates(weaker, approved)
	if err != nil || strength != StrengthWeaker {
		t.Fatalf("weaker exists strength = %q, err=%v", strength, err)
	}
	approvedOutcome := OutcomePredicate{Kind: PredicateOutcome, Allowed: []string{"no_change"}}
	unknownOutcome := OutcomePredicate{Kind: PredicateOutcome, Allowed: []string{"resolved"}}
	strength, err = CompareWorkflowPredicates(approvedOutcome, unknownOutcome)
	if err != nil || strength != StrengthWeaker {
		t.Fatalf("out-of-set outcome strength = %q, err=%v", strength, err)
	}
	if err := ValidateWorkflowActor(WorkflowActor{PrincipalRef: "p1", ClientRef: "c1", AgentRef: "a1", SessionRef: "s1", ActorClass: ActorAgent}); err != nil {
		t.Fatal(err)
	}
	executing := WorkflowActor{PrincipalRef: "p1", ClientRef: "c1", AgentRef: "a1", SessionRef: "s1", ActorClass: ActorAgent}
	verdict := executing
	if err := ValidateDistinctWorkflowActors(executing, verdict, false); err == nil {
		t.Fatal("same executing and verdict actor accepted")
	}
	operatorVerdict := executing
	operatorVerdict.ActorClass = ActorOperator
	if err := ValidateDistinctWorkflowActors(executing, operatorVerdict, false); err != nil {
		t.Fatalf("operator verdict was not distinct from agent executor: %v", err)
	}
}

func TestWorkflowOutcomeEvaluationUsesPinnedDefinitionAndAuthoritativeCheckStrength(t *testing.T) {
	_, registry, pin := phaseBDefinition(t, 0)
	approved := OutcomePredicate{Kind: PredicateCheck, CheckRef: "check:workflow-proof", ImmutableSubjectRef: "commit:aaaaaaaa", ExpectedResult: "pass"}
	for _, testCase := range []struct {
		name         string
		strength     WorkflowStrength
		satisfied    bool
		incomparable bool
	}{
		{"stronger", StrengthStrongerOrEqual, true, false},
		{"weaker", StrengthWeaker, false, false},
		{"incomparable", StrengthIncomparable, false, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := EvaluateWorkflowOutcome(approved, approved, WorkflowOutcomeEvaluationContext{Registry: registry, DefinitionPin: pin, Checks: phaseBCheckResolver{strength: testCase.strength}})
			if err != nil || result.Satisfied != testCase.satisfied || result.IncomparableWithApproved != testCase.incomparable {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	if _, err := EvaluateWorkflowOutcome(approved, approved, WorkflowOutcomeEvaluationContext{}); err == nil {
		t.Fatal("generic evaluation path without registry was accepted")
	}
	_, researchRegistry, researchPin := phaseBDefinition(t, 2)
	spikeToken := OutcomePredicate{Kind: PredicateOutcome, Allowed: []string{"accepted_decision"}}
	if _, err := EvaluateWorkflowOutcome(spikeToken, spikeToken, WorkflowOutcomeEvaluationContext{Registry: researchRegistry, DefinitionPin: researchPin}); err == nil {
		t.Fatal("research definition accepted an architecture-spike token")
	}
	_, spikeRegistry, spikePin := phaseBDefinition(t, 3)
	missingRecord := OutcomePredicate{Kind: PredicateOutcome, Allowed: []string{"accepted_decision"}}
	if _, err := EvaluateWorkflowOutcome(missingRecord, missingRecord, WorkflowOutcomeEvaluationContext{Registry: spikeRegistry, DefinitionPin: spikePin}); err == nil {
		t.Fatal("architecture spike accepted a missing decision record")
	}
}

func TestWorkflowPredicateRoundTripUsesStrictShape(t *testing.T) {
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

func TestWorkflowDefinitionPinPreflightFailsClosedOnDrift(t *testing.T) {
	registry := NewWorkflowDefinitionRegistry()
	definition := BuiltinWorkflowDefinitions()[0]
	registered, err := registry.Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: definition.Ref, Version: definition.Version, Digest: registered.Digest}); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: definition.Ref, Version: definition.Version, Digest: "sha256:" + strings.Repeat("f", 64)}); err == nil {
		t.Fatal("drifted workflow pin was accepted")
	}
	if err := workflowStartPreflightWithRegistry(context.Background(), nil, registry, WorkflowStartRequest{WorkID: "work-alpha", Definition: WorkflowDefinitionPin{Ref: definition.Ref, Version: definition.Version, Digest: registered.Digest}, StepID: "proposal", ActionID: "record_proposal", Actor: WorkflowActor{PrincipalRef: "principal:operator", ClientRef: "client:concord-1", AgentRef: "agent:engineer", SessionRef: "session:one", ActorClass: ActorAgent}}); err != nil {
		t.Fatal(err)
	}
	s := openTemp(t)
	seedWork(t, s, "drift-work")
	badEvent := workflowEvent("drift-definition", WorkflowDefinitionSelected, "drift-work", map[string]any{
		"work_id": "drift-work", "expected_version": 2, "resulting_version": 3, "ref": definition.Ref, "version": definition.Version,
		"digest": "sha256:" + strings.Repeat("f", 64), "work_kind": string(definition.WorkKind),
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{badEvent}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "drift-work"): 2}}); err == nil {
		t.Fatal("drifted definition selection was accepted")
	}
	var version int64
	if err := s.DB().QueryRow(`SELECT version FROM work_items WHERE id='drift-work'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("drifted selection changed work version to %d", version)
	}
	var instances int
	if err := s.DB().QueryRow(`SELECT count(*) FROM workflow_instances WHERE work_id='drift-work'`).Scan(&instances); err != nil {
		t.Fatal(err)
	}
	if instances != 0 {
		t.Fatalf("drifted selection created %d workflow instances", instances)
	}
}

func TestWorkflowActionAuthorizationPreflightsBeforeCallbackOnRegistryDrift(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "preflight-work")
	definition := BuiltinWorkflowDefinitions()[0]
	registered, err := BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	event := workflowEvent("preflight-definition", WorkflowDefinitionSelected, "preflight-work", map[string]any{
		"work_id": "preflight-work", "expected_version": 2, "resulting_version": 3,
		"ref": definition.Ref, "version": definition.Version, "digest": registered.Digest, "work_kind": string(definition.WorkKind),
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "preflight-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	called := false
	err = AuthorizeWorkflowAction(context.Background(), s, NewWorkflowDefinitionRegistry(), WorkflowActionPreflightRequest{WorkID: "preflight-work", StepID: "proposal", ActionID: "record_proposal"}, func() error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("registry drift did not fail before authorization: err=%v called=%v", err, called)
	}
}

type phaseBGroundTruth map[string]WorkflowGroundTruth

func (r phaseBGroundTruth) Resolve(_ string, subject string) (WorkflowGroundTruth, error) {
	state, ok := r[subject]
	if !ok {
		return "", workflowOutcomeFailure("test resolver was asked for an unregistered subject")
	}
	return state, nil
}

type phaseBCheckResolver struct{ strength WorkflowStrength }

func (r phaseBCheckResolver) Compare(string, string, string) (WorkflowStrength, error) {
	return r.strength, nil
}

func phaseBDefinition(t *testing.T, index int) (WorkflowDefinition, DefinitionRegistry, WorkflowDefinitionPin) {
	t.Helper()
	definition := BuiltinWorkflowDefinitions()[index]
	registry := NewWorkflowDefinitionRegistry()
	registered, err := registry.Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	return definition, registry, WorkflowDefinitionPin{Ref: definition.Ref, Version: definition.Version, Digest: registered.Digest}
}
