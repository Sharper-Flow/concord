package agent

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// An overlap that shares only Domains is the common case: the store's law,
// modification, and relation lists are empty. The envelope contract requires
// arrays for each, so a nil conversion marshals as null and the refusal the
// gate decided cannot cross the agent boundary (issue #783).
func TestArchitectureOnlyDomainOverlapRefusalIsDeliverable(t *testing.T) {
	failure := &store.Failure{
		Kind:           store.KindDomainOverlap,
		Op:             "workflow_domain_overlap",
		Detail:         "active Product-changing workflows have unresolved Domain overlap",
		RetrySafe:      false,
		RecoveryAction: "request_approval",
		DomainOverlap: &store.DomainOverlapFailure{
			Overlaps: []store.WorkflowDomainOverlap{{
				ProductID: "concord", FromWorkID: "work-a", ToWorkID: "work-b",
				FromContractVersion: 1, ToContractVersion: 1,
				SharedAffectedDomainIDs:   []string{"agent-surface"},
				OverlapClasses:            []string{"architecture"},
				ResolutionState:           "unresolved",
				RecoveryActions:           []string{"wait", "resolve_overlap", "terminal_work", "supersede_contract"},
				SharedAffectedDomainCount: 1,
			}},
			TotalOverlaps: 1, ReturnedOverlaps: 1,
		},
	}
	out := failureEnvelope(NewBase("overlap-1", "concord_work_transition", "workflow_action"), failure)
	if out.Error == nil || out.Error.Kind != "domain_overlap" {
		t.Fatalf("error=%+v, want kind domain_overlap", out.Error)
	}
	raw, err := out.Encode()
	if err != nil {
		t.Fatalf("an architecture-only overlap refusal must be deliverable, got %v", err)
	}
	var decoded struct {
		Error struct {
			DomainOverlap struct {
				Overlaps []struct {
					FromWorkID                string   `json:"from_work_id"`
					ToWorkID                  string   `json:"to_work_id"`
					SharedLawIDs              []string `json:"shared_law_ids"`
					SharedDomainModifications []string `json:"shared_domain_modifications"`
					SharedRelationTuples      []any    `json:"shared_relation_tuples"`
					ResolutionState           string   `json:"resolution_state"`
					RecoveryActions           []string `json:"recovery_actions"`
				} `json:"overlaps"`
			} `json:"domain_overlap"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Error.DomainOverlap.Overlaps
	if len(got) != 1 || got[0].FromWorkID != "work-a" || got[0].ToWorkID != "work-b" || got[0].ResolutionState != "unresolved" || len(got[0].RecoveryActions) != 4 {
		t.Fatalf("overlap detail lost: %s", raw)
	}
	if got[0].SharedLawIDs == nil || got[0].SharedDomainModifications == nil || got[0].SharedRelationTuples == nil {
		t.Fatalf("empty shared lists must marshal as arrays, not null: %s", raw)
	}
}
