package store

import (
	"context"
	"database/sql"
	"encoding/json"
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
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	rows, err := s.db.QueryContext(ctx,
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
