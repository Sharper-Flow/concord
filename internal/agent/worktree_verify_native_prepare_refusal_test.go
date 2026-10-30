package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// The genuine native-prepare admission refusal, driven through the agent
// surface against real git: a claimed worktree whose HEAD moved ahead of the
// recorded core subject refuses with missing_evidence, and the store-authored
// remedy — the exact worktree_verify HEAD-refresh argv — must stay
// model-visible, callable with fresh request identities, and leave the
// refused attempt without effect. The pinned external harness
// (refusal-surface.v1.json) owns the injected projection oracle; this fixture
// exercises the admission itself.
func TestNativePrepareStaleCoreSubjectSurfacesAuthoredRemedy(t *testing.T) {
	t.Parallel()
	s, service, grant, repoRoot := tiersRepoFixture(t)
	// A self-contained module the prepare stages compile without executing.
	for name, data := range map[string]string{
		"go.mod":       "module example.invalid/nativefixture\n\ngo 1.26.0\ntoolchain go1.26.7\n",
		"pkg/value.go": "package nativefixture\n\nfunc Value() int { return 7 }\n",
		"pkg/value_test.go": `package nativefixture

import "testing"

func TestValue(t *testing.T) {
	if Value() != 7 {
		t.Fatal("wrong value")
	}
}
`,
		"pkg/oracle.json": `{"kind":"go_top_level_v1","package_cwd":"pkg","test_files":["pkg/value_test.go"],"fixture_files":[],"cases":{"case:agent-native-prepare":["TestValue"]}}`,
	} {
		path := filepath.Join(repoRoot, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repoRoot, "add", ".")
	gitRun(t, repoRoot, "-c", "user.email=concord@example.invalid", "-c", "user.name=Concord Native Fixture", "commit", "-m", "fixture oracle module")
	// The claim resolves its base through the Project's origin, so the module
	// commit must land there before the claim.
	gitRun(t, repoRoot, "push", "-q", "origin", "main")
	base := gitRun(t, repoRoot, "rev-parse", "HEAD")

	claim := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_claim", map[string]any{
		"host_pid": os.Getpid(), "work_id": "work-1", "project_id": "project-1",
		"base_sha": base, "expected_version": 2, "idempotency_key": "native-prepare-claim",
	})
	if claim.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("claim response=%+v err=%+v", claim, claim.Error)
	}
	// The active workflow contract the prepare admission reads: the claiming
	// actor approved it, so the authority join is the fixture's own actor.
	// The current Domain registry seeds after the claim: its knowledge-home
	// locator sorts ahead of the repository locator, and the claim's base
	// resolution must read the repository.
	seedCurrentWorkflowDomainFixture(t, s)
	ownerRef, err := store.WorkflowActorRef(store.WorkflowActor{PrincipalRef: grant.PrincipalRef, ClientRef: grant.ClientRef, AgentRef: grant.AgentRef, SessionRef: grant.SessionRef, ActorClass: store.ActorAgent})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1);
		INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent',?);
		INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-1',1,'native prepare refusal fixture','internal_sqlite','[]','[]','now',?,'[]','[]',0,'prototype_internal');
		INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('work-1',1,'predicate:agent-native-prepare',0,'check','{"kind":"check","check_ref":"check:agent-native-prepare","immutable_subject_ref":"commit:agent-native-prepare","expected_result":"pass"}');
		DELETE FROM write_guard`, ownerRef, grant.PrincipalRef, grant.ClientRef, grant.AgentRef, grant.SessionRef, fixedTime().Format(time.RFC3339Nano), ownerRef); err != nil {
		t.Fatal(err)
	}

	var rootDomain string
	if err := s.DatabaseForTesting().QueryRow(`SELECT root_domain_id FROM domain_registries WHERE product_id='product-1'`).Scan(&rootDomain); err != nil {
		t.Fatal(err)
	}
	owner := store.OracleOwner{
		OwnerID: "owner:agent-native-prepare", DomainID: rootDomain,
		Mechanism:    store.OracleMechanism{ProjectID: "project-1", Path: "pkg/value.go", EntryPoint: "Value"},
		Obligation:   "the pinned native fixture harness resolves and compiles before admission",
		PredicateIDs: []string{"predicate:agent-native-prepare"},
	}
	control := store.OracleControl{
		ControlID: "control:agent-native-prepare", OwnerID: owner.OwnerID,
		PredicateIDs: owner.PredicateIDs, CaseIDs: []string{"case:agent-native-prepare"},
		Cwd:                  "pkg",
		Argv:                 []string{"go", "test", "-count=1", "-run", "^(TestValue)$", "."},
		RecipeSource:         store.WorkContextReadingSource{Kind: store.WorkContextSourceRepositoryFile, ProjectID: "project-1", Path: "pkg/oracle.json", CommitOID: base},
		ExpectedResult:       store.OracleExpectedResultPass,
		RequiredEvidenceRole: store.OracleEvidenceRoleReported,
	}
	bundle := store.NativeOracleControlBundle{
		Owner: owner, Control: control,
		Cases: []store.OracleCase{{
			CaseID: "case:agent-native-prepare", OwnerID: owner.OwnerID, EntryPoint: "Value",
			InputClass: "native fixture module", ExpectedState: "preparation compiles the pinned selector",
			ControlIDs: []string{control.ControlID},
		}},
	}
	// Prepare input omits readiness references, so the marshaled control
	// drops the recorded-job-only field before it crosses the input schema.
	bundleRaw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var bundleInput map[string]any
	if err := json.Unmarshal(bundleRaw, &bundleInput); err != nil {
		t.Fatal(err)
	}
	delete(bundleInput["control"].(map[string]any), "readiness_evidence_refs")
	prepareInput := func(key string) map[string]any {
		return map[string]any{"work_id": "work-1", "idempotency_key": key, "oracle": map[string]any{
			"phase": "prepare", "expected_contract_version": int64(1), "control_bundle": bundleInput,
		}}
	}
	decodeResult := func(env agentcontract.Envelope) struct {
		ExitCode int                       `json:"exit_code"`
		Oracle   *store.NativeOracleResult `json:"oracle"`
	} {
		t.Helper()
		var payload struct {
			ExitCode int                       `json:"exit_code"`
			Oracle   *store.NativeOracleResult `json:"oracle"`
		}
		if err := json.Unmarshal(env.Result, &payload); err != nil {
			t.Fatalf("verify result=%s", env.Result)
		}
		return payload
	}
	assertQualified := func(env agentcontract.Envelope, subject string) {
		t.Helper()
		payload := decodeResult(env)
		p := payload.Oracle
		if payload.ExitCode != 0 || p == nil || p.Phase != "prepare" || p.Qualification != "ready" || p.SubjectCommit != subject || !p.StreamsComplete || p.SelectedDistinctCount != 1 || len(p.SelectedTestNames) != 1 || p.SelectedTestNames[0] != "TestValue" {
			t.Fatalf("preparation is not a qualified ready prepare on %s: exit=%d oracle=%+v", subject, payload.ExitCode, p)
		}
		for _, stage := range p.Stages {
			if stage.ExitCode != 0 {
				t.Fatalf("preparation stage %s: %+v", stage.Name, stage)
			}
		}
	}

	// The recorded core subject is a real subject receipt: the HEAD-refresh
	// verify the remedy names, through the same agent surface.
	first := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-1", "command": []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, "idempotency_key": "native-prepare-subject-1",
	})
	if first.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("first subject verify=%+v err=%+v", first, first.Error)
	}
	prepared := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", prepareInput("native-prepare-1"))
	if prepared.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("baseline prepare=%+v err=%+v", prepared, prepared.Error)
	}
	assertQualified(prepared, base)

	// The claimed worktree moves ahead of the recorded core subject: clean
	// tree, stale subject — the exact stale-core-subject admission state.
	worktreePath := filepath.Join(filepath.Dir(s.Path()), "worktrees", "project-1", "work-1")
	gitRun(t, worktreePath, "-c", "user.email=concord@example.invalid", "-c", "user.name=Concord Native Fixture", "commit", "--allow-empty", "-m", "advance the claimed subject")
	advanced := gitRun(t, worktreePath, "rev-parse", "HEAD")
	if advanced == base {
		t.Fatalf("claimed subject did not advance past %s", base)
	}

	countLeases := func() int {
		t.Helper()
		var count int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_verify_leases`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	leasesBeforeRefusal := countLeases()

	refused := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", prepareInput("native-prepare-2"))
	if refused.Outcome != agentcontract.OutcomeError || refused.Error == nil {
		t.Fatalf("stale-core-subject prepare=%+v, want the typed admission refusal", refused)
	}
	errEnvelope := refused.Error
	const detail = "native verification requires clean HEAD equal to the current qualified subject"
	const remedy = `run worktree_verify with command ["git","rev-parse","--verify","HEAD^{commit}"] and a fresh request identity, then refresh context`
	if errEnvelope.Kind != "missing_evidence" || errEnvelope.RecoveryAction.Kind != "provide_evidence" || errEnvelope.RetrySafe || errEnvelope.EffectState != agentcontract.EffectNone {
		t.Fatalf("refusal lost its closed coupling or no-effect semantics: %+v", errEnvelope)
	}
	if !strings.Contains(errEnvelope.Message, detail) {
		t.Fatalf("refusal lost its cause: %+v", errEnvelope)
	}
	if got, _ := errEnvelope.Details["recovery_detail"].(string); got != remedy {
		t.Fatalf("refusal dropped or altered the authored remedy: %q", got)
	}
	// Decode the serialized response: an in-memory field that never crosses
	// the wire is not a model-visible remedy.
	encoded, err := refused.Encode()
	if err != nil {
		t.Fatalf("refusal cannot cross the model boundary: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	prose, err := json.Marshal(wire["error"])
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(remedy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prose), string(want[1:len(want)-1])) {
		t.Fatalf("serialized native refusal lost the authored remedy: %s", encoded)
	}
	if countLeases() != leasesBeforeRefusal {
		t.Fatalf("refused prepare changed lease count %d -> %d; refusal must record no effect", leasesBeforeRefusal, countLeases())
	}
	var spent int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM idempotency_records WHERE idempotency_key='native-prepare-2'`).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent != 0 {
		t.Fatalf("refused prepare consumed its request identity")
	}

	// The advertised remedy is callable: a fresh HEAD-refresh verify with a
	// fresh request identity re-records the advanced subject, and a fresh
	// prepare then qualifies on it.
	refreshed := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": "work-1", "command": []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, "idempotency_key": "native-prepare-subject-2",
	})
	if refreshed.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("remedy HEAD refresh=%+v err=%+v", refreshed, refreshed.Error)
	}
	qualified := tiersInvoke(t, s, service, grant, "concord_work_transition", "worktree_verify", prepareInput("native-prepare-3"))
	if qualified.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("post-remedy prepare=%+v err=%+v", qualified, qualified.Error)
	}
	assertQualified(qualified, advanced)
}
