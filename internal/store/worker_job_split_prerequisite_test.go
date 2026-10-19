package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWorkerJobSplitPrerequisiteRequiresExactDischarge(t *testing.T) {
	const workID = "split-prerequisite-discharge"
	fixture := dischargeFixture(t, workID)
	s := workerJobSizeStore(fixture)
	predecessor := latestSplitBinding(t, fixture, workID, splitPredecessorJobID)
	recordDependent := func(jobID, resultRef string) WorkerJobBinding {
		t.Helper()
		fields := splitSuccessorFields(t, workID, fixture, jobID, "internal/store/worker_jobs.go")
		fields["prerequisites"] = []WorkerJobPrerequisite{{JobID: predecessor.JobID, Revision: predecessor.Revision, ResultRef: resultRef}}
		if err := recordWorkerJobActionForTest(t, s, workID, fixture.owner, fields); err != nil {
			t.Fatalf("record dependent %s: %v", jobID, err)
		}
		return latestSplitBinding(t, fixture, workID, jobID)
	}
	assertReady := func(binding WorkerJobBinding, want bool) {
		t.Helper()
		views, err := readWorkerJobRevisions(context.Background(), s.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		for _, view := range views {
			if view.Binding == binding {
				if view.Ready != want {
					t.Fatalf("readiness of %s = %v, want %v", binding.JobID, view.Ready, want)
				}
				return
			}
		}
		t.Fatalf("missing revision %+v", binding)
	}
	unpinned := recordDependent("job:dependent", "")
	wrongRef := recordDependent("job:wrong-ref", "evidence:not-the-discharge")
	assertReady(unpinned, false)
	for index, binding := range dischargeLeafBindings(t, fixture, workID) {
		dischargeReplayAcceptLeaf(t, fixture, workID, fmt.Sprintf("attempt:prerequisite-%d", index), binding)
		assertReady(unpinned, false)
	}
	opRef := dischargeSeedVerifyRun(t, s, workID, strings.Repeat("a", 64), []string{"go", "test", "./internal/store/"}, time.Unix(30, 0).UTC(), dischargePrimaryProject(t, s, workID), 0, false)
	if err := runBindEvidenceAction(t, s, workID, opRef, nil); err != nil {
		t.Fatal(err)
	}
	dischargeID := splitStatesForTest(t, fixture, workID)[0].DischargeID
	if dischargeID == "" {
		t.Fatal("qualifying integration recorded no discharge")
	}
	assertReady(unpinned, true)
	assertReady(wrongRef, false)
	exact := recordDependent("job:exact-ref", dischargeID)
	integrationRef := recordDependent("job:integration-ref", opRef)
	assertReady(exact, true)
	assertReady(integrationRef, false)
	assertReady(predecessor, false)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	assertReady(unpinned, true)
	assertReady(exact, true)
	assertReady(wrongRef, false)
	assertReady(integrationRef, false)
	assertReady(predecessor, false)
}
