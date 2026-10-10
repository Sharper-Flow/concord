package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOwnerOracleProducerAuthority(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)
	ctx := context.Background()
	command := oracleGoArgv("immutability")
	result, err := s.VerifyWorktree(ctx, verifyRequest(git, "oracle-authority", command, func(context.Context, string, []string, int) (int, []byte, bool, error) {
		return 0, []byte("bounded output"), true, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	oracle := testOracleGraph()
	oracle.Controls[0].RecipeSource.ProjectID = "project-w"
	seedOracleExecuteLedgerFixture(t, s, &result, oracle)
	exit := 0
	receipt := WorkerOracleReceipt{ControlIDs: []string{"control:immutable"}, CaseIDs: []string{"case:immutable"}, SubjectCommit: strings.TrimPrefix(result.SubjectRef, "commit:"),
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project-w", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
		Result:       OracleReceiptResultPass, ExitCode: &exit, RunRef: result.OperationRef}
	validate := func(r WorkerOracleReceipt) error {
		return s.Transact(ctx, func(tx *Transaction) error {
			return validateWorkerOracleCompletedReportTx(ctx, tx.tx, "work-w", WorkerCompletedPayload{Evidence: []WorkerReportEvidence{{OracleReceipt: &r}}}, oracle, &workerOracleFindingLineage{})
		})
	}
	if err := validate(receipt); err != nil {
		t.Errorf("actual producer receipt refused: %v", err)
	}
	wrong := receipt
	wrong.SubjectCommit = strings.Repeat("b", 40)
	if err := validate(wrong); err == nil {
		t.Error("model-selected candidate qualified")
	}
	oracle.Controls[0].Argv = []string{"true"}
	if err := validate(receipt); err == nil {
		t.Error("unrelated executed command qualified the control")
	}
	oracle.Controls[0].Argv = command
	seedNativeRunForTest(t, s, "work-w", "run:started-only")
	wrong = receipt
	wrong.RunRef = "run:started-only"
	if err := validate(wrong); err == nil {
		t.Error("unverified started row proved pass")
	}
}

func TestOwnerOracleResolutionAuthority(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)
	ctx := context.Background()
	result, err := s.VerifyWorktree(ctx, verifyRequest(git, "oracle-closure", oracleGoArgv("immutability"), func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil }))
	if err != nil {
		t.Fatal(err)
	}
	oracle := testOracleGraph()
	oracle.Controls[0].RecipeSource.ProjectID = "project-w"
	seedOracleExecuteLedgerFixture(t, s, &result, oracle)
	exit := 0
	receipt := WorkerOracleReceipt{ControlIDs: []string{"control:immutable"}, CaseIDs: []string{"case:immutable", "case:rerecord"}, SubjectCommit: strings.TrimPrefix(result.SubjectRef, "commit:"),
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project-w", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: OracleReceiptResultPass, ExitCode: &exit, RunRef: result.OperationRef}
	finding := &WorkerOpenOracleFinding{FindingID: "finding:1:0", OwnerID: "owner:entry", Classification: OracleClassificationDeliveryBlocker, ControlIDs: []string{"control:immutable"}}
	validate := func(refs []string, receipts []WorkerOracleReceipt) error {
		return s.Transact(ctx, func(tx *Transaction) error {
			return validateWorkerOracleResolutionEvidenceTx(ctx, tx.tx, "work-w", finding, WorkerOracleResolution{FindingID: finding.FindingID, EvidenceRefs: refs}, strings.TrimPrefix(result.SubjectRef, "commit:"), &workerOracleFindingLineage{retainedReceipts: receipts}, oracle, nil)
		})
	}
	if err := validate([]string{result.OperationRef}, []WorkerOracleReceipt{receipt}); err != nil {
		t.Errorf("matched current control closure refused: %v", err)
	}
	failed, err := s.VerifyWorktree(ctx, verifyRequest(git, "oracle-later-fail", result.Command, func(context.Context, string, []string, int) (int, []byte, bool, error) { return 1, nil, false, nil }))
	if err != nil {
		t.Fatal(err)
	}
	newerFailure := receipt
	seedOracleExecuteLedgerFixture(t, s, &failed, oracle)
	failureExit := 1
	newerFailure.Result, newerFailure.RunRef, newerFailure.ExitCode = OracleReceiptResultFail, failed.OperationRef, &failureExit
	if err := validate([]string{result.OperationRef}, []WorkerOracleReceipt{receipt, newerFailure}); err == nil {
		t.Error("an earlier pass hid the current subject's later failure")
	}
	if err := validate([]string{result.OperationRef}, []WorkerOracleReceipt{receipt, newerFailure, receipt}); err == nil {
		t.Error("re-reporting an old pass overrode the newer native failure")
	}
	old := receipt
	old.SubjectCommit = strings.Repeat("b", 40)
	if err := validate([]string{result.OperationRef}, []WorkerOracleReceipt{old}); err == nil {
		t.Error("old subject closed current finding")
	}
	// A retained evidence binding alone carries no control execution.
	if err := s.Transact(ctx, func(tx *Transaction) error {
		_, err := tx.tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('unrelated-bound',?,'work_item','work-w','actor:other','2026-10-09T00:00:00Z',1,?)`, WorkflowEvidenceBound, `{"immutable_subject_ref":"evidence:unrelated"}`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := validate([]string{"evidence:unrelated"}, nil); err == nil {
		t.Error("unrelated bound evidence closed finding")
	}
	finding.ControlIDs = []string{"control:immutable", "control:bind"}
	if err := validate([]string{result.OperationRef}, []WorkerOracleReceipt{receipt}); err == nil {
		t.Error("one intersecting control closed a two-control finding")
	}
	finding.ControlIDs = nil
	finding.Classification = OracleClassificationUncoveredCase
	finding.Tie = &WorkerOracleFinding{EntryPoint: "unlisted path"}
	if err := validate([]string{"evidence:unrelated"}, nil); err == nil {
		t.Error("uncovered entry path closed without inventory or reproduction")
	}
}

func TestOwnerOracleOwnerCaseRetention(t *testing.T) {
	for _, mutate := range []func(*AcceptanceOracle){
		func(o *AcceptanceOracle) { o.Owners[0].Obligation = "a different obligation" },
		func(o *AcceptanceOracle) { o.Owners[0].Mechanism.EntryPoint = "another owner" },
		func(o *AcceptanceOracle) { o.Owners[0].DomainID = "another-domain" },
		func(o *AcceptanceOracle) { o.Owners[0].PredicateIDs = []string{"predicate:two"} },
		func(o *AcceptanceOracle) { o.Cases[0].ExpectedState = "different outcome" },
		func(o *AcceptanceOracle) { o.Cases = o.Cases[1:] },
	} {
		latest := testOracleGraph()
		mutate(latest)
		if oracleControlsRetained(testOracleGraph(), latest) {
			t.Error("changed owner/case retained the earlier obligation")
		}
	}
}

func TestOwnerOracleContractDebtIdentity(t *testing.T) {
	h := workflowCorrectionHistory{obligations: map[string]workflowJobObligationState{}, unresolved: map[string]bool{"job:contract|1": true}}
	for _, version := range []int64{1, 2} {
		payload := WorkerJobRecordedPayload{JobID: "job:contract", Revision: version, ContractVersion: version, Objective: "same objective", AcceptanceOracle: testOracleGraph()}
		if err := h.observe(workflowCorrectionHistoryRow{kind: WorkerJobRecorded, payload: string(mustJSONValue(payload))}, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if h.discharge("job:contract", 2) {
		t.Fatal("identical oracle under another contract discharged earlier debt")
	}
}

func TestOwnerOracleRealPacketAcceptance(t *testing.T) {
	const workID = "owner-oracle-real-packet"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := recordOracleJobRevision(t, s, workID, f.owner, "job:real-packet", twoPathOracleFields(t, s, workID, nil))
	lane := reviewGateLane(t, "implementation")
	attempt := "attempt:" + workID
	packet := joinPacketFor(t, s, workID, "repair", attempt, lane.ID, lane.Version, lane.Digest)
	packet["inputs"].(map[string]any)["worker_job"] = recordedPacketJobForTest(t, s, workID, job)
	contextView, err := readWorkContextView(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	packet["inputs"].(map[string]any)["work_context"] = contextView
	if err := dispatchJobPacketForTest(t, s, workID, attempt, f.owner, "real-packet-dispatch", packet); err != nil {
		t.Fatal(err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attempt, &job)
	run := oracleVerifyControl(t, f, workID, "real-packet-control", "1", oracleGoArgv("path-a"), 0)
	exit := 0
	event := oracleCompletionEvent(t, s, workID, attempt, "real-packet-completion", func(p *WorkerCompletedPayload) {
		withReceipt(p, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, SubjectCommit: strings.TrimPrefix(run.SubjectRef, "commit:"),
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: OracleReceiptResultPass, ExitCode: &exit, RunRef: run.OperationRef})
	})
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}}); err != nil {
		t.Fatal(err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attempt, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, f.acceptor); err != nil {
		t.Fatal(err)
	}
	view, err := readWorkContextView(context.Background(), s.db, workID)
	if err != nil || view == nil || view.SubjectCommit != strings.TrimPrefix(run.SubjectRef, "commit:") || len(view.OracleReceipts) != 1 {
		t.Fatalf("accepted context=%+v err=%v", view, err)
	}
}

func TestOwnerOracleMatchedNativeReceipt(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)
	ctx := context.Background()
	command := oracleGoArgv("immutability")
	result, err := s.VerifyWorktree(ctx, verifyRequest(git, "oracle-native-command", command, func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil }))
	if err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: ActorAgent}
	version := verdictItemVersion(t, s, "work-w")
	event, err := buildNativeRunEvent("oracle-native-health", "work-w", actor, time.Unix(40, 0).UTC(), version, "health", "run:matched", result.SubjectRef, "healthy", result.OperationRef, "sha256:"+strings.Repeat("b", 64), time.Unix(40, 0).UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}}); err != nil {
		t.Fatal(err)
	}
	oracle := testOracleGraph()
	oracle.Controls[0].RecipeSource.ProjectID = "project-w"
	seedOracleExecuteLedgerFixture(t, s, &result, oracle)
	exit := 0
	receipt := WorkerOracleReceipt{ControlIDs: []string{"control:immutable"}, CaseIDs: []string{"case:immutable", "case:rerecord"}, SubjectCommit: strings.TrimPrefix(result.SubjectRef, "commit:"),
		RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project-w", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: OracleReceiptResultPass, ExitCode: &exit, RunRef: "run:matched"}
	validate := func() error {
		return s.Transact(ctx, func(tx *Transaction) error {
			_, err := workerOracleReceiptProducerTx(ctx, tx.tx, "work-w", &receipt, oracle, strings.TrimPrefix(result.SubjectRef, "commit:"))
			return err
		})
	}
	if err := validate(); err == nil {
		t.Fatal("unverified native health report proved pass")
	}
	var observation string
	if err := s.db.QueryRow(`SELECT observation_id FROM workflow_native_runs WHERE work_id='work-w' AND run_id='run:matched'`).Scan(&observation); err != nil {
		t.Fatal(err)
	}
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return AppendExternalObservationVerificationTx(ctx, tx, "work-w", "principal-1", time.Unix(41, 0).UTC(), ExternalObservationVerification{ObservationID: observation, VerificationMethod: VerifyTrustedClientReport, VerifiedAt: time.Unix(41, 0).UTC().Format(time.RFC3339Nano), VerifyingAuthorityRef: "client:verifier", Result: VerificationMatched})
	}); err != nil {
		t.Fatal(err)
	}
	if err := validate(); err != nil {
		t.Fatalf("matched native producer refused: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE durable_operations SET result_payload='{}' WHERE op_id=?`, result.OperationRef); err != nil {
		t.Fatal(err)
	}
	if err := validate(); err == nil {
		t.Fatal("native binding lost its command producer but still proved pass")
	}
}

func TestOwnerOracleIndependentClosure(t *testing.T) {
	const workID = "owner-oracle-independent"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:independent"}
	first := "attempt:" + workID + ":1"
	oracleDispatchAttempt(t, f, workID, first, twoPathOracleFields(t, s, workID, nil), job)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, first, "independent-finding", func(p *WorkerCompletedPayload) {
		p.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{oracleBlockerFinding("independent control failed", "path-b", "control:check-b")}}
	})}}); err != nil {
		t.Fatal(err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	id := lineage.openFindingIDs()[0]
	second := "attempt:" + workID + ":2"
	oracleDispatchAttempt(t, f, workID, second, nil, job)
	command := oracleGoArgv("path-b")
	// First execute as the dispatched lane itself and bind the real producer.
	var laneActor string
	if err := s.db.QueryRow(`SELECT json_extract(payload,'$.lane_actor_ref') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.attempt_id')=?`, workID, WorkerDispatched, second).Scan(&laneActor); err != nil {
		t.Fatal(err)
	}
	var worker WorkflowActor
	if err := s.db.QueryRow(`SELECT principal_ref,client_ref,agent_ref,session_ref FROM workflow_actors WHERE actor_ref=?`, laneActor).Scan(&worker.PrincipalRef, &worker.ClientRef, &worker.AgentRef, &worker.SessionRef); err != nil {
		t.Fatal(err)
	}
	req := verifyRequest(f.git, "self-verify-"+workID, command, func(context.Context, string, []string, int) (int, []byte, bool, error) { return 0, nil, false, nil })
	req.WorkID, req.ProjectID, req.PrincipalRef = workID, "project", worker.PrincipalRef
	req.Owner = SessionWorktreeOwner{ClientRef: worker.ClientRef, AgentRef: worker.AgentRef, SessionRef: worker.SessionRef}
	self, err := s.VerifyWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	seedOracleExecuteLedgerFixture(t, s, &self, oracleFixtureGraphForCommand(t, s, workID, command))
	bind := func(result WorktreeVerifyResult, principal, watermark string) {
		t.Helper()
		event := workflowTypedEvent("bind-"+result.LeaseID, WorkflowEvidenceBound, workID, "actor:test", time.Unix(60, 0).UTC(), readWorkVersion(t, s, workID), map[string]any{
			"evidence_kind": "verification", "immutable_subject_ref": result.OperationRef, "producer_id": principal,
			"producer_run_ref": result.OperationRef, "producer_watermark": watermark, "observed_at": time.Unix(60, 0).UTC().Format(time.RFC3339Nano),
		})
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{event}}); err != nil {
			t.Fatal(err)
		}
	}
	bind(self, req.PrincipalRef, req.RequestID)
	completion := func(result WorktreeVerifyResult, key string) error {
		return ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, second, key, func(p *WorkerCompletedPayload) {
			exit := 0
			withReceipt(p, &WorkerOracleReceipt{ControlIDs: []string{"control:check-b"}, CaseIDs: []string{"case:path-b"}, SubjectCommit: strings.TrimPrefix(result.SubjectRef, "commit:"),
				RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: OracleReceiptResultPass, ExitCode: &exit, RunRef: result.OperationRef})
			p.Review.ResolvedFindings = []WorkerOracleResolution{{FindingID: id, EvidenceRefs: []string{result.OperationRef}}}
		})}})
	}
	if err := completion(self, "self-closure"); err == nil {
		t.Fatal("the worker's bound producer supplied independent execution")
	}
	independent := oracleVerifyControl(t, f, workID, "independent-pass", "1", command, 0)
	if err := completion(independent, "unbound-closure"); err == nil {
		t.Fatal("independent role closed without the producer binding")
	}
	bind(independent, "principal-1", "req-v-"+independent.LeaseID)
	if err := completion(independent, "qualified-closure"); err != nil {
		t.Fatalf("independent bound command producer refused: %v", err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(context.Background(), s.db, workID)
	if err != nil || len(lineage.openFindingIDs()) != 0 {
		t.Fatalf("independent closure lineage=%+v err=%v", lineage, err)
	}
}

func TestOwnerOracleUncoveredClosure(t *testing.T) {
	const workID = "owner-oracle-uncovered-closure"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:uncovered"}
	first := "attempt:" + workID + ":1"
	oracleDispatchAttempt(t, f, workID, first, twoPathOracleFields(t, s, workID, nil), job)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, first, "uncovered-finding", func(p *WorkerCompletedPayload) {
		p.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{Severity: "P1", Confidence: "high", Detail: "path c fails",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationUncoveredCase, OwnerID: "owner:repair", EntryPoint: "repair path c", FailureFamily: "path-c", EvidenceRefs: []string{"evidence:reproduce-c"}}}}}
	})}}); err != nil {
		t.Fatal(err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(context.Background(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	id := lineage.openFindingIDs()[0]
	// The reproduced case authors and executes on a fresh current subject:
	// its preparation and job revision bind that candidate.
	advanceOracleFixtureSubject(t, f, workID, "2")
	fields := twoPathOracleFields(t, s, workID, func(o map[string]any) {
		cases := o["cases"].([]map[string]any)
		controls := o["controls"].([]map[string]any)
		o["cases"] = append(cases, map[string]any{"case_id": "case:path-c", "owner_id": "owner:repair", "entry_point": "repair path c", "input_class": "reproduced entry", "expected_state": "path c passes", "control_ids": []string{"control:check-c"}})
		control := map[string]any{}
		for key, value := range controls[0] {
			control[key] = value
		}
		control["control_id"], control["case_ids"], control["argv"] = "control:check-c", []string{"case:path-c"}, oracleGoArgv("path-c")
		o["controls"] = append(controls, control)
	})
	newJob := recordOracleJobRevision(t, s, workID, f.owner, job.JobID, fields)
	second := "attempt:" + workID + ":2"
	oracleDispatchAttempt(t, f, workID, second, nil, &newJob)
	run := oracleVerifyControl(t, f, workID, "uncovered-pass", "2", oracleGoArgv("path-c"), 0)
	exit := 0
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, second, "uncovered-resolution", func(p *WorkerCompletedPayload) {
		withReceipt(p, &WorkerOracleReceipt{ControlIDs: []string{"control:check-c"}, CaseIDs: []string{"case:path-c"}, SubjectCommit: strings.TrimPrefix(run.SubjectRef, "commit:"),
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}, Result: OracleReceiptResultPass, ExitCode: &exit, RunRef: run.OperationRef})
		p.Review.ResolvedFindings = []WorkerOracleResolution{{FindingID: id, EvidenceRefs: []string{run.OperationRef}}}
	})}}); err != nil {
		t.Fatalf("controlled reproduction closure refused: %v", err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(context.Background(), s.db, workID)
	if err != nil || len(lineage.openFindingIDs()) != 0 {
		t.Fatalf("uncovered closure lineage=%+v err=%v", lineage, err)
	}
}

func TestOwnerOracleFollowUpSourceIdentity(t *testing.T) {
	finding := WorkerReviewFinding{Severity: "P2", Confidence: "high", Detail: "outside the declared owners", Oracle: &WorkerOracleFinding{Classification: OracleClassificationFollowUp, OwnerID: "owner:outside"}}
	view := rankedFindingView(finding, "source:event", 12, 3, &workerOracleFindingLineage{findings: map[string]*WorkerOpenOracleFinding{}})
	if view.DomainID != "unresolved" || view.Status != WorkContextFindingStatusReported || view.SourceKind != WorkContextSourceKindReviewFinding || view.SourceEventID != "source:event" || view.SourceEventSeq != 12 || view.Ordinal != 3 {
		t.Fatalf("unowned follow-up lost source identity or claimed a Domain: %+v", view)
	}
}

func TestOwnerOracleFindingLawBindingIdentity(t *testing.T) {
	oracle := testOracleGraph()
	owner := oracle.Owners[0]
	owner.LawBindings = []OracleLawBinding{{Source: WorkContextReadingSource{Kind: WorkContextSourceKnowledge, SourceID: "source:law", LawID: "law:one", ContentHash: "sha256:" + strings.Repeat("a", 64)}, Clause: "D1"}}
	oracle.Owners[0] = owner
	for _, mutate := range []func(*WorkerOracleLawBinding){
		func(b *WorkerOracleLawBinding) { b.Source.SourceID = "source:other" },
		func(b *WorkerOracleLawBinding) { b.Clause = "D2" },
	} {
		binding := WorkerOracleLawBinding{Source: WorkerOracleLawSource{Kind: WorkContextSourceKnowledge, SourceID: "source:law", LawID: "law:one", ContentHash: owner.LawBindings[0].Source.ContentHash}, Clause: "D1"}
		mutate(&binding)
		finding := &WorkerOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: owner.OwnerID, LawBindings: []WorkerOracleLawBinding{binding}, EvidenceRefs: []string{"evidence:repro"}}
		if err := validateWorkerOracleFindingJoins(finding, oracle, map[string]OracleOwner{owner.OwnerID: owner}); err == nil {
			t.Error("changed law source or clause joined the owner's pinned obligation")
		}
	}
}

// CON-890 slice A findings tests: typed report semantics, ranked identity
// lineage, receipt joins, the two-entry repair chain, the derived-set
// equality and convergence wall, oracle-bound job debt, replay boundaries,
// and the over-bound recovery refusal. Every fixture runs on the newest
// break-fix pin, which is oracle-capable, so the helpers exercise the real
// authoring, dispatch, and terminal boundaries.

// oracleRepairFixture is one break-fix work item at the repair step with
// the worker and acceptor actors recorded, ready for an oracle-bearing job.
type oracleRepairFixture struct {
	store    *Store
	owner    WorkflowActor
	worker   WorkflowActor
	acceptor WorkflowActor
	git      *fakeWorktreeGit
	entry    WorktreeEntry
}

func seedOracleRepairFixture(t *testing.T, workID string) oracleRepairFixture {
	t.Helper()
	fixture := seedWorkflowReturnRouteFixture(t, workID, "workflow.break_fix", "repair")
	s := fixture.store
	if !oracleCapablePinForWork(t, s, workID) {
		t.Fatal("the newest break-fix pin is not oracle-capable")
	}
	worker := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/worker", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	acceptor := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/oracle-acceptor", SessionRef: "session/" + workID + "-acceptor", ActorClass: ActorAgent}
	workerRef, err := WorkflowActorRef(worker)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("oracle-worker-"+workID, WorkflowActorRecorded, workID, workerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"actor_ref": workerRef, "principal_ref": worker.PrincipalRef, "client_ref": worker.ClientRef,
		"agent_ref": worker.AgentRef, "session_ref": worker.SessionRef, "actor_class": "agent",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	git, entry := oracleFixtureClaimedWorktree(t, s, workID)
	result := oracleRepairFixture{store: s, owner: fixture.owner, worker: worker, acceptor: acceptor, git: git, entry: entry}
	oracleVerifyControl(t, result, workID, "candidate", "1", []string{"true"}, 0)
	return result
}

// oracleFixtureClaimedWorktree returns fixture git for the active claim, or
// registers a Project locator and claims a fixture worktree when none exists.
func oracleFixtureClaimedWorktree(t *testing.T, s *Store, workID string) (*fakeWorktreeGit, WorktreeEntry) {
	t.Helper()
	if entry, err := activeWorktreeEntryForProject(context.Background(), s.db, "oracle_fixture", workID, "project"); err == nil {
		git := newFakeWorktreeGit(entry.Path)
		git.worktrees[entry.Path] = entry.Branch
		git.worktreeRepos[entry.Path] = entry.Path
		return git, entry
	} else if !hasFailureKind(err, KindProjectionNotFound) {
		t.Fatal(err)
	}
	root := t.TempDir()
	var projectVersion int64
	if err := s.db.QueryRow(`SELECT version FROM projects WHERE id='project'`).Scan(&projectVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProjectLocator(context.Background(), "project", ProjectLocator{ID: "oracle-repo", Kind: LocatorCanonicalPath, Value: root}, projectVersion); err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(root)
	claim := baseClaim(git)
	claim.WorkID, claim.ProjectID, claim.ExpectedVersion = workID, "project", verdictItemVersion(t, s, workID)
	claim.OpID, claim.RequestID = "oracle-claim-"+workID, "oracle-claim-"+workID
	claimed, err := s.ClaimWorktree(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(claimed.Entry.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	return git, claimed.Entry
}

// bootstrapOracleFixtureSubject claims a fixture worktree for the work and
// qualifies its candidate subject with a plain verify, the same route the
// repair family uses, so authoring and preparation fixtures bind a real
// subject on an active claim. It is idempotent: a work that already holds a
// qualified subject on an active claim keeps that subject and its evidence,
// and no second claim or verify is recorded.
func bootstrapOracleFixtureSubject(t *testing.T, s *Store, workID string) {
	t.Helper()
	if subject, err := readCurrentOracleSubject(context.Background(), s.db, workID); err != nil {
		t.Fatal(err)
	} else if subject != "" {
		return
	}
	git, entry := oracleFixtureClaimedWorktree(t, s, workID)
	oracleVerifyControl(t, oracleRepairFixture{store: s, git: git, entry: entry}, workID, "candidate", "1", []string{"true"}, 0)
}

func oracleTestSubject(round string) string { return "commit:" + strings.Repeat(round, 40) }

// advanceOracleFixtureSubject moves the candidate to the round's subject and
// qualifies it with a plain verify: each later round authors, prepares, and
// executes against a fresh current candidate, never a retired subject.
func advanceOracleFixtureSubject(t *testing.T, f oracleRepairFixture, workID, round string) {
	t.Helper()
	oracleVerifyControl(t, f, workID, "candidate-"+round, round, []string{"true"}, 0)
}

func oracleVerifyControl(t *testing.T, f oracleRepairFixture, workID, name, round string, command []string, exit int) WorktreeVerifyResult {
	t.Helper()
	f.git.branches[f.entry.Branch] = strings.TrimPrefix(oracleTestSubject(round), "commit:")
	req := verifyRequest(f.git, "verify-"+workID+"-"+name, command, func(context.Context, string, []string, int) (int, []byte, bool, error) { return exit, nil, false, nil })
	req.WorkID, req.ProjectID = workID, "project"
	result, err := f.store.VerifyWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(command) > 1 {
		seedOracleExecuteLedgerFixture(t, f.store, &result, oracleFixtureGraphForCommand(t, f.store, workID, command))
	}
	return result
}

// twoPathOracleFields authors the repair-family oracle: one owner over two
// entry paths, path-a exercised by a reported-role control and path-b by an
// independently-executed control. Authority joins derive from the same live
// fixture state acceptanceOracleFieldsForTest reads.
func twoPathOracleFields(t *testing.T, s *Store, workID string, mutation func(oracle map[string]any)) map[string]any {
	t.Helper()
	oracle := acceptanceOracleFieldsForTest(t, s, workID)
	owners := oracle["owners"].([]map[string]any)
	owners[0]["owner_id"] = "owner:repair"
	owners[0]["obligation"] = "the owner keeps both entry paths passing"
	control := oracle["controls"].([]map[string]any)[0]
	recipe := control["recipe_source"].(map[string]any)
	readiness := control["readiness_evidence_refs"].([]string)
	predicate := owners[0]["predicate_ids"].([]string)[0]
	oracle["cases"] = []map[string]any{
		{"case_id": "case:path-a", "owner_id": "owner:repair", "entry_point": "repair path a", "input_class": "normal entry", "expected_state": "path a passes", "control_ids": []string{"control:check-a"}},
		{"case_id": "case:path-b", "owner_id": "owner:repair", "entry_point": "repair path b", "input_class": "alternate entry", "expected_state": "path b passes", "control_ids": []string{"control:check-b"}},
	}
	oracle["controls"] = []map[string]any{
		{"control_id": "control:check-a", "owner_id": "owner:repair", "predicate_ids": []string{predicate}, "case_ids": []string{"case:path-a"},
			"recipe_source": recipe, "argv": oracleGoArgv("path-a"), "cwd": "internal/store",
			"expected_result": "pass", "required_evidence_role": "reported", "readiness_evidence_refs": readiness},
		{"control_id": "control:check-b", "owner_id": "owner:repair", "predicate_ids": []string{predicate}, "case_ids": []string{"case:path-b"},
			"recipe_source": recipe, "argv": oracleGoArgv("path-b"), "cwd": "internal/store",
			"expected_result": "pass", "required_evidence_role": "independently_executed", "readiness_evidence_refs": readiness},
	}
	if mutation != nil {
		mutation(oracle)
	}
	return oracle
}

// recordOracleJobRevision records one ready revision of the named job under
// the given oracle, with the fixed base fields every revision of the job
// shares, so obligation-key comparisons in the debt tests isolate the oracle.
func recordOracleJobRevision(t *testing.T, s *Store, workID string, actor WorkflowActor, jobID string, oracle map[string]any) WorkerJobBinding {
	t.Helper()
	raw, err := json.Marshal(oracle)
	if err != nil {
		t.Fatal(err)
	}
	var graph AcceptanceOracle
	if err := json.Unmarshal(raw, &graph); err != nil {
		t.Fatal(err)
	}
	seedOraclePreparationLedgerFixture(t, s, workID, &graph)
	fields := map[string]any{
		"job_id": jobID, "objective": "bounded job objective for " + jobID,
		"stopping_condition":   "the recorded checks pass with no unresolved reference",
		"path_scope":           []string{"internal/store"},
		"checks":               []string{"go test ./internal/store/"},
		"reserved_integration": "integration evidence binds at the parent effect step",
		"ready":                true, "readiness_evidence": []string{"evidence:coordinator-ready"},
		"acceptance_oracle": &graph,
	}
	if err := recordWorkerJobActionForTest(t, s, workID, actor, fields); err != nil {
		t.Fatalf("record oracle job %s: %v", jobID, err)
	}
	views, err := s.WorkerJobRevisions(context.Background(), workID)
	if err != nil {
		t.Fatal(err)
	}
	var binding WorkerJobBinding
	for _, view := range views {
		if view.Binding.JobID == jobID && view.AcceptanceOracle != nil {
			binding = view.Binding
		}
	}
	if binding.JobID == "" {
		t.Fatalf("oracle job %s recorded no oracle-bearing revision", jobID)
	}
	return binding
}

// oracleDispatchAttempt isolates the fold joins through a recorded dispatch
// authorization and worker evidence. TestOwnerOracleRealPacketAcceptance owns
// the acceptance proof through the actual generated-packet preflight route.
func oracleDispatchAttempt(t *testing.T, f oracleRepairFixture, workID, attemptID string, oracle map[string]any, job *WorkerJobBinding) {
	t.Helper()
	s := f.store
	if job.Digest == "" {
		binding := recordOracleJobRevision(t, s, workID, f.owner, job.JobID, oracle)
		*job = binding
	}
	lane := reviewGateLane(t, "implementation")
	actorRef, err := WorkflowActorRef(f.worker)
	if err != nil {
		t.Fatal(err)
	}
	version := verdictItemVersion(t, s, workID)
	epoch := latestStepStartEpoch(t, s, workID, "repair") + 1
	authorization := workflowEventWithActor("oracle-authorize-"+attemptID, WorkflowActionCompleted, workID, actorRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"step_id": "repair", "action_id": "dispatch_worker", "attempt_epoch": epoch, "worker_attempt_id": attemptID,
		"actor_ref":                   actorRef,
		"worker_job":                  map[string]any{"job_id": job.JobID, "revision": job.Revision, "digest": job.Digest},
		"worker_packet_digest":        "sha256:" + strings.Repeat("d", 64),
		"worker_packet_predicate_ids": []string{},
		"worker_lane_id":              lane.ID, "worker_lane_version": lane.Version, "worker_lane_digest": lane.Digest, "worker_capability_class": lane.CapabilityClass,
	})
	authorization.PayloadVersion = 4
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{authorization}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatalf("authorize oracle attempt %s: %v", attemptID, err)
	}
	recordJobBoundWorkerDispatch(t, s, workID, attemptID, job)
}

// seedNativeRunForTest retains an adversarial started, unverified row. It
// cannot prove an executed control; valid fixtures use VerifyWorktree.
func seedNativeRunForTest(t *testing.T, s *Store, workID, runID string) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO workflow_native_runs(work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref)
		VALUES(?,?,'start','started',?,'authority/oracle-host','actor/oracle-host','native://run',?,'evidence://run',?,'2026-10-09T00:00:00Z','2026-10-09T00:00:00Z','trusted_client_report','{}','policy/oracle','policy/oracle');
		DELETE FROM fold_guard`, workID, runID, "native-run-"+runID, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatalf("seed native run %s: %v", runID, err)
	}
}

// oracleCompletionEvent builds one job-bound worker.completed report whose
// review and receipts the caller authors. The attempt's recorded lane
// identity and dispatched job binding drive the payload, so the fold's
// end-to-end checks pass on the real boundaries.
func oracleCompletionEvent(t *testing.T, s *Store, workID, attemptID, eventKey string, build func(payload *WorkerCompletedPayload)) Event {
	t.Helper()
	var laneID, laneDigest string
	var laneVersion int64
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT lane_id,lane_version,lane_digest FROM worker_attempts WHERE work_id=? AND attempt_id=?`, workID, attemptID).Scan(&laneID, &laneVersion, &laneDigest); err != nil {
		t.Fatalf("read attempt lane for %s: %v", attemptID, err)
	}
	lane, err := LookupLane(laneID, laneVersion, laneDigest)
	if err != nil {
		t.Fatal(err)
	}
	dispatched, err := workflowDispatchedJobForAttempt(context.Background(), s.DatabaseForTesting(), workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	payload := WorkerCompletedPayload{
		AttemptID: attemptID, ReadbackModel: preferredModelForLane(lane),
		ReportSchemaVersion: WorkerReportSchemaVersion, EvidenceOrigin: WorkerEvidenceReported, WorkerJob: dispatched,
	}
	if len(lane.RequiredReportBlocks) > 0 {
		payload.Review = &WorkerReviewBlock{Verdict: "ship", Findings: []WorkerReviewFinding{}}
	}
	if payload.Review == nil {
		payload.Review = &WorkerReviewBlock{Verdict: "ship", Findings: []WorkerReviewFinding{}}
	}
	payload.Evidence = reportedLaneEvidenceForTest(lane, payload.Review)
	if build != nil {
		build(&payload)
	}
	for _, entry := range payload.Evidence {
		if entry.OracleReceipt != nil && entry.OracleReceipt.SubjectCommit == "" && entry.OracleReceipt.RunRef != "" {
			entry.OracleReceipt.SubjectCommit, err = readCurrentOracleSubject(context.Background(), s.db, workID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	return Event{EventID: eventKey + "-" + attemptID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(30, 0).UTC(), PayloadVersion: WorkerEvidenceEventPayloadVersion(WorkerCompleted), Payload: mustJSONValue(payload)}
}

// withReceipt attaches one typed receipt to the report's first evidence
// entry, the only structural requirement the schema places on it.
func withReceipt(payload *WorkerCompletedPayload, receipt *WorkerOracleReceipt) {
	payload.Evidence[0].OracleReceipt = receipt
}

// oracleBlockerFinding is one classified delivery blocker bound to the
// fixture owner by predicate.
func oracleBlockerFinding(detail, family string, controls ...string) WorkerReviewFinding {
	return WorkerReviewFinding{
		Severity: "P1", Confidence: "high", Detail: detail,
		Oracle: &WorkerOracleFinding{
			Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:repair",
			FailureFamily: family, ControlIDs: controls, EvidenceRefs: []string{"evidence:repro"},
		},
	}
}

// fixturePredicate reads the oracle fixture's covered predicate id.
func fixturePredicate(t *testing.T, s *Store, workID string) string {
	t.Helper()
	views, err := s.WorkerJobRevisions(context.Background(), workID)
	if err != nil || len(views) == 0 {
		t.Fatalf("read recorded oracle jobs for %s: %v", workID, err)
	}
	return views[len(views)-1].AcceptanceOracle.Owners[0].PredicateIDs[0]
}

// Typed findings bind real owners; an uncovered case carries its explicit
// entry path instead of a control; follow-ups never become delivery
// criteria; the verdict couplings hold beside the preserved P0 rule; an
// out-of-scope P0 follow-up stays a valid retained no_ship.
func TestOwnerOracleFinding(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-finding"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:finding"}
	attempt := "attempt:" + workID
	oracleDispatchAttempt(t, f, workID, attempt, twoPathOracleFields(t, s, workID, nil), job)
	predicate := fixturePredicate(t, s, workID)

	attempts := 0
	// Each report lands on a fresh attempt: a completed attempt is
	// terminal, and a refused report leaves its attempt dispatched for
	// the corrected retry the fold would see.
	report := func(build func(payload *WorkerCompletedPayload)) error {
		attempts++
		attemptID := attempt + ":" + itoa(attempts)
		oracleDispatchAttempt(t, f, workID, attemptID, nil, job)
		return ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attemptID, "oracle-finding-"+itoa(attempts), build)}})
	}
	// A sound no_ship blocker folds: owner bound, predicate tie,
	// reproducible evidence, and an unavailable receipt with the empty
	// run_ref and no exit code.
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
			Severity: "P1", Confidence: "high", Detail: "path a fails the obligation",
			Oracle: &WorkerOracleFinding{
				Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:repair",
				FailureFamily: "path-a", PredicateIDs: []string{predicate},
				ControlIDs: []string{"control:check-a"}, EvidenceRefs: []string{"run:path-a-fail"},
			},
		}}}
		withReceipt(payload, &WorkerOracleReceipt{
			ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"},
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
			Result:       OracleReceiptResultUnavailable, RunRef: "", EvidenceRefs: []string{"harness could not resolve"},
		})
	}); err != nil {
		t.Fatalf("sound classified blocker refused: %v", err)
	}

	// Every finding of an oracle-bound dispatch carries a classification.
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "ship", Findings: []WorkerReviewFinding{{Severity: "P2", Confidence: "low", Detail: "unclassified"}}}
	}); err == nil || !strings.Contains(err.Error(), "carries no oracle classification") {
		t.Fatalf("unclassified finding error = %v", err)
	}
	// A delivery blocker without an owner tie refuses.
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
			Severity: "P1", Confidence: "high", Detail: "no owner",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationDeliveryBlocker, FailureFamily: "path-a", EvidenceRefs: []string{"run:x"}},
		}}}
	}); err == nil || !strings.Contains(err.Error(), "must bind a declared owner") {
		t.Fatalf("ownerless blocker error = %v", err)
	}
	// A predicate the owner does not cover refuses.
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
			Severity: "P1", Confidence: "high", Detail: "foreign predicate",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:repair", FailureFamily: "path-a", PredicateIDs: []string{"predicate:never-approved"}, EvidenceRefs: []string{"run:x"}},
		}}}
	}); err == nil || !strings.Contains(err.Error(), "owner does not cover") {
		t.Fatalf("foreign predicate error = %v", err)
	}
	// Reproducible evidence is part of the blocker tie.
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
			Severity: "P1", Confidence: "high", Detail: "no evidence",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:repair", FailureFamily: "path-a", PredicateIDs: []string{predicate}},
		}}}
	}); err == nil || !strings.Contains(err.Error(), "reproducible evidence") {
		t.Fatalf("evidenceless blocker error = %v", err)
	}
	// An uncovered case without its explicit entry path refuses…
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
			Severity: "P1", Confidence: "high", Detail: "uncovered without a path",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationUncoveredCase, OwnerID: "owner:repair", FailureFamily: "path-c", EvidenceRefs: []string{"run:x"}},
		}}}
	}); err == nil || !strings.Contains(err.Error(), "explicit new entry path") {
		t.Fatalf("pathless uncovered case error = %v", err)
	}
	// …and one with the entry path and reproduction folds while naming no
	// control: a legitimate new blocker, not a rejected missing control.
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
			Severity: "P1", Confidence: "high", Detail: "uncovered path c reproduces",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationUncoveredCase, OwnerID: "owner:repair", FailureFamily: "path-c", EntryPoint: "repair path c", EvidenceRefs: []string{"run:path-c"}},
		}}}
	}); err != nil {
		t.Fatalf("uncovered case with its entry path refused: %v", err)
	}

	// An out-of-scope P0 follow-up records a valid retained no_ship and
	// holds review debt as non-progress without forcing an invalid report.
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
			Severity: "P0", Confidence: "high", Detail: "consequential outside-scope concern",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationFollowUp},
		}}}
	}); err != nil {
		t.Fatalf("out-of-scope P0 follow-up no_ship refused: %v", err)
	}
	nonProgress, err := workflowNonProgressAttemptCount(context.Background(), s.DatabaseForTesting(), workID, "worker_oracle_findings_test")
	if err != nil || nonProgress < 2 {
		t.Fatalf("non-progress after no_ship reports = (%d, %v), want the two no_ship attempts counted", nonProgress, err)
	}
	// A no_ship justified only by sub-P0 follow-ups is inconsistent.
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
			Severity: "P2", Confidence: "low", Detail: "polish outside the contract",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationFollowUp},
		}}}
	}); err == nil || !strings.Contains(err.Error(), "only by follow-ups below P0") {
		t.Fatalf("follow-up-only no_ship error = %v", err)
	}
	// A ship never carries a classified blocker.
	if err := report(func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "ship", Findings: []WorkerReviewFinding{oracleBlockerFinding("blocker under a ship verdict", "path-a", "control:check-a")}}
	}); err == nil || !strings.Contains(err.Error(), "cannot carry a classified delivery blocker") {
		t.Fatalf("ship-with-blocker error = %v", err)
	}
}

// Ranked identity is deterministic: the context findings of one terminal
// event keep their ordinals, the ranked review findings follow at the
// offset, a continuation reuses the canonical open identity, a variant or
// an uncertain join mints a new one, and a wrong continuation binding
// refuses.
func TestOwnerOracleLineage(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-lineage"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:lineage"}
	attempt := "attempt:" + workID
	oracleDispatchAttempt(t, f, workID, attempt, twoPathOracleFields(t, s, workID, nil), job)

	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt, "oracle-lineage", func(payload *WorkerCompletedPayload) {
		payload.ContextFindings = []WorkerContextFinding{
			{Kind: "observation", Statement: "first context claim", SubjectRef: "internal/store/a.go", EvidenceRefs: []string{}, DomainID: "root", ProductWideRationale: "the claim spans the fixture root Domain"},
			{Kind: "observation", Statement: "second context claim", SubjectRef: "internal/store/b.go", EvidenceRefs: []string{}, DomainID: "root", ProductWideRationale: "the claim spans the fixture root Domain"},
		}
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{
			oracleBlockerFinding("the ranked blocker", "path-a", "control:check-a"),
			{Severity: "P3", Confidence: "low", Detail: "a follow-up outside the contract", Oracle: &WorkerOracleFinding{Classification: OracleClassificationFollowUp}},
		}}
	})}}); err != nil {
		t.Fatalf("context-and-ranked report refused: %v", err)
	}
	firstSeq := maxEventSeq(t, s, workID)
	blockerID := workerRankedFindingID(firstSeq, 2, 0)
	lineage, err := readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lineage.openFindingIDs(); len(got) != 1 || got[0] != blockerID {
		t.Fatalf("open ids = %v, want the single ranked blocker %s (context ids 0 and 1 never collide with ranked ids 2 and 3)", got, blockerID)
	}
	if entry := lineage.findings[blockerID]; entry == nil || entry.SourceOrdinal != 2 {
		t.Fatalf("blocker entry = %#v, want ordinal 2 behind two context findings", entry)
	}
	// A follow_up is minted for identity but never blocks.
	if _, minted := lineage.findings[workerRankedFindingID(firstSeq, 2, 1)]; !minted {
		t.Fatal("the follow-up was not minted into the identity space")
	}

	// A continuation of the open blocker keeps the canonical identity.
	attempt2 := "attempt:" + workID + ":2"
	oracleDispatchAttempt(t, f, workID, attempt2, nil, job)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt2, "oracle-lineage-2", func(payload *WorkerCompletedPayload) {
		continueFinding := oracleBlockerFinding("the same failure persists", "path-a", "control:check-a")
		continueFinding.Oracle.ContinuesFindingID = blockerID
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{continueFinding}}
	})}}); err != nil {
		t.Fatalf("continuation refused: %v", err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lineage.openFindingIDs(); len(got) != 1 || got[0] != blockerID {
		t.Fatalf("open ids after continuation = %v, want the unchanged canonical id %s", got, blockerID)
	}

	// A continuation that drops the owner or family binding refuses.
	attempt3 := "attempt:" + workID + ":3"
	oracleDispatchAttempt(t, f, workID, attempt3, nil, job)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt3, "oracle-lineage-3", func(payload *WorkerCompletedPayload) {
		foreign := oracleBlockerFinding("a different failure family", "path-b", "control:check-b")
		foreign.Oracle.ContinuesFindingID = blockerID
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{foreign}}
	})}}); err == nil || !strings.Contains(err.Error(), "does not retain the earlier finding's owner") {
		t.Fatalf("foreign continuation error = %v", err)
	}

	// A variant of the owner family mints a new open identity, and an
	// uncertain join (no continues_finding_id) does too — neither earns
	// shrink credit.
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt3, "oracle-lineage-3b", func(payload *WorkerCompletedPayload) {
		variant := oracleBlockerFinding("an alternate entry path of the same owner", "path-a2", "control:check-a")
		variant.Oracle.VariantOf = blockerID
		uncertain := oracleBlockerFinding("same family, uncertain join", "path-a", "control:check-a")
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{variant, uncertain}}
	})}}); err != nil {
		t.Fatalf("variant and uncertain join refused: %v", err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lineage.openFindingIDs(); len(got) != 3 {
		t.Fatalf("open ids after variant and uncertain join = %v, want three distinct identities", got)
	}
	if err := validateCorrectionOpenFindingsTx(context.Background(), s.db, workID, lineage.openFindingIDs(), true); err != nil {
		t.Fatalf("exact derived set refused: %v", err)
	}
	if err := validateCorrectionOpenFindingsTx(context.Background(), s.db, workID, []string{blockerID, blockerID, blockerID}, true); err == nil {
		t.Fatal("duplicate finding ids hid two members of the derived open set")
	}

	// The context reader projects the open ranked finding with its source
	// kind and tie, never as a generic claim, and the resolver accepts the
	// ranked ordinal space.
	view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
	if viewErr != nil || view == nil {
		t.Fatalf("context view = (%#v, %v)", view, viewErr)
	}
	ranked := 0
	for _, finding := range view.Findings {
		if finding.SourceKind == WorkContextSourceKindReviewFinding {
			ranked++
			if finding.Oracle == nil || finding.Status != WorkContextFindingStatusOpen {
				t.Fatalf("ranked view entry = %#v, want the tie and the open status", finding)
			}
		}
	}
	if ranked != 3 {
		t.Fatalf("ranked view findings = %d, want the three open blockers", ranked)
	}
	var genericID string
	for _, finding := range view.Findings {
		if finding.SourceKind == "" {
			genericID = finding.FindingID
		}
	}
	if genericID != workerRankedFindingID(firstSeq, 0, 0) && genericID != workerRankedFindingID(firstSeq, 0, 1) {
		t.Fatalf("generic context finding id = %s, want one of the event's context ordinals", genericID)
	}
}

// Receipts join the dispatched oracle exactly: an executed result needs a
// retained native run — the concrete native-producer limitation refuses an
// invented locator — an unavailable result carries the empty run_ref and no
// exit code, and a wrong recipe or an unknown control refuses.
func TestOwnerOracleReceipt(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-receipt"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:receipt"}
	attempt := "attempt:" + workID
	oracleDispatchAttempt(t, f, workID, attempt, twoPathOracleFields(t, s, workID, nil), job)
	recipe := WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}
	attempts := 0
	report := func(build func(payload *WorkerCompletedPayload)) error {
		attempts++
		attemptID := attempt + ":" + itoa(attempts)
		oracleDispatchAttempt(t, f, workID, attemptID, nil, job)
		return ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attemptID, "oracle-receipt-"+itoa(attempts), build)}})
	}

	// A pass without a run_ref never reaches the fold: the shape refuses.
	exit := 0
	if err := report(func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, RecipeSource: recipe, Result: OracleReceiptResultPass, ExitCode: &exit, RunRef: ""})
	}); err == nil || !strings.Contains(err.Error(), "requires a nonempty run_ref") {
		t.Fatalf("pass without run_ref error = %v", err)
	}
	// A pass naming no retained native run refuses with the concrete
	// producer limitation — the claim never becomes proof.
	if err := report(func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, RecipeSource: recipe, Result: OracleReceiptResultPass, ExitCode: &exit, RunRef: "run:never-produced"})
	}); err == nil || !strings.Contains(err.Error(), "names no verified command producer") {
		t.Fatalf("unproduced run error = %v", err)
	}
	// An unavailable result with a locator refuses.
	if err := report(func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, RecipeSource: recipe, Result: OracleReceiptResultUnavailable, RunRef: "run:anything"})
	}); err == nil || !strings.Contains(err.Error(), "must carry the empty run_ref") {
		t.Fatalf("unavailable with locator error = %v", err)
	}
	// An unavailable result may not carry an exit code.
	unavailableExit := 1
	if err := report(func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, RecipeSource: recipe, Result: OracleReceiptResultUnavailable, ExitCode: &unavailableExit, RunRef: ""})
	}); err == nil || !strings.Contains(err.Error(), "cannot carry an exit code") {
		t.Fatalf("unavailable with exit code error = %v", err)
	}
	// An unknown control refuses.
	seedNativeRunForTest(t, s, workID, "run:ghost-control")
	if err := report(func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:ghost"}, CaseIDs: []string{"case:path-a"}, RecipeSource: recipe, Result: OracleReceiptResultFail, ExitCode: &unavailableExit, RunRef: "run:ghost-control"})
	}); err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("unknown control error = %v", err)
	}
	// A receipt from the candidate's modified copy of the harness refuses.
	wrongRecipe := recipe
	wrongRecipe.CommitOID = strings.Repeat("2d0a", 10)
	seedNativeRunForTest(t, s, workID, "run:wrong-recipe")
	if err := report(func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, RecipeSource: wrongRecipe, Result: OracleReceiptResultFail, ExitCode: &unavailableExit, RunRef: "run:wrong-recipe"})
	}); err == nil || !strings.Contains(err.Error(), "does not equal the pinned recipe") {
		t.Fatalf("wrong recipe error = %v", err)
	}
	// A case no named control exercises refuses.
	seedNativeRunForTest(t, s, workID, "run:unrelated-case")
	if err := report(func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-b"}, RecipeSource: recipe, Result: OracleReceiptResultFail, ExitCode: &unavailableExit, RunRef: "run:unrelated-case"})
	}); err == nil || !strings.Contains(err.Error(), "no named control exercises") {
		t.Fatalf("unrelated case error = %v", err)
	}
	// A fail receipt over a retained native run folds.
	failed := oracleVerifyControl(t, f, workID, "path-a-fail", "1", oracleGoArgv("path-a"), 1)
	if err := report(func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, RecipeSource: recipe, Result: OracleReceiptResultFail, ExitCode: &unavailableExit, RunRef: failed.OperationRef})
	}); err != nil {
		t.Fatalf("executed fail receipt refused: %v", err)
	}
	// The retained receipt projects onto the context view as prior
	// evidence with its exact identity.
	view, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID)
	if viewErr != nil || view == nil || len(view.OracleReceipts) != 1 || view.OracleReceipts[0].RunRef != failed.OperationRef {
		t.Fatalf("prior receipts = (%#v, %v), want the retained fail receipt", view.OracleReceipts, viewErr)
	}
}

// The two-entry repair chain: the first path fixes while the second fails,
// the next packet preserves the first receipt and names both controls, a
// later regression of the fixed path reopens the owner, and a prior-subject
// pass stays a baseline that closes nothing.
func TestOwnerOracleRepairFamily(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-repair-family"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:repair-family"}
	recipe := WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}
	pass := 0
	fail := 1

	// Round one: path a fails, path b passes at the old subject — the
	// baseline a later round must not treat as current proof.
	attempt1 := "attempt:" + workID + ":1"
	oracleDispatchAttempt(t, f, workID, attempt1, twoPathOracleFields(t, s, workID, nil), job)
	aFail1 := oracleVerifyControl(t, f, workID, "a-fail-s1", "1", oracleGoArgv("path-a"), fail)
	bPass1 := oracleVerifyControl(t, f, workID, "b-pass-s1", "1", oracleGoArgv("path-b"), pass)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt1, "oracle-family", func(payload *WorkerCompletedPayload) {
		payload.Evidence[0].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, SubjectCommit: strings.TrimPrefix(aFail1.SubjectRef, "commit:"), RecipeSource: recipe,
			Result: OracleReceiptResultFail, ExitCode: &fail, RunRef: aFail1.OperationRef, EvidenceRefs: []string{"harness output a"},
		}
		payload.Evidence[1].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:check-b"}, CaseIDs: []string{"case:path-b"}, SubjectCommit: strings.TrimPrefix(bPass1.SubjectRef, "commit:"), RecipeSource: recipe,
			Result: OracleReceiptResultPass, ExitCode: &pass, RunRef: bPass1.OperationRef, EvidenceRefs: []string{"harness output b"},
		}
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{oracleBlockerFinding("path a fails", "path-a", "control:check-a")}}
	})}}); err != nil {
		t.Fatalf("round one refused: %v", err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	firstFinding := lineage.openFindingIDs()[0]

	// Round two: path a passes at the current subject and its finding
	// closes on that receipt; path b now fails and opens a second finding.
	// The new subject needs a fresh preparation and a new job revision
	// bound to it: an old preparation never qualifies a new candidate.
	attempt2 := "attempt:" + workID + ":2"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	advanceOracleFixtureSubject(t, f, workID, "2")
	oracleDispatchAttempt(t, f, workID, attempt2, twoPathOracleFields(t, s, workID, nil), &WorkerJobBinding{JobID: job.JobID})
	aPass2 := oracleVerifyControl(t, f, workID, "a-pass-s2", "2", oracleGoArgv("path-a"), pass)
	bFail2 := oracleVerifyControl(t, f, workID, "b-fail-s2", "2", oracleGoArgv("path-b"), fail)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt2, "oracle-family-2", func(payload *WorkerCompletedPayload) {
		payload.Evidence[0].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, SubjectCommit: strings.TrimPrefix(aPass2.SubjectRef, "commit:"), RecipeSource: recipe,
			Result: OracleReceiptResultPass, ExitCode: &pass, RunRef: aPass2.OperationRef, EvidenceRefs: []string{"harness output a"},
		}
		payload.Evidence[1].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:check-b"}, CaseIDs: []string{"case:path-b"}, SubjectCommit: strings.TrimPrefix(bFail2.SubjectRef, "commit:"), RecipeSource: recipe,
			Result: OracleReceiptResultFail, ExitCode: &fail, RunRef: bFail2.OperationRef, EvidenceRefs: []string{"harness output b"},
		}
		payload.Review = &WorkerReviewBlock{
			Verdict: "no_ship",
			Findings: []WorkerReviewFinding{func() WorkerReviewFinding {
				finding := oracleBlockerFinding("path b fails after the first repair", "path-b", "control:check-b")
				return finding
			}()},
			ResolvedFindings: []WorkerOracleResolution{{FindingID: firstFinding, EvidenceRefs: []string{aPass2.OperationRef}}},
		}
	})}}); err != nil {
		t.Fatalf("round two refused: %v", err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	open := lineage.openFindingIDs()
	if len(open) != 1 {
		t.Fatalf("open ids after the first repair = %v, want only the path-b finding", open)
	}
	closed := lineage.findings[firstFinding]
	if closed == nil || !closed.Closed || closed.ClosureKind != oracleClosureResolution {
		t.Fatalf("first-path finding after closure = %#v, want a proven resolution", closed)
	}
	// The old pass on path b stays retained and visible, not current proof.
	if len(lineage.retainedReceipts) != 4 {
		t.Fatalf("retained receipts = %d, want both rounds' four receipts preserved", len(lineage.retainedReceipts))
	}

	// A closure of the path-b finding citing the prior-subject baseline
	// pass refuses: baseline never closes current obligations. Round three
	// runs on its own fresh subject, preparation, and job revision.
	attempt3 := "attempt:" + workID + ":3"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	advanceOracleFixtureSubject(t, f, workID, "3")
	oracleDispatchAttempt(t, f, workID, attempt3, twoPathOracleFields(t, s, workID, nil), &WorkerJobBinding{JobID: job.JobID})
	bPass3 := oracleVerifyControl(t, f, workID, "b-pass-s3", "3", oracleGoArgv("path-b"), pass)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt3, "oracle-family-3", func(payload *WorkerCompletedPayload) {
		payload.Evidence[0].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:check-b"}, CaseIDs: []string{"case:path-b"}, SubjectCommit: strings.TrimPrefix(bPass3.SubjectRef, "commit:"), RecipeSource: recipe,
			Result: OracleReceiptResultPass, ExitCode: &pass, RunRef: bPass3.OperationRef, EvidenceRefs: []string{"harness output b"},
		}
		payload.Review = &WorkerReviewBlock{
			Verdict:          "ship",
			Findings:         []WorkerReviewFinding{},
			ResolvedFindings: []WorkerOracleResolution{{FindingID: open[0], EvidenceRefs: []string{bPass1.OperationRef}}},
		}
	})}}); err == nil || !strings.Contains(err.Error(), "neither durably bound evidence nor a current-subject pass receipt") {
		t.Fatalf("prior-subject closure error = %v", err)
	}
	// The path-b control requires the independently-executed role, so even
	// the current-subject receipt route refuses: only durably bound
	// evidence closes it.
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt3, "oracle-family-3b", func(payload *WorkerCompletedPayload) {
		payload.Evidence[0].OracleReceipt = &WorkerOracleReceipt{
			ControlIDs: []string{"control:check-b"}, CaseIDs: []string{"case:path-b"}, SubjectCommit: strings.TrimPrefix(bPass3.SubjectRef, "commit:"), RecipeSource: recipe,
			Result: OracleReceiptResultPass, ExitCode: &pass, RunRef: bPass3.OperationRef, EvidenceRefs: []string{"harness output b"},
		}
		payload.Review = &WorkerReviewBlock{
			Verdict:          "ship",
			Findings:         []WorkerReviewFinding{},
			ResolvedFindings: []WorkerOracleResolution{{FindingID: open[0], EvidenceRefs: []string{bPass3.OperationRef}}},
		}
	})}}); err == nil || !strings.Contains(err.Error(), "neither durably bound evidence nor a current-subject pass receipt") {
		t.Fatalf("independent-role receipt closure error = %v", err)
	}
	// A later regression of the fixed first path reopens the owner with a
	// new identity: replacing one finding with another is not shrinkage.
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt3, "oracle-family-3c", func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{oracleBlockerFinding("path a regressed", "path-a", "control:check-a")}}
	})}}); err != nil {
		t.Fatalf("regression report refused: %v", err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if got := lineage.openFindingIDs(); len(got) != 2 {
		t.Fatalf("open ids after the regression = %v, want the path-b finding and the new path-a identity", got)
	}
}

// The derived-set equality and the strengthened convergence wall: a
// rejection must carry exactly the derived set, a proven strict subset buys
// exactly one findings_shrinking basis, an oracle-defect closure, a
// replacement identity, and a control-rewritten oracle never buy one, and
// an accepted no_ship stays non-progress.
func TestOwnerOracleConvergence(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-convergence"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:convergence"}
	recipe := WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}
	pass := 0
	reportedBoth := func(oracle map[string]any) {
		oracle["controls"].([]map[string]any)[1]["required_evidence_role"] = "reported"
	}

	reject := func(attemptID string, openIDs []string) error {
		payload := map[string]any{
			"attempt_id": attemptID, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair"),
			"diagnosis": "the review found open oracle blockers", "strategy": "repair the failing entry paths",
			"predicate_ids": []string{"predicate:return-route"}, "evidence_refs": []string{"evidence:return-route-verification"},
		}
		if openIDs != nil {
			payload["open_finding_ids"] = openIDs
		}
		return runVerdictActionAs(t, s, workID, "reject_worker_result", mustJSONValue(payload), 0, f.owner)
	}

	// Round one mints two blockers on reported-role controls.
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	attempt1 := "attempt:" + workID + ":1"
	oracleDispatchAttempt(t, f, workID, attempt1, twoPathOracleFields(t, s, workID, reportedBoth), job)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt1, "oracle-convergence", func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{
			oracleBlockerFinding("first blocker", "family-one", "control:check-a"),
			oracleBlockerFinding("second blocker", "family-two", "control:check-a"),
		}}
	})}}); err != nil {
		t.Fatalf("round one refused: %v", err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	open := lineage.openFindingIDs()
	if len(open) != 2 {
		t.Fatalf("open ids = %v, want two blockers", open)
	}
	// Omission refuses: a shorter list is not resolution.
	if err := reject(attempt1, []string{open[0]}); err == nil || !strings.Contains(err.Error(), "omission and addition are both refusals") {
		t.Fatalf("omitted blocker rejection error = %v", err)
	}
	// An added unknown id refuses too.
	if err := reject(attempt1, []string{open[0], open[1], "finding:9:9"}); err == nil || !strings.Contains(err.Error(), "omission and addition are both refusals") {
		t.Fatalf("fabricated id rejection error = %v", err)
	}
	// Absence refuses on an oracle-capable history.
	if err := reject(attempt1, nil); err == nil || !strings.Contains(err.Error(), "requires open_finding_ids") {
		t.Fatalf("absent set rejection error = %v", err)
	}
	if err := reject(attempt1, open); err != nil {
		t.Fatalf("exact-set rejection refused: %v", err)
	}

	// Round two closes one blocker on a current-subject pass and reports
	// nothing new; the rejection carries the strict subset. The new subject
	// gets a fresh preparation and job revision bound to it.
	attempt2 := "attempt:" + workID + ":2"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	advanceOracleFixtureSubject(t, f, workID, "2")
	oracleDispatchAttempt(t, f, workID, attempt2, twoPathOracleFields(t, s, workID, reportedBoth), &WorkerJobBinding{JobID: job.JobID})
	closeRun := oracleVerifyControl(t, f, workID, "convergence-close", "2", oracleGoArgv("path-a"), pass)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt2, "oracle-convergence-2", func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, SubjectCommit: strings.TrimPrefix(closeRun.SubjectRef, "commit:"), RecipeSource: recipe, Result: OracleReceiptResultPass, ExitCode: &pass, RunRef: closeRun.OperationRef})
		payload.Review = &WorkerReviewBlock{
			Verdict:          "ship",
			Findings:         []WorkerReviewFinding{},
			ResolvedFindings: []WorkerOracleResolution{{FindingID: open[1], EvidenceRefs: []string{closeRun.OperationRef}}},
		}
	})}}); err != nil {
		t.Fatalf("round two refused: %v", err)
	}
	if err := reject(attempt2, []string{open[0]}); err != nil {
		t.Fatalf("strict-subset rejection refused: %v", err)
	}
	convergence, err := workflowRetryConvergence(context.Background(), s.DatabaseForTesting(), workID, "repair")
	if err != nil {
		t.Fatal(err)
	}
	if convergence.Basis != "findings_shrinking" || convergence.LatestRecordSeq <= convergence.PreviousRecordSeq {
		t.Fatalf("convergence after a proven strict subset = %#v, want exactly one findings_shrinking basis", convergence)
	}

	// A replacement identity earns no shrink credit: closing the remaining
	// blocker while minting an uncertain same-family sibling leaves an
	// incomparable pair.
	attempt3 := "attempt:" + workID + ":3"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	advanceOracleFixtureSubject(t, f, workID, "3")
	oracleDispatchAttempt(t, f, workID, attempt3, twoPathOracleFields(t, s, workID, reportedBoth), &WorkerJobBinding{JobID: job.JobID})
	replaceRun := oracleVerifyControl(t, f, workID, "convergence-replace", "3", oracleGoArgv("path-a"), pass)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt3, "oracle-convergence-3", func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, SubjectCommit: strings.TrimPrefix(replaceRun.SubjectRef, "commit:"), RecipeSource: recipe, Result: OracleReceiptResultPass, ExitCode: &pass, RunRef: replaceRun.OperationRef})
		payload.Review = &WorkerReviewBlock{
			Verdict:          "no_ship",
			Findings:         []WorkerReviewFinding{oracleBlockerFinding("a fresh sibling of the closed family", "family-one", "control:check-a")},
			ResolvedFindings: []WorkerOracleResolution{{FindingID: open[0], EvidenceRefs: []string{replaceRun.OperationRef}}},
		}
	})}}); err != nil {
		t.Fatalf("replacement round refused: %v", err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	sibling := lineage.openFindingIDs()
	if len(sibling) != 1 {
		t.Fatalf("open ids after replacement = %v, want the fresh sibling identity", sibling)
	}
	if err := reject(attempt3, sibling); err != nil {
		t.Fatalf("replacement-set rejection refused: %v", err)
	}
	if convergence, err = workflowRetryConvergence(context.Background(), s.DatabaseForTesting(), workID, "repair"); err != nil || convergence.Basis != "" {
		t.Fatalf("convergence after replacement = (%#v, %v), want no basis", convergence, err)
	}

	// An oracle-defect closure never contributes to shrinkage: closing the
	// sibling through an oracle-defect correction leaves a subset shape
	// that the wall refuses.
	attempt4 := "attempt:" + workID + ":4"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	oracleDispatchAttempt(t, f, workID, attempt4, nil, job)
	// The sibling must be re-minted as open first: report a blocker plus
	// an oracle defect, then close both, the defect through its evidence.
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt4, "oracle-convergence-4", func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{
			oracleBlockerFinding("a fresh blocker to shrink against", "family-three", "control:check-a"),
			{Severity: "P1", Confidence: "high", Detail: "the harness fixture is unsound",
				Oracle: &WorkerOracleFinding{Classification: OracleClassificationOracleDefect, OwnerID: "owner:repair", FailureFamily: "harness", ControlIDs: []string{"control:check-b"}, EvidenceRefs: []string{"run:harness-self-test"}}},
		}}
	})}}); err != nil {
		t.Fatalf("defect round refused: %v", err)
	}
	lineage, err = readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	openNow := lineage.openFindingIDs()
	if len(openNow) != 3 {
		t.Fatalf("open ids before the defect closure = %v, want the sibling, the blocker, and the oracle defect", openNow)
	}
	siblingID := openNow[0]
	blockerID, defectID := openNow[1], openNow[2]
	if err := reject(attempt4, openNow); err != nil {
		t.Fatalf("two-id rejection refused: %v", err)
	}
	attempt5 := "attempt:" + workID + ":5"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	advanceOracleFixtureSubject(t, f, workID, "5")
	oracleDispatchAttempt(t, f, workID, attempt5, twoPathOracleFields(t, s, workID, reportedBoth), &WorkerJobBinding{JobID: job.JobID})
	defectRunA := oracleVerifyControl(t, f, workID, "defect-a", "5", oracleGoArgv("path-a"), pass)
	defectRunB := oracleVerifyControl(t, f, workID, "defect-b", "5", oracleGoArgv("path-b"), pass)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt5, "oracle-convergence-5", func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, SubjectCommit: strings.TrimPrefix(defectRunA.SubjectRef, "commit:"), RecipeSource: recipe, Result: OracleReceiptResultPass, ExitCode: &pass, RunRef: defectRunA.OperationRef})
		payload.Evidence[1].OracleReceipt = &WorkerOracleReceipt{ControlIDs: []string{"control:check-b"}, CaseIDs: []string{"case:path-b"}, SubjectCommit: strings.TrimPrefix(defectRunB.SubjectRef, "commit:"), RecipeSource: recipe, Result: OracleReceiptResultPass, ExitCode: &pass, RunRef: defectRunB.OperationRef}
		payload.Review = &WorkerReviewBlock{
			Verdict:  "ship",
			Findings: []WorkerReviewFinding{},
			ResolvedFindings: []WorkerOracleResolution{
				{FindingID: blockerID, EvidenceRefs: []string{defectRunA.OperationRef}},
				{FindingID: defectID, EvidenceRefs: []string{defectRunB.OperationRef}},
			},
		}
	})}}); err != nil {
		t.Fatalf("defect-closure round refused: %v", err)
	}
	if err := reject(attempt5, []string{siblingID}); err != nil {
		t.Fatalf("subset rejection after defect closure refused: %v", err)
	}
	if convergence, err = workflowRetryConvergence(context.Background(), s.DatabaseForTesting(), workID, "repair"); err != nil || convergence.Basis != "" {
		t.Fatalf("convergence after an oracle-defect shrink shape = (%#v, %v), want no basis: the defect closure never buys shrinkage", convergence, err)
	}

	// An accepted no_ship stays non-progress and mints no basis family:
	// the fresh attempt's retained out-of-scope P0 follow-up report is
	// accepted, and the wall neither resets nor widens behind it.
	attempt6 := "attempt:" + workID + ":6"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	oracleDispatchAttempt(t, f, workID, attempt6, nil, job)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt6, "oracle-convergence-6", func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: []WorkerReviewFinding{{
			Severity: "P0", Confidence: "high", Detail: "a consequential outside-scope concern",
			Oracle: &WorkerOracleFinding{Classification: OracleClassificationFollowUp},
		}}}
	})}}); err != nil {
		t.Fatalf("accepted-no_ship round refused: %v", err)
	}
	if err := runVerdictActionAs(t, s, workID, "accept_worker_result", mustJSONValue(map[string]any{"attempt_id": attempt6, "attempt_epoch": latestStepStartEpoch(t, s, workID, "repair")}), 0, f.acceptor); err != nil {
		t.Fatalf("accept the no_ship report: %v", err)
	}
	if count, countErr := workflowNonProgressAttemptCount(context.Background(), s.DatabaseForTesting(), workID, "worker_oracle_findings_test"); countErr != nil || count < 1 {
		t.Fatalf("non-progress after the accepted no_ship = (%d, %v), want the no_ship attempt counted", count, countErr)
	}
	if convergence, err = workflowRetryConvergence(context.Background(), s.DatabaseForTesting(), workID, "repair"); err != nil || (convergence.Basis != "" && convergence.Basis != "approach_changed") {
		t.Fatalf("convergence after an accepted no_ship = (%#v, %v), want no new basis family", convergence, err)
	}
}

// Oracle-bound job debt: a rewritten oracle does not discharge an earlier
// revision's unresolved window, a strengthened oracle with byte-identical
// old controls does, a renamed job never does, and recording any revision
// resets no retry count.
func TestOwnerOracleJobDebt(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-job-debt"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	jobID := "job:debt"
	baseOracle := twoPathOracleFields(t, s, workID, nil)
	rev1 := recordOracleJobRevision(t, s, workID, f.owner, jobID, baseOracle)

	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	attempt1 := "attempt:" + workID + ":1"
	oracleDispatchAttempt(t, f, workID, attempt1, nil, &rev1)
	failWorkerAttemptWithKind(t, s, workID, attempt1, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, f.owner, attempt1, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "oracle-debt-f1")
	if pin := issue1013Pin(t, s, workID); pin.Correction == nil || pin.Correction.AttemptCount != 1 {
		t.Fatalf("correction after the first failure = %#v, want one counted attempt", pin.Correction)
	}

	// Revision 2 rewrites a control: same base obligation, changed oracle.
	rewritten := twoPathOracleFields(t, s, workID, func(oracle map[string]any) {
		oracle["controls"].([]map[string]any)[0]["argv"] = oracleGoArgv("path-a-v2")
	})
	rev2 := recordOracleJobRevision(t, s, workID, f.owner, jobID, rewritten)
	if count := jobWindowCorrectionCount(t, s, workID); count != 1 {
		t.Fatalf("recording a revision reset the count to %d", count)
	}
	attempt2 := "attempt:" + workID + ":2"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	oracleDispatchAttempt(t, f, workID, attempt2, nil, &rev2)
	completeAndAcceptAttempt(t, s, workID, attempt2, reviewGateLane(t, "implementation"), f.acceptor)
	if count := jobWindowCorrectionCount(t, s, workID); count != 2 {
		t.Fatalf("count after the rewritten-oracle acceptance = %d, want the preserved window", count)
	}
	correction, corrErr := workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", "")
	if corrErr != nil || correction == nil || correction.AttemptCount != 2 {
		t.Fatalf("window after the rewritten-oracle acceptance = (%#v, %v), want the unresolved debt with both dispatches", correction, corrErr)
	}

	// Revision 3 strengthens the inventory with the old controls retained
	// byte-identically: an acceptance under it discharges the old debt.
	strengthened := twoPathOracleFields(t, s, workID, func(oracle map[string]any) {
		controls := oracle["controls"].([]map[string]any)
		first := controls[0]
		added := map[string]any{
			"control_id": "control:check-c", "owner_id": "owner:repair",
			"predicate_ids": first["predicate_ids"], "case_ids": []string{"case:path-c"},
			"recipe_source": first["recipe_source"], "argv": oracleGoArgv("path-c"),
			"cwd": "internal/store", "expected_result": "pass", "required_evidence_role": "reported",
			"readiness_evidence_refs": first["readiness_evidence_refs"],
		}
		oracle["controls"] = append(controls, added)
		oracle["cases"] = append(oracle["cases"].([]map[string]any), map[string]any{
			"case_id": "case:path-c", "owner_id": "owner:repair", "entry_point": "repair path c",
			"input_class": "recovery entry", "expected_state": "path c passes", "control_ids": []string{"control:check-c"},
		})
	})
	rev3 := recordOracleJobRevision(t, s, workID, f.owner, jobID, strengthened)
	attempt3 := "attempt:" + workID + ":3"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	oracleDispatchAttempt(t, f, workID, attempt3, nil, &rev3)
	completeAndAcceptAttempt(t, s, workID, attempt3, reviewGateLane(t, "implementation"), f.acceptor)
	if correction, corrErr = workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", ""); corrErr != nil || correction != nil {
		t.Fatalf("window after the strengthened-oracle acceptance = (%#v, %v), want the discharged debt", correction, corrErr)
	}

	// A renamed job with the same obligation never discharges another
	// job's debt: fresh window, new failure under a fresh unsatisfied
	// revision of the original job, then an unrelated acceptance.
	renamed := twoPathOracleFields(t, s, workID, nil)
	renamedBinding := recordOracleJobRevision(t, s, workID, f.owner, "job:debt-renamed", renamed)
	rev4 := recordOracleJobRevision(t, s, workID, f.owner, jobID, twoPathOracleFields(t, s, workID, nil))
	attempt4 := "attempt:" + workID + ":4"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	oracleDispatchAttempt(t, f, workID, attempt4, nil, &rev4)
	failWorkerAttemptWithKind(t, s, workID, attempt4, "lane fallback blocked before the model ran")
	applyRecordWorkerFailureForTest(t, s, workID, f.owner, attempt4, latestStepStartEpoch(t, s, workID, "repair"), readWorkVersion(t, s, workID), "oracle-debt-f4")
	attempt5 := "attempt:" + workID + ":5"
	issue1013StartRepair(t, s, workID, f.owner, readWorkVersion(t, s, workID), latestStepStartEpoch(t, s, workID, "repair")+1)
	oracleDispatchAttempt(t, f, workID, attempt5, nil, &renamedBinding)
	completeAndAcceptAttempt(t, s, workID, attempt5, reviewGateLane(t, "implementation"), f.acceptor)
	if correction, corrErr = workflowCorrectionContextForDispatch(context.Background(), s.DatabaseForTesting(), workID, "repair", ""); corrErr != nil || correction == nil {
		t.Fatalf("window after the renamed-job acceptance = (%#v, %v), want the original job's unresolved debt", correction, corrErr)
	}
}

// Replay boundaries: oracle report content below its payload-version
// boundary refuses as fabricated, historical oracle-free payloads fold
// unchanged at the current version, and two rebuilds of the log reach the
// same lineage the live derivation produced.
func TestOwnerOracleReplay(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-replay"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:replay"}
	attempt := "attempt:" + workID
	oracleDispatchAttempt(t, f, workID, attempt, twoPathOracleFields(t, s, workID, nil), job)

	// A v5 completion carrying a receipt refuses: the members did not exist
	// at that version, so the bytes are fabricated.
	stalePayload := oracleCompletionEvent(t, s, workID, attempt, "oracle-replay-stale", func(payload *WorkerCompletedPayload) {
		withReceipt(payload, &WorkerOracleReceipt{
			ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"},
			RecipeSource: WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()},
			Result:       OracleReceiptResultNotRun, RunRef: "",
		})
	})
	stalePayload.PayloadVersion = workerCompletedContextFindingsVersion
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{stalePayload}}); err == nil || !strings.Contains(err.Error(), "reserved for payload version >= 6") {
		t.Fatalf("stale-version oracle content error = %v", err)
	}

	// A current-version completion without oracle content folds unchanged.
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt, "oracle-replay-plain", nil)}}); err != nil {
		t.Fatalf("oracle-free completion refused: %v", err)
	}
	live, err := readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}

	// Two rebuilds reach the same live view: no test depends on today's
	// registry to reinterpret the recorded events.
	for range 2 {
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatalf("rebuild from log: %v", err)
		}
	}
	rebuilt, err := readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(live.openOrder) != len(rebuilt.openOrder) || len(live.findings) != len(rebuilt.findings) || live.oracleCapable != rebuilt.oracleCapable {
		t.Fatalf("rebuilt lineage = (%d minted, %d open-order, capable=%v), want the live derivation (%d, %d, %v)",
			len(rebuilt.findings), len(rebuilt.openOrder), rebuilt.oracleCapable, len(live.findings), len(live.openOrder), live.oracleCapable)
	}
}

// Recovery: more than 32 open blockers refuses dispatch and the correction
// surface explicitly — never truncating — while the supersession route and
// the stop route stay usable, and the context read refuses past the view
// bound instead of dropping blockers.
func TestOwnerOracleReceiptViewRecoversAfterContractSupersession(t *testing.T) {
	s, git, _ := worktreeFixture(t)
	claimFixtureWorktree(t, s, git)
	ctx := context.Background()
	appendEvent := func(id string, kind string, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Transact(ctx, func(tx *Transaction) error {
			_, err := tx.tx.ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,'work_item','work-w','actor:fixture','2026-10-10T00:00:00Z',6,?)`, id, kind, string(raw))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Ledger fixtures isolate projection lifetime. Native producer and terminal
	// admission tests separately establish receipt execution and authority.
	receipt := WorkerOracleReceipt{ControlIDs: []string{"control:immutable"}, CaseIDs: []string{"case:immutable"}, Result: OracleReceiptResultNotRun}
	for i := range oracleRetainedReceiptsMax + 1 {
		appendEvent("receipt-history-"+itoa(i), WorkerCompleted, WorkerCompletedPayload{Evidence: []WorkerReportEvidence{{OracleReceipt: &receipt}}})
	}
	if _, err := readWorkContextView(ctx, s.db, "work-w"); err == nil || !strings.Contains(err.Error(), "above the 64 view bound") {
		t.Fatalf("over-bound receipt view = %v, want an explicit refusal", err)
	}
	appendEvent("receipt-contract-supersession", WorkflowContractSuperseded, map[string]any{})
	lineage, err := readWorkerOracleFindingLineageTx(ctx, s.db, "work-w")
	if err != nil {
		t.Fatal(err)
	}
	if len(lineage.retainedReceipts) != 0 {
		t.Fatalf("superseded receipt view holds %d receipts, want 0", len(lineage.retainedReceipts))
	}
	if _, err := readWorkContextView(ctx, s.db, "work-w"); err != nil {
		t.Fatalf("context remains stranded after contract supersession: %v", err)
	}
	appendEvent("receipt-successor-report", WorkerCompleted, WorkerCompletedPayload{Evidence: []WorkerReportEvidence{{OracleReceipt: &receipt}}})
	lineage, err = readWorkerOracleFindingLineageTx(ctx, s.db, "work-w")
	if err != nil {
		t.Fatal(err)
	}
	if len(lineage.retainedReceipts) != 1 {
		t.Fatalf("successor receipt view holds %d receipts, want 1", len(lineage.retainedReceipts))
	}
	var retained int
	if err := s.db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id='work-w' AND event_id LIKE 'receipt-history-%'`).Scan(&retained); err != nil || retained != oracleRetainedReceiptsMax+1 {
		t.Fatalf("supersession changed immutable receipt history: %d %v", retained, err)
	}
}

func TestOwnerOracleRecovery(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-recovery"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:recovery"}
	attempt := "attempt:" + workID
	oracleDispatchAttempt(t, f, workID, attempt, twoPathOracleFields(t, s, workID, nil), job)

	// Thirty-three genuine blockers fold: the report bound holds 64
	// findings and the report itself never truncates.
	findings := make([]WorkerReviewFinding, 0, WorkflowOpenOracleFindingsLimit+1)
	for index := range WorkflowOpenOracleFindingsLimit + 1 {
		family := "family-" + itoa(index)
		findings = append(findings, WorkerReviewFinding{
			Severity: "P1", Confidence: "high", Detail: "blocker " + family,
			Oracle: &WorkerOracleFinding{
				Classification: OracleClassificationDeliveryBlocker, OwnerID: "owner:repair",
				FailureFamily: family, PredicateIDs: fixturePredicateList(t, s, workID),
				EvidenceRefs: []string{"run:blocker-" + family},
			},
		})
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{oracleCompletionEvent(t, s, workID, attempt, "oracle-recovery", func(payload *WorkerCompletedPayload) {
		payload.Review = &WorkerReviewBlock{Verdict: "no_ship", Findings: findings}
	})}}); err != nil {
		t.Fatalf("over-bound blocker report refused at the fold: %v", err)
	}
	lineage, err := readWorkerOracleFindingLineageTx(context.Background(), s.DatabaseForTesting(), workID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(lineage.openFindingIDs()); got != WorkflowOpenOracleFindingsLimit+1 {
		t.Fatalf("open blockers = %d, want %d retained with no omission", got, WorkflowOpenOracleFindingsLimit+1)
	}
	// The correction surface refuses explicitly rather than truncating.
	err = validateCorrectionOpenFindingsTx(context.Background(), s.DatabaseForTesting(), workID, lineage.openFindingIDs()[:32], true)
	if err == nil || !strings.Contains(err.Error(), "refuses rather than truncate") {
		t.Fatalf("over-bound correction error = %v", err)
	}
	// The pure dispatch admission carries the same refusal while the
	// supersession and stop routes stay usable.
	definition := mustBuiltinDefinition(t, "workflow.break_fix").Definition
	state := WorkflowAdmissionState{Step: "repair", OracleOpenFindings: WorkflowOpenOracleFindingsLimit + 1}
	if decision := workflowAdmit(definition, state, "dispatch_worker"); decision.Admitted || decision.Failure == nil || !strings.Contains(decision.Failure.Detail, "open oracle blockers above the 32-ID bound") {
		t.Fatalf("over-bound dispatch decision = %#v, want the explicit bound refusal", decision)
	}
	if decision := workflowAdmit(definition, WorkflowAdmissionState{Step: "repair", ActiveContracts: 1, LawPinStale: true, ContractCorrectionAvailable: true, OracleOpenFindings: WorkflowOpenOracleFindingsLimit + 1}, "supersede_contract"); !decision.Admitted {
		t.Fatalf("supersede decision over the bound = %#v, want the recovery route admitted", decision)
	}
	// The context read refuses past the view bound; blockers are never
	// silently dropped by a coordinator selection either way.
	if _, viewErr := readWorkContextView(context.Background(), s.DatabaseForTesting(), workID); viewErr == nil || !strings.Contains(viewErr.Error(), "refuses instead of truncating") {
		t.Fatalf("over-bound context view error = %v, want the explicit view refusal", viewErr)
	}
}

// fixturePredicateList reads the fixture oracle's covered predicate as a
// one-element list for blocker ties.
func fixturePredicateList(t *testing.T, s *Store, workID string) []string {
	t.Helper()
	return []string{fixturePredicate(t, s, workID)}
}

// The legacy request comparison keeps predicate ids when no open finding set
// was recorded, and prefers the derived set the moment one was.
func TestOwnerOracleRequestOpenFindingsComparison(t *testing.T) {
	t.Parallel()
	legacy := workflowCorrectionCompletionFields{ActionID: "request_correction", CorrectionPredicates: []string{"predicate:return-route"}}
	if got := legacy.openFindings(); len(got) != 1 || got[0] != "predicate:return-route" {
		t.Fatalf("legacy request open findings = %v, want the predicate ids", got)
	}
	oracle := workflowCorrectionCompletionFields{ActionID: "request_correction", CorrectionPredicates: []string{"predicate:return-route"}, OpenFindingIDs: []string{"finding:12:3"}}
	if got := oracle.openFindings(); len(got) != 1 || got[0] != "finding:12:3" {
		t.Fatalf("oracle request open findings = %v, want the recorded derived set", got)
	}
	rejection := workflowCorrectionCompletionFields{ActionID: "reject_worker_result", OpenFindingIDs: []string{"finding:12:3"}}
	if got := rejection.openFindings(); len(got) != 1 || got[0] != "finding:12:3" {
		t.Fatalf("rejection open findings = %v, want the recorded set", got)
	}
	// The fold admits the serialized set on both correction surfaces and
	// refuses it everywhere else.
	completion := func(actionID string, ids []string) workflowActionCompletedPayload {
		return workflowActionCompletedPayload{ActionID: actionID, StepID: "repair", AttemptEpoch: 1,
			CorrectionDiagnosis: "the review found open oracle blockers", CorrectionStrategy: "repair the failing entry paths",
			CorrectionPredicateIDs: []string{"predicate:return-route"}, CorrectionEvidenceRefs: []string{"evidence:return-route-verification"},
			CorrectionOpenFindingIDs: ids}
	}
	if err := validateWorkflowActionCompletedShape(completion("request_correction", []string{"finding:12:3"})); err != nil {
		t.Fatalf("request_correction completion carrying the derived set refused: %v", err)
	}
	if err := validateWorkflowActionCompletedShape(completion("reject_worker_result", []string{"finding:12:3"})); err != nil {
		t.Fatalf("rejection completion carrying the derived set refused: %v", err)
	}
	if err := validateWorkflowActionCompletedShape(completion("record_worker_failure", []string{"finding:12:3"})); err == nil {
		t.Fatal("an unrelated action carried correction_open_finding_ids through the fold shape")
	}
}

// The equality admits the empty derived set only through the field's
// absence: a coordinator cannot record zero ids, and a nonempty claim over
// an empty set refuses.
func TestOwnerOracleEmptyDerivedSetEquality(t *testing.T) {
	t.Parallel()
	const workID = "owner-oracle-empty-set"
	f := seedOracleRepairFixture(t, workID)
	s := f.store
	defer s.Close()
	job := &WorkerJobBinding{JobID: "job:empty-set"}
	attempt := "attempt:" + workID
	oracleDispatchAttempt(t, f, workID, attempt, twoPathOracleFields(t, s, workID, nil), job)
	// The dispatch alone makes the history oracle-capable with no findings.
	if err := validateCorrectionOpenFindingsTx(context.Background(), s.DatabaseForTesting(), workID, nil, false); err != nil {
		t.Fatalf("absent field over the empty derived set refused: %v", err)
	}
	if err := validateCorrectionOpenFindingsTx(context.Background(), s.DatabaseForTesting(), workID, []string{"finding:9:9"}, true); err == nil || !strings.Contains(err.Error(), "while the derived open set holds 0") {
		t.Fatalf("fabricated id over the empty derived set error = %v", err)
	}
}

// The typed receipt shape coupling is decidable without the store: the
// exit-code presence follows the result exactly.
func TestOwnerOracleReceiptShapeCoupling(t *testing.T) {
	t.Parallel()
	recipe := WorkerOracleRecipeSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "scripts/oracle_check.sh", CommitOID: testOracleCommit()}
	code := 0
	valid := &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, RecipeSource: recipe, Result: OracleReceiptResultPass, ExitCode: &code, RunRef: "run:one"}
	if err := ValidateWorkerOracleReceiptShape(valid); err != nil {
		t.Fatalf("executed receipt refused: %v", err)
	}
	unavailable := &WorkerOracleReceipt{ControlIDs: []string{"control:check-a"}, CaseIDs: []string{"case:path-a"}, RecipeSource: recipe, Result: OracleReceiptResultNotRun, RunRef: "", EvidenceRefs: []string{"selector empty"}}
	if err := ValidateWorkerOracleReceiptShape(unavailable); err != nil {
		t.Fatalf("not_run receipt refused: %v", err)
	}
	dupControl := *valid
	dupControl.ControlIDs = []string{"control:check-a", "control:check-a"}
	if err := ValidateWorkerOracleReceiptShape(&dupControl); err == nil || !strings.Contains(err.Error(), "unique") {
		t.Fatalf("duplicate control receipt error = %v", err)
	}
	badResult := *valid
	badResult.Result = "timeout"
	if err := ValidateWorkerOracleReceiptShape(&badResult); err == nil || !strings.Contains(err.Error(), "result") {
		t.Fatalf("unknown result receipt error = %v", err)
	}
	overBound := *valid
	overBound.ControlIDs = nil
	if err := ValidateWorkerOracleReceiptShape(&overBound); err == nil || !strings.Contains(err.Error(), "1 to 8 controls") {
		t.Fatalf("controlless receipt error = %v", err)
	}
}
