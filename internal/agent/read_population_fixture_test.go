package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/pm1fixture"
	"github.com/sharper-flow/concord/internal/store"
)

// The read population fixture exists so the agent read surface can be
// dispatched against a store that looks like a working operator store rather
// than a single-row stub. At 100 Products and 700 work items, each read family
// gets the rows its result schema declares: Domains carrying decision, spec,
// and constitution law, an Initiative with entries, knowledge notes and law in
// a committed Git home, work and Domain observations, a research pack, and a
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
	readPopulationLesson        = "knowledge-lesson-population"
	readPopulationResource      = "population-queue"

	readPopulationProducts       = 100
	readPopulationWorkPerProduct = 7
)

// readPopulationBlockedSessionRef is the session_ref of the active approval
// challenge the fixture seeds. The blocked_sessions witness ties its routing
// detail to this row rather than any session the producer happened to carry.
const readPopulationBlockedSessionRef = "session-population"

type readPopulationFixture struct {
	store    *store.Store
	service  *Service
	grant    Authority
	workID   string
	domainID string
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
	fx.seedKnowledgeHome(t)
	fx.seedFocusWorkflow(t)
	fx.seedResearchPack(t)

	fx.service, _, fx.grant = newAuthorizedService(t, s, "client-1", "human-1",
		[]Capability{"product_read", "work_define", "work_transition", "work_relate"},
		[]string{readPopulationProduct}, []string{readPopulationProject},
		store.ProjectResolution{ProjectID: readPopulationProject})
	fx.grant.SessionRef = "session-population"
	fx.grant.AgentRef = "agent-1"
	// The Initiative entry and every other event-folded relation derive its
	// identity from the relation-creating event count, so the bulk-seeded
	// blocker edges must land only after the last folded relation, or they
	// would occupy the identity the fold assigns.
	fx.seedWorktreeClaim(t)
	fx.seedPopulationRows(t)
	seedReadPopulationProducts(t, s)
	return fx
}

// seedReadPopulationProducts bulk-inserts the population shape: 100
// Products, two Projects each, seven work items per Product, memberships,
// blocker edges, and one workflow instance per Product.
func seedReadPopulationProducts(t *testing.T, s *store.Store) {
	t.Helper()
	definition, err := store.BuiltinWorkflowDefinitionForRef("workflow.implementation")
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
			fmt.Fprintf(&statements, "INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at,terminal_time) VALUES('%s','%s','Perf work %03d-%02d','%s',%d,1,'2026-08-%02dT00:00:00Z','2026-08-%02dT00:00:00Z',%s);", workID, kind, product, work, lifecycle, work+1, (work%8)+1, (work%8)+1, terminal)
			fmt.Fprintf(&statements, "INSERT INTO work_projects(work_id,project_id,role) VALUES('%s','%s','primary'),('%s','%s','secondary');", workID, projectPrimary, workID, projectSecondary)
			if work == 0 {
				fmt.Fprintf(&statements, "INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,started_at) VALUES('%s','%s',%d,'%s','planning','ready','2026-08-01T00:00:00Z');", workID, definition.Definition.Ref, definition.Definition.Version, definition.Digest)
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
// work-scoped read targets, plus observations so the observation reads and the
// continuity snapshot carry rows rather than empty pages.
func seedReadPopulationFocusWork(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	events := []store.Event{
		{EventID: "population-work-create", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: readPopulationWork, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: store.CurrentEventPayloadVersion("work.created"), Payload: json.RawMessage(`{"work_kind":"task","title":"Population focus","priority":1}`)},
		{EventID: "population-work-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: readPopulationWork, Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"proj-web","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, readPopulationWork): 0}}); err != nil {
		t.Fatal(err)
	}
	observationEvents := make([]store.Event, 0, 3)
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("obs:%016x", i+1)
		payload, _ := json.Marshal(map[string]any{
			"observation_id": id,
			"statement":      fmt.Sprintf("Population observation %d", i+1),
			"refs":           []string{},
			"tags":           []string{},
		})
		observationEvents = append(observationEvents, store.Event{EventID: fmt.Sprintf("population-observation-%d", i+1), Kind: "work.observation_recorded", SubjectType: store.SubjectWorkItem, SubjectID: readPopulationWork, Actor: "operator", OccurredAt: fixedTime().Add(+1), PayloadVersion: 1, Payload: payload})
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: observationEvents}); err != nil {
		t.Fatal(err)
	}
}

// seedKnowledgeHome commits a manifest whose Domain registry carries a root
// and a child Domain, whose child hosts accepted constitution, decision, and
// spec law with applicability out to the root, and whose lesson is an indexed
// knowledge note scoped to the focus Product. The home lives on its own
// Project so the focus Project's canonical-path locator stays unambiguous for
// the worktree claim.
func (fx readPopulationFixture) seedKnowledgeHome(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	repo, err := os.MkdirTemp(t.TempDir(), "population-knowledge-")
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "init", "--initial-branch=main")
	gitRun(t, repo, "config", "user.email", "concord@example.invalid")
	gitRun(t, repo, "config", "user.name", "Concord Population Test")

	const constitutionPath = ".concord/docs/constitution.md"
	const decisionPath = ".concord/docs/decisions/" + readPopulationDecision + ".md"
	const specPath = ".concord/docs/specs/" + readPopulationSpec + ".md"
	const lessonPath = ".concord/docs/lessons/population-lesson.md"
	bodies := map[string]string{
		constitutionPath: "The child Domain owns population constitution law.\n",
		decisionPath:     "The child Domain owns synchronization decisions.\n",
		specPath:         "The child Domain owns the synchronization specification.\n",
		lessonPath: "---\n" +
			"id: " + readPopulationLesson + "\n" +
			"type: lesson\n" +
			"title: Durable lesson\n" +
			"completed_at: 2026-09-01T00:00:00Z\n" +
			"outcome_tag: published\n" +
			"lesson_tags: [population]\n" +
			"terminal_state: completed\n" +
			"priority: 0\n" +
			"summary: Durable summary\n" +
			"product_ids: [" + readPopulationProduct + "]\n" +
			"project_ids: []\n" +
			"domain_ids: [" + readPopulationChildDomain + "]\n" +
			"tag_ids: [population]\n" +
			"---\n\nDurable knowledge.\n",
	}
	for path, body := range bodies {
		full := filepath.Join(repo, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scopes := func(domainIDs ...string) store.KnowledgeRecordScopes {
		return store.KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: domainIDs, TagIDs: []string{}}
	}
	legislated := store.KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-0f1e2d3c4b5a6978", ContractVersion: 1}
	manifest := store.KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"constitution", "decision", "spec", "lesson"},
		IndexedKinds:   []string{"constitution", "decision", "spec", "lesson"},
		DomainRegistry: store.KnowledgeDomainRegistry{
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
		},
		Records: []store.KnowledgeRecord{
			{
				ID: readPopulationConstitution, Kind: "constitution", Path: constitutionPath, Status: "accepted",
				Date: "2026-09-01T00:00:00Z", Title: "Population constitution", Summary: "The child Domain owns population constitution law", Tags: []string{},
				Authority: legislated, Scopes: scopes(readPopulationChildDomain),
				HomeDomainID: readPopulationChildDomain, AppliesToDomainIDs: []string{readPopulationRootDomain}, SHA256: pm1fixture.ContentDigest(bodies[constitutionPath]),
			},
			{
				ID: readPopulationDecision, Kind: "decision", Path: decisionPath, Status: "accepted",
				Date: "2026-09-01T00:00:00Z", Title: "Synchronization authority", Summary: "The child Domain owns synchronization decisions", Tags: []string{},
				Authority: legislated, Scopes: scopes(readPopulationChildDomain),
				HomeDomainID: readPopulationChildDomain, AppliesToDomainIDs: []string{readPopulationRootDomain}, SHA256: pm1fixture.ContentDigest(bodies[decisionPath]),
			},
			{
				ID: readPopulationSpec, Kind: "spec", Path: specPath, Status: "accepted",
				Date: "2026-09-01T00:00:00Z", Title: "Synchronization specification", Summary: "The child Domain owns the synchronization specification", Tags: []string{},
				Authority: store.KnowledgeAuthority{Tier: "derived"}, Scopes: scopes(readPopulationChildDomain),
				HomeDomainID: readPopulationChildDomain, AppliesToDomainIDs: []string{readPopulationRootDomain}, SHA256: pm1fixture.ContentDigest(bodies[specPath]),
			},
			{
				ID: readPopulationLesson, Kind: "lesson", Path: lessonPath, Status: "published",
				Date: "2026-09-01T00:00:00Z", Title: "Durable lesson", Summary: "Durable summary", Tags: []string{"population"},
				Authority: store.KnowledgeAuthority{Tier: "derived"},
				Scopes:    store.KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{readPopulationProduct}, ProjectIDs: []string{}, DomainIDs: []string{readPopulationChildDomain}, TagIDs: []string{"population"}},
				SHA256:    pm1fixture.ContentDigest(bodies[lessonPath]),
			},
		},
	}
	if err := pm1fixture.WriteKnowledgeShards(repo, manifest); err != nil {
		t.Fatalf("pm1fixture.WriteKnowledgeShards: %v", err)
	}
	gitRun(t, repo, "add", "--", ".")
	gitRun(t, repo, "commit", "--quiet", "-m", "population knowledge home")

	execPopulationStatement(t, fx.store, `INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES('proj-knowledge','Population knowledge',1,'now','now')`)
	execPopulationStatement(t, fx.store, `INSERT INTO product_projects(product_id,project_id,role) VALUES('prod-alpha','proj-knowledge','secondary')`)
	execPopulationStatement(t, fx.store, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES('population-knowledge-locator','proj-knowledge','canonical_path',?,?,'now','now')`, repo, repo)
	execPopulationStatement(t, fx.store, `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES('prod-alpha','proj-knowledge','population-knowledge-locator')`)
	home := store.KnowledgeHome{HomeProjectID: readPopulationKnowledgeHome, HomeLocatorID: "population-knowledge-locator", RepoPath: repo, HeadRef: "HEAD"}
	if err := fx.store.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatalf("rebuild population knowledge index: %v", err)
	}
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
// item, so the continuity read has a workflow instance to snapshot, and binds
// the work's contract to the child Domain, so the Domain active-work and
// overlap reads carry rows.
func (fx readPopulationFixture) seedFocusWorkflow(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	registered, err := store.BuiltinWorkflowRegistry().Register(store.BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: "human-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-population", ActorClass: store.ActorAgent}
	if err := fx.store.Transact(ctx, func(tx *store.Transaction) error {
		return store.InitializeWorkflowTx(ctx, tx, store.WorkflowInitializationRequest{
			WorkID: fx.workID, Definition: registered, Actor: actor, Now: fixedTime(),
		})
	}); err != nil {
		t.Fatal(err)
	}
	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-population")
	execPopulationStatement(t, fx.store, `INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-population','agent','now')`, actorRef)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'population focus','internal_sqlite','[]','[]','now',?,'[]',?,1,'prototype_internal')`, fx.workID, actorRef, `["`+readPopulationDecision+`"]`)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) SELECT ?,1,'prod-alpha',content_hash,?,content_hash FROM domain_registries WHERE product_id='prod-alpha'`, fx.workID, readPopulationChildDomain)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,?)`, fx.workID, readPopulationChildDomain)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES(?,1,?)`, fx.workID, readPopulationDecision)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,1,'predicate:population',0,'exists','{"kind":"exists","surface":"go_test:internal/agent","subjects":["work-population"]}')`, fx.workID)
}

// seedResearchPack authors a research pack through the store's own mutation
// API and binds it to the focus work item, so the research read resolves by
// owner.
func (fx readPopulationFixture) seedResearchPack(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	pack, err := createResearchPack(ctx, fx.store, store.CreateResearchPackRequest{
		OwnerWorkID: fx.workID, Freshness: store.ResearchCurrent,
		Revision: store.ResearchRevisionInput{
			Question: "does every read hold at population scale?",
			ScopeIn:  json.RawMessage(`[]`), ScopeOut: json.RawMessage(`[]`), DoneWhen: json.RawMessage(`[]`),
			Method: "source_code",
		},
	})
	if err != nil {
		t.Fatalf("create research pack: %v", err)
	}
	if err := recordResearchSource(ctx, fx.store, store.ResearchSourceRequest{
		PackID: pack.PackID, ExpectedVersion: 1,
		Source: store.ResearchSource{
			SourceID: "source-population", Kind: store.SourceCode, Locator: "internal/agent/result_payload_population_test.go",
			Title: "read population fixture", PublisherOrAuthor: "concord",
			PublishedAt: "2026-09-01T00:00:00Z", AccessedAt: "2026-09-02T00:00:00Z",
		},
	}); err != nil {
		t.Fatalf("add research source: %v", err)
	}
	if err := recordResearchFinding(ctx, fx.store, store.ResearchFindingRequest{
		PackID: pack.PackID, ExpectedVersion: 2,
		Finding: store.ResearchFinding{
			FindingID: "finding-population", Kind: store.FindingObservation, Statement: "the population fixture answers every read",
			Confidence: store.ConfidenceHigh, Freshness: store.ResearchCurrent, Status: store.FindingActive,
		},
	}); err != nil {
		t.Fatalf("add research finding: %v", err)
	}
}

// seedWorktreeClaim registers the focus Project's source repository and claims
// the focus work's canonical worktree through the real dispatch path, so the
// inspect read walks an actual Git worktree.
func (fx readPopulationFixture) seedWorktreeClaim(t *testing.T) {
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
	_, version := readWorkFromStore(t, fx.store, fx.workID)
	claim := dispatchMutation(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_transition", Operation: "worktree_claim", Input: json.RawMessage(`{"work_id":"` + fx.workID + `","project_id":"proj-web","base_sha":"` + gitRun(t, repo, "rev-parse", "HEAD") + `","expected_version":` + fmt.Sprintf("%d", version) + `,"host_pid":` + strconv.Itoa(os.Getpid()) + `,"idempotency_key":"population-claim"}`)}, fx.envelope(t))
	if claim.Outcome != OutcomeOK {
		t.Fatalf("worktree claim outcome=%s err=%+v", claim.Outcome, claim.Error)
	}
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
// row: a peer work item bound to the child Domain, Domain attachments and
// observations, a relation edge, a peer message, a resource claim, an external
// observation, an active approval challenge, a summary context boundary, and
// knowledge records in the knowledge home. Mutations and folds run
// through the store's own APIs; the two fold-guarded SQL statements seed
// authorization-shaped rows no public store API authors, mirroring the store
// fixtures the bulk Product rows and workflow contracts already follow.
func (fx readPopulationFixture) seedPopulationRows(t *testing.T) {
	t.Helper()
	fx.seedPopulationPeerWork(t)
	fx.seedPopulationDomainRows(t)
	fx.seedPopulationFocusRows(t)
}

// seedPopulationPeerWork creates a second work item bound to the child Domain
// beside the focus work, then folds a relation, messages it, and claims a resource.
// The shared law write and started peer give the Domain overlap read a pair,
// and the folded relation gives the relations read an edge.
func (fx readPopulationFixture) seedPopulationPeerWork(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := pm1fixture.SeedWorkItem(ctx, fx.store, readPopulationProject, readPopulationPeer, "Population peer", 2); err != nil {
		t.Fatalf("seed population peer work: %v", err)
	}
	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-population")
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'population peer','internal_sqlite','[]','[]','now',?,'[]',?,1,'prototype_internal')`, readPopulationPeer, actorRef, `["`+readPopulationDecision+`"]`)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) SELECT ?,1,'prod-alpha',content_hash,?,content_hash FROM domain_registries WHERE product_id='prod-alpha'`, readPopulationPeer, readPopulationChildDomain)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,?)`, readPopulationPeer, readPopulationChildDomain)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES(?,1,?)`, readPopulationPeer, readPopulationDecision)

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
	_, peerVersion = readWorkFromStore(t, fx.store, readPopulationPeer)
	send := dispatchMutation(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_relate", Operation: "message_send", Input: json.RawMessage(`{"work_id":"` + readPopulationPeer + `","recipient_work_id":"` + fx.workID + `","body":"population message row","expected_version":` + strconv.FormatInt(peerVersion, 10) + `,"idempotency_key":"population-message"}`)}, fx.envelope(t))
	if send.Outcome != OutcomeOK {
		t.Fatalf("population message_send outcome=%s err=%+v", send.Outcome, send.Error)
	}
	_, focusVersion = readWorkFromStore(t, fx.store, fx.workID)
	claim := dispatchMutation(t, fx.store, fx.service, InvokeRequest{Tool: "concord_work_relate", Operation: "resource_claim", Input: json.RawMessage(`{"work_id":"` + fx.workID + `","resource_key":"queue:population-claim","reason":"population claim row","expected_version":` + strconv.FormatInt(focusVersion, 10) + `,"idempotency_key":"population-claim-row"}`)}, fx.envelope(t))
	if claim.Outcome != OutcomeOK {
		t.Fatalf("population resource_claim outcome=%s err=%+v", claim.Outcome, claim.Error)
	}
	// The unresolved overlap must follow setup mutations that enforce admission.
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_instances(work_id,definition_ref,definition_version,definition_digest,current_step,instance_state,execution_started_at) VALUES(?,'workflow.break_fix',9,?,'repair','running','2026-08-19T00:00:00Z')`, readPopulationPeer, breakFixFixtureDigest(t))
}

// seedPopulationDomainRows attaches the home Project and one managed resource
// to the child Domain and records a Domain observation, so the Domain
// attachment and observation reads answer rows instead of empty collections.
func (fx readPopulationFixture) seedPopulationDomainRows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.CreateManagedResource(ctx, fx.store, store.ManagedResourceCreateRequest{
		EventID: "population-resource", ResourceID: readPopulationResource, ProductID: readPopulationProduct,
		DisplayName: "Population queue", Class: "infrastructure", Kind: "queue", Purpose: "dispatches population work",
		StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"},
		MetadataSchemaVersion: "1", Metadata: json.RawMessage(`{}`), ExpectedProductVersion: 0,
		Actor: "operator", OccurredAt: fixedTime(),
	}); err != nil {
		t.Fatalf("create population managed resource: %v", err)
	}
	if err := store.ReplaceDomainProjectAttachments(ctx, fx.store, store.DomainProjectAttachmentsRequest{
		EventID: "population-project-edges", ProductID: readPopulationProduct, DomainID: readPopulationChildDomain,
		ExpectedVersion: 0, Attachments: []store.DomainProjectAttachment{{ProjectID: readPopulationProject, Role: "primary"}},
		Actor: "operator", OccurredAt: fixedTime(),
	}); err != nil {
		t.Fatalf("seed population Project attachments: %v", err)
	}
	if err := store.ReplaceDomainResourceAttachments(ctx, fx.store, store.DomainResourceAttachmentsRequest{
		EventID: "population-resource-edges", ProductID: readPopulationProduct, DomainID: readPopulationChildDomain,
		ExpectedVersion: 0, Attachments: []store.DomainResourceAttachment{{ResourceID: readPopulationResource, Purpose: "dispatches population work", Environments: []string{"production"}}},
		Actor: "operator", OccurredAt: fixedTime().Add(1),
	}); err != nil {
		t.Fatalf("seed population resource attachments: %v", err)
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

// seedPopulationFocusRows captures one external observation for the focus work
// through the store's fold API, then seeds the two rows no public store API
// authors: an active approval challenge for the blocked-session read and a
// summary context boundary for the continuity read. Both mirror the store's
// own fixtures for those tables.
func (fx readPopulationFixture) seedPopulationFocusRows(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	policy, ok := store.ExternalSubjectPolicyFor("environment")
	if !ok {
		t.Fatal("no reviewed external subject policy for environment")
	}
	if err := fx.store.Transact(ctx, func(tx *store.Transaction) error {
		return store.AppendExternalObservationCaptureTx(ctx, tx, fx.workID, "human-1", fixedTime(), store.ExternalObservationCapture{
			ObservationID:         "xobs:0123456789abcdef",
			SubjectKind:           "environment",
			SubjectRef:            "environment://prod",
			CaptureMethod:         store.CaptureTrustedClientReport,
			CapturedAt:            "2026-09-01T00:00:00Z",
			ReportingAuthorityRef: "client:reporter-1",
			ObservedUniverse: store.ObservedUniverse{
				Shape: store.UniverseCollection, AppliedScope: "provider:services(env=prod)",
				AnchorToken: "page-1", Coverage: store.CoveragePartial,
				ObservedRefs: []string{"svc-a", "svc-b", "svc-c"},
				TotalKind:    store.TotalGte, TotalValue: 4,
				CanonicalIdentityKey: "service_name",
				Omissions:            []string{"pagination-truncated:page-2"},
			},
			FreshnessPolicyRef:  store.PolicyRef(policy),
			DivergencePolicyRef: store.PolicyRef(policy),
		})
	}); err != nil {
		t.Fatalf("capture population external observation: %v", err)
	}

	issued := time.Now().UTC().Add(-time.Hour)
	expires := time.Now().UTC().Add(24 * time.Hour)
	execPopulationStatement(t, fx.store, `INSERT INTO agent_approval_challenges(challenge_ref,client_ref,principal_ref,session_ref,agent_ref,directory,worktree,product_scope_json,operation_digest,scope_json,version_json,consequence,host_assertion_digest,issued_at,expires_at,status,consumed_at,max_uses,used_count) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		strings.Repeat("ab", 32), "client-1", "human-1", readPopulationBlockedSessionRef, "agent-1", "/repo", "/repo/wt-population",
		`["prod-alpha"]`, "sha256:"+strings.Repeat("2", 64), "{}", "{}", "lifecycle", "sha256:"+strings.Repeat("1", 64),
		issued.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), "active", nil, 1, 0)

	registered, err := store.BuiltinWorkflowRegistry().Register(store.BuiltinWorkflowDefinitions()[0])
	if err != nil {
		t.Fatal(err)
	}
	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-population")
	_, focusVersion := readWorkFromStore(t, fx.store, fx.workID)
	execPopulationStatement(t, fx.store, `INSERT INTO workflow_context_boundaries(work_id,work_version,boundary_sequence,boundary_count,boundary_id,boundary_kind,checkpoint_id,checkpoint_sequence,attempt_epoch,summary,workflow_ref,workflow_definition_version,workflow_definition_digest,actor_ref,request_id,recorded_at) VALUES(?,?,1,1,'population-boundary','summary','population-checkpoint:context-checkpoint',1,1,'population summary boundary',?,?,?,?,'request:population','2026-09-01T00:00:00Z')`,
		fx.workID, focusVersion, registered.Definition.Ref, registered.Definition.Version, registered.Digest, actorRef)
}
