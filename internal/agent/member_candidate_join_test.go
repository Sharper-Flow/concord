package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func TestMemberResultsJoinIntoReviewedIntegrationCandidate(t *testing.T) {
	f := newSpineExecutionFixtureForFamily(t, "workflow.break_fix")
	for _, name := range []string{"a", "b"} {
		for file, text := range map[string]string{
			"value.go":      "package member\nfunc Value() int { return 7 }\n",
			"value_test.go": "package member\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value()!=7 { t.Fatal(\"wrong value\") } }\n",
			"oracle.json":   fmt.Sprintf(`{"kind":"go_top_level_v1","package_cwd":"%s","test_files":["%s/value_test.go"],"fixture_files":[],"cases":{"case:member-%s":["TestValue"]}}`, name, name, name),
		} {
			if err := os.MkdirAll(filepath.Join(f.path, name), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.path, name, file), []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	gitRun(t, f.path, "add", ".")
	gitRun(t, f.path, "commit", "-qm", "independent member controls")
	f.base = gitRun(t, f.path, "rev-parse", "HEAD")
	proof := f.verify(t)
	for _, name := range []string{"a", "b"} {
		memberJoinJob(t, f, name)
	}
	jobs := f.read(t).Pinned.Jobs
	bindings := make([]store.WorkerJobBinding, len(jobs))
	for i, job := range jobs {
		bindings[i] = store.WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest}
	}
	// A baseline Review installs a nonnil current candidate before the roster.
	f.recordJob(t)
	oAgentReview(t, &oAgentFixtureState{spineFixture: f}, "continue", false, proof)
	groupFields := map[string]any{"assignment_group": map[string]any{"members": bindings}}
	groupResult := spineOK(t, f.action(t, "record_worker_job", groupFields))
	var descriptor struct {
		Group struct {
			ID      string `json:"group_id"`
			Members []struct {
				ID string `json:"member_id"`
				store.WorkerJobBinding
			} `json:"members"`
		} `json:"assignment_group"`
	}
	if err := json.Unmarshal(groupResult.Result, &descriptor); err != nil || len(descriptor.Group.Members) != 2 {
		t.Fatalf("group descriptor: %s (%v)", groupResult.Result, err)
	}
	type member struct {
		f       *spineFixture
		job     store.WorkerPacketJob
		epoch   int64
		attempt string
		commit  string
	}
	members := make([]member, 2)
	for i, assigned := range descriptor.Group.Members {
		m := &members[i]
		for _, job := range jobs {
			if job.JobID == assigned.JobID {
				m.job = job
			}
		}
		copy := *f
		copy.request += 1000 * (i + 1)
		copy.env.SessionRef = fmt.Sprintf("session-member-join-%d", i)
		copy.env.AgentRef = "agent-engineer"
		copy.proof = ""
		m.f = &copy
		spineOK(t, f.action(t, "record_worker_job", map[string]any{"member_binding": map[string]any{
			"group_id": descriptor.Group.ID, "member_id": assigned.ID, "project_id": "project-1",
			"actor": map[string]any{"principal_ref": copy.env.PrincipalRef, "client_ref": copy.env.ClientRef, "agent_ref": copy.env.AgentRef, "session_ref": copy.env.SessionRef},
		}}))
		claimed := spineOK(t, copy.call(t, "concord_work_transition", "worktree_claim", map[string]any{
			"work_id": f.work, "project_id": "project-1", "host_pid": os.Getpid(), "base_sha": f.base,
			"expected_version": workVersion(t, f.s, f.work), "idempotency_key": fmt.Sprintf("join-claim-%d", i),
		}))
		var claim struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(claimed.Result, &claim); err != nil {
			t.Fatal(err)
		}
		m.f.path, m.f.env.Directory, m.f.env.Worktree = claim.Path, claim.Path, claim.Path
		m.attempt = fmt.Sprintf("attempt:member-join-%d", i)
		_, m.epoch = memberJoinDispatch(t, m.f, "implement", m.attempt, m.job, true)
	}
	if members[0].f.path == members[1].f.path || members[0].f.path == f.path || members[1].f.path == f.path {
		t.Fatal("members did not receive isolated worktrees")
	}
	for i := range members {
		m := &members[i]
		path := m.job.PathScope[0]
		if err := os.WriteFile(filepath.Join(m.f.path, path), []byte("package member\nfunc Value() int { return 3 + 4 }\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, m.f.path, "add", path)
		gitRun(t, m.f.path, "commit", "-qm", "checked member implementation")
		m.commit = gitRun(t, m.f.path, "rev-parse", "HEAD")
		binding := &store.WorkerJobBinding{JobID: m.job.JobID, Revision: m.job.Revision, Digest: m.job.Digest}
		lane, _ := builtinLane("implement")
		m.f.finishWorker(t, lane, m.attempt, binding, nil)
		spineOK(t, f.action(t, "accept_worker_result", map[string]any{"attempt_id": m.attempt, "attempt_epoch": m.epoch}))
		if got := f.read(t).Pinned.Pin.Review.Candidate; got == nil || got.Commit != f.base {
			t.Fatalf("member acceptance replaced shared base: %+v", got)
		}
	}
	// A qualified candidate containing only A must not authorize combined Review.
	gitRun(t, f.path, "merge", "--ff-only", members[0].commit)
	f.verify(t)
	f.recordJob(t)
	missing, _ := memberJoinDispatch(t, f, "review", "attempt:join-missing-b", f.read(t).Pinned.Jobs[0], false)
	if missing.Error == nil || missing.Error.Kind != "stale_requires_review" || !strings.Contains(missing.Error.Message, "every accepted member") {
		t.Fatalf("missing-member Review = %s: %+v", missing.Outcome, missing.Error)
	}
	gitRun(t, f.path, "merge", "--no-ff", "-m", "integrate independent members", members[1].commit)
	combined := gitRun(t, f.path, "rev-parse", "HEAD")
	proof = f.verify(t)
	for _, name := range []string{"a", "b"} {
		memberJoinJob(t, f, name)
	}
	bindings = nil
	for _, job := range f.read(t).Pinned.Jobs {
		if strings.HasPrefix(job.JobID, "job:member-") {
			bindings = append(bindings, store.WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest})
		}
	}
	groupFields = map[string]any{"assignment_group": map[string]any{"members": bindings}}
	f.recordJob(t)
	// The baseline continue was consumed by the first roster, not renewed by acceptance.
	if response := f.action(t, "record_worker_job", groupFields); response.Outcome == OutcomeOK {
		t.Fatal("next group opened without Review of combined candidate")
	}
	oAgentReview(t, &oAgentFixtureState{spineFixture: f}, "continue", false, proof)
	if got := f.read(t).Pinned.Pin.Review.Candidate; got == nil || got.Commit != combined {
		t.Fatalf("accepted Review did not install combined candidate: %+v", got)
	}
	spineOK(t, f.action(t, "record_worker_job", groupFields))
}

func memberJoinJob(t *testing.T, f *spineFixture, name string) {
	t.Helper()
	state := f.read(t)
	owner := store.OracleOwner{OwnerID: "owner:member-" + name, DomainID: state.Pinned.Contract.ArchitectureBinding.HomeDomainID,
		Mechanism:  store.OracleMechanism{ProjectID: "project-1", Path: name + "/value.go", EntryPoint: "Value"},
		Obligation: "The member value is seven.", PredicateIDs: []string{"predicate:primary"}}
	control := store.OracleControl{ControlID: "control:member-" + name, OwnerID: owner.OwnerID, PredicateIDs: owner.PredicateIDs,
		CaseIDs: []string{"case:member-" + name}, Cwd: name, Argv: []string{"go", "test", "-count=1", "-run", "^(TestValue)$", "."},
		RecipeSource:   store.WorkContextReadingSource{Kind: store.WorkContextSourceRepositoryFile, ProjectID: "project-1", Path: name + "/oracle.json", CommitOID: f.base},
		ExpectedResult: store.OracleExpectedResultPass, RequiredEvidenceRole: store.OracleEvidenceRoleReported}
	cases := []store.OracleCase{{CaseID: control.CaseIDs[0], OwnerID: owner.OwnerID, EntryPoint: "Value", InputClass: "checked member value", ExpectedState: "value is seven", ControlIDs: []string{control.ControlID}}}
	var bundle map[string]any
	if err := json.Unmarshal(retryJSON(store.NativeOracleControlBundle{Owner: owner, Control: control, Cases: cases}), &bundle); err != nil {
		t.Fatal(err)
	}
	delete(bundle["control"].(map[string]any), "readiness_evidence_refs")
	prepared := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "idempotency_key": fmt.Sprintf("join-prepare-%s-%d", name, f.request),
		"oracle": map[string]any{"phase": "prepare", "expected_contract_version": state.Pinned.Contract.Version, "control_bundle": bundle},
	}))
	var result store.WorktreeVerifyResult
	if err := json.Unmarshal(prepared.Result, &result); err != nil || result.Oracle == nil || result.Oracle.Qualification != "ready" {
		t.Fatalf("member preparation: %s (%v)", prepared.Result, err)
	}
	control.ReadinessEvidenceRefs = []string{result.OperationRef}
	spineOK(t, f.action(t, "record_worker_job", map[string]any{
		"job_id": "job:member-" + name, "objective": "Implement the independent member value.", "stopping_condition": "The value check passes.",
		"ready": true, "checks": []string{"go test ./..."}, "path_scope": []string{name + "/value.go"}, "readiness_evidence": control.ReadinessEvidenceRefs,
		"acceptance_oracle": store.AcceptanceOracle{Owners: []store.OracleOwner{owner}, Controls: []store.OracleControl{control}, Cases: cases},
	}))
}

func memberJoinDispatch(t *testing.T, f *spineFixture, laneID, attempt string, job store.WorkerPacketJob, admit bool) (Envelope, int64) {
	t.Helper()
	lane, _ := builtinLane(laneID)
	job.CallerExclusions = nil
	packet := bindPacketToRecordedState(t, f.s, map[string]any{"schema_version": store.WorkerPacketSchemaVersion,
		"attempt_id": attempt, "lane_id": lane.ID, "lane_digest": lane.Digest, "work_id": f.work,
		"step_id": f.read(t).Pinned.Step, "inputs": map[string]any{"worker_job": retryJSON(job)}})
	response := f.action(t, "dispatch_worker", map[string]any{"attempt_id": attempt, "worker_packet": packet})
	if !admit {
		return response, 0
	}
	spineOK(t, response)
	var result struct {
		Digest string `json:"worker_packet_digest"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	epoch := f.read(t).Pinned.Pin.Attempt.Epoch
	binding := &store.WorkerJobBinding{JobID: job.JobID, Revision: job.Revision, Digest: job.Digest}
	event := store.Event{EventID: "join-host-" + attempt, Kind: store.WorkerDispatched, SubjectType: store.SubjectWorkItem, SubjectID: f.work,
		Actor: "worker:test", OccurredAt: f.service.Now(), PayloadVersion: store.CurrentEventPayloadVersion(store.WorkerDispatched),
		Payload: retryJSON(store.WorkerDispatchedPayload{AttemptID: attempt, LaneID: lane.ID, LaneDigest: lane.Digest, CapabilityClass: lane.CapabilityClass,
			PacketDigest: result.Digest, ReadbackModel: "openai/test-model", PacketSchemaVersion: store.WorkerPacketSchemaVersion, ReportSchemaVersion: store.WorkerReportSchemaVersion, WorkerJob: binding})}
	if err := f.s.Transact(context.Background(), func(tx *store.Transaction) error {
		pair, err := store.PrepareLaneActorDispatch(context.Background(), tx, event, f.env.PrincipalRef, f.env.ClientRef)
		if err != nil {
			return err
		}
		_, err = store.AppendLaneActorDispatchTx(context.Background(), tx, pair)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return response, epoch
}
