package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
	"github.com/sharper-flow/concord/internal/store"
)

func TestEscalatedHostRetryReadsHostLeaseBeforeAdmission(t *testing.T) {
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "worker_dispatch"})
	// The terminal predates the test host independently of the machine clock.
	version, _, worktree := seedEscalatedFailedWorkerMutation(t, s, service, grant, time.Unix(1, 0).UTC())
	grant.Worktree = worktree
	if _, err := s.RecordWorktreeClaimLanding(context.Background(), store.WorktreeClaimLandingRequest{
		WorkID: "work-1", SessionRef: grant.SessionRef, LandedDirectory: worktree, HostPID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, mustScopeVersion(t, s))
	attemptID := "attempt:work-1:host-convergence"
	packet := retryMutationPacket(t, s, attemptID, verifyWallPinCorrection(t, s))
	continuity, err := store.ReadWorkflowContinuity(context.Background(), s, store.ContinuityRequest{Work: "work-1"})
	if err != nil || len(continuity.ReadyWorkerJobs) == 0 {
		t.Fatalf("ready retry job: error=%v, continuity=%+v", err, continuity)
	}
	packet["inputs"].(map[string]any)["worker_job"] = continuity.ReadyWorkerJobs[len(continuity.ReadyWorkerJobs)-1]
	input := retryJSON(map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "dispatch_worker", "idempotency_key": "host-convergence",
		"fields": map[string]any{"attempt_id": attemptID, "worker_packet": packet},
	})
	request := InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}
	challenges := verifyWallChallenges(t, s)
	refused := dispatchMutation(t, s, service, request, env)
	if refused.Error == nil || refused.Error.Kind != "missing_evidence" {
		t.Fatalf("unleased host dispatch = %+v; want missing_evidence", refused)
	}
	if !strings.Contains(refused.Error.Message, "three-attempt limit") || !strings.Contains(refused.Error.Message, "restart the host more than one minute") || !strings.Contains(refused.Error.Message, "retry once") {
		t.Fatalf("refusal does not identify the convergence wall: %+v", refused.Error)
	}
	start, err := hostlease.ProcessStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := hostlease.Write(filepath.Dir(s.Path()), hostlease.Lease{PID: os.Getpid(), PidStart: start, ReleaseRoot: filepath.Dir(self), CoreBinary: self, SchemaVersion: 1, ManifestDigest: ManifestDigest}); err != nil {
		t.Fatal(err)
	}
	leases := s.ReadHostLeases()
	if leases.Err != nil || len(leases.Leases) != 1 {
		t.Fatalf("host lease observation = %+v", leases)
	}
	admitted := dispatchMutation(t, s, service, request, env)
	if admitted.Outcome != OutcomeOK {
		t.Fatalf("leased restarted-host dispatch = %+v", admitted.Error)
	}
	if convergence := verifyWallRecordedConvergence(t, s, attemptID); convergence.Basis != "host_changed" || convergence.HostPID != os.Getpid() || convergence.HostPIDStart != start {
		t.Fatalf("recorded host basis = %+v", convergence)
	}
	if verifyWallChallenges(t, s) != challenges {
		t.Fatal("host convergence minted an operator retry challenge")
	}
	replayed := dispatchMutation(t, s, service, request, env)
	if replayed.Outcome != OutcomeOK || !replayed.Replayed || verifyWallDispatchCompletions(t, s, attemptID) != 1 {
		t.Fatalf("host convergence replay = %+v; want the single recorded dispatch", replayed)
	}
}
