package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

const oracleFixtureControlID = "control:o-agent-value"

func oracleFixtureDigest(raw []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func oracleFixtureActiveClaimFor(t *testing.T, s *store.Store, work string) (string, string) {
	t.Helper()
	var path, branch string
	if err := s.DatabaseForTesting().QueryRow(`SELECT path,branch FROM worktree_entries WHERE set_id=? AND state='active' AND project_id='project-1'`, store.WorktreeSetID(work)).Scan(&path, &branch); err != nil {
		t.Fatal(err)
	}
	return path, branch
}

func fixtureClaimSubject(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", path, "rev-parse", "--verify", "HEAD").Run(); err == nil {
		return
	}
	gitRun(t, path, "init", "--quiet", "--initial-branch=main")
	gitRun(t, path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "--allow-empty", "-m", "fixture subject")
}

// oAgentFixture keeps Review setup local to the O journeys. Reads use the
// store projection; writes and native preparation use the agent boundary.
type oAgentFixtureState struct {
	*spineFixture
}

func oAgentFixture(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID string) *oAgentFixtureState {
	t.Helper()
	f := oAgentClaimFixture(t, s, service, env, workID)
	// Refine acceptance resolves checks from the Project's default ref, not
	// from the claimed candidate or the knowledge repository's mere presence.
	var canonical string
	if err := s.DatabaseForTesting().QueryRow(`SELECT normalized_value FROM project_locators WHERE kind=? AND project_id='project-1' ORDER BY locator_id LIMIT 1`, store.LocatorCanonicalPath).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	fixtureClaimSubject(t, canonical)
	manifest := filepath.Join(canonical, ".concord/tooling.v1.json")
	if _, err := os.Stat(manifest); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifest, []byte(`{"schema_version":"1.0","project":"fixture","tools":[{"id":"go-test","invocation":"go test ./..."},{"id":"go-vet","invocation":"go vet ./..."}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, canonical, "add", ".concord/tooling.v1.json")
		gitRun(t, canonical, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture declared checks")
	}
	if err := exec.Command("git", "-C", canonical, "remote", "get-url", "origin").Run(); err != nil {
		seedClaimOrigin(t, canonical)
	} else {
		gitRun(t, canonical, "push", "-q", "origin", "HEAD:main")
		gitRun(t, canonical, "fetch", "-q", "origin")
		gitRun(t, canonical, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	}
	return f
}

// oAgentClaimFixture turns the work's claimed worktree into a real Git
// candidate on its claimed branch and binds the caller's session to it.
func oAgentClaimFixture(t *testing.T, s *store.Store, service *Service, env CallEnvelope, workID string) *oAgentFixtureState {
	t.Helper()
	path, branch := oracleFixtureActiveClaimFor(t, s, workID)
	fixtureClaimSubject(t, path)
	if gitRun(t, path, "branch", "--show-current") != branch {
		gitRun(t, path, "branch", "-m", branch)
	}
	env.Directory, env.Worktree = path, path
	env.ScopeVersion = mustScopeVersion(t, s)
	return &oAgentFixtureState{spineFixture: &spineFixture{s: s, service: service, env: env, work: workID, path: path, base: gitRun(t, path, "rev-parse", "HEAD"), request: int(workVersion(t, s, workID) * 100)}}
}

func oAgentAction(t *testing.T, f *oAgentFixtureState, action string, fields map[string]any) agentcontract.Envelope {
	t.Helper()
	response := f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": workVersion(t, f.s, f.work), "action_id": action,
		"fields": fields, "idempotency_key": fmt.Sprintf("o-agent-%s-%d", action, f.request),
	})
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("O %s on %s refused: %+v", action, f.work, response.Error)
	}
	return response
}

// oAgentPreparedOracle qualifies the claimed candidate and prepares the real
// fixture control for the given approved predicates. The preparation key
// names only its inputs (subject, contract version, and bundle), so a re-run
// on unchanged inputs replays the same qualified preparation; preparation is
// not a per-consumer acceptance execution.
func oAgentPreparedOracle(t *testing.T, f *oAgentFixtureState, predicates []string) store.AcceptanceOracle {
	t.Helper()
	oAgentControlRepository(t, f)
	f.base = gitRun(t, f.path, "rev-parse", "HEAD")
	spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "command": []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, "idempotency_key": fmt.Sprintf("o-subject-%d-%.12s", f.request, f.base),
	}))
	snapshot, err := store.ReadWorkflowContinuity(context.Background(), f.s, store.ContinuityRequest{Work: f.work})
	if err != nil || snapshot.Contract == nil {
		t.Fatalf("O review contract: %v", err)
	}
	var domain string
	if err := f.s.DatabaseForTesting().QueryRow(`SELECT root_domain_id FROM domain_registries WHERE product_id='product-1'`).Scan(&domain); err != nil {
		t.Fatal(err)
	}
	owner := store.OracleOwner{OwnerID: "owner:o-agent-value", DomainID: domain,
		Mechanism:  store.OracleMechanism{ProjectID: "project-1", Path: "o_fixture/value.go", EntryPoint: "Value"},
		Obligation: "The fixture value satisfies the approved check.", PredicateIDs: predicates}
	control := store.OracleControl{ControlID: oracleFixtureControlID, OwnerID: owner.OwnerID, PredicateIDs: owner.PredicateIDs,
		CaseIDs: []string{"case:o-agent-value"}, Cwd: "o_fixture", Argv: []string{"go", "test", "-count=1", "-run", "^(TestValue)$", "."},
		RecipeSource:   store.WorkContextReadingSource{Kind: store.WorkContextSourceRepositoryFile, ProjectID: "project-1", Path: "o_fixture/oracle.json", CommitOID: f.base},
		ExpectedResult: store.OracleExpectedResultPass, RequiredEvidenceRole: store.OracleEvidenceRoleReported}
	cases := []store.OracleCase{{CaseID: "case:o-agent-value", OwnerID: owner.OwnerID, EntryPoint: "Value", InputClass: "fixture value", ExpectedState: "value is seven", ControlIDs: []string{control.ControlID}}}
	var bundle map[string]any
	if err := json.Unmarshal(retryJSON(store.NativeOracleControlBundle{Owner: owner, Control: control, Cases: cases}), &bundle); err != nil {
		t.Fatal(err)
	}
	delete(bundle["control"].(map[string]any), "readiness_evidence_refs")
	prepared := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "idempotency_key": fmt.Sprintf("o-prepare-%.12s-v%d-%s", f.base, snapshot.Contract.Version, oracleFixtureDigest(retryJSON(bundle))),
		"oracle": map[string]any{"phase": "prepare", "expected_contract_version": snapshot.Contract.Version, "control_bundle": bundle},
	}))
	var result store.WorktreeVerifyResult
	if err := json.Unmarshal(prepared.Result, &result); err != nil || result.Oracle == nil || result.Oracle.Qualification != "ready" {
		t.Fatalf("O native preparation: %s (%v)", prepared.Result, err)
	}
	control.ReadinessEvidenceRefs = []string{result.OperationRef}
	return store.AcceptanceOracle{Owners: []store.OracleOwner{owner}, Controls: []store.OracleControl{control}, Cases: cases}
}

// oAgentControlRepository commits the fixture control. A worktree that
// already holds a Go module keeps it, so the control joins that module.
func oAgentControlRepository(t *testing.T, f *oAgentFixtureState) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.path, "o_fixture/oracle.json")); os.IsNotExist(err) {
		files := map[string]string{
			"o_fixture/value.go":      "package fixture\nfunc Value() int { return 7 }\n",
			"o_fixture/value_test.go": "package fixture\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value() != 7 { t.Fatal(\"wrong value\") } }\n",
			"o_fixture/oracle.json":   `{"kind":"go_top_level_v1","package_cwd":"o_fixture","test_files":["o_fixture/value_test.go"],"fixture_files":[],"cases":{"case:o-agent-value":["TestValue"]}}`,
		}
		if _, err := os.Stat(filepath.Join(f.path, "go.mod")); os.IsNotExist(err) {
			files["go.mod"] = "module example.invalid/o-fixture\n\ngo 1.26.0\ntoolchain " + goruntime.Version() + "\n"
		}
		for name, content := range files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(f.path, name)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.path, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		gitRun(t, f.path, "add", "go.mod", "o_fixture")
		gitRun(t, f.path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture native control")
	}
}

// oAgentReview records the coordinator's subject-bound Review through
// record_review. Assessments cite this report's native execution of
// the prepared control.
func oAgentReview(t *testing.T, f *oAgentFixtureState, disposition string, prove bool) {
	t.Helper()
	// The Review output must be the current qualified clean candidate, so
	// requalify HEAD through the retained clean-subject owner first.
	qualified := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "command": []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, "idempotency_key": fmt.Sprintf("o-review-subject-%d", f.request),
	}))
	var subject store.WorktreeVerifyResult
	if err := json.Unmarshal(qualified.Result, &subject); err != nil || subject.ExitCode != 0 || subject.TrackedFilesChanged {
		t.Fatalf("O Review subject qualification: %s (%v)", qualified.Result, err)
	}
	pin, err := store.ReadWorkPin(context.Background(), f.s, f.work)
	if err != nil || pin.Review == nil || pin.Review.Dependencies == nil {
		t.Fatalf("O Review dependencies: %v", err)
	}
	candidate := store.ReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, f.path, "rev-parse", "HEAD"), Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	report := store.ReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: *pin.Review.Dependencies,
		Assessments: []store.ReviewAssessment{}, Findings: []store.ReviewFinding{}, RemainingObligations: []store.ReviewObligation{}}
	if prove {
		oracle := oAgentPreparedOracle(t, f, []string{"predicate:primary"})
		control := oracle.Controls[0]
		executed := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
			"work_id": f.work, "idempotency_key": fmt.Sprintf("o-execute-%d", f.request), "oracle": map[string]any{
				"phase": "execute", "preparation_run_ref": control.ReadinessEvidenceRefs[0],
			},
		}))
		var result store.WorktreeVerifyResult
		if err := json.Unmarshal(executed.Result, &result); err != nil || result.ExitCode != 0 || result.Oracle == nil || result.Oracle.Qualification != "pass" {
			t.Fatalf("O native execution: %s (%v)", executed.Result, err)
		}
		report.Assessments = []store.ReviewAssessment{{PredicateID: "predicate:primary", Assessment: "supported", ReceiptRefs: []string{result.OperationRef}}}
	}
	spineOK(t, oAgentAction(t, f, "record_review", map[string]any{"review": report}))
}

func oAgentVerify(t *testing.T, f *oAgentFixtureState, command []string) string {
	t.Helper()
	response := spineOK(t, f.call(t, "concord_work_transition", "worktree_verify", map[string]any{
		"work_id": f.work, "command": command, "idempotency_key": fmt.Sprintf("o-verify-%d", f.request),
	}))
	var result store.WorktreeVerifyResult
	if err := json.Unmarshal(response.Result, &result); err != nil || result.ExitCode != 0 || result.TrackedFilesChanged || result.OperationRef == "" {
		t.Fatalf("O candidate verification: %s (%v)", response.Result, err)
	}
	pin, err := store.ReadWorkPin(context.Background(), f.s, f.work)
	if err != nil || pin.WorkContext == nil || pin.WorkContext.SubjectCommit != gitRun(t, f.path, "rev-parse", "HEAD") {
		t.Fatalf("O verification subject: %+v (%v)", pin.WorkContext, err)
	}
	return result.OperationRef
}
