package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

// This diagnostic uses synthetic candidate contents and the existing trusted
// native-ledger fixtures. It exercises store Review dispatch and terminal ingest,
// not a real model, Git content read, native executable, or public adapter.
func TestDiagnosticSemanticCitationClosure(t *testing.T) {
	ctx := context.Background()
	const workID = "diagnostic-semantic-citation"
	const semanticEntry = "documentation citation accurately supports its claim"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()

	// Both versions have a consistent registry. Only the second citation
	// points to the synthetic source that actually supports the claim.
	type document struct{ id, path, claim, citation string }
	sources := map[string]string{"source:atomic": "writes are atomic", "source:format": "records are JSON"}
	beforeDoc := document{"doc:one", "docs/one.md", "writes are atomic", "source:format"}
	afterDoc := beforeDoc
	afterDoc.citation = "source:atomic"
	consistent := func(d document) bool { return d.id != "" && d.path != "" }
	accurate := func(d document) bool { return sources[d.citation] == d.claim }
	if !consistent(beforeDoc) || !consistent(afterDoc) || accurate(beforeDoc) || !accurate(afterDoc) {
		t.Fatal("fixture must isolate citation correction from registry consistency")
	}

	command := oracleGoArgv("registry-consistency")
	fields := twoPathOracleFields(t, s, workID, func(o map[string]any) {
		owner := o["owners"].([]map[string]any)[0]
		owner["obligation"] = "the document registry has an identifier and path for each record"
		control := o["controls"].([]map[string]any)[0]
		control["control_id"], control["case_ids"], control["argv"] = "control:registry", []string{"case:registry"}, command
		o["controls"] = []map[string]any{control}
		o["cases"] = []map[string]any{{"case_id": "case:registry", "owner_id": "owner:repair", "entry_point": "registry record identifier and path", "input_class": "document registry", "expected_state": "record has an identifier and path", "control_ids": []string{"control:registry"}}}
	})
	job := recordOracleJobRevision(t, s, workID, f.owner, "job:citation-review", fields)
	w := workContextFixture{store: s, workID: workID, owner: f.owner}
	j := &candidateJourney{fixture: w, s: s, workID: workID, repo: f.entry.Path}

	// The dispatch and terminal preparations mirror oCompleteOracleFixtureReview;
	// the report is authored here so both the semantic finding and closure claim
	// reach the real lane/subject/oracle validators.
	dispatchReview := func(attempt string) WorkerReviewCandidate {
		t.Helper()
		packet := joinPacketFor(t, s, workID, currentStep(t, s, workID), attempt, "review", registeredLaneIdentity(t, "review"))
		packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job, "review")
		correction, err := workflowCorrectionContextForDispatch(ctx, s.db, workID, currentStep(t, s, workID), attempt)
		if err != nil {
			t.Fatal(err)
		}
		if correction != nil {
			packet["inputs"].(map[string]any)["correction"] = correction
		}
		checkpoint, err := readLatestContextCheckpointTx(ctx, s.db, workID, "workflow_action")
		if err != nil {
			t.Fatal(err)
		}
		if checkpoint != nil {
			packet["inputs"].(map[string]any)["checkpoint"] = checkpoint
		}
		prepared, err := PrepareWorkerDispatchFreshness(ctx, s, PrepareWorkerDispatchRequest{WorkID: workID, SessionWorktree: f.entry.Path, Runner: f.git,
			DispatchPayload: mustJSONValue(map[string]any{"attempt_id": attempt, "worker_packet": packet}), reviewPlanner: oracleFixtureReviewPlanner(t, oracleFixtureRuntime)})
		if err != nil {
			t.Fatalf("prepare Review %s: %v", attempt, err)
		}
		if _, err := dispatchJoinAttempt(WithWorkerDispatchPreparation(ctx, prepared), t, s, workID, readWorkVersion(t, s, workID), f.owner, packet); err != nil {
			t.Fatalf("dispatch Review %s: %v", attempt, err)
		}
		j.laneDispatch(t, attempt, "review", &job)
		if prepared.proof.observation == nil {
			t.Fatal("Review dispatch has no host candidate observation")
		}
		o := prepared.proof.observation
		return WorkerReviewCandidate{ProjectID: "project", Commit: o.Commit, Tree: o.Tree}
	}
	completion := func(attempt string, candidate WorkerReviewCandidate, disposition string, mutate func(*WorkerCompletedPayload)) Event {
		t.Helper()
		lane := reviewGateLane(t, "review")
		event := jobBoundCompletionEvent(workID, attempt+":completed", attempt, lane, time.Now().UTC(), &job, f.entry.Path)
		var payload WorkerCompletedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		dependencies, err := workflowReviewDependencies(ctx, s.db, workID)
		if err != nil {
			t.Fatal(err)
		}
		payload.Review = &WorkerReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: dependencies,
			Assessments: []WorkerReviewAssessment{}, Findings: []WorkerReviewFinding{}, RemainingObligations: []WorkerReviewObligation{}}
		mutate(&payload)
		event.Payload = mustJSONValue(payload)
		return event
	}
	terminal := func(event Event) error {
		t.Helper()
		var payload WorkerCompletedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		prepared, err := PrepareWorkerTerminalDelivery(ctx, s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: payload.AttemptID, EventID: event.EventID, Runner: f.git})
		if err != nil {
			t.Fatalf("prepare Review terminal: %v", err)
		}
		terminalCtx := WithWorkerTerminalPreparation(ctx, prepared)
		return s.TransactDurable(terminalCtx, func(tx *Transaction) error {
			_, err := ApplyWorkerEvidenceOperationTx(terminalCtx, tx, event)
			return err
		})
	}

	first := "attempt:citation:original"
	a := dispatchReview(first)
	if err := terminal(completion(first, a, "correction_required", func(p *WorkerCompletedPayload) {
		p.Review.Findings = []WorkerReviewFinding{{FindingID: "finding:" + first + ":0", Status: "open", Severity: "P2", Confidence: "high",
			Detail: "The cited JSON-format source does not support the atomic-write claim.", EvidenceRefs: []string{}, ClosureRefs: []string{},
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationUncoveredCase, OwnerID: "owner:repair", EntryPoint: semanticEntry, FailureFamily: "citation-accuracy", EvidenceRefs: []string{"fixture:source-format-vs-atomic-claim"}}}}
	})); err != nil {
		t.Fatalf("retain original Review finding: %v", err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(ctx, s.db, workID)
	if err != nil || len(lineage.openFindingIDs()) != 1 {
		t.Fatalf("original lineage=%+v err=%v", lineage, err)
	}
	findingID := lineage.openFindingIDs()[0]
	findingBefore := string(mustJSONValue(lineage.findings[findingID]))

	advanceOracleFixtureSubject(t, f, workID, "2")
	// A fresh candidate needs its own qualified preparation. Re-record the
	// unchanged registry obligation, without adding a semantic case or control.
	job = recordOracleJobRevision(t, s, workID, f.owner, job.JobID, fields)
	second := "attempt:citation:corrected"
	b := dispatchReview(second)
	if a.Commit == b.Commit {
		t.Fatal("corrected Review must inspect a fresh candidate")
	}
	// Content belongs to these synthetic candidate identities only; the mock
	// Git runner does not read it from a repository checkout.
	documents := map[string]document{a.Commit: beforeDoc, b.Commit: afterDoc}
	if accurate(documents[a.Commit]) || !accurate(documents[b.Commit]) {
		t.Fatal("fresh fixture Review must observe the corrected citation")
	}
	// This is a mocked command with a trusted fixture-native execute ledger,
	// not a launched Go test or a citation-accuracy check.
	run := oracleVerifyControl(t, f, workID, "registry-pass", "2", command, 0)
	exit := 0
	receipt := WorkerOracleReceipt{ControlIDs: []string{"control:registry"}, CaseIDs: []string{"case:registry"}, SubjectCommit: b.Commit,
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
		Result:       OracleReceiptResultPass, ExitCode: &exit, RunRef: run.OperationRef, EvidenceRefs: []string{}}
	oracle := oracleFixtureGraphForCommand(t, s, workID, command)
	for _, c := range oracle.Cases {
		if c.OwnerID == "owner:repair" && c.EntryPoint == semanticEntry {
			t.Fatal("diagnostic accidentally inventoried the semantic finding")
		}
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		producer, err := workerOracleReceiptProducerTx(ctx, tx.tx, workID, &receipt, oracle, b.Commit)
		if err == nil && (producer == nil || producer.OperationRef != run.OperationRef) {
			t.Fatal("registry receipt did not resolve its exact qualified producer")
		}
		return err
	}); err != nil {
		t.Fatalf("unrelated registry producer must qualify: %v", err)
	}
	// A receipt-only report validates; the identical report plus a semantic
	// closure claim must fail. Qualification is not proof of citation accuracy.
	event := completion(second, b, "ready_for_final_proof", func(p *WorkerCompletedPayload) {
		p.Evidence[0].Detail = "Fixture Review compared the corrected citation with the atomic-write source; the claim is supported. Registry consistency alone does not establish this judgment."
		withReceipt(p, &receipt)
	})
	var payload WorkerCompletedPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return validateWorkerOracleCompletedReportTx(ctx, tx.tx, workID, payload, oracle, lineage)
	}); err != nil {
		t.Fatalf("receipt without closure claim must validate: %v", err)
	}
	payload.Review.ResolvedFindings = []WorkerOracleResolution{{FindingID: findingID, EvidenceRefs: []string{run.OperationRef}}}
	event.Payload = mustJSONValue(payload)
	readAttempt := func() WorkerAttempt {
		t.Helper()
		var attempt WorkerAttempt
		if err := s.ReadTransaction(ctx, func(tx *Transaction) error {
			var err error
			attempt, err = WorkerAttemptByIDTx(ctx, tx, second)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return attempt
	}
	attemptBefore, effectsBefore := readAttempt(), j.effects(t)
	var receiptCountBefore int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&receiptCountBefore); err != nil {
		t.Fatal(err)
	}
	err = terminal(event)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || failure.Op != "worker_oracle_findings" ||
		failure.Detail != "uncovered_case entry path is still absent from the controlled inventory" ||
		failure.RecoveryAction != "add the reproduced entry path and prove its controls" || failure.EffectPossible || failure.RetrySafe {
		t.Fatalf("semantic closure refusal=%v, want exact invalid_payload inventory contradiction", err)
	}
	if after := readAttempt(); !reflect.DeepEqual(attemptBefore, after) || WorkerAttemptIsTerminal(after) {
		t.Fatalf("refused report changed attempt: before=%+v after=%+v", attemptBefore, after)
	}
	if after := j.effects(t); after != effectsBefore {
		t.Fatalf("refused report changed workflow/subject state: before=%s after=%s", effectsBefore, after)
	}
	var receiptCountAfter int
	if err := s.db.QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&receiptCountAfter); err != nil || receiptCountAfter != receiptCountBefore {
		t.Fatalf("effect receipts before=%d after=%d err=%v", receiptCountBefore, receiptCountAfter, err)
	}
	if err := s.ReadTransaction(ctx, func(tx *Transaction) error {
		if _, found, err := EventByIDTx(ctx, tx, event.EventID); err != nil || found {
			t.Fatalf("refused terminal event retained: found=%v err=%v", found, err)
		}
		if _, found, err := EventByIDTx(ctx, tx, "candidate-dispatched-"+second); err != nil || !found {
			t.Fatalf("prior Review dispatch missing: found=%v err=%v", found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(ctx, s.db, workID)
	if err != nil || !reflect.DeepEqual(lineage.openFindingIDs(), []string{findingID}) || string(mustJSONValue(lineage.findings[findingID])) != findingBefore {
		t.Fatalf("refused report changed retained finding: lineage=%+v err=%v", lineage, err)
	}
	t.Logf("two real store Review dispatches; corrected candidate=%s; qualified mocked registry receipt=%s; semantic judgment true; exact refusal=%s; attempt/workflow/subject/finding/event state unchanged", b.Commit, run.OperationRef, failure.Detail)
}
