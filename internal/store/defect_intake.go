package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// The defect intake record and its capture admission owner (CON-797). A new
// public bug capture carries the record; the capture membership fold runs the
// one transaction-scoped admission inside the same transaction that creates
// the work item, so a refusal rolls the whole capture back before any durable
// or filesystem effect exists.

const (
	// MaxDefectFailureShapeLength bounds the canonical failure shape slug.
	MaxDefectFailureShapeLength = 64
	// MaxDefectTextLength bounds the reproduction and searched texts.
	MaxDefectTextLength = 8192
	// MaxRelatedDefectIDs bounds the explicit sibling declaration.
	MaxRelatedDefectIDs = 20
)

// defectFailureShapePattern is the canonical bounded slug identifier for a
// failure shape. It is an identifier, not a text heuristic: the operator picks
// it once per defect class and every capture of that class repeats it.
var defectFailureShapePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// DefectIntake is the capture admission record a new public bug capture
// requires and a research capture may carry as its cluster RCA identity. Bug
// and research are the only kinds that admit it.
type DefectIntake struct {
	FailureShape     string   `json:"failure_shape"`
	Reproduction     string   `json:"reproduction"`
	Searched         string   `json:"searched"`
	RelatedDefectIDs []string `json:"related_defect_ids"`
	RootCauseWorkID  string   `json:"root_cause_work_id,omitempty"`
}

// ValidateDefectIntake holds the kind rules and field bounds both public
// capture paths enforce before the capture transaction opens, so a malformed
// record refuses with no effect at all. The work.created fold repeats the
// same rules as the owning authority for directly appended events.
func ValidateDefectIntake(kind string, intake *DefectIntake) error {
	if intake == nil {
		if kind == "bug" {
			return newFailure(KindInvalidPayload, "defect_intake", "bug capture requires a defect_intake record", false,
				"supply a defect_intake record with failure_shape, reproduction, and searched")
		}
		return nil
	}
	if kind != "bug" && kind != "research" {
		return newFailure(KindInvalidPayload, "defect_intake", "defect_intake is admitted only by bug and research captures", false,
			"remove defect_intake or capture kind bug or research")
	}
	if kind == "research" && intake.RootCauseWorkID != "" {
		return newFailure(KindInvalidPayload, "defect_intake", "research intake cannot name a root_cause_work_id prerequisite", false,
			"capture the research analysis without a prerequisite; name its completed work ID on the bug retry")
	}
	if !defectFailureShapePattern.MatchString(intake.FailureShape) || len(intake.FailureShape) > MaxDefectFailureShapeLength {
		return newFailure(KindInvalidPayload, "defect_intake", "failure_shape is not a canonical bounded slug", false,
			"supply a lowercase slug of digits and hyphens up to 64 characters")
	}
	for name, text := range map[string]string{"reproduction": intake.Reproduction, "searched": intake.Searched} {
		if text == "" || len(text) > MaxDefectTextLength || strings.ContainsRune(text, '\x00') || !utf8.ValidString(text) {
			return newFailure(KindInvalidPayload, "defect_intake", name+" is empty, too long, or contains NUL", false,
				"supply bounded UTF-8 text")
		}
	}
	if len(intake.RelatedDefectIDs) > MaxRelatedDefectIDs {
		return newFailure(KindInvalidPayload, "defect_intake", "related_defect_ids exceeds its bound", false,
			fmt.Sprintf("declare at most %d earlier defect work IDs", MaxRelatedDefectIDs))
	}
	seen := make(map[string]bool, len(intake.RelatedDefectIDs))
	for _, related := range intake.RelatedDefectIDs {
		if related == "" || seen[related] {
			return newFailure(KindInvalidPayload, "defect_intake", "related_defect_ids carries an empty or duplicated work ID", false,
				"supply distinct earlier defect work IDs")
		}
		seen[related] = true
	}
	if intake.RootCauseWorkID != "" && !bootstrapIDPattern.MatchString(intake.RootCauseWorkID) {
		return newFailure(KindInvalidPayload, "defect_intake", "root_cause_work_id is not a valid bounded identifier", false,
			"supply a bounded work identifier")
	}
	return nil
}

// DefectClassification is the persisted intent_json block the admission
// owner writes: the captured intake plus the core's sibling snapshot. Every
// list field marshals as an array, never null, so the projection stays
// byte-stable across a rebuild.
type DefectClassification struct {
	FailureShape     string   `json:"failure_shape"`
	Reproduction     string   `json:"reproduction"`
	Searched         string   `json:"searched"`
	RelatedDefectIDs []string `json:"related_defect_ids"`
	RootCauseWorkID  string   `json:"root_cause_work_id"`
	SiblingIDs       []string `json:"sibling_ids"`
}

// defectIntakeFromProjection copies the captured intake fields onto a
// classification block whose snapshot the admission owner fills. A capture
// that carries no record persists no block.
func defectIntakeFromProjection(intake *DefectIntake) *DefectClassification {
	if intake == nil {
		return nil
	}
	related := intake.RelatedDefectIDs
	if related == nil {
		related = []string{}
	}
	return &DefectClassification{
		FailureShape:     intake.FailureShape,
		Reproduction:     intake.Reproduction,
		Searched:         intake.Searched,
		RelatedDefectIDs: append([]string{}, related...),
		RootCauseWorkID:  intake.RootCauseWorkID,
		SiblingIDs:       []string{},
	}
}

// normalizedDefectIntake returns a copy of the record whose related list is
// never nil, so request marshalling and digest comparison treat an omitted
// list and an empty list as the same capture intent without writing through
// the caller's pointer.
func normalizedDefectIntake(intake *DefectIntake) *DefectIntake {
	if intake == nil {
		return nil
	}
	copied := *intake
	if copied.RelatedDefectIDs == nil {
		copied.RelatedDefectIDs = []string{}
	}
	return &copied
}

// admitDefectIntakeTx is the one transaction-scoped owner of defect capture
// admission. The capture membership fold calls it with the new item's Project
// memberships already inserted, so Product scope is known inside the same
// transaction that creates the item: concurrent same-shape captures serialize
// on the store's single write connection, a refusal rolls the whole capture
// back, and a rebuild replays the identical decision from the log prefix.
func admitDefectIntakeTx(ctx context.Context, tx *sql.Tx, workID string) error {
	var kind, intentJSON string
	err := tx.QueryRowContext(ctx, `SELECT kind, intent_json FROM work_items WHERE id=?`, workID).Scan(&kind, &intentJSON)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "defect_intake", "work item does not exist at defect admission", false,
			"create the work item before admitting its defect intake")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "defect_intake", "cannot read the capturing work item", true,
			"retry once the database is readable", err)
	}
	var intent workIntentProjection
	if err := json.Unmarshal([]byte(intentJSON), &intent); err != nil {
		return wrapFailure(KindInvariantViolation, "defect_intake", "stored work intent is not readable", false,
			"rebuild the work item projection from its event log", err)
	}
	if intent.Defect == nil {
		return nil
	}
	cluster, err := defectSiblingClusterTx(ctx, tx, workID, intent.Defect)
	if err != nil {
		return err
	}
	if kind == "bug" && (len(cluster) > 0 || intent.Defect.RootCauseWorkID != "") {
		if err := admitRecurrenceBehindRootCauseTx(ctx, tx, workID, intent.Defect, cluster); err != nil {
			return err
		}
	}
	intent.Defect.SiblingIDs = cluster
	encoded, err := json.Marshal(intent)
	if err != nil {
		return wrapFailure(KindInvalidPayload, "defect_intake", "cannot encode the defect classification", false,
			"supply a JSON-safe intent", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET intent_json=? WHERE id=?`, string(encoded), workID); err != nil {
		return wrapFailure(KindUnavailable, "defect_intake", "cannot persist the defect classification", true,
			"retry once the database is writable", err)
	}
	return nil
}

// defectSiblingClusterTx computes the sibling cluster for one admission: the
// same-shape previous bug items of the capture's own Product across every
// lifecycle, read from the stored intent classification, unioned with the
// explicitly declared related defects so historical unclassified bugs join
// the cluster by declaration rather than by inference.
func defectSiblingClusterTx(ctx context.Context, tx *sql.Tx, workID string, classification *DefectClassification) ([]string, error) {
	// The cluster is the core's authoritative snapshot and carries no size
	// cap: every same-shape sibling and every declared related defect joins
	// it, because slicing it would let a later admission claim coverage a
	// complete cluster never had. Only the refusal's human detail is bounded.
	cluster := make(map[string]bool, len(classification.RelatedDefectIDs))
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT sibling.id
		FROM work_items sibling
		JOIN work_projects sibling_project ON sibling_project.work_id = sibling.id
		JOIN product_projects sibling_scope ON sibling_scope.project_id = sibling_project.project_id
		JOIN work_projects own_project ON own_project.work_id = ?
		JOIN product_projects own_scope ON own_scope.project_id = own_project.project_id
		WHERE sibling.kind = 'bug'
		  AND sibling.id <> ?
		  AND sibling_scope.product_id = own_scope.product_id
		  AND json_extract(sibling.intent_json, '$.defect.failure_shape') = ?`, workID, workID, classification.FailureShape)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "defect_intake", "cannot read same-shape defect siblings", true,
			"retry once the database is readable", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sibling string
		if err := rows.Scan(&sibling); err != nil {
			return nil, wrapFailure(KindUnavailable, "defect_intake", "cannot scan a defect sibling", true,
				"retry once the database is readable", err)
		}
		cluster[sibling] = true
	}
	if err := rows.Err(); err != nil {
		return nil, wrapFailure(KindUnavailable, "defect_intake", "cannot enumerate defect siblings", true,
			"retry once the database is readable", err)
	}
	for _, related := range classification.RelatedDefectIDs {
		if related == workID {
			return nil, newFailure(KindInvalidPayload, "defect_intake", "related defect "+related+" names the capture itself", false,
				"declare earlier defect work IDs, not this capture")
		}
		var kind, shape string
		err := tx.QueryRowContext(ctx, `SELECT kind, coalesce(json_extract(intent_json, '$.defect.failure_shape'), '') FROM work_items WHERE id=?`, related).Scan(&kind, &shape)
		if err == sql.ErrNoRows {
			return nil, newFailure(KindProjectionNotFound, "defect_intake", "related defect "+related+" does not exist", false,
				"declare earlier defect work IDs that exist")
		}
		if err != nil {
			return nil, wrapFailure(KindUnavailable, "defect_intake", "cannot read a related defect", true,
				"retry once the database is readable", err)
		}
		if kind != "bug" {
			return nil, newFailure(KindInvalidPayload, "defect_intake", "related defect "+related+" is kind "+kind+", not a bug", false,
				"declare earlier bug work IDs")
		}
		if shape != "" && shape != classification.FailureShape {
			return nil, newFailure(KindInvalidPayload, "defect_intake", "related defect "+related+" is classified with failure_shape "+shape+", not "+classification.FailureShape, false,
				"declare earlier bugs of the same failure shape")
		}
		sameProduct, err := defectSharesProductTx(ctx, tx, workID, related)
		if err != nil {
			return nil, err
		}
		if !sameProduct {
			return nil, newFailure(KindInvalidPayload, "defect_intake", "related defect "+related+" is not in this capture's Product", false,
				"declare earlier bugs of the same Product")
		}
		cluster[related] = true
	}
	out := make([]string, 0, len(cluster))
	for sibling := range cluster {
		out = append(out, sibling)
	}
	sort.Strings(out)
	return out, nil
}

// defectSharesProductTx reports whether two work items share at least one
// Product through their Project memberships.
func defectSharesProductTx(ctx context.Context, tx *sql.Tx, workID, otherID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM work_projects own_project
			JOIN product_projects own_scope ON own_scope.project_id = own_project.project_id
			JOIN work_projects other_project ON other_project.work_id = ?
			JOIN product_projects other_scope ON other_scope.project_id = other_project.project_id
			WHERE own_project.work_id = ? AND own_scope.product_id = other_scope.product_id
		)`, otherID, workID).Scan(&exists)
	if err != nil {
		return false, wrapFailure(KindUnavailable, "defect_intake", "cannot compare defect Product scope", true,
			"retry once the database is readable", err)
	}
	return exists, nil
}

// admitRecurrenceBehindRootCauseTx holds the recurrence gate: one earlier
// matching bug is recurrence, and a recurrent bug capture is admitted only
// behind a completed workflow.research item of the same Product carrying the
// same failure shape and a sibling snapshot that covers the complete current
// cluster.
func admitRecurrenceBehindRootCauseTx(ctx context.Context, tx *sql.Tx, workID string, classification *DefectClassification, cluster []string) error {
	if classification.RootCauseWorkID == "" {
		failure := newFailure(KindInvalidOperation, "defect_intake",
			recurrenceRefusalDetail(classification.FailureShape, cluster),
			false,
			recurrenceResearchRouteInstruction)
		failure.CandidateIDs = boundedDefectIDs(cluster)
		return failure
	}
	cause := classification.RootCauseWorkID
	var kind, lifecycle string
	err := tx.QueryRowContext(ctx, `SELECT kind, lifecycle FROM work_items WHERE id=?`, cause).Scan(&kind, &lifecycle)
	if err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "defect_intake", "root_cause_work_id "+cause+" does not exist", false,
			"name the completed cluster RCA work item")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "defect_intake", "cannot read the root cause work item", true,
			"retry once the database is readable", err)
	}
	if kind != "research" {
		return newFailure(KindInvalidPayload, "defect_intake", "root cause work item "+cause+" is kind "+kind+", not research", false,
			"name a completed workflow.research cluster RCA")
	}
	if sameProduct, err := defectSharesProductTx(ctx, tx, workID, cause); err != nil {
		return err
	} else if !sameProduct {
		return newFailure(KindInvalidPayload, "defect_intake", "root cause work item "+cause+" is not in this capture's Product", false,
			"name a cluster RCA of the same Product")
	}
	var definitionRef string
	err = tx.QueryRowContext(ctx, `SELECT definition_ref FROM workflow_instances WHERE work_id=?`, cause).Scan(&definitionRef)
	if err == sql.ErrNoRows {
		return newFailure(KindInvalidPayload, "defect_intake", "root cause work item "+cause+" pins no workflow family, not workflow.research", false,
			"name a completed workflow.research cluster RCA")
	}
	if err != nil {
		return wrapFailure(KindUnavailable, "defect_intake", "cannot read the root cause workflow family", true,
			"retry once the database is readable", err)
	}
	if definitionRef != "workflow.research" {
		return newFailure(KindInvalidPayload, "defect_intake", "root cause work item "+cause+" pins workflow family "+definitionRef+", not workflow.research", false,
			"name a completed workflow.research cluster RCA")
	}
	if lifecycle != "completed" {
		return newFailure(KindInvalidPayload, "defect_intake", "root cause work item "+cause+" is lifecycle "+lifecycle+", not completed", false,
			"complete the cluster RCA before retrying the bug capture")
	}
	var shape, snapshotJSON string
	err = tx.QueryRowContext(ctx, `SELECT coalesce(json_extract(intent_json, '$.defect.failure_shape'), ''), coalesce(json_extract(intent_json, '$.defect.sibling_ids'), '[]') FROM work_items WHERE id=?`, cause).Scan(&shape, &snapshotJSON)
	if err != nil {
		return wrapFailure(KindUnavailable, "defect_intake", "cannot read the root cause classification", true,
			"retry once the database is readable", err)
	}
	if shape != classification.FailureShape {
		return newFailure(KindInvalidPayload, "defect_intake", "root cause work item "+cause+" carries failure_shape "+shape+", not "+classification.FailureShape, false,
			"name a cluster RCA of the same failure shape")
	}
	var snapshot []string
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return wrapFailure(KindInvariantViolation, "defect_intake", "root cause sibling snapshot is not readable", false,
			"rebuild the root cause projection from its event log", err)
	}
	covered := make(map[string]bool, len(snapshot))
	for _, sibling := range snapshot {
		covered[sibling] = true
	}
	for _, sibling := range cluster {
		if !covered[sibling] {
			return newFailure(KindInvalidPayload, "defect_intake", "root cause work item "+cause+" does not cover cluster sibling "+sibling, false,
				"capture a research RCA covering the complete cluster, then retry")
		}
	}
	return nil
}

// boundedDefectIDs caps a cluster listing at the failure candidate bound the
// agent envelope enforces, so a refusal stays deliverable.
func boundedDefectIDs(ids []string) []string {
	capped := append([]string{}, ids...)
	if len(capped) > MaxFailureCandidates {
		capped = capped[:MaxFailureCandidates]
	}
	return capped
}

// maxPublicRefusalDetailBytes is the byte budget the recurrence refusal
// renders its detail within. The agent envelope bounds a typed error's
// message at the same value (internal/agent boundedErrorMessage), so a detail
// built inside this budget crosses the public boundary whole. The pairing
// mirrors the MaxFailureCandidates pairing between the two packages.
const maxPublicRefusalDetailBytes = 1000

// recurrenceResearchRouteInstruction is the route a recurrence refusal gives
// (CD-0211 D3): the admitted research capture, its completion through the
// research workflow, and the bug retry that names the completed root cause.
const recurrenceResearchRouteInstruction = "capture kind research with this defect_intake, complete the cluster RCA, then retry the bug capture with root_cause_work_id"

// recurrenceRefusalDetail renders the recurrence refusal inside the public
// message budget. The shape, the total sibling count, and the research route
// render first and always whole; the bounded candidate listing takes only the
// bytes that remain. Display bounds never reduce the admission population
// (CD-0211 D2): the persisted snapshot and the typed candidates keep carrying
// the cluster this rendering shortens.
func recurrenceRefusalDetail(shape string, cluster []string) string {
	head := fmt.Sprintf("recurrent defect capture refused: failure_shape=%s; siblings=%d; %s", shape, len(cluster), recurrenceResearchRouteInstruction)
	const separator = "; candidates="
	budget := maxPublicRefusalDetailBytes - len(head) - len(separator)
	if budget < 0 {
		budget = 0
	}
	return head + separator + budgetedDefectCandidates(boundedDefectIDs(cluster), budget)
}

// budgetedDefectCandidates joins whole candidate identifiers while they fit
// the remaining byte budget, then records how many candidates were dropped.
// The drop note reserves its own bytes before any identifier takes them, so
// the returned string never exceeds the budget, and an identifier is never
// cut mid-string: a partial identifier is a wrong identifier.
func budgetedDefectCandidates(ids []string, budget int) string {
	dropped := fmt.Sprintf(",... (+%d more)", len(ids))
	usable := budget - len(dropped)
	if usable < 0 {
		usable = 0
	}
	var list strings.Builder
	shown := 0
	for _, id := range ids {
		piece := "," + id
		if list.Len() == 0 {
			piece = id
		}
		if list.Len()+len(piece) > usable {
			break
		}
		list.WriteString(piece)
		shown++
	}
	if shown == len(ids) {
		return list.String()
	}
	if list.Len() > 0 {
		list.WriteString(",")
	}
	list.WriteString(dropped[1:])
	return list.String()
}
