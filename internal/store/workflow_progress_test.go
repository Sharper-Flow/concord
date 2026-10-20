package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func progressHistory(t *testing.T, legacy bool) (*sql.DB, func(string, any) int64, func() *workflowCorrectionHistory) {
	t.Helper()
	db, _ := correctionHistoryFixture(t)
	progressFixtureTables(t, db)
	var seq int64
	appendEvent := func(kind string, payload any) int64 {
		t.Helper()
		seq++
		if _, err := db.Exec(`INSERT INTO domain_events VALUES(?,?,?,?,?,?,?)`, seq, string(SubjectWorkItem), "work-history", kind, mustJSONValue(payload), "", fmt.Sprintf("progress-e%d", seq)); err != nil {
			t.Fatal(err)
		}
		return seq
	}
	version := int64(37)
	if legacy {
		version--
	}
	entry, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", version)
	if !ok {
		t.Fatal("missing progress definition")
	}
	appendEvent(WorkflowDefinitionSelected, workflowDefinitionSelectedPayload{Ref: entry.Definition.Ref, Version: version, Digest: entry.Digest})
	appendEvent(WorkflowContractApproved, workflowContractApprovedPayload{ContractVersion: 1, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}})
	read := func() *workflowCorrectionHistory {
		t.Helper()
		history, err := workflowCorrectionWalk(context.Background(), db, "work-history", 1<<62, "progress_test")
		if err != nil {
			t.Fatal(err)
		}
		return history
	}
	return db, appendEvent, read
}

// progressFixtureTables creates the immutable projections the shared
// qualification guards read: actor tuples, the instance executor lookup, and
// the durable completed operations behind evidence bindings.
func progressFixtureTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`DROP TABLE domain_events`,
		// The shared evaluation-authority guard reads the event actor column;
		// the correction fixture's narrower table shape is replaced whole.
		// event_id keeps the merged walk's split-event join readable.
		`CREATE TABLE domain_events(seq INTEGER PRIMARY KEY,subject_type TEXT,subject_id TEXT,kind TEXT,payload BLOB,actor TEXT NOT NULL DEFAULT '',event_id TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE workflow_actors (actor_ref TEXT PRIMARY KEY, principal_ref TEXT NOT NULL, client_ref TEXT NOT NULL, agent_ref TEXT NOT NULL, session_ref TEXT NOT NULL, actor_class TEXT NOT NULL)`,
		`CREATE TABLE workflow_instances (work_id TEXT PRIMARY KEY, execution_actor_ref TEXT)`,
		`CREATE TABLE durable_operations (op_id TEXT, attempt_epoch INTEGER NOT NULL DEFAULT 1, work_id TEXT NOT NULL, principal_ref TEXT NOT NULL, request_id TEXT NOT NULL, result_kind TEXT, evidence_refs TEXT NOT NULL DEFAULT '[]')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

// progressActorRecord inserts one canonical actor tuple and returns its
// derived reference; the replay validators refuse a tuple whose actor_ref
// does not match the canonical derivation.
func progressActorRecord(t *testing.T, db *sql.DB, name, class string) string {
	t.Helper()
	ref := DeriveWorkflowActorRef("principal:fixture", "client:fixture", "agent:"+name, "session:"+name)
	if _, err := db.Exec(`INSERT INTO workflow_actors VALUES(?,?,?,?,?,?)`, ref, "principal:fixture", "client:fixture", "agent:"+name, "session:"+name, class); err != nil {
		t.Fatal(err)
	}
	return ref
}

func progressCompletedOperation(t *testing.T, db *sql.DB, opID, producerID, requestID, subjectRef string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO durable_operations(op_id,work_id,principal_ref,request_id,result_kind,evidence_refs) VALUES(?,?,?,?,?,?)`, opID, "work-history", producerID, requestID, "completed", mustJSONValue([]string{subjectRef})); err != nil {
		t.Fatal(err)
	}
}

// progressQualifiedEvidence binds the required artifact kind with a completed
// durable producer operation behind it.
func progressQualifiedEvidence(t *testing.T, db *sql.DB, appendEvent func(string, any) int64, ref string) int64 {
	t.Helper()
	progressCompletedOperation(t, db, "run:qualified", "producer:independent", "request:qualified", ref)
	return appendEvent(WorkflowEvidenceBound, progressEvidence("artifact", ref))
}

func progressEvidence(kind, ref string) workflowEvidenceBoundPayload {
	return workflowEvidenceBoundPayload{EvidenceKind: kind, ImmutableSubjectRef: ref, ProducerID: "producer:independent", ProducerRunRef: "run:qualified", ProducerWatermark: "request:qualified", ObservedAt: "2026-10-20T00:00:00Z"}
}

func progressVerdict(verdictActor string, evidence []string) workflowVerdictRecordedPayload {
	return workflowVerdictRecordedPayload{ContractVersion: 1, PredicateID: "predicate:one", VerdictKind: "ok", VerdictActorRef: verdictActor, EvaluationEvidence: evidence}
}

func TestCoordinatorProgressKeepsWorkerRecoveryBoundary(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	worker := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:productive", "", 0))
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:failed", "", 0))
	appendEvent(WorkerFailed, map[string]any{"attempt_id": "attempt:failed"})
	seq := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	h := read()
	if h.boundary != worker || h.nonprogressBoundary != seq {
		t.Fatalf("worker/nonprogress anchors = %d/%d, want %d/%d; coordinator progress must not renew host recovery or erase exact retry bindings", h.boundary, h.nonprogressBoundary, worker, seq)
	}
}

func TestCoordinatorProgressRequiresFirstQualifiedObligation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "legacy"}[legacy], func(t *testing.T) {
			db, appendEvent, read := progressHistory(t, legacy)
			progressCompletedOperation(t, db, "run:qualified", "producer:independent", "request:qualified", "evidence:optional")
			appendEvent(WorkflowEvidenceBound, progressEvidence("commit", "evidence:optional"))
			appendEvent(WorkObservationRecorded, map[string]any{"statement": "claimed progress"})
			appendEvent(WorkflowActionCompleted, map[string]any{"action_id": "record_delivery"})
			if h := read(); h.nonprogressBoundary != 0 {
				t.Fatal("optional evidence, observation, or step advance renewed budget")
			}
			first := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
			progressCompletedOperation(t, db, "run:qualified", "producer:independent", "request:qualified", "evidence:replacement")
			appendEvent(WorkflowEvidenceBound, progressEvidence("artifact", "evidence:replacement"))
			want := first
			if legacy {
				want = 0
			}
			if h := read(); h.nonprogressBoundary != want || h.boundary != 0 {
				t.Fatalf("anchors = %d/%d, want 0/%d", h.boundary, h.nonprogressBoundary, want)
			}
		})
	}
}

func TestCoordinatorProgressCannotDischargeJobOrRepairDebt(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	coordinator := progressActorRecord(t, db, "coordinator", "agent")
	appendEvent(WorkerJobRecorded, WorkerJobRecordedPayload{JobID: "job:failed", Revision: 1, Objective: "retain the exact failed obligation"})
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:failed", "job:failed", 1))
	appendEvent(WorkerFailed, map[string]any{"attempt_id": "attempt:failed"})
	progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 1, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"})
	if h := read(); h.nonprogressBoundary != 0 || h.boundary != 0 || len(h.unresolved) != 1 {
		t.Fatal("coordinator progress erased failed-job debt")
	}
}

func TestPredicateProgressNeedsBoundComparableEvidenceAndSurvivesRename(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	evaluator := progressActorRecord(t, db, "evaluator", "agent")
	appendEvent(WorkflowVerdictRecorded, progressVerdict(evaluator, []string{"evidence:missing"}))
	if read().nonprogressBoundary != 0 {
		t.Fatal("unsupported verdict reset budget")
	}
	bound := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	verdict := progressVerdict(evaluator, []string{"evidence:qualified"})
	verdict.IncomparableWithApproved = true
	appendEvent(WorkflowVerdictRecorded, verdict)
	if read().nonprogressBoundary != bound {
		t.Fatal("incomparable verdict reset budget")
	}
	verdict.IncomparableWithApproved = false
	first := appendEvent(WorkflowVerdictRecorded, verdict)
	appendEvent(WorkflowVerdictRecorded, verdict)
	if read().nonprogressBoundary != first {
		t.Fatal("repeated verdict renewed credit")
	}
	contract := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:renamed", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"subjects": []string{"owner"}, "surface": "source.go", "kind": "exists"})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &contract})
	verdict.ContractVersion, verdict.PredicateID = 2, "predicate:renamed"
	appendEvent(WorkflowVerdictRecorded, verdict)
	if read().nonprogressBoundary != first {
		t.Fatal("renaming or superseding an obligation renewed credit")
	}
}

// TestPredicateProgressChangedSubjectBuysNoCredit proves the changed-subject
// and rename alias rules: the same predicate id under a changed recorded
// subject is the same spent obligation, and a renamed id carrying a
// comparable earlier payload aliases to it. Neither mints credit; a genuinely
// distinct required obligation earns its own first satisfaction.
func TestPredicateProgressChangedSubjectBuysNoCredit(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	evaluator := progressActorRecord(t, db, "evaluator", "agent")
	progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	first := appendEvent(WorkflowVerdictRecorded, progressVerdict(evaluator, []string{"evidence:qualified"}))
	changed := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "changed.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &changed})
	verdict := progressVerdict(evaluator, []string{"evidence:qualified"})
	verdict.ContractVersion = 2
	appendEvent(WorkflowVerdictRecorded, verdict)
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("changed subject under the same predicate id renewed credit: anchor %d, want %d", h.nonprogressBoundary, first)
	}
	aliased := workflowContractApprovedPayload{ContractVersion: 3, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:aliased", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"subjects": []string{"owner"}, "surface": "source.go", "kind": "exists"})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 3, SuccessorContract: &aliased})
	verdict.ContractVersion, verdict.PredicateID = 3, "predicate:aliased"
	appendEvent(WorkflowVerdictRecorded, verdict)
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("a renamed id carrying the earlier comparable payload renewed credit: anchor %d, want %d", h.nonprogressBoundary, first)
	}
	distinct := workflowContractApprovedPayload{ContractVersion: 4, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{
		{PredicateID: "predicate:aliased", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})},
		{PredicateID: "predicate:two", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "other.go", "subjects": []string{"owner"}})},
	}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 4, SuccessorContract: &distinct})
	verdict.ContractVersion, verdict.PredicateID = 4, "predicate:two"
	second := appendEvent(WorkflowVerdictRecorded, verdict)
	if h := read(); h.nonprogressBoundary != second {
		t.Fatalf("a genuinely distinct required obligation lost credit: anchor %d, want %d", h.nonprogressBoundary, second)
	}
}

// TestPredicateProgressRefusesSelfEvaluation proves the evaluator rules: the
// actor that executed the work, and an actor that dispatched the evaluated
// worker lane, cannot qualify a predicate verdict. An independent recorded
// evaluator can.
func TestPredicateProgressRefusesSelfEvaluation(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	executor := progressActorRecord(t, db, "executor", "agent")
	dispatcher := progressActorRecord(t, db, "dispatcher", "agent")
	evaluator := progressActorRecord(t, db, "evaluator", "agent")
	appendEvent(WorkflowActionStarted, workflowActionStartedPayload{StepID: "execution", ActionID: "start_execution", ActorRef: executor, ExecutionModel: "model:implementation"})
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:one", "", 0))
	appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:one", "lane_actor_ref": dispatcher})
	bound := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	verdict := progressVerdict(evaluator, []string{"evidence:qualified"})
	verdict.VerdictActorRef = executor
	appendEvent(WorkflowVerdictRecorded, verdict)
	if h := read(); h.nonprogressBoundary != bound {
		t.Fatal("the executing actor qualified its own predicate verdict")
	}
	verdict.VerdictActorRef = dispatcher
	appendEvent(WorkflowVerdictRecorded, verdict)
	if h := read(); h.nonprogressBoundary != bound {
		t.Fatal("an actor that dispatched the evaluated worker lane qualified the verdict")
	}
	verdict.VerdictActorRef = evaluator
	first := appendEvent(WorkflowVerdictRecorded, verdict)
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("an independent recorded evaluator lost credit: anchor %d, want %d", h.nonprogressBoundary, first)
	}
}

// TestEvidenceProgressRequiresCompletedProducer proves a kind requirement is
// satisfied only by a binding whose producer operation completed with the
// subject among its evidence; a well-formed binding without one qualifies
// nothing.
func TestEvidenceProgressRequiresCompletedProducer(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	appendEvent(WorkflowEvidenceBound, progressEvidence("artifact", "evidence:unbacked"))
	if h := read(); h.nonprogressBoundary != 0 {
		t.Fatal("an incomplete producer binding qualified the required kind")
	}
	qualified := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	if h := read(); h.nonprogressBoundary != qualified {
		t.Fatalf("the completed producer binding lost credit: anchor %d, want %d", h.nonprogressBoundary, qualified)
	}
}

// TestEvidenceProgressCoversMandatesAndObligations proves the requirement
// derivation covers spec mandates and verification-obligation tuples beside
// required kinds, and that an unrelated unbacked binding mints nothing.
func TestEvidenceProgressCoversMandatesAndObligations(t *testing.T) {
	db, appendEvent, _ := progressHistory(t, false)
	mandated := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, SpecMandate: []string{"law:mandate"}, ArchitectureBinding: &WorkflowArchitectureBinding{DomainRegistryContentHash: "sha256:" + strings.Repeat("a", 64), HomeDomainID: "domain:store", AffectedDomainIDs: []string{"domain:store"}, DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{}, VerificationObligations: []WorkflowVerificationObligation{{LawID: "law:tuple", ObligationID: "obligation:review"}}}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &mandated})
	mandateSeq := appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:mandate", ProducerID: "producer:x", ProducerRunRef: "run:x", ProducerWatermark: "request:x", ObservedAt: "2026-10-20T00:00:00Z"})
	// A mandate reference matches on the immutable subject alone; the
	// completed-operation rule belongs to kind requirements.
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != mandateSeq {
		t.Fatalf("a bound spec mandate lost credit: anchor %d, want %d", h.nonprogressBoundary, mandateSeq)
	}
	tupleSeq := appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "obligation:review", ImmutableSubjectRef: "law:tuple", ProducerID: "producer:x", ProducerRunRef: "run:x", ProducerWatermark: "request:x", ObservedAt: "2026-10-20T00:00:00Z"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != tupleSeq {
		t.Fatalf("a bound verification obligation lost credit: anchor %d, want %d", h.nonprogressBoundary, tupleSeq)
	}
	appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "review", ImmutableSubjectRef: "evidence:other", ProducerID: "producer:x", ProducerRunRef: "run:x", ProducerWatermark: "request:x", ObservedAt: "2026-10-20T00:00:00Z"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != tupleSeq || len(h.progress.satisfactions) != 2 {
		t.Fatalf("an unrelated binding minted credit: anchor %d with %d satisfactions", h.nonprogressBoundary, len(h.progress.satisfactions))
	}
}

// TestEvidenceProgressStopsAtCompleteStepCorrectionCutoff proves the walked
// complete-step correction cutoff: after a route-declaring supersession, a
// predecessor binding can no longer satisfy the successor's requirement, and
// only fresh post-cutoff evidence does.
func TestEvidenceProgressStopsAtCompleteStepCorrectionCutoff(t *testing.T) {
	db, appendEvent, _ := progressHistory(t, false)
	appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:mandate", ProducerID: "producer:x", ProducerRunRef: "run:x", ProducerWatermark: "request:x", ObservedAt: "2026-10-20T00:00:00Z"})
	routed := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, SpecMandate: []string{"law:mandate"}, RouteConventions: []string{workflowCompleteStepCorrectionRoute}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	superseded := appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &routed})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != 0 {
		t.Fatalf("the pre-cutoff binding satisfied the routed contract's requirement: anchor %d", h.nonprogressBoundary)
	}
	fresh := appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:mandate", ProducerID: "producer:x", ProducerRunRef: "run:fresh", ProducerWatermark: "request:fresh", ObservedAt: "2026-10-20T01:00:00Z"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != fresh {
		t.Fatalf("post-cutoff evidence lost credit: anchor %d, want %d", h.nonprogressBoundary, fresh)
	}
	if h := workflowCorrectionWalkPrefix(t, db, superseded); h.nonprogressBoundary != 0 {
		t.Fatalf("a prefix walk before the satisfaction anchored at %d", h.nonprogressBoundary)
	}
}

// TestPremiseProgressFollowsRecordedAuthority proves the premise rules: the
// recorded confirmation must carry the authority the walked contract's
// decision fact licenses, held by an actor of the matching class, and a
// contract recorded without the decision fact qualifies nothing.
func TestPremiseProgressFollowsRecordedAuthority(t *testing.T) {
	db, appendEvent, _ := progressHistory(t, false)
	coordinator := progressActorRecord(t, db, "coordinator", "agent")
	operator := progressActorRecord(t, db, "operator", "operator")
	worker := progressActorRecord(t, db, "worker", "agent")
	appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:w", "lane_actor_ref": worker})
	// Engineering domains license only the authenticated coordinator.
	engineering := appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 1, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != engineering {
		t.Fatalf("an engineering premise confirmation by an agent lost credit: anchor %d, want %d", h.nonprogressBoundary, engineering)
	}
	// A worker actor cannot carry the coordinator confirmation.
	appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 1, ConfirmingActorRef: worker, ConfirmationAuthority: "coordinator"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != engineering {
		t.Fatalf("a worker actor carried the coordinator confirmation: anchor %d", h.nonprogressBoundary)
	}
	// Decision-bearing domains license only the operator class.
	decided := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{"product"}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &decided})
	confirmed := appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 2, ConfirmingActorRef: operator, ConfirmationAuthority: "operator"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != confirmed {
		t.Fatalf("a decision-bearing confirmation by the operator lost credit: anchor %d, want %d", h.nonprogressBoundary, confirmed)
	}
	appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 2, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != confirmed {
		t.Fatalf("coordinator authority carried a decision-bearing confirmation: anchor %d", h.nonprogressBoundary)
	}
	// A contract recorded without the decision fact qualifies nothing.
	legacyShape := workflowContractApprovedPayload{ContractVersion: 3, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 3, SuccessorContract: &legacyShape})
	appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 3, ConfirmingActorRef: operator, ConfirmationAuthority: "operator"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != confirmed {
		t.Fatalf("a confirmation without the recorded decision fact minted credit: anchor %d, want %d", h.nonprogressBoundary, confirmed)
	}
}

// TestSatisfactionSpentUnderDebtCannotRebind proves the spent-once rule: a
// qualified satisfaction recorded while failed-job debt exists is consumed
// without renewal and cannot rebind after the debt clears; only a distinct
// obligation recorded after the debt clears renews the window.
func TestSatisfactionSpentUnderDebtCannotRebind(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	appendEvent(WorkerJobRecorded, WorkerJobRecordedPayload{JobID: "job:debt", Revision: 1, Objective: "repair the failed obligation"})
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:failed", "job:debt", 1))
	appendEvent(WorkerFailed, map[string]any{"attempt_id": "attempt:failed"})
	spent := progressQualifiedEvidence(t, db, appendEvent, "evidence:spent")
	progressQualifiedEvidence(t, db, appendEvent, "evidence:spent-again")
	if h := read(); h.nonprogressBoundary != 0 {
		t.Fatalf("satisfaction under debt renewed: anchor %d (spent at %d)", h.nonprogressBoundary, spent)
	}
	accepted := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:accepted", "job:debt", 1))
	if h := read(); h.nonprogressBoundary != accepted {
		t.Fatalf("the worker acceptance lost its renewal: anchor %d, want %d", h.nonprogressBoundary, accepted)
	}
	progressQualifiedEvidence(t, db, appendEvent, "evidence:spent-third")
	if h := read(); h.nonprogressBoundary != accepted {
		t.Fatalf("the spent obligation rebound after the debt cleared: anchor %d, want %d", h.nonprogressBoundary, accepted)
	}
	freshContract := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, SpecMandate: []string{"law:fresh"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &freshContract})
	fresh := appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:fresh", ProducerID: "producer:x", ProducerRunRef: "run:fresh", ProducerWatermark: "request:fresh", ObservedAt: "2026-10-20T00:00:00Z"})
	if h := read(); h.nonprogressBoundary != fresh {
		t.Fatalf("a distinct unspent obligation could not renew after the debt cleared: anchor %d, want %d", h.nonprogressBoundary, fresh)
	}
	if again := read(); again.nonprogressBoundary != fresh {
		t.Fatalf("replay lost the renewal: anchor %d, want %d", again.nonprogressBoundary, fresh)
	}
}

// TestPrefixWalkReplayParity proves the fold derives the same anchors from
// one immutable prefix: a prefix walk before the satisfaction reads no
// anchor, and repeated full walks agree.
func TestPrefixWalkReplayParity(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	evaluator := progressActorRecord(t, db, "evaluator", "agent")
	if h := workflowCorrectionWalkPrefix(t, db, 2); h.nonprogressBoundary != 0 {
		t.Fatalf("an empty policy prefix anchored at %d", h.nonprogressBoundary)
	}
	bound := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	first := appendEvent(WorkflowVerdictRecorded, progressVerdict(evaluator, []string{"evidence:qualified"}))
	full := read()
	if full.nonprogressBoundary != first {
		t.Fatalf("full walk anchor = %d, want %d", full.nonprogressBoundary, first)
	}
	if again := read(); again.nonprogressBoundary != full.nonprogressBoundary || again.boundary != full.boundary {
		t.Fatal("repeated walks disagreed")
	}
	if h := workflowCorrectionWalkPrefix(t, db, first-1); h.nonprogressBoundary != bound {
		t.Fatalf("a walk ending at the evidence lost its anchor: %d, want %d", h.nonprogressBoundary, bound)
	}
	if h := workflowCorrectionWalkPrefix(t, db, bound-1); h.nonprogressBoundary != 0 {
		t.Fatalf("a walk before any satisfaction anchored at %d", h.nonprogressBoundary)
	}
}

// TestPredicateProgressAuthorityIsAPrefixFact proves the evaluator rules read
// the walked prefix alone: a worker lane dispatched after the verdict, and a
// later instance-lease write naming the evaluator, can never reclassify the
// recorded evaluator or move the satisfaction at its unchanged prefix.
func TestPredicateProgressAuthorityIsAPrefixFact(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	evaluator := progressActorRecord(t, db, "evaluator", "agent")
	progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	first := appendEvent(WorkflowVerdictRecorded, progressVerdict(evaluator, []string{"evidence:qualified"}))
	if _, err := db.Exec(`INSERT INTO workflow_instances VALUES('work-history', ?)`, evaluator); err != nil {
		t.Fatal(err)
	}
	appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:late", "lane_actor_ref": evaluator})
	h := read()
	if h.nonprogressBoundary != first {
		t.Fatalf("a later dispatch or lease write reclassified the recorded evaluator: anchor %d, want %d", h.nonprogressBoundary, first)
	}
	predicate := false
	for _, satisfaction := range h.progress.satisfactions {
		if satisfaction.Qualification == "comparable_predicate_satisfied" && satisfaction.Seq == first {
			predicate = true
		}
	}
	if len(h.progress.satisfactions) != 2 || !predicate {
		t.Fatalf("the recorded evaluator verdict lost its satisfaction: %d satisfactions, predicate at %d present = %v", len(h.progress.satisfactions), first, predicate)
	}
}

// TestPremiseProgressAuthorityIsAPrefixFact proves the confirming
// coordinator's authority classification reads the walked prefix alone: a
// worker lane dispatched after the confirmation cannot retroactively demote
// the confirmation to a worker's.
func TestPremiseProgressAuthorityIsAPrefixFact(t *testing.T) {
	db, appendEvent, _ := progressHistory(t, false)
	coordinator := progressActorRecord(t, db, "coordinator", "agent")
	first := appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 1, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"})
	appendEvent(WorkerDispatched, map[string]any{"attempt_id": "attempt:late", "lane_actor_ref": coordinator})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != first {
		t.Fatalf("a later dispatch reclassified the confirming coordinator: anchor %d, want %d", h.nonprogressBoundary, first)
	}
}

// TestEvidenceProgressCreditsEachRequirementAtItsBinding proves one binding
// that satisfies two requirements credits both at its own seq: a later
// duplicate binding earns no deferred credit and renews nothing.
func TestEvidenceProgressCreditsEachRequirementAtItsBinding(t *testing.T) {
	db, appendEvent, _ := progressHistory(t, false)
	dual := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, SpecMandate: []string{"law:dual"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &dual})
	progressCompletedOperation(t, db, "run:dual", "producer:dual", "request:dual", "law:dual")
	first := appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:dual", ProducerID: "producer:dual", ProducerRunRef: "run:dual", ProducerWatermark: "request:dual", ObservedAt: "2026-10-20T00:00:00Z"})
	h := workflowCorrectionWalkT(t, db)
	if h.nonprogressBoundary != first {
		t.Fatalf("the dual binding lost its credit: anchor %d, want %d", h.nonprogressBoundary, first)
	}
	if len(h.progress.satisfactions) != 2 {
		t.Fatalf("satisfactions = %d, want 2: one binding satisfying kind and mandate credits both", len(h.progress.satisfactions))
	}
	for _, satisfaction := range h.progress.satisfactions {
		if satisfaction.Seq != first {
			t.Fatalf("satisfaction %s recorded at %d, want the binding that satisfied it at %d", satisfaction.Obligation, satisfaction.Seq, first)
		}
	}
	appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:duplicate", ProducerID: "producer:dual", ProducerRunRef: "run:dual", ProducerWatermark: "request:dual", ObservedAt: "2026-10-20T01:00:00Z"})
	h = workflowCorrectionWalkT(t, db)
	if h.nonprogressBoundary != first || len(h.progress.satisfactions) != 2 {
		t.Fatalf("a later duplicate binding earned deferred credit: anchor %d with %d satisfactions, want %d with 2", h.nonprogressBoundary, len(h.progress.satisfactions), first)
	}
}

// TestEvidenceProgressDebtSpendsEveryRequirementAtOneBinding proves the debt
// rule covers every requirement one binding satisfies: a satisfaction recorded
// under failed-job debt is spent forever, and a later unrelated duplicate
// binding cannot carry the deferred identity past the debt.
func TestEvidenceProgressDebtSpendsEveryRequirementAtOneBinding(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	appendEvent(WorkerJobRecorded, WorkerJobRecordedPayload{JobID: "job:debt", Revision: 1, Objective: "retain the failed obligation"})
	appendEvent(WorkflowActionCompleted, correctionHistoryAction("dispatch_worker", "attempt:failed", "job:debt", 1))
	appendEvent(WorkerFailed, map[string]any{"attempt_id": "attempt:failed"})
	dual := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, SpecMandate: []string{"law:dual"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &dual})
	progressCompletedOperation(t, db, "run:dual", "producer:dual", "request:dual", "law:dual")
	appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:dual", ProducerID: "producer:dual", ProducerRunRef: "run:dual", ProducerWatermark: "request:dual", ObservedAt: "2026-10-20T00:00:00Z"})
	if h := read(); h.nonprogressBoundary != 0 {
		t.Fatalf("the dual satisfaction under debt renewed: anchor %d", h.nonprogressBoundary)
	}
	accepted := appendEvent(WorkflowActionCompleted, correctionHistoryAction("accept_worker_result", "attempt:accepted", "job:debt", 1))
	appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "artifact", ImmutableSubjectRef: "law:duplicate", ProducerID: "producer:dual", ProducerRunRef: "run:dual", ProducerWatermark: "request:dual", ObservedAt: "2026-10-20T01:00:00Z"})
	if h := read(); h.nonprogressBoundary != accepted {
		t.Fatalf("the debt-spent identity deferred its credit past the debt: anchor %d, want %d", h.nonprogressBoundary, accepted)
	}
}

// TestEvidenceProgressLateRequirementCannotCreditOldEvidence proves a
// requirement a supersession adds late, whose matching evidence the prefix
// already holds, credits no later unrelated record.
func TestEvidenceProgressLateRequirementCannotCreditOldEvidence(t *testing.T) {
	db, appendEvent, _ := progressHistory(t, false)
	old := progressQualifiedEvidence(t, db, appendEvent, "law:old")
	late := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, SpecMandate: []string{"law:old"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &late})
	appendEvent(WorkflowEvidenceBound, workflowEvidenceBoundPayload{EvidenceKind: "review", ImmutableSubjectRef: "evidence:unrelated", ProducerID: "producer:x", ProducerRunRef: "run:x", ProducerWatermark: "request:x", ObservedAt: "2026-10-20T00:00:00Z"})
	if h := workflowCorrectionWalkT(t, db); h.nonprogressBoundary != old {
		t.Fatalf("a late-added requirement credited old evidence at a later record: anchor %d, want %d", h.nonprogressBoundary, old)
	}
}

// TestPredicateAliasFamilyRetainsLineage proves the alias rule keeps the
// whole equivalence family: a predicate refused once as another obligation's
// payload alias cannot reidentify with a changed payload and renew credit.
func TestPredicateAliasFamilyRetainsLineage(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	evaluator := progressActorRecord(t, db, "evaluator", "agent")
	progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	first := appendEvent(WorkflowVerdictRecorded, progressVerdict(evaluator, []string{"evidence:qualified"}))
	shared := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{
		{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})},
		{PredicateID: "predicate:two", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})},
	}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &shared})
	alias := progressVerdict(evaluator, []string{"evidence:qualified"})
	alias.ContractVersion, alias.PredicateID = 2, "predicate:two"
	appendEvent(WorkflowVerdictRecorded, alias)
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("the shared-payload alias renewed credit: anchor %d, want %d", h.nonprogressBoundary, first)
	}
	reidentified := workflowContractApprovedPayload{ContractVersion: 3, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{
		{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})},
		{PredicateID: "predicate:two", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "other.go", "subjects": []string{"owner"}})},
	}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 3, SuccessorContract: &reidentified})
	alias.ContractVersion, alias.PredicateID = 3, "predicate:two"
	appendEvent(WorkflowVerdictRecorded, alias)
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("the alias reidentified with a changed payload renewed credit: anchor %d, want %d", h.nonprogressBoundary, first)
	}
}

// TestPremiseProgressIdentityIsThePremiseContent proves the premise
// obligation is the confirmation's semantic content: a supersession that
// changes only the contract version records the same premise and renews
// nothing, while a genuinely changed decision fact earns its own first
// satisfaction. Version validity is unchanged.
func TestPremiseProgressIdentityIsThePremiseContent(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	coordinator := progressActorRecord(t, db, "coordinator", "agent")
	operator := progressActorRecord(t, db, "operator", "operator")
	first := appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 1, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"})
	same := workflowContractApprovedPayload{ContractVersion: 2, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 2, SuccessorContract: &same})
	appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 2, ConfirmingActorRef: coordinator, ConfirmationAuthority: "coordinator"})
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("a contract-version bump renewed the unchanged premise: anchor %d, want %d", h.nonprogressBoundary, first)
	}
	decided := workflowContractApprovedPayload{ContractVersion: 3, RequiredEvidence: []string{"artifact"}, OutcomePredicates: []workflowContractPredicatePayload{{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})}}, DecisionDomains: []string{"product"}}
	appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: 3, SuccessorContract: &decided})
	second := appendEvent(WorkflowPremiseConfirmed, workflowPremiseConfirmedPayload{ContractVersion: 3, ConfirmingActorRef: operator, ConfirmationAuthority: "operator"})
	if h := read(); h.nonprogressBoundary != second {
		t.Fatalf("a genuinely changed premise lost its first satisfaction: anchor %d, want %d", h.nonprogressBoundary, second)
	}
}

// TestPredicateProgressRefusesUnhealthyAndMixedVerdicts proves the verdict
// guards: an unhealthy recorded kind, and a mixed evidence set where one ref
// is not immutably bound in the prefix, each qualify nothing while a clean
// verdict does.
func TestPredicateProgressRefusesUnhealthyAndMixedVerdicts(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	evaluator := progressActorRecord(t, db, "evaluator", "agent")
	bound := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	unhealthy := progressVerdict(evaluator, []string{"evidence:qualified"})
	unhealthy.VerdictKind = "outcome_mismatch"
	appendEvent(WorkflowVerdictRecorded, unhealthy)
	mixed := progressVerdict(evaluator, []string{"evidence:qualified", "evidence:unbound"})
	appendEvent(WorkflowVerdictRecorded, mixed)
	if h := read(); h.nonprogressBoundary != bound {
		t.Fatalf("an unhealthy or mixed-evidence verdict qualified: anchor %d, want %d", h.nonprogressBoundary, bound)
	}
	first := appendEvent(WorkflowVerdictRecorded, progressVerdict(evaluator, []string{"evidence:qualified"}))
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("the clean verdict lost credit: anchor %d, want %d", h.nonprogressBoundary, first)
	}
}

// TestPredicateFamilySurvivesUnsatisfiedIntermediate proves the reviewer
// chain: a satisfied obligation's family stays spent through unsatisfied
// intermediate installs. A/X satisfied, superseded to A/Y with no verdict,
// then to B/Y: the qualifying B/Y verdict earns no credit, while a genuinely
// distinct required predicate still earns its own first credit.
func TestPredicateFamilySurvivesUnsatisfiedIntermediate(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	evaluator := progressActorRecord(t, db, "evaluator", "agent")
	progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	first := appendEvent(WorkflowVerdictRecorded, progressVerdict(evaluator, []string{"evidence:qualified"}))
	install := func(version int64, predicates ...workflowContractPredicatePayload) {
		t.Helper()
		contract := workflowContractApprovedPayload{ContractVersion: version, RequiredEvidence: []string{"artifact"}, OutcomePredicates: predicates, DecisionDomains: []string{}}
		appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: version, SuccessorContract: &contract})
	}
	verdict := func(version int64, predicateID string) workflowVerdictRecordedPayload {
		recorded := progressVerdict(evaluator, []string{"evidence:qualified"})
		recorded.ContractVersion, recorded.PredicateID = version, predicateID
		return recorded
	}
	payloadY := mustJSONValue(map[string]any{"kind": "exists", "surface": "changed.go", "subjects": []string{"owner"}})
	install(2, workflowContractPredicatePayload{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: payloadY})
	install(3, workflowContractPredicatePayload{PredicateID: "predicate:two", OutcomeKind: "exists", OutcomePayload: payloadY})
	appendEvent(WorkflowVerdictRecorded, verdict(3, "predicate:two"))
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("a qualifying verdict through unsatisfied intermediates renewed the spent lineage: anchor %d, want %d", h.nonprogressBoundary, first)
	}
	payloadZ := mustJSONValue(map[string]any{"kind": "exists", "surface": "fresh.go", "subjects": []string{"owner"}})
	install(4,
		workflowContractPredicatePayload{PredicateID: "predicate:two", OutcomeKind: "exists", OutcomePayload: payloadY},
		workflowContractPredicatePayload{PredicateID: "predicate:three", OutcomeKind: "exists", OutcomePayload: payloadZ})
	second := appendEvent(WorkflowVerdictRecorded, verdict(4, "predicate:three"))
	if h := read(); h.nonprogressBoundary != second {
		t.Fatalf("a genuinely distinct required predicate lost its first credit: anchor %d, want %d", h.nonprogressBoundary, second)
	}
}

// TestPredicateFamilySpendsAllAliasesOnFirstSatisfaction proves the reverse
// chain with no prior satisfaction: aliases carried across installs before
// any verdict — A/X installed, A/Y installed, B/Y installed, none satisfied —
// grant no progress themselves, make the family's first qualifying verdict
// its only credit, and spend every member, so a later verdict for the
// family's earlier member earns nothing.
func TestPredicateFamilySpendsAllAliasesOnFirstSatisfaction(t *testing.T) {
	db, appendEvent, read := progressHistory(t, false)
	evaluator := progressActorRecord(t, db, "evaluator", "agent")
	bound := progressQualifiedEvidence(t, db, appendEvent, "evidence:qualified")
	payloadX := mustJSONValue(map[string]any{"kind": "exists", "surface": "source.go", "subjects": []string{"owner"}})
	payloadY := mustJSONValue(map[string]any{"kind": "exists", "surface": "changed.go", "subjects": []string{"owner"}})
	install := func(version int64, predicates ...workflowContractPredicatePayload) {
		t.Helper()
		contract := workflowContractApprovedPayload{ContractVersion: version, RequiredEvidence: []string{"artifact"}, OutcomePredicates: predicates, DecisionDomains: []string{}}
		appendEvent(WorkflowContractSuperseded, workflowContractSupersededPayload{NewContractVersion: version, SuccessorContract: &contract})
	}
	install(2, workflowContractPredicatePayload{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: payloadY})
	install(3, workflowContractPredicatePayload{PredicateID: "predicate:two", OutcomeKind: "exists", OutcomePayload: payloadY})
	if h := read(); h.nonprogressBoundary != bound {
		t.Fatalf("a contract install granted progress: anchor %d, want the evidence binding at %d", h.nonprogressBoundary, bound)
	}
	verdictTwo := progressVerdict(evaluator, []string{"evidence:qualified"})
	verdictTwo.ContractVersion, verdictTwo.PredicateID = 3, "predicate:two"
	first := appendEvent(WorkflowVerdictRecorded, verdictTwo)
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("the family's first qualifying verdict lost credit: anchor %d, want %d", h.nonprogressBoundary, first)
	}
	install(4,
		workflowContractPredicatePayload{PredicateID: "predicate:one", OutcomeKind: "exists", OutcomePayload: payloadX},
		workflowContractPredicatePayload{PredicateID: "predicate:two", OutcomeKind: "exists", OutcomePayload: payloadY})
	verdictOne := progressVerdict(evaluator, []string{"evidence:qualified"})
	verdictOne.ContractVersion, verdictOne.PredicateID = 4, "predicate:one"
	appendEvent(WorkflowVerdictRecorded, verdictOne)
	if h := read(); h.nonprogressBoundary != first {
		t.Fatalf("a later verdict for the family's earlier member earned credit: anchor %d, want %d", h.nonprogressBoundary, first)
	}
}

func workflowCorrectionWalkT(t *testing.T, db *sql.DB) *workflowCorrectionHistory {
	t.Helper()
	history, err := workflowCorrectionWalk(context.Background(), db, "work-history", 1<<62, "progress_test")
	if err != nil {
		t.Fatal(err)
	}
	return history
}

func workflowCorrectionWalkPrefix(t *testing.T, db *sql.DB, prefix int64) *workflowCorrectionHistory {
	t.Helper()
	history, err := workflowCorrectionWalk(context.Background(), db, "work-history", prefix, "progress_test")
	if err != nil {
		t.Fatal(err)
	}
	return history
}
