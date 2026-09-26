package store

import (
	"context"
	"database/sql"
	"strings"
)

// workflowApprovalBindingSubject names the operation a fold admits only
// through a consumed one-use operator approval, and carries the remediation
// each refusal suggests for that operation's approved route.
type workflowApprovalBindingSubject struct {
	operation    string
	actorHint    string
	consumedHint string
	freshHint    string
}

// workflowApprovalBinding is the payload-carried approval binding a fold
// re-checks against the durable approval row inside its own transaction.
type workflowApprovalBinding struct {
	ApprovalRef     string
	OperationDigest string
	ScopeJSON       string
	VersionsJSON    string
	Consequence     string
}

var (
	// contractRecoveryApprovalSubject admits duplicate-contract supersession
	// recovery through its operator-approved route.
	contractRecoveryApprovalSubject = workflowApprovalBindingSubject{
		operation:    "duplicate contract recovery",
		actorHint:    "submit the recovery through the approved operator route",
		consumedHint: "submit the recovery through the approved operator route",
		freshHint:    "request a fresh approval for the exact recovery operation",
	}
	// deliveryCorrectionApprovalSubject admits a delivery correction through
	// its operator-approved route; the approval boundary consumes the
	// approval, so an unconsumed row also sends the requester back to it.
	deliveryCorrectionApprovalSubject = workflowApprovalBindingSubject{
		operation:    "delivery correction",
		actorHint:    "submit the correction through the approved operator route",
		consumedHint: "request the core operator approval for this correction",
		freshHint:    "request a fresh approval for the exact correction operation",
	}
)

// authorizeWorkflowOperatorApprovalTx admits a fold event only through a
// recorded one-use operator approval bound to this exact operation digest,
// scope, versions, consequence, and client. It runs inside the fold's
// transaction, so a replay re-checks the same binding. The actor row must
// record the operator class and name the approval, so the durable record
// shows the operator approval — not a session identity — asserting the event.
func authorizeWorkflowOperatorApprovalTx(ctx context.Context, tx *sql.Tx, event Event, binding workflowApprovalBinding, subject workflowApprovalBindingSubject) error {
	var actorClass ActorClass
	var agentRef, actorClientRef string
	if err := tx.QueryRowContext(ctx, `SELECT actor_class,agent_ref,client_ref FROM workflow_actors WHERE actor_ref=?`, event.Actor).Scan(&actorClass, &agentRef, &actorClientRef); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindUnauthorized, "fold_event", subject.operation+" requires a recorded operator approval actor", false, subject.actorHint)
		}
		return workflowProjectionError(err, "cannot read the "+subject.operation+" actor")
	}
	approvalRef := strings.TrimPrefix(agentRef, "approval:")
	if actorClass != ActorOperator || approvalRef == agentRef || binding.ApprovalRef == "" || binding.ApprovalRef != approvalRef {
		return newFailure(KindUnauthorized, "fold_event", subject.operation+" requires a recorded operator approval actor", false, subject.actorHint)
	}
	var usedCount, maxUses int
	var approvalDigest, approvalScopeJSON, approvalVersionsJSON, approvalConsequence, approvalClientRef string
	if err := tx.QueryRowContext(ctx, `SELECT operation_digest,scope_json,version_json,consequence,client_ref,used_count,max_uses FROM agent_approvals WHERE approval_ref=? AND revoked_at IS NULL`, binding.ApprovalRef).Scan(&approvalDigest, &approvalScopeJSON, &approvalVersionsJSON, &approvalConsequence, &approvalClientRef, &usedCount, &maxUses); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindApprovalRequired, "fold_event", subject.operation+" requires a consumed operator approval", false, subject.consumedHint)
		}
		return workflowProjectionError(err, "cannot read the "+subject.operation+" approval")
	}
	if usedCount != 1 || maxUses != 1 {
		return newFailure(KindApprovalRequired, "fold_event", subject.operation+" requires a consumed one-use operator approval", false, subject.consumedHint)
	}
	mismatches := make([]string, 0, 5)
	if !validDigest(binding.OperationDigest) || binding.OperationDigest != approvalDigest {
		mismatches = append(mismatches, "digest")
	}
	if binding.ScopeJSON == "" || binding.ScopeJSON != approvalScopeJSON {
		mismatches = append(mismatches, "scope")
	}
	if binding.VersionsJSON == "" || binding.VersionsJSON != approvalVersionsJSON {
		mismatches = append(mismatches, "versions")
	}
	if binding.Consequence == "" || binding.Consequence != approvalConsequence {
		mismatches = append(mismatches, "consequence")
	}
	if approvalClientRef != actorClientRef {
		mismatches = append(mismatches, "client")
	}
	if len(mismatches) != 0 {
		return newFailure(KindUnauthorized, "fold_event", subject.operation+" approval is not bound to the exact operation, scope, versions, or consequence: "+strings.Join(mismatches, ","), false, subject.freshHint)
	}
	return nil
}
