package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpineFactualDocumentationCorrectionWithoutApproval(t *testing.T) {
	f := newSpineExecutionFixtureForFamily(t, "workflow.break_fix")
	path := filepath.Join(f.path, "pkg/value.go")
	if err := os.WriteFile(path, []byte("package spine\n\n// Value returns eight.\nfunc Value() int { return 7 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "fixture factual documentation error")
	f.verify(t)
	f.recordJob(t)
	lane, job, epoch := f.startWorker(t, "implement", "attempt:doc-correction")
	if err := os.WriteFile(path, []byte("package spine\n\n// Value returns seven.\nfunc Value() int { return 7 }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, f.path, "add", "pkg/value.go")
	gitRun(t, f.path, "commit", "-qm", "correct the documented value")
	diff := gitRun(t, f.path, "diff", "HEAD^", "HEAD", "--", "pkg/value.go")
	if !strings.Contains(diff, "-// Value returns eight.") || !strings.Contains(diff, "+// Value returns seven.") || strings.Contains(diff, "+func") || strings.Contains(diff, "-func") {
		t.Fatalf("the correction changed more than the factual documentation: %s", diff)
	}
	f.proof = f.verify(t)
	f.finishWorker(t, lane, "attempt:doc-correction", job, nil)
	spineOK(t, f.action(t, "accept_worker_result", map[string]any{"attempt_id": "attempt:doc-correction", "attempt_epoch": epoch}))
	f.recordJob(t)
	f.review(t, "ready_for_final_proof", f.proof)
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "verification", "evidence_ref": f.proof}))
	f.advanceToAcceptance(t)
	spineOK(t, f.call(t, "concord_work_define", "observation_record", map[string]any{
		"work_id": f.work, "statement": "Only the Value documentation changed from eight to seven; the unchanged implementation and its check both return seven.",
		"refs": []string{f.read(t).Pinned.Contract.ArchitectureBinding.HomeDomainID, f.proof}, "idempotency_key": "doc-correction-observation",
	}))
	spineOK(t, f.action(t, "record_verdict", map[string]any{"verdicts": []map[string]any{{"predicate_id": "predicate:primary", "receipt_refs": []string{f.proof}}}}))
	spineOK(t, f.call(t, "concord_work_transition", "workflow_action", map[string]any{
		"work_id": f.work, "expected_version": f.read(t).Pinned.Pin.Version, "action_id": "confirm_premise", "selected_choice": "confirm", "idempotency_key": "doc-correction-confirm",
	}))
	spineOK(t, f.action(t, "bind_evidence", map[string]any{"evidence_kind": "review", "evidence_ref": "sha256:" + strings.Repeat("a", 64)}))
	spineOK(t, f.action(t, "complete", map[string]any{"impact_verdict": "non-breaking"}))
	done := f.read(t)
	if done.Pinned.Pin.Lifecycle != "completed" || len(done.Pinned.Pin.VerifiedCriteria) != 1 || done.Pinned.Contract.Version != 1 {
		t.Fatalf("factual correction did not complete on its original contract: %+v", done.Pinned)
	}
	if countWorkflowApprovalChallenges(t, f.s) != 0 || countRows(t, f.s.DatabaseForTesting(), `SELECT count(*) FROM agent_approvals`) != 0 || f.env.HostApproval != nil {
		t.Fatal("factual documentation correction required an operator prompt")
	}
	if attempts := countRows(t, f.s.DatabaseForTesting(), `SELECT count(*) FROM worker_attempts`); attempts != 2 {
		t.Fatalf("factual correction triggered extra implementation/review attempts: %d", attempts)
	}
}
