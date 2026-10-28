package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// The public concord_work_define.observation_record surface
// admits a third exclusive input variant conflict:{rule_id,outcome} beside
// statement and external. The variant records the settlement itself through
// the store recorder (store.RecordConflictApplicationTx) inside the
// admitted mutation transaction: exactly one work.conflict_applied event whose
// closed v1 payload carries the checked rule ID, the model-owned stored size
// frozen from the work's recorded assessment, and the outcome, with the
// authenticated principal as the actor. A caller never supplies size,
// assessment, actor, hash, generation, or settlement reference: those are
// computed, and the closed input refuses them.

// conflictSizedWorkEvent mirrors the store package's small-size fixture: the
// same inputs decide small under the pinned model version, so the recorded
// application must freeze size small at assessment sequence 1.
func conflictSizedWorkEvent(t *testing.T, eventID, workID string) store.Event {
	t.Helper()
	inputs := store.WorkSizeInputs{
		Kind:            "task",
		Maturity:        "alpha",
		Audience:        "operator_only",
		PathCount:       1,
		DomainCount:     1,
		PackageCount:    1,
		RepositoryCount: 1,
		Declared: store.WorkSizeDeclared{
			EstimatedProductionLines: 20,
			BlastRadius:              store.BlastRadiusPackage,
		},
	}
	footprint := store.WorkSizeFootprint{
		KnowledgeCommit:     "0123456789abcdef0123456789abcdef01234567",
		RegistryContentHash: "sha256:registry",
		Paths:               []store.FootprintPath{{Path: "internal/store/work_size.go", Package: "internal/store", DomainIDs: []string{"storage"}}},
		DomainIDs:           []string{"storage"},
		BehaviorLawIDs:      []string{},
	}
	size, err := store.DecideWorkSize(store.WorkSizeModelVersion, inputs, nil)
	if err != nil {
		t.Fatalf("decide the fixture size: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"model_version": store.WorkSizeModelVersion, "size": string(size), "inputs": inputs, "footprint": footprint})
	if err != nil {
		t.Fatalf("encode the size event: %v", err)
	}
	return store.Event{EventID: eventID, Kind: store.WorkSized, SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkSized), Payload: payload}
}

// createConflictWork seeds one plain task work item with a primary membership
// in the named project, so its recorded version after seeding is 2.
func createConflictWork(t *testing.T, ctx context.Context, s *store.Store, id, projectID string) {
	t.Helper()
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: id + "-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"` + id + `","priority":1}`)},
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
	createConflictWork(t, ctx, s, "work-sized", "project-1")
	createConflictWork(t, ctx, s, "work-unsized", "project-1")
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{conflictSizedWorkEvent(t, "work-sized-size", "work-sized")}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []agentcontract.Capability{"product_read", "work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
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
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{conflictSizedWorkEvent(t, "work-2-size", "work-2")}}); err != nil {
		t.Fatal(err)
	}
	service, _, grant := newAuthorizedService(t, s, "client-1", "human-1", []agentcontract.Capability{"product_read", "work_define", "cross_scope"}, []string{"product-1", "product-2"}, []string{"project-1", "project-2"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return s, service, grant, scopeVersion
}

func conflictEventCount(t *testing.T, s *store.Store) int {
	t.Helper()
	return scalarRow(t, s, `SELECT count(*) FROM effect_receipts WHERE kind=?`, store.WorkConflictApplied)
}

// conflictApplicationRecord reads the conflict application row recorded for
// one work item and fails unless the whole store holds exactly one.
func conflictApplicationRecord(t *testing.T, s *store.Store, workID string, expected conflictSettlement) (string, store.ConflictApplication, map[string]any) {
	t.Helper()
	if got := conflictEventCount(t, s); got != 1 {
		t.Fatalf("effect_receipts holds %d conflict applications, want one", got)
	}
	rows := conflictSettlementRows(t, s, workID, expected)
	if len(rows) != 1 {
		t.Fatalf("work %s carries %d conflict applications, want one", workID, len(rows))
	}
	record := rows[0].Application
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	return rows[0].Actor, record, fields
}

func dispatchConflictObservation(t *testing.T, s *store.Store, service *Service, grant Authority, scopeVersion string, input map[string]any) (agentcontract.Envelope, error) {
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
func requireRefused(t *testing.T, label string, response agentcontract.Envelope, err error) {
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
	response, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-settle-1", "conflict": map[string]any{"rule_id": "R1", "outcome": "settled: rule R1 applied, the slice stays out of scope"}})
	if err != nil || response.Error != nil || response.Outcome != agentcontract.OutcomeOK || response.Replayed {
		t.Fatalf("conflict observation_record = %+v err = %v, want a first ok settlement", response, err)
	}
	actor, record, fields := conflictApplicationRecord(t, s, "work-sized", expectedConflictSettlement(t, s, "work-sized", grant.PrincipalRef, "conflict-settle-1", "R1", "settled: rule R1 applied, the slice stays out of scope", 1))
	if actor != grant.PrincipalRef {
		t.Fatalf("conflict application actor %q, want the authenticated principal %q", actor, grant.PrincipalRef)
	}
	if record.RuleID != "R1" || record.Outcome != "settled: rule R1 applied, the slice stays out of scope" {
		t.Fatalf("recorded trio %+v, want rule R1 with the dispatched outcome", record)
	}
	if record.Size != store.WorkSizeSmall || record.SizeAssessmentSequence != 1 || record.ModelVersion != store.WorkSizeModelVersion {
		t.Fatalf("recorded size copy %+v, want the stored small assessment at sequence 1 under model version %d", record, store.WorkSizeModelVersion)
	}
	if len(fields) != 8 {
		t.Fatalf("recorded payload carries %d fields; the closed v1 shape has exactly 8", len(fields))
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
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id='work-sized'`); got != 0 {
		t.Fatalf("conflict settlement wrote %d ordinary observation row(s); it records the settlement itself, not an observation", got)
	}
	if got := workVersion(t, s, "work-sized"); got != 2 {
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
		{"mixed with statement", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-mixed-statement", "statement": "ordinary progress", "conflict": conflictBody("R1", "settled", nil)}},
		{"mixed with external", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-mixed-external", "external": conflictExternalCapture("xobs:fedcba9876543210"), "conflict": conflictBody("R1", "settled", nil)}},
		{"whitespace outcome", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-whitespace", "conflict": conflictBody("R1", " \t\n", nil)}},
		{"oversized outcome", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-oversized", "conflict": conflictBody("R1", strings.Repeat("x", 4097), nil)}},
		{"forged size", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-size", "conflict": conflictBody("R1", "settled", map[string]any{"size": string(store.WorkSizeCrucial)})}},
		{"forged size assessment sequence", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-sequence", "conflict": conflictBody("R1", "settled", map[string]any{"size_assessment_sequence": 2})}},
		{"forged rule source hash", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-hash", "conflict": conflictBody("R1", "settled", map[string]any{"rule_source_hash": "sha256:" + strings.Repeat("cd", 32)})}},
		{"forged catalog generation", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-generation", "conflict": conflictBody("R1", "settled", map[string]any{"catalog_generation": 9})}},
		{"forged model version", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-model", "conflict": conflictBody("R1", "settled", map[string]any{"model_version": 99})}},
		{"forged settlement reference", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-settlement-ref", "conflict": conflictBody("R1", "settled", map[string]any{"settlement_ref": "caller-settlement"})}},
		{"forged conflict actor", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-actor", "conflict": conflictBody("R1", "settled", map[string]any{"actor": "operator"})}},
		{"forged top-level actor", map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-refuse-actor-top", "actor": "operator", "conflict": conflictBody("R1", "settled", nil)}},
	}
	for _, tc := range cases {
		response, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, tc.input)
		requireRefused(t, tc.name, response, err)
	}
	if got := conflictEventCount(t, s); got != 0 {
		t.Fatalf("refused conflict inputs left %d event row(s); every refusal must leave zero effects", got)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id='work-sized'`); got != 0 {
		t.Fatalf("refused conflict inputs wrote %d ordinary observation row(s)", got)
	}
	if got := workVersion(t, s, "work-sized"); got != 2 {
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
		response, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-rule-" + ruleID, "conflict": map[string]any{"rule_id": ruleID, "outcome": "settled"}})
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

func TestConflictObservationRefusesAnUnsizedWorkItemWithoutEffects(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	response, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-unsized", "idempotency_key": "conflict-unsized-1", "conflict": map[string]any{"rule_id": "R1", "outcome": "settled"}})
	if err != nil {
		t.Fatalf("conflict observation_record on unsized work = %v, want a typed refusal from the recorder inside the mutation transaction", err)
	}
	if response.Error == nil || response.Error.Kind != "unknown_scope" {
		t.Fatalf("unsized conflict settlement = %+v, want unknown_scope from the recorder's missing-size refusal", response)
	}
	if got := conflictEventCount(t, s); got != 0 {
		t.Fatalf("unsized settlement left %d event row(s); missing size refuses without effects", got)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id='work-unsized'`); got != 0 {
		t.Fatalf("unsized settlement wrote %d ordinary observation row(s)", got)
	}
	if got := workVersion(t, s, "work-unsized"); got != 2 {
		t.Fatalf("unsized settlement moved the work version to %d, want 2", got)
	}
}

func TestConflictObservationReplaysFromCacheAndRefusesDivergence(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	first, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-replay-1", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: A2-3 recorded"}})
	if err != nil || first.Error != nil || first.Outcome != agentcontract.OutcomeOK || first.Replayed {
		t.Fatalf("first conflict settlement = %+v err = %v, want a first ok settlement", first, err)
	}
	replay, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-replay-1", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: A2-3 recorded"}})
	if err != nil || replay.Error != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("conflict replay = %+v err = %v, want a cached replay", replay, err)
	}
	if got := conflictEventCount(t, s); got != 1 {
		t.Fatalf("effect_receipts holds %d conflict applications after the replay, want the original 1", got)
	}
	divergent, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-replay-1", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: a changed outcome"}})
	if err != nil || divergent.Error == nil || divergent.Error.Kind != "idempotency_conflict" {
		t.Fatalf("divergent same-key conflict = %+v err = %v, want idempotency_conflict", divergent, err)
	}
	_, record, _ := conflictApplicationRecord(t, s, "work-sized", expectedConflictSettlement(t, s, "work-sized", grant.PrincipalRef, "conflict-replay-1", "A2-3", "settled: A2-3 recorded", 1))
	if record.Outcome != "settled: A2-3 recorded" {
		t.Fatalf("divergent retry rewrote the recorded outcome to %q", record.Outcome)
	}
}

func TestConflictObservationKeepsOrdinaryVariantsUnchanged(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	statement, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "statement": "ordinary progress note", "idempotency_key": "conflict-ordinary-statement"})
	if err != nil || statement.Error != nil || statement.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("statement observation = %+v err = %v, want ok", statement, err)
	}
	external, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-ordinary-external", "external": conflictExternalCapture("xobs:0123456789abcdef")})
	if err != nil || external.Error != nil || external.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("external observation = %+v err = %v, want ok", external, err)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id='work-sized'`); got != 1 {
		t.Fatalf("statement observation recorded %d work observation row(s), want 1", got)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM external_observations WHERE work_id='work-sized'`); got != 1 {
		t.Fatalf("external capture recorded %d external observation row(s), want 1", got)
	}
	if got := conflictEventCount(t, s); got != 0 {
		t.Fatalf("ordinary variants wrote %d conflict application event(s), want none", got)
	}
}

// CD-0243 D3/D7 retire cross-Product observation consent, not exact settlement
// identity, authenticated attribution or same-key replay binding.
func TestConflictObservationCrossProductConsentFreeAndExactBinding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, scopeVersion := conflictCrossProductFixture(t)
	env := mutationEnvelope(grant, scopeVersion)
	input := json.RawMessage(`{"work_id":"work-2","idempotency_key":"cross-conflict-1","conflict":{"rule_id":"A2-1","outcome":"settled: cross-Product conflict recorded"}}`)
	request := InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: input}
	approved, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || approved.Error != nil || approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approved conflict settlement = %+v err = %v, want ok", approved, dispatchErr)
	}
	actor, record, _ := conflictApplicationRecord(t, s, "work-2", expectedConflictSettlement(t, s, "work-2", grant.PrincipalRef, "cross-conflict-1", "A2-1", "settled: cross-Product conflict recorded", 1))
	if actor != grant.PrincipalRef {
		t.Fatalf("approved settlement actor %q, want the authenticated principal %q", actor, grant.PrincipalRef)
	}
	if record.RuleID != "A2-1" || record.Outcome != "settled: cross-Product conflict recorded" {
		t.Fatalf("approved settlement %+v, want the exact declared rule and outcome", record)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM work_observations WHERE work_id='work-2'`); got != 0 {
		t.Fatalf("approved settlement wrote %d ordinary observation row(s)", got)
	}
	replay, dispatchErr := Dispatch(ctx, s, service, request, env)
	if dispatchErr != nil || replay.Outcome != agentcontract.OutcomeOK || !replay.Replayed {
		t.Fatalf("cross-Product settlement replay = %+v err = %v", replay, dispatchErr)
	}
	tamperedResponse, dispatchErr := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_define", Operation: "observation_record", Input: json.RawMessage(`{"work_id":"work-2","idempotency_key":"cross-conflict-1","conflict":{"rule_id":"A2-1","outcome":"settled: a changed outcome"}}`)}, env)
	if dispatchErr != nil || tamperedResponse.Error == nil || tamperedResponse.Error.Kind != "idempotency_conflict" {
		t.Fatalf("tampered conflict resubmission = %+v err = %v, want idempotency_conflict", tamperedResponse, dispatchErr)
	}
	conflictApplicationRecord(t, s, "work-2", expectedConflictSettlement(t, s, "work-2", grant.PrincipalRef, "cross-conflict-1", "A2-1", "settled: cross-Product conflict recorded", 1))
	if got := workVersion(t, s, "work-2"); got != 2 {
		t.Fatalf("settlement or replay moved work version to %d, want 2", got)
	}
	if got := scalarRow(t, s, `SELECT count(*) FROM agent_approval_challenges`); got != 0 {
		t.Fatalf("ordinary cross-Product settlement minted %d challenges", got)
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
func conflictSettlementRows(t *testing.T, s *store.Store, workID string, expected ...conflictSettlement) []conflictSettlement {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT event_id,actor,payload_version,payload_digest FROM effect_receipts WHERE kind=? AND subject_type='work_item' AND subject_id=? ORDER BY effect_seq`, store.WorkConflictApplied, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []conflictSettlement
	for rows.Next() {
		var id, actor, digest string
		var version int
		if err := rows.Scan(&id, &actor, &version, &digest); err != nil {
			t.Fatal(err)
		}
		if len(out) >= len(expected) {
			t.Fatal("unexpected conflict receipt")
		}
		row := expected[len(out)]
		identity, _ := json.Marshal([]any{"conflict-identity", row.Actor, workID, row.Application.SettlementRef, row.Application.RuleID, row.Application.SizeAssessmentSequence})
		payload, err := json.Marshal(row.Application)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(payload, &fields); err != nil {
			t.Fatal(err)
		}
		canonical, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if id != fmt.Sprintf("conflict-%x", sha256.Sum256(identity)) || actor != row.Actor || version != 2 || digest != fmt.Sprintf("sha256:%x", sha256.Sum256(canonical)) {
			t.Fatalf("conflict receipt does not bind the exact actor, rule, size, outcome and settlement: id=%s actor=%s version=%d digest=%s expected=%s", id, actor, version, digest, canonical)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	page, err := s.QueryQ7(context.Background(), store.Q7Request{Work: workID, RecordKinds: []string{"conflict_application"}, Direction: "oldest_first", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var retained []conflictSettlement
	for _, entry := range page.Entries {
		if entry.ConflictApplication == nil {
			t.Fatalf("settlement lacks readable payload: %+v", entry)
		}
		retained = append(retained, conflictSettlement{Actor: entry.Actor, Application: *entry.ConflictApplication})
	}
	if !reflect.DeepEqual(retained, out) {
		t.Fatalf("history payloads do not match recorded settlements: got %+v, want %+v", retained, out)
	}
	return retained
}

// The receipt hashes all eight fields; the expected content comes from the
// authored request, checked rule table and retained size assessment, not a log.
func expectedConflictSettlement(t *testing.T, s *store.Store, workID, actor, key, rule, outcome string, sequence int) conflictSettlement {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/conflict-rules.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		CatalogGeneration int `json:"catalog_generation"`
		Source            struct {
			Hash string `json:"sha256"`
		} `json:"source"`
	}
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	application := store.ConflictApplication{RuleID: rule, Outcome: outcome, RuleSourceHash: table.Source.Hash, CatalogGeneration: table.CatalogGeneration, SizeAssessmentSequence: sequence, SettlementRef: conflictSettlementRef(actor, "concord_work_define", "observation_record", key)}
	if err := s.DatabaseForTesting().QueryRow(`SELECT model_version,size_class FROM work_size_assessments WHERE work_id=? AND sequence=?`, workID, sequence).Scan(&application.ModelVersion, &application.Size); err != nil {
		t.Fatal(err)
	}
	return conflictSettlement{Actor: actor, Application: application}
}

// conflictLaterAssessment records one synthetic model-owned reassessment
// (estimated lines 61 size larger at the next sequence) through the ordinary
// work.sized fold. It is the same fold fixture the store tests use, not a
// public resize route.
func conflictLaterAssessment(t *testing.T, s *store.Store, workID string) {
	t.Helper()
	inputs := store.WorkSizeInputs{
		Kind:            "task",
		Maturity:        "alpha",
		Audience:        "operator_only",
		PathCount:       1,
		DomainCount:     1,
		PackageCount:    1,
		RepositoryCount: 1,
		Declared: store.WorkSizeDeclared{
			EstimatedProductionLines: 61,
			BlastRadius:              store.BlastRadiusPackage,
		},
	}
	footprint := store.WorkSizeFootprint{
		KnowledgeCommit:     "0123456789abcdef0123456789abcdef01234567",
		RegistryContentHash: "sha256:registry",
		Paths:               []store.FootprintPath{{Path: "internal/store/work_size.go", Package: "internal/store", DomainIDs: []string{"storage"}}},
		DomainIDs:           []string{"storage"},
		BehaviorLawIDs:      []string{},
	}
	size, err := store.DecideWorkSize(store.WorkSizeModelVersion, inputs, nil)
	if err != nil {
		t.Fatalf("decide the later assessment size: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"model_version": store.WorkSizeModelVersion, "size": string(size), "inputs": inputs, "footprint": footprint})
	if err != nil {
		t.Fatalf("encode the later assessment event: %v", err)
	}
	if err := store.ApplyOperation(context.Background(), s, store.Operation{Events: []store.Event{{EventID: workID + "-size-later", Kind: store.WorkSized, SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkSized), Payload: payload}}}); err != nil {
		t.Fatal(err)
	}
}

// The same declared intent under a fresh idempotency key is a distinct
// settlement: it must record a second work.conflict_applied event. Only the
// cached same-key intent replays and the changed same-key intent refuses;
// both stay pinned by the replay test above.
func TestConflictObservationFreshKeyRecordsASecondSettlement(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	first, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-fresh-key-1", "conflict": map[string]any{"rule_id": "R1", "outcome": "settled: the same declared intent"}})
	if err != nil || first.Error != nil || first.Outcome != agentcontract.OutcomeOK || first.Replayed {
		t.Fatalf("first settlement = %+v err = %v, want ok", first, err)
	}
	second, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-fresh-key-2", "conflict": map[string]any{"rule_id": "R1", "outcome": "settled: the same declared intent"}})
	if err != nil || second.Error != nil || second.Outcome != agentcontract.OutcomeOK || second.Replayed {
		t.Fatalf("fresh-key settlement = %+v err = %v, want a recorded second settlement, not a digest-ID duplicate", second, err)
	}
	if got := conflictEventCount(t, s); got != 2 {
		t.Fatalf("effect_receipts holds %d conflict applications after a fresh-key settlement of the same intent, want 2", got)
	}
	rows := conflictSettlementRows(t, s, "work-sized",
		expectedConflictSettlement(t, s, "work-sized", grant.PrincipalRef, "conflict-fresh-key-1", "R1", "settled: the same declared intent", 1),
		expectedConflictSettlement(t, s, "work-sized", grant.PrincipalRef, "conflict-fresh-key-2", "R1", "settled: the same declared intent", 1))
	if len(rows) != 2 {
		t.Fatalf("work-sized carries %d settlement row(s), want 2", len(rows))
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
	otherService, _, otherGrant := newAuthorizedService(t, s, "client-2", "human-2", []agentcontract.Capability{"product_read", "work_define"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err := s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-shared-key", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: one rule, two principals"}})
	if err != nil || first.Error != nil || first.Outcome != agentcontract.OutcomeOK || first.Replayed {
		t.Fatalf("first principal settlement = %+v err = %v, want ok", first, err)
	}
	second, err := dispatchConflictObservation(t, s, otherService, otherGrant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-shared-key", "conflict": map[string]any{"rule_id": "A2-3", "outcome": "settled: one rule, two principals"}})
	if err != nil || second.Error != nil || second.Outcome != agentcontract.OutcomeOK || second.Replayed {
		t.Fatalf("second principal settlement = %+v err = %v, want a recorded own settlement, not a digest-ID duplicate", second, err)
	}
	if got := conflictEventCount(t, s); got != 2 {
		t.Fatalf("effect_receipts holds %d conflict applications after two principals settled the same intent, want 2", got)
	}
	rows := conflictSettlementRows(t, s, "work-sized",
		expectedConflictSettlement(t, s, "work-sized", grant.PrincipalRef, "conflict-shared-key", "A2-3", "settled: one rule, two principals", 1),
		expectedConflictSettlement(t, s, "work-sized", otherGrant.PrincipalRef, "conflict-shared-key", "A2-3", "settled: one rule, two principals", 1))
	if len(rows) != 2 {
		t.Fatalf("work-sized carries %d settlement row(s), want one per authenticated principal", len(rows))
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

// A fresh-key settlement after a later recorded assessment names the
// assessment it was applied against: the event identity must derive from the
// stored assessment sequence, so the second settlement records at sequence 2
// while the first keeps its historical copy.
func TestConflictObservationFreshKeyAfterLaterAssessmentRecordsAtTheNewSequence(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	first, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-seq-1", "conflict": map[string]any{"rule_id": "P3", "outcome": "settled: one rule across two assessments"}})
	if err != nil || first.Error != nil || first.Outcome != agentcontract.OutcomeOK || first.Replayed {
		t.Fatalf("first settlement = %+v err = %v, want ok", first, err)
	}
	conflictLaterAssessment(t, s, "work-sized")
	second, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-sized", "idempotency_key": "conflict-seq-2", "conflict": map[string]any{"rule_id": "P3", "outcome": "settled: one rule across two assessments"}})
	if err != nil || second.Error != nil || second.Outcome != agentcontract.OutcomeOK || second.Replayed {
		t.Fatalf("fresh-key settlement after the later assessment = %+v err = %v, want a second settlement at the new stored sequence, not a digest-ID collision refusal", second, err)
	}
	if got := conflictEventCount(t, s); got != 2 {
		t.Fatalf("effect_receipts holds %d conflict applications after the later assessment, want 2", got)
	}
	rows := conflictSettlementRows(t, s, "work-sized",
		expectedConflictSettlement(t, s, "work-sized", grant.PrincipalRef, "conflict-seq-1", "P3", "settled: one rule across two assessments", 1),
		expectedConflictSettlement(t, s, "work-sized", grant.PrincipalRef, "conflict-seq-2", "P3", "settled: one rule across two assessments", 2))
	if len(rows) != 2 {
		t.Fatalf("work-sized carries %d settlement row(s), want 2", len(rows))
	}
	if rows[0].Application.SizeAssessmentSequence != 1 || rows[0].Application.Size != store.WorkSizeSmall {
		t.Fatalf("first settlement %+v, want the historical small copy at sequence 1", rows[0].Application)
	}
	if rows[1].Application.SizeAssessmentSequence != 2 || rows[1].Application.Size != store.WorkSizeLarger {
		t.Fatalf("second settlement %+v, want the later larger assessment at sequence 2", rows[1].Application)
	}
}

func TestConflictObservationHistoryRoundTrip(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictDispatchFixture(t)
	outcome := strings.Repeat("é", 2048)
	input := map[string]any{"work_id": "work-sized", "idempotency_key": "history-settlement", "conflict": map[string]any{"rule_id": "R1", "outcome": outcome}}
	written, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, input)
	if err != nil || written.Error != nil || written.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record = %+v, %v", written, err)
	}
	want := expectedConflictSettlement(t, s, "work-sized", grant.PrincipalRef, "history-settlement", "R1", outcome, 1)
	read := func(cursor *string) agentcontract.Envelope {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"work_id": "work-sized", "record_kinds": []string{"conflict_application"}, "direction": "incoming", "page": map[string]any{"limit": 1, "cursor": cursor}})
		if err != nil {
			t.Fatal(err)
		}
		response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: raw}, mutationEnvelope(grant, scopeVersion))
		if err != nil || response.Error != nil || response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("history = %+v, error=%+v, dispatch error=%v", response, response.Error, err)
		}
		return response
	}
	decode := func(response agentcontract.Envelope) []store.HistoryEntry {
		t.Helper()
		var page struct {
			Entries []store.HistoryEntry `json:"entries"`
		}
		if err := json.Unmarshal(response.Result, &page); err != nil {
			t.Fatal(err)
		}
		return page.Entries
	}
	first := decode(read(nil))
	if len(first) != 1 || first[0].ConflictApplication == nil || *first[0].ConflictApplication != want.Application || first[0].Actor != want.Actor || first[0].Version != 2 || first[0].VersionWorkID != "work-sized" {
		t.Fatalf("public history lost full recorded payload or metadata: %+v", first)
	}
	conflictLaterAssessment(t, s, "work-sized")
	if got := decode(read(nil)); !reflect.DeepEqual(got, first) {
		t.Fatalf("later size rewrote public settlement: %+v, want %+v", got, first)
	}
	replay, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, input)
	if err != nil || replay.Error != nil || !replay.Replayed {
		t.Fatalf("retry = %+v, %v", replay, err)
	}
	if got := decode(read(nil)); !reflect.DeepEqual(got, first) {
		t.Fatalf("cached retry rewrote public settlement: %+v", got)
	}
	input["idempotency_key"] = "history-settlement-second"
	second, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, input)
	if err != nil || second.Error != nil || second.Replayed {
		t.Fatalf("new settlement = %+v, %v", second, err)
	}
	page := read(nil)
	newer := decode(page)
	if len(newer) != 1 || newer[0].ConflictApplication == nil || newer[0].ConflictApplication.SizeAssessmentSequence != 2 || newer[0].ConflictApplication.Size != store.WorkSizeLarger || page.NextCursor == nil {
		t.Fatalf("first public page = %+v, cursor=%v", newer, page.NextCursor)
	}
	last := read(page.NextCursor)
	if got := decode(last); !reflect.DeepEqual(got, first) || last.NextCursor != nil {
		t.Fatalf("last public page = %+v, cursor=%v; want original %+v", got, last.NextCursor, first)
	}
}

func TestConflictObservationHistoryRespectsScope(t *testing.T) {
	t.Parallel()
	s, service, grant, scopeVersion := conflictCrossProductFixture(t)
	response, err := dispatchConflictObservation(t, s, service, grant, scopeVersion, map[string]any{"work_id": "work-2", "idempotency_key": "history-scope", "conflict": map[string]any{"rule_id": "R1", "outcome": "settled outside the reader's Product"}})
	if err != nil || response.Error != nil || response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record = %+v, %v", response, err)
	}
	limitedService, _, limitedGrant := newAuthorizedService(t, s, "limited-client", "limited-human", []agentcontract.Capability{"product_read"}, []string{"product-1"}, []string{"project-1"}, store.ProjectResolution{ProjectID: "project-1"})
	scopeVersion, _, err = s.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	response, err = Dispatch(context.Background(), s, limitedService, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: json.RawMessage(`{"work_id":"work-2"}`)}, mutationEnvelope(limitedGrant, scopeVersion))
	if err != nil || response.Error == nil || response.Error.Kind != "unauthorized" {
		t.Fatalf("foreign settlement history = %+v, error=%+v, dispatch error=%v; want authorization refusal", response, response.Error, err)
	}
	if strings.Contains(string(response.Result), "settled outside") || strings.Contains(string(response.Result), "conflict_application") {
		t.Fatalf("refused history leaked settlement evidence: %s", response.Result)
	}
}
