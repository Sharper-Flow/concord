package pm1fixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// This file carries the single-Domain evidence fixture: Concord's accepted
// registry shape, projected from a committed knowledge manifest by the live
// knowledge index rather than written in as rows. Both the agent read plane and
// the launcher store port seed from it, so neither rebuilds the same state.

const (
	// SingleDomainRootID is the one registry entry. It has no children and no
	// architecture relations, so the relation set is legitimately empty.
	SingleDomainRootID = "product-root:concord"
	// SingleDomainName and SingleDomainPurpose are that Domain's identity.
	SingleDomainName    = "Concord"
	SingleDomainPurpose = "Product law"
	// DomainEvidenceLawID is the accepted decision homed to the root Domain.
	DomainEvidenceLawID = "CD-0041"
	// DomainEvidenceLawPath is the accepted decision's committed path.
	DomainEvidenceLawPath = "docs/decisions/CD-0041.md"
	// DomainEvidenceLawTitle is its manifest title.
	DomainEvidenceLawTitle = "Domain authority"
	// DomainEvidenceLawBody is the committed blob behind DomainEvidenceLawPath.
	DomainEvidenceLawBody = "Domains are the only architecture authority.\n"

	domainEvidenceProductKey     = "concord"
	domainEvidenceSupersededID   = "CD-0002"
	domainEvidenceSupersededPath = "docs/decisions/CD-0002.md"
	domainEvidenceSupersededBody = "Components were the prior architecture authority.\n"
)

// DomainEvidenceTime is the occurrence time every seeded event carries, so
// repeated runs produce identical rows.
var DomainEvidenceTime = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

// DomainEvidenceOptions names the identities the projected registry binds to.
// Every field is required: the fixture never invents an identity the caller did
// not ask for.
type DomainEvidenceOptions struct {
	// Dir is a caller-owned directory that receives the manifest Git repo.
	Dir string
	// ProductID owns the projected registry.
	ProductID string
	// ProjectID is the knowledge home Project.
	ProjectID string
	// LocatorID names the canonical-path locator for that home.
	LocatorID string
	// WorkIDs are existing work items bound to the root Domain by a
	// nonterminal contract. Two or more produce an unresolved overlap pair.
	WorkIDs []string
}

func (o DomainEvidenceOptions) validate() error {
	missing := ""
	switch {
	case o.Dir == "":
		missing = "Dir"
	case o.ProductID == "":
		missing = "ProductID"
	case o.ProjectID == "":
		missing = "ProjectID"
	case o.LocatorID == "":
		missing = "LocatorID"
	case len(o.WorkIDs) == 0:
		missing = "WorkIDs"
	}
	if missing != "" {
		return fmt.Errorf("pm1fixture: DomainEvidenceOptions.%s is required", missing)
	}
	return nil
}

// ContentDigest is the manifest's content hash form for a committed blob.
func ContentDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SeedProductAndProject creates the Product, the Project, and the membership
// between them through the event log.
func SeedProductAndProject(ctx context.Context, s *store.Store, productID, projectID string) error {
	membership := fmt.Sprintf(`{"product_id":%q,"project_id":%q,"role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`, productID, projectID)
	events := []store.Event{
		{EventID: "domain-evidence-product-" + productID, Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: productID, Actor: "operator", OccurredAt: DomainEvidenceTime, PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Product","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "domain-evidence-project-" + projectID, Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: projectID, Actor: "operator", OccurredAt: DomainEvidenceTime, PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Project"}`)},
		{EventID: "domain-evidence-membership-" + productID + "-" + projectID, Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: productID, Actor: "operator", OccurredAt: DomainEvidenceTime, PayloadVersion: 1, Payload: json.RawMessage(membership)},
	}
	versions := map[store.SubjectRef]int64{
		store.VersionRef(store.SubjectProduct, productID): 0,
		store.VersionRef(store.SubjectProject, projectID): 0,
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: versions}); err != nil {
		return fmt.Errorf("pm1fixture: seed Product and Project: %w", err)
	}
	return nil
}

// SeedWorkItem creates one work item and its primary Project membership through
// the event log.
func SeedWorkItem(ctx context.Context, s *store.Store, projectID, workID, title string, priority int) error {
	created := fmt.Sprintf(`{"work_kind":"task","title":%q,"priority":%d}`, title, priority)
	membership := fmt.Sprintf(`{"memberships":[{"project_id":%q,"role":"primary"}],"expected_version":1,"resulting_version":2}`, projectID)
	events := []store.Event{
		{EventID: "domain-evidence-work-" + workID, Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: DomainEvidenceTime, PayloadVersion: 2, Payload: json.RawMessage(created)},
		{EventID: "domain-evidence-work-" + workID + "-membership", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: DomainEvidenceTime, PayloadVersion: 1, Payload: json.RawMessage(membership)},
	}
	versions := map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, workID): 0}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: versions}); err != nil {
		return fmt.Errorf("pm1fixture: seed work %s: %w", workID, err)
	}
	return nil
}

// SeedDomainEvidence projects the single-Domain registry for opts.ProductID
// from a freshly committed manifest, then binds a nonterminal contract per work
// item to the root Domain and attaches the home Project to it.
func SeedDomainEvidence(ctx context.Context, s *store.Store, opts DomainEvidenceOptions) error {
	if err := opts.validate(); err != nil {
		return err
	}
	repo, err := DomainEvidenceRepo(opts.Dir)
	if err != nil {
		return err
	}
	if err := execDomainFold(ctx, s,
		domainStatement{"knowledge locator", `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?,'canonical_path',?,?,'now','now')`, []any{opts.LocatorID, opts.ProjectID, repo, repo}},
		domainStatement{"knowledge home", `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES(?,?,?)`, []any{opts.ProductID, opts.ProjectID, opts.LocatorID}},
	); err != nil {
		return err
	}
	home := store.KnowledgeHome{HomeProjectID: opts.ProjectID, HomeLocatorID: opts.LocatorID, RepoPath: repo, HeadRef: "HEAD"}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		return fmt.Errorf("pm1fixture: rebuild knowledge index: %w", err)
	}

	actorRef := store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-1")
	statements := []domainStatement{
		{"workflow actor", `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'human-1','client-1','agent-1','session-1','operator','now')`, []any{actorRef}},
	}
	const contract = `INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES(?,1,'domain evidence','check','{"kind":"check"}','internal_sqlite','[]','[]','now',?,'[]','["` + DomainEvidenceLawID + `"]',1,'prototype_internal')`
	const binding = `INSERT INTO workflow_architecture_bindings(work_id,contract_version,product_id,domain_registry_content_hash,home_domain_id,projection_hash) SELECT ?,1,?,content_hash,?,content_hash FROM domain_registries WHERE product_id=?`
	for _, workID := range opts.WorkIDs {
		statements = append(statements,
			domainStatement{"contract " + workID, contract, []any{workID, actorRef}},
			domainStatement{"architecture binding " + workID, binding, []any{workID, opts.ProductID, SingleDomainRootID, opts.ProductID}},
			domainStatement{"affected Domain " + workID, `INSERT INTO workflow_contract_affected_domains(work_id,contract_version,domain_id) VALUES(?,1,?)`, []any{workID, SingleDomainRootID}},
			domainStatement{"law modification " + workID, `INSERT INTO workflow_contract_law_modifications(work_id,contract_version,law_id) VALUES(?,1,?)`, []any{workID, DomainEvidenceLawID}},
		)
	}
	if err := execDomainFold(ctx, s, statements...); err != nil {
		return err
	}

	attachments := store.DomainProjectAttachmentsRequest{EventID: "domain-evidence-project-edges", ProductID: opts.ProductID, DomainID: SingleDomainRootID, ExpectedVersion: 0, Attachments: []store.DomainProjectAttachment{{ProjectID: opts.ProjectID, Role: "primary"}}, Actor: "operator", OccurredAt: DomainEvidenceTime}
	if err := store.ReplaceDomainProjectAttachments(ctx, s, attachments); err != nil {
		return fmt.Errorf("pm1fixture: seed Project attachments: %w", err)
	}
	return nil
}

// DomainEvidenceRepo commits a knowledge manifest declaring exactly one root
// Domain with an empty architecture relation set, plus an accepted decision
// homed to that Domain and the superseded decision it replaced.
func DomainEvidenceRepo(dir string) (string, error) {
	repo, err := initKnowledgeRepo(dir)
	if err != nil {
		return "", fmt.Errorf("pm1fixture: init Domain evidence repo: %w", err)
	}
	if err := writeKnowledgeFile(repo, DomainEvidenceLawPath, DomainEvidenceLawBody); err != nil {
		return "", fmt.Errorf("pm1fixture: write accepted decision: %w", err)
	}
	if err := writeKnowledgeFile(repo, domainEvidenceSupersededPath, domainEvidenceSupersededBody); err != nil {
		return "", fmt.Errorf("pm1fixture: write superseded decision: %w", err)
	}
	manifest := store.KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"decision"},
		IndexedKinds:   []string{"decision"},
		DomainRegistry: store.KnowledgeDomainRegistry{
			SchemaVersion: "1.0",
			ProductKey:    domainEvidenceProductKey,
			RootDomainID:  SingleDomainRootID,
			Domains: []store.KnowledgeDomain{
				{DomainID: SingleDomainRootID, Name: SingleDomainName, Purpose: SingleDomainPurpose, Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}},
			},
		},
		Records: []store.KnowledgeRecord{
			{
				ID: DomainEvidenceLawID, Kind: "decision", Path: DomainEvidenceLawPath, Status: "accepted", Date: "2026-08-18T00:00:00Z",
				Title: DomainEvidenceLawTitle, Summary: "Domains carry architecture authority", Tags: []string{},
				LawRelations: []store.KnowledgeRelation{{Kind: "supersedes", TargetID: domainEvidenceSupersededID}},
				Scopes:       store.KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{SingleDomainRootID}, TagIDs: []string{}},
				HomeDomainID: SingleDomainRootID, SHA256: ContentDigest(DomainEvidenceLawBody),
			},
			{
				ID: domainEvidenceSupersededID, Kind: "decision", Path: domainEvidenceSupersededPath, Status: "superseded", Date: "2026-08-17T00:00:00Z",
				Title: "Component authority", Summary: "Retired architecture authority", Tags: []string{}, Successor: DomainEvidenceLawID,
				Scopes:       store.KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{SingleDomainRootID}, TagIDs: []string{}},
				HomeDomainID: SingleDomainRootID, SHA256: ContentDigest(domainEvidenceSupersededBody),
			},
		},
	}
	encoded, err := encodeDomainManifest(manifest)
	if err != nil {
		return "", err
	}
	if err := writeKnowledgeFile(repo, "docs/concord-knowledge-index.v1.json", encoded); err != nil {
		return "", fmt.Errorf("pm1fixture: write Domain manifest: %w", err)
	}
	if _, err := commitKnowledgeRepo(repo, "domain evidence knowledge"); err != nil {
		return "", fmt.Errorf("pm1fixture: commit Domain evidence repo: %w", err)
	}
	return repo, nil
}

// encodeDomainManifest drops the retired component_ids scope key, which the
// v1.2 manifest schema rejects, from the marshalled record scopes.
func encodeDomainManifest(manifest store.KnowledgeManifest) (string, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("pm1fixture: marshal Domain manifest: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return "", fmt.Errorf("pm1fixture: decode Domain manifest: %w", err)
	}
	records, ok := object["records"].([]any)
	if !ok {
		return "", fmt.Errorf("pm1fixture: Domain manifest records are not a list")
	}
	for _, record := range records {
		scopes, ok := record.(map[string]any)["scopes"].(map[string]any)
		if !ok {
			return "", fmt.Errorf("pm1fixture: Domain manifest record has no scopes object")
		}
		delete(scopes, "component_ids")
	}
	out, err := json.Marshal(object)
	if err != nil {
		return "", fmt.Errorf("pm1fixture: re-encode Domain manifest: %w", err)
	}
	return string(out) + "\n", nil
}

type domainStatement struct {
	name  string
	query string
	args  []any
}

// execDomainFold applies raw projection seeding inside one fold-guarded
// transaction. The guard is what the projection tables require for direct
// writes.
func execDomainFold(ctx context.Context, s *store.Store, statements ...domainStatement) error {
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pm1fixture: begin Domain fixture transaction: %w", err)
	}
	defer tx.Rollback()
	all := append([]domainStatement{{"fold guard", `INSERT INTO fold_guard(active) VALUES(1)`, nil}}, statements...)
	all = append(all, domainStatement{"leave fold", `DELETE FROM fold_guard`, nil})
	for _, statement := range all {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return fmt.Errorf("pm1fixture: seed %s: %w", statement.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pm1fixture: commit Domain fixture transaction: %w", err)
	}
	return nil
}
