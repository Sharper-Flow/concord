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

func seedWorkerContextFindings(t *testing.T, f workContextFixture, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		refs := make([]string, 0, 32)
		for ref := 0; ref < 32; ref++ {
			prefix := fmt.Sprintf("evidence:context-%02d-%02d-", i, ref)
			refs = append(refs, prefix+strings.Repeat("e", 128-len(prefix)))
		}
		finding := WorkContextFinding{Kind: "observation", Statement: fmt.Sprintf("context finding %02d ", i) + strings.Repeat("m", 455), SubjectRef: "file:fixture", EvidenceRefs: refs, DomainID: workContextTestChild}
		seedContextFactTerminal(t, f, "worker.completed", map[string]any{"context_findings": []WorkContextFinding{finding}})
	}
}
