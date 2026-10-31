package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// A Product-changing approval verifies its architecture binding against the
// derived Domain registry. On a fresh store that registry exists only as
// committed content in the Product's knowledge home, and no operator step
// produces it. The demand for it is the approval, and the read an agent
// composes its pin from; both derive it on demand (CD-0082 D1), so the
// approval succeeds with nothing rebuilt by hand.
func TestProductChangingApprovalDerivesTheRegistryItPins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "product_read"})
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatalf("pm1fixture.SeedCommittedProductDomain: %v", err)
	}
	// Schema 174 retired the SQL Domain projection, so the registry exists
	// only as committed content by construction: nothing can be rebuilt or
	// pre-seeded, which is the state this approval must succeed in.
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	// The agent learns the root Domain it binds from a Domain read. That read
	// is the first demand: it derives the registry from committed content.
	detail := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: "list", Input: json.RawMessage(`{"product_id":"product-1","page":{"cursor":null,"limit":10}}`)}, env)
	if detail.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("domain list on a committed-only registry: %+v", detail.Error)
	}
	var listed struct {
		Registry struct {
			ContentHash  string `json:"content_hash"`
			RootDomainID string `json:"root_domain_id"`
		} `json:"registry"`
	}
	if err := json.Unmarshal(detail.Result, &listed); err != nil {
		t.Fatalf("decode domain list: %v: %s", err, string(detail.Result))
	}
	if listed.Registry.ContentHash == "" || listed.Registry.ContentHash == pm1fixture.FixtureDomainRegistryContentHash {
		t.Fatalf("registry hash must be derived from committed content, got %q", listed.Registry.ContentHash)
	}

	// The archived ops_runbook authoring left the live catalog (CD-0218 D1);
	// the derived-registry approval journey runs on the implementation head.
	registered, registeredErr := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if registeredErr != nil {
		t.Fatal(registeredErr)
	}
	ctx = agentWorkflowSourceProof(t, ctx, s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize workflow: %v", err)
	}
	// The canonical head declares approve_contract behind the pre-contract walk.
	version := advanceCapturedWorkToContract(t, s, service, env, "work-1", "derived-registry")
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = map[string]any{
		"home_domain_id":           listed.Registry.RootDomainID,
		"affected_domain_ids":      []string{listed.Registry.RootDomainID},
		"domain_modifies":          []string{},
		"domain_relation_modifies": []any{},
		"law_additions":            []any{},
		"verification_obligations": []any{},
	}
	resp, _ := approvedWorkflowContract(t, s, service, env, version, fields, "derived-registry-approval")
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract against a derived registry: %+v", resp.Error)
	}
	// The envelope is validated only at marshal, which in-process dispatch
	// never reaches. Marshal what the CLI would send.
	if _, err := json.Marshal(resp); err != nil {
		t.Fatalf("approve_contract ok envelope does not marshal: %v", err)
	}
}

// The approval owns its own demand. A registry an agent read earlier can be
// gone by the time the approval runs: the projection was rebuilt for another
// home, or the store was restored from a backup taken before the read. The
// pin the agent holds is still correct, because it names committed content,
// so the approval derives the registry again rather than refusing.
func TestProductChangingApprovalDerivesTheRegistryWithoutAPriorRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_transition", "product_read"})
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatalf("pm1fixture.SeedCommittedProductDomain: %v", err)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	// Learn the committed registry's root Domain through the production path
	// before testing approval with every trace of the derived projection removed.
	detail := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: "list", Input: json.RawMessage(`{"product_id":"product-1","page":{"cursor":null,"limit":10}}`)}, env)
	if detail.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("domain list before the no-prior-read approval: %+v", detail.Error)
	}
	var listed struct {
		Registry struct {
			RootDomainID string `json:"root_domain_id"`
		} `json:"registry"`
	}
	if err := json.Unmarshal(detail.Result, &listed); err != nil {
		t.Fatalf("decode domain list: %v: %s", err, string(detail.Result))
	}
	rootDomainID := listed.Registry.RootDomainID
	// The archived ops_runbook authoring left the live catalog (CD-0218 D1);
	// the no-prior-read derivation journey runs on the implementation head.
	registered, registeredErr := store.LiveWorkflowDefinitionForRef("workflow.implementation")
	if registeredErr != nil {
		t.Fatal(registeredErr)
	}
	ctx = agentWorkflowSourceProof(t, ctx, s, "work-1")
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize workflow: %v", err)
	}
	// The canonical head declares approve_contract behind the pre-contract walk.
	version := advanceCapturedWorkToContract(t, s, service, env, "work-1", "derived-registry-noread")
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = map[string]any{
		"home_domain_id":           rootDomainID,
		"affected_domain_ids":      []string{rootDomainID},
		"domain_modifies":          []string{},
		"domain_relation_modifies": []any{},
		"law_additions":            []any{},
		"verification_obligations": []any{},
	}
	// Owning-start source verification and the pre-contract actions derive
	// their own law inventory. Schema 174 retired the SQL Domain projection,
	// so the approval runs with no stored registry by construction: it must
	// derive everything from the committed home again.
	resp, _ := approvedWorkflowContract(t, s, service, env, version, fields, "derived-registry-approval-no-read")
	if resp.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract with no stored registry present: %+v", resp.Error)
	}
}
