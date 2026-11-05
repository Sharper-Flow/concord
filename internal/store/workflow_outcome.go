package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
)

// MaxWorkflowActionPayloadBytes is the whole-action payload decode budget:
// the shared action payload ceiling the agent request transport limit
// aliases. The widest-shape derivation in internal/agent pins it to one
// next power of two, so a shape growth fails the derivation instead of the
// transport.
const MaxWorkflowActionPayloadBytes = 4 * 1024 * 1024

// maxWorkflowActionPayloadBytes is the package-local alias of the shared
// action payload ceiling.
const maxWorkflowActionPayloadBytes = MaxWorkflowActionPayloadBytes

const maxWorkflowJSONDepth = 64
const maxWorkflowJSONContainerEntries = 128

type WorkflowActorTuple = WorkflowActor

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

// decodeWorkflowActionPayloadStrict decodes one whole workflow action payload
// — or one typed action document a payload field carries — under the
// whole-action limit, with the strictness the frozen decode and replay
// corpus replay through unchanged.
func decodeWorkflowActionPayloadStrict(data []byte, target any) error {
	return decodeWorkflowJSONStrict(data, maxWorkflowActionPayloadBytes, target)
}

func validateWorkflowJSONStructure(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanWorkflowJSONValue(decoder, 0, maxWorkflowJSONContainerEntries, ""); err != nil {
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
// than maxWorkflowJSONContainerEntries members, except the contract's
// legislative subjects. The decoder's own token
// state refuses malformed tokens and mismatched or missing closing
// delimiters. The caller reports every refusal as one structure failure.
func scanWorkflowJSONValue(decoder *json.Decoder, depth, limit int, parent string) error {
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
		if entries > limit {
			return workflowOutcomeFailure("JSON container exceeds the entry bound")
		}
		childLimit := maxWorkflowJSONContainerEntries
		childName := ""
		if delim == '{' {
			keyToken, tokenErr := decoder.Token()
			key, ok := keyToken.(string)
			if tokenErr != nil || !ok || seen[key] {
				return workflowOutcomeFailure("JSON object contains a duplicate or invalid field")
			}
			seen[key] = true
			childName = key
			if key == "legislative_subjects" && (depth == 0 || depth == 1 && parent == "contract") {
				childLimit = 800
			}
		}
		if err := scanWorkflowJSONValue(decoder, depth+1, childLimit, childName); err != nil {
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
	return newFailure(KindInvalidPayload, "workflow_outcome", detail, false, "supply valid bounded JSON")
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
	// which this actor acted. It is deliberately not part of the
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
