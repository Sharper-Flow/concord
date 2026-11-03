package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func authoredDecisionDomainSets() [][]string {
	classes := []string{"increased_spending", "legal", "product", "production_data", "security", "ui_ux"}
	sets := make([][]string, 0, 1<<len(classes))
	for mask := 0; mask < 1<<len(classes); mask++ {
		set := []string{}
		for index, class := range classes {
			if mask&(1<<index) != 0 {
				set = append(set, class)
			}
		}
		sets = append(sets, set)
	}
	return sets
}

func decisionFactInput(t *testing.T, id string, version int64, fact any) map[string]any {
	t.Helper()
	return firstCallActionInput(t, firstCallPublishedDefs(t), id, version, map[string]any{
		"outcome_predicates": firstCallPredicates(WorkflowStepPlanChange),
		"required_evidence":  []string{"verification"}, "spec_mandate": []string{"spec:one"},
		"architecture_binding": map[string]any{
			"home_domain_id": "root", "affected_domain_ids": []string{"root"},
			"domain_modifies": []string{}, "domain_relation_modifies": []map[string]any{},
			"law_additions":            []map[string]any{},
			"verification_obligations": []map[string]any{{"law_id": "spec:one", "obligation_id": "verification"}},
		},
		"decision_domains": fact,
	})
}

func decisionFactConstruct(t *testing.T, s *Store, id string, actor WorkflowActor, input map[string]any, version int64) ([]Event, error) {
	t.Helper()
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(input["fields"])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	events, constructErr := workflowApproveContractEvents(ctx, tx, WorkflowStepPlanChange, WorkflowActionExecutionRequest{
		WorkID: id, ExpectedVersion: version, ActionID: "approve_contract", Now: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC),
	}, actorRef, fields, "decision-fact-constructed", version)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	return events, constructErr
}

func decisionFactReceipt(t *testing.T, s *Store, id, kind string) (string, string, string) {
	t.Helper()
	var eventID, digest, actor string
	if err := s.db.QueryRow(`SELECT event_id,payload_digest,actor FROM effect_receipts WHERE subject_id=? AND kind=? ORDER BY effect_seq DESC LIMIT 1`, id, kind).Scan(&eventID, &digest, &actor); err != nil {
		t.Fatal(err)
	}
	return eventID, digest, actor
}

func decisionFactProjection(t *testing.T, s *Store, id string, version int64, want []string, actor string) {
	t.Helper()
	if got := storedDecisionDomains(t, s, id, version); got != workflowJSON(want) {
		t.Fatalf("stored v%d fact %s, want %s", version, got, workflowJSON(want))
	}
	contract, err := readWorkflowContractVersion(t.Context(), s.db, id, version)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Version != version || !reflect.DeepEqual(contract.DecisionDomains, want) || contract.ApprovedBy != actor {
		t.Fatalf("contract projection %+v, want v%d fact %v approved by %s", contract, version, want, actor)
	}
}

func decisionFactUpdateEvent(t *testing.T, s *Store, id string, actor WorkflowActor, contract map[string]any, version int64) Event {
	t.Helper()
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	var kind string
	if err := s.db.QueryRow(`SELECT kind FROM work_items WHERE id=?`, id).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"title": "Updated plan", "value_statement": "Retain valid proof", "kind": kind, "tags": []string{},
		"reason": "revise the plan", "expected_version": version, "resulting_version": version + 1, "contract": contract,
	})
	if err != nil {
		t.Fatal(err)
	}
	return Event{EventID: fmt.Sprintf("record-update-%s-%d", id, version), Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: id, Actor: actorRef, OccurredAt: time.Unix(40, version).UTC(), PayloadVersion: 1, Payload: payload}
}

func decisionFactAssertDigest(t *testing.T, s *Store, eventID string, payload []byte) {
	t.Helper()
	want, err := effectPayloadDigest(payload)
	if err != nil {
		t.Fatal(err)
	}
	var got string
	if err := s.db.QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, eventID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("receipt digest %s, want %s", got, want)
	}
}

func decisionFactBadCases() []struct {
	name    string
	value   any
	present bool
} {
	return []struct {
		name    string
		value   any
		present bool
	}{
		{"missing", nil, false}, {"null", nil, true}, {"string", "product", true},
		{"object", map[string]any{"product": true}, true}, {"unknown", []string{"economics"}, true},
		{"duplicate", []string{"product", "product"}, true}, {"unordered", []string{"ui_ux", "product"}, true},
		{"too many", []string{"increased_spending", "legal", "product", "production_data", "security", "ui_ux", "product"}, true},
	}
}

func TestDecisionFactWriteBoundary(t *testing.T) {
	for index, set := range authoredDecisionDomainSets() {
		t.Run(fmt.Sprintf("canonical-%02d", index), func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			id := fmt.Sprintf("decision-fact-%02d", index)
			actor := seedFirstCallWorkflow(t, s, id, "decision-fact", WorkflowStepPlanChange)
			actorRef, err := WorkflowActorRef(actor)
			if err != nil {
				t.Fatal(err)
			}
			version := readWorkVersion(t, s, id)
			input := decisionFactInput(t, id, version, set)
			events, err := decisionFactConstruct(t, s, id, actor, input, version)
			if err != nil || len(events) != 1 {
				t.Fatalf("construct initial: %v, events=%d", err, len(events))
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(events[0].Payload, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["decision_domains"]) != workflowJSON(set) {
				t.Fatalf("constructed fact %s, want %s", fields["decision_domains"], workflowJSON(set))
			}
			if err := firstCallApproveContract(t, s, id, version, "approve-"+id, actor, input); err != nil {
				t.Fatal(err)
			}
			contractID, contractDigest, _ := decisionFactReceipt(t, s, id, WorkflowContractApproved)
			decisionFactAssertDigest(t, s, contractID, events[0].Payload)
			decisionFactProjection(t, s, id, 1, set, actorRef)

			s2 := openTemp(t)
			defer s2.Close()
			id = fmt.Sprintf("decision-fact-update-%02d", index)
			actor = seedFirstCallWorkflow(t, s2, id, "decision-fact", WorkflowStepPlanChange)
			version = readWorkVersion(t, s2, id)
			if err := firstCallApproveContract(t, s2, id, version, "approve-"+id, actor, decisionFactInput(t, id, version, []string{})); err != nil {
				t.Fatal(err)
			}
			s = s2
			contractID, contractDigest, _ = decisionFactReceipt(t, s, id, WorkflowContractApproved)
			version = readWorkVersion(t, s, id)
			contract := pContractSuccessor(t, s, id, "The revised decision fact.")
			contract["decision_domains"] = set
			event := decisionFactUpdateEvent(t, s, id, actor, contract, version)
			if err := reviseContractRecord(t, s, id, actor, contract, version); err != nil {
				t.Fatal(err)
			}
			decisionFactAssertDigest(t, s, event.EventID, event.Payload)
			decisionFactProjection(t, s, id, 2, set, actorRef)
			decisionFactProjection(t, s, id, 1, []string{}, actorRef)
			var successor int64
			if err := s.db.QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, id).Scan(&successor); err != nil || successor != 2 {
				t.Fatalf("superseded_by=%d err=%v", successor, err)
			}
			_, after, _ := decisionFactReceipt(t, s, id, WorkflowContractApproved)
			if after != contractDigest || readWorkVersion(t, s, id) != version+1 {
				t.Fatal("revision changed predecessor receipt or did not advance once")
			}
		})
	}
}

func TestDecisionFactInvalidWritesAreAtomic(t *testing.T) {
	for _, tc := range decisionFactBadCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			id := "decision-fact-invalid-" + strings.ReplaceAll(tc.name, " ", "-")
			actor := seedFirstCallWorkflow(t, s, id, "decision-fact-invalid", WorkflowStepPlanChange)
			version := readWorkVersion(t, s, id)
			input := decisionFactInput(t, id, version, tc.value)
			if !tc.present {
				delete(input["fields"].(map[string]any), "decision_domains")
			}
			before := contractRecordEffectSnapshot(t, s, id)
			if err := firstCallApproveContract(t, s, id, version, "invalid-"+id, actor, input); !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("initial %s: %v", tc.name, err)
			}
			if got := contractRecordEffectSnapshot(t, s, id); got != before || readWorkVersion(t, s, id) != version {
				t.Fatal("invalid initial write changed durable state")
			}
			assertNoFoldGuardRows(t, s)
			if _, err := decisionFactConstruct(t, s, id, actor, input, version); !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("constructor %s: %v", tc.name, err)
			}

			valid := decisionFactInput(t, id, version, []string{})
			if err := firstCallApproveContract(t, s, id, version, "valid-"+id, actor, valid); err != nil {
				t.Fatal(err)
			}
			version = readWorkVersion(t, s, id)
			contract := pContractSuccessor(t, s, id, "A revised decision fact.")
			if tc.present {
				contract["decision_domains"] = tc.value
			} else {
				delete(contract, "decision_domains")
			}
			before = contractRecordEffectSnapshot(t, s, id)
			if err := reviseContractRecord(t, s, id, actor, contract, version); !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("revision %s: %v", tc.name, err)
			}
			if got := contractRecordEffectSnapshot(t, s, id); got != before || readWorkVersion(t, s, id) != version {
				t.Fatal("invalid revision changed durable state")
			}
			assertNoFoldGuardRows(t, s)
			var superseded any
			if err := s.db.QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, id).Scan(&superseded); err != nil || superseded != nil {
				t.Fatalf("predecessor superseded=%v err=%v", superseded, err)
			}
		})
	}
}

func TestDecisionFactRevisionPreservesConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after []string
	}{
		{"engineering-to-decision", []string{}, []string{"product"}},
		{"decision-to-engineering", []string{"product"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			id := "decision-fact-" + tc.name
			actor := seedFirstCallWorkflow(t, s, id, "decision-confirm", WorkflowStepPlanChange)
			actorRef, err := WorkflowActorRef(actor)
			if err != nil {
				t.Fatal(err)
			}
			version := readWorkVersion(t, s, id)
			if err := firstCallApproveContract(t, s, id, version, "approve-"+id, actor, decisionFactInput(t, id, version, tc.before)); err != nil {
				t.Fatal(err)
			}
			version = readWorkVersion(t, s, id)
			ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			confirmation := workflowTypedEvent("confirm-"+id, WorkflowPremiseConfirmed, id, actorRef, time.Unix(42, 0).UTC(), version, map[string]any{"contract_version": 1, "confirming_actor_ref": actorRef, "confirmation_authority": "coordinator"})
			confirmation.PayloadVersion = CurrentEventPayloadVersion(WorkflowPremiseConfirmed)
			if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{confirmation}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): version}}); err != nil {
				t.Fatal(err)
			}
			confirmID, confirmDigest, confirmActor := decisionFactReceipt(t, s, id, WorkflowPremiseConfirmed)
			contractID, contractDigest, _ := decisionFactReceipt(t, s, id, WorkflowContractApproved)
			if confirmed, ok := approvalPremiseConfirmationRow(t, s, id, 1); !ok || confirmed != actorRef {
				t.Fatalf("confirmation=%q present=%v", confirmed, ok)
			}
			for _, bad := range decisionFactBadCases() {
				version = readWorkVersion(t, s, id)
				invalid := pContractSuccessor(t, s, id, "A rejected reclassification.")
				if bad.present {
					invalid["decision_domains"] = bad.value
				} else {
					delete(invalid, "decision_domains")
				}
				before := contractRecordEffectSnapshot(t, s, id)
				if err := reviseContractRecord(t, s, id, actor, invalid, version); !hasFailureKind(err, KindInvalidPayload) {
					t.Fatalf("%s: %v", bad.name, err)
				}
				if contractRecordEffectSnapshot(t, s, id) != before || readWorkVersion(t, s, id) != version {
					t.Fatalf("%s changed durable state", bad.name)
				}
				if confirmed, ok := approvalPremiseConfirmationRow(t, s, id, 1); !ok || confirmed != actorRef {
					t.Fatalf("%s changed confirmation", bad.name)
				}
			}
			version = readWorkVersion(t, s, id)
			contract := pContractSuccessor(t, s, id, "A reclassified contract.")
			contract["decision_domains"] = tc.after
			if err := reviseContractRecord(t, s, id, actor, contract, version); err != nil {
				t.Fatal(err)
			}
			decisionFactProjection(t, s, id, 1, tc.before, actorRef)
			decisionFactProjection(t, s, id, 2, tc.after, actorRef)
			if confirmed, ok := approvalPremiseConfirmationRow(t, s, id, 1); !ok || confirmed != actorRef {
				t.Fatalf("predecessor confirmation=%q present=%v", confirmed, ok)
			}
			if _, ok := approvalPremiseConfirmationRow(t, s, id, 2); ok {
				t.Fatal("successor inherited confirmation")
			}
			idAfter, digestAfter, actorAfter := decisionFactReceipt(t, s, id, WorkflowPremiseConfirmed)
			oldID, oldDigest, _ := decisionFactReceipt(t, s, id, WorkflowContractApproved)
			if idAfter != confirmID || digestAfter != confirmDigest || actorAfter != confirmActor || oldID != contractID || oldDigest != contractDigest || readWorkVersion(t, s, id) != version+1 {
				t.Fatal("revision changed predecessor attribution or receipt")
			}
			var superseded int64
			if err := s.db.QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, id).Scan(&superseded); err != nil || superseded != 2 {
				t.Fatalf("superseded_by=%d err=%v", superseded, err)
			}
			tx, err := s.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			err = workflowPremiseConfirmedAuthorityTx(t.Context(), tx, id, 1, actorRef, "coordinator")
			if rollbackErr := tx.Rollback(); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			if err == nil {
				t.Fatal("superseded contract retained confirmation authority")
			}
		})
	}
}

func TestDecisionFactDeclarationsAgree(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	const id = "decision-fact-declarations"
	actor := seedFirstCallWorkflow(t, s, id, "decision-declarations", WorkflowStepPlanChange)
	version := readWorkVersion(t, s, id)
	input := decisionFactInput(t, id, version, []string{"product"})
	raw, err := json.Marshal(input["fields"])
	if err != nil {
		t.Fatal(err)
	}
	action, err := WorkflowActionDefinitionFor(context.Background(), s, WorkflowActionPreflightRequest{WorkID: id, ExpectedVersion: version, ActionID: "approve_contract", Actor: actor, Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := WorkflowPlanActionContract("approve_contract")
	if err != nil || !reflect.DeepEqual(action, catalog) {
		t.Fatalf("preflight action differs from catalog: %v", err)
	}
	for _, payload := range []WorkflowPayloadDefinition{action.Payload, WorkflowPlanContractUpdatePayload()} {
		var facts []WorkflowPayloadField
		for _, field := range payload.Fields {
			if field.Name == "decision_domains" {
				facts = append(facts, field)
			}
		}
		if !payload.Closed || len(facts) != 1 || !reflect.DeepEqual(facts[0], workflowDecisionFactField()) {
			t.Fatalf("decision fact declaration: %+v", facts)
		}
		fact := facts[0]
		if !fact.Required || fact.ValueType != PayloadStringList || fact.MinItems == nil || *fact.MinItems != 0 || fact.MaxItems == nil || *fact.MaxItems != 6 || !reflect.DeepEqual(fact.Enum, []string{"increased_spending", "legal", "product", "production_data", "security", "ui_ux"}) {
			t.Fatalf("decision fact bounds or enum: %+v", fact)
		}
	}
	pin, err := ReadWorkPin(t.Context(), s, id)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, intent := range pin.NextValidIntents {
		if intent.ActionID == "approve_contract" {
			found = true
			if !slices.Contains(intent.RequiredFields, "decision_domains") {
				t.Fatalf("intent fields %v", intent.RequiredFields)
			}
		}
	}
	if !found {
		t.Fatal("approval intent missing")
	}
	if err := payloadschema.Validate("work_transition_action_variant_approve_contract", mustJSONRaw(t, input)); err != nil {
		t.Fatal(err)
	}
	if err := firstCallApproveContract(t, s, id, version, "approve-declarations", actor, input); err != nil {
		t.Fatal(err)
	}
	version = readWorkVersion(t, s, id)
	defs := firstCallPublishedDefs(t)
	revise := firstCallObject(defs, firstCallResolve(defs, map[string]any{"$ref": "#/$defs/work_define_revise_input"}))
	contract := pContractSuccessor(t, s, id, "A revised decision fact.")
	revise["work_id"], revise["expected_version"], revise["contract"] = id, version, contract
	var reviseKind string
	if err := s.db.QueryRow(`SELECT kind FROM work_items WHERE id=?`, id).Scan(&reviseKind); err != nil {
		t.Fatal(err)
	}
	revise["kind"] = reviseKind
	if err := payloadschema.Validate("work_define_revise_input", mustJSONRaw(t, revise)); err != nil {
		t.Fatalf("complete update schema: %v", err)
	}
	for _, tc := range decisionFactBadCases() {
		if tc.present {
			contract["decision_domains"] = tc.value
		} else {
			delete(contract, "decision_domains")
		}
		if tc.name == "unordered" {
			continue
		}
		if err := payloadschema.Validate("work_define_revise_input", mustJSONRaw(t, revise)); err == nil {
			t.Errorf("update schema admitted %s", tc.name)
		}
	}
	delete(revise, "contract")
	if err := payloadschema.Validate("work_define_revise_input", mustJSONRaw(t, revise)); err != nil {
		t.Fatalf("optional contract: %v", err)
	}
	for _, value := range []any{nil, map[string]any{"unknown_field": true}} {
		revise["contract"] = value
		if err := payloadschema.Validate("work_define_revise_input", mustJSONRaw(t, revise)); err == nil {
			t.Errorf("schema admitted contract %v", value)
		}
	}
}
