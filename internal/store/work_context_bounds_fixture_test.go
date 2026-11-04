package store

import (
	"fmt"
	"strings"
	"testing"
)

func declarationEventCount(t *testing.T, f workContextFixture) int {
	t.Helper()
	var count int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id=? AND kind=?`, f.workID, WorkflowWorkContextRecorded).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func seedMandatoryNativeEvidence(t *testing.T, f workContextFixture, blockers int) {
	t.Helper()
	reviewSubjectClaim(t, f.store, f.workID)
	state := WorkflowReviewState{Findings: []ReviewFinding{}, findingSources: map[string]reviewFindingSource{}}
	for i := 0; i < blockers; i++ {
		refs := make([]string, 0, 16)
		for ref := 0; ref < 16; ref++ {
			prefix := fmt.Sprintf("evidence:mandatory-%02d-%02d-", i, ref)
			refs = append(refs, prefix+strings.Repeat("e", 128-len(prefix)))
		}
		id := fmt.Sprintf("finding:mandatory-%02d", i)
		state.Findings = append(state.Findings, ReviewFinding{
			FindingID: id, Severity: "P1", Confidence: "high", InScope: true, Blocking: true, Status: "open",
			Detail:       fmt.Sprintf("the mandatory ranked blocker %02d carries its sized detail ", i) + strings.Repeat("m", 455),
			EvidenceRefs: refs, ClosureRefs: []string{}, Oracle: &ReviewOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:repair", FailureFamily: fmt.Sprintf("mandatory-family-%02d", i), ControlIDs: []string{"control:fixture"}, EvidenceRefs: refs[:8]},
		})
		state.findingSources[id] = reviewFindingSource{CanonicalID: fmt.Sprintf("finding:%d:0", 100+i), DomainID: workContextTestChild, EventID: "mandatory-review-" + id, Kind: WorkflowActionCompleted, Seq: int64(100 + i)}
	}
	if err := f.store.Transact(t.Context(), func(tx *Transaction) error {
		scope, err := beginWrite(t.Context(), tx.tx)
		if err != nil {
			return err
		}
		if err := writeWorkflowReviewFacts(t.Context(), tx.tx, f.workID, state); err != nil {
			return err
		}
		return scope.close(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
}
