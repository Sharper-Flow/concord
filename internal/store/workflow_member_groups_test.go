package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func memberGroupAction(t *testing.T, f workContextFixture, actor WorkflowActor, fields any) (WorkflowActionExecutionResult, error) {
	t.Helper()
	version := readWorkVersion(t, f.store, f.workID)
	key := fmt.Sprintf("member-action-%s-%d", f.workID, version)
	return invokeWorkflowActionForCD0059(context.Background(), t, f.store, WorkflowActionExecutionRequest{
		WorkID: f.workID, ExpectedVersion: version, ActionID: "record_worker_job", Payload: mustJSONValue(fields), Actor: actor,
		AcceptedInputsDigest: nativeDigest(mustJSONValue(fields)), IdempotencyIdentity: key, OperationID: key,
		PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: key, RequestID: key,
		ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC(),
	})
}

func memberGroupFixture(t *testing.T) (workContextFixture, []WorkerJobBinding) {
	t.Helper()
	j := newCandidateJourney(t, "member-roster")
	f := j.fixture
	if err := f.store.RegisterTrustedClient(t.Context(), TrustedClientRecord{ClientRef: f.owner.ClientRef, Status: "active", PrincipalRef: f.owner.PrincipalRef, CapabilitiesJSON: "[]", ProductScopeJSON: "[]", ProjectScopeJSON: "[]", AgentScopeJSON: "[]"}, TrustedClientKeyRecord{ClientRef: f.owner.ClientRef, KeyID: "member-fixture-key", PublicKey: []byte(strings.Repeat("k", 32)), Status: "active"}, "2026-10-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	jobs := []WorkerJobBinding{{JobID: "job:alpha"}, {JobID: "job:beta"}}
	for i := range jobs {
		recordWorkerJobRevisionForTest(t, f.store, f.workID, f.owner, &jobs[i])
	}
	return f, jobs
}

func TestWorkflowMemberGroupAction(t *testing.T) {
	f, jobs := memberGroupFixture(t)
	result, err := memberGroupAction(t, f, f.owner, map[string]any{"assignment_group": map[string]any{"members": jobs}})
	if err != nil {
		t.Fatalf("record assignment group through production action: %v", err)
	}
	var output map[string]json.RawMessage
	if err := json.Unmarshal(result.Result, &output); err != nil || len(output["assignment_group"]) == 0 {
		t.Fatalf("missing core group descriptor: %s (%v)", result.Result, err)
	}
	var descriptor struct {
		GroupID string `json:"group_id"`
		Members []struct {
			MemberID string `json:"member_id"`
			JobID    string `json:"job_id"`
		} `json:"members"`
	}
	if err := json.Unmarshal(output["assignment_group"], &descriptor); err != nil || descriptor.GroupID == "" || len(descriptor.Members) != 2 || descriptor.Members[0].MemberID == descriptor.Members[1].MemberID {
		t.Fatalf("invalid group descriptor %s: %v", output["assignment_group"], err)
	}
	var count int
	if err := f.store.db.QueryRow(`SELECT COUNT(*) FROM worker_job_revisions WHERE work_id=?`, f.workID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("roster wrote an ordinary job revision: %d, %v", count, err)
	}
	reopened, err := Open(t.Context(), f.store.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	f.store = reopened
	if _, err := memberGroupAction(t, f, f.owner, map[string]any{"assignment_group": map[string]any{"members": jobs}}); err == nil {
		t.Fatal("reopened store let a second group reuse first-entry permission")
	}
	target := map[string]any{"principal_ref": f.owner.PrincipalRef, "client_ref": f.owner.ClientRef, "agent_ref": "agent/member", "session_ref": "session/member"}
	binding := map[string]any{"group_id": descriptor.GroupID, "member_id": descriptor.Members[0].MemberID, "project_id": "project", "actor": target}
	if _, err := memberGroupAction(t, f, f.owner, map[string]any{"member_binding": binding}); err != nil {
		t.Fatalf("bind intended caller through production action: %v", err)
	}
	var confirmed int
	if err := f.store.db.QueryRow(`SELECT confirmed FROM workflow_group_members WHERE member_id=?`, descriptor.Members[0].MemberID).Scan(&confirmed); err != nil || confirmed != 0 {
		t.Fatalf("binding falsely confirmed a host claim: %d, %v", confirmed, err)
	}
	member := WorkflowActor{PrincipalRef: f.owner.PrincipalRef, ClientRef: f.owner.ClientRef, AgentRef: "agent/member", SessionRef: "session/member", ActorClass: ActorAgent}
	caller, err := ReadWorkflowMemberCaller(t.Context(), f.store, member)
	if err != nil || caller == nil || caller.MemberID != descriptor.Members[0].MemberID || caller.GroupID != descriptor.GroupID {
		t.Fatalf("retained actor narrowing: %+v, %v", caller, err)
	}
	owner := SessionWorktreeOwner{ClientRef: member.ClientRef, AgentRef: member.AgentRef, SessionRef: member.SessionRef}
	if scope, err := workflowMemberClaimScope(t.Context(), f.store.db, f.workID, "project", owner); err != nil || scope != caller.MemberID {
		t.Fatalf("own unconfirmed scope: %q, %v", scope, err)
	}
	if _, err := workflowMemberClaimScope(t.Context(), f.store.db, "other-work", "project", owner); err == nil {
		t.Fatal("retained member accessed another work")
	}
	member.Model = "another-model"
	if same, err := ReadWorkflowMemberCaller(t.Context(), f.store, member); err != nil || *same != *caller {
		t.Fatalf("model changed member identity: %+v, %v", same, err)
	}
	if _, err := memberGroupAction(t, f, member, map[string]any{"member_binding": binding}); err == nil {
		t.Fatal("member acquired Main binding authority")
	}
	other := f.owner
	other.SessionRef += "-other"
	if _, err := memberGroupAction(t, f, other, map[string]any{"member_binding": binding}); err == nil {
		t.Fatal("another Main actor rebound the controller's member")
	}
	for _, change := range []string{"project", "controller", "member"} {
		copy := map[string]any{"group_id": descriptor.GroupID, "member_id": descriptor.Members[0].MemberID, "project_id": "project", "actor": target}
		switch change {
		case "project":
			copy["project_id"] = "foreign"
		case "controller":
			copy["actor"] = map[string]any{"principal_ref": f.owner.PrincipalRef, "client_ref": f.owner.ClientRef, "agent_ref": f.owner.AgentRef, "session_ref": f.owner.SessionRef}
		case "member":
			copy["member_id"] = descriptor.Members[1].MemberID
		}
		if _, err := memberGroupAction(t, f, f.owner, map[string]any{"member_binding": copy}); err == nil {
			t.Fatalf("accepted %s binding escape", change)
		}
	}
	newTarget := map[string]any{"principal_ref": f.owner.PrincipalRef, "client_ref": f.owner.ClientRef, "agent_ref": "agent/replacement", "session_ref": "session/replacement"}
	binding["actor"] = newTarget
	if _, err := memberGroupAction(t, f, f.owner, map[string]any{"member_binding": binding}); err != nil {
		t.Fatalf("reassign settled intent: %v", err)
	}
	if old, err := ReadWorkflowMemberCaller(t.Context(), f.store, member); err != nil || old == nil || *old != *caller {
		t.Fatalf("reassignment erased retained narrowing: %+v, %v", old, err)
	}
	if _, err := workflowMemberClaimScope(t.Context(), f.store.db, f.workID, "project", owner); err == nil {
		t.Fatal("reassigned actor fell back to integration")
	}
	mainOwner := SessionWorktreeOwner{ClientRef: f.owner.ClientRef, AgentRef: f.owner.AgentRef, SessionRef: f.owner.SessionRef}
	if scope, err := workflowMemberClaimScope(t.Context(), f.store.db, f.workID, "project", mainOwner); err != nil || scope != "" {
		t.Fatalf("unbound Main lost integration scope: %q, %v", scope, err)
	}
}

// Source validation remains useful before the primary regenerates embedded
// contracts. The production action test above still requires that generation.
func TestWorkflowMemberGroupSourceContract(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/agent-tool-surface-payloads.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	job := WorkerJobBinding{JobID: "job:alpha", Revision: 1, Digest: "sha256:" + strings.Repeat("a", 64)}
	other := job
	other.JobID = "job:beta"
	valid := map[string]any{"members": []WorkerJobBinding{job, other}}
	if err := payloadschema.ValidateInDocument(document, "workflow_assignment_group_input", mustJSONValue(valid)); err != nil {
		t.Fatal(err)
	}
	for i, bad := range []any{
		map[string]any{"members": []WorkerJobBinding{job}},
		map[string]any{"members": []WorkerJobBinding{job, job}},
		map[string]any{"members": []WorkerJobBinding{job, other}, "group_id": "chosen"},
	} {
		if err := payloadschema.ValidateInDocument(document, "workflow_assignment_group_input", mustJSONValue(bad)); err == nil {
			t.Fatalf("source contract accepted invalid roster %d: %s", i, mustJSONValue(bad))
		}
	}
	action, err := catalogAction("record_worker_job")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"assignment_group", "member_binding"} {
		found := false
		for _, field := range action.Payload.Fields {
			if field.Name == name && field.SchemaRef != "" {
				found = true
			}
		}
		if !found {
			t.Fatalf("catalog lacks the closed %s variant", name)
		}
	}
	fields, _ := workflowActionObject(mustJSONValue(map[string]any{"assignment_group": valid}))
	if err := validateWorkflowPayloadCrossFieldRules("record_worker_job", fields); err != nil {
		t.Fatal(err)
	}
	fields["checks"] = mustJSONValue([]string{"true"})
	if err := validateWorkflowPayloadCrossFieldRules("record_worker_job", fields); err == nil {
		t.Fatal("ordinary fields escaped the closed variant")
	}
}

func TestWorkflowMemberGroupDerivation(t *testing.T) {
	f, jobs := memberGroupFixture(t)
	ref, _ := WorkflowActorRef(f.owner)
	group, err := deriveWorkflowMemberGroup(t.Context(), f.store.db, f.workID, "operation:roster", ref, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if group.Permission != "first_entry" || group.BaseSubject.Commit == "" || group.BaseSubject.VerificationRef == "" || len(group.Members) != 2 {
		t.Fatalf("incomplete derived group: %+v", group)
	}
	repeated, err := deriveWorkflowMemberGroup(t.Context(), f.store.db, f.workID, "operation:roster", ref, jobs)
	if err != nil || reviewDigest(repeated) != reviewDigest(group) {
		t.Fatalf("unstable operation/member identities: %+v, %v", repeated, err)
	}
	newGroup, err := deriveWorkflowMemberGroup(t.Context(), f.store.db, f.workID, "operation:roster-next", ref, jobs)
	if err != nil || newGroup.GroupID == group.GroupID || newGroup.Members[0].MemberID == group.Members[0].MemberID {
		t.Fatalf("different operations shared identity: %+v, %v", newGroup, err)
	}
	duplicate := []WorkerJobBinding{jobs[0], jobs[0]}
	if _, err := deriveWorkflowMemberGroup(t.Context(), f.store.db, f.workID, "operation:duplicate", ref, duplicate); err == nil {
		t.Fatal("duplicate jobs admitted")
	}
	badDigest := append([]WorkerJobBinding(nil), jobs...)
	badDigest[0].Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := deriveWorkflowMemberGroup(t.Context(), f.store.db, f.workID, "operation:digest", ref, badDigest); err == nil {
		t.Fatal("invented job digest admitted")
	}
	// Revise beta through ordinary authoring to overlap alpha's owner.
	fields := map[string]any{"job_id": jobs[1].JobID, "objective": "keep the bounded owner correct", "stopping_condition": "the owner checks pass", "path_scope": []string{"internal/store"}, "ready": true, "readiness_evidence": []string{"evidence:ready"}}
	if err := recordWorkerJobFieldsForTest(t, f.store, f.workID, f.owner, fields); err != nil {
		t.Fatal(err)
	}
	views, err := readWorkerJobRevisions(t.Context(), f.store.db, f.workID)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if view.Binding.JobID == jobs[1].JobID && view.Ready {
			jobs[1] = view.Binding
		}
	}
	if _, err := deriveWorkflowMemberGroup(t.Context(), f.store.db, f.workID, "operation:overlap", ref, jobs); err == nil || !strings.Contains(err.Error(), "overlapping") {
		t.Fatalf("overlapping siblings: %v", err)
	}
}

func TestWorkflowMemberGroupClosedAndExact(t *testing.T) {
	f, jobs := memberGroupFixture(t)
	badDigest := append([]WorkerJobBinding(nil), jobs...)
	badDigest[0].Digest = "sha256:" + strings.Repeat("0", 64)
	for name, fields := range map[string]any{
		"single":          map[string]any{"assignment_group": map[string]any{"members": jobs[:1]}},
		"duplicate":       map[string]any{"assignment_group": map[string]any{"members": []WorkerJobBinding{jobs[0], jobs[0]}}},
		"digest":          map[string]any{"assignment_group": map[string]any{"members": badDigest}},
		"caller identity": map[string]any{"assignment_group": map[string]any{"members": jobs, "group_id": "chosen"}},
		"ordinary fields": map[string]any{"assignment_group": map[string]any{"members": jobs}, "checks": []string{"true"}},
		"both":            map[string]any{"assignment_group": map[string]any{"members": jobs}, "member_binding": map[string]any{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := memberGroupAction(t, f, f.owner, fields); err == nil {
				t.Fatal("accepted invalid roster")
			}
		})
	}
}

func TestWorkflowMemberGroupSupersession(t *testing.T) {
	f, jobs := memberGroupFixture(t)
	result, err := memberGroupAction(t, f, f.owner, map[string]any{"assignment_group": map[string]any{"members": jobs}})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		Group workflowMemberGroup `json:"assignment_group"`
	}
	if err := json.Unmarshal(result.Result, &output); err != nil {
		t.Fatal(err)
	}
	member := output.Group.Members[0]
	binding := workflowMemberBinding{GroupID: output.Group.GroupID, MemberID: member.MemberID, ProjectID: member.ProjectID, Actor: workflowMemberActor{PrincipalRef: f.owner.PrincipalRef, ClientRef: f.owner.ClientRef, AgentRef: "agent/supersession-member", SessionRef: "session/supersession-member"}}
	if _, err := memberGroupAction(t, f, f.owner, map[string]any{"member_binding": binding}); err != nil {
		t.Fatal(err)
	}
	owner := SessionWorktreeOwner{ClientRef: binding.Actor.ClientRef, AgentRef: binding.Actor.AgentRef, SessionRef: binding.Actor.SessionRef}
	reviewSubjectSupersedeContract(t, f.store, f.workID)
	if scope, err := workflowMemberClaimScope(t.Context(), f.store.db, f.workID, member.ProjectID, owner); err != nil || scope != member.MemberID {
		t.Fatalf("unchanged jobs lost group authority on contract supersession: %q, %v", scope, err)
	}
	if _, err := memberGroupAction(t, f, f.owner, map[string]any{"member_binding": binding}); err != nil {
		t.Fatalf("unchanged jobs lost binding authority on contract supersession: %v", err)
	}
	// Changing the sibling, not the caller's job, revokes the whole roster.
	changed := jobs[1]
	recordWorkerJobRevisionForTest(t, f.store, f.workID, f.owner, &changed)
	if _, err := workflowMemberClaimScope(t.Context(), f.store.db, f.workID, member.ProjectID, owner); err == nil {
		t.Fatal("a changed sibling job preserved group claim authority")
	}
	if _, err := memberGroupAction(t, f, f.owner, map[string]any{"member_binding": binding}); err == nil {
		t.Fatal("a changed sibling job preserved binding authority")
	}
}

// This owner-level check does not stand in for the production action test.
// It exercises the guarded group writer against real recorded jobs and a
// native-qualified base while the public payload schemas await generation.
func TestWorkflowMemberGroupCurrentBindings(t *testing.T) {
	f, jobs := memberGroupFixture(t)
	ref, err := WorkflowActorRef(f.owner)
	if err != nil {
		t.Fatal(err)
	}
	group, err := deriveWorkflowMemberGroup(t.Context(), f.store.db, f.workID, "operation:current-bindings", ref, jobs)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := enterWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "operation:current-bindings:completed", SubjectID: f.workID, Actor: ref, Seq: 100}
	if err := writeWorkflowMemberCompletionTx(t.Context(), tx, event, workflowActionCompletedPayload{ActionID: "record_worker_job", AssignmentGroup: group}); err != nil {
		t.Fatal(err)
	}
	if err := leaveWrite(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := workflowMemberGroupCurrent(t.Context(), f.store.db, f.workID, group.GroupID); err != nil {
		t.Fatalf("new group is not current: %v", err)
	}
	reviewSubjectSupersedeContract(t, f.store, f.workID)
	if err := workflowMemberGroupCurrent(t.Context(), f.store.db, f.workID, group.GroupID); err != nil {
		t.Fatalf("unchanged jobs lost authority on contract supersession: %v", err)
	}
	changed := jobs[1]
	recordWorkerJobRevisionForTest(t, f.store, f.workID, f.owner, &changed)
	if changed.Revision <= jobs[1].Revision || changed.Digest == jobs[1].Digest {
		t.Fatalf("fixture did not change the bound sibling: old=%+v new=%+v", jobs[1], changed)
	}
	if err := workflowMemberGroupCurrent(t.Context(), f.store.db, f.workID, group.GroupID); err == nil || !strings.Contains(err.Error(), "current job revision and digest") {
		t.Fatalf("changed sibling did not revoke group authority: %v", err)
	}
	var repo string
	if err := f.store.db.QueryRow(`SELECT path FROM worktree_entries WHERE set_id=? AND member_id='' AND state='active'`, WorktreeSetID(f.workID)).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	j := &candidateJourney{s: f.store, workID: f.workID, repo: repo}
	gitRunStore(t, repo, "-c", "user.email=integration@example.invalid", "-c", "user.name=integration", "commit", "--allow-empty", "-m", "advance integration subject")
	j.observe(t, "changed-group-base")
	if err := workflowMemberGroupCurrent(t.Context(), f.store.db, f.workID, group.GroupID); err == nil || !strings.Contains(err.Error(), "integration base changed") {
		t.Fatalf("changed qualified base did not revoke group authority: %v", err)
	}
}
