package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLawProofPreservesSourceResolutionFailure(t *testing.T) {
	t.Parallel()
	for _, designated := range []bool{false, true} {
		role := "standalone"
		if designated {
			role = "designated"
		}
		for _, mode := range []string{"removed-home", "missing-default-authority"} {
			t.Run(role+"/"+mode, func(t *testing.T) {
				t.Parallel()
				s, home := lawConflictFolderFixture(t, "derived", "derived", "")
				if designated {
					authorizeKnowledgeProductHome(t, s, "prod", home)
				}
				if mode == "removed-home" {
					if err := os.RemoveAll(home.RepoPath); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Remove(filepath.Join(home.RepoPath, ".git", "refs", "remotes", "origin", "HEAD")); err != nil {
					t.Fatal(err)
				}
				ctx, err := s.EstablishKnowledgeSourceSetProofForProject(t.Context(), home.HomeProjectID)
				if !designated {
					if err != nil {
						t.Fatalf("standalone preparation must defer its source failure: %v", err)
					}
					proof := knowledgeSourceSetProofFrom(ctx)
					if proof == nil {
						t.Fatal("standalone preparation lost its deferred source failure")
					}
					err = proof.refreshErr
				}
				var failure *Failure
				if !failureAs(err, &failure) || failure.Kind != KindGitUnreachable || failure.Op != "knowledge_home" || !failure.RetrySafe || failure.RecoveryAction != defaultLawRefRecovery {
					t.Fatalf("source resolution failure = %v, want the default-authority git_unreachable refusal unchanged", err)
				}
			})
		}
	}
}

func TestLawProofLeavesHomeIdentityRefusalsToBoundary(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unknown", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			const workID = "law-proof-home-identity"
			seedWork(t, s, workID)
			want := KindUnknownScope
			if mode == "ambiguous" {
				seedWorkflowLaw(t, s)
				seedSecondaryProductMembership(t, s, workID)
				if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); INSERT INTO product_projects(product_id,project_id,role) VALUES('product-secondary','project','secondary'); DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
				want = KindAmbiguousScope
			}
			ctx := t.Context()
			prepared, err := s.EstablishKnowledgeSourceSetProof(ctx, workID)
			if err != nil || prepared != ctx || knowledgeSourceSetProofFrom(prepared) != nil {
				t.Fatalf("preparation took ownership of unresolved home identity: context=%v error=%v", prepared, err)
			}
			_, _, err = workflowLawHome(prepared, s.db, workID)
			requireFailureKind(t, err, want, "home identity stays with its owning boundary")
		})
	}
}
