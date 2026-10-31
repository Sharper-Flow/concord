package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

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
// store projection; writes use the agent boundary.
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

// oAgentReview records the coordinator's subject-bound Review through
// record_review. Assessments cite a recorded verification report.
func oAgentReview(t *testing.T, f *oAgentFixtureState, disposition string, prove bool) {
	t.Helper()
	var proof string
	if prove {
		proof = oAgentReport(t, f)
	}
	pin, err := store.ReadWorkPin(context.Background(), f.s, f.work)
	if err != nil || pin.Review == nil || pin.Review.Dependencies == nil {
		t.Fatalf("O Review dependencies: %v", err)
	}
	candidate := store.ReviewCandidate{ProjectID: "project-1", Commit: gitRun(t, f.path, "rev-parse", "HEAD"), Tree: gitRun(t, f.path, "rev-parse", "HEAD^{tree}")}
	report := store.ReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: *pin.Review.Dependencies,
		Assessments: []store.ReviewAssessment{}, Findings: []store.ReviewFinding{}, RemainingObligations: []store.ReviewObligation{}}
	if prove {
		report.Assessments = []store.ReviewAssessment{{PredicateID: "predicate:primary", Assessment: "supported", ReceiptRefs: []string{proof}}}
	}
	spineOK(t, oAgentAction(t, f, "record_review", map[string]any{"review": report}))
}

// oAgentReport binds a passing verification report for the claimed HEAD.
// It binds through oAgentAction because O fixtures hold work_transition
// only, while the spine helper reads continuity under product_read.
func oAgentReport(t *testing.T, f *oAgentFixtureState) string {
	t.Helper()
	commit := gitRun(t, f.path, "rev-parse", "HEAD")
	oAgentAction(t, f, "bind_evidence", map[string]any{
		"evidence_kind": "verification", "immutable_subject_ref": "commit:" + commit,
		"verification_report": map[string]any{"source": "local", "commit": commit, "result": "pass", "run_ref": fmt.Sprintf("run:o-%d", f.request)},
	})
	return "commit:" + commit
}
