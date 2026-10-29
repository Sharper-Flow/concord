package agent

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

func TestLawOnlyDomainOverlapRefusalIsDeliverable(t *testing.T) {
	t.Parallel()
	failure := &store.Failure{
		Kind: store.KindDomainOverlap, Op: "workflow_domain_overlap",
		Detail:         "active Product-changing workflows have unresolved Domain overlap",
		RecoveryAction: "reconcile_operation",
		DomainOverlap: &store.DomainOverlapFailure{
			Overlaps: []store.WorkflowDomainOverlap{{
				ProductID: "concord", FromWorkID: "work-a", ToWorkID: "work-b",
				FromContractVersion: 1, ToContractVersion: 1,
				SharedLawIDs: []string{"spec:one"}, SharedLawCount: 1,
				OverlapClasses:  []string{"law_write"},
				ResolutionState: "unresolved",
				RecoveryActions: []string{"wait", "resolve_overlap", "terminal_work", "supersede_contract"},
			}},
			TotalOverlaps: 1, ReturnedOverlaps: 1,
		},
	}
	out := failureEnvelope(agentcontract.NewBase("overlap-law", "concord_work_transition", "workflow_action"), failure)
	if out.Error == nil || out.Error.Kind != "domain_overlap" {
		t.Fatalf("error=%+v, want kind domain_overlap", out.Error)
	}
	raw, err := out.Encode()
	if err != nil {
		t.Fatalf("a law-only overlap refusal must be deliverable, got %v", err)
	}
	var decoded agentcontract.Envelope
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Error == nil || decoded.Error.Kind != "domain_overlap" || decoded.Error.DomainOverlap == nil || len(decoded.Error.DomainOverlap.Overlaps) != 1 {
		t.Fatalf("overlap refusal lost: %s", raw)
	}
	row := decoded.Error.DomainOverlap.Overlaps[0]
	if row.SharedAffectedDomainIDs == nil || len(row.SharedAffectedDomainIDs) != 0 || len(row.SharedLawIDs) != 1 || row.SharedLawIDs[0] != "spec:one" || row.SharedLawCount != 1 || row.FromWorkID != "work-a" || row.ToWorkID != "work-b" {
		t.Fatalf("law-only overlap detail lost or Domain list is null: %s", raw)
	}

	t.Run("null_domain_list", func(t *testing.T) {
		out.Error.DomainOverlap.Overlaps[0].SharedAffectedDomainIDs = nil
		if _, err := out.Encode(); err == nil {
			t.Fatal("a null Domain list must fail envelope validation")
		}
	})
}

// An overlap that shares only Domains is the common case: the store's law,
// modification, and relation lists are empty. The envelope contract requires
// arrays for each, so a nil conversion marshals as null and the refusal the
// gate decided cannot cross the agent boundary (issue #783).
func TestArchitectureOnlyDomainOverlapRefusalIsDeliverable(t *testing.T) {
	t.Parallel()
	failure := &store.Failure{
		Kind:           store.KindDomainOverlap,
		Op:             "workflow_domain_overlap",
		Detail:         "active Product-changing workflows have unresolved Domain overlap",
		RetrySafe:      false,
		RecoveryAction: "reconcile_operation",
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
	out := failureEnvelope(agentcontract.NewBase("overlap-1", "concord_work_transition", "workflow_action"), failure)
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

// The envelope is the authority on how many overlaps a refusal may carry. This
// pins its side of the coupling: a producer that emits more than twenty makes
// the refusal undeliverable, whatever the byte budget allows. The store's bound
// honors the same number (internal/store/workflow_domain_overlap.go,
// maxWorkflowOverlapDetailItems), and a drift on either side fails a test.
func TestDomainOverlapRefusalAboveEnvelopeCapIsRefused(t *testing.T) {
	overlaps := make([]store.WorkflowDomainOverlap, 0, 21)
	for i := 0; i < 21; i++ {
		overlaps = append(overlaps, store.WorkflowDomainOverlap{
			ProductID: "concord", FromWorkID: "work-self", ToWorkID: "work-peer",
			FromContractVersion: 1, ToContractVersion: 1,
			SharedAffectedDomainIDs:   []string{"agent-surface"},
			OverlapClasses:            []string{"architecture"},
			ResolutionState:           "unresolved",
			RecoveryActions:           []string{"wait", "resolve_overlap", "terminal_work", "supersede_contract"},
			SharedAffectedDomainCount: 1,
		})
	}
	failure := &store.Failure{
		Kind: store.KindDomainOverlap, Op: "workflow_domain_overlap",
		Detail:    "active Product-changing workflows have unresolved Domain overlap",
		RetrySafe: false, RecoveryAction: "reconcile_operation",
		DomainOverlap: &store.DomainOverlapFailure{
			Overlaps: overlaps, TotalOverlaps: 21, ReturnedOverlaps: 21, Truncated: false,
		},
	}
	out := failureEnvelope(agentcontract.NewBase("overlap-cap", "concord_work_transition", "workflow_action"), failure)
	if _, err := out.Encode(); err == nil {
		t.Fatal("an envelope carrying twenty-one overlaps must be refused, not delivered")
	}
}
