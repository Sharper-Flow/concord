package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// storeOracleClone checks this repository's HEAD out on branch store-oracle in
// a private shared clone, so a test can pin committed store recipes.
func storeOracleClone(t *testing.T) (repo, head string) {
	t.Helper()
	source, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	rawHead, err := (ExecGitRunner{}).Run(context.Background(), source, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	head = strings.TrimSpace(string(rawHead))
	repo = filepath.Join(t.TempDir(), "source")
	gitRunStore(t, source, "clone", "--shared", "--no-checkout", source, repo)
	gitRunStore(t, repo, "checkout", "-b", "store-oracle", head)
	return repo, head
}

func TestStoreOracleRecipeRecordsReady(t *testing.T) {
	started := time.Now()
	ctx := context.Background()
	const work = "store-oracle-recipe"
	const recipePath = ".concord/oracles/store/immediate-transactions.v1.json"
	repo, head := storeOracleClone(t)
	manifest, _, err := nativeGitFile(ctx, ExecGitRunner{}, repo, head, recipePath)
	if err != nil {
		t.Fatal(err)
	}
	fixture := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	s := fixture.store
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1);
INSERT INTO worktree_entries(set_id,project_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at,git_facts)
VALUES(?,'project','store-oracle-claim','store-oracle',?,?,?,'active','2026-10-09T00:00:00Z','{}');
DELETE FROM write_guard`, WorktreeSetID(work), head, repo, repo); err != nil {
		t.Fatal(err)
	}
	request := func(id string) WorktreeVerifyRequest {
		return WorktreeVerifyRequest{
			WorkID: work, ProjectID: "project", LeaseID: "store-oracle-" + id, RequestID: "store-oracle-" + id,
			PrincipalRef: fixture.owner.PrincipalRef,
			Owner:        SessionWorktreeOwner{ClientRef: fixture.owner.ClientRef, AgentRef: fixture.owner.AgentRef, SessionRef: fixture.owner.SessionRef},
			Runner:       ExecGitRunner{}, Now: time.Now().UTC(), SupportedBudgetSeconds: 300,
		}
	}
	subject := request("subject")
	subject.Command = []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}
	if result, err := s.VerifyWorktree(ctx, subject); err != nil || result.ExitCode != 0 {
		t.Fatalf("verify subject: %+v, %v", result, err)
	}
	owner := OracleOwner{
		OwnerID: "owner:store-open", DomainID: "root",
		Mechanism:    OracleMechanism{ProjectID: "project", Path: "internal/store/store.go", EntryPoint: "Open"},
		Obligation:   "explicit transactions acquire the immediate SQLite write lock",
		PredicateIDs: []string{"predicate:return-route"},
	}
	control := OracleControl{
		ControlID: "control:store-immediate-transaction", OwnerID: owner.OwnerID, PredicateIDs: owner.PredicateIDs,
		CaseIDs: []string{"case:store-immediate-transaction"}, Cwd: "internal/store",
		Argv:           []string{"go", "test", "-count=1", "-run", "^(TestOpenURIPathUsesImmediateTransactions)$", "."},
		RecipeSource:   WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: recipePath, CommitOID: head},
		ExpectedResult: "pass", RequiredEvidenceRole: "independently_executed",
	}
	oracleCase := OracleCase{
		CaseID: control.CaseIDs[0], OwnerID: owner.OwnerID, EntryPoint: "Open",
		InputClass:    "database path with URI punctuation and two concurrent explicit transactions",
		ExpectedState: "the second BEGIN refuses with write-lock contention", ControlIDs: []string{control.ControlID},
	}
	bundle := NativeOracleControlBundle{Owner: owner, Control: control, Cases: []OracleCase{oracleCase}}
	launches := 0
	s.nativeOracleProgramLaunched = func() { launches++ }
	prepare := request("prepare")
	prepare.Oracle = &NativeOracleRequest{Phase: "prepare", ExpectedContractVersion: 1, ControlBundle: &bundle}
	result, err := s.VerifyWorktree(ctx, prepare)
	if err != nil || result.Oracle == nil {
		t.Fatalf("native preparation: %+v, %v", result, err)
	}
	p := result.Oracle
	if result.ExitCode != 0 || p.Protocol != "native_oracle_v2" || p.Phase != "prepare" || p.Qualification != "ready" || p.RunRef != result.OperationRef || !p.StreamsComplete || !p.Stdout.Complete || !p.Stderr.Complete {
		t.Fatalf("preparation is not qualified and complete: %+v", p)
	}
	if p.SubjectCommit != head || p.WorkID != work || p.ProjectID != "project" || p.ContractVersion != 1 || p.RecipeSource != control.RecipeSource || p.ManifestBlob != manifest.BlobOID || p.ManifestDigest != manifest.SHA256 || p.BundleDigest != nativeBundleDigest(bundle) {
		t.Fatalf("preparation lost its immutable authority joins: %+v", p)
	}
	if launches != 0 || len(p.ObservedTestNames) != 0 || !slices.Equal(p.SelectedTestNames, []string{"TestOpenURIPathUsesImmediateTransactions"}) {
		t.Fatalf("prepare executed tests or lost its selector: launches=%d receipt=%+v", launches, p)
	}
	wantStages := []string{"dependencies", "metadata", "compile", "converter_compile"}
	if len(p.Stages) != len(wantStages) {
		t.Fatalf("preparation stages: %+v", p.Stages)
	}
	for i, stage := range p.Stages {
		if stage.Name != wantStages[i] || stage.ExitCode != 0 {
			t.Fatalf("preparation stage %d: %+v", i, stage)
		}
	}
	// Caller mutation cannot replace the bundle retained by preparation.
	bundle.Cases[0].ExpectedState = "a different case expectation"
	var planRaw, digest string
	if err := s.db.QueryRow(`SELECT native_plan_json,native_plan_sha256 FROM worktree_verify_leases WHERE lease_id=?`, result.LeaseID).Scan(&planRaw, &digest); err != nil {
		t.Fatal(err)
	}
	var pinned nativeOraclePlan
	if err := json.Unmarshal([]byte(planRaw), &pinned); err != nil {
		t.Fatal(err)
	}
	if nativeDigest([]byte(planRaw)) != digest || pinned.Bundle.Cases[0].ExpectedState != oracleCase.ExpectedState || pinned.BundleDigest != p.BundleDigest {
		t.Fatal("preparation did not retain its immutable declared bundle")
	}
	execute := request("execute")
	execute.Oracle = &NativeOracleRequest{Phase: "execute", PreparationRunRef: result.OperationRef}
	run, err := s.VerifyWorktree(ctx, execute)
	if err != nil || run.Oracle == nil || run.Oracle.Qualification != "pass" || launches != 1 || run.Oracle.PreparationRunRef != result.OperationRef {
		t.Fatalf("execute exact preparation: %+v %v launches=%d", run, err, launches)
	}
	if err := validateNativeOracleProducerTx(ctx, s.db, work, &run, "pass"); err != nil {
		t.Fatalf("native producer receipt: %v", err)
	}
	t.Logf("recipe_commit=%s recipe_blob=%s native_run_ref=%s qualification=%s integration_wall_time=%s", head, manifest.BlobOID, run.OperationRef, run.Oracle.Qualification, time.Since(started))
}

// A selected native control compiles only its pinned test files and runs
// without the package TestMain. The template-based control must still reach a
// migrated database template, and every database file it creates must stay
// outside the controlled snapshot, which the producer fingerprints again after
// the run.
func TestStoreTemplateOracleRecipeExecutes(t *testing.T) {
	ctx := context.Background()
	const work = "store-template-oracle"
	const recipePath = ".concord/oracles/store/template-fixture.v1.json"
	const selected = "TestOpenTempCopiesIsolatedLatestSchema"
	repo, head := storeOracleClone(t)
	seed := seedWorkflowReturnRouteFixture(t, work, "workflow.break_fix", "repair")
	f := &nativeOracleFixture{s: seed.store, work: work, repo: repo, head: head}
	f.qualify(t, "store-oracle", "internal/store", recipePath, "case:store-template-fixture", selected)
	result, err := f.s.VerifyWorktree(ctx, f.request("execute", &f.a))
	if err != nil || result.Oracle == nil || result.Oracle.Qualification != "pass" || result.ExitCode != 0 {
		t.Fatalf("execute the template-based control: %+v, %v", result, err)
	}
	p := result.Oracle
	if f.launches != 1 || !slices.Equal(p.SelectedTestNames, []string{selected}) || !slices.Contains(p.ObservedTestNames, selected) {
		t.Fatalf("execution did not run exactly the selected control: launches=%d receipt=%+v", f.launches, p)
	}
}
