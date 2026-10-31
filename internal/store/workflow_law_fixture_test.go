package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Shared committed-fixture helpers for the workflow law tests. Every retired
// SQL law/Domain projection row a workflow fixture used to seed is replaced
// by real committed bytes in the synthetic home these helpers write: the law
// folder and the independent Domain registry the pre-transaction proof reads
// (law-folder-authority.md, Committed reads). Nothing here hardcodes the
// digest of committed bytes; tests derive pins from the bytes they commit.

// fixtureLawFolderLawHash derives the committed whole-file digest of one
// fixture law the shared home writes through writeFixtureLawSource, so tests
// pin the real committed bytes instead of a hardcoded hash.
func fixtureLawFolderLawHash(lawID string) string {
	sum := sha256.Sum256([]byte(fixtureLawFolderLaw(lawID)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// workflowFixtureLawPaths maps the fixture law IDs a workflow home commits to
// their committed law-folder paths.
var workflowFixtureLawPaths = map[string]string{
	"spec:one":  ".concord/law/specs/spec-one-fixture.md",
	"const:one": ".concord/law/constitution/const-one-fixture.md",
	"spec:pre":  ".concord/law/specs/spec-pre-fixture.md",
	"law:new":   ".concord/law/specs/law-new-fixture.md",
	"spec:two":  ".concord/law/specs/spec-two-fixture.md",
}

// committedLawFileHash derives the whole-file digest of one file the fixture
// home carries in its checkout, the same bytes the snapshot digests at the
// resolved commit.
func committedLawFileHash(t *testing.T, repo, relativePath string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatalf("read committed fixture law %s: %v", relativePath, err)
	}
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// workflowFixtureKnowledgeRepo resolves the checkout of the fixture Product's
// designated knowledge home, wherever the fixture named it. An empty result
// means the fixture designated no home, so no committed pin can be derived.
func workflowFixtureKnowledgeRepo(t *testing.T, s *Store) string {
	t.Helper()
	var repo string
	err := s.DatabaseForTesting().QueryRow(`SELECT l.normalized_value FROM product_knowledge_homes h JOIN project_locators l ON l.project_id=h.project_id AND l.locator_id=h.locator_id WHERE h.product_id='product' ORDER BY h.project_id, h.locator_id LIMIT 2`).Scan(&repo)
	if err != nil && err != sql.ErrNoRows {
		t.Fatal(err)
	}
	return repo
}

// workflowFixtureLawPin derives the committed digest of one fixture law from
// the bytes the home actually carries, so an approval pins the current law
// revision rather than a hardcoded hash.
func workflowFixtureLawPin(t *testing.T, repo, lawID string) string {
	t.Helper()
	path, ok := workflowFixtureLawPaths[lawID]
	if !ok {
		t.Fatalf("fixture law %s has no committed path in the workflow home", lawID)
	}
	return committedLawFileHash(t, repo, path)
}

// workflowArchitectureFixtureRegistry is the committed Domain registry the
// architecture-binding fixtures read: the Product root plus the root/child
// Domains and the canonical child depends_on root relation their binding
// tuples name, governed by the committed spec:one fixture law.
const workflowArchitectureFixtureRegistry = `{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Governs the synthetic fixture Product.","status":"current","architecture_relations":[]},{"domain_id":"root","name":"Root","purpose":"Fixture root child Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]},{"domain_id":"child","name":"Child","purpose":"Fixture child Domain.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[{"kind":"depends_on","target_domain_id":"root","governing_law_ids":["spec:one"]}]},{"domain_id":"child-alpha","name":"Affected child","purpose":"The approved affected child.","status":"current","parent_domain_id":"product-root:product","architecture_relations":[]}]}` + "\n"

// commitWorkflowFixtureRegistry re-commits one registry revision into the
// fixture home. It is the retained replacement for a projection-row mutation:
// a committed registry change is the only way a registry read changes.
func commitWorkflowFixtureRegistry(t *testing.T, home KnowledgeHome, registryJSON string) {
	t.Helper()
	writeKnowledgeFile(t, home.RepoPath, knowledgeRegistryPath, registryJSON)
	commitKnowledgeRepo(t, home.RepoPath, "fixture registry revision")
}

// commitFixtureLawFile commits one law file into the home under the fixture
// path of its ID. It is the retained replacement for a law_subjects row: the
// law exists when its file is on the default branch.
func commitFixtureLawFile(t *testing.T, home KnowledgeHome, lawID, content string) string {
	t.Helper()
	path, ok := workflowFixtureLawPaths[lawID]
	if !ok {
		t.Fatalf("fixture law %s has no committed path in the workflow home", lawID)
	}
	writeKnowledgeFile(t, home.RepoPath, path, content)
	return commitKnowledgeRepo(t, home.RepoPath, "commit fixture law "+lawID)
}

// workflowFixtureDecisionsLawPath is the committed decisions-tier path of one
// fixture law ID, derived the same way writeFixtureTierLawSource derives
// paths, so a test can pin the bytes it committed through
// commitWorkflowFixtureLaw without extending workflowFixtureLawPaths.
func workflowFixtureDecisionsLawPath(lawID string) string {
	return ".concord/law/decisions/" + strings.ReplaceAll(strings.ReplaceAll(lawID, ":", "-"), "/", "-") + "-fixture.md"
}

// commitWorkflowFixtureLaw commits one decisions-tier fixture law with
// caller-authored front matter and body. It is the retained replacement for a
// law_subjects insert and for a projection-row amendment: committed new bytes
// are the only way a law's revision, tier, or Domain homes change.
func commitWorkflowFixtureLaw(t *testing.T, home KnowledgeHome, lawID, content string) string {
	t.Helper()
	writeKnowledgeFile(t, home.RepoPath, workflowFixtureDecisionsLawPath(lawID), content)
	return commitKnowledgeRepo(t, home.RepoPath, "commit fixture law "+lawID)
}

// removeWorkflowFixtureLaw retires one decisions-tier fixture law by deleting
// its committed file, the ordinary pull-request route a law removal takes.
func removeWorkflowFixtureLaw(t *testing.T, home KnowledgeHome, lawID string) {
	t.Helper()
	if err := os.Remove(filepath.Join(home.RepoPath, filepath.FromSlash(workflowFixtureDecisionsLawPath(lawID)))); err != nil {
		t.Fatal(err)
	}
	commitKnowledgeRepo(t, home.RepoPath, "retire fixture law "+lawID)
}

// removeCommittedFixtureLaw retires one fixture law by deleting its file and
// committing, the ordinary pull-request route a law retires through.
func removeCommittedFixtureLaw(t *testing.T, home KnowledgeHome, lawID string) {
	t.Helper()
	path, ok := workflowFixtureLawPaths[lawID]
	if !ok {
		t.Fatalf("fixture law %s has no committed path in the workflow home", lawID)
	}
	if err := os.Remove(filepath.Join(home.RepoPath, filepath.FromSlash(path))); err != nil {
		t.Fatal(err)
	}
	commitKnowledgeRepo(t, home.RepoPath, "retire fixture law "+lawID)
}

// fixtureLawFolderLawWithCriteria renders one fixture spec whose front matter
// carries authored criterion bindings, the committed form the law context
// resolves this work's chaining from. Like fixtureLawFolderLaw it homes at
// the root child Domain and states no product-wide rationale.
func fixtureLawFolderLawWithCriteria(id, bindings string) string {
	return "---\n" +
		`{"id":"` + id + `","title":"Fixture folder law ` + id + `","authority":{"tier":"derived"},"home_domain_id":"root","applies_to_domain_ids":[],"criterion_bindings":` + bindings + `}` + "\n" +
		"---\n\nSynthetic fixture law body.\n"
}
