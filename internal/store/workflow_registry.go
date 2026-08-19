package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// These types are the code-owned representation of the v1 workflow-definition
// manifest. They deliberately contain no executable code or file path: a
// registered definition is data compiled into the binary.
type WorkKind string

const (
	WorkKindImplementation    WorkKind = "implementation"
	WorkKindBreakFix          WorkKind = "break_fix"
	WorkKindResearch          WorkKind = "research"
	WorkKindArchitectureSpike WorkKind = "architecture_spike"
	WorkKindOpsRunbook        WorkKind = "ops_runbook"
	WorkKindStaticAnalysis    WorkKind = "static_analysis"
	WorkKindGenericOneOff     WorkKind = "generic_one_off"
)

type WorkflowStepKind string

const (
	WorkflowStepInternalSQLite  WorkflowStepKind = "internal_sqlite"
	WorkflowStepCrossAuthority  WorkflowStepKind = "cross_authority"
	WorkflowStepExternalEffect  WorkflowStepKind = "external_effect"
	WorkflowStepHumanCheckpoint WorkflowStepKind = "human_checkpoint"
)

type WorkflowEdgeKind string

const (
	WorkflowEdgeForward  WorkflowEdgeKind = "forward"
	WorkflowEdgeRetry    WorkflowEdgeKind = "retry"
	WorkflowEdgeOptional WorkflowEdgeKind = "optional"
	WorkflowEdgeFailure  WorkflowEdgeKind = "failure"
)

type EvidenceKind string

const (
	EvidenceVerification EvidenceKind = "verification"
	EvidenceReview       EvidenceKind = "review"
	EvidenceApproval     EvidenceKind = "approval"
	EvidenceCommit       EvidenceKind = "commit"
	EvidenceDurableNote  EvidenceKind = "durable_note"
	EvidenceNativeRun    EvidenceKind = "native_run"
	EvidenceArtifact     EvidenceKind = "artifact"
)

type ActionConsequence string

const (
	ActionInternalSQLite ActionConsequence = "internal_sqlite"
	ActionCrossAuthority ActionConsequence = "cross_authority"
	ActionExternalEffect ActionConsequence = "external_effect"
)

type ActionApproval string

const (
	ActionApprovalNone        ActionApproval = "none"
	ActionApprovalConditional ActionApproval = "conditional"
	ActionApprovalRequired    ActionApproval = "required"
)

type ActionExecutionMode string

const (
	ActionAdvance    ActionExecutionMode = "advance"
	ActionHold       ActionExecutionMode = "hold"
	ActionFenced     ActionExecutionMode = "fenced"
	ActionCheckpoint ActionExecutionMode = "checkpoint"
)

type PayloadValueType string

const (
	PayloadString     PayloadValueType = "string"
	PayloadInteger    PayloadValueType = "integer"
	PayloadBoolean    PayloadValueType = "boolean"
	PayloadRef        PayloadValueType = "ref"
	PayloadDigest     PayloadValueType = "digest"
	PayloadStringList PayloadValueType = "string_list"
)

type WorkflowPayloadField struct {
	Name      string           `json:"name"`
	ValueType PayloadValueType `json:"value_type"`
	Required  bool             `json:"required"`
	MinLength *int64           `json:"min_length,omitempty"`
	MaxLength *int64           `json:"max_length,omitempty"`
	MinItems  *int64           `json:"min_items,omitempty"`
	MaxItems  *int64           `json:"max_items,omitempty"`
	Minimum   *int64           `json:"minimum,omitempty"`
	Maximum   *int64           `json:"maximum,omitempty"`
}

type WorkflowPayloadDefinition struct {
	Fields []WorkflowPayloadField `json:"fields"`
}

type WorkflowActionDefinition struct {
	ID            string                    `json:"id"`
	Consequence   ActionConsequence         `json:"consequence"`
	Approval      ActionApproval            `json:"approval"`
	ExecutionMode ActionExecutionMode       `json:"execution_mode,omitempty"`
	Payload       WorkflowPayloadDefinition `json:"payload"`
}

type WorkflowStep struct {
	ID                    string           `json:"id"`
	Kind                  WorkflowStepKind `json:"kind"`
	Actions               []string         `json:"actions"`
	RequiredEvidenceKinds []EvidenceKind   `json:"required_evidence_kinds,omitempty"`
}

type WorkflowEdge struct {
	From string           `json:"from"`
	To   string           `json:"to"`
	Kind WorkflowEdgeKind `json:"kind"`
}

type WorkflowStepGraph struct {
	StartStep     string         `json:"start_step"`
	TerminalSteps []string       `json:"terminal_steps"`
	Steps         []WorkflowStep `json:"steps"`
	Edges         []WorkflowEdge `json:"edges"`
}

type WorkflowOutcomeSchema struct {
	DefaultKind            PredicateKind   `json:"default_kind"`
	AllowedKinds           []PredicateKind `json:"allowed_kinds"`
	AllowedOutcomeTokens   []string        `json:"allowed_outcome_tokens"`
	DecisionRecordRequired bool            `json:"decision_record_required"`
}

type WorkflowRigorRule struct {
	Maturity              string         `json:"maturity"`
	AudienceBand          string         `json:"audience_band"`
	RequiredEvidenceKinds []EvidenceKind `json:"required_evidence_kinds"`
}

type WorkflowStalenessRule struct {
	ID       string `json:"id"`
	InputRef string `json:"input_ref"`
	Severity string `json:"severity"`
}

type WorkflowForbiddenComposition struct {
	SuccessorWorkKind WorkKind `json:"successor_work_kind"`
	Reason            string   `json:"reason"`
}

type WorkflowCompositionRules struct {
	ForwardLinkOnly           bool                           `json:"forward_link_only"`
	AllowedSuccessorWorkKinds []WorkKind                     `json:"allowed_successor_work_kinds"`
	ForbiddenCompositions     []WorkflowForbiddenComposition `json:"forbidden_compositions"`
}

// WorkflowEvaluatorIndependence carries the dimensions of evaluator
// independence a workflow declares. CD-0017 D6 makes model distinctness
// structurally available to every workflow and mandatory for none; a globally
// mandatory rule awaits the R6 section 5 measured basis.
type WorkflowEvaluatorIndependence struct {
	// ModelDistinct requires the implementation and review runs to resolve to
	// different readback model identities. Evaluated against actual readback
	// identity so a fallback-induced collision is caught.
	ModelDistinct bool `json:"model_distinct"`
}

type WorkflowDefinition struct {
	Ref                   string                        `json:"ref"`
	Version               int64                         `json:"version"`
	WorkKind              WorkKind                      `json:"work_kind"`
	ChangesProductTruth   *bool                         `json:"changes_product_truth,omitempty"`
	StepGraph             WorkflowStepGraph             `json:"step_graph"`
	AvailableActions      []string                      `json:"available_actions"`
	ActionDefinitions     []WorkflowActionDefinition    `json:"action_definitions"`
	RequiredEvidenceKinds []EvidenceKind                `json:"required_evidence_kinds"`
	OutcomeSchema         WorkflowOutcomeSchema         `json:"outcome_schema"`
	RigorRules            []WorkflowRigorRule           `json:"rigor_rules"`
	StalenessRules        []WorkflowStalenessRule       `json:"staleness_rules"`
	CompositionRules      WorkflowCompositionRules      `json:"composition_rules"`
	EvaluatorIndependence WorkflowEvaluatorIndependence `json:"evaluator_independence"`
}

type RegisteredDefinition struct {
	Definition WorkflowDefinition `json:"definition"`
	Digest     string             `json:"digest"`
}

type DefinitionRegistry interface {
	Register(WorkflowDefinition) (RegisteredDefinition, error)
	Lookup(ref string, version int64) (RegisteredDefinition, bool)
	Verify(ref string, version int64, digest string) error
}

type workflowDefinitionRegistry struct {
	mu      sync.RWMutex
	entries map[string]RegisteredDefinition
}

func NewWorkflowDefinitionRegistry() DefinitionRegistry {
	return &workflowDefinitionRegistry{entries: make(map[string]RegisteredDefinition)}
}

func registryKey(ref string, version int64) string { return fmt.Sprintf("%s\x00%d", ref, version) }

func (r *workflowDefinitionRegistry) Register(definition WorkflowDefinition) (RegisteredDefinition, error) {
	if err := ValidateWorkflowDefinition(definition); err != nil {
		return RegisteredDefinition{}, err
	}
	if definition.Version < 3 {
		for i := range definition.ActionDefinitions {
			if definition.ActionDefinitions[i].ExecutionMode == "" {
				definition.ActionDefinitions[i].ExecutionMode = legacyActionExecutionMode(definition.ActionDefinitions[i].ID)
			}
		}
	}
	for _, action := range definition.ActionDefinitions {
		if !validActionExecutionMode(action.ExecutionMode) {
			return RegisteredDefinition{}, definitionFailure(KindInvalidDefinition, "registered action execution mode is not declared")
		}
	}
	digest, err := WorkflowDefinitionDigest(definition)
	if err != nil {
		return RegisteredDefinition{}, err
	}
	copy := cloneWorkflowDefinition(definition)
	registered := RegisteredDefinition{Definition: copy, Digest: digest}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := registryKey(definition.Ref, definition.Version)
	if prior, ok := r.entries[key]; ok {
		if prior.Digest != digest {
			return RegisteredDefinition{}, definitionFailure(KindDefinitionVersionConflict, "definition version is already registered with another digest")
		}
		return cloneRegisteredDefinition(prior), nil
	}
	for existingKey, prior := range r.entries {
		if prior.Definition.Ref == definition.Ref && existingKey != key && definition.Version <= prior.Definition.Version {
			return RegisteredDefinition{}, definitionFailure(KindDefinitionVersionNotMonotonic, "definition version is not greater than the highest registered version")
		}
	}
	r.entries[key] = registered
	return cloneRegisteredDefinition(registered), nil
}

func (r *workflowDefinitionRegistry) Lookup(ref string, version int64) (RegisteredDefinition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[registryKey(ref, version)]
	if !ok {
		return RegisteredDefinition{}, false
	}
	return cloneRegisteredDefinition(entry), true
}

func (r *workflowDefinitionRegistry) Verify(ref string, version int64, digest string) error {
	entry, ok := r.Lookup(ref, version)
	if !ok {
		return definitionFailure(KindDefinitionDigestMismatch, "pinned workflow definition is not registered")
	}
	computed, err := WorkflowDefinitionDigest(entry.Definition)
	if err != nil {
		return err
	}
	if computed != entry.Digest || digest != entry.Digest || digest != computed {
		return definitionFailure(KindDefinitionDigestMismatch, "registered or pinned workflow definition digest drifted")
	}
	return nil
}

func definitionFailure(kind FailureKind, detail string) error {
	return newFailure(kind, "workflow_definition", detail, false, "reread_entities")
}

func cloneWorkflowDefinition(definition WorkflowDefinition) WorkflowDefinition {
	raw, _ := json.Marshal(definition)
	var copy WorkflowDefinition
	_ = json.Unmarshal(raw, &copy)
	return copy
}

func cloneRegisteredDefinition(registered RegisteredDefinition) RegisteredDefinition {
	registered.Definition = cloneWorkflowDefinition(registered.Definition)
	return registered
}

var workflowIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,63}$`)
var workflowRefPattern = regexp.MustCompile(`^workflow\.[a-z][a-z0-9_.-]{1,62}$`)
var workflowDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validWorkflowID(value string) bool  { return workflowIDPattern.MatchString(value) }
func validWorkflowRef(value string) bool { return workflowRefPattern.MatchString(value) }

func ValidateWorkflowDefinition(definition WorkflowDefinition) error {
	if !validWorkflowRef(definition.Ref) || definition.Version < 1 || definition.Version > 2147483647 || !validWorkKind(definition.WorkKind) {
		return definitionFailure(KindInvalidDefinition, "definition identity or work kind is invalid")
	}
	if definition.ChangesProductTruth == nil {
		return definitionFailure(KindInvalidDefinition, "definition product-truth classification is required")
	}
	if *definition.ChangesProductTruth != expectedProductTruth(definition.Version, definition.WorkKind) {
		return definitionFailure(KindInvalidDefinition, "definition product-truth classification does not match its versioned work-kind matrix")
	}
	graph := definition.StepGraph
	if len(graph.Steps) < 2 || len(graph.Steps) > 32 || len(graph.Edges) < 1 || len(graph.Edges) > 64 || !validWorkflowID(graph.StartStep) || len(graph.TerminalSteps) < 1 || len(graph.TerminalSteps) > 16 {
		return definitionFailure(KindInvalidDefinition, "definition graph bounds are invalid")
	}
	steps := make(map[string]WorkflowStep, len(graph.Steps))
	for _, step := range graph.Steps {
		if !validWorkflowID(step.ID) || !validWorkflowStepKind(step.Kind) || len(step.Actions) < 1 || len(step.Actions) > 16 || len(step.RequiredEvidenceKinds) > 7 || !uniqueStrings(evidenceStrings(step.RequiredEvidenceKinds)) || !allEvidenceKindsValid(step.RequiredEvidenceKinds) {
			return definitionFailure(KindInvalidDefinition, "definition step is invalid")
		}
		if _, exists := steps[step.ID]; exists {
			return definitionFailure(KindInvalidDefinition, "definition contains duplicate step IDs")
		}
		steps[step.ID] = step
	}
	if _, ok := steps[graph.StartStep]; !ok {
		return definitionFailure(KindInvalidDefinition, "graph start endpoint is not declared")
	}
	terminals := make(map[string]bool, len(graph.TerminalSteps))
	for _, terminal := range graph.TerminalSteps {
		if !validWorkflowID(terminal) {
			return definitionFailure(KindInvalidDefinition, "graph terminal endpoint is invalid")
		}
		if _, ok := steps[terminal]; !ok {
			return definitionFailure(KindInvalidDefinition, "graph terminal endpoint is not declared")
		}
		if terminals[terminal] {
			return definitionFailure(KindInvalidDefinition, "definition contains duplicate terminal steps")
		}
		terminals[terminal] = true
	}
	if !uniqueStrings(definition.AvailableActions) || len(definition.AvailableActions) < 1 || len(definition.AvailableActions) > 64 {
		return definitionFailure(KindInvalidDefinition, "available actions are invalid")
	}
	actions := make(map[string]WorkflowActionDefinition, len(definition.ActionDefinitions))
	for _, action := range definition.ActionDefinitions {
		modeInvalid := !validActionExecutionMode(action.ExecutionMode) && (definition.Version >= 3 || action.ExecutionMode != "")
		if !validWorkflowID(action.ID) || !validActionConsequence(action.Consequence) || !validActionApproval(action.Approval) || modeInvalid || len(action.Payload.Fields) > 32 {
			return definitionFailure(KindInvalidDefinition, "action definition is invalid")
		}
		if _, exists := actions[action.ID]; exists {
			return definitionFailure(KindInvalidDefinition, "definition contains duplicate action IDs")
		}
		if !validatePayloadFields(action.Payload.Fields) {
			return definitionFailure(KindInvalidDefinition, "action payload fields are invalid")
		}
		actions[action.ID] = action
	}
	if len(actions) != len(definition.AvailableActions) {
		return definitionFailure(KindInvalidDefinition, "action definitions do not match available actions")
	}
	for _, actionID := range definition.AvailableActions {
		if !validWorkflowID(actionID) {
			return definitionFailure(KindInvalidDefinition, "available action ID is invalid")
		}
		if _, ok := actions[actionID]; !ok {
			return definitionFailure(KindDefinitionActionOrStepUnknown, "root action definitions do not match available actions")
		}
	}
	for _, step := range graph.Steps {
		for _, actionID := range step.Actions {
			if !containsString(definition.AvailableActions, actionID) {
				return definitionFailure(KindDefinitionActionOrStepUnknown, "step action not declared at root")
			}
		}
	}
	adjacency := make(map[string][]string, len(steps))
	for _, edge := range graph.Edges {
		if !validWorkflowID(edge.From) || !validWorkflowID(edge.To) || !validEdgeKind(edge.Kind) {
			return definitionFailure(KindInvalidDefinition, "graph edge is invalid")
		}
		if _, ok := steps[edge.From]; !ok {
			return definitionFailure(KindInvalidDefinition, "graph edge endpoint is not declared")
		}
		if _, ok := steps[edge.To]; !ok {
			return definitionFailure(KindInvalidDefinition, "graph edge endpoint is not declared")
		}
		if edge.Kind != WorkflowEdgeRetry {
			adjacency[edge.From] = append(adjacency[edge.From], edge.To)
		}
	}
	if graphHasCycle(adjacency, steps) {
		return definitionFailure(KindInvalidDefinition, "non-retry graph cycle is not allowed")
	}
	if len(definition.RequiredEvidenceKinds) > 7 || !uniqueStrings(evidenceStrings(definition.RequiredEvidenceKinds)) || !allEvidenceKindsValid(definition.RequiredEvidenceKinds) || !validateOutcomeSchema(definition.WorkKind, definition.OutcomeSchema) || len(definition.RigorRules) < 1 || len(definition.RigorRules) > 16 || len(definition.StalenessRules) > 16 || !validateRigorRules(definition.RigorRules) || !validateComposition(definition.CompositionRules) {
		return definitionFailure(KindInvalidDefinition, "definition evidence, outcome, rigor, staleness, or composition rules are invalid")
	}
	for _, rule := range definition.StalenessRules {
		if !validWorkflowID(rule.ID) || !validWorkflowRef(rule.InputRef) && !validReference(rule.InputRef) || (rule.Severity != "warning" && rule.Severity != "block") {
			return definitionFailure(KindInvalidDefinition, "staleness rule is invalid")
		}
	}
	return nil
}

func graphHasCycle(adjacency map[string][]string, steps map[string]WorkflowStep) bool {
	state := make(map[string]uint8, len(steps))
	var visit func(string) bool
	visit = func(node string) bool {
		if state[node] == 1 {
			return true
		}
		if state[node] == 2 {
			return false
		}
		state[node] = 1
		for _, next := range adjacency[node] {
			if visit(next) {
				return true
			}
		}
		state[node] = 2
		return false
	}
	for node := range steps {
		if visit(node) {
			return true
		}
	}
	return false
}

func CanonicalWorkflowDefinition(definition WorkflowDefinition) ([]byte, error) {
	if err := ValidateWorkflowDefinition(definition); err != nil {
		return nil, err
	}
	// json.Marshal follows the field order below. The digest is deliberately not
	// part of this manifest, and nil arrays are normalized to schema arrays.
	definition = normalizeWorkflowDefinition(definition)
	schemaVersion := "1.3"
	if definition.Version < 3 {
		schemaVersion = "1.1"
		for i := range definition.ActionDefinitions {
			definition.ActionDefinitions[i].ExecutionMode = ""
		}
	} else if definition.Version == 3 {
		schemaVersion = "1.2"
	}
	manifest := struct {
		SchemaVersion         string                        `json:"schema_version"`
		Ref                   string                        `json:"ref"`
		Version               int64                         `json:"version"`
		WorkKind              WorkKind                      `json:"work_kind"`
		ChangesProductTruth   *bool                         `json:"changes_product_truth,omitempty"`
		StepGraph             WorkflowStepGraph             `json:"step_graph"`
		AvailableActions      []string                      `json:"available_actions"`
		ActionDefinitions     []WorkflowActionDefinition    `json:"action_definitions"`
		RequiredEvidenceKinds []EvidenceKind                `json:"required_evidence_kinds"`
		OutcomeSchema         WorkflowOutcomeSchema         `json:"outcome_schema"`
		RigorRules            []WorkflowRigorRule           `json:"rigor_rules"`
		StalenessRules        []WorkflowStalenessRule       `json:"staleness_rules"`
		CompositionRules      WorkflowCompositionRules      `json:"composition_rules"`
		EvaluatorIndependence WorkflowEvaluatorIndependence `json:"evaluator_independence"`
	}{
		SchemaVersion: schemaVersion, Ref: definition.Ref, Version: definition.Version, WorkKind: definition.WorkKind,
		StepGraph: definition.StepGraph, AvailableActions: definition.AvailableActions, ActionDefinitions: definition.ActionDefinitions,
		RequiredEvidenceKinds: definition.RequiredEvidenceKinds, OutcomeSchema: definition.OutcomeSchema, RigorRules: definition.RigorRules,
		StalenessRules: definition.StalenessRules, CompositionRules: definition.CompositionRules, EvaluatorIndependence: definition.EvaluatorIndependence,
	}
	if definition.Version >= 4 {
		manifest.ChangesProductTruth = definition.ChangesProductTruth
	}
	return json.Marshal(manifest)
}

func WorkflowDefinitionDigest(definition WorkflowDefinition) (string, error) {
	canonical, err := CanonicalWorkflowDefinition(definition)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalizeWorkflowDefinition(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	if definition.StepGraph.TerminalSteps == nil {
		definition.StepGraph.TerminalSteps = []string{}
	}
	if definition.StepGraph.Steps == nil {
		definition.StepGraph.Steps = []WorkflowStep{}
	}
	if definition.StepGraph.Edges == nil {
		definition.StepGraph.Edges = []WorkflowEdge{}
	}
	if definition.AvailableActions == nil {
		definition.AvailableActions = []string{}
	}
	if definition.ActionDefinitions == nil {
		definition.ActionDefinitions = []WorkflowActionDefinition{}
	}
	if definition.RequiredEvidenceKinds == nil {
		definition.RequiredEvidenceKinds = []EvidenceKind{}
	}
	if definition.OutcomeSchema.AllowedKinds == nil {
		definition.OutcomeSchema.AllowedKinds = []PredicateKind{}
	}
	if definition.OutcomeSchema.AllowedOutcomeTokens == nil {
		definition.OutcomeSchema.AllowedOutcomeTokens = []string{}
	}
	if definition.RigorRules == nil {
		definition.RigorRules = []WorkflowRigorRule{}
	}
	if definition.StalenessRules == nil {
		definition.StalenessRules = []WorkflowStalenessRule{}
	}
	if definition.CompositionRules.AllowedSuccessorWorkKinds == nil {
		definition.CompositionRules.AllowedSuccessorWorkKinds = []WorkKind{}
	}
	if definition.CompositionRules.ForbiddenCompositions == nil {
		definition.CompositionRules.ForbiddenCompositions = []WorkflowForbiddenComposition{}
	}
	for i := range definition.StepGraph.Steps {
		if definition.StepGraph.Steps[i].RequiredEvidenceKinds == nil {
			definition.StepGraph.Steps[i].RequiredEvidenceKinds = []EvidenceKind{}
		}
	}
	for i := range definition.ActionDefinitions {
		if definition.ActionDefinitions[i].Payload.Fields == nil {
			definition.ActionDefinitions[i].Payload.Fields = []WorkflowPayloadField{}
		}
	}
	return definition
}

func BuiltinWorkflowDefinitions() []WorkflowDefinition {
	latest := builtinWorkflowV3Definitions()
	for i := range latest {
		latest[i].Version = 4
		classification := workKindMayChangeProductTruth(latest[i].WorkKind)
		latest[i].ChangesProductTruth = &classification
		if latest[i].WorkKind == WorkKindBreakFix {
			latest[i] = withBreakFixV4ApprovalRoute(latest[i])
		}
	}
	return latest
}

// withBreakFixV4ApprovalRoute derives the Product-changing planning checkpoint
// for v4; earlier definitions are built separately with their pinned graph.
func withBreakFixV4ApprovalRoute(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	planning := step("planning", WorkflowStepHumanCheckpoint, "approve_contract", "checkpoint_context", "cross_context_boundary")
	steps := []WorkflowStep{}
	for _, existing := range definition.StepGraph.Steps {
		steps = append(steps, existing)
		if existing.ID == "diagnose" {
			steps = append(steps, planning)
		}
	}
	definition.StepGraph.Steps = steps
	edges := []WorkflowEdge{}
	for _, edge := range definition.StepGraph.Edges {
		if edge.From == "diagnose" && edge.To == "repair" && edge.Kind == WorkflowEdgeForward {
			edges = append(edges, WorkflowEdge{From: "diagnose", To: "planning", Kind: WorkflowEdgeForward}, WorkflowEdge{From: "planning", To: "repair", Kind: WorkflowEdgeForward})
			continue
		}
		edges = append(edges, edge)
	}
	definition.StepGraph.Edges = edges
	definition.AvailableActions = append(definition.AvailableActions, "approve_contract")
	definition.ActionDefinitions = append(definition.ActionDefinitions, actionDefinitions([]string{"approve_contract"})...)
	return definition
}

func NewBuiltinWorkflowRegistry() DefinitionRegistry {
	registry := NewWorkflowDefinitionRegistry()
	for _, definition := range builtinWorkflowV1Definitions() {
		if _, err := registry.Register(definition); err != nil {
			panic(err)
		}
	}
	for _, definition := range builtinWorkflowV2Definitions() {
		if _, err := registry.Register(definition); err != nil {
			panic(err)
		}
	}
	for _, definition := range builtinWorkflowV3Definitions() {
		if _, err := registry.Register(definition); err != nil {
			panic(err)
		}
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		if _, err := registry.Register(definition); err != nil {
			panic(err)
		}
	}
	return registry
}

var builtinWorkflowRegistry = NewBuiltinWorkflowRegistry()

func BuiltinWorkflowRegistry() DefinitionRegistry { return builtinWorkflowRegistry }

// BuiltinWorkflowDefinitionForRef resolves the immutable built-in definition
// selected by a work item. Capture and revise use this before opening their
// mutation path so an unknown workflow family cannot leave partial work state.
func BuiltinWorkflowDefinitionForRef(ref string) (RegisteredDefinition, error) {
	if !validWorkflowRef(ref) {
		return RegisteredDefinition{}, definitionFailure(KindInvalidDefinition, "workflow type reference is invalid")
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Ref == ref {
			registered, ok := BuiltinWorkflowRegistry().Lookup(ref, definition.Version)
			if !ok {
				return RegisteredDefinition{}, definitionFailure(KindDefinitionDigestMismatch, "workflow type reference is not registered")
			}
			return registered, nil
		}
	}
	return RegisteredDefinition{}, definitionFailure(KindDefinitionDigestMismatch, "workflow type reference is not registered")
}

func builtinWorkflowV1Definitions() []WorkflowDefinition {
	return []WorkflowDefinition{
		builtinImplementation(), builtinBreakFix(), builtinResearch(), builtinArchitectureSpike(), builtinOpsRunbook(), builtinStaticAnalysis(), builtinGenericOneOff(),
	}
}

func builtinWorkflowV2Definitions() []WorkflowDefinition {
	return []WorkflowDefinition{
		builtinWorkflowV2(builtinImplementation()), builtinWorkflowV2(builtinBreakFix()), builtinWorkflowV2(builtinResearch()), builtinWorkflowV2(builtinArchitectureSpike()), builtinWorkflowV2(builtinOpsRunbook()), builtinWorkflowV2(builtinStaticAnalysis()), builtinWorkflowV2(builtinGenericOneOff()),
	}
}

func builtinWorkflowV3Definitions() []WorkflowDefinition {
	definitions := builtinWorkflowV2Definitions()
	for i := range definitions {
		definitions[i].Version = 3
		falseValue := false
		definitions[i].ChangesProductTruth = &falseValue
	}
	return definitions
}

func builtinWorkflowV2(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	definition.Version = 2
	if definition.WorkKind == WorkKindResearch {
		return definition
	}
	acceptance := WorkflowActionDefinition{
		ID: "accept_worker_result", Consequence: ActionInternalSQLite, Approval: ActionApprovalNone, ExecutionMode: ActionAdvance,
		Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{
			{Name: "attempt_id", ValueType: PayloadRef, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(128)},
			{Name: "attempt_epoch", ValueType: PayloadInteger, Required: true, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)},
		}},
	}
	definition.AvailableActions = append(definition.AvailableActions, acceptance.ID)
	definition.ActionDefinitions = append(definition.ActionDefinitions, acceptance)
	for i := range definition.StepGraph.Steps {
		if definition.StepGraph.Steps[i].Kind == WorkflowStepExternalEffect {
			definition.StepGraph.Steps[i].Actions = append(definition.StepGraph.Steps[i].Actions, acceptance.ID)
		}
	}
	return definition
}

func validWorkKind(kind WorkKind) bool {
	switch kind {
	case WorkKindImplementation, WorkKindBreakFix, WorkKindResearch, WorkKindArchitectureSpike, WorkKindOpsRunbook, WorkKindStaticAnalysis, WorkKindGenericOneOff:
		return true
	}
	return false
}

func workKindMayChangeProductTruth(kind WorkKind) bool {
	return kind == WorkKindImplementation || kind == WorkKindBreakFix || kind == WorkKindOpsRunbook
}

func expectedProductTruth(version int64, kind WorkKind) bool {
	return version >= 4 && workKindMayChangeProductTruth(kind)
}
func validWorkflowStepKind(kind WorkflowStepKind) bool {
	return kind == WorkflowStepInternalSQLite || kind == WorkflowStepCrossAuthority || kind == WorkflowStepExternalEffect || kind == WorkflowStepHumanCheckpoint
}
func validEdgeKind(kind WorkflowEdgeKind) bool {
	return kind == WorkflowEdgeForward || kind == WorkflowEdgeRetry || kind == WorkflowEdgeOptional || kind == WorkflowEdgeFailure
}
func validActionConsequence(value ActionConsequence) bool {
	return value == ActionInternalSQLite || value == ActionCrossAuthority || value == ActionExternalEffect
}
func validActionApproval(value ActionApproval) bool {
	return value == ActionApprovalNone || value == ActionApprovalConditional || value == ActionApprovalRequired
}
func validActionExecutionMode(value ActionExecutionMode) bool {
	return value == ActionAdvance || value == ActionHold || value == ActionFenced || value == ActionCheckpoint
}
func validReference(value string) bool {
	return len(value) >= 2 && len(value) <= 128 && !strings.ContainsAny(value, " \t\r\n")
}
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func uniqueStrings(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
func evidenceStrings(values []EvidenceKind) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}
func validEvidence(value EvidenceKind) bool {
	return containsString([]string{"verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"}, string(value))
}
func allEvidenceKindsValid(values []EvidenceKind) bool {
	for _, value := range values {
		if !validEvidence(value) {
			return false
		}
	}
	return true
}
func validateRigorRules(values []WorkflowRigorRule) bool {
	for _, value := range values {
		if !containsString([]string{"prototype", "production", "critical"}, value.Maturity) || !containsString([]string{"internal", "trusted", "public", "safety_critical"}, value.AudienceBand) || len(value.RequiredEvidenceKinds) < 1 || len(value.RequiredEvidenceKinds) > 7 || !uniqueStrings(evidenceStrings(value.RequiredEvidenceKinds)) || !allEvidenceKindsValid(value.RequiredEvidenceKinds) {
			return false
		}
	}
	return true
}
func validPredicateKind(value PredicateKind) bool {
	return value == PredicateExists || value == PredicateAbsent || value == PredicateOutcome || value == PredicateCheck
}
func validatePayloadFields(fields []WorkflowPayloadField) bool {
	seen := map[string]bool{}
	for _, field := range fields {
		if !validWorkflowID(field.Name) || seen[field.Name] || !containsString([]string{"string", "integer", "boolean", "ref", "digest", "string_list"}, string(field.ValueType)) {
			return false
		}
		seen[field.Name] = true
		if field.MinLength != nil && (*field.MinLength < 0 || *field.MinLength > 16384) || field.MaxLength != nil && (*field.MaxLength < 0 || *field.MaxLength > 16384) || field.MinItems != nil && (*field.MinItems < 0 || *field.MinItems > 64) || field.MaxItems != nil && (*field.MaxItems < 0 || *field.MaxItems > 64) || field.Minimum != nil && (*field.Minimum < -2147483648 || *field.Minimum > 2147483647) || field.Maximum != nil && (*field.Maximum < -2147483648 || *field.Maximum > 2147483647) {
			return false
		}
		if field.MinLength != nil && field.MaxLength != nil && *field.MinLength > *field.MaxLength || field.MinItems != nil && field.MaxItems != nil && *field.MinItems > *field.MaxItems || field.Minimum != nil && field.Maximum != nil && *field.Minimum > *field.Maximum {
			return false
		}
	}
	return true
}

func validateOutcomeSchema(kind WorkKind, schema WorkflowOutcomeSchema) bool {
	if !validPredicateKind(schema.DefaultKind) || len(schema.AllowedKinds) < 1 || len(schema.AllowedKinds) > 4 || !uniquePredicateKinds(schema.AllowedKinds) || len(schema.AllowedOutcomeTokens) > 8 || !uniqueStrings(schema.AllowedOutcomeTokens) {
		return false
	}
	for _, allowed := range schema.AllowedKinds {
		if !validPredicateKind(allowed) {
			return false
		}
	}
	for _, token := range schema.AllowedOutcomeTokens {
		if !validOutcomeToken(token) {
			return false
		}
	}
	wantKinds := map[WorkKind][]PredicateKind{WorkKindImplementation: {PredicateExists, PredicateAbsent, PredicateCheck}, WorkKindBreakFix: {PredicateExists, PredicateAbsent, PredicateCheck}, WorkKindResearch: {PredicateOutcome}, WorkKindArchitectureSpike: {PredicateOutcome}, WorkKindOpsRunbook: {PredicateExists, PredicateAbsent, PredicateCheck}, WorkKindStaticAnalysis: {PredicateExists, PredicateAbsent, PredicateCheck}, WorkKindGenericOneOff: {PredicateExists, PredicateAbsent, PredicateOutcome, PredicateCheck}}
	wantTokens := map[WorkKind][]string{WorkKindImplementation: {}, WorkKindBreakFix: {}, WorkKindResearch: {"no_change", "resolved", "report_recorded"}, WorkKindArchitectureSpike: {"accepted_decision", "insufficient_evidence"}, WorkKindOpsRunbook: {}, WorkKindStaticAnalysis: {}, WorkKindGenericOneOff: {"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}}
	return samePredicateKinds(schema.AllowedKinds, wantKinds[kind]) && sameStrings(schema.AllowedOutcomeTokens, wantTokens[kind]) && schema.DefaultKind == defaultPredicateKind(kind) && schema.DecisionRecordRequired == (kind == WorkKindArchitectureSpike)
}
func uniquePredicateKinds(values []PredicateKind) bool {
	seen := map[PredicateKind]bool{}
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
func samePredicateKinds(a, b []PredicateKind) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range b {
		if !containsPredicateKind(a, x) {
			return false
		}
	}
	return true
}
func containsPredicateKind(values []PredicateKind, want PredicateKind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range b {
		if !containsString(a, x) {
			return false
		}
	}
	return true
}
func validOutcomeToken(value string) bool {
	return containsString([]string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}, value)
}
func defaultPredicateKind(kind WorkKind) PredicateKind {
	switch kind {
	case WorkKindImplementation, WorkKindOpsRunbook, WorkKindStaticAnalysis:
		return PredicateCheck
	case WorkKindBreakFix:
		return PredicateAbsent
	default:
		return PredicateOutcome
	}
}
func validateComposition(rules WorkflowCompositionRules) bool {
	if !rules.ForwardLinkOnly || len(rules.AllowedSuccessorWorkKinds) > 7 || !uniqueWorkKinds(rules.AllowedSuccessorWorkKinds) {
		return false
	}
	for _, kind := range rules.AllowedSuccessorWorkKinds {
		if !validWorkKind(kind) {
			return false
		}
	}
	for _, forbidden := range rules.ForbiddenCompositions {
		if !validWorkKind(forbidden.SuccessorWorkKind) || len(forbidden.Reason) < 1 || len(forbidden.Reason) > 256 {
			return false
		}
	}
	return true
}
func uniqueWorkKinds(values []WorkKind) bool {
	seen := map[WorkKind]bool{}
	for _, value := range values {
		if seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

type builtinActionPolicy struct {
	Consequence   ActionConsequence
	Approval      ActionApproval
	ExecutionMode ActionExecutionMode
}

func actionPolicy(consequence ActionConsequence, approval ActionApproval, mode ActionExecutionMode) builtinActionPolicy {
	return builtinActionPolicy{Consequence: consequence, Approval: approval, ExecutionMode: mode}
}

var builtinActionPolicies = map[string]builtinActionPolicy{
	"record_proposal":        actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"record_discovery":       actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"record_design":          actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"approve_contract":       actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionAdvance),
	"start_execution":        actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced),
	"checkpoint_execution":   actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint),
	"bind_evidence":          actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold),
	"declare_impact":         actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold),
	"link_successor":         actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold),
	"record_verdict":         actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold),
	"confirm_premise":        actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionAdvance),
	"complete":               actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold),
	"record_reproduction":    actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"record_root_cause":      actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"start_repair":           actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced),
	"checkpoint_repair":      actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint),
	"frame_research":         actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"record_finding":         actionPolicy(ActionCrossAuthority, ActionApprovalNone, ActionAdvance),
	"revise_candidates":      actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"record_report":          actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"record_conclusion":      actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"frame_question":         actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"record_research":        actionPolicy(ActionCrossAuthority, ActionApprovalNone, ActionAdvance),
	"record_option":          actionPolicy(ActionCrossAuthority, ActionApprovalNone, ActionAdvance),
	"start_poc":              actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced),
	"checkpoint_poc":         actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint),
	"discard_poc":            actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"record_decision":        actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionCheckpoint),
	"accept_decision":        actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionHold),
	"approve_operation":      actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionAdvance),
	"start_run":              actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced),
	"checkpoint_run":         actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint),
	"add_condition":          actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"resolve_condition":      actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"cancel_condition":       actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"record_health":          actionPolicy(ActionCrossAuthority, ActionApprovalNone, ActionAdvance),
	"rollback_run":           actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced),
	"cleanup_run":            actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold),
	"declare_scope":          actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"run_analysis":           actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced),
	"checkpoint_analysis":    actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint),
	"start_action":           actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced),
	"checkpoint_action":      actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint),
	"checkpoint_context":     actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold),
	"cross_context_boundary": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"accept_worker_result":   actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance),
	"supersede_contract":     actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionAdvance),
}

func actionDefinitions(ids []string) []WorkflowActionDefinition {
	result := make([]WorkflowActionDefinition, 0, len(ids))
	for _, id := range ids {
		policy, ok := builtinActionPolicies[id]
		if !ok {
			panic("built-in workflow action policy is not declared: " + id)
		}
		result = append(result, WorkflowActionDefinition{ID: id, Consequence: policy.Consequence, Approval: policy.Approval, ExecutionMode: policy.ExecutionMode, Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{}}})
	}
	return result
}

func workflowActionExecutionMode(definition WorkflowDefinition, actionID string) (ActionExecutionMode, bool) {
	for _, action := range definition.ActionDefinitions {
		if action.ID != actionID {
			continue
		}
		if validActionExecutionMode(action.ExecutionMode) {
			return action.ExecutionMode, true
		}
		break
	}
	// supersede_contract is a recovery-only action outside the pinned root list.
	if actionID == "supersede_contract" {
		policy, ok := builtinActionPolicies[actionID]
		return policy.ExecutionMode, ok
	}
	// Version 1 accepted undeclared completion action IDs. Replaying those
	// historical events must retain their exact naming-contract semantics.
	if definition.Version == 1 {
		return legacyActionExecutionMode(actionID), true
	}
	return "", false
}

func legacyActionExecutionMode(actionID string) ActionExecutionMode {
	if strings.HasPrefix(actionID, "start_") || strings.HasPrefix(actionID, "run_") || strings.HasPrefix(actionID, "rollback_") {
		return ActionFenced
	}
	if strings.HasPrefix(actionID, "checkpoint_") {
		if actionID == "checkpoint_context" {
			return ActionHold
		}
		return ActionCheckpoint
	}
	switch actionID {
	case "bind_evidence", "declare_impact", "link_successor", "record_verdict", "accept_decision", "cleanup_run", "complete":
		return ActionHold
	case "record_decision":
		return ActionCheckpoint
	default:
		return ActionAdvance
	}
}

func workflowInt(value int64) *int64 { return &value }

func withContinuityActions(definition WorkflowDefinition) WorkflowDefinition {
	checkpointFields := []WorkflowPayloadField{
		{Name: "checkpoint_id", ValueType: PayloadRef},
		{Name: "checkpoint_sequence", ValueType: PayloadInteger, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)},
		{Name: "active_unit", ValueType: PayloadString, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(256)},
		{Name: "hypothesis", ValueType: PayloadString, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		{Name: "diagnosis", ValueType: PayloadString, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		{Name: "strategy", ValueType: PayloadString, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		{Name: "touched_refs", ValueType: PayloadStringList, Required: true, MinItems: workflowInt(1), MaxItems: workflowInt(64)},
		{Name: "evidence_refs", ValueType: PayloadStringList, Required: true, MinItems: workflowInt(1), MaxItems: workflowInt(64)},
		{Name: "pending_questions", ValueType: PayloadStringList, Required: true, MinItems: workflowInt(0), MaxItems: workflowInt(16)},
		{Name: "pending_decisions", ValueType: PayloadStringList, Required: true, MinItems: workflowInt(0), MaxItems: workflowInt(16)},
	}
	boundaryFields := []WorkflowPayloadField{
		{Name: "boundary_kind", ValueType: PayloadString, Required: true, MinLength: workflowInt(1), MaxLength: workflowInt(16)},
		{Name: "mode", ValueType: PayloadString, Required: true, MinLength: workflowInt(1), MaxLength: workflowInt(16)},
		{Name: "checkpoint_id", ValueType: PayloadRef, Required: true},
		{Name: "checkpoint_sequence", ValueType: PayloadInteger, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)},
		{Name: "summary", ValueType: PayloadString, Required: true, MinLength: workflowInt(1), MaxLength: workflowInt(16384)},
	}
	continuity := []WorkflowActionDefinition{
		{ID: "checkpoint_context", Consequence: ActionInternalSQLite, Approval: ActionApprovalNone, ExecutionMode: ActionHold, Payload: WorkflowPayloadDefinition{Fields: checkpointFields}},
		{ID: "cross_context_boundary", Consequence: ActionInternalSQLite, Approval: ActionApprovalNone, ExecutionMode: ActionAdvance, Payload: WorkflowPayloadDefinition{Fields: boundaryFields}},
	}
	definition.AvailableActions = append(definition.AvailableActions, "checkpoint_context", "cross_context_boundary")
	definition.ActionDefinitions = append(definition.ActionDefinitions, continuity...)
	for i := range definition.StepGraph.Steps {
		definition.StepGraph.Steps[i].Actions = append(definition.StepGraph.Steps[i].Actions, "checkpoint_context", "cross_context_boundary")
	}
	return definition
}

func graph(steps []WorkflowStep, edges []WorkflowEdge, terminal string) WorkflowStepGraph {
	return WorkflowStepGraph{StartStep: steps[0].ID, TerminalSteps: []string{terminal}, Steps: steps, Edges: edges}
}
func step(id string, kind WorkflowStepKind, actions ...string) WorkflowStep {
	return WorkflowStep{ID: id, Kind: kind, Actions: actions}
}
func forward(ids ...string) []WorkflowEdge {
	result := make([]WorkflowEdge, 0, len(ids)-1)
	for i := 0; i < len(ids)-1; i++ {
		result = append(result, WorkflowEdge{From: ids[i], To: ids[i+1], Kind: WorkflowEdgeForward})
	}
	return result
}
func addEdge(edges []WorkflowEdge, from, to string, kind WorkflowEdgeKind) []WorkflowEdge {
	return append(edges, WorkflowEdge{From: from, To: to, Kind: kind})
}
func baseDefinition(ref string, kind WorkKind, g WorkflowStepGraph, actions []string, evidence []EvidenceKind, outcome WorkflowOutcomeSchema, successors []WorkKind) WorkflowDefinition {
	falseValue := false
	return WorkflowDefinition{Ref: ref, Version: 1, WorkKind: kind, ChangesProductTruth: &falseValue, StepGraph: g, AvailableActions: actions, ActionDefinitions: actionDefinitions(actions), RequiredEvidenceKinds: evidence, OutcomeSchema: outcome, RigorRules: []WorkflowRigorRule{{Maturity: "prototype", AudienceBand: "internal", RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification}}}, StalenessRules: []WorkflowStalenessRule{}, CompositionRules: WorkflowCompositionRules{ForwardLinkOnly: true, AllowedSuccessorWorkKinds: successors, ForbiddenCompositions: []WorkflowForbiddenComposition{}}}
}

func builtinImplementation() WorkflowDefinition {
	ids := []string{"proposal", "discovery", "design", "planning", "execution", "acceptance", "release"}
	steps := []WorkflowStep{step("proposal", WorkflowStepInternalSQLite, "record_proposal"), step("discovery", WorkflowStepInternalSQLite, "record_discovery"), step("design", WorkflowStepInternalSQLite, "record_design"), step("planning", WorkflowStepHumanCheckpoint, "approve_contract"), step("execution", WorkflowStepExternalEffect, "start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor"), step("acceptance", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("release", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "execution", "execution", WorkflowEdgeRetry)
	actions := []string{"record_proposal", "record_discovery", "record_design", "approve_contract", "start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.implementation", WorkKindImplementation, graph(steps, edges, "release"), actions, []EvidenceKind{EvidenceVerification, EvidenceReview}, WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindBreakFix, WorkKindResearch})
	return withContinuityActions(d)
}
func builtinBreakFix() WorkflowDefinition {
	ids := []string{"reproduce", "diagnose", "repair", "verify", "complete"}
	steps := []WorkflowStep{step("reproduce", WorkflowStepInternalSQLite, "record_reproduction"), step("diagnose", WorkflowStepInternalSQLite, "record_root_cause"), step("repair", WorkflowStepExternalEffect, "start_repair", "checkpoint_repair", "bind_evidence", "link_successor"), step("verify", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "repair", "repair", WorkflowEdgeRetry)
	actions := []string{"record_reproduction", "record_root_cause", "start_repair", "checkpoint_repair", "bind_evidence", "link_successor", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.break_fix", WorkKindBreakFix, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceVerification}, WorkflowOutcomeSchema{DefaultKind: PredicateAbsent, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindResearch})
	return withContinuityActions(d)
}
func builtinResearch() WorkflowDefinition {
	ids := []string{"frame", "investigate", "findings", "conclude", "complete"}
	steps := []WorkflowStep{step("frame", WorkflowStepHumanCheckpoint, "frame_research", "approve_contract"), step("investigate", WorkflowStepCrossAuthority, "record_finding", "revise_candidates", "bind_evidence"), step("findings", WorkflowStepInternalSQLite, "record_report", "link_successor"), step("conclude", WorkflowStepHumanCheckpoint, "record_conclusion", "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	actions := []string{"frame_research", "approve_contract", "record_finding", "revise_candidates", "bind_evidence", "record_report", "link_successor", "record_conclusion", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.research", WorkKindResearch, graph(steps, forward(ids...), "complete"), actions, []EvidenceKind{EvidenceArtifact}, WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"no_change", "resolved", "report_recorded"}, DecisionRecordRequired: false}, []WorkKind{WorkKindBreakFix, WorkKindArchitectureSpike, WorkKindStaticAnalysis})
	return withContinuityActions(d)
}
func builtinArchitectureSpike() WorkflowDefinition {
	ids := []string{"frame", "research", "options", "poc_optional", "decision_record", "review", "acceptance", "complete"}
	steps := []WorkflowStep{step("frame", WorkflowStepHumanCheckpoint, "frame_question", "approve_contract"), step("research", WorkflowStepCrossAuthority, "record_research", "bind_evidence"), step("options", WorkflowStepInternalSQLite, "record_option"), step("poc_optional", WorkflowStepExternalEffect, "start_poc", "checkpoint_poc", "discard_poc"), step("decision_record", WorkflowStepHumanCheckpoint, "record_decision"), step("review", WorkflowStepHumanCheckpoint, "record_verdict"), step("acceptance", WorkflowStepHumanCheckpoint, "accept_decision", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "options", "decision_record", WorkflowEdgeOptional)
	edges = addEdge(edges, "poc_optional", "poc_optional", WorkflowEdgeRetry)
	actions := []string{"frame_question", "approve_contract", "record_research", "bind_evidence", "record_option", "start_poc", "checkpoint_poc", "discard_poc", "record_decision", "record_verdict", "accept_decision", "confirm_premise", "complete"}
	d := baseDefinition("workflow.architecture_spike", WorkKindArchitectureSpike, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceReview, EvidenceApproval, EvidenceArtifact}, WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"accepted_decision", "insufficient_evidence"}, DecisionRecordRequired: true}, []WorkKind{WorkKindImplementation, WorkKindResearch, WorkKindStaticAnalysis})
	return withContinuityActions(d)
}
func builtinOpsRunbook() WorkflowDefinition {
	ids := []string{"plan", "approval", "execute", "health", "rollback_optional", "cleanup", "complete"}
	steps := []WorkflowStep{step("plan", WorkflowStepHumanCheckpoint, "approve_contract"), step("approval", WorkflowStepHumanCheckpoint, "approve_operation"), step("execute", WorkflowStepExternalEffect, "start_run", "checkpoint_run", "bind_evidence", "add_condition", "resolve_condition", "cancel_condition"), step("health", WorkflowStepCrossAuthority, "record_health", "record_verdict"), step("rollback_optional", WorkflowStepExternalEffect, "rollback_run"), step("cleanup", WorkflowStepInternalSQLite, "cleanup_run", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "health", "cleanup", WorkflowEdgeOptional)
	edges = addEdge(edges, "execute", "execute", WorkflowEdgeRetry)
	actions := []string{"approve_contract", "approve_operation", "start_run", "checkpoint_run", "bind_evidence", "add_condition", "resolve_condition", "cancel_condition", "record_health", "record_verdict", "rollback_run", "cleanup_run", "confirm_premise", "complete"}
	d := baseDefinition("workflow.ops_runbook", WorkKindOpsRunbook, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceApproval, EvidenceNativeRun}, WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch})
	return withContinuityActions(d)
}
func builtinStaticAnalysis() WorkflowDefinition {
	ids := []string{"scope", "analyze", "report", "review", "complete"}
	steps := []WorkflowStep{step("scope", WorkflowStepHumanCheckpoint, "approve_contract", "declare_scope"), step("analyze", WorkflowStepExternalEffect, "run_analysis", "checkpoint_analysis"), step("report", WorkflowStepInternalSQLite, "record_report", "bind_evidence"), step("review", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "analyze", "analyze", WorkflowEdgeRetry)
	actions := []string{"approve_contract", "declare_scope", "run_analysis", "checkpoint_analysis", "record_report", "bind_evidence", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.static_analysis", WorkKindStaticAnalysis, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceArtifact, EvidenceReview}, WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch})
	return withContinuityActions(d)
}
func builtinGenericOneOff() WorkflowDefinition {
	ids := []string{"define", "execute", "verify", "complete"}
	steps := []WorkflowStep{step("define", WorkflowStepHumanCheckpoint, "approve_contract"), step("execute", WorkflowStepExternalEffect, "start_action", "checkpoint_action", "bind_evidence", "link_successor"), step("verify", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "execute", "execute", WorkflowEdgeRetry)
	actions := []string{"approve_contract", "start_action", "checkpoint_action", "bind_evidence", "link_successor", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.generic_one_off", WorkKindGenericOneOff, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceArtifact}, WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateOutcome, PredicateCheck}, AllowedOutcomeTokens: []string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch, WorkKindArchitectureSpike, WorkKindOpsRunbook, WorkKindStaticAnalysis, WorkKindGenericOneOff})
	return withContinuityActions(d)
}
