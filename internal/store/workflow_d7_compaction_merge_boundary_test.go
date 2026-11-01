package store

import (
	"context"
	"slices"
	"testing"
)

func seedDeletedLawCompaction(t *testing.T) (*Store, KnowledgeHome, string, string) {
	t.Helper()
	s, home, _, commit, path := compactionFixture(t, false)
	seedWorkflowLaw(t, s)
	actor := DeriveWorkflowActorRef("principal:operator", "client:compaction", "agent:operator", "session:compaction")
	for _, statement := range []string{
		`INSERT INTO write_guard(active) VALUES(1)`,
		`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES('` + actor + `','principal:operator','client:compaction','agent:operator','session:compaction','operator','2026-08-19T00:00:00Z')`,
		`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('owner',1,'compaction boundary','internal_sqlite','[]','[]','2026-08-19T00:00:00Z','` + actor + `','["spec:one"]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('owner',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:compaction","immutable_subject_ref":"commit:compaction","expected_result":"pass"}')`,
		`DELETE FROM write_guard`,
	} {
		if _, err := s.db.Exec(statement); err != nil { t.Fatal(err) }
	}
	lawHome := lawChangeFixtureHome(t, s)
	removeCommittedFixtureLaw(t, lawHome)
	snapshot, err := loadLawFolderSnapshotAtHome(t.Context(), lawHome)
	if err != nil { t.Fatal(err) }
	if slices.Contains(snapshot.LawIDs(), "spec:one") { t.Fatal("fixture law was not deleted") }
	var lifecycle, mandate string
	if err := s.db.QueryRow(`SELECT w.lifecycle,c.spec_mandate FROM work_items w JOIN workflow_contracts c ON c.work_id=w.id WHERE w.id='owner' AND c.superseded_by IS NULL`).Scan(&lifecycle, &mandate); err != nil || lifecycle != "completed" || mandate != `["spec:one"]` { t.Fatalf("lifecycle=%q mandate=%q err=%v", lifecycle, mandate, err) }
	return s, home, commit, path
}

func TestCompactionLinkPublishesAfterLawDeletion(t *testing.T) {
	t.Parallel()
	s, home, commit, path := seedDeletedLawCompaction(t)
	request := compactionRequest(home, commit, path, "deleted-law-link")
	if err := PublishCompactionLink(context.Background(), s, request); err != nil { t.Fatal(err) }
	if countRows(t, s, "archived_work") != 1 { t.Fatal("link did not fold archived_work") }
}

func TestCompactionLinkPublishesWithoutLawHomeProof(t *testing.T) {
	t.Parallel()
	s, home, commit, path := seedDeletedLawCompaction(t)
	lawRepo := workflowLawFixtureRepo(t, s)
	writeKnowledgeFile(t, lawRepo, ".concord/law/decisions/CD-0999-broken.md", "# A law file without metadata\n")
	commitKnowledgeRepo(t, lawRepo, "commit a law file whose metadata does not load")
	if _, err := s.EstablishKnowledgeSourceSetProof(t.Context(), "owner"); err == nil { t.Fatal("fixture law home still verifies") }
	if err := PublishCompactionLink(t.Context(), s, compactionRequest(home, commit, path, "unverifiable-law-link")); err != nil { t.Fatal(err) }
	if countRows(t, s, "archived_work") != 1 { t.Fatal("link did not fold archived_work") }
}
