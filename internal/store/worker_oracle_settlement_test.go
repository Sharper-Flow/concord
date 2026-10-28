package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOwnerOracleFixedClosureContradictions(t *testing.T) {
	f := oracleProducerAuthorityFixture(t)
	s := f.store
	defer s.Close()
	ctx := context.Background()
	req := verifyRequest(f.git, "fixed-closure-pass", oracleGoArgv("immutability"), func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil })
	req.ProjectID = "project"
	run, err := s.VerifyWorktree(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	oracle := testOracleGraph()
	seedOracleExecuteLedgerFixture(t, s, &run, oracle)
	exit := 0
	receipt := WorkerOracleReceipt{ControlIDs: []string{"control:immutable"}, CaseIDs: []string{"case:immutable", "case:rerecord"}, SubjectCommit: strings.TrimPrefix(run.SubjectRef, "commit:"),
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: OracleReceiptResultPass, ExitCode: &exit, RunRef: run.OperationRef}
	for _, tc := range []struct {
		name, detail, remedy string
		mutate               func(*WorkerOpenOracleFinding)
	}{
		{"source-rewrite", "closure rewrites the finding's source owner, cases, or controls", "retain the source obligation or supersede the contract", func(f *WorkerOpenOracleFinding) {
			f.SourceOracle = testOracleGraph()
			f.SourceOracle.Owners[0].Obligation = "another immutable source obligation"
		}},
		{"no-obligation", "closure names no executable obligation", "prove the finding's owner controls", func(f *WorkerOpenOracleFinding) {
			f.OwnerID, f.ControlIDs = "owner:absent", nil
		}},
		{"no-retained-path", "uncovered_case has no retained reproduction path", "retain the uncovered path", func(f *WorkerOpenOracleFinding) {
			f.Classification = OracleClassificationUncoveredCase
		}},
		{"uninventoried-path", "uncovered_case entry path is still absent from the controlled inventory", "add the reproduced entry path and prove its controls", func(f *WorkerOpenOracleFinding) {
			f.Classification, f.Tie = OracleClassificationUncoveredCase, &WorkerOracleFinding{EntryPoint: "unlisted entry"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finding := &WorkerOpenOracleFinding{FindingID: "finding:fixed:0", OwnerID: "owner:entry", Classification: OracleClassificationDeliveryBlocker, ControlIDs: []string{"control:immutable"}}
			tc.mutate(finding)
			before := string(mustJSONValue(finding))
			for _, receipts := range [][]WorkerOracleReceipt{nil, {receipt}} {
				err := s.Transact(ctx, func(tx *Transaction) error {
					return validateWorkerOracleResolutionEvidenceTx(ctx, tx.tx, "work-w", finding, WorkerOracleResolution{FindingID: finding.FindingID, EvidenceRefs: []string{run.OperationRef}}, receipt.SubjectCommit, &workerOracleFindingLineage{retainedReceipts: receipts}, oracle, nil)
				})
				var failure *Failure
				if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || failure.Op != "worker_oracle_findings" || failure.Detail != tc.detail || failure.RecoveryAction != tc.remedy || failure.EffectPossible || failure.RetrySafe {
					t.Errorf("fixed contradiction with %d receipts: %+v", len(receipts), err)
				}
				if string(mustJSONValue(finding)) != before {
					t.Fatal("refusal changed the retained finding")
				}
			}
		})
	}
	t.Run("dynamic-evidence-precedes-fixed-contradiction", func(t *testing.T) {
		finding := &WorkerOpenOracleFinding{FindingID: "finding:fixed:0", OwnerID: "owner:entry", Classification: OracleClassificationUncoveredCase, Tie: &WorkerOracleFinding{EntryPoint: "unlisted entry"}}
		lineage := &workerOracleFindingLineage{findings: map[string]*WorkerOpenOracleFinding{finding.FindingID: finding}}
		for _, tc := range []struct {
			name   string
			mutate func(*WorkerOracleReceipt)
		}{
			{"old-subject", func(r *WorkerOracleReceipt) { r.SubjectCommit = strings.Repeat("b", 40) }},
			{"missing-producer", func(r *WorkerOracleReceipt) { r.RunRef = "worktree_verify:absent" }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := receipt
				tc.mutate(&r)
				payload := WorkerCompletedPayload{Evidence: []WorkerReportEvidence{{OracleReceipt: &r}}, Review: &WorkerReviewBlock{ResolvedFindings: []WorkerOracleResolution{{FindingID: finding.FindingID, EvidenceRefs: []string{run.OperationRef}}}}}
				validate := func() error {
					return s.Transact(ctx, func(tx *Transaction) error {
						return validateWorkerOracleCompletedReportTx(ctx, tx.tx, "work-w", payload, oracle, lineage)
					})
				}
				if err := validate(); !hasFailureKind(err, KindMissingEvidence) {
					t.Fatalf("dynamic evidence failure must remain recoverable: %v", err)
				}
				r = receipt
				if err := validate(); !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "uncovered_case entry path is still absent from the controlled inventory") {
					t.Fatalf("qualified evidence must still refuse the fixed contradiction: %v", err)
				}
			})
		}
	})
}

func TestOwnerOracleRefusedFoldRetainsDispatchAndFinding(t *testing.T) {
	ctx := context.Background()
	const workID = "oracle-refused-fold"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:refused-fold"}
	first := "attempt:refused:1"
	oracleDispatchAttempt(t, f, workID, first, twoPathOracleFields(t, s, workID, nil), job)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, first, "open-finding", func(p *WorkerCompletedPayload) {
		p.Review = &WorkerReviewBlock{Disposition: "correction_required", Findings: []WorkerReviewFinding{{Severity: "P2", Confidence: "high", Detail: "uncontrolled entry",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationUncoveredCase, OwnerID: "owner:repair", EntryPoint: "unlisted entry", FailureFamily: "entry", EvidenceRefs: []string{"evidence:reproduction"}}}}}
	})}}); err != nil {
		t.Fatal(err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(ctx, s.db, workID)
	if err != nil || len(lineage.openFindingIDs()) != 1 {
		t.Fatalf("initial lineage=%+v err=%v", lineage, err)
	}
	findingID := lineage.openFindingIDs()[0]
	findingBefore := string(mustJSONValue(lineage.findings[findingID]))
	second := "attempt:refused:2"
	oracleDispatchAttempt(t, f, workID, second, nil, job)
	readAttempt := func() WorkerAttempt {
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
	before := readAttempt()
	var dispatchID string
	if err := s.db.QueryRowContext(ctx, "SELECT dispatch_event_id FROM worker_attempts WHERE attempt_id=?", second).Scan(&dispatchID); err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	var receiptsBefore int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM effect_receipts").Scan(&receiptsBefore); err != nil {
		t.Fatal(err)
	}
	event := oracleCompletionEvent(t, s, workID, second, "refused-closure", func(p *WorkerCompletedPayload) {
		p.Review.ResolvedFindings = []WorkerOracleResolution{{FindingID: findingID, EvidenceRefs: []string{"evidence:reproduction"}}}
	})
	preparation, err := PrepareWorkerTerminalDelivery(ctx, s, PrepareWorkerTerminalDeliveryRequest{WorkID: workID, AttemptID: second, EventID: event.EventID, Runner: f.git})
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithWorkerTerminalPreparation(ctx, preparation)
	err = s.TransactDurable(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkerEvidenceOperationTx(ctx, tx, event)
		return err
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindInvalidPayload || failure.Detail != "uncovered_case entry path is still absent from the controlled inventory" || failure.EffectPossible {
		t.Errorf("fixed inventory contradiction: %v", err)
	}
	if after := readAttempt(); !reflect.DeepEqual(before, after) || WorkerAttemptIsTerminal(after) {
		t.Fatalf("refused fold changed attempt: before=%+v after=%+v", before, after)
	}
	if after := readWorkVersion(t, s, workID); after != version {
		t.Fatalf("version changed from %d to %d", version, after)
	}
	var receiptsAfter int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM effect_receipts").Scan(&receiptsAfter); err != nil || receiptsAfter != receiptsBefore {
		t.Fatalf("receipt count before=%d after=%d err=%v", receiptsBefore, receiptsAfter, err)
	}
	if err := s.ReadTransaction(ctx, func(tx *Transaction) error {
		if _, found, err := EventByIDTx(ctx, tx, event.EventID); err != nil || found {
			t.Fatalf("refused terminal retained: found=%v err=%v", found, err)
		}
		if _, found, err := EventByIDTx(ctx, tx, dispatchID); err != nil || !found {
			t.Fatalf("earlier dispatch disappeared: found=%v err=%v", found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(ctx, s.db, workID)
	if err != nil || !reflect.DeepEqual(lineage.openFindingIDs(), []string{findingID}) || string(mustJSONValue(lineage.findings[findingID])) != findingBefore {
		t.Fatalf("refusal changed finding: lineage=%+v err=%v", lineage, err)
	}
}
