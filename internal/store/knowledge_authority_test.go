package store

// Public knowledge reads and rebuilds resolve exact approved contract versions
// while preserving the frozen default cohort and unknown-work standing.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// authoritySyntheticWork is the synthetic hex work id fixture manifests name
// when a test controls the approving contract itself.
const authoritySyntheticWork = "work-0f1e2d3c4b5a6978"

// authoritySecondSyntheticWork names a second independent work for scenarios
// that hold two declared references at once.
const authoritySecondSyntheticWork = "work-aabbccdd11223344"

// authorityActorRef satisfies the workflow_actors key shape the contract
// approval column references.
var authorityActorRef = "actor:" + strings.Repeat("a", 64)

type committedAuthorityDefaults struct {
	VehicleWorkID  string
	VehicleVersion int64
	MemberID       string
	MemberKind     string
}

// readCommittedAuthorityDefaults reads the vehicle and one frozen member out
// of the committed cohort asset, so the tests track the one committed list
// instead of restating it.
func readCommittedAuthorityDefaults(t *testing.T) committedAuthorityDefaults {
	t.Helper()
	raw, err := os.ReadFile("knowledge_authority_defaults.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Vehicle struct {
			WorkID          string `json:"work_id"`
			ContractVersion int64  `json:"contract_version"`
		} `json:"vehicle"`
		Members []struct {
			RecordID string `json:"record_id"`
			Kind     string `json:"kind"`
		} `json:"members"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document.Vehicle.WorkID == "" || document.Vehicle.ContractVersion < 1 || len(document.Members) == 0 {
		t.Fatalf("committed cohort asset is not in the expected shape: vehicle=%+v members=%d", document.Vehicle, len(document.Members))
	}
	return committedAuthorityDefaults{
		VehicleWorkID:  document.Vehicle.WorkID,
		VehicleVersion: document.Vehicle.ContractVersion,
		MemberID:       document.Members[0].RecordID,
		MemberKind:     document.Members[0].Kind,
	}
}

// authorityCohortManifest builds a schema 1.3 manifest whose single decision
// record declares the given legislative reference.
func authorityCohortManifest(t *testing.T, legislatedBy, recordID string, version int64) []byte {
	t.Helper()
	document := map[string]any{
		"schema_version":  "1.3",
		"supported_kinds": sortedKnowledgeKinds(),
		"indexed_kinds":   fixtureIndexedKinds(),
		"domain_registry": map[string]any{
			"schema_version": "1.0", "product_key": "authority-fixture", "root_domain_id": "product-root:authority-fixture",
			"domains": []any{map[string]any{"domain_id": "product-root:authority-fixture", "name": "Authority fixture", "purpose": "fixture registry", "status": "current", "architecture_relations": []any{}}},
		},
		"records": []any{map[string]any{
			"id": recordID, "kind": "decision", "path": ".concord/docs/decisions/" + recordID + ".md", "status": "accepted",
			"date": "2026-10-11T00:00:00Z", "title": "Authority fixture", "summary": "Fixture law for the authority resolver.",
			"tags":                   []string{},
			"authority":              map[string]any{"tier": "legislated", "legislated_by": legislatedBy, "contract_version": version},
			"scopes":                 map[string]any{"mode": "home", "product_ids": []string{}, "project_ids": []string{}, "domain_ids": []string{}, "tag_ids": []string{}},
			"home_domain_id":         "product-root:authority-fixture",
			"product_wide_rationale": "Fixture law binds every child Domain.",
			"sha256":                 "sha256:" + strings.Repeat("a", 64),
		}},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestKnowledgeAuthorityDefaultRefusesOutOfCohortVehicleClaim(t *testing.T) {
	t.Parallel()
	defaults := readCommittedAuthorityDefaults(t)
	_, err := parseKnowledgeManifest(authorityCohortManifest(t, defaults.VehicleWorkID, "CD-9999", defaults.VehicleVersion))
	if err == nil {
		t.Fatal("a vehicle claim for a record outside the frozen cohort must refuse")
	}
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestKnowledgeAuthorityDefaultRefusesWrongVehicleVersion(t *testing.T) {
	t.Parallel()
	defaults := readCommittedAuthorityDefaults(t)
	_, err := parseKnowledgeManifest(authorityCohortManifest(t, defaults.VehicleWorkID, defaults.MemberID, defaults.VehicleVersion+1))
	if err == nil {
		t.Fatal("a vehicle claim at any version other than the frozen one must refuse")
	}
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestKnowledgeAuthorityDefaultAcceptsFrozenMemberAtVehicleVersion(t *testing.T) {
	t.Parallel()
	defaults := readCommittedAuthorityDefaults(t)
	if _, err := parseKnowledgeManifest(authorityCohortManifest(t, defaults.VehicleWorkID, defaults.MemberID, defaults.VehicleVersion)); err != nil {
		t.Fatalf("a frozen cohort member at the vehicle version must parse: %v", err)
	}
}

func TestKnowledgeAuthorityDefaultLeavesNonVehicleReferencesAlone(t *testing.T) {
	t.Parallel()
	if _, err := parseKnowledgeManifest(authorityCohortManifest(t, authoritySyntheticWork, "CD-0001", 7)); err != nil {
		t.Fatalf("a non-vehicle work reference keeps the resolver as its only runtime gate: %v", err)
	}
}

// authorityManifestFixture declares one manifest record for the resolver
// scenarios.
type authorityManifestFixture struct {
	ID           string
	Kind         string
	Status       string
	LegislatedBy string
	Version      int64
	Derived      bool
}

// writeAuthorityManifest lays the fixture records out as a committed manifest
// with the blob each record hashes.
func writeAuthorityManifest(t *testing.T, repo string, fixtures ...authorityManifestFixture) {
	t.Helper()
	records := make([]KnowledgeRecord, 0, len(fixtures))
	for _, fixture := range fixtures {
		path := ".concord/docs/decisions/" + fixture.ID + ".md"
		if fixture.Kind != "decision" && fixture.Kind != "constitution" && fixture.Kind != "spec" {
			path = ".concord/docs/lessons/" + fixture.ID + ".md"
		}
		content := "durable authority fixture blob\n"
		writeKnowledgeFile(t, repo, path, content)
		authority := KnowledgeAuthority{Tier: "legislated", LegislatedBy: fixture.LegislatedBy, ContractVersion: fixture.Version}
		if fixture.Derived {
			authority = KnowledgeAuthority{Tier: "derived"}
		}
		status := fixture.Status
		if status == "" {
			if manifestLawBearingKinds[fixture.Kind] {
				status = "accepted"
			} else {
				status = "published"
			}
		}
		records = append(records, KnowledgeRecord{
			ID: fixture.ID, Kind: fixture.Kind, Path: path, Status: status,
			Date: "2026-10-11T00:00:00Z", Title: "Authority fixture " + fixture.ID, Summary: "Fixture law for the authority resolver.",
			Tags: []string{}, Authority: authority,
			Scopes: KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}},
			SHA256: authorityBlobHash(t, repo, path),
		})
		if manifestLawBearingKinds[fixtures[len(records)-1].Kind] {
			records[len(records)-1].HomeDomainID = "fixture-law"
		}
	}
	manifest := KnowledgeManifest{
		SchemaVersion: "1.3", SupportedKinds: sortedKnowledgeKinds(), IndexedKinds: fixtureIndexedKinds(), Records: records,
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "authority-fixture", RootDomainID: "product-root:authority-fixture",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:authority-fixture", Name: "Authority fixture", Purpose: "fixture registry", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "fixture-law", Name: "fixture-law", Purpose: "fixture law home", ParentDomainID: "product-root:authority-fixture", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
	}
	writeManifestShards(t, repo, manifest)
}

func authorityBlobHash(t *testing.T, repo, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// seedAuthorityWork inserts the live work row the resolver probes.
func seedAuthorityWork(t *testing.T, s *Store, workID string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	if _, err := db.Exec(`INSERT OR IGNORE INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES(?, 'task', 'Authority fixture work', 'completed', 1, 1, 'now', 'now')`, workID); err != nil {
		t.Fatal(err)
	}
}

// seedAuthorityContract inserts one historical contract row with its
// predicate, the exact projection the resolver reads. This is synthetic
// fixture evidence about the rule, never a claim of real approval.
func seedAuthorityContract(t *testing.T, s *Store, workID string, version int64, ref string, definitionVersion int64, digest string, lawModifies []string, existsSubjects []string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
			t.Errorf("remove write guard: %v", err)
		}
	}()
	if _, err := db.Exec(`INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','now')`, authorityActorRef, "principal:authority-fixture", "client:authority-fixture", "agent:authority-fixture", "session:authority-fixture"); err != nil {
		t.Fatal(err)
	}
	if lawModifies == nil {
		lawModifies = []string{}
	}
	encodedModifies, err := json.Marshal(lawModifies)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,definition_ref,definition_version,definition_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		workID, version, "Synthetic fixture premise for the authority resolver.", "internal_sqlite", `[]`, `[]`, "2026-10-11T00:00:00Z", authorityActorRef, `[]`, string(encodedModifies), 0, "prototype_internal", ref, definitionVersion, digest); err != nil {
		t.Fatal(err)
	}
	outcomeKind, payload := "check", map[string]any{"kind": "check", "check_ref": "check:authority-fixture", "immutable_subject_ref": "commit:authority-fixture", "expected_result": "pass"}
	if len(existsSubjects) > 0 {
		outcomeKind, payload = "exists", map[string]any{"kind": "exists", "surface": "fixture:knowledge", "subjects": existsSubjects}
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES(?,?,?,?,?,?)`, workID, version, "predicate:authority-fixture", 0, outcomeKind, string(encodedPayload)); err != nil {
		t.Fatal(err)
	}
}

// authorityRegisteredDefinition returns a registered definition of the
// requested Product-truth class from the built-in registry, so the fixtures
// track the registry instead of hardcoding a version.
func authorityRegisteredDefinition(t *testing.T, changesProductTruth bool) (string, int64, string) {
	t.Helper()
	registry := BuiltinWorkflowRegistry()
	refs := make([]string, 0, len(builtinWorkflowCurrentVersions))
	for ref := range builtinWorkflowCurrentVersions {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		for version := int64(1); version <= builtinWorkflowCurrentVersions[ref]; version++ {
			registered, ok := registry.Lookup(ref, version)
			if !ok {
				continue
			}
			if (registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth) == changesProductTruth {
				return ref, version, registered.Digest
			}
		}
	}
	t.Fatalf("no registered workflow definition with changes_product_truth=%v", changesProductTruth)
	return "", 0, ""
}

// authorityScenario opens a store with one product home over the fixture repo.
func authorityScenario(t *testing.T, fixtures ...authorityManifestFixture) (*Store, KnowledgeHome) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeAuthorityManifest(t, repo, fixtures...)
	commitKnowledgeRepo(t, repo, "authority fixture")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "authority-project", HomeLocatorID: "authority-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "authority-product", home, "authority-project")
	return s, home
}

func TestRebuildRefusesWorkWithoutApprovedContract(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	err := s.RebuildKnowledgeIndex(context.Background(), home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestRebuildRefusesUnapprovedExactVersion(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 2})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, version, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, version, ref, version, digest, []string{"CD-8751"}, nil)
	err := s.RebuildKnowledgeIndex(context.Background(), home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestRebuildRefusesNonProductChangingContract(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, version, digest := authorityRegisteredDefinition(t, false)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, version, digest, []string{"CD-8751"}, nil)
	err := s.RebuildKnowledgeIndex(context.Background(), home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestRebuildRefusesContractWithoutAdmittedReference(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, version, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, version, digest, nil, nil)
	err := s.RebuildKnowledgeIndex(context.Background(), home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestRebuildRefusesRecordAnotherWorkAdmits(t *testing.T) {
	t.Parallel()
	// The declared reference is exact: a second work's contract that names the
	// same law cannot stand in for the declared one.
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	seedAuthorityWork(t, s, authoritySecondSyntheticWork)
	ref, version, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySecondSyntheticWork, 1, ref, version, digest, []string{"CD-8751"}, nil)
	err := s.RebuildKnowledgeIndex(context.Background(), home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestRebuildAcceptsProductChangingContractAdmittingTheLaw(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, version, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, version, digest, []string{"CD-8751"}, nil)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("an approved Product-changing contract naming the law admits it: %v", err)
	}
	q10, err := s.QueryQ10(context.Background(), Q10Request{KnowledgeID: "CD-8751", Product: "authority-product", Home: home})
	if err != nil || q10.Status != "canonical" {
		t.Fatalf("Q10 = %#v, err %v", q10, err)
	}
	if q10.UnverifiedAuthority != nil {
		t.Fatalf("a verified authority must not carry the unverified warning: %+v", q10.UnverifiedAuthority)
	}
}

func TestRebuildAcceptsExactSubjectAdmissionForNonDecisionKind(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "SPEC-8751", Kind: "spec", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, version, digest := authorityRegisteredDefinition(t, true)
	// A spec is not a decision or constitution, so only the exact
	// exists-subject field admits it; its contract carries an empty
	// law_modifies to prove the field split.
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, version, digest, nil, []string{"SPEC-8751"})
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("an admitted exists subject admits a non-decision record: %v", err)
	}
}

func TestRebuildRefusesNonDecisionLawModifiesAdmission(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "SPEC-8751", Kind: "spec", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, version, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, version, digest, []string{"SPEC-8751"}, nil)
	err := s.RebuildKnowledgeIndex(context.Background(), home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestRebuildRefusalKeepsThePriorProjection(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "lesson-before", Kind: "lesson", Derived: true})
	defer s.Close()
	ctx := context.Background()
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	// A second commit adds a held negative: the declared work exists but
	// carries no approved contract.
	writeAuthorityManifest(t, home.RepoPath,
		authorityManifestFixture{ID: "lesson-before", Kind: "lesson", Derived: true},
		authorityManifestFixture{ID: "CD-8752", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1},
	)
	commitKnowledgeRepo(t, home.RepoPath, "adds a held negative")
	seedAuthorityWork(t, s, authoritySyntheticWork)
	if err := s.RebuildKnowledgeIndex(ctx, home); err == nil {
		t.Fatal("a rebuild whose manifest declares a held negative must refuse")
	} else {
		assertFailureKind(t, err, KindInvalidNoteProof)
	}
	q9, err := s.QueryQ9(ctx, Q9Request{Home: home, AllowDegraded: true})
	if err != nil {
		t.Fatalf("the refused rebuild must leave the prior projection queryable: %v", err)
	}
	if len(q9.Items) != 1 || q9.Items[0].ID != "lesson-before" {
		t.Fatalf("the prior projection did not survive the refused rebuild: %#v", q9.Items)
	}
}

func TestRebuildKeepsDerivedRecordsAndTheMissingAuthorityLegacyRule(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "lesson-derived", Kind: "lesson", Derived: true})
	defer s.Close()
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("derived records resolve without any contract lookup: %v", err)
	}
	legacy := strings.Replace(string(authorityCohortManifest(t, authoritySyntheticWork, "CD-0001", 7)), `"schema_version":"1.3"`, `"schema_version":"1.2"`, 1)
	legacy = strings.Replace(legacy, `"authority":{"tier":"legislated","legislated_by":"`+authoritySyntheticWork+`","contract_version":7},`, "", 1)
	if _, err := parseKnowledgeManifest([]byte(legacy)); err != nil {
		t.Fatalf("a schema 1.2 record without authority keeps the applied derived default: %v", err)
	}
}

func TestFreshAdmissionRefusesHeldNegativeWithoutGitChange(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	ctx := context.Background()
	// The declared work is unknown at rebuild: absence is unverified, the
	// index builds, and the fresh watermark answers.
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatalf("an unverified authority keeps the fresh index answering: %v", err)
	}
	// The work becomes known with a held-negative history. No Git content
	// moves, so only the fresh-cache admission can see it.
	seedAuthorityWork(t, s, authoritySyntheticWork)
	err := s.EnsureKnowledgeIndexFresh(ctx, home)
	assertFailureKind(t, err, KindInvalidNoteProof)
	// The positive history clears the refusal on the same unchanged tree.
	ref, version, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, version, digest, []string{"CD-8751"}, nil)
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatalf("an approved exact contract clears the fresh-cache refusal: %v", err)
	}
}

func TestQ10ReportsUnverifiedAuthorityForAbsentWork(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	ctx := context.Background()
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	var tier string
	if err := s.DatabaseForTesting().QueryRow(`SELECT authority_tier FROM law_subjects WHERE law_id='CD-8751'`).Scan(&tier); err != nil {
		t.Fatal(err)
	}
	if tier != "legislated" {
		t.Fatalf("an unverified authority keeps its declared tier, got %q", tier)
	}
	q10, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "CD-8751", Product: "authority-product", Home: home})
	if err != nil || q10.Status != "canonical" {
		t.Fatalf("Q10 = %#v, err %v", q10, err)
	}
	if q10.UnverifiedAuthority == nil || *q10.UnverifiedAuthority != (KnowledgeUnverifiedAuthority{LawID: "CD-8751", WorkID: authoritySyntheticWork, ContractVersion: 1}) {
		t.Fatalf("the canonical read must carry the exact unverified warning, got %+v", q10.UnverifiedAuthority)
	}
	// The approved exact contract clears the warning on the next read.
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, version, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, version, digest, []string{"CD-8751"}, nil)
	cleared, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "CD-8751", Product: "authority-product", Home: home})
	if err != nil || cleared.Status != "canonical" {
		t.Fatalf("Q10 after approval = %#v, err %v", cleared, err)
	}
	if cleared.UnverifiedAuthority != nil {
		t.Fatalf("positive historical content clears the unverified warning: %+v", cleared.UnverifiedAuthority)
	}
}

func TestQ10HeldNegativeRefusesRegardlessOfDegraded(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	ctx := context.Background()
	// The declared work is unknown at rebuild: the index carries the record
	// unverified. The work then becomes known with a held-negative history,
	// so the projection predates the negative.
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	seedAuthorityWork(t, s, authoritySyntheticWork)
	if _, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "CD-8751", Product: "authority-product", Home: home}); err == nil {
		t.Fatal("a held negative must refuse the canonical read under AllowDegraded")
	}
}

func TestSupersededContractClassifiesThroughItsOwnPin(t *testing.T) {
	t.Parallel()
	s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	productRef, productVersion, productDigest := authorityRegisteredDefinition(t, true)
	retiredRef, retiredVersion, retiredDigest := authorityRegisteredDefinition(t, false)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, productRef, productVersion, productDigest, []string{"CD-8751"}, nil)
	// The superseding contract is not Product-changing and names nothing. A
	// resolver that read the current instance or the latest family pin would
	// refuse; the exact historical pin classifies instead.
	seedAuthorityContract(t, s, authoritySyntheticWork, 2, retiredRef, retiredVersion, retiredDigest, nil, nil)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_, updateErr := db.Exec(`UPDATE workflow_contracts SET superseded_by=2 WHERE work_id=? AND contract_version=1`, authoritySyntheticWork)
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Errorf("remove write guard: %v", err)
	}
	if updateErr != nil {
		t.Fatal(updateErr)
	}
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("a superseded contract keeps its own pin: %v", err)
	}
}

func TestCancelledWorkKeepsExactLegislativeAuthority(t *testing.T) {
	t.Parallel()
	for _, family := range []string{"workflow.implementation", "workflow.research"} {
		t.Run(family, func(t *testing.T) {
			s, home := authorityScenario(t, authorityManifestFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
			defer s.Close()
			seedAuthorityWork(t, s, authoritySyntheticWork)
			version, digest, changesProductTruth := authorityFamilyDefinition(t, family)
			seedAuthorityContract(t, s, authoritySyntheticWork, 1, family, version, digest, []string{"CD-8751"}, nil)
			db := s.DatabaseForTesting()
			if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
				t.Fatal(err)
			}
			_, updateErr := db.Exec(`UPDATE work_items SET lifecycle='cancelled' WHERE id=?`, authoritySyntheticWork)
			if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
				t.Errorf("remove write guard: %v", err)
			}
			if updateErr != nil {
				t.Fatal(updateErr)
			}
			err := s.RebuildKnowledgeIndex(context.Background(), home)
			if !changesProductTruth {
				assertFailureKind(t, err, KindInvalidNoteProof)
				return
			}
			if err != nil {
				t.Fatalf("cancellation must not erase an approved legislative contract: %v", err)
			}
			result, err := s.QueryQ10(context.Background(), Q10Request{KnowledgeID: "CD-8751", Product: "authority-product", Home: home})
			if err != nil || result.UnverifiedAuthority != nil {
				t.Fatalf("cancelled work is held history, not unverified absence: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestKnowledgeAuthorityCorpusResolvesThroughSyntheticContracts(t *testing.T) {
	t.Parallel()
	// Committed manifest metadata selects the whole population. Synthetic
	// contracts prove the resolver rule, not the corpus's real approval history.
	root := repositoryRootForTest(t)
	manifest, missing, err := readKnowledgeManifest(context.Background(), root, "HEAD", manifestSharedHomeRole)
	if err != nil || missing {
		t.Fatalf("cannot read the committed delivery manifest: missing=%v err=%v", missing, err)
	}
	defaults := readCommittedAuthorityDefaults(t)
	corpus := make([]KnowledgeRecord, 0)
	included := make(map[string]bool)
	for _, record := range manifest.Records {
		if record.Authority.Tier != "legislated" || record.Authority.LegislatedBy == defaults.VehicleWorkID {
			continue
		}
		corpus = append(corpus, record)
		included[record.ID] = true
	}
	if len(corpus) == 0 {
		t.Fatal("the committed corpus must contain work-backed legislative claims")
	}
	// CD-0159 D7 corrected the derived set; any tier change in the range
	// must update these sets.
	derivedInRange := map[string]bool{
		"CD-0192": true, "CD-0195": true, "CD-0197": true, "CD-0198": true,
		"CD-0199": true, "CD-0203": true, "CD-0204": true, "CD-0207": true, "CD-0209": true,
	}
	legislatedInRange := map[string]bool{
		"CD-0190": true, "CD-0191": true, "CD-0193": true, "CD-0194": true, "CD-0200": true,
		"CD-0201": true, "CD-0202": true, "CD-0205": true, "CD-0208": true,
	}
	byID := make(map[string]KnowledgeRecord, len(manifest.Records))
	for _, record := range manifest.Records {
		byID[record.ID] = record
	}
	for number := 190; number <= 209; number++ {
		id := fmt.Sprintf("CD-%04d", number)
		record, ok := byID[id]
		if !ok {
			if derivedInRange[id] || legislatedInRange[id] {
				t.Fatalf("the committed manifest is missing %s", id)
			}
			continue
		}
		if !derivedInRange[id] && !legislatedInRange[id] {
			t.Fatalf("%s must belong to exactly one tier set in CD-0190..CD-0209; observed tier %q", id, record.Authority.Tier)
		}
		if derivedInRange[id] {
			if record.Authority.Tier != "derived" || record.Authority.LegislatedBy != "" || record.Authority.ContractVersion != 0 {
				t.Fatalf("CD-0159 D7 corrected %s to derived with no legislative fields; observed tier %q legislated_by %q contract_version %d", id, record.Authority.Tier, record.Authority.LegislatedBy, record.Authority.ContractVersion)
			}
			continue
		}
		if !included[id] {
			t.Fatalf("%s must stay in the work-backed legislative corpus; observed tier %q", id, record.Authority.Tier)
		}
	}
	t.Logf("synthetic approval guard covers all %d work-backed legislated records", len(corpus))
	fixtures := make([]authorityManifestFixture, 0, len(corpus))
	// Group the declared references by exact (work, version) pair: the
	// resolver checks the exact historical version, so each corpus record's
	// own declared version is the one the synthetic contract must occupy.
	declared := map[string][]string{}
	for _, record := range corpus {
		fixtures = append(fixtures, authorityManifestFixture{ID: record.ID, Kind: record.Kind, LegislatedBy: record.Authority.LegislatedBy, Version: record.Authority.ContractVersion})
		key := record.Authority.LegislatedBy + ":" + strconv.FormatInt(record.Authority.ContractVersion, 10)
		declared[key] = append(declared[key], record.ID)
	}
	repo := initKnowledgeRepo(t)
	writeAuthorityManifest(t, repo, fixtures...)
	commitKnowledgeRepo(t, repo, "the committed corpus metadata")
	s := openTemp(t)
	defer s.Close()
	home := KnowledgeHome{HomeProjectID: "authority-project", HomeLocatorID: "authority-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "authority-product", home, "authority-project")
	// One synthetic Product-changing contract per declared (work, version)
	// pair, admitting every record it names through the exact exists subject —
	// a field every kind admits. law_modifies stays empty: its 32-entry bound
	// cannot carry a whole work's corpus in one row.
	ref, definitionVersion, digest := authorityRegisteredDefinition(t, true)
	for key, ids := range declared {
		parts := strings.SplitN(key, ":", 2)
		workID := parts[0]
		version, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seedAuthorityWork(t, s, workID)
		seedAuthorityContract(t, s, workID, version, ref, definitionVersion, digest, nil, ids)
	}
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatalf("every work-backed legislated record in the committed corpus must classify through a synthetic Product-changing contract: %v", err)
	}
}
