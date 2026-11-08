package agent

import (
	"encoding/json"
	"strconv"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func hasMutationResponse(scopeJSON string) (bool, error) {
	var scope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
		return false, err
	}
	if scope == nil {
		return false, newRuntimeFailure("malformed_response", "committed mutation scope is incomplete", "contact_operator", false)
	}
	_, present := scope["mutation_response"]
	return present, nil
}

// mutationResponseSnapshot retains the response facts that are not part of the
// operation's closed payload. It shares the effect's idempotency commit boundary.
type mutationResponseSnapshot struct {
	ManifestDigest   string                      `json:"manifest_digest"`
	ResolvedScope    *agentcontract.Scope        `json:"resolved_scope"`
	Omissions        []agentcontract.Notice      `json:"omissions"`
	NextValidIntents []agentcontract.NextIntent  `json:"next_valid_intents"`
	EvidenceRefs     []agentcontract.EvidenceRef `json:"evidence_refs"`
	PinlessWorkIDs   []string                    `json:"pinless_work_ids"`
}

func mutationResponseScope(scope map[string]any, response agentcontract.Envelope, pinless []string) map[string]any {
	snapshot := boundedMutationScope(scope)
	snapshot["mutation_response"] = mutationResponseSnapshot{
		ManifestDigest: agentcontract.ManifestDigest, ResolvedScope: response.ResolvedScope, Omissions: response.Omissions,
		NextValidIntents: *response.NextValidIntents, EvidenceRefs: response.EvidenceRefs,
		PinlessWorkIDs: pinless,
	}
	return snapshot
}

func retainedMutationResponse(scopeJSON string) (mutationResponseSnapshot, error) {
	var snapshot struct {
		Response *mutationResponseSnapshot `json:"mutation_response"`
	}
	var fields struct {
		Response map[string]json.RawMessage `json:"mutation_response"`
	}
	incomplete := newRuntimeFailure("malformed_response", "committed mutation response is incomplete; contact the operator to repair the retained response before retrying the same request", "contact_operator", false)
	// This digest records the producer, not a second invocation contract. The
	// caller binds the current manifest; consumers validate the retained facts
	// against the current result schema without conversion or a repeated effect.
	if err := json.Unmarshal([]byte(scopeJSON), &snapshot); err != nil || snapshot.Response == nil || snapshot.Response.ManifestDigest == "" {
		return mutationResponseSnapshot{}, incomplete
	}
	if err := json.Unmarshal([]byte(scopeJSON), &fields); err != nil {
		return mutationResponseSnapshot{}, incomplete
	}
	for _, key := range []string{"resolved_scope", "omissions", "next_valid_intents", "evidence_refs", "pinless_work_ids"} {
		if _, present := fields.Response[key]; !present {
			return mutationResponseSnapshot{}, incomplete
		}
	}
	if snapshot.Response.NextValidIntents == nil {
		return mutationResponseSnapshot{}, incomplete
	}
	return *snapshot.Response, nil
}

func incompleteMutationResponse(base agentcontract.Envelope, message string) agentcontract.Envelope {
	response := coreError(base, "malformed_response", message, "contact_operator", false)
	response.Error.EffectState = agentcontract.EffectPossible
	return response
}

// recordedMutationResult never reads mutable work state or the event log.
func (r runtime) recordedMutationResult(base agentcontract.Envelope, prior store.MutationIdempotencyRecord) agentcontract.Envelope {
	present, err := hasMutationResponse(prior.AuthorizedScopeSnapshot)
	if err == nil && !present && !(r.Tool == "concord_work_transition" && r.Operation == "workflow_action") {
		return r.legacyMutationResult(base, prior.ResultPayload, prior.ChangedRefs)
	}
	snapshot, err := retainedMutationResponse(prior.AuthorizedScopeSnapshot)
	if err != nil {
		return incompleteMutationResponse(base, err.Error())
	}
	var changed []agentcontract.ChangedRef
	if err := json.Unmarshal([]byte(prior.ChangedRefs), &changed); err != nil || changed == nil {
		return incompleteMutationResponse(base, "committed mutation changed references are incomplete; contact the operator to repair the retained response before retrying the same request")
	}
	base.Replayed = true
	base.ResolvedScope = snapshot.ResolvedScope
	base.Omissions = snapshot.Omissions
	base.EvidenceRefs = snapshot.EvidenceRefs
	response := r.mutationResult(base, json.RawMessage(prior.ResultPayload), changed, snapshot.NextValidIntents, snapshot.PinlessWorkIDs...)
	if response.Outcome == agentcontract.OutcomeError {
		response.Error.EffectState = agentcontract.EffectPossible
	}
	return response
}

func (r runtime) legacyMutationResult(base agentcontract.Envelope, payload, refs string) agentcontract.Envelope {
	var changed []agentcontract.ChangedRef
	if err := json.Unmarshal([]byte(refs), &changed); err != nil || changed == nil {
		return incompleteMutationResponse(base, "committed mutation changed references are incomplete; contact the operator to repair the retained response before retrying the same request")
	}
	var document struct {
		Intents []agentcontract.NextIntent `json:"next_valid_intents"`
	}
	if err := json.Unmarshal([]byte(payload), &document); err != nil || document.Intents == nil {
		return incompleteMutationResponse(base, "committed mutation result is incomplete; contact the operator before retrying the same request")
	}
	base.Replayed = true
	response := r.mutationResult(base, json.RawMessage(payload), changed, document.Intents)
	if response.Outcome == agentcontract.OutcomeError {
		response.Error.EffectState = agentcontract.EffectPossible
	}
	return response
}

func (r runtime) legacyWorkflowResult(base agentcontract.Envelope, operation store.OperationResult) agentcontract.Envelope {
	if operation.ChangedRefs == nil {
		return incompleteMutationResponse(base, "durable workflow changed references are incomplete; contact the operator before retrying the same request")
	}
	changed := make([]agentcontract.ChangedRef, 0, len(operation.ChangedRefs))
	for _, raw := range operation.ChangedRefs {
		var ref mutationResultChangedRef
		if err := json.Unmarshal([]byte(raw), &ref); err != nil || ref.EntityKind == "" || ref.ID == "" || ref.Version <= 0 {
			return incompleteMutationResponse(base, "durable workflow changed references are incomplete; contact the operator before retrying the same request")
		}
		changed = append(changed, agentcontract.ChangedRef{EntityKind: ref.EntityKind, ID: ref.ID, Version: strconv.FormatInt(ref.Version, 10)})
	}
	var doc struct {
		Pins []store.WorkPin            `json:"work_pins"`
		Refs []mutationResultChangedRef `json:"changed_refs"`
	}
	if err := json.Unmarshal([]byte(operation.ResultPayload), &doc); err != nil {
		return incompleteMutationResponse(base, "durable workflow result is incomplete; contact the operator before retrying the same request")
	}
	if len(doc.Refs) != len(changed) {
		return incompleteMutationResponse(base, "durable workflow result and changed references disagree; contact the operator before retrying the same request")
	}
	for i, ref := range doc.Refs {
		if ref.EntityKind != changed[i].EntityKind || ref.ID != changed[i].ID || strconv.FormatInt(ref.Version, 10) != changed[i].Version {
			return incompleteMutationResponse(base, "durable workflow result and changed references disagree; contact the operator before retrying the same request")
		}
	}
	if len(pinlessMutationWorkIDs(changed, doc.Pins)) != 0 {
		work := ""
		if len(operation.WorkID) > 0 && len(operation.WorkID) <= 128 {
			work = " for work " + strconv.Quote(operation.WorkID)
		}
		return incompleteMutationResponse(base, "original WorkPin was not retained"+work+"; inspect current state with concord_work_trace.resume, or concord_work_browse.list scoped to the work ID if no longer resumable; if the work was removed, no current work state is available; this does not recover the original pin or permit repeating the effect")
	}
	refs, err := json.Marshal(changed)
	if err != nil {
		return incompleteMutationResponse(base, "durable workflow changed references are incomplete; contact the operator before retrying the same request")
	}
	return r.legacyMutationResult(base, operation.ResultPayload, string(refs))
}
