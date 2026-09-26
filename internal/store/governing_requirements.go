package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// GoverningRequirement is a scope-level obligation that work captured into a
// Project must carry (CD-0035 D2). It is deliberately not attached to a law,
// rule, or spec clause: CD-0015 R0 forbids a per-rule obligation field.
type GoverningRequirement struct {
	ProjectID        string `json:"project_id"`
	RequirementRef   string `json:"requirement_ref"`
	Reason           string `json:"reason"`
	ExpectedVersion  int64  `json:"expected_version"`
	ResultingVersion int64  `json:"resulting_version"`
}

func decodeGoverningRequirement(event Event) (GoverningRequirement, error) {
	var r GoverningRequirement
	decoder := json.NewDecoder(strings.NewReader(string(event.Payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return GoverningRequirement{}, newFailure(KindInvalidPayload, "fold_event", "governing requirement payload is not decodable", false, "repair the stored governing requirement event payload")
	}
	if r.ProjectID == "" || r.RequirementRef == "" || r.Reason == "" {
		return GoverningRequirement{}, newFailure(KindInvalidPayload, "fold_event", "governing requirement payload is incomplete", false, "declare project_id, requirement_ref, and reason")
	}
	if len(r.RequirementRef) > 128 || len(r.Reason) > 1000 {
		return GoverningRequirement{}, newFailure(KindInvalidPayload, "fold_event", "governing requirement payload exceeds bounds", false, "shorten requirement_ref or reason")
	}
	return r, nil
}

func foldProjectGoverningRequirementDeclared(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProject); err != nil {
		return err
	}
	r, err := decodeGoverningRequirement(event)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO project_governing_requirements(project_id,requirement_ref,reason,declared_at) VALUES(?,?,?,?)
		 ON CONFLICT(project_id,requirement_ref) DO UPDATE SET reason=excluded.reason,declared_at=excluded.declared_at`,
		r.ProjectID, r.RequirementRef, r.Reason, event.OccurredAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot record governing requirement", true, "retry once the database is writable", err)
	}
	return bumpVersion(ctx, tx, "projects", event, r.ExpectedVersion, r.ResultingVersion, "Project")
}

func foldProjectGoverningRequirementWithdrawn(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProject); err != nil {
		return err
	}
	r, err := decodeGoverningRequirement(event)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM project_governing_requirements WHERE project_id=? AND requirement_ref=?`, r.ProjectID, r.RequirementRef)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot withdraw governing requirement", true, "retry once the database is writable", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return newFailure(KindInvariantViolation, "fold_event", "governing requirement is not declared for this Project", false, "withdraw a requirement that exists")
	}
	return bumpVersion(ctx, tx, "projects", event, r.ExpectedVersion, r.ResultingVersion, "Project")
}

// GoverningRequirementsForProjectIDs returns the union of requirements applicable
// to the given Projects. The union is the applicable set a capture must cover;
// CD-0035 D3 computes the refusal as a set difference against it.
func (s *Store) GoverningRequirementsForProjectIDs(ctx context.Context, ids []string) ([]string, error) {
	return governingRequirementsForProjectIDs(ctx, s.db, ids)
}

func governingRequirementsForProjectIDs(ctx context.Context, q queryer, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	rows, err := q.QueryContext(ctx,
		`SELECT DISTINCT requirement_ref FROM project_governing_requirements WHERE project_id IN (`+strings.Join(placeholders, ",")+") ORDER BY requirement_ref", args...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "scope", "cannot resolve governing requirements", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

func knownGoverningRequirementRefsForProducts(ctx context.Context, q queryer, productIDs []string) ([]string, error) {
	if len(productIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(productIDs))
	args := make([]any, len(productIDs))
	for i, id := range productIDs {
		placeholders[i], args[i] = "?", id
	}
	query := `SELECT requirement_ref FROM project_governing_requirements WHERE project_id IN (
		SELECT project_id FROM product_projects WHERE product_id IN (` + strings.Join(placeholders, ",") + `)
	) UNION SELECT s.law_id FROM law_subjects s JOIN law_domain_homes h ON h.home_project_id=s.home_project_id AND h.home_locator_id=s.home_locator_id AND h.law_id=s.law_id WHERE h.product_id IN (` + strings.Join(placeholders, ",") + `) AND s.status='accepted' ORDER BY 1`
	queryArgs := append(append([]any{}, args...), args...)
	rows, err := q.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "resolve_governing_requirement_refs", "cannot resolve registered requirements and accepted laws", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return refs, nil
}

// ValidateCaptureGoverningRequirements refuses declared refs that resolve in
// neither the capture scope nor the Product's registries and accepted laws.
func (s *Store) ValidateCaptureGoverningRequirements(ctx context.Context, productIDs, applicable, declared []string) error {
	known, err := knownGoverningRequirementRefsForProducts(ctx, s.db, productIDs)
	if err != nil {
		return err
	}
	resolved := make(map[string]struct{}, len(applicable)+len(known))
	for _, ref := range applicable {
		resolved[ref] = struct{}{}
	}
	for _, ref := range known {
		resolved[ref] = struct{}{}
	}
	for _, ref := range declared {
		if _, ok := resolved[ref]; !ok {
			return newFailure(KindProjectionNotFound, "capture", fmt.Sprintf("governing requirement %q is not registered or accepted", ref), false, "declare the requirement on the Project or name an accepted law ID")
		}
	}
	return nil
}

// MissingGoverningRequirements returns the applicable requirements the declared
// set does not cover, in deterministic order. An empty result permits capture.
func MissingGoverningRequirements(applicable, declared []string) []string {
	if len(applicable) == 0 {
		return nil
	}
	covered := make(map[string]bool, len(declared))
	for _, ref := range declared {
		covered[ref] = true
	}
	var missing []string
	for _, ref := range applicable {
		if !covered[ref] {
			missing = append(missing, ref)
		}
	}
	return missing
}
