package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// The contract approval binding: the typed design identity a binding-capable
// approval records, the live comparison that admits it, and the event-backed
// reader that reports it. Capability comes from the pinned closed
// approve_contract declaration alone: neither stored design presence nor a
// payload version grants it, and replay never re-derives a recorded identity
// from mutable design state.

// workflowDesignBinding is the typed design identity one contract approval
// binds: the design revision and the design projection's canonical content
// digest.
type workflowDesignBinding struct {
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

// workflowDesignDigestShape reports the canonical content digest form the
// design projection derives: sha256 over 64 lowercase hexadecimal digits.
func workflowDesignDigestShape(digest string) bool {
	if len(digest) != 71 || !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	for _, r := range digest[len("sha256:"):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// validateWorkflowDesignBindingShape holds the closed member shape: exactly
// the typed pair the decoder produced, a positive revision, and a canonical
// digest.
func validateWorkflowDesignBindingShape(binding *workflowDesignBinding, op string) error {
	if binding.Revision < 1 {
		return newFailure(KindInvalidPayload, op, "design_binding revision must be a positive design revision", false, "supply the current design projection's revision")
	}
	if !workflowDesignDigestShape(binding.Digest) {
		return newFailure(KindInvalidPayload, op, "design_binding digest is not a canonical content digest", false, "supply the design projection's sha256 digest")
	}
	return nil
}

// parseWorkflowDesignBinding decodes the closed binding member from raw
// payload bytes: one JSON object carrying exactly the typed revision and
// digest pair, never null and no other members.
func parseWorkflowDesignBinding(raw json.RawMessage, op string) (*workflowDesignBinding, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, newFailure(KindInvalidPayload, op, "design_binding is not a typed revision and digest pair", false, "supply design_binding with the design revision and its canonical digest")
	}
	var members map[string]json.RawMessage
	if err := decodeWorkflowActionPayloadStrict(raw, &members); err != nil || members == nil {
		return nil, newFailure(KindInvalidPayload, op, "design_binding is not a typed revision and digest pair", false, "supply design_binding with the design revision and its canonical digest")
	}
	if len(members) != 2 {
		return nil, newFailure(KindInvalidPayload, op, "design_binding carries only the revision and digest members", false, "supply exactly the typed design revision and digest pair")
	}
	revisionRaw, hasRevision := members["revision"]
	digestRaw, hasDigest := members["digest"]
	if !hasRevision || !hasDigest {
		return nil, newFailure(KindInvalidPayload, op, "design_binding requires the revision and digest members", false, "supply exactly the typed design revision and digest pair")
	}
	var revision int64
	if err := json.Unmarshal(revisionRaw, &revision); err != nil {
		return nil, newFailure(KindInvalidPayload, op, "design_binding revision is not an integer", false, "supply the design projection's revision")
	}
	var digest string
	if err := json.Unmarshal(digestRaw, &digest); err != nil {
		return nil, newFailure(KindInvalidPayload, op, "design_binding digest is not a string", false, "supply the design projection's canonical digest")
	}
	binding := &workflowDesignBinding{Revision: revision, Digest: digest}
	if err := validateWorkflowDesignBindingShape(binding, op); err != nil {
		return nil, err
	}
	return binding, nil
}

// workflowApproveContractDeclaresDesignBinding reports whether the pinned
// definition's approve_contract payload carries the full closed required
// object declaration. The declared shape is the capability source: a loose
// name, an optional member, or an open payload grants nothing.
func workflowApproveContractDeclaresDesignBinding(definition WorkflowDefinition) bool {
	for _, action := range definition.ActionDefinitions {
		if action.ID != "approve_contract" {
			continue
		}
		if !action.Payload.Closed {
			return false
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "design_binding" {
				return field.Required && field.ValueType == PayloadObject
			}
		}
	}
	return false
}

// workflowCurrentDesignBindingTx reads the work's current canonical complete
// design identity inside the caller's transaction. A work without an
// architecture-bearing design has no identity to bind and refuses rather than
// inventing one.
func workflowCurrentDesignBindingTx(ctx context.Context, q queryer, workID, op string) (*workflowDesignBinding, error) {
	current, absent, err := readCurrentWorkflowDesign(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if absent || current == nil || current.Revision == nil || current.Digest == "" {
		return nil, newFailure(KindInvalidPayload, op, "contract approval binding requires a current canonical complete design", false, "record the typed architecture design before approving the contract")
	}
	return &workflowDesignBinding{Revision: *current.Revision, Digest: current.Digest}, nil
}

// workflowCompareDesignBinding refuses any identity the current design does
// not carry exactly: a stale revision, a substituted digest, and a
// mismatched pair all refuse through this one comparison.
func workflowCompareDesignBinding(supplied, current *workflowDesignBinding, op string) error {
	if supplied.Revision == current.Revision && supplied.Digest == current.Digest {
		return nil
	}
	return newFailure(KindInvalidPayload, op, fmt.Sprintf("design binding names revision %d digest %s, but the current design identity is revision %d digest %s", supplied.Revision, supplied.Digest, current.Revision, current.Digest), false, "approve against the current canonical design identity")
}

// validateWorkflowContractDesignBindingTx owns the fold-side binding wall. A
// stored approval below the binding version upcast here, and the
// supersession fold synthesizes its successor approval at the last
// historical version outside the upcast machinery. Upcast provenance marks
// live events too, so the replay flag decides history: a live approval
// without a binding still answers to the pinned declaration, and only the
// supersession context admits a historical successor. A native approval at
// the binding-bearing version always carries the member — every constructor
// writes it or stays on the historical version — and its recorded bytes pass
// the same closed parser the reader applies, so no shape the fold admits can
// be unreadable, and no raw event can bypass the constructor's admission.
// Replay compares nothing: the recorded identity is the authority.
func validateWorkflowContractDesignBindingTx(ctx context.Context, tx *sql.Tx, event Event, definition WorkflowDefinition, p workflowContractApprovedPayload, rawBinding json.RawMessage) error {
	if event.replaySourcePayloadVersion != 0 || event.PayloadVersion < 5 {
		if p.DesignBinding != nil {
			return newFailure(KindInvalidPayload, "fold_event", "a historical contract approval cannot carry design_binding", false, "record the binding-bearing approval at payload version 5")
		}
		if isWorkflowReplay(ctx) || !workflowApproveContractDeclaresDesignBinding(definition) || inWorkflowContractSupersessionContext(ctx) {
			return nil
		}
		return newFailure(KindInvalidPayload, "fold_event", "the pinned approve_contract requires design_binding on a live approval", false, "approve with the typed design revision and digest at the binding-bearing payload version")
	}
	if p.DesignBinding == nil {
		return newFailure(KindInvalidPayload, "fold_event", "the binding-bearing contract approval records no design binding", false, "supply the typed design revision and digest on the approval payload")
	}
	// The typed member names are the only recognition on this surface: the
	// decoder matches members case-insensitively while the raw map and the
	// reader match exactly, so an alias key can carry a second identity the
	// fold and the reader would read differently.
	if err := workflowRejectPayloadMemberAliases(event.Payload, "fold_event"); err != nil {
		return err
	}
	// The raw member's parsed identity is the one authority for admission:
	// the parsed shape is the exact closed shape, so no separate decoded
	// struct check is needed.
	binding, err := parseWorkflowDesignBinding(rawBinding, "fold_event")
	if err != nil {
		return err
	}
	if !workflowApproveContractDeclaresDesignBinding(definition) {
		return newFailure(KindInvalidPayload, "fold_event", "the pinned approve_contract declaration does not admit design_binding", false, "pin a workflow whose closed approve_contract declaration requires the binding")
	}
	if isWorkflowReplay(ctx) {
		return nil
	}
	current, err := workflowCurrentDesignBindingTx(ctx, tx, event.SubjectID, "fold_event")
	if err != nil {
		return err
	}
	return workflowCompareDesignBinding(binding, current, "fold_event")
}

// workflowContractApprovedMembers is the typed contract approval payload's
// exact JSON member names, derived from the struct itself with embedded
// fields flattened.
var workflowContractApprovedMembers = workflowTypedPayloadJSONNames(reflect.TypeOf(workflowContractApprovedPayload{}))

func workflowTypedPayloadJSONNames(typ reflect.Type) map[string]bool {
	names := map[string]bool{}
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous {
			for name := range workflowTypedPayloadJSONNames(field.Type) {
				names[name] = true
			}
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		names[name] = true
	}
	return names
}

// workflowRejectPayloadMemberAliases scans the whole native envelope before
// any map normalization: the strict structural owner refuses duplicate
// canonical keys, so the decoder's last-wins read and SQLite's first lookup
// can never see two different values, and then holds every member to the
// typed JSON names the struct itself derives.
func workflowRejectPayloadMemberAliases(payload []byte, op string) error {
	var members map[string]json.RawMessage
	if err := decodeWorkflowActionPayloadStrict(payload, &members); err != nil {
		return newFailure(KindInvalidPayload, op, "contract approval payload is not one strict JSON object with unique typed member names", false, "repair the workflow contract payload")
	}
	for key := range members {
		if !workflowContractApprovedMembers[key] {
			return newFailure(KindInvalidPayload, op, fmt.Sprintf("contract approval payload member %q is not a typed member name", key), false, "supply the typed payload's exact member names")
		}
	}
	return nil
}

// workflowContractDesignBindingTx reads the recorded contract design identity
// of the work's current active contract from that contract's own approval
// event: never from a design row or any other mutable projection. A legacy
// active contract reports no binding, not an empty one. A successor whose
// approval the supersession carried without a logged approval event is
// unbound rather than inheriting its superseded predecessor's identity. A
// recorded member that lost its typed shape is fold damage the reader
// refuses instead of interpreting.
func workflowContractDesignBindingTx(ctx context.Context, q queryer, workID string) (*workflowDesignBinding, int, error) {
	// The singular active-contract reader owns the ambiguity refusal: a
	// duplicated projection is replay corruption the reader reports instead
	// of selecting one version from.
	activeVersion, activeErr := activeWorkflowContractVersion(ctx, q, workID, "workflow_contract_binding")
	if activeErr == sql.ErrNoRows {
		return nil, 0, nil
	}
	if activeErr != nil {
		return nil, 0, activeErr
	}
	var payloadVersion int
	var payload string
	err := q.QueryRowContext(ctx, `SELECT payload_version,CAST(payload AS TEXT) FROM domain_events WHERE subject_type=? AND subject_id=? AND kind=? AND json_extract(payload,'$.contract_version')=? ORDER BY seq DESC LIMIT 1`, string(SubjectWorkItem), workID, WorkflowContractApproved, activeVersion).Scan(&payloadVersion, &payload)
	if err == sql.ErrNoRows {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, workflowProjectionError(err, "cannot read the workflow contract approval event")
	}
	if payloadVersion < 5 {
		return nil, payloadVersion, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return nil, payloadVersion, newFailure(KindInvariantViolation, "workflow_contract_binding", "contract approval payload is malformed", false, "rebuild projections from the event log")
	}
	raw, present := fields["design_binding"]
	if !present {
		return nil, payloadVersion, newFailure(KindInvariantViolation, "workflow_contract_binding", "binding-bearing contract approval records no design binding", false, "rebuild projections from the event log")
	}
	binding, err := parseWorkflowDesignBinding(raw, "workflow_contract_binding")
	if err != nil {
		return nil, payloadVersion, newFailure(KindInvariantViolation, "workflow_contract_binding", "recorded contract design binding is not the typed shape", false, "rebuild projections from the event log")
	}
	return binding, payloadVersion, nil
}
