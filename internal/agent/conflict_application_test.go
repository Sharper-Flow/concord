package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// The public concord_work_define.observation_record surface
// admits a third exclusive input variant conflict:{rule_id,outcome} beside
// statement and external. The variant records the settlement itself through
// the store recorder (store.RecordConflictApplicationTx) inside the
// admitted mutation transaction: exactly one work.conflict_applied event whose
// closed payload carries the checked rule ID and the outcome, with the
// authenticated principal as the actor. A caller never supplies an actor,
// hash, generation, or settlement reference: those are computed, and the
// closed input refuses them.

// createConflictWork seeds one plain task work item with a primary membership
// in the named project, so its recorded version after seeding is 2.
func createConflictWork(t *testing.T, ctx context.Context, s *store.Store, id, projectID string) {
	t.Helper()
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: id + "-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"` + id + `","priority":1}`)},
		{EventID: id + "-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"` + projectID + `","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, id): 0}}); err != nil {
		t.Fatal(err)
	}
}

// conflictDispatchFixture seeds one Product whose project-1 holds a sized and
// an unsized work item, and an authorized work_define grant.
func conflictDispatchFixture(t *testing.T) (*store.Store, *Service, Authority, string) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	applyAgentFixtureProduct(ctx, t, s, "conflict", "product-1", "project-1", "Conflict", "Conflict Project", "conflict fixture")
	createConflictWork(t, ctx, s, "work-a", "project-1")
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"product_read", "work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, scopeVersion
}

// conflictCrossProductFixture seeds two Products with disjoint Projects and a
// sized work-2 on product-2, mirroring crossProductPolicyFixture.
func conflictCrossProductFixture(t *testing.T) (*store.Store, *Service, Authority, string) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	applyAgentFixtureProduct(ctx, t, s, "conflict-cross-home", "product-1", "project-1", "Conflict Home", "Conflict Home Project", "conflict cross fixture")
	applyAgentFixtureProduct(ctx, t, s, "conflict-cross-other", "product-2", "project-2", "Conflict Other", "Conflict Other Project", "conflict cross fixture")
	createConflictWork(t, ctx, s, "work-2", "project-2")
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []Capability{"product_read", "work_define", "cross_scope"}, []string{"product-1", "product-2"}, []string{"project-1", "project-2"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, scopeVersion
}

func conflictEventCount(t *testing.T, s *store.Store) int {
	t.Helper()
	return scalarRow(t, s, `SELECT count(*) FROM domain_events WHERE kind=?`, store.WorkConflictApplied)
}

// conflictApplicationRecord reads the conflict application rows recorded for
// one work item and fails unless the whole store holds exactly want of them.
func conflictApplicationRecord(t *testing.T, s *store.Store, workID string, want int) (string, store.ConflictApplication, map[string]any) {
	t.Helper()
	if got := conflictEventCount(t, s); got != want {
		t.Fatalf("domain_events holds %d conflict application events, want %d", got, want)
	}
	rows, err := s.DatabaseForTesting().Query(`SELECT actor, payload FROM domain_events WHERE kind=? AND subject_id=? ORDER BY event_id`, store.WorkConflictApplied, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var actor string
	var payload []byte
	for rows.Next() {
		if actor != "" {
			t.Fatalf("work %s carries more than one conflict application row", workID)
		}
		if err := rows.Scan(&actor, &payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want == 1 && actor == "" {
		t.Fatalf("work %s carries no conflict application row", workID)
	}
	var record store.ConflictApplication
	var fields map[string]any
	if actor != "" {
		if err := json.Unmarshal(payload, &record); err != nil {
			t.Fatalf("recorded conflict application does not decode: %v", err)
		}
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatalf("recorded conflict application is not an object: %v", err)
		}
	}
	return actor, record, fields
}

func dispatchConflictObservation(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion string, input map[string]any) (Envelope, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: raw}, mutationEnvelope(grant, scopeVersion))
}

// requireRefused accepts either refusal channel: a raw Dispatch error (the
// generated-schema channel for closed-input and exclusivity refusals) or a
// typed envelope error (the live planner and recorder channel).
func requireRefused(t *testing.T, label string, response Envelope, err error) {
	t.Helper()
	if err != nil {
		return
	}
	if response.Error == nil {
		t.Fatalf("%s = %+v, want a refusal", label, response)
	}
}

// conflictExternalCapture is the proven-valid external capture shape, used to
// prove the ordinary external variant and the mixed-variant refusal.
func conflictExternalCapture(observationID string) map[string]any {
	return map[string]any{"kind": "capture", "observation_id": observationID, "subject_kind": "git_position", "subject_ref": "git:refs/heads/main", "captured_at": "2026-08-08T11:55:00Z", "observed_universe": map[string]any{"shape": "item", "applied_scope": "refs/heads/main at origin", "anchor_token": "commit:0123456789abcdef", "coverage": "complete", "observed_refs": []string{"refs/heads/main"}, "total_kind": "eq", "total_value": 1, "completion_evidence": "authoritative_item_read", "canonical_identity_key": "ref_name"}}
}

func TestConflictObservationRecordsOneSettlementTrio(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	response, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-settle-1", "conflict": map[string]any{"rule_id": "R1", "outcome": "settled: rule R1 applied, the slice stays out of scope"}})
	if err != nil || response.Error != nil || response.Outcome != OutcomeOK || response.Replayed {
		t.Fatalf("conflict observation_record = %+v err = %v, want a first ok settlement", response, err)
	}
	actor, record, fields := conflictApplicationRecord(t, s, "work-a", 1)
	if actor != grant.PrincipalRef {
		t.Fatalf("conflict application actor %q, want the authenticated principal %q", actor, grant.PrincipalRef)
	}
	if record.RuleID != "R1" || record.Outcome != "settled: rule R1 applied, the slice stays out of scope" {
		t.Fatalf("recorded pair %+v, want rule R1 with the dispatched outcome", record)
	}
	if record.SizeAssessmentSequence != nil || record.ModelVersion != nil || record.Size != nil {
		t.Fatalf("recorded size copy %+v; the live recorder records no size members", record)
	}
	if len(fields) != 5 {
		t.Fatalf("recorded payload carries %d fields; the closed shape has exactly 5", len(fields))
	}
	if !strings.HasPrefix(record.RuleSourceHash, "sha256:") || len(record.RuleSourceHash) != len("sha256:")+64 {
		t.Fatalf("recorded rule source hash %q is not a sha256 digest", record.RuleSourceHash)
	}
	if record.CatalogGeneration < 1 {
		t.Fatalf("recorded catalog generation %d, want the live table generation", record.CatalogGeneration)
	}
	if record.SettlementRef == "" || len(record.SettlementRef) > 256 {
		t.Fatalf("recorded settlement reference %q, want a core-computed value inside its 256-byte bound", record.SettlementRef)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id='work-a'`); got != 0 {
		t.Fatalf("conflict settlement wrote %d ordinary observation row(s); it records the settlement itself, not an observation", got)
	}
	if got := workVersion(t, s, "work-a"); got != 2 {
		t.Fatalf("conflict settlement moved the work version to %d, want 2", got)
	}
}

func TestConflictObservationRefusesBadConflictInputsWithoutEffects(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	conflictBody := func(ruleID, outcome string, extra map[string]any) map[string]any {
		body := map[string]any{"rule_id": ruleID, "outcome": outcome}
		for field, value := range extra {
			body[field] = value
		}
		return body
	}
	cases := []struct {
		name  string
		input map[string]any
	}{
		{"mixed with statement", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-mixed-statement", "statement": "ordinary progress", "conflict": conflictBody("R1", "settled", nil)}},
		{"mixed with external", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-mixed-external", "external": conflictExternalCapture("xobs:fedcba9876543210"), "conflict": conflictBody("R1", "settled", nil)}},
		{"whitespace outcome", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-whitespace", "conflict": conflictBody("R1", " \t\n", nil)}},
		{"oversized outcome", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-oversized", "conflict": conflictBody("R1", strings.Repeat("x", 4097), nil)}},
		{"forged size member", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-size", "conflict": conflictBody("R1", "settled", map[string]any{"size": "crucial"})}},
		{"forged rule source hash", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-hash", "conflict": conflictBody("R1", "settled", map[string]any{"rule_source_hash": "sha256:" + strings.Repeat("cd", 32)})}},
		{"forged catalog generation", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-generation", "conflict": conflictBody("R1", "settled", map[string]any{"catalog_generation": 9})}},
		{"forged settlement reference", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-settlement-ref", "conflict": conflictBody("R1", "settled", map[string]any{"settlement_ref": "caller-settlement"})}},
		{"forged conflict actor", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-actor", "conflict": conflictBody("R1", "settled", map[string]any{"actor": "operator"})}},
		{"forged top-level actor", map[string]any{"work_id": "work-a", "idempotency_key": "conflict-refuse-actor-top", "actor": "operator", "conflict": conflictBody("R1", "settled", nil)}},
	}
	for _, tc := range cases {
		response, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, tc.input)
		requireRefused(t, tc.name, response, err)
	}
	if got := conflictEventCount(t, s); got != 0 {
		t.Fatalf("refused conflict inputs left %d event row(s); every refusal must leave zero effects", got)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id='work-a'`); got != 0 {
		t.Fatalf("refused conflict inputs wrote %d ordinary observation row(s)", got)
	}
	if got := workVersion(t, s, "work-a"); got != 2 {
		t.Fatalf("refused conflict inputs moved the work version to %d, want 2", got)
	}
}

// The rule vocabulary is the live checked table plus A2-1..A2-9, so an
// unknown rule refuses as a typed envelope refusal from the core, never as a
// frozen schema enum.
func TestConflictObservationRefusesAnUnknownCheckedRuleWithoutEffects(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	for _, ruleID := range []string{"P99", "A2-10", "X1"} {
		response, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-rule-" + ruleID, "conflict": map[string]any{"rule_id": ruleID, "outcome": "settled"}})
		if err != nil {
			t.Fatalf("unknown rule %q refused with the raw error %v, want the typed invalid_input envelope from the live checked-table check", ruleID, err)
		}
		if response.Error == nil || response.Error.Kind != "invalid_input" {
			t.Fatalf("unknown rule %q = %+v, want invalid_input from the checked conflict-rule table", ruleID, response)
		}
	}
	if got := conflictEventCount(t, s); got != 0 {
		t.Fatalf("unknown rules left %d event row(s)", got)
	}
}

func TestConflictObservationReplaysFromCacheAndRefusesDivergence(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	first, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-replay-1", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: A2-3 recorded"}})
	if err != nil || first.Error != nil || first.Outcome != OutcomeOK || first.Replayed {
		t.Fatalf("first conflict settlement = %+v err = %v, want a first ok settlement", first, err)
	}
	replay, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-replay-1", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: A2-3 recorded"}})
	if err != nil || replay.Error != nil || replay.Outcome != OutcomeOK || !replay.Replayed {
		t.Fatalf("conflict replay = %+v err = %v, want a cached replay", replay, err)
	}
	if got := conflictEventCount(t, s); got != 1 {
		t.Fatalf("domain_events holds %d conflict application events after the replay, want the original 1", got)
	}
	divergent, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-replay-1", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: a changed outcome"}})
	if err != nil || divergent.Error == nil || divergent.Error.Kind != "idempotency_conflict" {
		t.Fatalf("divergent same-key conflict = %+v err = %v, want idempotency_conflict", divergent, err)
	}
	_, record, _ := conflictApplicationRecord(t, s, "work-a", 1)
	if record.Outcome != "settled: A2-3 recorded" {
		t.Fatalf("divergent retry rewrote the recorded outcome to %q", record.Outcome)
	}
}

func TestConflictObservationKeepsOrdinaryVariantsUnchanged(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	statement, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "statement": "ordinary progress note", "idempotency_key": "conflict-ordinary-statement"})
	if err != nil || statement.Error != nil || statement.Outcome != OutcomeOK {
		t.Fatalf("statement observation = %+v err = %v, want ok", statement, err)
	}
	external, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-ordinary-external", "external": conflictExternalCapture("xobs:0123456789abcdef")})
	if err != nil || external.Error != nil || external.Outcome != OutcomeOK {
		t.Fatalf("external observation = %+v err = %v, want ok", external, err)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id='work-a'`); got != 1 {
		t.Fatalf("statement observation recorded %d work observation row(s), want 1", got)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM external_observations WHERE work_id='work-a'`); got != 1 {
		t.Fatalf("external capture recorded %d external observation row(s), want 1", got)
	}
	if got := conflictEventCount(t, s); got != 0 {
		t.Fatalf("ordinary variants wrote %d conflict application event(s), want none", got)
	}
}

func TestConflictObservationCrossProductConsentAndExactBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, scopeVersion := conflictCrossProductFixture(t)
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-2","idempotency_key":"cross-conflict-1","conflict":{"rule_id":"A2-1","outcome":"settled: cross-Product conflict recorded"}}`)
	request := InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: input}
	challenge, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || challenge.Error == nil || challenge.Error.Kind != "approval_required" {
		t.Fatalf("cross-Product conflict challenge = %+v err = %v, want approval_required", challenge, dispatchErr)
	}
	ref, ok := challenge.Error.Details["approval_ref"].(string)
	if !ok || len(ref) != 64 {
		t.Fatalf("approval_ref = %v, want a minted 64-character challenge ref", challenge.Error.Details["approval_ref"])
	}
	if got := conflictEventCount(t, s); got != 0 {
		t.Fatalf("challenge creation recorded %d conflict application event(s), want none", got)
	}
	scope := map[string]any{"product_id": "product-1", "product_ids": []string{"product-1", "product-2"}, "project_ids": []string{"project-1"}, "scope_version": scopeVersion, "work_ids": []string{"work-2"}}
	env.HostApproval = signedHostApproval(mustKey(t), ref, mutationDigest(request.Tool, request.Operation, env, input), scope, map[string]any{}, env.SessionRef, env.AgentRef, env.Worktree, fixedTime(), "cross-conflict-approval")
	// The exact receipt binds the declared conflict: changing the outcome
	// under the same approval reference refuses without effects.
	tamperedResponse, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: json.RawMessage(`{"work_id":"work-2","idempotency_key":"cross-conflict-1","conflict":{"rule_id":"A2-1","outcome":"settled: a changed outcome"},"approval":{"approval_ref":"` + ref + `"}}`)}, env)
	if dispatchErr != nil || tamperedResponse.Error == nil || tamperedResponse.Error.Kind != "approval_invalid" {
		t.Fatalf("tampered conflict resubmission = %+v err = %v, want approval_invalid", tamperedResponse, dispatchErr)
	}
	if got := conflictEventCount(t, s); got != 0 {
		t.Fatalf("tampered resubmission recorded %d conflict application event(s), want none", got)
	}
	request.Input = json.RawMessage(`{"work_id":"work-2","idempotency_key":"cross-conflict-1","conflict":{"rule_id":"A2-1","outcome":"settled: cross-Product conflict recorded"},"approval":{"approval_ref":"` + ref + `"}}`)
	approved, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || approved.Error != nil || approved.Outcome != OutcomeOK {
		t.Fatalf("approved conflict settlement = %+v err = %v, want ok", approved, dispatchErr)
	}
	actor, record, _ := conflictApplicationRecord(t, s, "work-2", 1)
	if actor != grant.PrincipalRef {
		t.Fatalf("approved settlement actor %q, want the authenticated principal %q", actor, grant.PrincipalRef)
	}
	if record.RuleID != "A2-1" || record.Outcome != "settled: cross-Product conflict recorded" {
		t.Fatalf("approved settlement %+v, want the exact declared rule and outcome", record)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id='work-2'`); got != 0 {
		t.Fatalf("approved settlement wrote %d ordinary observation row(s)", got)
	}
}

// conflictSettlement is one recorded settlement row: the event actor and the
// decoded v1 application payload.
type conflictSettlement struct {
	Actor       string
	Application store.ConflictApplication
}

// conflictSettlementRows reads every settlement recorded for one work item in
// write order, without asserting the total.
func conflictSettlementRows(t *testing.T, s *store.Store, workID string) []conflictSettlement {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT actor, payload FROM domain_events WHERE kind=? AND subject_id=? ORDER BY seq`, store.WorkConflictApplied, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []conflictSettlement
	for rows.Next() {
		var row conflictSettlement
		var payload []byte
		if err := rows.Scan(&row.Actor, &payload); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &row.Application); err != nil {
			t.Fatalf("recorded conflict application does not decode: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestConflictObservationFreshKeyRecordsASecondSettlement(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	first, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-fresh-key-1", "conflict": map[string]any{"rule_id": "R1", "outcome": "settled: the same declared intent"}})
	if err != nil || first.Error != nil || first.Outcome != OutcomeOK || first.Replayed {
		t.Fatalf("first settlement = %+v err = %v, want ok", first, err)
	}
	second, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-fresh-key-2", "conflict": map[string]any{"rule_id": "R1", "outcome": "settled: the same declared intent"}})
	if err != nil || second.Error != nil || second.Outcome != OutcomeOK || second.Replayed {
		t.Fatalf("fresh-key settlement = %+v err = %v, want a recorded second settlement, not a digest-ID duplicate", second, err)
	}
	if got := conflictEventCount(t, s); got != 2 {
		t.Fatalf("domain_events holds %d conflict application events after a fresh-key settlement of the same intent, want 2", got)
	}
	rows := conflictSettlementRows(t, s, "work-a")
	if len(rows) != 2 {
		t.Fatalf("work-a carries %d settlement row(s), want 2", len(rows))
	}
	for _, row := range rows {
		if row.Actor != grant.PrincipalRef || row.Application.RuleID != "R1" || row.Application.Outcome != "settled: the same declared intent" {
			t.Fatalf("recorded settlement %+v by %q, want rule R1 with the declared outcome by %q", row.Application, row.Actor, grant.PrincipalRef)
		}
	}
}

// A second authenticated principal settling the same declared intent records
// its own settlement: settlement identity includes the actor. The
// idempotency cache stays principal-partitioned, so the same key string
// under the second principal reaches the recorder.
func TestConflictObservationAnotherPrincipalRecordsItsOwnSettlement(t *testing.T) {
	t.Parallel()
	s, service, grant, _ := conflictDispatchFixture(t)
	otherService, _, otherGrant := newAuthorizedService(t, s, "client-2", "human-2", []Capability{"product_read", "work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-shared-key", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: one rule, two principals"}})
	if err != nil || first.Error != nil || first.Outcome != OutcomeOK || first.Replayed {
		t.Fatalf("first principal settlement = %+v err = %v, want ok", first, err)
	}
	second, err := dispatchConflictObservation(t, s, otherService, otherGrant, scopeVersion, map[string]any{"work_id": "work-a", "idempotency_key": "conflict-shared-key", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: one rule, two principals"}})
	if err != nil || second.Error != nil || second.Outcome != OutcomeOK || second.Replayed {
		t.Fatalf("second principal settlement = %+v err = %v, want a recorded own settlement, not a digest-ID duplicate", second, err)
	}
	if got := conflictEventCount(t, s); got != 2 {
		t.Fatalf("domain_events holds %d conflict application events after two principals settled the same intent, want 2", got)
	}
	rows := conflictSettlementRows(t, s, "work-a")
	if len(rows) != 2 {
		t.Fatalf("work-a carries %d settlement row(s), want one per authenticated principal", len(rows))
	}
	if rows[0].Actor == rows[1].Actor {
		t.Fatalf("both settlements carry actor %q; each authenticated principal records its own settlement", rows[0].Actor)
	}
	for _, row := range rows {
		if row.Actor != grant.PrincipalRef && row.Actor != otherGrant.PrincipalRef {
			t.Fatalf("settlement actor %q is neither authenticated principal", row.Actor)
		}
		if row.Application.RuleID != "A2-3" || row.Application.Outcome != "settled: one rule, two principals" {
			t.Fatalf("recorded settlement %+v, want rule A2-3 with the declared outcome", row.Application)
		}
	}
}
