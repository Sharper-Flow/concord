package store

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// First-call admission fixtures for approve_contract and revise_intent.contract.
// Each valid call is assembled from the published closed input variant. The
// contract names its legislative subjects and architecture obligations.

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

// firstCallActionInput assembles one complete approve_contract input from the
// published closed variant: the envelope requireds with the variant's own
// action_id const, plus the fields object the variant declares. Overrides
// carry the live pinned-context values no schema range can encode.
func firstCallActionInput(t *testing.T, defs map[string]any, workID string, version int64, overrides map[string]any) map[string]any {
	t.Helper()
	const variant = "work_transition_action_variant_approve_contract"
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
			// Every fixture here pins a consequence_v1 live head, whose
			// contract actions carry the explicit engineering decision
			// fact the published consequence_v1 branch requires.
			fields["decision_domains"] = []string{}
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

func firstCallApprovalFrontier(t *testing.T, planType WorkflowStepPlanType) string {
	t.Helper()
	for _, step := range currentWorkflowPlanFixture(t, planType).Steps {
		if slices.Contains(WorkflowStepPlanTemplateActions(planType, step.ID), "approve_contract") {
			return step.ID
		}
	}
	t.Fatalf("plan type %s has no contract approval step", planType)
	return ""
}

// seedFirstCallWorkflow initializes intake and seeds the contract-approval
// frontier of the selected Product-changing plan.
func seedFirstCallWorkflow(t *testing.T, s *Store, workID, actorName string, planType WorkflowStepPlanType) WorkflowActor {
	t.Helper()
	ctx := context.Background()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	actor := WorkflowActor{PrincipalRef: "principal:" + actorName, ClientRef: "client:" + actorName, AgentRef: "agent:" + actorName, SessionRef: "session:" + actorName, ActorClass: ActorAgent}
	ctx, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	initTx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(ctx, initTx, WorkflowInitializationRequest{WorkID: workID, Actor: actor, Now: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}); err != nil {
		initTx.Rollback()
		t.Fatal(err)
	}
	if err := initTx.Commit(); err != nil {
		t.Fatal(err)
	}
	seedWorkflowPlanFrontier(t, s, workID, planType, firstCallApprovalFrontier(t, planType))
	return actor
}

func firstCallApproveContract(t *testing.T, s *Store, workID string, version int64, operationID string, actor WorkflowActor, input map[string]any) error {
	t.Helper()
	payload, err := json.Marshal(input["fields"])
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := s.EstablishKnowledgeSourceSetProof(context.Background(), workID)
	if err != nil {
		return err
	}
	before := contractRecordEffectSnapshot(t, s, workID)
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, actionErr := applyWorkflowActionRawTx(ctx, tx, newWriteScope(tx), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: payload, Actor: actor,
		AcceptedInputsDigest: "sha256:first-call", IdempotencyIdentity: operationID, OperationID: operationID,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
		RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC),
	})
	if actionErr != nil {
		_ = tx.Rollback()
		if after := contractRecordEffectSnapshot(t, s, workID); after != before {
			t.Fatalf("refused approve_contract changed durable state: before=%s after=%s", before, after)
		}
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
	actor := seedFirstCallWorkflow(t, s, workID, "first-call", WorkflowStepPlanChange)
	const obligation = "verification"
	version := readWorkVersion(t, s, workID)

	defs := firstCallPublishedDefs(t)
	// The legal binding: the added law is authorized by spec_mandate, every
	// Domain names the seeded registry, and the verification obligation pairs
	// an obligation with the current law spec:one.
	legalBinding := func() map[string]any {
		return map[string]any{
			"home_domain_id":           "root",
			"affected_domain_ids":      []string{"root"},
			"domain_modifies":          []string{},
			"domain_relation_modifies": []map[string]any{},
			"law_additions":            []map[string]any{{"law_id": "law:new", "home_domain_id": "root"}},
			"verification_obligations": []map[string]any{{"law_id": "spec:one", "obligation_id": obligation}},
		}
	}
	input := firstCallActionInput(t, defs, workID, version, map[string]any{
		"legislative_subjects": []string{},
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

	// Unauthorized-addition mutant: the addition is not in spec_mandate. The
	// owning guard is validateArchitectureBindingLawAdditionsTx.
	unauthorizedBinding := legalBinding()
	unauthorizedBinding["law_additions"] = []map[string]any{{"law_id": "law:unauthorized", "home_domain_id": "root"}}
	unauthorizedMutant := firstCallActionInput(t, defs, workID, version, map[string]any{
		"legislative_subjects": []string{},
		"spec_mandate":         []string{"spec:one"},
		"architecture_binding": unauthorizedBinding,
	})
	mutantErr := firstCallApproveContract(t, s, workID, version, "first-call-approve-unauthorized", actor, unauthorizedMutant)
	firstCallRequireRefusal(t, mutantErr, "law addition is outside spec_mandate")

	// Affected-Domain membership mutant: affected_domain_ids names a Domain
	// the pinned registry does not declare. The owning guard is
	// validateArchitectureBindingDomainsTx.
	unknownDomainBinding := legalBinding()
	unknownDomainBinding["affected_domain_ids"] = []string{"root", "ghost"}
	unknownDomainMutant := firstCallActionInput(t, defs, workID, version, map[string]any{
		"legislative_subjects": []string{},
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": unknownDomainBinding,
	})
	mutantErr = firstCallApproveContract(t, s, workID, version, "first-call-approve-domain", actor, unknownDomainMutant)
	firstCallRequireRefusal(t, mutantErr, "architecture_binding.affected_domain_ids names an unknown Domain: ghost")

	// Law-modification subset mutant: law_modifies names a law outside
	// spec_mandate. The owning guard is validateLawModificationSubset.
	subsetMutant := firstCallActionInput(t, defs, workID, version, map[string]any{
		"legislative_subjects": []string{},
		"spec_mandate":         []string{"spec:one"},
		"law_modifies":         []string{"spec:unmandated"},
		"architecture_binding": legalBinding(),
	})
	mutantErr = firstCallApproveContract(t, s, workID, version, "first-call-approve-subset", actor, subsetMutant)
	firstCallRequireRefusal(t, mutantErr, "law_modifies must be a subset of spec_mandate")

	if err := firstCallApproveContract(t, s, workID, version, "first-call-approve", actor, input); err != nil {
		t.Fatalf("first-call approve_contract refused after the mutants were refused without effect: %v", err)
	}
}

// firstCallBreakFixSeed approves a Product-changing contract at the fix plan frontier.
func firstCallBreakFixSeed(t *testing.T, workID string) (*Store, WorkflowActor) {
	t.Helper()
	s := openTemp(t)
	actor := seedFirstCallWorkflow(t, s, workID, "first-call-bf", WorkflowStepPlanFix)
	version := readWorkVersion(t, s, workID)
	defs := firstCallPublishedDefs(t)
	binding := func() map[string]any {
		return map[string]any{
			"home_domain_id":           "root",
			"affected_domain_ids":      []string{"root"},
			"domain_modifies":          []string{},
			"domain_relation_modifies": []map[string]any{},
			"law_additions":            []map[string]any{{"law_id": "law:new", "home_domain_id": "root"}},
			"verification_obligations": []map[string]any{{"law_id": "spec:one", "obligation_id": "verification"}},
		}
	}
	approve := firstCallActionInput(t, defs, workID, version, map[string]any{
		"legislative_subjects": []string{},
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": binding(),
	})
	if err := firstCallApproveContract(t, s, workID, version, "first-call-bf-approve", actor, approve); err != nil {
		t.Fatalf("first-call break-fix approve_contract refused: %v", err)
	}
	return s, actor
}

func TestFirstCallContractUpdateAdmitsFromPublishedInput(t *testing.T) {
	t.Parallel()
	const workID = "work-first-call-update"
	s, owner := firstCallBreakFixSeed(t, workID)
	defer s.Close()
	var err error
	defs := firstCallPublishedDefs(t)
	var workKind string
	if err := s.db.QueryRow(`SELECT kind FROM work_items WHERE id=?`, workID).Scan(&workKind); err != nil {
		t.Fatal(err)
	}
	successorBinding := func() map[string]any {
		return map[string]any{
			"home_domain_id":           "root",
			"affected_domain_ids":      []string{"root"},
			"domain_modifies":          []string{},
			"domain_relation_modifies": []map[string]any{},
			"law_additions":            []map[string]any{{"law_id": "law:successor", "home_domain_id": "root"}},
			"verification_obligations": []map[string]any{{"law_id": "spec:one", "obligation_id": "verification"}},
		}
	}
	updateInput := func(mutate func(fields map[string]any)) map[string]any {
		overrides := map[string]any{
			"contract_version":     2,
			"legislative_subjects": []string{},
			"spec_mandate":         []string{"spec:one", "law:successor"},
			"architecture_binding": successorBinding(),
		}
		input := firstCallObject(defs, firstCallResolve(defs, map[string]any{"$ref": "#/$defs/work_define_revise_input"}))
		fields := firstCallObject(defs, firstCallResolve(defs, map[string]any{"$ref": "#/$defs/workflow_contract_update"}))
		fields["decision_domains"] = []string{}
		for key, value := range overrides {
			fields[key] = value
		}
		input["work_id"], input["expected_version"], input["contract"] = workID, readWorkVersion(t, s, workID), fields
		input["kind"] = workKind
		if mutate != nil {
			mutate(fields)
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := payloadschema.Validate("work_define_revise_input", encoded); err != nil {
			t.Fatalf("published update input refused the assembled contract: %v", err)
		}
		return input
	}
	applyUpdate := func(input map[string]any) error {
		before := contractRecordEffectSnapshot(t, s, workID)
		err := reviseContractRecord(t, s, workID, owner, input["contract"].(map[string]any), readWorkVersion(t, s, workID))
		if err != nil && contractRecordEffectSnapshot(t, s, workID) != before {
			t.Fatal("refused first-call update changed durable state")
		}
		return err
	}
	withoutContract := updateInput(nil)
	delete(withoutContract, "contract")
	if err := payloadschema.Validate("work_define_revise_input", mustJSONRaw(t, withoutContract)); err != nil {
		t.Fatalf("contract is not optional in the published update input: %v", err)
	}

	// Unauthorized-addition mutant: the successor addition sits outside
	// spec_mandate while every mandated law stays accepted, so the refusal
	// is the addition-subset guard, not the mandate guard.
	unauthorized := updateInput(func(fields map[string]any) {
		fields["spec_mandate"] = []string{"spec:one"}
		binding := fields["architecture_binding"].(map[string]any)
		binding["law_additions"] = []map[string]any{{"law_id": "law:unauthorized", "home_domain_id": "root"}}
	})
	err = applyUpdate(unauthorized)
	firstCallRequireRefusal(t, err, "law addition is outside spec_mandate")

	// Affected-Domain membership mutant: the successor binding names a
	// Domain the pinned registry does not declare.
	unknownDomain := updateInput(func(fields map[string]any) {
		binding := fields["architecture_binding"].(map[string]any)
		binding["affected_domain_ids"] = []string{"root", "ghost"}
	})
	err = applyUpdate(unknownDomain)
	firstCallRequireRefusal(t, err, "architecture_binding.affected_domain_ids names an unknown Domain: ghost")

	// Version mutant: the successor version does not immediately follow the
	// active contract.
	versionSkip := updateInput(func(fields map[string]any) {
		fields["contract_version"] = 4
	})
	err = applyUpdate(versionSkip)
	firstCallRequireRefusal(t, err, "next retained version")

	pairMismatch := updateInput(nil)
	pairFields := pairMismatch["contract"].(map[string]any)
	pairFields["outcome_kind"] = "exists"
	pairFields["outcome_payload"] = map[string]any{"kind": "check", "check_ref": "check:pair-mismatch", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}
	if err := payloadschema.Validate("work_define_revise_input", mustJSONRaw(t, pairMismatch)); err == nil {
		t.Fatal("published update input admitted the undeclared outcome pair")
	}
	err = applyUpdate(pairMismatch)
	firstCallRequireRefusal(t, err, "not declared")

	// The unchanged successor admits on its first submission after every
	// mutant was refused without effect.
	valid := updateInput(nil)
	if err := applyUpdate(valid); err != nil {
		t.Fatalf("first-call contract update refused after the mutants: %v", err)
	}
	var successorVersion int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&successorVersion); err != nil || successorVersion != 2 {
		t.Fatalf("active successor contract version=%d err=%v, want 2", successorVersion, err)
	}

	// The retired per-kind pair form — matching kinds, outcome_kind check
	// beside a check outcome_payload — refuses on both boundaries without
	// durable effect: the pair left canonical authoring (CD-0218 D1/D2),
	// and no synthetic outcome fallback is authorable.
	pairValid := updateInput(func(fields map[string]any) {
		fields["contract_version"] = 3
	})
	pairValidFields := pairValid["contract"].(map[string]any)
	pairValidFields["outcome_kind"] = "check"
	pairValidFields["outcome_payload"] = map[string]any{"kind": "check", "check_ref": "check:pair-valid", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"}
	if err := payloadschema.Validate("work_define_revise_input", mustJSONRaw(t, pairValid)); err == nil {
		t.Fatal("published update input admitted the undeclared outcome pair")
	}
	if err := applyUpdate(pairValid); err == nil {
		t.Fatal("whole-store admission admitted the retired outcome pair form")
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&successorVersion); err != nil || successorVersion != 2 {
		t.Fatalf("retired pair refusal changed durable state: successor contract version=%d err=%v, want 2", successorVersion, err)
	}

	second := updateInput(func(fields map[string]any) {
		fields["contract_version"] = 3
	})
	if err := applyUpdate(second); err != nil {
		t.Fatalf("second ordinary contract update refused: %v", err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&successorVersion); err != nil || successorVersion != 3 {
		t.Fatalf("active canonical successor contract version=%d err=%v, want 3", successorVersion, err)
	}
}
