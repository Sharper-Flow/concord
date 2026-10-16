package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
)

// The durable design projection: the derived per-work revision and canonical
// content digest one architecture-bearing design records, and the membership
// join its fold validates against. The public projection members live on
// WorkflowDesignRecord; a design without an architecture leaves every new
// member absent rather than empty.

// workflowDesignProjectionDigest pins the design digest: sha256 over the
// canonical JSON of the complete design content. The canonical form sorts
// keys and drops insignificant spelling, so the digest follows the semantic
// content alone — no timestamp, actor, or work version enters it.
func workflowDesignProjectionDigest(content workflowDesignContent) (string, error) {
	raw, err := json.Marshal(content)
	if err != nil {
		return "", designArchitectureFailure(KindInvalidPayload, "design content cannot be measured", "repair the design content")
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		return "", designArchitectureFailure(KindInvalidPayload, "design content has no canonical form", "repair the design content")
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// workflowDesignMemberProjectsTx reads the work's member Projects inside the
// caller's transaction, so architecture membership validates against the same
// projected state a replay rebuilds. The store connection pools one
// connection, so this tx read must never become an s.db read.
func workflowDesignMemberProjectsTx(ctx context.Context, tx *sql.Tx, workID string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT project_id FROM work_projects WHERE work_id=?`, workID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read the design's member projects")
	}
	defer rows.Close()
	projects := make(map[string]bool)
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			return nil, workflowProjectionError(err, "cannot read the design's member projects")
		}
		projects[project] = true
	}
	if err := rows.Err(); err != nil {
		return nil, workflowProjectionError(err, "cannot read the design's member projects")
	}
	return projects, nil
}

// workflowDesignArchitectureProjectionTx derives the durable projection pair
// one architecture-bearing design records: the next monotonic per-work
// revision and the canonical design-content digest. Both derive from
// transaction state and event content alone; the caller supplies neither.
// Historical rows are retained across contract invalidations, so the highest
// recorded revision stays monotonic, and a rebuild refolds the log in order
// and derives the same values again.
func workflowDesignArchitectureProjectionTx(ctx context.Context, tx *sql.Tx, workID string, content workflowDesignContent) (int64, string, error) {
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0)+1 FROM workflow_design_records WHERE work_id=?`, workID).Scan(&revision); err != nil {
		return 0, "", workflowProjectionError(err, "cannot derive the design revision")
	}
	digest, err := workflowDesignProjectionDigest(content)
	if err != nil {
		return 0, "", err
	}
	return revision, digest, nil
}
