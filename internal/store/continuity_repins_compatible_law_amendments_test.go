package store

import (
	"context"
	"strings"
	"testing"
)

// Issue #765: CD-0036 D2 — a same-ID accepted hash change is a compatible
// amendment, surfaced with both hashes; supersession belongs to stale_law_revision.
func TestContinuityRepinsCompatibleLawAmendments(t *testing.T) {

	s := openTemp(t)
	actor, _ := continuityTestWorkflow(t, s, "continuity-law-amendment")
	seedWorkflowLaw(t, s)
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,spec_mandate,law_modifies,law_boundary_version,rigor_class,approved_at,approved_by) VALUES('continuity-law-amendment',1,'law premise','internal_sqlite','[]','[]','["spec:one"]','[]',1,'prototype_internal','now',?); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('continuity-law-amendment',1,'predicate:law',0,'check','{"kind":"check"}'); INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES('continuity-law-amendment',1,'spec:one','sha256:`+strings.Repeat("a", 64)+`'); DELETE FROM fold_guard`, actorRef); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE law_subjects SET content_hash=? WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'; DELETE FROM fold_guard`, "sha256:"+strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "continuity-law-amendment", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StaleLawRevision != nil || len(snapshot.CompatibleLawAmendments) != 1 {
		t.Fatalf("stale=%+v amendments=%+v", snapshot.StaleLawRevision, snapshot.CompatibleLawAmendments)
	}
	amendment := snapshot.CompatibleLawAmendments[0]
	if amendment.LawID != "spec:one" || amendment.PinnedHash != "sha256:"+strings.Repeat("a", 64) || amendment.CurrentHash != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("amendment=%+v", amendment)
	}
}
