package store

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorkPinsAndContractSurviveSupportedUpgrade(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "store.db")
	db := openMigratedTo(t, path, 199)
	if err := ensureInstallationKey(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db, path: path}
	const pinned, peer = "upgrade-pinned", "upgrade-peer"
	owner := seedWorkflowLawFixture(t, s, pinned)
	seedWorkflowPlanFrontier(t, s, pinned, WorkflowStepPlanResearch, "frame")
	version := readWorkVersion(t, s, pinned)
	approval := workflowEventWithActor("upgrade-pin-contract", WorkflowContractApproved, pinned, mustActorRef(t, owner), map[string]any{
		"work_id": pinned, "expected_version": version, "resulting_version": version + 1,
		"contract_version": int64(1), "legislative_subjects": []string{}, "spec_mandate": []string{"spec:one"},
		"law_modifies": []string{}, "law_boundary_version": 1, "consequence_class": "internal_sqlite",
	})
	approval = withPlanDecisionFact(t, WorkflowStepPlanResearch, approval)
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{approval}, ExpectedVersions: workVersion(pinned, version)}); err != nil {
		t.Fatalf("approve pinned contract: %v", err)
	}
	seedTestWorkflow(t, s, peer)
	beforePinned, err := ReadWorkPin(ctx, s, pinned)
	if err != nil {
		t.Fatal(err)
	}
	beforePeer, err := ReadWorkPin(ctx, s, peer)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := readWorkflowContractVersion(ctx, db, pinned, 1)
	if err != nil || !reflect.DeepEqual(contract.SpecMandate, []string{"spec:one"}) {
		t.Fatalf("pinned contract=%+v error=%v", contract, err)
	}
	receipts := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`)
	actors := retirementUpgradeRows(t, db, `SELECT * FROM workflow_actors ORDER BY actor_ref`)
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("upgrade retained schema: %v", err)
	}
	for id, before := range map[string]WorkPin{pinned: beforePinned, peer: beforePeer} {
		after, err := ReadWorkPin(ctx, s, id)
		if err != nil || !reflect.DeepEqual(after, before) {
			t.Fatalf("pin %s changed across upgrade: before=%+v after=%+v error=%v", id, before, after, err)
		}
	}
	afterContract, err := readWorkflowContractVersion(ctx, db, pinned, 1)
	if err != nil || !reflect.DeepEqual(contract, afterContract) {
		t.Fatalf("contract changed across upgrade: before=%+v after=%+v error=%v", contract, afterContract, err)
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM effect_receipts ORDER BY effect_seq`); !reflect.DeepEqual(got, receipts) {
		t.Fatal("upgrade changed effect receipts")
	}
	if got := retirementUpgradeRows(t, db, `SELECT * FROM workflow_actors ORDER BY actor_ref`); !reflect.DeepEqual(got, actors) {
		t.Fatal("upgrade changed actor attribution")
	}
	if err := reviseContractRecord(t, s, pinned, owner, pContractSuccessor(t, s, pinned), readWorkVersion(t, s, pinned)); err != nil {
		t.Fatalf("revise retained contract after upgrade: %v", err)
	}
	active, err := activeWorkflowContractVersion(ctx, db, pinned, "test")
	if err != nil || active != 2 {
		t.Fatalf("active contract after revision=%d error=%v", active, err)
	}
	peerAfter, err := ReadWorkPin(ctx, s, peer)
	if err != nil {
		t.Fatal(err)
	}
	peerAfter.Watermark = beforePeer.Watermark
	if !reflect.DeepEqual(peerAfter, beforePeer) {
		t.Fatalf("revising another work item changed peer pin: before=%+v after=%+v", beforePeer, peerAfter)
	}
}
