package store

// The decision-fact write boundary for a carried historical contract: a
// synthetic legacy v1 definition pins the work, the legacy contract stores
// NULL absence, and a real WorkflowDefinitionSelected carry event moves the
// instance to the same-ref consequence_v1 v2 without rewriting any seeded
// state. Recovery is a complete flat supersede_contract action payload that
// carries the explicit decision_domains fact, authored through the actual
// workflowSupersedeContractEvents constructor and folded through the
// registered fold, so the successor stores [] at its own version and the
// engineering confirmation proceeds under the carried new policy.

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDecisionFactWriteBoundary(t *testing.T) {
	t.Parallel()

	t.Run("carried historical contract recovers through a complete explicit-fact successor", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-carry-recovery"

		// One local registry holds the synthetic legacy v1 twin and the
		// same-ref consequence_v1 v2, registered in increasing version order.
		definition := approvalRouteDefinition(t)
		legacy := definition
		legacy.ApprovalPolicy = ""
		higher := definition
		higher.Version = definition.Version + 1
		registry, legacyRegistered := newResolutionRegistry(t, legacy)
		higherRegistered, err := registry.Register(higher)
		if err != nil {
			t.Fatalf("the higher consequence_v1 definition refused registration: %v", err)
		}

		// The full legitimate prerequisites under the legacy pin: selection,
		// the actor triple, execution with its durable producer and bound
		// evidence, delivery, and the investigation artifact. The legacy
		// contract carries the null wire shape for the decision fact, which
		// stores NULL absence.
		s, owner := seedResolutionSelection(t, workID, registry, legacyRegistered)
		actors := recordApprovalActors(t, s, workID)
		ownerRef := ownerRefForTest(t, owner)
		advanceResolutionToExecution(t, s, registry, workID, owner, nil)
		advanceResolutionDelivery(t, s, registry, workID, owner)
		seedResolutionInvestigation(t, s, workID)

		// Capture the legacy state the carry forward must preserve.
		var premise string
		var legacyDomainsAbsent int
		if err := s.DatabaseForTesting().QueryRow(`SELECT premise,decision_domains IS NULL FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&premise, &legacyDomainsAbsent); err != nil {
			t.Fatal(err)
		}
		if legacyDomainsAbsent != 1 {
			t.Fatalf("the legacy contract stored decision_domains absent=%d, want NULL absence", legacyDomainsAbsent)
		}
		var contractPayload []byte
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowContractApproved).Scan(&contractPayload); err != nil {
			t.Fatalf("read the legacy contract event: %v", err)
		}
		if !strings.Contains(string(contractPayload), `"decision_domains":null`) {
			t.Fatalf("the legacy contract recorded payload %s, want the null wire shape", contractPayload)
		}
		var stepBefore string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&stepBefore); err != nil {
			t.Fatal(err)
		}
		if stepBefore != "acceptance" {
			t.Fatalf("the legacy seed parked the instance at %q, want acceptance", stepBefore)
		}
		if executionActor := approvalExecutionActor(t, s, workID); executionActor != ownerRef {
			t.Fatalf("the seed's executing actor = %q, want the owner %s", executionActor, ownerRef)
		}

		// The real carry event folds the instance onto the registered higher
		// consequence_v1 version at the owner's current version.
		carryVersion := readWorkVersion(t, s, workID)
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{
			Events: []Event{
				resolutionEvent("decision-fact-carry-"+workID, WorkflowDefinitionSelected, workID, ownerRef, carryVersion, map[string]any{
					"ref": higherRegistered.Definition.Ref, "version": higherRegistered.Definition.Version, "digest": higherRegistered.Digest,
					"work_kind": string(higherRegistered.Definition.WorkKind),
				}),
			},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): carryVersion},
		})

		// The carry kept the acceptance step, the NULL historical contract
		// with its recorded bytes, and the executing owner, and moved the pin
		// to the registered higher version and digest.
		var carriedRef, carriedDigest, carriedStep string
		var carriedVersion int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&carriedRef, &carriedVersion, &carriedDigest, &carriedStep); err != nil {
			t.Fatal(err)
		}
		if carriedRef != higherRegistered.Definition.Ref || carriedVersion != higherRegistered.Definition.Version || carriedDigest != higherRegistered.Digest {
			t.Fatalf("the carry forward pinned %s@%d %s, want the registered %s@%d %s", carriedRef, carriedVersion, carriedDigest, higherRegistered.Definition.Ref, higherRegistered.Definition.Version, higherRegistered.Digest)
		}
		if carriedStep != stepBefore {
			t.Fatalf("the carry forward moved the step from %q to %q, want acceptance kept", stepBefore, carriedStep)
		}
		var carriedPremise string
		var carriedDomainsAbsent int
		if err := s.DatabaseForTesting().QueryRow(`SELECT premise,decision_domains IS NULL FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&carriedPremise, &carriedDomainsAbsent); err != nil {
			t.Fatal(err)
		}
		if carriedPremise != premise || carriedDomainsAbsent != 1 {
			t.Fatalf("the carry forward changed the historical contract to %q (NULL absence %d), want %q preserved with NULL absence", carriedPremise, carriedDomainsAbsent, premise)
		}
		var carriedPayload []byte
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowContractApproved).Scan(&carriedPayload); err != nil {
			t.Fatalf("reread the legacy contract event: %v", err)
		}
		if string(carriedPayload) != string(contractPayload) {
			t.Fatalf("the carry forward rewrote the legacy contract event to %s, want %s preserved", carriedPayload, contractPayload)
		}
		if executionActor := approvalExecutionActor(t, s, workID); executionActor != ownerRef {
			t.Fatalf("the carry forward moved the execution actor to %q, want the owner %s", executionActor, ownerRef)
		}

		// The next contract version derives from the stored active version.
		activeVersion, err := activeWorkflowContractVersion(context.Background(), s.DatabaseForTesting(), workID, "workflow_decision_fact_write_test")
		if err != nil {
			t.Fatal(err)
		}
		nextVersion := activeVersion + 1

		// The complete flat supersede action payload: every declared recovery
		// field, the actual predicate and check immutable ref, and the
		// explicit engineering decision fact.
		predicates := []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}}
		payload := map[string]any{
			"contract_version":   nextVersion,
			"premise":            "recover the carried historical contract onto the explicit decision fact",
			"outcome_predicates": predicates,
			"required_evidence":  []string{"verification"},
			"route_conventions":  []string{},
			"spec_mandate":       []string{},
			"law_modifies":       []string{},
			"rigor_class":        "prototype_internal",
			"supersede_reason":   "the carried historical contract holds no explicit decision fact",
			"audit_evidence":     []string{"audit:decision-fact-successor-" + workID},
			"decision_domains":   []string{},
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}

		// The actual supersede constructor runs inside one transaction against
		// the carried definition; the transaction closes before the returned
		// events fold, because the store pools one connection.
		expected := readWorkVersion(t, s, workID)
		ctx := context.Background()
		tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		events, constructErr := workflowSupersedeContractEvents(ctx, tx, higher, WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: expected, ActionID: "supersede_contract",
			OperationID: "decision-fact-successor-" + workID, Now: time.Unix(12, 0).UTC(),
		}, ownerRef, raw, fields, "decision-fact-successor-"+workID, expected)
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if constructErr != nil {
			t.Fatalf("the complete explicit-fact successor refused construction under the carried consequence_v1 definition: %v", constructErr)
		}
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{
			Events:           events,
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): expected},
		})

		// The successor stores the exact explicit engineering fact at its own
		// version; the prior contract keeps NULL absence, keeps its recorded
		// event bytes, and names the successor as its superseder.
		if stored := storedDecisionDomains(t, s, workID, nextVersion); stored != "[]" {
			t.Fatalf("the successor contract stored decision_domains %q, want the exact explicit [] fact", stored)
		}
		var priorPremise string
		var priorDomainsAbsent int
		var supersededBy int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT premise,decision_domains IS NULL,superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, activeVersion).Scan(&priorPremise, &priorDomainsAbsent, &supersededBy); err != nil {
			t.Fatal(err)
		}
		if priorPremise != premise || priorDomainsAbsent != 1 {
			t.Fatalf("the superseded contract changed to %q (NULL absence %d), want %q preserved with NULL absence", priorPremise, priorDomainsAbsent, premise)
		}
		if supersededBy != nextVersion {
			t.Fatalf("the superseded contract names superseded_by=%d, want the successor version %d", supersededBy, nextVersion)
		}
		var finalPayload []byte
		if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowContractApproved).Scan(&finalPayload); err != nil {
			t.Fatalf("reread the legacy contract event after the successor fold: %v", err)
		}
		if string(finalPayload) != string(contractPayload) {
			t.Fatalf("the successor fold rewrote the legacy contract event to %s, want %s preserved", finalPayload, contractPayload)
		}
		assertFactWriteProjectedDomains(t, s, registry, workID, nextVersion, []string{})

		// The pending and withheld fact gates are gone at both engineering
		// levels: the continuity snapshot's work pin and the work pin's own
		// operator-question projection.
		snapshot := approvalContinuitySnapshot(t, s, registry, workID)
		if question := approvalWorkPinQuestion(snapshot); question != nil {
			t.Fatalf("the explicit-fact successor kept a pending operator question: %+v", question)
		}
		if withheld := approvalWorkPinWithheld(snapshot); withheld != nil {
			t.Fatalf("the explicit-fact successor kept a withheld operator question: %+v", withheld)
		}
		projectedQuestion, projectedWithheld := approvalOperatorQuestionProjection(t, s, registry, higher, higherRegistered.Digest, workID)
		if projectedQuestion != nil || projectedWithheld != nil {
			t.Fatalf("engineering recovery projects question %+v withheld %+v, want neither", projectedQuestion, projectedWithheld)
		}

		// The successor's supersession made the delivered artifact stale
		// (CD-0201 D5), so the evaluator's first verdict is admitted only as
		// a non-ok one. The owner's declared correction route returns the
		// instance to execution, and a genuinely new record_delivery there
		// re-produces the artifact through the normal event fold; the stale
		// bit survives the correction and clears only on that fresh
		// production.
		if err := runApprovalActionTx(t, s, registry, workID, "record_verdict", actors.reviewer, nil, "", "", `{"predicate_id":"predicate:primary","verdict_kind":"outcome_mismatch","evaluation_evidence":["evidence:operator-verdict"],"incomparable_with_approved":true}`); err != nil {
			t.Fatalf("the failed verdict was not admitted while the superseded artifact was stale: %v", err)
		}
		if err := runApprovalActionTx(t, s, registry, workID, "request_correction", owner, nil, "", "", `{"diagnosis":"the delivered subject does not satisfy the approved predicate","strategy":"repeat the implementation external effect","predicate_ids":["predicate:primary"],"evidence_refs":["evidence:operator-verdict"]}`); err != nil {
			t.Fatalf("the owner's declared correction route refused: %v", err)
		}
		if state := foldedApprovalAdmission(t, s, registry, higher, workID); !state.ArtifactStale {
			t.Fatal("the artifact folded fresh after the correction, want stale until fresh production at execution")
		}
		redeliveryVersion := readWorkVersion(t, s, workID)
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{
			Events: []Event{
				workflowActionCompletedFixture("resolution-redelivery-"+workID, workID, ownerRef, redeliveryVersion, "execution", "record_delivery"),
			},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): redeliveryVersion},
		})
		if state := foldedApprovalAdmission(t, s, registry, higher, workID); state.ArtifactStale {
			t.Fatal("the artifact folded stale after the fresh record_delivery at execution, want the production frontier past the stale cause")
		}

		// The independent evaluator records its verdict under the new active
		// contract through its admitted public route.
		if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused under the explicit-fact successor: %v", err)
		}
		if verdictActor := latestApprovalEventActor(t, s, workID, WorkflowVerdictRecorded); verdictActor != actors.reviewerRef {
			t.Fatalf("verdict attribution = %q, want the independent evaluator %s", verdictActor, actors.reviewerRef)
		}

		// The executing coordinator confirms through the public action route
		// with no operator actor and no human digest.
		if err := runApprovalActionTx(t, s, registry, workID, "confirm_premise", owner, nil, "confirm", "", `{"selected_choice":"confirm"}`); err != nil {
			t.Fatalf("the engineering coordinator confirmation refused under the explicit-fact successor: %v", err)
		}
		confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, nextVersion)
		if !ok || confirmed != ownerRef {
			t.Fatalf("confirmation row = %q (present %v), want the executing coordinator %s at contract version %d", confirmed, ok, ownerRef, nextVersion)
		}
		if eventActor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed); eventActor != ownerRef {
			t.Fatalf("confirmation event actor = %q, want the executing coordinator %s", eventActor, ownerRef)
		}
		confirmedEvent := latestApprovalEventPayload(t, s, workID, WorkflowPremiseConfirmed)
		if confirmedEvent["confirmation_authority"] != "coordinator" || confirmedEvent["confirming_actor_ref"] != ownerRef {
			t.Fatalf("confirmation event = %v/%v, want coordinator authority by the executing coordinator", confirmedEvent["confirmation_authority"], confirmedEvent["confirming_actor_ref"])
		}
		if _, consumed := confirmedEvent["approval_ref"]; consumed {
			t.Fatalf("the confirmation consumed an approval binding: %v", confirmedEvent["approval_ref"])
		}
		if _, operatorRecorded := confirmedEvent["operator_actor_ref"]; operatorRecorded {
			t.Fatalf("the confirmation fabricated operator attribution: %v", confirmedEvent["operator_actor_ref"])
		}

		// No consent challenge was persisted or consumed, no approval-kind
		// evidence was fabricated, and no fold guard row survived.
		if challenges := approvalChallengeCount(t, s); challenges != 0 {
			t.Fatalf("the recovery witness left %d persisted consent challenges", challenges)
		}
		if kind := approvalKindEvidenceCount(t, s, workID); kind != 0 {
			t.Fatalf("the recovery witness left %d approval-kind evidence bindings", kind)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("all eight canonical decision facts persist exact event and stored bytes on genuine initial writes", func(t *testing.T) {
		t.Parallel()
		for _, set := range canonicalDecisionDomainSets() {
			workID := "decision-fact-initial-" + factWriteSetLabel(set)
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			// The genuine local selection starts before the initial contract:
			// the prefix folds the contract as the work's first one, carrying
			// the exact fact at the current event payload version.
			advanceResolutionPrefix(t, s, registry, workID, owner, set, false)
			ownerRef := ownerRefForTest(t, owner)
			expected := workflowJSON(set)
			if stored := storedDecisionDomains(t, s, workID, 1); stored != expected {
				t.Fatalf("the initial contract for %s stored decision_domains %q, want the exact canonical bytes %q", expected, stored, expected)
			}
			payload := factWriteEventPayloadByID(t, s, "resolution-contract-"+workID)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(payload), &fields); err != nil {
				t.Fatal(err)
			}
			member, present := fields["decision_domains"]
			if !present || string(member) != expected {
				t.Fatalf("the initial contract event for %s recorded decision_domains %q (present %v), want the exact canonical member %q", expected, member, present, expected)
			}
			assertFactWriteProjectedDomains(t, s, registry, workID, 1, set)
			var rowRef, rowDigest, approvedBy string
			var rowVersion int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest,approved_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&rowRef, &rowVersion, &rowDigest, &approvedBy); err != nil {
				t.Fatal(err)
			}
			if rowRef != registered.Definition.Ref || rowVersion != registered.Definition.Version || rowDigest != registered.Digest {
				t.Fatalf("the initial contract pinned %s@%d %s, want the registered %s@%d %s", rowRef, rowVersion, rowDigest, registered.Definition.Ref, registered.Definition.Version, registered.Digest)
			}
			if approvedBy != ownerRef {
				t.Fatalf("the initial contract recorded approved_by %q, want the authoring owner %s", approvedBy, ownerRef)
			}
			s.Close()
		}
	})

	t.Run("missing null and broken initial facts refuse typed with no contract event version or guard effect", func(t *testing.T) {
		t.Parallel()
		for _, testCase := range []struct {
			name     string
			fact     any
			present  bool
			wantKind FailureKind
			required bool
		}{
			{name: "nonstring string", fact: "product", present: true, wantKind: KindInvalidPayload},
			{name: "nonstring object", fact: map[string]any{"product": true}, present: true, wantKind: KindInvalidPayload},
			{name: "unknown member", fact: []string{"preference", "economics"}, present: true, wantKind: KindInvalidPayload},
			{name: "duplicate member", fact: []string{"product", "product"}, present: true, wantKind: KindInvalidPayload},
			{name: "noncanonical order", fact: []string{"product", "preference"}, present: true, wantKind: KindInvalidPayload},
			{name: "over the closed set", fact: []string{"preference", "product", "ui_ux", "preference"}, present: true, wantKind: KindInvalidPayload},
			// Neither missing nor null declares an engineering fact.
			{name: "missing member", present: false, required: true},
			{name: "explicit null", fact: nil, present: true, required: true},
		} {
			workID := "decision-fact-initial-refuse-" + strings.ReplaceAll(testCase.name, " ", "-")
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			baselineVersion := readWorkVersion(t, s, workID)
			baselineEvents := approvalEventCount(t, s, workID)
			event := factWriteInitialContractEvent(t, s, workID, ownerRefForTest(t, owner), testCase.fact, testCase.present)
			operation := Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion}}
			err := tryAppendWorkflowEventsWithRegistry(s, registry, operation)
			var failure *Failure
			if testCase.required {
				if err == nil {
					s.Close()
					t.Fatalf("the %s initial contract under the consequence_v1 pin was accepted without any typed refusal; required decision-fact presence must refuse it before any write", testCase.name)
				}
				if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "absence is a legacy shape") {
					s.Close()
					t.Fatalf("the %s initial contract returned %v, want a typed refusal", testCase.name, err)
				}
			} else if err == nil || !failureAs(err, &failure) || failure.Kind != testCase.wantKind {
				s.Close()
				t.Fatalf("the %s initial contract returned %v, want the typed %s refusal", testCase.name, err, testCase.wantKind)
			}
			assertFactWriteInitialNoEffect(t, s, workID, baselineVersion, baselineEvents)
			s.Close()
		}
	})

	t.Run("all eight canonical decision facts persist on complete successors without mutating the predecessor", func(t *testing.T) {
		t.Parallel()
		for _, set := range canonicalDecisionDomainSets() {
			workID := "decision-fact-successor-write-" + factWriteSetLabel(set)
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			advanceResolutionPrefix(t, s, registry, workID, owner, []string{}, false)
			ownerRef := ownerRefForTest(t, owner)
			baselineVersion := readWorkVersion(t, s, workID)
			predecessorPayload := factWriteEventPayloadByID(t, s, "resolution-contract-"+workID)
			appendWorkflowEventsWithRegistry(t, s, registry, Operation{
				Events:           []Event{factWriteSuccessorEvent(t, s, workID, ownerRef, set, true)},
				ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion},
			})
			expected := workflowJSON(set)
			if stored := storedDecisionDomains(t, s, workID, 2); stored != expected {
				t.Fatalf("the complete successor for %s stored decision_domains %q, want the exact canonical bytes %q", expected, stored, expected)
			}
			if stored := storedDecisionDomains(t, s, workID, 1); stored != "[]" {
				t.Fatalf("the superseded predecessor for %s stored %q, want its recorded [] fact immutable", expected, stored)
			}
			var supersededBy int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil {
				t.Fatal(err)
			}
			if supersededBy != 2 {
				t.Fatalf("the superseded predecessor for %s names superseded_by=%d, want the successor version 2", expected, supersededBy)
			}
			if factWriteEventPayloadByID(t, s, "resolution-contract-"+workID) != predecessorPayload {
				t.Fatalf("the successor fold for %s rewrote the predecessor contract event bytes", expected)
			}
			assertFactWriteProjectedDomains(t, s, registry, workID, 2, set)
			if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
				t.Fatalf("the successor fold for %s left the work at version %d, want the one derived advance from %d", expected, version, baselineVersion)
			}
			s.Close()
		}
	})

	t.Run("broken successor facts refuse atomically beside a live confirmation", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-broken-successor"
		s, registry, _, owner, baselineVersion := seedFactWriteConfirmedEngineering(t, workID)
		ownerRef := ownerRefForTest(t, owner)
		for _, fact := range []struct {
			name  string
			value any
		}{
			{name: "unknown member", value: []string{"preference", "economics"}},
			{name: "duplicate member", value: []string{"product", "product"}},
			{name: "noncanonical order", value: []string{"product", "preference"}},
			{name: "over the closed set", value: []string{"preference", "product", "ui_ux", "preference"}},
			{name: "nonstring string", value: "product"},
			{name: "nonstring object", value: map[string]any{"ui_ux": true}},
		} {
			baselineEvents := approvalEventCount(t, s, workID)
			event := factWriteSuccessorEvent(t, s, workID, ownerRef, fact.value, true)
			operation := Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion}}
			err := tryAppendWorkflowEventsWithRegistry(s, registry, operation)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Fatalf("the successor with the %s fact returned %v, want the typed invalid-payload refusal", fact.name, err)
			}
			assertFactWritePredecessorPreserved(t, s, workID, baselineVersion, baselineEvents, ownerRef)
		}
	})

	t.Run("wrong pins and missing registries refuse a complete successor before any write", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-wrong-pin"
		definition := approvalRouteDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		advanceResolutionPrefix(t, s, registry, workID, owner, []string{}, false)
		ownerRef := ownerRefForTest(t, owner)
		baselineVersion := readWorkVersion(t, s, workID)
		baselineEvents := approvalEventCount(t, s, workID)
		event := factWriteSuccessorEvent(t, s, workID, ownerRef, []string{"product"}, true)
		operation := Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion}}
		// A second registry carries the same family and version with different
		// bytes, so only the stored digest can tell them apart; the empty
		// registry carries nothing that could fall back to the builtin.
		drifted := approvalRouteDefinition(t)
		drifted.EvaluatorIndependence.ModelDistinct = !drifted.EvaluatorIndependence.ModelDistinct
		driftRegistry := NewWorkflowDefinitionRegistry()
		if _, err := driftRegistry.Register(drifted); err != nil {
			t.Fatalf("the drifted definition refused registration: %v", err)
		}
		driftErr := tryAppendWorkflowEventsWithRegistry(s, driftRegistry, operation)
		var driftFailure *Failure
		if driftErr == nil || !failureAs(driftErr, &driftFailure) || driftFailure.Kind != KindInvariantViolation || !strings.Contains(driftFailure.Detail, "digest drifted") {
			t.Fatalf("the complete successor under a digest-drifted registry returned %v, want the drift refusal before any write", driftErr)
		}
		missingErr := tryAppendWorkflowEventsWithRegistry(s, NewWorkflowDefinitionRegistry(), operation)
		var missingFailure *Failure
		if missingErr == nil || !failureAs(missingErr, &missingFailure) {
			t.Fatalf("the complete successor under a registry missing the pinned definition returned %v, want a typed refusal", missingErr)
		}
		if contracts := decisionContractCount(t, s, workID); contracts != 1 {
			t.Fatalf("the refused successor writes left %d contract rows, want the active predecessor alone", contracts)
		}
		if stored := storedDecisionDomains(t, s, workID, 1); stored != "[]" {
			t.Fatalf("the refused successor writes changed the predecessor fact to %q, want [] preserved", stored)
		}
		var superseded any
		if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&superseded); err != nil {
			t.Fatal(err)
		}
		if superseded != nil {
			t.Fatalf("the refused successor writes named superseded_by %v, want the predecessor left active", superseded)
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion {
			t.Fatalf("the refused successor writes advanced the work version to %d", version)
		}
		if events := approvalEventCount(t, s, workID); events != baselineEvents {
			t.Fatalf("the refused successor writes left %d events (baseline %d), want rollback with no append", events, baselineEvents)
		}
		var pinRef, pinDigest string
		var pinVersion int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pinRef, &pinVersion, &pinDigest); err != nil {
			t.Fatal(err)
		}
		if pinRef != registered.Definition.Ref || pinVersion != registered.Definition.Version || pinDigest != registered.Digest {
			t.Fatalf("the refused successor writes moved the pin to %s@%d %s, want the registered %s@%d %s", pinRef, pinVersion, pinDigest, registered.Definition.Ref, registered.Definition.Version, registered.Digest)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("engineering to decision revision over a confirmed predecessor preserves attribution and refuses stale authority", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-revise-e2d"
		s, registry, definition, owner, actors := seedApprovalItemAtAcceptance(t, workID, []string{})
		ownerRef := ownerRefForTest(t, owner)
		if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused before the revision: %v", err)
		}
		if _, err := confirmResolutionPremise(t, s, registry, definition, workID, owner, nil, map[string]json.RawMessage{"selected_choice": json.RawMessage(`"confirm"`)}); err != nil {
			t.Fatalf("the executing coordinator's engineering confirmation refused: %v", err)
		}
		if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != ownerRef {
			t.Fatalf("the engineering confirmation row = %q (present %v), want the executing coordinator %s", confirmed, ok, ownerRef)
		}
		confirmEvent := factWriteEventPayloadByID(t, s, "resolution-confirm-"+workID)
		confirmActor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed)
		contractEvent := factWriteEventPayloadByID(t, s, "resolution-contract-"+workID)
		predecessorDigest := approvalDecisionDigest(t, s, workID)
		baselineVersion := readWorkVersion(t, s, workID)
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{
			Events:           []Event{factWriteSuccessorEvent(t, s, workID, ownerRef, []string{"product"}, true)},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion},
		})
		if stored := storedDecisionDomains(t, s, workID, 2); stored != `["product"]` {
			t.Fatalf("the decision successor stored decision_domains %q, want the exact [\"product\"] fact", stored)
		}
		if stored := storedDecisionDomains(t, s, workID, 1); stored != "[]" {
			t.Fatalf("the superseded engineering predecessor stored %q, want its recorded [] fact immutable", stored)
		}
		var supersededBy int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil {
			t.Fatal(err)
		}
		if supersededBy != 2 {
			t.Fatalf("the successful revision names superseded_by=%d, want the successor version 2", supersededBy)
		}
		if factWriteEventPayloadByID(t, s, "resolution-contract-"+workID) != contractEvent {
			t.Fatal("the revision rewrote the predecessor contract event bytes")
		}
		if factWriteEventPayloadByID(t, s, "resolution-confirm-"+workID) != confirmEvent {
			t.Fatal("the revision rewrote the predecessor confirmation event bytes")
		}
		if actor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed); actor != confirmActor {
			t.Fatalf("the revision moved the confirmation attribution to %q, want %s preserved", actor, confirmActor)
		}
		if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != ownerRef {
			t.Fatalf("the revision moved the confirmation row to %q (present %v), want the executing coordinator preserved", confirmed, ok)
		}
		if _, inherited := approvalPremiseConfirmationRow(t, s, workID, 2); inherited {
			t.Fatal("the decision successor inherited a confirmation row without a fresh confirmation")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
			t.Fatalf("the revision left the work at version %d, want the one derived advance from %d", version, baselineVersion)
		}
		// A confirmation naming the superseded predecessor version refuses
		// through the real constructor seam before any fold.
		_, staleVersionErr := confirmResolutionPremise(t, s, registry, definition, workID, owner, nil, map[string]json.RawMessage{"contract_version": json.RawMessage(`1`), "selected_choice": json.RawMessage(`"confirm"`)})
		var staleVersionFailure *Failure
		if staleVersionErr == nil || !failureAs(staleVersionErr, &staleVersionFailure) || staleVersionFailure.Kind != KindInvalidPayload || !strings.Contains(staleVersionFailure.Detail, "supersession invalidated") {
			t.Fatalf("the stale-version confirmation returned %v, want the supersession-invalidated refusal", staleVersionErr)
		}
		// The predecessor's human digest cannot answer the successor's open
		// question; the successor's own derived digest can. The stateless
		// selection chain owns the digest comparison, so a stale digest
		// refuses as stale-or-forged instead of behind the admission layer's
		// artifact staleness.
		staleDigestErr := factWriteOperatorSelection(t, s, registry, workID, predecessorDigest)
		var staleDigestFailure *Failure
		if staleDigestErr == nil || !failureAs(staleDigestErr, &staleDigestFailure) || staleDigestFailure.Kind != KindStaleRequiresReview || !strings.Contains(staleDigestFailure.Detail, "stale or forged") {
			t.Fatalf("the predecessor digest selection returned %v, want the stale-or-forged refusal", staleDigestErr)
		}
		successorDigest := approvalDecisionDigest(t, s, workID)
		if successorDigest == predecessorDigest {
			t.Fatal("the decision successor's digest equals the predecessor's; the old digest could answer the new contract")
		}
		if err := factWriteOperatorSelection(t, s, registry, workID, successorDigest); err != nil {
			t.Fatalf("the successor's own derived digest refused the operator selection: %v", err)
		}
		if _, inherited := approvalPremiseConfirmationRow(t, s, workID, 2); inherited {
			t.Fatal("the read-only stale-authority checks recorded a successor confirmation")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
			t.Fatalf("the stale-authority checks advanced the work version to %d", version)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("decision to engineering revision over a confirmed predecessor preserves attribution and refuses stale authority", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-revise-d2e"
		s, registry, definition, owner, actors := seedApprovalItemAtAcceptance(t, workID, []string{"product"})
		if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
			t.Fatalf("the independent evaluator's verdict refused before the revision: %v", err)
		}
		digest := approvalDecisionDigest(t, s, workID)
		if _, err := confirmResolutionPremise(t, s, registry, definition, workID, owner, &actors.operator, map[string]json.RawMessage{"selected_choice": json.RawMessage(`"confirm"`), "decision_context_digest": json.RawMessage(`"` + digest + `"`)}); err != nil {
			t.Fatalf("the verified operator's decision confirmation refused: %v", err)
		}
		if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != actors.operatorRef {
			t.Fatalf("the decision confirmation row = %q (present %v), want the verified operator %s", confirmed, ok, actors.operatorRef)
		}
		confirmEvent := factWriteEventPayloadByID(t, s, "resolution-confirm-"+workID)
		confirmActor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed)
		contractEvent := factWriteEventPayloadByID(t, s, "resolution-contract-"+workID)
		baselineVersion := readWorkVersion(t, s, workID)
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{
			Events:           []Event{factWriteSuccessorEvent(t, s, workID, ownerRefForTest(t, owner), []string{}, true)},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion},
		})
		if stored := storedDecisionDomains(t, s, workID, 2); stored != "[]" {
			t.Fatalf("the engineering successor stored decision_domains %q, want the exact [] fact", stored)
		}
		if stored := storedDecisionDomains(t, s, workID, 1); stored != `["product"]` {
			t.Fatalf("the superseded decision predecessor stored %q, want its recorded fact immutable", stored)
		}
		var supersededBy int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil {
			t.Fatal(err)
		}
		if supersededBy != 2 {
			t.Fatalf("the successful revision names superseded_by=%d, want the successor version 2", supersededBy)
		}
		if factWriteEventPayloadByID(t, s, "resolution-contract-"+workID) != contractEvent {
			t.Fatal("the revision rewrote the predecessor contract event bytes")
		}
		if factWriteEventPayloadByID(t, s, "resolution-confirm-"+workID) != confirmEvent {
			t.Fatal("the revision rewrote the predecessor confirmation event bytes")
		}
		if actor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed); actor != confirmActor {
			t.Fatalf("the revision moved the confirmation attribution to %q, want %s preserved", actor, confirmActor)
		}
		if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != actors.operatorRef {
			t.Fatalf("the revision moved the confirmation row to %q (present %v), want the verified operator preserved", confirmed, ok)
		}
		if _, inherited := approvalPremiseConfirmationRow(t, s, workID, 2); inherited {
			t.Fatal("the engineering successor inherited the operator's confirmation row")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
			t.Fatalf("the revision left the work at version %d, want the one derived advance from %d", version, baselineVersion)
		}
		// A confirmation naming the superseded predecessor version refuses
		// through the real constructor seam before any fold.
		_, staleVersionErr := confirmResolutionPremise(t, s, registry, definition, workID, owner, nil, map[string]json.RawMessage{"contract_version": json.RawMessage(`1`), "selected_choice": json.RawMessage(`"confirm"`)})
		var staleVersionFailure *Failure
		if staleVersionErr == nil || !failureAs(staleVersionErr, &staleVersionFailure) || staleVersionFailure.Kind != KindInvalidPayload || !strings.Contains(staleVersionFailure.Detail, "supersession invalidated") {
			t.Fatalf("the stale-version confirmation returned %v, want the supersession-invalidated refusal", staleVersionErr)
		}
		// The predecessor's human digest cannot confirm the engineering
		// successor: an engineering confirmation carries no human digest at
		// all, while the digest-less coordinator selection stands. The
		// stateless selection chain owns both answers.
		staleDigestErr := factWriteOperatorSelection(t, s, registry, workID, digest)
		var staleDigestFailure *Failure
		if staleDigestErr == nil || !failureAs(staleDigestErr, &staleDigestFailure) || staleDigestFailure.Kind != KindInvalidPayload || !strings.Contains(staleDigestFailure.Detail, "carries no human decision-context digest") {
			t.Fatalf("the engineering successor's selection with the predecessor digest returned %v, want the no-human-digest refusal", staleDigestErr)
		}
		if err := factWriteOperatorSelection(t, s, registry, workID, ""); err != nil {
			t.Fatalf("the engineering successor's digest-less coordinator selection refused: %v", err)
		}
		if _, inherited := approvalPremiseConfirmationRow(t, s, workID, 2); inherited {
			t.Fatal("the read-only stale-authority checks recorded a successor confirmation")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
			t.Fatalf("the stale-authority checks advanced the work version to %d", version)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("the legacy twin keeps null absence clone-only attribution and rebuild equality on the same folds", func(t *testing.T) {
		t.Parallel()
		// The unchanged builtin legacy twin: workflow.static_analysis v15 is
		// the released head whose approval policy is the legacy absence and
		// whose contracts are non-Product-changing, so the live folds and the
		// rebuild resolve the same pin through the builtin registry itself.
		legacyRegistered, ok := BuiltinWorkflowRegistry().Lookup("workflow.static_analysis", 15)
		if !ok {
			t.Fatal("the builtin registry carries no workflow.static_analysis v15 entry for the legacy twin")
		}
		legacy := legacyRegistered.Definition
		if legacy.ApprovalPolicy != "" {
			t.Fatalf("the builtin workflow.static_analysis v15 entry declares approval policy %q, want the released legacy absence", legacy.ApprovalPolicy)
		}
		if legacy.ChangesProductTruth == nil || *legacy.ChangesProductTruth {
			t.Fatal("the builtin workflow.static_analysis v15 entry must classify non-Product-changing for the legacy clone route to be real")
		}
		registry := BuiltinWorkflowRegistry()
		missingID := "decision-fact-legacy-missing"
		s, missingOwner := resolutionSeedStore(t, missingID)
		defer s.Close()
		appendWorkflowEventsWithRegistry(t, s, registry, resolutionSelectionOperation(t, s, missingID, legacyRegistered, missingOwner))
		seedLegacyItem := func(itemID string) WorkflowActor {
			t.Helper()
			seedWork(t, s, itemID)
			owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + itemID, ActorClass: ActorAgent}
			appendWorkflowEventsWithRegistry(t, s, registry, resolutionSelectionOperation(t, s, itemID, legacyRegistered, owner))
			return owner
		}
		nullOwner := seedLegacyItem("decision-fact-legacy-null")
		cloneOwner := seedLegacyItem("decision-fact-legacy-clone")
		// Missing member and explicit null both store NULL absence under the
		// legacy pin, with the authoring operator attributed.
		for _, legacyCase := range []struct {
			itemID  string
			owner   WorkflowActor
			present bool
		}{
			{itemID: missingID, owner: missingOwner, present: false},
			{itemID: "decision-fact-legacy-null", owner: nullOwner, present: true},
		} {
			ownerRef := ownerRefForTest(t, legacyCase.owner)
			version := readWorkVersion(t, s, legacyCase.itemID)
			event := factWriteInitialContractEvent(t, s, legacyCase.itemID, ownerRef, nil, legacyCase.present)
			appendWorkflowEventsWithRegistry(t, s, registry, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, legacyCase.itemID): version}})
			if stored := storedDecisionDomains(t, s, legacyCase.itemID, 1); stored != "" {
				t.Fatalf("the legacy %s contract stored decision_domains %q, want NULL absence", legacyCase.itemID, stored)
			}
			payload := factWriteEventPayloadByID(t, s, "decision-fact-initial-contract-"+legacyCase.itemID)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(payload), &fields); err != nil {
				t.Fatal(err)
			}
			member, present := fields["decision_domains"]
			if legacyCase.present && (!present || string(member) != "null") {
				t.Fatalf("the legacy explicit-null contract recorded member %q (present %v), want the null wire shape", member, present)
			}
			if !legacyCase.present && present {
				t.Fatalf("the legacy member-absent contract recorded a fact member: %s", payload)
			}
			var rowRef, rowDigest, approvedBy string
			var rowVersion int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version,definition_digest,approved_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, legacyCase.itemID).Scan(&rowRef, &rowVersion, &rowDigest, &approvedBy); err != nil {
				t.Fatal(err)
			}
			if rowRef != legacyRegistered.Definition.Ref || rowVersion != legacyRegistered.Definition.Version || rowDigest != legacyRegistered.Digest {
				t.Fatalf("the legacy contract pinned %s@%d %s, want the registered legacy twin %s@%d %s", rowRef, rowVersion, rowDigest, legacyRegistered.Definition.Ref, legacyRegistered.Definition.Version, legacyRegistered.Digest)
			}
			if approvedBy != ownerRef {
				t.Fatalf("the legacy contract recorded approved_by %q, want the authoring owner %s", approvedBy, ownerRef)
			}
		}
		// The legacy clone-only route keeps carrying the recorded fact with
		// its operator attribution and the authored supersession time.
		cloneRef := ownerRefForTest(t, cloneOwner)
		cloneVersion := readWorkVersion(t, s, "decision-fact-legacy-clone")
		cloneContract := factWriteInitialContractEvent(t, s, "decision-fact-legacy-clone", cloneRef, []string{"product"}, true)
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{Events: []Event{cloneContract}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "decision-fact-legacy-clone"): cloneVersion}})
		supersedeVersion := readWorkVersion(t, s, "decision-fact-legacy-clone")
		cloneEvent := resolutionEvent("decision-fact-legacy-clone-supersede", WorkflowContractSuperseded, "decision-fact-legacy-clone", cloneRef, supersedeVersion, map[string]any{
			"previous_contract_version": 1, "predecessor_contract_versions": []int64{1}, "new_contract_version": 2,
			"supersede_reason": "the legacy twin keeps its clone-only route", "audit_evidence": []string{"audit:decision-fact-legacy-clone"},
		})
		appendWorkflowEventsWithRegistry(t, s, registry, Operation{Events: []Event{cloneEvent}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "decision-fact-legacy-clone"): supersedeVersion}})
		if stored := storedDecisionDomains(t, s, "decision-fact-legacy-clone", 2); stored != `["product"]` {
			t.Fatalf("the legacy clone stored decision_domains %q, want the predecessor's recorded fact carried forward", stored)
		}
		if stored := storedDecisionDomains(t, s, "decision-fact-legacy-clone", 1); stored != `["product"]` {
			t.Fatalf("the superseded legacy predecessor stored %q, want its recorded fact immutable", stored)
		}
		var cloneApprovedBy, cloneApprovedAt string
		if err := s.DatabaseForTesting().QueryRow(`SELECT approved_by,approved_at FROM workflow_contracts WHERE work_id=? AND contract_version=2`, "decision-fact-legacy-clone").Scan(&cloneApprovedBy, &cloneApprovedAt); err != nil {
			t.Fatal(err)
		}
		if cloneApprovedBy != cloneRef {
			t.Fatalf("the legacy clone recorded approved_by %q, want the superseding owner %s", cloneApprovedBy, cloneRef)
		}
		if cloneApprovedAt != cloneEvent.OccurredAt.UTC().Format(time.RFC3339Nano) {
			t.Fatalf("the legacy clone recorded approved_at %q, want the authored supersession time %s", cloneApprovedAt, cloneEvent.OccurredAt.UTC().Format(time.RFC3339Nano))
		}
		var predecessorSupersededBy int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, "decision-fact-legacy-clone").Scan(&predecessorSupersededBy); err != nil {
			t.Fatal(err)
		}
		if predecessorSupersededBy != 2 {
			t.Fatalf("the superseded legacy predecessor names superseded_by=%d, want the clone version 2", predecessorSupersededBy)
		}
		// The legacy shapes rebuild and rehash equal: the recorded bytes stay
		// the authority across a full projection rebuild. Each stage holds the
		// legacy absence state too: the missing-member and explicit-null items
		// keep explicit SQL NULL decision_domains with their version-1
		// contracts active, and the clone keeps its carried product fact at
		// active version 2.
		assertLegacyNullState := func(stage string, store *Store) {
			t.Helper()
			var declaredFacts int64
			if err := store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id IN (?,?) AND decision_domains IS NOT NULL`, missingID, "decision-fact-legacy-null").Scan(&declaredFacts); err != nil {
				t.Fatal(err)
			}
			if declaredFacts != 0 {
				t.Fatalf("%s declares %d decision facts on the NULL-absence legacy items, want explicit SQL NULL absence", stage, declaredFacts)
			}
			for _, legacyCase := range []struct {
				itemID string
				want   int64
			}{
				{itemID: missingID, want: 1},
				{itemID: "decision-fact-legacy-null", want: 1},
				{itemID: "decision-fact-legacy-clone", want: 2},
			} {
				active, err := activeWorkflowContractVersion(context.Background(), store.DatabaseForTesting(), legacyCase.itemID, "workflow_decision_fact_write_test")
				if err != nil {
					t.Fatal(err)
				}
				if active != legacyCase.want {
					t.Fatalf("%s active contract version for %s = %d, want %d", stage, legacyCase.itemID, active, legacyCase.want)
				}
			}
		}
		before := fullWorkflowProjectionSnapshot(t, s)
		hashBefore, err := WorkflowProjectionHash(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		assertLegacyNullState("the legacy twin baseline", s)
		if err := RebuildFromLog(context.Background(), s); err != nil {
			t.Fatalf("legacy twin rebuild: %v", err)
		}
		if before != fullWorkflowProjectionSnapshot(t, s) {
			t.Fatal("the legacy twin rebuild diverged from the live projection")
		}
		hashAfter, err := WorkflowProjectionHash(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		if hashBefore != hashAfter {
			t.Fatal("the legacy twin rebuild moved the full projection hash")
		}
		assertLegacyNullState("the legacy twin rebuild", s)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		reopened := openTempAtPath(t, s.path)
		defer reopened.Close()
		if before != fullWorkflowProjectionSnapshot(t, reopened) {
			t.Fatal("the legacy twin reopen diverged from the pre-reopen projection")
		}
		reopenHash, err := WorkflowProjectionHash(context.Background(), reopened)
		if err != nil {
			t.Fatal(err)
		}
		if hashAfter != reopenHash {
			t.Fatal("the legacy twin reopen moved the full projection hash")
		}
		assertLegacyNullState("the legacy twin reopen", reopened)
	})

	t.Run("the clone-only successor refuses under the new policy with the confirmed predecessor preserved", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-clone-only-successor"
		s, registry, _, owner, baselineVersion := seedFactWriteConfirmedEngineering(t, workID)
		ownerRef := ownerRefForTest(t, owner)
		baselineEvents := approvalEventCount(t, s, workID)
		event := factWriteCloneOnlyEvent(t, s, workID, ownerRef)
		operation := Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion}}
		err := tryAppendWorkflowEventsWithRegistry(s, registry, operation)
		var failure *Failure
		if err == nil {
			s.Close()
			t.Fatal("the clone-only successor with no supplied contract was accepted under the consequence_v1 pin; the new policy must refuse the legacy clone route atomically")
		}
		if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "cannot use the legacy clone route") {
			s.Close()
			t.Fatalf("the clone-only successor returned %v, want the typed clone-route refusal", err)
		}
		assertFactWritePredecessorPreserved(t, s, workID, baselineVersion, baselineEvents, ownerRef)
	})

	t.Run("the copied decision-fact declaration selects on the new policy and keeps every legacy declaration byte-identical", func(t *testing.T) {
		t.Parallel()

		newPolicy := WorkflowDefinition{ApprovalPolicy: WorkflowApprovalPolicyConsequenceV1,
			ActionDefinitions: []WorkflowActionDefinition{currentActionDefinition("approve_contract", true)}}
		fact := workflowDecisionFactField()
		if fact.Name != "decision_domains" || fact.ValueType != PayloadStringList || !fact.Required {
			t.Fatalf("the copied decision-fact field = %+v, want the required decision_domains string list", fact)
		}
		if fact.MinItems == nil || *fact.MinItems != 0 || fact.MaxItems == nil || *fact.MaxItems != int64(len(workflowDecisionDomainSet)) {
			t.Fatalf("the copied decision-fact bounds = %v..%v, want 0..%d", fact.MinItems, fact.MaxItems, len(workflowDecisionDomainSet))
		}
		if !reflect.DeepEqual(fact.Enum, workflowDecisionDomainSet) {
			t.Fatalf("the copied decision-fact enum = %v, want the canonical closed set %v", fact.Enum, workflowDecisionDomainSet)
		}

		// Declaration shape: each contract-authoring declaration appends the
		// one fact field and keeps every declared field in order. The initial
		// selection answers from the pinned closed action, never from the
		// global builtin policy.
		initial, initialErr := workflowDecisionFactInitialActionDefinition(newPolicy)
		if initialErr != nil {
			t.Fatalf("the initial declaration selection refused the pinned closed action: %v", initialErr)
		}
		recovery := workflowDecisionFactRecoveryActionDefinition(newPolicy)
		for _, selected := range []struct {
			name    string
			action  WorkflowActionDefinition
			builtin []WorkflowPayloadField
		}{
			{name: "the initial approve_contract declaration", action: initial, builtin: builtinActionPolicies["approve_contract"].Payload.Fields},
			{name: "the supersede_contract recovery declaration", action: recovery, builtin: workflowContractRecoveryPayloadFields()},
		} {
			fields := selected.action.Payload.Fields
			if !reflect.DeepEqual(fields, factWriteExpectedSelection(selected.builtin)) {
				t.Fatalf("%s fields differ from the released fields plus the law-change inputs and canonical decision fact", selected.name)
			}
			for index, field := range selected.builtin {
				if !reflect.DeepEqual(fields[index], field) {
					t.Fatalf("%s altered declared field %d (%s)", selected.name, index, field.Name)
				}
			}
			appended := fields[len(fields)-1]
			if appended.Name != "decision_domains" || !appended.Required {
				t.Fatalf("%s appends %s (required %v), want the required decision_domains fact last", selected.name, appended.Name, appended.Required)
			}
		}

		// Empty-list bounds and the closed enum through the shared payload
		// value validator: the empty engineering list stays legal and every
		// member answers to the canonical set.
		factField := recovery.Payload.Fields[len(recovery.Payload.Fields)-1]
		for _, probe := range []struct {
			raw  string
			want bool
		}{
			{`[]`, true},
			{`["preference", "product"]`, true},
			{`["preference", "product", "ui_ux"]`, true},
			{`null`, false},
			{`"product"`, false},
			{`["economics"]`, false},
			{`["preference", "product", "ui_ux", "preference"]`, false},
		} {
			if got := validateWorkflowPayloadValue(factField, json.RawMessage(probe.raw)); got != probe.want {
				t.Fatalf("the decision-fact declaration answers %s with %v, want %v", probe.raw, got, probe.want)
			}
		}

		// Fresh backing storage: the selected field list never aliases the
		// builtin policy slice, and the fact enum never aliases the canonical
		// set. The alias probe runs on its own selected field, so the shared
		// enum clone the checks below read stays canonical.
		builtinFields := builtinActionPolicies["approve_contract"].Payload.Fields
		if reflect.ValueOf(builtinFields).Pointer() == reflect.ValueOf(initial.Payload.Fields).Pointer() {
			t.Fatal("the selected initial declaration shares the builtin policy backing storage")
		}
		aliasProbe := workflowDecisionFactRecoveryActionDefinition(newPolicy).Payload.Fields[len(recovery.Payload.Fields)-1]
		aliasProbe.Enum[0] = "mutated"
		if workflowDecisionDomainSet[0] != "preference" {
			t.Fatalf("the fact enum aliases the canonical set: %v", workflowDecisionDomainSet)
		}

		// Legacy declaration identity: a legacy pin resolves the initial
		// declaration byte-identically to its own pinned action, and a
		// definition that declares no initial action answers the zero action
		// with no error instead of the global builtin declaration.
		legacyPinned := WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{currentActionDefinition("approve_contract", true)}}
		legacy, legacyErr := workflowDecisionFactInitialActionDefinition(legacyPinned)
		if legacyErr != nil || !reflect.DeepEqual(legacy, currentActionDefinition("approve_contract", true)) {
			t.Fatalf("the legacy initial declaration = %+v (err %v), want the pinned legacy action unchanged", legacy, legacyErr)
		}
		absent, absentErr := workflowDecisionFactInitialActionDefinition(WorkflowDefinition{})
		if absentErr != nil || !reflect.DeepEqual(absent, WorkflowActionDefinition{}) {
			t.Fatalf("the absent initial selection = %+v (err %v), want the zero action with no fabricated declaration", absent, absentErr)
		}
		if legacy := workflowDecisionFactRecoveryActionDefinition(WorkflowDefinition{}); !reflect.DeepEqual(legacy, workflowContractRecoveryActionDefinition()) {
			t.Fatalf("the legacy recovery declaration = %+v, want the released supersede_contract declaration byte-identical", legacy)
		}

		// The canonical recovery projection filters the supplied selected
		// declaration instead of restating it: the fact keeps its declared
		// requiredness and closed enum, the paired outcome authoring shape
		// drops, and the explicit predicate array stays required.
		canonical := workflowCanonicalContractRecoveryPayloadFields(recovery.Payload.Fields)
		var canonicalFact *WorkflowPayloadField
		for index := range canonical {
			switch canonical[index].Name {
			case "decision_domains":
				canonicalFact = &canonical[index]
			case "outcome_kind", "outcome_payload":
				t.Fatalf("the canonical projection kept the paired authoring field %s", canonical[index].Name)
			}
		}
		if canonicalFact == nil {
			t.Fatal("the canonical projection dropped the copied decision-fact declaration")
		}
		if !canonicalFact.Required || !reflect.DeepEqual(canonicalFact.Enum, workflowDecisionDomainSet) {
			t.Fatalf("the canonical projection restated the fact as %+v, want the owner's required closed declaration", *canonicalFact)
		}
		for _, field := range canonical {
			if field.Name == "outcome_predicates" && !field.Required {
				t.Fatal("the canonical projection left outcome_predicates optional")
			}
		}

		// Unaffected declarations: no unrelated action, no released recovery
		// base, and no canonical publication carries the fact before its
		// consumer threads it.
		for _, actionID := range []string{"record_verdict", "confirm_premise", "record_alignment"} {
			for _, field := range currentActionDefinition(actionID, true).Payload.Fields {
				if field.Name == "decision_domains" {
					t.Fatalf("the unrelated action %s declares the decision fact", actionID)
				}
			}
		}
		for _, field := range workflowContractRecoveryActionDefinition().Payload.Fields {
			if field.Name == "decision_domains" {
				t.Fatal("the released supersede_contract base carries the decision fact")
			}
		}
		for _, action := range BuiltinWorkflowCanonicalRecoveryActionDefinitions() {
			for _, field := range action.Payload.Fields {
				if field.Name == "decision_domains" {
					t.Fatalf("the legacy canonical publication for %s carries an undeclared decision fact", action.ID)
				}
			}
		}
	})

	// The constructor checks bind the write boundary's constructors and
	// declaration consumers to the same canonical fact the fold gates carry:
	// the actual approve_contract and supersede_contract constructors emit
	// and preserve the exact typed bytes for all eight canonical sets, refuse
	// every broken and absent shape typed before any durable effect, and the
	// dispatch, closed-validation, preflight, and work-pin surfaces agree
	// with the copied declaration owner while every legacy declaration stays
	// byte-identical. Each constructor drive opens one short transaction and
	// rolls it back before any store-backed assertion, because the store
	// pools one connection.

	t.Run("submitted JSON null stays absent regardless of whitespace", func(t *testing.T) {
		definition := approvalRouteDefinition(t)
		for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(" \nnull\t")} {
			_, err := workflowSubmittedDecisionDomains(definition, "approval", map[string]json.RawMessage{"decision_domains": raw})
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Fatalf("submitted decision_domains %q returned %v, want invalid_payload", raw, err)
			}
		}
	})

	t.Run("the initial approve constructor emits and folds every canonical decision fact exactly", func(t *testing.T) {
		t.Parallel()
		for _, set := range canonicalDecisionDomainSets() {
			workID := "decision-fact-initial-ctor-" + factWriteSetLabel(set)
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			ownerRef := ownerRefForTest(t, owner)
			baseline := readWorkVersion(t, s, workID)
			events, constructErr := factWriteConstructInitialContract(t, s, definition, workID, ownerRef, factWriteApproveFields(t, workID, set, true), baseline)
			if constructErr != nil {
				s.Close()
				t.Fatalf("the initial approve constructor for %s refused the complete decision fact: %v", workflowJSON(set), constructErr)
			}
			if len(events) != 1 {
				s.Close()
				t.Fatalf("the initial approve constructor for %s returned %d events, want the one contract event", workflowJSON(set), len(events))
			}
			expected := workflowJSON(set)
			if member, present := factWritePayloadMember(t, string(events[0].Payload)); !present || member != expected {
				s.Close()
				t.Fatalf("the initial approve constructor event for %s recorded decision_domains %q (present %v), want the exact canonical member %q", workflowJSON(set), member, present, expected)
			}
			appendWorkflowEventsWithRegistry(t, s, registry, Operation{
				Events:           events,
				ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baseline},
			})
			if stored := storedDecisionDomains(t, s, workID, 1); stored != expected {
				t.Fatalf("the folded initial contract for %s stored decision_domains %q, want the exact canonical bytes %q", expected, stored, expected)
			}
			assertFactWriteProjectedDomains(t, s, registry, workID, 1, set)
			s.Close()
		}
	})

	t.Run("the initial approve constructor refuses absent and broken decision facts typed before any effect", func(t *testing.T) {
		t.Parallel()
		for _, factCase := range factWriteBrokenFacts() {
			workID := "decision-fact-initial-ctor-refuse-" + strings.ReplaceAll(factCase.name, " ", "-")
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			ownerRef := ownerRefForTest(t, owner)
			baselineVersion := readWorkVersion(t, s, workID)
			baselineEvents := approvalEventCount(t, s, workID)
			_, constructErr := factWriteConstructInitialContract(t, s, definition, workID, ownerRef, factWriteApproveFields(t, workID, factCase.fact, factCase.present), baselineVersion)
			var failure *Failure
			if constructErr == nil || !failureAs(constructErr, &failure) || failure.Kind != KindInvalidPayload {
				s.Close()
				t.Fatalf("the initial approve constructor with the %s fact returned %v, want the typed invalid-payload refusal", factCase.name, constructErr)
			}
			assertFactWriteInitialNoEffect(t, s, workID, baselineVersion, baselineEvents)
			s.Close()
		}
	})

	t.Run("the supersede constructor emits and folds every canonical decision fact exactly without mutating the predecessor", func(t *testing.T) {
		t.Parallel()
		for _, set := range canonicalDecisionDomainSets() {
			workID := "decision-fact-successor-ctor-" + factWriteSetLabel(set)
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			advanceResolutionPrefix(t, s, registry, workID, owner, []string{}, false)
			ownerRef := ownerRefForTest(t, owner)
			baselineVersion := readWorkVersion(t, s, workID)
			predecessorPayload := factWriteEventPayloadByID(t, s, "resolution-contract-"+workID)
			raw, fields := factWriteSuccessorPayload(t, workID, set, true)
			events, constructErr := factWriteConstructSuccessorContract(t, s, definition, workID, ownerRef, raw, fields, baselineVersion)
			if constructErr != nil {
				s.Close()
				t.Fatalf("the supersede constructor for %s refused the complete decision fact: %v", workflowJSON(set), constructErr)
			}
			if len(events) != 1 {
				s.Close()
				t.Fatalf("the supersede constructor for %s returned %d events, want the one supersession event", workflowJSON(set), len(events))
			}
			expected := workflowJSON(set)
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(events[0].Payload, &envelope); err != nil {
				t.Fatal(err)
			}
			successorRaw, recorded := envelope["successor_contract"]
			if !recorded {
				s.Close()
				t.Fatalf("the supersede constructor event for %s recorded no successor_contract", expected)
			}
			if member, present := factWritePayloadMember(t, string(successorRaw)); !present || member != expected {
				s.Close()
				t.Fatalf("the supersede constructor's successor contract for %s recorded decision_domains %q (present %v), want the exact canonical member %q", expected, member, present, expected)
			}
			appendWorkflowEventsWithRegistry(t, s, registry, Operation{
				Events:           events,
				ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion},
			})
			if stored := storedDecisionDomains(t, s, workID, 2); stored != expected {
				t.Fatalf("the folded successor for %s stored decision_domains %q, want the exact canonical bytes %q", expected, stored, expected)
			}
			if stored := storedDecisionDomains(t, s, workID, 1); stored != "[]" {
				t.Fatalf("the superseded predecessor for %s stored %q, want its recorded [] fact immutable", expected, stored)
			}
			var supersededBy int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil {
				t.Fatal(err)
			}
			if supersededBy != 2 {
				t.Fatalf("the superseded predecessor for %s names superseded_by=%d, want the successor version 2", expected, supersededBy)
			}
			if factWriteEventPayloadByID(t, s, "resolution-contract-"+workID) != predecessorPayload {
				t.Fatalf("the successor fold for %s rewrote the predecessor contract event bytes", expected)
			}
			assertFactWriteProjectedDomains(t, s, registry, workID, 2, set)
			if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
				t.Fatalf("the successor fold for %s left the work at version %d, want the one derived advance from %d", expected, version, baselineVersion)
			}
			s.Close()
		}
	})

	t.Run("the supersede constructor refuses absent and broken decision facts typed beside the confirmed predecessor", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-successor-ctor-refuse"
		s, _, definition, owner, baselineVersion := seedFactWriteConfirmedEngineering(t, workID)
		ownerRef := ownerRefForTest(t, owner)
		for _, factCase := range factWriteBrokenFacts() {
			baselineEvents := approvalEventCount(t, s, workID)
			raw, fields := factWriteSuccessorPayload(t, workID, factCase.fact, factCase.present)
			_, constructErr := factWriteConstructSuccessorContract(t, s, definition, workID, ownerRef, raw, fields, baselineVersion)
			var failure *Failure
			if constructErr == nil || !failureAs(constructErr, &failure) || failure.Kind != KindInvalidPayload {
				t.Fatalf("the supersede constructor with the %s fact returned %v, want the typed invalid-payload refusal", factCase.name, constructErr)
			}
			assertFactWritePredecessorPreserved(t, s, workID, baselineVersion, baselineEvents, ownerRef)
		}
	})

	t.Run("the complete successor direct fold refuses missing and null facts beside the confirmed predecessor", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-successor-absent"
		s, registry, _, owner, baselineVersion := seedFactWriteConfirmedEngineering(t, workID)
		ownerRef := ownerRefForTest(t, owner)
		for _, factCase := range []struct {
			name    string
			fact    any
			present bool
		}{
			{name: "missing member", present: false},
			{name: "explicit null", fact: nil, present: true},
		} {
			baselineEvents := approvalEventCount(t, s, workID)
			event := factWriteSuccessorEvent(t, s, workID, ownerRef, factCase.fact, factCase.present)
			operation := Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion}}
			err := tryAppendWorkflowEventsWithRegistry(s, registry, operation)
			var failure *Failure
			if err == nil {
				t.Fatalf("the %s successor folded with no typed refusal; an omission must not bypass the constructor's required decision fact", factCase.name)
			}
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "absence is a legacy shape") {
				t.Fatalf("the %s successor returned %v, want the typed absent-fact refusal", factCase.name, err)
			}
			assertFactWritePredecessorPreserved(t, s, workID, baselineVersion, baselineEvents, ownerRef)
		}
	})

	t.Run("the declaration consumers agree with the decision-fact owner and keep every legacy declaration unchanged", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-consumer-new-policy"
		s, registry, definition, _, _ := seedApprovalItemAtAcceptance(t, workID, []string{})
		ctx := context.Background()

		// The dispatch answer must come from the pin itself, never from the
		// selector under test or the builtin policy.
		pinnedInitial := workflowActionDefinitionByID(definition, "approve_contract")
		expectedInitial := pinnedInitial
		expectedInitial.Payload.Fields = factWriteExpectedSelection(pinnedInitial.Payload.Fields)
		_, initialAction, err := WorkflowActionDefinitionFor(ctx, s, registry, workID, "approve_contract")
		if err != nil {
			t.Fatalf("dispatch refused the new-policy initial action: %v", err)
		}
		if !reflect.DeepEqual(initialAction, expectedInitial) {
			t.Fatalf("dispatch resolved the new-policy initial action %+v, want the pinned initial declaration with the one canonical fact", initialAction)
		}
		assertFactWriteFieldsCarryFact(t, "the dispatched initial action", initialAction.Payload.Fields, pinnedInitial.Payload.Fields)
		_, recoveryAction, err := WorkflowActionDefinitionFor(ctx, s, registry, workID, "supersede_contract")
		if err != nil {
			t.Fatalf("the supersede recovery discovery refused beside the active contract: %v", err)
		}
		if !reflect.DeepEqual(recoveryAction, workflowDecisionFactRecoveryActionDefinition(definition)) {
			t.Fatalf("discovery resolved the new-policy recovery declaration %+v, want the copied declaration owner's selection", recoveryAction)
		}
		assertFactWriteFieldsCarryFact(t, "the discovered recovery declaration", recoveryAction.Payload.Fields, workflowContractRecoveryPayloadFields())

		// The legacy pin keeps the released initial declaration byte-identical
		// with no fact member at the same consumer.
		legacyRegistered, ok := BuiltinWorkflowRegistry().Lookup("workflow.static_analysis", 15)
		if !ok {
			t.Fatal("the builtin registry carries no workflow.static_analysis v15 entry for the legacy consumer witness")
		}
		const legacyID = "decision-fact-consumer-legacy"
		legacyStore, legacyOwner := resolutionSeedStore(t, legacyID)
		defer legacyStore.Close()
		appendWorkflowEventsWithRegistry(t, legacyStore, BuiltinWorkflowRegistry(), resolutionSelectionOperation(t, legacyStore, legacyID, legacyRegistered, legacyOwner))
		_, legacyAction, err := WorkflowActionDefinitionFor(ctx, legacyStore, BuiltinWorkflowRegistry(), legacyID, "approve_contract")
		if err != nil {
			t.Fatalf("dispatch refused the legacy-pinned initial action: %v", err)
		}
		var expectedLegacy WorkflowActionDefinition
		for _, action := range legacyRegistered.Definition.ActionDefinitions {
			if action.ID == "approve_contract" {
				expectedLegacy = action
				break
			}
		}
		if !reflect.DeepEqual(legacyAction, expectedLegacy) {
			t.Fatalf("dispatch resolved the legacy initial action %+v, want the released declaration byte-identical", legacyAction)
		}
		for _, field := range legacyAction.Payload.Fields {
			if field.Name == "decision_domains" {
				t.Fatal("the legacy-pinned initial action declares the decision fact")
			}
		}

		// Closed payload validation admits the new-policy initial action only
		// with the required fact and refuses every absent or broken shape,
		// while the legacy declaration keeps its released answers.
		completeRaw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, []string{"product"}, true))
		if err := validateWorkflowActionPayload(definition, "approve_contract", completeRaw); err != nil {
			t.Fatalf("closed validation refused the initial action payload carrying the required fact: %v", err)
		}
		missingRaw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, nil, false))
		var payloadFailure *Failure
		if err := validateWorkflowActionPayload(definition, "approve_contract", missingRaw); err == nil || !failureAs(err, &payloadFailure) || payloadFailure.Kind != KindInvalidPayload {
			t.Fatalf("closed validation answered %v for the initial action payload missing the required fact, want the typed invalid-payload refusal", err)
		}
		brokenRaw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, []string{"product", "product"}, true))
		if err := validateWorkflowActionPayload(definition, "approve_contract", brokenRaw); err == nil {
			t.Fatal("closed validation accepted an initial action payload with a duplicate fact member")
		}
		unorderedRaw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, []string{"product", "preference"}, true))
		if err := validateWorkflowActionPayload(definition, "approve_contract", unorderedRaw); err == nil {
			t.Fatal("closed validation accepted an initial action payload with a noncanonical fact order")
		}
		legacyPayload := factWriteAuthoringJSON(t, factWriteApproveAuthoring(legacyID, nil, false))
		if err := validateWorkflowActionPayload(legacyRegistered.Definition, "approve_contract", legacyPayload); err != nil {
			t.Fatalf("closed validation refused the legacy initial action payload without the fact: %v", err)
		}
		legacyWithFact := factWriteAuthoringJSON(t, factWriteApproveAuthoring(legacyID, []string{"product"}, true))
		if err := validateWorkflowActionPayload(legacyRegistered.Definition, "approve_contract", legacyWithFact); err == nil {
			t.Fatal("the legacy initial declaration accepted the undeclared decision fact")
		}

		// The recovery payload validator admits the complete successor only
		// with the required fact beside the new policy and keeps the released
		// declaration's answers for the legacy pin.
		recoveryComplete, _ := factWriteSuccessorPayload(t, workID, []string{"product"}, true)
		if err := validateWorkflowContractRecoveryPayload(definition, recoveryComplete); err != nil {
			t.Fatalf("recovery validation refused the complete successor payload carrying the required fact: %v", err)
		}
		recoveryMissing, _ := factWriteSuccessorPayload(t, workID, nil, false)
		if err := validateWorkflowContractRecoveryPayload(definition, recoveryMissing); err == nil {
			t.Fatal("recovery validation accepted a successor payload missing the required decision fact")
		}
		recoveryNull, _ := factWriteSuccessorPayload(t, workID, nil, true)
		if err := validateWorkflowContractRecoveryPayload(definition, recoveryNull); err == nil {
			t.Fatal("recovery validation accepted a successor payload whose decision fact is explicit null")
		}
		recoveryBroken, _ := factWriteSuccessorPayload(t, workID, []string{"product", "product"}, true)
		if err := validateWorkflowContractRecoveryPayload(definition, recoveryBroken); err == nil {
			t.Fatal("recovery validation accepted a successor payload with a duplicate fact member")
		}
		legacyRecovery, _ := factWriteSuccessorPayload(t, legacyID, nil, false)
		if err := validateWorkflowContractRecoveryPayload(legacyRegistered.Definition, legacyRecovery); err != nil {
			t.Fatalf("recovery validation refused the legacy successor payload without the fact: %v", err)
		}
		legacyRecoveryWithFact, _ := factWriteSuccessorPayload(t, legacyID, []string{"product"}, true)
		if err := validateWorkflowContractRecoveryPayload(legacyRegistered.Definition, legacyRecoveryWithFact); err == nil {
			t.Fatal("the legacy recovery declaration accepted the undeclared decision fact")
		}
	})

	t.Run("the recovery intent validates the required fact through preflight and the work pin advertises it", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-recovery-intent"
		s, registry, _, owner, baselineVersion := seedFactWriteConfirmedEngineering(t, workID)
		preflight := func(raw json.RawMessage) error {
			return WorkflowActionPreflightWithRegistry(context.Background(), s, registry, WorkflowActionPreflightRequest{
				WorkID: workID, ExpectedVersion: baselineVersion, ActionID: "supersede_contract", Payload: raw, Actor: owner,
			})
		}
		completeRaw, _ := factWriteSuccessorPayload(t, workID, []string{"ui_ux"}, true)
		if err := preflight(completeRaw); err != nil {
			t.Fatalf("preflight refused the recovery intent carrying the required decision fact: %v", err)
		}
		missingRaw, _ := factWriteSuccessorPayload(t, workID, nil, false)
		var missingFailure *Failure
		if err := preflight(missingRaw); err == nil || !failureAs(err, &missingFailure) || missingFailure.Kind != KindInvalidPayload {
			t.Fatalf("preflight answered %v for the recovery intent missing the required decision fact, want the typed invalid-payload refusal", err)
		}
		disagreeingRaw, _ := factWriteSuccessorPayload(t, workID, []string{"product", "product"}, true)
		var disagreeingFailure *Failure
		if err := preflight(disagreeingRaw); err == nil || !failureAs(err, &disagreeingFailure) {
			t.Fatalf("preflight answered %v for the recovery intent with a disagreeing decision fact, want a typed refusal", err)
		}

		// The work pin's required-field projection advertises the fact on the
		// supersede_contract recovery intent the confirmed predecessor keeps
		// open, so the operator supplies it on the first attempt.
		snapshot := approvalContinuitySnapshot(t, s, registry, workID)
		if snapshot.WorkPin == nil {
			t.Fatal("the continuity snapshot carried no work pin beside the confirmed predecessor")
		}
		advertised := false
		for _, intent := range snapshot.WorkPin.NextValidIntents {
			if intent.ActionID != "supersede_contract" {
				continue
			}
			advertised = true
			if !slices.Contains(intent.RequiredFields, "decision_domains") {
				t.Fatalf("the work pin's supersede_contract intent projects required fields %v, want the decision_domains fact advertised", intent.RequiredFields)
			}
		}
		if !advertised {
			t.Fatal("the work pin advertised no supersede_contract intent beside the confirmed predecessor")
		}
	})

	t.Run("the initial approve intent advertises the required fact only beside the new policy", func(t *testing.T) {
		t.Parallel()
		definition := approvalRouteDefinition(t)
		legacy := definition
		legacy.ApprovalPolicy = ""
		for _, pinCase := range []struct {
			name       string
			definition WorkflowDefinition
			want       bool
		}{
			{name: "consequence_v1", definition: definition, want: true},
			{name: "legacy", definition: legacy, want: false},
		} {
			advertised := false
			for _, step := range pinCase.definition.StepGraph.Steps {
				for _, intent := range workPinIntents(pinCase.definition, step.ID, 1) {
					if intent.ActionID != "approve_contract" {
						continue
					}
					advertised = true
					if got := slices.Contains(intent.RequiredFields, "decision_domains"); got != pinCase.want {
						t.Fatalf("the %s approve_contract intent at %s projects required fields %v, want decision_domains advertised %v", pinCase.name, step.ID, intent.RequiredFields, pinCase.want)
					}
				}
			}
			if !advertised {
				t.Fatalf("the %s definition advertises no approve_contract step intent", pinCase.name)
			}
		}
	})

	t.Run("a distinct pinned closed initial declaration survives discovery validation and advertisement", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-pinned-closed-initial"

		// The local pinned definition replaces the route's approve_contract
		// action with the distinct closed declaration, so only the pinned
		// action can answer discovery: its metadata, required_evidence
		// requiredness, premise minimum, and closed PublicPayload all differ
		// from the global builtin policy.
		definition := approvalRouteDefinition(t)
		pinned := factWritePinnedApproveAction(false)
		replaced := false
		for index := range definition.ActionDefinitions {
			if definition.ActionDefinitions[index].ID != "approve_contract" {
				continue
			}
			definition.ActionDefinitions[index] = pinned
			replaced = true
		}
		if !replaced {
			t.Fatal("the approval route declares no approve_contract action to pin")
		}
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		defer s.Close()
		ownerRef := ownerRefForTest(t, owner)
		factWriteSeedAtPlanning(t, s, registry, workID, ownerRef, true)

		approveStep := ""
		for _, step := range definition.StepGraph.Steps {
			if slices.Contains(step.Actions, "approve_contract") {
				approveStep = step.ID
			}
		}
		var currentStep string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
			t.Fatal(err)
		}
		if currentStep != approveStep {
			t.Fatalf("the seed parked the instance at %q, want the pinned approve step %q", currentStep, approveStep)
		}

		_, discovered, err := WorkflowActionDefinitionFor(context.Background(), s, registry, workID, "approve_contract")
		if err != nil {
			t.Fatalf("discovery refused the pinned closed initial declaration: %v", err)
		}
		if discovered.Consequence != pinned.Consequence || discovered.Approval != pinned.Approval || discovered.ExecutionMode != pinned.ExecutionMode || discovered.RequiredCapability != pinned.RequiredCapability {
			t.Errorf("discovery resolved metadata %+v, want the pinned action's %+v", WorkflowActionDefinition{ID: discovered.ID, Consequence: discovered.Consequence, Approval: discovered.Approval, ExecutionMode: discovered.ExecutionMode, RequiredCapability: discovered.RequiredCapability}, WorkflowActionDefinition{ID: pinned.ID, Consequence: pinned.Consequence, Approval: pinned.Approval, ExecutionMode: pinned.ExecutionMode, RequiredCapability: pinned.RequiredCapability})
		}
		if !discovered.Payload.Closed {
			t.Errorf("discovery resolved an open initial payload for the pinned closed declaration")
		}
		expectedFields := factWriteExpectedSelection(pinned.Payload.Fields)
		if count := countDecisionDomainFields(discovered.Payload.Fields); count != 1 {
			t.Errorf("discovery resolved %d decision_domains fields, want exactly the one canonical fact", count)
		}
		if !reflect.DeepEqual(discovered.Payload.Fields, expectedFields) {
			t.Errorf("discovery resolved fields %+v, want the %d pinned fields with the one canonical fact appended", discovered.Payload.Fields, len(pinned.Payload.Fields))
		}
		if discovered.PublicPayload == nil || !discovered.PublicPayload.Closed {
			t.Errorf("discovery dropped the pinned closed PublicPayload: %+v", discovered.PublicPayload)
		}
		expectedPublicFields := factWriteExpectedSelection(pinned.PublicPayload.Fields)
		var wantRequired []string
		for _, field := range expectedPublicFields {
			if field.Required {
				wantRequired = append(wantRequired, field.Name)
			}
		}
		if public := publicWorkflowActionPayload(discovered); !reflect.DeepEqual(public, WorkflowPayloadDefinition{Closed: true, Fields: expectedPublicFields}) {
			t.Errorf("the public projection resolved %+v, want the pinned public fields with the one canonical fact appended", public)
		}

		// Closed validation answers from the pinned declaration, so its
		// required_evidence requiredness and premise minimum stay enforced.
		completeRaw := factWriteAuthoringJSON(t, factWritePinnedApprovePayload(workID, "decide the pinned approval contract route for the witnessed change", []string{"verification"}, []string{"product"}, true))
		if err := validateWorkflowActionPayload(definition, "approve_contract", completeRaw); err != nil {
			t.Errorf("closed validation refused the complete pinned payload: %v", err)
		}
		missingEvidenceRaw := factWriteAuthoringJSON(t, factWritePinnedApprovePayload(workID, "decide the pinned approval contract route for the witnessed change", nil, []string{"product"}, true))
		var missingEvidenceFailure *Failure
		if err := validateWorkflowActionPayload(definition, "approve_contract", missingEvidenceRaw); err == nil || !failureAs(err, &missingEvidenceFailure) || missingEvidenceFailure.Kind != KindInvalidPayload {
			t.Errorf("closed validation answered %v for the payload missing the pinned required_evidence, want the typed invalid-payload refusal", err)
		}
		shortPremiseRaw := factWriteAuthoringJSON(t, factWritePinnedApprovePayload(workID, "decide the route", []string{"verification"}, []string{"product"}, true))
		var shortPremiseFailure *Failure
		if err := validateWorkflowActionPayload(definition, "approve_contract", shortPremiseRaw); err == nil || !failureAs(err, &shortPremiseFailure) || shortPremiseFailure.Kind != KindInvalidPayload {
			t.Errorf("closed validation answered %v for the below-minimum pinned premise, want the typed invalid-payload refusal", err)
		}

		preflight := func(raw json.RawMessage) error {
			return WorkflowActionPreflightWithRegistry(context.Background(), s, registry, WorkflowActionPreflightRequest{
				WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "approve_contract", Payload: raw, Actor: owner,
			})
		}
		if err := preflight(missingEvidenceRaw); err == nil {
			t.Errorf("preflight admitted the payload missing the pinned required_evidence")
		} else {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Errorf("preflight answered %v for the payload missing the pinned required_evidence, want the typed invalid-payload refusal", err)
			}
		}
		if err := preflight(shortPremiseRaw); err == nil {
			t.Errorf("preflight admitted the below-minimum pinned premise")
		} else {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Errorf("preflight answered %v for the below-minimum pinned premise, want the typed invalid-payload refusal", err)
			}
		}
		if err := preflight(completeRaw); err != nil {
			t.Errorf("preflight refused the complete pinned payload beside the active contract: %v", err)
		}

		// The work pin advertises the pinned requiredness beside the fact, so
		// the operator supplies both on the first attempt.
		snapshot := approvalContinuitySnapshot(t, s, registry, workID)
		if snapshot.WorkPin == nil {
			t.Fatal("the continuity snapshot carried no work pin at the pinned approve step")
		}
		advertised := false
		for _, intent := range snapshot.WorkPin.NextValidIntents {
			if intent.ActionID != "approve_contract" {
				continue
			}
			advertised = true
			if !reflect.DeepEqual(intent.RequiredFields, wantRequired) {
				t.Errorf("the work pin's approve_contract intent projects required fields %v, want the pinned requiredness and the fact: %v", intent.RequiredFields, wantRequired)
			}
		}
		if !advertised {
			t.Errorf("the work pin advertised no approve_contract intent at the pinned approve step")
		}
	})

	t.Run("an open pinned initial declaration refuses discovery preflight and advertisement", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-open-initial"

		// No selector may synthesize a usable shape around an open initial
		// declaration.
		definition := approvalRouteDefinition(t)
		for index := range definition.ActionDefinitions {
			if definition.ActionDefinitions[index].ID != "approve_contract" {
				continue
			}
			definition.ActionDefinitions[index].Payload.Closed = false
		}
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		defer s.Close()
		ownerRef := ownerRefForTest(t, owner)
		factWriteSeedAtPlanning(t, s, registry, workID, ownerRef, false)

		baselineVersion := readWorkVersion(t, s, workID)
		baselineEvents := approvalEventCount(t, s, workID)

		_, _, discoverErr := WorkflowActionDefinitionFor(context.Background(), s, registry, workID, "approve_contract")
		var discoverFailure *Failure
		if discoverErr == nil || !failureAs(discoverErr, &discoverFailure) || discoverFailure.Kind != KindInvalidDefinition {
			t.Errorf("discovery answered %v for the open pinned initial declaration, want the typed invalid-definition refusal", discoverErr)
		}
		preflightErr := WorkflowActionPreflightWithRegistry(context.Background(), s, registry, WorkflowActionPreflightRequest{
			WorkID: workID, ExpectedVersion: baselineVersion, ActionID: "approve_contract", Actor: owner,
		})
		var preflightFailure *Failure
		if preflightErr == nil || !failureAs(preflightErr, &preflightFailure) || preflightFailure.Kind != KindInvalidDefinition {
			t.Errorf("preflight answered %v for the open pinned initial declaration, want the typed invalid-definition refusal", preflightErr)
		}

		for _, step := range definition.StepGraph.Steps {
			intents := workPinIntents(definition, step.ID, baselineVersion)
			if workPinContainsAction(intents, "approve_contract") {
				t.Errorf("the pure intents at %s advertise approve_contract beside the open pinned initial declaration", step.ID)
			}
			for _, actionID := range step.Actions {
				if actionID == "approve_contract" {
					continue
				}
				if !workPinContainsAction(intents, actionID) {
					t.Errorf("the pure intents at %s dropped the unrelated declared action %s", step.ID, actionID)
				}
			}
		}

		// The actual work pin offers the same answer as the pure intents.
		snapshot := approvalContinuitySnapshot(t, s, registry, workID)
		if snapshot.WorkPin == nil {
			t.Fatal("the continuity snapshot carried no work pin at the planning step")
		}
		if workPinContainsAction(snapshot.WorkPin.NextValidIntents, "approve_contract") {
			t.Errorf("the work pin advertised approve_contract beside the open pinned initial declaration: %+v", snapshot.WorkPin.NextValidIntents)
		}

		// The missing-action probe stays unregistered: neither validation nor
		// the pure intents may fabricate a declaration for the absent action.
		missing := definition
		keptActions := make([]WorkflowActionDefinition, 0, len(definition.ActionDefinitions))
		for _, action := range definition.ActionDefinitions {
			if action.ID == "approve_contract" {
				continue
			}
			keptActions = append(keptActions, action)
		}
		missing.ActionDefinitions = keptActions
		undeclaredRaw := factWriteAuthoringJSON(t, map[string]any{"undeclared_member": true})
		if err := validateWorkflowActionPayload(missing, "approve_contract", undeclaredRaw); err != nil {
			t.Errorf("closed validation answered %v for a definition that declares no approve_contract action, want the generic no-op nil answer instead of a fabricated declaration", err)
		}
		for _, step := range definition.StepGraph.Steps {
			if workPinContainsAction(workPinIntents(missing, step.ID, baselineVersion), "approve_contract") {
				t.Errorf("the pure intents at %s fabricated an approve_contract advertisement for a definition that declares no such action", step.ID)
			}
		}

		// The refusals are reads: the work version and its event rows did
		// not move.
		if version := readWorkVersion(t, s, workID); version != baselineVersion {
			t.Errorf("the open-initial refusals advanced the work version from %d to %d", baselineVersion, version)
		}
		if events := approvalEventCount(t, s, workID); events != baselineEvents {
			t.Errorf("the open-initial refusals left %d events (baseline %d), want no append", events, baselineEvents)
		}
	})

	t.Run("a pinned closed initial declaration that already carries the fact stays single canonical and clone-only", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-pinned-fact-declared"
		definition := approvalRouteDefinition(t)
		pinned := factWritePinnedApproveAction(true)
		replaced := false
		for index := range definition.ActionDefinitions {
			if definition.ActionDefinitions[index].ID != "approve_contract" {
				continue
			}
			definition.ActionDefinitions[index] = pinned
			replaced = true
		}
		if !replaced {
			t.Fatal("the approval route declares no approve_contract action to pin")
		}
		factIndex := -1
		for index, field := range pinned.Payload.Fields {
			if field.Name == "decision_domains" {
				factIndex = index
			}
		}
		publicFactIndex := -1
		for index, field := range pinned.PublicPayload.Fields {
			if field.Name == "decision_domains" {
				publicFactIndex = index
			}
		}
		if factIndex < 0 || publicFactIndex < 0 {
			t.Fatalf("the pinned action authored no declared decision fact (payload %d, public %d)", factIndex, publicFactIndex)
		}
		registry, registered := newResolutionRegistry(t, definition)
		s, _ := seedResolutionSelection(t, workID, registry, registered)
		defer s.Close()

		_, first, err := WorkflowActionDefinitionFor(context.Background(), s, registry, workID, "approve_contract")
		if err != nil {
			t.Fatalf("discovery refused the pinned fact-declaring initial declaration: %v", err)
		}
		_, second, err := WorkflowActionDefinitionFor(context.Background(), s, registry, workID, "approve_contract")
		if err != nil {
			t.Fatalf("the repeated discovery refused the pinned fact-declaring initial declaration: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Errorf("the repeated discovery resolved %+v, want the first answer %+v", second, first)
		}
		if count := countDecisionDomainFields(first.Payload.Fields); count != 1 {
			t.Errorf("discovery resolved %d decision_domains fields, want exactly the pinned declaration", count)
		}
		if factIndex >= len(first.Payload.Fields) || first.Payload.Fields[factIndex].Name != "decision_domains" {
			t.Errorf("discovery moved the declared fact from position %d: %+v", factIndex, first.Payload.Fields)
		} else if !reflect.DeepEqual(first.Payload.Fields[factIndex], workflowDecisionFactField()) {
			t.Errorf("discovery restated the declared fact as %+v, want the canonical declaration", first.Payload.Fields[factIndex])
		}
		for index, field := range pinned.Payload.Fields {
			if index >= len(first.Payload.Fields) || !reflect.DeepEqual(first.Payload.Fields[index], field) {
				t.Errorf("discovery altered pinned field %d (%s)", index, field.Name)
			}
		}
		publicOK := first.PublicPayload != nil
		if !publicOK {
			t.Errorf("discovery dropped the pinned closed PublicPayload entirely")
		}
		if publicOK {
			public := publicWorkflowActionPayload(first)
			if !public.Closed {
				t.Errorf("the public projection lost the pinned closed shape")
			}
			if count := countDecisionDomainFields(public.Fields); count != 1 {
				t.Errorf("the public projection resolved %d decision_domains fields, want exactly the pinned declaration", count)
			}
			if publicFactIndex >= len(public.Fields) || public.Fields[publicFactIndex].Name != "decision_domains" {
				t.Errorf("the public projection moved the declared fact from position %d: %+v", publicFactIndex, public.Fields)
			}
			if !reflect.DeepEqual(publicWorkflowActionPayload(second), public) {
				t.Errorf("the repeated public projection resolved %+v, want the first answer %+v", publicWorkflowActionPayload(second), public)
			}
		}

		// The registry lookup deep-clones before discovery, so only a direct
		// selector call can expose selector aliasing: this call mutates the
		// premise bound, evidence enum, and public fields of the storage the
		// selector itself returned, and holds the input definition bytes and
		// the canonical fact set fixed. Every mutation is pointer-guarded, so
		// a failing assertion never writes through a shared slice.
		inputBefore, inputErr := json.Marshal(definition)
		if inputErr != nil {
			t.Fatal(inputErr)
		}
		canonicalBefore := append([]string(nil), workflowDecisionDomainSet...)
		selected, selectErr := workflowDecisionFactInitialActionDefinition(definition)
		if selectErr != nil {
			t.Fatalf("the direct selector refused the pinned fact-declaring definition: %v", selectErr)
		}
		if !reflect.DeepEqual(selected, first) {
			t.Errorf("the direct selector resolved %+v, want the discovered answer %+v", selected, first)
		}
		locate := func(fields []WorkflowPayloadField, name string) int {
			for index := range fields {
				if fields[index].Name == name {
					return index
				}
			}
			return -1
		}
		selectedPremise := locate(selected.Payload.Fields, "premise")
		selectedEvidence := locate(selected.Payload.Fields, "required_evidence")
		selectedFact := locate(selected.Payload.Fields, "decision_domains")
		if selectedPremise < 0 || selectedEvidence < 0 || selectedFact < 0 ||
			selected.Payload.Fields[selectedPremise].MinLength == nil ||
			len(selected.Payload.Fields[selectedEvidence].Enum) == 0 ||
			len(selected.Payload.Fields[selectedFact].Enum) == 0 {
			t.Fatalf("the direct selection lost the pinned members (premise %d, evidence %d, fact %d)", selectedPremise, selectedEvidence, selectedFact)
		}
		if reflect.ValueOf(selected.Payload.Fields).Pointer() == reflect.ValueOf(pinned.Payload.Fields).Pointer() {
			t.Errorf("the direct selection shares the pinned field backing storage")
		} else {
			*selected.Payload.Fields[selectedPremise].MinLength = 7
			selected.Payload.Fields[selectedEvidence].Enum[0] = "mutated"
		}
		if reflect.ValueOf(selected.Payload.Fields[selectedFact].Enum).Pointer() == reflect.ValueOf(workflowDecisionDomainSet).Pointer() {
			t.Errorf("the direct selection's fact enum aliases the canonical set")
		} else {
			selected.Payload.Fields[selectedFact].Enum[0] = "mutated"
		}
		switch {
		case selected.PublicPayload == nil:
			t.Errorf("the direct selection dropped the pinned closed PublicPayload")
		case len(selected.PublicPayload.Fields) == 0:
			t.Errorf("the direct selection carried no public fields")
		case reflect.ValueOf(selected.PublicPayload.Fields).Pointer() == reflect.ValueOf(pinned.PublicPayload.Fields).Pointer():
			t.Errorf("the direct selection shares the pinned PublicPayload backing storage")
		default:
			selected.PublicPayload.Fields[0].Name = "mutated"
		}
		inputAfter, inputErr := json.Marshal(definition)
		if inputErr != nil {
			t.Fatal(inputErr)
		}
		if string(inputAfter) != string(inputBefore) {
			t.Errorf("the direct selection mutated the input definition bytes")
		}
		if !reflect.DeepEqual(workflowDecisionDomainSet, canonicalBefore) {
			t.Errorf("the direct selection mutated the canonical decision-domain set: %v", workflowDecisionDomainSet)
		}

		// The selection is clone-only: mutating only the returned action and
		// its nested bounds, enums, and PublicPayload must not reach the
		// pinned definition, the canonical set, or the global builtin
		// storage.
		builtinBefore := builtinActionPolicies["approve_contract"]
		if publicOK {
			first.PublicPayload.Fields[0].Name = "mutated"
			if pinned.PublicPayload.Fields[0].Name != "premise" {
				t.Errorf("the returned PublicPayload aliases the pinned definition's PublicPayload")
			}
		}
		if factIndex < len(first.Payload.Fields) && first.Payload.Fields[factIndex].Name == "decision_domains" {
			returnedFact := first.Payload.Fields[factIndex]
			if len(returnedFact.Enum) != 0 {
				returnedFact.Enum[0] = "mutated"
				if workflowDecisionDomainSet[0] != "preference" {
					t.Errorf("the returned fact enum aliases the canonical set: %v", workflowDecisionDomainSet)
				}
				if pinned.Payload.Fields[factIndex].Enum[0] != "preference" {
					t.Errorf("the returned fact enum aliases the pinned declaration's enum")
				}
			}
			if returnedFact.MinItems == nil {
				t.Errorf("the returned fact carries no declared minimum bound")
			} else {
				*returnedFact.MinItems = 7
				if pinned.Payload.Fields[factIndex].MinItems == nil || *pinned.Payload.Fields[factIndex].MinItems != 0 {
					t.Errorf("the returned fact bounds alias the pinned declaration")
				}
			}
		}
		if reflect.ValueOf(first.Payload.Fields).Pointer() == reflect.ValueOf(pinned.Payload.Fields).Pointer() {
			t.Errorf("the selection shares the pinned definition's field backing storage")
		}
		if publicOK && reflect.ValueOf(first.PublicPayload.Fields).Pointer() == reflect.ValueOf(pinned.PublicPayload.Fields).Pointer() {
			t.Errorf("the selection shares the pinned PublicPayload's field backing storage")
		}
		if reflect.ValueOf(first.Payload.Fields).Pointer() == reflect.ValueOf(builtinActionPolicies["approve_contract"].Payload.Fields).Pointer() {
			t.Errorf("the selection shares the builtin policy's field backing storage")
		}
		if !reflect.DeepEqual(builtinActionPolicies["approve_contract"], builtinBefore) {
			t.Errorf("the selection mutated the global builtin approve_contract policy")
		}

		// A declared but optional wrong-bounds fact is still a declaration:
		// the selector replaces it once, at its position, in both payload
		// shapes, selection over its own answer is a fixpoint, and the local
		// declaration bytes stay untouched.
		wrongAction := factWritePinnedApproveAction(false)
		wrongFact := WorkflowPayloadField{Name: "decision_domains", ValueType: PayloadStringList, Required: false, MinItems: workflowInt(2), MaxItems: workflowInt(3), Enum: []string{"economics"}}
		insertAt := func(fields []WorkflowPayloadField, fact WorkflowPayloadField, index int) []WorkflowPayloadField {
			grown := make([]WorkflowPayloadField, 0, len(fields)+1)
			grown = append(grown, fields[:index]...)
			grown = append(grown, fact)
			return append(grown, fields[index:]...)
		}
		payloadWithout := wrongAction.Payload.Fields
		publicWithout := wrongAction.PublicPayload.Fields
		mid := len(payloadWithout) / 2
		wrongAction.Payload.Fields = insertAt(payloadWithout, wrongFact, mid)
		wrongAction.PublicPayload.Fields = insertAt(publicWithout, wrongFact, 1)
		wantPayload := append(insertAt(payloadWithout, workflowDecisionFactField(), mid), workflowLawChangeFields()...)
		wantPublic := append(insertAt(publicWithout, workflowDecisionFactField(), 1), workflowLawChangeFields()...)
		wrongDefinition := WorkflowDefinition{ApprovalPolicy: WorkflowApprovalPolicyConsequenceV1, ActionDefinitions: []WorkflowActionDefinition{wrongAction}}
		wrongBefore, wrongErr := json.Marshal(wrongDefinition)
		if wrongErr != nil {
			t.Fatal(wrongErr)
		}
		wrongSelected, wrongSelectErr := workflowDecisionFactInitialActionDefinition(wrongDefinition)
		if wrongSelectErr != nil {
			t.Fatalf("the selector refused the local wrong-bounds declaration: %v", wrongSelectErr)
		}
		if count := countDecisionDomainFields(wrongSelected.Payload.Fields); count != 1 {
			t.Errorf("the selection kept %d decision_domains fields beside the wrong-bounds declaration, want exactly one", count)
		}
		if !reflect.DeepEqual(wrongSelected.Payload.Fields, wantPayload) {
			t.Errorf("the selection resolved %+v, want the wrong-bounds declaration replaced in place with the canonical fact", wrongSelected.Payload.Fields)
		}
		if wrongSelected.PublicPayload == nil || !reflect.DeepEqual(wrongSelected.PublicPayload.Fields, wantPublic) {
			t.Errorf("the selection resolved public fields %+v, want the wrong-bounds public declaration replaced in place", wrongSelected.PublicPayload)
		}
		again, againErr := workflowDecisionFactInitialActionDefinition(WorkflowDefinition{ApprovalPolicy: WorkflowApprovalPolicyConsequenceV1, ActionDefinitions: []WorkflowActionDefinition{wrongSelected}})
		if againErr != nil {
			t.Fatalf("the selector refused its own selected declaration: %v", againErr)
		}
		if !reflect.DeepEqual(again, wrongSelected) {
			t.Errorf("selecting the selected action resolved %+v, want the same answer %+v", again, wrongSelected)
		}
		wrongAfter, wrongErr := json.Marshal(wrongDefinition)
		if wrongErr != nil {
			t.Fatal(wrongErr)
		}
		if string(wrongAfter) != string(wrongBefore) {
			t.Errorf("the selection mutated the local declaration bytes")
		}
	})
}

func assertFactWriteProjectedDomains(t *testing.T, s *Store, registry DefinitionRegistry, workID string, version int64, want []string) {
	t.Helper()
	contract, _, err := workflowCompletionContract(context.Background(), s.DatabaseForTesting(), registry, workID)
	if err != nil {
		t.Fatalf("read the registered active contract: %v", err)
	}
	if contract.Version != version || !contract.DecisionDomainsPresent || !reflect.DeepEqual(contract.DecisionDomains, want) {
		t.Fatalf("active contract v%d projects decision_domains %v (present %v), want v%d with the exact present fact %v", contract.Version, contract.DecisionDomains, contract.DecisionDomainsPresent, version, want)
	}
}

// factWriteSetLabel names a canonical decision-domain set for work item IDs.
func factWriteSetLabel(set []string) string {
	if len(set) == 0 {
		return "engineering"
	}
	return strings.Join(set, "-")
}

// factWriteInitialContractEvent authors the genuine initial contract event at
// its current payload version with an optional decision fact member: present
// false omits the member, a nil fact authors the explicit null, and typed Go
// values author the exact bytes the fold must store or refuse. The event
// folds through the registered fold; no projection column is touched.
func factWriteInitialContractEvent(t *testing.T, s *Store, workID, ownerRef string, fact any, present bool) Event {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	payload := map[string]any{
		"contract_version": int64(1), "premise": "deliver the checked change",
		"outcome_kind":      "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	}
	if present {
		payload["decision_domains"] = fact
	}
	return resolutionEvent("decision-fact-initial-contract-"+workID, WorkflowContractApproved, workID, ownerRef, version, payload)
}

// factWriteSuccessorEvent authors the versioned contract_superseded event
// whose complete successor contract carries an optional decision fact, the
// complete-successor shape the class-revision witnesses fold through the
// registered fold. present false omits the member from the successor alone.
func factWriteSuccessorEvent(t *testing.T, s *Store, workID, actorRef string, fact any, present bool) Event {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	successor := map[string]any{
		"contract_version": int64(2), "premise": "the reclassified accepted change",
		"outcome_kind":      "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
		"law_revisions": []any{}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	}
	if present {
		successor["decision_domains"] = fact
	}
	return resolutionEvent("decision-fact-successor-"+workID, WorkflowContractSuperseded, workID, actorRef, version, map[string]any{
		"previous_contract_version": 1, "predecessor_contract_versions": []int64{1}, "new_contract_version": 2,
		"supersede_reason": "reclassify the delivered work", "audit_evidence": []string{"audit:decision-fact-successor-" + workID},
		"successor_contract": successor,
	})
}

// factWriteCloneOnlyEvent authors the legacy clone-only supersession shape:
// no successor contract member at all.
func factWriteCloneOnlyEvent(t *testing.T, s *Store, workID, actorRef string) Event {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	return resolutionEvent("decision-fact-clone-only-"+workID, WorkflowContractSuperseded, workID, actorRef, version, map[string]any{
		"previous_contract_version": 1, "predecessor_contract_versions": []int64{1}, "new_contract_version": 2,
		"supersede_reason": "revise without supplying the successor contract", "audit_evidence": []string{"audit:decision-fact-clone-only-" + workID},
	})
}

// factWriteEventPayloadByID reads one recorded event's payload bytes by its
// event ID, so attribution bytes can be compared before and after a fold.
func factWriteEventPayloadByID(t *testing.T, s *Store, eventID string) string {
	t.Helper()
	var raw []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, eventID).Scan(&raw); err != nil {
		t.Fatalf("read event %s: %v", eventID, err)
	}
	return string(raw)
}

// seedFactWriteConfirmedEngineering seeds the consequence_v1 route to the
// acceptance step with an explicit engineering fact, records the independent
// verdict, and records the executing coordinator's confirmation through the
// constructor+fold seam. It returns the baseline work version.
func seedFactWriteConfirmedEngineering(t *testing.T, workID string) (*Store, DefinitionRegistry, WorkflowDefinition, WorkflowActor, int64) {
	t.Helper()
	s, registry, definition, owner, actors := seedApprovalItemAtAcceptance(t, workID, []string{})
	if err := recordApprovalVerdict(t, s, registry, workID, actors.reviewer); err != nil {
		t.Fatalf("the independent evaluator's verdict refused on the confirmed seed: %v", err)
	}
	if _, err := confirmResolutionPremise(t, s, registry, definition, workID, owner, nil, map[string]json.RawMessage{"selected_choice": json.RawMessage(`"confirm"`)}); err != nil {
		t.Fatalf("the executing coordinator's confirmation refused on the confirmed seed: %v", err)
	}
	if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != ownerRefForTest(t, owner) {
		t.Fatalf("the confirmed seed's confirmation row = %q (present %v), want the executing coordinator", confirmed, ok)
	}
	return s, registry, definition, owner, readWorkVersion(t, s, workID)
}

// factWriteOperatorSelection runs the stateless operator-selection chain in
// one short read transaction, so a decision-context digest refusal is
// observable on its own instead of behind the admission layer's artifact
// staleness. The version reads before the transaction opens, because the
// store pools one connection.
func factWriteOperatorSelection(t *testing.T, s *Store, registry DefinitionRegistry, workID, digest string) error {
	t.Helper()
	ctx := context.Background()
	version := readWorkVersion(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return validateWorkflowOperatorSelectionTx(ctx, tx, registry, WorkflowActionPreflightRequest{
		WorkID: workID, ActionID: "confirm_premise", SelectedChoice: "confirm", DecisionContextDigest: digest,
		ExpectedVersion: version,
	})
}

// assertFactWriteInitialNoEffect holds the refusal invariant for an initial
// decision-fact write: no contract row, no event movement, no version
// advance, and no stranded fold guard.
func assertFactWriteInitialNoEffect(t *testing.T, s *Store, workID string, baselineVersion int64, baselineEvents int) {
	t.Helper()
	if version := readWorkVersion(t, s, workID); version != baselineVersion {
		t.Fatalf("the refused initial fact write advanced the work version from %d to %d", baselineVersion, version)
	}
	if events := approvalEventCount(t, s, workID); events != baselineEvents {
		t.Fatalf("the refused initial fact write left %d events (baseline %d), want rollback with no append", events, baselineEvents)
	}
	if contracts := decisionContractCount(t, s, workID); contracts != 0 {
		t.Fatalf("the refused initial fact write left %d contract rows, want none", contracts)
	}
	assertNoFoldGuardRows(t, s)
}

// assertFactWritePredecessorPreserved holds the atomic-refusal invariant for a
// complete successor write over a confirmed engineering predecessor: the
// active predecessor keeps its fact and its open supersession slot, the work
// version and its event rows did not move, the live confirmation row stands,
// and no consent challenge, approval-kind evidence, or fold guard row exists.
func assertFactWritePredecessorPreserved(t *testing.T, s *Store, workID string, baselineVersion int64, baselineEvents int, confirmedBy string) {
	t.Helper()
	if version := readWorkVersion(t, s, workID); version != baselineVersion {
		t.Fatalf("the refused successor write advanced the work version from %d to %d", baselineVersion, version)
	}
	if events := approvalEventCount(t, s, workID); events != baselineEvents {
		t.Fatalf("the refused successor write left %d events (baseline %d), want rollback with no append", events, baselineEvents)
	}
	if contracts := decisionContractCount(t, s, workID); contracts != 1 {
		t.Fatalf("the refused successor write left %d contract rows, want the active predecessor alone", contracts)
	}
	if stored := storedDecisionDomains(t, s, workID, 1); stored != "[]" {
		t.Fatalf("the refused successor write changed the predecessor fact to %q, want [] preserved", stored)
	}
	var superseded any
	if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if superseded != nil {
		t.Fatalf("the refused successor write named superseded_by %v, want the predecessor left active", superseded)
	}
	activeVersion, err := activeWorkflowContractVersion(context.Background(), s.DatabaseForTesting(), workID, "workflow_decision_fact_write_test")
	if err != nil {
		t.Fatal(err)
	}
	if activeVersion != 1 {
		t.Fatalf("the refused successor write left active contract version %d, want 1", activeVersion)
	}
	if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != confirmedBy {
		t.Fatalf("the refused successor write moved the confirmation row to %q (present %v), want %q preserved", confirmed, ok, confirmedBy)
	}
	if challenges := approvalChallengeCount(t, s); challenges != 0 {
		t.Fatalf("the refused successor write left %d persisted consent challenges, want the zero-challenge state", challenges)
	}
	if kind := approvalKindEvidenceCount(t, s, workID); kind != 0 {
		t.Fatalf("the refused successor write left %d approval-kind evidence bindings, want none fabricated or consumed", kind)
	}
	assertNoFoldGuardRows(t, s)
}

// factWriteBrokenFacts enumerates the absent and broken decision-fact shapes
// both contract constructors must refuse under consequence_v1: the two
// required-presence shapes and every wire shape outside the canonical set.
func factWriteBrokenFacts() []struct {
	name    string
	fact    any
	present bool
} {
	return []struct {
		name    string
		fact    any
		present bool
	}{
		{name: "missing member", present: false},
		{name: "explicit null", fact: nil, present: true},
		{name: "nonstring string", fact: "product", present: true},
		{name: "nonstring object", fact: map[string]any{"product": true}, present: true},
		{name: "unknown member", fact: []string{"preference", "economics"}, present: true},
		{name: "duplicate member", fact: []string{"product", "product"}, present: true},
		{name: "noncanonical order", fact: []string{"product", "preference"}, present: true},
		{name: "over the closed set", fact: []string{"preference", "product", "ui_ux", "preference"}, present: true},
	}
}

// factWriteApproveAuthoring composes the initial approve_contract authoring
// fields the constructor and the closed payload validator consume: the valid
// flat planning shape beside an optional decision fact member.
func factWriteApproveAuthoring(workID string, fact any, present bool) map[string]any {
	payload := map[string]any{
		"contract_version": int64(1), "premise": "deliver the checked change",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "law_modifies": []string{},
		"rigor_class": "prototype_internal",
	}
	if present {
		payload["decision_domains"] = fact
	}
	return payload
}

// factWriteApproveFields decodes the authoring fields into the raw field map
// the approve_contract constructor consumes.
func factWriteApproveFields(t *testing.T, workID string, fact any, present bool) map[string]json.RawMessage {
	t.Helper()
	raw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, fact, present))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

// factWriteSuccessorPayload composes the complete supersede_contract recovery
// payload the carried-successor witness authors: every declared recovery
// field, the explicit predicate array, and an optional decision fact member.
// It returns the raw payload bytes beside their decoded fields.
func factWriteSuccessorPayload(t *testing.T, workID string, fact any, present bool) (json.RawMessage, map[string]json.RawMessage) {
	t.Helper()
	payload := map[string]any{
		"contract_version": 2,
		"premise":          "revise the active contract onto the explicit decision fact",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"},
		"route_conventions": []string{},
		"spec_mandate":      []string{},
		"law_modifies":      []string{},
		"rigor_class":       "prototype_internal",
		"supersede_reason":  "the active contract holds no explicit decision fact",
		"audit_evidence":    []string{"audit:decision-fact-successor-" + workID},
	}
	if present {
		payload["decision_domains"] = fact
	}
	raw := factWriteAuthoringJSON(t, payload)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return raw, fields
}

// factWriteAuthoringJSON encodes one composed authoring payload.
func factWriteAuthoringJSON(t *testing.T, authoring map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(authoring)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// factWritePayloadMember reads one raw payload object's decision_domains
// member bytes; present reports whether the member exists.
func factWritePayloadMember(t *testing.T, raw string) (member string, present bool) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("decode the recorded payload: %v", err)
	}
	if value, ok := fields["decision_domains"]; ok {
		return string(value), true
	}
	return "", false
}

// factWriteConstructInitialContract drives the actual approve_contract
// constructor in one short transaction and rolls the transaction back before
// any store-backed assertion, because the store pools one connection.
func factWriteConstructInitialContract(t *testing.T, s *Store, definition WorkflowDefinition, workID, ownerRef string, fields map[string]json.RawMessage, expected int64) ([]Event, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	events, constructErr := workflowApproveContractEvents(ctx, tx, definition, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expected, ActionID: "approve_contract",
		OperationID: "decision-fact-initial-" + workID, Now: time.Unix(12, 0).UTC(),
	}, ownerRef, fields, "decision-fact-initial-"+workID, expected)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	return events, constructErr
}

// factWriteConstructSuccessorContract drives the actual supersede_contract
// constructor in one short transaction and rolls the transaction back before
// any store-backed assertion, because the store pools one connection.
func factWriteConstructSuccessorContract(t *testing.T, s *Store, definition WorkflowDefinition, workID, ownerRef string, raw json.RawMessage, fields map[string]json.RawMessage, expected int64) ([]Event, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	events, constructErr := workflowSupersedeContractEvents(ctx, tx, definition, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expected, ActionID: "supersede_contract",
		OperationID: "decision-fact-successor-" + workID, Now: time.Unix(12, 0).UTC(),
	}, ownerRef, raw, fields, "decision-fact-successor-"+workID, expected)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	return events, constructErr
}

// factWriteExpectedSelection states the selected payload in order, independent
// of the production selector: released fields, law-change inputs, decision fact.
func factWriteExpectedSelection(released []WorkflowPayloadField) []WorkflowPayloadField {
	fields := append([]WorkflowPayloadField{}, released...)
	fields = append(fields, workflowLawChangeFields()...)
	return append(fields, workflowDecisionFactField())
}

// assertFactWriteFieldsCarryFact holds the declaration-owner agreement: the
// projected contract-authoring fields keep every released field in order,
// add the law-change inputs, and append the required closed decision fact.
func assertFactWriteFieldsCarryFact(t *testing.T, label string, fields, released []WorkflowPayloadField) {
	t.Helper()
	if !reflect.DeepEqual(fields, factWriteExpectedSelection(released)) {
		t.Fatalf("%s fields differ from the released fields plus the law-change inputs and canonical decision fact", label)
	}
	for index, field := range released {
		if !reflect.DeepEqual(fields[index], field) {
			t.Fatalf("%s altered released field %d (%s)", label, index, field.Name)
		}
	}
	fact := fields[len(fields)-1]
	if fact.Name != "decision_domains" || !fact.Required || fact.ValueType != PayloadStringList {
		t.Fatalf("%s appends %+v, want the required decision_domains string list", label, fact)
	}
	if fact.MinItems == nil || *fact.MinItems != 0 || fact.MaxItems == nil || *fact.MaxItems != int64(len(workflowDecisionDomainSet)) {
		t.Fatalf("%s restates the fact bounds %+v, want 0..%d", label, fact, len(workflowDecisionDomainSet))
	}
	if !reflect.DeepEqual(fact.Enum, workflowDecisionDomainSet) {
		t.Fatalf("%s restates the fact enum %v, want the canonical closed set %v", label, fact.Enum, workflowDecisionDomainSet)
	}
}

// factWritePinnedApproveAction authors the distinct closed approve_contract
// action on fresh storage — distinct premise minimum, required_evidence
// requiredness, metadata, and a closed PublicPayload — so no field list,
// bound pointer, or enum aliases the builtin policy or the canonical set.
// alreadyDeclaresFact inserts the canonical fact at a middle index in both
// payload shapes instead of leaving its declaration to selection.
func factWritePinnedApproveAction(alreadyDeclaresFact bool) WorkflowActionDefinition {
	distinctPremise := func() WorkflowPayloadField {
		return WorkflowPayloadField{Name: "premise", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(24), MaxLength: workflowInt(4096)}
	}
	distinctEvidence := func() WorkflowPayloadField {
		return WorkflowPayloadField{Name: "required_evidence", ValueType: PayloadStringList, Required: true, MinItems: workflowInt(1), MaxItems: workflowInt(7), Enum: []string{"verification", "review"}}
	}
	base := currentActionDefinition("approve_contract", true).Payload.Fields
	payloadFields := make([]WorkflowPayloadField, 0, len(base)+1)
	for _, field := range base {
		switch field.Name {
		case "premise":
			field = distinctPremise()
		case "required_evidence":
			field = distinctEvidence()
		}
		payloadFields = append(payloadFields, field)
	}
	publicFields := []WorkflowPayloadField{distinctPremise(), distinctEvidence()}
	if alreadyDeclaresFact {
		index := len(payloadFields)/2 + 1
		grown := make([]WorkflowPayloadField, 0, len(payloadFields)+1)
		grown = append(grown, payloadFields[:index]...)
		grown = append(grown, workflowDecisionFactField())
		grown = append(grown, payloadFields[index:]...)
		payloadFields = grown
		publicFields = []WorkflowPayloadField{distinctPremise(), workflowDecisionFactField(), distinctEvidence()}
	}
	return WorkflowActionDefinition{
		ID: "approve_contract", Consequence: ActionInternalSQLite, Approval: ActionApprovalRequired,
		ExecutionMode: ActionHold, RequiredCapability: "work_transition",
		Payload:       WorkflowPayloadDefinition{Closed: true, Fields: payloadFields},
		PublicPayload: &WorkflowPayloadDefinition{Closed: true, Fields: publicFields},
	}
}

// factWritePinnedApprovePayload varies the premise and required_evidence
// presence so the pinned premise minimum and requiredness stay observable
// beside an optional decision fact member.
func factWritePinnedApprovePayload(workID, premise string, evidence []string, fact any, present bool) map[string]any {
	payload := factWriteApproveAuthoring(workID, fact, present)
	payload["premise"] = premise
	if evidence != nil {
		payload["required_evidence"] = evidence
	} else {
		delete(payload, "required_evidence")
	}
	return payload
}

// factWriteSeedAtPlanning folds the planning prefix and, when withContract,
// the initial contract event. No approve_contract action is folded, so the
// pin keeps that route open.
func factWriteSeedAtPlanning(t *testing.T, s *Store, registry DefinitionRegistry, workID, ownerRef string, withContract bool) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	events := []Event{
		workflowActionCompletedFixture("decision-fact-planning-proposal-"+workID, workID, ownerRef, version, "proposal", "record_proposal"),
		workflowActionCompletedFixture("decision-fact-planning-discovery-"+workID, workID, ownerRef, version+1, "discovery", "record_discovery"),
		workflowActionCompletedFixture("decision-fact-planning-design-"+workID, workID, ownerRef, version+2, "design", "record_design"),
	}
	if withContract {
		events = append(events, resolutionEvent("decision-fact-planning-contract-"+workID, WorkflowContractApproved, workID, ownerRef, version+3, map[string]any{
			"contract_version": 1, "premise": "deliver the checked change under the pinned approval contract",
			"outcome_kind":      "check",
			"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
			"required_evidence": []string{"verification"}, "route_conventions": []string{},
			"spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"decision_domains": []string{"product"},
		}))
	}
	appendWorkflowEventsWithRegistry(t, s, registry, Operation{
		Events:           events,
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	})
}

// countDecisionDomainFields counts the decision_domains declarations in one
// field list.
func countDecisionDomainFields(fields []WorkflowPayloadField) int {
	count := 0
	for _, field := range fields {
		if field.Name == "decision_domains" {
			count++
		}
	}
	return count
}
