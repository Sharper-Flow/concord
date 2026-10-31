package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var historyRecordKinds = []string{"lifecycle", "phase_exit"}

// historyContractFixture is a work item whose contract v1 was superseded by
// v2 through the folded supersession event.
func historyContractFixture(t *testing.T, name string) (*Store, string) {
	t.Helper()
	fixture := seedWorkContextFixture(t, name)
	t.Cleanup(func() { fixture.store.Close() })
	successor := pContractSuccessor(t, fixture.store, fixture.workID, "retain a successor contract for historical reads")
	if err := runVerdictActionAs(t, fixture.store, fixture.workID, "supersede_contract", mustJSONValue(successor), 0, fixture.owner); err != nil {
		t.Fatal(err)
	}
	return fixture.store, fixture.workID
}

// setHistoryContractPin rewrites one contract version's definition pin
// through the write guard, the shape of a row recorded before pins existed or
// a pin the registry no longer holds.
func setHistoryContractPin(t *testing.T, s *Store, workID string, version int64, ref, digest string) {
	t.Helper()
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_, updateErr := db.Exec(`UPDATE workflow_contracts SET definition_ref=?,definition_digest=? WHERE work_id=? AND contract_version=?`, ref, digest, workID, version)
	if _, err := db.Exec(`DELETE FROM write_guard`); err != nil {
		t.Errorf("remove write guard: %v", err)
	}
	if updateErr != nil {
		t.Fatal(updateErr)
	}
}

// TestHistoryReadsASupersededContractVersion proves the contract_version selector: on
// a work item at contract v2, contract_version 1 returns v1 exactly as
// recorded, with its approval, its successor, its own definition pin, and
// the Product-truth flag that pin answers, while the instance fields stay
// current.
func TestHistoryReadsASupersededContractVersion(t *testing.T) {
	t.Parallel()
	s, workID := historyContractFixture(t, "history-contract-version")
	ctx := context.Background()

	active, err := s.QueryQ7(ctx, Q7Request{Work: workID, RecordKinds: historyRecordKinds})
	if err != nil {
		t.Fatal(err)
	}
	if active.Workflow == nil || active.Workflow.Contract == nil || active.Workflow.Contract.Version != 2 || active.Workflow.Contract.ChangesProductTruth == nil {
		t.Fatalf("active history workflow = %+v, want contract v2 with its Product-truth flag", active.Workflow)
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
	var approvedAt, approvedBy, pinRef, pinDigest, modifies string
	if err := s.DatabaseForTesting().QueryRow(`SELECT approved_at,approved_by,definition_ref,definition_digest,law_modifies FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&approvedAt, &approvedBy, &pinRef, &pinDigest, &modifies); err != nil {
		t.Fatal(err)
	}
	if contract.Version != 1 || contract.SupersededBy == nil || *contract.SupersededBy != 2 || contract.ApprovedAt != approvedAt || contract.ApprovedBy != approvedBy {
		t.Fatalf("selected contract = version %d superseded_by %v approved %q by %q, want v1 superseded by 2 approved %q by %q", contract.Version, contract.SupersededBy, contract.ApprovedAt, contract.ApprovedBy, approvedAt, approvedBy)
	}
	if pinRef == "" {
		t.Fatal("fixture contract v1 recorded no definition pin")
	}
	wantPin := &WorkflowContractDefinitionPin{Ref: pinRef, Digest: pinDigest, Verified: true}
	if !contract.DefinitionPin.Read || !reflect.DeepEqual(contract.DefinitionPin.Pin, wantPin) {
		t.Fatalf("definition pin = %+v, want %+v", contract.DefinitionPin.Pin, wantPin)
	}
	var wantModifies []string
	if err := json.Unmarshal([]byte(modifies), &wantModifies); err != nil {
		t.Fatal(err)
	}
	wantRevisions, err := readWorkflowLawRevisions(ctx, s.db, workID, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantBinding, err := readWorkflowArchitectureBinding(ctx, s.db, workID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(contract.LawModifies, nonNilStrings(wantModifies)) || !reflect.DeepEqual(contract.LawRevisions, wantRevisions) || !reflect.DeepEqual(contract.ArchitectureBinding, wantBinding) || !reflect.DeepEqual(workflow.ArchitectureBinding, wantBinding) {
		t.Fatalf("selected contract law and binding = %+v %+v %+v, want v1's %+v %+v %+v", contract.LawModifies, contract.LawRevisions, contract.ArchitectureBinding, wantModifies, wantRevisions, wantBinding)
	}
	registered, ok := BuiltinWorkflowRegistry().Current(pinRef)
	if !ok || registered.Definition.ChangesProductTruth == nil {
		t.Fatalf("fixture pin %s is not a registered definition with a Product-truth flag", pinRef)
	}
	wantTruth := registered.Definition.ChangesProductTruth
	if contract.ChangesProductTruth == nil || *contract.ChangesProductTruth != *wantTruth || workflow.ChangesProductTruth != *wantTruth {
		t.Fatalf("selected Product-truth flag = %v (top %v), want v1's pinned %v", contract.ChangesProductTruth, workflow.ChangesProductTruth, wantTruth)
	}
	if workflow.State != active.Workflow.State || workflow.CurrentStep != active.Workflow.CurrentStep || workflow.Definition != active.Workflow.Definition {
		t.Fatalf("instance fields = %s/%s/%+v, want the current %s/%s/%+v", workflow.State, workflow.CurrentStep, workflow.Definition, active.Workflow.State, active.Workflow.CurrentStep, active.Workflow.Definition)
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

	page, err := s.QueryQ7(ctx, Q7Request{Work: workID, RecordKinds: historyRecordKinds, ContractVersion: 1, Limit: 1})
	if err != nil || page.NextCursor == nil {
		t.Fatalf("first selected page = %+v, %v; want a next cursor", page.NextCursor, err)
	}
	if _, err := s.QueryQ7(ctx, Q7Request{Work: workID, RecordKinds: historyRecordKinds, ContractVersion: 1, Limit: 1, Cursor: *page.NextCursor}); err != nil {
		t.Fatalf("the cursor must continue its own selector: %v", err)
	}
	for _, other := range []int64{0, 2} {
		_, err := s.QueryQ7(ctx, Q7Request{Work: workID, RecordKinds: historyRecordKinds, ContractVersion: other, Limit: 1, Cursor: *page.NextCursor})
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

// TestHistoryContractVersionWithoutAVerifiedPin proves a version recorded
// without a pin answers definition_pin null and a pin whose ref the registry
// no longer carries answers verified false. A pin resolves by its ref to the
// one current definition (CD-0242 D14), so only a retired ref is unverified.
// Neither substitutes the instance's Product-truth flag: the field is omitted.
func TestHistoryContractVersionWithoutAVerifiedPin(t *testing.T) {
	t.Parallel()
	for name, ref := range map[string]string{"unpinned": "", "unverified": "workflow.retired"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, workID := historyContractFixture(t, "history-contract-"+name)
			digest := ""
			if ref != "" {
				digest = "sha256:" + strings.Repeat("f", 64)
			}
			setHistoryContractPin(t, s, workID, 1, ref, digest)
			result, err := s.QueryQ7(context.Background(), Q7Request{Work: workID, ContractVersion: 1})
			if err != nil {
				t.Fatal(err)
			}
			contract := result.Workflow.Contract
			if contract.ChangesProductTruth != nil || result.Workflow.ChangesProductTruth {
				t.Fatalf("Product-truth flag = %v (top %v), want omitted", contract.ChangesProductTruth, result.Workflow.ChangesProductTruth)
			}
			encoded, err := json.Marshal(contract)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), `"changes_product_truth"`) {
				t.Fatalf("contract JSON carries changes_product_truth: %s", encoded)
			}
			wantPin := `"definition_pin":null`
			if digest != "" {
				wantPin = `"definition_pin":{"ref":"` + ref + `","digest":"` + digest + `","verified":false}`
				if contract.DefinitionPin.Pin == nil || contract.DefinitionPin.Pin.Verified {
					t.Fatalf("definition pin = %+v, want an unverified pin", contract.DefinitionPin.Pin)
				}
			}
			if !strings.Contains(string(encoded), wantPin) {
				t.Fatalf("contract JSON = %s, want %s", encoded, wantPin)
			}
		})
	}
}
