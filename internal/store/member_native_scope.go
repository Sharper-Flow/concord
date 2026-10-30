package store

import "context"

type sessionWorktreeOwnerKey struct{}

// WithSessionWorktreeOwner carries the authenticated host tuple to native reads.
func WithSessionWorktreeOwner(ctx context.Context, owner SessionWorktreeOwner) context.Context {
	return context.WithValue(ctx, sessionWorktreeOwnerKey{}, owner)
}

func sessionWorktreeOwnerFromContext(ctx context.Context) SessionWorktreeOwner {
	owner, _ := ctx.Value(sessionWorktreeOwnerKey{}).(SessionWorktreeOwner)
	return owner
}

func worktreeMemberScopeFromContext(ctx context.Context, q queryer, workID, projectID string) (string, error) {
	owner := sessionWorktreeOwnerFromContext(ctx)
	if owner == (SessionWorktreeOwner{}) {
		return "", nil
	}
	return workflowMemberClaimScope(ctx, q, workID, projectID, owner)
}

func authorizeMemberWorkScope(ctx context.Context, q queryer, workID, projectID string) error {
	owner := sessionWorktreeOwnerFromContext(ctx)
	if owner == (SessionWorktreeOwner{}) {
		return nil
	}
	actor, err := workflowMemberOwnerActor(ctx, q, owner)
	if err != nil {
		return err
	}
	member, err := readWorkflowMemberCallerQ(ctx, q, actor)
	if err != nil {
		return err
	}
	if member != nil && (member.WorkID != workID || member.ProjectID != projectID) {
		return newFailure(KindUnauthorized, "member_scope", "member caller cannot access another work or Project", false, "use the recorded member assignment")
	}
	return nil
}

func memberCallerInSession(ctx context.Context, q queryer, session string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_member_callers m JOIN workflow_actors a ON a.actor_ref=m.actor_ref WHERE a.session_ref=?)`, session).Scan(&exists)
	return exists, err
}
