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
	operation string
	actorHint string
	freshHint string
}

// workflowApprovalBinding is the payload-carried approval binding a fold
// re-checks for self-consistency. The admission route verifies these fields
// against the durable approval row and consumes the row before the event
// exists, so the recorded fields are exactly what admission checked.
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
		operation: "duplicate contract recovery",
		actorHint: "submit the recovery through the approved operator route",
		freshHint: "request a fresh approval for the exact recovery operation",
	}
	// deliveryCorrectionApprovalSubject admits a delivery correction through
	// its operator-approved route; the approval boundary consumes the
	// approval, so an unconsumed row also sends the requester back to it.
	deliveryCorrectionApprovalSubject = workflowApprovalBindingSubject{
		operation: "delivery correction",
		actorHint: "submit the correction through the approved operator route",
		freshHint: "request a fresh approval for the exact correction operation",
	}
)

// authorizeWorkflowOperatorApprovalTx admits a fold event only through a
// complete, self-consistent approval binding recorded in the event payload and
// the recorded operator workflow actor that names that binding's approval.
// Every input is either the event payload or the log-derived workflow_actors
// projection, so a replay re-derives the same admission without mutable
// out-of-log approval state. Live-table authorization stays at the admission
// route, which verifies the binding against the durable approval row and
// consumes that one-use row before the event exists.
func authorizeWorkflowOperatorApprovalTx(ctx context.Context, tx *sql.Tx, event Event, binding workflowApprovalBinding, subject workflowApprovalBindingSubject) error {
	if binding.ApprovalRef == "" || !validDigest(binding.OperationDigest) || binding.ScopeJSON == "" || binding.VersionsJSON == "" || binding.Consequence == "" {
		return newFailure(KindInvalidPayload, "fold_event", subject.operation+" carries no complete operator approval binding", false, subject.freshHint)
	}
	var actorClass ActorClass
	var agentRef string
	if err := tx.QueryRowContext(ctx, `SELECT actor_class,agent_ref FROM workflow_actors WHERE actor_ref=?`, event.Actor).Scan(&actorClass, &agentRef); err != nil {
		if err == sql.ErrNoRows {
			return newFailure(KindUnauthorized, "fold_event", subject.operation+" requires a recorded operator approval actor", false, subject.actorHint)
		}
		return workflowProjectionError(err, "cannot read the "+subject.operation+" actor")
	}
	approvalRef := strings.TrimPrefix(agentRef, "approval:")
	if actorClass != ActorOperator || approvalRef == agentRef || binding.ApprovalRef != approvalRef {
		return newFailure(KindUnauthorized, "fold_event", subject.operation+" requires a recorded operator approval actor", false, subject.actorHint)
	}
	return nil
}
