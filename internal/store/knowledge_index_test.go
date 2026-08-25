package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVerifyCommittedNoteIgnoresWorkingTreeEdits(t *testing.T) {
	repo := initKnowledgeRepo(t)
	path := "docs/work/2026-08-07-proof-work.md"
	content := canonicalWorkNote("work-proof", "2026-08-07T00:00:00Z")
	writeKnowledgeFile(t, repo, path, content)
	commit := commitKnowledgeRepo(t, repo, "proof")
	hash := sha256.Sum256([]byte(content))

	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(path)), []byte("---\nconcord_work_id: work-proof\n---\nworking tree only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyCommittedNote(context.Background(), repo, commit, path, "sha256:"+hex.EncodeToString(hash[:]))
	if err != nil {
		t.Fatal(err)
	}
	if string(verified.Content) != content {
		t.Fatalf("verified bytes changed with working-tree edit: %q", verified.Content)
	}
}

func TestVerifyCommittedNoteRejectsHashMismatchAndSymlink(t *testing.T) {
	repo := initKnowledgeRepo(t)
	path := "docs/lessons/state.md"
	content := canonicalKnowledgeNote("lesson-state", "lesson", "2026-08-07T00:00:00Z", []string{"sqlite"})
	writeKnowledgeFile(t, repo, path, content)
	commit := commitKnowledgeRepo(t, repo, "regular note")
	_, err := VerifyCommittedNote(context.Background(), repo, commit, path, "sha256:"+strings.Repeat("0", 64))
	assertFailureKind(t, err, KindInvalidNoteProof)

	runKnowledgeGit(t, repo, "checkout", "--quiet", "--", ".")
	if err := os.Remove(filepath.Join(repo, filepath.FromSlash(path))); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../lessons/target.md", filepath.Join(repo, filepath.FromSlash(path))); err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, "docs/lessons/target.md", content)
	commit = commitKnowledgeRepo(t, repo, "symlink note")
	_, err = VerifyCommittedNote(context.Background(), repo, commit, path, "")
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestVerifyCommittedNoteRejectsUnsafeAndNonBlobPaths(t *testing.T) {
	ctx := context.Background()
	for _, path := range []string{"../docs/work/note.md", "/docs/work/note.md", "-docs/work/note.md", "docs/other/note.md", "docs/work/note.txt"} {
		_, err := VerifyCommittedNote(ctx, t.TempDir(), strings.Repeat("a", 40), path, "")
		var failure *Failure
		if !errors.As(err, &failure) || failure.Kind != KindInvalidNoteProof {
			t.Fatalf("path %q error = %v, want invalid note proof", path, err)
		}
	}

	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "docs/lessons/seed.md", canonicalKnowledgeNote("seed", "lesson", "2026-08-07T00:00:00Z", []string{"seed"}))
	commit := commitKnowledgeRepo(t, repo, "empty")
	_, err := VerifyCommittedNote(ctx, repo, commit, "docs/work/missing.md", "")
	assertFailureKind(t, err, KindInvalidNoteProof)
}

func TestFindVerifiedWorkNoteDiscoversOrphansWithoutCreatingNotes(t *testing.T) {
	repo := initKnowledgeRepo(t)
	path := "docs/work/2026-08-07-orphan.md"
	writeKnowledgeFile(t, repo, path, canonicalWorkNote("orphan-work", "2026-08-07T00:00:00Z"))
	commit := commitKnowledgeRepo(t, repo, "orphan note")
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	note, err := FindVerifiedWorkNote(context.Background(), home, "orphan-work", "")
	if err != nil || note.ID != "orphan-work" || note.NotePath != path {
		t.Fatalf("orphan discovery note=%#v err=%v", note, err)
	}
	if _, err := FindVerifiedWorkNote(context.Background(), home, "absent-work", ""); err == nil {
		t.Fatal("absent orphan was accepted")
	} else {
		assertFailureKind(t, err, KindKnowledgeMissing)
	}
	if _, err := FindVerifiedWorkNote(context.Background(), home, "orphan-work", "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("hash-mismatched orphan was accepted")
	} else {
		assertFailureKind(t, err, KindInvalidNoteProof)
	}
	writeKnowledgeFile(t, repo, "docs/work/2026-08-08-orphan-copy.md", canonicalWorkNote("orphan-work", "2026-08-08T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "competing orphan")
	if _, err := FindVerifiedWorkNote(context.Background(), home, "orphan-work", ""); err == nil {
		t.Fatal("competing orphan locator was silently selected")
	} else {
		assertFailureKind(t, err, KindKnowledgeAmbiguous)
	}
	_ = commit
}

// TestPublishCanonicalNoteCommitsOneNote covers the git half of the publication
// seam. Publication commits the note and returns an unverified proof; verifying
// that proof is the caller's separately ordered step, so this asserts the commit
// and then verifies it explicitly, in the order the contract requires.
func TestPublishCanonicalNoteCommitsOneNote(t *testing.T) {
	repo := initKnowledgeRepo(t)
	content := canonicalWorkNote("publish-work", "2026-08-07T00:00:00Z")
	sum := sha256.Sum256([]byte(content))
	hash := "sha256:" + hex.EncodeToString(sum[:])
	note, err := PublishCanonicalNote(context.Background(), KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}, "publish-work", content, hash)
	if err != nil || note.CommitOID == "" || note.NotePath == "" {
		t.Fatalf("published note=%#v err=%v", note, err)
	}
	verified, err := VerifyCommittedNote(context.Background(), repo, note.CommitOID, note.NotePath, hash)
	if err != nil || verified.ID != "publish-work" {
		t.Fatalf("verified note=%#v err=%v", verified, err)
	}
	repeat, err := PublishCanonicalNote(context.Background(), KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}, "publish-work", content, hash)
	if err != nil || repeat.CommitOID != note.CommitOID || repeat.NotePath != note.NotePath {
		t.Fatalf("repeat publication created a second note: first=%#v repeat=%#v err=%v", note, repeat, err)
	}
}

func TestRunGitBoundsCommandOutput(t *testing.T) {
	repo := initKnowledgeRepo(t)
	largePath := "docs/work/large.md"
	writeKnowledgeFile(t, repo, largePath, strings.Repeat("x", maxGitOutput+1))
	commit := commitKnowledgeRepo(t, repo, "large blob")
	if _, err := runGit(context.Background(), repo, "cat-file", "blob", commit+":"+largePath); err == nil || !strings.Contains(err.Error(), "output exceeds") {
		t.Fatalf("runGit large output error = %v, want bounded-output failure", err)
	}
}

func TestParseKnowledgeNoteRejectsAmbiguousAndMalformedCriticalMetadata(t *testing.T) {
	valid := canonicalWorkNote("work-proof", "2026-08-07T00:00:00Z")
	for name, content := range map[string]string{
		"duplicate identity": strings.Replace(valid, "concord_work_id: work-proof\n", "concord_work_id: work-proof\nid: another-work\n", 1),
		"duplicate type":     strings.Replace(valid, "work_type: implementation\n", "work_type: implementation\ntype: work_note\n", 1),
		"unclosed quote":     strings.Replace(valid, "title: Auth release", "title: \"Auth release", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseKnowledgeNote([]byte(content))
			assertFailureKind(t, err, KindInvalidNoteProof)
		})
	}
}

func TestKnowledgeNoteDomainScopeCompatibilityAndWriteBoundary(t *testing.T) {
	legacy := strings.Replace(canonicalWorkNote("legacy-scope", "2026-08-07T00:00:00Z"), "domain_ids: [auth]", "component_ids: [auth]", 1)
	note, err := parseKnowledgeNote([]byte(legacy))
	if err != nil || !note.HasComponentIDs || note.HasDomainIDs || !reflect.DeepEqual(note.ComponentIDs, []string{"auth"}) {
		t.Fatalf("legacy note = %#v, err %v", note, err)
	}
	empty := strings.Replace(canonicalWorkNote("empty-scope", "2026-08-07T00:00:00Z"), "domain_ids: [auth]", "domain_ids: []", 1)
	note, err = parseKnowledgeNote([]byte(empty))
	if err != nil || !note.HasDomainIDs || len(note.DomainIDs) != 0 {
		t.Fatalf("empty Domain scope = %#v, err %v", note, err)
	}
	both := strings.Replace(legacy, "component_ids: [auth]", "component_ids: [auth]\ndomain_ids: [auth]", 1)
	if _, err := parseKnowledgeNote([]byte(both)); err == nil {
		t.Fatal("expected mixed component/domain front matter to be rejected")
	}
	repo := initKnowledgeRepo(t)
	if _, err := PublishCanonicalNote(context.Background(), KnowledgeHome{RepoPath: repo}, "legacy-scope", legacy, ""); err == nil {
		t.Fatal("expected new note publication to reject component_ids")
	}
	missing := strings.Replace(canonicalWorkNote("missing-domain-scope", "2026-08-07T00:00:00Z"), "domain_ids: [auth]\n", "", 1)
	if _, err := PublishCanonicalNote(context.Background(), KnowledgeHome{RepoPath: repo}, "missing-domain-scope", missing, ""); err == nil {
		t.Fatal("expected new note publication to require domain_ids")
	}
}

func TestCompactionPayloadRequiresExplicitUniqueScopeArrays(t *testing.T) {
	valid := compactionLinkPayload{LessonTags: []string{}, ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}}
	if err := validateCompactionScopeArrays(valid); err != nil {
		t.Fatalf("valid empty scopes rejected: %v", err)
	}
	missing := valid
	missing.DomainIDs = nil
	if err := validateCompactionScopeArrays(missing); err == nil {
		t.Fatal("missing domain_ids accepted")
	}
	duplicate := valid
	duplicate.DomainIDs = []string{"auth", "auth"}
	if err := validateCompactionScopeArrays(duplicate); err == nil {
		t.Fatal("duplicate domain_ids accepted")
	}
}

func TestUpcastCompactionLinkPublishedV1PreservesLegacyBytesAndOrder(t *testing.T) {
	original, err := json.Marshal(compactionLinkPayloadV1{
		ID: "work", Type: "work_note", Title: "Title", CompletedAt: "2026-08-07T00:00:00Z", OutcomeTag: "shipped",
		LessonTags: []string{}, TerminalState: "completed", Summary: "Summary", ProductIDs: []string{}, ProjectIDs: []string{},
		ComponentIDs: []string{"z-domain", "a-domain"}, TagIDs: []string{}, HomeProjectID: "project", HomeLocatorID: "locator",
		NotePath: "docs/work/note.md", CommitOID: strings.Repeat("a", 40), ContentHash: "sha256:" + strings.Repeat("b", 64), Reason: "test", ExpectedVersion: 1, ResultingVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{PayloadVersion: 1, Payload: original}
	upcasted, err := upcastCompactionLinkPublishedV1(event)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(event.Payload, original) || upcasted.PayloadVersion != 2 {
		t.Fatal("upcast mutated the source event or did not advance version")
	}
	var current compactionLinkPayload
	if err := json.Unmarshal(upcasted.Payload, &current); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current.DomainIDs, []string{"z-domain", "a-domain"}) || strings.Contains(string(upcasted.Payload), "component_ids") {
		t.Fatalf("upcast payload = %s", upcasted.Payload)
	}
}

func TestRebuildKnowledgeIndexAndQ9Q10UseCurrentGitHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	workPath := "docs/work/2026-08-03-auth-release.md"
	lessonPath := "docs/lessons/2026-08-04-state-authority.md"
	decisionPath := "docs/decisions/CD-0002-state-authority.md"
	writeKnowledgeFile(t, repo, workPath, canonicalWorkNote("work-done", "2026-08-03T12:00:00Z"))
	writeKnowledgeFile(t, repo, lessonPath, canonicalKnowledgeNote("knowledge-lesson", "lesson", "2026-08-04T12:00:00Z", []string{"state-authority", "sqlite"}))
	writeKnowledgeFile(t, repo, decisionPath, canonicalKnowledgeNote("knowledge-decision", "decision", "2026-08-05T12:00:00Z", []string{"sqlite"}))
	scopes := KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{"prod-alpha"}, ProjectIDs: []string{}, DomainIDs: []string{"state"}, TagIDs: []string{"state-authority", "sqlite"}}
	writeManifestFixture(t, repo,
		manifestFixtureFromFile(t, repo, "knowledge-lesson", "lesson", lessonPath, "published", "2026-08-04T12:00:00Z", "Durable lesson", "Durable summary", []string{"state-authority", "sqlite"}, scopes),
		manifestFixtureFromFile(t, repo, "knowledge-decision", "decision", decisionPath, "accepted", "2026-08-05T12:00:00Z", "Durable decision", "Durable summary", []string{"sqlite"}, scopes),
	)
	commit := commitKnowledgeRepo(t, repo, "knowledge")

	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	seedKnowledgeWork(t, s, "work-done", "Auth release")
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if err := PublishCompactionLink(ctx, s, CompactionLinkRequest{EventID: "compact-work-done", WorkID: "work-done", ExpectedVersion: 3, Actor: "operator", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), Home: home, CommitOID: commit, NotePath: workPath, Reason: "durable outcome"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	q9, err := s.QueryQ9(ctx, Q9Request{Product: "prod-alpha", Tags: []string{"sqlite"}, AllowDegraded: false, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if len(q9.Items) != 2 || q9.Items[0].ID != "knowledge-decision" || q9.Items[1].ID != "knowledge-lesson" || q9.Authority != "authoritative" {
		t.Fatalf("Q9 = %#v", q9)
	}
	q10, err := s.QueryQ10(ctx, Q10Request{Work: "work-done", Home: home})
	if err != nil || q10.Status != "canonical" || q10.Note == nil || q10.Note.NotePath != workPath {
		t.Fatalf("Q10 = %#v, err %v", q10, err)
	}
}

func TestQ10OrphanWorkNoteRemainsNotCompacted(t *testing.T) {
	repo := initKnowledgeRepo(t)
	path := "docs/work/2026-08-07-orphan.md"
	writeKnowledgeFile(t, repo, path, canonicalWorkNote("work-orphan", "2026-08-07T00:00:00Z"))
	commitKnowledgeRepo(t, repo, "orphan")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj", HomeLocatorID: "loc", RepoPath: repo, HeadRef: "HEAD"}
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	seedKnowledgeWork(t, s, "work-orphan", "Orphan")
	authorizeKnowledgeLocator(t, s, home)
	result, err := s.QueryQ10(context.Background(), Q10Request{Work: "work-orphan", Home: home})
	if err != nil || result.Status != "not_compacted" || result.Authority != "authoritative" {
		t.Fatalf("Q10 orphan = %#v, err %v", result, err)
	}
}

func TestKnowledgeWatermarkControlsAuthoritativeEmptyAndDegradedResults(t *testing.T) {
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "docs/lessons/one.md", canonicalKnowledgeNote("one", "lesson", "2026-08-07T00:00:00Z", []string{"sqlite"}))
	commitKnowledgeRepo(t, repo, "first")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "p", HomeLocatorID: "l", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "prod-alpha", home)
	if err := s.RebuildKnowledgeIndex(context.Background(), home); err != nil {
		t.Fatal(err)
	}
	empty, err := s.QueryQ9(context.Background(), Q9Request{Product: "prod-alpha", Home: home})
	if err != nil || empty.Authority != "authoritative" || len(empty.Items) != 0 {
		t.Fatalf("authoritative empty = %#v, err %v", empty, err)
	}
	writeKnowledgeFile(t, repo, "docs/lessons/two.md", canonicalKnowledgeNote("two", "lesson", "2026-08-07T01:00:00Z", []string{"sqlite"}))
	commitKnowledgeRepo(t, repo, "second")
	_, err = s.QueryQ9(context.Background(), Q9Request{Home: home})
	assertFailureKind(t, err, KindIndexDegraded)
	degraded, err := s.QueryQ9(context.Background(), Q9Request{Home: home, AllowDegraded: true})
	if err != nil || degraded.Authority != "degraded" || len(degraded.Omissions) == 0 || degraded.IndexWatermark == "" {
		t.Fatalf("degraded knowledge = %#v, err %v", degraded, err)
	}
}

func TestQ9DoesNotReturnKnowledgeFromAnotherGitHome(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	firstRepo := initKnowledgeRepo(t)
	firstPath := "docs/lessons/first.md"
	writeKnowledgeFile(t, firstRepo, firstPath, canonicalKnowledgeNote("first-home", "lesson", "2026-08-07T00:00:00Z", []string{"sqlite"}))
	writeManifestFixture(t, firstRepo, manifestFixtureFromFile(t, firstRepo, "first-home", "lesson", firstPath, "published", "2026-08-07T00:00:00Z", "Durable lesson", "Durable summary", []string{"sqlite"}, KnowledgeRecordScopes{Mode: "home"}))
	commitKnowledgeRepo(t, firstRepo, "first home")
	firstHome := KnowledgeHome{HomeProjectID: "project-first", HomeLocatorID: "locator-first", RepoPath: firstRepo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-first-home", firstHome)
	if err := s.RebuildKnowledgeIndex(ctx, firstHome); err != nil {
		t.Fatal(err)
	}

	secondRepo := initKnowledgeRepo(t)
	secondPath := "docs/lessons/second.md"
	writeKnowledgeFile(t, secondRepo, secondPath, canonicalKnowledgeNote("second-home", "lesson", "2026-08-07T00:00:00Z", []string{"sqlite"}))
	writeManifestFixture(t, secondRepo, manifestFixtureFromFile(t, secondRepo, "second-home", "lesson", secondPath, "published", "2026-08-07T00:00:00Z", "Durable lesson", "Durable summary", []string{"sqlite"}, KnowledgeRecordScopes{Mode: "home"}))
	commitKnowledgeRepo(t, secondRepo, "second home")
	secondHome := KnowledgeHome{HomeProjectID: "project-second", HomeLocatorID: "locator-second", RepoPath: secondRepo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-second-home", secondHome)
	if err := s.RebuildKnowledgeIndex(ctx, secondHome); err != nil {
		t.Fatal(err)
	}

	result, err := s.QueryQ9(ctx, Q9Request{Tags: []string{"sqlite"}, Home: firstHome})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != "first-home" {
		t.Fatalf("Q9 returned knowledge outside its authoritative git home: %#v", result.Items)
	}
}

func TestRebuildKnowledgeIndexRejectsDuplicateStableIDs(t *testing.T) {
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "docs/lessons/a.md", canonicalKnowledgeNote("duplicate", "lesson", "2026-08-07T00:00:00Z", []string{"one"}))
	writeKnowledgeFile(t, repo, "docs/lessons/b.md", canonicalKnowledgeNote("duplicate", "lesson", "2026-08-07T00:00:00Z", []string{"two"}))
	writeManifestFixture(t, repo,
		manifestFixtureFromFile(t, repo, "duplicate-a", "lesson", "docs/lessons/a.md", "published", "2026-08-07T00:00:00Z", "Durable lesson", "Durable summary", []string{"one"}, KnowledgeRecordScopes{Mode: "home"}),
		manifestFixtureFromFile(t, repo, "duplicate-a", "lesson", "docs/lessons/b.md", "published", "2026-08-07T00:00:00Z", "Durable lesson", "Durable summary", []string{"two"}, KnowledgeRecordScopes{Mode: "home"}),
	)
	commitKnowledgeRepo(t, repo, "duplicate")
	s := openTemp(t)
	err := s.RebuildKnowledgeIndex(context.Background(), KnowledgeHome{HomeProjectID: "proj", HomeLocatorID: "loc", RepoPath: repo, HeadRef: "HEAD"})
	assertFailureKind(t, err, KindKnowledgeAmbiguous)
}

func TestRebuildFromLogLeavesGitKnowledgeTablesUntouched(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO archived_work (id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,home_project_id,home_locator_id,note_path,commit_oid,content_hash) VALUES ('w','lesson','L','2026-08-07T00:00:00Z','published','[]','completed',1,'S','p','l','docs/lessons/l.md','`+strings.Repeat("a", 40)+`','sha256:`+strings.Repeat("b", 64)+`')`); err == nil {
		t.Fatal("ad-hoc archived_work write succeeded")
	}
	// The public rebuild assertion is covered once a proof-backed compaction row exists.
	if version, err := SchemaVersion(ctx, s.DatabaseForTesting()); err != nil || version < 6 {
		t.Fatalf("schema version = %d, err %v", version, err)
	}
}

func TestKnowledgeSchemaHasNoNoteBodyAndRebuildFromLogPreservesIndex(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	rows, err := s.DatabaseForTesting().QueryContext(ctx, `PRAGMA table_info(archived_work)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(name), "body") || strings.EqualFold(name, "content") {
			t.Fatalf("archived_work contains forbidden note field %q", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	repo := initKnowledgeRepo(t)
	path := "docs/work/2026-08-07-linked.md"
	writeKnowledgeFile(t, repo, path, canonicalWorkNote("linked", "2026-08-07T00:00:00Z"))
	commit := commitKnowledgeRepo(t, repo, "linked")
	home := KnowledgeHome{HomeProjectID: "p", HomeLocatorID: "l", RepoPath: repo, HeadRef: "HEAD"}
	seedKnowledgeWork(t, s, "linked", "Linked")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: "complete-linked", Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: "linked", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"from":"needed","to":"completed","reason":"fixture","expected_version":2,"resulting_version":3}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "linked"): 2}}); err != nil {
		t.Fatal(err)
	}
	if err := PublishCompactionLink(ctx, s, CompactionLinkRequest{EventID: "compact-linked", WorkID: "linked", ExpectedVersion: 3, Actor: "operator", OccurredAt: time.Now().UTC(), Home: home, CommitOID: commit, NotePath: path, Reason: "fixture"}); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM archived_work`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM archived_work`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after || after != 1 {
		t.Fatalf("archived_work count before=%d after=%d", before, after)
	}
}

func seedKnowledgeWork(t *testing.T, s *Store, id, title string) {
	t.Helper()
	ctx := context.Background()
	events := []Event{
		{EventID: "create-prod-" + id, Kind: "product.created", SubjectType: SubjectProduct, SubjectID: "prod-alpha", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Alpha","stage_maturity":"production","stage_audience_commitment":"operator_only"}`)},
		{EventID: "create-proj-" + id, Kind: "project.created", SubjectType: SubjectProject, SubjectID: "proj-web", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"display_name":"Web"}`)},
		{EventID: "product-project-" + id, Kind: "product_project.added", SubjectType: SubjectProduct, SubjectID: "prod-alpha", Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"product_id":"prod-alpha","project_id":"proj-web","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
		{EventID: "create-work-" + id, Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 2, Payload: []byte(`{"work_kind":"task","title":"` + title + `","priority":2}`)},
		{EventID: "work-project-" + id, Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"` + id + `","project_id":"proj-web","role":"primary","reason":"fixture","expected_version":1,"resulting_version":2}`)},
	}
	if err := ApplyOperation(ctx, s, Operation{Events: events, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "prod-alpha"): 0, VersionRef(SubjectProject, "proj-web"): 0, VersionRef(SubjectWorkItem, id): 0}}); err != nil {
		t.Fatal(err)
	}
	if id == "work-done" {
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: "complete-" + id, Kind: "work.transitioned", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "operator", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: []byte(`{"from":"needed","to":"completed","reason":"fixture","expected_version":2,"resulting_version":3}`)}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 2}}); err != nil {
			t.Fatal(err)
		}
	}
}

func initKnowledgeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runKnowledgeGit(t, repo, "init", "--initial-branch=main")
	runKnowledgeGit(t, repo, "config", "user.email", "test@example.invalid")
	runKnowledgeGit(t, repo, "config", "user.name", "Concord Test")
	return repo
}

func writeKnowledgeFile(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitKnowledgeRepo(t *testing.T, repo, message string) string {
	t.Helper()
	runKnowledgeGit(t, repo, "add", "--", ".")
	runKnowledgeGit(t, repo, "commit", "--quiet", "--date", "2026-08-07T00:00:00Z", "-m", message, "--author", "Concord Test <test@example.invalid>")
	return strings.TrimSpace(runKnowledgeGit(t, repo, "rev-parse", "HEAD"))
}

func runKnowledgeGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-08-07T00:00:00Z", "GIT_COMMITTER_DATE=2026-08-07T00:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func canonicalWorkNote(id, completed string) string {
	return "---\n" +
		"concord_work_id: " + id + "\n" +
		"work_type: implementation\n" +
		"title: Auth release\n" +
		"completed_at: " + completed + "\n" +
		"outcome_tag: shipped\n" +
		"lesson_tags: [sqlite, state-authority]\n" +
		"terminal_state: completed\n" +
		"priority: 2\n" +
		"summary: Bounded summary\n" +
		"product_ids: [prod-alpha]\n" +
		"project_ids: [proj-web]\n" +
		"domain_ids: [auth]\n" +
		"tag_ids: [auth, release]\n" +
		"---\n\nDurable note.\n"
}

func canonicalKnowledgeNote(id, kind, completed string, tags []string) string {
	return "---\n" +
		"id: " + id + "\n" +
		"type: " + kind + "\n" +
		"title: Durable " + kind + "\n" +
		"completed_at: " + completed + "\n" +
		"outcome_tag: published\n" +
		"lesson_tags: [" + strings.Join(tags, ", ") + "]\n" +
		"terminal_state: completed\n" +
		"priority: 0\n" +
		"summary: Durable summary\n" +
		"product_ids: [prod-alpha]\n" +
		"project_ids: []\n" +
		"component_ids: [state]\n" +
		"tag_ids: [" + strings.Join(tags, ", ") + "]\n" +
		"---\n\nDurable knowledge.\n"
}
