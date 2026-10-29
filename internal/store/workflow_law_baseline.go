package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
)

// The owning-start inventory stays immutable. Completion attribution advances
// only the comparison cursor, never this recorded before-state.
type workflowLawBaseline struct {
	Home     workflowLawBaselineSource    `json:"home"`
	Sources  []workflowLawBaselineSource  `json:"sources"`
	NoHome   bool                         `json:"no_home,omitempty"`
	Laws     []workflowLawState           `json:"laws"`
	Registry *workflowLawRegistrySnapshot `json:"registry"`
}

type workflowLawBaselineSource struct {
	ProjectID string `json:"project_id"`
	LocatorID string `json:"locator_id"`
}

func readWorkflowLawInventoryTx(ctx context.Context, q queryer, workID string) (*workflowLawBaseline, error) {
	b := &workflowLawBaseline{Laws: []workflowLawState{}}
	project, locator, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		var f *Failure
		if failureAs(err, &f) && f.Kind == KindUnknownScope {
			b.NoHome = true
			return b, nil
		}
		return nil, err
	}
	b.Home = workflowLawBaselineSource{ProjectID: project, LocatorID: locator}
	sources := []KnowledgeHome{{HomeProjectID: project, HomeLocatorID: locator}}
	for _, source := range sources {
		b.Sources = append(b.Sources, workflowLawBaselineSource{ProjectID: source.HomeProjectID, LocatorID: source.HomeLocatorID})
		rows, err := q.QueryContext(ctx, `SELECT law_id,content_hash,authority_tier FROM law_subjects WHERE home_project_id=? AND home_locator_id=? ORDER BY law_id`, source.HomeProjectID, source.HomeLocatorID)
		if err != nil {
			return nil, workflowProjectionError(err, "cannot read the start law inventory")
		}
		var states []workflowLawState
		for rows.Next() {
			var state workflowLawState
			if err := rows.Scan(&state.LawID, &state.ContentHash, &state.Tier); err != nil {
				_ = rows.Close()
				return nil, workflowProjectionError(err, "cannot decode the start law inventory")
			}
			states = append(states, state)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, workflowProjectionError(err, "cannot finish the start law inventory")
		}
		for _, state := range states {
			domains, err := readWorkflowLawDomainsTx(ctx, q, lawReferenceResolution{source: source, lawID: state.LawID})
			if err != nil {
				return nil, err
			}
			state.DomainIDs = domains
			state.LawID = source.HomeProjectID + "/" + state.LawID
			b.Laws = append(b.Laws, state)
		}
	}
	slices.SortFunc(b.Laws, func(a, b workflowLawState) int { return strings.Compare(a.LawID, b.LawID) })
	for i := 1; i < len(b.Laws); i++ {
		if b.Laws[i-1].LawID == b.Laws[i].LawID {
			return nil, newFailure(KindKnowledgeAmbiguous, "workflow_law_change", "the source graph repeats a qualified law identity: "+b.Laws[i].LawID, false, "resolve the duplicate law sources")
		}
	}
	b.Registry, err = readWorkflowLawRegistrySnapshot(ctx, q, workID)
	return b, err
}

func initializeWorkflowLawBaselineTx(ctx context.Context, tx *sql.Tx, workID string) error {
	baseline, err := readWorkflowLawInventoryTx(ctx, tx, workID)
	if err != nil {
		return err
	}
	if !baseline.NoHome {
		if err := requireWorkflowLawInventoryProof(ctx, tx, baseline.Home.ProjectID, baseline.Home.LocatorID); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(baseline)
	if err != nil || len(encoded) > 4*1024*1024 {
		return newFailure(KindInvalidPayload, "workflow_law_change", "the start law inventory exceeds its bounded storage", false, "reduce the Product source inventory before starting work")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO workflow_law_baselines(work_id,baseline_json,start_effect_seq) SELECT ?,?,COALESCE(max(effect_seq),0) FROM effect_receipts`, workID, string(encoded))
	if err != nil {
		return workflowProjectionError(err, "cannot record the owning start law baseline")
	}
	return nil
}

func readWorkflowLawBaselineTx(ctx context.Context, q queryer, workID string) (*workflowLawBaseline, *workflowLawBaseline, error) {
	var encoded string
	var startSeq int64
	if err := q.QueryRowContext(ctx, `SELECT baseline_json,start_effect_seq FROM workflow_law_baselines WHERE work_id=?`, workID).Scan(&encoded, &startSeq); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, newFailure(KindMissingEvidence, "workflow_law_change", "the item has no law before-state anchored at its owning start", false, "retain the historical item without inventing a baseline; cancel and re-capture the remaining work, or use boundary-authenticated outside repair and reconcile its delivery evidence")
		}
		return nil, nil, workflowProjectionError(err, "cannot read the owning start law baseline")
	}
	var before workflowLawBaseline
	if err := json.Unmarshal([]byte(encoded), &before); err != nil {
		return nil, nil, workflowProjectionError(err, "cannot decode the owning start law baseline")
	}
	after, err := readWorkflowLawInventoryTx(ctx, q, workID)
	if err != nil {
		return nil, nil, err
	}
	if before.NoHome != after.NoHome || !reflect.DeepEqual(before.Home, after.Home) || !reflect.DeepEqual(before.Sources, after.Sources) {
		return nil, nil, newFailure(KindMissingEvidence, "workflow_law_change", "the item's law source graph differs from its owning start", false, "restore the pinned source graph; a new graph has no verified start baseline")
	}
	if !after.NoHome {
		if err := requireWorkflowLawInventoryProof(ctx, q, after.Home.ProjectID, after.Home.LocatorID); err != nil {
			return nil, nil, err
		}
	}
	if err := applyCompletedWorkflowLawEnactmentsTx(ctx, q, workID, startSeq, &before); err != nil {
		return nil, nil, err
	}
	return &before, after, nil
}

// Inventory admission has its own seam: unlike a mandate read, it must prove
// the complete inventory even when the contract names no law.
func requireWorkflowLawInventoryProof(ctx context.Context, q queryer, project, locator string) error {
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: project, HomeLocatorID: locator})
	if err != nil {
		return err
	}
	if productID != "" {
		return requireKnowledgeSourceSetProof(ctx, q, project, locator)
	}
	homes, err := projectCanonicalHomeCandidates(ctx, q, project)
	if err != nil {
		return err
	}
	for _, home := range homes {
		if home.HomeLocatorID == locator {
			return requireKnowledgeSourcesProof(ctx, q, "", []KnowledgeHome{home})
		}
	}
	return newFailure(KindUnknownScope, "workflow_law_change", "the standalone inventory has no canonical law home", false, "resolve one canonical Git knowledge home before verifying the inventory")
}

func workflowLawInventoryMap(b *workflowLawBaseline) map[string]workflowLawState {
	states := make(map[string]workflowLawState, len(b.Laws))
	for _, state := range b.Laws {
		states[state.LawID] = state
	}
	return states
}

func workflowLawInventoryDifferences(before, after *workflowLawBaseline) []string {
	old, now := workflowLawInventoryMap(before), workflowLawInventoryMap(after)
	var changed []string
	for id, state := range old {
		if other, present := now[id]; !present || state.differs(other) {
			changed = append(changed, id)
		}
	}
	for id := range now {
		if _, present := old[id]; !present {
			changed = append(changed, id)
		}
	}
	return uniqueSorted(changed)
}

// Canonical source identities keep bare/qualified aliases from hiding a delta.
// A missing new law belongs to the designated home; an ambiguous bare ID refuses.
func workflowLawInventoryReference(reference string, before, after *workflowLawBaseline) (string, error) {
	project, lawID, qualified, err := parseQualifiedKnowledgeID("workflow_law_change", reference)
	if err != nil {
		return "", err
	}
	if qualified {
		for _, source := range before.Sources {
			if source.ProjectID == project {
				return reference, nil
			}
		}
		return "", newFailure(KindUnknownScope, "workflow_law_change", "law reference is outside the start source graph: "+reference, false, "use the pinned law sources")
	}
	var matches []string
	for _, inventory := range []*workflowLawBaseline{before, after} {
		for _, state := range inventory.Laws {
			if strings.HasSuffix(state.LawID, "/"+lawID) {
				matches = append(matches, state.LawID)
			}
		}
	}
	matches = uniqueSorted(matches)
	if len(matches) > 1 {
		return "", newFailure(KindKnowledgeAmbiguous, "workflow_law_change", "law reference is held by more than one start source: "+reference, false, "qualify the law reference")
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return before.Home.ProjectID + "/" + lawID, nil
}
