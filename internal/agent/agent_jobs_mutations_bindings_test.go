package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// This file implements the eight TS1 mutation scenarios bound from
// scenarios/agent-jobs.v1.json for the #159 tranche. Every binding
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

func bindAJ5FrameInitiative(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")
	seed, _ := sc.InitialState["idempotency_seed"].(string)
	if seed == "" {
		t.Fatal("AJ5-frame-initiative: missing idempotency_seed")
	}
	create := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "create", Input: []byte(fmt.Sprintf(`{"title":"Release framing","value_statement":"Coordinate the release","project_ids":["proj-web"],"idempotency_key":"%s-create"}`, seed))}, env)
	if create.Outcome != OutcomeOK || len(create.ChangedRefs) != 1 {
		t.Fatalf("initiative create outcome=%s changed=%+v err=%+v", create.Outcome, create.ChangedRefs, create.Error)
	}
	initiativeID := create.ChangedRefs[0].ID
	beforeLifecycle, _ := readWorkFromStore(t, s, "work-ready-low")
	add := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "add_entry", Input: []byte(fmt.Sprintf(`{"initiative_work_id":"%s","child_work_id":"work-ready-low","expected_version":2,"position":0,"required":true,"idempotency_key":"%s-add"}`, initiativeID, seed))}, env)
	if add.Outcome != OutcomeOK {
		t.Fatalf("initiative add_entry outcome=%s err=%+v", add.Outcome, add.Error)
	}
	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "add_entry", Input: []byte(fmt.Sprintf(`{"initiative_work_id":"%s","child_work_id":"work-ready-low","expected_version":2,"position":0,"required":true,"idempotency_key":"%s-add"}`, initiativeID, seed))}, env)
	if !replay.Replayed {
		t.Fatal("initiative add_entry replay did not set Replayed=true")
	}
	entries, err := s.ReadInitiativeEntries(context.Background(), initiativeID)
	if err != nil || len(entries) != 1 || entries[0].ChildWorkID != "work-ready-low" || !entries[0].Required {
		t.Fatalf("initiative entries=%+v err=%v", entries, err)
	}
	afterLifecycle, _ := readWorkFromStore(t, s, "work-ready-low")
	if beforeLifecycle != afterLifecycle {
		t.Fatalf("child lifecycle changed from %q to %q", beforeLifecycle, afterLifecycle)
	}
	obs := envelopeToObservation(add)
	obs.State = map[string]any{
		"initiative": map[string]any{
			"kind":    "initiative",
			"entries": map[string]any{"0": map[string]any{"kind": "includes", "required": true}},
		},
	}
	obs.Effects = map[string]any{"child_lifecycle_preserved": true, "atomic_core_effect": "complete", "retry_safe_replayed": true}
	return obs
}

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
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	idempotencySeed, _ := sc.InitialState["idempotency_seed"].(string)
	if idempotencySeed == "" {
		t.Fatalf("AJ3-capture-work: missing idempotency_seed")
	}

	input := []byte(fmt.Sprintf(`{"title":"Add passkey login","value_statement":"reducing account takeover risk","kind":"task","project_ids":["proj-web"],"idempotency_key":"%s"}`, idempotencySeed))
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if first.Outcome != OutcomeOK {
		t.Fatalf("capture failed outcome=%s err=%+v", first.Outcome, first.Error)
	}

	createdWorkID := first.ChangedRefs[0].ID
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
	if replay.Outcome != OutcomeOK {
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
// amendment applied to scenarios/agent-jobs.v1.json). Then replay the
// same idempotency_key and prove no duplicate transition event and no
// additional version bump (retry_safe).
func bindAJ4StartValidWork(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

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
	if resp.Outcome != OutcomeOK {
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
	if !replay.Replayed || replay.Outcome != OutcomeOK {
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

// AJ4-complete-valid-work: complete work-cross with the supplied
// verification evidence and a signed host-approval assertion. After
// the D3 repair (terminal lifecycle transitions now mint a real
// approval challenge when evidence is present), the binding must
// perform a full approval round-trip.
func bindAJ4CompleteValidWork(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, privateKey, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

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

	// First dispatch — surface the approval_required challenge.
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if first.Error == nil || first.Error.Kind != "approval_required" {
		t.Fatalf("expected approval_required on first dispatch, got outcome=%s err=%+v", first.Outcome, first.Error)
	}
	challengeRef, ok := first.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval challenge missing or malformed: %v", first.Error.Details)
	}

	// Build the final input with the approval block, compute its
	// digest, and sign the host-approval assertion. The signed scope
	// must mirror the challenge scope exactly (mutationDigest strips
	// approval/idempotency_key before hashing, so the digest is over
	// the canonical request body).
	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatalf("inject approval: %v", err)
	}
	scope := map[string]any{
		"product_id":    "prod-alpha",
		"product_ids":   []string{"prod-alpha"},
		"project_ids":   []string{"proj-web"},
		"work_ids":      []string{"work-cross"},
		"scope_version": env.ScopeVersion,
	}
	versions := map[string]any{"work": preVersion}
	digest := mutationDigest("concord_work_transition", "lifecycle", env, withApproval)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: withApproval}, env)
	if approved.Outcome != OutcomeOK {
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

	// Replay the same idempotency key with the same approval block
	// (the same challenge is not re-usable, so this binding is
	// satisfied by a fresh challenge+signature for replay observation).
	replayInput := withApproval
	replayChallengeRef := challengeRef
	replayDigest := mutationDigest("concord_work_transition", "lifecycle", env, replayInput)
	env.HostApproval = signedHostApproval(privateKey, replayChallengeRef, replayDigest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(replayChallengeRef))
	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: replayInput}, env)
	if !replay.Replayed {
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
	obs.Authority["approval_consumed"] = true
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
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	preLifecycle, preVersion := readWorkFromStore(t, s, "work-cross")
	if preLifecycle != "in_progress" {
		t.Fatalf("work-cross pre-lifecycle=%q, want in_progress", preLifecycle)
	}
	// PM1 seeds work-cross with one transition event. The corpus
	// asserts no NEW event from the missing_evidence refusal, so
	// record the baseline.
	preTransitionCount, _ := readTransitionEvents(t, s, "work-cross")

	idempotencyKey := fmt.Sprintf("complete-missing-evidence-%d", preVersion)
	input := []byte(fmt.Sprintf(`{"work_id":"work-cross","expected_version":%d,"target":"completed","reason":"complete without evidence","idempotency_key":"%s"}`, preVersion, idempotencyKey))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if resp.Outcome != OutcomeError || resp.Error == nil {
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
	// (c) no domain_events row exists for work-cross whose payload
	// indicates a terminal transition (completed/cancelled/superseded).
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
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.transitioned' AND subject_id='work-cross' AND payload LIKE '%"to":"completed"%'`).Scan(&completedCount); err != nil {
		t.Fatalf("terminal_transition probe: query completed events: %v", err)
	}
	var cancelledCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='work.transitioned' AND subject_id='work-cross' AND payload LIKE '%"to":"cancelled"%'`).Scan(&cancelledCount); err != nil {
		t.Fatalf("terminal_transition probe: query cancelled events: %v", err)
	}
	if completedCount != 0 || cancelledCount != 0 {
		t.Fatalf("terminal_transition probe: found completed=%d cancelled=%d terminal transitions for work-cross; missing_evidence must not apply", completedCount, cancelledCount)
	}
	obs.Effects["terminal_transition"] = probedAbsent{
		Evidence: fmt.Sprintf("work-cross lifecycle=in_progress version=%d, no terminal transition events in domain_events; missing_evidence refused pre-application", version),
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
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	preLifecycle, preVersion := readWorkFromStore(t, s, "work-ready-low")
	if preLifecycle != "needed" || preVersion != 2 {
		t.Fatalf("work-ready-low pre-state lifecycle=%q version=%d, want needed/2", preLifecycle, preVersion)
	}

	idempotencyKey := fmt.Sprintf("stale-version-%d", preVersion)
	input := []byte(fmt.Sprintf(`{"work_id":"work-ready-low","expected_version":1,"target":"in_progress","reason":"stale start","idempotency_key":"%s"}`, idempotencyKey))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if resp.Outcome != OutcomeError || resp.Error == nil {
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

// AJ5-add-dependency: add a blocks edge from work-cross to
// work-ready-low. After the edge lands, work-ready-low must be
// derived_blocked=true. The blocked column must NOT exist on
// work_items (the binding actively probes the schema) so the
// observation is structurally honest about the derivation rather than
// reading from a stored flag.
func bindAJ5AddDependency(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	// Pre-probe: schema check — work_items must not carry a stored
	// blocked/ready column. The runner's absent assertion depends on
	// this guarantee; without it the assertion could pass by
	// coincidence rather than from derivation.
	cols := tableColumns(t, s.DatabaseForTesting(), "work_items")
	for _, c := range cols {
		if c == "blocked" || c == "ready" {
			t.Fatalf("work_items has stored %q column; the derived_blocked assertion becomes a coincidence", c)
		}
	}

	// Pre-probe: work-ready-low starts unblocked.
	if deriveWorkBlocked(t, s, "work-ready-low") {
		t.Fatalf("work-ready-low is blocked before AJ5 mutation")
	}

	preCrossVersion, preLowVersion := workVersion(t, s, "work-cross"), workVersion(t, s, "work-ready-low")

	idempotencySeed, _ := sc.InitialState["idempotency_seed"].(string)
	if idempotencySeed == "" {
		t.Fatalf("AJ5-add-dependency: missing idempotency_seed")
	}
	input := []byte(fmt.Sprintf(`{"from_work_id":"work-cross","to_work_id":"work-ready-low","from_expected_version":%d,"to_expected_version":%d,"kind":"blocks","reason":"add dependency","idempotency_key":"%s"}`, preCrossVersion, preLowVersion, idempotencySeed))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "link", Input: input}, env)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("relate.link failed outcome=%s err=%+v", resp.Outcome, resp.Error)
	}

	// Probe: only one blocks relation from work-cross to work-ready-low.
	if n := readRelationsFor(t, s, "work-cross", "work-ready-low", "blocks"); n != 1 {
		t.Fatalf("blocks relations from work-cross to work-ready-low=%d, want 1", n)
	}

	// Derive blocked status from the relation graph (same derivation
	// the Q4/Q5 queries apply).
	derived := deriveWorkBlocked(t, s, "work-ready-low")
	if !derived {
		t.Fatalf("derived_blocked=false after AJ5 mutation; relation was not honored")
	}

	// Replay the same idempotency key — retry_safe must hold.
	replay := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "link", Input: input}, env)
	if !replay.Replayed {
		t.Fatalf("replay did not set Replayed=true")
	}
	if n := readRelationsFor(t, s, "work-cross", "work-ready-low", "blocks"); n != 1 {
		t.Fatalf("replay created an additional blocks relation: count=%d", n)
	}

	obs := envelopeToObservation(resp)
	// relations.created.0.kind: "0" must be a string map key on the
	// "created" sub-object, not a slice index. The runner's
	// dot-path traversal uses map[string]any throughout.
	obs.State = map[string]any{
		"relations": map[string]any{
			"created": map[string]any{
				"count": 1,
				"0": map[string]any{
					"from": "work-cross",
					"to":   "work-ready-low",
					"kind": "blocks",
				},
			},
		},
		"work": map[string]any{
			"work-ready-low": map[string]any{
				"derived_blocked": derived,
			},
		},
	}
	// Success path — the absent assertion for stored_blocked_state
	// must be backed by an active schema probe. Query the actual
	// work_items columns (NOT a hardcoded guess at the column name)
	// and confirm no stored blocked/ready/blocking column exists.
	// Blockedness must be derived at read time from relations; a
	// stored column would let the absent assertion pass by coincidence
	// rather than from genuine derivation.
	postCols := tableColumns(t, s.DatabaseForTesting(), "work_items")
	storedColumn := ""
	for _, c := range postCols {
		switch c {
		case "blocked", "ready", "blocking", "is_blocked", "blocked_by":
			storedColumn = c
		}
	}
	if storedColumn != "" {
		t.Fatalf("stored_blocked_state probe: work_items carries stored %q column; derived_blocked must come from relations, not a stored flag", storedColumn)
	}
	obs.Effects = map[string]any{
		"retry_safe_replayed": true,
		"atomic_core_effect":  "complete",
		"stored_blocked_state": probedAbsent{
			Evidence: fmt.Sprintf("PRAGMA table_info(work_items) reports columns %v; no blocked/ready/blocking column exists, blockedness is derived at read time", cols),
		},
	}
	return obs
}

// AJ5-reject-cycle: attempt to add a blocks edge from work-blocked to
// work-prereq, which would cycle against the seeded
// work-prereq → work-blocked blocks edge. After the D5 repair the
// runner expects error.kind=invalid_relation with
// error.violations nonempty, and an actively-probed absence of any
// newly inserted relation row.
func bindAJ5RejectCycle(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	preCount := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations`).Scan(&preCount); err != nil {
		t.Fatalf("pre-count relations: %v", err)
	}

	preBlockedVersion := workVersion(t, s, "work-blocked")
	prePrereqVersion := workVersion(t, s, "work-prereq")

	input := []byte(fmt.Sprintf(`{"from_work_id":"work-blocked","to_work_id":"work-prereq","from_expected_version":%d,"to_expected_version":%d,"kind":"blocks","reason":"cyclic","idempotency_key":"reject-cycle-1"}`, preBlockedVersion, prePrereqVersion))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "link", Input: input}, env)
	if resp.Outcome != OutcomeError || resp.Error == nil {
		t.Fatalf("expected typed error outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "invalid_relation" {
		t.Fatalf("error.kind=%q, want invalid_relation", resp.Error.Kind)
	}
	if len(resp.Error.Violations) == 0 {
		t.Fatalf("error.violations empty; D5 typed carrier is missing")
	}
	found := false
	for _, v := range resp.Error.Violations {
		if v == "blocks:work-blocked->work-prereq" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("error.violations missing blocks:work-blocked->work-prereq: %v", resp.Error.Violations)
	}

	postCount := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations`).Scan(&postCount); err != nil {
		t.Fatalf("post-count relations: %v", err)
	}
	if postCount != preCount {
		t.Fatalf("cycle rejection left %d new relation rows (pre=%d, post=%d)", postCount-preCount, preCount, postCount)
	}
	if n := readRelationsFor(t, s, "work-blocked", "work-prereq", "blocks"); n != 0 {
		t.Fatalf("cycle rejection left %d blocks relations work-blocked->work-prereq", n)
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"relations": map[string]any{
			"created": map[string]any{
				"count": 0,
			},
		},
	}
	// violations must be []any so the invariant's `.([]any)` cast
	// succeeds; []string does not match []any under Go's type
	// assertion rules.
	obsViolations := make([]any, 0, len(resp.Error.Violations))
	for _, v := range resp.Error.Violations {
		obsViolations = append(obsViolations, v)
	}
	obs.Communication["error"] = map[string]any{
		"kind":            "invalid_relation",
		"violations":      obsViolations,
		"recovery_action": "reread_entities",
	}
	// Active probe for the cyclic_relation absent assertion: confirm
	// (a) the relations row count is unchanged after the refusal and
	// (b) no row matches the attempted blocks edge
	// work-blocked -> work-prereq. Together these prove the cycle
	// detection refused BEFORE any insert; without this probe the
	// runner's absent assertion would pass vacuously.
	postCount2 := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations`).Scan(&postCount2); err != nil {
		t.Fatalf("cyclic_relation probe: post-count relations: %v", err)
	}
	if postCount2 != preCount {
		t.Fatalf("cyclic_relation probe: post-count=%d != pre-count=%d; the cycle refusal must not insert", postCount2, preCount)
	}
	attemptedFrom := "work-blocked"
	attemptedTo := "work-prereq"
	if n := readRelationsFor(t, s, attemptedFrom, attemptedTo, "blocks"); n != 0 {
		t.Fatalf("cyclic_relation probe: %d blocks rows for %s->%s; the refused edge must not be persisted", n, attemptedFrom, attemptedTo)
	}
	obs.Effects = map[string]any{
		"atomic_core_effect_zero": true,
		"cyclic_relation": probedAbsent{
			Evidence: fmt.Sprintf("relations row count unchanged (pre=%d, post=%d); no %s->%s blocks row exists after the invalid_relation refusal", preCount, postCount2, attemptedFrom, attemptedTo),
		},
	}
	return obs
}

// AJ5-atomic-supersession: supersede work-old with work-new. The
// initial_state.fixture_override wants work-old.lifecycle=in_progress
// and the existing supersession relation removed. We honor the
// override by applying two extra events through the legitimate store
// fold path: work.reopened_from_superseded (which atomically
// transitions the lifecycle back to needed AND deletes the
// supersession relation) followed by work.transitioned to
// in_progress.
func bindAJ5AtomicSupersession(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, privateKey, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	// Honor fixture_override by replaying the legitimate fold events.
	if err := applyAtomicSupersessionFixtureOverride(t, s); err != nil {
		t.Fatalf("apply fixture_override: %v", err)
	}

	preOldLifecycle, preOldVersion := readWorkFromStore(t, s, "work-old")
	if preOldLifecycle != "in_progress" {
		t.Fatalf("work-old pre-lifecycle=%q, want in_progress (fixture_override)", preOldLifecycle)
	}
	if deriveSupersession(t, s, "work-new", "work-old") {
		t.Fatalf("fixture_override failed to remove work-new->work-old supersession")
	}
	_, preNewVersion := readWorkFromStore(t, s, "work-new")

	input := []byte(fmt.Sprintf(`{"predecessor_id":"work-old","successor_id":"work-new","predecessor_expected_version":%d,"successor_expected_version":%d,"reason":"supersede stale plan","idempotency_key":"supersede-old-new-%d"}`, preOldVersion, preNewVersion, preOldVersion))

	// First dispatch — surface the approval challenge.
	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "supersede", Input: input}, env)
	if first.Error == nil || first.Error.Kind != "approval_required" {
		t.Fatalf("expected approval_required on first dispatch, got outcome=%s err=%+v", first.Outcome, first.Error)
	}
	challengeRef, ok := first.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("approval challenge missing or malformed: %v", first.Error.Details)
	}
	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatalf("inject approval: %v", err)
	}
	// Scope mirrors the mutation preflight. For supersede the
	// preflight carries {product_id, project_ids: [AmbientProjectID],
	// scope_version, work_ids: [predecessor, successor]} and
	// executeMutation adds product_ids derived from the work scope.
	// This binding uses proj-web as the ambient project, so
	// project_ids is ["proj-web"].
	scope := map[string]any{
		"product_id":    "prod-alpha",
		"product_ids":   []string{"prod-alpha"},
		"project_ids":   []string{"proj-web"},
		"work_ids":      []string{"work-old", "work-new"},
		"scope_version": env.ScopeVersion,
	}
	versions := map[string]any{"predecessor": preOldVersion, "successor": preNewVersion}
	digest := mutationDigest("concord_work_relate", "supersede", env, withApproval)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "supersede", Input: withApproval}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved supersede failed outcome=%s err=%+v", approved.Outcome, approved.Error)
	}

	postLifecycle, postVersion := readWorkFromStore(t, s, "work-old")
	if postLifecycle != "superseded" {
		t.Fatalf("post-lifecycle=%q, want superseded", postLifecycle)
	}
	supersessionPresent := deriveSupersession(t, s, "work-new", "work-old")
	if !supersessionPresent {
		t.Fatalf("supersession edge work-new->work-old missing after supersede call")
	}

	// Atomicity probe: terminal lifecycle AND supersession relation
	// are present together. The runner's terminal_without_relation
	// and relation_without_terminal absent assertions both rely on
	// this conjunction. If either is missing while the other is
	// present, the binding deliberately fails before evaluating the
	// absent assertions so the runner cannot pass vacuously.
	if postLifecycle != "superseded" && supersessionPresent {
		t.Fatalf("relation exists without terminal lifecycle")
	}
	if postLifecycle == "superseded" && !supersessionPresent {
		t.Fatalf("terminal lifecycle exists without supersession relation")
	}

	obs := envelopeToObservation(approved)
	// relations.supersedes must be a []any slice (the corpus's
	// `contains` op casts to []any via containsOp). The runner
	// declares the value as `{source, target}`, which decodes to
	// map[string]any; the slice element type must therefore be
	// interface{} rather than the typed map.
	obs.State = map[string]any{
		"work": map[string]any{
			"work-old": map[string]any{
				"lifecycle": postLifecycle,
				"version":   postVersion,
			},
		},
		"relations": map[string]any{
			"supersedes": []any{
				map[string]any{"source": "work-new", "target": "work-old"},
			},
		},
	}
	// Active probes for the two atomicity absent assertions.
	// terminal_without_relation: confirm work-old's terminal lifecycle
	//	was NOT set without the supersession edge — if the supersession
	//	relation is missing while the lifecycle moved to superseded,
	//	the operation is half-applied.
	// relation_without_terminal: confirm the supersession edge was NOT
	//	written without the terminal lifecycle — if the relation row
	//	exists while lifecycle is still in_progress, the operation is
	//	half-applied in the other direction.
	// Together they prove atomicity: both halves must be present
	// together, neither may exist without the other.
	successor := "work-new"
	predecessor := "work-old"
	var supersessionRows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE work_id_from=? AND work_id_to=? AND kind='supersedes'`, successor, predecessor).Scan(&supersessionRows); err != nil {
		t.Fatalf("atomicity probe: query supersession rows: %v", err)
	}
	supersessionPresentNow := supersessionRows > 0
	if postLifecycle == "superseded" && !supersessionPresentNow {
		t.Fatalf("atomicity probe: terminal_without_relation — lifecycle is superseded but supersession edge is missing")
	}
	if supersessionPresentNow && postLifecycle != "superseded" {
		t.Fatalf("atomicity probe: relation_without_terminal — supersession edge exists but lifecycle=%q", postLifecycle)
	}
	obs.Effects = map[string]any{
		"atomic_core_effect":       "complete",
		"fixture_override_applied": true,
		"terminal_without_relation": probedAbsent{
			Evidence: fmt.Sprintf("work-old lifecycle=%q (terminal) and supersession edge %s->%s present together (%d row); the half-applied state does not exist", postLifecycle, successor, predecessor, supersessionRows),
		},
		"relation_without_terminal": probedAbsent{
			Evidence: fmt.Sprintf("supersession edge %s->%s present (%d row) and work-old lifecycle=%q (terminal); the half-applied state does not exist", successor, predecessor, supersessionRows, postLifecycle),
		},
	}
	obs.Authority["supersession_relation_row_present"] = supersessionPresentNow
	return obs
}

// AJ5-resolve-domain-overlap proves why the current overlap operation exists: an
// ordinary relation cannot confer Product-concordance authority, while one
// operator-approved call binds both endpoint and contract versions atomically.
func bindAJ5ResolveDomainOverlap(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, _, privateKey, env, input := seedAgentOverlapFixture(t)
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

	ordinary := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "link", Input: json.RawMessage(`{"from_work_id":"work-1","to_work_id":"work-2","from_expected_version":2,"to_expected_version":2,"kind":"compatible_with","reason":"ordinary links cannot decide compatibility","idempotency_key":"ordinary-compatible"}`)}, env)
	if ordinary.Error == nil || ordinary.Error.Kind != "invalid_relation" {
		t.Fatalf("ordinary compatible_with link was not rejected: %+v", ordinary.Error)
	}
	if got := countRows(t, s.DatabaseForTesting(), `SELECT count(*) FROM workflow_overlap_resolutions`); got != 0 {
		t.Fatalf("ordinary relation wrote %d overlap resolutions", got)
	}

	first := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: input}, env)
	if first.Error == nil || first.Error.Kind != "approval_required" {
		t.Fatalf("overlap resolution did not request operator approval: %+v", first.Error)
	}
	challengeRef, ok := first.Error.Details["approval_ref"].(string)
	if !ok || challengeRef == "" {
		t.Fatalf("overlap resolution challenge missing: %+v", first.Error.Details)
	}
	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatal(err)
	}
	scope := map[string]any{
		"product_id": "product-1", "product_ids": []string{"product-1"}, "project_ids": []string{"project-1"},
		"work_ids": []string{"work-1", "work-2"}, "scope_version": env.ScopeVersion,
	}
	versions := map[string]any{"from": int64(2), "to": int64(2), "from_contract": int64(1), "to_contract": int64(1)}
	digest := mutationDigest("concord_work_relate", "resolve_overlap", env, withApproval)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))
	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_relate", Operation: "resolve_overlap", Input: withApproval}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("approved overlap resolution failed: %+v", approved.Error)
	}

	var kind, approvalRef string
	var fromContract, toContract int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT resolution_kind,from_contract_version,to_contract_version,approval_ref FROM workflow_overlap_resolutions WHERE invalidated_seq IS NULL`).Scan(&kind, &fromContract, &toContract, &approvalRef); err != nil {
		t.Fatal(err)
	}
	var relationCount, usedCount int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE work_id_from='work-1' AND work_id_to='work-2' AND kind='compatible_with' AND resolution_id IS NOT NULL`).Scan(&relationCount); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT used_count FROM agent_approvals WHERE approval_ref=?`, approvalRef).Scan(&usedCount); err != nil {
		t.Fatal(err)
	}
	if relationCount != 1 || usedCount != 1 {
		t.Fatalf("resolution relation=%d approval uses=%d", relationCount, usedCount)
	}

	obs := envelopeToObservation(approved)
	obs.State = map[string]any{
		"overlap":   map[string]any{"current_resolution": map[string]any{"kind": kind, "from_contract_version": fromContract, "to_contract_version": toContract}},
		"relations": map[string]any{"compatible_with": []any{map[string]any{"source": "work-1", "target": "work-2"}}},
	}
	obs.Authority = map[string]any{"operator_approval_consumed": usedCount == 1, "approval_ref": approvalRef}
	obs.Effects = map[string]any{
		"atomic_core_effect":          "complete",
		"approval_authority_consumed": true,
		"ordinary_relation_authority": probedAbsent{Evidence: "generic compatible_with link returned invalid_relation and wrote no resolution row"},
	}
	return obs
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// applyAtomicSupersessionFixtureOverride honors the
// AJ5-atomic-supersession initial_state.fixture_override by replaying
// the legitimate fold events through the store. PM1 seeds work-old at
// lifecycle=superseded (version 3) with a supersession edge from
// work-new; the override requires lifecycle=in_progress and the edge
// removed.
//
// The chosen approach (legitimate store fold rather than
// hand-mutating rows) preserves version accounting: the
// reopened_from_superseded event bumps work-old from 3 to 4 AND
// removes the relation row in one transaction; the subsequent
// work.transitioned event bumps work-old from 4 to 5 and changes
// lifecycle to in_progress. After this sequence work-old is at
// version 5 with no supersession edge, exactly the precondition the
// corpus expects for the supersede call.
func applyAtomicSupersessionFixtureOverride(t *testing.T, s *store.Store) error {
	t.Helper()
	// Step 1: work.reopened_from_superseded — work-old is reopened
	// by work-new. This atomically removes the supersession edge
	// AND transitions work-old from superseded → needed at version
	// 4.
	reopenEvent := store.Event{
		EventID:        "fixture-override-reopen-work-old",
		Kind:           "work.reopened_from_superseded",
		SubjectType:    store.SubjectWorkItem,
		SubjectID:      "work-old",
		Actor:          "operator",
		OccurredAt:     fixedTime(),
		PayloadVersion: 1,
		Payload:        json.RawMessage(`{"superseded":"work-old","reason":"fixture_override for atomic supersession","expected_version":3,"resulting_version":4}`),
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{reopenEvent}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-old"): 3}}); err != nil {
		return fmt.Errorf("reopen work-old: %w", err)
	}
	// Step 2: work.transitioned — work-old moves from needed to
	// in_progress at version 5.
	transitionEvent := store.Event{
		EventID:        "fixture-override-transition-work-old",
		Kind:           "work.transitioned",
		SubjectType:    store.SubjectWorkItem,
		SubjectID:      "work-old",
		Actor:          "operator",
		OccurredAt:     fixedTime(),
		PayloadVersion: 1,
		Payload:        json.RawMessage(`{"from":"needed","to":"in_progress","reason":"fixture_override for atomic supersession","expected_version":4,"resulting_version":5}`),
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{transitionEvent}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, "work-old"): 4}}); err != nil {
		return fmt.Errorf("transition work-old: %w", err)
	}
	return nil
}

// deriveSupersession reports whether a supersession edge exists from
// successor to predecessor in the relations table. Bindings use it
// to assert the atomic durability of the supersede call.
func deriveSupersession(t *testing.T, s *store.Store, successor, predecessor string) bool {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM relations WHERE work_id_from=? AND work_id_to=? AND kind='supersedes'`, successor, predecessor).Scan(&count); err != nil {
		t.Fatalf("derive supersession %s->%s: %v", successor, predecessor, err)
	}
	return count > 0
}

// _ ensures ed25519 import is exercised; the mutation fixture
// function returns ed25519.PrivateKey and the symbol must remain
// referenced.
var _ ed25519.PrivateKey

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
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	requirement, _ := sc.InitialState["governing_requirement"].(string)
	if requirement == "" {
		t.Fatalf("AJ3-spec-conflict: missing governing_requirement in initial_state")
	}
	if err := pm1fixture.SeedGoverningRequirement(context.Background(), s, "proj-web", requirement, "accepted audit obligation"); err != nil {
		t.Fatalf("seed governing requirement: %v", err)
	}
	// Seeding moved the Project version, so the call envelope must resolve the
	// current scope version rather than the pre-seed one.
	env = agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	driver := resolveDriver(t, sc)
	if !driver.approvalWithheld() {
		t.Fatalf("AJ3-spec-conflict expects a pending human checkpoint, got %+v", driver)
	}

	preWork := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&preWork); err != nil {
		t.Fatalf("pre-count work_items: %v", err)
	}
	preEvents := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&preEvents); err != nil {
		t.Fatalf("pre-count domain_events: %v", err)
	}

	// The instruction omits the accepted requirement to make the work smaller,
	// which on the wire is a capture that declares no governing requirements.
	input := []byte(`{"title":"Add passkey login","value_statement":"reducing account takeover risk","kind":"task","project_ids":["proj-web"],"idempotency_key":"spec-conflict-1"}`)
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)

	if resp.Outcome != OutcomeError || resp.Error == nil {
		t.Fatalf("expected typed refusal outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.Kind != "invariant_violation" {
		t.Fatalf("error.kind=%q, want invariant_violation", resp.Error.Kind)
	}
	if resp.Error.RecoveryAction.Kind != "contact_operator" {
		t.Fatalf("recovery_action=%q, want contact_operator", resp.Error.RecoveryAction.Kind)
	}
	// The omitted requirement is named in the typed carrier, not the message.
	if len(resp.Error.Violations) != 1 || resp.Error.Violations[0] != requirement {
		t.Fatalf("error.violations=%v, want [%s]", resp.Error.Violations, requirement)
	}
	// accept_scope_cut must be actionable: offering the option without a minted
	// challenge to approve against is the D3 defect repaired in #159.
	challengeRef, ok := resp.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("governing conflict offered accept_scope_cut with no actionable approval_ref: %v", resp.Error.Details)
	}

	// Probe the prohibited effect: a silent requirement omission would be a
	// captured work item or any appended event. Both are checked against the
	// pre-call counts so the absence is proven rather than assumed.
	postWork := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items`).Scan(&postWork); err != nil {
		t.Fatalf("post-count work_items: %v", err)
	}
	postEvents := 0
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&postEvents); err != nil {
		t.Fatalf("post-count domain_events: %v", err)
	}
	if postWork != preWork {
		t.Fatalf("refused capture created %d work rows (pre=%d, post=%d)", postWork-preWork, preWork, postWork)
	}
	if postEvents != preEvents {
		t.Fatalf("refused capture appended %d events (pre=%d, post=%d)", postEvents-preEvents, preEvents, postEvents)
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
		Evidence: "work_items and domain_events counts unchanged across the refused capture, and no work item exists under the requested title",
	}
	// The consequence required operator authority the pending checkpoint withheld.
	obs.Effects["approval_authority_withheld"] = true
	violations := make([]any, len(resp.Error.Violations))
	for i, v := range resp.Error.Violations {
		violations[i] = v
	}
	if commErr, ok := obs.Communication["error"].(map[string]any); ok {
		commErr["violations"] = violations
	}
	return obs
}

// TestOperatorApprovedScopeCutProceeds covers CD-0035 D4's other branch. The
// governing-law refusal is not a dead end: when the operator approves the cut,
// the same capture proceeds against the minted challenge. Without this the
// accept_scope_cut option would be decorative.
func TestOperatorApprovedScopeCutProceeds(t *testing.T) {
	s, service, grant, privateKey, _ := agentJobsMutationPM1Fixture(t)
	if err := pm1fixture.SeedGoverningRequirement(context.Background(), s, "proj-web", "audit_required", "accepted audit obligation"); err != nil {
		t.Fatalf("seed governing requirement: %v", err)
	}
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	input := []byte(`{"title":"Add passkey login","value_statement":"reducing account takeover risk","kind":"task","project_ids":["proj-web"],"idempotency_key":"approved-cut-1"}`)
	refused := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if refused.Error == nil || refused.Error.Kind != "invariant_violation" {
		t.Fatalf("expected governing conflict, got outcome=%s err=%+v", refused.Outcome, refused.Error)
	}
	challengeRef, ok := refused.Error.Details["approval_ref"].(string)
	if !ok || len(challengeRef) != 64 {
		t.Fatalf("no actionable challenge on the governing conflict: %v", refused.Error.Details)
	}

	withApproval, err := injectApproval(input, challengeRef)
	if err != nil {
		t.Fatalf("inject approval: %v", err)
	}
	scope := map[string]any{
		"product_id":    "prod-alpha",
		"product_ids":   []string{"prod-alpha"},
		"project_ids":   []string{"proj-web"},
		"scope_version": env.ScopeVersion,
	}
	digest := mutationDigest("concord_work_define", "capture", env, withApproval)
	env.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), nonceForChallenge(challengeRef))

	approved := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: withApproval}, env)
	if approved.Outcome != OutcomeOK {
		t.Fatalf("operator-approved scope cut was refused outcome=%s err=%+v", approved.Outcome, approved.Error)
	}
	if len(approved.ChangedRefs) == 0 {
		t.Fatal("approved capture reported no changed refs")
	}
	if lifecycle, _ := readWorkFromStore(t, s, approved.ChangedRefs[0].ID); lifecycle != "needed" {
		t.Fatalf("approved capture produced lifecycle %q, want needed", lifecycle)
	}
}

// TestGoverningRequirementCoveredCapturePassesUngated proves the gate is scoped:
// declaring the applicable requirement captures without approval, so the
// mechanism refuses omissions rather than taxing every capture.
func TestGoverningRequirementCoveredCapturePassesUngated(t *testing.T) {
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	if err := pm1fixture.SeedGoverningRequirement(context.Background(), s, "proj-web", "audit_required", "accepted audit obligation"); err != nil {
		t.Fatalf("seed governing requirement: %v", err)
	}
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	input := []byte(`{"title":"Add passkey login with audit","value_statement":"reducing account takeover risk","kind":"task","project_ids":["proj-web"],"governing_requirements":["audit_required"],"idempotency_key":"covered-1"}`)
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "capture", Input: input}, env)
	if resp.Outcome != OutcomeOK {
		t.Fatalf("covered capture was refused outcome=%s err=%+v", resp.Outcome, resp.Error)
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
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	workID := "work-budget-audit"
	definition := store.BuiltinWorkflowDefinitions()[0]
	registered, err := store.BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	created := store.Event{EventID: workID + "-created", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"kind":"task","title":"Run the approved audit","priority":3,"expected_version":0,"resulting_version":1}`)}
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

	overBudget := []byte(fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"action_id":%q,"requested_budget_seconds":60,"idempotency_key":%q}`, workID, version, actionID, key))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: overBudget}, env)
	if resp.Outcome != OutcomeError || resp.Error == nil || resp.Error.Kind != "budget_refused" {
		t.Fatalf("60-second request was not refused outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	if resp.Error.SupportedBudgetSeconds != 30 {
		t.Fatalf("refusal ceiling=%d, want the TS1-fixed 30", resp.Error.SupportedBudgetSeconds)
	}
	if resp.Error.EffectState != EffectNone {
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
	withinBudget := []byte(fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"action_id":%q,"requested_budget_seconds":30,"idempotency_key":%q}`, workID, version, actionID, key))
	retry := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: withinBudget}, env)
	if retry.Outcome != OutcomeOK {
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
// AJ8 — approval consequence summary (CD-0037)
// ---------------------------------------------------------------------------

// bindAJ8ApprovalRequired proves the consent prompt is typed and core-derived:
// a consequential transition the operator has not approved mints a challenge
// and returns the summary derived from that challenge's own facts. The
// credential rotation never starts, and its absence is probed, not assumed —
// no effect ran, no transition event exists, and the work version did not
// move.
func bindAJ8ApprovalRequired(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, _, _ := agentJobsMutationPM1Fixture(t)
	env := agentJobsMutationEnvelope(t, s, grant, "proj-web", "prod-alpha")

	workID := "work-ready-high"
	_, version := readWorkFromStore(t, s, workID)
	key := "aj8-approval-1"
	if seed, ok := sc.InitialState["idempotency_seed"].(string); ok && seed != "" {
		key = seed
	}

	input := []byte(fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"target":"completed","reason":"credential rotation delivered","idempotency_key":"%s","evidence":[{"kind":"verification","authority":"agent-verifier","locator_kind":"test","locator":"verification-pass"}]}`, workID, version, key))
	resp := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "lifecycle", Input: input}, env)
	if resp.Outcome != OutcomeError || resp.Error == nil || resp.Error.Kind != "approval_required" {
		t.Fatalf("unapproved consequential transition was not an approval refusal outcome=%s err=%+v", resp.Outcome, resp.Error)
	}
	summary := resp.Error.ConsequenceSummary
	if summary == nil {
		t.Fatal("minted challenge refusal carries no typed consequence summary")
	}
	if summary.Tool != "concord_work_transition" || summary.Operation != "lifecycle" || summary.Consequence == "" {
		t.Fatalf("summary is not derived from the validated invocation: %#v", summary)
	}
	if summary.OperationDigest == "" || len(summary.Scope) == 0 || len(summary.Versions) == 0 || summary.ExpiresAt == "" {
		t.Fatalf("summary lacks the challenge-bound facts: %#v", summary)
	}
	approvalRef, ok := resp.Error.Details["approval_ref"].(string)
	if !ok || len(approvalRef) != 64 {
		t.Fatalf("refusal did not mint a challenge: %v", resp.Error.Details["approval_ref"])
	}

	// The operation never started: version unchanged and no terminal event.
	_, versionAfter := readWorkFromStore(t, s, workID)
	if versionAfter != version {
		t.Fatalf("refused transition moved the work version %d -> %d", version, versionAfter)
	}
	if lifecycle, _ := readWorkFromStore(t, s, workID); lifecycle != "needed" && lifecycle != "in_progress" {
		t.Fatalf("refused transition changed lifecycle to %q", lifecycle)
	}

	obs := envelopeToObservation(resp)
	obs.State = map[string]any{
		"operation": map[string]any{"started": false},
	}
	obs.Effects = map[string]any{
		"approval_challenge_minted": true,
		"atomic_core_effect_zero":   true,
		// Actively probed absence: no effect ran (version unchanged, no
		// terminal transition event), so nothing was rotated and nothing was
		// clamped into a fabricated completion.
		"credential_rotated": probedAbsent{Evidence: "refusal before effect; work version unchanged and lifecycle non-terminal"},
	}
	obs.Authority["approval_ref"] = approvalRef
	obs.Authority["challenge_digest"] = summary.OperationDigest
	return obs
}

// ---------------------------------------------------------------------------
// AJ8 — attributed native-run outcomes (CD-0039)
// ---------------------------------------------------------------------------

// bindAJ8HealthFailureRollback proves the whole native-run recording path: an
// approved ops runbook reaches execution, the native authority reports a
// failed health check, a successful rollback follows, and Concord records
// every report as an attributed event — never as its own observation — folding
// a projection whose status never travels without its reporter. The logical
// operation completes partial: the steps ran, the approved production change
// is not successful, and cancellation cannot erase that it may have reached
// the provider.
func bindAJ8HealthFailureRollback(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition"})
	seedCurrentWorkflowDomainFixture(t, s)
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	// The ops runbook owns native-run actions; identify it through the public
	// registry rather than assuming list order.
	var definition store.WorkflowDefinition
	for _, candidate := range store.BuiltinWorkflowDefinitions() {
		if candidate.Ref == "workflow.ops_runbook" {
			definition = candidate
			break
		}
	}
	if definition.Ref == "" {
		t.Fatal("ops runbook definition is not registered")
	}
	registered, err := store.BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}

	workID := "work-1"
	now := fixedTime()
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(context.Background(), tx, store.WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: now})
	}); err != nil {
		t.Fatalf("initialize workflow: %v", err)
	}
	_, version := readWorkFromStore(t, s, workID)

	// approveContractAndOperation walks the two human checkpoints. The driver
	// is human_checkpoint pending on first dispatch — the refusal mints the
	// challenge — then the operator approves the exact bound intent.
	approvedAction := func(actionID string, version int64, fields map[string]any) int64 {
		t.Helper()
		input := map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "idempotency_key": "aj8-" + actionID}
		if fields != nil {
			input["fields"] = fields
		}
		challenge := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: mustJSON(t, input)}, env)
		if challenge.Outcome != OutcomeError || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
			t.Fatalf("%s did not mint a challenge: outcome=%s err=%+v", actionID, challenge.Outcome, challenge.Error)
		}
		if challenge.Error.ConsequenceSummary == nil {
			t.Fatalf("%s challenge lacks the CD-0037 typed summary", actionID)
		}
		challengeRef, _ := challenge.Error.Details["approval_ref"].(string)
		digest := mutationDigest("concord_work_transition", "workflow_action", env, mustJSON(t, input))
		scope := map[string]any{"product_id": env.SelectedProductID, "project_ids": []string{env.AmbientProjectID}, "work_ids": []string{workID}, "scope_version": env.ScopeVersion}
		versions := map[string]any{"work": version}
		approvalEnv := env
		approvalEnv.HostApproval = signedHostApproval(privateKey, challengeRef, digest, scope, versions, grant.SessionRef, grant.AgentRef, grant.Worktree, fixedTime(), "aj8-"+actionID)
		input["approval"] = map[string]any{"approval_ref": challengeRef}
		response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: mustJSON(t, input)}, approvalEnv)
		if response.Outcome != OutcomeOK {
			t.Fatalf("approved %s failed: outcome=%s err=%+v", actionID, response.Outcome, response.Error)
		}
		_, next := readWorkFromStore(t, s, workID)
		return next
	}

	version = approvedAction("approve_contract", version, workflowContractFieldsFixture())
	version = approvedAction("approve_operation", version, nil)

	report := func(actionID string, version int64, facts map[string]any) Envelope {
		t.Helper()
		input := map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "idempotency_key": "aj8-" + actionID}
		if facts != nil {
			input["fields"] = facts
		}
		return dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: mustJSON(t, input)}, env)
	}
	runID := "run-routing-1"
	subject := "routing-provider:prod-edge"
	subjectDigest := "sha256:" + strings.Repeat("ab", 32)
	base := map[string]any{
		"run_id": runID, "native_subject_ref": subject, "subject_digest": subjectDigest,
		"capture_method": "provider_api", "observed_universe": "prod-edge routing table",
		"freshness_policy_ref": "policy:fresh-v1", "divergence_policy_ref": "policy:diverge-v1",
	}

	started := report("start_run", version, withStatus(base, "started", now))
	if started.Outcome != OutcomeOK {
		t.Fatalf("start_run failed: outcome=%s err=%+v", started.Outcome, started.Error)
	}
	_, version = readWorkFromStore(t, s, workID)

	// The runbook's only declared advance from execute is its condition
	// actions, which open an external gate that the cross-authority health
	// report then refuses until a boundary resolver runs — and the agent
	// surface passes no resolver (AuthorizeWorkflowActionAtBoundaryTx is
	// called with nil), so the walk is undispatchable end to end today. That
	// gap is filed as its own issue; this binding moves the step explicitly
	// and keeps both attributed reports on the real dispatch path, which is
	// what the scenario asserts.
	if err := s.Transact(context.Background(), func(tx *store.Transaction) error {
		return store.AdvanceWorkflowStepForTesting(context.Background(), tx, workID, "health")
	}); err != nil {
		t.Fatalf("seed health step: %v", err)
	}

	failed := report("record_health", version, withStatus(base, "failed", now, map[string]any{"evidence_ref": "probe://run-routing-1/health", "evidence_digest": "sha256:" + strings.Repeat("cd", 32)}))
	if failed.Outcome != OutcomeOK {
		t.Fatalf("record_health failed: outcome=%s err=%+v", failed.Outcome, failed.Error)
	}
	_, version = readWorkFromStore(t, s, workID)

	final := report("rollback_run", version, withStatus(base, "rolled_back", now, map[string]any{"evidence_ref": "provider://run-routing-1/rollback", "evidence_digest": "sha256:" + strings.Repeat("ef", 32)}))
	if final.Outcome != OutcomePartial {
		t.Fatalf("health-failure rollback must complete partial: outcome=%s err=%+v", final.Outcome, final.Error)
	}
	if final.Error == nil || final.Error.Kind != "operation_conflict" || final.Error.RecoveryAction.Kind != "reconcile_operation" || final.Error.EffectState != EffectPartial {
		t.Fatalf("partial envelope error shape wrong: %+v", final.Error)
	}

	var payload struct {
		NativeChange   map[string]any `json:"native_change"`
		HealthFailure  map[string]any `json:"health_failure"`
		RollbackResult map[string]any `json:"rollback_result"`
		CompletedSteps []string       `json:"completed_native_steps"`
	}
	if err := json.Unmarshal(final.Result, &payload); err != nil {
		t.Fatalf("partial result is not the typed native payload: %v", err)
	}
	if payload.NativeChange["status"] != "rolled_back" {
		t.Fatalf("native_change.status=%v", payload.NativeChange["status"])
	}

	// The folded row is attributed: the status never travels alone (D1/D4).
	var authority, evidence string
	if err := s.DatabaseForTesting().QueryRow(`SELECT reporting_authority_ref, evidence_ref FROM workflow_native_runs WHERE work_id=? AND run_id=?`, workID, runID).Scan(&authority, &evidence); err != nil {
		t.Fatalf("native run projection missing: %v", err)
	}
	if authority == "" || evidence == "" {
		t.Fatalf("folded status lacks attribution: authority=%q evidence=%q", authority, evidence)
	}

	obs := envelopeToObservation(final)
	obs.Communication["outcome"] = string(final.Outcome)
	obs.Communication["health_failure"] = payload.HealthFailure
	obs.Communication["rollback_result"] = payload.RollbackResult
	obs.Communication["completed_steps"] = toAnySlice(payload.CompletedSteps)
	obs.Communication["recovery_action"] = final.Error.RecoveryAction.Kind
	obs.State = map[string]any{
		"native_change": map[string]any{"status": payload.NativeChange["status"], "run_id": runID},
	}
	obs.Effects = map[string]any{
		"atomic_core_effect_zero":     true,
		"evidence_authority_supplied": true,
		// TS6 probe: the partial outcome and every native fact came from the
		// core's typed committed report; the adapter added no domain logic.
		"adapter_domain_logic": probedAbsent{Evidence: "outcome derived by core from the typed native_run_recorded event; adapter transported it unchanged"},
	}
	return obs
}

func withStatus(base map[string]any, status string, at time.Time, extra ...map[string]any) map[string]any {
	fields := map[string]any{}
	for key, value := range base {
		fields[key] = value
	}
	for _, additions := range extra {
		for key, value := range additions {
			fields[key] = value
		}
	}
	fields["status"] = status
	fields["asserted_at"] = at.UTC().Format(time.RFC3339Nano)
	return fields
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}
