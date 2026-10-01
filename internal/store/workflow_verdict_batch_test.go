package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// CD-0198 D1: one record_verdict call can carry a verdict for each approved
// predicate it judges. The batch writes all of its verdicts or none, keeps
// every CD-0012 D7 independence check and every evidence-binding check, and
// single-predicate calls behave exactly as they did before the batched form.

// seedWorkflowVerdictBatchFixture seeds one implementation item at its
// acceptance step under a contract with three approved predicates, two of
// whose evaluation references are already durably bound. It is the return
// route fixture's shape with a multi-predicate contract, so a batch has real
// work to divide and a real omission to leave missing.
func seedWorkflowVerdictBatchFixture(t *testing.T, workID string) workflowReturnRouteFixture {
	return seedWorkflowVerdictBatchFixtureWithEvidence(t, workID, 0)
}

// seedWorkflowVerdictBatchFixtureWithEvidence seeds the batch fixture and
// binds extraEvidence more durably bound evidence references beyond the three
// named kinds, named evidence:batch-extra-00 upward, so an evidence-bound
// regression has real locators to divide.
func seedWorkflowVerdictBatchFixtureWithEvidence(t *testing.T, workID string, extraEvidence int) workflowReturnRouteFixture {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)

	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	operator := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/operator", SessionRef: "session/" + workID + "-operator", ActorClass: ActorOperator}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	operatorRef, err := WorkflowActorRef(operator)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: owner, Now: time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	version := int64(4)
	nextEvent := func(event Event) Event {
		version++
		return event
	}
	actorEvent := func(id string, actor WorkflowActor, actorRef string) Event {
		return nextEvent(workflowEventWithActor(id, WorkflowActorRecorded, workID, actorRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"actor_ref": actorRef, "principal_ref": actor.PrincipalRef, "client_ref": actor.ClientRef,
			"agent_ref": actor.AgentRef, "session_ref": actor.SessionRef, "actor_class": string(actor.ActorClass),
		}))
	}
	events := []Event{actorEvent("batch-operator-"+workID, operator, operatorRef)}
	contract := workflowEventWithActor("batch-contract-"+workID, WorkflowContractApproved, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check",
		"outcome_predicates": batchVerdictContractPredicates(workID),
		"required_evidence":  []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal",
		"consequence_class": "internal_sqlite", "architecture_binding": WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		},
	})
	contract.PayloadVersion = 3
	events = append(events, nextEvent(contract))
	for _, kind := range []string{"verification", "review", "artifact"} {
		evidenceRef := "evidence:return-route-" + kind
		seedWorkflowAuthority(t, s, "batch-authority-"+kind+"-"+workID, workID, "principal/batch-"+kind, "request/batch-"+kind+"-"+workID, []string{evidenceRef})
		events = append(events, nextEvent(workflowEventWithActor("batch-evidence-"+kind+"-"+workID, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": kind,
			"immutable_subject_ref": evidenceRef, "producer_id": "principal/batch-" + kind, "producer_run_ref": "batch-authority-" + kind + "-" + workID,
			"producer_watermark": "request/batch-" + kind + "-" + workID, "observed_at": "2026-09-12T00:00:00Z",
		})))
	}
	for index := 0; index < extraEvidence; index++ {
		evidenceRef := fmt.Sprintf("evidence:batch-extra-%02d", index)
		authorityID := fmt.Sprintf("batch-authority-extra-%s-%02d", workID, index)
		seedWorkflowAuthority(t, s, authorityID, workID, "principal/batch-extra", "request/"+authorityID, []string{evidenceRef})
		events = append(events, nextEvent(workflowEventWithActor("batch-evidence-extra-"+workID+"-"+fmt.Sprintf("%02d", index), WorkflowEvidenceBound, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "evidence_kind": "artifact",
			"immutable_subject_ref": evidenceRef, "producer_id": "principal/batch-extra", "producer_run_ref": authorityID,
			"producer_watermark": "request/" + authorityID, "observed_at": "2026-09-12T00:00:00Z",
		})))
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{
		Events:           events,
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 4},
	}); err != nil {
		t.Fatal(err)
	}
	if err := advanceWorkflowTestInstanceToStep(ctx, s, workID, "acceptance", ownerRef); err != nil {
		t.Fatal(err)
	}
	return workflowReturnRouteFixture{store: s, owner: owner, operator: operator}
}

func batchVerdictContractPredicates(workID string) []map[string]any {
	check := func(ref string) map[string]any {
		return map[string]any{"kind": "check", "check_ref": "check:" + ref, "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}
	}
	return []map[string]any{
		{"predicate_id": "predicate:batch-present", "ordinal": 0, "outcome_kind": "check", "outcome_payload": check("batch-present")},
		{"predicate_id": "predicate:batch-absent", "ordinal": 1, "outcome_kind": "check", "outcome_payload": check("batch-absent")},
		{"predicate_id": "predicate:batch-third", "ordinal": 2, "outcome_kind": "check", "outcome_payload": check("batch-third")},
	}
}

// TestWorkflowVerdictBatchParityAndRebuild proves a valid batch produces the
// same per-predicate projections as separate calls, and that RebuildFromLog
// reproduces the batched history from the event log alone.
func TestWorkflowVerdictBatchParityAndRebuild(t *testing.T) {
	ctx := context.Background()
	singleWork, batchWork := "batch-parity-single", "batch-parity-batch"
	single := seedWorkflowVerdictBatchFixture(t, singleWork)
	batch := seedWorkflowVerdictBatchFixture(t, batchWork)

	present := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:batch-present","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}`)
	absent := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:batch-absent","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-review"]}`)
	if err := runIssue933OperatorAction(t, single.store, singleWork, "record_verdict", present, single.owner, single.operator); err != nil {
		t.Fatalf("separate present verdict: %v", err)
	}
	if err := runIssue933OperatorAction(t, single.store, singleWork, "record_verdict", absent, single.owner, single.operator); err != nil {
		t.Fatalf("separate absent verdict: %v", err)
	}
	batchPayload := json.RawMessage(`{"contract_version":1,"verdicts":[` +
		`{"predicate_id":"predicate:batch-present","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]},` +
		`{"predicate_id":"predicate:batch-absent","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-review"]}]}`)
	if err := runIssue933OperatorAction(t, batch.store, batchWork, "record_verdict", batchPayload, batch.owner, batch.operator); err != nil {
		t.Fatalf("batched verdict call: %v", err)
	}

	read := func(s *Store, workID string) []workflowVerdictRecordedPayload {
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		verdicts, err := latestWorkflowVerdicts(ctx, tx, workID, 1)
		_ = tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		sort.Slice(verdicts, func(i, j int) bool { return verdicts[i].PredicateID < verdicts[j].PredicateID })
		return verdicts
	}
	compare := func(stage string) {
		t.Helper()
		fromSingles, fromBatch := read(single.store, singleWork), read(batch.store, batchWork)
		if len(fromSingles) != 2 || len(fromBatch) != 2 {
			t.Fatalf("%s: singles=%d batch=%d recorded verdicts, want 2 each", stage, len(fromSingles), len(fromBatch))
		}
		for index := range fromSingles {
			want, got := fromSingles[index], fromBatch[index]
			// The actor reference is derived from the work's own session, so
			// each work holds a different operator ref; the actor must simply
			// be one and the same across each work's verdicts.
			if fromSingles[0].VerdictActorRef != fromSingles[1].VerdictActorRef || fromBatch[0].VerdictActorRef != fromBatch[1].VerdictActorRef {
				t.Fatalf("%s: the call actor must ride on every verdict of one call", stage)
			}
			if want.PredicateID != got.PredicateID || want.VerdictKind != got.VerdictKind ||
				want.ContractVersion != got.ContractVersion || want.IncomparableWithApproved != got.IncomparableWithApproved ||
				strings.Join(want.EvaluationEvidence, "|") != strings.Join(got.EvaluationEvidence, "|") {
				t.Fatalf("%s: verdict %d diverged: separate=%+v batch=%+v", stage, index, want, got)
			}
		}
		missingSingle, missingSingleErr := missingPredicateVerdictsCtx(t, single.store, singleWork)
		missingBatch, missingBatchErr := missingPredicateVerdictsCtx(t, batch.store, batchWork)
		if missingSingleErr != nil || missingBatchErr != nil {
			t.Fatalf("%s: missing-predicate reads failed: %v %v", stage, missingSingleErr, missingBatchErr)
		}
		if strings.Join(missingSingle, ",") != strings.Join(missingBatch, ",") || len(missingBatch) != 1 || missingBatch[0] != "predicate:batch-third" {
			t.Fatalf("%s: missing predicates separate=%v batch=%v, want [predicate:batch-third] on both", stage, missingSingle, missingBatch)
		}
	}
	compare("live projections")

	// One typed event per entry, with eventID-derived per-entry ids.
	var eventIDs []string
	rows, err := batch.store.DatabaseForTesting().Query(`SELECT event_id FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq`, batchWork, WorkflowVerdictRecorded)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var eventID string
		if err := rows.Scan(&eventID); err != nil {
			t.Fatal(err)
		}
		eventIDs = append(eventIDs, eventID)
	}
	rows.Close()
	if len(eventIDs) != 2 ||
		!strings.HasSuffix(eventIDs[0], ":semantic:verdict:0") || !strings.HasSuffix(eventIDs[1], ":semantic:verdict:1") ||
		strings.TrimSuffix(eventIDs[0], ":semantic:verdict:0") != strings.TrimSuffix(eventIDs[1], ":semantic:verdict:1") {
		t.Fatalf("batch event ids = %v, want the operation-derived per-entry pair", eventIDs)
	}

	if err := RebuildFromLog(ctx, batch.store); err != nil {
		t.Fatalf("rebuild workflow event log: %v", err)
	}
	compare("rebuilt projections")
}

func missingPredicateVerdictsCtx(t *testing.T, s *Store, workID string) ([]string, error) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return missingPredicateVerdicts(context.Background(), tx, workID)
}

// TestWorkflowVerdictBatchAtomicRefusal proves a batch holding a duplicate
// predicate, a predicate outside the active contract, unbound evidence, an
// executing-actor verdict, or a malformed shape refuses as a whole and
// writes nothing.
func TestWorkflowVerdictBatchAtomicRefusal(t *testing.T) {
	const workID = "batch-atomic-refusal"
	fixture := seedWorkflowVerdictBatchFixture(t, workID)
	s := fixture.store

	eventsForWork := func() int64 {
		var count int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := eventsForWork()

	cases := []struct {
		name    string
		payload string
		want    FailureKind
	}{
		{"both shapes", `{"predicate_id":"predicate:batch-present","verdicts":[{"predicate_id":"predicate:batch-absent"}]}`, KindInvalidPayload},
		{"neither shape", `{"verdict_kind":"ok"}`, KindInvalidPayload},
		{"entry field beside batch", `{"verdicts":[{"predicate_id":"predicate:batch-present"}],"evaluation_evidence":["evidence:return-route-verification"]}`, KindInvalidPayload},
		{"duplicate predicate", `{"verdicts":[{"predicate_id":"predicate:batch-present"},{"predicate_id":"predicate:batch-present","verdict_kind":"insufficient_evidence"}]}`, KindInvalidPayload},
		{"predicate outside contract", `{"verdicts":[{"predicate_id":"predicate:batch-present"},{"predicate_id":"predicate:not-approved"}]}`, KindInvalidPayload},
		{"unknown entry field", `{"verdicts":[{"predicate_id":"predicate:batch-present","ordinal":0}]}`, KindInvalidPayload},
		{"unbound entry evidence", `{"verdicts":[{"predicate_id":"predicate:batch-present","evaluation_evidence":["evidence:never-bound"]}]}`, KindMissingEvidence},
	}
	for _, testCase := range cases {
		err := runIssue933OperatorAction(t, s, workID, "record_verdict", json.RawMessage(testCase.payload), fixture.owner, fixture.operator)
		failure, ok := err.(*Failure)
		if !ok || failure.Kind != testCase.want {
			t.Errorf("%s: err=%v, want %s", testCase.name, err, testCase.want)
		}
		if got := eventsForWork(); got != before {
			t.Errorf("%s: event count moved from %d to %d; the refused batch must write nothing", testCase.name, before, got)
		}
	}

	// The executing actor cannot judge its own delivery, batched or not,
	// and the refusal still writes nothing (CD-0012 D7).
	executorBatch := json.RawMessage(`{"contract_version":1,"verdicts":[{"predicate_id":"predicate:batch-present","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}]}`)
	err := runVerdictActionAs(t, s, workID, "record_verdict", executorBatch, 0, fixture.owner)
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindUnauthorized {
		t.Errorf("executing-actor batch: err=%v, want %s", err, KindUnauthorized)
	}
	if got := eventsForWork(); got != before {
		t.Errorf("executing-actor batch wrote %d events over %d", got-before, before)
	}

	// The same batch from an independent evaluator records both entries.
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", executorBatch, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("the valid batch the refusals cleared the way for: %v", err)
	}
	if got := eventsForWork(); got != before+2 {
		t.Fatalf("valid batch wrote %d events over %d, want the two verdict events", got-before, before)
	}
}

// TestWorkflowVerdictBatchOmissionStaysMissing proves the predicates a batch
// omits stay missing: the premise confirmation names them until each is
// judged, so a batch cannot silently narrow completion coverage.
func TestWorkflowVerdictBatchOmissionStaysMissing(t *testing.T) {
	const workID = "batch-omission"
	fixture := seedWorkflowVerdictBatchFixture(t, workID)
	batchPayload := json.RawMessage(`{"contract_version":1,"verdicts":[` +
		`{"predicate_id":"predicate:batch-present","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]},` +
		`{"predicate_id":"predicate:batch-absent","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-review"]}]}`)
	if err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", batchPayload, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("batched verdict call: %v", err)
	}
	missing, err := missingPredicateVerdictsCtx(t, fixture.store, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "predicate:batch-third" {
		t.Fatalf("missing predicates = %v, want [predicate:batch-third]", missing)
	}
	confirmErr := runIssue933OperatorAction(t, fixture.store, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator)
	confirmFailure, ok := confirmErr.(*Failure)
	if !ok || confirmFailure.Kind != KindMissingEvidence {
		t.Fatalf("confirm_premise over an omitted predicate = %v, want %s", confirmErr, KindMissingEvidence)
	}
	if !strings.Contains(confirmFailure.Detail, "predicate:batch-third") {
		t.Fatalf("confirmation refusal %q must name the omitted predicate", confirmFailure.Detail)
	}
	var step string
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "acceptance" {
		t.Fatalf("step after refused confirmation = %q, want acceptance", step)
	}

	third := json.RawMessage(`{"contract_version":1,"predicate_id":"predicate:batch-third","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-review"]}`)
	if err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", third, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("single-form third verdict: %v", err)
	}
	missing, err = missingPredicateVerdictsCtx(t, fixture.store, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("missing predicates after full coverage = %v, want none", missing)
	}
	if err := runIssue933OperatorAction(t, fixture.store, workID, "confirm_premise", json.RawMessage(`{"contract_version":1}`), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("confirm_premise after full coverage: %v", err)
	}
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "release" {
		t.Fatalf("step after confirmation = %q, want release", step)
	}
}

// TestWorkflowVerdictBatchEvidenceUnionBound declares the batched form's
// evidence bound: a record_verdict call's distinct evidence union (entry
// evaluation_evidence plus the envelope's references plus any operation-minted
// reference) stays within the per-operation evidence bound the
// action_completed fold already enforces on result_evidence_refs. A
// schema-valid batch over the bound refuses before any event, the same
// predicates recorded as two single calls pass, and a batch whose entries
// share references under the bound passes, so the batched form never
// diverges from separate calls with an undeclared refusal.
func TestWorkflowVerdictBatchEvidenceUnionBound(t *testing.T) {
	const workID = "batch-union-bound"
	fixture := seedWorkflowVerdictBatchFixtureWithEvidence(t, workID, 34)

	extra := func(from, to int) []string {
		refs := make([]string, 0, to-from)
		for index := from; index < to; index++ {
			refs = append(refs, fmt.Sprintf("evidence:batch-extra-%02d", index))
		}
		return refs
	}
	marshal := func(value map[string]any) json.RawMessage {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	batchEntry := func(predicate string, refs []string) map[string]any {
		return map[string]any{"predicate_id": predicate, "verdict_kind": "ok", "evaluation_evidence": refs}
	}
	presentRefs := append([]string{"evidence:return-route-verification"}, extra(0, 16)...)
	absentRefs := append(extra(16, 32), "evidence:return-route-review")

	eventsForWork := func() int64 {
		var count int64
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=?`, workID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	before := eventsForWork()

	// Two entries of 17 distinct bound references each resolve a 34-reference
	// union, one past the bound.
	overbound := marshal(map[string]any{"contract_version": 1, "verdicts": []any{
		batchEntry("predicate:batch-present", presentRefs),
		batchEntry("predicate:batch-absent", absentRefs),
	}})
	err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", overbound, fixture.owner, fixture.operator)
	failure, ok := err.(*Failure)
	if !ok || failure.Kind != KindInvalidPayload {
		t.Fatalf("over-bound batch: err=%v, want %s", err, KindInvalidPayload)
	}
	if !strings.Contains(failure.Detail, "34 distinct evidence references") || !strings.Contains(failure.Detail, "the operation evidence bound is 32") || !strings.Contains(failure.RecoveryAction, "split the batch") {
		t.Fatalf("over-bound refusal = %q / %q, must name the union count, the bound, and the split remedy", failure.Detail, failure.RecoveryAction)
	}
	if got := eventsForWork(); got != before {
		t.Fatalf("over-bound batch moved events from %d to %d; it must refuse before any event", before, got)
	}

	// The same predicates recorded as two single calls stay inside the bound.
	present := marshal(map[string]any{"contract_version": 1, "predicate_id": "predicate:batch-present", "verdict_kind": "ok", "evaluation_evidence": presentRefs})
	absent := marshal(map[string]any{"contract_version": 1, "predicate_id": "predicate:batch-absent", "verdict_kind": "ok", "evaluation_evidence": absentRefs})
	if err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", present, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("single call with entry one's evidence: %v", err)
	}
	if err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", absent, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("single call with entry two's evidence: %v", err)
	}
	// Each single call writes one verdict event plus its action_completed.
	if got := eventsForWork(); got != before+4 {
		t.Fatalf("the two single calls wrote %d events over %d, want exactly four (two verdicts, two completions)", got-before, before)
	}

	// A batch whose entries share references keeps the union under the bound
	// and records both verdicts.
	const sharedWork = "batch-union-shared"
	shared := seedWorkflowVerdictBatchFixtureWithEvidence(t, sharedWork, 20)
	sharedRefs := extra(0, 20)
	sharedBatch := marshal(map[string]any{"contract_version": 1, "verdicts": []any{
		batchEntry("predicate:batch-present", sharedRefs),
		batchEntry("predicate:batch-absent", sharedRefs),
	}})
	if err := runIssue933OperatorAction(t, shared.store, sharedWork, "record_verdict", sharedBatch, shared.owner, shared.operator); err != nil {
		t.Fatalf("batch sharing references under the bound: %v", err)
	}
	var recorded int64
	if err := shared.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, sharedWork, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 2 {
		t.Fatalf("shared-reference batch recorded %d verdicts, want 2", recorded)
	}
}
