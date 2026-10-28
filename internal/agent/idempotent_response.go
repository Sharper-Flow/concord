package agent

import (
	"encoding/json"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// mutationResponseSnapshot retains the response facts that are not part of the
// operation's closed payload. It shares the effect's idempotency commit boundary.
type mutationResponseSnapshot struct {
	ManifestDigest   string                      `json:"manifest_digest"`
	ResolvedScope    *agentcontract.Scope        `json:"resolved_scope"`
	Omissions        []agentcontract.Notice      `json:"omissions"`
	NextValidIntents []agentcontract.NextIntent  `json:"next_valid_intents"`
	EvidenceRefs     []agentcontract.EvidenceRef `json:"evidence_refs"`
	PinlessWorkIDs   []string                    `json:"pinless_work_ids"`
	ApprovalConsumed bool                        `json:"approval_consumed"`
}

func mutationResponseScope(scope map[string]any, response agentcontract.Envelope, pinless []string, approvalConsumed bool) map[string]any {
	snapshot := boundedApprovalScope(scope)
	snapshot["mutation_response"] = mutationResponseSnapshot{
		ManifestDigest: agentcontract.ManifestDigest, ResolvedScope: response.ResolvedScope, Omissions: response.Omissions,
		NextValidIntents: *response.NextValidIntents, EvidenceRefs: response.EvidenceRefs,
		PinlessWorkIDs: pinless, ApprovalConsumed: approvalConsumed,
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
	incomplete := newRuntimeFailure("operation_conflict", "committed mutation response is incomplete or belongs to another contract; reconcile the retained record before retrying", "reconcile_operation", false)
	if err := json.Unmarshal([]byte(scopeJSON), &snapshot); err != nil || snapshot.Response == nil || snapshot.Response.ManifestDigest != agentcontract.ManifestDigest {
		return mutationResponseSnapshot{}, incomplete
	}
	if err := json.Unmarshal([]byte(scopeJSON), &fields); err != nil {
		return mutationResponseSnapshot{}, incomplete
	}
	for _, key := range []string{"resolved_scope", "omissions", "next_valid_intents", "evidence_refs", "pinless_work_ids", "approval_consumed"} {
		if _, present := fields.Response[key]; !present {
			return mutationResponseSnapshot{}, incomplete
		}
	}
	if snapshot.Response.NextValidIntents == nil || string(fields.Response["approval_consumed"]) == "null" {
		return mutationResponseSnapshot{}, incomplete
	}
	return *snapshot.Response, nil
}

func incompleteMutationResponse(base agentcontract.Envelope, message string) agentcontract.Envelope {
	response := coreError(base, "operation_conflict", message, "reconcile_operation", false)
	response.Error.EffectState = agentcontract.EffectPossible
	return response
}

// recordedMutationResult never reads mutable work state or the event log. A
// missing response is a cutover dependency, not evidence that no effect occurred.
func (r runtime) recordedMutationResult(base agentcontract.Envelope, prior store.MutationIdempotencyRecord) agentcontract.Envelope {
	snapshot, err := retainedMutationResponse(prior.AuthorizedScopeSnapshot)
	if err != nil {
		return incompleteMutationResponse(base, err.Error())
	}
	var changed []agentcontract.ChangedRef
	if err := json.Unmarshal([]byte(prior.ChangedRefs), &changed); err != nil || changed == nil {
		return incompleteMutationResponse(base, "committed mutation changed references are incomplete; reconcile the retained record")
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
