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
		if err := reviseContractRecord(t, s, "decision-historical-read", owner, successor, version); err != nil {
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
			contract, err := readWorkflowContractVersion(context.Background(), s.db, "decision-historical-read", testCase.selector)
			if err != nil {
				t.Fatalf("%s historical read: %v", testCase.name, err)
			}
			if contract.Version != testCase.selector {
				t.Fatalf("%s historical read returned contract version %d, want %d", testCase.name, contract.Version, testCase.selector)
			}
			if !reflect.DeepEqual(contract.DecisionDomains, testCase.wantDomains) {
				t.Fatalf("%s historical read projected %v, want %v", testCase.name, contract.DecisionDomains, testCase.wantDomains)
			}
			if testCase.wantSupersededB {
				if contract.SupersededBy == nil || *contract.SupersededBy != testCase.wantSuperseded {
					t.Fatalf("%s historical read superseded_by = %v, want %d", testCase.name, contract.SupersededBy, testCase.wantSuperseded)
				}
			} else if contract.SupersededBy != nil {
				t.Fatalf("%s historical read superseded_by = %v, want nil", testCase.name, contract.SupersededBy)
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
		absentContract, err := readWorkflowContractVersion(context.Background(), absent.db, "decision-historical-absent", 1)
		if err != nil {
			t.Fatalf("absent historical read: %v", err)
		}
		if absentContract.DecisionDomains != nil {
			t.Fatalf("absent historical read projected %v, want nil historical absence", absentContract.DecisionDomains)
		}
		engineering := openTemp(t)
		defer engineering.Close()
		seedDecisionDomainsItemFatal(t, engineering, "decision-historical-engineering", []string{})
		engineeringContract, err := readWorkflowContractVersion(context.Background(), engineering.db, "decision-historical-engineering", 1)
		if err != nil {
			t.Fatalf("engineering historical read: %v", err)
		}
		if engineeringContract.DecisionDomains == nil || len(engineeringContract.DecisionDomains) != 0 {
			t.Fatalf("engineering historical read projected %v, want the explicit empty set", engineeringContract.DecisionDomains)
		}
	})
}
