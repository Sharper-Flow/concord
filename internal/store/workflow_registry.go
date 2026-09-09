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

// WorkKind identifies a work class in the code-owned v1 workflow-definition
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
	PayloadObject     PayloadValueType = "object"
	PayloadArray      PayloadValueType = "array"
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
	Enum      []string         `json:"enum,omitempty"`
	SchemaRef string           `json:"schema_ref,omitempty"`
	ItemRef   string           `json:"item_ref,omitempty"`
}

type WorkflowPayloadDefinition struct {
	// Closed distinguishes the current fail-closed contract from released
	// definitions whose empty field list meant the legacy open payload.
	Closed bool                   `json:"closed,omitempty"`
	Fields []WorkflowPayloadField `json:"fields"`
}

type WorkflowActionDefinition struct {
	ID            string              `json:"id"`
	Consequence   ActionConsequence   `json:"consequence"`
	Approval      ActionApproval      `json:"approval"`
	ExecutionMode ActionExecutionMode `json:"execution_mode,omitempty"`
	// RequiredCapability names the agent capability the dispatcher must
	// present to invoke this action. It is structural: the runtime reads it
	// from the registry entry instead of branching on actionID. A zero
	// value (empty string) carries no enforcement, mirroring the pre-CD-0059
	// surface for legacy actions and tests that do not assert a capability.
	RequiredCapability string                    `json:"required_capability,omitempty"`
	Payload            WorkflowPayloadDefinition `json:"payload"`
	// PublicPayload replaces Payload only at the agent boundary. The adapter
	// uses it for dispatch_worker, where callers supply lane_id and the adapter
	// authors the core attempt identity and worker packet.
	PublicPayload *WorkflowPayloadDefinition `json:"public_payload,omitempty"`
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
	if *definition.ChangesProductTruth != workKindMayChangeProductTruth(definition.WorkKind) {
		return definitionFailure(KindInvalidDefinition, "definition product-truth classification does not match its work-kind matrix")
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
		if !validWorkflowID(action.ID) || !validActionConsequence(action.Consequence) || !validActionApproval(action.Approval) || !validActionExecutionMode(action.ExecutionMode) || len(action.Payload.Fields) > 32 {
			return definitionFailure(KindInvalidDefinition, "action definition is invalid")
		}
		if _, exists := actions[action.ID]; exists {
			return definitionFailure(KindInvalidDefinition, "definition contains duplicate action IDs")
		}
		if !validatePayloadFields(action.Payload.Fields) {
			return definitionFailure(KindInvalidDefinition, "action payload fields are invalid")
		}
		if action.PublicPayload != nil && (!action.PublicPayload.Closed || len(action.PublicPayload.Fields) > 32 || !validatePayloadFields(action.PublicPayload.Fields)) {
			return definitionFailure(KindInvalidDefinition, "public action payload fields are invalid")
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
		if !validWorkflowID(rule.ID) || !validWorkflowRef(rule.InputRef) && !ValidReference(rule.InputRef) || (rule.Severity != "warning" && rule.Severity != "block") {
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

// workflowDefinitionSchemaVersion is the single manifest shape Concord emits.
// It is the schema_version enumerated by contracts/workflow-definition.schema.json.
const workflowDefinitionSchemaVersion = "1.3"

func CanonicalWorkflowDefinition(definition WorkflowDefinition) ([]byte, error) {
	if err := ValidateWorkflowDefinition(definition); err != nil {
		return nil, err
	}
	// json.Marshal follows the field order below. The digest is deliberately not
	// part of this manifest, and nil arrays are normalized to schema arrays.
	definition = normalizeWorkflowDefinition(definition)
	manifest := struct {
		SchemaVersion         string                        `json:"schema_version"`
		Ref                   string                        `json:"ref"`
		Version               int64                         `json:"version"`
		WorkKind              WorkKind                      `json:"work_kind"`
		ChangesProductTruth   *bool                         `json:"changes_product_truth"`
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
		SchemaVersion: workflowDefinitionSchemaVersion, Ref: definition.Ref, Version: definition.Version, WorkKind: definition.WorkKind,
		ChangesProductTruth: definition.ChangesProductTruth,
		StepGraph:           definition.StepGraph, AvailableActions: definition.AvailableActions, ActionDefinitions: definition.ActionDefinitions,
		RequiredEvidenceKinds: definition.RequiredEvidenceKinds, OutcomeSchema: definition.OutcomeSchema, RigorRules: definition.RigorRules,
		StalenessRules: definition.StalenessRules, CompositionRules: definition.CompositionRules, EvaluatorIndependence: definition.EvaluatorIndependence,
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
		if definition.ActionDefinitions[i].PublicPayload != nil && definition.ActionDefinitions[i].PublicPayload.Fields == nil {
			definition.ActionDefinitions[i].PublicPayload.Fields = []WorkflowPayloadField{}
		}
	}
	return definition
}

// BuiltinWorkflowDefinitions authors the seven shipped workflow families in
// the shape they run in. Frozen prior versions live in
// workflow_registry_versions.go and never acquire current payload contracts.
func BuiltinWorkflowDefinitions() []WorkflowDefinition {
	return []WorkflowDefinition{
		withWorkerActions(builtinImplementation(true), true), breakFixEvidenceRecoveryV6(), withWorkerActions(builtinResearch(true), true), withWorkerActions(builtinArchitectureSpike(true), true), withWorkerActions(builtinOpsRunbook(true), true), withWorkerActions(builtinStaticAnalysis(true), true), withWorkerActions(builtinGenericOneOff(true), true),
	}
}

// builtinWorkflowDefinitionsWithHistory returns every registered built-in
// definition: the frozen version-1 shapes first, then the shipped shapes.
// Registration enforces ascending versions per reference, so the order is
// load-bearing (#861).
func builtinWorkflowDefinitionsWithHistory() []WorkflowDefinition {
	return append(
		[]WorkflowDefinition{
			withLegacyWorkerActions(legacyImplementationV1()), withLegacyWorkerActions(legacyBreakFixV1()), withLegacyWorkerActions(legacyResearchV1()), withLegacyWorkerActions(legacyGenericOneOffV1()),
			preJoinImplementationV2(), preJoinBreakFixV2(), preJoinGenericOneOffV2(), preJoinResearchV2(), preJoinArchitectureSpikeV1(), preJoinOpsRunbookV1(), preJoinStaticAnalysisV1(),
			prePayloadImplementationV3(), prePayloadBreakFixV3(), prePayloadGenericOneOffV3(), prePayloadResearchV3(), prePayloadArchitectureSpikeV2(), prePayloadOpsRunbookV2(), prePayloadStaticAnalysisV2(),
			preFailureImplementationV4(), preFailureBreakFixV4(), preFailureGenericOneOffV4(), preFailureResearchV4(), preFailureArchitectureSpikeV3(), preFailureOpsRunbookV3(), preFailureStaticAnalysisV3(),
			releasedBreakFixV5(),
		},
		BuiltinWorkflowDefinitions()...,
	)
}

func NewBuiltinWorkflowRegistry() DefinitionRegistry {
	registry := NewWorkflowDefinitionRegistry()
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
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
	var latest RegisteredDefinition
	for _, definition := range BuiltinWorkflowDefinitions() {
		if definition.Ref == ref && definition.Version > latest.Definition.Version {
			registered, ok := BuiltinWorkflowRegistry().Lookup(ref, definition.Version)
			if !ok {
				return RegisteredDefinition{}, definitionFailure(KindDefinitionDigestMismatch, "workflow type reference is not registered")
			}
			latest = registered
		}
	}
	if latest.Definition.Ref != "" {
		return latest, nil
	}
	return RegisteredDefinition{}, definitionFailure(KindDefinitionDigestMismatch, "workflow type reference is not registered")
}

// dispatchableStepKinds derives the workflow step kinds that may carry the
// worker-action set: the union of the bindings the generated
// lane-step dispatch join names (#892). A step kind outside the union hosts
// no lane at all, so the pair is not composed onto it.
func dispatchableStepKinds() map[string]bool {
	kinds := make(map[string]bool)
	for _, bindings := range laneStepDispatchKinds {
		for _, kind := range bindings {
			kinds[kind] = true
		}
	}
	return kinds
}

// LaneStepDispatchAllowed reports whether a lane of the given capability
// class may be dispatched at a step of the given kind. It is the dispatch-time
// half of the join: definition composition attaches the action set wherever
// some lane may dispatch, and this gate refuses the specific lane whose class
// the step kind does not admit.
func LaneStepDispatchAllowed(capabilityClass string, kind WorkflowStepKind) bool {
	for _, allowed := range laneStepDispatchKinds[capabilityClass] {
		if WorkflowStepKind(allowed) == kind {
			return true
		}
	}
	return false
}

// withWorkerActions composes the current worker actions onto a shipped
// definition. Prior definition versions use withWorkerActionsBeforeFailure so
// their immutable digests do not acquire record_worker_failure.
func withWorkerActions(definition WorkflowDefinition, payloadContracts bool) WorkflowDefinition {
	return withWorkerActionsForVersion(definition, payloadContracts, true)
}

func withWorkerActionsBeforeFailure(definition WorkflowDefinition, payloadContracts bool) WorkflowDefinition {
	return withWorkerActionsForVersion(definition, payloadContracts, false)
}

// withWorkerActionsForVersion lands worker actions on every non-terminal step
// whose kind the lane-step dispatch join admits (#892). An approval-gated step
// remains closed to worker actions.
func withWorkerActionsForVersion(definition WorkflowDefinition, payloadContracts, includeFailureRecord bool) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	acceptance := WorkflowActionDefinition{
		ID: "accept_worker_result", Consequence: ActionInternalSQLite, Approval: ActionApprovalNone, ExecutionMode: ActionAdvance, RequiredCapability: "work_transition",
		Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{
			{Name: "attempt_id", ValueType: PayloadRef, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(128)},
			{Name: "attempt_epoch", ValueType: PayloadInteger, Required: true, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)},
		}},
	}
	failureRecord := WorkflowActionDefinition{
		ID: "record_worker_failure", Consequence: ActionInternalSQLite, Approval: ActionApprovalNone, ExecutionMode: ActionHold, RequiredCapability: "work_transition",
		Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{
			{Name: "attempt_id", ValueType: PayloadRef, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(128)},
			{Name: "attempt_epoch", ValueType: PayloadInteger, Required: true, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)},
		}},
	}
	if payloadContracts {
		acceptance = currentActionDefinition("accept_worker_result", true)
		acceptance.RequiredCapability = "work_transition"
		failureRecord = currentActionDefinition("record_worker_failure", true)
		failureRecord.RequiredCapability = "work_transition"
	}
	// CD-0059 D1/D2/D3: dispatch_worker is the registered action that opens
	// the worker attempt window against the current step epoch. The
	// RequiredCapability is worker_dispatch rather than work_transition so
	// a worker holding only work_transition cannot dispatch a nested worker
	// (CD-0017 D4 becomes structural rather than convention). The lane
	// identity is recorded with the worker-dispatch CLI on consumption;
	// the authorization itself only needs to bind the attempt_id. CD-0067
	// D2 requires the lane packet itself as a structural payload field so
	// the fold can record its canonical digest alongside worker_attempt_id.
	dispatch := WorkflowActionDefinition{
		ID: "dispatch_worker", Consequence: ActionExternalEffect, Approval: ActionApprovalNone, ExecutionMode: ActionFenced, RequiredCapability: "worker_dispatch",
		Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{
			{Name: "attempt_id", ValueType: PayloadRef, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(128)},
			{Name: "worker_packet", ValueType: PayloadObject, Required: true},
		}},
	}
	if payloadContracts {
		dispatch = currentActionDefinition("dispatch_worker", true)
		dispatch.RequiredCapability = "worker_dispatch"
	}
	workerActions := []WorkflowActionDefinition{acceptance, dispatch}
	if includeFailureRecord {
		workerActions = []WorkflowActionDefinition{acceptance, failureRecord, dispatch}
	}
	for _, action := range workerActions {
		definition.AvailableActions = append(definition.AvailableActions, action.ID)
		definition.ActionDefinitions = append(definition.ActionDefinitions, action)
	}
	terminal := make(map[string]bool, len(definition.StepGraph.TerminalSteps))
	for _, id := range definition.StepGraph.TerminalSteps {
		terminal[id] = true
	}
	policies := make(map[string]WorkflowActionDefinition, len(definition.ActionDefinitions))
	for _, action := range definition.ActionDefinitions {
		policies[action.ID] = action
	}
	admitted := dispatchableStepKinds()
	for i := range definition.StepGraph.Steps {
		if terminal[definition.StepGraph.Steps[i].ID] || !admitted[string(definition.StepGraph.Steps[i].Kind)] {
			continue
		}
		// An approval-gated step exits only through its operator action: an
		// ungated advancing action beside the gate would let a worker
		// acceptance leave the step without the operator (the invariant
		// TestApprovalGateStepHasNoOtherAdvancingAction holds). Steps whose
		// kind is human_checkpoint never reach here; this guards the
		// internal_sqlite steps that carry an approval-required action.
		gated := false
		for _, actionID := range definition.StepGraph.Steps[i].Actions {
			if policy, ok := policies[actionID]; ok && policy.Approval == ActionApprovalRequired {
				gated = true
				break
			}
		}
		if gated {
			continue
		}
		for _, action := range workerActions {
			definition.StepGraph.Steps[i].Actions = append(definition.StepGraph.Steps[i].Actions, action.ID)
		}
	}
	return definition
}

// withLegacyWorkerActions reproduces, byte for byte, the worker-action
// composition the frozen version-1 definitions were pinned under (#861):
// the pair on external_effect steps only, and the research family excluded
// by the CD-0059 authoring decision the lane-step dispatch join replaced
// for shipped versions. Editing this rule moves the version-1 digests and
// wedges every instance that pinned them (CD-0115); it is frozen history,
// not living law.
func withLegacyWorkerActions(definition WorkflowDefinition) WorkflowDefinition {
	definition = cloneWorkflowDefinition(definition)
	if definition.WorkKind == WorkKindResearch {
		return definition
	}
	acceptance := WorkflowActionDefinition{
		ID: "accept_worker_result", Consequence: ActionInternalSQLite, Approval: ActionApprovalNone, ExecutionMode: ActionAdvance, RequiredCapability: "work_transition",
		Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{
			{Name: "attempt_id", ValueType: PayloadRef, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(128)},
			{Name: "attempt_epoch", ValueType: PayloadInteger, Required: true, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)},
		}},
	}
	dispatch := WorkflowActionDefinition{
		ID: "dispatch_worker", Consequence: ActionExternalEffect, Approval: ActionApprovalNone, ExecutionMode: ActionFenced, RequiredCapability: "worker_dispatch",
		Payload: WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{
			{Name: "attempt_id", ValueType: PayloadRef, Required: true, MinLength: workflowInt(2), MaxLength: workflowInt(128)},
			{Name: "worker_packet", ValueType: PayloadObject, Required: true},
		}},
	}
	definition.AvailableActions = append(definition.AvailableActions, acceptance.ID, dispatch.ID)
	definition.ActionDefinitions = append(definition.ActionDefinitions, acceptance, dispatch)
	for i := range definition.StepGraph.Steps {
		if definition.StepGraph.Steps[i].Kind == WorkflowStepExternalEffect {
			definition.StepGraph.Steps[i].Actions = append(definition.StepGraph.Steps[i].Actions, acceptance.ID, dispatch.ID)
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

// WorkflowPremiseMaxLength is the single bound for a workflow contract
// premise. The generated continuity read schema carries the same number in
// $defs/workflow_premise; internal/agent's round-trip test fails when the two
// drift apart.
const WorkflowPremiseMaxLength = 4096

// ValidReference reports whether a workflow reference list item is storable.
// The generated continuity read schema carries the same rule in
// $defs/reference; internal/agent's round-trip test fails when the two drift
// apart.
func ValidReference(value string) bool {
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
		if !validWorkflowID(field.Name) || seen[field.Name] || !containsString([]string{"string", "integer", "boolean", "ref", "digest", "string_list", "object", "array"}, string(field.ValueType)) {
			return false
		}
		seen[field.Name] = true
		if field.MinLength != nil && (*field.MinLength < 0 || *field.MinLength > 16384) || field.MaxLength != nil && (*field.MaxLength < 0 || *field.MaxLength > 16384) || field.MinItems != nil && (*field.MinItems < 0 || *field.MinItems > 64) || field.MaxItems != nil && (*field.MaxItems < 0 || *field.MaxItems > 64) || field.Minimum != nil && (*field.Minimum < -2147483648 || *field.Minimum > 2147483647) || field.Maximum != nil && (*field.Maximum < -2147483648 || *field.Maximum > 2147483647) {
			return false
		}
		if field.MinLength != nil && field.MaxLength != nil && *field.MinLength > *field.MaxLength || field.MinItems != nil && field.MaxItems != nil && *field.MinItems > *field.MaxItems || field.Minimum != nil && field.Maximum != nil && *field.Minimum > *field.Maximum {
			return false
		}
		if len(field.Enum) > 32 || !uniqueStrings(field.Enum) || len(field.Enum) != 0 && field.ValueType != PayloadString && field.ValueType != PayloadRef && field.ValueType != PayloadStringList || field.SchemaRef != "" && !validWorkflowID(field.SchemaRef) || field.ItemRef != "" && (field.ValueType != PayloadStringList || !validWorkflowID(field.ItemRef)) {
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

// ActionEventShape names which event a completed action appends. The shape is a
// property of the action, but applyWorkflowActionRawTx decides it from three
// separate pieces of control flow — the ActionCheckpoint branch, the "complete"
// guard, and whether workflowSemanticActionEvents has a case arm. Declaring it
// here makes the choice explicit at registration, so an action cannot acquire a
// shape by omission.
type ActionEventShape string

const (
	// ActionEventCheckpoint appends WorkflowActionCheckpointed. Reached by
	// execution mode, before the semantic switch, so these actions require
	// ExecutionMode ActionCheckpoint and must not carry a semantic case arm.
	ActionEventCheckpoint ActionEventShape = "checkpoint"
	// ActionEventTyped appends an event carrying the action's own payload shape,
	// built by a case arm in workflowSemanticActionEvents.
	ActionEventTyped ActionEventShape = "typed"
	// ActionEventCompletion appends the terminal completion events. Held by
	// "complete" alone, which the dispatcher excludes from the semantic switch.
	ActionEventCompletion ActionEventShape = "completion"
	// ActionEventGeneric appends WorkflowActionCompleted and nothing else. The
	// action is recorded, but its payload is not projected into a typed event.
	ActionEventGeneric ActionEventShape = "generic"
)

type builtinActionPolicy struct {
	Consequence   ActionConsequence
	Approval      ActionApproval
	ExecutionMode ActionExecutionMode
	EventShape    ActionEventShape
	Payload       WorkflowPayloadDefinition
	PublicPayload *WorkflowPayloadDefinition
}

func actionPolicy(consequence ActionConsequence, approval ActionApproval, mode ActionExecutionMode, shape ActionEventShape, fields ...WorkflowPayloadField) builtinActionPolicy {
	if fields == nil {
		fields = []WorkflowPayloadField{}
	}
	return builtinActionPolicy{Consequence: consequence, Approval: approval, ExecutionMode: mode, EventShape: shape, Payload: WorkflowPayloadDefinition{Closed: true, Fields: fields}}
}

func publicActionPolicy(policy builtinActionPolicy, fields ...WorkflowPayloadField) builtinActionPolicy {
	policy.PublicPayload = &WorkflowPayloadDefinition{Closed: true, Fields: fields}
	return policy
}

func actionStringField(name string, required bool, max int64) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadString, Required: required, MinLength: workflowInt(1), MaxLength: workflowInt(max)}
}

func actionRefField(name string, required bool) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadRef, Required: required, MinLength: workflowInt(2), MaxLength: workflowInt(128)}
}

func actionListField(name string, required bool, min, max int64) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MinItems: workflowInt(min), MaxItems: workflowInt(max), ItemRef: "reference"}
}

func actionEnumListField(name string, required bool, min, max int64, values ...string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MinItems: workflowInt(min), MaxItems: workflowInt(max), Enum: values}
}

func actionLawListField(name string, required bool, min, max int64) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MinItems: workflowInt(min), MaxItems: workflowInt(max), ItemRef: "law_id"}
}

func actionIntegerField(name string, required bool, min, max int64) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadInteger, Required: required, Minimum: workflowInt(min), Maximum: workflowInt(max)}
}

func actionEnumField(name string, required bool, values ...string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadString, Required: required, Enum: values}
}

func actionObjectField(name string, required bool, schemaRef string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadObject, Required: required, SchemaRef: schemaRef}
}

func actionArrayField(name string, required bool, min, max int64, schemaRef string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadArray, Required: required, MinItems: workflowInt(min), MaxItems: workflowInt(max), SchemaRef: schemaRef}
}

func evidenceBindingActionFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionStringField("evidence_ref", false, 2048),
		actionEnumField("evidence_kind", false, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"),
		actionStringField("immutable_subject_ref", false, 2048),
		actionRefField("producer_id", false),
		actionRefField("producer_run_ref", false),
		actionRefField("producer_watermark", false),
	}
}

func nativeRunActionFields(statuses ...string) []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionRefField("run_id", true),
		actionStringField("native_subject_ref", true, 2048),
		actionEnumField("status", true, statuses...),
		actionStringField("evidence_ref", true, 2048),
		{Name: "evidence_digest", ValueType: PayloadDigest, Required: true},
		actionStringField("asserted_at", false, 64),
	}
}

var builtinActionPolicies = map[string]builtinActionPolicy{
	"record_proposal":  actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	"record_discovery": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	"record_design":    actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	"approve_contract": actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionAdvance, ActionEventTyped,
		actionEnumField("route_convention", false, "workflow_action"),
		actionListField("route_conventions", false, 0, 16),
		actionListField("proposed_route_conventions", false, 0, 16),
		actionListField("required_route_conventions", false, 0, 16),
		actionIntegerField("contract_version", false, 1, 2147483647),
		actionStringField("premise", false, WorkflowPremiseMaxLength),
		actionArrayField("outcome_predicates", true, 1, 8, "workflow_action_outcome_predicates"),
		actionEnumListField("required_evidence", false, 0, 7, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"),
		actionLawListField("spec_mandate", false, 0, 32),
		actionLawListField("law_modifies", false, 0, 32),
		actionEnumField("rigor_class", false, "prototype_internal", "prototype_trusted", "prototype_public", "prototype_safety_critical", "production_internal", "production_trusted", "production_public", "production_safety_critical", "critical_internal", "critical_trusted", "critical_public", "critical_safety_critical"),
		actionObjectField("architecture_binding", false, "architecture_binding"),
	),
	"start_execution":      actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced, ActionEventGeneric),
	"checkpoint_execution": actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint, ActionEventCheckpoint),
	"bind_evidence":        actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped, evidenceBindingActionFields()...),
	"declare_impact": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped,
		actionRefField("target_work_id", true), actionRefField("edge_id", false),
		actionEnumField("edge_kind", false, "modifies", "depends_on", "forward_link"),
		actionEnumField("edge_class", false, "hard", "soft", "none"),
		actionEnumField("severity", false, "breaking", "non-breaking", "informational"),
	),
	"link_successor": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped,
		actionEnumField("relation", false, "forward_link"), actionObjectField("relation_data", false, "workflow_forward_relation"), actionRefField("successor_work_id", true),
	),
	"record_verdict": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped,
		actionIntegerField("contract_version", false, 1, 2147483647), actionRefField("predicate_id", true),
		actionEnumField("verdict_kind", false, "ok", "outcome_mismatch", "insufficient_evidence"),
		actionStringField("verdict_actor_ref", false, 70), actionListField("evaluation_evidence", false, 1, 32),
		WorkflowPayloadField{Name: "incomparable_with_approved", ValueType: PayloadBoolean},
	),
	"confirm_premise": actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionAdvance, ActionEventTyped,
		actionIntegerField("contract_version", false, 1, 2147483647),
	),
	"complete": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventCompletion,
		actionStringField("evidence_commit", false, 128), actionStringField("current_commit", false, 128),
		actionObjectField("payload", false, "workflow_completion_payload"), actionStringField("verdict_actor_ref", false, 70),
		actionEnumField("impact_verdict", true, "breaking", "non-breaking"), actionListField("evidence_refs", false, 1, 32),
		actionEnumField("evidence_kind", false, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"),
	),
	"record_reproduction": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	"record_root_cause":   actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	"start_repair":        actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced, ActionEventGeneric),
	"checkpoint_repair":   actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint, ActionEventCheckpoint),
	"frame_research":      actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventGeneric),
	"record_finding":      actionPolicy(ActionCrossAuthority, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	"revise_candidates": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventTyped,
		actionIntegerField("contract_version", false, 1, 2147483647),
		actionEnumField("candidate_kind", false, "work_item", "product", "project"), actionRefField("candidate_ref", false),
		actionListField("added", false, 1, 64), actionListField("candidate_ids", false, 1, 64), actionListField("removed", false, 1, 64),
	),
	"record_report":     actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"record_conclusion": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventGeneric),
	"frame_question":    actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventGeneric),
	"record_research":   actionPolicy(ActionCrossAuthority, ActionApprovalNone, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"record_option":     actionPolicy(ActionCrossAuthority, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	"start_poc":         actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced, ActionEventGeneric),
	"checkpoint_poc":    actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint, ActionEventCheckpoint),
	"discard_poc":       actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	"record_decision":   actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventCheckpoint),
	"accept_decision":   actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"approve_operation": actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"start_run":         actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced, ActionEventTyped, nativeRunActionFields("started", "failed_to_start")...),
	"checkpoint_run":    actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint, ActionEventCheckpoint),
	"add_condition": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped,
		actionRefField("condition_id", false), actionEnumField("await_type", false, "pr_merge", "ci_result", "timer", "human_approval", "remote_work_state"),
		actionRefField("await_ref", false), actionRefField("resolution_authority", false), actionIntegerField("expected_within_seconds", false, 1, 31536000),
	),
	"resolve_condition": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped,
		actionRefField("condition_id", false), actionListField("resolution_evidence", false, 1, 32), actionRefField("resolved_by_event", false),
	),
	"cancel_condition": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped,
		actionRefField("condition_id", false), actionRefField("cancellation_authority", false), actionListField("cancellation_evidence", false, 1, 32), actionRefField("cancelled_by_event", false),
	),
	"record_health":       actionPolicy(ActionCrossAuthority, ActionApprovalNone, ActionAdvance, ActionEventTyped, nativeRunActionFields("healthy", "degraded", "failed")...),
	"rollback_run":        actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced, ActionEventTyped, nativeRunActionFields("rolled_back", "partially_rolled_back", "rollback_failed")...),
	"cleanup_run":         actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped, nativeRunActionFields("cleaned", "cleanup_failed")...),
	"declare_scope":       actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventGeneric),
	"run_analysis":        actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced, ActionEventGeneric),
	"checkpoint_analysis": actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint, ActionEventCheckpoint),
	"start_action":        actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced, ActionEventGeneric),
	"checkpoint_action":   actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionCheckpoint, ActionEventCheckpoint),
	"checkpoint_context": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped,
		actionRefField("checkpoint_id", false), actionIntegerField("checkpoint_sequence", false, 1, 2147483647),
		actionStringField("active_unit", true, 256), actionStringField("hypothesis", true, 4096), actionStringField("diagnosis", true, 4096), actionStringField("strategy", true, 4096),
		actionListField("touched_refs", true, 1, 64), actionListField("evidence_refs", true, 1, 64), actionListField("pending_questions", true, 0, 16), actionListField("pending_decisions", true, 0, 16),
	),
	"cross_context_boundary": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventTyped,
		actionEnumField("boundary_kind", true, "summary", "restart"), actionEnumField("mode", true, "summary", "restart"), actionRefField("checkpoint_id", true),
		actionIntegerField("boundary_sequence", false, 1, 2147483647), actionIntegerField("checkpoint_sequence", false, 1, 2147483647), actionStringField("summary", true, 16384),
		WorkflowPayloadField{Name: "restart", ValueType: PayloadBoolean},
	),
	"record_delivery": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventGeneric),
	"accept_worker_result": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionAdvance, ActionEventTyped,
		actionRefField("attempt_id", true), actionIntegerField("attempt_epoch", true, 1, 2147483647),
	),
	"record_worker_failure": actionPolicy(ActionInternalSQLite, ActionApprovalNone, ActionHold, ActionEventGeneric,
		actionRefField("attempt_id", true), actionIntegerField("attempt_epoch", true, 1, 2147483647),
	),
	"dispatch_worker": publicActionPolicy(actionPolicy(ActionExternalEffect, ActionApprovalNone, ActionFenced, ActionEventGeneric,
		actionRefField("attempt_id", true), actionObjectField("worker_packet", true, "worker_packet"),
	), actionRefField("lane_id", true)),
	"supersede_contract": actionPolicy(ActionInternalSQLite, ActionApprovalRequired, ActionAdvance, ActionEventTyped),
}

func currentActionDefinition(id string, payloadContracts bool) WorkflowActionDefinition {
	policy, ok := builtinActionPolicies[id]
	if !ok {
		panic("built-in workflow action policy is not declared: " + id)
	}
	payload := WorkflowPayloadDefinition{Fields: []WorkflowPayloadField{}}
	var publicPayload *WorkflowPayloadDefinition
	if payloadContracts {
		payload = policy.Payload
		publicPayload = policy.PublicPayload
	}
	return WorkflowActionDefinition{ID: id, Consequence: policy.Consequence, Approval: policy.Approval, ExecutionMode: policy.ExecutionMode, Payload: payload, PublicPayload: publicPayload}
}

func actionDefinitions(ids []string, payloadContracts bool) []WorkflowActionDefinition {
	result := make([]WorkflowActionDefinition, 0, len(ids))
	for _, id := range ids {
		result = append(result, currentActionDefinition(id, payloadContracts))
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
	// Recovery actions can be outside the pinned root list.
	if actionID == "supersede_contract" || actionID == "record_verdict" {
		policy, ok := builtinActionPolicies[actionID]
		return policy.ExecutionMode, ok
	}
	return "", false
}

func workflowInt(value int64) *int64 { return &value }

func withContinuityActions(definition WorkflowDefinition, payloadContracts bool) WorkflowDefinition {
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
		{ID: "cross_context_boundary", Consequence: ActionInternalSQLite, Approval: ActionApprovalNone, ExecutionMode: ActionHold, Payload: WorkflowPayloadDefinition{Fields: boundaryFields}},
	}
	if payloadContracts {
		continuity = []WorkflowActionDefinition{currentActionDefinition("checkpoint_context", true), currentActionDefinition("cross_context_boundary", true)}
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
func baseDefinition(ref string, kind WorkKind, g WorkflowStepGraph, actions []string, evidence []EvidenceKind, outcome WorkflowOutcomeSchema, successors []WorkKind, payloadContracts bool) WorkflowDefinition {
	changesProductTruth := workKindMayChangeProductTruth(kind)
	return WorkflowDefinition{Ref: ref, Version: 1, WorkKind: kind, ChangesProductTruth: &changesProductTruth, StepGraph: g, AvailableActions: actions, ActionDefinitions: actionDefinitions(actions, payloadContracts), RequiredEvidenceKinds: evidence, OutcomeSchema: outcome, RigorRules: []WorkflowRigorRule{{Maturity: "prototype", AudienceBand: "internal", RequiredEvidenceKinds: []EvidenceKind{EvidenceVerification}}}, StalenessRules: []WorkflowStalenessRule{}, CompositionRules: WorkflowCompositionRules{ForwardLinkOnly: true, AllowedSuccessorWorkKinds: successors, ForbiddenCompositions: []WorkflowForbiddenComposition{}}}
}

func builtinImplementation(payloadContracts bool) WorkflowDefinition {
	ids := []string{"proposal", "discovery", "design", "planning", "execution", "acceptance", "release"}
	steps := []WorkflowStep{step("proposal", WorkflowStepInternalSQLite, "record_proposal"), step("discovery", WorkflowStepInternalSQLite, "record_discovery"), step("design", WorkflowStepInternalSQLite, "record_design"), step("planning", WorkflowStepHumanCheckpoint, "approve_contract"), step("execution", WorkflowStepExternalEffect, "start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor", "record_delivery"), step("acceptance", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("release", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "execution", "execution", WorkflowEdgeRetry)
	actions := []string{"record_proposal", "record_discovery", "record_design", "approve_contract", "start_execution", "checkpoint_execution", "bind_evidence", "declare_impact", "link_successor", "record_delivery", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.implementation", WorkKindImplementation, graph(steps, edges, "release"), actions, []EvidenceKind{EvidenceVerification, EvidenceReview}, WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindBreakFix, WorkKindResearch}, payloadContracts)
	d.Version = 5
	return withContinuityActions(d, payloadContracts)
}
func builtinBreakFix(payloadContracts bool) WorkflowDefinition {
	// Break-fix changes Product truth, so the repair route passes through a
	// human approval checkpoint between diagnosis and repair.
	ids := []string{"reproduce", "diagnose", "planning", "repair", "verify", "complete"}
	steps := []WorkflowStep{step("reproduce", WorkflowStepInternalSQLite, "record_reproduction"), step("diagnose", WorkflowStepInternalSQLite, "record_root_cause"), step("planning", WorkflowStepHumanCheckpoint, "approve_contract"), step("repair", WorkflowStepExternalEffect, "start_repair", "checkpoint_repair", "bind_evidence", "link_successor", "record_delivery"), step("verify", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "repair", "repair", WorkflowEdgeRetry)
	actions := []string{"record_reproduction", "record_root_cause", "approve_contract", "start_repair", "checkpoint_repair", "bind_evidence", "link_successor", "record_delivery", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.break_fix", WorkKindBreakFix, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceVerification}, WorkflowOutcomeSchema{DefaultKind: PredicateAbsent, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindResearch}, payloadContracts)
	d.Version = 5
	return withContinuityActions(d, payloadContracts)
}
func builtinResearch(payloadContracts bool) WorkflowDefinition {
	ids := []string{"frame", "investigate", "findings", "conclude", "complete"}
	steps := []WorkflowStep{step("frame", WorkflowStepHumanCheckpoint, "frame_research", "approve_contract"), step("investigate", WorkflowStepCrossAuthority, "record_finding", "revise_candidates", "bind_evidence"), step("findings", WorkflowStepInternalSQLite, "record_report", "link_successor"), step("conclude", WorkflowStepHumanCheckpoint, "record_conclusion", "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	actions := []string{"frame_research", "approve_contract", "record_finding", "revise_candidates", "bind_evidence", "record_report", "link_successor", "record_conclusion", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.research", WorkKindResearch, graph(steps, forward(ids...), "complete"), actions, []EvidenceKind{EvidenceArtifact}, WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"no_change", "resolved", "report_recorded"}, DecisionRecordRequired: false}, []WorkKind{WorkKindBreakFix, WorkKindArchitectureSpike, WorkKindStaticAnalysis}, payloadContracts)
	d.Version = 5
	return withContinuityActions(d, payloadContracts)
}
func builtinArchitectureSpike(payloadContracts bool) WorkflowDefinition {
	ids := []string{"frame", "research", "options", "poc_optional", "decision_record", "review", "acceptance", "complete"}
	steps := []WorkflowStep{step("frame", WorkflowStepHumanCheckpoint, "frame_question", "approve_contract"), step("research", WorkflowStepCrossAuthority, "record_research", "bind_evidence"), step("options", WorkflowStepInternalSQLite, "record_option"), step("poc_optional", WorkflowStepExternalEffect, "start_poc", "checkpoint_poc", "discard_poc", "record_delivery"), step("decision_record", WorkflowStepHumanCheckpoint, "record_decision"), step("review", WorkflowStepHumanCheckpoint, "record_verdict", "accept_decision"), step("acceptance", WorkflowStepHumanCheckpoint, "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "options", "decision_record", WorkflowEdgeOptional)
	edges = addEdge(edges, "poc_optional", "poc_optional", WorkflowEdgeRetry)
	actions := []string{"frame_question", "approve_contract", "record_research", "bind_evidence", "record_option", "start_poc", "checkpoint_poc", "discard_poc", "record_delivery", "record_decision", "record_verdict", "accept_decision", "confirm_premise", "complete"}
	d := baseDefinition("workflow.architecture_spike", WorkKindArchitectureSpike, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceReview, EvidenceApproval, EvidenceArtifact}, WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateOutcome}, AllowedOutcomeTokens: []string{"accepted_decision", "insufficient_evidence"}, DecisionRecordRequired: true}, []WorkKind{WorkKindImplementation, WorkKindResearch, WorkKindStaticAnalysis}, payloadContracts)
	d.Version = 4
	return withContinuityActions(d, payloadContracts)
}
func builtinOpsRunbook(payloadContracts bool) WorkflowDefinition {
	ids := []string{"plan", "approval", "execute", "health", "rollback_optional", "cleanup", "complete"}
	steps := []WorkflowStep{step("plan", WorkflowStepHumanCheckpoint, "approve_contract", "resolve_condition", "cancel_condition"), step("approval", WorkflowStepHumanCheckpoint, "approve_operation", "resolve_condition", "cancel_condition"), step("execute", WorkflowStepExternalEffect, "start_run", "checkpoint_run", "bind_evidence", "add_condition", "resolve_condition", "cancel_condition", "record_delivery"), step("health", WorkflowStepCrossAuthority, "record_health", "record_verdict", "resolve_condition", "cancel_condition"), step("rollback_optional", WorkflowStepExternalEffect, "rollback_run", "resolve_condition", "cancel_condition", "record_delivery"), step("cleanup", WorkflowStepInternalSQLite, "cleanup_run", "confirm_premise", "resolve_condition", "cancel_condition"), step("complete", WorkflowStepInternalSQLite, "complete", "resolve_condition", "cancel_condition")}
	edges := forward(ids...)
	edges = addEdge(edges, "health", "cleanup", WorkflowEdgeOptional)
	edges = addEdge(edges, "execute", "execute", WorkflowEdgeRetry)
	actions := []string{"approve_contract", "approve_operation", "start_run", "checkpoint_run", "bind_evidence", "add_condition", "resolve_condition", "cancel_condition", "record_delivery", "record_health", "record_verdict", "rollback_run", "cleanup_run", "confirm_premise", "complete"}
	d := baseDefinition("workflow.ops_runbook", WorkKindOpsRunbook, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceApproval, EvidenceNativeRun}, WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch}, payloadContracts)
	d.Version = 4
	return withContinuityActions(d, payloadContracts)
}
func builtinStaticAnalysis(payloadContracts bool) WorkflowDefinition {
	ids := []string{"scope", "analyze", "report", "review", "complete"}
	steps := []WorkflowStep{step("scope", WorkflowStepHumanCheckpoint, "approve_contract", "declare_scope"), step("analyze", WorkflowStepExternalEffect, "run_analysis", "checkpoint_analysis", "record_delivery"), step("report", WorkflowStepInternalSQLite, "record_report", "bind_evidence"), step("review", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "analyze", "analyze", WorkflowEdgeRetry)
	actions := []string{"approve_contract", "declare_scope", "run_analysis", "checkpoint_analysis", "record_delivery", "record_report", "bind_evidence", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.static_analysis", WorkKindStaticAnalysis, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceArtifact, EvidenceReview}, WorkflowOutcomeSchema{DefaultKind: PredicateCheck, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateCheck}, AllowedOutcomeTokens: []string{}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch}, payloadContracts)
	d.Version = 4
	return withContinuityActions(d, payloadContracts)
}
func builtinGenericOneOff(payloadContracts bool) WorkflowDefinition {
	ids := []string{"define", "execute", "verify", "complete"}
	steps := []WorkflowStep{step("define", WorkflowStepHumanCheckpoint, "approve_contract"), step("execute", WorkflowStepExternalEffect, "start_action", "checkpoint_action", "bind_evidence", "link_successor", "record_delivery"), step("verify", WorkflowStepHumanCheckpoint, "record_verdict", "confirm_premise"), step("complete", WorkflowStepInternalSQLite, "complete")}
	edges := forward(ids...)
	edges = addEdge(edges, "execute", "execute", WorkflowEdgeRetry)
	actions := []string{"approve_contract", "start_action", "checkpoint_action", "bind_evidence", "link_successor", "record_delivery", "record_verdict", "confirm_premise", "complete"}
	d := baseDefinition("workflow.generic_one_off", WorkKindGenericOneOff, graph(steps, edges, "complete"), actions, []EvidenceKind{EvidenceArtifact}, WorkflowOutcomeSchema{DefaultKind: PredicateOutcome, AllowedKinds: []PredicateKind{PredicateExists, PredicateAbsent, PredicateOutcome, PredicateCheck}, AllowedOutcomeTokens: []string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}, DecisionRecordRequired: false}, []WorkKind{WorkKindImplementation, WorkKindBreakFix, WorkKindResearch, WorkKindArchitectureSpike, WorkKindOpsRunbook, WorkKindStaticAnalysis, WorkKindGenericOneOff}, payloadContracts)
	d.Version = 5
	return withContinuityActions(d, payloadContracts)
}
