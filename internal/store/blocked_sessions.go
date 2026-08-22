package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// BlockedSession resolves active approval challenges to the session,
// agent, worktree, and consequence that the operator's attention should
// route to (issue #72). The projection reads only existing tables and
// indexes: agent_approval_challenges joined to agent_grants. Identity and
// consequence class are surfaced; grant material (tokens, hashes, keys) never
// is. Revoked, consumed, and expired challenges are excluded — expiry is
// re-evaluated at read time so a stale row can never present as blocked.
type BlockedSession struct {
	SessionRef   string `json:"session_ref"`
	AgentRef     string `json:"agent_ref"`
	Worktree     string `json:"worktree"`
	Directory    string `json:"directory"`
	Consequence  string `json:"consequence"`
	BlockedSince string `json:"blocked_since"`
	BlockAgeSec  int64  `json:"block_age_seconds"`
}

// BlockedSessionsResult carries the bounded page with its read metadata.
type BlockedSessionsResult struct {
	ResultMeta
	Sessions []BlockedSession `json:"sessions"`
}

// BlockedSessions lists sessions with active approval challenges, oldest
// block first, bounded by limit (<=100). Product scoping follows the grant's
// resolved Product scope: when products is non-empty, only grants whose
// product_scope_json intersects it are listed. An empty products list lists
// every Product — this projection exists precisely because concurrent
// sessions may span Products, and C18 §12 AR11 constrains result sets by
// Product context, not operator attention.
func (s *Store) BlockedSessions(ctx context.Context, now time.Time, products []string, limit int) (BlockedSessionsResult, error) {
	if s == nil || s.db == nil {
		return BlockedSessionsResult{}, newFailure(KindUnavailable, "blocked_sessions", "store is not open", false, "open the authority database")
	}
	// The projection issues one session query plus a grant lookup per session,
	// so it reads inside a transaction: without one, the rows come from
	// separate snapshots and a session can be listed against a grant state
	// that never coexisted with it.
	tx, err := beginRead(ctx, s, "blocked_sessions")
	if err != nil {
		return BlockedSessionsResult{}, err
	}
	defer tx.Rollback()
	out, err := blockedSessionsTx(ctx, tx, now, products, limit)
	if err != nil {
		return BlockedSessionsResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BlockedSessionsResult{}, wrapFailure(KindUnavailable, "blocked_sessions", "cannot close the read", true, "retry once the database is readable", err)
	}
	return out, nil
}

// blockedSessionsTx is the read core on a caller's transaction: the
// Product-row query embeds it inside its own read transaction, and a second
// connection would deadlock on SQLite's single writer.
func blockedSessionsTx(ctx context.Context, tx *sql.Tx, now time.Time, products []string, limit int) (BlockedSessionsResult, error) {
	return blockedSessionsCore(ctx, tx, now, products, limit)
}

func blockedSessionsCore(ctx context.Context, q queryer, now time.Time, products []string, limit int) (BlockedSessionsResult, error) {
	var out BlockedSessionsResult
	out.Sessions = []BlockedSession{}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nowText := now.UTC().Format(time.RFC3339Nano)

	rows, err := q.QueryContext(ctx, `
		SELECT g.session_ref, g.agent_ref, g.worktree, g.directory, c.consequence, c.issued_at
		FROM agent_approval_challenges c
		JOIN agent_grants g ON g.grant_ref = c.grant_ref
		WHERE c.status = 'active' AND c.expires_at > ?
		ORDER BY c.issued_at ASC
		LIMIT ?`, nowText, limit)
	if err != nil {
		return out, wrapFailure(KindUnavailable, "blocked_sessions", "cannot read blocked sessions", true, "retry once the database is readable", err)
	}
	defer rows.Close()

	want := map[string]bool{}
	for _, product := range products {
		want[product] = true
	}
	for rows.Next() {
		var session BlockedSession
		if err := rows.Scan(&session.SessionRef, &session.AgentRef, &session.Worktree, &session.Directory, &session.Consequence, &session.BlockedSince); err != nil {
			return out, wrapFailure(KindUnavailable, "blocked_sessions", "cannot decode blocked session", true, "retry once the database is readable", err)
		}
		out.Sessions = append(out.Sessions, session)
	}
	if err := rows.Err(); err != nil {
		return out, wrapFailure(KindUnavailable, "blocked_sessions", "cannot enumerate blocked sessions", true, "retry once the database is readable", err)
	}

	// Product filtering happens after the join because product scope lives
	// as JSON on the grant row; the grant set is small and bounded by the
	// active-grant horizon.
	if len(want) > 0 {
		filtered := out.Sessions[:0]
		for _, session := range out.Sessions {
			var grantProducts []string
			// Re-read the grant's product scope through the same bounded
			// result: one extra query per session is bounded by the page.
			var productJSON string
			if err := q.QueryRowContext(ctx, `SELECT product_scope_json FROM agent_grants WHERE session_ref=? AND agent_ref=? AND worktree=? ORDER BY issued_at DESC LIMIT 1`, session.SessionRef, session.AgentRef, session.Worktree).Scan(&productJSON); err != nil {
				return out, wrapFailure(KindUnavailable, "blocked_sessions", "cannot read grant scope", true, "retry once the database is readable", err)
			}
			if json.Unmarshal([]byte(productJSON), &grantProducts) != nil {
				continue
			}
			for _, product := range grantProducts {
				if want[product] {
					filtered = append(filtered, session)
					break
				}
			}
		}
		out.Sessions = filtered
	}

	// Age is computed at read time against the caller's clock.
	for i := range out.Sessions {
		if issued, err := time.Parse(time.RFC3339Nano, out.Sessions[i].BlockedSince); err == nil {
			out.Sessions[i].BlockAgeSec = int64(now.Sub(issued).Seconds())
			if out.Sessions[i].BlockAgeSec < 0 {
				out.Sessions[i].BlockAgeSec = 0
			}
		}
	}
	out.ResultMeta = ResultMeta{QueryID: "PM1.Q12", ContractVersion: "PM1/1.0", Authority: "authoritative", Freshness: Freshness{ObservedAt: now.UTC().Format(time.RFC3339Nano)}, OrderingKeys: []string{"issued_at"}}
	return out, nil
}
