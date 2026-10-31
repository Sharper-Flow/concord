package store

import (
	"context"
	"testing"
)

// Issue #765: CD-0036 D2 — a same-ID accepted hash change is a compatible
// amendment, surfaced with both hashes; supersession belongs to stale_law_revision.
// The amendment is a real committed revision of the fixture law: the pin
// records the hash the contract approved, and the read compares it against
// the hash the verified snapshot now carries.
func TestContinuityRepinsCompatibleLawAmendments(t *testing.T) {
	t.Parallel()

	s := openTemp(t)
	actor, _ := continuityTestWorkflow(t, s, "continuity-law-amendment")
	seedWorkflowLaw(t, s)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	pinnedHash := workflowFixtureLawPin(t, home.RepoPath, "spec:one")
	actorRef, err := WorkflowActorRef(actor)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(query string, args ...any) {
		t.Helper()
		if _, err := s.DatabaseForTesting().Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO write_guard(active) VALUES(1)`)
	seed(`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,spec_mandate,law_modifies,law_boundary_version,rigor_class,approved_at,approved_by) VALUES('continuity-law-amendment',1,'law premise','internal_sqlite','[]','[]','["spec:one"]','[]',1,'prototype_internal','now',?)`, actorRef)
	seed(`INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('continuity-law-amendment',1,'predicate:law',0,'check','{"kind":"check"}')`)
	seed(`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES('continuity-law-amendment',1,'spec:one',?)`, pinnedHash)
	seed(`DELETE FROM write_guard`)
	commitFixtureLawFile(t, home, "spec:one", "---\n"+
		`{"id":"spec:one","title":"Fixture folder law spec:one","authority":{"tier":"derived"},"home_domain_id":"root","applies_to_domain_ids":[]}`+"\n"+
		"---\n\nSynthetic fixture law body, amended in place.\n")
	currentHash := workflowFixtureLawPin(t, home.RepoPath, "spec:one")
	snapshot, err := ReadWorkflowContinuity(context.Background(), s, ContinuityRequest{Work: "continuity-law-amendment", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StaleLawRevision != nil || len(snapshot.CompatibleLawAmendments) != 1 {
		t.Fatalf("stale=%+v amendments=%+v", snapshot.StaleLawRevision, snapshot.CompatibleLawAmendments)
	}
	amendment := snapshot.CompatibleLawAmendments[0]
	if amendment.LawID != "spec:one" || amendment.PinnedHash != pinnedHash || amendment.CurrentHash != currentHash {
		t.Fatalf("amendment=%+v", amendment)
	}
}
