package store

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDocumentationRecipeRecordsReady(t *testing.T) {
	ctx := context.Background()
	const recipePath = ".concord/oracles/knowledge/documentation.v1.json"
	repo, head := storeOracleClone(t)
	manifestFile, raw, err := nativeGitFile(ctx, ExecGitRunner{}, repo, head, recipePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := decodeNativeTargetManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	wantHarness := []string{
		"scripts/check-knowledge-index.py",
		"scripts/generate-knowledge-index.py",
		"scripts/knowledge_authority_defaults.py",
		"scripts/knowledge_index.py",
		"scripts/shard_format.py",
	}
	if manifest.Materializer != "none" || manifest.Runner != "check" || manifest.Root != "." || !slices.Equal(manifest.HarnessFiles, wantHarness) || len(manifest.SupportFiles) != 0 {
		t.Fatalf("documentation recipe must pin only its validator closure, not candidate documents: %+v", manifest)
	}
	gitRunStore(t, repo, "branch", "-m", "target-branch")
	const work = "documentation-recipe"
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeTargetFixture{
		s: seed.store, work: work, repo: repo, head: head,
		root: ".", recipePath: recipePath,
		controlArgv: []string{"check", "knowledge-index"},
	}
	f.seedTargetAuthority(t)
	f.bundle.Owner.Mechanism = OracleMechanism{ProjectID: "project", Path: "scripts/check-knowledge-index.py", EntryPoint: "validate"}
	f.bundle.Owner.Obligation = "registered knowledge documents retain matching hashes and valid registry metadata"
	f.bundle.Control.CaseIDs = []string{"case:documentation-registry"}
	f.bundle.Cases[0].CaseID = f.bundle.Control.CaseIDs[0]
	f.bundle.Cases[0].EntryPoint = "validate"
	f.bundle.Cases[0].InputClass = "candidate documentation and authored knowledge registry"
	f.bundle.Cases[0].ExpectedState = "registry metadata validates and document hashes match"
	if err := validateNativeTargetManifest(manifest, f.bundle.Control); err != nil {
		t.Fatal(err)
	}
	wantPython := "Python " + manifest.Pin.Python
	if out, err := exec.Command("python3", "--version").Output(); err != nil || strings.TrimSpace(string(out)) != wantPython {
		t.Skipf("pinned documentation runtime unavailable: requires %s, got %q (%v)", wantPython, out, err)
	}
	f.prepareWithCurrentBundle(t, "documentation")
	p := f.prepare.Oracle
	if f.launches != 0 || p.ManifestBlob != manifestFile.BlobOID || p.ManifestDigest != manifestFile.SHA256 || p.RecipeSource != f.bundle.Control.RecipeSource || p.SubjectCommit != head || !p.StreamsComplete {
		t.Fatalf("preparation lost its pinned recipe or ran acceptance: %+v launches=%d", p, f.launches)
	}
	baseline := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err := f.s.VerifyWorktree(ctx, f.request("documentation-baseline", &baseline))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 || f.launches != 1 {
		t.Fatalf("execute documentation check: oracle=%+v err=%v launches=%d output=%s", r.Oracle, err, f.launches, r.Output)
	}
	assertNativeTargetReceipt(t, r, "none", "check", ".")
	t.Logf("recipe_commit=%s recipe_blob=%s native_preparation=%s qualification=%s native_execution=%s", head, manifestFile.BlobOID, f.prepare.OperationRef, p.Qualification, r.OperationRef)

	const documentPath = ".concord/docs/goals.md"
	document, err := os.ReadFile(filepath.Join(repo, documentPath))
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFilesForTest(t, repo, map[string]string{documentPath: string(document) + "\nSynthetic fixture reference.\n"})
	f.commitAndPrepare(t, "documentation-drift")
	drift := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err = f.s.VerifyWorktree(ctx, f.request("documentation-drift", &drift))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "fail" || r.ExitCode == 0 || f.launches != 2 {
		t.Fatalf("candidate document drift escaped the pinned validator: oracle=%+v err=%v launches=%d output=%s", r.Oracle, err, f.launches, r.Output)
	}

	update := exec.Command("python3", "scripts/check-knowledge-index.py", "--update")
	update.Dir = repo
	if out, err := update.CombinedOutput(); err != nil {
		t.Fatalf("regenerate candidate document hash: %s: %v", out, err)
	}
	f.commitAndPrepare(t, "documentation-rehashed")
	rehashed := NativeOracleRequest{Phase: "execute", PreparationRunRef: f.prepare.OperationRef}
	r, err = f.s.VerifyWorktree(ctx, f.request("documentation-rehashed", &rehashed))
	if err != nil || r.Oracle == nil || r.Oracle.Qualification != "pass" || r.ExitCode != 0 || f.launches != 3 {
		t.Fatalf("updated candidate hash did not pass: oracle=%+v err=%v launches=%d output=%s", r.Oracle, err, f.launches, r.Output)
	}
	assertNativeTargetReceipt(t, r, "none", "check", ".")
	if r.Oracle.RecipeSource.CommitOID != head || r.Oracle.SubjectCommit != f.head || f.head == head {
		t.Fatalf("candidate changes moved the harness pin or lost their subject: %+v", r.Oracle)
	}
}
