package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// standaloneInventoryFixture seeds the committed law-folder fixture home and
// removes its Product designation, so the home is a standalone Project home
// whose verification is demand-driven.
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

func initializeStandaloneInventory(t *testing.T, s *Store, ctx context.Context, workID string) error {
	t.Helper()
	definition, err := LiveWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	return s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: definition, Actor: lawChangeConstructorActor})
	})
}

// A standalone home's inventory admission revalidates the same bounded proof
// as a Product home's: a missing proof, committed law content moved after the
// verification, or a canonical location rebound after it refuses; a recorded
// verification failure reaches the consumer unchanged; a verified folder, or
// a later commit that leaves law and registry content alone, admits. Every
// refusal leaves the instance, the baseline, and the work version unchanged.
func TestLawBaselineStandaloneInventoryFreshness(t *testing.T) {
	for _, boundary := range []string{"initialization", "comparison"} {
		for _, mode := range []string{"failed-refresh", "unreachable", "missing", "stale-law", "changed-location", "verified", "code-only-after-proof"} {
			t.Run(boundary+"/"+mode, func(t *testing.T) {
				s, home, workID := standaloneInventoryFixture(t)
				if boundary == "comparison" {
					ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
					if err != nil {
						t.Fatal(err)
					}
					if err := initializeStandaloneInventory(t, s, ctx, workID); err != nil {
						t.Fatal(err)
					}
				}
				var version int64
				if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "failed-refresh":
					writeKnowledgeFile(t, home.RepoPath, ".concord/law/decisions/CD-0999-broken.md", "# A law file without metadata\n")
					commitKnowledgeRepo(t, home.RepoPath, "commit a law file whose metadata does not load")
				case "unreachable":
					if err := os.Rename(filepath.Join(home.RepoPath, ".git"), filepath.Join(home.RepoPath, "offline.git")); err != nil {
						t.Fatal(err)
					}
				}
				ctx := t.Context()
				if mode != "missing" {
					var err error
					ctx, err = s.EstablishKnowledgeSourceSetProofForProject(ctx, home.HomeProjectID)
					if err != nil {
						t.Fatalf("standalone preparation must defer failures to the consumer: %v", err)
					}
				}
				var refreshErr error
				if mode == "failed-refresh" || mode == "unreachable" {
					proof := knowledgeSourceSetProofFrom(ctx)
					if proof == nil || proof.refreshErr == nil {
						t.Fatal("fixture did not retain a failed standalone verification")
					}
					refreshErr = proof.refreshErr
				}
				switch mode {
				case "stale-law":
					seedLawChangeLaw(t, s, "law-peer", "derived", "agent-surface")
				case "changed-location":
					moved := t.TempDir()
					lawChangeFold(t, s, `UPDATE project_locators SET locator_value=?,normalized_value=? WHERE locator_id=?`, moved, moved, home.HomeLocatorID)
				case "code-only-after-proof":
					writeKnowledgeFile(t, home.RepoPath, "unrelated.go", "package synthetic\n")
					commitKnowledgeRepo(t, home.RepoPath, "code only after proof")
				}
				var err error
				if boundary == "initialization" {
					err = initializeStandaloneInventory(t, s, ctx, workID)
				} else {
					tx := lawChangeTx(t, s)
					err = checkWorkflowLawChangeEnactmentTx(ctx, tx, workID, nil, nil)
					_ = tx.Rollback()
				}
				if mode == "verified" || mode == "code-only-after-proof" {
					if err != nil {
						t.Fatalf("verified standalone inventory refused: %v", err)
					}
					var raw string
					if err := s.db.QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id=?`, workID).Scan(&raw); err != nil || !strings.Contains(raw, "project/spec:one") {
						t.Fatalf("verified baseline did not capture the committed law: %s error=%v", raw, err)
					}
					return
				}
				switch {
				case err == nil:
					t.Errorf("%s accepted %s standalone inventory", boundary, mode)
				case refreshErr != nil:
					if !errors.Is(err, refreshErr) {
						t.Errorf("verification failure was not propagated: got %v, want %v", err, refreshErr)
					}
				default:
					requireFailureKind(t, err, KindInvalidOperation, "standalone inventory proof")
					message := map[string]string{
						"missing":          "was not verified before this transaction",
						"stale-law":        "moved its committed law content after its verification",
						"changed-location": "resolved to a different canonical location after its verification",
					}[mode]
					if !strings.Contains(err.Error(), message) {
						t.Errorf("wrong %s refusal: %v", mode, err)
					}
				}
				var count int
				if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM workflow_instances WHERE work_id=?)+(SELECT count(*) FROM workflow_law_baselines WHERE work_id=?)`, workID, workID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				want := 0
				if boundary == "comparison" {
					want = 2
				}
				if count != want {
					t.Errorf("failed %s left unexpected instance/baseline rows: got %d, want %d", boundary, count, want)
				}
				var afterVersion int64
				if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&afterVersion); err != nil || afterVersion != version {
					t.Errorf("failed boundary changed work: version=%d want=%d error=%v", afterVersion, version, err)
				}
			})
		}
	}
}

// A standalone verification failure is demand-driven: a law-free transition
// sharing the preparation keeps its admission, and a mandate that reads the
// snapshot consumes the recorded failure.
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
