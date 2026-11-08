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
			for key, value := range overrides {
				fields[key] = value
			}
			input[name] = fields
			continue
		}
		input[name] = firstCallSample(defs, property)
	}
	if _, ok := input["fields"]; !ok {
		property, ok := properties["fields"].(map[string]any)
		if !ok {
			t.Fatalf("published variant %s declares no fields property", variant)
		}
		fields := crossFieldSampleObject(defs, property)
		for key, value := range overrides {
			fields[key] = value
		}
		input["fields"] = fields
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

func TestFirstCallMandateCodePointBounds(t *testing.T) {
	for _, tc := range []struct {
		name, id string
		valid    bool
	}{
		{"minimum", "ab", true},
		{"below minimum", "a", false},
		{"ascii maximum", strings.Repeat("a", 128), true},
		{"ascii above maximum", strings.Repeat("a", 129), false},
		{"unicode maximum", strings.Repeat("é", 128), true},
		{"unicode above maximum", strings.Repeat("é", 129), false},
		{"qualified maximum", "project/" + strings.Repeat("b", 120), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			defer s.Close()
			const workID = "work-mandate-bound"
			actor := seedFirstCallWorkflow(t, s, workID, "mandate-bound", WorkflowStepPlanChange)
			home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
			lawID := strings.TrimPrefix(tc.id, "project/")
			writeKnowledgeFile(t, home.RepoPath, ".concord/law/decisions/bound-fixture.md", fixtureLawFolderLawWithCriteria(lawID, "[]"))
			commitKnowledgeRepo(t, home.RepoPath, "commit bound fixture")
			version := readWorkVersion(t, s, workID)
			binding := map[string]any{
				"home_domain_id": "root", "affected_domain_ids": []string{"root"},
				"domain_additions": []string{}, "domain_modifies": []string{}, "domain_relation_modifies": []map[string]any{},
				"law_additions": []map[string]any{}, "verification_obligations": []map[string]any{{"law_id": "spec:one", "obligation_id": "verification"}},
			}
			input := firstCallActionInput(t, firstCallPublishedDefs(t), workID, version, map[string]any{
				"legislative_subjects": []string{}, "spec_mandate": []string{"spec:one", tc.id}, "law_modifies": []string{tc.id}, "architecture_binding": binding,
			})
			err := firstCallApproveContract(t, s, workID, version, "mandate-bound", actor, input)
			if !tc.valid {
				if err == nil {
					t.Fatal("invalid mandate admitted")
				}
				return
			}
			if err != nil {
				t.Fatalf("valid mandate refused: %v", err)
			}
			contract, err := readWorkflowContractVersion(t.Context(), s.DatabaseForTesting(), workID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(contract.SpecMandate, []string{"spec:one", tc.id}) || !slices.Equal(contract.LawModifies, []string{tc.id}) {
				t.Fatalf("persisted identities: mandate=%q modifies=%q", contract.SpecMandate, contract.LawModifies)
			}
		})
	}
}

func TestContractLawListSchemaPreflightParity(t *testing.T) {
	root := payloadschema.Document()
	defs := root["$defs"].(map[string]any)
	approve := defs["work_transition_action_variant_approve_contract"].(map[string]any)["properties"].(map[string]any)["fields"].(map[string]any)["properties"].(map[string]any)
	update := defs["workflow_contract_update"].(map[string]any)["properties"].(map[string]any)
	for _, fieldName := range []string{"spec_mandate", "law_modifies"} {
		for _, shape := range []map[string]any{approve, update} {
			field := shape[fieldName].(map[string]any)
			for _, tc := range []struct {
				value string
				valid bool
			}{
				{"a", false}, {"ab", true}, {strings.Repeat("a", 128), true}, {strings.Repeat("a", 129), false},
				{strings.Repeat("é", 128), true}, {strings.Repeat("é", 129), false},
			} {
				values := []any{tc.value}
				schemaOK := payloadschema.ValidateValue(values, field, root, "$") == nil
				storeOK := validateWorkflowPayloadStringList(actionContractLawListField(fieldName, true), values)
				if schemaOK != tc.valid || storeOK != tc.valid {
					t.Errorf("%s length %d: schema=%v preflight=%v want=%v", fieldName, len([]rune(tc.value)), schemaOK, storeOK, tc.valid)
				}
			}
		}
	}
	for _, n := range []int{128, 129, 256, 257} {
		value := strings.Repeat("a", n)
		want := n <= 256
		genericOK := payloadschema.ValidateValue(value, defs["law_id"].(map[string]any), root, "$") == nil
		retiringOK := validateWorkflowPayloadStringList(actionLawListField("retiring_clauses", true), []any{value})
		if genericOK != want || retiringOK != want {
			t.Errorf("generic and retiring length %d: schema=%v preflight=%v want=%v", n, genericOK, retiringOK, want)
		}
	}
}

func TestFirstCallApproveContractAdmitsAddedDomainWithLaw(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	defer s.Close()
	const workID = "work-first-call-added-domain"
	actor := seedFirstCallWorkflow(t, s, workID, "first-call-added-domain", WorkflowStepPlanChange)
	version := readWorkVersion(t, s, workID)
	defs := firstCallPublishedDefs(t)
	// frontend-experience is absent from the committed fixture registry; the
	// contract declares it added and homes its first law there.
	addedBinding := func(affected []string) map[string]any {
		return map[string]any{
			"home_domain_id":           "root",
			"affected_domain_ids":      affected,
			"domain_additions":         []string{"frontend-experience"},
			"domain_modifies":          []string{},
			"domain_relation_modifies": []map[string]any{},
			"law_additions":            []map[string]any{{"law_id": "law:new", "home_domain_id": "frontend-experience"}},
			"verification_obligations": []map[string]any{{"law_id": "spec:one", "obligation_id": "verification"}},
		}
	}
	input := firstCallActionInput(t, defs, workID, version, map[string]any{
		"legislative_subjects": []string{},
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": addedBinding([]string{"root", "child", "frontend-experience"}),
	})
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("work_transition_action_variant_approve_contract", payload); err != nil {
		t.Fatalf("published variant refused a declared Domain addition: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"registered", func(b map[string]any) {
			b["domain_additions"] = []string{"root"}
			b["affected_domain_ids"] = []string{"root"}
			b["law_additions"] = []map[string]any{{"law_id": "law:new", "home_domain_id": "root"}}
		}, "Domain addition already exists"},
		{"outside affected", func(b map[string]any) { b["affected_domain_ids"] = []string{"root"} }, "domain_additions contains a Domain outside affected_domain_ids"},
		{"overlap", func(b map[string]any) { b["domain_modifies"] = []string{"frontend-experience"} }, "domain_additions overlaps domain_modifies"},
		{"missing law", func(b map[string]any) {
			b["law_additions"] = []map[string]any{{"law_id": "law:new", "home_domain_id": "root"}}
		}, "Domain addition has no homed law addition"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := addedBinding([]string{"root", "frontend-experience"})
			tc.mutate(binding)
			mutant := firstCallActionInput(t, defs, workID, version, map[string]any{
				"legislative_subjects": []string{}, "spec_mandate": []string{"spec:one", "law:new"}, "architecture_binding": binding,
			})
			err := firstCallApproveContract(t, s, workID, version, "added-domain-"+strings.ReplaceAll(tc.name, " ", "-"), actor, mutant)
			firstCallRequireRefusal(t, err, tc.want)
			if tc.name == "registered" {
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindProjectionConflict {
					t.Fatalf("registered Domain refusal = %v, want projection conflict", err)
				}
			}
		})
	}

	// A declared addition exempts only itself: another affected Domain that
	// is neither registered nor added still refuses without effect.
	ghostMutant := firstCallActionInput(t, defs, workID, version, map[string]any{
		"legislative_subjects": []string{},
		"spec_mandate":         []string{"spec:one", "law:new"},
		"architecture_binding": addedBinding([]string{"root", "child", "frontend-experience", "ghost"}),
	})
	mutantErr := firstCallApproveContract(t, s, workID, version, "first-call-added-domain-ghost", actor, ghostMutant)
	firstCallRequireRefusal(t, mutantErr, "architecture_binding.affected_domain_ids names an unknown Domain: ghost")

	if err := firstCallApproveContract(t, s, workID, version, "first-call-added-domain", actor, input); err != nil {
		t.Fatalf("approve_contract refused a declared Domain addition with its first law: %v", err)
	}
	// The approved contract keeps the addition, so a read of it, or a
	// successor contract built from that read, still declares the Domain added.
	contract, err := readWorkflowContractVersion(t.Context(), s.DatabaseForTesting(), workID, 1)
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := json.Marshal(contract.ArchitectureBinding)
	if err != nil {
		t.Fatal(err)
	}
	var additions struct {
		DomainAdditions []string `json:"domain_additions"`
	}
	if err := json.Unmarshal(recorded, &additions); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(additions.DomainAdditions, []string{"frontend-experience"}) {
		t.Fatalf("approved architecture binding = %s, want domain_additions [frontend-experience]", recorded)
	}
	for _, query := range []string{
		`INSERT INTO workflow_contract_domain_additions(work_id,contract_version,domain_id) VALUES(?,1,'other')`,
		`UPDATE workflow_contract_domain_additions SET domain_id='other' WHERE work_id=? AND contract_version=1`,
		`DELETE FROM workflow_contract_domain_additions WHERE work_id=? AND contract_version=1`,
	} {
		if _, err := s.DatabaseForTesting().Exec(query, workID); err == nil || !strings.Contains(err.Error(), "write-seam-only") {
			t.Fatalf("unguarded Domain addition write %q: %v", query, err)
		}
	}
	detail, err := readLawContextItem(t.Context(), s, workID)
	if err != nil {
		t.Fatalf("full detail refused the added Domain: %v", err)
	}
	encodedDetail, err := json.Marshal(detail.LawContext.Domains)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encodedDetail), `"domain_id":"frontend-experience","name":"","purpose":"","status":"added"`) {
		t.Fatalf("full detail Domains = %s, want the declared addition", encodedDetail)
	}
	for _, domain := range detail.LawContext.Domains {
		encoded, err := json.Marshal(domain)
		if err != nil {
			t.Fatal(err)
		}
		if err := payloadschema.Validate("workflow_law_context_domain", encoded); err != nil {
			t.Fatalf("published Domain output rejected %s: %v", encoded, err)
		}
	}
	if err := payloadschema.Validate("workflow_law_context_domain", []byte(`{"domain_id":"frontend-experience","name":"","purpose":"","status":"current"}`)); err == nil {
		t.Fatal("published Domain output accepted an undeclared status")
	}
	successor := pContractSuccessor(t, s, workID)
	if err := reviseContractRecord(t, s, workID, actor, successor, readWorkVersion(t, s, workID)); err != nil {
		t.Fatalf("unchanged added Domain revision refused: %v", err)
	}
	revised, err := readWorkflowContractVersion(t.Context(), s.DatabaseForTesting(), workID, 2)
	if err != nil || revised.ArchitectureBinding == nil || !slices.Equal(revised.ArchitectureBinding.DomainAdditions, []string{"frontend-experience"}) {
		t.Fatalf("revised binding = %+v, err = %v", revised, err)
	}
	var registry KnowledgeDomainRegistry
	if err := json.Unmarshal([]byte(workflowArchitectureFixtureRegistry), &registry); err != nil {
		t.Fatal(err)
	}
	missingRegistry := registry
	missingRegistry.Domains = slices.DeleteFunc(slices.Clone(registry.Domains), func(domain KnowledgeDomain) bool { return domain.DomainID == "child" })
	missingJSON, err := json.Marshal(missingRegistry)
	if err != nil {
		t.Fatal(err)
	}
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	commitWorkflowFixtureRegistry(t, home, string(missingJSON))
	_, err = readLawContextItem(t.Context(), s, workID)
	var missing *Failure
	if !failureAs(err, &missing) || missing.Kind != KindProjectionNotFound || !slices.Contains(missing.CandidateIDs, "child") {
		t.Fatalf("missing non-addition Domain refusal = %+v, err = %v", missing, err)
	}
	registry.Domains = append(registry.Domains, KnowledgeDomain{DomainID: "frontend-experience", Name: "Frontend", Purpose: "Fixture frontend Domain.", ParentDomainID: "product-root:product", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}})
	registryJSON, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	commitWorkflowFixtureRegistry(t, home, string(registryJSON))
	detail, err = readLawContextItem(t.Context(), s, workID)
	if err != nil {
		t.Fatalf("full detail refused the registered Domain: %v", err)
	}
	encodedDetail, err = json.Marshal(detail.LawContext.Domains)
	if err != nil || !strings.Contains(string(encodedDetail), `"domain_id":"frontend-experience","name":"Frontend","purpose":"Fixture frontend Domain."}`) {
		t.Fatalf("registered Domain detail = %s, err = %v", encodedDetail, err)
	}
	err = reviseContractRecord(t, s, workID, actor, pContractSuccessor(t, s, workID), readWorkVersion(t, s, workID))
	var conflict *Failure
	if !failureAs(err, &conflict) || conflict.Kind != KindProjectionConflict || !strings.Contains(conflict.RecoveryAction, "modify the registered Domain") {
		t.Fatalf("registered Domain revision refusal = %+v, err = %v", conflict, err)
	}
	req := removalTestRequest()
	req.WorkID, req.ExpectedVersion = workID, readWorkVersion(t, s, workID)
	confirmRemovalHandoff(t, s, &req)
	if err := removeWorkForTest(t.Context(), s, req); err != nil {
		t.Fatalf("remove work with added Domain: %v", err)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contract_domain_additions WHERE work_id=?`, workID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("removed work retains %d Domain additions: %v", count, err)
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
			"domain_additions":         []string{},
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
			"domain_additions":         []string{},
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
			"domain_additions":         []string{},
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
	longID := strings.Repeat("é", 128)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	writeKnowledgeFile(t, home.RepoPath, ".concord/law/decisions/update-bound-fixture.md", fixtureLawFolderLawWithCriteria(longID, "[]"))
	commitKnowledgeRepo(t, home.RepoPath, "commit update bound fixture")
	invalidBound := updateInput(nil)
	invalidBound["contract"].(map[string]any)["law_modifies"] = []string{strings.Repeat("é", 129)}
	if err := applyUpdate(invalidBound); err == nil {
		t.Fatal("contract update admitted a 129-code-point law modification")
	}
	valid := updateInput(func(fields map[string]any) {
		fields["spec_mandate"] = []string{"spec:one", "law:successor", longID}
		fields["law_modifies"] = []string{longID}
	})
	if err := applyUpdate(valid); err != nil {
		t.Fatalf("first-call contract update refused after the mutants: %v", err)
	}
	updated, err := readWorkflowContractVersion(t.Context(), s.DatabaseForTesting(), workID, 2)
	if err != nil || !slices.Equal(updated.LawModifies, []string{longID}) || !slices.Contains(updated.SpecMandate, longID) {
		t.Fatalf("updated contract lost law identity: %+v err=%v", updated, err)
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
