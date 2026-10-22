package store

import "testing"

func TestCriterionReceiptResearchRequiresSelectedSourcedFindingWithoutGit(t *testing.T) {
	for _, tc := range []struct {
		name, finding string
		invented      bool
		want          bool
	}{
		{"sourced finding", "f2", false, true},
		{"unsourced finding", "f0", false, false},
		{"invented reference", "f2", true, false},
		{"rebound", "f2", false, false},
		{"stale", "f2", false, false},
		{"inactive", "f2", false, false},
		{"missing", "f2", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, err := BuiltinWorkflowDefinitionForRef("workflow.research")
			if err != nil {
				t.Fatal(err)
			}
			route := seedWorkflowReturnRouteFixtureWithDefinition(t, "research-receipt", definition, "conclude", []string{}, []string{})
			defer route.store.Close()
			f := workContextFixture{store: route.store, workID: "research-receipt", owner: route.owner, operator: route.operator}
			pack := seedResearchContextPack(t, f, "receipt-pack")
			bindResearchPack(t, f, pack.PackID, 1, true)
			selection := researchSelectionEntry(pack.PackID, 1, tc.finding, "root")
			selection["product_wide_rationale"] = "The selected research supports the Product-wide investigation."
			if err := f.recordWorkContext(t, map[string]any{"research_finding_refs": []map[string]any{selection}}); err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "rebound":
				bindResearchPack(t, f, pack.PackID, 2, true)
			case "stale":
				injectResearchWrite(t, f, `UPDATE active_research_findings SET freshness='stale' WHERE pack_id=? AND revision=1 AND finding_id='f2'`, pack.PackID)
			case "inactive":
				injectResearchWrite(t, f, `UPDATE active_research_findings SET status='contradicted' WHERE pack_id=? AND revision=1 AND finding_id='f2'`, pack.PackID)
			case "missing":
				injectResearchRowDelete(t, f, `DELETE FROM active_research_findings WHERE pack_id=? AND revision=1 AND finding_id='f2'`, pack.PackID)
			}
			view, err := readWorkContextView(t.Context(), f.store.db, f.workID)
			if err != nil || view == nil || len(view.ResearchFindings) != 1 || view.SubjectCommit != "" {
				t.Fatalf("research receipt fixture requires one selected finding and no Git subject: %+v %v", view, err)
			}
			ref := view.ResearchFindings[0].ViewID
			if tc.invented {
				ref = "research:invented"
			}
			err = workflowCriterionReceipt(t.Context(), f.store.db, f.workID, "predicate:return-route", ref, "")
			if (err == nil) != tc.want {
				t.Fatalf("research criterion receipt admitted=%t, want %t: %v", err == nil, tc.want, err)
			}
			if !tc.want && failureKind(err) != KindMissingEvidence {
				t.Fatalf("research receipt failed for a reason other than missing qualifying evidence: %v", err)
			}
		})
	}
}
