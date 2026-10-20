package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func workflowProvingGoReceipt(check FootprintCheck) *WorktreeVerifyResult {
	command, _ := workflowFootprintCheckCommand(check)
	subject := "commit:" + strings.Repeat("a", 40)
	observed := WorktreeGoSuitePackage{Package: "example.test/m/internal/store", Passed: true, PassedCount: 1}
	if check.Kind == "go_test" {
		observed.ObservedTestNames = []string{"TestBody"}
	}
	return &WorktreeVerifyResult{Command: command, SubjectRef: subject, GoSuite: &WorktreeGoSuiteResult{
		Protocol: "native_go_suite_v1", Command: command, Complete: true,
		Closure:  &WorktreeGoSourceClosure{SubjectRef: subject, ModulePath: "example.test/m", Complete: true},
		Packages: []WorktreeGoSuitePackage{observed},
	}}
}

func TestWorkflowReceiptProvesCheckRequiresBoundCompleteObservation(t *testing.T) {
	named := FootprintCheck{Kind: "go_test", Value: "internal/store.TestBody"}
	pkg := FootprintCheck{Kind: "go_package", Value: "./internal/store"}
	cases := []struct {
		name   string
		check  FootprintCheck
		mutate func(*WorktreeVerifyResult)
		want   bool
	}{
		{"named test observed passing", named, func(*WorktreeVerifyResult) {}, true},
		{"receipt ran another command", named, func(r *WorktreeVerifyResult) { r.Command = []string{"go", "test", "./internal/store"} }, false},
		{"exit status without observation", named, func(r *WorktreeVerifyResult) { r.GoSuite = nil }, false},
		{"incomplete observation", named, func(r *WorktreeVerifyResult) { r.GoSuite.Complete = false }, false},
		{"unknown protocol", named, func(r *WorktreeVerifyResult) { r.GoSuite.Protocol = "other" }, false},
		{"observation of another command", named, func(r *WorktreeVerifyResult) { r.GoSuite.Command = []string{"go", "test", "./..."} }, false},
		{"no source closure", named, func(r *WorktreeVerifyResult) { r.GoSuite.Closure = nil }, false},
		{"unproved source closure", named, func(r *WorktreeVerifyResult) { r.GoSuite.Closure.Complete = false }, false},
		{"closure of another commit", named, func(r *WorktreeVerifyResult) {
			r.GoSuite.Closure.SubjectRef = "commit:" + strings.Repeat("b", 40)
		}, false},
		{"second package", named, func(r *WorktreeVerifyResult) {
			r.GoSuite.Packages = append(r.GoSuite.Packages, WorktreeGoSuitePackage{Package: "example.test/m/internal/agent", Passed: true, PassedCount: 1})
		}, false},
		{"lookalike package", named, func(r *WorktreeVerifyResult) { r.GoSuite.Packages[0].Package = "example.test/m/internal/storex" }, false},
		{"package of another module", named, func(r *WorktreeVerifyResult) { r.GoSuite.Packages[0].Package = "example.test/other/internal/store" }, false},
		{"package did not pass", named, func(r *WorktreeVerifyResult) { r.GoSuite.Packages[0].Passed = false }, false},
		{"named test skipped or absent", named, func(r *WorktreeVerifyResult) {
			r.GoSuite.Packages[0].ObservedTestNames, r.GoSuite.Packages[0].SkippedCount = nil, 1
		}, false},
		{"package passing with tests", pkg, func(*WorktreeVerifyResult) {}, true},
		{"package with no test files still built", pkg, func(r *WorktreeVerifyResult) {
			r.GoSuite.Packages[0].Passed, r.GoSuite.Packages[0].PassedCount = false, 0
		}, true},
		{"package whose every test skipped", pkg, func(r *WorktreeVerifyResult) {
			r.GoSuite.Packages[0].PassedCount, r.GoSuite.Packages[0].SkippedCount = 0, 2
		}, false},
		{"package skip that records skipped tests", pkg, func(r *WorktreeVerifyResult) {
			r.GoSuite.Packages[0].Passed, r.GoSuite.Packages[0].PassedCount, r.GoSuite.Packages[0].SkippedCount = false, 0, 1
		}, false},
		{"package of another path", pkg, func(r *WorktreeVerifyResult) { r.GoSuite.Packages[0].Package = "example.test/m/internal" }, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := workflowProvingGoReceipt(testCase.check)
			testCase.mutate(result)
			command, _ := workflowFootprintCheckCommand(testCase.check)
			if got := workflowReceiptProvesCheck(testCase.check, command, result); got != testCase.want {
				t.Fatalf("proves=%v, want %v: %+v", got, testCase.want, result.GoSuite)
			}
		})
	}
	for _, check := range []FootprintCheck{{Kind: "validator", Value: "scripts/check-alpha.py"}, {Kind: "generated", Value: "internal/agent/generated_contracts.go#Surface"}} {
		command, _ := workflowFootprintCheckCommand(check)
		if !workflowReceiptProvesCheck(check, command, &WorktreeVerifyResult{Command: command}) {
			t.Fatalf("a green exact %s receipt did not prove its check", check.Kind)
		}
	}
	validator := FootprintCheck{Kind: "validator", Value: "scripts/check-alpha.py"}
	command, _ := workflowFootprintCheckCommand(validator)
	if workflowReceiptProvesCheck(validator, command, &WorktreeVerifyResult{Command: []string{"python3", "-u", "scripts/check-alpha.py"}}) {
		t.Fatal("a differently spelled validator run proved the check")
	}
}

const workflowFootprintReceiptWork = "footprint-receipts"

func workflowFootprintVerify(t *testing.T, s *Store, git *fakeWorktreeGit, run string, command []string, exitCode int) WorktreeVerifyResult {
	t.Helper()
	workID := workflowFootprintReceiptWork
	req := verifyEvidenceRun(git, workID, "sha256:"+strings.Repeat(run, 64)+":worktree-verify:"+workID, exitCode)
	req.Command, req.RequestID = command, verifyRequestID(workID)+"-"+run
	if command[0] == "go" {
		req.RunCommand = nil
	}
	result, err := s.VerifyWorktree(context.Background(), req)
	if err != nil || result.ExitCode != exitCode {
		t.Fatalf("verify %q=%+v, %v", command, result, err)
	}
	return result
}

// Bind the actual native producer through the ordinary bind_evidence action;
// the accepting actor need not be the producer principal.
func bindWorkflowFootprintVerify(t *testing.T, s *Store, workID, operationRef string) int64 {
	t.Helper()
	var principal, watermark string
	if err := s.DatabaseForTesting().QueryRow(`SELECT principal_ref,request_id FROM durable_operations WHERE work_id=? AND op_id=?`, workID, operationRef).Scan(&principal, &watermark); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{
		"evidence_kind": "verification", "evidence_ref": operationRef, "immutable_subject_ref": operationRef,
		"producer_id": principal, "producer_run_ref": operationRef, "producer_watermark": watermark,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runVerdictAction(t, s, workID, "bind_evidence", payload, 0); err != nil {
		t.Fatalf("binding actual footprint producer: %v", err)
	}
	var seq int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT max(seq) FROM domain_events WHERE subject_id=? AND kind=? AND json_extract(payload,'$.producer_run_ref')=?`, workID, WorkflowEvidenceBound, operationRef).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestWorkflowFootprintProofsBindExactCommandProjectCommitAndFrontier(t *testing.T) {
	ctx := context.Background()
	const workID = workflowFootprintReceiptWork
	s, git, _, _ := verifyEvidenceFixture(t, workID)
	defer s.Close()
	alpha := FootprintCheck{Kind: "validator", Value: "scripts/check-alpha.py"}
	beta := FootprintCheck{Kind: "validator", Value: "scripts/check-beta.py"}
	gamma := FootprintCheck{Kind: "validator", Value: "scripts/check-gamma.py"}
	named := FootprintCheck{Kind: "go_test", Value: "internal/store.TestBody"}
	scenario := FootprintCheck{Kind: "scenario", Value: "cmd/scenario/run"}
	recordWorktreeGoSuiteFootprint(t, s, workID, []FootprintCheck{alpha, beta, gamma, named, scenario})
	worktreeGoSuiteEnvironmentFixture(t)

	green := workflowFootprintVerify(t, s, git, "b", []string{"python3", "scripts/check-alpha.py"}, 0)
	workflowFootprintVerify(t, s, git, "c", []string{"python3", "scripts/check-beta.py"}, 1)
	workflowFootprintVerify(t, s, git, "d", []string{"python3", "-u", "scripts/check-gamma.py"}, 0)
	goCommand, _ := workflowFootprintCheckCommand(named)
	workflowFootprintVerify(t, s, git, "e", goCommand, 0)
	commit, ok := strings.CutPrefix(green.SubjectRef, "commit:")
	if !ok {
		t.Fatalf("green fixture run recorded no commit subject: %+v", green)
	}
	var watermark int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT event_watermark FROM worktree_verify_leases WHERE lease_id=?`, green.LeaseID).Scan(&watermark); err != nil || watermark == 0 {
		t.Fatalf("green fixture lease watermark=%d, %v", watermark, err)
	}

	checks := []FootprintCheck{alpha, beta, gamma, named, scenario}
	read := func(basis workflowFootprintProofBasis) []workflowFootprintProof {
		t.Helper()
		proofs, err := readWorkflowFootprintProofs(ctx, s.DatabaseForTesting(), basis, checks)
		if err != nil || len(proofs) != len(checks) {
			t.Fatalf("proofs=%+v, %v", proofs, err)
		}
		return proofs
	}
	basis := workflowFootprintProofBasis{WorkID: workID, ProjectID: "project-w", Commit: commit, Frontier: watermark}
	proofs := read(basis)
	if proofs[0].OperationRef != "" {
		t.Fatalf("an unbound green run proved the footprint check: %+v", proofs[0])
	}
	boundAt := bindWorkflowFootprintVerify(t, s, workID, green.OperationRef)
	proofs = read(basis)
	if proofs[0].OperationRef != green.OperationRef || proofs[0].PrincipalRef != "principal-1" || !proofs[0].Supported {
		t.Fatalf("exact green validator receipt did not prove its check: %+v", proofs[0])
	}
	for index, reason := range map[int]string{1: "failed run", 2: "differently spelled run", 3: "Go run without a proved source closure"} {
		if proofs[index].OperationRef != "" || !proofs[index].Supported {
			t.Fatalf("%s proved %+v", reason, proofs[index])
		}
	}
	if proofs[4].Supported || proofs[4].OperationRef != "" {
		t.Fatalf("scenario check without a receipt consumer was supported: %+v", proofs[4])
	}
	for boundary, want := range map[int64]string{boundAt: "", boundAt + 1: green.OperationRef} {
		prefix := basis
		prefix.BeforeSeq = boundary
		if proof := read(prefix)[0]; proof.OperationRef != want {
			t.Fatalf("binding at seq %d was read before %d: %+v; want %q", boundAt, boundary, proof, want)
		}
	}
	var repairFrontier int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT max(seq) FROM domain_events WHERE subject_id=?`, workID).Scan(&repairFrontier); err != nil {
		t.Fatal(err)
	}
	lateBinding := bindWorkflowFootprintVerify(t, s, workID, green.OperationRef)
	if lateBinding <= repairFrontier {
		t.Fatal("the late binding fixture did not follow its frontier")
	}
	late := basis
	late.Frontier = repairFrontier
	if proof := read(late)[0]; proof.OperationRef != "" {
		t.Fatalf("late binding rehabilitated a pre-frontier run: %+v", proof)
	}

	for name, other := range map[string]workflowFootprintProofBasis{
		"run before the frontier": {WorkID: workID, ProjectID: "project-w", Commit: commit, Frontier: watermark + 1},
		"another Project":         {WorkID: workID, ProjectID: "project-other", Commit: commit, Frontier: watermark},
		"another commit":          {WorkID: workID, ProjectID: "project-w", Commit: strings.Repeat("c", 40), Frontier: watermark},
	} {
		if proof := read(other)[0]; proof.OperationRef != "" {
			t.Fatalf("%s proved %+v", name, proof)
		}
	}
	for name, incomplete := range map[string]workflowFootprintProofBasis{
		"no commit":       {WorkID: workID, ProjectID: "project-w", Frontier: watermark},
		"no frontier":     {WorkID: workID, ProjectID: "project-w", Commit: commit},
		"negative prefix": {WorkID: workID, ProjectID: "project-w", Commit: commit, Frontier: watermark, BeforeSeq: -1},
	} {
		if _, err := readWorkflowFootprintProofs(ctx, s.DatabaseForTesting(), incomplete, checks); err == nil {
			t.Fatalf("a basis with %s resolved proofs", name)
		}
	}
	if _, err := s.DatabaseForTesting().Exec(`UPDATE worktree_verify_leases SET event_watermark=NULL WHERE lease_id=?`, green.LeaseID); err != nil {
		t.Fatal(err)
	}
	if proof := read(basis)[0]; proof.OperationRef != "" {
		t.Fatalf("a lease without a watermark proved %+v", proof)
	}
}

func TestWorkflowFootprintProofsConsumeRealGoVerifyReceipts(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	const workID = "footprint-real-go"
	s, git, _, _ := verifyEvidenceFixture(t, workID)
	defer s.Close()
	checks := []FootprintCheck{
		{Kind: "go_test", Value: "pkg.TestBody"},
		{Kind: "go_package", Value: "./pkg"},
		{Kind: "go_package", Value: "./compileonly"},
	}
	recordWorktreeGoSuiteFootprint(t, s, workID, checks)
	entry, err := activeWorktreeEntryForProject(ctx, s.DatabaseForTesting(), "fixture", workID, "project-w")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture supplies a synthetic claim. Its on-disk subject and every
	// verify probe, source-closure read, and Go test execution below are real.
	for name, content := range map[string]string{
		"go.mod":           "module example.test/receipt\n\ngo 1.26.0\n",
		"pkg/body_test.go": "package pkg\nimport \"testing\"\nfunc TestBody(t *testing.T) { t.Log(\"body executed\") }\n",
		"compileonly/p.go": "package compileonly\nfunc Value() int { return 7 }\n",
	} {
		path := filepath.Join(entry.Path, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runTestGit(t, entry.Path, "init", "-q", "-b", entry.Branch)
	runTestGit(t, entry.Path, "add", "-A")
	runTestGit(t, entry.Path, "commit", "-q", "-m", "Go receipt fixture")
	commit := runTestGit(t, entry.Path, "rev-parse", "HEAD")
	var frontier int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT MAX(seq) FROM domain_events WHERE subject_type='work_item' AND subject_id=?`, workID).Scan(&frontier); err != nil || frontier <= 0 {
		t.Fatalf("fixture frontier=%d, %v", frontier, err)
	}
	basis := workflowFootprintProofBasis{WorkID: workID, ProjectID: "project-w", Commit: commit, Frontier: frontier}
	read := func(t *testing.T) []workflowFootprintProof {
		t.Helper()
		proofs, err := readWorkflowFootprintProofs(ctx, s.DatabaseForTesting(), basis, checks)
		if err != nil || len(proofs) != len(checks) {
			t.Fatalf("proofs=%+v, %v", proofs, err)
		}
		return proofs
	}
	for _, proof := range read(t) {
		if !proof.Supported || proof.OperationRef != "" {
			t.Fatalf("check had proof before its execution: %+v", proof)
		}
	}
	operations := make([]string, 0, len(checks))
	for index, check := range checks {
		t.Run(check.Kind+":"+check.Value, func(t *testing.T) {
			command, ok := workflowFootprintCheckCommand(check)
			if !ok {
				t.Fatalf("check did not derive a command: %+v", check)
			}
			leaseID := "sha256:" + strings.Repeat(strconv.Itoa(index), 64) + ":worktree-verify:" + workID
			req := verifyEvidenceRun(git, workID, leaseID, 0)
			req.Command, req.Runner, req.RunCommand = command, ExecGitRunner{}, nil
			req.RequestID += "-" + strconv.Itoa(index)
			result, err := s.VerifyWorktree(ctx, req)
			if err != nil || result.ExitCode != 0 || result.SubjectRef != "commit:"+commit {
				t.Fatalf("real Go verify=%+v, %v", result, err)
			}
			bindWorkflowFootprintVerify(t, s, workID, result.OperationRef)
			proofs := read(t)
			if proof := proofs[index]; proof.OperationRef != result.OperationRef || proof.PrincipalRef != req.PrincipalRef {
				t.Fatalf("real Go receipt did not prove its check: %+v; verify=%+v", proof, result)
			}
			operations = append(operations, result.OperationRef)
			for earlier, proof := range proofs[:index] {
				if proof.OperationRef != operations[earlier] {
					t.Fatalf("a later run displaced an earlier check's own proof: %+v, want %s", proof, operations[earlier])
				}
			}
			for _, unrun := range proofs[index+1:] {
				if unrun.OperationRef != "" {
					t.Fatalf("another command's run proved an unexecuted check: %+v", unrun)
				}
			}
		})
	}
}
