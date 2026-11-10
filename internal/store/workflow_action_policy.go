package store

import (
	"regexp"
	"slices"
	"unicode"
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

// workflowEvidenceKinds is the closed evidence kind enum, in declaration order.
var workflowEvidenceKinds = []EvidenceKind{EvidenceVerification, EvidenceReview, EvidenceApproval, EvidenceCommit, EvidenceDurableNote, EvidenceNativeRun, EvidenceArtifact}

type ActionConsequence string

const (
	ActionInternalSQLite ActionConsequence = "internal_sqlite"
	ActionCrossAuthority ActionConsequence = "cross_authority"
	ActionExternalEffect ActionConsequence = "external_effect"
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
	Name              string           `json:"name"`
	ValueType         PayloadValueType `json:"value_type"`
	Required          bool             `json:"required"`
	NonBlank          bool             `json:"non_blank,omitempty"`
	Forbidden         []string         `json:"forbidden_values,omitempty"`
	MinLength         *int64           `json:"min_length,omitempty"`
	MaxLength         *int64           `json:"max_length,omitempty"`
	MinItems          *int64           `json:"min_items,omitempty"`
	MaxItems          *int64           `json:"max_items,omitempty"`
	Minimum           *int64           `json:"minimum,omitempty"`
	Maximum           *int64           `json:"maximum,omitempty"`
	Enum              []string         `json:"enum,omitempty"`
	SchemaRef         string           `json:"schema_ref,omitempty"`
	ItemRef           string           `json:"item_ref,omitempty"`
	ItemMinCodePoints *int64           `json:"item_min_code_points,omitempty"`
	ItemMaxCodePoints *int64           `json:"item_max_code_points,omitempty"`
}

type WorkflowPayloadDefinition struct {
	// Closed makes the declared field list a fail-closed payload contract.
	Closed bool                   `json:"closed,omitempty"`
	Fields []WorkflowPayloadField `json:"fields"`
}

type WorkflowActionDefinition struct {
	ID            string              `json:"id"`
	Consequence   ActionConsequence   `json:"consequence"`
	ExecutionMode ActionExecutionMode `json:"execution_mode,omitempty"`
	// RequiredCapability names the agent capability the dispatcher must
	// present to invoke this action. It is structural: the runtime reads it
	// from the registry entry instead of branching on actionID. A zero
	// value (empty string) carries no enforcement, so actions and tests
	// that do not assert a capability keep a permissive default.
	RequiredCapability string                    `json:"required_capability,omitempty"`
	Payload            WorkflowPayloadDefinition `json:"payload"`
	// PublicPayload replaces Payload only at the agent boundary.
	PublicPayload *WorkflowPayloadDefinition `json:"public_payload,omitempty"`
}

func definitionFailure(kind FailureKind, detail string) error {
	return newFailure(kind, "workflow_definition", detail, false, "reread_entities")
}

var workflowIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,63}$`)
var workflowDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validWorkflowID(value string) bool { return workflowIDPattern.MatchString(value) }
func validActionExecutionMode(value ActionExecutionMode) bool {
	return value == ActionAdvance || value == ActionHold || value == ActionFenced || value == ActionCheckpoint
}

// ValidReference reports whether a new workflow reference is admissible.
// The generated payload schema carries the same rule in $defs/reference; internal/agent's round-trip test fails when the two drift
// apart.
func ValidReference(value string) bool {
	if len(value) < 2 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if unicode.IsSpace(char) {
			return false
		}
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
	return slices.Contains(evidenceStrings(workflowEvidenceKinds), string(value))
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range b {
		if !slices.Contains(a, x) {
			return false
		}
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
	ExecutionMode ActionExecutionMode
	EventShape    ActionEventShape
	Payload       WorkflowPayloadDefinition
	PublicPayload *WorkflowPayloadDefinition
}

func actionPolicy(consequence ActionConsequence, mode ActionExecutionMode, shape ActionEventShape, fields ...WorkflowPayloadField) builtinActionPolicy {
	if fields == nil {
		fields = []WorkflowPayloadField{}
	}
	return builtinActionPolicy{Consequence: consequence, ExecutionMode: mode, EventShape: shape, Payload: WorkflowPayloadDefinition{Closed: true, Fields: fields}}
}

func actionStringField(name string, required bool, upper int64) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadString, Required: required, NonBlank: true, MinLength: workflowInt(1), MaxLength: workflowInt(upper)}
}

func actionRefField(name string, required bool) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadRef, Required: required, MinLength: workflowInt(2), MaxLength: workflowInt(128)}
}

func actionListField(name string, required bool, lower, upper int64) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MinItems: workflowInt(lower), MaxItems: workflowInt(upper), ItemRef: "reference"}
}

// actionIDListField declares a string list whose entries answer to the
// generated id schema, which forbids the slashes a repository path reference
// allows.
func actionIDListField(name string, required bool, lower, upper int64) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MinItems: workflowInt(lower), MaxItems: workflowInt(upper), ItemRef: "id"}
}

func actionProseListField(name string, required bool, lower, upper int64, itemRef string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MinItems: workflowInt(lower), MaxItems: workflowInt(upper), ItemRef: itemRef}
}

const workflowLawListMax = 32

func actionLawListField(name string, required bool) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MinItems: workflowInt(0), MaxItems: workflowInt(workflowLawListMax), ItemRef: "law_id"}
}

func actionContractLawListField(name string, required bool) WorkflowPayloadField {
	field := actionLawListField(name, required)
	field.ItemMinCodePoints = workflowInt(2)
	field.ItemMaxCodePoints = workflowInt(128)
	return field
}

func actionContractVersionField(required bool) WorkflowPayloadField {
	return WorkflowPayloadField{Name: "contract_version", ValueType: PayloadInteger, Required: required, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)}
}

func actionEnumField(name string, required bool, values ...string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadString, Required: required, Enum: values}
}

func actionObjectField(name string, schemaRef string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadObject, Required: false, SchemaRef: schemaRef}
}

func actionArrayField(name string, required bool, upper int64, schemaRef string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadArray, Required: required, MinItems: workflowInt(1), MaxItems: workflowInt(upper), SchemaRef: schemaRef}
}

// actionItemArrayField declares an array whose elements answer to one generated
// schema. It states the per-element reading that schema_ref left to inference.
func actionItemArrayField(name string, required bool, lower, upper int64, itemRef string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadArray, Required: required, MinItems: workflowInt(lower), MaxItems: workflowInt(upper), ItemRef: itemRef}
}

func evidenceBindingActionFields() []WorkflowPayloadField {
	return []WorkflowPayloadField{
		actionRefField("evidence_ref", false),
		actionEnumField("evidence_kind", false, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"),
		actionRefField("immutable_subject_ref", false),
		actionRefField("producer_id", false),
		actionRefField("producer_run_ref", false),
		actionRefField("producer_watermark", false),
	}
}

var builtinActionPolicies = map[string]builtinActionPolicy{
	"record_discovery": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric),
	"approve_contract": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionContractVersionField(false),
		actionContractLawListField("spec_mandate", false),
		actionContractLawListField("law_modifies", false),
		actionListField("legislative_subjects", false, 0, 800),
		actionObjectField("architecture_binding", "architecture_binding"),
	),
	"start_execution":      actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_execution": actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"bind_evidence": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped,
		actionRefField("evidence_ref", false),
		actionEnumField("evidence_kind", false, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"),
		actionRefField("immutable_subject_ref", false), actionRefField("producer_id", false),
		actionRefField("producer_run_ref", false), actionRefField("producer_watermark", false),
	),
	"link_successor": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped,
		actionEnumField("relation", false, "forward_link"), actionObjectField("relation_data", "workflow_forward_relation"), actionRefField("successor_work_id", true),
	),
	"confirm_premise": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionContractVersionField(false),
	),
	"complete": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventCompletion,
		actionStringField("evidence_commit", false, 128), actionStringField("current_commit", false, 128),
		actionObjectField("payload", "workflow_completion_payload"),
		actionEnumField("impact_verdict", true, "breaking", "non-breaking"), actionListField("evidence_refs", false, 1, 32),
	),
	"record_reproduction": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric),
	"record_root_cause":   actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric),
	"start_repair":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_repair":   actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"start_refine":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_refine":   actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"record_diagnosis": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		WorkflowPayloadField{Name: "reproduction", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		WorkflowPayloadField{Name: "root_cause", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
	),
	"frame_research":      actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric),
	"record_finding":      actionPolicy(ActionCrossAuthority, ActionAdvance, ActionEventGeneric),
	"record_report":       actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"record_conclusion":   actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric),
	"frame_question":      actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric),
	"record_option":       actionPolicy(ActionCrossAuthority, ActionAdvance, ActionEventGeneric),
	"start_poc":           actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_poc":      actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"discard_poc":         actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric),
	"approve_operation":   actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"start_run":           actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_run":      actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"record_health":       actionPolicy(ActionCrossAuthority, ActionAdvance, ActionEventGeneric),
	"rollback_run":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"cleanup_run":         actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric),
	"declare_scope":       actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric),
	"run_analysis":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_analysis": actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"start_action":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_action":   actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"record_delivery": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric,
		actionRefField("delivery_artifact", true), actionEnumField("delivery_state", true, "asserted")),
	// The intake and step plan writes. record_intake records
	// the fourteen decided checks (which found something) and one of four
	// outcomes; record_clarify records the clarification step;
	// pick_type copies the picked type's template into the stored plan;
	// insert_step adds a step between required steps; complete_step completes
	// a plan step, carrying the concord-review receipt a reviewed step
	// requires. record_plan is the six-part plan a Change or Fix records;
	// record_removal_scope lists what a Removal retires and its clauses.
	"record_intake": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionEnumField("outcome", true, "proceed", "already_handled", "decline", "split_or_merge"),
	),
	"record_clarify": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionProseListField("resolved_questions", true, 0, 16, "clarification_answer_text"),
		WorkflowPayloadField{Name: "note", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
	),
	"pick_type": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionEnumField("type", true, "change", "fix", "research", "removal"),
		WorkflowPayloadField{Name: "reason", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(2048)},
	),
	"insert_step": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped,
		actionRefField("id", true),
		actionIDListField("after", true, 1, 16),
		actionRefField("skill", false), actionRefField("helper", false),
		WorkflowPayloadField{Name: "reviewed", ValueType: PayloadBoolean},
		WorkflowPayloadField{Name: "reason", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(2048)},
	),
	"complete_step": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionRefField("step_id", true),
		WorkflowPayloadField{Name: "note", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		actionRefField("reviewer", false),
		actionEnumField("result", false, "pass", "fixed"),
		actionRefField("commit", false), actionRefField("link", false),
	),
	"record_plan": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		WorkflowPayloadField{Name: "outcomes", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		WorkflowPayloadField{Name: "approach", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		actionArrayField("decisions", true, 16, "workflow_design_decision"),
		actionListField("touches", true, 1, 64),
		WorkflowPayloadField{Name: "merge_split", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		WorkflowPayloadField{Name: "risks", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		WorkflowPayloadField{Name: "reason", ValueType: PayloadString, Required: false, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(2048)},
	),
	"record_removal_scope": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionListField("removed", true, 1, 64),
		actionLawListField("retiring_clauses", true),
		WorkflowPayloadField{Name: "note", ValueType: PayloadString, Required: false, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
	),
	"record_review": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped,
		actionRefField("step_id", true),
		WorkflowPayloadField{Name: "note", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		actionRefField("reviewer", true), actionEnumField("result", true, "pass", "fixed"),
		actionRefField("commit", true), actionRefField("link", true),
	),
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
	return WorkflowActionDefinition{ID: id, Consequence: policy.Consequence, ExecutionMode: policy.ExecutionMode, Payload: payload, PublicPayload: publicPayload}
}

func workflowInt(value int64) *int64 { return &value }
