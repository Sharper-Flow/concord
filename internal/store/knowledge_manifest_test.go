package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestManifestPathBoundUsesUnicodeScalarsAtSchemaLimit(t *testing.T) {
	valid := "docs/" + strings.Repeat("é", 504) + ".md"
	if utf8.RuneCountInString(valid) != 512 {
		t.Fatalf("valid path rune count = %d", utf8.RuneCountInString(valid))
	}
	if err := validateManifestPath(valid); err != nil {
		t.Fatalf("512 Unicode-scalar path rejected: %v", err)
	}
	tooLong := "docs/" + strings.Repeat("é", 505) + ".md"
	if utf8.RuneCountInString(tooLong) != 513 {
		t.Fatalf("invalid path rune count = %d", utf8.RuneCountInString(tooLong))
	}
	if err := validateManifestPath(tooLong); err == nil {
		t.Fatal("513 Unicode-scalar path accepted")
	}
}

func TestKnowledgeManifestRejectsUnknownFieldsAndInvalidCombinations(t *testing.T) {
	valid := `{"schema_version":"1.0","supported_kinds":["lesson","research"],"indexed_kinds":["lesson"],"records":[{"id":"lesson-1","kind":"lesson","path":"docs/lessons/one.md","status":"published","date":"2026-08-10T00:00:00Z","title":"Lesson","summary":"Summary","tags":[],"scopes":{"mode":"home","product_ids":[],"project_ids":[],"component_ids":[],"tag_ids":[]},"sha256":"sha256:` + strings.Repeat("a", 64) + `"}]}`
	for name, raw := range map[string]string{
		"unknown field":  strings.Replace(valid, `"summary":"Summary"`, `"summary":"Summary","body":"forbidden"`, 1),
		"duplicate id":   strings.Replace(valid, `"records":[{`, `"records":[{`, 1),
		"bad status":     strings.Replace(valid, `"status":"published"`, `"status":"accepted"`, 1),
		"bad path":       strings.Replace(valid, `docs/lessons/one.md`, `docs/generated/one.md`, 1),
		"uppercase hash": strings.Replace(valid, "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("A", 64), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "duplicate id" {
				raw = strings.Replace(valid, `}]}`, `},{"id":"lesson-1","kind":"lesson","path":"docs/lessons/two.md","status":"published","date":"2026-08-10T00:00:00Z","title":"Lesson","summary":"Summary","tags":[],"scopes":{"mode":"home","product_ids":[],"project_ids":[],"component_ids":[],"tag_ids":[]},"sha256":"sha256:`+strings.Repeat("b", 64)+`"}]}`, 1)
			}
			_, err := parseKnowledgeManifest([]byte(raw))
			if name == "duplicate id" {
				assertFailureKind(t, err, KindKnowledgeAmbiguous)
			} else {
				assertFailureKind(t, err, KindInvalidNoteProof)
			}
		})
	}
}

func TestKnowledgeManifestV12RequiresDomainHomesAndDomainScopes(t *testing.T) {
	valid := `{"schema_version":"1.2","supported_kinds":["decision","spec"],"indexed_kinds":["decision","spec"],"domain_registry":{"schema_version":"1.0","product_key":"concord","root_domain_id":"product-root:concord","domains":[{"domain_id":"product-root:concord","name":"Concord","purpose":"Product-wide Concord law and architecture","status":"current","architecture_relations":[]}]},"records":[{"id":"CD-0001","kind":"decision","path":"docs/decisions/CD-0001.md","status":"accepted","date":"2026-08-10T00:00:00Z","title":"Decision","summary":"Summary","tags":[],"scopes":{"mode":"home","product_ids":[],"project_ids":[],"domain_ids":[],"tag_ids":[]},"home_domain_id":"product-root:concord","sha256":"sha256:` + strings.Repeat("a", 64) + `"}]}`
	if _, err := parseKnowledgeManifest([]byte(valid)); err != nil {
		t.Fatalf("valid 1.2 manifest rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"missing domain registry": strings.Replace(valid, `,"domain_registry":{"schema_version":"1.0","product_key":"concord","root_domain_id":"product-root:concord","domains":[{"domain_id":"product-root:concord","name":"Concord","purpose":"Product-wide Concord law and architecture","status":"current","architecture_relations":[]}]}`, "", 1),
		"missing domain scope":    strings.Replace(valid, `"domain_ids":[]`, `"component_ids":[]`, 1),
		"missing law home":        strings.Replace(valid, `,"home_domain_id":"product-root:concord"`, "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseKnowledgeManifest([]byte(raw)); err == nil {
				t.Fatal("invalid 1.2 manifest accepted")
			}
		})
	}
}

func TestKnowledgeManifestV12RequiresLawHomeForApplicability(t *testing.T) {
	valid := `{"schema_version":"1.2","supported_kinds":["decision"],"indexed_kinds":["decision"],"domain_registry":{"schema_version":"1.0","product_key":"concord","root_domain_id":"product-root:concord","domains":[{"domain_id":"product-root:concord","name":"Concord","purpose":"Product-wide Concord law and architecture","status":"current","architecture_relations":[]}]},"records":[{"id":"CD-0001","kind":"decision","path":"docs/decisions/CD-0001.md","status":"superseded","date":"2026-08-10T00:00:00Z","title":"Decision","summary":"Summary","tags":[],"scopes":{"mode":"home","product_ids":[],"project_ids":[],"domain_ids":[],"tag_ids":[]},"successor":"CD-0002","home_domain_id":"product-root:concord","applies_to_domain_ids":[],"sha256":"sha256:` + strings.Repeat("a", 64) + `"},{"id":"CD-0002","kind":"decision","path":"docs/decisions/CD-0002.md","status":"accepted","date":"2026-08-10T00:00:00Z","title":"Successor","summary":"Summary","tags":[],"scopes":{"mode":"home","product_ids":[],"project_ids":[],"domain_ids":[],"tag_ids":[]},"home_domain_id":"product-root:concord","law_relations":[{"kind":"supersedes","target_id":"CD-0001"}],"sha256":"sha256:` + strings.Repeat("b", 64) + `"}]}`
	if _, err := parseKnowledgeManifest([]byte(valid)); err != nil {
		t.Fatalf("superseded law with home and empty applicability rejected: %v", err)
	}
	missingHome := strings.Replace(valid, `,"home_domain_id":"product-root:concord","applies_to_domain_ids":[]`, `,"applies_to_domain_ids":["product-root:concord"]`, 1)
	if _, err := parseKnowledgeManifest([]byte(missingHome)); err == nil {
		t.Fatal("superseded law applicability without a home was accepted")
	}
}

func TestKnowledgeManifestV12RejectsInvalidDomainRelations(t *testing.T) {
	valid := `{"schema_version":"1.2","supported_kinds":["decision","spec"],"indexed_kinds":["decision","spec"],"domain_registry":{"schema_version":"1.0","product_key":"concord","root_domain_id":"product-root:concord","domains":[{"domain_id":"product-root:concord","name":"Concord","purpose":"Product-wide Concord law and architecture","status":"current","architecture_relations":[{"kind":"depends_on","target_domain_id":"product-root:concord","governing_law_ids":["CD-0001"]}]}]},"records":[{"id":"CD-0001","kind":"decision","path":"docs/decisions/CD-0001.md","status":"accepted","date":"2026-08-10T00:00:00Z","title":"Decision","summary":"Summary","tags":[],"scopes":{"mode":"home","product_ids":[],"project_ids":[],"domain_ids":[],"tag_ids":[]},"home_domain_id":"product-root:concord","sha256":"sha256:` + strings.Repeat("a", 64) + `"}]}`
	if _, err := parseKnowledgeManifest([]byte(valid)); err == nil {
		t.Fatal("self-referential domain dependency accepted")
	}
}

func TestKnowledgeDomainRegistryHashIsDeterministicAndNonMutating(t *testing.T) {
	first := KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
		Domains: []KnowledgeDomain{
			{DomainID: "zeta", Name: "Zeta", Purpose: "Zeta domain", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{{Kind: "depends_on", TargetDomainID: "alpha", GoverningLawIDs: []string{"CD-0002", "CD-0001"}}}},
			{DomainID: "alpha", Name: "Alpha", Purpose: "Alpha domain", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
		},
	}
	second := KnowledgeDomainRegistry{
		SchemaVersion: "1.0", ProductKey: "concord", RootDomainID: "product-root:concord",
		Domains: []KnowledgeDomain{
			{DomainID: "alpha", Name: "Alpha", Purpose: "Alpha domain", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{}},
			{DomainID: "zeta", Name: "Zeta", Purpose: "Zeta domain", Status: "current", ArchitectureRelations: []KnowledgeArchitectureRelation{{Kind: "depends_on", TargetDomainID: "alpha", GoverningLawIDs: []string{"CD-0001", "CD-0002"}}}},
		},
	}
	original := first.Domains[0].ArchitectureRelations[0].GoverningLawIDs
	if got, want := domainRegistryContentHash(first), domainRegistryContentHash(second); got == "" || got != want {
		t.Fatalf("registry hashes = %q and %q", got, want)
	}
	if !reflect.DeepEqual(first.Domains[0].ArchitectureRelations[0].GoverningLawIDs, original) {
		t.Fatal("hash normalization mutated caller-owned governing law IDs")
	}
}

func TestMigrateLegacyKnowledgeManifestMapsD9ScopesDeterministically(t *testing.T) {
	legacy := legacyManifestForDomainMigration()
	before := legacy
	before.Records = append([]KnowledgeRecord{}, legacy.Records...)
	for index := range before.Records {
		before.Records[index].Scopes.ComponentIDs = append([]string{}, legacy.Records[index].Scopes.ComponentIDs...)
	}
	migrated, err := MigrateLegacyKnowledgeManifest(legacy, "concord", "note-only")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(legacy, before) {
		t.Fatal("migration mutated its caller-owned manifest")
	}
	if err := validateKnowledgeManifest(migrated); err != nil {
		t.Fatalf("migrated manifest is invalid: %v", err)
	}
	if got, want := migrated.DomainRegistry.RootDomainID, "product-root:concord"; got != want {
		t.Fatalf("root domain = %q, want %q", got, want)
	}
	if got := migrated.DomainRegistry.Domains; len(got) != 4 || got[0].DomainID != "product-root:concord" || got[1].DomainID != "alpha" || got[2].DomainID != "note-only" || got[3].DomainID != "zeta" || got[1].ParentDomainID != "product-root:concord" || got[2].ParentDomainID != "product-root:concord" || got[3].ParentDomainID != "product-root:concord" {
		t.Fatalf("domains = %#v", got)
	}
	byID := map[string]KnowledgeRecord{}
	for _, record := range migrated.Records {
		byID[record.ID] = record
		if record.Scopes.ComponentIDs != nil || !record.Scopes.domainIDsPresent {
			t.Fatalf("record %s retained component scope: %#v", record.ID, record.Scopes)
		}
	}
	if got := byID["CD-0001-zero"]; got.HomeDomainID != "product-root:concord" || len(got.AppliesToDomainIDs) != 0 || len(got.Scopes.DomainIDs) != 0 {
		t.Fatalf("zero-component law = %#v", got)
	}
	if got := byID["CD-0002-one"]; got.HomeDomainID != "zeta" || len(got.AppliesToDomainIDs) != 0 || !reflect.DeepEqual(got.Scopes.DomainIDs, []string{"zeta"}) || !reflect.DeepEqual(got.LawRelations, []KnowledgeRelation{{Kind: "supersedes", TargetID: "CD-0003-many"}}) {
		t.Fatalf("one-component law = %#v", got)
	}
	if got := byID["CD-0003-many"]; got.HomeDomainID != "product-root:concord" || !reflect.DeepEqual(got.AppliesToDomainIDs, []string{"alpha", "zeta"}) || !reflect.DeepEqual(got.Scopes.DomainIDs, []string{"alpha", "zeta"}) || got.Successor != "CD-0002-one" {
		t.Fatalf("many-component historical law = %#v", got)
	}
	if got := byID["lesson-1"]; got.HomeDomainID != "" || got.AppliesToDomainIDs != nil || !reflect.DeepEqual(got.Scopes.DomainIDs, []string{"alpha"}) {
		t.Fatalf("lesson migration = %#v", got)
	}

	reordered := legacyManifestForDomainMigration()
	reordered.Records[2].Scopes.ComponentIDs = []string{"alpha", "zeta"}
	again, err := MigrateLegacyKnowledgeManifest(reordered, "concord", "note-only")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(migrated, again) {
		t.Fatalf("set-equivalent component order produced different migrations:\n%#v\n%#v", migrated, again)
	}
}

func TestMigrateLegacyKnowledgeManifestAcceptsV10(t *testing.T) {
	legacy := legacyManifestForDomainMigration()
	legacy.SchemaVersion = "1.0"
	legacy.Records = append([]KnowledgeRecord{}, legacy.Records[:2]...)
	legacy.Records[1].LawRelations = nil
	migrated, err := MigrateLegacyKnowledgeManifest(legacy, "concord")
	if err != nil {
		t.Fatal(err)
	}
	if migrated.SchemaVersion != "1.2" || migrated.DomainRegistry.RootDomainID != "product-root:concord" {
		t.Fatalf("v1.0 migration = %#v", migrated)
	}
}

func TestMigrateLegacyKnowledgeManifestRejectsInvalidInputs(t *testing.T) {
	legacy := legacyManifestForDomainMigration()
	for name, test := range map[string]struct {
		manifest   KnowledgeManifest
		productKey string
		want       FailureKind
	}{
		"invalid product key": {manifest: legacy, productKey: "Concord", want: KindInvalidNoteProof},
		"non-legacy schema":   {manifest: func() KnowledgeManifest { candidate := legacy; candidate.SchemaVersion = "1.2"; return candidate }(), productKey: "concord", want: KindInvalidNoteProof},
		"derived root collision": {manifest: func() KnowledgeManifest {
			candidate := legacy
			candidate.Records = append([]KnowledgeRecord{}, legacy.Records...)
			candidate.Records[0].Scopes.ComponentIDs = []string{"product-root:concord"}
			candidate.Records[0].Scopes.Mode = "explicit"
			return candidate
		}(), productKey: "concord", want: KindKnowledgeAmbiguous},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := MigrateLegacyKnowledgeManifest(test.manifest, test.productKey)
			assertFailureKind(t, err, test.want)
		})
	}
}

func legacyManifestForDomainMigration() KnowledgeManifest {
	base := func(id, kind, path, status string, scopes KnowledgeRecordScopes) KnowledgeRecord {
		return KnowledgeRecord{ID: id, Kind: kind, Path: path, Status: status, Date: "2026-08-18T00:00:00Z", Title: id, Summary: "Legacy component-scoped law.", Tags: []string{"legacy"}, Scopes: scopes, SHA256: "sha256:" + strings.Repeat("a", 64)}
	}
	zero := base("CD-0001-zero", "decision", "docs/decisions/CD-0001-zero.md", "accepted", KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, ComponentIDs: []string{}, TagIDs: []string{}})
	one := base("CD-0002-one", "decision", "docs/decisions/CD-0002-one.md", "accepted", KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{"product"}, ProjectIDs: []string{}, ComponentIDs: []string{"zeta"}, TagIDs: []string{}})
	many := base("CD-0003-many", "decision", "docs/decisions/CD-0003-many.md", "superseded", KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{"project"}, ComponentIDs: []string{"zeta", "alpha"}, TagIDs: []string{}})
	many.Successor = one.ID
	one.LawRelations = []KnowledgeRelation{{Kind: "supersedes", TargetID: many.ID}}
	lesson := base("lesson-1", "lesson", "docs/lessons/lesson.md", "published", KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{}, ProjectIDs: []string{}, ComponentIDs: []string{"alpha"}, TagIDs: []string{"lesson"}})
	return KnowledgeManifest{SchemaVersion: "1.1", SupportedKinds: []string{"decision", "lesson"}, IndexedKinds: []string{"decision", "lesson"}, Records: []KnowledgeRecord{zero, one, many, lesson}}
}

func TestManifestSuccessorsAreValidatedAfterTheFullRecordSet(t *testing.T) {
	base := func(id, kind, status, successor string) KnowledgeRecord {
		recordPath := "docs/" + id + ".md"
		if kind == "decision" {
			recordPath = "docs/decisions/CD-0001-" + id + ".md"
		}
		record := KnowledgeRecord{ID: id, Kind: kind, Path: recordPath, Status: status, Date: "2026-08-10T00:00:00Z", Title: id, Summary: "summary", Tags: []string{}, Scopes: KnowledgeRecordScopes{Mode: "home", ProductIDs: []string{}, ProjectIDs: []string{}, ComponentIDs: []string{}, TagIDs: []string{}}, SHA256: "sha256:" + strings.Repeat("a", 64)}
		if successor != "" {
			record.Successor = successor
		}
		return record
	}
	manifest := func(records ...KnowledgeRecord) KnowledgeManifest {
		return KnowledgeManifest{SchemaVersion: "1.0", SupportedKinds: []string{"lesson", "decision", "spec", "research"}, IndexedKinds: []string{"lesson", "decision", "spec"}, Records: records}
	}
	for name, candidate := range map[string]KnowledgeManifest{
		"missing target":    manifest(base("old", "decision", "superseded", "new")),
		"self target":       manifest(base("old", "decision", "superseded", "old")),
		"cross kind":        manifest(base("old", "decision", "superseded", "new"), base("new", "spec", "accepted", "")),
		"bad target status": manifest(base("old", "decision", "superseded", "new"), base("new", "decision", "superseded", "other"), base("other", "decision", "accepted", "")),
	} {
		t.Run(name, func(t *testing.T) {
			assertFailureKind(t, validateKnowledgeManifest(candidate), KindInvalidNoteProof)
		})
	}
	if err := validateKnowledgeManifest(manifest(base("old", "decision", "superseded", "new"), base("new", "decision", "accepted", ""))); err != nil {
		t.Fatalf("valid supersession rejected: %v", err)
	}
}

func TestManifestRebuildIndexesDecisionSpecLessonAndQ10Proof(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	decision := "docs/decisions/CD-0001.md"
	spec := "docs/spec.md"
	lesson := "docs/lessons/lesson.md"
	writeManifestFixture(t, repo,
		manifestFixture{ID: "decision-1", Kind: "decision", Path: decision, Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Decision", Summary: "Decision summary", Tags: []string{"sqlite"}, Scopes: KnowledgeRecordScopes{Mode: "home"}},
		manifestFixture{ID: "spec-1", Kind: "spec", Path: spec, Status: "accepted", Date: "2026-08-09T00:00:00Z", Title: "Spec", Summary: "Spec summary", Tags: []string{"sqlite"}, Scopes: KnowledgeRecordScopes{Mode: "home"}},
		manifestFixture{ID: "lesson-1", Kind: "lesson", Path: lesson, Status: "published", Date: "2026-08-08T00:00:00Z", Title: "Lesson", Summary: "Lesson summary", Tags: []string{"sqlite"}, Scopes: KnowledgeRecordScopes{Mode: "home"}},
	)
	commit := commitKnowledgeRepo(t, repo, "manifest knowledge")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	result, err := s.QueryQ9(ctx, Q9Request{Kinds: []string{"decision", "lesson", "spec"}, Product: "product", Tags: []string{"sqlite"}, Home: home})
	if err != nil || len(result.Items) != 3 || result.Authority != "authoritative" {
		t.Fatalf("manifest Q9=%#v err=%v", result, err)
	}
	q10, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "decision-1"})
	if err != nil || q10.Status != "canonical" || q10.Note == nil || q10.Note.NotePath != decision || q10.Note.CommitOID != commit {
		t.Fatalf("manifest Q10=%#v err=%v", q10, err)
	}
	before := knowledgeProjectionSnapshot(t, s)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	if after := knowledgeProjectionSnapshot(t, s); after != before {
		t.Fatalf("same-commit rebuild changed persisted projection:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestQueryQ9StructuredTextRankingIsCursorSafe(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo,
		manifestFixture{ID: "sqlite", Kind: "decision", Path: "docs/decisions/CD-0100-sqlite.md", Status: "accepted", Date: "2026-08-08T00:00:00Z", Title: "Storage decision", Summary: "Exact stable ID match", Tags: []string{"storage"}, Scopes: KnowledgeRecordScopes{Mode: "home"}},
		manifestFixture{ID: "newer-text", Kind: "lesson", Path: "docs/lessons/newer.md", Status: "published", Date: "2026-08-10T00:00:00Z", Title: "Newer lesson", Summary: "Uses SQLite safely", Tags: []string{"storage"}, Scopes: KnowledgeRecordScopes{Mode: "home"}},
		manifestFixture{ID: "older-text", Kind: "lesson", Path: "docs/lessons/older.md", Status: "published", Date: "2026-08-09T00:00:00Z", Title: "Older lesson", Summary: "SQLite recovery notes", Tags: []string{"storage"}, Scopes: KnowledgeRecordScopes{Mode: "home"}},
	)
	commitKnowledgeRepo(t, repo, "structured knowledge ranking")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-ranking", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	first, err := s.QueryQ9(ctx, Q9Request{Text: "SQLITE", Limit: 1, Home: home})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != "sqlite" || first.NextCursor == nil {
		t.Fatalf("first page = %#v, err %v", first, err)
	}
	firstCursor, err := decodeKnowledgeCursor(*first.NextCursor, Q9Request{Text: "SQLITE", Limit: 1, Home: home}, nil, nil)
	if err != nil || firstCursor.Version != 2 || firstCursor.MatchClass != 0 {
		t.Fatalf("first cursor = %#v, err %v", firstCursor, err)
	}
	second, err := s.QueryQ9(ctx, Q9Request{Text: "SQLITE", Limit: 1, Cursor: *first.NextCursor, Home: home})
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "newer-text" || second.NextCursor == nil {
		t.Fatalf("second page = %#v, err %v", second, err)
	}
	secondCursor, err := decodeKnowledgeCursor(*second.NextCursor, Q9Request{Text: "SQLITE", Limit: 1, Home: home}, nil, nil)
	if err != nil || secondCursor.MatchClass != 1 {
		t.Fatalf("second cursor = %#v, err %v", secondCursor, err)
	}
	third, err := s.QueryQ9(ctx, Q9Request{Text: "SQLITE", Limit: 1, Cursor: *second.NextCursor, Home: home})
	if err != nil || len(third.Items) != 1 || third.Items[0].ID != "older-text" || third.NextCursor == nil {
		t.Fatalf("third page = %#v, err %v", third, err)
	}
	fourth, err := s.QueryQ9(ctx, Q9Request{Text: "SQLITE", Limit: 1, Cursor: *third.NextCursor, Home: home})
	if err != nil || len(fourth.Items) != 0 || fourth.NextCursor != nil {
		t.Fatalf("fourth page = %#v, err %v", fourth, err)
	}
	legacy, err := encodeKnowledgeCursor(knowledgeCursor{Version: 1, Text: "SQLITE", HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID, HeadRef: home.HeadRef, CompletedAt: first.Items[0].CompletedAt, ID: first.Items[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.QueryQ9(ctx, Q9Request{Text: "SQLITE", Limit: 1, Cursor: legacy, Home: home})
	assertFailureKind(t, err, KindInvalidCursor)
}

func TestQueryQ9StructuredTextExactFieldsAreCaseInsensitiveAndUnique(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo,
		manifestFixture{ID: "title-match", Kind: "decision", Path: "docs/decisions/CD-0101-title.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "SQLite", Summary: "title", Tags: []string{"title"}, Scopes: KnowledgeRecordScopes{Mode: "home"}},
		manifestFixture{ID: "tag-match", Kind: "lesson", Path: "docs/lessons/tag.md", Status: "published", Date: "2026-08-09T00:00:00Z", Title: "Tag lesson", Summary: "tag", Tags: []string{"SQLITE"}, Scopes: KnowledgeRecordScopes{Mode: "explicit", TagIDs: []string{"SQLITE"}}},
		manifestFixture{ID: "domain-match", Kind: "spec", Path: "docs/specs/domain.md", Status: "accepted", Date: "2026-08-08T00:00:00Z", Title: "Domain spec", Summary: "domain", Tags: []string{"domain"}, Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"SQLITE"}}},
	)
	commitKnowledgeRepo(t, repo, "structured exact fields")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-exact-fields", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}

	result, err := s.QueryQ9(ctx, Q9Request{Text: "sqlite", Home: home})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.ID)
	}
	want := []string{"title-match", "tag-match", "domain-match"}
	if !equalStrings(ids, want) {
		t.Fatalf("exact-field IDs = %#v, want %#v", ids, want)
	}
}

func knowledgeProjectionSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	parts := make([]string, 0, 3)
	for _, query := range []string{
		`SELECT id,type,title,completed_at,outcome_tag,lesson_tags,summary,scope_mode,note_path,commit_oid,content_hash FROM archived_work ORDER BY id`,
		`SELECT work_id,product_id FROM archived_work_products ORDER BY work_id,product_id`,
		`SELECT work_id,project_id FROM archived_work_projects ORDER BY work_id,project_id`,
		`SELECT work_id,component_id FROM archived_work_components ORDER BY work_id,component_id`,
		`SELECT work_id,tag_id FROM archived_work_tags ORDER BY work_id,tag_id`,
		`SELECT kind,coverage,reason,scanned_commit_oid FROM knowledge_kind_coverage ORDER BY kind`,
		`SELECT law_id,kind,status,path,title,content_hash,scanned_commit_oid FROM law_subjects ORDER BY home_project_id,home_locator_id,law_id`,
		`SELECT source_law_id,kind,target_law_id,scanned_commit_oid FROM law_relations ORDER BY home_project_id,home_locator_id,source_law_id,kind,target_law_id`,
		`SELECT scanned_commit_oid,scanned_at,complete FROM knowledge_index_watermark ORDER BY home_project_id,home_locator_id,head_ref`,
	} {
		rows, err := s.DatabaseForTesting().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			fields := make([]string, len(values))
			for i, value := range values {
				fields[i] = fmt.Sprint(value)
			}
			lines = append(lines, strings.Join(fields, "|"))
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, strings.Join(lines, ";"))
	}
	return strings.Join(parts, "\n")
}

func TestKnowledgeCoverageDistinguishesIndexedEmptyFromUnavailableResearch(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo)
	commitKnowledgeRepo(t, repo, "empty manifest")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-coverage", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	empty, err := s.QueryQ9(ctx, Q9Request{Kinds: []string{"lesson"}, Home: home})
	if err != nil || empty.Authority != "authoritative" || len(empty.Items) != 0 {
		t.Fatalf("indexed empty=%#v err=%v", empty, err)
	}
	_, err = s.QueryQ9(ctx, Q9Request{Kinds: []string{"research"}, Home: home})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindKnowledgeUnavailable || len(failure.UnavailableKinds) != 1 || failure.UnavailableKinds[0] != "research" {
		t.Fatalf("research unavailable=%v", err)
	}
}

func TestKnowledgeScopeModesAreStructural(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo,
		manifestFixture{ID: "home-decision", Kind: "decision", Path: "docs/decisions/CD-0001-home.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Home", Summary: "Home summary", Scopes: KnowledgeRecordScopes{Mode: "home"}},
		manifestFixture{ID: "explicit-decision", Kind: "decision", Path: "docs/decisions/CD-0002-explicit.md", Status: "accepted", Date: "2026-08-09T00:00:00Z", Title: "Explicit", Summary: "Explicit summary", Scopes: KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{"product-a"}, ProjectIDs: []string{"project-a"}}},
	)
	commitKnowledgeRepo(t, repo, "scope modes")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project-home", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-a", home, "project-a", "project-b")
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, product, project string
		want                   []string
	}{
		{"home resolved to other project", "product-a", "project-b", []string{"home-decision"}},
		{"explicit matching", "product-a", "project-a", []string{"explicit-decision", "home-decision"}},
		{"explicit not matching", "product-a", "project-b", []string{"home-decision"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := s.QueryQ9(ctx, Q9Request{Product: test.product, Project: test.project, Home: home})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(result.Items))
			for _, item := range result.Items {
				got = append(got, item.ID)
			}
			if len(got) != len(test.want) {
				t.Fatalf("items=%v want=%v", got, test.want)
			}
		})
	}
	if result, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "explicit-decision", Product: "product-a"}); err != nil || result.Status != "canonical" {
		t.Fatalf("explicit Q10 in declared Product=%#v err=%v", result, err)
	}
	if _, err := s.QueryQ10(ctx, Q10Request{KnowledgeID: "explicit-decision", Product: "product-b"}); err == nil {
		t.Fatal("explicit Q10 record escaped its frozen Product scope")
	} else {
		assertFailureKind(t, err, KindUnknownScope)
	}
}

func TestHomeScopeRoutingDoesNotLeakAcrossCanonicalHomes(t *testing.T) {
	ctx := context.Background()
	firstRepo := initKnowledgeRepo(t)
	writeManifestFixture(t, firstRepo,
		manifestFixture{ID: "home-first", Kind: "decision", Path: "docs/decisions/CD-0001-home-first.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "First home", Summary: "First home summary", Scopes: KnowledgeRecordScopes{Mode: "home"}},
		manifestFixture{ID: "explicit-first", Kind: "decision", Path: "docs/decisions/CD-0002-explicit-first.md", Status: "accepted", Date: "2026-08-09T00:00:00Z", Title: "First explicit", Summary: "First explicit summary", Scopes: KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{"product-a"}, ProjectIDs: []string{"project-a"}, DomainIDs: []string{"domain-a"}}},
	)
	commitKnowledgeRepo(t, firstRepo, "first canonical home")
	secondRepo := initKnowledgeRepo(t)
	writeManifestFixture(t, secondRepo,
		manifestFixture{ID: "home-second", Kind: "decision", Path: "docs/decisions/CD-0003-home-second.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Second home", Summary: "Second home summary", Scopes: KnowledgeRecordScopes{Mode: "home"}},
		manifestFixture{ID: "explicit-second", Kind: "decision", Path: "docs/decisions/CD-0004-explicit-second.md", Status: "accepted", Date: "2026-08-09T00:00:00Z", Title: "Second explicit", Summary: "Second explicit summary", Scopes: KnowledgeRecordScopes{Mode: "explicit", ProductIDs: []string{"product-b"}, ProjectIDs: []string{"project-b"}, DomainIDs: []string{"domain-b"}}},
	)
	commitKnowledgeRepo(t, secondRepo, "second canonical home")
	s := openTemp(t)
	firstHome := KnowledgeHome{HomeProjectID: "project-first", HomeLocatorID: "locator-first", RepoPath: firstRepo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-a", firstHome, "project-a", "project-b")
	secondHome := KnowledgeHome{HomeProjectID: "project-second", HomeLocatorID: "locator-second", RepoPath: secondRepo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-b", secondHome, "project-a", "project-b")
	if err := s.RebuildKnowledgeIndex(ctx, firstHome); err != nil {
		t.Fatal(err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, secondHome); err != nil {
		t.Fatal(err)
	}
	queryIDs := func(home KnowledgeHome, product, project, domain string) []string {
		result, err := s.QueryQ9(ctx, Q9Request{Product: product, Project: project, Domain: domain, Home: home})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			ids = append(ids, item.ID)
		}
		return ids
	}
	firstIDs := queryIDs(firstHome, "product-a", "project-a", "domain-a")
	if len(firstIDs) != 1 || !containsKnowledgeString(firstIDs, "explicit-first") || containsKnowledgeString(firstIDs, "home-first") || containsKnowledgeString(firstIDs, "home-second") || containsKnowledgeString(firstIDs, "explicit-second") {
		t.Fatalf("first-home routing=%v", firstIDs)
	}
	firstHomeIDs := queryIDs(firstHome, "product-a", "project-b", "")
	if len(firstHomeIDs) != 1 || !containsKnowledgeString(firstHomeIDs, "home-first") || containsKnowledgeString(firstHomeIDs, "home-second") || containsKnowledgeString(firstHomeIDs, "explicit-first") || containsKnowledgeString(firstHomeIDs, "explicit-second") {
		t.Fatalf("first home scope visibility=%v", firstHomeIDs)
	}
	secondIDs := queryIDs(secondHome, "product-b", "project-b", "domain-b")
	if len(secondIDs) != 1 || !containsKnowledgeString(secondIDs, "explicit-second") || containsKnowledgeString(secondIDs, "home-second") || containsKnowledgeString(secondIDs, "home-first") || containsKnowledgeString(secondIDs, "explicit-first") {
		t.Fatalf("second-home routing=%v", secondIDs)
	}
	secondHomeIDs := queryIDs(secondHome, "product-b", "project-a", "")
	if len(secondHomeIDs) != 1 || !containsKnowledgeString(secondHomeIDs, "home-second") || containsKnowledgeString(secondHomeIDs, "home-first") || containsKnowledgeString(secondHomeIDs, "explicit-first") || containsKnowledgeString(secondHomeIDs, "explicit-second") {
		t.Fatalf("second home scope visibility=%v", secondHomeIDs)
	}
	if got := queryIDs(firstHome, "product-a", "project-b", "domain-b"); len(got) != 0 {
		t.Fatalf("cross-scope explicit records leaked into first home: %v", got)
	}
}

func containsKnowledgeString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestManifestFailureLeavesPriorProjectionAndCoverageUnchanged(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	path := "docs/decisions/CD-0099-rollback.md"
	writeManifestFixture(t, repo, manifestFixture{ID: "rollback", Kind: "decision", Path: path, Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Rollback", Summary: "Stable summary", Scopes: KnowledgeRecordScopes{Mode: "home"}})
	firstCommit := commitKnowledgeRepo(t, repo, "good manifest")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-rollback", home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.DatabaseForTesting().QueryRow(`SELECT id||'|'||commit_oid||'|'||content_hash FROM archived_work WHERE id='rollback'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Change the referenced blob without changing authored metadata or its old
	// projection; the new commit must be rejected before the replacement tx.
	writeKnowledgeFile(t, repo, path, "tampered bytes\n")
	secondCommit := commitKnowledgeRepo(t, repo, "tampered blob")
	if secondCommit == firstCommit {
		t.Fatal("tamper fixture did not advance commit")
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err == nil {
		t.Fatal("tampered manifest blob was accepted")
	}
	var after string
	if err := s.DatabaseForTesting().QueryRow(`SELECT id||'|'||commit_oid||'|'||content_hash FROM archived_work WHERE id='rollback'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("failed rebuild changed prior projection: before=%s after=%s", before, after)
	}
}

func TestManifestAndWorkNoteStableIDCollisionFailsClosed(t *testing.T) {
	repo := initKnowledgeRepo(t)
	workPath := "docs/work/2026-08-10-collision.md"
	writeKnowledgeFile(t, repo, workPath, canonicalWorkNote("collision", "2026-08-10T00:00:00Z"))
	writeManifestFixture(t, repo, manifestFixture{ID: "collision", Kind: "lesson", Path: "docs/lessons/collision.md", Status: "published", Date: "2026-08-10T00:00:00Z", Title: "Collision", Summary: "Collision summary", Scopes: KnowledgeRecordScopes{Mode: "home"}})
	commitKnowledgeRepo(t, repo, "cross-population collision")
	if err := openTemp(t).RebuildKnowledgeIndex(context.Background(), KnowledgeHome{HomeProjectID: "p", HomeLocatorID: "l", RepoPath: repo, HeadRef: "HEAD"}); err == nil {
		t.Fatal("cross-population stable ID collision was accepted")
	} else {
		assertFailureKind(t, err, KindKnowledgeAmbiguous)
	}
}

func TestLegacyManifestAbsenceCoverageIsExplicit(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeKnowledgeFile(t, repo, "README.md", "legacy home\n")
	commitKnowledgeRepo(t, repo, "legacy home without manifest")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "project", HomeLocatorID: "locator", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeLocator(t, s, home)
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
	rows, err := s.DatabaseForTesting().Query(`SELECT kind,coverage FROM knowledge_kind_coverage WHERE home_project_id='project' ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	coverage := map[string]string{}
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			t.Fatal(err)
		}
		coverage[kind] = value
	}
	if coverage["work_note"] != "indexed" || coverage["lesson"] != "supported_not_indexed" || coverage["decision"] != "supported_not_indexed" || coverage["spec"] != "supported_not_indexed" || coverage["research"] != "supported_not_indexed" {
		t.Fatalf("legacy coverage=%v", coverage)
	}
}
