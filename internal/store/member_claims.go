package store

import (
	"context"
	"encoding/json"
)

type sessionWorktreeOwnerKey struct{}

// WithSessionWorktreeOwner carries an authenticated host tuple to claim and
// native-read owners. It selects authority, never a caller-supplied member ID.
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

func worktreeClaimMemberScope(ctx context.Context, q queryer, req WorktreeClaimRequest) (string, error) {
	if req.Owner == (SessionWorktreeOwner{}) {
		req.Owner = sessionWorktreeOwnerFromContext(ctx)
	}
	if req.Owner == (SessionWorktreeOwner{}) {
		// Legacy host routes lack the complete tuple. A known narrowed session
		// must authenticate rather than silently selecting the integration tree.
		var narrowed bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_member_callers m JOIN workflow_actors a ON a.actor_ref=m.actor_ref WHERE a.session_ref=?)`, req.SessionRef).Scan(&narrowed); err != nil {
			return "", err
		}
		if narrowed {
			return "", newFailure(KindUnauthorized, "worktree_claim", "member claim requires the authenticated client, agent and session", false, "claim through the authenticated host boundary")
		}
		return "", nil
	}
	if req.Owner.SessionRef != req.SessionRef {
		return "", newFailure(KindUnauthorized, "worktree_claim", "claim session differs from its authenticated caller", false, "use the original authenticated session")
	}
	actor, err := workflowMemberOwnerActor(ctx, q, req.Owner)
	if err != nil {
		return "", err
	}
	if actor.PrincipalRef != req.PrincipalRef {
		return "", newFailure(KindUnauthorized, "worktree_claim", "claim principal differs from its registered client", false, "use the authenticated principal")
	}
	member, err := workflowMemberClaimScope(ctx, q, req.WorkID, req.ProjectID, req.Owner)
	if err != nil || member == "" {
		return member, err
	}
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT g.base_subject FROM workflow_member_groups g JOIN workflow_group_members m USING(work_id,group_id) WHERE m.work_id=? AND m.project_id=? AND m.member_id=?`, req.WorkID, req.ProjectID, member).Scan(&raw); err != nil {
		return "", err
	}
	var base workflowMemberBase
	if err := json.Unmarshal([]byte(raw), &base); err != nil {
		return "", err
	}
	if base.Commit != req.BaseSHA {
		return "", memberGroupFailure("member claim base differs from the group's qualified integration base")
	}
	return member, nil
}

// Confirmed member authority is written only by the normal created-claim
// effect. This read keeps retained restriction independent of claim lifetime.
func memberCallerInSession(ctx context.Context, q queryer, session string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_member_callers m JOIN workflow_actors a ON a.actor_ref=m.actor_ref WHERE a.session_ref=?)`, session).Scan(&exists)
	return exists, err
}

// Landing is a host-only session route. Even without a full owner tuple, its
// session must keep every retained member restriction before occupancy moves.
func authorizeMemberClaimLanding(ctx context.Context, q queryer, session, workID, projectID, memberID string) error {
	owner := sessionWorktreeOwnerFromContext(ctx)
	if owner != (SessionWorktreeOwner{}) {
		if owner.SessionRef != session {
			return newFailure(KindUnauthorized, "claim-landing", "landing session differs from its authenticated caller", false, "use the original authenticated session")
		}
		if _, err := worktreeMemberScopeFromContext(ctx, q, workID, projectID); err != nil {
			return err
		}
	}
	var foreign bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workflow_member_callers m JOIN workflow_actors a ON a.actor_ref=m.actor_ref WHERE a.session_ref=? AND (m.work_id<>? OR m.project_id<>? OR m.member_id<>?))`, session, workID, projectID, memberID).Scan(&foreign); err != nil {
		return err
	}
	if foreign {
		return newFailure(KindUnauthorized, "claim-landing", "member session cannot land outside its retained claim scope", false, "land in the recorded member worktree")
	}
	return nil
}

// Read-only helper for the host bootstrap route, which has only a session
// reference and cannot authenticate a member's complete registered tuple.
func (s *Store) RequireUnrestrictedBootstrapSession(ctx context.Context, session string) error {
	exists, err := memberCallerInSession(ctx, s.db, session)
	if err != nil {
		return err
	}
	if exists {
		return newFailure(KindUnauthorized, "work_resume", "a member session must resume through its authenticated claim boundary", false, "use worktree_claim as the bound member caller")
	}
	return nil
}
