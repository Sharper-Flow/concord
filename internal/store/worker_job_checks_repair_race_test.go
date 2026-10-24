package store

import (
	"context"
	"strings"
	"testing"
)

// A repairing acceptance refuses transactionally while another repair of the
// same job stands open, including when both were recorded before either
// result was accepted. Rebuild must fold the same single candidate.

func TestChecksRepairSecondRepairingAcceptanceRefusesWhileOneOpen(t *testing.T) {
	const workID = "checks-repair-second-open"
	const jobID = "job:checks-repair-second-open"
	fixture := seedChecksRepairFixture(t, workID, "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	reviewSubjectClaim(t, s, workID)
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/checks-repair-second-open-worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	acceptor := reviewGateAcceptor(workID)

	jobFields := func(checks string, repairRevisions []int64) map[string]any {
		fields := map[string]any{
			"job_id":               jobID,
			"objective":            "Corrective bounded objective for the checks-repair-second-open job",
			"stopping_condition":   "the recorded checks pass with no unresolved reference",
			"path_scope":           []string{"internal/store"},
			"checks":               []string{checks},
			"reserved_integration": "integration evidence binds at the parent effect step",
			"ready":                true,
			"readiness_evidence":   []string{"evidence:checks-repair-second-open"},
		}
		if repairRevisions != nil {
			fields["repair_revisions"] = repairRevisions
			fields["repair_reason"] = "the named revision's checks invoked a command no admitted lane can execute or decide as written"
		}
		return fields
	}
	record := func(fields map[string]any, what string) {
		t.Helper()
		if err := recordWorkerJobFieldsForTest(t, s, workID, fixture.owner, fields); err != nil {
			t.Fatalf("record the %s: %v", what, err)
		}
	}
	latest := func(what string) *WorkerJobBinding {
		t.Helper()
		binding := checksRepairLatestBinding(t, s, workID, jobID)
		if binding == nil {
			t.Fatalf("no recorded revision of %s after the %s", jobID, what)
		}
		return binding
	}
	jobRow := func(binding WorkerJobBinding) (string, string) {
		t.Helper()
		var state, ref string
		if err := s.DatabaseForTesting().QueryRow(`SELECT state, COALESCE(satisfied_result_ref,'') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, binding.JobID, binding.Revision).Scan(&state, &ref); err != nil {
			t.Fatal(err)
		}
		return state, ref
	}
	var revA, revB *WorkerJobBinding
	oneOpenRepairA := func(stage string) {
		t.Helper()
		candidates, err := workerChecksRepairAwaitingReviewTx(ctx, s.DatabaseForTesting(), workID)
		if err != nil || len(candidates) != 1 || candidates[0] != *revA {
			t.Fatalf("candidates %s = (%#v, %v), want exactly repair A %s@%d", stage, candidates, err, revA.JobID, revA.Revision)
		}
	}

	// Revision 1 fails on its own checks and stays unresolved.
	record(jobFields("concord check --second-open-original --exit-code=never", nil), "original revision 1")
	rev1 := latest("original recording")
	attempt1 := "attempt:" + workID + ":1"
	dispatchJobBoundAttempt(t, s, workID, "repair", attempt1, nil, worker, 0, "checks-repair-second-open-1", rev1)
	s2WorkerFailedAttributedLive(t, s, workID, attempt1, "the recorded check names a command no admitted lane can execute")

	// Repair A records and dispatches; its acceptance has not landed.
	record(jobFields("go test ./internal/store/ -run TestChecksRepairSecondOpenA", []int64{1}), "repair A")
	revA = latest("repair A recording")
	if revA.Revision != rev1.Revision+1 {
		t.Fatalf("repair A = %s@%d, want the revision beside %d", revA.JobID, revA.Revision, rev1.Revision)
	}
	pin := issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attemptA := "attempt:" + workID + ":A"
	dispatchJobBoundAttempt(t, s, workID, "repair", attemptA, pin.Correction, worker, pin.Version, "checks-repair-second-open-A", revA)
	completeChecksRepairAttemptObserved(t, s, workID, attemptA, "checks-repair-completed-"+attemptA)

	// Repair B records while A is still unaccepted — the legal window the
	// race rides: A's repair holds no acceptance, so the recording guard
	// sees no open repair and derives B against the same unresolved set.
	record(jobFields("go test ./internal/store/ -run TestChecksRepairSecondOpenB", []int64{1}), "repair B")
	revB = latest("repair B recording")
	if revB.Revision != revA.Revision+1 {
		t.Fatalf("repair B = %s@%d, want the revision beside %d", revB.JobID, revB.Revision, revA.Revision)
	}

	// A accepts and opens: exactly one candidate stands for the job.
	acceptChecksRepairAttempt(t, s, workID, attemptA, acceptor)
	oneOpenRepairA("behind the accepted repair A")

	// B's attempt dispatches and completes, then B accepts second: the fold
	// refuses the second repairing acceptance while A's repair stands
	// open-unconfirmed, inside the append's transaction.
	pin = issue1013Pin(t, s, workID)
	issue1013StartRepair(t, s, workID, fixture.owner, pin.Version, latestStepStartEpoch(t, s, workID, "repair")+1)
	pin = issue1013Pin(t, s, workID)
	attemptB := "attempt:" + workID + ":B"
	dispatchJobBoundAttempt(t, s, workID, "repair", attemptB, pin.Correction, worker, pin.Version, "checks-repair-second-open-B", revB)
	completeChecksRepairAttemptObserved(t, s, workID, attemptB, "checks-repair-completed-"+attemptB)

	version := readWorkVersion(t, s, workID)
	seq := maxEventSeq(t, s, workID)
	acceptErr := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attemptB, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, acceptor)
	if acceptErr == nil || !strings.Contains(acceptErr.Error(), "second unconfirmed checks repair") {
		t.Fatalf("the second repairing acceptance = %v, want the second-open-repair refusal", acceptErr)
	}

	// The refusal committed no version, event, or projection write.
	if after := readWorkVersion(t, s, workID); after != version {
		t.Fatalf("work version after the refused acceptance = %d, want %d", after, version)
	}
	if after := maxEventSeq(t, s, workID); after != seq {
		t.Fatalf("event seq after the refused acceptance = %d, want %d", after, seq)
	}
	if state, ref := jobRow(*revB); state != "recorded" || ref != "" {
		t.Fatalf("the refused repairing revision %s@%d = %s via %q, want recorded with no disposition", revB.JobID, revB.Revision, state, ref)
	}
	stateA, refA := jobRow(*revA)
	if stateA != "satisfied" || refA == "" {
		t.Fatalf("the accepted repairing revision %s@%d = %s via %q, want satisfied through its own acceptance", revA.JobID, revA.Revision, stateA, refA)
	}
	if state, ref := jobRow(*rev1); state != "satisfied" || ref != refA {
		t.Fatalf("the named revision %s@%d = %s via %q, want its provisional readiness through repair A's acceptance %q", rev1.JobID, rev1.Revision, state, ref, refA)
	}
	oneOpenRepairA("behind the refused second acceptance")

}
