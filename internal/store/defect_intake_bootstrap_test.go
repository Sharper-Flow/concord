package store

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// The host work_start/bootstrap capture path shares the fold owner with the
// agent capture path, so these tests hold the pre-effect promise at the host
// boundary: a refused bug capture leaves no work item, no claim, no branch,
// and no worktree, and the recovery route names the research path.

func defectBootstrapRequest(idempotency, kind, task string, intake map[string]any) BootstrapRequest {
	fields := map[string]any{"product_id": "product-bootstrap", "project_id": "project-bootstrap", "title": "Defect bootstrap", "value_statement": "The host capture admits defects", "kind": kind, "task": task, "idempotency_key": idempotency, "priority": 1, "urgency": "standard"}
	if intake != nil {
		fields["defect_intake"] = intake
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	var req BootstrapRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		panic(err)
	}
	return req
}

// bootstrapWorkItems counts the work items a bootstrap captured.
func bootstrapWorkItems(t *testing.T, s *Store, workID string) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id=?`, workID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func bootstrapBranchExists(t *testing.T, repo, workID string) bool {
	t.Helper()
	command := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+workID)
	return command.Run() == nil
}

// An unclassified bug capture refuses at the host boundary before any durable
// or filesystem effect.
func TestBootstrapBugCaptureRequiresDefectIntakePreEffect(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	t.Parallel()
	repo := initBootstrapStoreRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	_, workID, _, err := CanonicalBootstrapIdentity(defectBootstrapRequest("defect-bootstrap-1", "bug", "reproduce the refusal", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapWorktree(context.Background(), defectBootstrapRequest("defect-bootstrap-1", "bug", "reproduce the refusal", nil), nil); err == nil {
		t.Fatal("unclassified bug bootstrap was admitted")
	} else if text := failureText(err); !strings.Contains(text, "defect_intake") {
		t.Fatalf("bootstrap refusal %q does not name defect_intake", text)
	}
	if count := bootstrapWorkItems(t, s, workID); count != 0 {
		t.Fatalf("refused bootstrap left %d work rows", count)
	}
	if bootstrapBranchExists(t, repo, workID) {
		t.Fatal("refused bootstrap created a branch")
	}
	var claims int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if claims != 0 {
		t.Fatalf("refused bootstrap left %d claims", claims)
	}
}

// The full host route: first bug admitted, repeat refused with the route,
// research capture admissible without a completed RCA, and the retry behind a
// completed covering RCA admitted.
func TestBootstrapBugCaptureRecurrenceRoute(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	t.Parallel()
	repo := initBootstrapStoreRepo(t)
	s := openTemp(t)
	seedBootstrapStoreAuthority(t, s, repo)
	first := defectBootstrapRequest("defect-bootstrap-first", "bug", "reproduce the checksum refusal", defectIntakeJSON("upload-checksum-mismatch", nil, ""))
	firstResult, err := s.BootstrapWorktree(context.Background(), first, nil)
	if err != nil {
		t.Fatalf("first bug bootstrap refused: %v", err)
	}
	if block := defectBlock(t, s, firstResult.WorkID); block == nil || block["failure_shape"] != "upload-checksum-mismatch" {
		t.Fatalf("first bug bootstrap persisted no classification: %+v", block)
	}

	repeat := defectBootstrapRequest("defect-bootstrap-repeat", "bug", "the checksum refusal returned", defectIntakeJSON("upload-checksum-mismatch", nil, ""))
	_, repeatWorkID, _, err := CanonicalBootstrapIdentity(repeat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BootstrapWorktree(context.Background(), repeat, nil); err == nil {
		t.Fatal("recurrent bug bootstrap was admitted")
	} else {
		text := failureText(err)
		for _, needle := range []string{"upload-checksum-mismatch", firstResult.WorkID, "research", "root_cause_work_id"} {
			if !strings.Contains(text, needle) {
				t.Fatalf("bootstrap recurrence refusal %q does not name %s", text, needle)
			}
		}
	}
	if count := bootstrapWorkItems(t, s, repeatWorkID); count != 0 {
		t.Fatalf("refused recurrent bootstrap left %d work rows", count)
	}

	research := defectBootstrapRequest("defect-bootstrap-research", "research", "find the root cause of the checksum cluster", defectIntakeJSON("upload-checksum-mismatch", []string{firstResult.WorkID}, ""))
	researchResult, err := s.BootstrapWorktree(context.Background(), research, nil)
	if err != nil {
		t.Fatalf("research bootstrap refused without a completed RCA: %v", err)
	}
	// The bootstrap capture already pinned the research family default, so
	// completing the item needs only the lifecycle record.
	forceWorkLifecycleTransition(t, s, researchResult.WorkID, "completed", "cluster root cause recorded")

	retry := defectBootstrapRequest("defect-bootstrap-retry", "bug", "the checksum refusal returned again", defectIntakeJSON("upload-checksum-mismatch", nil, researchResult.WorkID))
	retryResult, err := s.BootstrapWorktree(context.Background(), retry, nil)
	if err != nil {
		t.Fatalf("recurrent bug bootstrap behind completed RCA refused: %v", err)
	}
	if siblings := defectBlockSiblings(t, s, retryResult.WorkID); len(siblings) != 1 || siblings[0] != firstResult.WorkID {
		t.Fatalf("retry sibling snapshot=%v, want [%s]", siblings, firstResult.WorkID)
	}
}
