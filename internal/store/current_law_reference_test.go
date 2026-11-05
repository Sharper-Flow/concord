package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCurrentLawSnapshotUsesDefaultBranchAuthority(t *testing.T) {
	const currentPath = ".concord/law/specs/current-law.md"
	const retainedPath = ".concord/law/specs/retained-law.md"
	const featurePath = ".concord/law/specs/feature-only-law.md"
	mergedContent := lawSnapshotTierLawContent("CURRENT-LAW", "Merged default law", "")
	for _, checkout := range []string{"aligned_main", "stale_main", "feature_addition_and_removal", "detached_feature_commit"} {
		t.Run(checkout, func(t *testing.T) {
			repo, oldHead := lawSnapshotFixture(t, map[string]string{
				currentPath:  lawSnapshotTierLawContent("CURRENT-LAW", "Old checkout law", ""),
				retainedPath: lawSnapshotTierLawContent("RETAINED-LAW", "Retained default law", ""),
				".concord/docs/knowledge/domain-registry.json": lawSnapshotRegistryJSON,
			})
			writeKnowledgeFile(t, repo, currentPath, mergedContent)
			writeKnowledgeFile(t, repo, ".concord/law/specs/merged-only-law.md", lawSnapshotTierLawContent("MERGED-ONLY-LAW", "Merged addition", ""))
			mergedHead := commitKnowledgeRepo(t, repo, "merged default law")
			runKnowledgeGit(t, repo, "update-ref", "--no-deref", "refs/remotes/origin/main", mergedHead)
			runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			if checkout != "aligned_main" {
				runKnowledgeGit(t, repo, "reset", "--hard", oldHead)
			}
			if checkout == "feature_addition_and_removal" || checkout == "detached_feature_commit" {
				runKnowledgeGit(t, repo, "checkout", "-b", "feature-law")
				writeKnowledgeFile(t, repo, currentPath, lawSnapshotTierLawContent("CURRENT-LAW", "Unmerged feature law", ""))
				writeKnowledgeFile(t, repo, featurePath, lawSnapshotTierLawContent("FEATURE-ONLY-LAW", "Unmerged addition", ""))
				runKnowledgeGit(t, repo, "rm", "--", retainedPath)
				featureHead := commitKnowledgeRepo(t, repo, "unmerged feature law addition and removal")
				if checkout == "detached_feature_commit" {
					runKnowledgeGit(t, repo, "checkout", "--detach", featureHead)
				}
			}
			checkoutHead := strings.TrimSpace(runKnowledgeGit(t, repo, "rev-parse", "HEAD"))
			if got := strings.TrimSpace(runKnowledgeGit(t, repo, "rev-parse", "refs/remotes/origin/HEAD")); got != mergedHead {
				t.Fatalf("default authority fixture moved: got %s, want %s", got, mergedHead)
			}
			home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "canonical", RepoPath: repo, HeadRef: "HEAD"}
			snapshot, err := loadLawFolderSnapshotAtHome(t.Context(), home)
			if err != nil {
				t.Fatalf("a resolvable origin/HEAD default must be read, not refused: %v", err)
			}
			if snapshot.CommitOID() != mergedHead {
				t.Errorf("silently read checkout law: snapshot commit=%s checkout HEAD=%s; want origin/HEAD default commit=%s", snapshot.CommitOID(), checkoutHead, mergedHead)
			}
			if got, want := snapshot.LawIDs(), []string{"CURRENT-LAW", "MERGED-ONLY-LAW", "RETAINED-LAW"}; !slices.Equal(got, want) {
				t.Errorf("law inventory=%v, want merged default inventory=%v (feature-only additions and removals are not law)", got, want)
			}
			record, ok := snapshot.Law("CURRENT-LAW")
			sum := sha256.Sum256([]byte(mergedContent))
			wantHash := "sha256:" + hex.EncodeToString(sum[:])
			if !ok || record.SHA256 != wantHash {
				t.Errorf("current law digest=%s present=%t, want merged default digest=%s", record.SHA256, ok, wantHash)
			}
		})
	}
}

func TestCurrentLawDefaultAuthorityRefusal(t *testing.T) {
	for _, mode := range []string{"missing", "direct_oid", "local_branch", "self", "traversal", "ref_expression", "unresolved", "missing_commit", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			repo, commit := lawSnapshotFixture(t, map[string]string{
				".concord/law/specs/current.md": lawSnapshotTierLawContent("CURRENT", "Current", ""),
				knowledgeRegistryPath:           lawSnapshotRegistryJSON,
			})
			refPath := filepath.Join(repo, ".git/refs/remotes/origin/HEAD")
			if mode == "missing" {
				if err := os.Remove(refPath); err != nil {
					t.Fatal(err)
				}
			} else {
				value := map[string]string{
					"direct_oid":     commit,
					"local_branch":   "ref: refs/heads/main",
					"self":           "ref: refs/remotes/origin/HEAD",
					"traversal":      "ref: refs/remotes/origin/../../heads/main",
					"ref_expression": "ref: refs/remotes/origin/main^{commit}",
					"unresolved":     "ref: refs/remotes/origin/missing",
					"missing_commit": "ref: refs/remotes/origin/main",
					"cycle":          "ref: refs/remotes/origin/cycle",
				}[mode]
				if mode == "cycle" {
					runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/cycle", "refs/remotes/origin/cycle")
				}
				if mode == "missing_commit" {
					if err := os.WriteFile(filepath.Join(repo, ".git/refs/remotes/origin/main"), []byte(strings.Repeat("a", 40)+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(refPath, []byte(value+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			_, err := loadLawFolderSnapshotAtHome(t.Context(), KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "home", RepoPath: repo, HeadRef: commit})
			var failure *Failure
			if !errors.As(err, &failure) || failure.Kind != KindGitUnreachable || !strings.Contains(failure.Detail, "default") || !strings.Contains(failure.RecoveryAction, "git fetch origin") || !strings.Contains(failure.RecoveryAction, "git remote set-head origin --auto") {
				t.Fatalf("missing precise default authority recovery: %v", err)
			}
			// An explicit historical commit does not require current authority.
			if snapshot, err := loadLawFolderSnapshot(t.Context(), repo, commit); err != nil || snapshot.CommitOID() != commit {
				t.Fatalf("explicit historical read changed: snapshot=%v error=%v", snapshot, err)
			}
		})
	}
}

func TestCurrentLawProofBindsConcreteDefaultRef(t *testing.T) {
	for _, mode := range []string{"retarget_same_content", "default_law_moved", "default_registry_moved", "default_code_only", "checkout_only", "dirty_checkout"} {
		t.Run(mode, func(t *testing.T) {
			s := seedLawChangeFixture(t, "default-proof-work")
			repo := workflowLawFixtureRepo(t, s)
			defaultHead := strings.TrimSpace(runKnowledgeGit(t, repo, "rev-parse", "HEAD"))
			runKnowledgeGit(t, repo, "update-ref", "--no-deref", "refs/remotes/origin/main", defaultHead)
			ctx, err := establishKnowledgeSourceSetProofForProject(t.Context(), s, "project")
			if err != nil {
				t.Fatal(err)
			}
			proof := knowledgeSourceSetProofFrom(ctx)
			if proof == nil || len(proof.sources) != 1 || proof.sources[0].HeadRef != "refs/remotes/origin/main" {
				t.Fatalf("proof did not retain the concrete default ref: %+v", proof)
			}
			source := proof.sources[0]
			if got := proof.snapshots[knowledgeSourceProofKey(source)].CommitOID(); got != defaultHead {
				t.Fatalf("proof snapshot=%s, want %s", got, defaultHead)
			}
			switch mode {
			case "retarget_same_content":
				runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/other", defaultHead)
				runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/other")
			case "default_law_moved", "default_registry_moved", "default_code_only":
				switch mode {
				case "default_law_moved":
					writeKnowledgeFile(t, repo, ".concord/law/specs/new-default-law.md", fixtureLawFolderLaw("NEW-DEFAULT-LAW"))
				case "default_registry_moved":
					writeKnowledgeFile(t, repo, knowledgeRegistryPath, fixtureLawFolderRegistry+"\n")
				case "default_code_only":
					writeKnowledgeFile(t, repo, "code.txt", "unrelated code\n")
				}
				moved := commitKnowledgeRepo(t, repo, "move default content")
				runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", moved)
				runKnowledgeGit(t, repo, "reset", "--hard", defaultHead)
			case "checkout_only":
				runKnowledgeGit(t, repo, "checkout", "-b", "unmerged")
				writeKnowledgeFile(t, repo, ".concord/law/specs/feature-only.md", fixtureLawFolderLaw("FEATURE-ONLY"))
				commitKnowledgeRepo(t, repo, "feature law is not default law")
			case "dirty_checkout":
				writeKnowledgeFile(t, repo, ".concord/law/specs/dirty.md", "malformed uncommitted law\n")
				writeKnowledgeFile(t, repo, knowledgeRegistryPath, "invalid dirty registry\n")
			}
			before := runKnowledgeGit(t, repo, "status", "--porcelain")
			commandPath := os.Getenv("PATH")
			t.Setenv("PATH", t.TempDir())
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = requireKnowledgeSourceSetProof(ctx, tx, source.HomeProjectID, source.HomeLocatorID)
			_ = tx.Rollback()
			t.Setenv("PATH", commandPath)
			refuse := mode == "retarget_same_content" || mode == "default_law_moved" || mode == "default_registry_moved"
			if refuse {
				requireFailureKind(t, err, KindInvalidOperation, mode)
			} else if err != nil {
				t.Fatalf("unchanged default law was refused: %v", err)
			}
			if got := runKnowledgeGit(t, repo, "status", "--porcelain"); got != before {
				t.Fatalf("law read mutated checkout: before=%q after=%q", before, got)
			}
			if mode == "dirty_checkout" || mode == "checkout_only" {
				snapshot, err := loadLawFolderSnapshotAtHome(t.Context(), source)
				if err != nil || snapshot.CommitOID() != defaultHead {
					t.Fatalf("checkout leaked into current-law snapshot: %v, %v", snapshot, err)
				}
			}
		})
	}
}

func TestCurrentLawDefaultRefPackedAndLinkedWorktree(t *testing.T) {
	repo, commit := lawSnapshotFixture(t, map[string]string{"README.md": "synthetic repository\n"})
	runKnowledgeGit(t, repo, "update-ref", "--no-deref", "refs/remotes/origin/main", commit)
	runKnowledgeGit(t, repo, "pack-refs", "--all", "--prune")
	linked := filepath.Join(t.TempDir(), "linked")
	runKnowledgeGit(t, repo, "worktree", "add", "--detach", linked, commit)
	for _, home := range []string{repo, linked} {
		ref, err := resolveDefaultLawRefCheap(home)
		if err != nil || ref != "refs/remotes/origin/main" {
			t.Fatalf("default ref=%q, error=%v", ref, err)
		}
		if got, err := resolveKnowledgeHeadCheap(home, ref); err != nil || got != commit {
			t.Fatalf("default commit=%q, error=%v", got, err)
		}
	}
}

func TestCurrentLawMigrationAccountsDefaultNotFeatureLaw(t *testing.T) {
	for _, mode := range []string{"default_only", "feature_only", "retarget_after_proof", "default_moves_after_proof"} {
		t.Run(mode, func(t *testing.T) {
			repo := initKnowledgeRepo(t)
			files := map[string]string{}
			content := fixtureLawFolderLaw(lawIndexCurrentLawID)
			if mode != "feature_only" {
				files[lawIndexCurrentPath] = content
			}
			defaultHead := writeLawIndexHome(t, repo, files)
			runKnowledgeGit(t, repo, "update-ref", "--no-deref", "refs/remotes/origin/main", defaultHead)
			runKnowledgeGit(t, repo, "checkout", "-b", "feature")
			if mode == "feature_only" {
				writeKnowledgeFile(t, repo, lawIndexCurrentPath, content)
			} else {
				runKnowledgeGit(t, repo, "rm", lawIndexCurrentPath)
			}
			featureHead := commitKnowledgeRepo(t, repo, "unmerged feature law inventory")
			db, _ := lawIndexRetirementFixture(t, repo, defaultHead, []lawIndexSubjectSeed{{lawID: lawIndexCurrentLawID, recordPath: lawIndexCurrentPath, content: content}})
			before := retirementUpgradeSnapshot(t, db)
			proof, err := establishLawIndexRetirementProof(t.Context(), db)
			if mode == "feature_only" {
				assertFailureKind(t, err, KindUpgradeBlocked)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				key := lawIndexHomeKey("project-upgrade", lawIndexRetirementLocator)
				if proof.heads[key] != defaultHead || proof.defaultRefs[key] != "refs/remotes/origin/main" {
					t.Fatalf("accounting did not bind the default ref and commit: %+v", proof)
				}
				switch mode {
				case "retarget_after_proof":
					runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/other", defaultHead)
					runKnowledgeGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/other")
				case "default_moves_after_proof":
					runKnowledgeGit(t, repo, "update-ref", "refs/remotes/origin/main", featureHead)
				}
				tx, err := db.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}
				err = verifyLawIndexRetirementProof(t.Context(), tx, proof)
				_ = tx.Rollback()
				if mode == "default_only" {
					if err != nil {
						t.Fatalf("feature checkout invalidated default accounting: %v", err)
					}
				} else {
					assertFailureKind(t, err, KindUpgradeBlocked)
				}
			}
			after := retirementUpgradeSnapshot(t, db)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("accounting or refusal mutated the legacy database")
			}
		})
	}
}
