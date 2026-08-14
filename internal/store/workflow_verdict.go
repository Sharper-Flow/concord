package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// CD-0023: an acceptance verdict is readable by every authority except the
// one that executed the work. The influence half is already held by CD-0013
// D5, which refuses a verdict authored by the executing actor tuple; this
// file owns the read half.

// WorkflowReadVerdict is the bounded, point-in-time view of one recorded
// acceptance verdict.
type WorkflowReadVerdict struct {
	TerminalState    string `json:"terminal_state"`
	FinalVerdictKind string `json:"final_verdict_kind"`
	VerdictActorRef  string `json:"verdict_actor_ref"`
	ImpactVerdict    string `json:"impact_verdict"`
	CompletedAt      string `json:"completed_at"`
}

// ReadWorkflowVerdict returns the verdict recorded by the most recent
// workflow.completed event for a work item, or nil when none exists. The
// verdict is immutable once recorded; a superseding completion event replaces
// the view, matching rebuild semantics.
func ReadWorkflowVerdict(ctx context.Context, s *Store, workID string) (*WorkflowReadVerdict, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "workflow_verdict", "store is not open", false, "open the authority database")
	}
	var payload []byte
	var occurredAt string
	err := s.db.QueryRowContext(ctx, `SELECT payload, occurred_at FROM domain_events WHERE kind='workflow.completed' AND subject_type=? AND subject_id=? ORDER BY seq DESC LIMIT 1`, SubjectWorkItem, workID).Scan(&payload, &occurredAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "workflow_verdict", "cannot read recorded verdict", true, "retry once the database is readable", err)
	}
	var completed workflowCompletedPayload
	if err := json.Unmarshal(payload, &completed); err != nil {
		return nil, wrapFailure(KindInvalidPayload, "workflow_verdict", "recorded verdict payload is not decodable", false, "contact_operator", err)
	}
	return &WorkflowReadVerdict{
		TerminalState:    completed.TerminalState,
		FinalVerdictKind: completed.FinalVerdictKind,
		VerdictActorRef:  completed.VerdictActorRef,
		ImpactVerdict:    completed.ImpactVerdict,
		CompletedAt:      occurredAt,
	}, nil
}

// WorkflowExecutingIdentity returns the agent and session refs of the actor
// recorded as executing a work item's workflow. found is false when the work
// has no workflow instance or no recorded executing actor.
func WorkflowExecutingIdentity(ctx context.Context, s *Store, workID string) (agentRef, sessionRef string, found bool, err error) {
	if s == nil || s.db == nil {
		return "", "", false, newFailure(KindUnavailable, "workflow_verdict", "store is not open", false, "open the authority database")
	}
	err = s.db.QueryRowContext(ctx, `SELECT a.agent_ref, a.session_ref FROM workflow_instances i JOIN workflow_actors a ON a.actor_ref = i.execution_actor_ref WHERE i.work_id=?`, workID).Scan(&agentRef, &sessionRef)
	if err == sql.ErrNoRows {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, wrapFailure(KindUnavailable, "workflow_verdict", "cannot read executing identity", true, "retry once the database is readable", err)
	}
	return agentRef, sessionRef, true, nil
}
