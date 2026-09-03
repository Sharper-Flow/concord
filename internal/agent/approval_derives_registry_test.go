package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

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
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "product_read"})
	if _, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir()); err != nil {
		t.Fatalf("pm1fixture.SeedCommittedProductDomain: %v", err)
	}
	var registries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(*) FROM domain_registries WHERE product_id='product-1'`).Scan(&registries); err != nil {
		t.Fatal(err)
	}
	if registries != 0 {
		t.Fatalf("fixture precondition: want an empty Domain registry, got %d rows", registries)
	}
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)

	// The agent learns the hash it pins from a Domain read. That read is the
	// first demand: it derives the registry from committed content.
	detail := dispatchRead(t, s, service, InvokeRequest{Tool: "concord_domain", Operation: "list", Input: json.RawMessage(`{"product_id":"product-1","page":{"cursor":null,"limit":10}}`)}, env)
	if detail.Outcome != OutcomeOK {
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

	registered := mustOpsRunbookDefinition(t)
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize workflow: %v", err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = map[string]any{
		"domain_registry_content_hash": listed.Registry.ContentHash,
		"home_domain_id":               listed.Registry.RootDomainID,
		"affected_domain_ids":          []string{listed.Registry.RootDomainID},
		"domain_modifies":              []string{},
		"domain_relation_modifies":     []any{},
		"law_additions":                []any{},
		"verification_obligations":     []any{},
	}
	resp, _ := approvedOpsAction(t, s, service, grant, privateKey, env, version, "approve_contract", fields, "derived-registry-approval")
	if resp.Outcome != OutcomeOK {
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
	ctx := context.Background()
	s, service, grant, privateKey := mutationDispatchFixture(t, []Capability{"work_transition", "product_read"})
	home, err := pm1fixture.SeedCommittedProductDomain(ctx, s, "product-1", "project-1", t.TempDir())
	if err != nil {
		t.Fatalf("pm1fixture.SeedCommittedProductDomain: %v", err)
	}
	// Learn the committed registry's hash through the production path, then
	// remove every trace of the derived projection.
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	var contentHash, rootDomainID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash, root_domain_id FROM domain_registries WHERE product_id='product-1'`).Scan(&contentHash, &rootDomainID); err != nil {
		t.Fatal(err)
	}
	wipeDerivedKnowledge(t, s, home)
	var registries int
	if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(*) FROM domain_registries`).Scan(&registries); err != nil || registries != 0 {
		t.Fatalf("precondition: want no derived registry, got %d err=%v", registries, err)
	}

	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	registered := mustOpsRunbookDefinition(t)
	if err := s.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{WorkID: "work-1", Definition: registered, Actor: store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent}, Now: fixedTime()})
	}); err != nil {
		t.Fatalf("initialize workflow: %v", err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-1'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	fields := workflowContractFieldsFixture()
	fields["architecture_binding"] = map[string]any{
		"domain_registry_content_hash": contentHash,
		"home_domain_id":               rootDomainID,
		"affected_domain_ids":          []string{rootDomainID},
		"domain_modifies":              []string{},
		"domain_relation_modifies":     []any{},
		"law_additions":                []any{},
		"verification_obligations":     []any{},
	}
	resp, _ := approvedOpsAction(t, s, service, grant, privateKey, env, version, "approve_contract", fields, "derived-registry-approval-no-read")
	if resp.Outcome != OutcomeOK {
		t.Fatalf("approve_contract with no derived registry present: %+v", resp.Error)
	}
}

// wipeDerivedKnowledge leaves the store with no derived projection for the
// home, the state a restore from before any rebuild would leave. The rebuild
// is the one mechanism that clears the projection in dependency order, so
// the wipe runs it against the home's own history at a commit that projects
// nothing, then removes the watermark that rebuild wrote. The committed
// content at HEAD is untouched.
func wipeDerivedKnowledge(t *testing.T, s *store.Store, home store.KnowledgeHome) {
	t.Helper()
	empty := home
	empty.HeadRef = emptyKnowledgeCommit(t, home.RepoPath)
	if err := s.RebuildKnowledgeIndex(context.Background(), empty); err != nil {
		t.Fatalf("rebuild against the empty commit: %v", err)
	}
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM knowledge_index_watermark`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
}

// emptyKnowledgeCommit is a commit in the repository whose tree carries no
// manifest and no notes: an empty tree committed with no parent.
func emptyKnowledgeCommit(t *testing.T, repo string) string {
	t.Helper()
	tree := strings.TrimSpace(runFixtureGit(t, repo, "hash-object", "-t", "tree", "/dev/null"))
	return strings.TrimSpace(runFixtureGit(t, repo, "commit-tree", tree, "-m", "empty"))
}

func runFixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Concord Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Concord Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
