package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The typed decision fact is a contract-owned array on workflow.contract_approved
// and its successor construction. The closed canonical set is preference,
// product, ui_ux; [] is engineering; NULL on the projection column is
// historical absence, never engineering. Current approvals require the fact;
// the read decoder and projection hash still distinguish a stored NULL from [].
// Storage events are constructed directly at explicit payload versions.

// decisionDomainsContract builds one contract-approved payload for a research
// fixture with an optional decision_domains value (nil omits the member).
func decisionDomainsContract(workID string, contractVersion int64, domains any) map[string]any {
	contract := map[string]any{
		"work_id": workID, "expected_version": int64(4), "resulting_version": int64(5), "contract_version": contractVersion,
		"premise": "record the research report", "outcome_kind": "outcome",
		"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}},
		"outcome_predicates": []map[string]any{{"predicate_id": "predicate:research-report-exit", "ordinal": 0, "outcome_kind": "outcome",
			"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}},
		"required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{},
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	}
	if domains != nil {
		contract["decision_domains"] = domains
	}
	return contract
}

// seedDecisionDomainsItem seeds a research-pinned work item through its first
// contract, the minimal fold chain a decision fact needs. payloadVersion pins
// the event's payload version: the current version for a live-shaped contract,
// an older one for a historical or forged shape.
func seedDecisionDomainsItem(t *testing.T, s *Store, workID string, domains any, payloadVersion int) error {
	t.Helper()
	ctx := context.Background()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := BuiltinWorkflowRegistry().Current("workflow.research")
	if !ok {
		t.Fatal("workflow.research v5 is not registered")
	}
	event := workflowEventWithActor("decision-contract-"+workID, WorkflowContractApproved, workID, ownerRef, decisionDomainsContract(workID, 1, domains))
	event.PayloadVersion = payloadVersion
	setup := []Event{
		workflowEventWithActor("decision-actor-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEventWithActor("decision-definition-"+workID, WorkflowDefinitionSelected, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
		event,
	}
	return applyWorkflowTestOperation(ctx, s, Operation{Events: setup, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}})
}

// seedDecisionDomainsItemFatal seeds at the current payload version and fails
// the test on any refusal.
func seedDecisionDomainsItemFatal(t *testing.T, s *Store, workID string, domains any) string {
	t.Helper()
	if err := seedDecisionDomainsItem(t, s, workID, domains, eventKindRegistry[WorkflowContractApproved].CurrentVersion); err != nil {
		t.Fatalf("seed decision domains item: %v", err)
	}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	return ownerRef
}

// storedDecisionDomains reads the projection column text for one contract row;
// empty string means NULL.
func storedDecisionDomains(t *testing.T, s *Store, workID string, contractVersion int64) string {
	t.Helper()
	var raw *string
	if err := s.DatabaseForTesting().QueryRow(`SELECT decision_domains FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, contractVersion).Scan(&raw); err != nil {
		t.Fatalf("read decision_domains column: %v", err)
	}
	if raw == nil {
		return ""
	}
	return *raw
}

// seedStoredDecisionDomainAbsence isolates the read/hash representation of a
// stored NULL without claiming that a current approval may author that shape.
func seedStoredDecisionDomainAbsence(t *testing.T, s *Store, workID string) {
	t.Helper()
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		_, err := tx.tx.ExecContext(context.Background(), `INSERT INTO fold_guard(active) VALUES(1);
			UPDATE workflow_contracts SET decision_domains=NULL WHERE work_id=?;
			DELETE FROM fold_guard`, workID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// decisionContractCount counts the contract rows a work item holds.
func decisionContractCount(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&count); err != nil {
		t.Fatalf("count contract rows: %v", err)
	}
	return count
}

// projectedDecisionDomains reads the active contract's typed fact through the
// read projection.
func projectedDecisionDomains(t *testing.T, s *Store, workID string) []string {
	t.Helper()
	projection, err := ReadWorkflowProjection(context.Background(), s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatalf("read workflow projection: %v", err)
	}
	if projection.Contract == nil {
		t.Fatal("the projection exposes no active contract")
	}
	return projection.Contract.DecisionDomains
}

func TestDecisionDomains(t *testing.T) {
	t.Parallel()

	t.Run("current approvals refuse missing and null facts atomically", func(t *testing.T) {
		t.Parallel()
		for name, domains := range map[string]any{"missing": nil, "null": json.RawMessage("null")} {
			s := openTemp(t)
			workID := "decision-" + name
			err := seedDecisionDomainsItem(t, s, workID, domains, eventKindRegistry[WorkflowContractApproved].CurrentVersion)
			if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "requires the declared decision-domain fact") {
				t.Fatalf("%s decision fact error = %v, want the declared-fact refusal", name, err)
			}
			if decisionContractCount(t, s, workID) != 0 || readWorkVersion(t, s, workID) != 2 {
				t.Fatalf("%s refusal changed the contract population or work version", name)
			}
			s.Close()
		}
	})

	t.Run("typed JSON omits only absence and keeps engineering present", func(t *testing.T) {
		t.Parallel()
		absent := workflowContractApprovedPayload{}
		encoded, err := json.Marshal(absent)
		if err != nil {
			t.Fatal(err)
		}
		if containsJSONMember(string(encoded)) {
			t.Fatalf("an absent fact serialized the member: %s", encoded)
		}
		engineering := workflowContractApprovedPayload{DecisionDomains: []string{}}
		encoded, err = json.Marshal(engineering)
		if err != nil {
			t.Fatal(err)
		}
		if !containsJSONMember(string(encoded)) || !strings.Contains(string(encoded), `"decision_domains":[]`) {
			t.Fatalf("an engineering fact serialized %s, want decision_domains:[]", encoded)
		}
		declared := workflowContractApprovedPayload{DecisionDomains: []string{"product"}}
		encoded, err = json.Marshal(declared)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"decision_domains":["product"]`) {
			t.Fatalf("a declared fact serialized %s, want the values", encoded)
		}
		readAbsent := WorkflowReadContract{}
		encoded, err = json.Marshal(readAbsent)
		if err != nil {
			t.Fatal(err)
		}
		if containsJSONMember(string(encoded)) {
			t.Fatalf("an absent read fact serialized the member: %s", encoded)
		}
		readEngineering := WorkflowReadContract{DecisionDomains: []string{}}
		encoded, err = json.Marshal(readEngineering)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"decision_domains":[]`) {
			t.Fatalf("an engineering read fact serialized %s, want decision_domains:[]", encoded)
		}
	})

	t.Run("unknown duplicate unsorted and oversized sets refuse", func(t *testing.T) {
		t.Parallel()
		for name, domains := range map[string][]string{
			"unknown":   {"preference", "economics"},
			"duplicate": {"product", "product"},
			"unsorted":  {"product", "preference"},
			"oversized": {"preference", "product", "ui_ux", "preference"},
		} {
			s := openTemp(t)
			if err := seedDecisionDomainsItem(t, s, "decision-refuse-"+name, domains, eventKindRegistry[WorkflowContractApproved].CurrentVersion); err == nil {
				t.Errorf("%s decision set folded, want refusal", name)
			}
			s.Close()
		}
	})

	t.Run("engineering empty and mixed sets round trip", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		defer s.Close()
		seedDecisionDomainsItemFatal(t, s, "decision-engineering", []string{})
		if stored := storedDecisionDomains(t, s, "decision-engineering", 1); stored != "[]" {
			t.Fatalf("engineering contract stored %q, want [] distinguished from NULL", stored)
		}
		if domains := projectedDecisionDomains(t, s, "decision-engineering"); domains == nil || len(domains) != 0 {
			t.Fatalf("engineering contract projected %v, want the explicit empty set", domains)
		}
		// A separate store: the law fixture pins fixed event IDs, so each
		// seeded item owns its store.
		s2 := openTemp(t)
		defer s2.Close()
		seedDecisionDomainsItemFatal(t, s2, "decision-mixed", []string{"preference", "ui_ux"})
		if stored := storedDecisionDomains(t, s2, "decision-mixed", 1); stored != `["preference","ui_ux"]` {
			t.Fatalf("mixed contract stored %q, want the canonical sorted set", stored)
		}
		if domains := projectedDecisionDomains(t, s2, "decision-mixed"); !reflect.DeepEqual(domains, []string{"preference", "ui_ux"}) {
			t.Fatalf("mixed contract projected %v, want the canonical sorted set", domains)
		}
	})

	t.Run("successor correction reclassifies only through supersession", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		defer s.Close()
		ownerRef := seedDecisionDomainsItemFatal(t, s, "decision-successor", []string{"product"})
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='decision-successor'`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		successor := map[string]any{
			"contract_version": int64(2), "premise": "the corrected decision-bearing premise",
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:research-report-exit", "ordinal": 0, "outcome_kind": "outcome",
				"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}},
			"required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{},
			"law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
			"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"decision_domains": []string{"preference", "ui_ux"},
		}
		supersede := workflowEventWithActor("decision-successor-supersede", WorkflowContractSuperseded, "decision-successor", ownerRef, map[string]any{
			"work_id": "decision-successor", "expected_version": version, "resulting_version": version + 1,
			"previous_contract_version": 1, "new_contract_version": 2,
			"supersede_reason": "correct the declared decision domains", "audit_evidence": []string{"evidence:domain-audit"},
			"successor_contract": successor,
		})
		supersede.PayloadVersion = eventKindRegistry[WorkflowContractSuperseded].CurrentVersion
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{supersede}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "decision-successor"): version}}); err != nil {
			t.Fatalf("successor correction refused: %v", err)
		}
		if stored := storedDecisionDomains(t, s, "decision-successor", 1); stored != `["product"]` {
			t.Fatalf("superseded predecessor stored %q, want its recorded fact immutable", stored)
		}
		if stored := storedDecisionDomains(t, s, "decision-successor", 2); stored != `["preference","ui_ux"]` {
			t.Fatalf("successor stored %q, want the corrected set", stored)
		}
		if domains := projectedDecisionDomains(t, s, "decision-successor"); !reflect.DeepEqual(domains, []string{"preference", "ui_ux"}) {
			t.Fatalf("active contract projected %v, want the corrected set", domains)
		}
	})

	t.Run("current supersession refuses the legacy clone route atomically", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		defer s.Close()
		ownerRef := seedDecisionDomainsItemFatal(t, s, "decision-clone", []string{"ui_ux"})
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='decision-clone'`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		clone := workflowEventWithActor("decision-clone-supersede", WorkflowContractSuperseded, "decision-clone", ownerRef, map[string]any{
			"work_id": "decision-clone", "expected_version": version, "resulting_version": version + 1,
			"previous_contract_version": 1, "new_contract_version": 2,
			"supersede_reason": "legacy revision", "audit_evidence": []string{"evidence:clone-audit"},
		})
		err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{clone}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "decision-clone"): version}})
		if !hasFailureKind(err, KindInvalidPayload) || !strings.Contains(err.Error(), "cannot use the legacy clone route") {
			t.Fatalf("legacy clone error = %v, want the current supersession refusal", err)
		}
		if decisionContractCount(t, s, "decision-clone") != 1 || readWorkVersion(t, s, "decision-clone") != version {
			t.Fatal("refused clone changed the contract population or work version")
		}
		if stored := storedDecisionDomains(t, s, "decision-clone", 1); stored != `["ui_ux"]` {
			t.Fatalf("refused clone changed the predecessor's recorded fact to %q", stored)
		}
	})

	t.Run("successor version boundary governs the inner fact", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		defer s.Close()
		ownerRef := seedDecisionDomainsItemFatal(t, s, "decision-successor-boundary", []string{"product"})
		var version int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='decision-successor-boundary'`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		successor := map[string]any{
			"contract_version": int64(2), "premise": "the corrected decision-bearing premise",
			"outcome_predicates": []map[string]any{{"predicate_id": "predicate:research-report-exit", "ordinal": 0, "outcome_kind": "outcome",
				"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{"report_recorded"}}}},
			"required_evidence": []string{"artifact"}, "route_conventions": []string{}, "spec_mandate": []string{},
			"law_modifies": []string{}, "law_revisions": []WorkflowLawRevision{}, "law_boundary_version": 1,
			"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"decision_domains": []string{"preference", "ui_ux"},
		}
		forged := workflowEventWithActor("decision-boundary-forged", WorkflowContractSuperseded, "decision-successor-boundary", ownerRef, map[string]any{
			"work_id": "decision-successor-boundary", "expected_version": version, "resulting_version": version + 1,
			"previous_contract_version": 1, "new_contract_version": 2,
			"supersede_reason": "successor claims a fact its recorded supersession predates", "audit_evidence": []string{"evidence:boundary-audit"},
			"successor_contract": successor,
		})
		forged.PayloadVersion = eventKindRegistry[WorkflowContractSuperseded].CurrentVersion - 1
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{forged}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "decision-successor-boundary"): version}}); err == nil {
			t.Fatal("a supersession recorded below its own boundary installed a successor carrying the fact, want refusal")
		}
		if count := decisionContractCount(t, s, "decision-successor-boundary"); count != 1 {
			t.Fatalf("refused forged successor left %d contract rows, want the original 1", count)
		}
		declared := workflowEventWithActor("decision-boundary-declared", WorkflowContractSuperseded, "decision-successor-boundary", ownerRef, map[string]any{
			"work_id": "decision-successor-boundary", "expected_version": version, "resulting_version": version + 1,
			"previous_contract_version": 1, "new_contract_version": 2,
			"supersede_reason": "correct the declared decision domains", "audit_evidence": []string{"evidence:boundary-audit"},
			"successor_contract": successor,
		})
		declared.PayloadVersion = eventKindRegistry[WorkflowContractSuperseded].CurrentVersion
		if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{declared}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "decision-successor-boundary"): version}}); err != nil {
			t.Fatalf("declared successor correction refused: %v", err)
		}
		if stored := storedDecisionDomains(t, s, "decision-successor-boundary", 1); stored != `["product"]` {
			t.Fatalf("superseded predecessor stored %q, want its recorded fact immutable", stored)
		}
		if stored := storedDecisionDomains(t, s, "decision-successor-boundary", 2); stored != `["preference","ui_ux"]` {
			t.Fatalf("successor stored %q, want the corrected set", stored)
		}
	})

	t.Run("the decision fact moves the full production hash", func(t *testing.T) {
		t.Parallel()
		hashes := map[string]string{}
		for name, domains := range map[string]any{
			"engineering": []string{},
			"declared":    []string{"product"},
		} {
			s := openTemp(t)
			seedDecisionDomainsItemFatal(t, s, "decision-hash", domains)
			first, err := WorkflowProjectionHash(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			hashes[name] = first
			if name == "engineering" {
				seedStoredDecisionDomainAbsence(t, s, "decision-hash")
				absent, err := WorkflowProjectionHash(context.Background(), s)
				if err != nil {
					t.Fatal(err)
				}
				hashes["absent"] = absent
			}
			s.Close()
		}
		if hashes["absent"] == hashes["engineering"] {
			t.Fatal("engineering [] hashed equal to historical absence, want distinct full hashes")
		}
		if hashes["engineering"] == hashes["declared"] {
			t.Fatal("a declared fact hashed equal to engineering, want distinct full hashes")
		}
	})

	t.Run("authored fact on a pre-boundary payload refuses", func(t *testing.T) {
		t.Parallel()
		s := openTemp(t)
		defer s.Close()
		if err := seedDecisionDomainsItem(t, s, "decision-forged", []string{"product"}, eventKindRegistry[WorkflowContractApproved].CurrentVersion-1); err == nil {
			t.Fatal("a payload carrying decision_domains at the pre-boundary version folded, want refusal")
		}
		var count int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id='decision-forged'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("refused forged fact left %d contract rows, want none", count)
		}
	})

}

// containsJSONMember reports whether an encoded object carries the
// decision_domains member, without decoding the whole payload into a map.
func containsJSONMember(payload string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &fields); err != nil {
		return false
	}
	_, present := fields["decision_domains"]
	return present
}
