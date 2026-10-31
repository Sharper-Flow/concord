package store

import (
	"context"
	"slices"
)

func (s *Store) CheckDefaultCheckoutRemoval(ctx context.Context, req WorkRemovalRequest, project string) error {
	return checkDefaultCheckoutRemovalQ(ctx, s.db, req, project)
}

func CheckDefaultCheckoutRemovalTx(ctx context.Context, transaction *Transaction, req WorkRemovalRequest, project string) error {
	q, err := transactionSQL(transaction, "work_removal")
	if err != nil {
		return err
	}
	return checkDefaultCheckoutRemovalQ(ctx, q, req, project)
}

func checkDefaultCheckoutRemovalQ(ctx context.Context, q queryer, req WorkRemovalRequest, project string) error {
	if err := validateRemovalRequest(req); err != nil {
		return err
	}
	refuse := func(detail string) error {
		return newFailure(KindUnauthorized, "work_removal", detail, false, "cancel the item and reconcile its scope before shelving from the default checkout")
	}
	if req.Reason != "shelved" || project == "" || req.ProductID == "" {
		return refuse("default-checkout removal requires shelving in one selected Product and resolved Project")
	}
	rows, err := q.QueryContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? ORDER BY project_id`, req.WorkID)
	if err != nil {
		return err
	}
	var projects []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		projects = append(projects, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	products, err := productsForWorkIDs(ctx, q, []string{req.WorkID})
	if err != nil {
		return err
	}
	if !slices.Equal(projects, []string{project}) || !slices.Equal(products[req.WorkID], []string{req.ProductID}) {
		return refuse("default-checkout shelving requires the caller's sole Project and selected sole Product")
	}
	var lifecycle string
	if err := q.QueryRowContext(ctx, `SELECT lifecycle FROM work_items WHERE id=?`, req.WorkID).Scan(&lifecycle); err != nil {
		return err
	}
	var uncancelled int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM workflow_instances WHERE work_id=? AND instance_state<>'cancelled'`, req.WorkID).Scan(&uncancelled); err != nil {
		return err
	}
	if lifecycle != "cancelled" || uncancelled != 0 {
		return refuse("default-checkout shelving requires cancelled lifecycle and workflow")
	}
	if err := validateRemovalDestinationQ(ctx, q, req); err != nil {
		return err
	}
	if req.Linear != nil && req.Linear.HandoffDigest != removalHandoffDigest(req.Handoff) {
		return refuse("Linear confirmation does not match the handoff")
	}
	return validateRemovalGatesQ(ctx, q, req)
}
