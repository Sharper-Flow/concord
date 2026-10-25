package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// checksRepairNativeFixture keeps one executable oracle frozen across repairs
// of checks. Its passing implementation is committed before any authorization;
// an unchanged delivery must prove that outcome through the native producer.
type checksRepairNativeFixture struct {
	journey         candidateJourney
	oracle          AcceptanceOracle
	contractVersion int64
}

func newChecksRepairNativeFixture(t *testing.T, s *Store, workID string) *checksRepairNativeFixture {
	t.Helper()
	repo, _ := reviewSubjectClaim(t, s, workID)
	f := &checksRepairNativeFixture{journey: candidateJourney{s: s, workID: workID, repo: repo}}
	var branch string
	if err := s.db.QueryRow(`SELECT branch FROM worktree_entries WHERE set_id=? AND project_id='project' AND state='active'`, WorktreeSetID(workID)).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	gitRunStore(t, repo, "branch", "-m", branch)
	for name, data := range map[string]string{
		"go.mod":                       "module example.invalid/oracle\n\ngo 1.26.0\ntoolchain go1.26.7\n",
		"internal/store/value.go":      "package oracle\nfunc Value() int { return 7 }\n",
		"internal/store/value_test.go": candidateJourneyHarness,
		"internal/store/oracle.json":   `{"kind":"go_top_level_v1","package_cwd":"internal/store","test_files":["internal/store/value_test.go"],"fixture_files":[],"cases":{"case:fixture":["TestValue"]}}`,
	} {
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	head := f.journey.commit(t, "fixture@example.invalid", "pin the checks-repair oracle")
	f.journey.observeRun(t, "checks-repair-base")
	raw := mustJSONValue(acceptanceOracleFieldsWithoutPreparationsForTest(t, s, workID))
	if err := json.Unmarshal(raw, &f.oracle); err != nil {
		t.Fatal(err)
	}
	control := &f.oracle.Controls[0]
	control.RecipeSource = WorkContextReadingSource{Kind: WorkContextSourceRepositoryFile, ProjectID: "project", Path: "internal/store/oracle.json", CommitOID: head}
	control.Argv = []string{"go", "test", "-count=1", "-run", "^(TestValue)$", "."}
	control.RequiredEvidenceRole = OracleEvidenceRoleReported
	f.prepare(t)
	return f
}

func (f *checksRepairNativeFixture) prepare(t *testing.T) {
	t.Helper()
	j := &f.journey
	version, err := activeWorkflowContractVersion(t.Context(), j.s.db, j.workID, "checks-repair-fixture")
	if err != nil {
		t.Fatal(err)
	}
	control := f.oracle.Controls[0]
	control.ReadinessEvidenceRefs = nil
	j.bundle = NativeOracleControlBundle{Owner: f.oracle.Owners[0], Cases: f.oracle.Cases, Control: control}
	run, err := j.s.VerifyWorktree(t.Context(), j.request(fmt.Sprintf("checks-repair-prepare-%d", version), &NativeOracleRequest{
		Phase: "prepare", ExpectedContractVersion: version, ControlBundle: &j.bundle,
	}))
	if err != nil || run.Oracle == nil || run.Oracle.Qualification != "ready" {
		t.Fatalf("prepare checks-repair oracle: %+v, %v", run.Oracle, err)
	}
	f.oracle.Controls[0].ReadinessEvidenceRefs = []string{run.OperationRef}
	f.contractVersion = version
}

func (f *checksRepairNativeFixture) record(t *testing.T, actor WorkflowActor, fields map[string]any) error {
	t.Helper()
	j := &f.journey
	version, err := activeWorkflowContractVersion(t.Context(), j.s.db, j.workID, "checks-repair-fixture")
	if err != nil {
		return err
	}
	if version != f.contractVersion {
		f.prepare(t)
	}
	fields["acceptance_oracle"] = f.oracle
	return recordWorkerJobActionForTest(t, j.s, j.workID, actor, fields)
}

func (f *checksRepairNativeFixture) execute(t *testing.T, attemptID string) *WorkerOracleReceipt {
	t.Helper()
	j := &f.journey
	job, err := workflowDispatchedJobForAttempt(t.Context(), j.s.db, j.workID, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	control := checksRepairRevisionView(t, j.s, j.workID, *job).AcceptanceOracle.Controls[0]
	request := j.executeRequest(t, attemptID, *job, WorktreeVerifyResult{OperationRef: control.ReadinessEvidenceRefs[0]})
	run, err := j.s.VerifyWorktree(t.Context(), j.request("checks-repair-execute-"+attemptID, &request))
	if err != nil || run.Oracle == nil || run.Oracle.Qualification != "pass" || run.ExitCode != 0 {
		t.Fatalf("execute checks-repair oracle for %s: %+v, %v", attemptID, run.Oracle, err)
	}
	source := control.RecipeSource
	return &WorkerOracleReceipt{
		ControlIDs: []string{control.ControlID}, CaseIDs: control.CaseIDs,
		RecipeSource: WorkerOracleRecipeSource{Kind: source.Kind, ProjectID: source.ProjectID, Path: source.Path, CommitOID: source.CommitOID},
		Result:       OracleReceiptResultPass, ExitCode: &run.ExitCode, RunRef: run.OperationRef, EvidenceRefs: []string{run.OperationRef},
	}
}
