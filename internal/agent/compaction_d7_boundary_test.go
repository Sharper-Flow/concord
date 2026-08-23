package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// supersedeCompactionMandatedLaw gives work-cancelled an active contract that
// mandates one law, then supersedes that law in the Git-derived projection its
// resolved knowledge home owns. The compaction claim must refuse against this
// state before any git note is written.
func supersedeCompactionMandatedLaw(t *testing.T, s *store.Store, home store.KnowledgeHome) {
	t.Helper()
	actor := store.DeriveWorkflowActorRef("principal:operator", "client:compaction", "agent:operator", "session:compaction")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO fold_guard(active) VALUES(1)`, nil},
		{`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal:operator','client:compaction','agent:operator','session:compaction','operator','2026-08-19T00:00:00Z')`, []any{actor}},
		{`INSERT INTO workflow_contracts(work_id,contract_version,premise,outcome_kind,outcome_payload,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-cancelled',1,'compaction claim boundary','check','{"kind":"check","check_ref":"check:compaction","immutable_subject_ref":"commit:compaction","expected_result":"pass"}','internal_sqlite','[]','[]','2026-08-19T00:00:00Z',?,'["spec:one"]','[]',1,'prototype_internal')`, []any{actor}},
		{`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,'spec:one','spec','superseded','docs/spec.md','Synthetic test law','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','test')`, []any{home.HomeProjectID, home.HomeLocatorID}},
		{`INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,'spec:two','spec','accepted','docs/spec-two.md','Synthetic successor law','sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','test')`, []any{home.HomeProjectID, home.HomeLocatorID}},
		{`INSERT INTO law_relations(home_project_id,home_locator_id,source_law_id,kind,target_law_id,scanned_commit_oid) VALUES(?,?,'spec:two','supersedes','spec:one','test')`, []any{home.HomeProjectID, home.HomeLocatorID}},
		{`DELETE FROM fold_guard`, nil},
	}
	for _, statement := range statements {
		if _, err := s.DatabaseForTesting().Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("supersede mandated law (%s): %v", statement.query, err)
		}
	}
}

// TestCompactionPublishClaimRefusesStaleLawRevision covers the CD-0041 D7 claim
// boundary for compaction publication. The refusal must land at the claim, so
// no canonical note reaches the git knowledge home and no durable operation is
// recorded.
func TestCompactionPublishClaimRefusesStaleLawRevision(t *testing.T) {
	s, service, grant, privateKey, home := agentJobsCompactionFixture(t)
	supersedeCompactionMandatedLaw(t, s, home)

	var operationsBefore, eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations`).Scan(&operationsBefore); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}

	response := publishCancelledNote(t, s, service, grant, privateKey, home)

	if response.Outcome != OutcomeError || response.Error == nil {
		t.Fatalf("compaction publish outcome=%v error=%+v, want a refusal", response.Outcome, response.Error)
	}
	if response.Error.Kind != "stale_law_revision" {
		t.Fatalf("claim refusal kind=%q, want stale_law_revision (error=%+v)", response.Error.Kind, response.Error)
	}
	if response.Error.StaleLawRevision == nil {
		t.Fatal("claim refusal carries no typed stale_law_revision detail")
	}
	if response.Error.StaleLawRevision.OldLawID != "spec:one" || response.Error.StaleLawRevision.AcceptedSuccessorLawID != "spec:two" {
		t.Fatalf("claim refusal old=%q successor=%q, want spec:one and spec:two",
			response.Error.StaleLawRevision.OldLawID, response.Error.StaleLawRevision.AcceptedSuccessorLawID)
	}

	if archivedWorkCount(t, s, "work-cancelled") != 0 {
		t.Fatal("refused compaction claim recorded a canonical locator")
	}
	var operationsAfter, eventsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations`).Scan(&operationsAfter); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if operationsAfter != operationsBefore {
		t.Fatalf("refused compaction claim recorded a durable operation: %d, want %d", operationsAfter, operationsBefore)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("refused compaction claim appended events: %d, want %d", eventsAfter, eventsBefore)
	}
}
