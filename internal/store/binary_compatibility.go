package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/sharper-flow/concord/internal/version"
)

// Runtime compatibility is independent of the schema floor and release age.
// Inspect one snapshot before migration or command execution. Compatible
// pinned pairs keep operating even after later additive schema steps.
func (s *Store) checkBinaryCompatibility(ctx context.Context) error {
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		return compatibilityReadFailure(err)
	}
	defer func() { _ = tx.Rollback() }()
	return checkBinaryCompatibility(ctx, tx)
}

func checkBinaryCompatibility(ctx context.Context, q queryer) error {
	present, err := columnPresent(ctx, q, "knowledge_index_watermark", "projection_version")
	if err != nil {
		return err
	}
	if present {
		var required int64
		err := q.QueryRowContext(ctx, `SELECT projection_version FROM knowledge_index_watermark
			WHERE projection_version > ? ORDER BY projection_version DESC LIMIT 1`, knowledgeProjectionVersion).Scan(&required)
		if err != nil && err != sql.ErrNoRows {
			return compatibilityReadFailure(err)
		}
		if err == nil {
			return binaryCompatibilityFailure(ctx, q, "knowledge", "", required, "",
				fmt.Sprintf("knowledge projection version %d exceeds this binary's version %d", required, knowledgeProjectionVersion))
		}
	}
	return checkWorkflowBinaryCompatibility(ctx, q)
}

func checkWorkflowBinaryCompatibility(ctx context.Context, q queryer) error {
	// Both current instances and historical contracts can supply a pin to a
	// read or a completion gate. Empty pre-pin contract rows are not pins.
	queries := []string{}
	for _, table := range []string{"workflow_instances", "workflow_contracts"} {
		present, err := columnPresent(ctx, q, table, "definition_version")
		if err != nil {
			return err
		}
		if present {
			queries = append(queries, "SELECT definition_ref,definition_version,definition_digest FROM "+table+" WHERE definition_ref <> '' AND definition_version > 0")
		}
	}
	if len(queries) == 0 {
		return nil
	}
	query := queries[0]
	if len(queries) == 2 {
		query += " UNION " + queries[1]
	}
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT definition_ref,definition_version,definition_digest FROM ("+query+") ORDER BY definition_ref,definition_version,definition_digest") //nolint:gosec // table names come only from the closed list above.
	if err != nil {
		return compatibilityReadFailure(err)
	}
	defer func() { _ = rows.Close() }()
	var unsupported *WorkflowDefinitionPin
	for rows.Next() {
		var pin WorkflowDefinitionPin
		if err := rows.Scan(&pin.Ref, &pin.Version, &pin.Digest); err != nil {
			return compatibilityReadFailure(err)
		}
		if !builtinWorkflowVersionRegistered(pin.Ref, pin.Version) && !registryHolds(pin) {
			unsupported = &pin
			break
		}
	}
	if err := rows.Err(); err != nil {
		return compatibilityReadFailure(err)
	}
	if err := rows.Close(); err != nil {
		return compatibilityReadFailure(err)
	}
	if unsupported != nil {
		return binaryCompatibilityFailure(ctx, q, "workflow", unsupported.Ref, unsupported.Version, unsupported.Digest,
			fmt.Sprintf("workflow definition %s version %d is not registered by this binary", unsupported.Ref, unsupported.Version))
	}
	return nil
}

// registryHolds is the slow path for a pin outside the built-in current
// versions table: it builds the registry, which also holds definitions
// registered at runtime (test fixture families). Store open reaches it only
// for such a pin or for one this binary refuses.
func registryHolds(pin WorkflowDefinitionPin) bool {
	_, ok := BuiltinWorkflowRegistry().Lookup(pin.Ref, pin.Version)
	return ok
}

func binaryCompatibilityFailure(ctx context.Context, q queryer, surface, ref string, required int64, digest, detail string) error {
	writer := "unknown (writer provenance was not recorded)"
	present, err := columnPresent(ctx, q, "runtime_state_writers", "binary_version")
	if err != nil {
		return err
	}
	if present {
		err := q.QueryRowContext(ctx, `SELECT binary_version FROM runtime_state_writers WHERE surface=? AND definition_ref=? AND version=? AND digest=?`, surface, ref, required, digest).Scan(&writer)
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

// Writer identity is diagnostic storage metadata, not replay authority. It
// commits with the representation it names; legacy state remains unknown.
func recordRuntimeStateWriter(ctx context.Context, tx *sql.Tx, surface, ref string, required int64, digest string) error {
	// An older schema can be folded during explicit migration/replay. It has
	// no writer provenance yet; compatibility never relies on this metadata.
	present, err := columnPresent(ctx, tx, "runtime_state_writers", "binary_version")
	if err != nil || !present {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_state_writers(surface,definition_ref,version,digest,binary_version) VALUES(?,?,?,?,?)
		ON CONFLICT(surface,definition_ref,version,digest) DO UPDATE SET binary_version=excluded.binary_version`, surface, ref, required, digest, version.Value)
	if err != nil {
		return wrapFailure(KindUnavailable, "binary_compatibility", "cannot record the shared-state writer binary", true, "confirm the database is writable", err)
	}
	return nil
}

func recordRuntimeContractWriter(ctx context.Context, tx *sql.Tx, workID string, contractVersion int64) error {
	var pin WorkflowDefinitionPin
	err := tx.QueryRowContext(ctx, `SELECT definition_ref,definition_version,definition_digest FROM workflow_contracts
		WHERE work_id=? AND contract_version=? AND definition_version>0`, workID, contractVersion).Scan(&pin.Ref, &pin.Version, &pin.Digest)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return compatibilityReadFailure(err)
	}
	return recordRuntimeStateWriter(ctx, tx, "workflow", pin.Ref, pin.Version, pin.Digest)
}
