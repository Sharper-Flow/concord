package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// The Project-session handoff tool boundary (CD-0182 amendment): the record
// and consume mutations resolve their source and receiving identities from
// the authenticated call context, the artifact-preservation probe runs
// before the transaction, and the retirement read derives from verified
// facts only.

func resultMap(t *testing.T, e agentcontract.Envelope) map[string]any {
	t.Helper()
	var out map[string]any
	if json.Unmarshal(e.Result, &out) != nil {
		t.Fatalf("result is not an object: %s", e.Result)
	}
	return out
}

func resultField(t *testing.T, e agentcontract.Envelope, key string) string {
	t.Helper()
	value, _ := resultMap(t, e)[key].(string)
	return value
}

func resultBool(t *testing.T, e agentcontract.Envelope, key string) bool {
	t.Helper()
	value, _ := resultMap(t, e)[key].(bool)
	return value
}

func osWrite(dir, name, content string) error {
	return os.WriteFile(dir+"/"+name, []byte(content), 0o644)
}

func cleanHandoffRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := exec.Command("git", "init", "--quiet", "-b", "main", repo).Run(); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", repo, "config", "user.email", "test@example.invalid").Run()
	_ = exec.Command("git", "-C", repo, "config", "user.name", "Concord Test").Run()
	if err := osWrite(repo, "tracked.txt", "base\n"); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", repo, "add", ".").Run()
	if err := exec.Command("git", "-C", repo, "commit", "--quiet", "-m", "base").Run(); err != nil {
		t.Fatal(err)
	}
	// The claim preflight fetches origin's default branch, so the fixture
	// seeds a real local bare origin the fetch can reach.
	origin := repo + "-origin.git"
	if err := exec.Command("git", "-C", repo, "init", "--quiet", "--bare", "-b", "main", origin).Run(); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", repo, "remote", "add", "origin", origin).Run()
	if err := exec.Command("git", "-C", repo, "push", "--quiet", "origin", "main").Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "fetch", "--quiet", "origin").Run(); err != nil {
		t.Fatal(err)
	}
	_ = exec.Command("git", "-C", repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main").Run()
	return repo
}

func handoffDispatchFixture(t *testing.T) (*store.Store, *Service, *Service, CallEnvelope, CallEnvelope, string, string) {
	t.Helper()
	ctx := context.Background()
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "handoff-product", Kind: "product.created", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"display_name":"Handoff","stage_maturity":"prototype","stage_audience_commitment":"operator_only"}`)},
		{EventID: "handoff-project-w", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"display_name":"Source Project"}`)},
		{EventID: "handoff-project-e", Kind: "project.created", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: json.RawMessage(`{"display_name":"Receiving Project"}`)},
		{EventID: "handoff-member-w", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-1","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "handoff-member-e", Kind: "product_project.added", SubjectType: store.SubjectProduct, SubjectID: "product-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"product_id":"product-1","project_id":"project-2","role":"secondary","reason":"fixture","expected_version":2,"resulting_version":3}`)},
		{EventID: "handoff-work", Kind: "work.created", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 4, Payload: json.RawMessage(`{"work_kind":"task","title":"Handoff","priority":1}`)},
		{EventID: "handoff-work-members", Kind: "work.memberships_replaced", SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"project-1","role":"primary"},{"project_id":"project-2","role":"secondary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProduct, "product-1"): 0, store.VersionRef(store.SubjectProject, "project-1"): 0, store.VersionRef(store.SubjectProject, "project-2"): 0, store.VersionRef(store.SubjectWorkItem, "work-1"): 0}}); err != nil {
		t.Fatal(err)
	}
	// Two repositories, one per Project, with linked worktrees for the sessions.
	repoSource := cleanHandoffRepo(t)
	repoReceive := cleanHandoffRepo(t)
	normalizedSource, err := store.NormalizeProjectLocator(store.LocatorCanonicalPath, repoSource)
	if err != nil {
		t.Fatal(err)
	}
	normalizedReceive, err := store.NormalizeProjectLocator(store.LocatorCanonicalPath, repoReceive)
	if err != nil {
		t.Fatal(err)
	}
	sourceLocatorPayload, err := json.Marshal(map[string]any{"project_id": "project-1", "locator_id": "canonical-source", "kind": string(store.LocatorCanonicalPath), "value": repoSource, "normalized_value": normalizedSource, "expected_version": 1, "resulting_version": 2})
	if err != nil {
		t.Fatal(err)
	}
	locatorPayload, err := json.Marshal(map[string]any{"project_id": "project-2", "locator_id": "path-receive", "kind": string(store.LocatorCanonicalPath), "value": repoReceive, "normalized_value": normalizedReceive, "expected_version": 1, "resulting_version": 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{
		{EventID: "handoff-locator-w", Kind: "project.locator_added", SubjectType: store.SubjectProject, SubjectID: "project-1", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: sourceLocatorPayload},
		{EventID: "handoff-locator-e", Kind: "project.locator_added", SubjectType: store.SubjectProject, SubjectID: "project-2", Actor: "operator", OccurredAt: fixedTime(), PayloadVersion: 2, Payload: locatorPayload},
	}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProject, "project-1"): 1, store.VersionRef(store.SubjectProject, "project-2"): 1}}); err != nil {
		t.Fatal(err)
	}
	sourceService, sourceGrant := authorizedHandoffService(t, s, "client-source", []string{"project-1"}, repoSource)
	receiveService, receiveGrant := authorizedHandoffService(t, s, "client-receive", []string{"project-2"}, repoReceive)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	receiveScope, _, err := s.ScopeVersion(ctx, "project-2")
	if err != nil {
		t.Fatal(err)
	}
	sourceEnv := handoffEnvelope(sourceGrant, "project-1", scopeVersion)
	receiveEnv := handoffEnvelope(receiveGrant, "project-2", receiveScope)
	version := seedAgentWorkflow(t, s, sourceGrant)
	if version != 4 {
		t.Fatalf("workflow seed version=%d, want 4", version)
	}
	// Walk the implementation workflow to its approved contract through the real
	// tool boundary, so the handoff record binds an active workflow
	// contract version. Each step carries the fields its contract declares.
	stepFields := map[string]map[string]any{
		"record_proposal":  {"problem": "The bounded problem statement.", "affected": []string{"The affected system."}, "stakes": "The bounded stakes statement.", "user_outcomes": []string{"The expected user outcome."}},
		"record_alignment": {"searched": "The bounded backlog search statement.", "outcome": "none_found"},
	}
	for _, action := range []string{"record_proposal", "record_alignment"} {
		stepped := handoffInvoke(t, s, sourceService, sourceEnv, "workflow_action", map[string]any{
			"work_id": "work-1", "expected_version": version, "action_id": action,
			"fields": stepFields[action], "idempotency_key": "handoff-" + action,
		})
		if stepped.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("%s failed: %+v", action, stepped.Error)
		}
		version, err = workVersionForWorkflow(t, s)
		if err != nil {
			t.Fatal(err)
		}
	}
	contractFields := workflowContractFieldsForSeededRegistry(t, s)
	approved, _ := dispatchWorkflowActionResult(t, s, sourceService, sourceEnv, version, "approve_contract", contractFields, "handoff-approve-contract")
	if approved.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("approve_contract failed: %+v", approved.Error)
	}
	recordApprovedWorkDesign(t, s, sourceService, sourceEnv, "work-1", "handoff-design")
	sourcePath := filepath.Join(t.TempDir(), "source")
	receivePath := filepath.Join(t.TempDir(), "receive")
	for _, pair := range [][2]string{{repoSource, sourcePath}, {repoReceive, receivePath}} {
		if out, err := exec.Command("git", "-C", pair[0], "worktree", "add", "-b", "work/work-1", pair[1], "HEAD").CombinedOutput(); err != nil {
			t.Fatalf("worktree add: %s: %v", out, err)
		}
	}
	// Re-authorize the source session in its linked worktree.
	scopeVersion, _, err = s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	sourceGrant = handoffSessionReauthorized(t, sourceService, "client-source", "project-1", sourcePath)
	sourceEnv = handoffEnvelope(sourceGrant, "project-1", scopeVersion)
	return s, sourceService, receiveService, sourceEnv, receiveEnv, repoSource, receivePath
}

// handoffSessionReauthorized re-authorizes a client in a linked worktree.
func handoffSessionReauthorized(t *testing.T, service *Service, client, project, worktree string) Authority {
	t.Helper()
	invocation := Invocation{
		ClientRef: client, PrincipalRef: "human-1", SessionRef: "session-" + client, AgentRef: "agent-1",
		Directory: worktree, Worktree: worktree, ManifestDigest: agentcontract.ManifestDigest,
		RequiredCapability: "work_transition", ProductID: "product-1", ProjectID: project,
	}
	authority, err := service.Authorize(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

// authorizedHandoffService mirrors newAuthorizedService with a linked
// worktree the handoff record can probe.
func authorizedHandoffService(t *testing.T, db *store.Store, client string, projects []string, worktree string) (*Service, Authority) {
	t.Helper()
	ctx := context.Background()
	service := NewService(db)
	service.Now = fixedTime
	resolveProjectAuthority(service, store.ProjectResolution{ProjectID: projects[0]})
	if err := service.RegisterTrustedClient(ctx, testClientRegistration(client, "human-1", []agentcontract.Capability{"product_read", "work_transition"}, []string{"product-1"}, projects)); err != nil {
		t.Fatal(err)
	}
	invocation := Invocation{
		ClientRef: client, PrincipalRef: "human-1", SessionRef: "session-" + client, AgentRef: "agent-1",
		Directory: worktree, Worktree: worktree, ManifestDigest: agentcontract.ManifestDigest,
		RequiredCapability: "work_transition", ProductID: "product-1", ProjectID: projects[0],
	}
	authority, err := service.Authorize(ctx, invocation)
	if err != nil {
		t.Fatal(err)
	}
	return service, authority
}

func handoffEnvelope(grant Authority, ambientProject, scopeVersion string) CallEnvelope {
	return CallEnvelope{SchemaVersion: "1.0", RequestID: "handoff-request", ClientRef: grant.ClientRef, PrincipalRef: grant.PrincipalRef, SessionRef: grant.SessionRef, AgentRef: grant.AgentRef, Directory: grant.Worktree, Worktree: grant.Worktree, AmbientProjectID: ambientProject, SelectedProductID: "product-1", ScopeVersion: scopeVersion, ManifestDigest: grant.ManifestDigest}
}

func handoffInvoke(t *testing.T, s *store.Store, service *Service, env CallEnvelope, operation string, input map[string]any) agentcontract.Envelope {
	t.Helper()
	raw, _ := json.Marshal(input)
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_transition", Operation: operation, Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func retirementInvoke(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID string) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"work_id": workID})
	response, err := Dispatch(context.Background(), s, service, InvokeRequest{Tool: "concord_work_trace", Operation: "project_retirement", Input: raw}, env)
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("retirement read failed: %+v", response.Error)
	}
	payload, _ := json.Marshal(response.Result)
	var out map[string]any
	if json.Unmarshal(payload, &out) != nil {
		t.Fatalf("retirement result is not an object: %s", payload)
	}
	return out
}

func TestProjectHandoffToolsRecordConsumeAndGateAtTheToolBoundary(t *testing.T) {
	t.Parallel()
	s, sourceService, receiveService, sourceEnv, receiveEnv, _, _ := handoffDispatchFixture(t)
	// The source records the addressed handoff from its authenticated
	// identities: no input names the source Project or session.
	record := handoffInvoke(t, s, sourceService, sourceEnv, "project_handoff_record", map[string]any{
		"idempotency_key": "record-1",
		"work_id":         "work-1", "target_project_id": "project-2",
		"bounded_job": "verify the receiving repository's adapter surface",
		"next_action": "consume the handoff and run the bounded job",
		"changes":     []string{"adapter/opencode: opener route"},
	})
	if record.Outcome != agentcontract.OutcomeOK {
		_ = osWrite("/tmp/opencode", "handoff-error.txt", record.Error.Message)
		t.Fatalf("record failed: %+v", record.Error)
	}
	handoffID := resultField(t, record, "handoff_id")
	if handoffID == "" {
		t.Fatalf("record result carries no handoff id: %s", record.Result)
	}
	// The same-content replay resolves the recorded handoff from state.
	replay := handoffInvoke(t, s, sourceService, sourceEnv, "project_handoff_record", map[string]any{
		"idempotency_key": "record-2",
		"work_id":         "work-1", "target_project_id": "project-2",
		"bounded_job": "verify the receiving repository's adapter surface",
		"next_action": "consume the handoff and run the bounded job",
		"changes":     []string{"adapter/opencode: opener route"},
	})
	if replay.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("replay failed: %+v", replay.Error)
	}
	if resultField(t, replay, "handoff_id") != handoffID || !resultBool(t, replay, "already_recorded") {
		t.Fatalf("replay=%s, want the same id flagged already_recorded", replay.Result)
	}
	// The session's dirty worktree refuses before any transaction.
	if err := osWrite(sourceEnv.Worktree, "tracked.txt", "mutated\n"); err != nil {
		t.Fatal(err)
	}
	dirty := handoffInvoke(t, s, sourceService, sourceEnv, "project_handoff_record", map[string]any{
		"idempotency_key": "record-dirty",
		"work_id":         "work-1", "target_project_id": "project-2",
		"bounded_job": "second job", "next_action": "consume",
	})
	if dirty.Outcome == agentcontract.OutcomeOK || dirty.Error == nil || !strings.Contains(dirty.Error.Message, "unpreserved changes") {
		t.Fatalf("dirty record=%+v, want the unpreserved-changes refusal", dirty.Error)
	}
	if err := exec.Command("git", "-C", sourceEnv.Worktree, "checkout", "--", "tracked.txt").Run(); err != nil {
		t.Fatal(err)
	}
	// The source session cannot consume its own handoff: the core binds the
	// consume to the addressed Project, which the ambient context carries.
	wrongTarget := handoffInvoke(t, s, sourceService, sourceEnv, "project_handoff_consume", map[string]any{
		"idempotency_key": "consume-wrong", "work_id": "work-1", "handoff_id": handoffID,
	})
	if wrongTarget.Outcome == agentcontract.OutcomeOK || wrongTarget.Error == nil || !strings.Contains(wrongTarget.Error.Message, "addresses Project") {
		t.Fatalf("wrong-target consume=%+v, want the addressed-Project refusal", wrongTarget.Error)
	}
	// The receiving session consumes before managed execution.
	consumed := handoffInvoke(t, s, receiveService, receiveEnv, "project_handoff_consume", map[string]any{
		"idempotency_key": "consume-1", "work_id": "work-1", "handoff_id": handoffID,
	})
	if consumed.Outcome != agentcontract.OutcomeOK || resultBool(t, consumed, "already_consumed") {
		t.Fatalf("consume=%s err=%+v, want the fresh bind", consumed.Result, consumed.Error)
	}
	again := handoffInvoke(t, s, receiveService, receiveEnv, "project_handoff_consume", map[string]any{
		"work_id": "work-1", "handoff_id": handoffID, "idempotency_key": "consume-2",
	})
	if again.Outcome != agentcontract.OutcomeOK || !resultBool(t, again, "already_consumed") {
		t.Fatalf("consume replay=%s err=%+v, want AlreadyConsumed", again.Result, again.Error)
	}
	// The retirement read derives pending from the verified facts; this
	// fixture records no vacate, so readiness stays open with its blockers.
	retirement := retirementInvoke(t, s, receiveService, receiveEnv, "work-1")
	if retirement["state"] != "pending" || retirement["recorded_handoff"] == true {
		t.Fatalf("retirement=%+v, want pending with the recorded-handoff blocker", retirement)
	}
	blockers, _ := retirement["blockers"].([]any)
	if len(blockers) == 0 {
		t.Fatalf("retirement=%+v, want named blockers", retirement)
	}
}

// The consume's idempotent replay rechecks the recorded state instead of
// returning the cached success: the owning session's same-key replay
// resolves the standing bind with no second consumed event, a foreign
// receiver's same-key replay refuses on the recorded bind, and a replay
// after a contract replacement refuses closed on the stale bind.
func TestProjectHandoffConsumeReplayRechecksBindAndContract(t *testing.T) {
	s, sourceService, receiveService, sourceEnv, receiveEnv, _, _ := handoffDispatchFixture(t)
	record := handoffInvoke(t, s, sourceService, sourceEnv, "project_handoff_record", map[string]any{
		"idempotency_key": "replay-record",
		"work_id":         "work-1", "target_project_id": "project-2",
		"bounded_job": "bounded receiver job", "next_action": "execute receiver job",
	})
	if record.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record failed: %+v", record.Error)
	}
	handoffID := resultField(t, record, "handoff_id")
	input := map[string]any{"work_id": "work-1", "handoff_id": handoffID, "idempotency_key": "replay-consume"}
	first := handoffInvoke(t, s, receiveService, receiveEnv, "project_handoff_consume", input)
	if first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("first consume failed: %+v", first.Error)
	}
	// The owning session's same-key replay resolves the standing bind.
	ownerReplay := handoffInvoke(t, s, receiveService, receiveEnv, "project_handoff_consume", input)
	if ownerReplay.Outcome != agentcontract.OutcomeOK || !resultBool(t, ownerReplay, "already_consumed") {
		t.Fatalf("owner replay=%+v result=%s, want the standing bind re-resolved", ownerReplay.Error, ownerReplay.Result)
	}
	// A second receiver's same-key replay resolves the standing shared bind
	// The bind names the work-and-Project pair, so another session of the
	// receiving Project reads already_consumed without a new handoff.
	foreign := receiveEnv
	foreign.SessionRef = "session-foreign-receiver"
	foreignReplay := handoffInvoke(t, s, receiveService, foreign, "project_handoff_consume", input)
	if foreignReplay.Outcome != agentcontract.OutcomeOK || !resultBool(t, foreignReplay, "already_consumed") {
		t.Fatalf("foreign replay=%+v result=%s, want the shared bind resolved for the second receiver", foreignReplay.Error, foreignReplay.Result)
	}
	foreignNewKey := handoffInvoke(t, s, receiveService, foreign, "project_handoff_consume", map[string]any{"work_id": "work-1", "handoff_id": handoffID, "idempotency_key": "replay-consume-foreign-new-key"})
	if foreignNewKey.Outcome != agentcontract.OutcomeOK || !resultBool(t, foreignNewKey, "already_consumed") {
		t.Fatalf("foreign new-key consume=%s err=%+v, want the same shared-bind resolution", foreignNewKey.Result, foreignNewKey.Error)
	}
	// A contract replacement holds the same-key replay closed on the stale
	// bind: the cached success would otherwise authorize execution under a
	// contract the receiver can no longer consume.
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
	INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class)
	SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id='work-1' AND contract_version=1;
	UPDATE workflow_contracts SET superseded_by=2 WHERE work_id='work-1' AND contract_version=1;
	DELETE FROM write_guard;`); err != nil {
		t.Fatal(err)
	}
	staleReplay := handoffInvoke(t, s, receiveService, receiveEnv, "project_handoff_consume", input)
	if staleReplay.Outcome == agentcontract.OutcomeOK || staleReplay.Error == nil || !strings.Contains(staleReplay.Error.Message, "contract version 1") || !strings.Contains(staleReplay.Error.Message, "version 2") {
		t.Fatalf("stale replay=%+v, want the stale-bind refusal", staleReplay.Error)
	}
	var consumedEvents int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts WHERE kind='work.project_handoff_consumed' AND subject_id='work-1'`).Scan(&consumedEvents); err != nil {
		t.Fatal(err)
	}
	if consumedEvents != 1 {
		t.Fatalf("consumed events=%d, want exactly the first consume's event", consumedEvents)
	}
}

// The probe checks the calling session's worktree, not another linked tree.
func TestProjectHandoffRecordProbesTheSessionWorktree(t *testing.T) {
	t.Parallel()
	s, sourceService, _, sourceEnv, _, repoSource, _ := handoffDispatchFixture(t)
	ctx := context.Background()
	for _, table := range []string{"worktree_claims", "worktree_entries", "worktree_occupancy", "session_claim_landings"} {
		var count int
		if err := s.DatabaseForTesting().QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s rows=%d err=%v, want zero", table, count, err)
		}
	}
	// The source session's worktree carries unpreserved changes.
	if err := osWrite(sourceEnv.Worktree, "tracked.txt", "unpreserved source\n"); err != nil {
		t.Fatal(err)
	}
	// A clean linked worktree of the same repository can record independently.
	unrelated := filepath.Join(t.TempDir(), "unrelated-tree")
	if out, err := exec.Command("git", "-C", repoSource, "worktree", "add", "-b", "handoff-unrelated", unrelated, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %s %v", out, err)
	}
	rogueService, rogueGrant := authorizedHandoffService(t, s, "client-source-rogue", []string{"project-1"}, unrelated)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	rogueEnv := handoffEnvelope(rogueGrant, "project-1", scopeVersion)
	rogue := handoffInvoke(t, s, rogueService, rogueEnv, "project_handoff_record", map[string]any{
		"idempotency_key": "record-wrong-source",
		"work_id":         "work-1", "target_project_id": "project-2",
		"bounded_job": "bounded receiver job", "next_action": "consume",
	})
	if rogue.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("clean session record=%+v, want success", rogue.Error)
	}
	// The dirty session refuses until its changes are discarded.
	honest := handoffInvoke(t, s, sourceService, sourceEnv, "project_handoff_record", map[string]any{
		"idempotency_key": "record-honest-dirty",
		"work_id":         "work-1", "target_project_id": "project-2",
		"bounded_job": "another receiver job", "next_action": "consume",
	})
	if honest.Outcome == agentcontract.OutcomeOK || honest.Error == nil || !strings.Contains(honest.Error.Message, "unpreserved changes") {
		t.Fatalf("honest dirty record=%+v, want the unpreserved-changes refusal", honest.Error)
	}
	if err := exec.Command("git", "-C", sourceEnv.Worktree, "checkout", "--", "tracked.txt").Run(); err != nil {
		t.Fatal(err)
	}
	honestClean := handoffInvoke(t, s, sourceService, sourceEnv, "project_handoff_record", map[string]any{
		"idempotency_key": "record-honest-clean",
		"work_id":         "work-1", "target_project_id": "project-2",
		"bounded_job": "another receiver job", "next_action": "consume",
	})
	if honestClean.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("honest clean record failed: %+v", honestClean.Error)
	}
}

// The adapter derives the automatic consume idempotency key from the complete
// consume identity — work and handoff — through a bounded hash, so every
// accepted identity pair yields a key inside the payload contract's
// 128-character bound. The reviewed reproduction accepted a 123-character
// work_id and refused the pre-fix concatenated 156-character key with
// "maxLength at $.idempotency_key", so this regression pins both directions
// at maximum accepted identity lengths through the real payload validator
// the core runs on the adapter's input.
func TestProjectHandoffConsumeKeyFitsTheContractAtMaximumIdentityLengths(t *testing.T) {
	consumeKey := func(workID, handoffID string) string {
		raw, err := json.Marshal([2]string{workID, handoffID})
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		return "handoff-consume-" + hex.EncodeToString(sum[:])[:32]
	}
	workID := "work-" + strings.Repeat("w", 118) // the reviewed accepted 123-character id
	handoffID := "project-handoff-" + strings.Repeat("a", 32)
	maxWorkID := "work-" + strings.Repeat("w", 123)
	maxHandoffID := "project-handoff-" + strings.Repeat("a", 112)
	for name, tc := range map[string]struct{ workID, handoffID string }{
		"reviewed lengths":  {workID, handoffID},
		"maximum lengths":   {maxWorkID, maxHandoffID},
		"generated lengths": {workID[:29], handoffID[:48]},
	} {
		raw, err := json.Marshal(map[string]any{"work_id": tc.workID, "handoff_id": tc.handoffID, "idempotency_key": consumeKey(tc.workID, tc.handoffID)})
		if err != nil {
			t.Fatal(err)
		}
		if err := agentcontract.ValidateOperationPayload("concord_work_transition", "project_handoff_consume", raw, false); err != nil {
			t.Fatalf("%s: bounded consume key refused: %v", name, err)
		}
	}
	legacy := "handoff-consume-" + workID + "-" + strings.Repeat("b", 16)
	raw, err := json.Marshal(map[string]any{"work_id": workID, "handoff_id": handoffID, "idempotency_key": legacy})
	if err != nil {
		t.Fatal(err)
	}
	if err := agentcontract.ValidateOperationPayload("concord_work_transition", "project_handoff_consume", raw, false); err == nil {
		t.Fatalf("the concatenated legacy key (%d characters) must fail the 128-character bound", len(legacy))
	}
}
