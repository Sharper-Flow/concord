package store

import (
	"context"
	"database/sql"
	"net/url"
	"regexp"
	"time"
)

// A managed work unit carries at most one Linear issue identity,
// the key, UUID, and URL the agent read from the Linear MCP server. Concord
// stores the identity as reported and makes no Linear call. The table is
// direct authority and holds no foreign key.

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

// RecordLinearIssueLinkTx records one work item's Linear issue identity inside
// the caller's transaction. Recording the identity the work item already holds
// is a no-op. A different identity for the same work item refuses.
// One live work item per Linear issue: a key or issue UUID another LIVE work
// item's link holds refuses, while a terminal item's recorded identity never
// blocks a fresh capture.
func RecordLinearIssueLinkTx(ctx context.Context, transaction *Transaction, link LinearIssueLink) (LinearIssueLink, error) {
	const op = "linear_link_record"
	if err := validateLinearIssueLink(link); err != nil {
		return LinearIssueLink{}, err
	}
	tx, err := transactionSQL(transaction, op)
	if err != nil {
		return LinearIssueLink{}, err
	}
	var lifecycle string
	if err := tx.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, link.WorkID).Scan(&lifecycle); err == sql.ErrNoRows {
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
	if !isTerminalLifecycle(lifecycle) {
		if err := requireAvailableLinearIssue(ctx, tx, link); err != nil {
			return LinearIssueLink{}, err
		}
	}
	scope := transaction.write
	if scope == nil {
		scope = newWriteScope(tx)
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

func requireAvailableLinearIssue(ctx context.Context, q queryer, link LinearIssueLink) error {
	const op = "linear_link_record"
	for _, probe := range []struct {
		sql   string
		value string
		what  string
	}{
		{`SELECT l.work_id FROM linear_issue_links l JOIN work_items w ON w.id=l.work_id WHERE l.remote_issue_uuid=? AND w.lifecycle IN ('needed','in_progress') AND l.work_id<>? LIMIT 1`, link.RemoteIssueUUID, "issue UUID"},
		{`SELECT l.work_id FROM linear_issue_links l JOIN work_items w ON w.id=l.work_id WHERE l.human_key=? AND w.lifecycle IN ('needed','in_progress') AND l.work_id<>? LIMIT 1`, link.HumanKey, "issue key"},
	} {
		var owner string
		err := q.QueryRowContext(ctx, probe.sql, probe.value, link.WorkID).Scan(&owner)
		if err == nil {
			var shared bool
			err = q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM work_projects holder JOIN product_projects hp ON hp.project_id=holder.project_id JOIN product_projects lp ON lp.product_id=hp.product_id JOIN work_projects linked ON linked.project_id=lp.project_id WHERE holder.work_id=? AND linked.work_id=?)`, owner, link.WorkID).Scan(&shared)
			if err != nil {
				return wrapFailure(KindUnavailable, op, "cannot inspect work Product scope", true, "retry once the database is readable", err)
			}
			message := "another live work item already holds that Linear " + probe.what
			// A holder outside the linked item's Products is outside the caller's readable scope, so its work ID stays undisclosed.
			if shared {
				message += ": " + owner
			}
			return newFailure(KindInvalidRelation, op, message, false, "one live work item per Linear issue; capture again once the holding item is terminal")
		}
		if err != sql.ErrNoRows {
			return wrapFailure(KindUnavailable, op, "cannot inspect recorded Linear issue identities", true, "retry once the database is readable", err)
		}
	}
	return nil
}
