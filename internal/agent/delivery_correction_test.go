package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// The agent contract for the terminal delivery correction: the correction is
// dispatched only behind a consumed core operator approval, the completed
// work stays closed, and the read surface shows the coordinator-asserted
// merge evidence beside the unchanged original. The core never claims it
// verified the merge: the provenance field is the coordinator_asserted
// constant, and no surface it renders speaks of a verification the core did
// not perform.
// proves check:terminal-delivery-correction-agent-contract.

const deliveryCorrectionAgentMergeRef = "https://github.com/Sharper-Flow/concord/pull/1340"

func completedDeliveryAgentFixture(t *testing.T, s *store.Store, grant Authority) (string, int64, int, int64) {
	t.Helper()
	ctx := context.Background()
	if version := seedAgentWorkflow(t, s, grant); version != 4 {
		t.Fatalf("workflow seed version=%d, want 4", version)
	}
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	actorRef, err := store.WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	const assertionEventID = "agent-assertion-work-1"
	assertionPayload, err := json.Marshal(map[string]any{
		"work_id": "work-1", "expected_version": 4, "resulting_version": 5,
		"step_id": "delivery", "action_id": "record_delivery", "attempt_epoch": 1,
		"delivery_artifact": "file:internal/adapter/opencode/run.ts", "delivery_state": "asserted",
		"result_evidence_refs": []string{}, "changed_refs": []string{"work-1"}, "actor_ref": actorRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		assertionEventID, store.WorkflowActionCompleted, string(store.SubjectWorkItem), "work-1", actorRef, fixedTime().Format(time.RFC3339Nano), 2, assertionPayload); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE workflow_instances SET instance_state='completed' WHERE work_id='work-1';
		UPDATE work_items SET lifecycle='completed' WHERE id='work-1';
		DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	var targetSeq int64
	var targetPayloadVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT seq,payload_version FROM domain_events WHERE event_id=?`, assertionEventID).Scan(&targetSeq, &targetPayloadVersion); err != nil {
		t.Fatal(err)
	}
	version := workVersion(t, s, "work-1")
	return assertionEventID, version, targetPayloadVersion, targetSeq
}

func TestDeliveryCorrectionAgentContract(t *testing.T) {
	t.Parallel()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "product_read"})
	targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryAgentFixture(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	input := map[string]any{
		"work_id": "work-1", "expected_version": version,
		"target_event_id": targetEventID, "target_seq": targetSeq, "target_payload_version": targetPayloadVersion,
		"reason":            "the delivery asserted repository paths before the pull request merged",
		"delivery_artifact": deliveryCorrectionAgentMergeRef, "delivery_state": "asserted",
		"idempotency_key": "delivery-correction-1",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}

	// Without an approval the boundary mints a challenge and refuses.
	challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: raw}, env)
	if challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("correction without approval = %+v, want approval_required", challenge.Error)
	}
	challengeRef, ok := challenge.Error.Details["approval_ref"].(string)
	if !ok || challengeRef == "" {
		t.Fatalf("correction challenge has no approval reference: %+v", challenge.Error.Details)
	}
	var correctionsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=? AND subject_id='work-1'`, store.WorkflowDeliveryCorrected).Scan(&correctionsBefore); err != nil {
		t.Fatal(err)
	}
	if correctionsBefore != 0 {
		t.Fatalf("refused correction wrote %d correction events", correctionsBefore)
	}

	// A repository path is not merge evidence: the closed input contract
	// refuses it before any approval challenge is minted, because a
	// repository path cannot carry a merge the core would have to claim it
	// verified.
	repoPathInput := map[string]any{}
	for key, value := range input {
		repoPathInput[key] = value
	}
	repoPathInput["delivery_artifact"] = "file:internal/adapter/opencode/other.ts"
	repoPathInput["idempotency_key"] = "delivery-correction-repo-path"
	repoPathRaw, err := json.Marshal(repoPathInput)
	if err != nil {
		t.Fatal(err)
	}
	rejected, rejectErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: repoPathRaw}, env)
	if rejectErr == nil || !strings.Contains(rejectErr.Error(), "delivery_artifact") {
		t.Fatalf("repository path evidence = (%+v, %v), want an input refusal naming delivery_artifact", rejected, rejectErr)
	}
	var correctionsAfterRefusal int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=? AND subject_id='work-1'`, store.WorkflowDeliveryCorrected).Scan(&correctionsAfterRefusal); err != nil {
		t.Fatal(err)
	}
	if correctionsAfterRefusal != 0 {
		t.Fatalf("refused repository-path correction wrote %d correction events", correctionsAfterRefusal)
	}

	// With the consumed operator approval the correction appends.
	approvedInput := cloneWithApproval(t, input, challengeRef)
	approvedRaw, err := json.Marshal(approvedInput)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"}, "work_ids": []string{"work-1"}, "scope_version": scopeVersion}
	versions := map[string]any{"work": version}
	env.HostApproval = signedHostApproval(privateKey, challengeRef, mutationDigest("concord_work_transition", "correct_delivery", env, approvedRaw), scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: approvedRaw}, env)
	if approved.Outcome != OutcomeOK || approved.Error != nil {
		t.Fatalf("approved correction outcome=%+v error=%+v", approved.Outcome, approved.Error)
	}

	// The completed work stays closed and the log carries one appended
	// correction beside the untouched original.
	var instanceState string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='work-1'`).Scan(&instanceState); err != nil {
		t.Fatal(err)
	}
	if instanceState != "completed" {
		t.Fatalf("correction reopened the instance to %q", instanceState)
	}
	var corrections int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind=? AND subject_id='work-1'`, store.WorkflowDeliveryCorrected).Scan(&corrections); err != nil {
		t.Fatal(err)
	}
	if corrections != 1 {
		t.Fatalf("correction event count=%d, want one appended correction", corrections)
	}
	if got := workVersion(t, s, "work-1"); got != version+2 {
		t.Fatalf("correction version=%d, want %d", got, version+2)
	}

	// The read surface shows the effective merge evidence beside the
	// unchanged original, under coordinator provenance only. The core
	// records the coordinator's evidence and never claims a verification of
	// any forge or merge state.
	projection, err := store.ReadWorkflowProjection(context.Background(), s, store.WorkflowReadRequest{WorkID: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	assertion := projection.DeliveryAssertion
	if assertion == nil || assertion.Correction == nil {
		t.Fatalf("read carries no corrected delivery assertion: %+v", projection.DeliveryAssertion)
	}
	if assertion.EventID != targetEventID || assertion.Artifact != "file:internal/adapter/opencode/run.ts" {
		t.Fatalf("original assertion changed in the read: %+v", assertion)
	}
	if assertion.Correction.Artifact != deliveryCorrectionAgentMergeRef {
		t.Fatalf("effective artifact=%q, want the merge evidence", assertion.Correction.Artifact)
	}
	if assertion.Correction.EvidenceSource != store.DeliveryEvidenceSourceCoordinatorAsserted {
		t.Fatalf("evidence source=%q, want the coordinator-asserted provenance", assertion.Correction.EvidenceSource)
	}

	// The corrected assertion round-trips through the closed workflow_read
	// contract, and the required target_payload_version fails closed when a
	// reader drops it: the read must stay able to name the exact target a
	// correction admission consumes.
	projectionJSON, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var shaped map[string]any
	if err := json.Unmarshal(projectionJSON, &shaped); err != nil {
		t.Fatal(err)
	}
	// The projection carries internal observation fields the published
	// contract does not declare (changes_product_truth, overdue_awaits,
	// await_health, withheld_operator_question, proposal_record,
	// architecture_binding). That divergence predates the delivery
	// correction; this attempt owns delivery_assertion only, so the
	// round-trip covers the published subset.
	for _, internal := range []string{"changes_product_truth", "overdue_awaits", "await_health", "withheld_operator_question", "proposal_record", "architecture_binding"} {
		delete(shaped, internal)
	}
	published, err := json.Marshal(shaped)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("workflow_read", published); err != nil {
		t.Fatalf("published workflow_read subset does not round-trip through the closed contract: %v", err)
	}
	assertionShape, ok := shaped["delivery_assertion"].(map[string]any)
	if !ok {
		t.Fatalf("read shape carries no delivery_assertion object: %+v", shaped["delivery_assertion"])
	}
	delete(assertionShape, "target_payload_version")
	withoutVersion, err := json.Marshal(shaped)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("workflow_read", withoutVersion); err == nil || !strings.Contains(err.Error(), "target_payload_version") {
		t.Fatalf("dropped target_payload_version validated as %v, want a required-field refusal naming it", err)
	}

	// The public history read carries the same effective assertion beside the
	// unchanged original: the page's workflow projection names the asserted
	// artifact, the correction's merge evidence under coordinator provenance,
	// and the target_payload_version a correction admission consumes. The
	// closed work_event_page contract refuses the projection's internal
	// fields, so an ok outcome proves the published subset shape.
	history := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: json.RawMessage(`{"work_id":"work-1","page":{"cursor":null,"limit":20}}`)}, env)
	if history.Outcome != OutcomeOK || history.Error != nil {
		t.Fatalf("history read outcome=%+v error=%+v", history.Outcome, history.Error)
	}
	var page struct {
		Workflow *struct {
			DeliveryAssertion *struct {
				EventID              string `json:"event_id"`
				Artifact             string `json:"artifact"`
				TargetPayloadVersion int    `json:"target_payload_version"`
				Correction           *struct {
					Artifact       string `json:"artifact"`
					EvidenceSource string `json:"evidence_source"`
				} `json:"correction"`
			} `json:"delivery_assertion"`
		} `json:"workflow"`
	}
	if err := json.Unmarshal(history.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.Workflow == nil || page.Workflow.DeliveryAssertion == nil || page.Workflow.DeliveryAssertion.Correction == nil {
		t.Fatalf("history page carries no corrected delivery assertion: %s", history.Result)
	}
	historyAssertion := page.Workflow.DeliveryAssertion
	if historyAssertion.EventID != targetEventID || historyAssertion.Artifact != "file:internal/adapter/opencode/run.ts" {
		t.Fatalf("history page changed the original assertion: %+v", historyAssertion)
	}
	if historyAssertion.TargetPayloadVersion != targetPayloadVersion {
		t.Fatalf("history target_payload_version=%d, want %d", historyAssertion.TargetPayloadVersion, targetPayloadVersion)
	}
	if historyAssertion.Correction.Artifact != deliveryCorrectionAgentMergeRef || historyAssertion.Correction.EvidenceSource != store.DeliveryEvidenceSourceCoordinatorAsserted {
		t.Fatalf("history correction = %+v, want the merge evidence under coordinator provenance", historyAssertion.Correction)
	}
}
