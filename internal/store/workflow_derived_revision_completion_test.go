package store

import (
	"encoding/json"
	"testing"
)

// derivedRevisionConflictLaw is the committed spec:one revision whose record
// declares the conflicts_with relation against the legislated fixture law:
// the declarer is whichever record carries the relation.
const derivedRevisionConflictLaw = "---\n" +
	`{"id":"spec:one","title":"Fixture folder law spec:one","authority":{"tier":"derived"},"home_domain_id":"root","applies_to_domain_ids":[],"law_relations":[{"kind":"conflicts_with","target_id":"spec:legislated"}]}` + "\n" +
	"---\n\nSynthetic fixture law body.\n"

func TestCompletionDoesNotRecheckMandatedLaw(t *testing.T) {
	t.Parallel()
	const workID = "derived-revision-lands"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	seedDerivedLawExtension(t, s)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	commitFixtureLawFile(t, home, "spec:one", derivedRevisionConflictLaw)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/researcher", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	version := seedDerivedRevisionWorkflow(t, s, workID, owner)
	approval := json.RawMessage(`{"premise":"Bring the derived law into conformance with the legislated one.","spec_mandate":["spec:legislated","spec:one"],"law_modifies":["spec:one"],"required_evidence":["artifact"],"outcome_predicates":[{"predicate_id":"predicate:primary","ordinal":0,"outcome_kind":"outcome","outcome_payload":{"kind":"outcome","allowed":["report_recorded"]}}]}`)
	if err := runDerivedLawApproval(t, s, workID, version, owner, approval); err != nil {
		t.Fatalf("the derived revision was not admitted at approval: %v", err)
	}
	// Completion is plan-gated, not gated on proof or premise confirmation.
	completeWorkflowPlanFixture(t, s, workID)
	// Completion reads no law: an unreadable mandated law cannot refuse it.
	commitFixtureLawFile(t, home, "spec:one", "---\nnot json\n---\n\nbroken\n")
	if err := runVerdictActionAs(t, s, workID, "complete", json.RawMessage(`{"impact_verdict":"non-breaking"}`), 0, owner); err != nil {
		t.Fatalf("completion rechecked mandated law: %v", err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT instance_state FROM workflow_instances WHERE work_id=?`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "completed" {
		t.Fatalf("instance_state=%s, want completed", state)
	}
	var contracts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=?`, workID).Scan(&contracts); err != nil {
		t.Fatal(err)
	}
	if contracts != 1 {
		t.Fatalf("the landed revision needed %d contracts, want the one approval", contracts)
	}
}
