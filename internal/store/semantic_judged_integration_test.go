package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// These journeys use synthetic Git observations and no live worker or native
// command. Dispatch, terminal ingest, and Main acceptance use their real owners.
type semanticJudgedSnapshot struct {
	records map[string]string
	times   [3]string
	finding WorkerOpenOracleFinding
}

func snapshotSemanticJudged(t *testing.T, d *semanticReviewFixture) semanticJudgedSnapshot {
	t.Helper()
	s := semanticJudgedSnapshot{records: map[string]string{}}
	rows, err := d.f.store.db.Query(`SELECT 'dispatch:'||attempt_id,dispatch_payload FROM worker_attempts WHERE work_id=? AND dispatch_payload IS NOT NULL
		UNION ALL SELECT 'terminal:'||attempt_id,terminal_payload FROM worker_attempts WHERE work_id=? AND terminal_payload IS NOT NULL
		UNION ALL SELECT 'authorization:'||attempt_id,payload FROM worker_authorizations WHERE work_id=?`, d.workID, d.workID, d.workID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			t.Fatal(err)
		}
		s.records[key] = raw
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := d.f.store.db.QueryRow(`SELECT w.created_at,i.started_at,i.execution_started_at FROM work_items w JOIN workflow_instances i ON i.work_id=w.id WHERE w.id=?`, d.workID).Scan(&s.times[0], &s.times[1], &s.times[2]); err != nil {
		t.Fatal(err)
	}
	for _, at := range s.times {
		if _, err := time.Parse(time.RFC3339Nano, at); err != nil {
			t.Fatalf("capture/start timestamp %q: %v", at, err)
		}
	}
	lineage, err := readWorkerOracleFindingLineageTx(t.Context(), d.f.store.db, d.workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lineage.findings) != 1 || lineage.findings[d.canonicalID] == nil {
		t.Fatalf("canonical identity changed: %+v", lineage.findings)
	}
	s.finding = *lineage.findings[d.canonicalID]
	// Closure is the only allowed mutation to the canonical source finding.
	s.finding.Closed, s.finding.ClosureKind = false, ""
	return s
}

func (before semanticJudgedSnapshot) requireRetained(t *testing.T, d *semanticReviewFixture) {
	t.Helper()
	after := snapshotSemanticJudged(t, d)
	for key, raw := range before.records {
		if got, ok := after.records[key]; !ok || got != raw {
			t.Errorf("retained %s changed: before=%s after=%s", key, raw, got)
		}
	}
	if before.times != after.times {
		t.Errorf("capture/start timestamps changed: before=%v after=%v", before.times, after.times)
	}
	if !reflect.DeepEqual(before.finding, after.finding) {
		t.Errorf("canonical source identity/provenance changed: before=%+v after=%+v", before.finding, after.finding)
	}
}

func semanticJudgedRefineFixture(t *testing.T, workID string) *semanticReviewFixture {
	t.Helper()
	// Seed the existing return-route fixture at refine, where combined acceptance
	// is admitted. Never move an open repair finding past the delivery guard.
	d := &semanticReviewFixture{
		f:      oracleRepairFixtureFrom(t, workID, seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "refine")),
		workID: workID, localID: "finding:original:0",
		tie: WorkerOracleFinding{Classification: OracleClassificationUncoveredCase, OwnerID: "owner:repair", EntryPoint: "citation accuracy", FailureFamily: "citation-accuracy", EvidenceRefs: []string{"fixture:unsupported-citation"}},
	}
	t.Cleanup(func() { d.f.store.Close() })
	d.fields = twoPathOracleFields(t, d.f.store, workID, nil)
	d.job = recordOracleJobRevision(t, d.f.store, workID, d.f.owner, "job:citation-review", d.fields)
	candidate := d.dispatch(t, "attempt:original", "review")
	if err := d.terminal(t, d.completion(t, "attempt:original", "review", candidate, false)); err != nil {
		t.Fatal(err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(t.Context(), d.f.store.db, workID)
	if err != nil || len(lineage.openFindingIDs()) != 1 {
		t.Fatalf("original lineage=%+v err=%v", lineage, err)
	}
	d.canonicalID = lineage.openFindingIDs()[0]
	advanceOracleFixtureSubject(t, d.f, workID, "2")
	d.fields["cases"] = append(d.fields["cases"].([]map[string]any), map[string]any{"case_id": "case:semantic", "owner_id": "owner:repair", "entry_point": d.tie.EntryPoint, "input_class": "citation", "expected_state": "source supports claim", "judge": "review"})
	d.job = recordOracleJobRevision(t, d.f.store, workID, d.f.owner, d.job.JobID, d.fields)
	return d
}

func TestSemanticJudgedCombinedAcceptanceDelivery(t *testing.T) {
	d := semanticJudgedRefineFixture(t, "judged-combined-delivery")
	original := snapshotSemanticJudged(t, d)
	candidate := d.dispatch(t, "attempt:judgment", "review")
	if err := d.terminal(t, d.completion(t, "attempt:judgment", "review", candidate, true)); err != nil {
		t.Fatal(err)
	}
	d.assertClosed(t, false)
	before := snapshotSemanticJudged(t, d)
	refineProofSeedGreenRun(t, d.f.store, d.workID, "judged-refine-proof")
	workerJobIntegrationGreenRun(t, d.f.store, d.workID, "judged-integration-proof")
	if err := d.journey().action(t, "accept_worker_result", map[string]any{
		"attempt_id": "attempt:judgment", "attempt_epoch": d.journey().dispatchEpoch(t, "attempt:judgment"),
		"delivery_artifact": "artifact:judged-citation", "delivery_state": "asserted",
	}, d.f.owner); err != nil {
		t.Fatalf("combined acceptance/delivery: %v", err)
	}
	d.assertClosed(t, true)
	reviewGateRequireStep(t, d.f.store, d.workID, "delivery")
	var count int
	if err := d.f.store.db.QueryRow(`SELECT count(*) FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id='attempt:judgment' AND action_id='accept_worker_result' AND json_extract(payload,'$.delivery_artifact')='artifact:judged-citation' AND json_extract(payload,'$.delivery_state')='asserted'`, d.workID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("combined disposition count=%d err=%v", count, err)
	}
	if seq, err := workflowLatestDeliveryAssertionSeq(t.Context(), d.f.store, d.workID); err != nil || seq == 0 {
		t.Fatalf("delivery assertion seq=%d err=%v", seq, err)
	}
	original.requireRetained(t, d)
	before.requireRetained(t, d)
}

// Model exactly one descendant repair and its scoped delta. The shared fake
// models unrelated tips, so it cannot supply these two observations itself.
type semanticJudgedRepairGit struct {
	*fakeWorktreeGit
	input, output string
}

func (g semanticJudgedRepairGit) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if reflect.DeepEqual(args, []string{"merge-base", "--is-ancestor", g.input, g.output}) {
		return nil, nil
	}
	if reflect.DeepEqual(args, append(workerDiffSharedDiffArgs(), "--name-only", "-z", g.input, g.output)) {
		return []byte("internal/store/citation.go\x00"), nil
	}
	return g.fakeWorktreeGit.Run(ctx, dir, args...)
}

func TestSemanticJudgedRepairingReviewAcceptance(t *testing.T) {
	d := newSemanticReviewFixture(t, "judged-repairing-review", false)
	original := snapshotSemanticJudged(t, d)
	input := d.dispatch(t, "attempt:repair", "review")
	output := WorkerReviewCandidate{ProjectID: input.ProjectID, Commit: strings.Repeat("3", 40), Tree: strings.Repeat("3", 40)}
	d.f.git.branches["fixture-review-input"] = input.Commit
	d.f.git.branches[d.f.entry.Branch] = output.Commit
	e := d.completion(t, "attempt:repair", "review", input, true)
	var p WorkerCompletedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	p.Review.OutputCandidate = output
	e.Payload = mustJSONValue(p)
	prepared, err := PrepareWorkerTerminalDelivery(t.Context(), d.f.store, PrepareWorkerTerminalDeliveryRequest{
		WorkID: d.workID, AttemptID: "attempt:repair", EventID: e.EventID,
		Runner: semanticJudgedRepairGit{d.f.git, input.Commit, output.Commit},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkerTerminalPreparation(t.Context(), prepared)
	if err := d.f.store.TransactDurable(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkerEvidenceOperationTx(ctx, tx, e)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d.assertClosed(t, false)
	before := snapshotSemanticJudged(t, d)
	var retained WorkerCompletedPayload
	if err := json.Unmarshal([]byte(before.records["terminal:attempt:repair"]), &retained); err != nil {
		t.Fatal(err)
	}
	o := retained.ObservedDelivery
	if o == nil || o.State != "clean" || o.Commit != output.Commit || o.Tree != output.Tree || o.Base != input.Commit || o.BaseTree != input.Tree || !reflect.DeepEqual(o.ChangedPaths, []string{"internal/store/citation.go"}) {
		t.Fatalf("repair output lacks exact host observation: %+v", o)
	}
	if retained.Review.InputCandidate != input || retained.Review.OutputCandidate != output || input == output {
		t.Fatalf("repair input/output=%+v", retained.Review)
	}
	if err := d.journey().acceptEvidence(t, "attempt:repair"); err != nil {
		t.Fatal(err)
	}
	d.assertClosed(t, true)
	state, err := readWorkflowReviewFacts(t.Context(), d.f.store.db, d.workID)
	if err != nil || state.Candidate == nil || *state.Candidate != output || state.Accepted == nil || state.Accepted.AttemptID != "attempt:repair" {
		t.Fatalf("accepted repair candidate=%+v err=%v", state, err)
	}
	original.requireRetained(t, d)
	before.requireRetained(t, d)
}

func TestSemanticJudgedRepeatedExactAcceptance(t *testing.T) {
	d := newSemanticReviewFixture(t, "judged-repeated-acceptance", false)
	original := snapshotSemanticJudged(t, d)
	candidate := d.dispatch(t, "attempt:judgment", "review")
	if err := d.terminal(t, d.completion(t, "attempt:judgment", "review", candidate, true)); err != nil {
		t.Fatal(err)
	}
	d.assertClosed(t, false)
	before := snapshotSemanticJudged(t, d)
	if err := d.journey().acceptEvidence(t, "attempt:judgment"); err != nil {
		t.Fatal(err)
	}
	d.assertClosed(t, true)
	var firstRef, firstPayload string
	var firstSeq int64
	if err := d.f.store.db.QueryRow(`SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, d.workID, d.job.JobID, d.job.Revision).Scan(&firstRef); err != nil || firstRef == "" {
		t.Fatalf("first job satisfaction=%q err=%v", firstRef, err)
	}
	if err := d.f.store.db.QueryRow(`SELECT first_acceptance_seq,first_acceptance_payload FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id='attempt:judgment' AND action_id='accept_worker_result'`, d.workID).Scan(&firstSeq, &firstPayload); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := d.journey().acceptEvidence(t, "attempt:judgment"); err != nil {
			t.Fatalf("repeated exact acceptance: %v", err)
		}
		d.assertClosed(t, true)
		before.requireRetained(t, d)
	}
	var ref, payload string
	var seq, latest int64
	if err := d.f.store.db.QueryRow(`SELECT satisfied_result_ref FROM worker_job_revisions WHERE work_id=? AND job_id=? AND revision=?`, d.workID, d.job.JobID, d.job.Revision).Scan(&ref); err != nil || ref != firstRef {
		t.Fatalf("satisfaction watermark changed: before=%s after=%s err=%v", firstRef, ref, err)
	}
	if err := d.f.store.db.QueryRow(`SELECT first_acceptance_seq,first_acceptance_payload,seq FROM worker_attempt_dispositions WHERE work_id=? AND attempt_id='attempt:judgment' AND action_id='accept_worker_result'`, d.workID).Scan(&seq, &payload, &latest); err != nil || seq != firstSeq || payload != firstPayload || latest <= seq {
		t.Fatalf("first acceptance changed or repeat not recorded: first=%d current=%d latest=%d err=%v", firstSeq, seq, latest, err)
	}
	original.requireRetained(t, d)
}

func TestSemanticJudgedInvalidReportThenAuthorizedReview(t *testing.T) {
	d := newSemanticReviewFixture(t, "judged-invalid-report-recovery", false)
	original := snapshotSemanticJudged(t, d)
	candidate := d.dispatch(t, "attempt:malformed", "review")
	before := snapshotSemanticJudged(t, d)
	e := d.completion(t, "attempt:malformed", "review", candidate, true)
	var p WorkerCompletedPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		t.Fatal(err)
	}
	p.Review.Disposition = "not-a-review-disposition"
	e.Payload = mustJSONValue(p)
	// The report is malformed without consulting any mutable Store state.
	if err := validateWorkerCompletedPayload(e, p); err == nil {
		t.Fatal("malformed disposition passed report-shape validation")
	}
	if err := d.terminal(t, e); !hasFailureKind(err, KindInvalidPayload) {
		t.Fatalf("malformed report refusal=%v", err)
	}
	d.assertClosed(t, false)
	before.requireRetained(t, d)
	failed := s2WorkerFailedAttributedEvent(d.workID, "attempt:malformed", "malformed:failed", preferredModelForLane(reviewGateLane(t, "review")), WorkerFailureInvalidReport, "Review returned an unknown disposition: not-a-review-disposition", time.Now().UTC())
	if err := s2WorkerFailedLive(t, d.f.store, failed); err != nil {
		t.Fatal(err)
	}
	d.assertClosed(t, false)
	var lifecycle, kind string
	if err := d.f.store.db.QueryRow(`SELECT lifecycle_state,failure_kind FROM worker_attempts WHERE work_id=? AND attempt_id='attempt:malformed'`, d.workID).Scan(&lifecycle, &kind); err != nil || lifecycle != "failed" || kind != WorkerFailureInvalidReport {
		t.Fatalf("invalid-report terminal=%s/%s err=%v", lifecycle, kind, err)
	}
	if records := s2DerivedFailureRecords(t, d.f.store, d.workID, "attempt:malformed"); len(records) != 1 {
		t.Fatalf("derived failure records=%+v", records)
	}
	failedSnapshot := snapshotSemanticJudged(t, d)
	fresh := d.dispatch(t, "attempt:recovery", "review")
	if d.journey().dispatchEpoch(t, "attempt:recovery") <= d.journey().dispatchEpoch(t, "attempt:malformed") {
		t.Fatal("recovery did not acquire a separate fenced authorization")
	}
	if err := d.terminal(t, d.completion(t, "attempt:recovery", "review", fresh, true)); err != nil {
		t.Fatal(err)
	}
	d.assertClosed(t, false)
	pending := snapshotSemanticJudged(t, d)
	if err := d.journey().acceptEvidence(t, "attempt:recovery"); err != nil {
		t.Fatal(err)
	}
	d.assertClosed(t, true)
	original.requireRetained(t, d)
	before.requireRetained(t, d)
	failedSnapshot.requireRetained(t, d)
	pending.requireRetained(t, d)
}
