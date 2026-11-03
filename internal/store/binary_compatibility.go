package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/sharper-flow/concord/internal/version"
)

// runtimeStateWriter identifies the knowledge projection's writer.
type runtimeStateWriter struct {
	surface           string
	definitionRef     string
	digest            string
	projectionVersion int64
}

func binaryCompatibilityFailure(ctx context.Context, q queryer, subject runtimeStateWriter, detail string) error {
	writer := "unknown (writer provenance was not recorded)"
	present, err := columnPresent(ctx, q, "runtime_state_writers", "binary_version")
	if err != nil {
		return err
	}
	if present {
		query := `SELECT binary_version FROM runtime_state_writers WHERE surface=? AND definition_ref=? AND digest=?`
		args := []any{subject.surface, subject.definitionRef, subject.digest}
		if subject.surface == "knowledge" {
			current, err := columnPresent(ctx, q, "runtime_state_writers", "knowledge_projection_version")
			if err != nil {
				return err
			}
			if current {
				query += " AND knowledge_projection_version=?"
			} else {
				query += " AND version=?"
			}
			args = append(args, subject.projectionVersion)
		}
		err := q.QueryRowContext(ctx, query, args...).Scan(&writer)
		if err != nil && err != sql.ErrNoRows {
			return compatibilityReadFailure(err)
		}
	}
	return newFailure(KindSchemaUnsupported, "binary_compatibility",
		fmt.Sprintf("binary compatibility refused: serving binary %s; state writer binary %s; %s; contact the operator to stop traffic to this binary and use a release that supports the recorded state; drain incompatible binaries before release rotation", version.Value, writer, detail), false,
		"contact_operator")
}

func compatibilityReadFailure(err error) error {
	return wrapFailure(KindUnavailable, "binary_compatibility", "cannot inspect shared-state binary compatibility", false, "contact_operator", err)
}

// Writer identity is diagnostic storage metadata, not workflow authority. It
// commits with the representation it names; legacy state remains unknown.
func recordRuntimeStateWriter(ctx context.Context, tx *sql.Tx, subject runtimeStateWriter) error {
	present, err := columnPresent(ctx, tx, "runtime_state_writers", "knowledge_projection_version")
	if err != nil || !present {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_state_writers(surface,definition_ref,knowledge_projection_version,digest,binary_version) VALUES(?,?,?,?,?)
		ON CONFLICT(surface,definition_ref,knowledge_projection_version,digest) DO UPDATE SET binary_version=excluded.binary_version`, subject.surface, subject.definitionRef, subject.projectionVersion, subject.digest, version.Value)
	if err != nil {
		return wrapFailure(KindUnavailable, "binary_compatibility", "cannot record the shared-state writer binary", true, "confirm the database is writable", err)
	}
	return nil
}
