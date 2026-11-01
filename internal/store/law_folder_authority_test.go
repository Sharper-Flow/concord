package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

const (
	lawFolderAuthorityLawID     = "CD-9001"
	lawFolderAuthorityOutsideID = "CD-9002"
)

// Kind, acceptance and digest are not authored metadata.
const lawFolderAuthorityLawContent = "---\n" +
	`{"id":"` + lawFolderAuthorityLawID + `","title":"Folder authority synthetic law","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"product-root:product","applies_to_domain_ids":[],"product_wide_rationale":"This synthetic law governs the whole Product."}` + "\n" +
	"---\n\nSynthetic folder law body.\n"

func lawFolderAuthorityFixture(t *testing.T) (*Store, KnowledgeHome) {
	t.Helper()
	s := openTemp(t)
	repo := initKnowledgeRepo(t) // --initial-branch=main: HEAD is the default branch.
	writeKnowledgeFile(t, repo, ".concord/law/decisions/CD-9001-folder-authority.md", lawFolderAuthorityLawContent)
	writeKnowledgeFile(t, repo, ".concord/docs/decisions/CD-9002-ordinary.md", "# CD-9002: ordinary document\n\nNot a law record.\n")
	writeKnowledgeFile(t, repo, ".concord/docs/knowledge/domain-registry.json", `{"schema_version":"1.0","product_key":"product","root_domain_id":"product-root:product","domains":[{"domain_id":"product-root:product","name":"Product","purpose":"Synthetic Product law organization.","status":"current","architecture_relations":[]}]}`+"\n")
	commitKnowledgeRepo(t, repo, "synthetic folder authority law")
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "folder-law-locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product", home)
	return s, home
}

func TestLawFolderAuthorityFolderLawSatisfiesMandate(t *testing.T) {
	s, home := lawFolderAuthorityFixture(t)
	ctx, err := s.EstablishKnowledgeSourceSetProofForProject(t.Context(), home.HomeProjectID)
	if err != nil {
		t.Fatalf("pre-transaction verification refused a committed folder-law home: %v", err)
	}
	tx := lawChangeTx(t, s)
	if err := checkMandatedLawsTxAtHome(ctx, tx, home.HomeProjectID, home.HomeLocatorID, []string{lawFolderAuthorityLawID}, nil, false); err != nil {
		t.Fatalf("committed folder law did not satisfy the mandate: %v", err)
	}
	resolved, missing, err := resolveLawReferences(ctx, "check_mandated_laws", []KnowledgeHome{home}, []string{lawFolderAuthorityLawID})
	if err != nil || len(missing) != 0 {
		t.Fatalf("resolve accepted folder law: missing=%v, err=%v", missing, err)
	}
	law := resolved[lawFolderAuthorityLawID]
	sum := sha256.Sum256([]byte(lawFolderAuthorityLawContent))
	if law.status != "accepted" || law.tier != "legislated" {
		t.Fatalf("folder acceptance or legislative standing changed: %+v", law)
	}
	if want := "sha256:" + hex.EncodeToString(sum[:]); law.hash != want {
		t.Fatalf("folder law digest is not the whole-file sha256: got %s, want %s", law.hash, want)
	}
}

func TestLawFolderAuthorityOutsideFileDoesNotSatisfyMandate(t *testing.T) {
	s, home := lawFolderAuthorityFixture(t)
	ctx, err := s.EstablishKnowledgeSourceSetProofForProject(t.Context(), home.HomeProjectID)
	if err != nil {
		t.Fatalf("pre-transaction verification refused a committed folder-law home: %v", err)
	}
	tx := lawChangeTx(t, s)
	err = checkMandatedLawsTxAtHome(ctx, tx, home.HomeProjectID, home.HomeLocatorID, []string{lawFolderAuthorityOutsideID}, nil, false)
	requireFailureKind(t, err, KindProjectionNotFound, "mandate naming a document outside the law folder")
	if !strings.Contains(err.Error(), lawFolderAuthorityOutsideID) {
		t.Fatalf("wrong refusal for a document outside the law folder: %v", err)
	}
	resolved, missing, err := resolveLawReferences(ctx, "check_mandated_laws", []KnowledgeHome{home}, []string{lawFolderAuthorityOutsideID})
	if err != nil || len(resolved) != 0 || len(missing) != 1 || missing[0] != lawFolderAuthorityOutsideID {
		t.Fatalf("outside-folder document resolved as law: resolved=%v missing=%v err=%v", resolved, missing, err)
	}
}
