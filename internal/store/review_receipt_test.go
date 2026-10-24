package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCriterionReceiptBatchIsExplicitAndAtomic(t *testing.T) {
	entry := func(predicate, ref string) map[string]any {
		return map[string]any{"predicate_id": predicate, "receipt_refs": []string{ref}}
	}
	for _, step := range []string{"conclude", "complete"} {
		for _, tc := range []struct {
			name   string
			fields func(string) map[string]any
			kind   FailureKind
			count  int
			badRef string
		}{
			{"two criteria", func(ref string) map[string]any {
				return map[string]any{"verdicts": []any{entry("predicate:first", ref), entry("predicate:second", ref)}}
			}, "", 2, ""},
			{"one criterion", func(ref string) map[string]any {
				return map[string]any{"verdicts": []any{entry("predicate:first", ref)}}
			}, "", 1, ""},
			{"resumed Main", func(ref string) map[string]any {
				return map[string]any{"verdicts": []any{entry("predicate:first", ref)}}
			}, "", 1, ""},
			{"unapproved last criterion", func(ref string) map[string]any {
				return map[string]any{"verdicts": []any{entry("predicate:first", ref), entry("predicate:foreign", ref)}}
			}, KindInvalidPayload, 0, ""},
			{"unproved last criterion", func(ref string) map[string]any {
				return map[string]any{"verdicts": []any{entry("predicate:first", ref), entry("predicate:second", "research:invented")}}
			}, KindMissingEvidence, 0, "research:invented"},
			{"duplicate criterion", func(ref string) map[string]any {
				return map[string]any{"verdicts": []any{entry("predicate:first", ref), entry("predicate:first", ref)}}
			}, KindInvalidPayload, 0, ""},
			{"missing receipts", func(string) map[string]any {
				return map[string]any{"verdicts": []any{map[string]any{"predicate_id": "predicate:first"}}}
			}, KindInvalidPayload, 0, ""},
			{"empty receipts", func(string) map[string]any {
				return map[string]any{"verdicts": []any{map[string]any{"predicate_id": "predicate:first", "receipt_refs": []string{}}}}
			}, KindInvalidPayload, 0, ""},
			{"empty batch", func(string) map[string]any { return map[string]any{"verdicts": []any{}} }, KindInvalidPayload, 0, ""},
			{"scalar verdict", func(ref string) map[string]any {
				return map[string]any{"predicate_id": "predicate:first", "verdict_kind": "ok", "evaluation_evidence": []string{ref}}
			}, KindInvalidPayload, 0, ""},
			{"caller verdict", func(ref string) map[string]any {
				value := entry("predicate:first", ref)
				value["verdict_kind"] = "ok"
				return map[string]any{"verdicts": []any{value}}
			}, KindInvalidPayload, 0, ""},
		} {
			t.Run(step+"/"+tc.name, func(t *testing.T) {
				definition, err := BuiltinWorkflowDefinitionForRef("workflow.research")
				if err != nil {
					t.Fatal(err)
				}
				var predicates []map[string]any
				for ordinal, id := range []string{"predicate:first", "predicate:second"} {
					predicates = append(predicates, map[string]any{"predicate_id": id, "ordinal": ordinal, "outcome_kind": "outcome", "outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}})
				}
				const workID = "research-receipt-batch"
				route := seedWorkflowReturnRouteFixtureWithDefinition(t, workID, definition, step, []string{}, []string{}, predicates...)
				defer route.store.Close()
				f := workContextFixture{store: route.store, workID: workID, owner: route.owner, operator: route.operator}
				pack := seedResearchContextPack(t, f, "receipt-batch-pack")
				bindResearchPack(t, f, pack.PackID, 1, true)
				selection := researchSelectionEntry(pack.PackID, 1, "f2", "root")
				selection["product_wide_rationale"] = "The selected finding supports the Product-wide research criteria."
				if err := f.recordWorkContext(t, map[string]any{"research_finding_refs": []map[string]any{selection}}); err != nil {
					t.Fatal(err)
				}
				view, err := readWorkContextView(t.Context(), f.store.db, workID)
				if err != nil || view == nil || len(view.ResearchFindings) != 1 {
					t.Fatalf("missing selected research proof: %+v err=%v", view, err)
				}
				ref := view.ResearchFindings[0].ViewID
				payload := mustJSONValue(tc.fields(ref))
				var evidenceBefore int
				if err := f.store.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowEvidenceBound).Scan(&evidenceBefore); err != nil {
					t.Fatal(err)
				}
				before := contractCorrectionEffectSnapshot(t, f.store, workID)
				actor := f.owner
				if tc.name == "resumed Main" {
					actor.SessionRef += ":resumed"
				}
				preflight := WorkflowActionPreflightWithRegistry(t.Context(), f.store, BuiltinWorkflowRegistry(), WorkflowActionPreflightRequest{
					WorkID: workID, ExpectedVersion: readWorkVersion(t, f.store, workID), ActionID: "record_verdict", Payload: payload, Actor: actor,
				})
				if after := contractCorrectionEffectSnapshot(t, f.store, workID); after != before {
					t.Fatal("criterion preflight changed durable state")
				}
				err = runVerdictActionAs(t, f.store, workID, "record_verdict", payload, 0, actor)
				if tc.kind != "" {
					if !hasFailureKind(preflight, tc.kind) || !hasFailureKind(err, tc.kind) {
						t.Fatalf("preflight=%v mutation=%v, want %s", preflight, err, tc.kind)
					}
					if tc.badRef != "" {
						for _, refusal := range []error{preflight, err} {
							if !strings.Contains(refusal.Error(), tc.badRef) || strings.Contains(refusal.Error(), ref) {
								t.Errorf("refusal must name only the disqualified reference %q, not %q: %v", tc.badRef, ref, refusal)
							}
							var failure *Failure
							if !failureAs(refusal, &failure) || !strings.Contains(failure.RecoveryAction, "record_work_context") || !strings.Contains(failure.RecoveryAction, "research view reference") {
								t.Errorf("refusal must explain how to select and cite qualifying research proof: %v", refusal)
							}
						}
					}
					if after := contractCorrectionEffectSnapshot(t, f.store, workID); after != before {
						t.Fatal("refused criterion batch changed durable state")
					}
				} else if preflight != nil || err != nil {
					t.Fatalf("current criterion proof refused: preflight=%v mutation=%v", preflight, err)
				}
				if got := currentStep(t, f.store, workID); got != step {
					t.Fatalf("criterion association advanced from %s to %s", step, got)
				}
				var count int
				if err := f.store.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&count); err != nil || count != tc.count {
					t.Fatalf("recorded %d criterion associations, want %d: %v", count, tc.count, err)
				}
				var evidenceAfter int
				if err := f.store.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowEvidenceBound).Scan(&evidenceAfter); err != nil || evidenceAfter != evidenceBefore {
					t.Fatalf("criterion association minted or rebound evidence: before=%d after=%d err=%v", evidenceBefore, evidenceAfter, err)
				}
				for index := 0; index < tc.count; index++ {
					var raw []byte
					if err := f.store.db.QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq LIMIT 1 OFFSET ?`, workID, WorkflowVerdictRecorded, index).Scan(&raw); err != nil {
						t.Fatal(err)
					}
					var association workflowVerdictRecordedPayload
					if err := json.Unmarshal(raw, &association); err != nil {
						t.Fatal(err)
					}
					if association.Candidate != nil || association.CriterionDigest == "" || len(association.EvaluationEvidence) != 1 || association.EvaluationEvidence[0] != ref {
						t.Fatalf("association did not retain exact research proof and current criterion content: %+v", association)
					}
					if association.VerdictActorRef != DeriveWorkflowActorRef(actor.PrincipalRef, actor.ClientRef, actor.AgentRef, actor.SessionRef) {
						t.Fatal("criterion association did not retain the calling Main identity")
					}
					if err := verifyWorkflowCriterionAssociation(t.Context(), f.store.db, workID, association); err != nil {
						t.Fatalf("recorded criterion association cannot substantiate acceptance: %v", err)
					}
				}
			})
		}
	}
}

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
