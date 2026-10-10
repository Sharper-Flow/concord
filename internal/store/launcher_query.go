package store

// This file carries the in-process launcher forwarding reads: the
// landing-Project Product rule the session entry route applies when it
// starts or resumes a session for selected work. It deliberately is not a
// new Product-memory query; each read is one bounded transaction.

import (
	"context"
	"database/sql"
)

// ResolveLauncherWorkProduct selects the Product scope for direct work
// forwarding from the landing Project: the Project the session names, else
// the work's primary Project. The entry route does not write a remembered
// Product to the store. preferredProduct is an inherited selection; the
// landing Project owns the scope, so it is honored only when it is one of
// the landing Project's Products.
func (s *Store) ResolveLauncherWorkProduct(ctx context.Context, workID, projectID, preferredProduct string) (string, error) {
	if workID == "" {
		return "", unknownScope("launcher.forward", "work forwarding requires a work")
	}
	tx, err := beginRead(ctx, s, "launcher.forward")
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	return resolveLandingProjectProductTx(ctx, tx, workID, projectID, preferredProduct)
}

// resolveLandingProjectProductTx applies the landing-Project Product rule
// inside the caller's read transaction: the landing Project is the named
// member Project or the work's primary Project, and its Product set must
// resolve to one Product, or the inherited selection must name one of them.
func resolveLandingProjectProductTx(ctx context.Context, tx *sql.Tx, workID, projectID, preferredProduct string) (string, error) {
	landing := projectID
	if landing == "" {
		err := tx.QueryRowContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&landing)
		if err == sql.ErrNoRows {
			return "", unknownScope("launcher.forward", "work has no primary Project")
		}
		if err != nil {
			return "", wrapFailure(KindUnavailable, "launcher.forward", "cannot read work Project memberships", true, "retry once the database is readable", err)
		}
	} else {
		var members int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM work_projects WHERE work_id=? AND project_id=?`, workID, landing).Scan(&members)
		if err != nil {
			return "", wrapFailure(KindUnavailable, "launcher.forward", "cannot read work Project memberships", true, "retry once the database is readable", err)
		}
		if members == 0 {
			return "", newFailure(KindUnknownScope, "launcher.forward", "work item does not hold Project "+landing, false, "forward from a Project the work item belongs to")
		}
	}
	products, err := landingProjectProductsTx(ctx, tx, landing)
	if err != nil {
		return "", err
	}
	if len(products) == 0 {
		return "", unknownScope("launcher.forward", "landing Project has no Product")
	}
	if len(products) > 1 {
		for _, productID := range products {
			if productID == preferredProduct {
				return productID, nil
			}
		}
		return "", newAmbiguousScopeFailure("launcher.forward", "landing Project spans more than one Product", "name one of the landing Project's Products", products)
	}
	return products[0], nil
}

func landingProjectProductsTx(ctx context.Context, tx *sql.Tx, projectID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT product_id FROM product_projects WHERE project_id=? ORDER BY product_id`, projectID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "launcher.forward", "cannot read Project Products", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var products []string
	for rows.Next() {
		var productID string
		if err := rows.Scan(&productID); err != nil {
			return nil, wrapFailure(KindUnavailable, "launcher.forward", "cannot decode Project Products", true, "retry once the database is readable", err)
		}
		products = append(products, productID)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "launcher.forward", "cannot enumerate Project Products", true, "retry once the database is readable", err)
	}
	return products, nil
}

// LauncherLinearIssue resolves a confirmed Linear issue link without a remote
// read. The session scope follows the same landing-Project Product rule as
// direct work forwarding.
type LauncherLinearIssue struct {
	WorkID    string
	ProductID string
}

// ResolveLauncherLinearIssue resolves a confirmed Linear issue link and its
// Product in one read transaction. projectID names the landing Project
// (empty means the work's primary Project); preferredProduct is honored only
// when it is one of the landing Project's Products.
func (s *Store) ResolveLauncherLinearIssue(ctx context.Context, humanKey, issueURL, projectID, preferredProduct string) (LauncherLinearIssue, error) {
	tx, err := beginRead(ctx, s, "launcher.forward")
	if err != nil {
		return LauncherLinearIssue{}, err
	}
	defer tx.Rollback()
	workID, err := launcherLinkedWorkTx(ctx, tx, humanKey, issueURL)
	if err != nil {
		return LauncherLinearIssue{}, err
	}
	productID, err := resolveLandingProjectProductTx(ctx, tx, workID, projectID, preferredProduct)
	if err != nil {
		return LauncherLinearIssue{}, err
	}
	return LauncherLinearIssue{WorkID: workID, ProductID: productID}, nil
}

// ResolveLauncherLinearIssueWork resolves only the confirmed link, so a
// caller can answer "is this issue linked" separately from the landing-Project
// Product rule. The only unknown-scope refusal it returns is the unlinked
// case.
func (s *Store) ResolveLauncherLinearIssueWork(ctx context.Context, humanKey, issueURL string) (string, error) {
	tx, err := beginRead(ctx, s, "launcher.forward")
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	return launcherLinkedWorkTx(ctx, tx, humanKey, issueURL)
}

func launcherLinkedWorkTx(ctx context.Context, tx *sql.Tx, humanKey, issueURL string) (string, error) {
	var workID string
	err := tx.QueryRowContext(ctx, `SELECT work_id FROM linear_issue_links
		WHERE link_state='confirmed' AND (human_key=? OR url=?)
		ORDER BY work_id LIMIT 1`, humanKey, issueURL).Scan(&workID)
	if err == sql.ErrNoRows {
		return "", unknownScope("launcher.forward", "Linear issue is not linked to a work")
	}
	if err != nil {
		return "", wrapFailure(KindUnavailable, "launcher.forward", "cannot resolve Linear issue link", true, "retry once the database is readable", err)
	}
	return workID, nil
}
