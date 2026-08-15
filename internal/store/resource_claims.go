package store

import (
	"context"
	"database/sql"
	"regexp"
	"time"
)

// CD-0028: a resource claim is a durable record of intent, not a lock.
// Concurrent agents may hold legitimate reasons to touch the same external
// thing; the claim makes that intent legible to every other agent before it
// acts, and records holder and reason in terms another session can act on.
// Claims never grant authority, never enforce, and never promise exclusivity
// against foreign actors: a deployment pipeline can still mutate a claimed
// resource, and the claim record does not imply it could not.

const (
	ResourceClaimHeld     = "held"
	ResourceClaimReleased = "released"
)

// resourceKeyPattern requires a typed prefix before a colon, so claims form a
// typed namespace (fence:prod-pause, db:analytics, slot:eu-deploy) rather
// than an informal lock namespace of arbitrary strings.
var resourceKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}:[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// ResourceClaim is the folded projection of one claim on one resource key.
type ResourceClaim struct {
	ResourceKey   string `json:"resource_key"`
	HolderWorkID  string `json:"holder_work_id"`
	HolderAgent   string `json:"holder_agent"`
	HolderSession string `json:"holder_session"`
	Reason        string `json:"reason"`
	State         string `json:"state"`
	ClaimedAt     string `json:"claimed_at"`
	ReleasedAt    string `json:"released_at,omitempty"`
}

type resourceClaimedPayload struct {
	WorkflowVersionFields
	ResourceKey   string `json:"resource_key"`
	Reason        string `json:"reason"`
	HolderAgent   string `json:"holder_agent"`
	HolderSession string `json:"holder_session"`
}

type resourceClaimReleasedPayload struct {
	WorkflowVersionFields
	ResourceKey string `json:"resource_key"`
}

func foldResourceClaimed(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p resourceClaimedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if !resourceKeyPattern.MatchString(p.ResourceKey) {
		return newFailure(KindInvalidPayload, "fold_event", "resource key is not a typed bounded identifier", false, "claim with a typed resource key like fence:prod-pause")
	}
	if p.Reason == "" || len(p.Reason) > 512 {
		return newFailure(KindInvalidPayload, "fold_event", "claim reason must be a bounded non-empty string", false, "state why the resource is held")
	}
	if p.ExpectedVersion == nil || p.ResultingVersion == nil || *p.ResultingVersion != *p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "resource claim version must advance by exactly one", false, "supply expected and resulting versions one apart")
	}
	// One held claim per resource key: the second claimant sees who holds and
	// why, and must coordinate — the collision is refused, not merged.
	var holder string
	err := tx.QueryRowContext(ctx, `SELECT holder_work_id FROM resource_claims WHERE resource_key=? AND state=?`, p.ResourceKey, ResourceClaimHeld).Scan(&holder)
	switch {
	case err == nil:
		if holder != event.SubjectID {
			return newFailure(KindResourceClaimHeld, "fold_event", "resource is already claimed by another work item", false, "coordinate with the holding work item or release its claim first")
		}
		// Same holder re-claiming is an idempotent replay.
		return bumpVersion(ctx, tx, "work_items", event, *p.ExpectedVersion, *p.ResultingVersion, "work item")
	case err != sql.ErrNoRows:
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_claims(resource_key,holder_work_id,holder_agent,holder_session,reason,state,claimed_at) VALUES(?,?,?,?,?,'held',?)
		ON CONFLICT(resource_key) DO UPDATE SET holder_work_id=excluded.holder_work_id, holder_agent=excluded.holder_agent, holder_session=excluded.holder_session, reason=excluded.reason, state='held', claimed_at=excluded.claimed_at, released_at=NULL`,
		p.ResourceKey, event.SubjectID, p.HolderAgent, p.HolderSession, p.Reason, event.OccurredAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "work_items", event, *p.ExpectedVersion, *p.ResultingVersion, "work item")
}

func foldResourceClaimReleased(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var p resourceClaimReleasedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if !resourceKeyPattern.MatchString(p.ResourceKey) {
		return newFailure(KindInvalidPayload, "fold_event", "resource key is not a typed bounded identifier", false, "release with the typed resource key")
	}
	if p.ExpectedVersion == nil || p.ResultingVersion == nil || *p.ResultingVersion != *p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "resource release version must advance by exactly one", false, "supply expected and resulting versions one apart")
	}
	res, err := tx.ExecContext(ctx, `UPDATE resource_claims SET state='released', released_at=? WHERE resource_key=? AND holder_work_id=? AND state='held'`,
		event.OccurredAt.Format(time.RFC3339Nano), p.ResourceKey, event.SubjectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "no held claim on that resource by this work item", false, "claim the resource before releasing it")
	}
	return bumpVersion(ctx, tx, "work_items", event, *p.ExpectedVersion, *p.ResultingVersion, "work item")
}

// foldTerminalReleasesResourceClaims: a terminal work item cannot hold
// claims — the crash-or-completion case cannot deadlock a resource. Any
// terminal transition releases everything the work still holds.
func foldTerminalReleasesResourceClaims(ctx context.Context, tx *sql.Tx, event Event) error {
	_, err := tx.ExecContext(ctx, `UPDATE resource_claims SET state='released', released_at=? WHERE holder_work_id=? AND state='held'`,
		event.OccurredAt.Format(time.RFC3339Nano), event.SubjectID)
	return err
}

// ResourceClaims lists claims in state, bounded. Exact resource_key returns
// the single claim for that key (any state); otherwise the ambient listing
// is scoped to the holder work's Product memberships.
func (s *Store) ResourceClaims(ctx context.Context, resourceKey string, productID string, limit int) ([]ResourceClaim, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "resource_claims", "store is not open", false, "open the authority database")
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if resourceKey != "" {
		if !resourceKeyPattern.MatchString(resourceKey) {
			return nil, newFailure(KindInvalidPayload, "resource_claims", "resource key is not a typed bounded identifier", false, "look up a typed resource key")
		}
		rows, err = s.db.QueryContext(ctx, `SELECT resource_key,holder_work_id,holder_agent,holder_session,reason,state,claimed_at,coalesce(released_at,'') FROM resource_claims WHERE resource_key=?`, resourceKey)
	} else if productID != "" {
		rows, err = s.db.QueryContext(ctx, `SELECT rc.resource_key,rc.holder_work_id,rc.holder_agent,rc.holder_session,rc.reason,rc.state,rc.claimed_at,coalesce(rc.released_at,'') FROM resource_claims rc JOIN work_projects wp ON wp.work_id=rc.holder_work_id JOIN product_projects pp ON pp.project_id=wp.project_id WHERE pp.product_id=? ORDER BY rc.claimed_at DESC LIMIT ?`, productID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, `SELECT resource_key,holder_work_id,holder_agent,holder_session,reason,state,claimed_at,coalesce(released_at,'') FROM resource_claims WHERE state='held' ORDER BY claimed_at DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "resource_claims", "cannot read resource claims", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	out := []ResourceClaim{}
	for rows.Next() {
		var c ResourceClaim
		if err := rows.Scan(&c.ResourceKey, &c.HolderWorkID, &c.HolderAgent, &c.HolderSession, &c.Reason, &c.State, &c.ClaimedAt, &c.ReleasedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
