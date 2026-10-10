package store

import (
	"context"
	"database/sql"
	"net/url"
	"regexp"
	"time"
)

// CD-0213 D3: a managed work unit carries at most one Linear issue identity,
// the key, UUID, and URL the agent read from the Linear MCP server. Concord
// stores the identity as reported and makes no Linear call. The table is
// direct authority: it holds no foreign key, so a rebuild from the event log
// leaves it untouched.

// LinearIssueLink is one work item's recorded Linear issue identity.
type LinearIssueLink struct {
	WorkID          string `json:"work_id"`
	RemoteIssueUUID string `json:"remote_issue_uuid"`
	HumanKey        string `json:"human_key"`
	URL             string `json:"url"`
}

// linearIssueKeyPattern is the Linear issue key shape: a team key, a hyphen,
// and the issue number.
var linearIssueKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*$`)

// ReadLinearLink returns one work item's recorded identity, refusing when none
// exists.
func (s *Store) ReadLinearLink(ctx context.Context, workID string) (LinearIssueLink, error) {
	link, found, err := readLinearLinkCore(ctx, s.db, workID)
	if err != nil {
		return LinearIssueLink{}, err
	}
	if !found {
		return LinearIssueLink{}, newFailure(KindUnknownScope, "linear_link_read", "no Linear issue identity is recorded for the work item", false, "record the identity with concord_work_define.issue_link_record")
	}
	return link, nil
}

func readLinearLinkCore(ctx context.Context, q queryer, workID string) (LinearIssueLink, bool, error) {
	var link LinearIssueLink
	err := q.QueryRowContext(ctx, `SELECT work_id, remote_issue_uuid, human_key, url FROM linear_issue_links WHERE work_id=?`, workID).Scan(&link.WorkID, &link.RemoteIssueUUID, &link.HumanKey, &link.URL)
	if err == sql.ErrNoRows {
		return LinearIssueLink{}, false, nil
	} else if err != nil {
		return LinearIssueLink{}, false, wrapFailure(KindUnavailable, "linear_link_read", "cannot read the Linear issue identity", true, "retry once the database is readable", err)
	}
	return link, true, nil
}

// validateLinearIssueLink refuses an identity that is not a bounded Linear
// issue key, UUID, and HTTPS URL.
func validateLinearIssueLink(link LinearIssueLink) error {
	const op = "linear_link_record"
	if len(link.WorkID) < 2 || len(link.WorkID) > 128 {
		return newFailure(KindInvalidPayload, op, "work id must be 2 to 128 characters", false, "supply a bounded work id")
	}
	if len(link.RemoteIssueUUID) < 2 || len(link.RemoteIssueUUID) > 128 {
		return newFailure(KindInvalidPayload, op, "remote issue uuid must be 2 to 128 characters", false, "supply the issue UUID the Linear MCP server reported")
	}
	if len(link.HumanKey) > 64 || !linearIssueKeyPattern.MatchString(link.HumanKey) {
		return newFailure(KindInvalidPayload, op, "issue key must be a Linear issue key such as CON-801", false, "supply the issue key the Linear MCP server reported")
	}
	parsed, err := url.Parse(link.URL)
	if len(link.URL) > 2048 || err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return newFailure(KindInvalidPayload, op, "issue url must be an https URL without credentials", false, "supply the issue URL the Linear MCP server reported")
	}
	return nil
}

// RecordLinearIssueLink records one work item's Linear issue identity in its
// own transaction.
func (s *Store) RecordLinearIssueLink(ctx context.Context, link LinearIssueLink) (LinearIssueLink, error) {
	var recorded LinearIssueLink
	err := s.Transact(ctx, func(transaction *Transaction) error {
		return recordLinearIssueLinkResultTx(ctx, transaction, link, &recorded)
	})
	return recorded, err
}

func recordLinearIssueLinkResultTx(ctx context.Context, transaction *Transaction, link LinearIssueLink, recorded *LinearIssueLink) error {
	result, err := RecordLinearIssueLinkTx(ctx, transaction, link)
	if err == nil {
		*recorded = result
	}
	return err
}

// RecordLinearIssueLinkTx records one work item's Linear issue identity inside
// the caller's transaction. Recording the identity the work item already holds
// is a no-op. A different identity for the same work item, or the same issue
// UUID on another work item, refuses (CD-0213 D3).
func RecordLinearIssueLinkTx(ctx context.Context, transaction *Transaction, link LinearIssueLink) (LinearIssueLink, error) {
	const op = "linear_link_record"
	if err := validateLinearIssueLink(link); err != nil {
		return LinearIssueLink{}, err
	}
	tx, err := transactionSQL(transaction, op)
	if err != nil {
		return LinearIssueLink{}, err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM work_items WHERE id=?`, link.WorkID).Scan(&exists); err == sql.ErrNoRows {
		return LinearIssueLink{}, newFailure(KindUnknownScope, op, "work item does not exist", false, "supply an existing work item")
	} else if err != nil {
		return LinearIssueLink{}, wrapFailure(KindUnavailable, op, "cannot read work item", true, "retry once the database is readable", err)
	}
	if err := refuseRemovedWorkTx(ctx, tx, link.WorkID, op); err != nil {
		return LinearIssueLink{}, err
	}
	current, found, err := readLinearLinkCore(ctx, tx, link.WorkID)
	if err != nil {
		return LinearIssueLink{}, err
	}
	if found {
		if current == link {
			return current, nil
		}
		return LinearIssueLink{}, newFailure(KindInvalidOperation, op, "the work item already records a different Linear issue identity", false, "a work item holds at most one Linear issue identity")
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT work_id FROM linear_issue_links WHERE remote_issue_uuid=?`, link.RemoteIssueUUID).Scan(&owner); err == nil {
		return LinearIssueLink{}, newFailure(KindInvalidRelation, op, "another work item already records that Linear issue", false, "record an issue no other work item records")
	} else if err != sql.ErrNoRows {
		return LinearIssueLink{}, wrapFailure(KindUnavailable, op, "cannot inspect recorded Linear issue identities", true, "retry once the database is readable", err)
	}
	scope := transaction.fold
	if scope == nil {
		scope = newFoldScope(tx)
	}
	if err := scope.enter(ctx); err != nil {
		return LinearIssueLink{}, err
	}
	now := transaction.now().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO linear_issue_links(work_id, remote_issue_uuid, human_key, url, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
		link.WorkID, link.RemoteIssueUUID, link.HumanKey, link.URL, now, now); err != nil {
		return LinearIssueLink{}, wrapFailure(KindUnavailable, op, "cannot record the Linear issue identity", true, "retry once the database is writable", err)
	}
	if err := scope.close(ctx); err != nil {
		return LinearIssueLink{}, err
	}
	return link, nil
}
