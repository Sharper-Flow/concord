package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// seedStaleLawCompaction builds a terminal work item that already carries a
// proof-backed canonical note, then deletes the law revision its active
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
		`INSERT INTO write_guard(active) VALUES(1)`,
		`INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES('` + actor + `','principal:operator','client:compaction','agent:operator','session:compaction','operator','2026-08-19T00:00:00Z')`,
		`INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) VALUES('owner',1,'compaction boundary','internal_sqlite','[]','[]','2026-08-19T00:00:00Z','` + actor + `','["spec:one"]','[]',1,'prototype_internal'); INSERT INTO workflow_contract_predicates(work_id,contract_version,predicate_id,ordinal,outcome_kind,outcome_payload) VALUES('owner',1,'predicate:primary',0,'check','{"kind":"check","check_ref":"check:compaction","immutable_subject_ref":"commit:compaction","expected_result":"pass"}')`,
		`INSERT INTO workflow_contract_law_revisions(work_id,contract_version,law_id,content_hash) VALUES('owner',1,'spec:one','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`,
		`DELETE FROM write_guard`,
	}
	for _, statement := range statements {
		if _, err := s.DatabaseForTesting().Exec(statement); err != nil {
			t.Fatalf("seed stale-law compaction (%s): %v", statement, err)
		}
	}
	// A law retires by deleting its committed file, the ordinary pull-request
	// route: the workflow law home no longer carries spec:one.
	lawHome := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "workflow-law-locator", RepoPath: workflowLawFixtureRepo(t, s), HeadRef: "HEAD"}
	removeCommittedFixtureLaw(t, lawHome)
	assertStaleLawCompactionEstablished(t, s, lawHome)
	return s, home, commit, path
}

// assertStaleLawCompactionEstablished proves the fixture created the refusing
// condition rather than some unrelated failure: the subject is terminal, its
// active contract pins one law, and the committed law folder no longer
// carries that law.
func assertStaleLawCompactionEstablished(t *testing.T, s *Store, lawHome KnowledgeHome) {
	t.Helper()
	var lifecycle, mandate, pinnedHash string
	db := s.DatabaseForTesting()
	if err := db.QueryRow(`SELECT lifecycle FROM work_items WHERE id='owner'`).Scan(&lifecycle); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT spec_mandate FROM workflow_contracts WHERE work_id='owner' AND superseded_by IS NULL`).Scan(&mandate); err != nil {
		t.Fatal(err)
	}
	snapshot, err := loadLawFolderSnapshotAtHome(context.Background(), lawHome)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(snapshot.LawIDs(), "spec:one") {
		t.Fatalf("stale-law compaction not established: spec:one still committed at %s", lawHome.RepoPath)
	}
	if err := db.QueryRow(`SELECT content_hash FROM workflow_contract_law_revisions WHERE work_id='owner' AND contract_version=1 AND law_id='spec:one'`).Scan(&pinnedHash); err != nil {
		t.Fatal(err)
	}
	if lifecycle != "completed" || mandate != `["spec:one"]` || pinnedHash != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("stale-law compaction not established: lifecycle=%q mandate=%q pin=%q", lifecycle, mandate, pinnedHash)
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
	if failure.StaleLawRevision.OldLawID != "spec:one" || failure.StaleLawRevision.OldContentHash != "sha256:"+strings.Repeat("a", 64) || strings.Join(failure.StaleLawRevision.RecoveryActions, ",") != "supersede_contract,terminal_work" {
		t.Fatalf("stale-law refusal = %+v, want deleted spec:one, its pinned hash, and closed recovery actions", failure.StaleLawRevision)
	}
}

// TestCompactionLinkBoundaryRefusesStaleLawRevisionAfterClaim covers the window
// between the claim transaction and the link transaction. A law cutover that
// lands while the external git write is in flight must refuse the link, so
// output authorized under a deleted revision never enters Product truth.
func TestCompactionLinkBoundaryRefusesStaleLawRevisionAfterClaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, commit, path := seedStaleLawCompaction(t)
	beforeEvents := countRows(t, s, "effect_receipts")

	err := PublishCompactionLink(ctx, s, compactionRequest(home, commit, path, "stale-law-link"))
	assertStaleLawRefusal(t, err)

	if countRows(t, s, "archived_work") != 0 {
		t.Fatal("refused compaction link folded an archived_work row")
	}
	if countRows(t, s, "effect_receipts") != beforeEvents {
		t.Fatalf("refused compaction link appended effects: %d, want %d", countRows(t, s, "effect_receipts"), beforeEvents)
	}
}

// TestCompactionLinkRecoveryExemptPublishesUnderStaleLawRevision is the control
// for the guard above. Reconcile is the closed recovery choice for an orphaned
// note, so gating it would deadlock the only way out of a pending compaction.
func TestCompactionLinkRecoveryExemptPublishesUnderStaleLawRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, commit, path := seedStaleLawCompaction(t)

	request := compactionRequest(home, commit, path, "stale-law-recovery-link")
	request.Boundary = CompactionBoundaryRecoveryExempt
	if err := PublishCompactionLink(ctx, s, request); err != nil {
		t.Fatalf("recovery-exempt compaction link refused under a deleted law revision: %v", err)
	}
	if countRows(t, s, "archived_work") != 1 {
		t.Fatal("recovery-exempt compaction link did not fold the archived_work row")
	}
}

// TestCompactionLinkRecoveryExemptPublishesWithUnverifiableLawHome keeps the
// recovery exemption whole: a law folder that no longer loads must not refuse
// the reconcile link, which never consults law, while the revalidated link
// still refuses on that law home.
func TestCompactionLinkRecoveryExemptPublishesWithUnverifiableLawHome(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, commit, path := seedStaleLawCompaction(t)
	lawRepo := workflowLawFixtureRepo(t, s)
	writeKnowledgeFile(t, lawRepo, ".concord/law/decisions/CD-0999-broken.md", "# A law file without metadata\n")
	commitKnowledgeRepo(t, lawRepo, "commit a law file whose metadata does not load")
	if _, err := s.EstablishKnowledgeSourceSetProof(ctx, "owner"); err == nil {
		t.Fatal("fixture law home still verifies; the unverifiable condition was not established")
	}

	if err := PublishCompactionLink(ctx, s, compactionRequest(home, commit, path, "unverifiable-law-link")); err == nil {
		t.Fatal("revalidated compaction link admitted an unverifiable law home")
	}
	request := compactionRequest(home, commit, path, "unverifiable-law-recovery-link")
	request.Boundary = CompactionBoundaryRecoveryExempt
	if err := PublishCompactionLink(ctx, s, request); err != nil {
		t.Fatalf("recovery-exempt compaction link refused for a law home it never checks: %v", err)
	}
	if countRows(t, s, "archived_work") != 1 {
		t.Fatal("recovery-exempt compaction link did not fold the archived_work row")
	}
}
