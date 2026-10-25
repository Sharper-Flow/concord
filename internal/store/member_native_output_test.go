package store

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func memberNativeOutputFixture(t *testing.T) (workContextFixture, []SessionWorktreeOwner, []WorktreeEntry) {
	t.Helper()
	f, group, owners := boundMemberClaimFixture(t)
	repo, err := f.store.ProjectCanonicalPath(t.Context(), "project")
	if err != nil {
		t.Fatal(err)
	}
	git := newFakeWorktreeGit(repo)
	git.branches["main"] = group.BaseSubject.Commit
	main, err := activeWorktreeEntryForProject(t.Context(), f.store.db, "test", f.workID, "project")
	if err != nil {
		t.Fatal(err)
	}
	entries := []WorktreeEntry{main}
	for i, member := range group.Members {
		claim, err := f.store.ClaimWorktree(t.Context(), WorktreeClaimRequest{
			OpID: "output-claim-" + member.MemberID, WorkID: f.workID, ProjectID: member.ProjectID, BaseSHA: group.BaseSubject.Commit,
			PrincipalRef: f.owner.PrincipalRef, RequestID: "output-request-" + member.MemberID,
			SessionRef: owners[i].SessionRef, Owner: owners[i], ExpectedVersion: readWorkVersion(t, f.store, f.workID), Runner: git,
		})
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, claim.Entry)
	}
	return f, owners, entries
}

// Seed the retained execution boundary directly: these tests exercise SQL
// consumers, not native stage execution or dispatch admission. Lane ownership
// deliberately differs from the member who owns the claimed path.
func memberNativeOutputRun(t *testing.T, f workContextFixture, entry WorktreeEntry, lease string, exit int) WorktreeVerifyResult {
	t.Helper()
	plan := `{"protocol":"native_oracle_v2","worktree_identity":"` + workerWorktreeIdentity(entry.Path) + `"}`
	r := WorktreeVerifyResult{
		WorkID: f.workID, ProjectID: entry.ProjectID, Path: entry.Path, Branch: entry.Branch, LeaseID: lease,
		OperationRef: worktreeVerifyOperationRef(lease), SubjectRef: "commit:" + entry.BaseSHA, ExitCode: exit,
		Command: []string{"go", "test", "./synthetic"},
		Oracle: &NativeOracleResult{NativeOraclePreparation: NativeOraclePreparation{
			Protocol: "native_oracle_v2", Phase: "execute", Qualification: "pass", RunRef: worktreeVerifyOperationRef(lease),
			NativePlanSHA256: nativeDigest([]byte(plan)), BundleDigest: "bundle", LogicalCwd: ".", BuildEnvironmentDigest: "environment",
		}},
	}
	if exit != 0 {
		r.Oracle.Qualification = "fail"
	}
	stdout, stderr := []byte("stdout:"+lease), []byte("stderr:"+lease)
	raw, err := marshalNativeVerifyRecord(&r, &nativeStreamCapture{stdout: stdout, stderr: stderr, complete: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.db.Exec(`INSERT INTO worktree_verify_leases(lease_id,work_id,project_id,path,state,client_ref,agent_ref,session_ref,principal_ref,command_json,acquired_at,released_at,outcome,exit_code,result_json,native_plan_json,native_plan_sha256,stdout_blob,stderr_blob)
		VALUES(?,?,?,?,'released','client/lane','agent/lane','session/lane','principal/lane',?,'2026-10-25T00:00:00Z','2026-10-25T00:00:01Z','completed',?,?,?,?,?,?)`,
		lease, f.workID, entry.ProjectID, entry.Path, workflowJSON(r.Command), exit, string(raw), plan, r.Oracle.NativePlanSHA256, stdout, stderr)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMemberNativeOutputRetainedClaimScope(t *testing.T) {
	f, owners, entries := memberNativeOutputFixture(t)
	runs := []WorktreeVerifyResult{
		memberNativeOutputRun(t, f, entries[0], "output-integration", 0),
		memberNativeOutputRun(t, f, entries[1], "output-member-a", 0),
		memberNativeOutputRun(t, f, entries[2], "output-member-b", 0),
	}
	check := func(t *testing.T, ctx context.Context, run WorktreeVerifyResult, allowed bool) {
		t.Helper()
		for _, stream := range []string{"stdout", "stderr"} {
			page, err := f.store.InspectWorktree(ctx, WorktreeInspectRequest{WorkID: f.workID, ProjectID: "project", Mode: "oracle_output", RunRef: run.OperationRef, Stream: stream, Length: 16384})
			if !allowed {
				if !hasFailureKind(err, KindProjectionNotFound) || page.OracleOutput != nil {
					t.Errorf("foreign %s output leaked for %s: %+v, %v", stream, run.LeaseID, page, err)
				}
				continue
			}
			if err != nil || page.OracleOutput == nil {
				t.Fatalf("own/ordinary retained output unavailable for %s: %+v, %v", run.LeaseID, page, err)
			}
			data, err := base64.StdEncoding.DecodeString(page.OracleOutput.DataBase64)
			if err != nil || string(data) != stream+":"+run.LeaseID || !page.OracleOutput.EOF || !page.OracleOutput.Complete {
				t.Fatalf("retained output changed: %+v, %q, %v", page, data, err)
			}
		}
	}
	for _, phase := range []string{"active", "reclaimed", "collected-binding"} {
		t.Run(phase, func(t *testing.T) {
			if phase == "reclaimed" {
				if _, err := f.store.db.Exec(`UPDATE worktree_entries SET state='reclaimed' WHERE set_id=?; UPDATE worktree_claims SET state='reclaimed' WHERE work_id=?`, WorktreeSetID(f.workID), f.workID); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "collected-binding" {
				if _, err := f.store.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM workflow_group_members WHERE work_id=?; DELETE FROM write_guard`, f.workID); err != nil {
					t.Fatal(err)
				}
			}
			for i, owner := range owners {
				ctx := WithSessionWorktreeOwner(t.Context(), owner)
				for j, run := range runs {
					check(t, ctx, run, j == i+1)
				}
				for _, wrong := range []WorktreeInspectRequest{
					{WorkID: "foreign-work", ProjectID: "project"}, {WorkID: f.workID, ProjectID: "foreign-project"},
				} {
					wrong.Mode, wrong.RunRef, wrong.Stream, wrong.Length = "oracle_output", runs[i+1].OperationRef, "stdout", 100
					if _, err := f.store.InspectWorktree(ctx, wrong); !hasFailureKind(err, KindUnauthorized) {
						t.Errorf("retained member crossed work/Project scope: %v", err)
					}
				}
				wrongOwner := owner
				wrongOwner.AgentRef = "agent/replaced"
				if _, err := f.store.InspectWorktree(WithSessionWorktreeOwner(t.Context(), wrongOwner), WorktreeInspectRequest{WorkID: f.workID, ProjectID: "project", Mode: "oracle_output", RunRef: runs[0].OperationRef, Stream: "stdout", Length: 100}); !hasFailureKind(err, KindUnauthorized) {
					t.Errorf("altered member tuple escaped retained narrowing: %v", err)
				}
			}
			main := SessionWorktreeOwner{ClientRef: f.owner.ClientRef, AgentRef: f.owner.AgentRef, SessionRef: f.owner.SessionRef}
			for _, run := range runs {
				check(t, t.Context(), run, true)
				check(t, WithSessionWorktreeOwner(t.Context(), main), run, true)
			}
		})
	}
}

func TestMemberOracleProducerFreshnessIsolatedByClaim(t *testing.T) {
	f, _, entries := memberNativeOutputFixture(t)
	first := memberNativeOutputRun(t, f, entries[1], "fresh-member-a", 0)
	control := OracleControl{Argv: first.Command, Cwd: ".", RecipeSource: WorkContextReadingSource{ProjectID: "project"}}
	subject := strings.TrimPrefix(first.SubjectRef, "commit:")
	check := func(producer WorktreeVerifyResult, want bool) {
		t.Helper()
		fresh, err := oracleControlProducerFreshTx(t.Context(), f.store.db, f.workID, subject, control, &producer)
		if err != nil || fresh != want {
			t.Errorf("producer %s fresh=%v, want %v: %v", producer.LeaseID, fresh, want, err)
		}
	}
	check(first, true)
	for _, entry := range []WorktreeEntry{entries[0], entries[2]} {
		for _, exit := range []int{0, 1} {
			lease := "foreign-" + entry.ClaimOpID + "-" + strings.Repeat("x", exit+1)
			memberNativeOutputRun(t, f, entry, lease, exit)
			check(first, true)
		}
	}
	// The retained lease, not a caller-supplied Path, owns the comparison.
	altered := first
	altered.Path = entries[2].Path
	check(altered, true)
	red := memberNativeOutputRun(t, f, entries[1], "fresh-member-a-failure", 1)
	check(first, false)
	check(red, true)
	green := memberNativeOutputRun(t, f, entries[1], "fresh-member-a-recovery", 0)
	check(first, false)
	check(red, false)
	check(green, true)
	if _, err := f.store.db.Exec(`DELETE FROM worktree_verify_leases WHERE lease_id=?`, green.LeaseID); err != nil {
		t.Fatal(err)
	}
	check(green, false)
}
