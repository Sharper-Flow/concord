package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Issue #983. An accepted delivery can reach the acceptance checkpoint with a
// contract-required evidence kind still unbound. Confirmation refuses, and the
// declared bind_evidence route refused as well, because recovery admitted only
// an unbound law mandate or an exact obligation tuple. Recovery and acceptance
// now read one outstanding-requirement calculation, so the typed route binds
// what the gate still demands, and refuses everything else.
//
// The corpus scenario WF04 carries an empty law mandate, so these tests also
// cover the empty-mandate contract the earlier guard returned early for.

// wf04OutstandingKind is a contract-required evidence kind the replayed
// history never binds, which is the live shape this repair addresses: an
// accepted delivery whose contract demands a kind no binding satisfies.
const (
	wf04OutstandingKind = "commit"
	wf04RecoveryRef     = "commit:3333333333333333333333333333333333333333333333333333333333333333"
)

type acceptanceRecoveryFixture struct {
	store    *Store
	workID   string
	scenario workflowScenario
}

// newAcceptanceRecoveryFixture replays WF04 without its premise confirmation,
// with the named kinds added to the approved contract's required evidence, and
// holds the instance at acceptance. The replayed history binds none of the
// added kinds, so each one is outstanding at the checkpoint.
func newAcceptanceRecoveryFixture(ctx context.Context, t *testing.T, extraRequiredKinds ...string) acceptanceRecoveryFixture {
	t.Helper()
	corpus := readWorkflowScenarioCorpus(t)
	var scenario workflowScenario
	for _, candidate := range corpus.Scenarios {
		if candidate.ID == "WF04-weaker-delivery" {
			scenario = candidate
			break
		}
	}
	if scenario.ID == "" {
		t.Fatal("WF04 is missing from the corpus")
	}
	registered, err := BuiltinWorkflowDefinitionForRef(scenario.Request.DefinitionPin.Ref)
	if err != nil {
		t.Fatal(err)
	}
	history := make([]workflowCorpusEvent, 0, len(scenario.Setup.EventHistory))
	for _, event := range scenario.Setup.EventHistory {
		if event.Kind == "workflow.premise_confirmed" {
			continue
		}
		if event.Kind == "workflow.contract_approved" && len(extraRequiredKinds) != 0 {
			event.Payload = withRequiredEvidenceKinds(t, event.Payload, extraRequiredKinds)
		}
		history = append(history, event)
	}
	setup := scenario.Setup
	setup.EventHistory = history
	store := openTemp(t)
	if err := replayWorkflowCorpusSetup(ctx, store, setup, registered, scenarioActorRef(scenario), true); err != nil {
		t.Fatal(err)
	}
	workID := scenario.Setup.FixtureRefs.WorkItem
	var step string
	if err := store.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "acceptance" {
		// The corpus replays the acceptance action, so the instance settles at
		// the next step. Hold it at the checkpoint the recovery route governs.
		if err := applyProjectionCorruptionFault(ctx, store, projectionCorruptionFaultInput{WorkID: workID, Target: "workflow_instances", Field: "current_step", Value: "acceptance"}); err != nil {
			t.Fatal(err)
		}
	}
	return acceptanceRecoveryFixture{store: store, workID: workID, scenario: scenario}
}

// withRequiredEvidenceKinds copies one contract payload and appends the added
// kinds to its required evidence, leaving the corpus event untouched.
func withRequiredEvidenceKinds(t *testing.T, payload map[string]any, extra []string) map[string]any {
	t.Helper()
	copied := make(map[string]any, len(payload))
	for key, value := range payload {
		copied[key] = value
	}
	var required []string
	switch declared := copied["required_evidence"].(type) {
	case []any:
		for _, kind := range declared {
			text, ok := kind.(string)
			if !ok {
				t.Fatalf("required_evidence entry %v is not a string", kind)
			}
			required = append(required, text)
		}
	case []string:
		required = append(required, declared...)
	default:
		t.Fatalf("required_evidence has unexpected type %T", copied["required_evidence"])
	}
	copied["required_evidence"] = append(required, extra...)
	return copied
}

func (f acceptanceRecoveryFixture) version(ctx context.Context, t *testing.T) int64 {
	t.Helper()
	version, err := workflowCurrentVersion(ctx, f.store, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func (f acceptanceRecoveryFixture) actors() (WorkflowActor, WorkflowActor) {
	grant := f.scenario.Request.Grant
	invoking := WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: "agent:operator-signed", SessionRef: "session:operator-signed", ActorClass: ActorOperator}
	return invoking, operator
}

// run applies one declared action through the real action layer. Only
// confirm_premise carries the signed operator actor.
func (f acceptanceRecoveryFixture) run(ctx context.Context, actionID, label string, version int64, payload json.RawMessage, evidenceRefs []string) error {
	invoking, operator := f.actors()
	request := WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: version, ActionID: actionID, Payload: payload, EvidenceRefs: evidenceRefs,
		Actor: invoking, OperationID: f.workID + ":" + label, PrincipalRef: operator.PrincipalRef, Tool: "concord_work_transition",
		IdempotencyKey: f.workID + ":" + label, IdempotencyIdentity: f.workID + ":" + label, RequestID: f.workID + ":" + label,
		AcceptedInputsDigest: "sha256:" + strings.Repeat("a", 64), ContractDigest: testManifestDigest,
		Now: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC),
	}
	if actionID == "confirm_premise" {
		request.OperatorActor = &operator
	}
	tx, err := f.store.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := enterFold(ctx, tx); err != nil {
		return err
	}
	if _, actionErr := applyWorkflowActionRawTx(ctx, tx, BuiltinWorkflowRegistry(), request); actionErr != nil {
		_ = leaveFold(ctx, tx)
		return actionErr
	}
	if err := leaveFold(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (f acceptanceRecoveryFixture) bind(ctx context.Context, label string, version int64, kind, reference string) error {
	payload := json.RawMessage(`{"evidence_kind":"` + kind + `","immutable_subject_ref":"` + reference + `"}`)
	return f.run(ctx, "bind_evidence", label, version, payload, []string{reference})
}

func (f acceptanceRecoveryFixture) boundCount(t *testing.T, kind, reference string) int {
	t.Helper()
	var count int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')=? AND json_extract(payload,'$.immutable_subject_ref')=?`, f.workID, WorkflowEvidenceBound, kind, reference).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f acceptanceRecoveryFixture) authority(t *testing.T) (step string, contractVersion int64, verdicts int) {
	t.Helper()
	db := f.store.DatabaseForTesting()
	if err := db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, f.workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT MAX(contract_version) FROM workflow_contracts WHERE work_id=?`, f.workID).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=?`, f.workID, WorkflowVerdictRecorded).Scan(&verdicts); err != nil {
		t.Fatal(err)
	}
	return step, contractVersion, verdicts
}

// requireRecoveryFailure asserts the refusal kind and returns its detail. It
// returns the detail rather than the failure so that callers never hold an
// error value they do not check.
func requireRecoveryFailure(t *testing.T, err error, want FailureKind, context string) string {
	t.Helper()
	if err == nil {
		t.Fatalf("%s passed, want %s", context, want)
	}
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != want {
		t.Fatalf("%s error = %v, want %s", context, err, want)
	}
	return failure.Detail
}

// The whole typed path: an accepted delivery whose contract-required kind was
// never bound refuses confirmation, recovers through the declared
// bind_evidence route, and then confirms. Recovery holds the step and
// preserves the contract and the recorded verdict.
func TestAcceptanceRecoversMissingRequiredEvidenceKind(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t, wf04OutstandingKind)

	beforeStep, beforeContract, beforeVerdicts := f.authority(t)
	confirmErr := f.run(ctx, "confirm_premise", "confirm-missing", f.version(ctx, t), nil, nil)
	detail := requireRecoveryFailure(t, confirmErr, KindMissingEvidence, "confirmation with an unbound required kind")
	if !strings.Contains(detail, wf04OutstandingKind) {
		t.Fatalf("confirmation refusal = %q, want it to name the missing %s kind", detail, wf04OutstandingKind)
	}

	if err := f.bind(ctx, "recover-outstanding", f.version(ctx, t), wf04OutstandingKind, wf04RecoveryRef); err != nil {
		t.Fatalf("recovery binding for the outstanding kind refused: %v", err)
	}
	if got := f.boundCount(t, wf04OutstandingKind, wf04RecoveryRef); got != 1 {
		t.Fatalf("recovered binding count = %d, want 1", got)
	}

	afterStep, afterContract, afterVerdicts := f.authority(t)
	if afterStep != beforeStep {
		t.Fatalf("recovery moved the workflow step %q to %q", beforeStep, afterStep)
	}
	if afterContract != beforeContract {
		t.Fatalf("recovery changed the contract version %d to %d", beforeContract, afterContract)
	}
	if afterVerdicts != beforeVerdicts {
		t.Fatalf("recovery changed the recorded verdict count %d to %d", beforeVerdicts, afterVerdicts)
	}

	if err := f.run(ctx, "confirm_premise", "confirm-recovered", f.version(ctx, t), nil, nil); err != nil {
		t.Fatalf("confirmation after recovery refused: %v", err)
	}
	var confirmed int
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=?`, f.workID, WorkflowPremiseConfirmed).Scan(&confirmed); err != nil {
		t.Fatal(err)
	}
	if confirmed != 1 {
		t.Fatalf("premise confirmation count = %d, want 1", confirmed)
	}
}

// Recovery admits only what the acceptance gate still demands. A kind the
// contract never required, a stale version, and a second bind of a satisfied
// kind all refuse, and none of them writes a binding.
func TestAcceptanceRecoveryRefusesOutsideOutstandingRequirements(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t, wf04OutstandingKind)

	const unrelated = "artifact:unrelated-recovery-subject"
	unrelatedErr := f.bind(ctx, "unrelated-kind", f.version(ctx, t), "artifact", unrelated)
	requireRecoveryFailure(t, unrelatedErr, KindIllegalLifecycleTransition, "recovery binding for an unrequired kind")
	if got := f.boundCount(t, "artifact", unrelated); got != 0 {
		t.Fatalf("refused unrelated binding count = %d, want 0", got)
	}

	staleErr := f.bind(ctx, "stale-version", f.version(ctx, t)-1, wf04OutstandingKind, wf04RecoveryRef)
	requireRecoveryFailure(t, staleErr, KindVersionConflict, "recovery binding at a stale version")
	if got := f.boundCount(t, wf04OutstandingKind, wf04RecoveryRef); got != 0 {
		t.Fatalf("stale-version binding count = %d, want 0", got)
	}

	if err := f.bind(ctx, "recover-outstanding", f.version(ctx, t), wf04OutstandingKind, wf04RecoveryRef); err != nil {
		t.Fatalf("recovery binding for the outstanding kind refused: %v", err)
	}
	repeatErr := f.bind(ctx, "recover-again", f.version(ctx, t), wf04OutstandingKind, wf04RecoveryRef)
	requireRecoveryFailure(t, repeatErr, KindIllegalLifecycleTransition, "second recovery binding of a satisfied kind")
	if got := f.boundCount(t, wf04OutstandingKind, wf04RecoveryRef); got != 1 {
		t.Fatalf("binding count after the refused repeat = %d, want 1", got)
	}
}

// A contract whose kinds are all bound keeps recovery closed: the route is
// not a general reopening of evidence binding past its declared step.
func TestAcceptanceRecoveryClosedWhenNoRequirementOutstanding(t *testing.T) {
	ctx := context.Background()
	f := newAcceptanceRecoveryFixture(ctx, t)
	extraErr := f.bind(ctx, "extra-binding", f.version(ctx, t), "review", wf04RecoveryRef)
	requireRecoveryFailure(t, extraErr, KindIllegalLifecycleTransition, "recovery binding with no outstanding requirement")
	if err := f.run(ctx, "confirm_premise", "confirm-satisfied", f.version(ctx, t), nil, nil); err != nil {
		t.Fatalf("confirmation with every required kind bound refused: %v", err)
	}
}
