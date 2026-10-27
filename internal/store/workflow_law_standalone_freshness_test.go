package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Index real committed law while its home is designated, then remove the
// designation. A standalone home retains those rows but cannot rebuild them
// without a Product home; inventory admission must not mistake them for proof.
func standaloneInventoryFixture(t *testing.T) (*Store, KnowledgeHome, string) {
	t.Helper()
	s := openTemp(t)
	const workID = "standalone-inventory-work"
	seedWork(t, s, workID)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "standalone-locator", RepoPath: initKnowledgeRepo(t), HeadRef: "HEAD"}
	writeStandaloneInventoryLaw(t, home.RepoPath, "# Standalone law\n")
	commitKnowledgeRepo(t, home.RepoPath, "standalone inventory law")
	authorizeKnowledgeProductHome(t, s, "product", home)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	lawChangeFold(t, s, `DELETE FROM product_knowledge_homes WHERE product_id='product'`)
	productID, _, err := resolveKnowledgeSourceRole(t.Context(), s.db, home)
	if err != nil || productID != "" {
		t.Fatalf("fixture is not standalone: product=%q error=%v", productID, err)
	}
	return s, home, workID
}

func writeStandaloneInventoryLaw(t *testing.T, repo, content string) {
	t.Helper()
	writeManifestFixture(t, repo, manifestFixture{
		ID: "STANDALONE-LAW", Kind: "spec", Path: ".concord/docs/standalone.md", Status: "accepted",
		Date: "2026-10-01T00:00:00Z", Title: "Standalone law", Summary: "Standalone inventory law", Scopes: homeScope(), Content: content,
	})
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

func TestLawBaselineStandaloneInventoryFreshness(t *testing.T) {
	for _, boundary := range []string{"initialization", "comparison"} {
		for _, mode := range []string{"failed-refresh", "missing", "stale-head", "stale-projection", "changed-location", "unreachable", "verified", "code-only-before-proof"} {
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
				if mode == "failed-refresh" {
					writeStandaloneInventoryLaw(t, home.RepoPath, "# Committed law edit\n")
					commitKnowledgeRepo(t, home.RepoPath, "edit standalone law")
				}
				if mode == "code-only-before-proof" {
					writeKnowledgeFile(t, home.RepoPath, "unrelated.go", "package synthetic\n")
					commitKnowledgeRepo(t, home.RepoPath, "code only before proof")
				}
				if mode == "unreachable" {
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
				if mode == "failed-refresh" {
					proof := knowledgeSourceSetProofFrom(ctx)
					if proof == nil || proof.refreshErr == nil {
						t.Fatal("fixture did not retain a failed standalone refresh")
					}
					refreshErr = proof.refreshErr
				}
				switch mode {
				case "stale-head":
					writeStandaloneInventoryLaw(t, home.RepoPath, "# Edit after verification\n")
					commitKnowledgeRepo(t, home.RepoPath, "move standalone head")
				case "stale-projection":
					lawChangeFold(t, s, `UPDATE knowledge_index_watermark SET scanned_commit_oid=? WHERE home_project_id=? AND home_locator_id=?`, strings.Repeat("f", 40), home.HomeProjectID, home.HomeLocatorID)
				case "changed-location":
					lawChangeFold(t, s, `UPDATE project_locators SET locator_value=? WHERE locator_id=?`, t.TempDir(), home.HomeLocatorID)
				}
				var err error
				if boundary == "initialization" {
					err = initializeStandaloneInventory(t, s, ctx, workID)
				} else {
					tx := lawChangeTx(t, s)
					err = checkWorkflowLawChangeEnactmentTx(ctx, tx, workID, nil, nil)
					_ = tx.Rollback()
				}
				if mode == "verified" || mode == "code-only-before-proof" {
					if err != nil {
						t.Fatalf("verified standalone inventory refused: %v", err)
					}
					var raw string
					if err := s.db.QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id=?`, workID).Scan(&raw); err != nil || !strings.Contains(raw, "project/STANDALONE-LAW") {
						t.Fatalf("verified baseline did not capture real law: %s error=%v", raw, err)
					}
					return
				}
				switch {
				case err == nil:
					t.Errorf("%s accepted %s standalone inventory", boundary, mode)
				case refreshErr != nil:
					if !errors.Is(err, refreshErr) {
						t.Errorf("refresh error was not propagated: got %v, want %v", err, refreshErr)
					}
				case mode == "unreachable":
					requireFailureKind(t, err, KindUnreachable, "unreachable standalone authority")
				default:
					requireFailureKind(t, err, KindInvalidOperation, "standalone inventory proof")
					message := map[string]string{
						"missing":          "was not verified before this transaction",
						"stale-head":       "moved to a new commit after its verification",
						"stale-projection": "is no longer the projection this transaction reads",
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

func TestLawProofStandaloneDemandDrivenConsumers(t *testing.T) {
	for _, mode := range []string{"failed-refresh", "unreachable"} {
		t.Run(mode, func(t *testing.T) {
			s, home, workID := standaloneInventoryFixture(t)
			if mode == "failed-refresh" {
				writeStandaloneInventoryLaw(t, home.RepoPath, "# Committed law edit\n")
				commitKnowledgeRepo(t, home.RepoPath, "standalone refresh fails")
			} else if err := os.Rename(filepath.Join(home.RepoPath, ".git"), filepath.Join(home.RepoPath, "offline.git")); err != nil {
				t.Fatal(err)
			}
			ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
			if err != nil {
				t.Fatal(err)
			}
			proof := knowledgeSourceSetProofFrom(ctx)
			if proof == nil || (mode == "failed-refresh") != (proof.refreshErr != nil) {
				t.Fatal("fixture did not preserve demand-driven refresh behavior")
			}
			tx := lawChangeTx(t, s)
			if err := checkMandatedLawsTxAtHome(ctx, tx, home.HomeProjectID, home.HomeLocatorID, nil, nil, false); err != nil {
				t.Fatalf("law-free legacy transition consumed a deferred error: %v", err)
			}
			err = checkMandatedLawsTxAtHome(ctx, tx, home.HomeProjectID, home.HomeLocatorID, []string{"STANDALONE-LAW"}, nil, false)
			if (proof.refreshErr != nil && !errors.Is(err, proof.refreshErr)) || (proof.refreshErr == nil && err != nil) {
				t.Fatalf("generic mandate changed demand-driven refresh admission: %v", err)
			}
			if err := checkMandatedLawsTxAtHome(t.Context(), tx, home.HomeProjectID, home.HomeLocatorID, []string{"STANDALONE-LAW"}, nil, false); err != nil {
				t.Fatalf("generic standalone mandate unexpectedly requires inventory proof: %v", err)
			}
		})
	}
}
