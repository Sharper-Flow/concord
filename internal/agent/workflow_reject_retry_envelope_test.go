package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// seedDerivedFailureWorkerForCombinedReject seeds work-1 on the current
// implementation definition at its execution step with one dispatched,
// completed, undispositioned worker attempt, the contract and predicates the
// packet derivation reads, and the claimed worktree a retry authorization
// rests on. The caller decides whether a work context rides the view.
func seedDerivedFailureWorkerForCombinedReject(t *testing.T, s *store.Store, service *Service, grant Authority) (int64, string) {
	t.Helper()
	seedCurrentWorkflowDomainFixture(t, s)
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	owner := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: definition, Actor: owner, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	// The recorded narrative rides the packet's work_record member verbatim,
	// so the fixture seeds it directly the way capture records it.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE work_items SET narrative=? WHERE id='work-1';
		DELETE FROM write_guard`, strings.Repeat("n", 16384)); err != nil {
		t.Fatalf("seed the recorded narrative: %v", err)
	}
	approvalStep := ""
	for _, step := range definition.Definition.StepGraph.Steps {
		if slices.Contains(step.Actions, "approve_contract") {
			approvalStep = step.ID
			break
		}
	}
	if approvalStep == "" {
		t.Fatal("combined-reject fixture definition has no contract approval step")
	}
	path := t.TempDir()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		UPDATE workflow_instances SET current_step=? WHERE work_id='work-1';
		DELETE FROM write_guard`, approvalStep); err != nil {
		t.Fatalf("seed combined-reject projections: %v", err)
	}
	fields := workflowContractFieldsFixture()
	fields["premise"] = "approved retry objective"
	fields["outcome_predicates"] = []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:verification", "expected_result": "pass"}}}
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	runVerificationStoreAction(t, s, "approve_contract", fields, owner, "combined-reject-fixture")
	runVerificationStoreAction(t, s, "record_design", workflowDesignFieldsFixture("combined-reject-fixture"), owner, "combined-reject-fixture-design")
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) VALUES(?,?,?,?,?,?,?,'active',?);
		DELETE FROM write_guard`, store.WorktreeSetID("work-1"), "project-1", "claim:combined-reject", "combined-reject", strings.Repeat("a", 40), path, "repo:combined-reject", fixedTime().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed combined-reject worktree: %v", err)
	}
	grant.Worktree = path
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	version := workVersion(t, s, "work-1")
	start := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: json.RawMessage(`{"work_id":"work-1","expected_version":` + strconv.FormatInt(version, 10) + `,"action_id":"start_execution","idempotency_key":"combined-reject-start-1"}`)}, env)
	if start.Outcome != OutcomeOK {
		t.Fatalf("seed combined-reject start: %+v", start.Error)
	}
	fixture := oAgentFixture(t, s, service, env, "work-1")
	oAgentNativeJob(t, fixture)
	recordReadyRetryJob(t, s, service, env, "job:combined-reject-architecture-review")
	oAgentReviewJob(t, fixture, "job:combined-reject-architecture-review", "continue", false)
	attemptID := "attempt:work-1:combined-reject-1"
	lane, job, _ := fixture.startWorker(t, "implement", attemptID)
	if job.JobID != "job:o-agent-native" {
		t.Fatalf("combined-reject implementation job=%s, want job:o-agent-native", job.JobID)
	}
	// A rejected result need not edit the candidate. Keeping it unchanged
	// lets the retry consume the same qualified native preparation.
	fixture.finishWorker(t, lane, attemptID, job, nil)
	return workVersion(t, s, "work-1"), path
}

// recordOversizedWorkContext declares the largest legal findings context the
// action admits, so the derived retry packet carries a work-context view that
// alone pushes the combined response past the agent envelope budget. Every
// value stays inside its declared field bound: the packet is legal, only its
// serialization is unreportable.
func recordOversizedWorkContext(t *testing.T, s *store.Store, service *Service, grant Authority) {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	evidence := strings.Repeat("e", 240)
	// Ten findings keep the record call's own ok echo under the agent
	// envelope budget while the derived packet still carries a view that
	// crosses it beside the recorded text and the rejection correction.
	findings := make([]map[string]any, 0, 10)
	for i := 0; i < 10; i++ {
		findings = append(findings, map[string]any{
			"kind": "observation", "statement": strings.Repeat("s", 1024),
			"subject_ref": "context:work-1:" + strconv.Itoa(i),
			"evidence_refs": []string{
				evidence + ":0", evidence + ":1", evidence + ":2", evidence + ":3",
				evidence + ":4", evidence + ":5", evidence + ":6", evidence + ":7",
			},
			"domain_id":              pm1fixture.FixtureRootDomainID,
			"product_wide_rationale": "the fixture root domain carries every combined-reject finding",
		})
	}
	raw, err := json.Marshal(map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "record_work_context",
		"idempotency_key": "combined-reject-context-1", "fields": map[string]any{"context_findings": findings},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.RequestID = "request:combined-reject-context-1"
	recorded := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if recorded.Outcome != OutcomeOK {
		t.Fatalf("record the oversized work context: %+v", recorded.Error)
	}
}

// combinedRejectInput builds the combined reject_worker_result input whose
// own diagnosis and strategy ride the derived packet's correction context.
func combinedRejectInput(version, epoch int64, diagnosis, strategy string) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"work_id": "work-1", "expected_version": version, "action_id": "reject_worker_result",
		"idempotency_key": "combined-reject-1",
		"fields": map[string]any{
			"attempt_id": "attempt:work-1:combined-reject-1", "attempt_epoch": epoch,
			"diagnosis": diagnosis, "strategy": strategy,
			"predicate_ids": []string{"predicate:primary"},
			"evidence_refs": []string{"evidence:combined-reject"},
			"retry":         map[string]any{"lane_id": "implement"},
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

// latestDispatchEpoch reads the fenced epoch of the fixture dispatch start.
func latestDispatchEpoch(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var epoch int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(latest_dispatch_start_payload,'$.attempt_epoch') FROM workflow_step_epochs WHERE work_id='work-1' ORDER BY latest_dispatch_start_seq DESC LIMIT 1`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

// combinedRejectRefusalEvents counts the rejection and dispatch completions
// the fixture's own seed dispatch does not account for.
func combinedRejectRefusalEvents(t *testing.T, s *store.Store) int {
	t.Helper()
	return countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM effect_receipts r WHERE r.subject_type='work_item' AND r.subject_id='work-1' AND r.kind='workflow.action_completed' AND (EXISTS(SELECT 1 FROM worker_authorizations a WHERE a.event_id=r.event_id) OR EXISTS(SELECT 1 FROM worker_attempt_dispositions d WHERE d.event_id=r.event_id AND d.action_id='reject_worker_result'))`)
}

// TestCombinedRejectOversizedResultRefusesZeroWrite proves a legal oversized
// retry packet cannot strand a committed dispatch: the combined rejection's
// response carries the derived packet inline, and when that serialization
// exceeds the agent envelope budget the whole call refuses typed at the
// envelope budget before anything commits — no rejection record, no retry
// attempt, no work version movement, no cached idempotent success, and the
// rejection-only escape stays available. The normal-size control on an
// identical fixture commits the same combined call.
func TestCombinedRejectOversizedResultRefusesZeroWrite(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_transition", "worker_dispatch"})
	_, worktree := seedDerivedFailureWorkerForCombinedReject(t, s, service, grant)
	grant.Worktree = worktree
	recordOversizedWorkContext(t, s, service, grant)
	version := workVersion(t, s, "work-1")
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "request:combined-reject-1"
	input := combinedRejectInput(version, latestDispatchEpoch(t, s), strings.Repeat("d", 4096), strings.Repeat("t", 4096))
	before := combinedRejectRefusalEvents(t, s)
	durableBefore := countRows(t, s.DatabaseForTesting(), "SELECT count(*) FROM durable_operations WHERE work_id='work-1' AND result_kind='completed'")
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if refused.Outcome != OutcomeError || refused.Error == nil {
		t.Fatalf("the oversized combined rejection answered %+v, want the typed envelope refusal", refused)
	}
	if refused.Error.Kind != "limit_exceeded" || refused.Error.RecoveryAction.Kind != "reduce_limit" {
		t.Fatalf("the oversized refusal is %s/%s, want limit_exceeded with reduce_limit", refused.Error.Kind, refused.Error.RecoveryAction.Kind)
	}
	if refused.Error.EffectState != EffectNone {
		t.Fatalf("the oversized refusal reports effect %q, want none: nothing committed", refused.Error.EffectState)
	}
	if after := combinedRejectRefusalEvents(t, s); after != before {
		t.Fatalf("the oversized refusal changed rejection or dispatch completions %d -> %d", before, after)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id LIKE '%:retry'`); got != 0 {
		t.Fatalf("the oversized refusal minted %d retry attempts", got)
	}
	if after := workVersion(t, s, "work-1"); after != version {
		t.Fatalf("the oversized refusal moved the work version %d -> %d", version, after)
	}
	if got := countRows(t, s.DatabaseForTesting(), "SELECT count(*) FROM durable_operations WHERE work_id='work-1' AND result_kind='completed'"); got != durableBefore {
		t.Fatalf("the oversized refusal left %d completed durable operation rows on the work", got-durableBefore)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM idempotency_records WHERE idempotency_key='combined-reject-1'`); got != 0 {
		t.Fatalf("the oversized refusal cached %d idempotent successes", got)
	}
	// The refusal is stable on replay: the same request re-executes the
	// same derivation and refuses the same way with the same zero writes.
	replayed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: input}, env)
	if replayed.Outcome != OutcomeError || replayed.Error == nil || replayed.Error.Kind != "limit_exceeded" {
		t.Fatalf("the replayed oversized rejection answered %+v, want the same typed refusal", replayed)
	}
	if after := combinedRejectRefusalEvents(t, s); after != before {
		t.Fatalf("the replayed oversized refusal changed rejection or dispatch completions %d -> %d", before, after)
	}
	if got := countRows(t, s.DatabaseForTesting(), "SELECT count(*) FROM durable_operations WHERE work_id='work-1' AND result_kind='completed'"); got != durableBefore {
		t.Fatalf("the replayed oversized refusal left %d completed durable operation rows on the work", got-durableBefore)
	}
}

// TestCombinedRejectNormalSizeCommits is the control: an identical fixture
// without the oversized context commits the same combined call — one
// rejection, one retry attempt, the ok envelope with the derived dispatch.
func TestCombinedRejectNormalSizeCommits(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_transition", "worker_dispatch"})
	version, worktree := seedDerivedFailureWorkerForCombinedReject(t, s, service, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "request:combined-reject-normal-1"
	before := combinedRejectRefusalEvents(t, s)
	committed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: combinedRejectInput(version, latestDispatchEpoch(t, s), "the bounded diagnosis", "the bounded strategy")}, env)
	if committed.Outcome != OutcomeOK {
		t.Fatalf("the normal-size combined rejection: %+v", committed.Error)
	}
	var result map[string]any
	if err := json.Unmarshal(committed.Result, &result); err != nil {
		t.Fatal(err)
	}
	operationID, ok := result["operation_id"].(string)
	if !ok || operationID == "" || result["retry_attempt_id"] != operationID+":retry" {
		t.Fatalf("the committed retry does not bind the original rejection operation: %v", result)
	}
	if after := combinedRejectRefusalEvents(t, s); after != before+2 {
		t.Fatalf("the normal-size combined call recorded %d rejection and dispatch completions, want the rejection and the retry dispatch", after-before)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id LIKE '%:retry'`); got != 1 {
		t.Fatalf("the normal-size combined call minted %d retry attempts, want one", got)
	}
}

// seedCombinedRejectResearchPack authors one research pack owned by work-1
// itself, so the selection's owner scope matches the combined-reject
// fixture's Product without a cross-Product grant, and links one source
// descriptor the resolved view can publish.
func seedCombinedRejectResearchPack(t *testing.T, s *store.Store, packID string) {
	t.Helper()
	ctx := context.Background()
	pack, err := createResearchPack(ctx, s, store.CreateResearchPackRequest{
		PackID: packID, OwnerWorkID: "work-1", Freshness: store.ResearchCurrent,
		Revision: store.ResearchRevisionInput{Question: "combined-reject research question", Method: "source inspection", ScopeIn: json.RawMessage(`{}`), ScopeOut: json.RawMessage(`{}`), DoneWhen: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("seed combined-reject research pack: %v", err)
	}
	if err := recordResearchSource(ctx, s, store.ResearchSourceRequest{
		PackID: packID, Revision: 1, ExpectedVersion: pack.ExpectedVersion,
		Source: store.ResearchSource{SourceID: "src-" + packID, Kind: store.SourceCode, Locator: "internal/agent/workflow_reject_retry_envelope_test.go", Title: "combined-reject fixture source", PublisherOrAuthor: "concord", AccessedAt: "2026-01-01T00:00:00Z"},
	}); err != nil {
		t.Fatalf("seed combined-reject research source: %v", err)
	}
	if err := recordResearchFinding(ctx, s, store.ResearchFindingRequest{
		PackID: packID, Revision: 1, ExpectedVersion: pack.ExpectedVersion + 1,
		Finding: store.ResearchFinding{FindingID: "f-" + packID, Kind: store.FindingObservation, Statement: "combined-reject research finding statement", Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive, SourceIDs: []string{"src-" + packID}, Scopes: store.ResearchScopes{Mode: "home"}},
	}); err != nil {
		t.Fatalf("seed combined-reject research finding: %v", err)
	}
}

// recordSelectedResearchContextForCombinedReject declares work-1's work
// context with the pack's selection bound to the same action, so the current
// view the derived retry packet rides carries a resolved research entry.
func recordSelectedResearchContextForCombinedReject(t *testing.T, s *store.Store, service *Service, grant Authority, key, packID string) {
	t.Helper()
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "request:" + key
	input := map[string]any{
		"work_id": "work-1", "expected_version": workVersion(t, s, "work-1"), "action_id": "record_work_context",
		"idempotency_key":   key,
		"research_bindings": []map[string]any{{"pack_id": packID, "revision": int64(1), "use_role": "context", "required": true}},
		"fields": map[string]any{
			"required_reading": []any{}, "finding_refs": []any{}, "context_findings": []any{},
			"research_finding_refs": []map[string]any{{
				"pack_id": packID, "revision": int64(1), "finding_id": "f-" + packID,
				"domain_id":              pm1fixture.FixtureRootDomainID,
				"product_wide_rationale": "the selection governs every Domain of the fixture Product",
			}},
		},
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	recorded := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
	if recorded.Outcome != OutcomeOK {
		t.Fatalf("record the selected research context: %+v", recorded.Error)
	}
}

// assertCombinedRejectRetryTransport proves the committed response returns
// exactly the admitted retry dispatch: the operation-bound fresh attempt,
// the derived packet bytes the fresh dispatch window's digest covers, and no
// second dispatch authorization.
func assertCombinedRejectRetryTransport(t *testing.T, s *store.Store, committed Envelope, before int) {
	t.Helper()
	if committed.Outcome != OutcomeOK {
		t.Fatalf("the combined rejection: %+v", committed.Error)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(committed.Result, &result); err != nil {
		t.Fatal(err)
	}
	var operationID string
	if err := json.Unmarshal(result["operation_id"], &operationID); err != nil {
		t.Fatal(err)
	}
	var retryAttemptID string
	if err := json.Unmarshal(result["retry_attempt_id"], &retryAttemptID); err != nil {
		t.Fatal(err)
	}
	if retryAttemptID != operationID+":retry" {
		t.Fatalf("the committed retry does not bind the original rejection operation: %s vs %s", retryAttemptID, operationID+":retry")
	}
	// The dispatch admission digests canonicalJSON(worker_packet), so the
	// returned packet must reproduce that digest exactly: canonical packet
	// identity with the admitted dispatch. The digest comparison proves
	// canonical equality, not whitespace or raw encoding equality.
	decoded := json.NewDecoder(bytes.NewReader(result["retry_worker_packet"]))
	decoded.UseNumber()
	var packetValue any
	if err := decoded.Decode(&packetValue); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(packetValue)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	var returnedDigest string
	if err := json.Unmarshal(result["worker_packet_digest"], &returnedDigest); err != nil {
		t.Fatal(err)
	}
	if returnedDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("the returned packet does not carry canonical identity with the admitted retry dispatch: %s vs sha256:%s", returnedDigest, hex.EncodeToString(sum[:]))
	}
	if after := combinedRejectRefusalEvents(t, s); after != before+2 {
		t.Fatalf("the combined call recorded %d rejection and dispatch completions, want the rejection and the one retry dispatch", after-before)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts WHERE work_id='work-1' AND attempt_id LIKE '%:retry'`); got != 1 {
		t.Fatalf("the combined call minted %d retry attempts, want the one derived attempt", got)
	}
}

// TestCombinedRejectResearchHeadTransportsAdmittedPacket3 proves the
// response transport invariant: the current dispatch admits a
// worker_packet whose work-context view carries the selected research, and
// the combined rejection's response returns exactly
// those admitted bytes — canonical packet identity with the digest the
// fresh dispatch window opened for — with no second dispatch
// authorization.
func TestCombinedRejectResearchHeadTransportsAdmittedPacket3(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_transition", "worker_dispatch"})
	_, worktree := seedDerivedFailureWorkerForCombinedReject(t, s, service, grant)
	grant.Worktree = worktree
	seedCombinedRejectResearchPack(t, s, "pack-combined-reject-rc")
	recordSelectedResearchContextForCombinedReject(t, s, service, grant, "combined-reject-research-1", "pack-combined-reject-rc")
	version := workVersion(t, s, "work-1")
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "request:combined-reject-research-1"
	before := combinedRejectRefusalEvents(t, s)
	committed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: combinedRejectInput(version, latestDispatchEpoch(t, s), "the bounded diagnosis", "the bounded strategy")}, env)
	assertCombinedRejectRetryTransport(t, s, committed, before)
	var packet struct {
		Inputs struct {
			WorkContext *struct {
				ResearchFindings []struct {
					Selection struct {
						PackID    string `json:"pack_id"`
						FindingID string `json:"finding_id"`
						Revision  int64  `json:"revision"`
					} `json:"selection"`
					State string `json:"state"`
				} `json:"research_findings"`
			} `json:"work_context"`
		} `json:"inputs"`
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(committed.Result, &result); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(result["retry_worker_packet"], &packet); err != nil {
		t.Fatal(err)
	}
	if packet.Inputs.WorkContext == nil || len(packet.Inputs.WorkContext.ResearchFindings) != 1 {
		t.Fatalf("the returned retry packet lost the selected research: %s", result["retry_worker_packet"])
	}
	entry := packet.Inputs.WorkContext.ResearchFindings[0]
	if entry.State != "resolved" || entry.Selection.PackID != "pack-combined-reject-rc" || entry.Selection.FindingID != "f-pack-combined-reject-rc" || entry.Selection.Revision != 1 {
		t.Fatalf("the returned retry packet carries a wrong research entry: %+v", entry)
	}
}

// TestCombinedRejectResearchHeadPlainPacketCommits is the control: a derived
// packet whose view carries no research commits with the plain transport and
// invents no research member.
func TestCombinedRejectResearchHeadPlainPacketCommits(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"product_read", "work_transition", "worker_dispatch"})
	version, worktree := seedDerivedFailureWorkerForCombinedReject(t, s, service, grant)
	grant.Worktree = worktree
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	env.RequestID = "request:combined-reject-plain-1"
	before := combinedRejectRefusalEvents(t, s)
	committed := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: combinedRejectInput(version, latestDispatchEpoch(t, s), "the bounded diagnosis", "the bounded strategy")}, env)
	assertCombinedRejectRetryTransport(t, s, committed, before)
	var result map[string]json.RawMessage
	if err := json.Unmarshal(committed.Result, &result); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result["retry_worker_packet"]), `"research_findings"`) {
		t.Fatalf("the plain-head retry packet invented a research member: %s", result["retry_worker_packet"])
	}
}
