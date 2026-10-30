package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reviewObservedTree(t *testing.T, repo, commit string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw, err := (ExecGitRunner{}).Run(ctx, repo, "rev-parse", commit+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func TestReviewDispositionRepairMustNotDemandEvaluator(t *testing.T) {
	f := newNativeOracleFixture(t)
	if err := os.WriteFile(filepath.Join(f.repo, "pkg/value.go"), []byte("package oracle\nfunc Value() int { return 8 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "broken-baseline")
	baselineHead, baselinePreparation := f.head, f.prepare
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: baselinePreparation.OperationRef}
	baseline, err := f.s.VerifyWorktree(t.Context(), f.request("review-baseline", &o))
	if err != nil || baseline.Oracle == nil || baseline.Oracle.Qualification != "fail" || baseline.Oracle.SubjectCommit != baselineHead {
		t.Fatalf("B did not expose the defect: %+v %v", baseline.Oracle, err)
	}
	var retainedBaseline string
	if err := f.s.db.QueryRow(`SELECT result_json FROM worktree_verify_leases WHERE lease_id=?`, baseline.LeaseID).Scan(&retainedBaseline); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, "pkg/value.go"), []byte("package oracle\nfunc Value() int { return 3 + 4 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.commitAndPrepare(t, "review-repaired-candidate")
	candidate := f.head
	o = NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(t.Context(), f.request("review-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.Oracle.SubjectCommit != candidate || r.SubjectRef != "commit:"+candidate {
		t.Fatalf("verification of repaired C: %+v err=%v", r.Oracle, err)
	}
	if f.launches != 2 || r.Oracle.InputManifestDigest == "" || r.Oracle.InputManifestDigest == baseline.Oracle.InputManifestDigest || r.Oracle.BinaryDigest == "" {
		t.Fatalf("C was not actually built and executed: %+v launches=%d", r.Oracle, f.launches)
	}
	if baselinePreparation.Oracle.SubjectCommit != baselineHead || baselineHead == candidate || f.prepare.OperationRef == baselinePreparation.OperationRef {
		t.Fatal("baseline preparation was relabelled as C")
	}
	if err := validateNativeOracleProducerTx(t.Context(), f.s.db, f.work, &r, "pass"); err != nil {
		t.Fatalf("producer cannot resolve its own C receipt: %v", err)
	}
	if err := validateNativeOracleProducerTx(t.Context(), f.s.db, f.work, &baseline, "fail"); err != nil {
		t.Fatalf("B's failing native receipt no longer resolves: %v", err)
	}
	var after string
	if err := f.s.db.QueryRow(`SELECT result_json FROM worktree_verify_leases WHERE lease_id=?`, baseline.LeaseID).Scan(&after); err != nil || after != retainedBaseline {
		t.Fatalf("B's failing receipt changed during repair: %v", err)
	}
	forged := r
	forgedOracle := *r.Oracle
	forgedOracle.SubjectCommit = baselineHead
	forged.Oracle, forged.SubjectRef = &forgedOracle, "commit:"+baselineHead
	if err := validateNativeOracleProducerTx(t.Context(), f.s.db, f.work, &forged, "pass"); err == nil {
		t.Fatal("a caller-bound B subject overwrote the native C receipt")
	}
}

func commitReviewRepair(t *testing.T, f *nativeOracleFixture, path string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.repo, path), []byte("package oracle\nfunc Value() int { return 7 } // scoped repair\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.repo, "add", path)
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "scoped repair")
	head, err := (ExecGitRunner{}).Run(t.Context(), f.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(head))
}

func TestReviewRepairNativeScopeAndIdentityNegatives(t *testing.T) {
	for _, tc := range []string{"dirty", "unrelated", "changed-subject", "foreign-preparation", "foreign-control", "wrong-recipe"} {
		t.Run(tc, func(t *testing.T) {
			f := newNativeOracleFixture(t)
			o := f.a
			switch tc {
			case "dirty":
				if err := os.WriteFile(filepath.Join(f.repo, "untracked"), []byte("dirty\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "unrelated":
				gitRunStore(t, f.repo, "checkout", "--orphan", "unrelated")
				gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "unrelated")
			case "changed-subject":
				commitReviewRepair(t, f, "pkg/value.go")
				if _, err := f.s.VerifyWorktree(t.Context(), f.request("changed-subject", nil)); err != nil {
					t.Fatal(err)
				}
			case "foreign-preparation":
				o.PreparationRunRef = "worktree_verify:invented"
			case "foreign-control":
				memberMigrationExec(t, f.s.db, `UPDATE worktree_verify_leases SET native_plan_json=json_set(native_plan_json,'$.bundle.control.control_id','control:invented') WHERE lease_id=?`, f.prepare.LeaseID)
			case "wrong-recipe":
				memberMigrationExec(t, f.s.db, `UPDATE worktree_verify_leases SET result_json=json_set(result_json,'$.oracle.recipe_source.path','pkg/foreign.json') WHERE lease_id=?`, f.prepare.LeaseID)
			}
			before := outsideRepairDatabaseSnapshot(t, f.s)
			_, err := f.s.VerifyWorktree(t.Context(), f.request("repair-negative", &o))
			if err == nil {
				t.Fatal("invalid native repair verification ran")
			}
			f.assertNoLaunch(t)
			if outsideRepairDatabaseSnapshot(t, f.s) != before {
				t.Fatal("refused native verification changed durable state")
			}
		})
	}
}

func TestReviewRepairSubjectRejectsDirtyUnrelatedOrFailedObservation(t *testing.T) {
	for _, tc := range []string{"dirty", "unrelated", "failed-git"} {
		t.Run(tc, func(t *testing.T) {
			j := pReturnReviewFixture(t, "review-repair-subject", "workflow.break_fix", "")
			descendantCommit(t, j, "repair")
			qualifyReviewSubjectForTest(t, j.s, j.workID, j.repo)
			if tc == "dirty" {
				if err := os.WriteFile(filepath.Join(j.repo, "dirty"), []byte("dirty\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc == "unrelated" {
				gitRunStore(t, j.repo, "checkout", "--orphan", "unrelated")
				gitRunStore(t, j.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "unrelated")
			}
			var runner GitRunner
			if tc == "failed-git" {
				runner = failingReviewGitRunner{}
			}
			before := outsideRepairDatabaseSnapshot(t, j.s)
			prepared, err := PrepareWorkflowAcceptanceFreshness(t.Context(), j.s, PrepareWorkflowSubjectRequest{WorkID: j.workID, Runner: runner})
			if err == nil || prepared != nil || outsideRepairDatabaseSnapshot(t, j.s) != before {
				t.Fatalf("invalid subject observation for %s landed: %+v %v", tc, prepared, err)
			}
		})
	}
}

type failingReviewGitRunner struct{}

func (failingReviewGitRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, errors.New("synthetic Git observation failure")
}
