package store

import "testing"

// CD-0242 D7: supersession invalidates proof only through the digests captured
// by its owners (Review dependencies and criterion associations). Reassessment
// of an unchanged candidate must not demand another implementation delivery.
func TestArtifactSupersessionUsesRecordedProofDependencies(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "unchanged dependency"
		if changed {
			name = "changed dependency"
		}
		t.Run(name, func(t *testing.T) {
			f, _, refs := pReceiptFixture(t, "artifact-dependencies", 1, 1)
			s := f.store
			if err := runVerdictActionAs(t, s, f.workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
				t.Fatal(err)
			}
			before, err := loadWorkflowReviewState(t.Context(), s.db, f.workID)
			if err != nil {
				t.Fatal(err)
			}
			associations, err := latestWorkflowVerdicts(t.Context(), s.db, f.workID, 1)
			if err != nil || len(associations) != 1 || associations[0].CriterionDigest == "" {
				t.Fatalf("captured proof dependencies = %+v, %v", associations, err)
			}
			successor := pContractSuccessor(t, s, f.workID, "Reassess the same candidate.")
			if changed {
				predicate := successor["outcome_predicates"].([]any)[0].(map[string]any)
				predicate["outcome_payload"].(map[string]any)["check_ref"] = "check:reassessed"
			}
			if err := runVerdictActionAs(t, s, f.workID, "supersede_contract", mustJSONValue(successor), 0, f.owner); err != nil {
				t.Fatal(err)
			}
			after, err := loadWorkflowReviewState(t.Context(), s.db, f.workID)
			if err != nil || after.DependenciesCurrent == changed || *after.Candidate != *before.Candidate {
				t.Fatalf("supersession dependency state = %+v, %v", after, err)
			}
			err = verifyWorkflowCriterionAssociation(t.Context(), s.db, f.workID, associations[0])
			if changed {
				if !hasFailureKind(err, KindStaleRequiresReview) {
					t.Fatalf("changed criterion reused old association: %v", err)
				}
				assertRecoveryRefusalWithoutEffect(t, s, f.workID, KindMissingEvidence, func() error {
					return runVerdictActionAs(t, s, f.workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner)
				})
				pAcceptReceiptReview(t, f, refs)
				if step := currentStep(t, s, f.workID); step != "acceptance" {
					t.Fatalf("reassessment Review advanced acceptance to %s", step)
				}
			} else if err != nil {
				t.Fatalf("unchanged criterion lost its proof: %v", err)
			}
			if err := runVerdictActionAs(t, s, f.workID, "record_verdict", pReceiptBatch(refs, 0), 0, f.owner); err != nil {
				t.Fatalf("associate reassessed proof without repeated production: %v", err)
			}
			current, err := loadWorkflowReviewState(t.Context(), s.db, f.workID)
			if err != nil || !current.DependenciesCurrent || *current.Candidate != *before.Candidate {
				t.Fatalf("reassessment changed candidate or left stale dependencies: %+v, %v", current, err)
			}
		})
	}
}
