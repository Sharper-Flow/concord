package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQ7OmitsUnboundWorkflow(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedWork(t, s, "q7-unbound")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := s.QueryQ7(ctx, Q7Request{Work: "q7-unbound"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result == nil || out.Workflow != nil || out.Result.Workflow != nil {
		t.Fatalf("Q7 must omit an unbound workflow: %+v", out)
	}
}

// TestQ7ContinuationPagesHoldTheNextEvents walks every history page of
// ordinary lifecycle events through its cursor: each page holds exactly the
// next events in the requested direction and kind filter, and only the last
// page carries no cursor.
func TestQ7ContinuationPagesHoldTheNextEvents(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const work = "q7-pages"
	seedWork(t, s, work)
	for i, event := range []Event{
		workTransitionEvent("q7-start", work, "needed", "in_progress", 2, 3),
		workTransitionEvent("q7-finish", work, "in_progress", "completed", 3, 4),
		workReopenedEvent("q7-reopen", work, "completed", 4, 5),
		workTransitionEvent("q7-restart", work, "needed", "in_progress", 5, 6),
		workTransitionEvent("q7-cancel", work, "in_progress", "cancelled", 6, 7),
	} {
		if err := applyWorkEvent(t, s, event, workVersion(work, int64(i+2))); err != nil {
			t.Fatalf("apply %s: %v", event.EventID, err)
		}
	}
	all := []string{"create-" + work, "q7-start", "q7-finish", "q7-reopen", "q7-restart", "q7-cancel"}
	transitions := []string{"q7-start", "q7-finish", "q7-restart", "q7-cancel"}
	reversed := func(ids []string) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[len(ids)-1-i] = id
		}
		return out
	}
	for _, tc := range []struct {
		name      string
		direction string
		kinds     []string
		want      []string
	}{
		{name: "default kinds oldest first", direction: "oldest_first", want: all},
		{name: "default kinds newest first", direction: "newest_first", want: reversed(all)},
		{name: "one kind oldest first", direction: "oldest_first", kinds: []string{"work.transitioned"}, want: transitions},
		{name: "one kind newest first", direction: "newest_first", kinds: []string{"work.transitioned"}, want: reversed(transitions)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const limit = 2
			var want [][]string
			for start := 0; start < len(tc.want); start += limit {
				want = append(want, tc.want[start:min(start+limit, len(tc.want))])
			}
			var got [][]string
			cursor := ""
			for len(got) <= len(want) {
				out, err := s.QueryQ7(context.Background(), Q7Request{Work: work, Direction: tc.direction, EventKinds: tc.kinds, Limit: limit, Cursor: cursor})
				if err != nil {
					t.Fatalf("page %d: %v", len(got)+1, err)
				}
				ids := []string{}
				for _, event := range out.Events {
					ids = append(ids, event.EventID)
				}
				got = append(got, ids)
				if out.NextCursor == nil {
					break
				}
				cursor = *out.NextCursor
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("pages = %v, want %v with no cursor after the last page", got, want)
			}
		})
	}
}

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
	s, completion := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}, emptyMandate: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, err := workflowCurrentVersion(ctx, s, workID)
	if err != nil {
		t.Fatal(err)
	}
	executor := completion.Actor
	noticeID := WorkflowNoticeID(workID, 1, "spec", "spec:one", workID, "informational")
	events := []Event{
		workflowEventWithActor("q7-candidate", WorkflowCandidateSetRevised, workID, executor, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1,
			"candidate_kind": "work_item", "candidate_ref": "candidate:one", "added": []string{"candidate:one"}, "removed": []string{},
		}),
		workflowEvent("q7-notice", WorkflowImpactNoticeRecorded, workID, map[string]any{
			"work_id": workID, "expected_version": version + 1, "resulting_version": version + 2, "notice_id": noticeID,
			"source_contract_version": 1, "entity_kind": "spec", "entity_ref": "spec:one", "target_work_id": workID,
			"edge_owner_work_id": workID, "edge_id": "edge:" + workID, "old_hash": nil, "new_hash": nil, "severity": "informational",
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
	pinnedHash := "sha256:" + strings.Repeat("a", 64)
	attachWorkflowLawPin(t, s, workID, pinnedHash)
	cutoverLawProjection(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	full, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if full.StaleLawRevision == nil || full.StaleLawRevision.OldLawID != "spec:one" || full.StaleLawRevision.OldContentHash != pinnedHash || !reflect.DeepEqual(full.StaleLawRevision.RecoveryActions, []string{"supersede_contract", "terminal_work"}) {
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
	if !reflect.DeepEqual(stale, full.StaleLawRevision) {
		t.Fatalf("Q7 stale law revision = %+v, want deleted spec:one at pinned hash %s with closed recovery actions", stale, pinnedHash)
	}
	// The stale-law check belongs to the active contract: a selected
	// contract version, even the active one, omits it.
	if out.Result.Workflow.Contract == nil {
		t.Fatal("Q7 omitted the active contract")
	}
	selected, err := s.QueryQ7(ctx, Q7Request{Work: workID, ContractVersion: out.Result.Workflow.Contract.Version})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Result == nil || selected.Result.Workflow == nil || selected.Result.Workflow.Contract == nil || selected.Result.Workflow.StaleLawRevision != nil {
		t.Fatalf("selected Q7 workflow = %+v, want the contract without a stale law revision", selected.Result)
	}
}
