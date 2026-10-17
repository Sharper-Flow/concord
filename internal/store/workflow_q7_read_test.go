package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQ7ProjectsParkedDelivery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancelled bool
	}{{name: "running"}, {name: "cancelled", cancelled: true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := deliveryGateFixture(t)
			if tc.cancelled {
				if err := transitionDeliverWork(t, s, "cancelled", "operator abandoned delivery"); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := s.QueryQ7(ctx, Q7Request{Work: "work-deliv"})
			if err != nil {
				t.Fatal(err)
			}
			if out.Result == nil || out.Result.Workflow == nil {
				t.Fatal("Q7 omitted the recorded workflow")
			}
			parked := out.Result.Workflow.ParkedDelivery
			if parked == nil {
				t.Fatal("Q7 omitted the parked delivery")
			}
			if parked.WorkID != "work-deliv" || parked.StepID != "delivery" || parked.ResumeAction != "record_delivery" || parked.Unreconciled != tc.cancelled || parked.ParkedSeconds < 0 {
				t.Fatalf("Q7 parked delivery = %+v, want delivery gate with unreconciled=%v and nonnegative age", parked, tc.cancelled)
			}
		})
	}
}

func TestQ7ProjectsCandidatesNoticesAndWarnings(t *testing.T) {
	t.Parallel()
	const workID = "q7-fields"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, emptyMandate: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := workflowCurrentVersion(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	executor := DeriveWorkflowActorRef("principal/operator", "client/concord-1", "agent/executor", "session/"+workID)
	noticeID := WorkflowNoticeID(workID, 1, "spec", "spec:one", workID, "informational")
	events := []Event{
		workflowEventWithActor("q7-candidate", WorkflowCandidateSetRevised, workID, executor, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1,
			"candidate_kind": "work_item", "candidate_ref": "candidate:one", "added": []string{"candidate:one"}, "removed": []string{},
		}),
		workflowEvent("q7-notice", WorkflowImpactNoticeRecorded, workID, map[string]any{
			"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2, "notice_id": noticeID,
			"source_contract_version": 1, "entity_kind": "spec", "entity_ref": "spec:one", "target_work_id": workID,
			"edge_id": "edge:" + workID, "old_hash": nil, "new_hash": nil, "severity": "informational",
		}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	if err := AppendWorkflowStalenessObservation(ctx, s, "q7-warning", workID, executor, testManifestDigest,
		json.RawMessage(`{"staleness_rule_id":"staleness:warning","observed_drift":{"severity":"warning","drifted":true}}`), time.Unix(10, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	full, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.CandidateIDs) != 1 || full.CandidateIDs[0] != "candidate:one" || len(full.ImpactNotices) != 1 || full.ImpactNotices[0].NoticeID != noticeID || len(full.CompletionWarnings) != 1 || full.CompletionWarnings[0] != "staleness:warning" {
		t.Fatalf("fixture did not populate the full reader: candidates=%v notices=%v warnings=%v", full.CandidateIDs, full.ImpactNotices, full.CompletionWarnings)
	}
	out, err := s.QueryQ7(ctx, Q7Request{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result == nil || out.Result.Workflow == nil {
		t.Fatal("Q7 omitted the recorded workflow")
	}
	projection := out.Result.Workflow
	if len(projection.CandidateIDs) != 1 || projection.CandidateIDs[0] != "candidate:one" {
		t.Errorf("Q7 candidate IDs = %v, want [candidate:one]", projection.CandidateIDs)
	}
	if len(projection.ImpactNotices) != 1 || projection.ImpactNotices[0].NoticeID != noticeID {
		t.Errorf("Q7 impact notices = %v, want notice %s", projection.ImpactNotices, noticeID)
	}
	if len(projection.CompletionWarnings) != 1 || projection.CompletionWarnings[0] != "staleness:warning" {
		t.Errorf("Q7 completion warnings = %v, want [staleness:warning]", projection.CompletionWarnings)
	}
}

func TestQ7ProjectsStaleLawRevision(t *testing.T) {
	t.Parallel()
	const workID = "q7-stale-law"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	successorHash := "sha256:" + strings.Repeat("b", 64)
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1);
		UPDATE law_subjects SET status='superseded' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one';
		INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid)
		VALUES('project','workflow-law-locator','spec:next','spec','accepted','.concord/docs/next.md','Successor',?,'test');
		INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid)
		VALUES('project','workflow-law-locator','spec:next','supersedes','spec:one','test');
		DELETE FROM fold_guard`, successorHash); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	full, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if full.StaleLawRevision == nil || full.StaleLawRevision.OldLawID != "spec:one" || full.StaleLawRevision.AcceptedSuccessorLawID != "spec:next" || full.StaleLawRevision.AcceptedSuccessorContentHash != successorHash {
		t.Fatalf("fixture did not populate the full reader's stale law revision: %+v", full.StaleLawRevision)
	}
	out, err := s.QueryQ7(ctx, Q7Request{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result == nil || out.Result.Workflow == nil {
		t.Fatal("Q7 omitted the recorded workflow")
	}
	stale := out.Result.Workflow.StaleLawRevision
	if stale == nil || stale.OldLawID != "spec:one" || stale.AcceptedSuccessorLawID != "spec:next" || stale.AcceptedSuccessorContentHash != successorHash {
		t.Fatalf("Q7 stale law revision = %+v, want spec:one superseded by spec:next at %s", stale, successorHash)
	}
}
