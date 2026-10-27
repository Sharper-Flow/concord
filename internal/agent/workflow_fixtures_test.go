package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func seedCurrentWorkflowDomainFixture(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatalf("pm1fixture.SeedCommittedProductDomain: %v", err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
}

// Source verification runs before the owning transaction; its returned context
// carries the production proof into InitializeWorkflowTx.
func agentWorkflowSourceProof(t *testing.T, ctx context.Context, s *store.Store, workID string) context.Context {
	t.Helper()
	verified, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	return verified
}

// seedCurrentWorkflowDomainOnce seeds the canonical Product Domain registry
// when the store holds none, so journeys that call it per subtest on one
// shared store never double-seed the law locator.
func seedCurrentWorkflowDomainOnce(t *testing.T, s *store.Store) {
	t.Helper()
	var registries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_registries WHERE product_id='product-1'`).Scan(&registries); err != nil {
		t.Fatal(err)
	}
	if registries == 0 {
		seedCurrentWorkflowDomainFixture(t, s)
	}
}

// readOperatorQuestionFromWorkPinForTest reads the open operator question
// through its production owner, store.ReadWorkPin, so a journey sees the
// exact question the engine serves.
func readOperatorQuestionFromWorkPinForTest(ctx context.Context, s *store.Store, workID string) (*store.WorkflowOperatorQuestion, error) {
	pin, err := store.ReadWorkPin(ctx, s, workID)
	if err != nil {
		return nil, err
	}
	return pin.PendingOperatorDecision, nil
}

func workflowArchitectureBindingFixture() map[string]any {
	return map[string]any{
		"domain_registry_content_hash": pm1fixture.FixtureDomainRegistryContentHash,
		"home_domain_id":               pm1fixture.FixtureRootDomainID,
		"affected_domain_ids":          []string{pm1fixture.FixtureRootDomainID},
		"domain_modifies":              []string{},
		"domain_relation_modifies":     []any{},
		"law_additions":                []any{},
		"verification_obligations":     []any{},
	}
}

// workflowArchitectureBindingForSeededRegistry reads the committed
// registry's content hash and root Domain from the store, so a binding
// cannot drift from the registry the store validates against.
func workflowArchitectureBindingForSeededRegistry(t *testing.T, s *store.Store) map[string]any {
	t.Helper()
	var hash, root string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash,root_domain_id FROM domain_registries WHERE product_id='product-1'`).Scan(&hash, &root); err != nil {
		t.Fatal(err)
	}
	binding := workflowArchitectureBindingFixture()
	binding["domain_registry_content_hash"] = hash
	binding["home_domain_id"] = root
	binding["affected_domain_ids"] = []string{root}
	return binding
}

func addWorkflowContractApprovalFields(input map[string]any) {
	input["fields"] = workflowDecisionContractFieldsFixture()
}

// workflowDecisionContractFieldsFixture is the contract fixture declaring a
// Product decision: under consequence_v1 its approval and its premise
// confirmation require the operator's consent.
func workflowDecisionContractFieldsFixture() map[string]any {
	fields := workflowContractFieldsFixture()
	fields["decision_domains"] = []string{"product"}
	return fields
}

func workflowContractFieldsFixture() map[string]any {
	return map[string]any{
		"architecture_binding": workflowArchitectureBindingFixture(),
		"premise":              "The operator stated this premise.",
		"outcome_predicates":   []map[string]any{{"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check", "outcome_payload": map[string]any{"kind": "check", "check_ref": "check:fixture", "immutable_subject_ref": "commit:fixture", "expected_result": "pass"}}},
		"spec_mandate":         []string{},
		"law_modifies":         []string{},
		// The live heads answer consent under consequence_v1: the contract
		// declares its decision domains, here none, so it is engineering.
		"decision_domains": []string{},
	}
}

func workflowContractActionInput(t *testing.T, s *store.Store, key, approvalRef string) json.RawMessage {
	t.Helper()
	fields := workflowDecisionContractFieldsFixture()
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	return workflowContractActionInputWithFields(t, key, approvalRef, fields)
}

func workflowContractFieldsForSeededRegistry(t *testing.T, s *store.Store) map[string]any {
	t.Helper()
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	return fields
}

// workflowLegacyContractActionInput is the approval input for a pin under the
// legacy approval policy, whose contract declares no decision-domain fact.
func workflowLegacyContractActionInput(t *testing.T, key, approvalRef string) json.RawMessage {
	t.Helper()
	fields := workflowContractFieldsFixture()
	delete(fields, "decision_domains")
	return workflowContractActionInputWithFields(t, key, approvalRef, fields)
}

func workflowContractActionInputWithFields(t *testing.T, key, approvalRef string, fields map[string]any) json.RawMessage {
	t.Helper()
	input := map[string]any{
		"work_id": "work-1",
		// Proposal and alignment each record their document and completion.
		"expected_version": 8,
		"action_id":        "approve_contract",
		"fields":           fields,
		"idempotency_key":  key,
	}
	if approvalRef != "" {
		input["approval"] = map[string]any{"approval_ref": approvalRef}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// challengeApprovalBindings converts the challenge's consequence summary —
// the wire owner of the exact bindings — into the scope and version maps the
// host approval must sign. The approval validates equality against the
// challenge's canonical bindings, so a fixture that signs its own literals
// breaks whenever the admission binds a value the fixture guessed (the
// derived failure record's window epoch, for one).
func challengeApprovalBindings(t *testing.T, summary *ConsequenceSummary) (map[string]any, map[string]any) {
	t.Helper()
	if summary == nil {
		t.Fatal("challenge carries no consequence summary")
	}
	scope := map[string]any{}
	versions := map[string]any{}
	parseInt := func(text string) (int64, bool) {
		var value int64
		_, err := fmt.Sscanf(text, "%d", &value)
		return value, err == nil
	}
	for _, entry := range summary.Scope {
		key, value, found := strings.Cut(entry, ":")
		if !found {
			t.Fatalf("scope binding %q is not key:value", entry)
		}
		switch key {
		case "project_ids", "work_ids", "product_ids":
			existing, _ := scope[key].([]string)
			scope[key] = append(existing, value)
		default:
			scope[key] = value
		}
	}
	for _, entry := range summary.Versions {
		key, value, found := strings.Cut(entry, ":")
		if !found {
			t.Fatalf("version binding %q is not key:value", entry)
		}
		if number, ok := parseInt(value); ok {
			versions[key] = number
		} else {
			versions[key] = value
		}
	}
	return scope, versions
}

// advanceCapturedWorkToContract walks one captured implementation work item
// through the canonical pre-contract steps (proposal, alignment) so
// approve_contract is declared on the work's current step. Each action
// consumes two events; the walk returns the version approve_contract takes.
func advanceCapturedWorkToContract(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID, keyPrefix string) int64 {
	t.Helper()
	ctx := context.Background()
	dispatch := func(actionID string, fields map[string]any, key string) int64 {
		t.Helper()
		var version int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{"work_id": workID, "expected_version": version, "action_id": actionID, "fields": fields, "idempotency_key": keyPrefix + "-" + key})
		if err != nil {
			t.Fatal(err)
		}
		env.RequestID = "request:" + keyPrefix + "-" + key
		response := dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: raw}, env)
		if response.Outcome != OutcomeOK {
			t.Fatalf("pre-contract %s: %+v", actionID, response.Error)
		}
		var after int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		return after
	}
	dispatch("record_proposal", map[string]any{"problem": "The bounded problem statement.", "affected": []string{"The affected system."}, "stakes": "The bounded stakes statement.", "user_outcomes": []string{"The expected user outcome."}}, "proposal")
	return dispatch("record_alignment", map[string]any{"searched": "The bounded backlog search statement.", "outcome": "none_found"}, "alignment")
}

func recordApprovedWorkDesign(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID, key string) int64 {
	t.Helper()
	input := map[string]any{"work_id": workID, "expected_version": workVersion(t, s, workID), "action_id": "record_design", "idempotency_key": key,
		"fields": workflowDesignFieldsFixture(key)}
	spineOK(t, dispatchMutation(t, s, service, InvokeRequest{Tool: "concord_work_transition", Operation: "workflow_action", Input: retryJSON(input)}, env))
	return workVersion(t, s, workID)
}

func workflowDesignFieldsFixture(key string) map[string]any {
	return map[string]any{"approach": "The recorded approach is the implementation boundary.", "decisions": []map[string]any{{"id": "decision:" + key, "question": "What crosses into execution?", "choice": "The typed design record.", "rationale": "The worker must receive the approved decision.", "rejected": []string{}}}, "touched_refs": []string{"path:" + key}}
}

func TestArchitectureBindingSchemaPreservesCurrentIdentifierBounds(t *testing.T) {
	t.Parallel()
	domainID := strings.Repeat("d", 256)
	lawID := strings.Repeat("l", 256)
	obligationID := strings.Repeat("o", 256)
	binding := workflowArchitectureBindingFixture()
	binding["home_domain_id"] = domainID
	binding["affected_domain_ids"] = []string{domainID}
	binding["domain_modifies"] = []string{domainID}
	binding["law_additions"] = []map[string]any{{"law_id": lawID, "home_domain_id": domainID}}
	binding["verification_obligations"] = []map[string]any{{"law_id": lawID, "obligation_id": obligationID}}
	raw, err := json.Marshal(map[string]any{
		"work_id":          "work-1",
		"expected_version": 7,
		"action_id":        "approve_contract",
		"fields": map[string]any{
			"architecture_binding": binding,
			"premise":              "The operator stated this premise.",
			"outcome_predicates":   workflowContractFieldsFixture()["outcome_predicates"],
			"spec_mandate":         []string{lawID},
			"law_modifies":         []string{lawID},
		},
		"idempotency_key": "wide-architecture-ids",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("work_transition_action_input", raw); err != nil {
		t.Fatalf("current-width architecture identifiers rejected: %v", err)
	}
	for _, invalidDomainID := range []string{" " + domainID, domainID + " "} {
		invalid := workflowArchitectureBindingFixture()
		invalid["home_domain_id"] = invalidDomainID
		invalid["affected_domain_ids"] = []string{invalidDomainID}
		payload, err := json.Marshal(map[string]any{
			"work_id": "work-1", "expected_version": 7, "action_id": "approve_contract",
			"fields":          map[string]any{"architecture_binding": invalid, "premise": "The operator stated this premise.", "outcome_predicates": workflowContractFieldsFixture()["outcome_predicates"], "spec_mandate": []string{}, "law_modifies": []string{}},
			"idempotency_key": "invalid-domain-id",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := payloadschema.Validate("work_transition_action_input", payload); err == nil {
			t.Fatalf("Domain identifier with boundary whitespace accepted: %q", invalidDomainID)
		}
	}
}

func TestApproveContractSchemaAcceptsExplicitEvidenceAndRigor(t *testing.T) {
	t.Parallel()
	fields := workflowContractFieldsFixture()
	fields["required_evidence"] = []string{"verification", "review"}
	fields["rigor_class"] = "prototype_internal"
	payload := func(key string) []byte {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"work_id":          "work-1",
			"expected_version": 7,
			"action_id":        "approve_contract",
			"fields":           fields,
			"idempotency_key":  key,
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if err := payloadschema.Validate("work_transition_action_input", payload("complete-approve-contract-fields")); err != nil {
		t.Fatalf("approve_contract evidence or rigor rejected: %v", err)
	}

	fields["required_evidence"] = []string{"unknown"}
	if err := payloadschema.Validate("work_transition_action_input", payload("unknown-evidence-kind")); err == nil {
		t.Fatal("approve_contract schema accepted an unknown evidence kind")
	}
	fields["required_evidence"] = []string{"verification", "review"}
	fields["rigor_class"] = "unknown"
	if err := payloadschema.Validate("work_transition_action_input", payload("unknown-rigor-class")); err == nil {
		t.Fatal("approve_contract schema accepted an unknown rigor class")
	}
	fields["rigor_class"] = "prototype_internal"
	fields["unknown_contract_field"] = true
	if err := payloadschema.Validate("work_transition_action_input", payload("unknown-approve-contract-field")); err == nil {
		t.Fatal("approve_contract schema accepted an unknown field")
	}
}

// Consent fixtures resolve the current family, just like live work.
func legacyApprovalPolicyDefinitionForRef(t testing.TB) store.RegisteredDefinition {
	t.Helper()
	const ref = "workflow.implementation"
	live, err := store.LiveWorkflowDefinitionForRef(ref)
	if err != nil {
		t.Fatal(err)
	}
	return live
}

// seedLegacyApprovalPolicyWork pins work-1 to the newest implementation
// version under the legacy approval policy, for journeys whose operator
// substitution (CD-0116 verdicts) only that policy keeps. The live selector
// pins every public capture to the consequence_v1 head, so the fixture
// initializes the instance directly.
func seedLegacyApprovalPolicyWork(t *testing.T, s *store.Store, grant Authority) string {
	t.Helper()
	ctx := context.Background()
	registered := legacyApprovalPolicyDefinitionForRef(t)
	actor := store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: actor, Now: fixedTime()})
	}); err != nil {
		t.Fatal(err)
	}
	return "work-1"
}
