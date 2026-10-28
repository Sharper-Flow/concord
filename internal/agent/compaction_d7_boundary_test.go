package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// deleteCompactionMandatedLaw leaves work-cancelled pinning a law deleted from
// its resolved knowledge home. The claim must refuse before any Git note write.
func deleteCompactionMandatedLaw(t *testing.T, s *store.Store, home store.KnowledgeHome) {
	t.Helper()
	actor := store.DeriveWorkflowActorRef("principal:operator", "client:compaction", "agent:operator", "session:compaction")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO write_guard(active) VALUES(1)`, nil},
		{`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,'principal:operator','client:compaction','agent:operator','session:compaction','operator','2026-08-19T00:00:00Z')`, []any{actor}},
		{`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('work-cancelled',1,'compaction claim boundary','internal_sqlite','[]','[]','2026-08-19T00:00:00Z',?,'["spec:one"]','[]',1,'prototype_internal')`, []any{actor}},
		{`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES('work-cancelled',1,'spec:one','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`, nil},
		{`DELETE FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id='spec:one'`, []any{home.HomeProjectID, home.HomeLocatorID}},
		{`DELETE FROM write_guard`, nil},
	}
	for _, statement := range statements {
		if _, err := s.DatabaseForTesting().Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("delete mandated law (%s): %v", statement.query, err)
		}
	}
}

// TestCompactionPublishClaimRefusesStaleLawRevision covers the CD-0222 D7 claim
// boundary for compaction publication. The refusal must land at the claim, so
// no canonical note reaches the git knowledge home and no durable operation is
// recorded.
func TestCompactionPublishClaimRefusesStaleLawRevision(t *testing.T) {
	t.Parallel()
	s, service, grant, privateKey, home := agentJobsCompactionFixture(t)
	deleteCompactionMandatedLaw(t, s, home)

	var operationsBefore, eventsBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations`).Scan(&operationsBefore); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}

	response := publishCancelledNote(t, s, service, grant, privateKey, home)

	if response.Outcome != agentcontract.OutcomeError || response.Error == nil {
		t.Fatalf("compaction publish outcome=%v error=%+v, want a refusal", response.Outcome, response.Error)
	}
	if response.Error.Kind != "stale_law_revision" {
		t.Fatalf("claim refusal kind=%q, want stale_law_revision (error=%+v)", response.Error.Kind, response.Error)
	}
	if response.Error.StaleLawRevision == nil {
		t.Fatal("claim refusal carries no typed stale_law_revision detail")
	}
	if response.Error.StaleLawRevision.OldLawID != "spec:one" || response.Error.StaleLawRevision.OldContentHash != observationBoundaryOldHash {
		t.Fatalf("claim refusal old=%q hash=%q, want the deleted law and its pin",
			response.Error.StaleLawRevision.OldLawID, response.Error.StaleLawRevision.OldContentHash)
	}

	if archivedCancelledWorkCount(t, s) != 0 {
		t.Fatal("refused compaction claim recorded a canonical locator")
	}
	var operationsAfter, eventsAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM durable_operations`).Scan(&operationsAfter); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if operationsAfter != operationsBefore {
		t.Fatalf("refused compaction claim recorded a durable operation: %d, want %d", operationsAfter, operationsBefore)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("refused compaction claim appended events: %d, want %d", eventsAfter, eventsBefore)
	}
}
