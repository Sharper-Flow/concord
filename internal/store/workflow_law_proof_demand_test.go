package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The standalone Project has no Product designation. Verification is demand-driven.
func standaloneInventoryFixture(t *testing.T) (*Store, KnowledgeHome, string) {
	t.Helper()
	const workID = "standalone-inventory-work"
	s := seedLawChangeFixture(t, workID)
	lawChangeFold(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='product'`)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	productID, err := resolveKnowledgeSourceRole(t.Context(), s.db, home)
	if err != nil || productID != "" {
		t.Fatalf("fixture is not standalone: product=%q error=%v", productID, err)
	}
	return s, home, workID
}

func TestCurrentLawProofRefusesReboundCanonicalLocation(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		t.Run(map[bool]string{false: "Product", true: "standalone"}[standalone], func(t *testing.T) {
			s := seedLawChangeFixture(t, "rebound-law-home")
			home := lawChangeFixtureHome(t, s)
			if standalone {
				lawChangeFold(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='product'`)
			}
			ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), "rebound-law-home")
			if err != nil {
				t.Fatal(err)
			}
			moved := initKnowledgeRepo(t)
			writeFixtureSpecLawSource(t, KnowledgeHome{RepoPath: moved}, "spec:one")
			lawChangeFold(t, s, `UPDATE project_locators SET locator_value=?,normalized_value=? WHERE locator_id=?`, moved, moved, home.HomeLocatorID)
			tx := lawChangeTx(t, s)
			err = checkMandatedLawsTxAtHome(ctx, tx, home.HomeProjectID, home.HomeLocatorID, []string{"spec:one"}, nil, false)
			requireFailureKind(t, err, KindInvalidOperation, "rebound canonical location")
			if !strings.Contains(err.Error(), "resolved to a different canonical location after its verification") {
				t.Fatalf("wrong canonical-location refusal: %v", err)
			}
		})
	}
}

// A law-free consumer ignores a deferred failure; a mandate consumes it unchanged.
func TestLawProofStandaloneDemandDrivenConsumers(t *testing.T) {
	for _, mode := range []string{"failed-refresh", "unreachable"} {
		t.Run(mode, func(t *testing.T) {
			s, home, workID := standaloneInventoryFixture(t)
			if mode == "failed-refresh" {
				writeKnowledgeFile(t, home.RepoPath, ".concord/law/decisions/CD-0999-broken.md", "# A law file without metadata\n")
				commitKnowledgeRepo(t, home.RepoPath, "standalone verification fails")
			} else if err := os.Rename(filepath.Join(home.RepoPath, ".git"), filepath.Join(home.RepoPath, "offline.git")); err != nil {
				t.Fatal(err)
			}
			ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
			if err != nil {
				t.Fatalf("standalone preparation must defer failures to the consumer: %v", err)
			}
			proof := knowledgeSourceSetProofFrom(ctx)
			if proof == nil || proof.refreshErr == nil {
				t.Fatal("fixture did not preserve demand-driven verification failure")
			}
			tx := lawChangeTx(t, s)
			if err := checkMandatedLawsTxAtHome(ctx, tx, home.HomeProjectID, home.HomeLocatorID, nil, nil, false); err != nil {
				t.Fatalf("law-free transition consumed a deferred verification failure: %v", err)
			}
			err = checkMandatedLawsTxAtHome(ctx, tx, home.HomeProjectID, home.HomeLocatorID, []string{"spec:one"}, nil, false)
			if !errors.Is(err, proof.refreshErr) {
				t.Fatalf("mandate did not consume the deferred verification failure: %v", err)
			}
		})
	}
}
