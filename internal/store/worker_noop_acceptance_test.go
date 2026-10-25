package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAcceptWorkerResultNoChangeProvesDeclaredOutcomeAndReachesReview(t *testing.T) {
	const workID = "accept-confirmed-outcome"
	f := seedOracleRepairFixture(t, workID)
	defer f.store.Close()
	job := recordOracleJobRevision(t, f.store, workID, f.owner, "job:confirm", twoPathOracleFields(t, f.store, workID, nil))
	attempt := "attempt:" + workID
	subject := dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
	receipts, _ := noChangeOutcomeReceipts(t, f, workID, true)
	completed := completeChangeAcceptanceAttempt(t, f, workID, attempt, subject, func(p *WorkerCompletedPayload) {
		appendNoChangeReceipts(p, receipts)
	})
	if completed.ObservedDelivery == nil || completed.ObservedDelivery.State != "clean" || completed.ObservedDelivery.Commit != subject {
		t.Fatalf("confirmation did not retain the clean dispatch subject: %+v", completed.ObservedDelivery)
	}
	if err := acceptChangeTestAttempt(t, f, workID, attempt); err != nil {
		t.Fatalf("declared outcome already passes without a change: %v", err)
	}
	review := "attempt:confirmed-review"
	reviewJob := recordOracleJobRevision(t, f.store, workID, f.owner, "job:confirmed-review", twoPathOracleFields(t, f.store, workID, nil))
	lane := reviewGateLane(t, "review")
	packet := joinPacketFor(t, f.store, workID, "repair", review, lane.ID, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, f.store, workID, reviewJob, lane.ID)
	if err := dispatchJobPacketForTest(t, f.store, workID, review, f.owner, "dispatch-confirmed-review", packet); err != nil {
		t.Fatalf("review after accepted confirmation: %v", err)
	}
	if window := reviewSubjectWindow(t, f.store, workID, review); window.SubjectCommit != subject {
		t.Fatalf("review subject = %s, want unchanged accepted subject %s", window.SubjectCommit, subject)
	}
}

func TestAcceptWorkerResultNoChangeRequiresCompleteFreshOutcomeProof(t *testing.T) {
	for _, name := range []string{"no-proof", "missing-control", "later-failure", "unbound-independent", "self-produced-independent"} {
		t.Run(name, func(t *testing.T) {
			workID := "accept-nochange-" + name
			f := seedOracleRepairFixture(t, workID)
			defer f.store.Close()
			job := recordOracleJobRevision(t, f.store, workID, f.owner, "job:confirm", twoPathOracleFields(t, f.store, workID, nil))
			attempt := "attempt:" + workID
			subject := dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
			receipts, independent := noChangeOutcomeReceipts(t, f, workID, name != "unbound-independent")
			if name == "self-produced-independent" {
				req := verifyRequest(f.git, "self-confirm-"+workID, oracleGoArgv("path-b"), func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil })
				req.WorkID, req.ProjectID = workID, "project"
				if err := f.store.db.QueryRow(`SELECT a.principal_ref,a.client_ref,a.agent_ref,a.session_ref FROM workflow_actors a JOIN domain_events e ON a.actor_ref=json_extract(e.payload,'$.lane_actor_ref') WHERE e.subject_id=? AND e.kind=? AND json_extract(e.payload,'$.attempt_id')=?`, workID, WorkerDispatched, attempt).Scan(&req.PrincipalRef, &req.Owner.ClientRef, &req.Owner.AgentRef, &req.Owner.SessionRef); err != nil {
					t.Fatal(err)
				}
				self, err := f.store.VerifyWorktree(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				seedOracleExecuteLedgerFixture(t, f.store, &self, oracleFixtureGraphForCommand(t, f.store, workID, req.Command))
				bindNoChangeRun(t, f, workID, self, req.PrincipalRef)
				receipts[1].RunRef, receipts[1].EvidenceRefs = self.OperationRef, []string{self.OperationRef}
			}
			switch name {
			case "no-proof":
				receipts = nil
			case "missing-control":
				receipts = receipts[:1]
			}
			completeChangeAcceptanceAttempt(t, f, workID, attempt, subject, func(p *WorkerCompletedPayload) {
				appendNoChangeReceipts(p, receipts)
			})
			if name == "later-failure" {
				oracleVerifyControl(t, f, workID, "outcome-still-fails", "1", oracleGoArgv("path-a"), 1)
			}
			before := maxEventSeq(t, f.store, workID)
			err := acceptChangeTestAttempt(t, f, workID, attempt)
			if err == nil || !strings.Contains(err.Error(), "no changed paths") {
				t.Fatalf("accept with %s proof = %v, want no-change refusal", name, err)
			}
			var failure *Failure
			if !errors.As(err, &failure) || !strings.Contains(failure.RecoveryAction, "reject_worker_result and dispatch a correction") {
				t.Fatalf("no-change refusal recovery must name a route open to a terminal result: %v", err)
			}
			if after := maxEventSeq(t, f.store, workID); after != before {
				t.Fatalf("refused acceptance wrote events: before=%d after=%d", before, after)
			}
			if name == "unbound-independent" {
				bindNoChangeRun(t, f, workID, independent, "principal-1")
				if err := acceptChangeTestAttempt(t, f, workID, attempt); err != nil {
					t.Fatalf("accept after binding the independent run: %v", err)
				}
			}
		})
	}
}

func appendNoChangeReceipts(p *WorkerCompletedPayload, receipts []WorkerOracleReceipt) {
	for i := range receipts {
		entry := p.Evidence[0]
		entry.Detail = receipts[i].RunRef
		entry.OracleReceipt = &receipts[i]
		p.Evidence = append(p.Evidence, entry)
	}
}

func noChangeOutcomeReceipts(t *testing.T, f oracleRepairFixture, workID string, bindIndependent bool) ([]WorkerOracleReceipt, WorktreeVerifyResult) {
	t.Helper()
	var receipts []WorkerOracleReceipt
	var independent WorktreeVerifyResult
	for _, path := range []string{"a", "b"} {
		run := oracleVerifyControl(t, f, workID, "confirm-"+path, "1", oracleGoArgv("path-"+path), 0)
		if path == "b" {
			independent = run
			if bindIndependent {
				bindNoChangeRun(t, f, workID, run, "principal-1")
			}
		}
		exit := 0
		receipts = append(receipts, WorkerOracleReceipt{ControlIDs: []string{"control:check-" + path}, CaseIDs: []string{"case:path-" + path}, SubjectCommit: strings.TrimPrefix(run.SubjectRef, "commit:"),
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: OracleReceiptResultPass, ExitCode: &exit, RunRef: run.OperationRef, EvidenceRefs: []string{run.OperationRef}})
	}
	return receipts, independent
}

func bindNoChangeRun(t *testing.T, f oracleRepairFixture, workID string, run WorktreeVerifyResult, principal string) {
	t.Helper()
	event := workflowTypedEvent("bind-"+run.LeaseID, WorkflowEvidenceBound, workID, "actor:test", time.Unix(60, 0).UTC(), readWorkVersion(t, f.store, workID), map[string]any{
		"evidence_kind": "verification", "immutable_subject_ref": run.OperationRef, "producer_id": principal,
		"producer_run_ref": run.OperationRef, "producer_watermark": "req-v-" + run.LeaseID, "observed_at": time.Unix(60, 0).UTC().Format(time.RFC3339Nano),
	})
	if err := applyWorkflowTestOperation(context.Background(), f.store, Operation{Events: []Event{event}}); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptWorkerResultRefusesNoOpLaneReport(t *testing.T) {
	const workID = "accept-noop-implementation"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := recordOracleJobRevision(t, s, workID, f.owner, "job:noop", twoPathOracleFields(t, s, workID, nil))
	attempt := "attempt:" + workID
	subject := dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
	failed := oracleVerifyControl(t, f, workID, "required-outcome-fails", "1", oracleGoArgv("path-a"), 1)
	exit := 1
	completed := completeChangeAcceptanceAttempt(t, f, workID, attempt, subject, func(p *WorkerCompletedPayload) {
		withReceipt(p, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, SubjectCommit: subject,
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: OracleReceiptResultFail, ExitCode: &exit, RunRef: failed.OperationRef, EvidenceRefs: []string{failed.OperationRef}})
	})
	if completed.ObservedDelivery == nil || completed.ObservedDelivery.State != "clean" || completed.ObservedDelivery.Commit != subject {
		t.Fatalf("no-op fixture must complete clean at its dispatch subject: observation=%+v subject=%s", completed.ObservedDelivery, subject)
	}
	before := maxEventSeq(t, s, workID)
	err := acceptChangeTestAttempt(t, f, workID, attempt)
	if err == nil || !strings.Contains(err.Error(), "no changed paths") {
		t.Fatalf("no-op implementation acceptance error = %v, want the no changed paths refusal", err)
	}
	if after := maxEventSeq(t, s, workID); after != before {
		t.Fatalf("refused no-op acceptance changed the event log: before=%d after=%d", before, after)
	}
	if err := runVerdictActionAs(t, s, workID, "reject_worker_result", mustJSONValue(map[string]any{
		"attempt_id": attempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair"),
		"diagnosis": "the editing attempt returned without changing its dispatch subject", "strategy": "deliver the recorded job in a correction",
		"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:unchanged-subject"},
	}), 0, f.acceptor); err != nil {
		t.Fatalf("no-op result lost its rejection route: %v", err)
	}
}

func dispatchChangeAcceptanceAttempt(t *testing.T, f oracleRepairFixture, workID, attempt string, job WorkerJobBinding) string {
	t.Helper()
	s := f.store
	lane := reviewGateLane(t, "implementation")
	packet := joinPacketFor(t, s, workID, "repair", attempt, lane.ID, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job, lane.ID)
	view, err := readWorkContextView(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	packet["inputs"].(map[string]any)["work_context"] = view
	if pin := issue1013Pin(t, s, workID); pin.Correction != nil {
		packet["inputs"].(map[string]any)["correction"] = pin.Correction
	}
	if err := dispatchJobPacketForTest(t, s, workID, attempt, f.owner, "dispatch-"+attempt, packet); err != nil {
		t.Fatal(err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attempt, &job)
	return view.SubjectCommit
}

func completeChangeAcceptanceAttempt(t *testing.T, f oracleRepairFixture, workID, attempt, head string, builds ...func(*WorkerCompletedPayload)) WorkerCompletedPayload {
	t.Helper()
	s := f.store
	completion := oracleCompletionEvent(t, s, workID, attempt, "noop-completion", func(p *WorkerCompletedPayload) {
		for _, build := range builds {
			build(p)
		}
	})
	prepared, err := PrepareWorkerTerminalDelivery(context.Background(), s, PrepareWorkerTerminalDeliveryRequest{
		WorkID: workID, AttemptID: attempt, EventID: completion.EventID, Runner: qualifiedSubjectRunner{head: head},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerTerminalEventThroughSeamCtx(t, WithWorkerTerminalPreparation(context.Background(), prepared), s, completion)
	var raw []byte
	if err := s.db.QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerCompleted, attempt).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var completed WorkerCompletedPayload
	if err := json.Unmarshal(raw, &completed); err != nil {
		t.Fatal(err)
	}
	return completed
}

func acceptChangeTestAttempt(t *testing.T, f oracleRepairFixture, workID, attempt string) error {
	t.Helper()
	return runVerdictActionAs(t, f.store, workID, "accept_worker_result", mustJSONValue(map[string]any{
		"attempt_id": attempt, "attempt_epoch": latestStepStartEpoch(t, f.store, workID, "repair"),
	}), 0, f.acceptor)
}

func TestAcceptWorkerResultChangeObservationBoundaries(t *testing.T) {
	for _, historical := range []bool{false, true} {
		name := "changed-subject"
		if historical {
			name = "historical-unobserved"
		}
		t.Run(name, func(t *testing.T) {
			workID := "accept-change-" + name
			f := seedOracleRepairFixture(t, workID)
			defer f.store.Close()
			job := recordOracleJobRevision(t, f.store, workID, f.owner, "job:boundary", twoPathOracleFields(t, f.store, workID, nil))
			attempt := "attempt:" + workID
			dispatchChangeAcceptanceAttempt(t, f, workID, attempt, job)
			if historical {
				if err := completeJobBoundAttemptForTest(f.store, workID, attempt, reviewGateLane(t, "implementation"), &job); err != nil {
					t.Fatal(err)
				}
			} else {
				completeChangeAcceptanceAttempt(t, f, workID, attempt, strings.Repeat("2", 40))
			}
			if err := acceptChangeTestAttempt(t, f, workID, attempt); err != nil {
				t.Fatalf("accept %s: %v", name, err)
			}
		})
	}
}

func TestAcceptWorkerResultKeepsRetainedDraftCorrection(t *testing.T) {
	const workID = "accept-retained-draft"
	f := seedOracleRepairFixture(t, workID)
	defer f.store.Close()
	job := recordOracleJobRevision(t, f.store, workID, f.owner, "job:retained-draft", twoPathOracleFields(t, f.store, workID, nil))
	first := "attempt:" + workID + ":first"
	originalSubject := dispatchChangeAcceptanceAttempt(t, f, workID, first, job)
	s2WorkerFailedAttributedLive(t, f.store, workID, first, "the attempt committed its draft but failed before a judgeable report")
	advanceOracleFixtureSubject(t, f, workID, "2")
	job = recordOracleJobRevision(t, f.store, workID, f.owner, job.JobID, twoPathOracleFields(t, f.store, workID, nil))
	corrective := "attempt:" + workID + ":correction"
	subject := dispatchChangeAcceptanceAttempt(t, f, workID, corrective, job)
	if subject == originalSubject {
		t.Fatal("retained draft fixture did not advance beyond the original authoring subject")
	}
	completed := completeChangeAcceptanceAttempt(t, f, workID, corrective, subject)
	if completed.ObservedDelivery == nil || completed.ObservedDelivery.Commit != subject {
		t.Fatal("corrective completion must retain its own dispatch subject")
	}
	if err := acceptChangeTestAttempt(t, f, workID, corrective); err != nil {
		t.Fatalf("retained draft correction refused as a no-op: %v", err)
	}
}
