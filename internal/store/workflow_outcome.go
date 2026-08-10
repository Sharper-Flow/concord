package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
)

const maxWorkflowPredicateBytes = 64 * 1024

const maxWorkflowJSONDepth = 64
const maxWorkflowJSONContainerEntries = 128

type PredicateKind string

const (
	PredicateExists  PredicateKind = "exists"
	PredicateAbsent  PredicateKind = "absent"
	PredicateOutcome PredicateKind = "outcome"
	PredicateCheck   PredicateKind = "check"
)

type WorkflowDecisionRecord struct {
	Question            string   `json:"question"`
	OptionsConsidered   []string `json:"options_considered"`
	Decision            string   `json:"decision"`
	Rationale           string   `json:"rationale"`
	Consequences        []string `json:"consequences"`
	Inputs              []string `json:"inputs"`
	POCFindings         string   `json:"poc_findings"`
	Supersedes          *string  `json:"supersedes"`
	SupersededBy        *string  `json:"superseded_by"`
	Unknowns            []string `json:"unknowns"`
	RequiredToDecide    []string `json:"required_to_decide"`
	ReviewerActorRef    string   `json:"reviewer_actor_ref"`
	OperatorApprovalRef string   `json:"operator_approval_ref"`
}

// OutcomePredicate is the closed v1 union. Only fields belonging to Kind may
// be populated; DecodeWorkflowPredicate enforces that rule for untrusted JSON.
type OutcomePredicate struct {
	Kind                PredicateKind           `json:"kind"`
	Surface             string                  `json:"surface,omitempty"`
	Subjects            []string                `json:"subjects,omitempty"`
	DistinguishFrom     []string                `json:"distinguish_from,omitempty"`
	Allowed             []string                `json:"allowed,omitempty"`
	DecisionRecord      *WorkflowDecisionRecord `json:"decision_record,omitempty"`
	CheckRef            string                  `json:"check_ref,omitempty"`
	ImmutableSubjectRef string                  `json:"immutable_subject_ref,omitempty"`
	ExpectedResult      string                  `json:"expected_result,omitempty"`
}

type WorkflowOutcomePredicate = OutcomePredicate
type WorkflowActorTuple = WorkflowActor

func DecodeWorkflowPredicate(data []byte) (OutcomePredicate, error) {
	if len(data) == 0 || len(data) > maxWorkflowPredicateBytes {
		return OutcomePredicate{}, workflowOutcomeFailure("predicate exceeds the bounded input size")
	}
	if err := validateWorkflowJSONStructure(data); err != nil {
		return OutcomePredicate{}, err
	}
	fields, err := decodeObjectFields(data)
	if err != nil {
		return OutcomePredicate{}, err
	}
	kindRaw, ok := fields["kind"]
	if !ok {
		return OutcomePredicate{}, workflowOutcomeFailure("predicate kind is required")
	}
	var kind PredicateKind
	if err := json.Unmarshal(kindRaw, &kind); err != nil || !validPredicateKind(kind) {
		return OutcomePredicate{}, workflowOutcomeFailure("predicate kind is unknown")
	}
	allowed := map[PredicateKind]map[string]bool{
		PredicateExists:  {"kind": true, "surface": true, "subjects": true},
		PredicateAbsent:  {"kind": true, "surface": true, "subjects": true, "distinguish_from": true},
		PredicateOutcome: {"kind": true, "allowed": true, "decision_record": true},
		PredicateCheck:   {"kind": true, "check_ref": true, "immutable_subject_ref": true, "expected_result": true},
	}
	for key := range fields {
		if !allowed[kind][key] {
			return OutcomePredicate{}, workflowOutcomeFailure("predicate contains an unknown or recursive field")
		}
	}
	var predicate OutcomePredicate
	if err := decodePredicateStrict(data, &predicate); err != nil {
		return OutcomePredicate{}, workflowOutcomeFailure(err.Error())
	}
	if err := ValidateWorkflowPredicate(predicate); err != nil {
		return OutcomePredicate{}, err
	}
	return predicate, nil
}

// DecodeOutcomePredicate is an explicit alias for callers that use the schema
// name rather than the engine's shorter predicate name.
func DecodeOutcomePredicate(data []byte) (OutcomePredicate, error) {
	return DecodeWorkflowPredicate(data)
}

func DecodeWorkflowOutcome(data []byte) (OutcomePredicate, error) {
	return DecodeWorkflowPredicate(data)
}

func ValidateWorkflowPredicate(predicate OutcomePredicate) error {
	if !validPredicateKind(predicate.Kind) {
		return workflowOutcomeFailure("predicate kind is unknown")
	}
	if predicate.Kind == PredicateExists || predicate.Kind == PredicateAbsent {
		if predicate.Allowed != nil || predicate.DecisionRecord != nil || predicate.CheckRef != "" || predicate.ImmutableSubjectRef != "" || predicate.ExpectedResult != "" {
			return workflowOutcomeFailure("predicate contains fields from another union member")
		}
		if !workflowSurfacePattern.MatchString(predicate.Surface) || !validSubjectSet(predicate.Subjects) {
			return workflowOutcomeFailure("predicate surface or subjects are invalid")
		}
		if predicate.Kind == PredicateExists && len(predicate.DistinguishFrom) != 0 {
			return workflowOutcomeFailure("exists predicate has absent-only fields")
		}
		if predicate.Kind == PredicateAbsent {
			if len(predicate.DistinguishFrom) < 1 || len(predicate.DistinguishFrom) > 4 || !uniqueStrings(predicate.DistinguishFrom) {
				return workflowOutcomeFailure("absent predicate distinction rules are invalid")
			}
			for _, value := range predicate.DistinguishFrom {
				if !containsString([]string{"archived", "relocated", "renamed", "disabled"}, value) {
					return workflowOutcomeFailure("absent predicate distinction rule is unknown")
				}
			}
		}
	}
	if predicate.Kind == PredicateOutcome {
		if predicate.Surface != "" || predicate.Subjects != nil || predicate.DistinguishFrom != nil || predicate.CheckRef != "" || predicate.ImmutableSubjectRef != "" || predicate.ExpectedResult != "" {
			return workflowOutcomeFailure("predicate contains fields from another union member")
		}
		if len(predicate.Allowed) < 1 || len(predicate.Allowed) > 8 || !uniqueStrings(predicate.Allowed) {
			return workflowOutcomeFailure("outcome predicate allowed tokens are invalid")
		}
		for _, token := range predicate.Allowed {
			if !validOutcomeToken(token) {
				return workflowOutcomeFailure("outcome predicate token is unknown")
			}
		}
		if predicate.DecisionRecord != nil {
			if err := validateDecisionRecord(*predicate.DecisionRecord); err != nil {
				return err
			}
			if len(predicate.Allowed) == 1 && predicate.DecisionRecord.Decision != predicate.Allowed[0] {
				return workflowOutcomeFailure("decision record token does not match the outcome predicate")
			}
		}
	}
	if predicate.Kind == PredicateCheck {
		if predicate.Surface != "" || predicate.Subjects != nil || predicate.DistinguishFrom != nil || predicate.Allowed != nil || predicate.DecisionRecord != nil {
			return workflowOutcomeFailure("predicate contains fields from another union member")
		}
		if !workflowCheckPattern.MatchString(predicate.CheckRef) || !workflowSubjectPattern.MatchString(predicate.ImmutableSubjectRef) || !containsString([]string{"true", "false", "pass", "fail", "present", "absent", "healthy", "unhealthy", "accepted", "rejected"}, predicate.ExpectedResult) {
			return workflowOutcomeFailure("check predicate fields are invalid")
		}
	}
	return nil
}

func ValidateWorkflowPredicateForDefinition(definition WorkflowDefinition, predicate OutcomePredicate) error {
	if err := ValidateWorkflowPredicate(predicate); err != nil {
		return err
	}
	if !containsPredicateKind(definition.OutcomeSchema.AllowedKinds, predicate.Kind) {
		return definitionFailure(KindInvalidDefinition, "predicate kind not allowed by the pinned workflow definition")
	}
	if predicate.Kind == PredicateOutcome {
		for _, token := range predicate.Allowed {
			if !containsString(definition.OutcomeSchema.AllowedOutcomeTokens, token) {
				return definitionFailure(KindInvalidDefinition, "outcome token not allowed by the pinned workflow definition")
			}
		}
		if definition.OutcomeSchema.DecisionRecordRequired && predicate.DecisionRecord == nil {
			return definitionFailure(KindInvalidDefinition, "decision record required by the pinned workflow definition")
		}
		if predicate.DecisionRecord != nil && !containsString(predicate.Allowed, predicate.DecisionRecord.Decision) {
			return definitionFailure(KindInvalidDefinition, "decision record token is not allowed by the pinned workflow definition")
		}
	}
	return nil
}

func validateDecisionRecord(record WorkflowDecisionRecord) error {
	if len(record.Question) < 1 || len(record.Question) > 4096 || len(record.OptionsConsidered) < 1 || len(record.OptionsConsidered) > 16 || !uniqueStrings(record.OptionsConsidered) || (record.Decision != "accepted_decision" && record.Decision != "insufficient_evidence") || len(record.Rationale) < 1 || len(record.Rationale) > 4096 || len(record.Consequences) < 1 || len(record.Consequences) > 16 || len(record.Inputs) < 1 || len(record.Inputs) > 32 || len(record.POCFindings) < 1 || len(record.POCFindings) > 4096 || len(record.Unknowns) > 16 || len(record.RequiredToDecide) > 16 || !workflowActorRefPattern.MatchString(record.ReviewerActorRef) || !workflowApprovalPattern.MatchString(record.OperatorApprovalRef) {
		return workflowOutcomeFailure("decision record is incomplete or invalid")
	}
	if record.Decision == "insufficient_evidence" && (len(record.Unknowns) == 0 || len(record.RequiredToDecide) == 0) {
		return workflowOutcomeFailure("insufficient-evidence decision record must name unknowns and required follow-up")
	}
	return nil
}

func decodeObjectFields(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, workflowOutcomeFailure("predicate must be a JSON object")
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, workflowOutcomeFailure("predicate must be a JSON object")
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, workflowOutcomeFailure("predicate object is malformed")
		}
		key, ok := keyToken.(string)
		if !ok || fields[key] != nil {
			return nil, workflowOutcomeFailure("predicate has duplicate or invalid field names")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, workflowOutcomeFailure("predicate field has invalid JSON")
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, workflowOutcomeFailure("predicate object is malformed")
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	return fields, nil
}

func decodePredicateStrict(data []byte, target any) error {
	if len(data) == 0 || len(data) > maxWorkflowPredicateBytes {
		return workflowOutcomeFailure("JSON payload exceeds the bounded input size")
	}
	if err := validateWorkflowJSONStructure(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func validateWorkflowJSONStructure(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanWorkflowJSONValue(decoder, 0); err != nil {
		return workflowOutcomeFailure("JSON payload has duplicate keys, invalid nesting, or malformed structure")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return workflowOutcomeFailure("JSON payload has trailing data")
	}
	return nil
}

func scanWorkflowJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxWorkflowJSONDepth {
		return workflowOutcomeFailure("JSON payload exceeds the recursion bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, isDelimiter := token.(json.Delim)
	if !isDelimiter || (delim != '{' && delim != '[') {
		return nil
	}
	entries := 0
	if delim == '{' {
		seen := map[string]bool{}
		for decoder.More() {
			entries++
			if entries > maxWorkflowJSONContainerEntries {
				return workflowOutcomeFailure("JSON object exceeds the field bound")
			}
			keyToken, tokenErr := decoder.Token()
			key, ok := keyToken.(string)
			if tokenErr != nil || !ok || seen[key] {
				return workflowOutcomeFailure("JSON object contains a duplicate or invalid field")
			}
			seen[key] = true
			if err := scanWorkflowJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	} else {
		for decoder.More() {
			entries++
			if entries > maxWorkflowJSONContainerEntries {
				return workflowOutcomeFailure("JSON array exceeds the item bound")
			}
			if err := scanWorkflowJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	}
	closing, err := decoder.Token()
	closingDelim, closingOK := closing.(json.Delim)
	wantClosing := json.Delim('}')
	if delim == '[' {
		wantClosing = ']'
	}
	if err != nil || !closingOK || closingDelim != wantClosing {
		return workflowOutcomeFailure("JSON container is not closed")
	}
	return nil
}
func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return workflowOutcomeFailure("trailing JSON data")
		}
		return err
	}
	return nil
}
func workflowOutcomeFailure(detail string) error {
	return newFailure(KindInvalidPayload, "workflow_outcome", detail, false, "supply one strict closed predicate")
}

var workflowSurfacePattern = regexp.MustCompile(`^[a-z][a-z0-9_.:/-]{1,127}$`)
var workflowSubjectPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/-]{1,127}$`)
var workflowCheckPattern = regexp.MustCompile(`^check:[a-z][a-z0-9_.:/-]{1,127}$`)
var workflowActorRefPattern = regexp.MustCompile(`^actor:[0-9a-f]{64}$`)
var workflowApprovalPattern = regexp.MustCompile(`^approval:[a-z0-9][a-z0-9_.:/-]{1,127}$`)

func validSubjectSet(values []string) bool {
	if len(values) < 1 || len(values) > 100 || !uniqueStrings(values) {
		return false
	}
	for _, value := range values {
		if !workflowSubjectPattern.MatchString(value) {
			return false
		}
	}
	return true
}

type WorkflowStrength string

const (
	StrengthStrongerOrEqual WorkflowStrength = "stronger_or_equal"
	StrengthWeaker          WorkflowStrength = "weaker"
	StrengthIncomparable    WorkflowStrength = "incomparable"
	StrengthEqual           WorkflowStrength = StrengthStrongerOrEqual
)

func CompareWorkflowPredicates(approved, delivered OutcomePredicate) (WorkflowStrength, error) {
	if err := ValidateWorkflowPredicate(approved); err != nil {
		return "", err
	}
	if err := ValidateWorkflowPredicate(delivered); err != nil {
		return "", err
	}
	if approved.Kind != delivered.Kind {
		return StrengthIncomparable, nil
	}
	switch approved.Kind {
	case PredicateExists:
		if approved.Surface != delivered.Surface {
			return StrengthIncomparable, nil
		}
		return compareSubjectSets(approved.Subjects, delivered.Subjects), nil
	case PredicateAbsent:
		if approved.Surface != delivered.Surface || !sameStrings(approved.DistinguishFrom, delivered.DistinguishFrom) {
			return StrengthIncomparable, nil
		}
		return compareSubjectSets(approved.Subjects, delivered.Subjects), nil
	case PredicateOutcome:
		return compareTokenSets(approved.Allowed, delivered.Allowed), nil
	case PredicateCheck:
		if approved.CheckRef == delivered.CheckRef && approved.ImmutableSubjectRef == delivered.ImmutableSubjectRef && approved.ExpectedResult == delivered.ExpectedResult {
			return StrengthStrongerOrEqual, nil
		}
		return StrengthIncomparable, nil
	default:
		return StrengthIncomparable, nil
	}
}

func CompareOutcomeStrength(approved, delivered OutcomePredicate) (WorkflowStrength, error) {
	return CompareWorkflowPredicates(approved, delivered)
}

func compareSubjectSets(approved, delivered []string) WorkflowStrength {
	approvedSet, deliveredSet := predicateStringSet(approved), predicateStringSet(delivered)
	if setContains(deliveredSet, approvedSet) {
		if len(approvedSet) == len(deliveredSet) {
			return StrengthStrongerOrEqual
		}
		return StrengthStrongerOrEqual
	}
	if setContains(approvedSet, deliveredSet) {
		return StrengthWeaker
	}
	return StrengthIncomparable
}
func compareTokenSets(approved, delivered []string) WorkflowStrength {
	a, d := predicateStringSet(approved), predicateStringSet(delivered)
	if len(d) == 1 {
		for token := range d {
			if !a[token] {
				return StrengthWeaker
			}
		}
	}
	if setContains(a, d) {
		return StrengthStrongerOrEqual
	}
	if setContains(d, a) {
		return StrengthWeaker
	}
	return StrengthIncomparable
}
func predicateStringSet(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}
func setContains(container, values map[string]bool) bool {
	for value := range values {
		if !container[value] {
			return false
		}
	}
	return true
}

type WorkflowGroundTruth string

const (
	GroundTruthPresent   WorkflowGroundTruth = "present"
	GroundTruthAbsent    WorkflowGroundTruth = "absent"
	GroundTruthArchived  WorkflowGroundTruth = "archived"
	GroundTruthRelocated WorkflowGroundTruth = "relocated"
	GroundTruthRenamed   WorkflowGroundTruth = "renamed"
	GroundTruthDisabled  WorkflowGroundTruth = "disabled"
)

type WorkflowGroundTruthResolver interface {
	Resolve(surface, subject string) (WorkflowGroundTruth, error)
}
type WorkflowCheckResolver interface {
	Compare(checkRef, immutableSubjectRef, expectedResult string) (WorkflowStrength, error)
}
type WorkflowOutcomeEvaluation struct {
	Satisfied                bool
	VerdictKind              string
	IncomparableWithApproved bool
	Strength                 WorkflowStrength
}
type WorkflowOutcomeEvaluationContext struct {
	GroundTruth    WorkflowGroundTruthResolver
	Checks         WorkflowCheckResolver
	ExecutingActor *WorkflowActor
	VerdictActor   *WorkflowActor
	Registry       DefinitionRegistry
	DefinitionPin  WorkflowDefinitionPin
}

func EvaluateWorkflowOutcome(approved, delivered OutcomePredicate, context WorkflowOutcomeEvaluationContext) (WorkflowOutcomeEvaluation, error) {
	if context.Registry == nil {
		return WorkflowOutcomeEvaluation{}, workflowPinFailure("workflow definition registry is required for outcome evaluation")
	}
	registered, err := VerifyWorkflowDefinitionPin(context.Registry, context.DefinitionPin)
	if err != nil {
		return WorkflowOutcomeEvaluation{}, err
	}
	if err := ValidateWorkflowPredicateForDefinition(registered.Definition, approved); err != nil {
		return WorkflowOutcomeEvaluation{}, err
	}
	if err := ValidateWorkflowPredicateForDefinition(registered.Definition, delivered); err != nil {
		return WorkflowOutcomeEvaluation{}, err
	}
	if err := ValidateWorkflowPredicate(approved); err != nil {
		return WorkflowOutcomeEvaluation{}, err
	}
	if err := ValidateWorkflowPredicate(delivered); err != nil {
		return WorkflowOutcomeEvaluation{}, err
	}
	if context.ExecutingActor != nil || context.VerdictActor != nil {
		if context.ExecutingActor == nil || context.VerdictActor == nil {
			return WorkflowOutcomeEvaluation{}, newFailure(KindUnauthorized, "workflow_outcome", "evaluator actor tuple is incomplete", false, "supply complete executing and verdict actor tuples")
		}
		if err := ValidateDistinctWorkflowActors(*context.ExecutingActor, *context.VerdictActor); err != nil {
			return WorkflowOutcomeEvaluation{}, err
		}
	}
	strength, err := CompareWorkflowPredicates(approved, delivered)
	if err != nil {
		return WorkflowOutcomeEvaluation{}, err
	}
	result := WorkflowOutcomeEvaluation{Strength: strength, VerdictKind: "outcome_mismatch", IncomparableWithApproved: strength == StrengthIncomparable}
	switch approved.Kind {
	case PredicateExists, PredicateAbsent:
		if context.GroundTruth == nil {
			return WorkflowOutcomeEvaluation{}, workflowOutcomeFailure("ground-truth resolver is required")
		}
		for _, subject := range approved.Subjects {
			state, err := context.GroundTruth.Resolve(approved.Surface, subject)
			if err != nil {
				return WorkflowOutcomeEvaluation{}, err
			}
			if approved.Kind == PredicateExists && state != GroundTruthPresent {
				return result, nil
			}
			if approved.Kind == PredicateAbsent && state != GroundTruthAbsent {
				return result, nil
			}
		}
	case PredicateOutcome:
		if delivered.Kind != PredicateOutcome || len(delivered.Allowed) != 1 || !containsString(approved.Allowed, delivered.Allowed[0]) {
			return result, nil
		}
	case PredicateCheck:
		if strength == StrengthIncomparable {
			return result, nil
		}
		if context.Checks == nil {
			return WorkflowOutcomeEvaluation{}, workflowOutcomeFailure("check resolver is required")
		}
		strength, err = context.Checks.Compare(approved.CheckRef, approved.ImmutableSubjectRef, approved.ExpectedResult)
		if err != nil {
			return WorkflowOutcomeEvaluation{}, err
		}
		result.Strength, result.IncomparableWithApproved = strength, strength == StrengthIncomparable
		if strength != StrengthStrongerOrEqual {
			return result, nil
		}
	}
	if strength == StrengthStrongerOrEqual {
		result.Satisfied, result.VerdictKind = true, "ok"
	}
	return result, nil
}

// CompareCheckResults adapts a registered check's closed result to the
// engine's strength vocabulary without coercing incomparable outcomes.
func CompareCheckResults(strength WorkflowStrength) error {
	if strength != StrengthStrongerOrEqual && strength != StrengthWeaker && strength != StrengthIncomparable {
		return workflowOutcomeFailure("check evaluator returned an unknown strength")
	}
	return nil
}

type ActorClass string

const (
	ActorAgent    ActorClass = "agent"
	ActorOperator ActorClass = "operator"
)

type WorkflowActor struct {
	ActorRef     string     `json:"actor_ref,omitempty"`
	PrincipalRef string     `json:"principal_ref"`
	ClientRef    string     `json:"client_ref"`
	AgentRef     string     `json:"agent_ref"`
	SessionRef   string     `json:"session_ref"`
	ActorClass   ActorClass `json:"actor_class"`
}

func ValidateWorkflowActor(actor WorkflowActor) error {
	if !validReference(actor.PrincipalRef) || !validReference(actor.ClientRef) || !validReference(actor.AgentRef) || !validReference(actor.SessionRef) || (actor.ActorClass != ActorAgent && actor.ActorClass != ActorOperator) {
		return newFailure(KindUnauthorized, "workflow_actor", "actor tuple is incomplete or has an unknown actor class", false, "supply all four authenticated actor references")
	}
	if actor.ActorRef != "" && actor.ActorRef != DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef) {
		return newFailure(KindUnauthorized, "workflow_actor", "actor_ref does not match its canonical tuple", false, "derive actor_ref from the complete tuple")
	}
	return nil
}
func CanonicalWorkflowActorTuple(actor WorkflowActor) ([]byte, error) {
	if err := ValidateWorkflowActor(actor); err != nil {
		return nil, err
	}
	return []byte("actor-v1\x00" + actorField("principal_ref", actor.PrincipalRef) + actorField("client_ref", actor.ClientRef) + actorField("agent_ref", actor.AgentRef) + actorField("session_ref", actor.SessionRef)), nil
}
func WorkflowActorRef(actor WorkflowActor) (string, error) {
	canonical, err := CanonicalWorkflowActorTuple(actor)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "actor:" + hex.EncodeToString(sum[:]), nil
}
func ValidateDistinctWorkflowActors(executing, verdict WorkflowActor) error {
	if err := ValidateWorkflowActor(executing); err != nil {
		return err
	}
	if err := ValidateWorkflowActor(verdict); err != nil {
		return err
	}
	if executing.ActorClass == ActorAgent && verdict.ActorClass == ActorOperator {
		return nil
	}
	if executing.AgentRef == verdict.AgentRef && executing.SessionRef == verdict.SessionRef {
		return newFailure(KindUnauthorized, "workflow_actor", "executing actor cannot author its own verdict", false, "record an independent evaluator verdict")
	}
	return nil
}
