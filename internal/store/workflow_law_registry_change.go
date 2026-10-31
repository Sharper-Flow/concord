package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Compact registry fingerprints distinguish each Domain and typed relation.
// Commit watermarks and the whole-registry hash are not entry content.
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

// readWorkflowLawRegistrySnapshot projects the verified committed Domain
// registry into compact per-entry fingerprints. The registry comes from the
// pre-transaction proof's law-folder snapshot: the same committed registry the
// Domain reads and the law loader answer from, at the same frozen commit. A
// home the proof does not carry refuses; a Product whose law home is not a
// designated Product home keeps its nil-registry answer.
func readWorkflowLawRegistrySnapshot(ctx context.Context, q queryer, workID string) (*workflowLawRegistrySnapshot, error) {
	homeProjectID, homeLocatorID, err := workflowLawHome(ctx, q, workID)
	if err != nil {
		return nil, err
	}
	if _, _, err := resolveKnowledgeSourceRole(ctx, q, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}); err != nil {
		var f *Failure
		if failureAs(err, &f) && f.Kind == KindUnknownScope {
			return nil, nil
		}
		return nil, err
	}
	snapshot, err := lawSnapshotForSource(ctx, KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: homeLocatorID}, "workflow_law_change")
	if err != nil {
		return nil, err
	}
	registry := snapshot.Registry()
	result := &workflowLawRegistrySnapshot{RootDomainID: registry.RootDomainID, Entries: []workflowLawRegistryPin{}}
	for _, domain := range registry.Domains {
		result.Entries = append(result.Entries, workflowLawRegistryPin{
			DomainID:    domain.DomainID,
			ContentHash: workflowLawRegistryEntryHash([]string{domain.Name, domain.Purpose, domain.ParentDomainID, domainRegistryStatus(domain.Status)}),
		})
	}
	for _, domain := range registry.Domains {
		for _, relation := range domain.ArchitectureRelations {
			fields := []string{domainRegistryRelationState(relation.State)}
			fields = append(fields, orderedStrings(relation.GoverningLawIDs)...)
			result.Entries = append(result.Entries, workflowLawRegistryPin{
				DomainID:       domain.DomainID,
				Kind:           relation.Kind,
				TargetDomainID: relation.TargetDomainID,
				ContentHash:    workflowLawRegistryEntryHash(fields),
			})
		}
	}
	slices.SortFunc(result.Entries, func(a, b workflowLawRegistryPin) int {
		return strings.Compare(a.key(), b.key())
	})
	return result, nil
}

// Scope follows the registry's typed entry or relation identity, not a count
// or the endpoints alone. Naming two Domains does not authorize their relation.
func checkWorkflowLawRegistryScope(before, after *workflowLawRegistrySnapshot, binding *WorkflowArchitectureBinding) error {
	if before == nil {
		before = &workflowLawRegistrySnapshot{}
	}
	if after == nil {
		after = &workflowLawRegistrySnapshot{}
	}
	declaredDomain := func(id string) bool { return id == "" || binding != nil && slices.Contains(binding.DomainModifies, id) }
	if before.RootDomainID != after.RootDomainID && (!declaredDomain(before.RootDomainID) || !declaredDomain(after.RootDomainID)) {
		return newFailure(KindOutcomeMismatch, "complete_workflow", "the enacted registry root changed outside the declared Domain scope", false, "restore the registry root or declare both root identities")
	}
	old, now := map[string]workflowLawRegistryPin{}, map[string]workflowLawRegistryPin{}
	for _, pin := range before.Entries {
		old[pin.key()] = pin
	}
	for _, pin := range after.Entries {
		now[pin.key()] = pin
	}
	check := func(entries, other map[string]workflowLawRegistryPin) error {
		for _, key := range slices.Sorted(maps.Keys(entries)) {
			pin := entries[key]
			if candidate, exists := other[key]; exists && candidate.ContentHash == pin.ContentHash {
				continue
			}
			if pin.Kind == "" && declaredDomain(pin.DomainID) {
				continue
			}
			if pin.Kind != "" && binding != nil && slices.ContainsFunc(binding.DomainRelationModifies, func(relation WorkflowDomainRelationModification) bool {
				return relation.SourceDomainID == pin.DomainID && relation.Kind == pin.Kind && relation.TargetDomainID == pin.TargetDomainID
			}) {
				continue
			}
			return newFailure(KindOutcomeMismatch, "complete_workflow", "the enacted registry entry is outside the declared scope: "+pin.key(), false, "restore the registry entry or declare its exact Domain or relation identity")
		}
		return nil
	}
	if err := check(old, now); err != nil {
		return err
	}
	return check(now, old)
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
