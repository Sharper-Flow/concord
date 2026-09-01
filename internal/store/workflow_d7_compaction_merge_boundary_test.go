package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// seedStaleLawCompaction builds a terminal work item that already carries a
// proof-backed canonical note, then supersedes the law revision its active
// contract mandates. The compaction link boundary must refuse against this
// state unless the caller is the closed recovery choice.
//
// The subject is terminal, so the Domain-overlap half of the boundary check has
// an empty footprint by construction. The law-revision half is what refuses.
func seedStaleLawCompaction(t *testing.T) (*Store, KnowledgeHome, string, string) {
	t.Helper()
	s, home, _, commit, path := compactionFixture(t, false)
	seedWorkflowLaw(t, s)
	actor := DeriveWorkflowActorRef("principal:operator", "client:compaction", "agent:operator", "session:compaction")
	statements := []string{
		`INSERT INTO fold_guard(active) VALUES(1)`,
		`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES('` + actor + `','principal:operator','client:compaction','agent:operator','session:compaction','operator','2026-08-19T00:00:00Z')`,
		`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('owner',1,'compaction boundary','internal_sqlite','[]','[]','2026-08-19T00:00:00Z','` + actor + `','["spec:one"]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('owner',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:compaction","immutable_subject_ref":"commit:compaction","expected_result":"pass"}')`,
		`UPDATE law_subjects SET status='superseded' WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`,
		`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','spec','accepted','docs/spec-two.md','Synthetic successor law','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test')`,
		`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES('project','workflow-law-locator','spec:two','supersedes','spec:one','test')`,
		`DELETE FROM fold_guard`,
	}
	for _, statement := range statements {
		if _, err := s.DatabaseForTesting().Exec(statement); err != nil {
			t.Fatalf("seed stale-law compaction (%s): %v", statement, err)
		}
	}
	assertStaleLawCompactionEstablished(t, s)
	return s, home, commit, path
}

// assertStaleLawCompactionEstablished proves the fixture created the refusing
// condition rather than some unrelated failure: the subject is terminal, its
// active contract mandates one law, and that law is superseded by an accepted
// successor.
func assertStaleLawCompactionEstablished(t *testing.T, s *Store) {
	t.Helper()
	var lifecycle, mandate, status, successor string
	db := s.DatabaseForTesting()
	if err := db.QueryRow(`SELECT lifecycle FROM work_items WHERE id='owner'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id='owner' AND superseded_by IS NULL`).Scan(&mandate); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM law_subjects WHERE home_project_id='project' AND home_locator_id='workflow-law-locator' AND law_id='spec:one'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT source_law_id FROM law_relations WHERE kind='supersedes' AND target_law_id='spec:one'`).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" || mandate != `["spec:one"]` || status != "superseded" || successor != "spec:two" {
		t.Fatalf("stale-law compaction not established: lifecycle=%q mandate=%q status=%q successor=%q", lifecycle, mandate, status, successor)
	}
}

func assertStaleLawRefusal(t *testing.T, err error) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("boundary error=%v, want a typed store failure", err)
	}
	if failure.Kind != KindStaleLawRevision {
		t.Fatalf("boundary failure kind=%s, want %s (err=%v)", failure.Kind, KindStaleLawRevision, err)
	}
	if failure.StaleLawRevision == nil {
		t.Fatalf("stale-law refusal carries no typed revision detail: %#v", failure)
	}
	if failure.StaleLawRevision.OldLawID != "spec:one" || failure.StaleLawRevision.AcceptedSuccessorLawID != "spec:two" {
		t.Fatalf("stale-law refusal old=%q successor=%q, want spec:one and spec:two",
			failure.StaleLawRevision.OldLawID, failure.StaleLawRevision.AcceptedSuccessorLawID)
	}
}

// TestCompactionLinkBoundaryRefusesStaleLawRevisionAfterClaim covers the window
// between the claim transaction and the link transaction. A law cutover that
// lands while the external git write is in flight must refuse the link, so
// output authorized under a superseded revision never enters Product truth.
func TestCompactionLinkBoundaryRefusesStaleLawRevisionAfterClaim(t *testing.T) {
	ctx := context.Background()
	s, home, commit, path := seedStaleLawCompaction(t)
	beforeEvents := countRows(t, s, "domain_events")

	err := PublishCompactionLink(ctx, s, compactionRequest(home, commit, path, "stale-law-link", 3))
	assertStaleLawRefusal(t, err)

	if countRows(t, s, "archived_work") != 0 {
		t.Fatal("refused compaction link folded an archived_work row")
	}
	if countRows(t, s, "domain_events") != beforeEvents {
		t.Fatalf("refused compaction link appended events: %d, want %d", countRows(t, s, "domain_events"), beforeEvents)
	}
}

// TestCompactionLinkRecoveryExemptPublishesUnderStaleLawRevision is the control
// for the guard above. Reconcile is the closed recovery choice for an orphaned
// note, so gating it would deadlock the only way out of a pending compaction.
func TestCompactionLinkRecoveryExemptPublishesUnderStaleLawRevision(t *testing.T) {
	ctx := context.Background()
	s, home, commit, path := seedStaleLawCompaction(t)

	request := compactionRequest(home, commit, path, "stale-law-recovery-link", 3)
	request.Boundary = CompactionBoundaryRecoveryExempt
	if err := PublishCompactionLink(ctx, s, request); err != nil {
		t.Fatalf("recovery-exempt compaction link refused under a superseded law revision: %v", err)
	}
	if countRows(t, s, "archived_work") != 1 {
		t.Fatal("recovery-exempt compaction link did not fold the archived_work row")
	}
}

// seedPRMergeConditionOverlap gives one active work item an unresolved Domain
// overlap and an open pr_merge condition, which is the only surface that
// accepts a merge result.
func seedPRMergeConditionOverlap(t *testing.T, workID, otherID string) (*Store, int64) {
	t.Helper()
	s, _, version := seedD7BoundaryOverlap(t, workID, otherID, "execution")
	seedWorkflowAuthority(t, s, "merge-authority-"+workID, workID, "principal/merge", "request/merge", []string{"evidence:merge"})
	added := workflowEvent("merge-condition-added-"+workID, WorkflowConditionAdded, workID, map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"condition_id": "condition:merge", "await_type": "pr_merge", "await_ref": "pr:merge",
		"resolution_authority": "durable_operation:merge-authority-" + workID,
	})
	if err := applyWorkflowTestOperation(context.Background(), s, Operation{Events: []Event{added}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}); err != nil {
		t.Fatal(err)
	}
	return s, version + 1
}

type mergeConditionResolver struct{ calls int }

func (r *mergeConditionResolver) Resolve(_ context.Context, _ ExternalCondition, _ time.Time) (Resolution, error) {
	r.calls++
	return Resolution{ResolutionEvidence: []string{"evidence:merge"}, ResolvedByEvent: "merge-resolution-event", ActorRef: "principal/merge"}, nil
}

// TestResolveWorkflowConditionRefusesMergeUnderUnresolvedOverlap covers the
// CD-0041 D7 merge/ship boundary. Accepting a merge result is a consequential
// mutation, so it revalidates architecture and law inside its own transaction.
func TestResolveWorkflowConditionRefusesMergeUnderUnresolvedOverlap(t *testing.T) {
	ctx := context.Background()
	const workID = "d7-merge-condition"
	const otherID = "d7-merge-condition-other"
	s, version := seedPRMergeConditionOverlap(t, workID, otherID)
	beforeEvents := countRows(t, s, "domain_events")

	resolver := &mergeConditionResolver{}
	err := ResolveWorkflowCondition(ctx, s, workID, "condition:merge", resolver, time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC))
	assertD7TypedRefusal(t, err, workID, otherID)

	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id=? AND condition_id='condition:merge'`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "open" {
		t.Fatalf("refused merge boundary left condition state=%q, want open", state)
	}
	if got := readWorkVersion(t, s, workID); got != version {
		t.Fatalf("refused merge boundary changed work version=%d, want %d", got, version)
	}
	if countRows(t, s, "domain_events") != beforeEvents {
		t.Fatalf("refused merge boundary appended events: %d, want %d", countRows(t, s, "domain_events"), beforeEvents)
	}
}

// TestResolveWorkflowConditionAcceptsMergeWithoutOverlap is the control: the
// same call succeeds once no unresolved overlap exists, so the refusal above is
// attributable to the boundary rather than to the fixture.
func TestResolveWorkflowConditionAcceptsMergeWithoutOverlap(t *testing.T) {
	ctx := context.Background()
	const workID = "d7-merge-clean"
	s := openTemp(t)
	seedWork(t, s, workID)
	seedWorkflowAuthority(t, s, "merge-authority-clean", workID, "principal/merge", "request/merge", []string{"evidence:merge"})
	added := workflowEvent("merge-condition-clean", WorkflowConditionAdded, workID, map[string]any{
		"work_id": workID, "expected_version": 2, "resulting_version": 3,
		"condition_id": "condition:merge", "await_type": "pr_merge", "await_ref": "pr:merge",
		"resolution_authority": "durable_operation:merge-authority-clean",
	})
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{added}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 2}}); err != nil {
		t.Fatal(err)
	}
	resolver := &mergeConditionResolver{}
	if err := ResolveWorkflowCondition(ctx, s, workID, "condition:merge", resolver, time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("merge boundary refused without an overlap: %v", err)
	}
	var state string
	if err := s.DatabaseForTesting().QueryRow(`SELECT condition_state FROM workflow_external_conditions WHERE work_id=? AND condition_id='condition:merge'`, workID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "resolved" {
		t.Fatalf("merge condition state=%q, want resolved", state)
	}
}

// TestResolveWorkflowConditionsAtBoundaryRefusesMergeUnderUnresolvedOverlap
// covers the batch form of the same boundary.
func TestResolveWorkflowConditionsAtBoundaryRefusesMergeUnderUnresolvedOverlap(t *testing.T) {
	ctx := context.Background()
	const workID = "d7-merge-batch"
	const otherID = "d7-merge-batch-other"
	s, _ := seedPRMergeConditionOverlap(t, workID, otherID)
	beforeEvents := countRows(t, s, "domain_events")

	resolver := &mergeConditionResolver{}
	resolved, err := ResolveWorkflowConditionsAtBoundary(ctx, s, workID, resolver, time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC))
	assertD7TypedRefusal(t, err, workID, otherID)
	if resolved != 0 {
		t.Fatalf("refused batch merge boundary resolved %d conditions, want 0", resolved)
	}
	if countRows(t, s, "domain_events") != beforeEvents {
		t.Fatalf("refused batch merge boundary appended events: %d, want %d", countRows(t, s, "domain_events"), beforeEvents)
	}
	if resolver.calls != 0 {
		t.Fatalf("refused batch merge boundary consulted the resolver %d times, want 0", resolver.calls)
	}
}
