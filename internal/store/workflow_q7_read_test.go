package store

import (
	"context"
	"reflect"
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

// CD-0242 D2 retires the exact transition trace. Paging retains only current rows.
func TestQ7ContinuationPagesHoldCurrentEntries(t *testing.T) {
	t.Parallel()
	s := seedQueryFixture(t)
	const work = "blocked"
	for _, tc := range []struct {
		name      string
		direction string
		kinds     []string
	}{
		{name: "all oldest first", direction: "oldest_first"},
		{name: "all newest first", direction: "newest_first"},
		{name: "relations oldest first", direction: "oldest_first", kinds: []string{"relation"}},
		{name: "relations newest first", direction: "newest_first", kinds: []string{"relation"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full, err := s.QueryQ7(context.Background(), Q7Request{Work: work, Direction: tc.direction, RecordKinds: tc.kinds, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			var got []HistoryEntry
			cursor := ""
			for len(got) <= len(full.Entries) {
				out, err := s.QueryQ7(context.Background(), Q7Request{Work: work, Direction: tc.direction, RecordKinds: tc.kinds, Limit: 1, Cursor: cursor})
				if err != nil {
					t.Fatalf("page %d: %v", len(got)+1, err)
				}
				got = append(got, out.Entries...)
				if out.NextCursor == nil {
					break
				}
				cursor = *out.NextCursor
			}
			if !reflect.DeepEqual(got, full.Entries) {
				t.Fatalf("pages = %v, want %v with no cursor after the last page", got, full.Entries)
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

func TestQ7ProjectsCandidatesAndWarnings(t *testing.T) {
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
	events := []Event{
		workflowEventWithActor("q7-candidate", WorkflowCandidateSetRevised, workID, executor, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1, "contract_version": 1,
			"candidate_kind": "work_item", "candidate_ref": "candidate:one", "added": []string{"candidate:one"}, "removed": []string{},
		}),
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); UPDATE workflow_instances SET completion_payload='{"warnings":["staleness:warning"]}' WHERE work_id=?; DELETE FROM write_guard`, workID); err != nil {
		t.Fatal(err)
	}
	full, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.CandidateIDs) != 1 || full.CandidateIDs[0] != "candidate:one" || len(full.CompletionWarnings) != 1 || full.CompletionWarnings[0] != "staleness:warning" {
		t.Fatalf("fixture did not populate the full reader: candidates=%v warnings=%v", full.CandidateIDs, full.CompletionWarnings)
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
	if len(projection.CompletionWarnings) != 1 || projection.CompletionWarnings[0] != "staleness:warning" {
		t.Errorf("Q7 completion warnings = %v, want [staleness:warning]", projection.CompletionWarnings)
	}
}

func TestQ7ReadsContractAfterLawDeletion(t *testing.T) {
	t.Parallel()
	const workID = "q7-stale-law"
	s, _ := seedCompletionGateCase(t, workID, completionGateCase{requiredEvidence: []string{"verification", "review"}})
	cutoverLawProjection(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	full, err := ReadWorkflowProjection(ctx, s, WorkflowReadRequest{WorkID: workID})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.QueryQ7(ctx, Q7Request{Work: workID})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result == nil || out.Result.Workflow == nil {
		t.Fatal("Q7 omitted the recorded workflow")
	}
	if !reflect.DeepEqual(out.Result.Workflow.Contract, full.Contract) {
		t.Fatalf("Q7 contract = %+v, want %+v", out.Result.Workflow.Contract, full.Contract)
	}
	if out.Result.Workflow.Contract == nil {
		t.Fatal("Q7 omitted the active contract")
	}
	selected, err := s.QueryQ7(ctx, Q7Request{Work: workID, ContractVersion: out.Result.Workflow.Contract.Version})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Result == nil || selected.Result.Workflow == nil || selected.Result.Workflow.Contract == nil {
		t.Fatalf("selected Q7 workflow = %+v, want the recorded contract", selected.Result)
	}
}
