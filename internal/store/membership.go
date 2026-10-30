package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/sharper-flow/concord/internal/store/internal/sqlitedb"
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

func validateMembershipPayload(payload membershipPayload, subjectField, subjectID string) error {
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

// membershipEdge is one subject's Project membership projection: the
// Product–Project edge or the work–Project edge. The added, removed and
// role-changed folds share every step and differ only in these values.
type membershipEdge struct {
	subject        SubjectType
	label          string // names the membership in refusal messages
	table          string // the edge table
	idColumn       string // the edge table's subject column and payload field
	versionTable   string // the subject table whose version each fold bumps
	versionLabel   string // names the subject in version refusals
	roleChangeKind string // the event kind that promotes a Project
	endpointRemedy string // the remedy when an edge endpoint does not exist
}

var (
	productMembershipEdge = membershipEdge{
		subject: SubjectProduct, label: "Product", table: "product_projects", idColumn: "product_id",
		versionTable: "products", versionLabel: "Product", roleChangeKind: "product_project.role_changed",
		endpointRemedy: "create both endpoints before adding membership",
	}
	workMembershipEdge = membershipEdge{
		subject: SubjectWorkItem, label: "work", table: "work_projects", idColumn: "work_id",
		versionTable: "work_items", versionLabel: "work item", roleChangeKind: "work_project.role_changed",
		endpointRemedy: "create the work and Project before adding membership",
	}
)

// payload decodes and validates one membership event for this edge and
// returns it with the edge's subject ID.
func (edge membershipEdge) payload(event Event) (membershipPayload, string, error) {
	var payload membershipPayload
	if err := checkSubject(event, edge.subject); err != nil {
		return payload, "", err
	}
	if err := decodePayload(event, &payload); err != nil {
		return payload, "", err
	}
	if err := validateMembershipPayload(payload, edge.idColumn, event.SubjectID); err != nil {
		return payload, "", err
	}
	return payload, event.SubjectID, nil
}

func (edge membershipEdge) bumpVersion(ctx context.Context, tx *sql.Tx, event Event, payload membershipPayload) error {
	return bumpVersion(ctx, tx, edge.versionTable, event, payload.ExpectedVersion, payload.ResultingVersion, edge.versionLabel)
}

// foldAdded folds product_project.added or work_project.added.
func (edge membershipEdge) foldAdded(ctx context.Context, tx *sql.Tx, event Event) error {
	payload, subjectID, err := edge.payload(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO `+edge.table+`(`+edge.idColumn+`, project_id, role) VALUES (?, ?, ?)`, //nolint:gosec // table and idColumn come from the two closed membershipEdge values and all values stay parameter-bound.
		subjectID, payload.ProjectID, payload.Role)
	switch {
	case err == nil:
	case sqlitedb.IsUniqueViolation(err):
		return newFailure(KindMembershipConflict, "fold_event", edge.label+" membership already exists or another primary is present", false,
			"remove the duplicate or use "+edge.roleChangeKind+" to promote a Project")
	case sqlitedb.IsForeignKeyViolation(err):
		return newFailure(KindProjectionNotFound, "fold_event", edge.label+" or Project membership endpoint does not exist", false,
			edge.endpointRemedy)
	default:
		return wrapFailure(KindUnavailable, "fold_event", "cannot add "+edge.label+" membership", true,
			"retry once the database is writable", err)
	}
	return edge.bumpVersion(ctx, tx, event, payload)
}

// foldRemoved folds product_project.removed or work_project.removed.
func (edge membershipEdge) foldRemoved(ctx context.Context, tx *sql.Tx, event Event) error {
	payload, subjectID, err := edge.payload(event)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM `+edge.table+` WHERE `+edge.idColumn+` = ? AND project_id = ? AND role = ?`, //nolint:gosec // table and idColumn come from the two closed membershipEdge values and all values stay parameter-bound.
		subjectID, payload.ProjectID, payload.Role)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove "+edge.label+" membership", true,
			"retry once the database is writable", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot verify "+edge.label+" membership removal", true,
			"retry once the database is readable", err)
	}
	if affected == 0 {
		return newFailure(KindMembershipConflict, "fold_event", edge.label+" membership does not exist with that role", false,
			"reload the "+edge.label+" memberships and remove the stored role")
	}
	return edge.bumpVersion(ctx, tx, event, payload)
}

// foldRoleChanged folds product_project.role_changed or
// work_project.role_changed: read the old role, refuse a no-op, demote the
// existing primary when the new role is primary, apply the change, and bump
// the subject's version.
func (edge membershipEdge) foldRoleChanged(ctx context.Context, tx *sql.Tx, event Event) error {
	payload, subjectID, err := edge.payload(event)
	if err != nil {
		return err
	}
	var oldRole string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM `+edge.table+` WHERE `+edge.idColumn+` = ? AND project_id = ?`,
		subjectID, payload.ProjectID).Scan(&oldRole); err == sql.ErrNoRows { //nolint:gosec // table and idColumn come from the two closed membershipEdge values and all values stay parameter-bound.
		return newFailure(KindMembershipConflict, "fold_event", edge.label+" membership does not exist", false,
			"add the membership before changing its role")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot read "+edge.label+" membership", true,
			"retry once the database is readable", err)
	}
	if oldRole == payload.Role {
		return newFailure(KindMembershipConflict, "fold_event", edge.label+" membership already has that role", false,
			"request a different role")
	}
	if payload.Role == "primary" {
		if _, err := tx.ExecContext(ctx, `UPDATE `+edge.table+` SET role = 'secondary' WHERE `+edge.idColumn+` = ? AND role = 'primary' AND project_id <> ?`, //nolint:gosec // table and idColumn come from the two closed membershipEdge values and all values stay parameter-bound.
			subjectID, payload.ProjectID); err != nil {
			return wrapFailure(KindUnavailable, "fold_event", "cannot demote the existing "+edge.label+" primary", true,
				"retry once the database is writable", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE `+edge.table+` SET role = ? WHERE `+edge.idColumn+` = ? AND project_id = ?`, //nolint:gosec // table and idColumn come from the two closed membershipEdge values and all values stay parameter-bound.
		payload.Role, subjectID, payload.ProjectID); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot change "+edge.label+" membership role", true,
			"retry once the database is writable", err)
	}
	return edge.bumpVersion(ctx, tx, event, payload)
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
		f.CurrentVersions = []SubjectCurrentVersion{{SubjectType: event.SubjectType, SubjectID: event.SubjectID, Version: current}}
		return f
	}
	result, err := tx.ExecContext(ctx, "UPDATE "+table+" SET version = ?, updated_at = ? WHERE id = ? AND version = ?", //nolint:gosec // all callers use products, projects, or work_items and every value stays parameter-bound.
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
			"compose creation with its initial membership or restore a verified snapshot")
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
	rows, err := tx.QueryContext(ctx, "SELECT DISTINCT work_id FROM work_projects WHERE project_id IN ("+strings.Join(placeholders, ",")+") ORDER BY work_id", args...) //nolint:gosec // the fragment contains only generated question-mark placeholders and every Project ID stays parameter-bound.
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
	return projectsForProduct(ctx, s.db, productID)
}

func ProjectsForProductTx(ctx context.Context, transaction *Transaction, productID string) ([]ProjectMembership, error) {
	tx, err := transactionSQL(transaction, "projects_for_product")
	if err != nil {
		return nil, err
	}
	return projectsForProduct(ctx, tx, productID)
}

func projectsForProduct(ctx context.Context, q queryer, productID string) ([]ProjectMembership, error) {
	rows, err := q.QueryContext(ctx, `
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

func (s *Store) ProjectsForWork(ctx context.Context, workID string) ([]ProjectMembership, error) {
	return projectsForWork(ctx, s.db, workID)
}

func projectsForWork(ctx context.Context, q queryer, workID string) ([]ProjectMembership, error) {
	rows, err := q.QueryContext(ctx, `
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
