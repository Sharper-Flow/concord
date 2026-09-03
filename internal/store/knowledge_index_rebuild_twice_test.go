package store

import (
	"context"
	"testing"
)

// The clear order is a hand list. This derives the truth from SQLite's own
// foreign-key metadata on a migrated store and refuses any list where a
// table is cleared after one that references it. Every edge is read before
// any is judged: the pool holds one connection, and a second query while a
// result set is open would park forever.
func TestDerivedKnowledgeClearOrderRespectsForeignKeys(t *testing.T) {
	s := openTemp(t)
	db := s.DatabaseForTesting()
	position := map[string]int{}
	for i, table := range derivedKnowledgeClearOrder {
		position[table] = i
	}
	edges := map[string]map[string]bool{}
	for _, table := range derivedKnowledgeClearOrder {
		rows, err := db.Query(`SELECT "table" FROM pragma_foreign_key_list(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		edges[table] = map[string]bool{}
		for rows.Next() {
			var referenced string
			if err := rows.Scan(&referenced); err != nil {
				t.Fatal(err)
			}
			edges[table][referenced] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	for _, table := range derivedKnowledgeClearOrder {
		for referenced := range edges[table] {
			refPos, derived := position[referenced]
			if !derived || referenced == table {
				continue // a non-derived referent (products, work_items), or a parent self-reference
			}
			if edges[referenced][table] {
				continue // domains <-> domain_registries reference each other; both clear in one transaction
			}
			if refPos <= position[table] {
				t.Errorf("%s references %s but is cleared after it (positions %d, %d)", table, referenced, position[table], refPos)
			}
		}
	}
}

// A rebuild clears every derived law table before it re-projects them, and
// the clear must run in dependency order. law_relations, law_domain_homes,
// law_domain_applicability, and domain_relation_governing_laws all reference
// law_subjects. The rebuild cleared law_relations and law_subjects first, so
// on any home whose registry names a governing law the second rebuild
// violated a foreign key and every strict read refused. RebuildKnowledgeIndex
// had no production caller until #708, so no populated store had ever
// rebuilt twice, and the fixtures name no governing laws. This one does.
func TestKnowledgeIndexRebuildsTwiceWithGoverningLaws(t *testing.T) {
	ctx := context.Background()
	repo := initKnowledgeRepo(t)
	writeManifestFixture(t, repo,
		manifestFixture{ID: "CD-0001", Kind: "decision", Path: "docs/decisions/CD-0001-alpha-law.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Alpha law", Summary: "Governs alpha", Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"alpha"}}},
		manifestFixture{ID: "CD-0002", Kind: "decision", Path: "docs/decisions/CD-0002-zeta-law.md", Status: "accepted", Date: "2026-08-11T00:00:00Z", Title: "Zeta law", Summary: "Governs zeta", Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"zeta"}}},
	)
	setManifestGoverningLaw(t, repo, "zeta", "alpha", "CD-0001")
	commitKnowledgeRepo(t, repo, "laws with a governing relation")
	s := openTemp(t)
	home := KnowledgeHome{HomeProjectID: "proj", HomeLocatorID: "loc", RepoPath: repo, HeadRef: "HEAD"}
	authorizeKnowledgeProductHome(t, s, "product-a", home, home.HomeProjectID)

	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	var governing int
	if err := s.DatabaseForTesting().QueryRow(`SELECT COUNT(*) FROM domain_relation_governing_laws`).Scan(&governing); err != nil {
		t.Fatal(err)
	}
	if governing == 0 {
		t.Fatal("fixture premise: the first rebuild projected no governing-law row, so a second rebuild proves nothing")
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatalf("second rebuild on a populated home: %v", err)
	}
}
