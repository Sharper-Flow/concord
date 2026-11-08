package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

func retryJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func agentPlanFields() map[string]any {
	return map[string]any{"outcomes": "Working outcome", "approach": "Bounded change", "decisions": []map[string]any{{"id": "decision:one", "question": "Which approach?", "choice": "One approach", "rationale": "It fits", "rejected": []string{}}}, "touches": []string{"file:one"}, "merge_split": "One item", "risks": "Bounded risk"}
}

func mustScopeVersion(t *testing.T, s *store.Store) string {
	t.Helper()
	version, _, err := s.ScopeVersion(t.Context(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func setWorkflowFixtureHomeOn(t *testing.T, s *store.Store) {
	t.Helper()
	var mode string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT repository_mode,version FROM projects WHERE id='project-1'`).Scan(&mode, &version); err != nil {
		t.Fatal(err)
	}
	if mode == "on" {
		return
	}
	if _, err := s.SetProjectRepositoryMode(t.Context(), store.ProjectRepositoryModeChange{ProjectID: "project-1", RepositoryMode: "on", ExpectedVersion: version}); err != nil {
		t.Fatal(err)
	}
}

func seedCurrentWorkflowDomainFixture(t *testing.T, s *store.Store) {
	t.Helper()
	setWorkflowFixtureHomeOn(t, s)
	ctx := context.Background()
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatalf("pm1fixture.SeedCommittedProductDomain: %v", err)
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
	var homes int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM product_knowledge_homes WHERE product_id='product-1'`).Scan(&homes); err != nil {
		t.Fatal(err)
	}
	if homes == 0 {
		seedCurrentWorkflowDomainFixture(t, s)
	}
}

// committedProductRegistry reads product-1's committed Domain registry
// through the production Domain read.
func committedProductRegistry(t *testing.T, s *store.Store) store.DomainRegistryView {
	t.Helper()
	list, err := s.QueryDomainList(context.Background(), store.DomainListRequest{Product: "product-1"})
	if err != nil {
		t.Fatal(err)
	}
	if list.Registry == nil {
		t.Fatal("product-1 has no committed Domain registry")
	}
	return *list.Registry
}

// agentFixtureLawFrontmatter builds one derived fixture law file's closed
// JSON frontmatter from the retained metadata only. Only a root-homed law may
// state the product-wide rationale, so a child-homed fixture law passes an
// empty one and omits the field.
func agentFixtureLawFrontmatter(id, homeDomainID, productWideRationale string) string {
	authority := `{"tier":"derived"}`
	frontmatter := `{"id":"` + id + `","title":"Fixture law","authority":` + authority +
		`,"home_domain_id":"` + homeDomainID + `","applies_to_domain_ids":[]`
	if productWideRationale != "" {
		frontmatter += `,"product_wide_rationale":"` + productWideRationale + `"`
	}
	return frontmatter + "}"
}

// commitAgentFixtureLaw amends a seeded home's committed law folder with one
// law file and commits it. Anything already staged in the repository's index
// rides the same commit.
func commitAgentFixtureLaw(t *testing.T, repo, relativePath, frontmatter string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" + frontmatter + "\n---\n\nDurable fixture law.\n"
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "--", relativePath)
	gitRun(t, repo, "commit", "--quiet", "-m", "fixture law "+relativePath)
}

// retireAgentFixtureLaw commits the removal of one committed law file: the
// CD-0036 D2 cutover a stale contract pin refers to. The pin outlives the law,
// exactly as a production cutover leaves it.
func retireAgentFixtureLaw(t *testing.T, repo, relativePath string) {
	t.Helper()
	gitRun(t, repo, "rm", "--quiet", "--", relativePath)
	gitRun(t, repo, "commit", "--quiet", "-m", "retire fixture law "+relativePath)
}

func workflowArchitectureBindingFixture() map[string]any {
	return map[string]any{
		"home_domain_id":           pm1fixture.FixtureRootDomainID,
		"affected_domain_ids":      []string{pm1fixture.FixtureRootDomainID},
		"domain_additions":         []string{},
		"domain_modifies":          []string{},
		"domain_relation_modifies": []any{},
		"law_additions":            []any{},
		"verification_obligations": []any{},
	}
}

// workflowArchitectureBindingForSeededRegistry reads the committed
// registry's root Domain from the store, so a binding cannot drift from the
// registry the store validates against.
func workflowArchitectureBindingForSeededRegistry(t *testing.T, s *store.Store) map[string]any {
	t.Helper()
	root := committedProductRegistry(t, s).RootDomainID
	binding := workflowArchitectureBindingFixture()
	binding["home_domain_id"] = root
	binding["affected_domain_ids"] = []string{root}
	return binding
}

func addWorkflowContractApprovalFields(input map[string]any) {
	input["fields"] = workflowContractFieldsFixture()
}

func workflowContractFieldsFixture() map[string]any {
	return map[string]any{
		"architecture_binding": workflowArchitectureBindingFixture(),
		"legislative_subjects": []string{},
		"spec_mandate":         []string{},
		"law_modifies":         []string{},
	}
}

func workflowContractActionInput(t *testing.T, s *store.Store, key string) json.RawMessage {
	t.Helper()
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	return workflowContractActionInputWithFields(t, key, fields)
}

func workflowContractFieldsForSeededRegistry(t *testing.T, s *store.Store) map[string]any {
	t.Helper()
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = workflowArchitectureBindingForSeededRegistry(t, s)
	return fields
}

func workflowContractActionInputWithFields(t *testing.T, key string, fields map[string]any) json.RawMessage {
	t.Helper()
	input := map[string]any{
		"work_id": "work-1",
		// Proposal and alignment each record their document and completion.
		"expected_version": 8,
		"action_id":        "approve_contract",
		"fields":           fields,
		"idempotency_key":  key,
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// advanceCapturedWorkToContract records intake and a change plan before
// returning the version used to approve the contract on its plan frontier.
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
		if response.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("pre-contract %s: %+v", actionID, response.Error)
		}
		var after int64
		if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id=?`, workID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		return after
	}
	dispatch("record_intake", map[string]any{"outcome": "proceed"}, "intake")
	dispatch("pick_type", map[string]any{"type": "change", "reason": "The work changes behavior."}, "type")
	dispatch("record_clarify", map[string]any{"resolved_questions": []string{}, "note": "No operator questions remain."}, "clarify")
	dispatch("complete_step", map[string]any{"step_id": "clarify", "note": "Clarification complete."}, "complete-clarify")
	return dispatch("record_plan", agentPlanFields(), "plan")
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
	mandateLawID := strings.Repeat("l", 128)
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
			"legislative_subjects": workflowContractFieldsFixture()["legislative_subjects"],
			"spec_mandate":         []string{mandateLawID},
			"law_modifies":         []string{mandateLawID},
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
			"fields":          map[string]any{"architecture_binding": invalid, "legislative_subjects": workflowContractFieldsFixture()["legislative_subjects"], "spec_mandate": []string{}, "law_modifies": []string{}},
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

func TestWorkflowContractFieldsFixtureAccepted(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"work_id": "work-1", "expected_version": 7, "action_id": "approve_contract",
		"fields": workflowContractFieldsFixture(), "idempotency_key": "fixture-fields",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := payloadschema.Validate("work_transition_action_input", raw); err != nil {
		t.Fatalf("contract fixture rejected: %v", err)
	}
}

func TestApproveContractSchemaAcceptsLegislativeSubjects(t *testing.T) {
	t.Parallel()
	fields := workflowContractFieldsFixture()
	fields["legislative_subjects"] = []string{"spec:example", "decision:example"}
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
		t.Fatalf("approve_contract legislative subjects rejected: %v", err)
	}

	fields["legislative_subjects"] = []string{"invalid subject"}
	if err := payloadschema.Validate("work_transition_action_input", payload("invalid-legislative-subject")); err == nil {
		t.Fatal("approve_contract schema accepted an invalid legislative subject")
	}
	fields["legislative_subjects"] = []string{"spec:example", "decision:example"}
	fields["unknown_contract_field"] = true
	if err := payloadschema.Validate("work_transition_action_input", payload("unknown-approve-contract-field")); err == nil {
		t.Fatal("approve_contract schema accepted an unknown field")
	}
}

// seedAgentPlanFrontier installs a real template with a synthetic completed
// prefix. It does not create evidence receipts.
func seedAgentPlanFrontier(t testing.TB, s *store.Store, workID string, planType store.WorkflowStepPlanType, target string) {
	t.Helper()
	raw := agentPlanFixtureJSON(t, planType, target)
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	result, err := tx.Exec(`UPDATE workflow_instances SET workflow_type=?, step_plan=?, prelude_state=?, instance_state='running' WHERE work_id=?`, planType, raw, `{"intake":{"outcome":"proceed"}}`, workID)
	if err != nil {
		t.Fatal(err)
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("seed plan for %s: updated %d rows: %v", workID, count, err)
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func agentPlanFixtureJSON(t testing.TB, planType store.WorkflowStepPlanType, target string) string {
	t.Helper()
	plan, err := store.WorkflowStepPlanTemplate(planType)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range plan.Steps {
		step := &plan.Steps[i]
		if step.ID == target {
			found = true
			break
		}
		attempt := store.WorkflowPlanAttempt{Note: "synthetic prerequisite", CompletedAt: "2026-09-12T00:00:00Z"}
		if step.Reviewed {
			attempt.Reviewer, attempt.Result, attempt.Commit, attempt.Link = "concord-review", "pass", "commit:fixture", "https://example.com/review"
		}
		step.Attempts = []store.WorkflowPlanAttempt{attempt}
	}
	if !found {
		t.Fatalf("workflow type %s has no step %q", planType, target)
	}
	frontier := store.WorkflowStepPlanFrontier(plan)
	if len(frontier) != 1 || frontier[0].ID != target {
		t.Fatalf("unexpected plan frontier: %+v", frontier)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
