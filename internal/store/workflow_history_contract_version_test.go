package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

var historyRecordKinds = []string{"lifecycle", "phase_exit"}

// historyContractFixture retains two versions through an ordinary record update.
func historyContractFixture(t *testing.T, name string) (*Store, string) {
	t.Helper()
	fixture := seedWorkContextFixture(t, name)
	t.Cleanup(func() { fixture.store.Close() })
	successor := pContractSuccessor(t, fixture.store, fixture.workID, "retain a successor contract for historical reads")
	if err := reviseContractRecord(t, fixture.store, fixture.workID, fixture.owner, successor, readWorkVersion(t, fixture.store, fixture.workID)); err != nil {
		t.Fatal(err)
	}
	return fixture.store, fixture.workID
}

// TestHistoryReadsASupersededContractVersion proves the contract_version selector: on
// a work item at contract v2, contract_version 1 returns v1 exactly as
// recorded, with its approval and successor, while the instance fields stay current.
func TestHistoryReadsASupersededContractVersion(t *testing.T) {
	t.Parallel()
	s, workID := historyContractFixture(t, "history-contract-version")
	ctx := context.Background()

	active, err := s.QueryQ7(ctx, Q7Request{Work: workID, RecordKinds: historyRecordKinds})
	if err != nil {
		t.Fatal(err)
	}
	if active.Workflow == nil || active.Workflow.Contract == nil || active.Workflow.Contract.Version != 2 {
		t.Fatalf("active history workflow = %+v, want contract v2", active.Workflow)
	}

	selected, err := s.QueryQ7(ctx, Q7Request{Work: workID, RecordKinds: historyRecordKinds, ContractVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	workflow := selected.Workflow
	if workflow == nil || workflow.Contract == nil {
		t.Fatalf("selected history carries no workflow contract: %+v", workflow)
	}
	contract := workflow.Contract
	var approvedAt, approvedBy, modifies string
	if err := s.DatabaseForTesting().QueryRow(`SELECT approved_at,approved_by,law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&approvedAt, &approvedBy, &modifies); err != nil {
		t.Fatal(err)
	}
	if contract.Version != 1 || contract.SupersededBy == nil || *contract.SupersededBy != 2 || contract.ApprovedAt != approvedAt || contract.ApprovedBy != approvedBy {
		t.Fatalf("selected contract = version %d superseded_by %v approved %q by %q, want v1 superseded by 2 approved %q by %q", contract.Version, contract.SupersededBy, contract.ApprovedAt, contract.ApprovedBy, approvedAt, approvedBy)
	}
	var wantModifies []string
	if err := json.Unmarshal([]byte(modifies), &wantModifies); err != nil {
		t.Fatal(err)
	}
	wantBinding, err := readWorkflowArchitectureBinding(ctx, s.db, workID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(contract.LawModifies, nonNilStrings(wantModifies)) || !reflect.DeepEqual(contract.ArchitectureBinding, wantBinding) || !reflect.DeepEqual(workflow.ArchitectureBinding, wantBinding) {
		t.Fatalf("selected contract law and binding = %+v %+v, want v1's %+v %+v", contract.LawModifies, contract.ArchitectureBinding, wantModifies, wantBinding)
	}
	planJSON := func(p *WorkflowStepPlan) string {
		if p == nil {
			return ""
		}
		raw, err := json.Marshal(*p)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	if workflow.State != active.Workflow.State || workflow.CurrentStep != active.Workflow.CurrentStep || planJSON(workflow.Plan) != planJSON(active.Workflow.Plan) || workflow.ChangesProductTruth != active.Workflow.ChangesProductTruth {
		t.Fatalf("instance fields = %s/%s/%+v truth=%v, want the current %s/%s/%+v truth=%v", workflow.State, workflow.CurrentStep, workflow.Plan, workflow.ChangesProductTruth, active.Workflow.State, active.Workflow.CurrentStep, active.Workflow.Plan, active.Workflow.ChangesProductTruth)
	}

	// CD-0242 D2 retires approval traces; selecting a contract only changes the summary.
	if !reflect.DeepEqual(selected.Entries, active.Entries) {
		t.Fatalf("contract selector changed the current entry population")
	}
}

// TestHistoryContractVersionRefusals proves a version the work never had,
// or a selector on work outside a workflow, refuses unknown_scope, and a
// cursor never carries a page across selectors.
func TestHistoryContractVersionRefusals(t *testing.T) {
	t.Parallel()
	s, workID := historyContractFixture(t, "history-contract-refusals")
	ctx := context.Background()
	_, err := s.QueryQ7(ctx, Q7Request{Work: workID, ContractVersion: 9})
	assertFailureKind(t, err, KindUnknownScope)
	seedWork(t, s, "history-cursor-related")
	version := readWorkVersion(t, s, workID)
	if err := applyWorkEvent(t, s, relationAddedEvent("history-cursor-relation", "depends_on", workID, "history-cursor-related", version, version+1), workVersion(workID, version)); err != nil {
		t.Fatal(err)
	}
	kinds := []string{"lifecycle", "phase_exit", "relation"}

	page, err := s.QueryQ7(ctx, Q7Request{Work: workID, RecordKinds: kinds, ContractVersion: 1, Limit: 1})
	if err != nil || page.NextCursor == nil {
		t.Fatalf("first selected page = %+v, %v; want a next cursor", page.NextCursor, err)
	}
	if _, err := s.QueryQ7(ctx, Q7Request{Work: workID, RecordKinds: kinds, ContractVersion: 1, Limit: 1, Cursor: *page.NextCursor}); err != nil {
		t.Fatalf("the cursor must continue its own selector: %v", err)
	}
	for _, other := range []int64{0, 2} {
		_, err := s.QueryQ7(ctx, Q7Request{Work: workID, RecordKinds: kinds, ContractVersion: other, Limit: 1, Cursor: *page.NextCursor})
		assertFailureKind(t, err, KindInvalidCursor)
	}

	plain := openTemp(t)
	t.Cleanup(func() { plain.Close() })
	const plainWork = "history-outside-workflow"
	seedWork(t, plain, plainWork)
	if out, err := plain.QueryQ7(ctx, Q7Request{Work: plainWork}); err != nil || out.Workflow != nil {
		t.Fatalf("history outside a workflow = %+v, %v; want events without a workflow", out.Workflow, err)
	}
	_, err = plain.QueryQ7(ctx, Q7Request{Work: plainWork, ContractVersion: 1})
	assertFailureKind(t, err, KindUnknownScope)
}
