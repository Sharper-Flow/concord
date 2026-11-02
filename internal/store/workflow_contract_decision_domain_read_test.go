package store

import (
	"context"
	"reflect"
	"testing"
)

// The active and the historical contract-version selector share one contract
// query and one canonical decode. These tests pin the read side alone: each
// version answers its own recorded fact through either selector, NULL stays
// historical absence, and an explicit [] stays engineering.

func TestHistoricalReadCarriesContractDecisionDomains(t *testing.T) {
	t.Parallel()

	t.Run("each version answers its own recorded fact", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		defer s.Close()
		seedDecisionDomainsItemFatal(t, s, "decision-historical-read", []string{"product"})
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='decision-historical-read'`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		successor := pContractSuccessor(t, s, "decision-historical-read", "the corrected decision-bearing premise")
		successor["decision_domains"] = []string{"ui_ux"}
		owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/decision-historical-read", ActorClass: ActorAgent}
		if err := reviseContractRecord(t, s, "decision-historical-read", owner, successor, version, nil); err != nil {
			t.Fatalf("successor correction refused: %v", err)
		}
		// Current authoring excludes preference; historical readers still decode it.
		if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_contracts SET decision_domains='["preference","ui_ux"]' WHERE work_id='decision-historical-read' AND contract_version=2; DELETE FROM write_guard`); err != nil {
			t.Fatal(err)
		}
		for _, testCase := range []struct {
			name            string
			selector        int64
			wantDomains     []string
			wantSuperseded  int64
			wantSupersededB bool
		}{
			{name: "superseded predecessor", selector: 1, wantDomains: []string{"product"}, wantSuperseded: 2, wantSupersededB: true},
			{name: "active successor", selector: 2, wantDomains: []string{"preference", "ui_ux"}},
		} {
			projection, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: "decision-historical-read", ContractVersion: testCase.selector})
			if err != nil {
				t.Fatalf("%s historical read: %v", testCase.name, err)
			}
			if projection.Contract == nil {
				t.Fatalf("%s historical read carried no contract", testCase.name)
			}
			if projection.Contract.Version != testCase.selector {
				t.Fatalf("%s historical read returned contract version %d, want %d", testCase.name, projection.Contract.Version, testCase.selector)
			}
			if !reflect.DeepEqual(projection.Contract.DecisionDomains, testCase.wantDomains) {
				t.Fatalf("%s historical read projected %v, want %v", testCase.name, projection.Contract.DecisionDomains, testCase.wantDomains)
			}
			if testCase.wantSupersededB {
				if projection.Contract.SupersededBy == nil || *projection.Contract.SupersededBy != testCase.wantSuperseded {
					t.Fatalf("%s historical read superseded_by = %v, want %d", testCase.name, projection.Contract.SupersededBy, testCase.wantSuperseded)
				}
			} else if projection.Contract.SupersededBy != nil {
				t.Fatalf("%s historical read superseded_by = %v, want nil", testCase.name, projection.Contract.SupersededBy)
			}
		}
		active := projectedDecisionDomains(t, s, "decision-historical-read")
		if !reflect.DeepEqual(active, []string{"preference", "ui_ux"}) {
			t.Fatalf("active read projected %v, want the same set the historical selector answered", active)
		}
	})

	t.Run("absence and engineering stay distinct through the historical selector", func(t *testing.T) {
		t.Parallel()
		absent := openTemp(t)
		defer absent.Close()
		seedDecisionDomainsItemFatal(t, absent, "decision-historical-absent", []string{})
		// The read decoder must preserve an existing NULL. Current approval
		// refuses that shape, so seed the stored read facet directly.
		seedStoredDecisionDomainAbsence(t, absent, "decision-historical-absent")
		absentProjection, err := ReadWorkflowProjection(context.Background(), absent, WorkflowReadRequest{WorkID: "decision-historical-absent", ContractVersion: 1})
		if err != nil {
			t.Fatalf("absent historical read: %v", err)
		}
		if absentProjection.Contract == nil {
			t.Fatal("absent historical read carried no contract")
		}
		if absentProjection.Contract.DecisionDomains != nil {
			t.Fatalf("absent historical read projected %v, want nil historical absence", absentProjection.Contract.DecisionDomains)
		}
		engineering := openTemp(t)
		defer engineering.Close()
		seedDecisionDomainsItemFatal(t, engineering, "decision-historical-engineering", []string{})
		engineeringProjection, err := ReadWorkflowProjection(context.Background(), engineering, WorkflowReadRequest{WorkID: "decision-historical-engineering", ContractVersion: 1})
		if err != nil {
			t.Fatalf("engineering historical read: %v", err)
		}
		if engineeringProjection.Contract == nil {
			t.Fatal("engineering historical read carried no contract")
		}
		if engineeringProjection.Contract.DecisionDomains == nil || len(engineeringProjection.Contract.DecisionDomains) != 0 {
			t.Fatalf("engineering historical read projected %v, want the explicit empty set", engineeringProjection.Contract.DecisionDomains)
		}
	})
}
