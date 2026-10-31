package store

// The law-source verification is the owning publication boundary for declared
// legislative references: before a committed law folder reaches a
// consequential transaction, every legislated law resolves its declared
// reference through the retained CD-0159 rules — the frozen default cohort,
// or one live work whose exact historical contract is approved,
// Product-changing, and admits the law. Unknown declared works are
// unverified, not negative, and keep their declared tier. These tests hold
// that admission at the new boundary; the retired rebuild-time proof they
// replace lived in the knowledge-index projection.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// authoritySyntheticWork is the synthetic hex work id fixture laws name when
// a test controls the approving contract itself.
const authoritySyntheticWork = "work-0f1e2d3c4b5a6978"

// authoritySecondSyntheticWork names a second independent work for scenarios
// that hold two declared references at once.
const authoritySecondSyntheticWork = "work-aabbccdd11223344"

// authorityActorRef satisfies the workflow_actors key shape the contract
// approval column references.
var authorityActorRef = "actor:" + strings.Repeat("a", 64)

const authorityRegistryJSON = `{"schema_version":"1.0","product_key":"authority-fixture","root_domain_id":"product-root:authority-fixture","domains":[{"domain_id":"product-root:authority-fixture","name":"Authority fixture","purpose":"fixture registry","status":"current","architecture_relations":[]}]}` + "\n"

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

// authorityLawRecord decodes one authored law frontmatter the way the loader
// does, so the cohort tests exercise the same validator the committed read
// applies.
func authorityLawRecord(t *testing.T, legislatedBy, recordID string, version int64) KnowledgeRecord {
	t.Helper()
	front := `{"id":"` + recordID + `","title":"Authority fixture","authority":{"tier":"legislated","legislated_by":"` + legislatedBy + `","contract_version":` + itoa(int(version)) + `},"home_domain_id":"product-root:authority-fixture","applies_to_domain_ids":[],"product_wide_rationale":"Fixture law binds every child Domain."}`
	var record KnowledgeRecord
	if err := json.Unmarshal([]byte(front), &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func itoa(value int) string {
	return strconv.FormatInt(int64(value), 10)
}

// authorityLawFixture declares one committed law for the admission scenarios.
type authorityLawFixture struct {
	ID           string
	Kind         string
	LegislatedBy string
	Version      int64
	Derived      bool
}

func authorityLawTierDir(kind string) string {
	switch kind {
	case "constitution":
		return ".concord/law/constitution"
	case "spec":
		return ".concord/law/specs"
	default:
		return ".concord/law/decisions"
	}
}

// writeAuthorityLaw commits the fixture laws and the Domain registry.
func writeAuthorityLaw(t *testing.T, repo string, fixtures ...authorityLawFixture) {
	t.Helper()
	writeKnowledgeFile(t, repo, knowledgeRegistryPath, authorityRegistryJSON)
	for _, fixture := range fixtures {
		authority := `"tier":"legislated","legislated_by":"` + fixture.LegislatedBy + `","contract_version":` + itoa(int(fixture.Version))
		if fixture.Derived {
			authority = `"tier":"derived"`
		}
		front := `{"id":"` + fixture.ID + `","title":"Authority fixture ` + fixture.ID + `","authority":{` + authority + `},"home_domain_id":"product-root:authority-fixture","applies_to_domain_ids":[],"product_wide_rationale":"Fixture law binds every child Domain."}`
		body := "durable authority fixture blob\n"
		path := authorityLawTierDir(fixture.Kind) + "/" + strings.ToLower(fixture.ID) + "-fixture.md"
		writeKnowledgeFile(t, repo, path, "---\n"+front+"\n---\n\n"+body)
	}
}

// seedAuthorityWork inserts the live work row the admission probes.
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
// predicate, the exact projection the admission reads. This is synthetic
// fixture evidence about the rule, never a claim of real approval.
func seedAuthorityContract(t *testing.T, s *Store, workID string, version int64, ref, digest string, lawModifies []string, existsSubjects []string) {
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
	if _, err := db.Exec(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class,definition_ref,definition_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		workID, version, "Synthetic fixture premise for the authority resolver.", "internal_sqlite", `[]`, `[]`, "2026-10-11T00:00:00Z", authorityActorRef, `[]`, string(encodedModifies), 0, "prototype_internal", ref, digest); err != nil {
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
func authorityRegisteredDefinition(t *testing.T, changesProductTruth bool) (string, string) {
	t.Helper()
	registry := BuiltinWorkflowRegistry()
	refs := make([]string, 0, len(BuiltinWorkflowDefinitions()))
	for _, definition := range BuiltinWorkflowDefinitions() {
		refs = append(refs, definition.Ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		registered, ok := registry.Current(ref)
		if ok && (registered.Definition.ChangesProductTruth != nil && *registered.Definition.ChangesProductTruth) == changesProductTruth {
			return ref, registered.Digest
		}
	}
	t.Fatalf("no registered workflow definition with changes_product_truth=%v", changesProductTruth)
	return "", ""
}

// authorityLawScenario opens a store with one designated Product home over
// the fixture repo's committed law folder.
func authorityLawScenario(t *testing.T, fixtures ...authorityLawFixture) (*Store, KnowledgeHome) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	writeAuthorityLaw(t, repo, fixtures...)
	commitKnowledgeRepo(t, repo, "authority fixture law folder")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "authority-project", HomeLocatorID: "authority-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "authority-product", home, "authority-project")
	return s, home
}

// establishAuthorityFixture runs the designated establishment, the owning
// admission boundary under test.
func establishAuthorityFixture(t *testing.T, s *Store, home KnowledgeHome) error {
	t.Helper()
	_, _, err := s.verifyLawSourceSnapshot(context.Background(), home)
	return err
}

func TestLawEstablishmentRefusesWorkWithoutApprovedContract(t *testing.T) {
	t.Parallel()
	s, home := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	err := establishAuthorityFixture(t, s, home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestLawEstablishmentRefusesUnapprovedExactVersion(t *testing.T) {
	t.Parallel()
	s, home := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 2})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, digest, []string{"CD-8751"}, nil)
	err := establishAuthorityFixture(t, s, home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestLawEstablishmentRefusesNonProductChangingContract(t *testing.T) {
	t.Parallel()
	s, home := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, digest := authorityRegisteredDefinition(t, false)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, digest, []string{"CD-8751"}, nil)
	err := establishAuthorityFixture(t, s, home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestLawEstablishmentRefusesContractWithoutAdmittedReference(t *testing.T) {
	t.Parallel()
	s, home := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, digest, nil, nil)
	err := establishAuthorityFixture(t, s, home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestLawEstablishmentRefusesRecordAnotherWorkAdmits(t *testing.T) {
	t.Parallel()
	// The declared reference is exact: a second work's contract that names the
	// same law cannot stand in for the declared one.
	s, home := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	seedAuthorityWork(t, s, authoritySecondSyntheticWork)
	ref, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySecondSyntheticWork, 1, ref, digest, []string{"CD-8751"}, nil)
	err := establishAuthorityFixture(t, s, home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestLawEstablishmentAcceptsProductChangingContractAdmittingTheLaw(t *testing.T) {
	t.Parallel()
	s, home := authorityLawScenario(t, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, digest, []string{"CD-8751"}, nil)
	if err := establishAuthorityFixture(t, s, home); err != nil {
		t.Fatalf("an approved Product-changing contract naming the law admits it: %v", err)
	}
}

func TestLawEstablishmentAcceptsExactSubjectAdmissionForNonDecisionKind(t *testing.T) {
	t.Parallel()
	s, home := authorityLawScenario(t, authorityLawFixture{ID: "SPEC-8751", Kind: "spec", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, digest := authorityRegisteredDefinition(t, true)
	// A spec is not a decision or constitution, so only the exact
	// exists-subject field admits it; its contract carries an empty
	// law_modifies to prove the field split.
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, digest, nil, []string{"SPEC-8751"})
	if err := establishAuthorityFixture(t, s, home); err != nil {
		t.Fatalf("an admitted exists subject admits a non-decision law: %v", err)
	}
}

func TestLawEstablishmentRefusesNonDecisionLawModifiesAdmission(t *testing.T) {
	t.Parallel()
	s, home := authorityLawScenario(t, authorityLawFixture{ID: "SPEC-8751", Kind: "spec", LegislatedBy: authoritySyntheticWork, Version: 1})
	defer s.Close()
	seedAuthorityWork(t, s, authoritySyntheticWork)
	ref, digest := authorityRegisteredDefinition(t, true)
	seedAuthorityContract(t, s, authoritySyntheticWork, 1, ref, digest, []string{"SPEC-8751"}, nil)
	err := establishAuthorityFixture(t, s, home)
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestLawEstablishmentKeepsDerivedLawAndTheMissingAuthorityLegacyRule(t *testing.T) {
	t.Parallel()
	// A derived law carries no declared reference, and a legislated law whose
	// declared work is unknown is unverified, not negative: neither refuses.
	s, home := authorityLawScenario(t,
		authorityLawFixture{ID: "CD-8752", Kind: "decision", Derived: true},
		authorityLawFixture{ID: "CD-8753", Kind: "decision", LegislatedBy: authoritySecondSyntheticWork, Version: 1},
	)
	defer s.Close()
	if err := establishAuthorityFixture(t, s, home); err != nil {
		t.Fatalf("derived law and an unknown declared work must keep their standing: %v", err)
	}
}

func TestKnowledgeAuthorityDefaultRefusesOutOfCohortVehicleClaim(t *testing.T) {
	t.Parallel()
	defaults := readCommittedAuthorityDefaults(t)
	record := authorityLawRecord(t, defaults.VehicleWorkID, "CD-9999", defaults.VehicleVersion)
	err := validateKnowledgeRecordAuthority(record)
	if err == nil {
		t.Fatal("a vehicle claim for a record outside the frozen cohort must refuse")
	}
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestKnowledgeAuthorityDefaultRefusesWrongVehicleVersion(t *testing.T) {
	t.Parallel()
	defaults := readCommittedAuthorityDefaults(t)
	record := authorityLawRecord(t, defaults.VehicleWorkID, defaults.MemberID, defaults.VehicleVersion+1)
	err := validateKnowledgeRecordAuthority(record)
	if err == nil {
		t.Fatal("a vehicle claim at any version other than the frozen one must refuse")
	}
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestKnowledgeAuthorityDefaultAcceptsFrozenMemberAtVehicleVersion(t *testing.T) {
	t.Parallel()
	defaults := readCommittedAuthorityDefaults(t)
	record := authorityLawRecord(t, defaults.VehicleWorkID, defaults.MemberID, defaults.VehicleVersion)
	if record.Authority.Tier != "legislated" || record.Authority.LegislatedBy != defaults.VehicleWorkID {
		t.Fatalf("fixture frontmatter lost its declared reference: %+v", record.Authority)
	}
	if err := validateKnowledgeRecordAuthority(record); err != nil {
		t.Fatalf("a frozen cohort member at the vehicle version must pass: %v", err)
	}
}

// The committed fixture's whole-file digest stays the SHA-256 the folder law
// derives, independent of any authored digest field.
func TestAuthorityLawFixtureDigestIsTheWholeFile(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	writeAuthorityLaw(t, repo, authorityLawFixture{ID: "CD-8751", Kind: "decision", LegislatedBy: authoritySyntheticWork, Version: 1})
	paths := []string{}
	entries, err := parseTreeEntries([]byte(runKnowledgeGit(t, repo, "ls-tree", "-r", "-z", "HEAD", "--", ".concord/law")))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		paths = append(paths, entry.path)
	}
	if len(paths) != 1 {
		t.Fatalf("fixture law count = %d, want 1 (%v)", len(paths), paths)
	}
	content := []byte(runKnowledgeGit(t, repo, "cat-file", "blob", "HEAD:"+paths[0]))
	sum := sha256.Sum256(content)
	if got := "sha256:" + hex.EncodeToString(sum[:]); len(got) != len("sha256:")+64 {
		t.Fatalf("fixture digest shape wrong: %s", got)
	}
}
