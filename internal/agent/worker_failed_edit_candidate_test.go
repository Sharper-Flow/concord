package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// Candidate recovery must reach both dispatch and acceptance. A preparation
// receipt cannot substitute for Main accepting a completed result.
func TestFailedEditingCandidateRecoveryPublicRoute(t *testing.T) {
	for _, laneID := range []string{"implement", "review"} {
		for _, edited := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/edited=%t", laneID, edited), func(t *testing.T) {
				f := &oAgentFixtureState{spineFixture: newSpineExecutionFixture(t)}
				oracle := failedEditThreeControlOracle(t, f)
				const jobID = "job:failed-edit-recovery"
				fields := map[string]any{
					"job_id": jobID, "objective": "Preserve the checked value.", "stopping_condition": "The value check passes.",
					"checks": []string{"go test ./..."}, "ready": true, "path_scope": []string{"o_fixture/value.go"},
					"readiness_evidence": oracle.Controls[0].ReadinessEvidenceRefs, "acceptance_oracle": oracle,
				}
				spineOK(t, revisionRecoveryInvoke(t, f, "record_worker_job", "record-baseline", fields))
				oAgentReviewJob(t, f, jobID, "continue", false)
				baseline := f.read(t).Pinned.Pin.Review
				if baseline == nil || baseline.Candidate == nil || baseline.Accepted == nil || baseline.Accepted.Report.Disposition != "continue" {
					t.Fatalf("baseline lacks genuinely accepted candidate/Review: %+v", baseline)
				}
				baselineBytes := failedEditReviewBytes(baseline)
				const failed = "attempt:failed-edit"
				lane, job, epoch := f.startWorker(t, laneID, failed)
				if edited {
					if err := os.WriteFile(filepath.Join(f.path, "o_fixture/value.go"), []byte("package fixture\nfunc Value() int { return 3 + 4 }\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					gitRun(t, f.path, "add", "o_fixture/value.go")
					gitRun(t, f.path, "commit", "-qm", "retained worker correction")
				}
				retained := gitRun(t, f.path, "rev-parse", "HEAD")
				gitRun(t, f.path, "merge-base", "--is-ancestor", baseline.Candidate.Commit, retained)
				if (retained != baseline.Candidate.Commit) != edited || gitRun(t, f.path, "status", "--porcelain") != "" {
					t.Fatal("fixture did not retain the expected clean descendant")
				}
				kind := store.WorkerFailureWorkerError
				if laneID == "review" {
					failedEditRefuseReviewReport(t, f, lane, job, failed, *baseline.Accepted, retained)
					kind = store.WorkerFailureInvalidReport
				}
				failure := agentWorkerFailedAttributedEvent("failed-edit-terminal", store.WorkerFailedPayload{
					AttemptID: failed, ReadbackModel: "openai/test-model", FailureKind: kind, Detail: "synthetic retained editing failure",
				})
				failure.SubjectID, failure.OccurredAt = f.work, f.service.Now()
				if err := f.s.Transact(t.Context(), func(tx *store.Transaction) error {
					_, err := store.ApplyWorkerEvidenceOperationTx(t.Context(), tx, failure)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				failedBytes := failedEditAttemptBytes(t, f, failed)
				if got := f.read(t).Pinned.Pin.Review; !reflect.DeepEqual(failedEditReviewBytes(got), baselineBytes) {
					t.Fatal("failure installed an unaccepted candidate or changed baseline Review")
				}
				fresh := prepareRevisionRecoveryOracle(t, f, oracle, "retained-candidate")
				fields["acceptance_oracle"] = fresh
				fields["readiness_evidence"] = fresh.Controls[0].ReadinessEvidenceRefs
				spineOK(t, revisionRecoveryInvoke(t, f, "record_worker_job", "ordinary-correction", fields))
				state := f.read(t)
				if len(state.Pinned.Jobs) != 1 || state.Pinned.Jobs[0].JobID != jobID || state.Pinned.Jobs[0].Revision != 2 || len(state.Pinned.Jobs[0].AcceptanceOracle.Controls) != 3 {
					t.Fatalf("ordinary correction did not preserve the same three-control job: %+v", state.Pinned.Jobs)
				}
				if state.Pinned.Pin.WorkContext.SubjectCommit != retained || state.Pinned.Pin.Review.Candidate.Commit != baseline.Candidate.Commit {
					t.Fatal("preparation fabricated candidate acceptance or lost retained subject")
				}
				before := revisionRecoveryState(t, f)
				packet := failedEditPacket(t, f, "implement", "attempt:must-review")
				refused := revisionRecoveryInvoke(t, f, "dispatch_worker", "must-review", map[string]any{"attempt_id": "attempt:must-review", "worker_packet": packet})
				if refused.Error == nil || refused.Error.Kind != "missing_evidence" || !strings.Contains(refused.Error.Message, "accepted Review") || refused.Error.EffectState != agentcontract.EffectNone || revisionRecoveryState(t, f) != before {
					t.Fatalf("ordinary correction bypassed the required Review or changed state: %+v", refused.Error)
				}
				// Failure never creates a completed result Main can accept.
				noResult := revisionRecoveryInvoke(t, f, "accept_worker_result", "cannot-accept-failure", map[string]any{"attempt_id": failed, "attempt_epoch": epoch})
				if noResult.Error == nil || noResult.Error.EffectState != agentcontract.EffectNone || revisionRecoveryState(t, f) != before {
					t.Fatalf("failed attempt became an acceptable result: %+v", noResult.Error)
				}
				t.Logf("failed-attempt acceptance refused: kind=%s message=%q", noResult.Error.Kind, noResult.Error.Message)
				const reviewAttempt = "attempt:fresh-required-review"
				packet = failedEditPacket(t, f, "review", reviewAttempt)
				var packetInput struct {
					Inputs struct {
						ReviewInput store.WorkerReviewInput `json:"review_input"`
					} `json:"inputs"`
				}
				if err := json.Unmarshal(retryJSON(packet), &packetInput); err != nil {
					t.Fatal(err)
				}
				disclosure := packetInput.Inputs.ReviewInput.RetainedFailedEdit
				if edited {
					if disclosure == nil || disclosure.AcceptedCandidate != *baseline.Candidate || !reflect.DeepEqual(disclosure.ChangedPaths, []string{"o_fixture/value.go"}) || packetInput.Inputs.ReviewInput.InputCandidate.Commit != retained {
						t.Fatalf("public runtime lost retained delta disclosure: %+v", packetInput.Inputs.ReviewInput)
					}
				} else if disclosure != nil {
					t.Fatalf("unchanged input invented retained delta: %+v", disclosure)
				}
				response := revisionRecoveryInvoke(t, f, "dispatch_worker", "fresh-required-review", map[string]any{"attempt_id": reviewAttempt, "worker_packet": packet})
				if response.Error != nil {
					if revisionRecoveryState(t, f) != before || failedEditAttemptBytes(t, f, failed) != failedBytes || !reflect.DeepEqual(failedEditReviewBytes(f.read(t).Pinned.Pin.Review), baselineBytes) {
						t.Fatal("refused fresh Review changed authority or failed history")
					}
					if !edited || response.Error.Kind != "stale_requires_review" || response.Error.Message != "Review input differs from Main's current integrated candidate" {
						t.Fatalf("unexpected fresh Review refusal (edited=%t): %s", edited, retryJSON(response.Error))
					}
					t.Fatalf("fresh required Review cannot reach result acceptance (edited=%t): %s", edited, retryJSON(response.Error))
				}
				// Exercise acceptance too: a dispatch-only exception is not recovery.
				failedEditStartAuthorized(t, f, "review", reviewAttempt, response)
				current := f.read(t)
				candidate := store.WorkerReviewCandidate{ProjectID: "project-1", Commit: retained, Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
				review := &store.WorkerReviewBlock{Disposition: "continue", InputCandidate: candidate, OutputCandidate: candidate,
					Dependencies: *current.Pinned.Pin.Review.Dependencies, Assessments: []store.WorkerReviewAssessment{}, Findings: []store.WorkerReviewFinding{}, RemainingObligations: []store.WorkerReviewObligation{}}
				binding := &store.WorkerJobBinding{JobID: jobID, Revision: current.Pinned.Jobs[0].Revision, Digest: current.Pinned.Jobs[0].Digest}
				f.finishWorker(t, deliveryRecoveryLane(t, "review"), reviewAttempt, binding, review)
				spineOK(t, revisionRecoveryInvoke(t, f, "accept_worker_result", "accept-fresh-review", map[string]any{"attempt_id": reviewAttempt, "attempt_epoch": current.Pinned.Pin.Attempt.Epoch}))
				// Main must also accept the subsequent implementation, not merely launch it.
				implLane, implJob, implEpoch := f.startWorker(t, "implement", "attempt:fresh-correction")
				if err := os.WriteFile(filepath.Join(f.path, "o_fixture/value.go"), []byte("package fixture\nfunc Value() int { return 14 / 2 }\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				gitRun(t, f.path, "add", "o_fixture/value.go")
				gitRun(t, f.path, "commit", "-qm", "fresh in-scope correction")
				f.verify(t)
				f.finishWorker(t, implLane, "attempt:fresh-correction", implJob, nil)
				spineOK(t, revisionRecoveryInvoke(t, f, "accept_worker_result", "accept-fresh-correction", map[string]any{"attempt_id": "attempt:fresh-correction", "attempt_epoch": implEpoch}))
				if failedEditAttemptBytes(t, f, failed) != failedBytes {
					t.Fatal("recovery rewrote failed attempt")
				}
				t.Logf("accepted baseline -> failed %s -> ordinary revision2 -> required Review acceptance -> correction result acceptance; edited=%t", laneID, edited)
			})
		}
	}
}

func failedEditThreeControlOracle(t *testing.T, f *oAgentFixtureState) store.AcceptanceOracle {
	t.Helper()
	oracle := oAgentPreparedOracle(t, f, []string{"predicate:primary"})
	for i := 1; i < 3; i++ {
		control := oracle.Controls[0]
		control.ControlID, control.CaseIDs = fmt.Sprintf("control:retained-%d", i), []string{fmt.Sprintf("case:retained-%d", i)}
		control.RecipeSource.Path = fmt.Sprintf("retained-%d.json", i)
		manifest := fmt.Sprintf(`{"kind":"go_top_level_v1","package_cwd":"o_fixture","test_files":["o_fixture/value_test.go"],"fixture_files":["o_fixture/oracle.json"],"cases":{%q:["TestValue"]}}`, control.CaseIDs[0])
		if err := os.WriteFile(filepath.Join(f.path, control.RecipeSource.Path), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		oracle.Controls = append(oracle.Controls, control)
		entry := oracle.Cases[0]
		entry.CaseID, entry.ControlIDs = control.CaseIDs[0], []string{control.ControlID}
		oracle.Cases = append(oracle.Cases, entry)
	}
	gitRun(t, f.path, "add", "retained-1.json", "retained-2.json")
	gitRun(t, f.path, "commit", "-qm", "three pinned recovery controls")
	for i := range oracle.Controls {
		oracle.Controls[i].RecipeSource.CommitOID = gitRun(t, f.path, "rev-parse", "HEAD")
	}
	return prepareRevisionRecoveryOracle(t, f, oracle, "baseline-three-controls")
}

func failedEditLane(t *testing.T, laneID string) store.LaneDefinition {
	t.Helper()
	lane, ok := builtinLane(laneID)
	if !ok {
		t.Fatalf("lane %s unavailable", laneID)
	}
	return lane
}

func failedEditPacket(t *testing.T, f *oAgentFixtureState, laneID, attempt string) map[string]any {
	t.Helper()
	state := f.read(t)
	lane := failedEditLane(t, laneID)
	inputs := map[string]any{}
	if state.Pinned.Pin.Correction != nil {
		inputs["correction"] = state.Pinned.Pin.Correction
	}
	return bindPacketToRecordedState(t, f.s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion,
		"attempt_id": attempt, "lane_id": lane.ID, "lane_digest": lane.Digest, "work_id": f.work, "step_id": state.Pinned.Step, "inputs": inputs})
}

func failedEditStartAuthorized(t *testing.T, f *oAgentFixtureState, laneID, attempt string, response agentcontract.Envelope) {
	t.Helper()
	var authorization struct {
		PacketDigest string `json:"worker_packet_digest"`
	}
	if err := json.Unmarshal(response.Result, &authorization); err != nil || authorization.PacketDigest == "" {
		t.Fatalf("missing authorization: %s (%v)", response.Result, err)
	}
	lane := failedEditLane(t, laneID)
	ready := f.read(t).Pinned.Jobs[0]
	job := &store.WorkerJobBinding{JobID: ready.JobID, Revision: ready.Revision, Digest: ready.Digest}
	event := store.Event{EventID: "started-" + attempt, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: f.work, Actor: "worker:test", OccurredAt: f.service.Now(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerDispatched), Payload: retryJSON(store.WorkerDispatchedPayload{
		AttemptID: attempt, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass, PacketDigest: authorization.PacketDigest, ReadbackModel: "openai/test-model", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: job})}
	if err := f.s.Transact(t.Context(), func(tx *store.Transaction) error {
		pair, err := store.PrepareLaneActorDispatch(t.Context(), tx, event, f.env.PrincipalRef, f.env.ClientRef)
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(t.Context(), tx, pair)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func failedEditAttemptBytes(t *testing.T, f *oAgentFixtureState, attempt string) string {
	t.Helper()
	var state, payload string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,terminal_payload FROM worker_attempts WHERE work_id=? AND attempt_id=?`, f.work, attempt).Scan(&state, &payload); err != nil || state != "failed" {
		t.Fatalf("failed terminal missing: %s (%v)", state, err)
	}
	return payload
}

func failedEditReviewBytes(review *store.WorkflowReviewState) json.RawMessage {
	return retryJSON(map[string]any{"candidate": review.Candidate, "accepted": review.Accepted})
}

func failedEditRefuseReviewReport(t *testing.T, f *oAgentFixtureState, lane store.LaneDefinition, job *store.WorkerJobBinding, attempt string, accepted store.WorkflowAcceptedReview, retained string) {
	t.Helper()
	report := accepted.Report
	report.OutputCandidate = store.WorkerReviewCandidate{ProjectID: "project-1", Commit: retained, Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	report.Findings = []store.WorkerReviewFinding{{FindingID: "finding:missing-classification", Severity: "P2", Confidence: "high", Detail: "Synthetic missing oracle classification", InScope: true, Status: "open", EvidenceRefs: []string{}, ClosureRefs: []string{}}}
	evidence := []store.WorkerReportEvidence{}
	for _, obligation := range lane.EvidenceObligations {
		if obligation != "severity" {
			evidence = append(evidence, store.WorkerReportEvidence{Obligation: obligation, Detail: "Synthetic Review evidence."})
		}
	}
	event := store.Event{EventID: "invalid-editing-review", Kind: store.WorkerCompleted, SubjectType: store.SubjectWorkItem, SubjectID: f.work, Actor: "worker:test", OccurredAt: f.service.Now(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerCompleted), Payload: retryJSON(store.WorkerCompletedPayload{AttemptID: attempt, ReadbackModel: "openai/test-model", ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerDirectory: f.path, Evidence: evidence, EvidenceOrigin: store.WorkerEvidenceReported, WorkerJob: job, Review: &report})}
	before := revisionRecoveryState(t, f)
	prepared, err := store.PrepareWorkerTerminalDelivery(t.Context(), f.s, store.PrepareWorkerTerminalDeliveryRequest{WorkID: f.work, AttemptID: attempt, EventID: event.EventID})
	if err != nil {
		t.Fatal(err)
	}
	ctx := store.WithWorkerTerminalPreparation(context.Background(), prepared)
	err = f.s.Transact(ctx, func(tx *store.Transaction) error {
		_, err := store.ApplyWorkerEvidenceOperationTx(ctx, tx, event)
		return err
	})
	var failure *store.Failure
	if !errors.As(err, &failure) || failure.Kind != store.KindInvalidPayload || !strings.Contains(failure.Detail, "a review finding on an oracle-bound dispatch carries no oracle classification") || revisionRecoveryState(t, f) != before {
		t.Fatalf("wrong invalid-report refusal or terminal effects: %v", err)
	}
}
