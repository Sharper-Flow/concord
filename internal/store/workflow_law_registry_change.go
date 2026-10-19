package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// A registry edit retains compact before-state fingerprints. At enactment,
// the count follows entries and relations actually changed in the projection,
// not just the contract's declared registry scope. Commit watermarks and the
// whole-registry hash are not entry content.
type workflowLawRegistrySnapshot struct {
	RootDomainID string                   `json:"root_domain_id"`
	Entries      []workflowLawRegistryPin `json:"entries"`
}

type workflowLawRegistryPin struct {
	DomainID       string `json:"domain_id"`
	Kind           string `json:"kind,omitempty"`
	TargetDomainID string `json:"target_domain_id,omitempty"`
	ContentHash    string `json:"content_hash"`
}

func (p workflowLawRegistryPin) key() string {
	return fmt.Sprintf("%q/%q/%q", p.DomainID, p.Kind, p.TargetDomainID)
}

func workflowLawRegistryEntryHash(fields []string) string {
	data, _ := json.Marshal(fields) // A string slice has no unsupported JSON values.
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

func readWorkflowLawRegistrySnapshot(ctx context.Context, q queryer, workID string) (*workflowLawRegistrySnapshot, error) {
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	productID, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID})
	if err != nil {
		return nil, err
	}
	result := &workflowLawRegistrySnapshot{Entries: []workflowLawRegistryPin{}}
	if err := q.QueryRowContext(ctx, `SELECT root_domain_id FROM domain_registries WHERE product_id=?`, productID).Scan(&result.RootDomainID); err != nil {
		if err == sql.ErrNoRows {
			return nil, newFailure(KindMissingEvidence, "workflow_law_change", "the changed Domain registry has no projection", false, "rebuild the Product's Domain registry")
		}
		return nil, workflowProjectionError(err, "cannot read the changed Domain registry")
	}
	rows, err := q.QueryContext(ctx, `SELECT domain_id,name,purpose,COALESCE(parent_domain_id,''),status FROM domains WHERE product_id=? ORDER BY domain_id`, productID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read registry entries for the law change")
	}
	for rows.Next() {
		var id, name, purpose, parent, status string
		if err := rows.Scan(&id, &name, &purpose, &parent, &status); err != nil {
			_ = rows.Close()
			return nil, workflowProjectionError(err, "cannot decode the changed registry's entries")
		}
		result.Entries = append(result.Entries, workflowLawRegistryPin{DomainID: id, ContentHash: workflowLawRegistryEntryHash([]string{name, purpose, parent, status})})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, workflowProjectionError(err, "cannot finish reading the changed registry's entries")
	}
	if err := rows.Close(); err != nil {
		return nil, workflowProjectionError(err, "cannot close the changed registry's entries")
	}
	rows, err = q.QueryContext(ctx, `SELECT r.source_domain_id,r.kind,r.target_domain_id,r.state,COALESCE(g.law_id,'')
FROM domain_architecture_relations r LEFT JOIN domain_relation_governing_laws g
ON g.home_project_id=r.home_project_id AND g.home_locator_id=r.home_locator_id AND g.product_id=r.product_id AND g.source_domain_id=r.source_domain_id AND g.kind=r.kind AND g.target_domain_id=r.target_domain_id
WHERE r.product_id=? ORDER BY r.source_domain_id,r.kind,r.target_domain_id,g.law_id`, productID)
	if err != nil {
		return nil, workflowProjectionError(err, "cannot read registry relations for the law change")
	}
	var current workflowLawRegistryPin
	var fields []string
	appendRelation := func() {
		if current.Kind != "" {
			current.ContentHash = workflowLawRegistryEntryHash(fields)
			result.Entries = append(result.Entries, current)
		}
	}
	for rows.Next() {
		var pin workflowLawRegistryPin
		var state, lawID string
		if err := rows.Scan(&pin.DomainID, &pin.Kind, &pin.TargetDomainID, &state, &lawID); err != nil {
			_ = rows.Close()
			return nil, workflowProjectionError(err, "cannot decode the changed registry's relations")
		}
		if pin.key() != current.key() {
			appendRelation()
			current = pin
			fields = []string{state}
		}
		if lawID != "" {
			fields = append(fields, lawID)
		}
	}
	appendRelation()
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, workflowProjectionError(err, "cannot finish reading the changed registry's relations")
	}
	if err := rows.Close(); err != nil {
		return nil, workflowProjectionError(err, "cannot close the changed registry's relations")
	}
	slices.SortFunc(result.Entries, func(a, b workflowLawRegistryPin) int {
		return strings.Compare(a.key(), b.key())
	})
	return result, nil
}

func workflowLawRegistryChangedDomains(before, after *workflowLawRegistrySnapshot) []string {
	old := map[string]string{}
	now := map[string]string{}
	for _, pin := range before.Entries {
		old[pin.key()] = pin.ContentHash
	}
	for _, pin := range after.Entries {
		now[pin.key()] = pin.ContentHash
	}
	var domains []string
	collect := func(state *workflowLawRegistrySnapshot, other map[string]string) {
		for _, pin := range state.Entries {
			if pin.ContentHash == other[pin.key()] {
				continue
			}
			if pin.DomainID != state.RootDomainID {
				domains = append(domains, pin.DomainID)
			}
			if pin.TargetDomainID != "" && pin.TargetDomainID != state.RootDomainID {
				domains = append(domains, pin.TargetDomainID)
			}
		}
	}
	collect(before, now)
	collect(after, old)
	return uniqueSorted(domains)
}

func validateWorkflowLawRegistrySnapshot(snapshot *workflowLawRegistrySnapshot) error {
	if snapshot.RootDomainID == "" || snapshot.Entries == nil {
		return newFailure(KindInvalidPayload, "fold_event", "law_change registry before-state requires a root and entry list", false, "record the projected registry before-state")
	}
	previous := ""
	for _, pin := range snapshot.Entries {
		if pin.DomainID == "" || pin.key() <= previous || validateContentHash(pin.ContentHash) != nil || (pin.Kind == "") != (pin.TargetDomainID == "") {
			return newFailure(KindInvalidPayload, "fold_event", "law_change registry before-state must contain sorted unique entry fingerprints", false, "record each projected registry entry once")
		}
		previous = pin.key()
	}
	return nil
}
