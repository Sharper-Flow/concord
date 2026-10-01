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
	return seedWorkflowVerdictBatchFixtureRequiring(t, workID, 0, []string{"verification"})
}

// seedWorkflowVerdictBatchFixtureWithEvidence seeds the batch fixture and
// binds extraEvidence more durably bound evidence references beyond the three
// named kinds, named evidence:batch-extra-00 upward, so an evidence-bound
// regression has real locators to divide.
func seedWorkflowVerdictBatchFixtureWithEvidence(t *testing.T, workID string, extraEvidence int) workflowReturnRouteFixture {
	return seedWorkflowVerdictBatchFixtureRequiring(t, workID, extraEvidence, []string{"verification"})
}

// seedWorkflowVerdictBatchFixtureRequiring seeds the batch fixture with a
// chosen required_evidence set, so a contract can demand native_run and the
// batch's mint and union bound resolve a real captured record.
func seedWorkflowVerdictBatchFixtureRequiring(t *testing.T, workID string, extraEvidence int, requiredEvidence []string) workflowReturnRouteFixture {
	return seedWorkflowVerdictBatchFixtureDefinition(t, workID, extraEvidence, requiredEvidence, batchVerdictContractPredicates(workID), "workflow.implementation")
}

// seedWorkflowVerdictBatchFixtureOnFixtureDefinition seeds the batch fixture's
// multi-predicate contract on the fixture definition, whose acceptance step
// carries no refinement failure edge, so a confirmation over non-ok verdicts
// still advances to the terminal step — the late recovery position.
func seedWorkflowVerdictBatchFixtureOnFixtureDefinition(t *testing.T, workID string) workflowReturnRouteFixture {
	return seedWorkflowVerdictBatchFixtureDefinition(t, workID, 0, []string{"verification"}, batchVerdictContractPredicates(workID), workflowFixtureRef)
}

// seedWorkflowVerdictBatchFixturePredicates seeds the batch fixture with a
// chosen approved predicate set, so a batch can fill the declared eight-entry
// verdicts bound with distinct approved predicates.
func seedWorkflowVerdictBatchFixturePredicates(t *testing.T, workID string, extraEvidence int, requiredEvidence []string, predicates []map[string]any) workflowReturnRouteFixture {
	return seedWorkflowVerdictBatchFixtureDefinition(t, workID, extraEvidence, requiredEvidence, predicates, "workflow.implementation")
}

// seedWorkflowVerdictBatchFixtureDefinition seeds the batch fixture on a
// chosen workflow family, so the late recovery route can ride the fixture
// definition while every other test rides the production definition.
func seedWorkflowVerdictBatchFixtureDefinition(t *testing.T, workID string, extraEvidence int, requiredEvidence []string, predicates []map[string]any, definitionRef string) workflowReturnRouteFixture {
	t.Helper()
	var registered RegisteredDefinition
	if definitionRef == workflowFixtureRef {
		registered = workflowFixtureDefinition(t, 2)
	} else {
		var err error
		registered, err = BuiltinWorkflowDefinitionForRef(definitionRef)
		if err != nil {
			t.Fatal(err)
		}
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
	contractFields := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check",
		"outcome_predicates": predicates,
		"required_evidence":  requiredEvidence, "route_conventions": []string{}, "spec_mandate": []string{}, "law_modifies": []string{},
		"law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1, "rigor_class": "prototype_internal",
		"consequence_class": "internal_sqlite",
	}
	if definitionRef != workflowFixtureRef {
		contractFields["architecture_binding"] = WorkflowArchitectureBinding{
			DomainRegistryContentHash: "sha256:" + strings.Repeat("b", 64), HomeDomainID: "root", AffectedDomainIDs: []string{"root"},
			DomainModifies: []string{}, DomainRelationModifies: []WorkflowDomainRelationModification{}, LawAdditions: []WorkflowLawAddition{},
			VerificationObligations: []WorkflowVerificationObligation{},
		}
	}
	contract := workflowEventWithActor("batch-contract-"+workID, WorkflowContractApproved, workID, ownerRef, contractFields)
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

// batchVerdictPredicateSet declares count distinct approved predicates, so a
// test can fill the declared eight-entry verdicts bound.
func batchVerdictPredicateSet(workID string, count int) []map[string]any {
	check := func(ref string) map[string]any {
		return map[string]any{"kind": "check", "check_ref": "check:" + ref, "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}
	}
	set := make([]map[string]any, 0, count)
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("batch-p%02d", index)
		set = append(set, map[string]any{"predicate_id": "predicate:" + name, "ordinal": index, "outcome_kind": "check", "outcome_payload": check(name)})
	}
	return set
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
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
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
		{"malformed last entry", `{"verdicts":[{"predicate_id":"predicate:batch-present","evaluation_evidence":["evidence:return-route-verification"]},{"predicate_id":"predicate:batch-absent","verdict_kind":"excellent"}]}`, KindInvalidPayload},
		{"unbound last entry", `{"verdicts":[{"predicate_id":"predicate:batch-present","evaluation_evidence":["evidence:return-route-verification"]},{"predicate_id":"predicate:batch-absent","evaluation_evidence":["evidence:never-bound"]}]}`, KindMissingEvidence},
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

// TestWorkflowVerdictBatchLateRecoveryResolvesActiveContract proves the
// batched late-recovery route resolves an omitted contract_version to the
// active contract, exactly as the verdict constructor does at the
// verification step: after a supersession to v2, an eligible batch without a
// version passes admission and mutates, an ineligible entry refuses the batch
// as a whole, and the released single-form defaulting is unchanged.
func TestWorkflowVerdictBatchLateRecoveryResolvesActiveContract(t *testing.T) {
	const workID = "batch-late-active-version"
	fixture := seedWorkflowVerdictBatchFixtureOnFixtureDefinition(t, workID)
	s := fixture.store

	// Three non-ok verdicts at the verification step, so the successor keeps
	// full coverage through the carried verdicts and every predicate holds an
	// eligible latest verdict at the late recovery step.
	v1Mismatches := json.RawMessage(`{"contract_version":1,"verdicts":[` +
		`{"predicate_id":"predicate:batch-present","verdict_kind":"outcome_mismatch","incomparable_with_approved":true,"evaluation_evidence":["evidence:return-route-verification"]},` +
		`{"predicate_id":"predicate:batch-absent","verdict_kind":"insufficient_evidence","evaluation_evidence":["evidence:return-route-review"]},` +
		`{"predicate_id":"predicate:batch-third","verdict_kind":"insufficient_evidence","evaluation_evidence":["evidence:return-route-artifact"]}]}`)
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", v1Mismatches, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("verification-step mismatch batch: %v", err)
	}
	seedComparisonObservation(t, s, workID)
	predicate := func(id string, ordinal int) string {
		return `{"predicate_id":"predicate:` + id + `","ordinal":` + fmt.Sprint(ordinal) + `,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:` + id + `","immutable_subject_ref":"commit:` + workID + `","expected_result":"pass"}}`
	}
	successor := json.RawMessage(`{"contract_version":2,"premise":"corrected premise","outcome_predicates":[` +
		predicate("batch-present", 0) + "," + predicate("batch-absent", 1) + "," + predicate("batch-third", 2) +
		`],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_modifies":[],"rigor_class":"prototype_internal","supersede_reason":"correct the accepted premise","audit_evidence":["evidence:batch-late-supersede"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", successor, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("supersede contract at the verification step: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "confirm_premise", json.RawMessage(`{"contract_version":2}`), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("confirm premise on the successor: %v", err)
	}
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "release" {
		t.Fatalf("step before the late recovery = %q, want release", step)
	}
	verdictEvents := func() int64 {
		var count int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	// The released single-form defaulting is unchanged: a late single verdict
	// without a version still refuses against the active successor, while the
	// explicit successor version is admitted.
	singleNoVersion := json.RawMessage(`{"predicate_id":"predicate:batch-present","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}`)
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", singleNoVersion, fixture.owner, fixture.operator); err == nil {
		t.Fatal("single-form late verdict without a version must keep the released refusal")
	}
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", json.RawMessage(`{"contract_version":2,"predicate_id":"predicate:batch-present","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}`), fixture.owner, fixture.operator); err != nil {
		t.Fatalf("explicit successor single verdict: %v", err)
	}

	// A mixed batch without a version refuses as a whole: batch-present is
	// now ok and comparable, so one ineligible entry writes nothing.
	before := verdictEvents()
	mixed := json.RawMessage(`{"verdicts":[` +
		`{"predicate_id":"predicate:batch-absent","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-review"]},` +
		`{"predicate_id":"predicate:batch-present","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-verification"]}]}`)
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", mixed, fixture.owner, fixture.operator); err == nil {
		t.Fatal("mixed batch with an ineligible entry must refuse as a whole")
	}
	if got := verdictEvents(); got != before {
		t.Fatalf("refused mixed batch moved verdict events from %d to %d", before, got)
	}

	// Every entry eligible: the batch without a version passes admission and
	// mutates against the active successor, stamping contract_version 2 on
	// each recorded verdict.
	eligible := json.RawMessage(`{"verdicts":[` +
		`{"predicate_id":"predicate:batch-absent","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-review"]},` +
		`{"predicate_id":"predicate:batch-third","verdict_kind":"ok","evaluation_evidence":["evidence:return-route-artifact"]}]}`)
	if err := InspectWorkflowActionAdmission(context.Background(), s, WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: "record_verdict", Payload: eligible, Actor: fixture.owner,
	}); err != nil {
		t.Fatalf("late batch admission without a version: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "record_verdict", eligible, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("late batch mutation without a version must resolve the active contract: %v", err)
	}
	var versions []int64
	rows, err := s.DatabaseForTesting().Query(`SELECT json_extract(payload,'$.contract_version') FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 2`, workID, WorkflowVerdictRecorded)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0] != 2 || versions[1] != 2 {
		t.Fatalf("late batch verdict versions = %v, want contract_version 2 on both entries", versions)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "release" {
		t.Fatalf("step after the late batch = %q, want release", step)
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

// batchVerdictCompletion reads the work's latest workflow.action_completed
// event, so a test can hold the durable operation against the evidence union
// its completion declared.
func batchVerdictCompletion(t *testing.T, s *Store, workID string) (eventID string, entryCount int, refs []string) {
	t.Helper()
	var payload []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_id, payload FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowActionCompleted).Scan(&eventID, &payload); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		VerdictEntryCount  int      `json:"verdict_entry_count"`
		ResultEvidenceRefs []string `json:"result_evidence_refs"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	return eventID, decoded.VerdictEntryCount, decoded.ResultEvidenceRefs
}

// batchVerdictDurableRefs reads the durable operation's evidence authority —
// the complete deduplicated union the call resolved.
func batchVerdictDurableRefs(t *testing.T, s *Store, operationID string) []string {
	t.Helper()
	var raw []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT evidence_refs FROM durable_operations WHERE op_id=?`, operationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var refs []string
	if err := json.Unmarshal(raw, &refs); err != nil {
		t.Fatal(err)
	}
	return refs
}

// batchVerdictRefSet sorts a copy of refs, so two unions compare as sets.
func batchVerdictRefSet(refs []string) []string {
	sorted := append([]string(nil), refs...)
	sort.Strings(sorted)
	return sorted
}

// TestWorkflowVerdictBatchEvidenceUnionIsTheDurableAuthority proves the
// declared batch bound holds the schema, not an aggregate cap: two entries of
// 17 distinct durably bound references each — a 34-reference union past the
// single-form per-operation bound — record both verdicts, the durable
// operation and its completion name the complete union, and RebuildFromLog
// reproduces the batched history from the event log alone.
func TestWorkflowVerdictBatchEvidenceUnionIsTheDurableAuthority(t *testing.T) {
	const workID = "batch-union-authority"
	fixture := seedWorkflowVerdictBatchFixtureWithEvidence(t, workID, 32)
	version := verdictItemVersion(t, fixture.store, workID)
	operationID := fmt.Sprintf("issue933-record_verdict-%s-%d", workID, version)

	extra := func(from, to int) []string {
		refs := make([]string, 0, to-from)
		for index := from; index < to; index++ {
			refs = append(refs, fmt.Sprintf("evidence:batch-extra-%02d", index))
		}
		return refs
	}
	presentRefs := append([]string{"evidence:return-route-verification"}, extra(0, 16)...)
	absentRefs := append(extra(16, 32), "evidence:return-route-review")
	batchPayload, err := json.Marshal(map[string]any{"contract_version": 1, "verdicts": []any{
		map[string]any{"predicate_id": "predicate:batch-present", "verdict_kind": "ok", "evaluation_evidence": presentRefs},
		map[string]any{"predicate_id": "predicate:batch-absent", "verdict_kind": "ok", "evaluation_evidence": absentRefs},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", batchPayload, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("a 34-reference batch of separately bound entries refused: %v", err)
	}

	want := batchVerdictRefSet(append(append([]string{}, presentRefs...), absentRefs...))
	if len(want) != 34 {
		t.Fatalf("test built a %d-reference union, want the 34 distinct locators", len(want))
	}
	eventID, entryCount, completionRefs := batchVerdictCompletion(t, fixture.store, workID)
	if entryCount != 2 {
		t.Fatalf("completion verdict_entry_count = %d, want the declared 2 entries", entryCount)
	}
	if len(completionRefs) != 34 || strings.Join(batchVerdictRefSet(completionRefs), "|") != strings.Join(want, "|") {
		t.Fatalf("completion result_evidence_refs holds %d refs, want the complete 34-reference union", len(completionRefs))
	}
	if eventID != operationID+":completed" {
		t.Fatalf("completion event id = %q, want the batch operation's", eventID)
	}
	durableRefs := batchVerdictDurableRefs(t, fixture.store, operationID)
	if len(durableRefs) != 34 || strings.Join(batchVerdictRefSet(durableRefs), "|") != strings.Join(want, "|") {
		t.Fatalf("durable operation evidence authority holds %d refs, want the complete 34-reference union", len(durableRefs))
	}
	var recorded int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 2 {
		t.Fatalf("batch recorded %d verdicts, want 2", recorded)
	}
	if err := RebuildFromLog(context.Background(), fixture.store); err != nil {
		t.Fatalf("rebuild workflow event log: %v", err)
	}
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 2 {
		t.Fatalf("rebuild reproduced %d verdicts, want 2", recorded)
	}
}

// TestWorkflowVerdictBatchMaximumUnionRebuilds proves the schema-bounded
// maximum batch succeeds with its complete durable union: eight entries of 32
// distinct durably bound references each — the 256-reference union the
// payload schema bounds — record eight verdicts in one call, the completion
// declares the eight-entry count, the fold admits the union, and
// RebuildFromLog reproduces the history.
func TestWorkflowVerdictBatchMaximumUnionRebuilds(t *testing.T) {
	const workID = "batch-union-maximum"
	fixture := seedWorkflowVerdictBatchFixturePredicates(t, workID, 256, []string{"verification"}, batchVerdictPredicateSet(workID, 8))
	version := verdictItemVersion(t, fixture.store, workID)
	operationID := fmt.Sprintf("issue933-record_verdict-%s-%d", workID, version)

	entries := make([]any, 0, 8)
	want := make([]string, 0, 256)
	for index := 0; index < 8; index++ {
		refs := make([]string, 0, 32)
		for ref := 0; ref < 32; ref++ {
			refs = append(refs, fmt.Sprintf("evidence:batch-extra-%02d", index*32+ref))
		}
		want = append(want, refs...)
		entries = append(entries, map[string]any{"predicate_id": fmt.Sprintf("predicate:batch-p%02d", index), "verdict_kind": "ok", "evaluation_evidence": refs})
	}
	batchPayload, err := json.Marshal(map[string]any{"contract_version": 1, "verdicts": entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", batchPayload, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("the schema-bounded 256-reference batch refused: %v", err)
	}

	wantSet := batchVerdictRefSet(want)
	eventID, entryCount, completionRefs := batchVerdictCompletion(t, fixture.store, workID)
	if entryCount != 8 {
		t.Fatalf("completion verdict_entry_count = %d, want the declared 8 entries", entryCount)
	}
	if len(completionRefs) != 256 || strings.Join(batchVerdictRefSet(completionRefs), "|") != strings.Join(wantSet, "|") {
		t.Fatalf("completion result_evidence_refs holds %d refs, want the complete 256-reference union", len(completionRefs))
	}
	if eventID != operationID+":completed" {
		t.Fatalf("completion event id = %q, want the batch operation's", eventID)
	}
	durableRefs := batchVerdictDurableRefs(t, fixture.store, operationID)
	if len(durableRefs) != 256 || strings.Join(batchVerdictRefSet(durableRefs), "|") != strings.Join(wantSet, "|") {
		t.Fatalf("durable operation evidence authority holds %d refs, want the complete 256-reference union", len(durableRefs))
	}
	var recorded int64
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 8 {
		t.Fatalf("batch recorded %d verdicts, want 8", recorded)
	}
	if err := RebuildFromLog(context.Background(), fixture.store); err != nil {
		t.Fatalf("rebuild workflow event log: %v", err)
	}
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowVerdictRecorded).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 8 {
		t.Fatalf("rebuild reproduced %d verdicts, want 8", recorded)
	}
}

// TestWorkflowVerdictBatchMixedDefaultEvidenceIsPerEntry proves the
// mixed-evidence isolation: with artifact-only evidence on the first entry
// and an evidence-defaulted second entry, the defaulted entry resolves the
// operation-minted reference alone. It never inherits the first entry's
// artifact, and the mint binds no required kind onto evidence the caller
// supplied for a different entry — the minted binding kinds stay exactly the
// contract's required kinds.
func TestWorkflowVerdictBatchMixedDefaultEvidenceIsPerEntry(t *testing.T) {
	const workID = "batch-mixed-default-per-entry"
	fixture := seedWorkflowVerdictBatchFixture(t, workID)
	version := verdictItemVersion(t, fixture.store, workID)
	operationID := fmt.Sprintf("issue933-record_verdict-%s-%d", workID, version)
	mintedRef := "evidence:" + operationID
	artifact := "evidence:return-route-artifact"

	batchPayload, err := json.Marshal(map[string]any{"contract_version": 1, "verdicts": []any{
		map[string]any{"predicate_id": "predicate:batch-present", "verdict_kind": "ok", "evaluation_evidence": []string{artifact}},
		map[string]any{"predicate_id": "predicate:batch-absent"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", batchPayload, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("mixed explicit and defaulted batch refused: %v", err)
	}

	entryEvidence := func(predicate string) []string {
		var raw []byte
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.predicate_id')=?`, workID, WorkflowVerdictRecorded, predicate).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			EvaluationEvidence []string `json:"evaluation_evidence"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded.EvaluationEvidence
	}
	if got := entryEvidence("predicate:batch-present"); len(got) != 1 || got[0] != artifact {
		t.Fatalf("explicit entry evidence = %v, want exactly its own artifact", got)
	}
	if got := entryEvidence("predicate:batch-absent"); len(got) != 1 || got[0] != mintedRef {
		t.Fatalf("defaulted entry evidence = %v, want exactly the operation-minted %s", got, mintedRef)
	}
	// The mint bound exactly its unchanged kind set — the definition's
	// verification, review, and artifact kinds — and bound them only on the
	// operation-minted reference. The first entry's artifact gained no
	// binding from this call; its seeded artifact-kind binding is the only
	// one it carries.
	var mintedBindings int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref')=?`, workID, WorkflowEvidenceBound, operationID).Scan(&mintedBindings); err != nil {
		t.Fatal(err)
	}
	if mintedBindings != 3 {
		t.Fatalf("the mint produced %d bindings, want exactly the minted reference's verification, review, and artifact bindings", mintedBindings)
	}
	var mintedKinds []string
	rows, err := fixture.store.DatabaseForTesting().Query(`SELECT json_extract(payload,'$.evidence_kind') FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref')=?`, workID, WorkflowEvidenceBound, operationID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatal(err)
		}
		mintedKinds = append(mintedKinds, kind)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(mintedKinds)
	if strings.Join(mintedKinds, ",") != "artifact,review,verification" {
		t.Fatalf("minted binding kinds = %v, want the unchanged artifact, review, verification set", mintedKinds)
	}
	var mintedOnArtifact int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref')=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, operationID, artifact).Scan(&mintedOnArtifact); err != nil {
		t.Fatal(err)
	}
	if mintedOnArtifact != 0 {
		t.Fatal("the mint bound a kind onto the explicit entry's artifact")
	}
	var artifactBindings int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, artifact).Scan(&artifactBindings); err != nil {
		t.Fatal(err)
	}
	if artifactBindings != 1 {
		t.Fatalf("the artifact carries %d bindings, want only its seeded artifact-kind binding", artifactBindings)
	}
}

// TestWorkflowVerdictBatchEvidenceUnionSpansNativeRunEnrichment proves the
// required native-run capture joins both the mint set and the durable union:
// with a verification and native_run contract, a mixed batch resolves its
// defaulted entry against the operation-minted reference plus the verified
// capture, mints the capture under the non-native required kind exactly as a
// defaulted single call does, names the capture and the minted reference in
// the completed operation's evidence union, and an all-defaulted batch shares
// that one minted set across every entry.
func TestWorkflowVerdictBatchEvidenceUnionSpansNativeRunEnrichment(t *testing.T) {
	const workID = "batch-union-native-run"
	fixture := seedWorkflowVerdictBatchFixtureRequiring(t, workID, 4, []string{"verification", "native_run"})
	capture := seedVerifiedNativeRunCapture(t, fixture.store, workID)
	version := verdictItemVersion(t, fixture.store, workID)
	operationID := fmt.Sprintf("issue933-record_verdict-%s-%d", workID, version)
	mintedRef := "evidence:" + operationID
	explicitRefs := []string{"evidence:return-route-artifact", "evidence:batch-extra-00"}

	marshal := func(value map[string]any) json.RawMessage {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	mixedBatch := marshal(map[string]any{"contract_version": 1, "verdicts": []any{
		map[string]any{"predicate_id": "predicate:batch-present", "verdict_kind": "ok", "evaluation_evidence": explicitRefs},
		map[string]any{"predicate_id": "predicate:batch-absent"},
	}})
	if err := runIssue933OperatorAction(t, fixture.store, workID, "record_verdict", mixedBatch, fixture.owner, fixture.operator); err != nil {
		t.Fatalf("mixed batch under a verification and native_run contract refused: %v", err)
	}
	entryEvidence := func(predicate string) []string {
		var raw []byte
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.predicate_id')=?`, workID, WorkflowVerdictRecorded, predicate).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			EvaluationEvidence []string `json:"evaluation_evidence"`
		}
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded.EvaluationEvidence
	}
	if got := entryEvidence("predicate:batch-present"); strings.Join(got, "|") != strings.Join(explicitRefs, "|") {
		t.Fatalf("explicit entry evidence = %v, want exactly its own references", got)
	}
	if got := entryEvidence("predicate:batch-absent"); strings.Join(got, "|") != mintedRef+"|"+capture {
		t.Fatalf("defaulted entry evidence = %v, want exactly the minted reference plus the verified capture", got)
	}
	// The mint bound its unchanged kind set — verification, review, and
	// artifact — on the minted reference and on the verified capture, plus
	// native_run on the capture alone: seven bindings, none on the explicit
	// entry's references.
	var mintedBindings int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref')=?`, workID, WorkflowEvidenceBound, operationID).Scan(&mintedBindings); err != nil {
		t.Fatal(err)
	}
	if mintedBindings != 7 {
		t.Fatalf("the mint produced %d bindings, want the kind set on the minted reference and the capture plus native_run", mintedBindings)
	}
	bindingCount := func(ref string) int {
		t.Helper()
		var count int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref')=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, operationID, ref).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if got := bindingCount(mintedRef); got != 3 {
		t.Fatalf("the minted reference carries %d minted bindings, want the three non-native kinds", got)
	}
	if got := bindingCount(capture); got != 4 {
		t.Fatalf("the capture carries %d minted bindings, want the three non-native kinds plus native_run", got)
	}
	var captureVerification int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')='verification' AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, capture).Scan(&captureVerification); err != nil {
		t.Fatal(err)
	}
	if captureVerification == 0 {
		t.Fatal("the defaulted entry's mint bound no verification kind on the verified capture")
	}
	for _, ref := range explicitRefs {
		var mintedOnExplicit int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref')=? AND json_extract(payload,'$.immutable_subject_ref')=?`, workID, WorkflowEvidenceBound, operationID, ref).Scan(&mintedOnExplicit); err != nil {
			t.Fatal(err)
		}
		if mintedOnExplicit != 0 {
			t.Fatalf("the mint bound a kind onto the explicit entry's %s", ref)
		}
	}
	_, entryCount, completionRefs := batchVerdictCompletion(t, fixture.store, workID)
	if entryCount != 2 {
		t.Fatalf("completion verdict_entry_count = %d, want 2", entryCount)
	}
	want := batchVerdictRefSet(append(append([]string{}, explicitRefs...), mintedRef, capture))
	if len(completionRefs) != 4 || strings.Join(batchVerdictRefSet(completionRefs), "|") != strings.Join(want, "|") {
		t.Fatalf("completion result_evidence_refs = %v, want the complete four-reference union", completionRefs)
	}
	if err := RebuildFromLog(context.Background(), fixture.store); err != nil {
		t.Fatalf("rebuild workflow event log: %v", err)
	}

	// An all-defaulted batch under the same contract shares the one minted
	// set: the capture is bound under the non-native required kind and every
	// verdict names it, matching the defaulted single call.
	const defaultedWork = "batch-defaulted-native-run"
	defaulted := seedWorkflowVerdictBatchFixtureRequiring(t, defaultedWork, 0, []string{"verification", "native_run"})
	defaultedCapture := seedVerifiedNativeRunCapture(t, defaulted.store, defaultedWork)
	defaultedBatch := marshal(map[string]any{"contract_version": 1, "verdicts": []any{
		map[string]any{"predicate_id": "predicate:batch-present"},
		map[string]any{"predicate_id": "predicate:batch-absent"},
	}})
	if err := runIssue933OperatorAction(t, defaulted.store, defaultedWork, "record_verdict", defaultedBatch, defaulted.owner, defaulted.operator); err != nil {
		t.Fatalf("defaulted batch under a native_run contract: %v", err)
	}
	var defaultedCaptureVerification int
	if err := defaulted.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.evidence_kind')='verification' AND json_extract(payload,'$.immutable_subject_ref')=?`, defaultedWork, WorkflowEvidenceBound, defaultedCapture).Scan(&defaultedCaptureVerification); err != nil {
		t.Fatal(err)
	}
	if defaultedCaptureVerification == 0 {
		t.Fatal("the defaulted batch minted no verification binding on the verified capture")
	}
	var naming int
	if err := defaulted.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=? AND EXISTS (SELECT 1 FROM json_each(json_extract(payload,'$.evaluation_evidence')) WHERE value=?)`, defaultedWork, WorkflowVerdictRecorded, defaultedCapture).Scan(&naming); err != nil {
		t.Fatal(err)
	}
	if naming != 2 {
		t.Fatalf("%d batched verdicts name the capture, want every defaulted entry sharing the minted set", naming)
	}
}
