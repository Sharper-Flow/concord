package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkResumePublishedExecutionAbsence(t *testing.T) {
	repo := initLocatorRepo(t)
	s := mustOpenStore(t, filepath.Join(t.TempDir(), "concord.db"))
	seedLocatorAuthority(t, s, repo)
	db := s.DatabaseForTesting()
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1);
		INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash)
		SELECT 'work-published','work_note','Published','2026-08-07T00:00:00Z','shipped','[]','completed',0,'Outcome',project_id,locator_id,'.concord/docs/work/work-published.md','published-commit','published-hash' FROM project_locators WHERE project_id='project-wl' AND kind='canonical_path';
		INSERT INTO archived_work_products(home_project_id,home_locator_id,work_id,product_id) SELECT home_project_id,home_locator_id,id,'product-wl' FROM archived_work WHERE id='work-published';
		INSERT INTO archived_work_projects(home_project_id,home_locator_id,work_id,project_id) SELECT home_project_id,home_locator_id,id,'project-wl' FROM archived_work WHERE id='work-published';
		INSERT INTO linear_issue_links(work_id,remote_issue_uuid,human_key,url,created_at,updated_at) VALUES('work-published','remote-published','EX-30','https://linear.app/example/issue/EX-30','now','now');
		DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"work_id", "issue_key"} {
		t.Run(selector, func(t *testing.T) {
			var before, after int64
			if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			input := map[string]any{"product_id": "product-wl", "project_id": "project-wl", selector: map[string]string{"work_id": "work-published", "issue_key": "EX-30"}[selector]}
			code, _, stderr := resumeIssueCLI(t, s, repo, input)
			if code != workResumeRefusalExit {
				t.Fatalf("resume code=%d stderr=%s", code, stderr)
			}
			for _, text := range []string{"execution state is not retained", ".concord/docs/work/work-published.md", "published-commit", "published-hash"} {
				if !strings.Contains(stderr, text) {
					t.Errorf("resume lost %q: %s", text, stderr)
				}
			}
			if err := db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || after != before {
				t.Fatalf("resume wrote state: %d -> %d, %v", before, after, err)
			}
		})
	}
}
