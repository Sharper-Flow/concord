package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type membershipPayload struct {
	ProductID        string `json:"product_id"`
	WorkID           string `json:"work_id"`
	ProjectID        string `json:"project_id"`
	Role             string `json:"role"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

// ProjectMembership is a typed membership read with the Project's display
// metadata. Role is intentionally read-only edge metadata.
type ProjectMembership struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// ProductMembership is a typed membership read with the Product's display
// metadata. Role is intentionally read-only edge metadata.
type ProductMembership struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// ProductScope is the derived Product set for one canonical work item.
type ProductScope struct {
	Products     []ProductMembership
	CrossProduct bool
}

func validateMembershipPayload(event Event, payload membershipPayload, subjectField, subjectID string) error {
	if payload.ProjectID == "" || payload.Role == "" || payload.Reason == "" {
		return newFailure(KindInvalidPayload, "fold_event", "membership payload requires project_id, role, and reason", false,
			"supply a valid role and non-empty reason")
	}
	if payload.Role != "primary" && payload.Role != "secondary" {
		return newFailure(KindInvalidPayload, "fold_event", "membership role is not recognized", false,
			"use primary or secondary")
	}
	var got string
	switch subjectField {
	case "product_id":
		got = payload.ProductID
	case "work_id":
		got = payload.WorkID
	default:
		return newFailure(KindInvalidOperation, "fold_event", "unknown membership subject field", false,
			"use product_id or work_id")
	}
	if got != subjectID {
		return newFailure(KindInvalidPayload, "fold_event", "membership subject does not match its event subject", false,
			"use the event subject as the membership owner")
	}
	if payload.ExpectedVersion < 1 || payload.ResultingVersion != payload.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "membership version evidence is not consecutive", false,
			"supply expected_version and resulting_version with resulting_version one greater")
	}
	return nil
}

func foldProductProjectAdded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload membershipPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if err := validateMembershipPayload(event, payload, "product_id", event.SubjectID); err != nil {
		return err
	}
	if err := insertProductProject(ctx, tx, payload); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "products", event, payload.ExpectedVersion, payload.ResultingVersion, "Product")
}

func foldProductProjectRemoved(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload membershipPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if err := validateMembershipPayload(event, payload, "product_id", event.SubjectID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM product_projects WHERE product_id = ? AND project_id = ? AND role = ?`,
		payload.ProductID, payload.ProjectID, payload.Role)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove Product membership", true,
			"retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify Product membership removal", true,
			"retry once the database is readable", err)
	} else if affected == 0 {
		return newFailure(KindMembershipConflict, "fold_event", "Product membership does not exist with that role", false,
			"reload the Product memberships and remove the stored role")
	}
	return bumpVersion(ctx, tx, "products", event, payload.ExpectedVersion, payload.ResultingVersion, "Product")
}

func foldProductProjectRoleChanged(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var payload membershipPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if err := validateMembershipPayload(event, payload, "product_id", event.SubjectID); err != nil {
		return err
	}
	var oldRole string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM product_projects WHERE product_id = ? AND project_id = ?`,
		payload.ProductID, payload.ProjectID).Scan(&oldRole); err == sql.ErrNoRows {
		return newFailure(KindMembershipConflict, "fold_event", "Product membership does not exist", false,
			"add the membership before changing its role")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read Product membership", true,
			"retry once the database is readable", err)
	}
	if oldRole == payload.Role {
		return newFailure(KindMembershipConflict, "fold_event", "Product membership already has that role", false,
			"request a different role")
	}
	if payload.Role == "primary" {
		if _, err := tx.ExecContext(ctx, `UPDATE product_projects SET role = 'secondary' WHERE product_id = ? AND role = 'primary' AND project_id <> ?`,
			payload.ProductID, payload.ProjectID); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot demote the existing Product primary", true,
				"retry once the database is writable", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE product_projects SET role = ? WHERE product_id = ? AND project_id = ?`,
		payload.Role, payload.ProductID, payload.ProjectID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot change Product membership role", true,
			"retry once the database is writable", err)
	}
	return bumpVersion(ctx, tx, "products", event, payload.ExpectedVersion, payload.ResultingVersion, "Product")
}

func foldWorkProjectAdded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload membershipPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if err := validateMembershipPayload(event, payload, "work_id", event.SubjectID); err != nil {
		return err
	}
	if err := insertWorkProject(ctx, tx, payload); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "work_items", event, payload.ExpectedVersion, payload.ResultingVersion, "work item")
}

func foldWorkProjectRemoved(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload membershipPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if err := validateMembershipPayload(event, payload, "work_id", event.SubjectID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM work_projects WHERE work_id = ? AND project_id = ? AND role = ?`,
		payload.WorkID, payload.ProjectID, payload.Role)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove work membership", true,
			"retry once the database is writable", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify work membership removal", true,
			"retry once the database is readable", err)
	}
	if affected == 0 {
		return newFailure(KindMembershipConflict, "fold_event", "work membership does not exist with that role", false,
			"reload the work memberships and remove the stored role")
	}
	return bumpVersion(ctx, tx, "work_items", event, payload.ExpectedVersion, payload.ResultingVersion, "work item")
}

func foldWorkProjectRoleChanged(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectWorkItem); err != nil {
		return err
	}
	var payload membershipPayload
	if err := decodePayload(event, &payload); err != nil {
		return err
	}
	if err := validateMembershipPayload(event, payload, "work_id", event.SubjectID); err != nil {
		return err
	}
	var oldRole string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM work_projects WHERE work_id = ? AND project_id = ?`,
		payload.WorkID, payload.ProjectID).Scan(&oldRole); err == sql.ErrNoRows {
		return newFailure(KindMembershipConflict, "fold_event", "work membership does not exist", false,
			"add the membership before changing its role")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read work membership", true,
			"retry once the database is readable", err)
	}
	if oldRole == payload.Role {
		return newFailure(KindMembershipConflict, "fold_event", "work membership already has that role", false,
			"request a different role")
	}
	if payload.Role == "primary" {
		if _, err := tx.ExecContext(ctx, `UPDATE work_projects SET role = 'secondary' WHERE work_id = ? AND role = 'primary' AND project_id <> ?`,
			payload.WorkID, payload.ProjectID); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot demote the existing work primary", true,
				"retry once the database is writable", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_projects SET role = ? WHERE work_id = ? AND project_id = ?`,
		payload.Role, payload.WorkID, payload.ProjectID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot change work membership role", true,
			"retry once the database is writable", err)
	}
	return bumpVersion(ctx, tx, "work_items", event, payload.ExpectedVersion, payload.ResultingVersion, "work item")
}

func insertProductProject(ctx context.Context, tx *sql.Tx, payload membershipPayload) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO product_projects(product_id, project_id, role) VALUES (?, ?, ?)`,
		payload.ProductID, payload.ProjectID, payload.Role)
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return newFailure(KindMembershipConflict, "fold_event", "Product membership already exists or another primary is present", false,
			"remove the duplicate or use product_project.role_changed to promote a Project")
	}
	if isForeignKeyViolation(err) {
		return newFailure(KindProjectionNotFound, "fold_event", "Product or Project membership endpoint does not exist", false,
			"create both endpoints before adding membership")
	}
	return wrapFailure(KindUnavailable, "fold_event", "cannot add Product membership", true,
		"retry once the database is writable", err)
}

func insertWorkProject(ctx context.Context, tx *sql.Tx, payload membershipPayload) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO work_projects(work_id, project_id, role) VALUES (?, ?, ?)`,
		payload.WorkID, payload.ProjectID, payload.Role)
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return newFailure(KindMembershipConflict, "fold_event", "work membership already exists or another primary is present", false,
			"remove the duplicate or use work_project.role_changed to promote a Project")
	}
	if isForeignKeyViolation(err) {
		return newFailure(KindProjectionNotFound, "fold_event", "work or Project membership endpoint does not exist", false,
			"create the work and Project before adding membership")
	}
	return wrapFailure(KindUnavailable, "fold_event", "cannot add work membership", true,
		"retry once the database is writable", err)
}

func bumpVersion(ctx context.Context, tx *sql.Tx, table string, event Event, expected, resulting int64, label string) error {
	var current int64
	if err := tx.QueryRowContext(ctx, "SELECT version FROM "+table+" WHERE id = ?", event.SubjectID).Scan(&current); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", label+" does not exist", false,
			"create the subject before changing membership")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read "+label+" version", true,
			"retry once the database is readable", err)
	}
	if current != expected {
		f := newFailure(KindVersionConflict, "fold_event",
			fmt.Sprintf("%s %s has version %d, want %d", label, event.SubjectID, current, expected), false,
			"reload the subject and retry with its current version")
		// fold_event subjects share the SubjectWorkItem/SubjectProduct/SubjectProject
		// vocabulary; carry the typed current version so the agent layer does not
		// have to parse the human detail string.
		f.CurrentVersions = []SubjectCurrentVersion{{SubjectType: event.SubjectType, SubjectID: event.SubjectID, Version: current, Exists: true}}
		return f
	}
	result, err := tx.ExecContext(ctx, "UPDATE "+table+" SET version = ?, updated_at = ? WHERE id = ? AND version = ?",
		resulting, event.OccurredAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), event.SubjectID, expected)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot update "+label+" version", true,
			"retry once the database is writable", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot verify "+label+" version update", true,
				"retry once the database is readable", err)
		}
		return newFailure(KindProjectionNotFound, "fold_event", label+" changed before its membership update", false,
			"reload the subject before applying membership")
	}
	return nil
}

func validateMembershipInvariants(ctx context.Context, db *sql.DB) error {
	return validateMembershipInvariantQueries(ctx, db)
}

func validateMembershipInvariantsTx(ctx context.Context, tx *sql.Tx) error {
	return validateMembershipInvariantQueries(ctx, tx)
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateMembershipInvariantQueries(ctx context.Context, q queryer) error {
	checks := []struct {
		name  string
		table string
		join  string
	}{
		{"Product", "products", "product_projects.product_id = products.id"},
		{"Project", "projects", "product_projects.project_id = projects.id"},
		{"work item", "work_items", "work_projects.work_id = work_items.id"},
	}
	for _, check := range checks {
		var id string
		query := fmt.Sprintf("SELECT %s.id FROM %s WHERE NOT EXISTS (SELECT 1 FROM %s WHERE %s) LIMIT 1",
			check.table, check.table,
			map[string]string{"products": "product_projects", "projects": "product_projects", "work_items": "work_projects"}[check.table], check.join)
		if err := q.QueryRowContext(ctx, query).Scan(&id); err == sql.ErrNoRows {
			continue
		} else if err != nil {
			return wrapFailure(KindUnavailable, "membership_invariants", "cannot validate "+check.name+" memberships", true,
				"retry once the database is readable", err)
		}
		return newFailure(KindMembershipInvariant, "membership_invariants",
			fmt.Sprintf("%s %s has no required membership", check.name, id), false,
			"compose creation with its initial membership or repair the database from a valid event log")
	}
	return nil
}

func membershipImpact(ctx context.Context, tx *sql.Tx, operation Operation) (MembershipImpact, error) {
	impact := MembershipImpact{}
	projectIDs := make(map[string]struct{})
	for _, event := range operation.Events {
		if !strings.HasPrefix(event.Kind, "product_project.") {
			continue
		}
		var payload membershipPayload
		if jsonErr := decodePayload(event, &payload); jsonErr != nil {
			continue
		}
		projectIDs[payload.ProjectID] = struct{}{}
		impact.EventIDs = append(impact.EventIDs, event.EventID)
	}
	if len(projectIDs) == 0 {
		return impact, nil
	}
	args := make([]any, 0, len(projectIDs))
	placeholders := make([]string, 0, len(projectIDs))
	for projectID := range projectIDs {
		placeholders = append(placeholders, "?")
		args = append(args, projectID)
	}
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT work_id FROM work_projects WHERE project_id IN ("+strings.Join(placeholders, ",")+") ORDER BY work_id", args...)
	if err != nil {
		return impact, wrapFailure(KindUnavailable, "apply_operation", "cannot compute membership impact", true,
			"retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return impact, wrapFailure(KindUnavailable, "apply_operation", "cannot decode membership impact", true,
				"retry once the database is readable", err)
		}
		impact.TotalAffectedWorkCount++
		if len(impact.AffectedWorkIDs) < 100 {
			impact.AffectedWorkIDs = append(impact.AffectedWorkIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return impact, wrapFailure(KindUnavailable, "apply_operation", "cannot read membership impact", true,
			"retry once the database is readable", err)
	}
	impact.AffectedWorkCount = impact.TotalAffectedWorkCount
	return impact, nil
}

func (s *Store) ProjectsForProduct(ctx context.Context, productID string) ([]ProjectMembership, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT projects.id, projects.display_name, product_projects.role
		FROM product_projects JOIN projects ON projects.id = product_projects.project_id
		WHERE product_projects.product_id = ?
		ORDER BY CASE product_projects.role WHEN 'primary' THEN 0 ELSE 1 END, projects.display_name, projects.id`, productID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "projects_for_product", "cannot read Product memberships", true,
			"retry once the database is readable", err)
	}
	defer rows.Close()
	var memberships []ProjectMembership
	for rows.Next() {
		var item ProjectMembership
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Role); err != nil {
			return nil, wrapFailure(KindUnavailable, "projects_for_product", "cannot decode Product memberships", true,
				"retry once the database is readable", err)
		}
		memberships = append(memberships, item)
	}
	return memberships, rows.Err()
}

func (s *Store) ProductsForProject(ctx context.Context, projectID string) ([]ProductMembership, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT products.id, products.display_name, product_projects.role
		FROM product_projects JOIN products ON products.id = product_projects.product_id
		WHERE product_projects.project_id = ?
		ORDER BY products.id`, projectID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "products_for_project", "cannot read Project memberships", true,
			"retry once the database is readable", err)
	}
	defer rows.Close()
	var memberships []ProductMembership
	for rows.Next() {
		var item ProductMembership
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Role); err != nil {
			return nil, wrapFailure(KindUnavailable, "products_for_project", "cannot decode Project memberships", true,
				"retry once the database is readable", err)
		}
		memberships = append(memberships, item)
	}
	return memberships, rows.Err()
}

func (s *Store) ProjectsForWork(ctx context.Context, workID string) ([]ProjectMembership, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT projects.id, projects.display_name, work_projects.role
		FROM work_projects JOIN projects ON projects.id = work_projects.project_id
		WHERE work_projects.work_id = ?
		ORDER BY CASE work_projects.role WHEN 'primary' THEN 0 ELSE 1 END, projects.id`, workID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "projects_for_work", "cannot read work memberships", true,
			"retry once the database is readable", err)
	}
	defer rows.Close()
	var memberships []ProjectMembership
	for rows.Next() {
		var item ProjectMembership
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Role); err != nil {
			return nil, wrapFailure(KindUnavailable, "projects_for_work", "cannot decode work memberships", true,
				"retry once the database is readable", err)
		}
		memberships = append(memberships, item)
	}
	return memberships, rows.Err()
}

func (s *Store) ProductsForWork(ctx context.Context, workID string) (ProductScope, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT products.id, products.display_name
		FROM work_projects
		JOIN product_projects ON product_projects.project_id = work_projects.project_id
		JOIN products ON products.id = product_projects.product_id
		WHERE work_projects.work_id = ?
		ORDER BY products.id`, workID)
	if err != nil {
		return ProductScope{}, wrapFailure(KindUnavailable, "products_for_work", "cannot read derived Product scope", true,
			"retry once the database is readable", err)
	}
	defer rows.Close()
	var scope ProductScope
	for rows.Next() {
		var item ProductMembership
		if err := rows.Scan(&item.ID, &item.DisplayName); err != nil {
			return ProductScope{}, wrapFailure(KindUnavailable, "products_for_work", "cannot decode derived Product scope", true,
				"retry once the database is readable", err)
		}
		scope.Products = append(scope.Products, item)
	}
	if err := rows.Err(); err != nil {
		return ProductScope{}, wrapFailure(KindUnavailable, "products_for_work", "cannot read derived Product scope", true,
			"retry once the database is readable", err)
	}
	scope.CrossProduct = len(scope.Products) > 1
	return scope, nil
}
