package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestLawDeletionLeavesLateVerdictAdmitted(t *testing.T) {
	const workID = "law-deletion-late-verdict"
	fixture := seedWorkflowReturnRouteFixtureWithSource(t, workID, currentFixtureDefinition(t, "workflow.research"), "complete", []string{"verification"}, []string{"verification", "review", "artifact"}, func(s *Store) {
		home := lawChangeFixtureHome(t, s)
		removeCommittedFixtureLaw(t, home)
		commitFixtureLawFile(t, home, "spec:two", fixtureLawFolderLaw("spec:two"))
	})
	s, owner := fixture.store, fixture.owner
	defer s.Close()
	var startBaseline string
	if err := s.db.QueryRow(`SELECT baseline_json FROM workflow_law_baselines WHERE work_id=?`, workID).Scan(&startBaseline); err != nil { t.Fatal(err) }
	var baseline workflowLawBaseline
	if err := json.Unmarshal([]byte(startBaseline), &baseline); err != nil { t.Fatal(err) }
	if len(baseline.Laws) != 1 || baseline.Laws[0].LawID != "project/spec:two" { t.Fatalf("owning start inventory=%+v", baseline.Laws) }
	if got := currentStep(t, s, workID); got != "complete" { t.Fatalf("step=%q", got) }
	f := workContextFixture{store: s, workID: workID, owner: owner}
	pack := seedResearchContextPack(t, f, "research-proof")
	bindResearchPack(t, f, pack.PackID, 1, true)
	selection := researchSelectionEntry(pack.PackID, 1, "f2", "root")
	if err := f.recordWorkContext(t, map[string]any{"research_finding_refs": []map[string]any{selection}}); err != nil { t.Fatal(err) }
	view, err := readWorkContextView(context.Background(), s.db, workID)
	if err != nil || view == nil || len(view.ResearchFindings) != 1 { t.Fatalf("research receipt=%+v err=%v", view, err) }
	execInFold(t, s, `UPDATE workflow_contracts SET spec_mandate='["spec:two"]' WHERE work_id='`+workID+`' AND contract_version=1`)
	version := readWorkVersion(t, s, workID)
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil { t.Fatal(err) }
	seedWorkflowAuthority(t, s, "law-binding-"+workID, workID, owner.PrincipalRef, "request/law-binding-"+workID, []string{"spec:two"})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{workflowEventWithActor("law-evidence-"+workID, WorkflowEvidenceBound, workID, ownerRef, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version+1, "evidence_kind": "approval", "immutable_subject_ref": "spec:two", "producer_id": owner.PrincipalRef, "producer_run_ref": "law-binding-"+workID,
		"producer_watermark": "request/law-binding-"+workID, "observed_at": "2026-10-11T00:00:00Z",
	})}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil { t.Fatal(err) }
	view, err = readWorkContextView(context.Background(), s.db, workID)
	if err != nil || view == nil || len(view.ResearchFindings) != 1 { t.Fatalf("research receipt=%+v err=%v", view, err) }
	if err := workflowCriterionReceipt(context.Background(), s.db, workID, "predicate:return-route", view.ResearchFindings[0].ViewID, ""); err != nil { t.Fatal(err) }
	reviewer := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/late-verdict-judge", SessionRef: "session/"+workID+"-judge", ActorClass: ActorAgent}
	payload := mustJSONValue(map[string]any{"contract_version": 1, "verdicts": []map[string]any{{"predicate_id": "predicate:return-route", "receipt_refs": []string{view.ResearchFindings[0].ViewID}}}})
	if err := runVerdictActionAs(t, s, workID, "record_verdict", payload, 0, reviewer); err != nil { t.Fatal(err) }
	var recorded int
	if err := s.db.QueryRow(`SELECT count(*) FROM workflow_predicate_verdicts WHERE work_id=? AND contract_version=1`, workID).Scan(&recorded); err != nil || recorded != 1 { t.Fatalf("verdicts=%d err=%v", recorded, err) }
	if got := currentStep(t, s, workID); got != "complete" { t.Fatalf("step=%q", got) }
	if got, active := activeContractState(t, s, workID); got != 1 || active != 1 { t.Fatalf("contract=%d/%d", got, active) }
	var finalBaseline string
	var noEnactment bool
	if err := s.db.QueryRow(`SELECT baseline_json,enactment_json IS NULL AND enactment_effect_seq IS NULL FROM workflow_law_baselines WHERE work_id=?`, workID).Scan(&finalBaseline, &noEnactment); err != nil { t.Fatal(err) }
	if finalBaseline != startBaseline || !noEnactment { t.Fatal("late verdict rewrote its owning start or invented enactment attribution") }
}
