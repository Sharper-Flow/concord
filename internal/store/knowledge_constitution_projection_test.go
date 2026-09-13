package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// homeScope is the schema 1.2 home-scope shape: present, empty ID arrays.
func homeScope() KnowledgeRecordScopes {
	return KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, DomainIDs: []string{}, TagIDs: []string{}}
}

// seedConstitutionHome writes a knowledge home whose manifest carries one
// accepted constitution with a home Domain, one accepted decision, and one
// published lesson, then returns the home and the manifest record IDs.
func seedConstitutionHome(t *testing.T) (KnowledgeHome, string, string, string) {
	t.Helper()
	repo := initKnowledgeRepo(t)
	constitutionPath := "docs/proc.md"
	constitutionBody := "formalization procedure\n"
	decisionPath := "docs/decisions/CD-0001.md"
	decisionBody := "decision body\n"
	lessonPath := "docs/lessons/lesson.md"
	lessonBody := "lesson body\n"
	writeKnowledgeFile(t, repo, constitutionPath, constitutionBody)
	writeKnowledgeFile(t, repo, decisionPath, decisionBody)
	writeKnowledgeFile(t, repo, lessonPath, lessonBody)
	constitutionSum, decisionSum, lessonSum := sha256.Sum256([]byte(constitutionBody)), sha256.Sum256([]byte(decisionBody)), sha256.Sum256([]byte(lessonBody))
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"constitution", "decision", "lesson"},
		IndexedKinds:   []string{"constitution", "decision", "lesson"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{
				{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
				{DomainID: "product-memory", Name: "Memory", Purpose: "Knowledge", Status: "current", ParentDomainID: "product-root:concord", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			},
		},
		Records: []KnowledgeRecord{
			{ID: "proc-1", Kind: "constitution", Path: constitutionPath, Status: "accepted", Date: "2026-08-24T00:00:00Z", Title: "Formalization procedure", Summary: "How documents become law", Tags: []string{}, Scopes: homeScope(), HomeDomainID: "product-memory", SHA256: "sha256:" + hex.EncodeToString(constitutionSum[:])},
			{ID: "CD-0001", Kind: "decision", Path: decisionPath, Status: "accepted", Date: "2026-08-18T00:00:00Z", Title: "Domain law", Summary: "A domain law", Tags: []string{}, Scopes: homeScope(), HomeDomainID: "product-root:concord", ProductWideRationale: "cross-cutting law", SHA256: "sha256:" + hex.EncodeToString(decisionSum[:])},
			{ID: "lesson-1", Kind: "lesson", Path: lessonPath, Status: "published", Date: "2026-08-10T00:00:00Z", Title: "Lesson", Summary: "A lesson", Tags: []string{}, Scopes: homeScope(), SHA256: "sha256:" + hex.EncodeToString(lessonSum[:])},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(manifestBytes)+"\n")
	commitKnowledgeRepo(t, repo, "constitution knowledge")
	return KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}, "proc-1", "CD-0001", "lesson-1"
}

// TestAcceptedConstitutionProjectsResolvesAndAdmitsAsLaw proves the three
// halves of the constitution classification defect together: an accepted
// constitution discoverable through Q9 must project into law_subjects with
// its Domain home, resolve canonically through Q10 with matching proof, and
// satisfy a workflow law mandate.
func TestAcceptedConstitutionProjectsResolvesAndAdmitsAsLaw(t *testing.T) {
	ctx := context.Background()
	home, constitutionID, decisionID, lessonID := seedConstitutionHome(t)
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "concord", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	var constitutionSubjects, constitutionHomes int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=? AND kind='constitution' AND status='accepted'`, home.HomeProjectID, home.HomeLocatorID, constitutionID).Scan(&constitutionSubjects); err != nil {
		t.Fatal(err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM law_domain_homes WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, home.HomeProjectID, home.HomeLocatorID, constitutionID).Scan(&constitutionHomes); err != nil {
		t.Fatal(err)
	}
	if constitutionSubjects != 1 || constitutionHomes != 1 {
		t.Fatalf("constitution projection rows = subjects %d homes %d, want 1 and 1", constitutionSubjects, constitutionHomes)
	}

	var lessonSubjects int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, home.HomeProjectID, home.HomeLocatorID, lessonID).Scan(&lessonSubjects); err != nil {
		t.Fatal(err)
	}
	if lessonSubjects != 0 {
		t.Fatalf("non-law lesson projected into law_subjects (%d rows)", lessonSubjects)
	}

	q9, err := s.QueryQ9(ctx, Q9Request{Product: "concord", Kinds: []string{"constitution"}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range q9.Items {
		if item.ID == constitutionID {
			found = true
		}
	}
	if !found {
		t.Fatalf("Q9 constitution search did not return %s: %#v", constitutionID, q9.Items)
	}

	q10, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: constitutionID})
	if err != nil {
		t.Fatalf("Q10 constitution resolution failed: %v", err)
	}
	if q10.Status != "canonical" {
		t.Fatalf("Q10 constitution status = %s, want canonical", q10.Status)
	}
	if q10.Note == nil || q10.Note.NotePath != "docs/proc.md" {
		t.Fatalf("Q10 constitution note = %#v", q10.Note)
	}

	if err := s.CheckMandatedLawsAtHome(ctx, home.HomeProjectID, home.HomeLocatorID, []string{constitutionID, decisionID}, nil, true); err != nil {
		t.Fatalf("mandated constitution and decision failed admission: %v", err)
	}
}

// TestConstitutionCannotAuthorLawRelations keeps the law-relation graph
// restricted to decision and spec endpoints: admitting constitutions as law
// subjects must not broaden relation authorship.
func TestConstitutionCannotAuthorLawRelations(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	path := "docs/proc.md"
	content := "constitution body\n"
	writeKnowledgeFile(t, repo, path, content)
	sum := sha256.Sum256([]byte(content))
	manifest := KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"constitution"}, IndexedKinds: []string{"constitution"},
		DomainRegistry: KnowledgeDomainRegistry{
			SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
			Domains: []KnowledgeDomain{{DomainID: "product-root:concord", Name: "Concord", Purpose: "Product law", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}}},
		},
		Records: []KnowledgeRecord{{
			ID: "proc-1", Kind: "constitution", Path: path, Status: "accepted", Date: "2026-08-24T00:00:00Z",
			Title: "Constitution", Summary: "s", Tags: []string{}, Scopes: homeScope(), HomeDomainID: "product-root:concord", ProductWideRationale: "cross-cutting law",
			SHA256:       "sha256:" + hex.EncodeToString(sum[:]),
			LawRelations: []KnowledgeRelation{{Kind: "refines", TargetID: "CD-0001"}},
		}},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeKnowledgeFile(t, repo, knowledgeManifestPath, string(manifestBytes)+"\n")
	commit := commitKnowledgeRepo(t, repo, "constitution relations")
	_, _, readErr := readKnowledgeManifest(ctx, repo, commit)
	if readErr == nil {
		t.Fatal("constitution law_relations parsed without refusal")
	}
	if !strings.Contains(readErr.Error(), "law_relations are only allowed on decision/spec records") {
		t.Fatalf("constitution law_relations refusal = %v", readErr)
	}
}

// TestConstitutionProjectionConvergesAfterWatermarkInvalidation proves the
// convergence seam: content-digest freshness alone cannot repair a pre-fix
// index because the digest tracks Git content, not projection semantics, so
// the semantic migration must invalidate the watermark and let the
// demand-driven rebuild (CD-0082 D1) repopulate.
func TestConstitutionProjectionConvergesAfterWatermarkInvalidation(t *testing.T) {
	ctx := context.Background()
	home, constitutionID, _, _ := seedConstitutionHome(t)
	s := openTemp(t)
	authorizeKnowledgeProductHome(t, s, "concord", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	db := s.DatabaseForTesting()
	stripped := func() int {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`, home.HomeProjectID, home.HomeLocatorID, constitutionID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	if stripped() != 1 {
		t.Fatalf("post-rebuild constitution rows = %d, want 1", stripped())
	}

	// Reproduce the pre-fix residual: the watermark claims fresh content
	// while the constitution rows are absent.
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DELETE FROM law_domain_applicability WHERE home_project_id=? AND home_locator_id=? AND law_id=?`,
		`DELETE FROM law_domain_homes WHERE home_project_id=? AND home_locator_id=? AND law_id=?`,
		`DELETE FROM law_subjects WHERE home_project_id=? AND home_locator_id=? AND law_id=?`,
	} {
		if _, err := db.Exec(stmt, home.HomeProjectID, home.HomeLocatorID, constitutionID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if stripped() != 0 {
		t.Fatal("pre-fix residual simulation left constitution rows")
	}

	// Freshness reads Git content only, so it cannot detect the omission.
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	if stripped() != 0 {
		t.Fatal("freshness alone rebuilt the constitution projection; invalidation would be unnecessary")
	}

	// The migration's invalidation is the seam the rebuild demand needs.
	if _, err := db.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM knowledge_index_watermark`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM fold_guard WHERE active=1`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	if stripped() != 1 {
		t.Fatalf("invalidation did not converge the constitution projection (%d rows)", stripped())
	}
	q10, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: constitutionID})
	if err != nil || q10.Status != "canonical" {
		t.Fatalf("post-convergence Q10 = %#v err=%v", q10, err)
	}
}
