package agent

import "github.com/sharper-flow/concord/internal/agentcontract"

// mutationDurability declares one registered mutation's transaction class.
// The table is closed, keyed by the exact registered ContractOperation.ID; a
// lookup that misses is an invariant failure, never a default write. Consent
// metadata never feeds this selection, so consent cannot lower the boundary.
type mutationDurability struct {
	// initial is the executor's opening selection before planner facts join.
	initial bool
	// replay is the early-replay baseline before stored receipts apply.
	replay bool
}

// Baselines state the transaction class each operation keeps regardless of
// consent: initial selects the executor's opening transaction — the durable
// journal behind an operator-governed consequence, plus worktree_claim,
// whose SQL authority binds to an already-created native tree — and replay
// selects the early-replay baseline that re-acknowledges the original
// authority. A planner's conditional fact replaces initial, never replay.
var mutationDurabilityDeclarations = map[string]mutationDurability{
	"concord_work_define.capture":                      {},
	"concord_work_define.revise_intent":                {},
	"concord_work_define.research_pack_create":         {},
	"concord_work_define.research_revision_append":     {},
	"concord_work_define.research_finding_record":      {},
	"concord_work_define.research_source_record":       {},
	"concord_work_define.research_freshness_set":       {},
	"concord_work_define.research_retire":              {},
	"concord_work_define.observation_record":           {},
	"concord_work_define.issue_link_record":            {},
	"concord_work_transition.lifecycle":                {},
	"concord_work_transition.remove":                   {initial: true, replay: true},
	"concord_work_transition.workflow_action":          {replay: true},
	"concord_work_transition.worktree_claim":           {initial: true, replay: true},
	"concord_work_transition.session_vacate":           {},
	"concord_work_transition.project_handoff_record":   {},
	"concord_work_transition.project_handoff_consume":  {},
	"concord_work_transition.worktree_reclaim":         {},
	"concord_work_transition.worktree_audit_reclaim":   {},
	"concord_work_transition.worktree_verify":          {},
	"concord_work_transition.worktree_destroy":         {},
	"concord_work_relate.set_memberships":              {},
	"concord_work_relate.resolve_overlap":              {initial: true, replay: true},
	"concord_work_relate.message_send":                 {},
	"concord_work_relate.message_withdraw":             {},
	"concord_work_compact.publish":                     {initial: true, replay: true},
	"concord_work_compact.reconcile":                   {replay: true},
	"concord_work_transition.correct_delivery":         {initial: true, replay: true},
	"concord_work_transition.outside_repair":           {initial: true, replay: true},
	"concord_work_transition.outside_repair_reconcile": {initial: true, replay: true},
}

// mutationDurabilityError is the typed invariant failure of a durability lookup.
type mutationDurabilityError struct{ opID, reason string }

func (e *mutationDurabilityError) Error() string {
	return "mutation durability declaration " + e.reason + ": " + e.opID
}

// mutationDurabilityFor resolves the declared baselines for one registered
// contract operation. Keying by identity alone means a copied operation with
// altered Approval metadata resolves identically; reads carry no durability.
func mutationDurabilityFor(op agentcontract.ContractOperation) (mutationDurability, error) {
	if op.Kind != agentcontract.OperationMutation {
		return mutationDurability{}, &mutationDurabilityError{opID: op.ID, reason: "kind " + string(op.Kind) + " carries no mutation durability"}
	}
	declared, ok := mutationDurabilityDeclarations[op.ID]
	if !ok {
		return mutationDurability{}, &mutationDurabilityError{opID: op.ID, reason: "registered mutation has no declaration"}
	}
	return declared, nil
}
