package store

import (
	"strings"
	"testing"
	"time"
)

func TestTerminalCleanupRetiresUnreadKnowledgeTables(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	for _, table := range []string{"archived_work_components", "compaction_publications"} {
		var count int
		if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("retired knowledge table %s still exists", table)
		}
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='workflow_backlog_alignment'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("required alignment table count=%d, err=%v", count, err)
	}
}

func TestTerminalCleanupPublishedIdentityCannotBeCapturedAgain(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, ".concord/docs/work/closed-work.md", canonicalWorkNote("closed-work", "2026-08-07T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "canonical work outcome")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	seedKnowledgeWork(t, s, "live-work", "Live")
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	capture := func(id string) error {
		return ApplyOperation(t.Context(), s, Operation{Events: []Event{
			{EventID: "capture-" + id, Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: workCreatedCurrentVersion(), Payload: []byte(`{"work_kind":"task","title":"Capture","priority":0}`)},
			{EventID: "capture-project-" + id, Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"` + id + `","project_id":"proj-web","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 0}})
	}
	if err := capture("fresh-work"); err != nil {
		t.Fatalf("control capture of an unpublished identity failed: %v", err)
	}
	assertFailureKind(t, capture("closed-work"), KindProjectionNotFound)
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM work_items WHERE id='closed-work'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("published work was recreated: count=%d, err=%v", count, err)
	}
}

func TestTerminalCleanupLegacyComponentsDoNotBecomeDomains(t *testing.T) {
	t.Parallel()
	repo := initKnowledgeRepo(t)
	content := strings.Replace(canonicalWorkNote("legacy-work", "2026-08-07T00:00:00Z"), "domain_ids: [auth]", "component_ids: [auth]", 1)
	writeKnowledgeFile(t, repo, ".concord/docs/work/legacy-work.md", content)
	commitKnowledgeRepo(t, repo, "canonical work outcome")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	if err := s.RebuildKnowledgeIndex(t.Context(), home); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM archived_work_domains WHERE work_id='legacy-work'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy components became Domain scope: count=%d, err=%v", count, err)
	}
}
