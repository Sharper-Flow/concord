package store

import "testing"

func TestPlanConfirmationBindsRevisedContract(t *testing.T) {
	const workID = "plan-revised-confirmation"
	f := qCurrentAcceptance(t, workID)
	defer f.store.Close()
	contract := pContractSuccessor(t, f.store, workID, "Continue under the revised contract.")
	if err := reviseContractRecord(t, f.store, workID, f.owner, contract, readWorkVersion(t, f.store, workID)); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t.Context(), "confirm_premise", "confirm-revised", f.version(t.Context(), t), nil, nil); err != nil {
		t.Fatalf("revised contract confirmation: %v", err)
	}
	var confirmed int
	if err := f.store.db.QueryRow(`SELECT count(*) FROM workflow_premise_confirmations p JOIN workflow_contracts c ON c.work_id=p.work_id AND c.contract_version=p.contract_version WHERE p.work_id=? AND c.contract_version=2 AND c.superseded_by IS NULL`, workID).Scan(&confirmed); err != nil || confirmed != 1 {
		t.Fatalf("current version-2 confirmations=%d err=%v", confirmed, err)
	}
}
