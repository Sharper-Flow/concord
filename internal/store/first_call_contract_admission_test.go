package store

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// First-call admission fixtures for approve_contract and supersede_contract
// (CON-412). Each valid call is assembled from the PUBLISHED closed action
// variant — the same artifact contracts/agent-tool-surface-payloads.schema.json
// host publication consumes — by walking the variant's own required set and
// bounds. Live values the schema cannot encode (the pinned definition's
// admitted predicate kinds and the WorkPin obligation membership) come from
// pinned-context reads. The assembled call then passes the real store
// admission on its first submission, and each invalid mutant is refused at
// its owning guard with the guard's own refusal text.

func firstCallPublishedDefs(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("../../contracts/agent-tool-surface-payloads.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Defs map[string]any `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Defs
}

func firstCallResolve(defs map[string]any, node map[string]any) map[string]any {
	resolved := node
	for {
		ref, ok := resolved["$ref"].(string)
		if !ok {
			return resolved
		}
		target, ok := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		if !ok {
			return resolved
		}
		resolved = target
	}
}

// firstCallSample builds one minimal value for a published property node
// from the node's own bounds: const and enum first, then typed minima, then
// pattern-derived strings. It reads no rule the schema does not state.
func firstCallSample(defs map[string]any, node map[string]any) any {
	node = firstCallResolve(defs, node)
	if value, ok := node["const"]; ok {
		return value
	}
	if enum, ok := node["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	kind, _ := node["type"].(string)
	switch kind {
	case "integer", "number":
		if minimum, ok := node["minimum"].(float64); ok {
			return int64(minimum)
		}
		return 1
	case "boolean":
		return false
	case "string":
		if format, _ := node["format"].(string); format == "date-time" {
			return "2026-10-08T00:00:00Z"
		}
		pattern, _ := node["pattern"].(string)
		switch {
		case strings.Contains(pattern, "sha256:"):
			return "sha256:" + strings.Repeat("0", 64)
		case strings.HasPrefix(pattern, "^msg:"):
			return "msg:" + strings.Repeat("0", 32)
		case strings.HasPrefix(pattern, "^https://"):
			return "https://example.test/pull/1"
		case strings.HasPrefix(pattern, "^predicate:"):
			return "predicate:first-call-sample"
		case strings.Contains(pattern, "[0-9a-f]{40}"):
			return strings.Repeat("0", 40)
		default:
			return "first-call"
		}
	case "array":
		if minimum, ok := node["minItems"].(float64); ok && minimum > 0 {
			items, _ := node["items"].(map[string]any)
			return []any{firstCallSample(defs, items)}
		}
		return []any{}
	case "object":
		return firstCallObject(defs, node)
	}
	return nil
}

// firstCallObject assembles the minimal object a published object node
// admits: every required property, sampled from its own schema.
func firstCallObject(defs map[string]any, node map[string]any) map[string]any {
	node = firstCallResolve(defs, node)
	properties, _ := node["properties"].(map[string]any)
	object := map[string]any{}
	for _, name := range firstCallRequired(node) {
		if property, ok := properties[name].(map[string]any); ok {
			object[name] = firstCallSample(defs, property)
		}
	}
	return object
}

func firstCallRequired(node map[string]any) []string {
	required, _ := node["required"].([]any)
	names := make([]string, 0, len(required))
	for _, name := range required {
		if text, ok := name.(string); ok {
			names = append(names, text)
		}
	}
	return names
}

// firstCallActionInput assembles one complete workflow action input from the
// published closed variant: the envelope requireds with the variant's own
// action_id const, plus the fields object the variant declares. Overrides
// carry the live pinned-context values no schema range can encode.
func firstCallActionInput(t *testing.T, defs map[string]any, variant string, workID string, version int64, overrides map[string]any) map[string]any {
	t.Helper()
	node := firstCallResolve(defs, map[string]any{"$ref": "#/$defs/" + variant})
	properties, _ := node["properties"].(map[string]any)
	input := map[string]any{}
	for _, name := range firstCallRequired(node) {
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("published variant %s requires unknown property %s", variant, name)
		}
		if name == "fields" {
			// A fields object the registry's cross-field declarations split
			// into branches samples its first branch; crossFieldSampleObject
			// follows oneOf and $ref-with-siblings exactly as the validators
			// read them.
			fields := crossFieldSampleObject(defs, property)
			for key, value := range overrides {
				fields[key] = value
			}
			input[name] = fields
			continue
		}
		input[name] = firstCallSample(defs, property)
	}
	input["work_id"] = workID
	input["expected_version"] = version
	return input
}

// firstCallPredicates builds the outcome predicates from the pinned
// definition's admitted predicate kinds and, for outcome-kind definitions,
// the pinned outcome tokens: one predicate at the zero position, its ordinal
// equal to that position. The payload shape follows the closed v1 union the
// fold decodes: check carries check fields, outcome carries its token.
func firstCallPredicates(definition WorkflowDefinition) []map[string]any {
	admitted := map[PredicateKind]bool{}
	for _, kind := range definition.OutcomeSchema.AllowedKinds {
		admitted[kind] = true
	}
	switch {
	case admitted[PredicateCheck]:
		return []map[string]any{{
			"predicate_id": "predicate:first-call-admission", "ordinal": 0, "outcome_kind": string(PredicateCheck),
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:first-call-admission", "immutable_subject_ref": "commit:" + strings.Repeat("0", 40), "expected_result": "pass"},
		}}
	case admitted[PredicateOutcome]:
		if len(definition.OutcomeSchema.AllowedOutcomeTokens) == 0 {
			return nil
		}
		return []map[string]any{{
			"predicate_id": "predicate:first-call-admission", "ordinal": 0, "outcome_kind": string(PredicateOutcome),
			"outcome_payload": map[string]any{"kind": "outcome", "allowed": []string{definition.OutcomeSchema.AllowedOutcomeTokens[0]}},
		}}
	default:
		return nil
	}
}

// firstCallImplementationDefinition returns the registered implementation
// definition. approve_contract sits on its planning step, so a first call
// needs the proposal, discovery, and design actions first; the definition is
// Product-changing, so the published architecture_binding fields are part of
// the legal call the variant must carry.
func firstCallImplementationDefinition(t *testing.T) RegisteredDefinition {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

// seedFirstCallWorkflow seeds the Product-changing fixture owners: the law
// home (seedWorkflowLaw), the root Domain registry
// (seedIssue31DomainRegistry), and the law-domain home row the obligation
// guard's current-law lookup joins against, seeded under an open fold like
// seedLawContextFixture seeds it. It then initializes a real instance of the
// named workflow, pinned to the current registered definition.
func seedFirstCallWorkflow(t *testing.T, s *Store, workID, workflowRef, actorName string) WorkflowActor {
	t.Helper()
	ctx := context.Background()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedIssue31DomainRegistry(t, s)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO law_domain_homes(home_project_id,home_locator_id,law_id,product_id,domain_id,law_content_hash,scanned_commit_oid) SELECT 'project','workflow-law-locator','spec:one','product','root',content_hash,'test' FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	actor := WorkflowActor{PrincipalRef: "principal:" + actorName, ClientRef: "client:" + actorName, AgentRef: "agent:" + actorName, SessionRef: "session:" + actorName, ActorClass: ActorAgent}
	definition, err := BuiltinWorkflowDefinitionForRef(workflowRef)
	if err != nil {
		t.Fatal(err)
	}
	initTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, initTx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: actor, Now: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}); err != nil {
		initTx.Rollback()
		t.Fatal(err)
	}
	if err := initTx.Commit(); err != nil {
		t.Fatal(err)
	}
	return actor
}

func firstCallApproveContract(t *testing.T, s *Store, workID string, version int64, operationID string, input map[string]any) error {
	t.Helper()
	payload, err := json.Marshal(input["fields"])
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(context.Background(), tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: payload, Actor: stepFixtureActor(),
		AcceptedInputsDigest: "sha256:first-call", IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: stepFixtureActor().PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
		RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC),
	})
	if actionErr != nil {
		_ = tx.Rollback()
		return actionErr
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return nil
}

func firstCallRequireRefusal(t *testing.T, err error, fragment string) {
	t.Helper()
	if err == nil {
		t.Fatalf("mutant %s was admitted", fragment)
	}
	var failure *Failure
	if !failureAs(err, &failure) {
		t.Fatalf("mutant refusal is not typed: %v", err)
	}
	if !strings.Contains(failure.Detail, fragment) {
		t.Fatalf("mutant refusal detail = %q, want the owning guard text %q", failure.Detail, fragment)
	}
}

func TestFirstCallApproveContractAdmitsFromPublishedVariant(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "work-first-call-approve"
	actor := seedFirstCallWorkflow(t, s, workID, "workflow.implementation", "first-call")
	registered := firstCallImplementationDefinition(t)
	definition := registered.Definition
	if definition.ChangesProductTruth == nil || !*definition.ChangesProductTruth {
		t.Fatal("the pinned implementation definition is not Product-changing; the fixture cannot exercise the binding guards")
	}

	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	// Advance to the planning step through the same real action owners the
	// semantic-event test uses; these calls are scaffolding, not the surface
	// under proof.
	for _, action := range []string{"record_proposal", "record_alignment", "record_discovery", "record_design"} {
		version = issue31WorkflowAction(t, s, workID, version, action, "first-call-"+action, actor)
	}

	// Pinned-context read: the WorkPin publishes the exact obligation
	// membership the pinned definition declares, and its definition identity
	// verifies against the registry. The verification obligation below is
	// taken from that exact verified list, and the obligation mutant is
	// refused against the same collector's membership rule.
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pin.Obligations) == 0 {
		t.Fatal("pinned definition declares no obligations; the fixture cannot prove declared-obligation membership")
	}
	if err := BuiltinWorkflowRegistry().Verify(definition.Ref, pin.WorkflowDefinitionVersion, pin.WorkflowDefinitionDigest); err != nil {
		t.Fatalf("pinned definition identity does not verify: %v", err)
	}
	pinnedObligation := pin.Obligations[0]

	defs := firstCallPublishedDefs(t)
	// The legal binding: the added law is authorized by spec_mandate, every
	// Domain names the seeded registry, and the verification obligation pairs
	// the pinned obligation ID with the current pinned law spec:one.
	legalBinding := func() map[string]any {
		return map[string]any{
			"domain_registry_content_hash": "sha256:" + strings.Repeat("b", 64),
			"home_domain_id":               "root",
			"affected_domain_ids":          []string{"root"},
			"domain_modifies":              []string{},
			"domain_relation_modifies":     []map[string]any{},
			"law_additions":                []map[string]any{{"law_id": "law:new", "home_domain_id": "root"}},
			"verification_obligations":     []map[string]any{{"law_id": "spec:one", "obligation_id": pinnedObligation}},
		}
	}
	input := firstCallActionInput(t, defs, "work_transition_action_variant_approve_contract", workID, version, map[string]any{
		"outcome_predicates":   firstCallPredicates(definition),
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": legalBinding(),
	})
	// The assembled call admits against the published variant first: the
	// same bytes validate at the published boundary and at the store.
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("work_transition_action_variant_approve_contract", payload); err != nil {
		t.Fatalf("published variant refused the assembled first call: %v", err)
	}

	// Mutants run first, at the same pinned state: each is refused at its
	// owning guard with no durable effect, and the unchanged state then
	// admits the valid call on its first submission.

	// Positional ordinal mutant: the predicate at position 0 declares
	// ordinal 1. The owning guard is validateWorkflowContractPredicate.
	ordinalMutant := firstCallActionInput(t, defs, "work_transition_action_variant_approve_contract", workID, version, map[string]any{
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:first-call-ordinal", "ordinal": 1, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:ordinal", "immutable_subject_ref": "commit:ordinal", "expected_result": "pass"},
		}},
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": legalBinding(),
	})
	mutantErr := firstCallApproveContract(t, s, workID, version, "first-call-approve-ordinal", ordinalMutant)
	firstCallRequireRefusal(t, mutantErr, "declares ordinal 1 at position 0")

	// Unauthorized-addition mutant: the addition is not in spec_mandate. The
	// owning guard is validateArchitectureBindingLawAdditionsTx.
	unauthorizedBinding := legalBinding()
	unauthorizedBinding["law_additions"] = []map[string]any{{"law_id": "law:unauthorized", "home_domain_id": "root"}}
	unauthorizedMutant := firstCallActionInput(t, defs, "work_transition_action_variant_approve_contract", workID, version, map[string]any{
		"outcome_predicates":   firstCallPredicates(definition),
		"spec_mandate":         []string{"spec:one"},
		"architecture_binding": unauthorizedBinding,
	})
	mutantErr = firstCallApproveContract(t, s, workID, version, "first-call-approve-unauthorized", unauthorizedMutant)
	firstCallRequireRefusal(t, mutantErr, "law addition is outside spec_mandate")

	// Undeclared-obligation mutant: the verification obligation names an ID
	// the pinned workflow definition does not declare at root, step, or
	// rigor level. The owning guard is
	// validateArchitectureBindingObligationsTx against the pinned
	// definition's obligation declarations.
	undeclaredBinding := legalBinding()
	undeclaredBinding["verification_obligations"] = []map[string]any{{"law_id": "spec:one", "obligation_id": "undeclared-obligation"}}
	undeclaredMutant := firstCallActionInput(t, defs, "work_transition_action_variant_approve_contract", workID, version, map[string]any{
		"outcome_predicates":   firstCallPredicates(definition),
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": undeclaredBinding,
	})
	mutantErr = firstCallApproveContract(t, s, workID, version, "first-call-approve-undeclared", undeclaredMutant)
	firstCallRequireRefusal(t, mutantErr, "verification obligation is not declared by the pinned workflow definition")

	// Affected-Domain membership mutant: affected_domain_ids names a Domain
	// the pinned registry does not declare. The owning guard is
	// validateArchitectureBindingDomainsTx.
	unknownDomainBinding := legalBinding()
	unknownDomainBinding["affected_domain_ids"] = []string{"root", "ghost"}
	unknownDomainMutant := firstCallActionInput(t, defs, "work_transition_action_variant_approve_contract", workID, version, map[string]any{
		"outcome_predicates":   firstCallPredicates(definition),
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": unknownDomainBinding,
	})
	mutantErr = firstCallApproveContract(t, s, workID, version, "first-call-approve-domain", unknownDomainMutant)
	firstCallRequireRefusal(t, mutantErr, "architecture binding names an unknown Domain: ghost")

	// Law-modification subset mutant: law_modifies names a law outside
	// spec_mandate. The owning guard is validateLawModificationSubset.
	subsetMutant := firstCallActionInput(t, defs, "work_transition_action_variant_approve_contract", workID, version, map[string]any{
		"outcome_predicates":   firstCallPredicates(definition),
		"spec_mandate":         []string{"spec:one"},
		"law_modifies":         []string{"spec:unmandated"},
		"architecture_binding": legalBinding(),
	})
	mutantErr = firstCallApproveContract(t, s, workID, version, "first-call-approve-subset", subsetMutant)
	firstCallRequireRefusal(t, mutantErr, "law_modifies must be a subset of spec_mandate")

	// Nested kind-equality mutant (CON-412): the predicate declares
	// outcome_kind exists with a check outcome_payload. The owning guard is
	// the per-kind item contract the publication and payload validation
	// share (validateWorkflowPayloadSchema against the generated items def);
	// the fold keeps its deeper DecodeWorkflowPredicate refusal.
	kindMismatchMutant := firstCallActionInput(t, defs, "work_transition_action_variant_approve_contract", workID, version, map[string]any{
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:first-call-kind", "ordinal": 0, "outcome_kind": "exists",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:kind-mismatch", "immutable_subject_ref": "commit:kind-mismatch", "expected_result": "pass"},
		}},
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": legalBinding(),
	})
	kindMismatchEncoded, err := json.Marshal(kindMismatchMutant)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("work_transition_action_variant_approve_contract", kindMismatchEncoded); err == nil {
		t.Fatal("published approve variant admitted an outcome_predicates item whose outcome_kind and outcome_payload.kind disagree")
	}
	mutantErr = firstCallApproveContract(t, s, workID, version, "first-call-approve-kind", kindMismatchMutant)
	firstCallRequireRefusal(t, mutantErr, `does not satisfy its declared schema "workflow_action_outcome_predicates"`)

	if err := firstCallApproveContract(t, s, workID, version, "first-call-approve", input); err != nil {
		t.Fatalf("first-call approve_contract refused after the mutants were refused without effect: %v", err)
	}
}

// firstCallBreakFixSeed drives the real break-fix admission path to a state
// one supersede away from an approved Product-changing contract: the
// alignment step records the legal none_found combination the published
// variant admits, and the approve step assembles the same Product-changing
// binding the approve first-call test proves.
func firstCallBreakFixSeed(t *testing.T, workID string) (*Store, WorkflowActor, WorkflowActor) {
	t.Helper()
	s := openTemp(t)
	actor := seedFirstCallWorkflow(t, s, workID, "workflow.break_fix", "first-call-bf")
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	if registered.Definition.ChangesProductTruth == nil || !*registered.Definition.ChangesProductTruth {
		t.Fatal("the pinned break-fix definition is not Product-changing; the fixture cannot exercise the binding guards")
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"record_reproduction", "record_alignment", "record_root_cause"} {
		version = issue31WorkflowAction(t, s, workID, version, action, "first-call-bf-"+action, actor)
	}
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pin.Obligations) == 0 {
		t.Fatal("pinned break-fix definition declares no obligations")
	}
	if err := BuiltinWorkflowRegistry().Verify(registered.Definition.Ref, pin.WorkflowDefinitionVersion, pin.WorkflowDefinitionDigest); err != nil {
		t.Fatalf("pinned break-fix definition identity does not verify: %v", err)
	}
	defs := firstCallPublishedDefs(t)
	binding := func() map[string]any {
		return map[string]any{
			"domain_registry_content_hash": "sha256:" + strings.Repeat("b", 64),
			"home_domain_id":               "root",
			"affected_domain_ids":          []string{"root"},
			"domain_modifies":              []string{},
			"domain_relation_modifies":     []map[string]any{},
			"law_additions":                []map[string]any{{"law_id": "law:new", "home_domain_id": "root"}},
			"verification_obligations":     []map[string]any{{"law_id": "spec:one", "obligation_id": pin.Obligations[0]}},
		}
	}
	approve := firstCallActionInput(t, defs, "work_transition_action_variant_approve_contract", workID, version, map[string]any{
		"outcome_predicates":   firstCallPredicates(registered.Definition),
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": binding(),
	})
	if err := firstCallApproveContract(t, s, workID, version, "first-call-bf-approve", approve); err != nil {
		t.Fatalf("first-call break-fix approve_contract refused: %v", err)
	}
	operator := WorkflowActor{PrincipalRef: "principal:first-call-bf-op", ClientRef: "client:first-call-bf", AgentRef: "agent:first-call-bf-op", SessionRef: "session:first-call-bf-op", ActorClass: ActorOperator}
	return s, actor, operator
}

func TestFirstCallSupersedeContractAdmitsFromPublishedVariant(t *testing.T) {
	t.Parallel()
	const workID = "work-first-call-supersede"
	s, owner, operator := firstCallBreakFixSeed(t, workID)
	defer s.Close()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	// Pinned-context read: the successor's required evidence and obligation
	// come from the exact verified WorkPin the continuity read publishes,
	// never from the collector the pin derives from.
	pin, err := ReadWorkPin(context.Background(), s, workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := BuiltinWorkflowRegistry().Verify(registered.Definition.Ref, pin.WorkflowDefinitionVersion, pin.WorkflowDefinitionDigest); err != nil {
		t.Fatalf("pinned break-fix definition identity does not verify: %v", err)
	}
	defs := firstCallPublishedDefs(t)
	successorBinding := func() map[string]any {
		return map[string]any{
			"domain_registry_content_hash": "sha256:" + strings.Repeat("b", 64),
			"home_domain_id":               "root",
			"affected_domain_ids":          []string{"root"},
			"domain_modifies":              []string{},
			"domain_relation_modifies":     []map[string]any{},
			"law_additions":                []map[string]any{{"law_id": "law:successor", "home_domain_id": "root"}},
			"verification_obligations":     []map[string]any{{"law_id": "spec:one", "obligation_id": pin.Obligations[0]}},
		}
	}
	supersedeInput := func(mutate func(fields map[string]any)) map[string]any {
		overrides := map[string]any{
			"contract_version": 2,
			"outcome_predicates": []map[string]any{{
				"predicate_id": "predicate:first-call-supersede", "ordinal": 0, "outcome_kind": "check",
				"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:first-call-supersede", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
			}},
			"required_evidence":    pin.Obligations,
			"spec_mandate":         []string{"spec:one", "law:successor"},
			"architecture_binding": successorBinding(),
		}
		input := firstCallActionInput(t, defs, "work_transition_action_variant_supersede_contract", workID, pin.Version, overrides)
		if mutate != nil {
			mutate(input["fields"].(map[string]any))
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := payloadschema.Validate("work_transition_action_variant_supersede_contract", encoded); err != nil {
			t.Fatalf("published supersede variant refused the assembled successor: %v", err)
		}
		return input
	}
	fieldsOf := func(input map[string]any) json.RawMessage {
		encoded, err := json.Marshal(input["fields"])
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}

	// Positional ordinal mutant: the successor predicate at position 0
	// declares ordinal 1.
	ordinal := supersedeInput(func(fields map[string]any) {
		predicates := fields["outcome_predicates"].([]map[string]any)
		predicates[0]["ordinal"] = 1
	})
	err = runIssue933OperatorAction(t, s, workID, "supersede_contract", fieldsOf(ordinal), owner, operator)
	firstCallRequireRefusal(t, err, "declares ordinal 1 at position 0")

	// Unauthorized-addition mutant: the successor addition sits outside
	// spec_mandate while every mandated law stays accepted, so the refusal
	// is the addition-subset guard, not the mandate guard.
	unauthorized := supersedeInput(func(fields map[string]any) {
		fields["spec_mandate"] = []string{"spec:one"}
		binding := fields["architecture_binding"].(map[string]any)
		binding["law_additions"] = []map[string]any{{"law_id": "law:unauthorized", "home_domain_id": "root"}}
	})
	err = runIssue933OperatorAction(t, s, workID, "supersede_contract", fieldsOf(unauthorized), owner, operator)
	firstCallRequireRefusal(t, err, "law addition is outside spec_mandate")

	// Undeclared-obligation mutant: the successor verification obligation
	// names an ID the pinned workflow definition does not declare.
	undeclared := supersedeInput(func(fields map[string]any) {
		binding := fields["architecture_binding"].(map[string]any)
		binding["verification_obligations"] = []map[string]any{{"law_id": "spec:one", "obligation_id": "undeclared-obligation"}}
	})
	err = runIssue933OperatorAction(t, s, workID, "supersede_contract", fieldsOf(undeclared), owner, operator)
	firstCallRequireRefusal(t, err, "verification obligation is not declared by the pinned workflow definition")

	// Affected-Domain membership mutant: the successor binding names a
	// Domain the pinned registry does not declare.
	unknownDomain := supersedeInput(func(fields map[string]any) {
		binding := fields["architecture_binding"].(map[string]any)
		binding["affected_domain_ids"] = []string{"root", "ghost"}
	})
	err = runIssue933OperatorAction(t, s, workID, "supersede_contract", fieldsOf(unknownDomain), owner, operator)
	firstCallRequireRefusal(t, err, "architecture binding names an unknown Domain: ghost")

	// Version mutant: the successor version does not immediately follow the
	// active contract.
	versionSkip := supersedeInput(func(fields map[string]any) {
		fields["contract_version"] = 4
	})
	err = runIssue933OperatorAction(t, s, workID, "supersede_contract", fieldsOf(versionSkip), owner, operator)
	firstCallRequireRefusal(t, err, "immediately follow")

	// Nested kind-equality mutants (CON-412): outcome_kind and
	// outcome_payload.kind are payload-only values that must hold one
	// value. The equality is declared in the engine cross-field
	// kind-match (workflowActionCrossField for supersede_contract) and the
	// published items and pair branches close per kind, so the publication
	// refuses first and whole-store admission refuses at payload
	// validation.

	// Item mismatch: outcome_kind exists beside a check outcome_payload.
	// Built outside supersedeInput because the published per-kind items
	// refuse it — that refusal is the assertion.
	itemMismatch := firstCallActionInput(t, defs, "work_transition_action_variant_supersede_contract", workID, pin.Version, map[string]any{
		"contract_version": 2,
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:first-call-supersede", "ordinal": 0, "outcome_kind": "exists",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:first-call-supersede", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence":    pin.Obligations,
		"spec_mandate":         []string{"spec:one", "law:successor"},
		"architecture_binding": successorBinding(),
	})
	if err := payloadschema.Validate("work_transition_action_variant_supersede_contract", mustJSONRaw(t, itemMismatch)); err == nil {
		t.Fatal("published supersede variant admitted an outcome_predicates item whose outcome_kind and outcome_payload.kind disagree")
	}
	err = runIssue933OperatorAction(t, s, workID, "supersede_contract", fieldsOf(itemMismatch), owner, operator)
	firstCallRequireRefusal(t, err, `does not satisfy its declared schema "workflow_action_outcome_predicates"`)

	// Pair mismatch: the legacy outcome pair group with outcome_kind exists
	// beside a check outcome_payload. The engine kind-match declaration
	// owns the refusal; the published pair branches close per kind.
	pairMismatch := firstCallActionInput(t, defs, "work_transition_action_variant_supersede_contract", workID, pin.Version, map[string]any{
		"contract_version":     2,
		"required_evidence":    pin.Obligations,
		"spec_mandate":         []string{"spec:one", "law:successor"},
		"architecture_binding": successorBinding(),
	})
	pairFields := pairMismatch["fields"].(map[string]any)
	delete(pairFields, "outcome_predicates")
	pairFields["outcome_kind"] = "exists"
	pairFields["outcome_payload"] = map[string]any{"kind": "check", "check_ref": "check:pair-mismatch", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}
	if err := payloadschema.Validate("work_transition_action_variant_supersede_contract", mustJSONRaw(t, pairMismatch)); err == nil {
		t.Fatal("published supersede variant admitted an outcome pair whose outcome_kind and outcome_payload.kind disagree")
	}
	err = runIssue933OperatorAction(t, s, workID, "supersede_contract", fieldsOf(pairMismatch), owner, operator)
	firstCallRequireRefusal(t, err, `carries kind "check"`)

	// The unchanged successor admits on its first submission after every
	// mutant was refused without effect.
	valid := supersedeInput(nil)
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", fieldsOf(valid), owner, operator); err != nil {
		t.Fatalf("first-call supersede_contract refused after the mutants were refused without effect: %v", err)
	}
	var successorVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&successorVersion); err != nil || successorVersion != 2 {
		t.Fatalf("active successor contract version=%d err=%v, want 2", successorVersion, err)
	}

	// Valid per-kind pair fixture (CON-412): the outcome pair group with
	// matching kinds — outcome_kind check beside a check outcome_payload —
	// is a legal successor form. The published per-kind pair branch admits
	// it, and whole-store admission records the supersession.
	pairValid := firstCallActionInput(t, defs, "work_transition_action_variant_supersede_contract", workID, verdictItemVersion(t, s, workID), map[string]any{
		"contract_version":     3,
		"required_evidence":    pin.Obligations,
		"spec_mandate":         []string{"spec:one", "law:successor"},
		"architecture_binding": successorBinding(),
	})
	pairValidFields := pairValid["fields"].(map[string]any)
	delete(pairValidFields, "outcome_predicates")
	pairValidFields["outcome_kind"] = "check"
	pairValidFields["outcome_payload"] = map[string]any{"kind": "check", "check_ref": "check:pair-valid", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}
	if err := payloadschema.Validate("work_transition_action_variant_supersede_contract", mustJSONRaw(t, pairValid)); err != nil {
		t.Fatalf("published supersede variant refused the per-kind pair successor: %v", err)
	}
	if err := runIssue933OperatorAction(t, s, workID, "supersede_contract", fieldsOf(pairValid), owner, operator); err != nil {
		t.Fatalf("first-call per-kind pair supersede_contract refused: %v", err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&successorVersion); err != nil || successorVersion != 3 {
		t.Fatalf("active pair-form successor contract version=%d err=%v, want 3", successorVersion, err)
	}
}
