package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Selected research in the shared work context. One closed selection tuple
// names an exact pack, revision, finding, and declared Domain; the
// canonical selected-read helper resolves it, and unresolved state becomes
// typed view data, never a fabricated finding or a swallowed error.
// Selections store references only: no corpus body, no second budget, no
// cache, and no research event stream.

// The closed resolution states of one research entry. Exactly the resolved
// state carries the native finding and its linked source descriptors; every
// other state carries references and provenance only.
const (
	WorkContextResearchStateResolved = "resolved"
	WorkContextResearchStateMissing  = "missing"
	WorkContextResearchStateStale    = "stale"
	WorkContextResearchStateRebound  = "rebound"
	WorkContextResearchStateInactive = "inactive"
)

// WorkContextResearchSelection is one closed selection the declaration
// event stores: references and the declared Domain, never research content.
// The root Domain carries a product-wide rationale exactly as the reading
// and finding entries do.
type WorkContextResearchSelection struct {
	PackID               string `json:"pack_id"`
	Revision             int64  `json:"revision"`
	FindingID            string `json:"finding_id"`
	DomainID             string `json:"domain_id"`
	ProductWideRationale string `json:"product_wide_rationale,omitempty"`
}

// WorkContextResearchFindingView is one assembled research entry of the
// current view. The provenance fields identify the selection event and
// ordinal — the event that authored the reference, never a claim that the
// research body came from that event. Required is derived from the
// consuming work's present binding, and a missing binding is fail-closed
// required: absence cannot establish optional reliance.
type WorkContextResearchFindingView struct {
	Selection         WorkContextResearchSelection `json:"selection"`
	ViewID            string                       `json:"view_id"`
	SelectionEventID  string                       `json:"selection_event_id"`
	SelectionEventSeq int64                        `json:"selection_event_seq"`
	Ordinal           int                          `json:"ordinal"`
	Required          bool                         `json:"required"`
	State             string                       `json:"state"`
	// Finding and Sources carry the exact selected content exactly when
	// State is resolved, and stay absent on every unresolved state: the
	// native finding with its own kind, confidence, freshness, status, and
	// scopes unchanged, and every linked source descriptor once, sorted by
	// source ID, with no unrelated source. Sources is a present pointer so
	// a resolved finding with no linked sources stays accurately unsourced
	// as an empty array rather than a missing member.
	Finding *ResearchFinding  `json:"finding,omitempty"`
	Sources *[]ResearchSource `json:"sources,omitempty"`
}

// readWorkContextResearchSelections resolves every authored selection in
// its authored order through the canonical selected-read helper inside the
// caller's transaction. The deterministic precedence is: missing pack,
// revision, finding, linked source, or consuming binding, then a rebound
// binding, then stale revision or finding freshness, then an inactive
// finding, then resolved. Only recognized not-found research failures map
// to missing; database and decoding errors propagate, and the canonical
// bounded-read failure translates to the view limit kind.
func readWorkContextResearchSelections(ctx context.Context, q queryer, workID, selectionEventID string, selectionEventSeq int64, selections []WorkContextResearchSelection) ([]WorkContextResearchFindingView, error) {
	if len(selections) == 0 {
		return nil, nil
	}
	views := make([]WorkContextResearchFindingView, 0, len(selections))
	for ordinal, selection := range selections {
		entry := WorkContextResearchFindingView{
			Selection:         selection,
			ViewID:            fmt.Sprintf("research:%d:%d", selectionEventSeq, ordinal),
			SelectionEventID:  selectionEventID,
			SelectionEventSeq: selectionEventSeq,
			Ordinal:           ordinal,
			Required:          true,
			State:             WorkContextResearchStateMissing,
		}
		var bindingRevision int64
		var bindingRequired bool
		bindingErr := q.QueryRowContext(ctx, `SELECT revision,required FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?`, selection.PackID, workID).Scan(&bindingRevision, &bindingRequired)
		if bindingErr == sql.ErrNoRows {
			// The consuming binding is absent: the reader cannot establish
			// optional reliance, so the entry stays fail-closed required.
			views = append(views, entry)
			continue
		}
		if bindingErr != nil {
			return nil, wrapFailure(KindUnavailable, "work_context_read", "cannot read the consuming research binding", true, "retry once the research projection is readable", bindingErr)
		}
		entry.Required = bindingRequired
		pack, readErr := readResearchSelectionTx(ctx, q, ResearchReadRequest{
			PackID: selection.PackID, Revision: selection.Revision, FindingIDs: []string{selection.FindingID}, Limit: 1000, // the canonical selected-read completeness bound, not a research budget
		})
		if readErr != nil {
			if isResearchNotFound(readErr) {
				views = append(views, entry)
				continue
			}
			return nil, translateWorkContextResearchReadError(readErr)
		}
		revision := pack.Revisions[0]
		finding := revision.Findings[0]
		switch {
		case bindingRevision != selection.Revision:
			// The present binding moved to another revision: the entry
			// reports the binding's required flag and never its content.
			entry.State = WorkContextResearchStateRebound
		case revision.Freshness != ResearchCurrent || finding.Freshness != ResearchCurrent:
			entry.State = WorkContextResearchStateStale
		case finding.Status != FindingActive:
			entry.State = WorkContextResearchStateInactive
		default:
			entry.State = WorkContextResearchStateResolved
			entry.Finding = &finding
			sources := revision.Sources
			if sources == nil {
				sources = []ResearchSource{}
			}
			entry.Sources = &sources
		}
		views = append(views, entry)
	}
	return views, nil
}

// validateWorkContextResearchSelectionsTx is the live authoring validation:
// every selection resolves against the action transaction's own state, so a
// declaration with no pin, another revision, a missing finding, or a
// foreign Domain refuses without effects. The same-action research bindings
// are already recorded when this runs, because the action binds reliance
// before the semantic constructor assembles its events.
func validateWorkContextResearchSelectionsTx(ctx context.Context, tx *sql.Tx, workID string, selections []WorkContextResearchSelection) error {
	for _, selection := range selections {
		var revision int64
		err := tx.QueryRowContext(ctx, `SELECT revision FROM active_research_consumers WHERE pack_id=? AND consumer_work_id=?`, selection.PackID, workID).Scan(&revision)
		if err == sql.ErrNoRows {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("research selection %s has no consuming binding", selection.PackID), "declare research_bindings for the pack on this action, or pin it before declaring")
		}
		if err != nil {
			return wrapFailure(KindUnavailable, "work_context", "cannot read the consuming research binding", true, "retry once the research projection is readable", err)
		}
		if revision != selection.Revision {
			return workContextFailure(KindInvalidPayload, "work_context", fmt.Sprintf("research selection pins %s revision %d while the consuming binding holds revision %d", selection.PackID, selection.Revision, revision), "pin the bound revision or rebind the pack to the selected revision")
		}
		if _, err := readResearchSelectionTx(ctx, tx, ResearchReadRequest{
			PackID: selection.PackID, Revision: selection.Revision, FindingIDs: []string{selection.FindingID}, Limit: 1000, // the canonical selected-read completeness bound, not a research budget
		}); err != nil {
			return translateWorkContextResearchReadError(err)
		}
		if err := ValidateWorkContextDomainTx(ctx, tx, workID, selection.DomainID, selection.ProductWideRationale); err != nil {
			return err
		}
	}
	return nil
}

// isResearchNotFound reports whether an error is one of the canonical
// selected-read helper's recognized not-found refusals. The typed kind and
// operation decide; no error-string matching runs here.
func isResearchNotFound(err error) bool {
	var failure *Failure
	if !errors.As(err, &failure) {
		return false
	}
	return failure.Kind == KindProjectionNotFound && failure.Op == "research_mutation"
}

// translateWorkContextResearchReadError is the context bridge's bounded-read
// translation owner: the canonical helper's bounded-read refusal — the typed
// KindInvalidOperation pair with operation research_read — becomes the
// view's own KindLimitExceeded while preserving the source cause. Every
// other invalid-operation failure propagates unchanged.
func translateWorkContextResearchReadError(err error) error {
	var failure *Failure
	if errors.As(err, &failure) && failure.Kind == KindInvalidOperation && failure.Op == "research_read" {
		return wrapFailure(KindLimitExceeded, "work_context_read", failure.Detail, false, "request a narrower research selection or a larger supported bound", err)
	}
	return err
}
