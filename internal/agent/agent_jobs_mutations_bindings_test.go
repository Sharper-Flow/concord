package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// This file implements mutation scenarios bound from
// .concord/scenarios/agent-jobs.v1.json. Every binding
// returns a jobObservation whose state/result/communication/effects/
// authority maps satisfy the corpus assertions literally. The
// fixture helpers live in agent_jobs_mutation_fixture_test.go.
//
// Style note: the five-facet observation model is followed exactly.
// The "actively probe" guidance from the issue brief is honored by
// querying the database for state the runner's own assertions rely on
// (e.g. no terminal transition event exists for work-cross after the
// AJ4-completion-missing-evidence refusal) — a nil-valued absent path
// is never trusted to be vacuously true.

// Observation model — the runner resolves paths like
// `work.work-ready-high.lifecycle` and `effects.transition_events.0.evidence_refs`
// via dot-path traversal over map[string]any, so sub-objects that the
// corpus expects to index must be map[string]any (NOT []any). The
// integer "0", "1", ... indices are map keys, not slice positions.
// Bindings below all follow this rule.

// ---------------------------------------------------------------------------
// AJ5 — Initiative framing
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// AJ3 — capture_needed_work
// ---------------------------------------------------------------------------

// AJ3-capture-work: capture needed work for passkey login and observe
// the resulting durable row. After capture, replay the SAME idempotency
// key and prove that no second work item is created and the result is
// equivalent (retry_safe). The replayed response must carry
// Replayed=true so the runner can read the replay evidence
// independently of the assertion path.
func bindAJ3CaptureWork(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	idempotencySeed, _ := sc.InitialState["idempotency_seed"].(string)
	if idempotencySeed == "" {
		t.Fatalf("AJ3-capture-work: missing idempotency_seed")
	}

	input := []byte(fmt.Sprintf(`{"title":"Add passkey login","value_statement":"reducing account takeover risk","kind":"task","project_ids":["proj-web"],"idempotency_key":"%s"}`, idempotencySeed))
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("capture failed outcome=%s err=%+v", first.Outcome, first.Error)
	}

	createdWorkID := (*first.ChangedRefs)[0].ID
	lifecycle, version := readWorkFromStore(t, s, createdWorkID)
	if lifecycle != "needed" {
		t.Fatalf("created work lifecycle=%q, want needed", lifecycle)
	}
	// Read project_ids from the DB and present them as []any so the
	// runner's eq check matches the JSON value ["proj-web"] (which
	// decodes to []interface{}{"proj-web"}). The runner's
	// deepEqualTolerant uses reflect.DeepEqual which distinguishes
	// []string from []interface{}.
	rows, err := s.DatabaseForTesting().Query(`SELECT project_id FROM work_projects WHERE work_id=?`, createdWorkID)
	if err != nil {
		t.Fatalf("read work_projects[%s]: %v", createdWorkID, err)
	}
	defer rows.Close()
	projects := []any{}
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			t.Fatalf("scan project id: %v", err)
		}
		projects = append(projects, pid)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}

	// Replay the same idempotency key — retry_safe must hold. The
	// Replayed flag alone is the idempotency layer's own claim; the
	// durable proof is that no second work item was written, so count
	// rows either side of the replay rather than trusting the flag.
	preReplayWorkCount := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&preReplayWorkCount); err != nil {
		t.Fatalf("pre-replay work_items count: %v", err)
	}
	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if replay.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("replay failed outcome=%s err=%+v", replay.Outcome, replay.Error)
	}
	if !replay.Replayed {
		t.Fatalf("replay did not set Replayed=true")
	}
	postReplayWorkCount := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&postReplayWorkCount); err != nil {
		t.Fatalf("post-replay work_items count: %v", err)
	}
	if postReplayWorkCount != preReplayWorkCount {
		t.Fatalf("replay created a duplicate work item: count %d -> %d", preReplayWorkCount, postReplayWorkCount)
	}
	if replayLifecycle, replayVersion := readWorkFromStore(t, s, createdWorkID); replayLifecycle != lifecycle || replayVersion != version {
		t.Fatalf("replay mutated the captured work: lifecycle %q -> %q, version %d -> %d", lifecycle, replayLifecycle, version, replayVersion)
	}

	obs := envelopeToObservation(first)
	obs.State = map[string]any{
		"created_work": map[string]any{
			"count":       1,
			"lifecycle":   lifecycle,
			"project_ids": projects,
			"value":       "reducing account takeover risk",
			"id":          createdWorkID,
			"version":     version,
		},
	}
	obs.Communication["created_work_id"] = createdWorkID
	// Success path — atomic_core_effect is "complete", not the
	// refusal marker, so invariantAtomicCoreEffect can read it.
	obs.Effects = map[string]any{
		"retry_safe_replayed": true,
		"atomic_core_effect":  "complete",
	}
	obs.Authority["replayed"] = replay.Replayed
	return obs
}

// ---------------------------------------------------------------------------
// AJ4 — transition_work_with_evidence
// ---------------------------------------------------------------------------

// AJ4-start-valid-work: transition work-ready-high from needed to
// in_progress using the post-amendment version 2 (per the operator
// amendment applied to .concord/scenarios/agent-jobs.v1.json). Then replay the
// same idempotency_key and prove no duplicate transition event and no
// additional version bump (retry_safe).
func bindAJ4StartValidWork(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	preLifecycle, preVersion := readWorkFromStore(t, s, "work-ready-high")
	if preLifecycle != "needed" {
		t.Fatalf("work-ready-high pre-lifecycle=%q, want needed", preLifecycle)
	}

	idempotencySeed, _ := sc.InitialState["idempotency_seed"].(string)
	if idempotencySeed == "" {
		t.Fatalf("AJ4-start-valid-work: missing idempotency_seed")
	}
	input := []byte(fmt.Sprintf(`{"work_id":"work-ready-high","expected_version":2,"target":"in_progress","reason":"start","idempotency_key":"%s"}`, idempotencySeed))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("lifecycle failed outcome=%s err=%+v", resp.Outcome, resp.Error)
	}

	lifecycle, version := readWorkFromStore(t, s, "work-ready-high")
	if lifecycle != "in_progress" {
		t.Fatalf("post-lifecycle=%q, want in_progress", lifecycle)
	}
	if version != 3 {
		t.Fatalf("post-version=%d, want 3", version)
	}
	count, _ := readTransitionEvents(t, s, "work-ready-high")
	if count != 1 {
		t.Fatalf("transition_events.count=%d, want 1", count)
	}

	// Replay the same idempotency key — retry_safe requires the runner
	// to see a replay outcome with Replayed=true AND the durable
	// state unchanged (still 1 transition, still version 3).
	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if !replay.Replayed || replay.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("replay outcome=%s replayed=%v err=%+v", replay.Outcome, replay.Replayed, replay.Error)
	}
	if _, v := readWorkFromStore(t, s, "work-ready-high"); v != 3 {
		t.Fatalf("replay bumped version")
	}
	if c, _ := readTransitionEvents(t, s, "work-ready-high"); c != 1 {
		t.Fatalf("replay created transition events, count=%d", c)
	}

	obs := envelopeToObservation(resp)
	// The corpus resolves `state.work.work-ready-high.lifecycle` —
	// "work" must be a map, "work-ready-high" a sub-key on it.
	obs.State = map[string]any{
		"work": map[string]any{
			"work-ready-high": map[string]any{
				"lifecycle": lifecycle,
				"version":   version,
			},
		},
	}
	// new_version must be an integer 3 (the corpus value is `3`, a
	// JSON number, not the string "3") because deepEqualTolerant
	// does not auto-coerce int vs string.
	obs.Communication["new_version"] = 3
	// transition_events.0.evidence_refs must be a list of strings.
	obs.Effects = map[string]any{
		"transition_events": map[string]any{
			"count": count,
			"0": map[string]any{
				"kind":          "started",
				"from":          preLifecycle,
				"to":            "in_progress",
				"evidence_refs": []string{},
			},
		},
		"version_increment":   version - preVersion,
		"retry_safe_replayed": true,
		"atomic_core_effect":  "complete",
	}
	return obs
}

// CD-0243 D3/D7 retire terminal-only consent, not verification or replay.
// AJ4 completes work-cross with verification and no consent gate.
func bindAJ4CompleteValidWork(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	preLifecycle, preVersion := readWorkFromStore(t, s, "work-cross")
	if preLifecycle != "in_progress" {
		t.Fatalf("work-cross pre-lifecycle=%q, want in_progress", preLifecycle)
	}
	// PM1 seeds work-cross with one transition event (needed →
	// in_progress). The corpus asserts the COMPLETION itself adds
	// exactly one transition event; record the pre-state so we can
	// observe the increment.
	preTransitionCount, _ := readTransitionEvents(t, s, "work-cross")

	idempotencyKey := fmt.Sprintf("complete-cross-%d", preVersion)
	input := []byte(fmt.Sprintf(`{"work_id":"work-cross","expected_version":%d,"target":"completed","reason":"complete with verification","idempotency_key":"%s","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"verification-pass"}]}`, preVersion, idempotencyKey))

	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approved dispatch failed outcome=%s err=%+v", approved.Outcome, approved.Error)
	}

	lifecycle, version := readWorkFromStore(t, s, "work-cross")
	if lifecycle != "completed" {
		t.Fatalf("post-lifecycle=%q, want completed", lifecycle)
	}
	count, evidenceRefs := readTransitionEvents(t, s, "work-cross")
	// The completion adds one transition event on top of the seeded
	// in_progress event; the corpus asserts the count for THIS
	// operation.
	if count != preTransitionCount+1 {
		t.Fatalf("transition_events.count=%d, want %d (one new event from this completion)", count, preTransitionCount+1)
	}
	foundVerification := false
	for _, ref := range evidenceRefs {
		if ref == "verification-pass" {
			foundVerification = true
			break
		}
	}
	if !foundVerification {
		t.Fatalf("transition_events.0.evidence_refs missing verification-pass: %v", evidenceRefs)
	}

	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if !replay.Replayed || replay.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("replay did not set Replayed=true")
	}
	if _, v := readWorkFromStore(t, s, "work-cross"); v != version {
		t.Fatalf("replay bumped version to %d (was %d)", v, version)
	}
	if c, _ := readTransitionEvents(t, s, "work-cross"); c != count {
		t.Fatalf("replay created additional transition events: %d", c)
	}

	obs := envelopeToObservation(approved)
	obs.State = map[string]any{
		"work": map[string]any{
			"work-cross": map[string]any{
				"lifecycle": lifecycle,
				"version":   version,
			},
		},
	}
	obs.Communication["new_version"] = version
	// evidence_refs must be []any so the corpus `contains` op's
	// slice cast succeeds. evidenceRefs is []string from the store
	// query; the conversion is intentional.
	evidenceRefsAny := make([]any, 0, len(evidenceRefs))
	for _, r := range evidenceRefs {
		evidenceRefsAny = append(evidenceRefsAny, r)
	}
	obs.Effects = map[string]any{
		"transition_events": map[string]any{
			"count": count - preTransitionCount,
			"0": map[string]any{
				"kind":          "completed",
				"from":          preLifecycle,
				"to":            "completed",
				"evidence_refs": evidenceRefsAny,
			},
		},
		"version_increment":           version - preVersion,
		"evidence_authority_supplied": true,
		"retry_safe_replayed":         true,
		"atomic_core_effect":          "complete",
	}
	return obs
}

// AJ4-completion-missing-evidence: attempt to complete work-cross
// with NO evidence. After the D3 repair the runner expects a typed
// missing_evidence refusal with required_kind=verification, with zero
// durable effect — no terminal transition event, lifecycle still
// in_progress, version unchanged.
func bindAJ4CompletionMissingEvidence(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	preLifecycle, preVersion := readWorkFromStore(t, s, "work-cross")
	if preLifecycle != "in_progress" {
		t.Fatalf("work-cross pre-lifecycle=%q, want in_progress", preLifecycle)
	}
	// PM1 seeds work-cross with one transition event. The corpus
	// asserts no NEW event from the missing_evidence refusal, so
	// record the baseline.
	preTransitionCount, _ := readTransitionEvents(t, s, "work-cross")

	idempotencyKey := fmt.Sprintf("complete-missing-evidence-%d", preVersion)
	preEffects := agentCurrentEffects(t, s)
	input := []byte(fmt.Sprintf(`{"work_id":"work-cross","expected_version":%d,"target":"completed","reason":"complete without evidence","idempotency_key":"%s"}`, preVersion, idempotencyKey))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if resp.Outcome != agentcontract.OutcomeError || resp.Error == nil {
		t.Fatalf("expected typed error outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "missing_evidence" {
		t.Fatalf("error.kind=%q, want missing_evidence", resp.Error.Kind)
	}
	if resp.Error.RecoveryAction.Kind != "provide_evidence" {
		t.Fatalf("error.recovery_action=%q, want provide_evidence", resp.Error.RecoveryAction.Kind)
	}
	requiredKind, _ := resp.Error.Details["required_kind"].(string)
	if requiredKind != "verification" {
		t.Fatalf("error.required_kind=%q, want verification", requiredKind)
	}

	// Actively probe: lifecycle unchanged, no terminal transition
	// events exist for work-cross. The absent assertion for
	// effects.terminal_transition is honored by NOT setting it in the
	// effects map (the runner's `absent` op requires a nil value).
	lifecycle, version := readWorkFromStore(t, s, "work-cross")
	if lifecycle != "in_progress" {
		t.Fatalf("post-lifecycle=%q, want in_progress (no effect should have applied)", lifecycle)
	}
	if version != preVersion {
		t.Fatalf("post-version=%d, want %d (version bumped despite typed refusal)", version, preVersion)
	}
	count, _ := readTransitionEvents(t, s, "work-cross")
	if count != preTransitionCount {
		t.Fatalf("transition_events.count=%d, want %d (no new event from missing_evidence refusal)", count, preTransitionCount)
	}
	if agentCurrentEffects(t, s) != preEffects {
		t.Fatal("missing_evidence refusal changed current rows, receipts or effect sequence")
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"work": map[string]any{
			"work-cross": map[string]any{
				"lifecycle": lifecycle,
				"version":   version,
			},
		},
	}
	obs.Communication["error"] = map[string]any{
		"kind":            "missing_evidence",
		"required_kind":   "verification",
		"recovery_action": "provide_evidence",
	}
	// Refusal path: atomic_core_effect_zero=true (invariant requires
	// this when an error is present), and missing_evidence_refused=true
	// (evidence_authority invariant).
	obs.Effects = map[string]any{
		"transition_events":        map[string]any{"count": count - preTransitionCount},
		"atomic_core_effect_zero":  true,
		"missing_evidence_refused": true,
	}
	// Active probe: the corpus's effects.terminal_transition absent
	// assertion must be backed by a real query. Confirm (a) lifecycle
	// is still in_progress, (b) version unchanged from preVersion, and
	// (c) the owning current row records no terminal transition.
	// Together these three checks prove the missing_evidence refusal
	// minted zero terminal transition; recording probedAbsent at the
	// assertion path replaces the previous vacuous nil.
	if lifecycle != "in_progress" {
		t.Fatalf("terminal_transition probe: lifecycle=%q, want in_progress", lifecycle)
	}
	if version != preVersion {
		t.Fatalf("terminal_transition probe: version=%d, want %d", version, preVersion)
	}
	var completedCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id='work-cross' AND lifecycle='completed'`).Scan(&completedCount); err != nil {
		t.Fatalf("terminal_transition probe: query completed events: %v", err)
	}
	var cancelledCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id='work-cross' AND lifecycle IN ('cancelled','superseded')`).Scan(&cancelledCount); err != nil {
		t.Fatalf("terminal_transition probe: query cancelled events: %v", err)
	}
	if completedCount != 0 || cancelledCount != 0 {
		t.Fatalf("terminal_transition probe: found completed=%d cancelled=%d terminal transitions for work-cross; missing_evidence must not apply", completedCount, cancelledCount)
	}
	obs.Effects["terminal_transition"] = probedAbsent{
		Evidence: fmt.Sprintf("work-cross lifecycle=in_progress version=%d, no terminal current transition or new receipt; missing_evidence refused pre-application", version),
	}
	return obs
}

// AJ4-stale-version: attempt to start work-ready-low with
// expected_version=1 while the current is 2. After the D4 repair the
// runner expects error.kind=version_conflict with
// error.current_version=2 structurally available — no regex of the
// human detail.
func bindAJ4StaleVersion(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	preLifecycle, preVersion := readWorkFromStore(t, s, "work-ready-low")
	if preLifecycle != "needed" || preVersion != 2 {
		t.Fatalf("work-ready-low pre-state lifecycle=%q version=%d, want needed/2", preLifecycle, preVersion)
	}

	idempotencyKey := fmt.Sprintf("stale-version-%d", preVersion)
	input := []byte(fmt.Sprintf(`{"work_id":"work-ready-low","expected_version":1,"target":"in_progress","reason":"stale start","idempotency_key":"%s"}`, idempotencyKey))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if resp.Outcome != agentcontract.OutcomeError || resp.Error == nil {
		t.Fatalf("expected typed error outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "version_conflict" {
		t.Fatalf("error.kind=%q, want version_conflict", resp.Error.Kind)
	}
	if resp.Error.RecoveryAction.Kind != "reread_entities" {
		t.Fatalf("error.recovery_action=%q, want reread_entities", resp.Error.RecoveryAction.Kind)
	}
	if len(resp.Error.CurrentVersions) != 1 {
		t.Fatalf("error.current_versions len=%d, want 1 (D4 typed carrier)", len(resp.Error.CurrentVersions))
	}
	current := resp.Error.CurrentVersions[0]
	if current.ID != "work-ready-low" || current.EntityKind != "work_item" {
		t.Fatalf("error.current_versions[0]=%+v, want work_item/work-ready-low", current)
	}
	if v, err := strconv.ParseInt(current.Version, 10, 64); err != nil || v != 2 {
		t.Fatalf("error.current_versions[0].version=%q, want 2", current.Version)
	}

	// Active probe: durable state unchanged, no transition events.
	lifecycle, version := readWorkFromStore(t, s, "work-ready-low")
	if lifecycle != "needed" {
		t.Fatalf("post-lifecycle=%q, want needed", lifecycle)
	}
	if version != preVersion {
		t.Fatalf("post-version=%d, want %d (stale start must not have applied)", version, preVersion)
	}
	count, _ := readTransitionEvents(t, s, "work-ready-low")
	if count != 0 {
		t.Fatalf("transition_events.count=%d, want 0", count)
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"work": map[string]any{
			"work-ready-low": map[string]any{
				"lifecycle": lifecycle,
				"version":   version,
			},
		},
	}
	// current_version must be a float64(2) (the corpus decodes the
	// JSON value `2` to float64; the invariant also casts through
	// float64 so storing as int breaks both checks).
	obs.Communication["error"] = map[string]any{
		"kind":            "version_conflict",
		"current_version": float64(2),
		"recovery_action": "reread_entities",
		"current_versions": []map[string]any{
			{"entity_kind": "work_item", "id": "work-ready-low", "version": "2"},
		},
	}
	obs.Effects = map[string]any{
		"transition_events":       map[string]any{"count": 0},
		"atomic_core_effect_zero": true,
	}
	return obs
}

// ---------------------------------------------------------------------------
// AJ5 — relate_and_scope_work
// ---------------------------------------------------------------------------

// AJ5-resolve-domain-overlap proves ordinary relation authority cannot settle
// coordination and a destructive scope cut requires the cross-scope capability.
func bindAJ5ResolveDomainOverlap(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, _, _, env, input := seedAgentOverlapFixture(t)
	seed, _ := sc.InitialState["idempotency_seed"].(string)
	var request map[string]any
	if err := json.Unmarshal(input, &request); err != nil {
		t.Fatal(err)
	}
	request["idempotency_key"] = seed
	input, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	ordinary := store.Event{
		EventID: "ordinary-compatible", Kind: "relation.added", SubjectType: store.SubjectWorkItem,
		SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"from":"work-1","to":"work-2","kind":"compatible_with","reason":"ordinary relation carries no overlap authority","expected_version":2,"resulting_version":3,"to_expected_version":2,"to_resulting_version":3}`),
	}
	beforeEvents := agentCurrentEffects(t, s)
	ordinaryErr := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{ordinary}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-1"): 2, store.VersionRef(store.SubjectWorkItem, "work-2"): 2}})
	var failure *store.Failure
	if !errors.As(ordinaryErr, &failure) || failure.Kind != store.KindRelationContractViolation {
		t.Fatalf("ordinary compatible_with fold was not refused: %v", ordinaryErr)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM relations WHERE kind='compatible_with'`); got != 0 {
		t.Fatalf("ordinary relation rows=%d, want 0", got)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 0 {
		t.Fatalf("ordinary relation wrote %d overlap resolutions", got)
	}
	if agentCurrentEffects(t, s) != beforeEvents || workVersion(t, s, "work-1") != 2 || workVersion(t, s, "work-2") != 2 {
		t.Fatal("ordinary relation refusal changed journal or endpoint versions")
	}

	routine := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
	if routine.Outcome != agentcontract.OutcomeOK || routine.Error != nil {
		t.Fatalf("unsigned routine overlap resolution failed: %+v", routine.Error)
	}
	var kind string
	var fromContract, toContract int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT resolution_kind,from_contract_version,to_contract_version FROM workflow_overlap_resolutions WHERE invalidated_seq IS NULL`).Scan(&kind, &fromContract, &toContract); err != nil {
		t.Fatal(err)
	}
	if kind != "depends_on" || fromContract != 1 || toContract != 1 {
		t.Fatalf("routine resolution %s contracts %d/%d, want the unsigned sequencing answer at 1/1", kind, fromContract, toContract)
	}
	var relationCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE work_id_from='work-1' AND work_id_to='work-2' AND kind='depends_on' AND resolution_id IS NOT NULL`).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if relationCount != 1 {
		t.Fatalf("resolution relation rows=%d, want 1", relationCount)
	}
	if workVersion(t, s, "work-1") != 3 || workVersion(t, s, "work-2") != 3 {
		t.Fatal("routine resolution left the endpoints off the single-step versions")
	}

	obs := envelopeToObservation(routine)
	obs.State = map[string]any{
		"overlap":   map[string]any{"current_resolution": map[string]any{"kind": kind, "from_contract_version": fromContract, "to_contract_version": toContract}},
		"relations": map[string]any{"depends_on": []any{map[string]any{"source": "work-1", "target": "work-2"}}},
	}
	destructiveStore, destructiveService, _, _, destructiveEnv, destructiveBase := seedAgentOverlapFixture(t)
	setAgentDeclaredRequirements(t, destructiveStore, "work-1", []string{"audit_required"})
	setAgentDeclaredRequirements(t, destructiveStore, "work-2", []string{})
	destructiveInput := []byte(strings.Replace(string(destructiveBase), `"resolution_kind":"depends_on"`, `"resolution_kind":"merged_into"`, 1))
	destructiveBefore := agentCurrentEffects(t, destructiveStore)
	refused := dispatchMutation(t, destructiveStore, destructiveService, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: destructiveInput}, destructiveEnv)
	if refused.Error == nil || refused.Error.Kind != "unauthorized" {
		t.Fatalf("destructive merged_into without cross scope outcome=%s error=%+v, want unauthorized", refused.Outcome, refused.Error)
	}
	if got := countRows(t, destructiveStore.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 0 {
		t.Fatalf("the refused destructive control wrote %d resolutions", got)
	}
	if agentCurrentEffects(t, destructiveStore) != destructiveBefore || workVersion(t, destructiveStore, "work-1") != 2 || workVersion(t, destructiveStore, "work-2") != 2 {
		t.Fatal("refused destructive control changed effects or endpoint versions")
	}
	obs.State["destructive_control"] = map[string]any{
		"resolution_kind": "merged_into",
		"refused":         "unauthorized",
	}
	obs.Communication = envelopeToObservation(refused).Communication
	violations := make([]any, len(refused.Error.Violations))
	for i, violation := range refused.Error.Violations {
		violations[i] = violation
	}
	obs.Communication["error"].(map[string]any)["violations"] = violations
	obs.Effects = map[string]any{
		"atomic_core_effect":          "complete",
		"atomic_core_effect_zero":     true,
		"ordinary_relation_authority": probedAbsent{Evidence: "the generic compatible_with fold returned relation_contract_violation, and wrote no relation or resolution"},
		// The destructive control withheld its effect pending the operator's
		// cross-scope choice: unauthorized, contact_operator, named omissions.
		"operator_choice_withheld": true,
	}
	return obs
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// AJ3-spec-conflict: capture into a Project carrying an accepted governing
// requirement while declaring a requirement set that omits it. CD-0035 D3 makes
// the refusal a set difference, so the omission is detected structurally rather
// than read out of the instruction text. The scenario's human_checkpoint driver
// is pending, which models an operator who has not authorized a scope cut, so
// the call carries no approval reference and the refusal is the only correct
// outcome.
func bindAJ3SpecConflict(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	requirement, _ := sc.InitialState["governing_requirement"].(string)
	if requirement == "" {
		t.Fatalf("AJ3-spec-conflict: missing governing_requirement in initial_state")
	}
	if err := pm1fixture.SeedGoverningRequirement(context.Background(), s, "proj-web", requirement, "accepted audit obligation"); err != nil {
		t.Fatalf("seed governing requirement: %v", err)
	}
	// Seeding moved the Project version, so the call envelope must resolve the
	// current scope version rather than the pre-seed one.
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	driver := resolveDriver(t, sc)
	if !driver.approvalWithheld() {
		t.Fatalf("AJ3-spec-conflict expects a pending human checkpoint, got %+v", driver)
	}

	preWork := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&preWork); err != nil {
		t.Fatalf("pre-count work_items: %v", err)
	}
	preEvents := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&preEvents); err != nil {
		t.Fatalf("pre-effect sequence: %v", err)
	}
	preEffects := agentCurrentEffects(t, s)

	// The instruction omits the accepted requirement to make the work smaller,
	// which on the wire is a capture that declares no governing requirements.
	input := []byte(`{"title":"Add passkey login","value_statement":"reducing account takeover risk","kind":"task","project_ids":["proj-web"],"idempotency_key":"spec-conflict-1"}`)
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)

	if resp.Outcome != agentcontract.OutcomeError || resp.Error == nil {
		t.Fatalf("expected typed refusal outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "unauthorized" {
		t.Fatalf("error.kind=%q, want unauthorized", resp.Error.Kind)
	}
	if resp.Error.RecoveryAction.Kind != "contact_operator" {
		t.Fatalf("recovery_action=%q, want contact_operator", resp.Error.RecoveryAction.Kind)
	}
	// The omitted requirement is named in the typed carrier, not the message.
	if len(resp.Error.Violations) != 1 || resp.Error.Violations[0] != requirement {
		t.Fatalf("error.violations=%v, want [%s]", resp.Error.Violations, requirement)
	}
	// Probe the prohibited effect: a silent requirement omission would be a
	// captured work item or any appended event. Both are checked against the
	// pre-call counts so the absence is proven rather than assumed.
	postWork := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&postWork); err != nil {
		t.Fatalf("post-count work_items: %v", err)
	}
	postEvents := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&postEvents); err != nil {
		t.Fatalf("post-effect sequence: %v", err)
	}
	if postWork != preWork {
		t.Fatalf("refused capture created %d work rows (pre=%d, post=%d)", postWork-preWork, preWork, postWork)
	}
	if postEvents != preEvents {
		t.Fatalf("refused capture appended %d events (pre=%d, post=%d)", postEvents-preEvents, preEvents, postEvents)
	}
	if agentCurrentEffects(t, s) != preEffects {
		t.Fatal("refused capture changed current rows or effect receipts")
	}
	titled := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE title=?`, "Add passkey login").Scan(&titled); err != nil {
		t.Fatalf("probe captured title: %v", err)
	}
	if titled != 0 {
		t.Fatalf("refused capture persisted %d work items under the requested title", titled)
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"created_work": map[string]any{"count": 0},
	}
	obs.Effects["silent_requirement_omission"] = probedAbsent{
		Evidence: "current work rows, effect receipts and effect sequence unchanged across the refused capture, and no work item exists under the requested title",
	}
	// The consequence required the operator's choice and the governing-law
	// refusal withheld the effect.
	obs.Effects["operator_choice_withheld"] = true
	violations := make([]any, len(resp.Error.Violations))
	for i, v := range resp.Error.Violations {
		violations[i] = v
	}
	if commErr, ok := obs.Communication["error"].(map[string]any); ok {
		commErr["violations"] = violations
	}
	return obs
}

func TestCrossScopeCapabilityAllowsScopeCut(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	if err := pm1fixture.SeedGoverningRequirement(context.Background(), s, "proj-web", "audit_required", "accepted audit obligation"); err != nil {
		t.Fatalf("seed governing requirement: %v", err)
	}
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	input := []byte(`{"title":"Add passkey login","value_statement":"reducing account takeover risk","kind":"task","project_ids":["proj-web"],"idempotency_key":"approved-cut-1"}`)
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if refused.Error == nil || refused.Error.Kind != "unauthorized" {
		t.Fatalf("expected governing conflict, got outcome=%s err=%+v", refused.Outcome, refused.Error)
	}
	if err := service.ExpandTrustedClientPolicy(context.Background(), grant.ClientRef, TrustedClientPolicy{Capabilities: []agentcontract.Capability{"cross_scope"}}); err != nil {
		t.Fatal(err)
	}
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("operator-approved scope cut was refused outcome=%s err=%+v", approved.Outcome, approved.Error)
	}
	if len(*approved.ChangedRefs) == 0 {
		t.Fatal("approved capture reported no changed refs")
	}
	if lifecycle, _ := readWorkFromStore(t, s, (*approved.ChangedRefs)[0].ID); lifecycle != "needed" {
		t.Fatalf("approved capture produced lifecycle %q, want needed", lifecycle)
	}
	var declared string
	if err := s.DatabaseForTesting().QueryRow(`SELECT json_extract(intent_json,'$.governing_requirements') FROM work_items WHERE id=?`, (*approved.ChangedRefs)[0].ID).Scan(&declared); err != nil {
		t.Fatal(err)
	}
	if declared != "[]" {
		t.Fatalf("approved cut invented scope %s", declared)
	}
}

// TestGoverningRequirementCoveredCapturePassesUngated proves the gate is scoped:
// declaring the applicable requirement captures without approval, so the
// mechanism refuses omissions rather than taxing every capture.
func TestGoverningRequirementCoveredCapturePassesUngated(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	if err := pm1fixture.SeedGoverningRequirement(context.Background(), s, "proj-web", "audit_required", "accepted audit obligation"); err != nil {
		t.Fatalf("seed governing requirement: %v", err)
	}
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	input := []byte(`{"title":"Add passkey login with audit","value_statement":"reducing account takeover risk","kind":"task","project_ids":["proj-web"],"governing_requirements":["audit_required"],"idempotency_key":"covered-1"}`)
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("covered capture was refused outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
}

func TestCaptureRefusesUnknownGoverningRequirement(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")
	input := []byte(`{"title":"Unknown requirement","value_statement":"reject an invalid governing reference","kind":"task","project_ids":["proj-web"],"governing_requirements":["fabricated_requirement"],"idempotency_key":"unknown-requirement-1"}`)
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if resp.Outcome != agentcontract.OutcomeError || resp.Error == nil {
		t.Fatalf("unknown requirement capture outcome=%s error=%+v, want typed refusal", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "unknown_scope" {
		t.Fatalf("error.kind=%q, want unknown_scope", resp.Error.Kind)
	}
	if !strings.Contains(resp.Error.Message, "fabricated_requirement") {
		t.Fatalf("refusal message %q does not name unknown id", resp.Error.Message)
	}
	if !strings.Contains(resp.Error.Message, "declare the requirement on the Project or name an accepted law ID") {
		t.Fatalf("refusal message %q does not carry the one-line recovery", resp.Error.Message)
	}
	if resp.Error.RecoveryAction.Kind != "reread_entities" {
		t.Fatalf("recovery action %q, want reread_entities so the caller can act on the refusal", resp.Error.RecoveryAction.Kind)
	}
}

// TestCaptureAcceptsRequirementFromSiblingProjectOfTheProduct proves the
// resolution target is the Product, not the capture scope alone: a requirement
// registered on another Project of the same Product resolves, so a capture
// that legitimately declares it is not refused.
func TestCaptureAcceptsRequirementFromSiblingProjectOfTheProduct(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	if err := pm1fixture.SeedGoverningRequirement(context.Background(), s, "proj-api", "cross_project_review", "sibling Project obligation"); err != nil {
		t.Fatalf("seed governing requirement: %v", err)
	}
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	input := []byte(`{"title":"Sibling obligation","value_statement":"declaring a sibling Project requirement","kind":"task","project_ids":["proj-web"],"governing_requirements":["cross_project_review"],"idempotency_key":"sibling-requirement-1"}`)
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("sibling-requirement capture was refused outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
}

// TestCaptureAcceptsAcceptedLawID proves the second legitimate referencing
// surface: an accepted committed law visible for the Product's Projects
// resolves without being registered as a Project requirement.
func TestCaptureAcceptsAcceptedLawID(t *testing.T) {
	t.Parallel()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	if _, err := pm1fixture.SeedCommittedProductDomain(context.Background(), s, "prod-alpha", "proj-web", t.TempDir()); err != nil {
		t.Fatalf("seed accepted law: %v", err)
	}
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	input := []byte(`{"title":"Law-bound capture","value_statement":"declaring an accepted law id","kind":"task","project_ids":["proj-web"],"governing_requirements":["CD-0001"],"idempotency_key":"accepted-law-1"}`)
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("accepted-law capture was refused outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
}

// ---------------------------------------------------------------------------
// AJ8 — budget refusal (CD-0038)
// ---------------------------------------------------------------------------

// bindAJ8BudgetRefused proves the seconds ceiling is real: an approved audit
// requesting 60 seconds against workflow_action's TS1-fixed 30 refuses before
// any effect, carries the typed ceiling, and never clamps. The clamp probe is
// the retry: a refused request records no idempotency effect, so the same key
// reused with an admissible budget executes for real. A silent clamp would
// surface there as a replay or as a fabricated completion.
func bindAJ8BudgetRefused(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web")

	workID := "work-budget-audit"
	definition := store.BuiltinWorkflowDefinitions()[0]
	registered, err := store.BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	created := store.Event{EventID: workID + "-created", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Run the approved audit","priority":3}`)}
	membership := store.Event{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"proj-web","role":"primary"}],"expected_version":1,"resulting_version":2}`)}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{created, membership}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): 0}}); err != nil {
		t.Fatalf("seed work: %v", err)
	}
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: store.WorkflowActor{PrincipalRef: "human-operator", ClientRef: "client-mutation", AgentRef: "agent-engineer", SessionRef: "session-mutation", ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize workflow: %v", err)
	}
	_, version := readWorkFromStore(t, s, workID)
	actionID := definition.AvailableActions[0]
	key := "aj8-budget-1"
	if seed, ok := sc.InitialState["idempotency_seed"].(string); ok && seed != "" {
		key = seed
	}

	overBudget := []byte(fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"action_id":%q,"fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"requested_budget_seconds":60,"idempotency_key":%q}`, workID, version, actionID, key))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: overBudget}, env)
	if resp.Outcome != agentcontract.OutcomeError || resp.Error == nil || resp.Error.Kind != "budget_refused" {
		t.Fatalf("60-second request was not refused outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.SupportedBudgetSeconds != 30 {
		t.Fatalf("refusal ceiling=%d, want the TS1-fixed 30", resp.Error.SupportedBudgetSeconds)
	}
	if resp.Error.EffectState != agentcontract.EffectNone {
		t.Fatalf("refusal claims effect state %q", resp.Error.EffectState)
	}
	if resp.Replayed {
		t.Fatal("a refused request must not be marked as a replay")
	}

	// The clamp probe, first half: the refusal changed nothing durably.
	_, versionAfterRefusal := readWorkFromStore(t, s, workID)
	if versionAfterRefusal != version {
		t.Fatalf("refusal bumped work version %d -> %d", version, versionAfterRefusal)
	}

	// The clamp probe, second half: the same idempotency key with an
	// admissible budget executes for real. A silent clamp would have consumed
	// the key or fabricated a completion; either fails here.
	withinBudget := []byte(fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"action_id":%q,"fields":{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]},"requested_budget_seconds":30,"idempotency_key":%q}`, workID, version, actionID, key))
	retry := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: withinBudget}, env)
	if retry.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("lowered-budget retry on the same key was refused outcome=%s err=%+v", retry.Outcome, retry.Error)
	}
	if retry.Replayed {
		t.Fatal("retry was served as a replay: the refusal recorded an idempotency effect")
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"operation": map[string]any{"started": false},
	}
	obs.Effects = map[string]any{
		"refused_before_effect":             true,
		"atomic_core_effect_zero":           true,
		"budget_retry_executed":             true,
		"work_version_unchanged_by_refusal": true,
		// The active absence probe: the refusal response was an error, the
		// work version did not move, and the same idempotency key executed
		// for real at an admissible budget. A clamp would have been a
		// success envelope, a version bump, or a replay — each is excluded
		// above, so the absence is observed, not assumed.
		"silent_budget_clamp": probedAbsent{Evidence: "refused without version change; same key executed at 30s without replay"},
	}
	obs.Authority["budget_refusal_typed"] = true
	return obs
}

// ---------------------------------------------------------------------------
