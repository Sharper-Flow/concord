package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	productRowQueryID                   = "C14.ProductRows"
	productRowContract                  = "C14/1.0"
	productRowOrdering                  = "display_name:asc,id:asc"
	ProductRowAuthorityAuthoritative    = "authoritative"
	ProductRowAuthorityDegraded         = "degraded"
	ProductRowAuthorityUnreachable      = "unreachable"
	ProductRowCountsKnown               = "known"
	ProductRowCountsUnavailable         = "unavailable"
	ProductRowAttentionApprovalRequired = "approval_required"
	ProductRowAttentionActiveProblem    = "active_problem"
	ProductRowAttentionBlocked          = "blocked"
	ProductRowAttentionInProgress       = "in_progress"
	ProductRowAttentionReady            = "ready"
	ProductRowFocusStaleBlock           = "stale_block"
	ProductRowFocusUnreachable          = "unreachable"
	ProductRowFocusNoActionableWork     = "no_actionable_work"
	ProductRowFocusAuthoritativeEmpty   = "authoritative_empty"
)

var productRowWorkflowRegistry = BuiltinWorkflowRegistry()

// ProductRowStage is the declared Product or Project stage. A Project's paired
// override is authoritative and inherits the Product default when absent.
type ProductRowStage struct {
	Maturity           string `json:"maturity"`
	AudienceCommitment string `json:"audience_commitment"`
}

type ProductRowReliance struct {
	Authority       string   `json:"authority"`
	ObservedAt      string   `json:"observed_at"`
	Age             int64    `json:"age"`
	Stale           bool     `json:"stale"`
	BlocksExecution bool     `json:"blocks_execution"`
	Reason          string   `json:"reason,omitempty"`
	Omissions       []string `json:"omissions"`
}

type ProductRowRelianceInput struct {
	Authority       string   `json:"authority"`
	ObservedAt      string   `json:"observed_at,omitempty"`
	Age             int64    `json:"age,omitempty"`
	Stale           bool     `json:"stale,omitempty"`
	BlocksExecution bool     `json:"blocks_execution,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	Omissions       []string `json:"omissions,omitempty"`
}

type ProductRowUnavailable struct {
	Reason    string   `json:"reason"`
	Omissions []string `json:"omissions"`
}

type ProductRowActionCountValues struct {
	InProgress       int `json:"in_progress"`
	Blocked          int `json:"blocked"`
	Ready            int `json:"ready"`
	ActiveProblems   int `json:"active_problems"`
	ApprovalRequired int `json:"approval_required"`
	// OverdueAwaits counts open external awaits whose wait exceeded the
	// declared bound (issue #87): waiting-vs-never-completable made visible
	// in the row the operator scans.
	OverdueAwaits int `json:"overdue_awaits"`
}

type ProductRowActionCounts struct {
	State       string                       `json:"state"`
	Values      *ProductRowActionCountValues `json:"values,omitempty"`
	Unavailable *ProductRowUnavailable       `json:"unavailable,omitempty"`
}

type ProductRowStageContext struct {
	Kind          string           `json:"kind"`
	FocusOverride *ProductRowStage `json:"focus_override,omitempty"`
}

type ProductRowFocus struct {
	WorkID            string                 `json:"work_id"`
	Title             string                 `json:"title"`
	WorkKind          string                 `json:"work_kind"`
	Lifecycle         string                 `json:"lifecycle"`
	AttentionKind     string                 `json:"attention_kind"`
	Priority          int64                  `json:"priority"`
	WorkflowStepLabel string                 `json:"workflow_step_label,omitempty"`
	ProjectCount      int                    `json:"project_count"`
	StageContext      ProductRowStageContext `json:"stage_context"`
	// BlockedSessions routes operator attention when AttentionKind is
	// approval_required: the sessions waiting on an operator decision, oldest
	// first (issue #72). Empty for every other attention kind.
	BlockedSessions []BlockedSession `json:"blocked_sessions,omitempty"`
}

// ProductRow contains exactly C14's five row groups: identity, declared stage,
// reliance, action counts, and focus. The suffix is display metadata, not a
// second identity; ProductID remains the immutable navigation key.
type ProductRow struct {
	ProductID         string                 `json:"product_id"`
	DisplayName       string                 `json:"display_name"`
	DisplayNameSuffix string                 `json:"display_name_suffix,omitempty"`
	Stage             ProductRowStage        `json:"stage"`
	Reliance          ProductRowReliance     `json:"reliance"`
	ActionCounts      ProductRowActionCounts `json:"action_counts"`
	Focus             *ProductRowFocus       `json:"focus,omitempty"`
	FocusAbsentReason string                 `json:"focus_absent_reason,omitempty"`
}

type ProductRowRequest struct {
	// Product is an exact stable Product ID filter. No text search or
	// cross-Product semantic query is accepted by this API.
	Product string                   `json:"product,omitempty"`
	Limit   int                      `json:"limit,omitempty"`
	Cursor  string                   `json:"cursor,omitempty"`
	Source  *ProductRowRelianceInput `json:"source,omitempty"`
}

type ProductRowResult struct {
	ResultMeta
	ObservedAt string       `json:"observed_at"`
	Rows       []ProductRow `json:"rows"`
}

// ProductRowPagePayload is the one wire projection for C14 rows. The agent
// envelope and the launcher adapter both consume the same ProductRowResult;
// keeping this payload constructor here prevents either boundary from
// re-deriving or reshaping the row data independently.
func ProductRowPagePayload(result ProductRowResult) ([]byte, error) {
	rows := result.Rows
	if rows == nil {
		rows = []ProductRow{}
	}
	return json.Marshal(struct {
		ObservedAt string       `json:"observed_at"`
		Rows       []ProductRow `json:"rows"`
	}{ObservedAt: result.ObservedAt, Rows: rows})
}

type productRowCursor struct {
	Version   int    `json:"version"`
	QueryID   string `json:"query_id"`
	Product   string `json:"product"`
	Order     string `json:"order"`
	Watermark int64  `json:"watermark"`
	Name      string `json:"name"`
	ID        string `json:"id"`
}

type productRowWork struct {
	ID                string
	Kind              string
	Title             string
	Lifecycle         string
	Priority          int64
	Urgency           string
	CreatedAt         string
	UpdatedAt         string
	ProjectCount      int
	StageOverrides    []ProductRowStage
	Blocked           bool
	Ready             bool
	ActiveProblem     bool
	ApprovalRequired  bool
	OverdueAwaits     bool
	WorkflowStepLabel string
}

// productRowPageSQL is the only page-data statement. page_products first
// bounds Products, then one grouped join gathers all canonical work once per
// Product/work identity. The caller never performs a Product or work fan-out.
const productRowPageSQL = `
WITH page_products AS (
	SELECT id, display_name, stage_maturity, stage_audience_commitment, version, created_at, updated_at
	FROM products
	WHERE (? = '' OR id = ?)
	  AND (? = '' OR display_name > ? OR (display_name = ? AND id > ?))
	ORDER BY display_name COLLATE BINARY, id COLLATE BINARY
	LIMIT ?
), scoped_work AS (
	SELECT
		pp.id AS product_id,
		w.id AS work_id,
		w.kind,
		w.title,
		w.lifecycle,
		w.priority,
		w.urgency,
		w.created_at,
		w.updated_at,
		COUNT(wp.project_id) AS project_count,
		(w.lifecycle IN ('needed', 'in_progress') AND EXISTS (
			SELECT 1
			FROM relations r
			JOIN work_items blocker ON blocker.id = r.work_id_from
			WHERE r.work_id_to = w.id
			  AND r.kind = 'blocks'
			  AND blocker.lifecycle IN ('needed', 'in_progress')
		)) AS blocked,
		(w.lifecycle = 'needed' AND NOT EXISTS (
			SELECT 1
			FROM relations r
			JOIN work_items blocker ON blocker.id = r.work_id_from
			WHERE r.work_id_to = w.id
			  AND r.kind = 'blocks'
			  AND blocker.lifecycle IN ('needed', 'in_progress')
		)) AS ready,
		(w.kind = 'bug' AND w.lifecycle IN ('needed', 'in_progress')) AS active_problem,
		(w.lifecycle IN ('needed', 'in_progress') AND EXISTS (
			SELECT 1
			FROM workflow_external_conditions c
			WHERE c.work_id = w.id
			  AND c.condition_state = 'open'
			  AND c.expected_within_seconds IS NOT NULL
			  AND c.recorded_at IS NOT NULL
			  AND (julianday('now') - julianday(c.recorded_at)) * 86400 > c.expected_within_seconds
		)) AS overdue_awaits,
		wi.current_step,
		wi.definition_ref,
		wi.definition_version,
		wi.definition_digest,
		wi.instance_state,
		MIN(CASE WHEN project.stage_maturity_override IS NOT NULL THEN project.stage_maturity_override || char(31) || project.stage_audience_commitment_override END) AS stage_override_min,
		MAX(CASE WHEN project.stage_maturity_override IS NOT NULL THEN project.stage_maturity_override || char(31) || project.stage_audience_commitment_override END) AS stage_override_max
	FROM page_products pp
	JOIN product_projects product_membership ON product_membership.product_id = pp.id
	JOIN work_projects wp ON wp.project_id = product_membership.project_id
	JOIN projects project ON project.id = wp.project_id
	JOIN work_items w ON w.id = wp.work_id
	LEFT JOIN workflow_instances wi ON wi.work_id = w.id
	GROUP BY pp.id, w.id, w.kind, w.title, w.lifecycle, w.priority, w.urgency, w.created_at, w.updated_at,
		wi.current_step, wi.definition_ref, wi.definition_version, wi.definition_digest, wi.instance_state
)
SELECT
	pp.id, pp.display_name, pp.stage_maturity, pp.stage_audience_commitment, pp.version, pp.created_at, pp.updated_at,
	sw.work_id, sw.kind, sw.title, sw.lifecycle, sw.priority, sw.urgency, sw.created_at, sw.updated_at, sw.project_count,
	sw.blocked, sw.ready, sw.active_problem, sw.overdue_awaits,
	sw.current_step, sw.definition_ref, sw.definition_version, sw.definition_digest, sw.instance_state, sw.stage_override_min, sw.stage_override_max
FROM page_products pp
LEFT JOIN scoped_work sw ON sw.product_id = pp.id`

func validateProductRowSource(input *ProductRowRelianceInput) error {
	if input == nil || input.Authority == "" {
		return nil
	}
	if input.Authority != ProductRowAuthorityAuthoritative && input.Authority != ProductRowAuthorityDegraded && input.Authority != ProductRowAuthorityUnreachable {
		return newFailure(KindInvalidFilter, productRowQueryID, "source authority is not recognized", false, "use authoritative, degraded, or unreachable")
	}
	if input.Age < 0 {
		return newFailure(KindInvalidFilter, productRowQueryID, "source age cannot be negative", false, "supply a non-negative source age")
	}
	return nil
}

func parseProductRowStageOverrides(minValue, maxValue string) []ProductRowStage {
	if minValue == "" {
		return nil
	}
	parse := func(value string) (ProductRowStage, bool) {
		fields := strings.SplitN(value, string(rune(31)), 2)
		if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
			return ProductRowStage{}, false
		}
		return ProductRowStage{Maturity: fields[0], AudienceCommitment: fields[1]}, true
	}
	minStage, ok := parse(minValue)
	if !ok {
		return nil
	}
	if minValue == maxValue {
		return []ProductRowStage{minStage}
	}
	maxStage, ok := parse(maxValue)
	if !ok {
		return nil
	}
	return []ProductRowStage{minStage, maxStage}
}

func decodeProductRowCursor(value string) (productRowCursor, error) {
	var cursor productRowCursor
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Version != 1 || cursor.QueryID != productRowQueryID || cursor.Order != productRowOrdering || cursor.Name == "" || cursor.ID == "" {
		return cursor, newFailure(KindInvalidCursor, productRowQueryID, "cursor is not valid for the Product-row listing", false, "restart the bounded Product-row listing")
	}
	return cursor, nil
}

func encodeProductRowCursor(cursor productRowCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func productRowReliance(meta ResultMeta, input *ProductRowRelianceInput) ProductRowReliance {
	reliance := ProductRowReliance{
		Authority:       ProductRowAuthorityAuthoritative,
		ObservedAt:      meta.Freshness.ObservedAt,
		Age:             0,
		Stale:           false,
		BlocksExecution: false,
		Omissions:       []string{},
	}
	if input == nil {
		return reliance
	}
	if input.Authority != "" {
		reliance.Authority = input.Authority
	}
	reliance.ObservedAt = input.ObservedAt
	if reliance.ObservedAt == "" {
		reliance.ObservedAt = meta.Freshness.ObservedAt
	}
	reliance.Age = input.Age
	reliance.Stale = input.Stale
	reliance.BlocksExecution = input.BlocksExecution
	reliance.Reason = input.Reason
	reliance.Omissions = append([]string{}, input.Omissions...)
	if len(reliance.Omissions) > 16 {
		reliance.Omissions = reliance.Omissions[:16]
	}
	return reliance
}

func productRowSourceUnavailable(reliance ProductRowReliance) (string, bool) {
	if reliance.Stale || reliance.BlocksExecution {
		return ProductRowFocusStaleBlock, true
	}
	if reliance.Authority != ProductRowAuthorityAuthoritative {
		return ProductRowFocusUnreachable, true
	}
	return "", false
}

func productRowUnavailableReason(reliance ProductRowReliance) string {
	if reliance.Reason != "" {
		return reliance.Reason
	}
	if reliance.Stale || reliance.BlocksExecution {
		return "source_stale"
	}
	if reliance.Authority == ProductRowAuthorityUnreachable {
		return "authority_unreachable"
	}
	return "source_lag"
}

func productRowStepRequiresApproval(registry DefinitionRegistry, ref string, version int64, digest, currentStep, state string) (bool, string, error) {
	return productRowStepRequiresApprovalCached(registry, ref, version, digest, currentStep, state, nil)
}

func productRowStepRequiresApprovalCached(registry DefinitionRegistry, ref string, version int64, digest, currentStep, state string, cache map[string]RegisteredDefinition) (bool, string, error) {
	if ref == "" || digest == "" || version == 0 || state == "completed" || state == "cancelled" || state == "superseded" {
		return false, "", nil
	}
	key := fmt.Sprintf("%s\x00%d\x00%s", ref, version, digest)
	entry, ok := cache[key]
	if !ok {
		var err error
		entry, err = VerifyWorkflowDefinitionPin(registry, WorkflowDefinitionPin{Ref: ref, Version: version, Digest: digest})
		if err != nil {
			return false, "", err
		}
		if cache != nil {
			cache[key] = entry
		}
	}
	if currentStep == "start" {
		currentStep = entry.Definition.StepGraph.StartStep
	}
	step := workflowStep(entry.Definition, currentStep)
	if step == nil || step.Kind != WorkflowStepHumanCheckpoint {
		return false, currentStep, nil
	}
	for _, candidate := range step.Actions {
		for _, action := range entry.Definition.ActionDefinitions {
			if action.ID == candidate && action.Approval == ActionApprovalRequired {
				return true, currentStep, nil
			}
		}
	}
	return false, currentStep, nil
}

func (w productRowWork) attentionKind() string {
	if w.Lifecycle == "completed" || w.Lifecycle == "cancelled" || w.Lifecycle == "superseded" {
		return ""
	}
	switch {
	case w.ApprovalRequired:
		return ProductRowAttentionApprovalRequired
	case w.ActiveProblem:
		return ProductRowAttentionActiveProblem
	case w.Blocked:
		return ProductRowAttentionBlocked
	case w.Lifecycle == "in_progress":
		return ProductRowAttentionInProgress
	case w.Ready:
		return ProductRowAttentionReady
	default:
		return ""
	}
}

func (w productRowWork) actionable() bool { return w.attentionKind() != "" }

func productRowRelevantTime(w productRowWork) string {
	if w.Lifecycle == "in_progress" {
		return w.UpdatedAt
	}
	return w.CreatedAt
}

func lessProductRowWork(a, b productRowWork) bool {
	if a.Urgency != b.Urgency {
		return a.Urgency < b.Urgency
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if tA, tB := productRowRelevantTime(a), productRowRelevantTime(b); tA != tB {
		return tA < tB
	}
	return a.ID < b.ID
}

func productRowStageContext(defaultStage ProductRowStage, focus productRowWork) ProductRowStageContext {
	declared := make(map[string]ProductRowStage)
	for _, stage := range focus.StageOverrides {
		if stage != defaultStage {
			declared[fmt.Sprintf("%s\x00%s", stage.Maturity, stage.AudienceCommitment)] = stage
		}
	}
	if len(declared) == 0 {
		return ProductRowStageContext{Kind: "product_default"}
	}
	if len(declared) == 1 {
		for _, stage := range declared {
			if stage == defaultStage {
				return ProductRowStageContext{Kind: "product_default"}
			}
			stageCopy := stage
			return ProductRowStageContext{Kind: "single_focus_override", FocusOverride: &stageCopy}
		}
	}
	return ProductRowStageContext{Kind: "mixed"}
}

func applyProductRowNameSuffixes(rows []ProductRow) {
	counts := make(map[string]int)
	for _, row := range rows {
		counts[row.DisplayName]++
	}
	for i := range rows {
		if counts[rows[i].DisplayName] > 1 {
			rows[i].DisplayNameSuffix = " [" + rows[i].ProductID + "]"
		}
	}
}

// QueryProductRows returns one bounded C14 Product page. It performs exactly
// two SQL statements: one shared watermark read and one page-data statement.
// All Product/work aggregation, deduplication, and project counts happen in
// the latter statement; workflow registry resolution is in-memory and stage
// declarations come from the authoritative Project projection.
func (s *Store) QueryProductRows(ctx context.Context, req ProductRowRequest) (ProductRowResult, error) {
	var out ProductRowResult
	if err := validateProductRowSource(req.Source); err != nil {
		return out, err
	}
	limit, err := queryLimit(req.Limit)
	if err != nil {
		return out, err
	}
	tx, err := beginRead(ctx, s, productRowQueryID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	meta, err := queryMeta(ctx, tx, productRowQueryID, ResolvedScope{ProductID: req.Product}, []string{"display_name", "id"})
	if err != nil {
		return out, err
	}
	meta.ContractVersion = productRowContract
	out.ResultMeta = meta
	out.ObservedAt = meta.Freshness.ObservedAt

	var cursor productRowCursor
	if req.Cursor != "" {
		cursor, err = decodeProductRowCursor(req.Cursor)
		if err != nil {
			return out, err
		}
		if cursor.Product != req.Product || cursor.Watermark != meta.SourceVersionWatermark {
			return out, newFailure(KindInvalidCursor, productRowQueryID, "cursor scope, ordering, or source watermark is stale", false, "restart the bounded Product-row listing")
		}
	}

	args := []any{req.Product, req.Product, "", "", "", "", limit + 1}
	if req.Cursor != "" {
		args[2], args[3], args[4], args[5] = cursor.Name, cursor.Name, cursor.Name, cursor.ID
	}
	rows, err := tx.QueryContext(ctx, productRowPageSQL, args...)
	if err != nil {
		return out, wrapFailure(KindUnavailable, productRowQueryID, "cannot read Product rows", true, "retry once the database is readable", err)
	}
	defer rows.Close()

	type rawProductRow struct {
		row   ProductRow
		works []productRowWork
	}
	products := make([]rawProductRow, 0, limit+1)
	productIndex := make(map[string]int)
	registry := productRowWorkflowRegistry
	definitionCache := make(map[string]RegisteredDefinition)
	for rows.Next() {
		var p Product
		var workID, workKind, title, lifecycle, urgency, createdAt, updatedAt sql.NullString
		var priority, projectCount, definitionVersion sql.NullInt64
		var blocked, ready, activeProblem, overdueAwaits sql.NullBool
		var currentStep, definitionRef, definitionDigest, instanceState, stageOverrideMin, stageOverrideMax sql.NullString
		if err := rows.Scan(&p.ID, &p.DisplayName, &p.StageMaturity, &p.StageAudienceCommitment, &p.Version, &p.CreatedAt, &p.UpdatedAt,
			&workID, &workKind, &title, &lifecycle, &priority, &urgency, &createdAt, &updatedAt, &projectCount, &blocked, &ready, &activeProblem, &overdueAwaits,
			&currentStep, &definitionRef, &definitionVersion, &definitionDigest, &instanceState, &stageOverrideMin, &stageOverrideMax); err != nil {
			return out, wrapFailure(KindUnavailable, productRowQueryID, "cannot decode Product row", true, "retry once the database is readable", err)
		}
		idx, ok := productIndex[p.ID]
		if !ok {
			idx = len(products)
			productIndex[p.ID] = idx
			products = append(products, rawProductRow{row: ProductRow{ProductID: p.ID, DisplayName: p.DisplayName, Stage: ProductRowStage{Maturity: p.StageMaturity, AudienceCommitment: p.StageAudienceCommitment}}})
		}
		if !workID.Valid {
			continue
		}
		approvalRequired, stepLabel, err := productRowStepRequiresApprovalCached(registry, definitionRef.String, definitionVersion.Int64, definitionDigest.String, currentStep.String, instanceState.String, definitionCache)
		if err != nil {
			return out, newFailure(KindInvariantViolation, productRowQueryID, "workflow definition pin cannot be verified for Product-row projection", false, "repair or rebuild the workflow projection")
		}
		if lifecycle.String == "completed" || lifecycle.String == "cancelled" || lifecycle.String == "superseded" {
			approvalRequired = false
		}
		products[idx].works = append(products[idx].works, productRowWork{
			ID: workID.String, Kind: workKind.String, Title: title.String, Lifecycle: lifecycle.String, Priority: priority.Int64, Urgency: urgency.String,
			CreatedAt: createdAt.String, UpdatedAt: updatedAt.String, ProjectCount: int(projectCount.Int64), Blocked: blocked.Bool,
			Ready: ready.Bool, ActiveProblem: activeProblem.Bool, ApprovalRequired: approvalRequired, OverdueAwaits: overdueAwaits.Bool, WorkflowStepLabel: stepLabel,
			StageOverrides: parseProductRowStageOverrides(stageOverrideMin.String, stageOverrideMax.String),
		})
	}
	if err := rows.Err(); err != nil {
		return out, wrapFailure(KindUnavailable, productRowQueryID, "cannot scan Product rows", true, "retry once the database is readable", err)
	}
	sort.SliceStable(products, func(i, j int) bool {
		if products[i].row.DisplayName != products[j].row.DisplayName {
			return products[i].row.DisplayName < products[j].row.DisplayName
		}
		return products[i].row.ProductID < products[j].row.ProductID
	})

	reliance := productRowReliance(meta, req.Source)
	out.Authority = reliance.Authority
	out.Freshness.ObservedAt = reliance.ObservedAt
	out.Freshness.Age = reliance.Age
	out.Freshness.Stale = reliance.Stale
	for i := range products {
		products[i].row.Reliance = reliance
		unavailableReason, unavailable := productRowSourceUnavailable(reliance)
		if unavailable {
			products[i].row.ActionCounts = ProductRowActionCounts{
				State:       ProductRowCountsUnavailable,
				Unavailable: &ProductRowUnavailable{Reason: productRowUnavailableReason(reliance), Omissions: append([]string{}, reliance.Omissions...)},
			}
			products[i].row.FocusAbsentReason = unavailableReason
			continue
		}
		values := &ProductRowActionCountValues{}
		for _, work := range products[i].works {
			if work.Lifecycle == "in_progress" {
				values.InProgress++
			}
			if work.Blocked {
				values.Blocked++
			}
			if work.Ready {
				values.Ready++
			}
			if work.ActiveProblem {
				values.ActiveProblems++
			}
			if work.OverdueAwaits {
				values.OverdueAwaits++
			}
			if work.ApprovalRequired {
				values.ApprovalRequired++
			}
		}
		products[i].row.ActionCounts = ProductRowActionCounts{State: ProductRowCountsKnown, Values: values}
		focusCandidates := append([]productRowWork(nil), products[i].works...)
		sort.SliceStable(focusCandidates, func(a, b int) bool {
			rank := func(work productRowWork) int {
				switch work.attentionKind() {
				case ProductRowAttentionApprovalRequired:
					return 0
				case ProductRowAttentionActiveProblem:
					return 1
				case ProductRowAttentionBlocked:
					return 2
				case ProductRowAttentionInProgress:
					return 3
				case ProductRowAttentionReady:
					return 4
				default:
					return 5
				}
			}
			ra, rb := rank(focusCandidates[a]), rank(focusCandidates[b])
			if ra != rb {
				return ra < rb
			}
			return lessProductRowWork(focusCandidates[a], focusCandidates[b])
		})
		if len(focusCandidates) == 0 || !focusCandidates[0].actionable() {
			if len(products[i].works) == 0 {
				products[i].row.FocusAbsentReason = ProductRowFocusAuthoritativeEmpty
			} else {
				products[i].row.FocusAbsentReason = ProductRowFocusNoActionableWork
			}
			continue
		}
		focus := focusCandidates[0]
		products[i].row.Focus = &ProductRowFocus{
			WorkID: focus.ID, Title: focus.Title, WorkKind: focus.Kind, Lifecycle: focus.Lifecycle,
			AttentionKind: focus.attentionKind(), Priority: focus.Priority, WorkflowStepLabel: focus.WorkflowStepLabel,
			ProjectCount: focus.ProjectCount, StageContext: productRowStageContext(products[i].row.Stage, focus),
		}
		// Approval-gated focus carries the routing detail: which sessions
		// are waiting, oldest first. Read bounded and indexed (issue #72).
		if products[i].row.Focus.AttentionKind == ProductRowAttentionApprovalRequired {
			if blocked, blockedErr := blockedSessionsTx(ctx, tx, time.Now().UTC(), []string{products[i].row.ProductID}, 10); blockedErr == nil && len(blocked.Sessions) > 0 {
				products[i].row.Focus.BlockedSessions = blocked.Sessions
			}
		}
	}

	hasNext := len(products) > limit
	if hasNext {
		products = products[:limit]
	}
	out.Rows = make([]ProductRow, len(products))
	for i := range products {
		out.Rows[i] = products[i].row
	}
	applyProductRowNameSuffixes(out.Rows)
	if hasNext {
		last := out.Rows[len(out.Rows)-1]
		value, err := encodeProductRowCursor(productRowCursor{Version: 1, QueryID: productRowQueryID, Product: req.Product, Order: productRowOrdering, Watermark: meta.SourceVersionWatermark, Name: last.DisplayName, ID: last.ProductID})
		if err != nil {
			return out, err
		}
		out.NextCursor = &value
	}
	if out.Rows == nil {
		out.Rows = []ProductRow{}
	}
	return out, nil
}
