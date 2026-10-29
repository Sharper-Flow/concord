package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func reviewBlockFixture() *ReviewBlock {
	candidate := ReviewCandidate{ProjectID: "project:one", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	return &ReviewBlock{
		Disposition: "correction_required", InputCandidate: candidate, OutputCandidate: candidate,
		Dependencies: ReviewDependencies{ArchitectureNotApplicable: true, Criteria: []ReviewCriterion{{PredicateID: "predicate:primary", ContentDigest: nativeDigest([]byte("criterion"))}}},
		Assessments:  []ReviewAssessment{}, RemainingObligations: []ReviewObligation{},
		Findings: []ReviewFinding{{FindingID: "finding:one", Severity: "P1", Confidence: "high", Detail: "the current candidate misses its declared obligation", InScope: true, Blocking: true, Status: "open", EvidenceRefs: []string{"evidence:review"}, ClosureRefs: []string{}}},
	}
}

func acceptCurrentReviewForTest(t *testing.T, f workContextFixture, repo, disposition string) {
	t.Helper()
	completeCurrentReviewForTest(t, f, repo, disposition)
}

func completeCurrentReviewForTest(t *testing.T, f workContextFixture, repo, disposition string, mutations ...func(*ReviewBlock)) (string, int64) {
	t.Helper()
	qualifyReviewSubjectForTest(t, f.store, f.workID, repo)
	report := currentReviewBlockForTest(t, f.store, f.workID, "", disposition)
	for _, mutate := range mutations {
		mutate(report)
	}
	if err := recordReviewForTest(t, f.store, f.workID, f.owner, report); err != nil {
		t.Fatalf("record fixture Review: %v", err)
	}
	state, err := loadWorkflowReviewState(t.Context(), f.store.db, f.workID)
	if err != nil || state.Accepted == nil {
		t.Fatalf("read fixture Review: %+v, %v", state.Accepted, err)
	}
	return state.Accepted.ReportRef, latestStepStartEpoch(t, f.store, f.workID, currentStep(t, f.store, f.workID))
}

func recordReviewForTest(t *testing.T, s *Store, workID string, actor WorkflowActor, report *ReviewBlock) error {
	t.Helper()
	ctx, err := prepareTerminalTestContext(t.Context(), s, workID, "record_review")
	if err != nil {
		return err
	}
	version := readWorkVersion(t, s, workID)
	id := fmt.Sprintf("fixture-review-%s-%d", workID, version)
	return s.Transact(ctx, func(tx *Transaction) error {
		_, err := ApplyWorkflowActionTx(ctx, tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "record_review", Payload: mustJSONValue(map[string]any{"review": report}), Actor: actor,
			AcceptedInputsDigest: nativeDigest([]byte(id)), IdempotencyIdentity: id, OperationID: id,
			PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: id, RequestID: id, ContractDigest: testManifestDigest, Now: time.Now().UTC(),
		})
		return err
	})
}

func qualifyReviewSubjectForTest(t *testing.T, s *Store, workID, repo string) WorktreeVerifyResult {
	t.Helper()
	var project string
	if err := s.db.QueryRow(`SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("fixture-subject-%s-%d-%s", workID, readWorkVersion(t, s, workID), runTestGit(t, repo, "rev-parse", "HEAD"))
	result, err := s.VerifyWorktree(t.Context(), WorktreeVerifyRequest{
		WorkID: workID, ProjectID: project, LeaseID: key, RequestID: key, PrincipalRef: "principal/operator",
		Owner:   SessionWorktreeOwner{ClientRef: "client/concord-1", AgentRef: "agent/review-fixture", SessionRef: "session/review-fixture"},
		Command: []string{"git", "rev-parse", "--verify", "HEAD^{commit}"}, Runner: ExecGitRunner{}, SupportedBudgetSeconds: 30,
	})
	if err != nil || result.ExitCode != 0 || result.TrackedFilesChanged || result.SubjectRef == "" {
		t.Fatalf("qualify fixture candidate: %+v, %v", result, err)
	}
	return result
}

func currentReviewBlockForTest(t *testing.T, s *Store, workID, label, disposition string) *ReviewBlock {
	t.Helper()
	repo := reviewFixtureDirectory(t, s, workID)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw, err := (ExecGitRunner{}).Run(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(raw))
	var project string
	if err := s.db.QueryRow(`SELECT project_id FROM work_projects WHERE work_id=? AND role='primary'`, workID).Scan(&project); err != nil {
		t.Fatal(err)
	}
	candidate := ReviewCandidate{ProjectID: project, Commit: commit, Tree: reviewObservedTree(t, repo, commit)}
	dependencies, err := workflowReviewDependencies(t.Context(), s.db, workID)
	if err != nil {
		t.Fatal(err)
	}
	return &ReviewBlock{Disposition: disposition, InputCandidate: candidate, OutputCandidate: candidate, Dependencies: dependencies,
		Assessments: []ReviewAssessment{}, Findings: []ReviewFinding{}, RemainingObligations: []ReviewObligation{}}
}

func reviewFixtureDirectory(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var path string
	if err := s.db.QueryRow(`SELECT e.path FROM worktree_entries e JOIN work_projects p ON p.work_id=? AND p.project_id=e.project_id AND p.role='primary' WHERE e.set_id=? AND e.state='active'`, workID, WorktreeSetID(workID)).Scan(&path); err != nil {
		t.Fatal(err)
	}
	return path
}
