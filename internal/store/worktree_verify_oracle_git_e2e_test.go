package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nativeGitWitnessScript = `import os, pathlib, subprocess, tempfile
root = pathlib.Path.cwd()
def git(*args, cwd=root):
    return subprocess.check_output(["git", *args], cwd=cwd, text=True).strip()
assert (root / ".git").is_dir()
assert git("rev-parse", "--show-toplevel", cwd=root / "scripts") == str(root)
assert git("rev-parse", "HEAD^{tree}") == git("write-tree")
assert set(git("ls-files").splitlines()) == {"recipe.json", "value.txt", "scripts/check.py", "scripts/check.sh"}
assert not (root / "recipe.json").exists(), "producer must retain the harness overlay policy"
assert "candidate harness" in git("show", "HEAD:scripts/check.py")
assert (root / "scripts/check.py").read_text().startswith("import os")
assert git("describe", "--tags", "--abbrev=0") == "v0.1.0"
assert git("tag", "--list").splitlines() == ["v0.0.9", "v0.1.0"]
assert git("cat-file", "-t", "refs/tags/v0.1.0") == "tag"
assert git("cat-file", "-t", "refs/tags/v0.0.9") == "commit"
assert not any(x.startswith("remote.") or x.startswith("credential.") or x.startswith("include.") for x in git("config", "--name-only", "--list").splitlines())
assert not (root / ".git/objects/info/alternates").exists()
assert not (root / ".git/hooks").exists()
assert not os.environ.get("GIT_DIR") and not os.environ.get("GIT_WORK_TREE")
assert not os.environ.get("CONCORD_GIT_CREDENTIAL_CANARY")
git("status", "--porcelain")
with tempfile.TemporaryDirectory() as other:
    git("init", "--template=", cwd=other)
    assert git("rev-parse", "--show-toplevel", cwd=other) == other
print("candidate git witness " + git("rev-parse", "HEAD"))
`

func newNativeGitTargetFixture(t *testing.T) *nativeTargetFixture {
	t.Helper()
	f := newNativeTargetCheckFixtureAtRoot(t, ".")
	gitRunStore(t, f.repo, "tag", "v0.0.9")
	gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "tag", "-a", "v0.1.0", "-m", "release")
	orphan, err := (ExecGitRunner{}).Run(context.Background(), f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit-tree", f.head+"^{tree}", "-m", "unrelated release")
	if err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.repo, "tag", "v9.9.9", strings.TrimSpace(string(orphan)))
	var m map[string]any
	if err := json.Unmarshal([]byte(f.manifestJSON), &m); err != nil {
		t.Fatal(err)
	}
	m["git_metadata"] = nativeGitMetadataMode
	f.setManifestForTest(t, m)
	writeRepoFilesForTest(t, f.repo, map[string]string{"scripts/check.py": nativeGitWitnessScript})
	f.commitAndPrepareRepin(t, "git-recipe")
	writeRepoFilesForTest(t, f.repo, map[string]string{"scripts/check.py": "# candidate harness\nraise RuntimeError('candidate harness must not run')\n"})
	f.commitAndPrepare(t, "candidate-harness")
	return f
}

func TestNativeTargetGitMetadataProducer(t *testing.T) {
	f := newNativeGitTargetFixture(t)
	t.Setenv("CONCORD_GIT_CREDENTIAL_CANARY", "must-not-reach-runner")
	hooks := t.TempDir()
	canary := filepath.Join(t.TempDir(), "source-hook-ran")
	writeRepoFilesForTest(t, hooks, map[string]string{"reference-transaction": fmt.Sprintf("#!/bin/sh\nprintf 'ran' > %q\n", canary)})
	if err := os.Chmod(filepath.Join(hooks, "reference-transaction"), 0700); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, f.repo, "config", "remote.origin.url", "https://example.invalid/private-source")
	gitRunStore(t, f.repo, "config", "credential.helper", "!exit 99")
	gitRunStore(t, f.repo, "config", "core.hooksPath", hooks)
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("git-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" {
		t.Fatalf("candidate Git execution: %+v, %v", r, err)
	}
	if !strings.Contains(r.Output, "candidate git witness "+f.head) || f.launches != 2 {
		t.Fatalf("candidate witness or launch count missing: %q, %d", r.Output, f.launches)
	}
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Fatalf("metadata construction executed a source hook: %v", err)
	}
	if r.Oracle.GitMetadata == nil || r.Oracle.GitMetadata.Candidate.Commit != f.head || len(r.Oracle.GitMetadata.Candidate.Tags) != 2 || r.Oracle.GitMetadata.Recipe.Commit != f.bundle.Control.RecipeSource.CommitOID {
		t.Fatalf("metadata descriptor does not bind the two real views: %+v", r.Oracle.GitMetadata)
	}
	assertNativeTargetPublicWire(t, f.prepare)
	assertNativeTargetPublicWire(t, r)
	if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &r, "pass"); err != nil {
		t.Fatalf("native producer receipt refused: %v", err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var forged WorktreeVerifyResult
	if err := json.Unmarshal(raw, &forged); err != nil {
		t.Fatal(err)
	}
	forged.Oracle.GitMetadata.Candidate.Tags[0].Ref = "refs/tags/forged"
	if err := validateNativeOracleProducerTx(context.Background(), f.s.db, f.work, &forged, "pass"); err == nil {
		t.Fatal("a receipt could replace its prepared tag descriptor")
	}
	if _, err := f.s.VerifyWorktree(context.Background(), f.request("git-execute", &o)); err != nil || f.launches != 2 {
		t.Fatalf("native replay relaunched or refused: %v, %d", err, f.launches)
	}
}

func TestNativeTargetGitMetadataTagDriftRequiresPreparation(t *testing.T) {
	f := newNativeGitTargetFixture(t)
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	before := f.prepare.Oracle.BuildEnvironmentDigest
	gitRunStore(t, f.repo, "tag", "new-candidate-tag")
	r, err := f.s.VerifyWorktree(context.Background(), f.request("git-tag-drift", &o))
	if failureKind(err) != KindMissingEvidence || !strings.Contains(err.Error(), "environment or pinned inputs") || f.launches != 0 || r.Oracle != nil && r.Oracle.Qualification == "pass" {
		t.Fatalf("tag drift launched with old readiness: %+v, %v, launches=%d", r, err, f.launches)
	}
	f.prepareWithCurrentBundle(t, "git-tag-reprepare")
	if f.prepare.Oracle.BuildEnvironmentDigest == before {
		t.Fatal("new genuine tag did not re-pin readiness")
	}
}

func TestNativeTargetGitMetadataMutationCannotPass(t *testing.T) {
	f := newNativeGitTargetFixture(t)
	f.s.nativeOracleStageObserved = func(stage, cwd string, argv, env []string) {
		if stage != "test" {
			return
		}
		config := filepath.Join(cwd, ".git", "config")
		data, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config, append(data, []byte("\n[test]\nvalue = changed\n")...), 0600); err != nil {
			t.Fatal(err)
		}
	}
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("git-mutated-execute", &o))
	if err == nil || !strings.Contains(err.Error(), ".git/config") || r.Oracle == nil || r.Oracle.Qualification != "unavailable" || f.launches != 1 {
		t.Fatalf("mutated Git inputs did not refuse through sealed inventory: %+v, %v, launches=%d", r, err, f.launches)
	}
}

func TestNativeTargetGitMetadataPytest(t *testing.T) {
	f := newNativeTargetFixtureAtRoot(t, ".")
	gitRunStore(t, f.repo, "tag", "v0.1.0")
	var m map[string]any
	if err := json.Unmarshal([]byte(f.manifestJSON), &m); err != nil {
		t.Fatal(err)
	}
	m["git_metadata"] = nativeGitMetadataMode
	f.setManifestForTest(t, m)
	writeRepoFilesForTest(t, f.repo, map[string]string{"tests/test_value.py": `from pathlib import Path
import subprocess
from app.value import VALUE

def test_value():
    assert VALUE == 7
    assert Path('.git').is_dir()
    assert subprocess.check_output(['git', 'describe', '--tags', '--abbrev=0'], text=True).strip() == 'v0.1.0'
    assert subprocess.check_output(['git', 'rev-parse', '--show-toplevel'], text=True).strip() == str(Path.cwd())
    assert 'app/value.py' in subprocess.check_output(['git', 'ls-files'], text=True).splitlines()
`})
	f.commitAndPrepareRepin(t, "git-pytest")
	o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(context.Background(), f.request("git-pytest-execute", &o))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.Oracle.GitMetadata == nil || f.launches != 1 {
		t.Fatalf("metadata-aware pytest: %+v, %v, launches=%d", r, err, f.launches)
	}
	assertNativeTargetPublicWire(t, r)
}

func TestNativeTargetGitMetadataCapacity(t *testing.T) {
	f := newNativeGitTargetFixture(t)
	var updates strings.Builder
	for i := range 80 {
		fmt.Fprintf(&updates, "create refs/tags/capacity-%03d %s\n", i, f.bundle.Control.RecipeSource.CommitOID)
	}
	if _, err := (ExecGitRunner{}).RunStdin(context.Background(), f.repo, []byte(updates.String()), "update-ref", "--stdin"); err != nil {
		t.Fatal(err)
	}
	f.prepareWithCurrentBundle(t, "git-capacity")
	if len(workflowJSON(f.prepare.Oracle.GitMetadata)) <= 16*1024 || len(f.prepare.Oracle.GitMetadata.Candidate.Tags) != 82 {
		t.Fatalf("capacity case did not exercise the full tag inventory: %+v", f.prepare.Oracle.GitMetadata)
	}
	assertNativeTargetPublicWire(t, f.prepare)
	if _, err := readOraclePreparationReceiptTx(context.Background(), f.s.db, f.work, "project", f.prepare.OperationRef); err != nil {
		t.Fatalf("full metadata preparation did not survive its receipt join: %v", err)
	}
	first := f.prepare
	writeRepoFilesForTest(t, f.repo, map[string]string{"value.txt": "7\n\n"})
	f.commitAndPrepare(t, "git-capacity-next")
	view, err := readWorkContextView(t.Context(), f.s.db, f.work)
	if err != nil || view == nil {
		t.Fatalf("two full-tag preparations blocked work-context reads: %v", err)
	}
	if len(view.OraclePreparations) != 2 {
		t.Fatalf("context selected %d preparations, want original and current", len(view.OraclePreparations))
	}
	raw, err := json.Marshal(view.OraclePreparations)
	if err != nil || strings.Contains(string(raw), "git_metadata") {
		t.Fatalf("work context duplicated full receipt-only Git inventories: %s, %v", raw, err)
	}
	for _, prep := range view.OraclePreparations {
		want := first.Oracle.BuildEnvironmentDigest
		if prep.RunRef == f.prepare.OperationRef {
			want = f.prepare.Oracle.BuildEnvironmentDigest
		} else if prep.RunRef != first.OperationRef {
			t.Fatalf("context replaced its qualified preparation: %s", prep.RunRef)
		}
		if prep.BuildEnvironmentDigest != want {
			t.Fatal("compact context lost its descriptor-bound environment identity")
		}
	}
}

func TestNativeTargetGitMetadataChangedCandidateRequiresPreparation(t *testing.T) {
	for _, tc := range []string{"scoped", "outside", "tag-added", "tag-moved", "base-tag-added"} {
		t.Run(tc, func(t *testing.T) {
			f := newNativeGitTargetFixture(t)
			o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
			original := f.prepare
			path := "value.txt"
			if tc == "outside" {
				path = "outside.txt"
			}
			writeRepoFilesForTest(t, f.repo, map[string]string{path: "7\n\n"})
			gitRunStore(t, f.repo, "add", path)
			gitRunStore(t, f.repo, "-c", "user.email=oracle@example.invalid", "-c", "user.name=Oracle", "commit", "-m", "scoped value repair")
			head, err := (ExecGitRunner{}).Run(t.Context(), f.repo, "rev-parse", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			candidate := strings.TrimSpace(string(head))
			switch tc {
			case "tag-added":
				gitRunStore(t, f.repo, "tag", "new-repair-tag")
			case "tag-moved":
				gitRunStore(t, f.repo, "tag", "-f", "v0.0.9")
			case "base-tag-added":
				gitRunStore(t, f.repo, "tag", "new-base-tag", f.head)
			}
			r, err := f.s.VerifyWorktree(t.Context(), f.request("git-review-execute", &o))
			if failureKind(err) != KindMissingEvidence || f.launches != 0 {
				t.Fatalf("stale candidate preparation ran: %+v, %v, launches=%d", r, err, f.launches)
			}
			f.head = candidate
			if _, err := f.s.VerifyWorktree(t.Context(), f.request("git-current-subject", nil)); err != nil {
				t.Fatal(err)
			}
			f.prepareWithCurrentBundle(t, "git-current-prepare")
			o = NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
			r, err = f.s.VerifyWorktree(t.Context(), f.request("git-current-execute", &o))
			qualification, launches := "pass", 2
			if tc != "scoped" {
				// Fresh preparation admits the subject, not a different harness:
				// the pinned check still rejects changed file/tag inventories.
				qualification, launches = "fail", 1
			}
			if err != nil || r.Oracle == nil || r.Oracle.Qualification != qualification {
				t.Fatalf("fresh metadata preparation refused: %+v, %v", r, err)
			}
			if r.Oracle.SubjectCommit != candidate || r.Oracle.GitMetadata.Candidate.Commit != candidate || f.launches != launches {
				t.Fatalf("repair did not execute its actual Git candidate: %+v, output=%q, launches=%d", r.Oracle, r.Output, f.launches)
			}
			if qualification == "pass" && !strings.Contains(r.Output, "candidate git witness "+candidate) || qualification == "fail" && r.ExitCode == 0 {
				t.Fatalf("pinned Git harness result differs from its candidate: %+v", r)
			}
			if r.Oracle.BuildEnvironmentDigest != f.prepare.Oracle.BuildEnvironmentDigest || original.Oracle.GitMetadata.Candidate.Commit == candidate || f.prepare.Oracle.SubjectCommit != candidate {
				t.Fatal("repair relabelled the original preparation")
			}
			assertNativeTargetPublicWire(t, r)
			if err := validateNativeOracleProducerTx(t.Context(), f.s.db, f.work, &r, qualification); err != nil {
				t.Fatalf("native repaired receipt cannot join its fresh preparation: %v", err)
			}
			r.Oracle.GitMetadata.Candidate.Commit = original.Oracle.SubjectCommit
			if err := validateNativeOracleProducerTx(t.Context(), f.s.db, f.work, &r, qualification); err == nil {
				t.Fatal("repair receipt could replace its actual candidate with the preparation subject")
			}
		})
	}
}

func TestNativeTargetGitMetadataFreshPlanCapacity(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			t.Setenv("GIT_DEFAULT_HASH", format)
			f := newNativeTargetCheckFixtureAtRoot(t, ".")
			var refs strings.Builder
			for i := range 79 {
				fmt.Fprintf(&refs, "create refs/tags/v1.%03d.0 %s\n", i, f.head)
			}
			if _, err := (ExecGitRunner{}).RunStdin(t.Context(), f.repo, []byte(refs.String()), "update-ref", "--stdin"); err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal([]byte(f.manifestJSON), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["git_metadata"] = nativeGitMetadataMode
			f.setManifestForTest(t, manifest)
			f.commitAndPrepareRepin(t, "capacity-recipe")
			writeRepoFilesForTest(t, f.repo, map[string]string{"value.txt": "7\n\n"})
			f.commitAndPrepare(t, "capacity-current-candidate")
			o := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
			r, err := f.s.VerifyWorktree(t.Context(), f.request("capacity-review-execute", &o))
			if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" {
				t.Fatalf("full-tag %s repair failed: %+v, %v", format, r, err)
			}
			plan, _, err := readNativeOracleExecutePlanTx(t.Context(), f.s.db, f.work, &r)
			if err != nil || plan.Target == nil || plan.Target.GitMetadata == nil {
				t.Fatalf("execute did not retain its exact Git metadata: %v", err)
			}
			if plan.Target.GitMetadata.Candidate.ObjectFormat != format || len(plan.Target.GitMetadata.Candidate.Tags) != 79 || len(workflowJSON(plan)) > 65536 {
				t.Fatalf("wrong %s full-tag repair capacity: %d bytes", format, len(workflowJSON(plan)))
			}
			if err := validateNativeOracleProducerTx(t.Context(), f.s.db, f.work, &r, "pass"); err != nil {
				t.Fatalf("full-tag repair receipt lost its preparation join: %v", err)
			}
			assertNativeTargetPublicWire(t, r)
			t.Logf("%s: 79 ancestral tags, %d-byte exact-subject plan", format, len(workflowJSON(plan)))
		})
	}
}
