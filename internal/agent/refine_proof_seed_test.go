package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// agentSeedRefineProofRun seeds one green worktree_verify run acquired just
// after the work item's latest refine start and returns the operation ref the
// CD-0192 refine exit binds. The verify lease and its authority are
// operational tables the store's own verify route owns; the seed mirrors the
// rows a green release writes.
func agentSeedRefineProofRun(t *testing.T, s *store.Store, workID, digest string) string {
	t.Helper()
	var occurred string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT occurred_at FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? AND json_extract(payload,'$.step_id')='refine' ORDER BY seq DESC LIMIT 1`, workID, store.WorkflowActionStarted).Scan(&occurred); err != nil {
		t.Fatalf("read the refine start: %v", err)
	}
	startedAt, err := time.Parse(time.RFC3339Nano, occurred)
	if err != nil {
		t.Fatalf("parse the refine start: %v", err)
	}
	leaseID := digest + ":worktree-verify:" + workID
	opRef := "worktree_verify:" + digest
	command := []string{"go", "vet", "./..."}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := json.Marshal(store.WorktreeVerifyResult{WorkID: workID, ProjectID: "project-1", LeaseID: leaseID, OperationRef: opRef, Command: command, TrackedFilesChanged: false})
	if err != nil {
		t.Fatal(err)
	}
	acquired := startedAt.Add(time.Second).UTC().Format(time.RFC3339Nano)
	released := startedAt.Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,released_at,exit_code,outcome,result_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		leaseID, workID, "project-1", "/fixture/"+workID, "released", "client/fixture", "agent/fixture", "session/"+workID, "principal/fixture",
		string(commandJSON), acquired, released, 0, "completed", string(resultJSON)); err != nil {
		t.Fatalf("seed the verify lease: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations
		(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,
		 accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,
		 result_kind,result_payload,evidence_refs,changed_refs,completed_at)
		VALUES(?,1,?,'worktree.verify',1,'','external_effect','sha256:`+digest+`','{}','principal/fixture','request/verify','`+acquired+`','','completed',?,?,'[]',?)`,
		opRef, workID, string(resultJSON), `["`+opRef+`"]`, released); err != nil {
		t.Fatalf("seed the verify authority: %v", err)
	}
	return opRef
}

// agentSeedIntegrationVerifyRun seeds one green worktree_verify run of the
// work's primary Project acquired after the work's latest recorded event —
// the current phase start or required job acceptance, whichever is later —
// and returns the operation ref the CD-0205 delivery admission binds as the
// integration evidence.
func agentSeedIntegrationVerifyRun(t *testing.T, s *store.Store, workID, digest string) string {
	t.Helper()
	var latest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT occurred_at FROM domain_events WHERE subject_type='work_item' AND subject_id=? ORDER BY seq DESC LIMIT 1`, workID).Scan(&latest); err != nil {
		t.Fatalf("read the work's latest event: %v", err)
	}
	anchor, err := time.Parse(time.RFC3339Nano, latest)
	if err != nil {
		t.Fatalf("parse the work's latest event time: %v", err)
	}
	var project string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE((SELECT DISTINCT project_scope FROM worker_job_revisions WHERE work_id=? AND project_scope!='' LIMIT 1),(SELECT project_id FROM work_projects WHERE work_id=? AND role='primary' LIMIT 1),'project-1')`, workID, workID).Scan(&project); err != nil || project == "" {
		project = "project-1"
	}
	leaseID := digest + ":worktree-verify:" + workID
	opRef := "worktree_verify:" + digest
	command := []string{"go", "vet", "./..."}
	commandJSON, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, err := json.Marshal(store.WorktreeVerifyResult{WorkID: workID, ProjectID: project, LeaseID: leaseID, OperationRef: opRef, Command: command, TrackedFilesChanged: false})
	if err != nil {
		t.Fatal(err)
	}
	acquired := anchor.Add(time.Second).UTC().Format(time.RFC3339Nano)
	released := anchor.Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,released_at,exit_code,outcome,result_json)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		leaseID, workID, project, "/fixture/"+workID, "released", "client/fixture", "agent/fixture", "session/"+workID, "principal/fixture",
		string(commandJSON), acquired, released, 0, "completed", string(resultJSON)); err != nil {
		t.Fatalf("seed the integration verify lease: %v", err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations
		(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_version,step_id,step_kind,
		 accepted_inputs_digest,accepted_scope_snapshot,principal_ref,request_id,observed_at,contract_digest,
		 result_kind,result_payload,evidence_refs,changed_refs,completed_at)
		VALUES(?,1,?,'worktree.verify',1,'','external_effect','sha256:`+digest+`','{}','principal/fixture','request/verify','`+acquired+`','','completed',?,?,'[]',?)`,
		opRef, workID, string(resultJSON), `["`+opRef+`"]`, released); err != nil {
		t.Fatalf("seed the integration verify authority: %v", err)
	}
	return opRef
}

// fixtureRepoWithOrigin creates one real git repository whose origin HEAD is
// set, the shape the tooling resolution reads the default ref from.
func fixtureRepoWithOrigin(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "Fixture")
	run("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "fixture")
	run("remote", "add", "origin", "https://example.invalid/fixture.git")
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo
}
