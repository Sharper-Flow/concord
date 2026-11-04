package store

import (
	"fmt"
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
	Name      string           `json:"name"`
	ValueType PayloadValueType `json:"value_type"`
	Required  bool             `json:"required"`
	NonBlank  bool             `json:"non_blank,omitempty"`
	Forbidden []string         `json:"forbidden_values,omitempty"`
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
	// Closed makes the declared field list a fail-closed payload contract.
	// The payload carries fields only: cross-field rules are version-
	// invariant engine law, so they live on the builtin action policy
	// beside this declaration, never on digest-covered definition content
	// (CON-412).
	Closed bool                   `json:"closed,omitempty"`
	Fields []WorkflowPayloadField `json:"fields"`
}

// WorkflowActionCrossField is the engine action registry's typed declaration
// of one action's version-invariant cross-field payload rules. It lives on
// builtinActionPolicy (and the engine recovery registry for correction
// actions), beside the payload fields it constrains — never on the
// digest-covered WorkflowPayloadDefinition. Core payload validation, the
// fold guards, replay, and the generated published variants all read this
// one declaration, so the enforced and advertised contracts cannot disagree
// (CON-412). validateCrossFieldDeclaration holds its shape.
type WorkflowActionCrossField struct {
	// Combinations declares legal input combinations: under one
	// discriminator value, Requires names fields the payload must carry and
	// Forbids names fields it must omit.
	Combinations []WorkflowPayloadCombination `json:"combinations,omitempty"`
	// Alternatives declares mutually exclusive field groups of which the
	// payload must supply exactly one — one complete group with no field of
	// any other group present.
	Alternatives []WorkflowPayloadAlternative `json:"alternatives,omitempty"`
	// KindMatches declares nested discriminator equalities: when a scalar
	// enum field and an object field are both present, the object's
	// discriminator must hold the scalar's value — outcome_kind and
	// outcome_payload.kind are one value (CON-412). Core payload validation
	// enforces the equality from this declaration and the generated
	// published variants turn each match into closed per-kind branches, so
	// neither side can admit a pair the other refuses.
	KindMatches []WorkflowPayloadKindMatch `json:"kind_matches,omitempty"`
}

// WorkflowPayloadKindMatch declares that a scalar enum field and an object
// field's discriminator hold one value whenever both are present. Remedy
// optionally states the refusal remedy for a payload that breaks the
// equality (CON-412).
type WorkflowPayloadKindMatch struct {
	Field         string `json:"field"`
	Object        string `json:"object"`
	Discriminator string `json:"discriminator"`
	Remedy        string `json:"remedy,omitempty"`
}

// WorkflowPayloadAlternative declares one mutually exclusive alternative
// field group: the payload completes exactly one group, a field of any
// other group stays absent, and Forbids names declared fields that cannot
// ride beside this group even though no group claims them — an
// unconditional member of the payload under another group (record_verdict's
// entry-level fields beside the batch, CD-0198 D1). Remedy optionally
// states the refusal remedy for a payload that carries a forbidden field
// beside the group (CON-412).
type WorkflowPayloadAlternative struct {
	Fields  []string `json:"fields"`
	Forbids []string `json:"forbids,omitempty"`
	Remedy  string   `json:"remedy,omitempty"`
}

// WorkflowPayloadCombination declares one cross-field legal input combination
// a flat field list cannot express: under one discriminator value, Requires
// names fields the payload must carry and Forbids names fields it must omit.
// The declaration lives on the action's registry payload beside the fields it
// constrains; core validation enforces it and the generated core and
// published variants turn each one into a closed per-value field branch, so
// one declaration owns the rule everywhere it applies (CON-412). Remedy
// optionally states the refusal remedy for a payload that breaks the rule.
type WorkflowPayloadCombination struct {
	Field    string   `json:"field"`
	Value    string   `json:"value"`
	Requires []string `json:"requires,omitempty"`
	Forbids  []string `json:"forbids,omitempty"`
	Remedy   string   `json:"remedy,omitempty"`
}

type WorkflowActionDefinition struct {
	ID            string              `json:"id"`
	Consequence   ActionConsequence   `json:"consequence"`
	ExecutionMode ActionExecutionMode `json:"execution_mode,omitempty"`
	// RequiredCapability names the agent capability the dispatcher must
	// present to invoke this action. It is structural: the runtime reads it
	// from the registry entry instead of branching on actionID. A zero
	// value (empty string) carries no enforcement, mirroring the pre-CD-0059
	// surface for legacy actions and tests that do not assert a capability.
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

// WorkflowPremiseMaxLength is the single bound for a workflow contract
// premise. The generated continuity read schema carries the same number in
// $defs/workflow_premise; internal/agent's round-trip test fails when the two
// drift apart.
const WorkflowPremiseMaxLength = 4096

// ValidReference reports whether a new workflow reference is admissible.
// The generated continuity read schema carries the same rule in
// $defs/reference; internal/agent's round-trip test fails when the two drift
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
	return slices.Contains(evidenceStrings(workflowEvidenceKinds), string(value))
}
func validPredicateKind(value PredicateKind) bool {
	return value == PredicateExists || value == PredicateAbsent || value == PredicateOutcome || value == PredicateCheck
}

func validatePayloadFieldBounds(field WorkflowPayloadField) bool {
	if field.NonBlank && field.ValueType != PayloadString && field.ValueType != PayloadRef && field.ValueType != PayloadDigest || len(field.Forbidden) > 32 || !uniqueStrings(field.Forbidden) || len(field.Forbidden) != 0 && field.ValueType != PayloadString && field.ValueType != PayloadRef && field.ValueType != PayloadDigest {
		return false
	}
	if field.MinLength != nil && (*field.MinLength < 0 || *field.MinLength > 16384) || field.MaxLength != nil && (*field.MaxLength < 0 || *field.MaxLength > 16384) || field.MinItems != nil && (*field.MinItems < 0 || *field.MinItems > 64) || field.MaxItems != nil && (*field.MaxItems < 0 || *field.MaxItems > 64) || field.Minimum != nil && (*field.Minimum < -2147483648 || *field.Minimum > 2147483647) || field.Maximum != nil && (*field.Maximum < -2147483648 || *field.Maximum > 2147483647) {
		return false
	}
	if field.MinLength != nil && field.MaxLength != nil && *field.MinLength > *field.MaxLength || field.MinItems != nil && field.MaxItems != nil && *field.MinItems > *field.MaxItems || field.Minimum != nil && field.Maximum != nil && *field.Minimum > *field.Maximum {
		return false
	}
	return true
}

// validateCrossFieldDeclaration holds the shape of an engine policy's
// declared cross-field rules so a declaration that drifts from its own
// fields fails validation rather than publishing a rule nothing enforces.
// A combination must discriminate on the payload's one enum field that
// carries combinations, name only enum-admitted values, cover each value of
// that enum exactly once (so the generated branches are total and mutually
// exclusive), and require or forbid only declared fields. Alternatives must
// name declared fields in pairwise-disjoint groups, and a group's Forbids
// must name declared fields no group claims. One action cannot declare
// both: no generated shape expresses the two rule families at once.
func validateCrossFieldDeclaration(fields []WorkflowPayloadField, cross WorkflowActionCrossField) bool {
	byName := make(map[string]*WorkflowPayloadField, len(fields))
	for i := range fields {
		byName[fields[i].Name] = &fields[i]
	}
	if len(cross.Combinations) != 0 && len(cross.Alternatives) != 0 {
		return false
	}
	return validateCrossFieldCombinations(byName, cross.Combinations) &&
		validateCrossFieldAlternatives(byName, cross.Alternatives) &&
		validateCrossFieldKindMatches(byName, cross.KindMatches)
}

func validateCrossFieldCombinations(byName map[string]*WorkflowPayloadField, combinations []WorkflowPayloadCombination) bool {
	discriminator := ""
	values := map[string]bool{}
	for _, combination := range combinations {
		if discriminator == "" {
			discriminator = combination.Field
		}
		if combination.Field != discriminator || !validWorkflowID(combination.Field) {
			return false
		}
		field, declared := byName[combination.Field]
		if !declared || len(field.Enum) == 0 {
			return false
		}
		if !slices.Contains(field.Enum, combination.Value) || values[combination.Value] {
			return false
		}
		values[combination.Value] = true
		if !validateCrossFieldCombinationFields(byName, combination) {
			return false
		}
	}
	if discriminator != "" {
		for _, value := range byName[discriminator].Enum {
			if !values[value] {
				return false
			}
		}
	}
	return true
}

func validateCrossFieldCombinationFields(byName map[string]*WorkflowPayloadField, combination WorkflowPayloadCombination) bool {
	if len(combination.Requires) == 0 && len(combination.Forbids) == 0 {
		return false
	}
	for _, side := range [][]string{combination.Requires, combination.Forbids} {
		for _, name := range side {
			if _, ok := byName[name]; !ok || name == combination.Field {
				return false
			}
		}
	}
	return true
}

func validateCrossFieldAlternatives(byName map[string]*WorkflowPayloadField, alternatives []WorkflowPayloadAlternative) bool {
	grouped := map[string]bool{}
	for _, group := range alternatives {
		if len(group.Fields) == 0 {
			return false
		}
		seen := map[string]bool{}
		for _, name := range group.Fields {
			if _, ok := byName[name]; !ok || seen[name] || grouped[name] {
				return false
			}
			seen[name] = true
			grouped[name] = true
		}
	}
	for _, group := range alternatives {
		forbidden := map[string]bool{}
		for _, name := range group.Forbids {
			// A forbidden field must be declared and ungrouped: a
			// group member is already excluded by the exactly-one
			// rule, so forbidding it here would state no rule.
			if _, ok := byName[name]; !ok || grouped[name] || forbidden[name] {
				return false
			}
			forbidden[name] = true
		}
	}
	return true
}

func validateCrossFieldKindMatches(byName map[string]*WorkflowPayloadField, matches []WorkflowPayloadKindMatch) bool {
	seenMatches := map[string]bool{}
	for _, match := range matches {
		field, fieldDeclared := byName[match.Field]
		object, objectDeclared := byName[match.Object]
		// Both sides must be declared fields; the scalar side must be a
		// closed enum (the generated per-kind branches const each enum
		// value); the object side must not be the scalar itself; the
		// discriminator names the object member that carries the kind.
		if !fieldDeclared || !objectDeclared || match.Field == match.Object || len(field.Enum) == 0 || match.Discriminator == "" || seenMatches[match.Field+"\x00"+match.Object] {
			return false
		}
		if object.ValueType != PayloadObject {
			return false
		}
		seenMatches[match.Field+"\x00"+match.Object] = true
	}
	return true
}

// validateEngineCrossFieldDeclarations checks every engine-declared
// cross-field rule against the action's own payload fields. The builtin
// table and recovery registry are static, so the builtin registry runs it
// once at construction rather than on every definition validation.
func validateEngineCrossFieldDeclarations() error {
	for id, policy := range builtinActionPolicies {
		if policy.CrossField == nil {
			continue
		}
		fields := policy.Payload.Fields
		if id == "record_verdict" {
			fields = verdictBatchActionFields()
		} else if current, err := catalogAction(id); err == nil {
			fields = current.Payload.Fields
		}
		if !validateCrossFieldDeclaration(fields, *policy.CrossField) {
			return fmt.Errorf("engine cross-field declaration for %s does not match its payload fields", id)
		}
		if policy.PublicPayload != nil && len(policy.CrossField.Combinations) != 0 {
			// The public payload shares the discriminator branches; its
			// fields must carry the same discriminator enum.
			if !validateCrossFieldDeclaration(policy.PublicPayload.Fields, WorkflowActionCrossField{Combinations: policy.CrossField.Combinations}) {
				return fmt.Errorf("engine cross-field declaration for %s does not match its public payload fields", id)
			}
		}
	}
	return nil
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
func validOutcomeToken(value string) bool {
	return slices.Contains([]string{"no_change", "accepted_decision", "insufficient_evidence", "resolved", "remediated", "report_recorded", "completed", "operator_defined"}, value)
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
	// CrossField declares the action's version-invariant cross-field rules.
	// It rides the engine policy, not the digest-covered payload, and every
	// enforcer and the published variants resolve it through
	// workflowActionCrossField (CON-412).
	CrossField *WorkflowActionCrossField
}

func actionPolicy(consequence ActionConsequence, mode ActionExecutionMode, shape ActionEventShape, fields ...WorkflowPayloadField) builtinActionPolicy {
	if fields == nil {
		fields = []WorkflowPayloadField{}
	}
	return builtinActionPolicy{Consequence: consequence, ExecutionMode: mode, EventShape: shape, Payload: WorkflowPayloadDefinition{Closed: true, Fields: fields}}
}

// actionPolicyWithCombinations attaches the action's cross-field legal input
// combinations to its engine policy declaration: the one typed declaration
// core validation enforces and the published variants generate their closed
// per-value branches from (CON-412).
func actionPolicyWithCombinations(policy builtinActionPolicy, combinations ...WorkflowPayloadCombination) builtinActionPolicy {
	policy.CrossField = &WorkflowActionCrossField{Combinations: combinations}
	return policy
}

// actionPolicyWithAlternatives attaches the action's cross-field mutually
// exclusive field groups to its engine policy declaration: the one typed
// declaration core validation enforces and the published variants generate
// their closed per-group branches from (CON-412).
func actionPolicyWithAlternatives(policy builtinActionPolicy, alternatives ...WorkflowPayloadAlternative) builtinActionPolicy {
	policy.CrossField = &WorkflowActionCrossField{Alternatives: alternatives}
	return policy
}

// workflowActionCrossField resolves one action's cross-field declaration
// from its single owner: the builtin engine policy table, or the engine
// recovery registry for correction actions admitted off a pinned step.
// Core preflight, the fold guards, and the exported generator projection
// all read this accessor, so no second copy of a rule can drift (CON-412).
func workflowActionCrossField(actionID string) WorkflowActionCrossField {
	if policy, ok := builtinActionPolicies[actionID]; ok && policy.CrossField != nil {
		return *policy.CrossField
	}
	return WorkflowActionCrossField{}
}

// WorkflowActionCrossFieldRules is the exported projection of
// workflowActionCrossField for the contract generator: the same single
// engine declaration the guards enforce.
func WorkflowActionCrossFieldRules(actionID string) WorkflowActionCrossField {
	return workflowActionCrossField(actionID)
}

// WorkflowActionCanonicalCrossFieldRules projects the enforcing declaration
// onto one closed action payload. Partial rules refuse; vacuous forbids drop.
func WorkflowActionCanonicalCrossFieldRules(actionID string, fields []WorkflowPayloadField) (WorkflowActionCrossField, error) {
	declared := make(map[string]bool, len(fields))
	for _, field := range fields {
		declared[field.Name] = true
	}
	cross := WorkflowActionCrossFieldRules(actionID)
	projected := WorkflowActionCrossField{}
	if len(cross.Combinations) != 0 && len(cross.Alternatives) != 0 {
		return WorkflowActionCrossField{}, fmt.Errorf("action %s declares both cross-field rule families", actionID)
	}
	for _, combination := range cross.Combinations {
		if !declared[combination.Field] {
			continue
		}
		projectedCombination, err := workflowCanonicalCombination(actionID, declared, combination)
		if err != nil {
			return WorkflowActionCrossField{}, err
		}
		projected.Combinations = append(projected.Combinations, projectedCombination)
	}
	for _, group := range cross.Alternatives {
		switch workflowDeclaredFieldCount(declared, group.Fields) {
		case 0:
			continue
		case len(group.Fields):
			projectedGroup := WorkflowPayloadAlternative{Fields: group.Fields, Remedy: group.Remedy}
			for _, name := range group.Forbids {
				if declared[name] {
					projectedGroup.Forbids = append(projectedGroup.Forbids, name)
				}
			}
			projected.Alternatives = append(projected.Alternatives, projectedGroup)
		default:
			return WorkflowActionCrossField{}, fmt.Errorf("canonical cross-field projection for %s: alternative group %v partially overlaps the declared fields", actionID, group.Fields)
		}
	}
	for _, match := range cross.KindMatches {
		fieldDeclared, objectDeclared := declared[match.Field], declared[match.Object]
		switch {
		case !fieldDeclared && !objectDeclared:
			continue
		case !fieldDeclared || !objectDeclared || match.Discriminator == "":
			return WorkflowActionCrossField{}, fmt.Errorf("canonical cross-field projection for %s: kind match %s/%s partially overlaps the declared fields", actionID, match.Field, match.Object)
		default:
			projected.KindMatches = append(projected.KindMatches, match)
		}
	}
	return projected, nil
}

func workflowDeclaredFieldCount(declared map[string]bool, names []string) int {
	count := 0
	for _, name := range names {
		if declared[name] {
			count++
		}
	}
	return count
}

func workflowCanonicalCombination(actionID string, declared map[string]bool, combination WorkflowPayloadCombination) (WorkflowPayloadCombination, error) {
	projected := WorkflowPayloadCombination{Field: combination.Field, Value: combination.Value, Remedy: combination.Remedy}
	for _, name := range combination.Requires {
		if !declared[name] {
			return WorkflowPayloadCombination{}, fmt.Errorf("canonical cross-field projection for %s: combination on %s requires undeclared field %s", actionID, combination.Field, name)
		}
		projected.Requires = append(projected.Requires, name)
	}
	for _, name := range combination.Forbids {
		if declared[name] {
			projected.Forbids = append(projected.Forbids, name)
		}
	}
	return projected, nil
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

func actionEnumListField(name string, required bool, upper int64, values ...string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MaxItems: workflowInt(upper), Enum: values}
}

// workflowDecisionFactField declares the copied decision-fact field a
// consequence_v1 contract carries: a required closed string list whose
// members answer to CD-0243's six-class workflowDecisionDomainSet. The empty
// list records engineering and absence has no declared shape. The enum storage
// is copied, so the field never aliases the canonical set.
func workflowDecisionFactField() WorkflowPayloadField {
	return actionEnumListField("decision_domains", true, int64(len(workflowDecisionDomainSet)), slices.Clone(workflowDecisionDomainSet)...)
}

func actionProseListField(name string, required bool, lower, upper int64, itemRef string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MinItems: workflowInt(lower), MaxItems: workflowInt(upper), ItemRef: itemRef}
}

// actionLawListField declares a law-id list: the mandate may be empty and
// holds at most 32 ids.
func actionLawListField(name string, required bool) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadStringList, Required: required, MinItems: workflowInt(0), MaxItems: workflowInt(32), ItemRef: "law_id"}
}

func actionIntegerField(name string, required bool) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadInteger, Required: required, Minimum: workflowInt(1), Maximum: workflowInt(2147483647)}
}

func actionEnumField(name string, required bool, values ...string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadString, Required: required, Enum: values}
}

func actionObjectField(name string, required bool, schemaRef string) WorkflowPayloadField {
	return WorkflowPayloadField{Name: name, ValueType: PayloadObject, Required: required, SchemaRef: schemaRef}
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
		actionObjectField("verification_report", false, "workflow_verification_report"),
	}
}

const workflowPremiseFallback = "workflow premise"

func actionPremiseField() WorkflowPayloadField {
	// The enforcing floor is workflowString's workflowStringMinBytes bytes
	// (UTF-8 bytes, not runes — see validateWorkflowPayloadValue, which now
	// counts bytes like every fold guard). The current action catalog raises
	// approve_contract's declared minimum to that floor (premiseFloor), and
	// the contract-payload workflowString guard refuses a shorter premise
	// wherever this base field applies.
	return WorkflowPayloadField{Name: "premise", ValueType: PayloadString, Required: true, NonBlank: true, Forbidden: []string{workflowPremiseFallback}, MinLength: workflowInt(1), MaxLength: workflowInt(WorkflowPremiseMaxLength)}
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

// workflowAlignmentCombinationTeaching states the record_alignment cross-field
// rule the registry's combination declarations enforce through
// validateWorkflowPayloadCrossFieldRules; the teaching projection publishes
// the same text from this declaration site.
const workflowAlignmentCombinationTeaching = "record_alignment couples outcome and related_ids: outcome related_found requires a non-empty related_ids list, and outcome none_found must omit related_ids entirely; the store refuses any other combination."

var builtinActionPolicies = map[string]builtinActionPolicy{
	"record_discovery": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric),
	"approve_contract": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionEnumListField("decision_domains", true, 6, workflowDecisionDomainSet...),
		actionEnumField("route_convention", false, "workflow_action"),
		actionListField("route_conventions", false, 0, 16),
		actionListField("proposed_route_conventions", false, 0, 16),
		actionListField("required_route_conventions", false, 0, 16),
		actionIntegerField("contract_version", false),
		actionPremiseField(),
		actionArrayField("outcome_predicates", true, 8, "workflow_action_outcome_predicates"),
		actionEnumListField("required_evidence", false, 7, "verification", "review", "approval", "commit", "durable_note", "native_run", "artifact"),
		actionLawListField("spec_mandate", false),
		actionLawListField("law_modifies", false),
		actionEnumField("rigor_class", false, "prototype_internal", "prototype_trusted", "prototype_public", "prototype_safety_critical", "production_internal", "production_trusted", "production_public", "production_safety_critical", "critical_internal", "critical_trusted", "critical_public", "critical_safety_critical"),
		actionObjectField("architecture_binding", false, "architecture_binding"),
	),
	"start_execution":      actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_execution": actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"bind_evidence":        actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped, evidenceBindingActionFields()...),
	"link_successor": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped,
		actionEnumField("relation", false, "forward_link"), actionObjectField("relation_data", false, "workflow_forward_relation"), actionRefField("successor_work_id", true),
	),
	"record_verdict": actionPolicyWithAlternatives(actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped,
		actionIntegerField("contract_version", false), actionRefField("predicate_id", true),
		actionEnumField("verdict_kind", false, "ok", "outcome_mismatch", "insufficient_evidence"),
		actionStringField("verdict_actor_ref", false, 70), actionListField("evaluation_evidence", false, 1, 32),
		WorkflowPayloadField{Name: "incomparable_with_approved", ValueType: PayloadBoolean},
	),
		WorkflowPayloadAlternative{Fields: []string{"predicate_id"}},
		WorkflowPayloadAlternative{
			Fields:  []string{"verdicts"},
			Forbids: []string{"verdict_kind", "evaluation_evidence", "incomparable_with_approved"},
			Remedy:  "move the field into the verdicts entries",
		},
	),
	"confirm_premise": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionIntegerField("contract_version", false),
	),
	"complete": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventCompletion,
		actionStringField("evidence_commit", false, 128), actionStringField("current_commit", false, 128),
		actionObjectField("payload", false, "workflow_completion_payload"), actionStringField("verdict_actor_ref", false, 70),
		actionEnumField("impact_verdict", true, "breaking", "non-breaking"), actionListField("evidence_refs", false, 1, 32),
	),
	"record_reproduction": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric),
	"record_root_cause":   actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric),
	"start_repair":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_repair":   actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"start_refine":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_refine":   actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	// CD-0198 D2: the typed diagnosis record. The payload carries one
	// required section per merged source action: the reproduction record,
	// the CD-0156 alignment search whose searched/outcome/related_ids
	// fields and legal input combinations the merged action carries
	// unchanged, and the root-cause record. The projection fold writes the
	// same workflow_backlog_alignment rows the alignment action wrote and
	// creates no relation.
	"record_diagnosis": actionPolicyWithCombinations(actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		// The bounds match the searched floor the fold enforces through
		// workflowString: a schema-valid section cannot be refused by the
		// fold.
		WorkflowPayloadField{Name: "reproduction", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		WorkflowPayloadField{Name: "searched", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
		actionEnumField("outcome", true, "related_found", "none_found"),
		actionIDListField("related_ids", false, 1, 64),
		WorkflowPayloadField{Name: "root_cause", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
	),
		WorkflowPayloadCombination{Field: "outcome", Value: "related_found", Requires: []string{"related_ids"}, Remedy: "name the related work items the search found"},
		WorkflowPayloadCombination{Field: "outcome", Value: "none_found", Forbids: []string{"related_ids"}, Remedy: "drop related_ids or record outcome related_found"},
	),
	"frame_research": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric),
	"record_finding": actionPolicy(ActionCrossAuthority, ActionAdvance, ActionEventGeneric),
	"revise_candidates": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionIntegerField("contract_version", false),
		actionEnumField("candidate_kind", false, "work_item", "product", "project"), actionRefField("candidate_ref", false),
		actionListField("added", false, 1, 64), actionListField("candidate_ids", false, 1, 64), actionListField("removed", false, 1, 64),
	),
	"record_report":       actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"record_conclusion":   actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric),
	"frame_question":      actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric),
	"record_research":     actionPolicy(ActionCrossAuthority, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"record_option":       actionPolicy(ActionCrossAuthority, ActionAdvance, ActionEventGeneric),
	"start_poc":           actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_poc":      actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"discard_poc":         actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric),
	"accept_decision":     actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"approve_operation":   actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped, evidenceBindingActionFields()...),
	"start_run":           actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric, nativeRunActionFields("started", "failed_to_start")...),
	"checkpoint_run":      actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"record_health":       actionPolicy(ActionCrossAuthority, ActionAdvance, ActionEventGeneric, nativeRunActionFields("healthy", "degraded", "failed")...),
	"rollback_run":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric, nativeRunActionFields("rolled_back", "partially_rolled_back", "rollback_failed")...),
	"cleanup_run":         actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric, nativeRunActionFields("cleaned", "cleanup_failed")...),
	"declare_scope":       actionPolicy(ActionInternalSQLite, ActionHold, ActionEventGeneric),
	"run_analysis":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_analysis": actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"start_action":        actionPolicy(ActionExternalEffect, ActionFenced, ActionEventGeneric),
	"checkpoint_action":   actionPolicy(ActionExternalEffect, ActionCheckpoint, ActionEventCheckpoint),
	"checkpoint_context": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped,
		actionRefField("checkpoint_id", false), actionIntegerField("checkpoint_sequence", false),
		actionStringField("active_unit", true, 256), actionStringField("hypothesis", true, 4096), actionStringField("diagnosis", true, 4096), actionStringField("strategy", true, 4096),
		actionListField("touched_refs", true, 1, 64), actionListField("evidence_refs", true, 1, 64), actionListField("pending_questions", true, 0, 16), actionListField("pending_decisions", true, 0, 16),
	),
	"cross_context_boundary": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped,
		actionEnumField("boundary_kind", true, "summary", "restart"), actionEnumField("mode", true, "summary", "restart"), actionRefField("checkpoint_id", true),
		actionIntegerField("boundary_sequence", false), actionIntegerField("checkpoint_sequence", false), actionStringField("summary", true, 16384),
		WorkflowPayloadField{Name: "restart", ValueType: PayloadBoolean},
	),
	// CON-887: record_work_context declares the durable working context on
	// the workflow-action route. The action holds the step and advances the
	// work version; its typed event is the durable action boundary, so it
	// joins the omit-generic-completion list. The full current list of
	// readings, the selected earlier finding references, and the new
	// bounded findings each answer to a closed item schema the generated
	// payload contracts carry.
	"record_work_context": actionPolicy(ActionInternalSQLite, ActionHold, ActionEventTyped,
		actionItemArrayField("required_reading", false, 0, WorkContextRequiredReadingMax, "work_context_reading"),
		actionListField("finding_refs", false, 0, WorkContextFindingRefsMax),
		actionItemArrayField("context_findings", false, 0, WorkContextActionFindingsMax, "work_context_finding"),
	),
	"record_delivery": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventGeneric,
		actionRefField("delivery_artifact", true), actionEnumField("delivery_state", true, "asserted")),
	// CON-1574: the pre-plan prelude and plan writes. record_intake records
	// the fourteen decided checks (which found something) and one of five
	// outcomes; record_clarify resolves the open questions intake found;
	// pick_type copies the picked type's template into the stored plan;
	// insert_step adds a step between required steps; complete_step completes
	// a plan step, carrying the concord-review receipt a reviewed step
	// requires. record_plan is the six-part plan a Change or Fix records;
	// record_removal_scope lists what a Removal retires and its clauses.
	"record_intake": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionIDListField("checks_found", false, 0, 14),
		actionEnumField("outcome", true, "proceed", "clarify", "already_handled", "decline", "split_or_merge"),
		WorkflowPayloadField{Name: "open_questions_found", ValueType: PayloadBoolean, Required: true},
		WorkflowPayloadField{Name: "note", ValueType: PayloadString, Required: false, NonBlank: true, MinLength: workflowInt(2), MaxLength: workflowInt(4096)},
	),
	"record_clarify": actionPolicy(ActionInternalSQLite, ActionAdvance, ActionEventTyped,
		actionListField("resolved_questions", true, 1, 16),
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
		actionObjectField("review", true, "workflow_review_report"),
	),
}

// workflowCallerEvidenceBinders names the actions whose completed event binds
// the evidence kind the call submitted. Each payload declares the
// evidence_kind enum (evidenceBindingActionFields), so one action can produce
// any declared kind. The evidence-reference payload revisions select binders
// through workflowCallerEvidenceBinder, and
// TestEvidenceBinderCaseArmMatchesTheProducerTable binds the
// workflowSemanticActionEvents case arm to this list, so producibility and
// routing cannot disagree.
var workflowCallerEvidenceBinders = []string{"bind_evidence", "record_research", "record_report", "accept_decision", "approve_operation"}

func workflowCallerEvidenceBinder(actionID string) bool {
	return slices.Contains(workflowCallerEvidenceBinders, actionID)
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
