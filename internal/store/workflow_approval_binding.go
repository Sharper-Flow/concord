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

// workflowApprovalOperator pairs the core approval's actor tuple and its ref.
type workflowApprovalOperator struct {
	WorkflowActor
	ref string
}

// workflowOperatorFromConsumedApprovalTx is the live admission half shared by
// operator-only event routes. Replay uses the payload binding and actor instead.
func workflowOperatorFromConsumedApprovalTx(ctx context.Context, tx *sql.Tx, binding workflowApprovalBinding, operation string) (workflowApprovalOperator, error) {
	if binding.ApprovalRef == "" {
		return workflowApprovalOperator{}, newFailure(KindInvalidPayload, operation, "operation requires an operator approval reference", false, "request the core operator approval for this operation")
	}
	var principalRef, clientRef, sessionRef, approvalDigest, approvalScopeJSON, approvalVersionsJSON, approvalConsequence string
	var usedCount, maxUses int
	if err := tx.QueryRowContext(ctx, `SELECT human_principal_ref,client_ref,session_ref,operation_digest,scope_json,version_json,consequence,used_count,max_uses FROM agent_approvals WHERE approval_ref=? AND revoked_at IS NULL`, binding.ApprovalRef).Scan(&principalRef, &clientRef, &sessionRef, &approvalDigest, &approvalScopeJSON, &approvalVersionsJSON, &approvalConsequence, &usedCount, &maxUses); err != nil {
		if err == sql.ErrNoRows {
			return workflowApprovalOperator{}, newFailure(KindApprovalRequired, operation, "operation requires a consumed operator approval", false, "request the core operator approval for this operation")
		}
		return workflowApprovalOperator{}, wrapFailure(KindUnavailable, operation, "cannot read the operator approval", true, "retry once the approval projection is readable", err)
	}
	if usedCount != 1 || maxUses != 1 {
		return workflowApprovalOperator{}, newFailure(KindApprovalRequired, operation, "operation requires a consumed one-use operator approval", false, "request the core operator approval for this operation")
	}
	mismatches := make([]string, 0, 4)
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
	if len(mismatches) != 0 {
		return workflowApprovalOperator{}, newFailure(KindUnauthorized, operation, "operator approval is not bound to the exact operation, scope, versions, or consequence: "+strings.Join(mismatches, ","), false, "request approval for the exact operation")
	}
	tuple := WorkflowActor{PrincipalRef: principalRef, ClientRef: clientRef, AgentRef: "approval:" + binding.ApprovalRef, SessionRef: sessionRef, ActorClass: ActorOperator}
	ref, err := WorkflowActorRef(tuple)
	if err != nil {
		return workflowApprovalOperator{}, newFailure(KindInvalidPayload, operation, "operator approval does not carry a bounded actor tuple", false, "request approval for this operation")
	}
	return workflowApprovalOperator{WorkflowActor: tuple, ref: ref}, nil
}

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
