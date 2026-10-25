package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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
		// Known retired families remain readable metadata. Their nonterminal
		// work still answers to cutover readiness and mutation admission.
		if workflowFamilyArchived(pin.Ref) {
			continue
		}
		if _, current := builtinWorkflowCurrentVersions[pin.Ref]; !current && !registryHolds(pin) {
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

// checkWorkflowAuthoringCutover is the readiness half of the authoring
// retirement. A store whose nonterminal work still pins retired or old
// authoring — through the work's instance row or its active contract —
// refuses activation: either half can still mutate the work. The work item's
// lifecycle is the terminal authority: workflow_instance states such as
// outside_repair are held dispositions, not terminal ones, so an item still
// in progress blocks wherever its instance state sits. Terminal work opens
// and reads. Plain opens stay permissive; the per-mutation refusal lives in
// the workflow admission.
func checkWorkflowAuthoringCutover(ctx context.Context, q queryer) error {
	work, err := cutoverBlockedWork(ctx, q)
	if err != nil {
		return err
	}
	if len(work) != 0 {
		first := work[0]
		return binaryCompatibilityFailure(ctx, q, "workflow", first.Pin.Ref, first.Pin.Version, first.Pin.Digest,
			fmt.Sprintf("workflow definition %s version %d %s; nonterminal work item %s still pins it through its instance or active contract; re-run concord upgrade with {\"cancel_cutover_pinned_work\":true} to cancel every selected item inside the maintenance boundary after the verified snapshot", first.Pin.Ref, first.Pin.Version, first.Classification, first.WorkID))
	}
	return nil
}

type cutoverWork struct {
	WorkID         string
	Lifecycle      string
	Version        int64
	Pin            WorkflowDefinitionPin
	Classification string
}

// cutoverBlockedWork selects nonterminal work with an archived-family pin or
// pre-canonical authoring pin in its instance or active contract. Registry
// content owns the classification. Closed contracts remain historical only.
// Each work appears once, ordered by work ID; pin order breaks ties when both
// surfaces block the same work.
func cutoverBlockedWork(ctx context.Context, q queryer) ([]cutoverWork, error) {
	queries := make([]string, 0, 2)
	instancePresent, err := columnPresent(ctx, q, "workflow_instances", "definition_ref")
	if err != nil {
		return nil, err
	}
	if instancePresent {
		queries = append(queries, `SELECT w.id,w.lifecycle,w.version,i.definition_ref,i.definition_version,i.definition_digest
			FROM workflow_instances i JOIN work_items w ON w.id=i.work_id
			WHERE i.definition_ref IN `+authoringFamilySQLList()+`
			  AND w.lifecycle NOT IN `+terminalLifecycleSQLList())
	}
	contractPresent, err := columnPresent(ctx, q, "workflow_contracts", "definition_version")
	if err != nil {
		return nil, err
	}
	if contractPresent {
		activePresent, err := columnPresent(ctx, q, "workflow_contracts", "superseded_by")
		if err != nil {
			return nil, err
		}
		// Before supersession existed, every pinned contract was active.
		activeClause := ""
		if activePresent {
			activeClause = " AND c.superseded_by IS NULL"
		}
		queries = append(queries, `SELECT w.id,w.lifecycle,w.version,c.definition_ref,c.definition_version,c.definition_digest
			FROM workflow_contracts c JOIN work_items w ON w.id=c.work_id
			WHERE c.definition_ref IN `+authoringFamilySQLList()+`
			  AND c.definition_ref <> '' AND c.definition_version > 0`+activeClause+`
			  AND w.lifecycle NOT IN `+terminalLifecycleSQLList())
	}
	selected := []cutoverWork{}
	if len(queries) == 0 {
		return selected, nil
	}
	query := strings.Join(queries, " UNION ")
	rows, err := q.QueryContext(ctx, "SELECT id,lifecycle,version,definition_ref,definition_version,definition_digest FROM ("+query+") ORDER BY id,definition_ref,definition_version,definition_digest") //nolint:gosec // table names come only from the closed list above.
	if err != nil {
		return nil, compatibilityReadFailure(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var work cutoverWork
		if err := rows.Scan(&work.WorkID, &work.Lifecycle, &work.Version, &work.Pin.Ref, &work.Pin.Version, &work.Pin.Digest); err != nil {
			return nil, compatibilityReadFailure(err)
		}
		if len(selected) > 0 && selected[len(selected)-1].WorkID == work.WorkID {
			continue
		}
		if workflowFamilyArchived(work.Pin.Ref) {
			work.Classification = "is archived"
		} else {
			if !liveWorkflowAuthoringFamilies[work.Pin.Ref] {
				continue
			}
			entry, ok := BuiltinWorkflowRegistry().Current(work.Pin.Ref)
			if !ok || workflowCanonicalAuthoringActive(entry.Definition) {
				continue
			}
			work.Classification = "predates the canonical authoring contract"
		}
		selected = append(selected, work)
	}
	if err := rows.Err(); err != nil {
		return nil, compatibilityReadFailure(err)
	}
	if err := rows.Close(); err != nil {
		return nil, compatibilityReadFailure(err)
	}
	return selected, nil
}

// authoringFamilySQLList renders the retired and live authoring families as a
// parenthesised SQL literal list. The set is the two closed family maps, never
// caller input.
func authoringFamilySQLList() string {
	refs := make([]string, 0, len(archivedWorkflowFamilies)+len(liveWorkflowAuthoringFamilies))
	for ref := range archivedWorkflowFamilies {
		refs = append(refs, ref)
	}
	for ref := range liveWorkflowAuthoringFamilies {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	quoted := make([]string, len(refs))
	for i, ref := range refs {
		quoted[i] = "'" + ref + "'"
	}
	return "(" + strings.Join(quoted, ",") + ")"
}

// cutoverCancellationAdmission predicts read-only refusals using the same
// owners as ordinary event append and fold. Older schemas have no hold table.
func cutoverCancellationAdmission(ctx context.Context, q queryer, work cutoverWork) error {
	present, err := columnPresent(ctx, q, "outside_repair_dispositions", "state")
	if err != nil {
		return err
	}
	if present {
		if err := requireNoOutsideRepairTx(ctx, q, work.WorkID, "fold_event"); err != nil {
			return err
		}
	}
	if err := refuseRemovedWorkTx(ctx, q, work.WorkID, "apply_operation"); err != nil {
		return err
	}
	var kind string
	if err := q.QueryRowContext(ctx, `SELECT kind FROM work_items WHERE id=?`, work.WorkID).Scan(&kind); err != nil {
		return compatibilityReadFailure(err)
	}
	if !actionableWorkKind(kind) {
		return retiredInitiativeWriteRefused("work.transitioned")
	}
	if !ordinaryTransitions[work.Lifecycle]["cancelled"] {
		return illegalTransition(work.Lifecycle, "cancelled")
	}
	return nil
}

// cancelCutoverPinnedWork uses ordinary lifecycle events in one transaction.
// The final predicate read is inside that transaction so an incomplete
// disposition cannot commit any cancellation.
func (s *Store) cancelCutoverPinnedWork(ctx context.Context) (*CutoverDisposition, error) {
	tx, err := beginOrdinaryTx(ctx, s.db)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "upgrade", "cannot begin cutover disposition", true, "retry once the database is writable", err)
	}
	defer func() { _ = tx.Rollback() }()
	selected, err := cutoverBlockedWork(ctx, tx)
	if err != nil {
		return nil, err
	}
	disposition := newCutoverDisposition()
	disposition.include(selected)
	for _, work := range selected {
		payload, err := json.Marshal(workTransitionPayload{
			From: work.Lifecycle, To: "cancelled",
			Reason:          fmt.Sprintf("CD-0218 D5 cutover disposition: %s version %d %s (digest %s)", work.Pin.Ref, work.Pin.Version, work.Classification, work.Pin.Digest),
			ExpectedVersion: work.Version, ResultingVersion: work.Version + 1,
		})
		if err != nil {
			return nil, err
		}
		event := Event{
			EventID: fmt.Sprintf("upgrade-cutover-cancel:%s:%d", work.WorkID, work.Version), Kind: "work.transitioned",
			SubjectType: SubjectWorkItem, SubjectID: work.WorkID, Actor: "operator",
			OccurredAt: s.now(), PayloadVersion: 1, Payload: payload,
		}
		if _, err := applyOperationTx(ctx, tx, Operation{Events: []Event{event}}, newWriteScope(tx), false); err != nil {
			return nil, fmt.Errorf("cutover cancellation of work %s refused: %w", work.WorkID, err)
		}
	}
	if err := checkWorkflowAuthoringCutover(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, wrapFailure(KindUnavailable, "upgrade", "cannot commit cutover disposition", true, "inspect the store before retrying", err)
	}
	return disposition, nil
}

// registryHolds is the slow path for a pin outside the built-in current
// versions table: it builds the registry, which also holds definitions
// registered at runtime (test fixture families). Store open reaches it only
// for such a pin or for one this binary refuses.
func registryHolds(pin WorkflowDefinitionPin) bool {
	_, ok := BuiltinWorkflowRegistry().Current(pin.Ref)
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

// Writer identity is diagnostic storage metadata, not workflow authority. It
// commits with the representation it names; legacy state remains unknown.
func recordRuntimeStateWriter(ctx context.Context, tx *sql.Tx, surface, ref string, required int64, digest string) error {
	// An older schema can be written during explicit migration. It has
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
