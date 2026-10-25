package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Focused witnesses for the bounded integration attribution reader (the integration settlement
// settlement route 2). The reader resolves the coordinator's cited combined
// verify run from its lease, refuses runs that cannot anchor attribution,
// and enumerates the exact accepted input population the run could observe
// in event order from the acquire watermark. The tests drive the real lease
// lifecycle and the real acceptance owners; they never assert that a failed
// run is verification evidence.

// integrationAttributionObservation is one coordinator integration
// observation citing the combined run locator.
func integrationAttributionObservation(locator string) *WorkerFailureObservation {
	return &WorkerFailureObservation{
		Observer: WorkerFailureObserverCoordinator,
		Phase:    WorkerFailurePhaseIntegration,
		Cause:    WorkerFailureCauseImplementation,
		Basis:    WorkerFailureBasisHypothesis,
		Sources: []WorkerFailureSource{{
			Kind:    WorkerFailureSourceNativeRun,
			Locator: locator,
		}},
	}
}

// integrationFailedVerify runs one completed, clean-subject, failing verify
// on the claimed fixture worktree.
func integrationFailedVerify(t *testing.T, s *Store, git *fakeWorktreeGit, leaseID string) WorktreeVerifyResult {
	t.Helper()
	return integrationFailedVerifyFor(t, s, git, "work-w", "project-w", leaseID)
}

// integrationFailedVerifyFor is the same failing verify on an explicit work
// and Project, for a fixture whose work item is not the default work-w.
func integrationFailedVerifyFor(t *testing.T, s *Store, git *fakeWorktreeGit, workID, projectID, leaseID string) WorktreeVerifyResult {
	t.Helper()
	result, err := s.VerifyWorktree(context.Background(), WorktreeVerifyRequest{
		Owner:  SessionWorktreeOwner{ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1"},
		WorkID: workID, ProjectID: projectID,
		Command: []string{"true"}, LeaseID: leaseID, PrincipalRef: "principal-1", RequestID: "req-v-" + leaseID,
		Now: time.Unix(20, 0).UTC(), Runner: git,
		RunCommand: func(context.Context, string, []string, int) (int, []byte, bool, error) {
			return 1, nil, false, nil
		},
	})
	if err != nil {
		t.Fatalf("failed verify %s: %v", leaseID, err)
	}
	if result.ExitCode != 1 || result.TrackedFilesChanged || result.SubjectRef == "" {
		t.Fatalf("failed verify %s = %+v, want exit 1 on a clean subject", leaseID, result)
	}
	return result
}

// integrationRetainedResultDigest reads the retained result bytes and hashes
// them the way the core does.
func integrationRetainedResultDigest(t *testing.T, s *Store, leaseID string) string {
	t.Helper()
	var stored string
	if err := s.db.QueryRow(`SELECT result_json FROM worktree_verify_leases WHERE lease_id=?`, leaseID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(stored))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// integrationWorkflowWorktree returns the fake git for the work's active
// claimed worktree, claiming one when no oracle fixture claimed it yet. An
// oracle-capable pin's job recording bootstraps a qualified subject on its
// own claim and registers an earlier-sorting canonical locator, so the
// helper reuses the active entry instead of claiming a second worktree.
func integrationWorkflowWorktree(t *testing.T, s *Store, workID string) *fakeWorktreeGit {
	t.Helper()
	entry, entryErr := activeWorktreeEntryForProject(context.Background(), s.db, "worktree_verify", workID, "project")
	if entryErr == nil {
		git := newFakeWorktreeGit(entry.Path)
		git.worktrees[entry.Path] = entry.Branch
		git.worktreeRepos[entry.Path] = entry.Path
		if _, ok := git.branches[entry.Branch]; !ok {
			git.branches[entry.Branch] = git.branches["main"]
		}
		return git
	}
	if !hasFailureKind(entryErr, KindProjectionNotFound) {
		t.Fatal(entryErr)
	}
	var repoRoot string
	if err := s.db.QueryRow(`SELECT normalized_value FROM project_locators WHERE kind=? AND project_id=? ORDER BY locator_id LIMIT 1`, LocatorCanonicalPath, "project").Scan(&repoRoot); err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(repoRoot)
	req := WorktreeClaimRequest{
		OpID: "wt-op-integration", WorkID: workID, ProjectID: "project",
		BaseSHA: git.branches["main"], PrincipalRef: "principal-1", RequestID: "req-claim-integration",
		ExpectedVersion: readWorkVersion(t, s, workID), Now: time.Unix(10, 0).UTC(), Runner: git,
	}
	if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
		t.Fatalf("claim the integration worktree: %v", err)
	}
	return git
}

// TestDeriveWorkerFailureIntegrationComposesWatermarkPopulation drives the
// composed route: one input job accepted before the combined run, one after.
// The derived identity names the canonical operation reference, the retained
// result digest, and exactly the acceptance at or below the acquire
// watermark; the after-watermark acceptance is excluded, and the lease id
// and operation reference are aliases for one identity.
func TestDeriveWorkerFailureIntegrationComposesWatermarkPopulation(t *testing.T) {
	t.Parallel()
	const workID = "integration-attribution-witness"
	fixture := oSeedReviewRoute(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	defer s.Close()
	ctx := context.Background()
	lane := reviewGateLane(t, "implementation")
	acceptor := reviewGateAcceptor(workID)

	jobA := &WorkerJobBinding{JobID: "job:integration-input-a", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, jobA)
	attemptA := "attempt:" + workID + ":input-a"
	dispatchJobBoundAttempt(t, s, workID, "repair", attemptA, nil, fixture.owner, 0, "integration-input-a-"+workID, jobA)
	completeAndAcceptAttempt(t, s, workID, attemptA, lane, acceptor)
	var acceptedRef string
	if err := s.db.QueryRow(`SELECT COALESCE(satisfied_result_ref,'') FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, workID, jobA.JobID, jobA.Revision).Scan(&acceptedRef); err != nil || acceptedRef == "" {
		t.Fatalf("the first acceptance recorded no result reference (%q, %v)", acceptedRef, err)
	}

	git := integrationWorkflowWorktree(t, s, workID)
	failed := integrationFailedVerifyFor(t, s, git, workID, "project", "lease-integration-witness")

	jobB := &WorkerJobBinding{JobID: "job:integration-input-b", Revision: 1}
	recordWorkerJobRevisionForTest(t, s, workID, fixture.owner, jobB)
	attemptB := "attempt:" + workID + ":input-b"
	oDispatchJobBoundAttempt(t, s, workID, attemptB, nil, fixture.owner, 0, "integration-input-b-"+workID, jobB)
	completeAndAcceptAttempt(t, s, workID, attemptB, lane, acceptor)
	assertEventLogAbsent(t, s)

	identity, err := deriveWorkerFailureIntegration(ctx, s.db, workID, integrationAttributionObservation(failed.LeaseID))
	if err != nil {
		t.Fatalf("derive integration identity: %v", err)
	}
	if identity == nil || identity.Kind != WorkerFailureIdentityIntegration || identity.Attempt != nil || identity.Integration == nil {
		t.Fatalf("identity=%+v, want the integration variant alone", identity)
	}
	integration := identity.Integration
	if integration.NativeRun.Kind != WorkerFailureSubjectNativeRun || integration.NativeRun.Locator != failed.OperationRef {
		t.Fatalf("native run=%+v, want the canonical operation reference %q", integration.NativeRun, failed.OperationRef)
	}
	if want := integrationRetainedResultDigest(t, s, failed.LeaseID); integration.NativeRun.Digest != want {
		t.Fatalf("native run digest=%q, want the retained result digest %q", integration.NativeRun.Digest, want)
	}
	if integration.InputsUnavailable {
		t.Fatal("a recorded watermark names its population available")
	}
	if len(integration.InputResults) != 1 {
		t.Fatalf("input population=%+v, want exactly the pre-watermark acceptance", integration.InputResults)
	}
	input := integration.InputResults[0]
	if input.Job == nil || *input.Job != *jobA {
		t.Fatalf("input binding=%+v, want the recorded %s@%d", input.Job, jobA.JobID, jobA.Revision)
	}
	if input.ResultRef != acceptedRef {
		t.Fatalf("input result ref=%q, want the acceptance event id %q", input.ResultRef, acceptedRef)
	}
	if err := validateWorkerFailureIdentity(identity); err != nil {
		t.Fatalf("the derived identity fails its own structural validation: %v", err)
	}

	byAlias, err := deriveWorkerFailureIntegration(ctx, s.db, workID, integrationAttributionObservation(failed.OperationRef))
	if err != nil || !reflect.DeepEqual(byAlias, identity) {
		t.Fatalf("operation-reference alias derive=(%+v, %v), want the identical identity", byAlias, err)
	}
	both := integrationAttributionObservation(failed.LeaseID)
	both.Sources = append(both.Sources, WorkerFailureSource{Kind: WorkerFailureSourceNativeRun, Locator: failed.OperationRef})
	byBoth, err := deriveWorkerFailureIntegration(ctx, s.db, workID, both)
	if err != nil || !reflect.DeepEqual(byBoth, identity) {
		t.Fatalf("two-source derive=(%+v, %v), want the identical identity for one lease", byBoth, err)
	}
}

// TestDeriveWorkerFailureIntegrationNullWatermarkReportsUnavailable proves
// the pre-column lease shape: a NULL watermark yields the explicit
// unavailable marker with no input entries, never an empty accepted list
// and never a backfilled timestamp guess.
func TestDeriveWorkerFailureIntegrationNullWatermarkReportsUnavailable(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)
	ctx := context.Background()
	failed := integrationFailedVerify(t, s, git, "lease-integration-legacy")
	if _, err := s.db.Exec(`UPDATE worktree_verify_leases SET event_watermark=NULL WHERE lease_id=?`, failed.LeaseID); err != nil {
		t.Fatal(err)
	}
	identity, err := deriveWorkerFailureIntegration(ctx, s.db, "work-w", integrationAttributionObservation(failed.LeaseID))
	if err != nil {
		t.Fatalf("derive over the legacy lease: %v", err)
	}
	integration := identity.Integration
	if !integration.InputsUnavailable || len(integration.InputResults) != 0 {
		t.Fatalf("integration=%+v, want the explicit unavailable population with no entries", integration)
	}
	if integration.NativeRun.Locator != failed.OperationRef || integration.NativeRun.Digest == "" {
		t.Fatalf("native run=%+v, want the canonical run reference and digest beside the unavailable marker", integration.NativeRun)
	}
	if err := validateWorkerFailureIdentity(identity); err != nil {
		t.Fatalf("the unavailable-population identity fails validation: %v", err)
	}
}

// TestDeriveWorkerFailureIntegrationRefusesMutatedAbortedAndSubjectless
// proves the anchor refusals: a mutated run, an aborted run, and a run with
// no clean unchanged subject each refuse typed and name their cause.
func TestDeriveWorkerFailureIntegrationRefusesMutatedAbortedAndSubjectless(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	entry := claimFixtureWorktree(t, s, git)
	ctx := context.Background()

	git.dirty[entry.Path] = true
	mutated, mutateErr := s.VerifyWorktree(ctx, verifyRequest(git, "lease-integration-mutated", []string{"true"}, func(_ context.Context, dir string, _ []string, _ int) (int, []byte, bool, error) {
		delete(git.dirty, dir)
		return 1, nil, false, nil
	}))
	if failureKind(mutateErr) != KindWorktreeVerifyMutated || !mutated.TrackedFilesChanged {
		t.Fatalf("mutated verify=(%+v, %v), want the typed mutation refusal", mutated, mutateErr)
	}
	var failure *Failure
	_, err := deriveWorkerFailureIntegration(ctx, s.db, "work-w", integrationAttributionObservation(mutated.LeaseID))
	if !failureAs(err, &failure) || failure.Kind != KindWorktreeVerifyMutated || !strings.Contains(failure.Detail, "mutated") {
		t.Fatalf("mutated anchor err=%v, want the named mutation refusal", err)
	}

	abortedCtx, cancel := context.WithCancel(ctx)
	aborted, abortErr := s.VerifyWorktree(abortedCtx, verifyRequest(git, "lease-integration-aborted", []string{"true"}, func(context.Context, string, []string, int) (int, []byte, bool, error) {
		cancel()
		return 1, nil, false, nil
	}))
	if !errors.Is(abortErr, context.Canceled) || aborted.ExitCode != -1 {
		t.Fatalf("aborted verify=(%+v, %v), want the canceled run outcome", aborted, abortErr)
	}
	_, err = deriveWorkerFailureIntegration(ctx, s.db, "work-w", integrationAttributionObservation("lease-integration-aborted"))
	if !failureAs(err, &failure) || failure.Kind != KindCancelled || !strings.Contains(failure.Detail, "aborted") {
		t.Fatalf("aborted anchor err=%v, want the named aborted refusal", err)
	}

	git.dirty[entry.Path] = true
	dirty, err := s.VerifyWorktree(ctx, verifyRequest(git, "lease-integration-dirty", []string{"true"}, func(context.Context, string, []string, int) (int, []byte, bool, error) {
		return 1, nil, false, nil
	}))
	if err != nil || dirty.ExitCode != 1 || dirty.SubjectRef != "" || dirty.TrackedFilesChanged {
		t.Fatalf("dirty verify=(%+v, %v), want a completed failing run with no subject", dirty, err)
	}
	_, err = deriveWorkerFailureIntegration(ctx, s.db, "work-w", integrationAttributionObservation(dirty.LeaseID))
	if !failureAs(err, &failure) || failure.Kind != KindMissingEvidence || !strings.Contains(failure.Detail, "clean") {
		t.Fatalf("dirty anchor err=%v, want the named clean-subject refusal", err)
	}
}

// TestDeriveWorkerFailureIntegrationRefusesUnanchorableRuns proves the
// remaining refusals: a green run is verification authority and never a
// failure anchor, a locator that names no released lease of the work is
// missing, a supplied digest must match the retained result bytes, and a
// non-integration observation cannot anchor.
func TestDeriveWorkerFailureIntegrationRefusesUnanchorableRuns(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)
	ctx := context.Background()

	green, err := s.VerifyWorktree(ctx, verifyRequest(git, "lease-integration-green", []string{"true"}, func(context.Context, string, []string, int) (int, []byte, bool, error) {
		return 0, nil, false, nil
	}))
	if err != nil || green.ExitCode != 0 {
		t.Fatalf("green verify=(%+v, %v)", green, err)
	}
	var failure *Failure
	_, err = deriveWorkerFailureIntegration(ctx, s.db, "work-w", integrationAttributionObservation(green.LeaseID))
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "green") {
		t.Fatalf("green anchor err=%v, want the named refusal that only the failed run anchors", err)
	}

	failed := integrationFailedVerify(t, s, git, "lease-integration-anchors")

	_, err = deriveWorkerFailureIntegration(ctx, s.db, "work-w", integrationAttributionObservation("lease-integration-absent"))
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || !strings.Contains(failure.Detail, "lease-integration-absent") {
		t.Fatalf("missing anchor err=%v, want the named missing-lease refusal", err)
	}

	_, err = deriveWorkerFailureIntegration(ctx, s.db, "work-other", integrationAttributionObservation(failed.LeaseID))
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || !strings.Contains(failure.Detail, "work-other") {
		t.Fatalf("wrong-work anchor err=%v, want the missing refusal bound to the citing work", err)
	}

	mismatched := integrationAttributionObservation(failed.LeaseID)
	mismatched.Sources[0].Digest = testDigestA
	_, err = deriveWorkerFailureIntegration(ctx, s.db, "work-w", mismatched)
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "digest") {
		t.Fatalf("digest anchor err=%v, want the named digest mismatch refusal", err)
	}

	second := integrationFailedVerify(t, s, git, "lease-integration-second")
	conflicting := integrationAttributionObservation(failed.LeaseID)
	conflicting.Sources = append(conflicting.Sources, WorkerFailureSource{Kind: WorkerFailureSourceNativeRun, Locator: second.LeaseID})
	_, err = deriveWorkerFailureIntegration(ctx, s.db, "work-w", conflicting)
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "different leases") {
		t.Fatalf("conflicting anchors err=%v, want the named different-lease refusal", err)
	}

	notIntegration := &WorkerFailureObservation{
		Observer: WorkerFailureObserverCoordinator,
		Phase:    WorkerFailurePhaseResultReview,
		Cause:    WorkerFailureCauseUnknown,
		Basis:    WorkerFailureBasisUnknown,
		Sources:  []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: "result-finding-1", Digest: testDigestB}},
	}
	_, err = deriveWorkerFailureIntegration(ctx, s.db, "work-w", notIntegration)
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "coordinator integration observation") {
		t.Fatalf("review-phase anchor err=%v, want the named coupling refusal", err)
	}

	noNative := integrationAttributionObservation(failed.LeaseID)
	noNative.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceResult, Locator: "result-finding-1", Digest: testDigestB}}
	_, err = deriveWorkerFailureIntegration(ctx, s.db, "work-w", noNative)
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "native_run") {
		t.Fatalf("result-source anchor err=%v, want the named native_run refusal", err)
	}
}

// TestDeriveWorkerFailureIntegrationRefusesAmbiguousAlias proves a locator
// that names two released leases refuses instead of selecting one.
func TestDeriveWorkerFailureIntegrationRefusesAmbiguousAlias(t *testing.T) {
	t.Parallel()
	s, _, _ := worktreeFixture(t)
	ctx := context.Background()
	leaseInsert := `INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,outcome,result_json) VALUES
		('worktree_verify:dupe','work-w','project-w','/repo/wt-dupe','released','client-1','agent-1','session-1','principal-1','["true"]','2026-10-14T00:00:00Z','completed','{}'),
		('lease-alias-other','work-w','project-w','/repo/wt-alias','released','client-1','agent-1','session-1','principal-1','["true"]','2026-10-14T00:00:00Z','completed','{"operation_ref":"worktree_verify:dupe"}')`
	if _, err := s.db.Exec(leaseInsert); err != nil {
		t.Fatal(err)
	}
	var failure *Failure
	_, err := deriveWorkerFailureIntegration(ctx, s.db, "work-w", integrationAttributionObservation("worktree_verify:dupe"))
	if !failureAs(err, &failure) || failure.Kind != KindAmbiguousScope || !strings.Contains(failure.Detail, "worktree_verify:dupe") {
		t.Fatalf("ambiguous alias err=%v, want the named ambiguity refusal", err)
	}
}
