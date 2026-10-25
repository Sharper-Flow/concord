package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

type memberResumeFixture struct {
	s                           *store.Store
	input                       workResumeInput
	repo, integration, memberID string
}

func newMemberResumeFixture(t *testing.T, claim bool) memberResumeFixture {
	t.Helper()
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	boot, err := s.BootstrapWorktree(t.Context(), bootstrapRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = s.RegisterTrustedClient(t.Context(), store.TrustedClientRecord{
		ClientRef: "client-resume", PrincipalRef: "principal-resume", Status: "active",
		CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `["product-wl"]`, ProjectScopeJSON: `["project-wl"]`, AgentScopeJSON: `["agent-member","agent-other"]`,
	}, store.TrustedClientKeyRecord{ClientRef: "client-resume", KeyID: "key-resume", PublicKey: bytes.Repeat([]byte{1}, 32), Status: "active"}, "2026-10-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	err = s.RegisterTrustedClient(t.Context(), store.TrustedClientRecord{
		ClientRef: "client-foreign", PrincipalRef: "principal-foreign", Status: "active",
		CapabilitiesJSON: `["product_read"]`, ProductScopeJSON: `["product-wl"]`, ProjectScopeJSON: `["project-wl"]`, AgentScopeJSON: `["agent-member"]`,
	}, store.TrustedClientKeyRecord{ClientRef: "client-foreign", KeyID: "key-foreign", PublicKey: bytes.Repeat([]byte{2}, 32), Status: "active"}, "2026-10-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	owner := store.SessionWorktreeOwner{ClientRef: "client-resume", AgentRef: "agent-other", SessionRef: "session-main"}
	verified, err := s.VerifyWorktree(t.Context(), store.WorktreeVerifyRequest{WorkID: boot.WorkID, ProjectID: "project-wl", Owner: owner, Command: []string{"true"}, LeaseID: "resume-base", PrincipalRef: "principal-resume", RequestID: "resume-base"})
	if err != nil {
		t.Fatal(err)
	}
	actor := store.WorkflowActor{PrincipalRef: "principal-resume", ClientRef: "client-resume", AgentRef: "agent-member", SessionRef: "session-member", ActorClass: store.ActorAgent}
	actorRef, err := store.WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	mainRef, err := store.WorkflowActorRef(store.WorkflowActor{PrincipalRef: "principal-resume", ClientRef: owner.ClientRef, AgentRef: owner.AgentRef, SessionRef: owner.SessionRef, ActorClass: store.ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	db := s.DatabaseForTesting()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	// Seed an existing assignment and claim, not a new workflow. Native
	// verification is real; resume must neither create nor confirm a claim.
	exec(`INSERT INTO write_guard(active) VALUES(1)`)
	exec(`INSERT OR IGNORE INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal-resume','client-resume','agent-other','session-main','agent','now')`, mainRef)
	exec(`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal-resume','client-resume','agent-member','session-member','agent','now')`, actorRef)
	base, _ := json.Marshal(map[string]string{"commit": boot.Entry.BaseSHA, "verification_ref": verified.OperationRef})
	exec(`INSERT INTO workflow_member_groups(work_id,group_id,controller_actor_ref,base_subject,permission,recorded_seq) VALUES(?,'resume-group',?,?,'first_entry',1)`, boot.WorkID, mainRef, string(base))
	memberID := ""
	for _, jobID := range []string{"job:resume", "job:other"} {
		job := store.WorkerJobRecordedPayload{JobID: jobID, Revision: 1, ContractVersion: 1, Objective: "Resume member fixture", StoppingCondition: "Member resume tested", ProjectScope: "project-wl"}
		job.Digest = store.DeriveWorkerJobDigest(job)
		raw, _ := json.Marshal(job)
		exec(`INSERT INTO worker_job_revisions(work_id,job_id,revision,contract_version,objective,stopping_condition,project_scope,path_scope,predicate_ids,checks,prerequisites,unresolved_refs,digest,state,recorded_at,recorded_payload) VALUES(?,?,1,1,?,?,?,'[]','[]','[]','[]','[]',?,'recorded','now',?)`, boot.WorkID, jobID, job.Objective, job.StoppingCondition, job.ProjectScope, job.Digest, string(raw))
		identity, _ := json.Marshal(struct {
			Group string
			Job   store.WorkerJobBinding
		}{"resume-group", store.WorkerJobBinding{JobID: jobID, Revision: 1, Digest: job.Digest}})
		var canonical any
		if err := json.Unmarshal(identity, &canonical); err != nil {
			t.Fatal(err)
		}
		identity, _ = json.Marshal(canonical)
		digest := sha256.Sum256(identity)
		id := "member-" + hex.EncodeToString(digest[:])
		var boundActor any
		if jobID == "job:resume" {
			memberID, boundActor = id, actorRef
		}
		exec(`INSERT INTO workflow_group_members(work_id,group_id,member_id,job_id,job_revision,job_digest,project_id,actor_ref) VALUES(?,'resume-group',?,?,1,?,'project-wl',?)`, boot.WorkID, id, jobID, job.Digest, boundActor)
	}
	exec(`INSERT INTO workflow_member_callers(actor_ref,work_id,project_id,group_id,member_id) VALUES(?,?,'project-wl','resume-group',?)`, actorRef, boot.WorkID, memberID)
	if claim {
		path, branch := boot.Entry.Path+"--"+memberID, boot.Entry.Branch+"--"+memberID
		gitOutput(t, repo, "worktree", "add", "-b", branch, path, boot.Entry.BaseSHA)
		exec(`INSERT INTO worktree_entries(set_id,project_id,member_id,claim_op_id,branch,base_sha,path,repository_id,state,verified_at) SELECT set_id,project_id,?,'resume-member-claim',?,base_sha,?,repository_id,'active','now' FROM worktree_entries WHERE set_id=? AND project_id='project-wl' AND member_id=''`, memberID, branch, path, boot.Entry.SetID)
	}
	exec(`DELETE FROM write_guard`)
	version, _, err := s.ScopeVersion(t.Context(), "project-wl")
	if err != nil {
		t.Fatal(err)
	}
	return memberResumeFixture{s: s, repo: repo, integration: boot.Entry.Path, memberID: memberID, input: workResumeInput{ProductID: "product-wl", ProjectID: "project-wl", WorkID: boot.WorkID, SessionRef: actor.SessionRef, CallEnvelope: &agent.CallEnvelope{SchemaVersion: "1.0", RequestID: "resume-request", ClientRef: actor.ClientRef, AgentRef: actor.AgentRef, SessionRef: actor.SessionRef, Directory: repo, Worktree: repo, AmbientProjectID: "project-wl", SelectedProductID: "product-wl", ScopeVersion: version, ManifestDigest: agent.ManifestDigest}}}
}

func runMemberResume(t *testing.T, f memberResumeFixture) (int, workResumeOutput, string) {
	t.Helper()
	t.Chdir(f.repo)
	raw, err := json.Marshal(f.input)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := runWorkResume(raw, f.s, &out, &errOut)
	var result workResumeOutput
	if code == 0 {
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	} else if out.Len() != 0 {
		t.Fatalf("refusal carried output: %s", out.String())
	}
	return code, result, errOut.String()
}

func resumeChanges(t *testing.T, s *store.Store) int64 {
	t.Helper()
	var count int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT total_changes()`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestWorkResumeAuthenticatedMember(t *testing.T) {
	f := newMemberResumeFixture(t, true)
	before := resumeChanges(t, f.s)
	code, output, stderr := runMemberResume(t, f)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var path string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND project_id='project-wl' AND member_id=?`, output.Worktree.SetID, f.memberID).Scan(&path); err != nil {
		t.Fatal(err)
	}
	if output.Worktree.Path != path || path == f.integration || output.BranchFreshness == nil {
		t.Fatalf("member path=%s integration=%s output=%+v", path, f.integration, output)
	}
	if after := resumeChanges(t, f.s); after != before {
		t.Fatalf("active resume wrote: %d -> %d", before, after)
	}
	// Restoration must receive the same authenticated owner, not reselect the
	// integration entry when the claimed member's native tree disappears.
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	code, output, stderr = runMemberResume(t, f)
	if code != 0 || !output.Worktree.Restored || output.Worktree.Path != path {
		t.Fatalf("member restoration code=%d output=%+v stderr=%s", code, output, stderr)
	}
}

func TestWorkResumeMemberRefusalsBeforeWrites(t *testing.T) {
	for _, name := range []string{"missing-envelope", "unknown-client", "foreign-client", "foreign-agent", "wrong-principal", "wrong-session", "wrong-work", "wrong-project", "wrong-directory", "wrong-manifest", "reassigned-member", "missing-member-tree"} {
		t.Run(name, func(t *testing.T) {
			f := newMemberResumeFixture(t, name != "missing-member-tree")
			switch name {
			case "missing-envelope":
				f.input.CallEnvelope = nil
			case "foreign-client":
				f.input.CallEnvelope.ClientRef = "client-foreign"
			case "unknown-client":
				f.input.CallEnvelope.ClientRef = "client-unknown"
			case "foreign-agent":
				f.input.CallEnvelope.AgentRef = "agent-other"
			case "wrong-principal":
				f.input.CallEnvelope.PrincipalRef = "principal-foreign"
			case "wrong-session":
				f.input.CallEnvelope.SessionRef = "session-foreign"
			case "wrong-work":
				f.input.WorkID = "work-wl"
			case "wrong-project":
				f.input.ProjectID = "project-foreign"
			case "wrong-directory":
				f.input.CallEnvelope.Directory = f.integration
			case "wrong-manifest":
				f.input.CallEnvelope.ManifestDigest = "sha256:" + strings.Repeat("0", 64)
			case "reassigned-member":
				if _, err := f.s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_group_members SET actor_ref=NULL; DELETE FROM write_guard`); err != nil {
					t.Fatal(err)
				}
			}
			before := resumeChanges(t, f.s)
			code, _, stderr := runMemberResume(t, f)
			if code != workResumeRefusalExit {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			if after := resumeChanges(t, f.s); after != before {
				t.Fatalf("refusal wrote: %d -> %d", before, after)
			}
			if name == "missing-member-tree" && !strings.Contains(stderr, "worktree_claim") {
				t.Fatalf("missing claim remedy: %s", stderr)
			}
		})
	}
}

func TestWorkResumeAuthenticatedOrdinaryCaller(t *testing.T) {
	f := newMemberResumeFixture(t, true)
	f.input.SessionRef = "session-main"
	f.input.CallEnvelope.SessionRef = f.input.SessionRef
	f.input.CallEnvelope.AgentRef = "agent-other"
	before := resumeChanges(t, f.s)
	code, output, stderr := runMemberResume(t, f)
	if code != 0 || output.Worktree.Path != f.integration {
		t.Fatalf("ordinary resume code=%d output=%+v stderr=%s", code, output, stderr)
	}
	if after := resumeChanges(t, f.s); after != before {
		t.Fatalf("ordinary active resume wrote: %d -> %d", before, after)
	}
}
