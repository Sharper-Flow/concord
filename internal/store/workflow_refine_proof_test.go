package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Refine exit requires a passing report covering the recorded Review candidate.

func refineProofStartRefine(t *testing.T, fixture workflowReturnRouteFixture, workID string) {
	t.Helper()
	reviewGateStartStep(t, fixture.store, workID, "refine", "start_refine", fixture.owner)
}

// refineProofCurrentFixture seeds the refine-proof work on the family's
// current definition with verification, review, and artifact evidence bound.
func refineProofCurrentFixture(t *testing.T, workID, definitionRef string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, definitionRef), "refine", []string{"verification"}, []string{"verification", "review", "artifact"})
}

// Leaving execution requires a recorded ready Review of the claimed candidate.
func refineProofAcceptReadyReview(t *testing.T, fixture workflowReturnRouteFixture, workID string) {
	t.Helper()
	reviewSubjectClaim(t, fixture.store, workID)
	f := workContextFixture{store: fixture.store, workID: workID, owner: fixture.owner, operator: fixture.operator}
	acceptCurrentReviewForTest(t, f, reviewFixtureDirectory(t, fixture.store, workID), "ready_for_final_proof")
}

// refineProofCandidateFixture seeds the refine-proof work without a
// pre-bound verification, so every bound verification a test reads is one it
// produced.
func refineProofCandidateFixture(t *testing.T, workID, definitionRef string) workflowReturnRouteFixture {
	t.Helper()
	return seedWorkflowReturnRouteFixtureWithDefinition(t, workID, mustBuiltinDefinition(t, definitionRef), "refine", []string{"verification"}, []string{"review", "artifact"})
}

// refineProofCandidateRun binds a coordinator's report for a recorded commit.
func refineProofCandidateRun(t *testing.T, s *Store, workID, name, head string, command []string) {
	t.Helper()
	report := VerificationReport{Source: "local", Commit: head, Result: "pass", RunRef: "run:" + name}
	if err := runVerdictActionAs(t, s, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "commit:" + head, "verification_report": report}), 0, reviewGateAcceptor(workID)); err != nil {
		t.Fatalf("bind report %s: %v", name, err)
	}
}

// refineProofCandidate is the accepted Review's integrated candidate commit.
func refineProofCandidate(t *testing.T, s *Store, workID string) string {
	t.Helper()
	candidate := issue1013Pin(t, s, workID).Review.Candidate
	if candidate == nil {
		t.Fatal("no integrated candidate after the accepted Review")
	}
	return candidate.Commit
}

func refineProofDelivery(t *testing.T, s *Store, workID string, tooling *ProjectToolingManifest) error {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	actor := reviewGateAcceptor(workID)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	payload := json.RawMessage(`{"delivery_artifact":"artifact:refine-proof-` + workID + `","delivery_state":"asserted"}`)
	_, err = applyWorkflowActionRawTx(context.Background(), tx, newWriteScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		EvidenceRefs: actionEvidenceRefs("record_delivery", payload), WorkID: workID, ExpectedVersion: version, ActionID: "record_delivery", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("c", 64) + fmt.Sprint(version), IdempotencyIdentity: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version), OperationID: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version),
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "record_delivery-refine-proof-" + workID + "-" + fmt.Sprint(version), RequestID: "request:refine-proof-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(9, 0).UTC(),
		ProjectTooling: tooling,
	})
	if err != nil {
		return err
	}
	return tx.Commit()
}

func refineProofRequireMissingEvidence(t *testing.T, err error, wantContains string) {
	t.Helper()
	var failure *Failure
	if err == nil || !failureAs(err, &failure) || failure.Kind != KindMissingEvidence {
		t.Fatalf("record_delivery = %v, want a missing-evidence refusal", err)
	}
	if !strings.Contains(failure.Detail, wantContains) {
		t.Fatalf("refusal %q does not name %q", failure.Detail, wantContains)
	}
}

// refineProofBindVerification binds one verification evidence. The fold
// validates the binding's producer operation, so the caller passes a
// producerRunRef that names a completed durable operation for this work:
// the verify run's own operation ref, or a seeded workflow.test authority.
func refineProofBindVerification(t *testing.T, s *Store, workID, ref, producerRunRef string) {
	t.Helper()
	ownerRef, err := WorkflowActorRef(reviewGateAcceptor(workID))
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor(workID+":refine-proof-bind:"+ref, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "verification",
		"immutable_subject_ref": ref, "producer_id": "principal/operator", "producer_run_ref": producerRunRef,
		"producer_watermark": "request/verify", "observed_at": "2026-09-29T00:00:00Z",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("bind the verification evidence: %v", err)
	}
}

func refineProofTooling(entries ...ProjectDeclaredTool) *ProjectToolingManifest {
	return &ProjectToolingManifest{Project: "concord", Declared: entries}
}

// refineProofSeedGreenRun binds a report for the recorded Review candidate.
func refineProofSeedGreenRun(t *testing.T, s *Store, workID, digest string) {
	t.Helper()
	if workflowCurrentStartAtStep(t, s, workID, "refine") == nil {
		t.Fatal("the refine proof fixture has no current refine start")
	}
	refineProofCandidateRun(t, s, workID, digest, refineProofCandidate(t, s, workID), nil)
}

func TestRefineExitRefusesWithoutGreenVerifyRunInEpoch(t *testing.T) {
	const workID = "refine-proof-refusal"
	c := acceptDeliveryUnobservedCandidateAt(t, workID, "workflow.implementation")
	s := c.f.store

	// The retained candidate has no applicable producer receipt.
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, nil), "no passing verification report covers the recorded Review candidate")

	// A binding that names a workflow operation id proves nothing. The
	// seeded workflow.test authority carries the binding's producer identity
	// and records the reference among its evidence.
	seedWorkflowAuthority(t, s, "return-authority-refine-proof-"+workID, workID, "principal/operator", "request/verify", []string{"evidence:return-route-verification"})
	refineProofBindVerification(t, s, workID, "evidence:return-route-verification", "return-authority-refine-proof-"+workID)
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, nil), "verification does not cover the current integrated candidate")

	// The green run in the epoch admits the exit.
	acceptDeliveryBindFresh(t, c, "exit")
	if err := refineProofDelivery(t, s, workID, nil); err != nil {
		t.Fatalf("record_delivery with the candidate's green run: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineExitRefusesAStaleVerifyRun(t *testing.T) {
	const workID = "refine-proof-stale-run"
	c := acceptDeliveryCorrectedCandidateAt(t, workID, "workflow.implementation")
	s := c.f.store
	c.j.bindVerification(t, c.early)
	// The retained report must cover the recorded candidate.
	tooling := refineProofTooling(ProjectDeclaredTool{ID: "go-vet", Invocation: "go vet ./..."})
	before := c.j.effects(t)
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, tooling), "verification does not cover the current integrated candidate")
	if c.j.effects(t) != before {
		t.Fatal("refused stale proof changed durable state")
	}
	reviewGateRequireStep(t, s, workID, "refine")

	refineProofCandidateRun(t, s, workID, "corrected", refineProofCandidate(t, s, workID), []string{"go", "vet", "./..."})
	if err := refineProofDelivery(t, s, workID, tooling); err != nil {
		t.Fatalf("record_delivery with corrected candidate proof: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineExitRefusesWithoutGreenVerifyRunOnBreakFix(t *testing.T) {
	const workID = "refine-proof-break-fix"
	c := acceptDeliveryUnobservedCandidateAt(t, workID, "workflow.break_fix")
	s := c.f.store
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, nil), "no passing verification report covers the recorded Review candidate")
	acceptDeliveryBindFresh(t, c, "exit")
	if err := refineProofDelivery(t, s, workID, nil); err != nil {
		t.Fatalf("record_delivery with the candidate's green run: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineExitRefusesFailedReport(t *testing.T) {
	const workID = "refine-proof-failed-report"
	c := acceptDeliveryUnobservedCandidateAt(t, workID, "workflow.implementation")
	commit := refineProofCandidate(t, c.f.store, workID)
	report := VerificationReport{Source: "required_ci", Commit: commit, Result: "fail", RunRef: "run:failed"}
	err := runVerdictActionAs(t, c.f.store, workID, "bind_evidence", mustJSONValue(map[string]any{"evidence_kind": "verification", "immutable_subject_ref": "commit:" + commit, "verification_report": report}), 0, reviewGateAcceptor(workID))
	if err != nil {
		t.Fatal(err)
	}
	refineProofRequireMissingEvidence(t, refineProofDelivery(t, c.f.store, workID, nil), "verification does not cover the current integrated candidate")
	refineProofCandidateRun(t, c.f.store, workID, "passing", commit, nil)
	if err := refineProofDelivery(t, c.f.store, workID, nil); err != nil {
		t.Fatalf("passing replacement report refused: %v", err)
	}
}

func TestRefineReportDoesNotDependOnDeclaredInvocation(t *testing.T) {
	const workID = "refine-proof-declared-invocation"
	c := acceptDeliveryCandidateAt(t, workID, "workflow.implementation", "refine", false)
	s := c.f.store
	refineProofCandidateRun(t, s, workID, "declared", refineProofCandidate(t, s, workID), []string{"go", "vet", "./..."})

	undeclared := refineProofTooling(ProjectDeclaredTool{ID: "go-test-race", Invocation: "go test -race ./..."})
	if err := refineProofDelivery(t, s, workID, undeclared); err != nil {
		t.Fatalf("record_delivery with the report: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestProjectWithoutManifestPassesOnGreenRunRefusesWithoutRun(t *testing.T) {
	const workID = "refine-proof-no-manifest"
	c := acceptDeliveryUnobservedCandidateAt(t, workID, "workflow.implementation")
	s := c.f.store

	refineProofRequireMissingEvidence(t, refineProofDelivery(t, s, workID, nil), "no passing verification report covers the recorded Review candidate")

	// A recorded passing report qualifies without a tooling manifest.
	c.j.bindVerification(t, c.j.observeRun(t, "exit"))
	if err := refineProofDelivery(t, s, workID, nil); err != nil {
		t.Fatalf("record_delivery without a manifest: %v", err)
	}
	reviewGateRequireStep(t, s, workID, "delivery")
}

func TestRefineProofStartRecorded(t *testing.T) {
	const workID = "refine-proof-start-recorded"
	fixture := refineProofCurrentFixture(t, workID, "workflow.implementation")
	refineProofStartRefine(t, fixture, workID)

	// The current refine start bounds the epoch in event history: the start
	// event predates every binding the guard accepts.
	var startSeq int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT latest_start_seq FROM workflow_step_epochs WHERE work_id=? AND step_id='refine'`, workID).Scan(&startSeq); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_type='work_item' AND subject_id=? AND kind=? AND effect_seq>?`, workID, WorkflowActionStarted, startSeq).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d workflow action starts follow the refine start", count)
	}
}

type refineProofFakeRunner struct {
	responses map[string][]byte
	absent    bool
}

func (r refineProofFakeRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	key := strings.Join(args, " ")
	if r.absent && len(args) > 0 && args[0] == "ls-tree" {
		// The manifest is absent at the ref: ls-tree exits 0 with no output.
		return nil, nil
	}
	if out, ok := r.responses[key]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("unexpected git invocation: %s", key)
}

func TestRefineToolingResolutionReadsTheDefaultRefOnly(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "refine-proof-tooling")

	// The working tree carries a different declaration than the default ref.
	// The resolution resolves the Project's canonical path at this working
	// tree and must still read only the ref: git is faked, so the only way
	// the working-tree file could leak is an os read, which the resolution
	// never performs.
	repo := t.TempDir()
	if err := os.MkdirAll(repo+"/.concord", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repo+"/.concord/tooling.v1.json", []byte(`{"schema_version":"1.0","project":"concord","tools":[{"id":"working-tree-check","invocation":"bin/working-tree-check","tier":"fast"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	seedWorkCanonicalPath(t, s, repo)
	runner := refineProofFakeRunner{responses: map[string][]byte{
		"symbolic-ref refs/remotes/origin/HEAD":           []byte("refs/remotes/origin/main\n"),
		"ls-tree origin/main -- .concord/tooling.v1.json": []byte("100644 blob 1111111111111111111111111111111111111111\t.concord/tooling.v1.json\n"),
		"show origin/main:.concord/tooling.v1.json":       []byte(`{"schema_version":"1.0","project":"concord","tools":[{"id":"default-ref-check","invocation":"bin/check default","tier":"fast"}]}`),
	}}

	tooling, err := resolveWorkProjectTooling(ctx, s, "refine-proof-tooling", runner)
	if err != nil {
		t.Fatalf("resolve the tooling manifest: %v", err)
	}
	if tooling == nil || len(tooling.Declared) != 1 || tooling.Declared[0].ID != "default-ref-check" {
		t.Fatalf("resolved tooling = %+v, want the default ref's declaration", tooling)
	}
	if ProjectToolingInvocationDeclared(tooling, []string{"bin", "working-tree-check"}) {
		t.Fatal("the working tree's declaration leaked into the resolution")
	}
	if !ProjectToolingInvocationDeclared(tooling, []string{"bin/check", "default"}) {
		t.Fatal("the default ref's declaration does not split to its argv")
	}

	// No manifest at the default ref: the resolution reports none and the
	// working tree's manifest stays unread.
	runner.absent = true
	missing, err := resolveWorkProjectTooling(ctx, s, "refine-proof-tooling", runner)
	if err != nil || missing != nil {
		t.Fatalf("resolution with an absent manifest = (%v, %v), want (nil, nil)", missing, err)
	}
}

func TestParseProjectToolingManifest(t *testing.T) {
	parsed, err := ParseProjectToolingManifest([]byte(`{"schema_version":"1.0","project":"concord","tools":[{"id":"go-vet","invocation":"go vet ./...","tier":"fast"}]}`))
	if err != nil {
		t.Fatalf("parse the manifest: %v", err)
	}
	if parsed.Project != "concord" || len(parsed.Declared) != 1 || parsed.Declared[0].Invocation != "go vet ./..." {
		t.Fatalf("parsed manifest = %+v", parsed)
	}
	for name, raw := range map[string]string{
		"not json":       `{`,
		"old schema":     `{"schema_version":"0.9","project":"concord","tools":[{"id":"x","invocation":"x"}]}`,
		"no project":     `{"schema_version":"1.0","tools":[]}`,
		"no tools":       `{"schema_version":"1.0","project":"concord","tools":[]}`,
		"blank argv":     `{"schema_version":"1.0","project":"concord","tools":[{"id":"x","invocation":"  \t "}]}`,
		"missing invoc.": `{"schema_version":"1.0","project":"concord","tools":[{"id":"x"}]}`,
	} {
		if _, err := ParseProjectToolingManifest([]byte(raw)); err == nil {
			t.Fatalf("%s parsed without a refusal", name)
		}
	}
}

// seedWorkCanonicalPath gives the work item's seeded primary Project a
// canonical_path locator, which is the only locator the tooling resolution
// reads.
func seedWorkCanonicalPath(t *testing.T, s *Store, canonicalPath string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('locator:refine-proof','project','canonical_path',?,?,'2026-09-29T00:00:00Z','2026-09-29T00:00:00Z')`, canonicalPath, canonicalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}
