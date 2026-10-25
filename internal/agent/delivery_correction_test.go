package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// The agent contract for the terminal delivery correction: the routine
// correction is dispatched without any approval claim and mints no challenge,
// it is attributed to the authenticated coordinator's own workflow actor, and
// it appends one versioned correction event beside the unchanged original.
// The completed work stays closed, a supplied approval claim is refused
// without consuming or minting any approval row, and the read surface shows
// the coordinator-asserted merge evidence beside the unchanged original. The
// core never claims it verified the merge: the provenance field is the
// coordinator_asserted constant, and no surface it renders speaks of a
// verification the core did not perform.
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
	// Seed the current assertion and its exact receipt in the migrated store.
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var targetSeq int64
	if err := tx.QueryRowContext(ctx, `UPDATE store_sequences SET last=last+1 WHERE name='effect' RETURNING last`).Scan(&targetSeq); err != nil {
		t.Fatal(err)
	}
	// Marshal of the fixture map supplies the canonical JSON the receipt hashes.
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(assertionPayload))
	if _, err := tx.ExecContext(ctx, `INSERT INTO effect_receipts(event_id,effect_seq,kind,subject_type,subject_id,actor,payload_version,payload_digest,delivery_assertion)
		VALUES(?,?,'workflow.action_completed','work_item','work-1',?,2,?,1)`, assertionEventID, targetSeq, actorRef, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_delivery_state(work_id,assertion_seq,assertion_id,actor,occurred_at,payload_version,payload)
			VALUES('work-1',?,?,?, ?,2,?);
		UPDATE workflow_instances SET instance_state='completed' WHERE work_id='work-1';
		UPDATE work_items SET lifecycle='completed',effect_seq=? WHERE id='work-1';
		DELETE FROM write_guard`, targetSeq, assertionEventID, actorRef, fixedTime().Format(time.RFC3339Nano), string(assertionPayload), targetSeq); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var targetPayloadVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT effect_seq,payload_version FROM effect_receipts WHERE event_id=?`, assertionEventID).Scan(&targetSeq, &targetPayloadVersion); err != nil {
		t.Fatal(err)
	}
	version := workVersion(t, s, "work-1")
	return assertionEventID, version, targetPayloadVersion, targetSeq
}

// agentDeliveryCorrectionEvents counts the recorded delivery corrections on
// the fixture work.
func agentDeliveryCorrectionEvents(t *testing.T, s *store.Store) int {
	t.Helper()
	var corrections int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=? AND subject_id='work-1'`, store.WorkflowDeliveryCorrected).Scan(&corrections); err != nil {
		t.Fatal(err)
	}
	return corrections
}

// agentApprovalRowCounts returns the challenge and approval rows the boundary
// and the approval boundary hold.
func agentApprovalRowCounts(t *testing.T, s *store.Store) (int, int) {
	t.Helper()
	var challenges, approvals int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_approval_challenges`).Scan(&challenges); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM agent_approvals`).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	return challenges, approvals
}

// agentCorrectionDispatchInput renders the closed correction input targeting
// the exact recorded assertion; mutate adjusts one field for a refusal case.
func agentCorrectionDispatchInput(t *testing.T, s *store.Store, targetEventID string, targetSeq int64, targetPayloadVersion int, idempotencyKey string, mutate func(map[string]any)) []byte {
	t.Helper()
	input := map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"),
		"target_event_id": targetEventID, "target_seq": targetSeq, "target_payload_version": targetPayloadVersion,
		"reason":            "the delivery asserted repository paths before the pull request merged",
		"delivery_artifact": deliveryCorrectionAgentMergeRef, "delivery_state": "asserted",
		"idempotency_key": idempotencyKey,
	}
	if mutate != nil {
		mutate(input)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// freshCoordinatorSessionGrant authorizes a second, never-before-recorded
// coordinator session through the real authorization boundary — the same
// Service.Authorize shape issue31EvaluatorGrant uses — so a correction can
// meet an authenticated caller whose workflow actor the fixture has not
// recorded. No authority row is fabricated anywhere.
func freshCoordinatorSessionGrant(t *testing.T, service *Service) Authority {
	t.Helper()
	grant, err := service.Authorize(context.Background(), Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-coordinator-fresh", AgentRef: "agent-other", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: "work_transition", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	return grant
}

func TestDeliveryCorrectionAgentContract(t *testing.T) {
	t.Parallel()

	t.Run("the unsigned correction appends once without any approval claim", func(t *testing.T) {
		t.Parallel()
		s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "product_read"})
		targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryAgentFixture(t, s, grant)
		scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
		if err != nil {
			t.Fatal(err)
		}
		env := mutationEnvelope(grant, scopeVersion)
		correctionInput := func(idempotencyKey string, mutate func(map[string]any)) []byte {
			return agentCorrectionDispatchInput(t, s, targetEventID, targetSeq, targetPayloadVersion, idempotencyKey, mutate)
		}

		// The original assertion bytes are captured before any effect, so a
		// writer that rewrote the assertion could not escape the comparison.
		var originalPayload string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_delivery_state WHERE assertion_id=?`, targetEventID).Scan(&originalPayload); err != nil {
			t.Fatal(err)
		}
		// The clean fixture holds no challenge or approval row before the
		// unsigned effect, and the routine path mints none with it.
		if challenges, approvals := agentApprovalRowCounts(t, s); challenges != 0 || approvals != 0 {
			t.Fatalf("clean fixture holds %d challenge and %d approval rows", challenges, approvals)
		}

		// A repository path is not merge evidence: the closed input contract
		// refuses it, because a repository path cannot carry a merge the core
		// would have to claim it verified.
		repoPathRaw := correctionInput("delivery-correction-repo-path", func(input map[string]any) {
			input["delivery_artifact"] = "file:internal/adapter/opencode/other.ts"
		})
		rejected, rejectErr := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: repoPathRaw}, env)
		assertInputCorrection(t, rejected, rejectErr, "delivery_artifact")
		if corrections := agentDeliveryCorrectionEvents(t, s); corrections != 0 {
			t.Fatalf("refused corrections wrote %d correction events", corrections)
		}

		// The unsigned correction succeeds: the routine path needs no
		// approval claim and mints no challenge.
		unsignedRaw := correctionInput("delivery-correction-1", nil)
		approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: unsignedRaw}, env)
		if approved.Outcome != OutcomeOK || approved.Error != nil {
			t.Fatalf("unsigned correction outcome=%+v error=%+v", approved.Outcome, approved.Error)
		}
		if challenges, approvals := agentApprovalRowCounts(t, s); challenges != 0 || approvals != 0 {
			t.Fatalf("unsigned correction left %d challenge and %d approval rows", challenges, approvals)
		}

		// The correction appends exactly one versioned event attributed to
		// the authenticated coordinator's own workflow actor, with no
		// approval claim and no operator substitute.
		grantActor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
		grantActorRef, err := store.WorkflowActorRef(grantActor)
		if err != nil {
			t.Fatal(err)
		}
		var eventActor string
		var payloadVersion int
		var payloadApprovalRef string
		if err := s.DatabaseForTesting().QueryRow(`SELECT r.actor,r.payload_version,COALESCE(json_extract(d.correction_payload,'$.approval_ref'),'') FROM workflow_delivery_state d JOIN effect_receipts r ON r.event_id=d.correction_id WHERE r.kind=? AND d.work_id='work-1'`, store.WorkflowDeliveryCorrected).Scan(&eventActor, &payloadVersion, &payloadApprovalRef); err != nil {
			t.Fatal(err)
		}
		if eventActor != grantActorRef {
			t.Fatalf("correction actor = %q, want the authenticated coordinator %q", eventActor, grantActorRef)
		}
		if payloadVersion != 2 || payloadApprovalRef != "" {
			t.Fatalf("correction event payload version %d approval claim %q, want version 2 with no claim", payloadVersion, payloadApprovalRef)
		}
		var operatorActors int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors a JOIN effect_receipts r ON r.actor=a.actor_ref WHERE r.subject_id='work-1' AND r.kind=? AND a.actor_class='operator'`, store.WorkflowActorRecorded).Scan(&operatorActors); err != nil {
			t.Fatal(err)
		}
		if operatorActors != 0 {
			t.Fatalf("correction recorded %d operator substitute actor events", operatorActors)
		}

		// The completed work stays closed, the log carries exactly one
		// appended correction, and the work advanced one version — the
		// correction's own — with the result's changed ref naming the actual
		// resulting version.
		var instanceState, lifecycle string
		if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='work-1'`).Scan(&instanceState); err != nil {
			t.Fatal(err)
		}
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id='work-1'`).Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		if instanceState != "completed" || lifecycle != "completed" {
			t.Fatalf("correction moved the closed work to instance %q lifecycle %q", instanceState, lifecycle)
		}
		if got := workVersion(t, s, "work-1"); got != version+1 {
			t.Fatalf("correction version = %d, want the single correction advance %d", got, version+1)
		}
		if approved.ChangedRefs == nil || len(*approved.ChangedRefs) != 1 || (*approved.ChangedRefs)[0].ID != "work-1" {
			t.Fatalf("correction changed refs = %+v, want the corrected work alone", approved.ChangedRefs)
		}
		if changedVersion, parseErr := strconv.ParseInt((*approved.ChangedRefs)[0].Version, 10, 64); parseErr != nil || changedVersion != version+1 {
			t.Fatalf("changed ref version = %s, want the actual resulting version %d", (*approved.ChangedRefs)[0].Version, version+1)
		}

		// The original assertion bytes are untouched.
		var replayedPayload string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_delivery_state WHERE assertion_id=?`, targetEventID).Scan(&replayedPayload); err != nil {
			t.Fatal(err)
		}
		if originalPayload != replayedPayload {
			t.Fatal("the original assertion event payload was rewritten")
		}

		// The idempotent replay of the same request returns a result and
		// appends nothing.
		replayedDispatch := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: unsignedRaw}, env)
		if replayedDispatch.Outcome != OutcomeOK || replayedDispatch.Error != nil {
			t.Fatalf("idempotent replay outcome=%+v error=%+v", replayedDispatch.Outcome, replayedDispatch.Error)
		}
		if corrections := agentDeliveryCorrectionEvents(t, s); corrections != 1 {
			t.Fatalf("replayed correction wrote %d correction events, want one", corrections)
		}

		// The read surface shows the effective merge evidence beside the
		// unchanged original, under coordinator provenance only. The core
		// records the coordinator's evidence and never claims a verification
		// of any forge or merge state.
		workflowRead, err := s.QueryQ7(context.Background(), store.Q7Request{Work: "work-1"})
		if err != nil {
			t.Fatal(err)
		}
		projection := workflowRead.Workflow
		if projection == nil {
			t.Fatal("Q7 omitted the recorded workflow")
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
		if assertion.Correction.ApprovalRef != "" {
			t.Fatalf("correction read carries an approval claim %q", assertion.Correction.ApprovalRef)
		}

		// The corrected assertion round-trips through the closed workflow_read
		// contract, and the required target_payload_version and effective_artifact
		// fail closed when a reader drops them: the read must stay able to name
		// the exact target a correction admission consumes and the artifact the
		// correction overlay selects.
		published, err := publishedWorkflowRead(projection)
		if err != nil {
			t.Fatal(err)
		}
		publishedJSON, err := json.Marshal(published)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePayloadSchema("workflow_read", publishedJSON); err != nil {
			t.Fatalf("published workflow_read subset does not round-trip through the closed contract: %v", err)
		}
		assertionShape, ok := published["delivery_assertion"].(map[string]any)
		if !ok {
			t.Fatalf("read shape carries no delivery_assertion object: %+v", published["delivery_assertion"])
		}
		if assertionShape["effective_artifact"] != deliveryCorrectionAgentMergeRef {
			t.Fatalf("effective_artifact = %v, want the merge evidence", assertionShape["effective_artifact"])
		}
		delete(assertionShape, "target_payload_version")
		withoutVersion, err := json.Marshal(published)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePayloadSchema("workflow_read", withoutVersion); err == nil || !strings.Contains(err.Error(), "target_payload_version") {
			t.Fatalf("dropped target_payload_version validated as %v, want a required-field refusal naming it", err)
		}
		delete(assertionShape, "effective_artifact")
		assertionShape["target_payload_version"] = targetPayloadVersion
		withoutEffective, err := json.Marshal(published)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePayloadSchema("workflow_read", withoutEffective); err == nil || !strings.Contains(err.Error(), "effective_artifact") {
			t.Fatalf("dropped effective_artifact validated as %v, want a required-field refusal naming it", err)
		}

		// The public history read carries the same effective assertion beside
		// the unchanged original: the page's workflow projection names the
		// asserted artifact, the correction's merge evidence under
		// coordinator provenance, and the target_payload_version a correction
		// admission consumes. The closed work_event_page contract refuses the
		// projection's internal fields, so an ok outcome proves the published
		// subset shape.
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
	})

	// A fresh authenticated coordinator session — a grant whose workflow
	// actor this workflow has never recorded — corrects once: the core
	// records the session's real tuple through the same owner normal action
	// entry uses, then appends the correction under that actor. The work
	// advances two versions in one effect (the actor prefix plus the
	// correction), the response names the actual resulting version, and the
	// completed work stays closed with no challenge and no approval
	// consumption. The grant is genuine: it comes from the real
	// authorization boundary the Service exposes, never from a fabricated
	// authority row.
	t.Run("a fresh coordinator session records its actor and corrects once", func(t *testing.T) {
		t.Parallel()
		s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
		targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryAgentFixture(t, s, grant)
		freshGrant := freshCoordinatorSessionGrant(t, service)
		freshActorRef, err := store.WorkflowActorRef(store.WorkflowActor{PrincipalRef: freshGrant.PrincipalRef, ClientRef: freshGrant.ClientRef, AgentRef: freshGrant.AgentRef, SessionRef: freshGrant.SessionRef, ActorClass: store.ActorAgent})
		if err != nil {
			t.Fatal(err)
		}
		// The fresh session is authenticated and holds the capability, but
		// no workflow actor row records its tuple: the correction itself
		// must record it through the shared actor-prefix owner.
		var actorRows int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_ref=?`, freshActorRef).Scan(&actorRows); err != nil {
			t.Fatal(err)
		}
		if actorRows != 0 {
			t.Fatalf("the fresh session already holds %d workflow actor rows", actorRows)
		}
		scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
		if err != nil {
			t.Fatal(err)
		}
		env := mutationEnvelope(freshGrant, scopeVersion)

		// The original assertion bytes are captured before any effect, so a
		// writer that rewrote the assertion could not escape the comparison.
		var originalPayload string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_delivery_state WHERE assertion_id=?`, targetEventID).Scan(&originalPayload); err != nil {
			t.Fatal(err)
		}

		raw := agentCorrectionDispatchInput(t, s, targetEventID, targetSeq, targetPayloadVersion, "delivery-correction-fresh-session", nil)
		corrected := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: raw}, env)
		if corrected.Outcome != OutcomeOK || corrected.Error != nil {
			t.Fatalf("fresh-session correction outcome=%+v error=%+v, want the unsigned success with no challenge", corrected.Outcome, corrected.Error)
		}
		if challenges, approvals := agentApprovalRowCounts(t, s); challenges != 0 || approvals != 0 {
			t.Fatalf("fresh-session correction left %d challenge and %d approval rows", challenges, approvals)
		}

		// Exactly one actor-prefix event records the fresh session's exact
		// authenticated tuple, and the recorded actor class stays agent: no
		// operator substitute, no copied projection row.
		var prefixCount int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind=? AND subject_id='work-1' AND actor=?`, store.WorkflowActorRecorded, freshActorRef).Scan(&prefixCount); err != nil {
			t.Fatal(err)
		}
		if prefixCount != 1 {
			t.Fatalf("the fresh session recorded %d actor-prefix events, want exactly one", prefixCount)
		}
		var prefixPrincipal, prefixClient, prefixAgent, prefixSession, prefixClass string
		if err := s.DatabaseForTesting().QueryRow(`SELECT a.principal_ref,a.client_ref,a.agent_ref,a.session_ref,a.actor_class FROM workflow_actors a JOIN effect_receipts r ON r.actor=a.actor_ref WHERE r.kind=? AND r.subject_id='work-1' AND a.actor_ref=?`, store.WorkflowActorRecorded, freshActorRef).Scan(&prefixPrincipal, &prefixClient, &prefixAgent, &prefixSession, &prefixClass); err != nil {
			t.Fatal(err)
		}
		if prefixPrincipal != freshGrant.PrincipalRef || prefixClient != freshGrant.ClientRef || prefixAgent != freshGrant.AgentRef || prefixSession != freshGrant.SessionRef || prefixClass != string(store.ActorAgent) {
			t.Fatalf("actor prefix tuple = (%s,%s,%s,%s,%s), want the authenticated session (%s,%s,%s,%s,agent)", prefixPrincipal, prefixClient, prefixAgent, prefixSession, prefixClass, freshGrant.PrincipalRef, freshGrant.ClientRef, freshGrant.AgentRef, freshGrant.SessionRef)
		}
		var recordedClass string
		if err := s.DatabaseForTesting().QueryRow(`SELECT actor_class FROM workflow_actors WHERE actor_ref=?`, freshActorRef).Scan(&recordedClass); err != nil {
			t.Fatal(err)
		}
		if recordedClass != string(store.ActorAgent) {
			t.Fatalf("recorded fresh actor class = %q, want agent", recordedClass)
		}

		// The correction itself is one unsigned v2 event attributed to the
		// fresh session's own workflow actor.
		var eventActor string
		var payloadVersion int
		var payloadApprovalRef string
		if err := s.DatabaseForTesting().QueryRow(`SELECT r.actor,r.payload_version,COALESCE(json_extract(d.correction_payload,'$.approval_ref'),'') FROM workflow_delivery_state d JOIN effect_receipts r ON r.event_id=d.correction_id WHERE r.kind=? AND d.work_id='work-1'`, store.WorkflowDeliveryCorrected).Scan(&eventActor, &payloadVersion, &payloadApprovalRef); err != nil {
			t.Fatal(err)
		}
		if eventActor != freshActorRef {
			t.Fatalf("correction actor = %q, want the fresh session's own actor %q", eventActor, freshActorRef)
		}
		if payloadVersion != 2 || payloadApprovalRef != "" {
			t.Fatalf("correction event payload version %d approval claim %q, want version 2 with no claim", payloadVersion, payloadApprovalRef)
		}

		// The work advanced two versions — the actor prefix plus the
		// correction — and the response names the actual resulting version.
		if got := workVersion(t, s, "work-1"); got != version+2 {
			t.Fatalf("fresh-session correction version = %d, want the prefix plus correction advance %d", got, version+2)
		}
		if corrected.ChangedRefs == nil || len(*corrected.ChangedRefs) != 1 || (*corrected.ChangedRefs)[0].ID != "work-1" {
			t.Fatalf("fresh-session correction changed refs = %+v, want the corrected work alone", corrected.ChangedRefs)
		}
		if changedVersion, parseErr := strconv.ParseInt((*corrected.ChangedRefs)[0].Version, 10, 64); parseErr != nil || changedVersion != version+2 {
			t.Fatalf("changed ref version = %s, want the actual resulting version %d", (*corrected.ChangedRefs)[0].Version, version+2)
		}

		// The completed work stays closed and the original assertion is
		// untouched.
		var instanceState string
		if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='work-1'`).Scan(&instanceState); err != nil {
			t.Fatal(err)
		}
		var lifecycle string
		if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id='work-1'`).Scan(&lifecycle); err != nil {
			t.Fatal(err)
		}
		if instanceState != "completed" || lifecycle != "completed" {
			t.Fatalf("correction moved the closed work to instance %q lifecycle %q", instanceState, lifecycle)
		}
		var replayedPayload string
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_delivery_state WHERE assertion_id=?`, targetEventID).Scan(&replayedPayload); err != nil {
			t.Fatal(err)
		}
		if originalPayload != replayedPayload {
			t.Fatal("the original assertion event payload was rewritten")
		}
	})

	// Each target race runs on its own fixture, so a refusal case can neither
	// contaminate the no-challenge contract above nor depend on its ordering.
	for _, race := range []struct {
		name   string
		mutate func(input map[string]any, targetSeq int64, targetPayloadVersion int)
	}{
		{name: "wrong target sequence", mutate: func(input map[string]any, targetSeq int64, _ int) {
			input["target_seq"] = targetSeq + 7
		}},
		{name: "wrong target payload version", mutate: func(input map[string]any, _ int64, targetPayloadVersion int) {
			input["target_payload_version"] = targetPayloadVersion + 1
		}},
	} {
		t.Run(race.name, func(t *testing.T) {
			t.Parallel()
			s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition", "product_read"})
			targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryAgentFixture(t, s, grant)
			scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
			if err != nil {
				t.Fatal(err)
			}
			env := mutationEnvelope(grant, scopeVersion)
			raw := agentCorrectionDispatchInput(t, s, targetEventID, targetSeq, targetPayloadVersion, "delivery-correction-"+strings.ReplaceAll(race.name, " ", "-"), func(input map[string]any) {
				race.mutate(input, targetSeq, targetPayloadVersion)
			})
			refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: raw}, env)
			if refused.Error == nil || refused.Error.Kind != "invalid_input" || !strings.Contains(refused.Error.Message, "does not match the recorded assertion") {
				t.Fatalf("%s = %+v, want invalid_input naming the recorded assertion", race.name, refused.Error)
			}
			if challenges, approvals := agentApprovalRowCounts(t, s); challenges != 0 || approvals != 0 {
				t.Fatalf("refused %s left %d challenge and %d approval rows", race.name, challenges, approvals)
			}
			if corrections := agentDeliveryCorrectionEvents(t, s); corrections != 0 {
				t.Fatalf("refused %s wrote %d correction events", race.name, corrections)
			}
			if lifecycle, currentVersion := readWorkFromStore(t, s, "work-1"); lifecycle != "completed" || currentVersion != version {
				t.Fatalf("refused %s moved the closed work to %q at %d", race.name, lifecycle, currentVersion)
			}
		})
	}

	t.Run("a supplied approval claim refuses without consumption or effects", func(t *testing.T) {
		t.Parallel()
		suppliedStore, suppliedService, suppliedGrant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
		suppliedTarget, suppliedVersion, suppliedPayloadVersion, suppliedSeq := completedDeliveryAgentFixture(t, suppliedStore, suppliedGrant)
		suppliedScopeVersion, _, err := suppliedStore.ScopeVersion(context.Background(), "project-1")
		if err != nil {
			t.Fatal(err)
		}
		suppliedEnv := mutationEnvelope(suppliedGrant, suppliedScopeVersion)
		suppliedBase, err := json.Marshal(map[string]any{
			"work_id": "work-1", "expected_version": suppliedVersion,
			"target_event_id": suppliedTarget, "target_seq": suppliedSeq, "target_payload_version": suppliedPayloadVersion,
			"reason":            "the delivery asserted repository paths before the pull request merged",
			"delivery_artifact": deliveryCorrectionAgentMergeRef, "delivery_state": "asserted",
			"idempotency_key": "delivery-correction-supplied",
		})
		if err != nil {
			t.Fatal(err)
		}
		suppliedInput, err := injectApproval(suppliedBase, "challenge:supplied-approval")
		if err != nil {
			t.Fatal(err)
		}
		supplied := dispatchMutation(t, suppliedStore, suppliedService, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: suppliedInput}, suppliedEnv)
		if supplied.Outcome != OutcomeError || supplied.Error == nil || supplied.Error.Kind != "invalid_input" || !strings.Contains(supplied.Error.Message, "approval") {
			t.Fatalf("supplied approval input outcome=%+v error=%+v, want invalid_input rejecting the approval claim", supplied.Outcome, supplied.Error)
		}
		if challenges, approvals := agentApprovalRowCounts(t, suppliedStore); challenges != 0 || approvals != 0 {
			t.Fatalf("refused supplied claim left %d challenge and %d approval rows", challenges, approvals)
		}
		if corrections := agentDeliveryCorrectionEvents(t, suppliedStore); corrections != 0 {
			t.Fatalf("refused supplied claim wrote %d correction events", corrections)
		}
		if got := workVersion(t, suppliedStore, "work-1"); got != suppliedVersion {
			t.Fatalf("refused supplied claim moved version to %d", got)
		}
	})
}

// correctionApprovedDispatch re-dispatches the correction input with the
// approval attached and a HostApprovalAssertion bound to the caller-supplied
// digest, mirroring the overlap route's approved retry. At this Service layer
// the helper constructs the structurally accepted assertion; it performs no
// signature of its own.
func correctionApprovedDispatch(t *testing.T, s *store.Store, service *Service, privateKey ed25519.PrivateKey, env CallEnvelope, approvalRef, digest string, scope, versions map[string]any, input []byte) Envelope {
	t.Helper()
	approvedEnv := env
	approvedEnv.HostApproval = signedHostApproval(privateKey, approvalRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(approvalRef))
	return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: input}, approvedEnv)
}

// TestDeliveryCorrectionCrossProductBoundarySettlesThroughAConsumedHostApproval:
// the correction's derived authorized Product scope crosses the selected
// Product through the shared Project's second membership while the completed
// work keeps its single footprint Product. The first dispatch returns the
// typed challenge, and the host-approved retry consumes exactly one one-use
// approval, appends one correction under the authenticated coordinator's own
// workflow actor, and leaves the original assertion, the completed state, the
// seeded actor population, and the recorded binding untouched.
func TestDeliveryCorrectionCrossProductBoundarySettlesThroughAConsumedHostApproval(t *testing.T) {
	t.Parallel()
	// product_read authorizes the read-surface assertions below; the
	// correction boundary itself needs work_transition and cross_scope only.
	s, service, grant, privateKey := crossProductDispatchFixture(t, []Capability{"work_transition", "cross_scope", "product_read"})
	targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryAgentFixture(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	// The original assertion bytes are captured before any effect, so a
	// writer that rewrote the assertion could not escape the comparison.
	var originalPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_delivery_state WHERE assertion_id=?`, targetEventID).Scan(&originalPayload); err != nil {
		t.Fatal(err)
	}
	// The workflow seed already records the caller's actor tuple, so the
	// seeded actor event population is the baseline the approved correction
	// must leave unchanged: no fresh recording, no operator substitute.
	var baselineActors int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id='work-1' AND kind=?`, store.WorkflowActorRecorded).Scan(&baselineActors); err != nil {
		t.Fatal(err)
	}

	input := agentCorrectionDispatchInput(t, s, targetEventID, targetSeq, targetPayloadVersion, "delivery-correction-cross-product", nil)
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: input}, env)
	if first.Error == nil || first.Error.Kind != "approval_required" {
		t.Fatalf("cross-Product correction without approval outcome=%s error=%+v, want the typed challenge", first.Outcome, first.Error)
	}
	challengeRef, ok := first.Error.Details["approval_ref"].(string)
	if !ok || challengeRef == "" {
		t.Fatalf("cross-Product correction challenge carries no approval ref: %+v", first.Error.Details)
	}
	firstChallenges, firstApprovals := agentApprovalRowCounts(t, s)
	if firstChallenges != 1 || firstApprovals != 0 {
		t.Fatalf("the cross-Product challenge left %d challenge and %d approval rows, want exactly the one unconsumed challenge", firstChallenges, firstApprovals)
	}

	// The host-approved retry settles the cross-Product consent the tail
	// derives: one consumed one-use approval, no other authority row.
	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1", "product-2"}, "project_ids": []string{"project-1"}, "scope_version": scopeVersion, "work_ids": []string{"work-1"}}
	versions := map[string]any{"work": version}
	digest := mutationDigest("concord_work_transition", "correct_delivery", env, withApproval)
	approved := correctionApprovedDispatch(t, s, service, privateKey, env, challengeRef, digest, scope, versions, withApproval)
	if approved.Outcome != OutcomeOK || approved.Error != nil {
		t.Fatalf("approved cross-Product correction outcome=%s error=%+v, want the appended correction", approved.Outcome, approved.Error)
	}
	secondChallenges, secondApprovals := agentApprovalRowCounts(t, s)
	if secondApprovals != 1 {
		t.Fatalf("the approved cross-Product correction left %d challenge and %d approval rows, want the single consumed approval", secondChallenges, secondApprovals)
	}
	if used := countRows(t, s.DatabaseForTesting(), `SELECT used_count FROM agent_approvals`); used != 1 {
		t.Fatalf("cross-Product correction approval used_count=%d, want the consumed one-use row", used)
	}
	var approvalScopeJSON string
	if err := s.DatabaseForTesting().QueryRow(`SELECT scope_json FROM agent_approvals`).Scan(&approvalScopeJSON); err != nil {
		t.Fatal(err)
	}
	expectedScope, marshalErr := json.Marshal(scope)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if approvalScopeJSON != string(expectedScope) {
		t.Fatalf("consumed approval scope %s, want the exact bounded scope %s with no new host scope fields", approvalScopeJSON, expectedScope)
	}

	// The correction appends one versioned event attributed to the
	// authenticated coordinator's own workflow actor, never to the approval
	// authority and never to an operator substitute.
	grantActorRef, err := store.WorkflowActorRef(store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	var eventActor string
	var payloadVersion int
	var payloadApprovalRef string
	if err := s.DatabaseForTesting().QueryRow(`SELECT r.actor,r.payload_version,COALESCE(json_extract(d.correction_payload,'$.approval_ref'),'') FROM workflow_delivery_state d JOIN effect_receipts r ON r.event_id=d.correction_id WHERE r.kind=? AND d.work_id='work-1'`, store.WorkflowDeliveryCorrected).Scan(&eventActor, &payloadVersion, &payloadApprovalRef); err != nil {
		t.Fatal(err)
	}
	if eventActor != grantActorRef {
		t.Fatalf("correction actor = %q, want the authenticated coordinator %q", eventActor, grantActorRef)
	}
	if payloadVersion != 2 || payloadApprovalRef != "" {
		t.Fatalf("correction event payload version %d approval claim %q, want version 2 with no claim", payloadVersion, payloadApprovalRef)
	}
	var correctedActors int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id='work-1' AND kind=?`, store.WorkflowActorRecorded).Scan(&correctedActors); err != nil {
		t.Fatal(err)
	}
	if correctedActors != baselineActors {
		t.Fatalf("the approved correction moved the actor population from %d to %d, want the seeded tuple left unchanged with no operator substitute", baselineActors, correctedActors)
	}

	// The completed work stays closed, the log carries exactly one appended
	// correction, and the work advanced one version with the result's changed
	// ref naming the actual resulting version.
	var instanceState, lifecycle string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id='work-1'`).Scan(&instanceState); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle FROM work_items WHERE id='work-1'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if instanceState != "completed" || lifecycle != "completed" {
		t.Fatalf("correction moved the closed work to instance %q lifecycle %q", instanceState, lifecycle)
	}
	if corrections := agentDeliveryCorrectionEvents(t, s); corrections != 1 {
		t.Fatalf("the approved correction wrote %d correction events, want one", corrections)
	}
	if got := workVersion(t, s, "work-1"); got != version+1 {
		t.Fatalf("cross-Product correction version = %d, want the single correction advance %d", got, version+1)
	}
	if approved.ChangedRefs == nil || len(*approved.ChangedRefs) != 1 || (*approved.ChangedRefs)[0].ID != "work-1" {
		t.Fatalf("cross-Product correction changed refs = %+v, want the corrected work alone", approved.ChangedRefs)
	}
	if changedVersion, parseErr := strconv.ParseInt((*approved.ChangedRefs)[0].Version, 10, 64); parseErr != nil || changedVersion != version+1 {
		t.Fatalf("changed ref version = %s, want the actual resulting version %d", (*approved.ChangedRefs)[0].Version, version+1)
	}

	// The original assertion bytes are untouched.
	var replayedPayload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM workflow_delivery_state WHERE assertion_id=?`, targetEventID).Scan(&replayedPayload); err != nil {
		t.Fatal(err)
	}
	if originalPayload != replayedPayload {
		t.Fatal("the original assertion event payload was rewritten")
	}

	// The durable record binds the derived cross-Product set under the
	// operation's durable commit.
	var snapshot string
	if err := s.DatabaseForTesting().QueryRow(`SELECT authorized_scope_snapshot FROM idempotency_records WHERE idempotency_key=?`, "delivery-correction-cross-product").Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, `"durable_commit":true`) {
		t.Fatalf("cross-Product correction committed a non-durable snapshot: %s", snapshot)
	}
	if !strings.Contains(snapshot, `"product_ids":["product-1","product-2"]`) {
		t.Fatalf("cross-Product correction snapshot lost the derived Product set: %s", snapshot)
	}

	// The read surface shows the effective merge evidence beside the
	// unchanged original, under coordinator provenance only. The core
	// records the coordinator's evidence and never claims a verification of
	// any forge or merge state.
	workflowRead, err := s.QueryQ7(context.Background(), store.Q7Request{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	projection := workflowRead.Workflow
	if projection == nil {
		t.Fatal("Q7 omitted the recorded workflow")
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
	// contract, and the required target_payload_version and effective_artifact
	// fail closed when a reader drops them: the read must stay able to name
	// the exact target a correction admission consumes and the artifact the
	// correction overlay selects.
	published, err := publishedWorkflowRead(projection)
	if err != nil {
		t.Fatal(err)
	}
	publishedJSON, err := json.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("workflow_read", publishedJSON); err != nil {
		t.Fatalf("published workflow_read subset does not round-trip through the closed contract: %v", err)
	}
	assertionShape, ok := published["delivery_assertion"].(map[string]any)
	if !ok {
		t.Fatalf("read shape carries no delivery_assertion object: %+v", published["delivery_assertion"])
	}
	if assertionShape["effective_artifact"] != deliveryCorrectionAgentMergeRef {
		t.Fatalf("effective_artifact = %v, want the merge evidence", assertionShape["effective_artifact"])
	}
	delete(assertionShape, "target_payload_version")
	withoutVersion, err := json.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("workflow_read", withoutVersion); err == nil || !strings.Contains(err.Error(), "target_payload_version") {
		t.Fatalf("dropped target_payload_version validated as %v, want a required-field refusal naming it", err)
	}
	delete(assertionShape, "effective_artifact")
	assertionShape["target_payload_version"] = targetPayloadVersion
	withoutEffective, err := json.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayloadSchema("workflow_read", withoutEffective); err == nil || !strings.Contains(err.Error(), "effective_artifact") {
		t.Fatalf("dropped effective_artifact validated as %v, want a required-field refusal naming it", err)
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

// TestDeliveryCorrectionCachedReplayPreservesOriginalResultWithoutNewEffects:
// a cached same-key correction replay that adds an unneeded approval claim
// refuses with the same invalid_input refusal the fresh correction call
// returns. The canonical mutation digest excludes the approval block by
// design, so the claim-bearing replay carries the stored digest of the
// unsigned success, and the routine correction surface owns that refusal at
// every replay owner. The refused replay records no new durable effect: no
// correction, no domain event, no actor prefix, no version move, no
// approval row consumed or minted, no idempotency row added, and no receipt
// touch. The unchanged unsigned replay stays the cached success: the ok
// outcome, the replayed marker, and the exact original result payload and
// changed refs.
func TestDeliveryCorrectionCachedReplayPreservesOriginalResultWithoutNewEffects(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"work_transition"})
	targetEventID, version, targetPayloadVersion, targetSeq := completedDeliveryAgentFixture(t, s, grant)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	unsignedRaw := agentCorrectionDispatchInput(t, s, targetEventID, targetSeq, targetPayloadVersion, "delivery-correction-cached-replay", nil)
	unsigned := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: unsignedRaw}, env)
	if unsigned.Outcome != OutcomeOK || unsigned.Error != nil {
		t.Fatalf("unsigned correction outcome=%s error=%+v, want the routine success", unsigned.Outcome, unsigned.Error)
	}
	// The baseline populations the claim replay is measured against: one
	// appended correction on the closed work, no challenge, no approval row.
	baselineCorrections := agentDeliveryCorrectionEvents(t, s)
	if baselineCorrections != 1 {
		t.Fatalf("the unsigned correction left %d correction events, want one", baselineCorrections)
	}
	baselineEvents := agentCurrentEffects(t, s)
	var baselineActors int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id='work-1' AND kind=?`, store.WorkflowActorRecorded).Scan(&baselineActors); err != nil {
		t.Fatal(err)
	}
	baselineChallenges, baselineApprovals := agentApprovalRowCounts(t, s)
	if baselineChallenges != 0 || baselineApprovals != 0 {
		t.Fatalf("the unsigned correction left %d challenge and %d approval rows", baselineChallenges, baselineApprovals)
	}
	baselineIdempotency := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`)
	if got := workVersion(t, s, "work-1"); got != version+1 {
		t.Fatalf("the unsigned correction version = %d, want the single correction advance %d", got, version+1)
	}
	baselineVersion := workVersion(t, s, "work-1")

	withClaim, err := injectApproval(unsignedRaw, "challenge:cached-replay-claim")
	if err != nil {
		t.Fatal(err)
	}
	claimReplay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: withClaim}, env)
	if claimReplay.Outcome != OutcomeError || claimReplay.Error == nil || claimReplay.Error.Kind != "invalid_input" || claimReplay.Error.Message != "a routine delivery correction accepts no approval claim" {
		t.Errorf("cached correction replay with an unneeded claim outcome=%s error=%+v, want the same fresh invalid_input refusal the unsigned claim path returns", claimReplay.Outcome, claimReplay.Error)
	}
	// The replay must settle nothing new: these probes measure the reply
	// against the baseline populations captured before it.
	if got := agentDeliveryCorrectionEvents(t, s); got != baselineCorrections {
		t.Errorf("the claim replay left %d correction events, want the baseline %d", got, baselineCorrections)
	}
	if got := agentCurrentEffects(t, s); got != baselineEvents {
		t.Errorf("the claim replay changed current rows or receipts: got=%s baseline=%s", got, baselineEvents)
	}
	var afterActors int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE subject_id='work-1' AND kind=?`, store.WorkflowActorRecorded).Scan(&afterActors); err != nil {
		t.Fatal(err)
	}
	if afterActors != baselineActors {
		t.Errorf("the claim replay moved the actor prefix population from %d to %d", baselineActors, afterActors)
	}
	if challenges, approvals := agentApprovalRowCounts(t, s); challenges != baselineChallenges || approvals != baselineApprovals {
		t.Errorf("the claim replay left %d challenge and %d approval rows, want the baseline %d and %d", challenges, approvals, baselineChallenges, baselineApprovals)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records`); got != baselineIdempotency {
		t.Errorf("the claim replay left %d idempotency rows, want the baseline %d", got, baselineIdempotency)
	}
	if touched := countRows(t, s.DatabaseForTesting(), `SELECT replayed_count FROM idempotency_records WHERE idempotency_key='delivery-correction-cached-replay'`); touched != 0 {
		t.Errorf("the claim replay touched the receipt %d times, want the refused claim to leave it untouched", touched)
	}
	if got := workVersion(t, s, "work-1"); got != baselineVersion {
		t.Errorf("the claim replay moved the work to v%d, want %d", got, baselineVersion)
	}

	// The unchanged unsigned replay stays the cached success: the ok
	// outcome, the replayed marker, and the exact original result payload
	// and changed refs, with the single receipt observation the replay
	// touch records.
	unchanged := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "correct_delivery", Input: unsignedRaw}, env)
	if unchanged.Outcome != OutcomeOK || unchanged.Error != nil {
		t.Fatalf("the unchanged unsigned replay outcome=%s error=%+v, want the cached success", unchanged.Outcome, unchanged.Error)
	}
	if !unchanged.Replayed {
		t.Errorf("the unchanged unsigned replay carries replayed=false, want the replayed marker")
	}
	if !sameResultPayload(t, unsigned.Result, unchanged.Result) {
		t.Errorf("the unchanged unsigned replay result payload differs from the original: original=%s replayed=%s", unsigned.Result, unchanged.Result)
	}
	if !sameChangedRefs(t, unsigned.ChangedRefs, unchanged.ChangedRefs) {
		t.Errorf("the unchanged unsigned replay changed refs %+v differ from the original %+v", unchanged.ChangedRefs, unsigned.ChangedRefs)
	}
	if touched := countRows(t, s.DatabaseForTesting(), `SELECT replayed_count FROM idempotency_records WHERE idempotency_key='delivery-correction-cached-replay'`); touched != 1 {
		t.Errorf("the unchanged unsigned replay touched the receipt %d times, want exactly the one replay observation", touched)
	}
	if got := agentDeliveryCorrectionEvents(t, s); got != 1 {
		t.Fatalf("the unchanged unsigned replay left %d correction events, want one", got)
	}
}
