package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

const knowledgeBodyBenchmarkRows = 1000

func seedKnowledgeBodyBenchmark(t *testing.T) (*Store, KnowledgeHome) {
	t.Helper()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "README.md", "knowledge body benchmark")
	commit := commitKnowledgeRepo(t, repo, "knowledge body benchmark")
	home := KnowledgeHome{HomeProjectID: "body-benchmark-project", HomeLocatorID: "body-benchmark-locator", RepoPath: repo, HeadRef: "HEAD"}
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "body-benchmark-product", home)
	digest, err := knowledgeContentDigest(ctx, home, commit)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < knowledgeBodyBenchmarkRows; i++ {
		id := fmt.Sprintf("body-law-%04d", i)
		hash := "sha256:" + strings.Repeat(fmt.Sprintf("%x", i%16), 64)[:64]
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_subjects(home_project_id,home_locator_id,law_id,kind,status,path,title,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, id, "decision", "accepted", "docs/decisions/"+id+".md", "Storage law", hash, commit); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO law_bodies(home_project_id,home_locator_id,law_id,body,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, id, "benchmark body-only-discovery phrase", hash, commit); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO archived_work(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash,scope_mode) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, "decision", "Storage law", "2026-09-20T00:00:00Z", "accepted", "[]", "completed", 0, "Storage summary", home.HomeProjectID, home.HomeLocatorID, "docs/decisions/"+id+".md", commit, hash, "home"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_kind_coverage(home_project_id,home_locator_id,head_ref,kind,coverage,reason,scanned_commit_oid) VALUES(?,?,?,?,?,?,?)`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef, "decision", "indexed", "benchmark", commit); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_index_watermark(home_project_id,home_locator_id,head_ref,scanned_commit_oid,scanned_content_digest,scanned_at,complete,projection_version) VALUES(?,?,?,?,?,?,1,?)`, home.HomeProjectID, home.HomeLocatorID, home.HeadRef, commit, digest, commit, knowledgeProjectionVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return s, home
}

func TestKnowledgeBodySearchP99At10xDataset(t *testing.T) {
	if productRowSkipPerformanceUnderRace {
		t.Skip("P99 threshold is measured without race instrumentation")
	}
	s, home := seedKnowledgeBodyBenchmark(t)
	defer s.Close()
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := s.QueryQ9(ctx, Q9Request{Text: "body-only-discovery", Kinds: []string{"decision"}, Limit: 100, Home: home}); err != nil {
			t.Fatal(err)
		}
	}
	durations := make([]time.Duration, 100)
	for i := range durations {
		started := time.Now()
		result, err := s.QueryQ9(ctx, Q9Request{Text: "body-only-discovery", Kinds: []string{"decision"}, Limit: 100, Home: home})
		if err != nil || len(result.Items) != 100 {
			t.Fatalf("body search result=%d err=%v", len(result.Items), err)
		}
		durations[i] = time.Since(started)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p99 := durations[(99*len(durations)+99)/100-1]
	if !representativeP99WithinTarget(t, "PM1 Q9 law-body discovery", p99, 100*time.Millisecond, "10x synthetic law-body dataset (1000 rows)", len(durations)) {
		t.Fatalf("PM1 Q9 law-body discovery P99=%s exceeds 100ms target", p99)
	}
}
