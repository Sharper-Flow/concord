package agent

import (
	"reflect"
	"testing"
)

func TestContractRevisionPreservesExecutionAndLegislativeSubjects(t *testing.T) {
	f := newSpineFixtureForFamily(t, "workflow.break_fix")
	before := f.read(t)
	contract := before.Pinned.Contract
	if contract == nil {
		t.Fatal("approved contract missing")
	}
	// Workflow engine contract §4.1: unchanged valid proof needs no
	// replacement merely because a contract version was appended.
	const evidenceRows = `SELECT * FROM workflow_evidence_bindings WHERE work_id=?`
	proof := agentRowsSnapshot(t, f.s, evidenceRows, f.work)
	if proof == "[]" {
		t.Fatal("fixture bound no proof before the revision")
	}
	successor := map[string]any{
		"contract_version":     contract.Version + 1,
		"premise":              contract.Premise,
		"legislative_subjects": contract.LegislativeSubjects,
		"route_conventions":    contract.RouteConventions,
		"spec_mandate":         contract.SpecMandate,
		"law_modifies":         contract.LawModifies,
		"architecture_binding": contract.ArchitectureBinding,
		"rigor_class":          contract.RigorClass,
		"decision_domains":     contract.DecisionDomains,
	}
	updated, _ := dispatchContractRevisionResult(t, f.s, f.service, f.env, f.work, before.Pinned.Pin.Version, successor, "journey-revise-unchanged")
	spineOK(t, updated)
	after := f.read(t)
	if after.Pinned.Step != before.Pinned.Step || after.Pinned.Contract == nil || after.Pinned.Contract.Version != contract.Version+1 ||
		!reflect.DeepEqual(after.Pinned.Contract.LegislativeSubjects, contract.LegislativeSubjects) || after.Pinned.Contract.Premise != contract.Premise {
		t.Fatalf("revision lost execution position or contract authority: before=%+v after=%+v", before.Pinned, after.Pinned)
	}
	if agentRowsSnapshot(t, f.s, evidenceRows, f.work) != proof {
		t.Fatal("ordinary revision discarded or rewrote bound proof")
	}
}
