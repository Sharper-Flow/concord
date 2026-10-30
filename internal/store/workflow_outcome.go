package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"regexp"
	"slices"
	"strings"
)

// maxWorkflowPredicateBytes bounds one serialized outcome predicate. It is
// the predicate population's own limit and never measures a whole action
// payload; the whole-action decode budget is maxWorkflowActionPayloadBytes.
const maxWorkflowPredicateBytes = 64 * 1024

// MaxWorkflowActionPayloadBytes is the whole-action payload decode budget:
// the shared action payload ceiling the agent request transport limit
// aliases. The widest-shape derivation in internal/agent pins it to one
// next power of two, so a shape growth fails the derivation instead of the
// transport.
const MaxWorkflowActionPayloadBytes = 16 * 1024 * 1024

// maxWorkflowActionPayloadBytes is the package-local alias of the shared
// action payload ceiling.
const maxWorkflowActionPayloadBytes = MaxWorkflowActionPayloadBytes

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
	Question          string   `json:"question"`
	OptionsConsidered []string `json:"options_considered"`
	Decision          string   `json:"decision"`
	Rationale         string   `json:"rationale"`
	Consequences      []string `json:"consequences"`
	Inputs            []string `json:"inputs"`
	POCFindings       string   `json:"poc_findings"`
	Supersedes        *string  `json:"supersedes"`
	SupersededBy      *string  `json:"superseded_by"`
	Unknowns          []string `json:"unknowns"`
	RequiredToDecide  []string `json:"required_to_decide"`
	ReviewerActorRef  string   `json:"reviewer_actor_ref"`
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

func ValidateWorkflowPredicate(predicate OutcomePredicate) error {
	if !validPredicateKind(predicate.Kind) {
		return workflowOutcomeFailure("predicate kind is unknown")
	}
	switch predicate.Kind {
	case PredicateExists, PredicateAbsent:
		return validateWorkflowPresencePredicate(predicate)
	case PredicateOutcome:
		return validateWorkflowOutcomePredicate(predicate)
	case PredicateCheck:
		return validateWorkflowCheckPredicate(predicate)
	}
	return nil
}

func validateWorkflowPresencePredicate(predicate OutcomePredicate) error {
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
			if !slices.Contains([]string{"archived", "relocated", "renamed", "disabled"}, value) {
				return workflowOutcomeFailure("absent predicate distinction rule is unknown")
			}
		}
	}
	return nil
}

func validateWorkflowOutcomePredicate(predicate OutcomePredicate) error {
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
	return nil
}

func validateWorkflowCheckPredicate(predicate OutcomePredicate) error {
	if predicate.Surface != "" || predicate.Subjects != nil || predicate.DistinguishFrom != nil || predicate.Allowed != nil || predicate.DecisionRecord != nil {
		return workflowOutcomeFailure("predicate contains fields from another union member")
	}
	if !workflowCheckPattern.MatchString(predicate.CheckRef) || !workflowSubjectPattern.MatchString(predicate.ImmutableSubjectRef) || !slices.Contains([]string{"true", "false", "pass", "fail", "present", "absent", "healthy", "unhealthy", "accepted", "rejected"}, predicate.ExpectedResult) {
		return workflowOutcomeFailure("check predicate fields are invalid")
	}
	return nil
}

func ValidateWorkflowPredicateForDefinition(definition WorkflowDefinition, predicate OutcomePredicate) error {
	if err := ValidateWorkflowPredicate(predicate); err != nil {
		return err
	}
	if !slices.Contains(definition.OutcomeSchema.AllowedKinds, predicate.Kind) {
		return definitionFailure(KindInvalidDefinition, "predicate kind not allowed by the pinned workflow definition")
	}
	if predicate.Kind == PredicateOutcome {
		for _, token := range predicate.Allowed {
			if !slices.Contains(definition.OutcomeSchema.AllowedOutcomeTokens, token) {
				return definitionFailure(KindInvalidDefinition, "outcome token not allowed by the pinned workflow definition")
			}
		}
		if predicate.DecisionRecord != nil && !slices.Contains(predicate.Allowed, predicate.DecisionRecord.Decision) {
			return definitionFailure(KindInvalidDefinition, "decision record token is not allowed by the pinned workflow definition")
		}
	}
	return nil
}

func validateDecisionRecord(record WorkflowDecisionRecord) error {
	if len(record.Question) < 1 || len(record.Question) > 4096 || len(record.OptionsConsidered) < 1 || len(record.OptionsConsidered) > 16 || !uniqueStrings(record.OptionsConsidered) || (record.Decision != "accepted_decision" && record.Decision != "insufficient_evidence") || len(record.Rationale) < 1 || len(record.Rationale) > 4096 || len(record.Consequences) < 1 || len(record.Consequences) > 16 || len(record.Inputs) < 1 || len(record.Inputs) > 32 || len(record.POCFindings) < 1 || len(record.POCFindings) > 4096 || len(record.Unknowns) > 16 || len(record.RequiredToDecide) > 16 || !workflowActorRefPattern.MatchString(record.ReviewerActorRef) {
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

// decodeWorkflowJSONStrict is the shared strict JSON decode: one value, no
// duplicate keys, bounded nesting, no unknown fields, no trailing content,
// and a whole-payload size bound that refuses without truncating. The named
// wrappers carry each object's own limit; callers never pass a raw budget.
func decodeWorkflowJSONStrict(data []byte, maxBytes int, target any) error {
	if len(data) == 0 || len(data) > maxBytes {
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

// decodePredicateStrict decodes one serialized outcome predicate under the
// predicate population's own limit.
func decodePredicateStrict(data []byte, target any) error {
	return decodeWorkflowJSONStrict(data, maxWorkflowPredicateBytes, target)
}

// decodeWorkflowActionPayloadStrict decodes one whole workflow action payload
// — or one typed action document a payload field carries — under the
// whole-action limit, with the strictness the frozen decode and replay
// corpus replay through unchanged.
func decodeWorkflowActionPayloadStrict(data []byte, target any) error {
	return decodeWorkflowJSONStrict(data, maxWorkflowActionPayloadBytes, target)
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

// scanWorkflowJSONValue walks one JSON value and refuses duplicate object
// keys, values nested past maxWorkflowJSONDepth, and containers holding more
// than maxWorkflowJSONContainerEntries members. The decoder's own token
// state refuses malformed tokens and mismatched or missing closing
// delimiters. The caller reports every refusal as one structure failure.
func scanWorkflowJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxWorkflowJSONDepth {
		return workflowOutcomeFailure("JSON payload exceeds the recursion bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, _ := token.(json.Delim)
	if delim != '{' && delim != '[' {
		return nil
	}
	seen := map[string]bool{}
	for entries := 1; decoder.More(); entries++ {
		if entries > maxWorkflowJSONContainerEntries {
			return workflowOutcomeFailure("JSON container exceeds the entry bound")
		}
		if delim == '{' {
			keyToken, tokenErr := decoder.Token()
			key, ok := keyToken.(string)
			if tokenErr != nil || !ok || seen[key] {
				return workflowOutcomeFailure("JSON object contains a duplicate or invalid field")
			}
			seen[key] = true
		}
		if err := scanWorkflowJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
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
	// Model is the readback executing-model identity observed for the run in
	// which this actor acted (CD-0017 D5). It is deliberately not part of the
	// actor identity tuple: workflow_actors is keyed by its four authenticated
	// references, and one identity may act on different models across runs.
	Model string `json:"model,omitempty"`
}

// ValidateWorkflowActorModel bounds a recorded readback model identity. An
// empty value means no model was observed, which is legal for operator actors
// and for runs that never dispatched a typed lane.
func ValidateWorkflowActorModel(model string) error {
	if len(model) > 128 || model != strings.TrimSpace(model) {
		return newFailure(KindUnauthorized, "workflow_actor", "readback model identity is not a bounded reference", false, "record the readback model reported by the host")
	}
	return nil
}

func ValidateWorkflowActor(actor WorkflowActor) error {
	if !ValidReference(actor.PrincipalRef) || !ValidReference(actor.ClientRef) || !ValidReference(actor.AgentRef) || !ValidReference(actor.SessionRef) || (actor.ActorClass != ActorAgent && actor.ActorClass != ActorOperator) {
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
