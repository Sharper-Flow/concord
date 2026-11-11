package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// The read population fixture exists so the agent read surface can be
// dispatched against a store that looks like a working operator store rather
// than a single-row stub. At 100 Products and 700 work items, each read family
// gets the rows its result schema declares: Domains carrying decision, spec,
// and constitution law, an Initiative with entries, law in a committed Git
// home, work and Domain observations, a research pack, and a
// real claimed worktree backed by a real Git repository.

const (
	readPopulationProduct       = "prod-alpha"
	readPopulationProject       = "proj-web"
	readPopulationKnowledgeHome = "proj-knowledge"
	readPopulationWork          = "work-population"
	readPopulationPeer          = "work-population-2"
	readPopulationRootDomain    = "product-root:population"
	readPopulationChildDomain   = "sync"
	readPopulationConstitution  = "CONST-0001"
	readPopulationDecision      = "CD-0201"
	readPopulationSpec          = "SPEC-0201"

	readPopulationProducts       = 100
	readPopulationWorkPerProduct = 7
)

type readPopulationFixture struct {
	store        *store.Store
	service      *Service
	grant        Authority
	workID       string
	domainID     string
	registryHash string
}

// seedReadPopulationFixture builds the population-scale store. Every step uses
// the store's own mutation, fold, or dispatch paths; the only direct SQL is
// the bulk Product-row population and the knowledge-home authorization rows,
// both mirroring existing store fixtures.
func seedReadPopulationFixture(t *testing.T) readPopulationFixture {
	t.Helper()
	ctx := context.Background()
	corpus, err := pm1fixture.Load()
	if err != nil {
		t.Fatalf("pm1fixture.Load: %v", err)
	}
	s, err := pm1fixture.OpenTemp(t.TempDir())
	if err != nil {
		t.Fatalf("pm1fixture.OpenTemp: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := pm1fixture.Seed(ctx, s, corpus); err != nil {
		t.Fatalf("pm1fixture.Seed: %v", err)
	}

	fx := readPopulationFixture{store: s, workID: readPopulationWork, domainID: readPopulationChildDomain}
	seedReadPopulationFocusWork(t, s)
	fx.registryHash = fx.seedKnowledgeHome(t)
	fx.seedFocusWorkflow(t)

	fx.service, _, fx.grant = newAuthorizedService(t, s, "client-1", "human-1",
		[]agentcontract.Capability{"product_read", "work_define", "work_transition", "work_relate"},
		[]string{readPopulationProduct}, []string{readPopulationProject},
		store.ProjectResolution{ProjectID: readPopulationProject})
	fx.grant.SessionRef = "session-population"
	fx.grant.AgentRef = "agent-1"
	// The Initiative entry and every other event-folded relation derive its
	// identity from the relation-creating event count, so the bulk-seeded
	// blocker edges must land only after the last folded relation, or they
	// would occupy the identity the fold assigns.
	fx.seedHostCheckout(t)
	fx.seedPopulationRows(t)
	seedReadPopulationProducts(t, s)
	return fx
}

// seedReadPopulationProducts bulk-inserts the population shape: 100
// Products, two Projects each, seven work items per Product, memberships,
// blocker edges, and one workflow instance per Product.
func seedReadPopulationProducts(t *testing.T, s *store.Store) {
	t.Helper()
	plan, err := store.WorkflowStepPlanTemplate(store.WorkflowStepPlanChange)
	if err != nil {
		t.Fatal(err)
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var statements strings.Builder
	statements.WriteString("INSERT INTO write_guard(active) VALUES(1);")
	for product := 0; product < readPopulationProducts; product++ {
		productID := fmt.Sprintf("perf-product-%03d", product)
		projectPrimary := fmt.Sprintf("perf-project-%03d-primary", product)
		projectSecondary := fmt.Sprintf("perf-project-%03d-secondary", product)
		fmt.Fprintf(&statements, "INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES('%s','Perf Product %03d','prototype','operator_only',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');", productID, product)
		fmt.Fprintf(&statements, "INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('%s','Perf Primary %03d',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');", projectPrimary, product)
		fmt.Fprintf(&statements, "INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('%s','Perf Secondary %03d',1,'2026-08-01T00:00:00Z','2026-08-01T00:00:00Z');", projectSecondary, product)
		fmt.Fprintf(&statements, "INSERT INTO product_projects(product_id,project_id,role) VALUES('%s','%s','primary'),('%s','%s','secondary');", productID, projectPrimary, productID, projectSecondary)
		for work := 0; work < readPopulationWorkPerProduct; work++ {
			workID := fmt.Sprintf("perf-work-%03d-%02d", product, work)
			kind, lifecycle := "task", "needed"
			terminal := "NULL"
			switch work {
			case 1:
				kind, lifecycle = "bug", "in_progress"
			case 4:
				lifecycle, terminal = "completed", "'2026-08-05T00:00:00Z'"
			case 5:
				lifecycle, terminal = "cancelled", "'2026-08-05T00:00:00Z'"
			case 6:
				lifecycle = "in_progress"
			}
			fmt.Fprintf(&statements, "INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at,terminal_time) VALUES('%s','%s','Perf work %03d-%02d','%s',1,'2026-08-%02dT00:00:00Z','2026-08-%02dT00:00:00Z',%s);", workID, kind, product, work, lifecycle, (work%8)+1, (work%8)+1, terminal)
			fmt.Fprintf(&statements, "INSERT INTO work_projects(work_id,project_id,role) VALUES('%s','%s','primary'),('%s','%s','secondary');", workID, projectPrimary, workID, projectSecondary)
			if work == 0 {
				fmt.Fprintf(&statements, "INSERT INTO workflow_instances(work_id,workflow_type,step_plan,instance_state,started_at) VALUES('%s','change','%s','running','2026-08-01T00:00:00Z');", workID, planJSON)
			}
		}
		blockerID := fmt.Sprintf("perf-work-%03d-06", product)
		fmt.Fprintf(&statements, "INSERT INTO relations(work_id_from,work_id_to,kind,created_at) VALUES('%s','perf-work-%03d-02','blocks','2026-08-05T00:00:00Z'),('%s','perf-work-%03d-04','blocks','2026-08-05T00:00:00Z');", blockerID, product, blockerID, product)
	}
	statements.WriteString("DELETE FROM write_guard;")
	if _, err := s.DatabaseForTesting().ExecContext(context.Background(), statements.String()); err != nil {
		t.Fatal(err)
	}
}

// seedReadPopulationFocusWork creates the nonterminal work item every
// work-scoped read targets.
func seedReadPopulationFocusWork(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "population-work-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: readPopulationWork, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Population focus"}`)},
		{EventID: "population-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: readPopulationWork, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"proj-web","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, readPopulationWork): 0}}); err != nil {
		t.Fatal(err)
	}
}

func TestReadPopulationKnowledgeFixtureDesignatesDefault(t *testing.T) {
	corpus, err := pm1fixture.Load()
	if err != nil {
		t.Fatal(err)
	}
	s, err := pm1fixture.OpenTemp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := pm1fixture.Seed(context.Background(), s, corpus); err != nil {
		t.Fatal(err)
	}
	fx := readPopulationFixture{store: s}
	fx.seedKnowledgeHome(t)
	var repo string
	if err := fx.store.DatabaseForTesting().QueryRow(`SELECT locator_value FROM project_locators WHERE locator_id = 'population-knowledge-locator'`).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitRun(t, repo, "symbolic-ref", "--no-recurse", "refs/remotes/origin/HEAD")); got != "refs/remotes/origin/main" {
		t.Fatalf("default designation = %q, want refs/remotes/origin/main", got)
	}
	committed := strings.TrimSpace(gitRun(t, repo, "rev-parse", "refs/heads/main^{commit}"))
	if got := strings.TrimSpace(gitRun(t, repo, "rev-parse", "refs/remotes/origin/main^{commit}")); got != committed {
		t.Fatalf("default commit = %q, want %q", got, committed)
	}
}

// seedKnowledgeHome commits a Domain registry carrying a root and a child
// Domain, plus committed law documents the child hosts, and authorizes the
// home. The home lives on its own Project so the focus Project's
// canonical-path locator stays unambiguous for the worktree claim. It returns
// the committed registry's content fingerprint, the value the architecture
// bindings record instead of a projection row.
func (fx readPopulationFixture) seedKnowledgeHome(t *testing.T) string {
	t.Helper()
	repo, err := os.MkdirTemp(t.TempDir(), "population-knowledge-")
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "--initial-branch=main")
	gitRun(t, repo, "config", "user.email", "concord@example.invalid")
	gitRun(t, repo, "config", "user.name", "Concord Population Test")

	for _, law := range []struct{ path, front string }{
		{".concord/law/constitution/CONST-0001.md", `{"id":"CONST-0001","title":"Population constitution","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"sync","applies_to_domain_ids":[]}`},
		{".concord/law/decisions/" + readPopulationDecision + ".md", `{"id":"CD-0201","title":"Synchronization authority","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"sync","applies_to_domain_ids":[]}`},
		{".concord/law/specs/" + readPopulationSpec + ".md", `{"id":"SPEC-0201","title":"Synchronization specification","authority":{"tier":"derived"},"home_domain_id":"sync","applies_to_domain_ids":[]}`},
	} {
		full := filepath.Join(repo, filepath.FromSlash(law.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("---\n"+law.front+"\n---\n\nThe child Domain owns the committed law it declares.\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	registry := store.KnowledgeDomainRegistry{
		SchemaVersion: "1.0",
		ProductKey:    "population",
		RootDomainID:  readPopulationRootDomain,
		Domains: []store.KnowledgeDomain{
			{DomainID: readPopulationRootDomain, Name: "Population", Purpose: "Product law", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
			{
				DomainID: readPopulationChildDomain, Name: "Sync", Purpose: "Synchronization", Status: "current", ParentDomainID: readPopulationRootDomain,
				ArchitectureRelations: []store.KnowledgeArchitectureRelation{{Kind: "depends_on", TargetDomainID: readPopulationRootDomain, GoverningLawIDs: []string{readPopulationDecision}}},
			},
		},
	}
	if err := pm1fixture.WriteKnowledgeShards(repo, registry); err != nil {
		t.Fatalf("pm1fixture.WriteKnowledgeShards: %v", err)
	}
	gitRun(t, repo, "add", "--", ".")
	gitRun(t, repo, "commit", "--quiet", "-m", "population knowledge home")
	gitRun(t, repo, "symbolic-ref", "refs/remotes/origin/main", "refs/heads/main")
	gitRun(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	execPopulationStatement(t, fx.store, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('proj-knowledge','Population knowledge',1,'now','now')`)
	execPopulationStatement(t, fx.store, `INSERT INTO product_projects(product_id,project_id,role) VALUES('prod-alpha','proj-knowledge','secondary')`)
	execPopulationStatement(t, fx.store, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('population-knowledge-locator','proj-knowledge','canonical_path',?,?,'now','now')`, repo, repo)
	execPopulationStatement(t, fx.store, `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('prod-alpha','proj-knowledge','population-knowledge-locator')`)
	return pm1fixture.CommittedRegistryContentHash(registry)
}

// execPopulationStatement runs one fold-guarded SQL statement. Statements run
// one ExecContext each: parameter binding across a multi-statement string is
// not reliable, and a fixture that binds into the wrong statement would seed
// law the registry never declared.
func execPopulationStatement(t *testing.T, s *store.Store, statement string, args ...any) {
	t.Helper()
	ctx := context.Background()
	db := s.DatabaseForTesting()
	if _, err := db.ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, statement, args...); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
}

// seedFocusWorkflow pins the builtin implementation workflow to the focus work
// item, so the work reads have a workflow instance to project, and binds
// the work's contract to the child Domain, so the Domain active-work reads
// carry rows.
func (fx readPopulationFixture) seedFocusWorkflow(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	actor := store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-population", ActorClass: store.ActorAgent}
	ctx = agentWorkflowSourceProof(t, ctx, fx.store, fx.workID)
	if err := fx.store.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{
			WorkID: fx.workID, Actor: actor, Now: fixedTime(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	seedAgentPlanFrontier(t, fx.store, fx.workID, store.WorkflowStepPlanChange, "plan")
	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-population")
	execPopulationStatement(t, fx.store, `INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-population','agent','now')`, actorRef)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version) VALUES(?,1,'internal_sqlite','now',?,'[]',?,1)`, fx.workID, actorRef, `["`+readPopulationDecision+`"]`)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES(?,1,'prod-alpha',?,?)`, fx.workID, readPopulationChildDomain, fx.registryHash)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,?)`, fx.workID, readPopulationChildDomain)
	execPopulationStatement(t, fx.store, `UPDATE workflow_contracts SET legislative_subjects='["work-population"]' WHERE work_id=? AND contract_version=1`, fx.workID)
}

// The inspect read walks an actual Git worktree and the retained entry projection.
func (fx readPopulationFixture) seedHostCheckout(t *testing.T) {
	t.Helper()
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.email", "concord@example.invalid")
	gitRun(t, repo, "config", "user.name", "Concord Population Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# population fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", "README.md")
	gitRun(t, repo, "commit", "--quiet", "-m", "population fixture base")
	seedClaimOrigin(t, repo)
	execPopulationStatement(t, fx.store, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('population-project-repo','proj-web','canonical_path',?,?,'now','now')`, repo, repo)
	seedHostWorktree(t, fx.store, repo, fx.workID, "proj-web", gitRun(t, repo, "rev-parse", "HEAD"))
}

func (fx readPopulationFixture) envelope(t *testing.T) CallEnvelope {
	t.Helper()
	env := CallEnvelope{
		SchemaVersion: "1.0", RequestID: "population-request",
		ClientRef: fx.grant.ClientRef, PrincipalRef: fx.grant.PrincipalRef,
		SessionRef: fx.grant.SessionRef, AgentRef: fx.grant.AgentRef,
		Directory: fx.grant.Directory, Worktree: fx.grant.Worktree,
		AmbientProjectID: readPopulationProject, SelectedProductID: readPopulationProduct,
		ScopeVersion:   scopeVersionForProject(t, fx.store, readPopulationProject),
		ManifestDigest: fx.grant.ManifestDigest,
	}
	return env
}

// seedPopulationRows gives every declared primary row collection at least one
// row: a peer work item bound to the child Domain, Domain observations,
// and a relation edge. Mutations and folds run through the store's own APIs.
func (fx readPopulationFixture) seedPopulationRows(t *testing.T) {
	t.Helper()
	fx.seedPopulationPeerWork(t)
	fx.seedPopulationDomainRows(t)
}

// seedPopulationPeerWork creates a second work item bound to the child Domain
// beside the focus work, then folds a relation and claims a resource.
// The started peer gives the Domain reads a second bound contract, and the
// folded relation gives the relations read an edge.
func (fx readPopulationFixture) seedPopulationPeerWork(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := pm1fixture.SeedWorkItem(ctx, fx.store, readPopulationProject, readPopulationPeer, "Population peer", 2); err != nil {
		t.Fatalf("seed population peer work: %v", err)
	}
	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-population")
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contracts(work_id,contract_version,consequence_class,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version) VALUES(?,1,'internal_sqlite','now',?,'[]',?,1)`, readPopulationPeer, actorRef, `["`+readPopulationDecision+`"]`)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,home_domain_id,projection_hash) VALUES(?,1,'prod-alpha',?,?)`, readPopulationPeer, readPopulationChildDomain, fx.registryHash)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,?)`, readPopulationPeer, readPopulationChildDomain)

	_, focusVersion := readWorkFromStore(t, fx.store, fx.workID)
	_, peerVersion := readWorkFromStore(t, fx.store, readPopulationPeer)
	payload, err := json.Marshal(map[string]any{
		"from": fx.workID, "to": readPopulationPeer, "kind": "blocks", "reason": "population relation row",
		"expected_version": focusVersion, "resulting_version": focusVersion + 1,
		"to_expected_version": peerVersion, "to_resulting_version": peerVersion + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(ctx, fx.store, store.Operation{
		Events:           []store.Event{{EventID: "population-relation", Kind: "relation.added", SubjectType: store.SubjectWorkItem, SubjectID: fx.workID, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: payload}},
		ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, fx.workID): focusVersion, store.VersionRef(store.SubjectWorkItem, readPopulationPeer): peerVersion},
	}); err != nil {
		t.Fatalf("fold population relation: %v", err)
	}
	// The peer starts last, after the setup mutations above.
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_instances(work_id,instance_state,execution_started_at) VALUES(?,'intake','2026-08-19T00:00:00Z')`, readPopulationPeer)
	seedAgentPlanFrontier(t, fx.store, readPopulationPeer, store.WorkflowStepPlanFix, "implement")
}

// seedPopulationDomainRows records a Domain observation so observation reads
// answer rows instead of empty collections.
func (fx readPopulationFixture) seedPopulationDomainRows(t *testing.T) {
	t.Helper()
	// The recording route verifies the committed registry before its
	// transaction opens; the fold reads the Domain from that proof.
	ctx, err := fx.store.EstablishDomainRegistryProof(context.Background(), readPopulationProduct)
	if err != nil {
		t.Fatalf("verify population Domain registry: %v", err)
	}
	if err := store.ApplyOperation(ctx, fx.store, store.Operation{Events: []store.Event{{
		EventID: "population-domain-observation", Kind: "domain.observation_recorded",
		SubjectType: store.SubjectProduct, SubjectID: readPopulationProduct, Actor: "agent",
		OccurredAt: fixedTime(), PayloadVersion: 1,
		Payload: json.RawMessage(`{"observation_id":"dob:0000000000000001","product_id":"prod-alpha","domain_id":"sync","statement":"population domain observation","refs":[],"tags":[]}`),
	}}}); err != nil {
		t.Fatalf("fold population Domain observation: %v", err)
	}
}
