package store

// Bounded exploration exercises the real engine with isolated ordered paths.
// A state with no sampled admissible input is a candidate failure, not proof
// that every possible input refuses. Omitted inputs and truncated paths make
// the result inconclusive. Separate finite witnesses prove durable completion.
// Payload synthesis failures are harness failures, never liveness findings.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// livenessDepth bounds the explored path length. A frontier that reaches this
// bound before a terminal step makes the result inconclusive.
const livenessDepth = 12

const livenessStateBudget = 64

// errLivenessProbe forces the probe transaction to roll back after the action
// applied successfully. Its presence in the returned error means the engine
// admitted the action.
var errLivenessProbe = errors.New("liveness probe rollback")

type livenessMove struct {
	action  string
	variant string
	payload json.RawMessage
}

func (m livenessMove) String() string {
	if m.variant == "" {
		return m.action
	}
	return m.action + "[" + m.variant + "]"
}

// livenessProbe is one action's observed admissibility in one state.
type livenessProbe struct {
	move     livenessMove
	failure  string
	failKind string
}

// livenessReport is a non-terminal state whose admitted actions all hold.
// livenessDigest is the one digest value the explorer uses everywhere: in the
// fixture's Domain registry and in every synthesized payload. The engine joins
// some payload digests against recorded state, so a second value would refuse
// on the join and read as a stranding that no agent would meet.
var livenessDigest = "sha256:" + strings.Repeat("d", 64)

type livenessReport struct {
	definition string
	step       string
	path       []livenessMove
	probes     []livenessProbe
}

type livenessExploration struct {
	reports            []livenessReport
	testedStates       int
	testedTransitions  int
	testedProbes       int
	terminalStates     int
	depthBoundStates   int
	depthBoundStepHits map[string]int
	omittedVariants    []string
}

func (result livenessExploration) conclusion() string {
	if result.depthBoundStates != 0 || len(result.omittedVariants) != 0 {
		return "inconclusive"
	}
	if len(result.reports) != 0 {
		return "candidate-found"
	}
	if result.terminalStates == 0 {
		return "inconclusive"
	}
	return "complete-within-model"
}

func (r livenessReport) String() string {
	path := make([]string, 0, len(r.path))
	for _, move := range r.path {
		path = append(path, move.String())
	}
	lines := []string{fmt.Sprintf("%s has no sampled admissible transition at step %q after %s", r.definition, r.step, strings.Join(path, " -> "))}
	for _, probe := range r.probes {
		lines = append(lines, fmt.Sprintf("    %-28s refused (%s): %s", probe.move.String(), probe.failKind, probe.failure))
	}
	return strings.Join(lines, "\n")
}

// livenessActionDefinition resolves the registered payload contract for an
// action, including the two recovery actions the registry holds outside the
// pinned root list.
func livenessActionDefinition(definition WorkflowDefinition, actionID string) (WorkflowActionDefinition, bool) {
	for _, action := range definition.ActionDefinitions {
		if action.ID == actionID {
			return action, true
		}
	}
	return workflowRecoveryActionDefinition(actionID)
}

// livenessVariants enumerates the enum combinations worth exploring for one
// action. A small enum such as verdict_kind selects the control flow the
// recovery guards branch on, so each of its values becomes a separate branch.
// A wide enum such as rigor_class classifies the work rather than routing it,
// so this bounded explorer omits it and reports the result as inconclusive.
// Exploration breadth is a heuristic: a branch missed here costs a finding,
// never a false one, because every reported state is reached by the engine.
// livenessEvidenceKindVariants expands an action that names an evidence kind
// into one move per contract-required kind. Premise confirmation refuses until
// every required kind is bound, and the binding action carries the kind in its
// payload, so one binding leaves the confirmation permanently short and the
// step reads as stranded.
func livenessEvidenceKindVariants(definition WorkflowDefinition, action WorkflowActionDefinition, variants []map[string]string) []map[string]string {
	declares := false
	for _, field := range action.Payload.Fields {
		if field.Name == "evidence_kind" && len(field.Enum) != 0 {
			declares = true
		}
	}
	if !declares || len(definition.RequiredEvidenceKinds) == 0 {
		return variants
	}
	expanded := make([]map[string]string, 0, len(variants)*len(definition.RequiredEvidenceKinds))
	for _, base := range variants {
		for _, kind := range definition.RequiredEvidenceKinds {
			combined := make(map[string]string, len(base)+1)
			for name, chosen := range base {
				combined[name] = chosen
			}
			combined["evidence_kind"] = string(kind)
			expanded = append(expanded, combined)
		}
	}
	return expanded
}

func livenessVariants(action WorkflowActionDefinition) []map[string]string {
	const routingEnumMax = 4
	enums := make([]WorkflowPayloadField, 0, 2)
	for _, field := range action.Payload.Fields {
		if action.ID == "supersede_contract" && field.Name == "outcome_kind" {
			continue // This sampler uses the outcome_predicates arm.
		}
		if len(field.Enum) > 1 && len(field.Enum) <= routingEnumMax {
			enums = append(enums, field)
		}
	}
	sort.Slice(enums, func(i, j int) bool { return enums[i].Name < enums[j].Name })
	variants := []map[string]string{{}}
	for _, field := range enums {
		next := make([]map[string]string, 0, len(variants)*len(field.Enum))
		for _, base := range variants {
			for _, value := range field.Enum {
				combined := make(map[string]string, len(base)+1)
				for name, chosen := range base {
					combined[name] = chosen
				}
				combined[field.Name] = value
				next = append(next, combined)
			}
		}
		variants = next
	}
	return variants
}

func livenessOmittedVariants(definition WorkflowDefinition) []string {
	const routingEnumMax = 4
	omitted := []string{}
	seen := map[string]bool{}
	for _, step := range definition.StepGraph.Steps {
		for _, actionID := range livenessDeclaredActions(definition, step.ID) {
			if seen[actionID] {
				continue
			}
			seen[actionID] = true
			action, ok := livenessActionDefinition(definition, actionID)
			if !ok {
				continue
			}
			for _, field := range action.Payload.Fields {
				if !field.Required {
					omitted = append(omitted, "optional-presence:"+action.ID+"."+field.Name)
				}
				if field.SchemaRef != "" || field.ItemRef != "" {
					omitted = append(omitted, "schema-values:"+action.ID+"."+field.Name)
				}
				if len(field.Enum) > routingEnumMax {
					omitted = append(omitted, action.ID+"."+field.Name)
				}
			}
		}
	}
	for _, kind := range definition.OutcomeSchema.AllowedKinds {
		if kind != definition.OutcomeSchema.DefaultKind {
			omitted = append(omitted, "outcome_kind="+string(kind))
		}
	}
	for i, token := range definition.OutcomeSchema.AllowedOutcomeTokens {
		if definition.OutcomeSchema.DefaultKind != PredicateOutcome || i > 0 {
			omitted = append(omitted, "outcome_token="+token)
		}
	}
	return omitted
}

func livenessVariantLabel(variant map[string]string) string {
	if len(variant) == 0 {
		return ""
	}
	names := make([]string, 0, len(variant))
	for name := range variant {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+variant[name])
	}
	return strings.Join(parts, ",")
}

// livenessValue synthesizes one value that validateWorkflowPayloadValue
// accepts. It mirrors that function's rules field by field; a value it rejects
// is a defect in this synthesizer and fails the test as such.
func livenessValue(field WorkflowPayloadField, chosen string) any {
	// An enum on a list field constrains each item, so a chosen value becomes a
	// single-item list rather than a bare string.
	if chosen != "" {
		if field.ValueType == PayloadStringList {
			return []string{chosen}
		}
		return chosen
	}
	if len(field.Enum) != 0 {
		if field.ValueType == PayloadStringList {
			if field.MinItems == nil || *field.MinItems == 0 {
				return []string{}
			}
			return []string{field.Enum[0]}
		}
		return field.Enum[0]
	}
	switch field.ValueType {
	case PayloadString:
		return livenessText(field, "liveness-probe-value")
	case PayloadRef:
		return livenessText(field, livenessPeerWorkID)
	case PayloadDigest:
		return livenessDigest
	case PayloadInteger:
		value := int64(1)
		if field.Minimum != nil && *field.Minimum > value {
			value = *field.Minimum
		}
		if field.Maximum != nil && *field.Maximum < value {
			value = *field.Maximum
		}
		return value
	case PayloadBoolean:
		return true
	case PayloadStringList:
		// An empty list is the minimal valid value and avoids tripping the
		// cross-field rules that bind list contents to the work kind, such as
		// the refusal of law_modifies on a non-Product-changing approval.
		count := int64(0)
		if field.MinItems != nil && *field.MinItems > count {
			count = *field.MinItems
		}
		if field.MaxItems != nil && *field.MaxItems < count {
			count = *field.MaxItems
		}
		items := make([]string, 0, count)
		for index := int64(0); index < count; index++ {
			items = append(items, livenessListItem(field.Name, field.ItemRef, index))
		}
		return items
	case PayloadObject, PayloadArray:
		// A structural field names its schema, and that schema is the published
		// contract. Building the value from it is the point of the exercise: if
		// this explorer can construct a valid payload from the declaration
		// alone, so can the adapter and so can an agent reading the same
		// contract. A field with no declared schema has nothing to build from.
		// item_ref names the element contract outright. A frozen definition
		// instead points schema_ref at a non-array schema and means the same
		// thing, so the element reading is recovered from the schema's own type.
		element := field.ItemRef
		if element == "" && field.ValueType == PayloadArray && field.SchemaRef != "" && !payloadschema.DescribesArray(field.SchemaRef) {
			element = field.SchemaRef
		}
		if element != "" {
			count := int64(1)
			if field.MinItems != nil && *field.MinItems > count {
				count = *field.MinItems
			}
			schema := livenessResolveSchema(element)
			items := make([]any, 0, count)
			for index := int64(0); index < count; index++ {
				items = append(items, livenessFromSchema(schema, 0))
			}
			return items
		}
		if field.SchemaRef == "" {
			return nil
		}
		return livenessFromSchema(livenessResolveSchema(field.SchemaRef), 0)
	}
	return nil
}

// livenessResolveSchema returns the named schema node from the generated
// payload document.
func livenessResolveSchema(name string) map[string]any {
	defs, ok := payloadschema.Document()["$defs"].(map[string]any)
	if !ok {
		return nil
	}
	schema, _ := defs[name].(map[string]any)
	return schema
}

// livenessFromSchema builds the smallest value the given schema node accepts.
// It follows the subset the generated contracts use: $ref, const, enum, oneOf,
// object properties with required, arrays with minItems, and the scalar types
// with their length and numeric bounds.
func livenessFromSchema(schema map[string]any, depth int) any {
	if schema == nil || depth > 8 {
		return nil
	}
	if ref, ok := schema["$ref"].(string); ok {
		return livenessFromSchema(livenessResolveSchema(strings.TrimPrefix(ref, "#/$defs/")), depth+1)
	}
	if value, ok := schema["const"]; ok {
		return value
	}
	if values, ok := schema["enum"].([]any); ok && len(values) != 0 {
		return values[0]
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		if variants, ok := schema[key].([]any); ok && len(variants) != 0 {
			first, _ := variants[0].(map[string]any)
			return livenessFromSchema(first, depth+1)
		}
	}
	switch schema["type"] {
	case "object":
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		object := map[string]any{}
		for _, name := range required {
			key, ok := name.(string)
			if !ok {
				continue
			}
			property, _ := properties[key].(map[string]any)
			object[key] = livenessFromSchema(property, depth+1)
		}
		return object
	case "array":
		items, _ := schema["items"].(map[string]any)
		count := livenessSchemaInt(schema, "minItems")
		values := make([]any, 0, count)
		for index := int64(0); index < count; index++ {
			values = append(values, livenessFromSchema(items, depth+1))
		}
		return values
	case "integer", "number":
		return livenessSchemaInt(schema, "minimum")
	case "boolean":
		return true
	case "string", nil:
		text := "liveness"
		if min := livenessSchemaInt(schema, "minLength"); int64(len(text)) < min {
			text += strings.Repeat("x", int(min)-len(text))
		}
		if max := livenessSchemaInt(schema, "maxLength"); max > 0 && int64(len(text)) > max {
			text = text[:max]
		}
		if pattern, ok := schema["pattern"].(string); ok {
			return livenessPatternSample(pattern, text)
		}
		return text
	}
	return nil
}

func livenessSchemaInt(schema map[string]any, key string) int64 {
	switch value := schema[key].(type) {
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0
		}
		return parsed
	case float64:
		return int64(value)
	}
	return 0
}

// livenessPatternSample produces a value for the few anchored prefix patterns
// the generated contracts use. An unrecognized pattern returns the plain text,
// and the schema gate then reports it rather than letting it pass unnoticed.
func livenessPatternSample(pattern, fallback string) string {
	switch {
	case strings.HasPrefix(pattern, "^sha256:"), strings.HasPrefix(pattern, "^(sha256:)?"):
		return livenessDigest
	case strings.HasPrefix(pattern, "^check:"):
		return "check:liveness"
	case strings.HasPrefix(pattern, "^approval:"):
		return "approval:liveness"
	case strings.HasPrefix(pattern, "^actor:"):
		return "actor:" + strings.Repeat("0", 64)
	case strings.HasPrefix(pattern, "^msg:"):
		return "msg:" + strings.Repeat("0", 32)
	}
	if matched, err := regexp.MatchString(pattern, fallback); err == nil && matched {
		return fallback
	}
	return fallback
}

// livenessText fits a string to the field's declared length bounds.
func livenessText(field WorkflowPayloadField, base string) string {
	text := base
	if field.MinLength != nil && int64(len([]rune(text))) < *field.MinLength {
		text += strings.Repeat("x", int(*field.MinLength)-len([]rune(text)))
	}
	if field.MaxLength != nil && int64(len([]rune(text))) > *field.MaxLength {
		text = string([]rune(text)[:*field.MaxLength])
	}
	return text
}

// livenessListItem produces a distinct item that validWorkflowPayloadListItem
// accepts for the declared item reference.
// livenessListItem names one item of a list field. The field name is part of
// the item because some folds require two lists on one payload to be disjoint,
// and a value derived from the index alone repeats across them.
func livenessListItem(field, itemRef string, index int64) string {
	switch itemRef {
	case "law_id":
		return fmt.Sprintf("law/liveness-%s-%d", field, index)
	case "proposal_affected_text", "proposal_text":
		return fmt.Sprintf("liveness probe %s item %d", field, index)
	default:
		return fmt.Sprintf("liveness-%s-item-%d", field, index)
	}
}

// livenessPayload builds the payload for one action variant from the registered
// field list, so the synthesizer follows the definition instead of a hand-kept
// table that would drift from it.
func livenessPayload(definition WorkflowDefinition, action WorkflowActionDefinition, variant map[string]string) (json.RawMessage, error) {
	fields := map[string]any{}
	for _, field := range action.Payload.Fields {
		if action.ID == "supersede_contract" && (field.Name == "outcome_kind" || field.Name == "outcome_payload") {
			continue
		}
		if value, ok := livenessOutcomeField(definition, field); ok {
			fields[field.Name] = value
			continue
		}
		// A verdict names the actor that recorded it, and the engine requires
		// that name to match the authenticated invocation. A synthesized string
		// would be refused as forgery rather than tested for liveness.
		if field.Name == "verdict_actor_ref" {
			if action.ID == "complete" {
				// Completion derives the evaluator from the latest recorded
				// verdict. Supplying the owner here would forge that identity.
				continue
			}
			actor := livenessActionActor(action.ID)
			if action.ID == "record_verdict" {
				// The signed operator identity becomes the recorded verdict actor.
				// The invoking evaluator remains the authenticated actor for the
				// independent-evaluation guard.
				actor = livenessOperator()
			}
			fields[field.Name] = DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef)
			continue
		}
		if field.Name == "asserted_at" {
			// Omission exercises the native default, which uses the request time
			// and therefore stays within the report skew bound.
			continue
		}
		if field.Name == "incomparable_with_approved" {
			fields[field.Name] = false
			continue
		}
		// Evidence a verdict cites must equal a reference some earlier action
		// durably bound. One subject reference runs through binding, the
		// contract predicate, and the verdict so the explored path satisfies
		// that join instead of failing on unrelated synthesized names.
		if livenessEvidenceField(field) {
			fields[field.Name] = []string{livenessSubjectRef}
			continue
		}
		if field.Name == "evidence_ref" || field.Name == "immutable_subject_ref" || field.Name == "native_subject_ref" {
			fields[field.Name] = livenessSubjectRef
			continue
		}
		// The CD-0156 alignment search names the peer work item, which the
		// fixture commits, because the constructor joins every related id
		// against the work table like the other reference fields here. The
		// cross-field outcome rule stays off the declared contract, so the
		// none_found variant drops the list here the same way the
		// non-Product-changing approval drops its architecture binding.
		if field.Name == "related_ids" && field.ValueType == PayloadStringList {
			if variant["outcome"] == "none_found" {
				continue
			}
			fields[field.Name] = []string{livenessPeerWorkID}
			continue
		}
		// architecture_binding is required when the definition changes Product
		// truth and refused when it does not. The declaration says only that
		// the field is optional, so the condition is applied here; it is the
		// one structural rule the published contract still cannot state.
		if field.Name == "architecture_binding" && (definition.ChangesProductTruth == nil || !*definition.ChangesProductTruth) {
			continue
		}
		// Optional structured values can request distinct effects, such as
		// replacing a design. A schema reference does not require their presence.
		// State-bound outcomes and architecture bindings are handled explicitly.
		if !field.Required && (field.ValueType == PayloadObject || field.ValueType == PayloadArray) && variant[field.Name] == "" && field.Name != "architecture_binding" {
			continue
		}
		value := livenessValue(field, variant[field.Name])
		if value == nil {
			return nil, fmt.Errorf("no synthesized value for field %q of type %q", field.Name, field.ValueType)
		}
		fields[field.Name] = value
	}
	return json.Marshal(fields)
}

// livenessSubjectRef is the one immutable subject the explored contract,
// evidence binding, and verdict all name.
const livenessSubjectRef = "commit:liveness"

// livenessWorkID and livenessPeerWorkID name the two work items the fixture
// commits. A reference payload field takes the peer, because the engine joins
// some of them against the work table and a reference naming nothing refuses on
// the join rather than on the contract.
const livenessWorkID = "work-liveness"
const livenessPeerWorkID = livenessWorkID + "-peer"

// livenessEvidenceField reports whether a declared list field carries evidence
// references that must join to a durably bound subject.
func livenessEvidenceField(field WorkflowPayloadField) bool {
	if field.ValueType != PayloadStringList {
		return false
	}
	switch field.Name {
	case "evidence_refs", "evaluation_evidence", "resolution_evidence", "cancellation_evidence":
		return true
	}
	return false
}

// livenessOutcomeField builds the contract's outcome fields, whose item
// structure is owned by workflow_outcome.go rather than by the registry's field
// declaration. The shape follows the definition's own OutcomeSchema, so a
// definition that changes its allowed predicate kinds changes this payload with
// it instead of drifting from a fixed table.
func livenessOutcomeField(definition WorkflowDefinition, field WorkflowPayloadField) (any, bool) {
	kind := definition.OutcomeSchema.DefaultKind
	switch field.Name {
	case "outcome_kind":
		return string(kind), true
	case "outcome_payload":
		return livenessOutcomePayload(definition, kind), true
	case "outcome_predicates":
		return []map[string]any{{
			"predicate_id":    "predicate:liveness-exit",
			"ordinal":         0,
			"outcome_kind":    string(kind),
			"outcome_payload": livenessOutcomePayload(definition, kind),
		}}, true
	}
	return nil, false
}

// livenessOutcomePayload builds one outcome payload from the published schema
// for the predicate kind, then applies the two facts the schema cannot carry:
// which kind this definition defaults to, and which outcome tokens it allows.
// The structure comes from the contract; only the definition-scoped choices are
// made here.
func livenessOutcomePayload(definition WorkflowDefinition, kind PredicateKind) map[string]any {
	built, _ := livenessFromSchema(livenessResolveSchema("workflow_outcome_"+string(kind)), 0).(map[string]any)
	if built == nil {
		built = map[string]any{"kind": string(kind)}
	}
	switch kind {
	case PredicateOutcome:
		token := "completed"
		if len(definition.OutcomeSchema.AllowedOutcomeTokens) != 0 {
			token = definition.OutcomeSchema.AllowedOutcomeTokens[0]
		}
		payload := built
		payload["allowed"] = []string{token}
		// A definition that requires a decision record refuses the contract
		// without one, so the explorer supplies it rather than reporting the
		// refusal as a workflow that cannot be approved.
		if definition.OutcomeSchema.DecisionRecordRequired {
			record, _ := livenessFromSchema(livenessResolveSchema("workflow_outcome_decision_record"), 0).(map[string]any)
			if record == nil {
				record = map[string]any{}
			}
			record["decision"] = token
			payload["decision_record"] = record
		}
		return payload
	default:
		return built
	}
}

func livenessActor() WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/liveness", AgentRef: "agent/owner", SessionRef: "session/liveness", ActorClass: ActorAgent}
}

// livenessEvaluator is a second actor, independent of the one that executes the
// work. The engine refuses a verdict from the actor that delivered, so a single
// actor would make every verify step look stranded when the workflow is only
// asking for the independent evaluator it was designed to require.
func livenessEvaluator() WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/liveness", AgentRef: "agent/evaluator", SessionRef: "session/liveness-evaluator", ActorClass: ActorAgent}
}

func livenessOperator() WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/liveness", AgentRef: "agent/operator", SessionRef: "session/liveness-operator", ActorClass: ActorOperator}
}

// livenessActionActor routes each action to the actor the engine expects to
// perform it.
func livenessActionActor(actionID string) WorkflowActor {
	switch actionID {
	case "record_verdict", "record_health", "accept_decision":
		return livenessEvaluator()
	default:
		return livenessActor()
	}
}

// livenessApply runs one action against the store. commit decides whether the
// action persists; a probe rolls back so the caller can test many actions from
// one state.
func livenessApply(ctx context.Context, s *Store, workID string, move livenessMove, sequence int, commit bool) error {
	version, err := livenessWorkVersion(ctx, s, workID)
	if err != nil {
		return err
	}
	label := fmt.Sprintf("liveness-%s-%d", move.action, sequence)
	// An approval-gated action is answered by the operator, not by the agent.
	// The explorer supplies that operator so the probe measures the workflow
	// route rather than the approval boundary, which is proved elsewhere. A
	// state whose only exit needs the operator is waiting, not stranded.
	var operator *WorkflowActor
	selectedChoice := ""
	decisionDigest := ""
	if workflowActionAllowsOperatorIdentity(move.action) {
		operatorActor := livenessOperator()
		operator = &operatorActor
	}
	if move.action == "confirm_premise" {
		question, questionErr := ReadWorkflowOperatorQuestion(ctx, s, workID)
		if questionErr != nil {
			return questionErr
		}
		if question == nil || question.ActionID != move.action {
			return fmt.Errorf("no open operator question for %s", move.action)
		}
		selectedChoice = "confirm"
		decisionDigest = question.DecisionContextDigest
		// The operator is a distinct actor, not the invoking agent under
		// another label. The engine refuses a relabel of the caller, which is
		// the control this explorer must not defeat.
	}
	actor := livenessActionActor(move.action)
	payload, bindErr := livenessBindRecordedState(ctx, s, workID, move.action, move.payload)
	if bindErr != nil {
		return bindErr
	}
	request := WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: move.action,
		SelectedChoice: selectedChoice, DecisionContextDigest: decisionDigest,
		OperatorActor: operator,
		Payload:       payload, Actor: actor,
		EvidenceRefs:         []string{livenessSubjectRef},
		AcceptedInputsDigest: "sha256:" + strings.Repeat("b", 64),
		ContractDigest:       testManifestDigest,
		Tool:                 "concord_work_transition",
		IdempotencyKey:       label, RequestID: label, IdempotencyIdentity: label, OperationID: label,
		PrincipalRef: actor.PrincipalRef,
		Now:          time.Unix(int64(100+sequence), 0).UTC(),
	}
	switch move.action {
	case "start_run", "record_health", "rollback_run", "cleanup_run":
		digest := sha256.Sum256([]byte(livenessSubjectRef))
		request.EvidenceRefs = append(request.EvidenceRefs, nativeRunObservationID(fmt.Sprintf("sha256:%x", digest)))
	}
	preflight := WorkflowActionPreflightRequest{
		WorkID: request.WorkID, ExpectedVersion: request.ExpectedVersion, ActionID: request.ActionID,
		SelectedChoice: request.SelectedChoice, DecisionContextDigest: request.DecisionContextDigest,
		Payload: request.Payload, Actor: request.Actor,
	}
	err = AuthorizeWorkflowActionAtBoundaryTx(ctx, s, BuiltinWorkflowRegistry(), preflight, nil, time.Time{}, nil, func(tx *Transaction) error {
		if _, inner := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), request); inner != nil {
			return inner
		}
		if commit {
			return nil
		}
		return errLivenessProbe
	})
	if !commit && errors.Is(err, errLivenessProbe) {
		return nil
	}
	return err
}

// livenessProducer reads the completed durable operation that already carries
// the explored evidence reference and returns a rewriter that points a
// bind_evidence payload at it.
// livenessDeclaresProducer reports whether a payload carries the producer
// identity that evidence binding resolves against.
// livenessBindRecordedState rewrites the payload fields whose only valid value
// is recorded state. The engine joins these against committed rows, so a value
// synthesized from the declaration alone refuses on the join, and every state
// behind that refusal reads as stranded when it is not.
func livenessBindRecordedState(ctx context.Context, s *Store, workID, actionID string, raw json.RawMessage) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw, nil
	}
	bound := map[string]any{}
	if _, declared := fields["contract_version"]; declared {
		var version int64
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(contract_version),0) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&version); err != nil {
			return nil, err
		}
		if actionID == "supersede_contract" {
			version++
		}
		if version > 0 {
			bound["contract_version"] = version
		}
	}
	if actionID == "request_correction" {
		entry, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
		if err != nil {
			return nil, err
		}
		step, err := livenessStep(ctx, s, workID)
		if err != nil {
			return nil, err
		}
		correction, err := workflowVerdictCorrectionContext(ctx, s.db, workID, entry.Definition, step, "liveness")
		if err != nil {
			return nil, err
		}
		if correction != nil {
			bound["predicate_ids"] = correction.PredicateIDs
			bound["evidence_refs"] = correction.EvidenceRefs
		}
	}
	subject := livenessSubjectRef
	if string(fields["evidence_kind"]) == `"native_run"` {
		if err := s.db.QueryRowContext(ctx, `SELECT observation_id FROM workflow_native_runs WHERE work_id=? ORDER BY recorded_at DESC LIMIT 1`, workID).Scan(&subject); err != nil {
			return nil, fmt.Errorf("no native report to bind: %w", err)
		}
		bound["immutable_subject_ref"] = subject
		bound["evidence_ref"] = subject
	}
	if _, declared := fields["producer_run_ref"]; declared {
		// Evidence binds only to a completed durable operation that already
		// carries the reference. The producer identity is recorded state, so
		// the explorer reads it rather than synthesizing a name that could
		// never match.
		var opID, principal, requestID string
		if err := s.db.QueryRowContext(ctx, `SELECT op_id,principal_ref,request_id FROM durable_operations WHERE work_id=? AND result_kind='completed' AND EXISTS (SELECT 1 FROM json_each(durable_operations.evidence_refs) WHERE value=?) ORDER BY rowid DESC LIMIT 1`, workID, subject).Scan(&opID, &principal, &requestID); err != nil {
			return nil, fmt.Errorf("no completed durable operation carries %s: %w", subject, err)
		}
		bound["producer_id"] = principal
		bound["producer_run_ref"] = opID
		bound["producer_watermark"] = requestID
	}
	if _, declared := fields["attempt_epoch"]; declared {
		// A checkpoint names the attempt it belongs to. The attempt epoch is
		// assigned when the action starts, so it cannot be known from the
		// contract.
		var currentStep string
		if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
			return nil, fmt.Errorf("cannot read the current workflow step: %w", err)
		}
		_, epoch, found, err := latestWorkflowActionStart(ctx, s.db, workID, currentStep)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("no workflow action start on step %q to checkpoint", currentStep)
		}
		bound["attempt_epoch"] = epoch
	}
	if _, declared := fields["predicate_id"]; declared {
		// A verdict names a predicate the approved contract carries. The
		// contract is approved earlier in the same run, so the approved
		// predicate and its contract version are recorded state.
		var predicateID string
		var contractVersion int64
		if err := s.db.QueryRowContext(ctx, `SELECT predicate_id,contract_version FROM workflow_contract_predicates WHERE work_id=? ORDER BY contract_version DESC,rowid ASC LIMIT 1`, workID).Scan(&predicateID, &contractVersion); err != nil {
			return nil, fmt.Errorf("no approved contract predicate to name in a verdict: %w", err)
		}
		bound["predicate_id"] = predicateID
		bound["contract_version"] = contractVersion
	}
	if _, declared := fields["resolution_authority"]; declared {
		// A condition names the durable operation that may resolve it. The
		// operation is recorded state, and the fold requires its reference
		// form, so the explorer reads one rather than naming a shape.
		var opID string
		if err := s.db.QueryRowContext(ctx, `SELECT op_id FROM durable_operations WHERE work_id=? ORDER BY rowid DESC LIMIT 1`, workID).Scan(&opID); err != nil {
			return nil, fmt.Errorf("no durable operation to carry condition authority: %w", err)
		}
		bound["resolution_authority"] = "durable_operation:" + opID
	}
	if len(bound) == 0 {
		return raw, nil
	}
	for name, value := range bound {
		if _, declared := fields[name]; !declared {
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		fields[name] = encoded
	}
	rewritten, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return rewritten, nil
}

func livenessWorkVersion(ctx context.Context, s *Store, workID string) (int64, error) {
	var version int64
	err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version)
	return version, err
}

func livenessStep(ctx context.Context, s *Store, workID string) (string, error) {
	var step string
	err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step)
	return step, err
}

func livenessCompletion(ctx context.Context, s *Store, workID string) (string, int64, error) {
	var state string
	var completions int64
	err := s.db.QueryRowContext(ctx, `SELECT instance_state,(SELECT count(*) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=?) FROM workflow_instances WHERE work_id=?`, string(SubjectWorkItem), workID, WorkflowCompleted, workID).Scan(&state, &completions)
	return state, completions, err
}

// livenessFailureKind names the typed class of a refusal so a payload defect in
// this harness is never reported as a liveness finding.
func livenessFailureKind(err error) string {
	var failure *Failure
	if errors.As(err, &failure) {
		return string(failure.Kind)
	}
	return "untyped"
}

// livenessReplay rebuilds a state by committing the given path into a fresh
// store. Replaying is slower than snapshotting the database file, but it keeps
// the explored state exactly what the engine produces.
type livenessReplayCache map[string]string

// Cache only closed, checkpointed images of exact ordered prefixes. A branch
// gets its own copy and still runs every remaining action through the engine.
func (cache livenessReplayCache) replay(t *testing.T, definition WorkflowDefinition, path []livenessMove) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	var s *Store
	start := 0
	workID := livenessWorkID
	for prefix := len(path); prefix >= 0; prefix-- {
		if image, ok := cache[livenessPathKey(path[:prefix])]; ok {
			s = openTempAtPath(t, copyClosedTestDatabase(t, image))
			start = prefix
			break
		}
	}
	if s == nil {
		s = openTemp(t)
		seedStepWork(t, s, workID)
		livenessSeedInvestigation(t, s, workID)
		livenessInitialize(t, s, workID, definition)
	}
	for index := start; index < len(path); index++ {
		move := path[index]
		if err := livenessApply(ctx, s, workID, move, index, true); err != nil {
			t.Fatalf("%s replay of %s at index %d failed: %v", definition.Ref, move.String(), index, err)
		}
	}
	return s, workID
}

func (cache livenessReplayCache) retain(t *testing.T, s *Store, path []livenessMove) {
	t.Helper()
	var busy, pages, checkpointed int
	if err := s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &pages, &checkpointed); err != nil {
		t.Fatal(err)
	}
	if busy != 0 {
		t.Fatal("exploration snapshot has an active WAL reader")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	cache[livenessPathKey(path)] = s.Path()
}

// livenessSeedInvestigation records the observation that opens an operator
// question: one current Domain of the work item's Product, plus a second work
// item, named by one observation.
//
// No workflow action produces this state. The gate reads the observation
// projection, which concord_work_define fills from a different tool surface, so
// an instance whose only exit is an operator question waits on evidence its own
// declared actions cannot record. Seeding it here keeps the explorer measuring
// the workflow graph rather than re-reporting that one cross-surface gate at
// every checkpoint step.
// livenessInitialize pins the definition as the actor the explorer then acts
// as. The selecting session is recorded as the instance's executor, and a
// checkpoint must come from that executor, so selecting as one identity and
// acting as another refuses on every checkpoint a fenced start has not
// reassigned.
func livenessInitialize(t *testing.T, s *Store, workID string, definition WorkflowDefinition) {
	t.Helper()
	registered, err := BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: livenessActor(), Now: time.Unix(5, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
}

func livenessSeedInvestigation(t *testing.T, s *Store, workID string) {
	t.Helper()
	ctx := context.Background()
	peerID := livenessPeerWorkID
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: peerID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: peerID, Actor: "operator", OccurredAt: time.Unix(3, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Peer work","priority":1}`)},
		{EventID: peerID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: peerID, Actor: "operator", OccurredAt: time.Unix(4, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-s","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, peerID): 0}}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	hash := livenessDigest
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('liveness-locator','project-s','canonical_path','/liveness','/liveness','t','t')`, nil},
		{`INSERT OR IGNORE INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('product-s','project-s','liveness-locator')`, nil},
		{`INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES('product-s','project-s','liveness-locator','liveness','root','1.0',?,'liveness')`, []any{hash}},
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project-s','liveness-locator','product-s','root','Root','Product law','current',?,'liveness')`, []any{hash}},
		// The explorer synthesizes "liveness" for an unconstrained string, so a
		// Domain of that id must exist for a synthesized architecture binding to
		// resolve. Without it the binding refuses on the registry join and reads
		// as a stranding that no agent would meet.
		{`INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,status,registry_content_hash,scanned_commit_oid) VALUES('project-s','liveness-locator','product-s','liveness','Liveness','Explorer domain','current',?,'liveness')`, []any{hash}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	refs := `["root","` + peerID + `"]`
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_observations(observation_id,work_id,statement,refs,tags,recorded_at) VALUES(?,?,?,?,?,?)`, "obs:"+strings.Repeat("a", 16), workID, "liveness investigation", refs, `[]`, "2026-09-12T00:00:00Z"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// livenessExplore walks one definition and returns every stranded state it
// reaches. It keeps action order in the state key because the engine does not
// declare that independently recorded actions commute.
func livenessExplore(t *testing.T, definition WorkflowDefinition) livenessExploration {
	t.Helper()
	ctx := context.Background()
	terminal := map[string]bool{}
	for _, step := range definition.StepGraph.TerminalSteps {
		terminal[step] = true
	}
	reports := []livenessReport{}
	depthBoundStepHits := map[string]int{}
	omittedVariants := livenessOmittedVariants(definition)
	omittedVariants = append(omittedVariants, "host-dispatch", "context-continuity", "external-environment-transitions")
	testedStates := 0
	testedTransitions := 0
	testedProbes := 0
	terminalStates := 0
	seen := map[string]bool{}
	cache := livenessReplayCache{}
	initial, _ := cache.replay(t, definition, nil)
	cache.retain(t, initial, nil)
	queue := [][]livenessMove{{}}
	for len(queue) > 0 {
		if testedStates >= livenessStateBudget {
			depthBoundStepHits["state-budget"] += len(queue)
			break
		}
		path := queue[0]
		queue = queue[1:]
		key := livenessPathKey(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		s, workID := cache.replay(t, definition, path)
		step, err := livenessStep(ctx, s, workID)
		if err != nil {
			t.Fatalf("%s read step: %v", definition.Ref, err)
		}
		testedStates++
		if testedStates == 1 || testedStates%16 == 0 {
			t.Logf("exploration progress: states=%d probes=%d transitions=%d frontier=%d", testedStates, testedProbes, testedTransitions, len(queue))
		}
		state, completions, completionErr := livenessCompletion(ctx, s, workID)
		if completionErr != nil {
			t.Fatalf("%s read durable completion: %v", definition.Ref, completionErr)
		}
		if terminal[step] && state == "completed" {
			terminalStates++
			if state != "completed" || completions != 1 {
				t.Errorf("%s terminal step %q lacks one durable workflow.completed record: state=%q completions=%d", definition.Ref, step, state, completions)
			}
			s.Close()
			continue
		}
		if len(path) >= livenessDepth {
			depthBoundStepHits[step]++
			s.Close()
			continue
		}
		moves := livenessStateMoves(t, definition, step)
		probes := make([]livenessProbe, 0, len(moves))
		advancing := []livenessMove{}
		for _, move := range moves {
			if livenessContinuityAction(move.action) || move.action == "dispatch_worker" {
				continue
			}
			testedProbes++
			probeErr := livenessApply(ctx, s, workID, move, len(path), false)
			kind := ""
			detail := ""
			if probeErr != nil {
				kind = livenessFailureKind(probeErr)
				detail = probeErr.Error()
				if kind == string(KindInvalidPayload) {
					t.Fatalf("%s harness defect: %s payload rejected by its own registered contract: %v", definition.Ref, move.String(), probeErr)
				}
			}
			probes = append(probes, livenessProbe{move: move, failure: detail, failKind: kind})
			if probeErr == nil {
				testedTransitions++
				advancing = append(advancing, move)
			}
		}
		cache.retain(t, s, path)

		if len(advancing) == 0 {
			reports = append(reports, livenessReport{definition: definition.Ref, step: step, path: path, probes: probes})
			continue
		}
		for _, move := range advancing {
			next := make([]livenessMove, len(path), len(path)+1)
			copy(next, path)
			queue = append(queue, append(next, move))
		}
	}
	depthBoundStates := 0
	for _, count := range depthBoundStepHits {
		depthBoundStates += count
	}
	return livenessExploration{reports: reports, testedStates: testedStates, testedTransitions: testedTransitions, testedProbes: testedProbes, terminalStates: terminalStates, depthBoundStates: depthBoundStates, depthBoundStepHits: depthBoundStepHits, omittedVariants: omittedVariants}
}

// livenessStateMoves builds every action variant the step declares.
func livenessStateMoves(t *testing.T, definition WorkflowDefinition, stepID string) []livenessMove {
	t.Helper()
	moves := []livenessMove{}
	for _, actionID := range livenessDeclaredActions(definition, stepID) {
		action, ok := livenessActionDefinition(definition, actionID)
		if !ok {
			continue
		}
		for _, variant := range livenessEvidenceKindVariants(definition, action, livenessVariants(action)) {
			payload, err := livenessPayload(definition, action, variant)
			if err != nil {
				t.Fatalf("%s payload synthesis for %s: %v", definition.Ref, actionID, err)
			}
			moves = append(moves, livenessMove{action: actionID, variant: livenessVariantLabel(variant), payload: payload})
		}
	}
	return moves
}

func livenessContinuityAction(actionID string) bool {
	return actionID == "checkpoint_context" || actionID == "cross_context_boundary"
}

func livenessDeclaredActions(definition WorkflowDefinition, stepID string) []string {
	// These engine-owned recovery routes may be admitted outside the step's
	// action list. The real preflight, not this enumeration, decides admission.
	actions := []string{"bind_evidence"}
	for id := range builtinActionPolicies {
		if _, ok := workflowRecoveryActionDefinition(id); ok {
			actions = append(actions, id)
		}
	}
	for _, step := range definition.StepGraph.Steps {
		if step.ID == stepID {
			for _, action := range step.Actions {
				if !containsString(actions, action) {
					actions = append(actions, action)
				}
			}
		}
	}
	sort.Strings(actions)
	return actions
}

// Paths are replayed from the same fixture. Only identical ordered inputs may
// be deduplicated; selecting tables by a column name is not state equivalence.
func livenessPathKey(path []livenessMove) string {
	type input struct {
		Action  string
		Variant string
		Payload json.RawMessage
	}
	inputs := make([]input, len(path))
	for i, move := range path {
		inputs[i] = input{move.action, move.variant, move.payload}
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

// A builtin workflow exploration reports observed stranded states. Its bounded
// result is evidence for the sampled paths only, not a general liveness proof.
func TestBuiltinWorkflowExplorationReportsNoObservedStrandedState(t *testing.T) {
	for _, definition := range BuiltinWorkflowDefinitions() {
		definition := definition
		t.Run(definition.Ref, func(t *testing.T) {
			result := livenessExplore(t, definition)
			for _, report := range result.reports {
				t.Errorf("%s", report.String())
			}
			if result.depthBoundStates != 0 {
				t.Logf("inconclusive: %d paths left unexpanded (depth limit %d, state limit %d): %v", result.depthBoundStates, livenessDepth, livenessStateBudget, result.depthBoundStepHits)
			}
			if len(result.omittedVariants) != 0 {
				t.Logf("inconclusive: omitted enum variants for %s", strings.Join(result.omittedVariants, ", "))
			}
			t.Logf("coverage: states=%d probes=%d admitted_transitions=%d terminal_states=%d", result.testedStates, result.testedProbes, result.testedTransitions, result.terminalStates)
			if result.conclusion() == "inconclusive" {
				t.Skip("inconclusive exploration; required completion witnesses run separately")
			}
		})
	}
}
