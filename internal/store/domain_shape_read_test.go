package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// The same questions deliberately have different answers in the two homes.
// Expected facts come from the committed fixture inputs, not the read core.
func domainShapeGoldenFixture(t *testing.T, s *Store, suffix string) (KnowledgeHome, string, []DomainFact) {
	t.Helper()
	home, product := domainShapeProjectionFixture(t, s, suffix)
	facts := []DomainFact{}
	for _, kind := range []DomainFactKind{DomainFactInvariant, DomainFactDataObject, DomainFactStateAuthority, DomainFactConfigCoupling} {
		raw := runKnowledgeGit(t, home.RepoPath, "show", "HEAD:"+knowledgeFactTreeDir+string(kind)+"-"+suffix+".json")
		for i := 0; i < 5; i++ {
			var fact DomainFact
			if err := json.Unmarshal([]byte(raw), &fact); err != nil {
				t.Fatal(err)
			}
			fact.ID = fmt.Sprintf("%s-%s-%d", kind, suffix, i)
			switch kind {
			case DomainFactInvariant:
				fact.Invariant.Statement = fmt.Sprintf("Only Product %s owner writes partition %d.", suffix, i)
			case DomainFactDataObject:
				fact.DataObject.Name = fmt.Sprintf("rows_%d", i)
			case DomainFactStateAuthority:
				fact.StateAuthority.State = fmt.Sprintf("account state %d", i)
			case DomainFactConfigCoupling:
				fact.ConfigCoupling.Setting = fmt.Sprintf("CACHE_LIMIT_%d", i)
			}
			writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+fact.ID+".json", string(marshalDomainFact(t, fact))+"\n")
			facts = append(facts, fact)
		}
	}
	commitKnowledgeRepo(t, home.RepoPath, "synthetic golden questions")
	return home, product, facts
}

func goldenDomainShapeSubject(fact DomainFact) string {
	switch fact.Kind {
	case DomainFactDataObject:
		return fact.DataObject.Name
	case DomainFactStateAuthority:
		return fact.StateAuthority.State
	case DomainFactConfigCoupling:
		return fact.ConfigCoupling.Setting
	default:
		return fact.ID
	}
}

func TestDomainShapeReadGoldenQuestionsAndProductIsolation(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	a, productA, factsA := domainShapeGoldenFixture(t, s, "a")
	b, productB, factsB := domainShapeGoldenFixture(t, s, "b")
	for _, fixture := range []struct {
		home         KnowledgeHome
		product, law string
		facts        []DomainFact
	}{{a, productA, "CD-0001", factsA}, {b, productB, "CD-0002", factsB}} {
		for _, fact := range fixture.facts {
			t.Run(fixture.product+"/"+fact.ID, func(t *testing.T) {
				selector := DomainShapeSelector{Subject: goldenDomainShapeSubject(fact)}
				got, err := s.QueryDomainShape(ctx, fixture.product, selector)
				if err != nil {
					t.Fatal(err)
				}
				raw := runKnowledgeGit(t, fixture.home.RepoPath, "show", "HEAD:"+knowledgeFactTreeDir+fact.ID+".json")
				want := []DomainShapeFact{{Fact: fact, Path: knowledgeFactTreeDir + fact.ID + ".json", ContentHash: domainShapeBytesHash([]byte(raw))}}
				if !reflect.DeepEqual(got.Facts, want) || !reflect.DeepEqual(got.Roots, []DomainShapeRoot{{Path: "src", Role: "domain", OwnerDomainID: "fixture-law"}}) || !reflect.DeepEqual(got.DomainIDs, []string{"fixture-law"}) {
					t.Fatalf("golden answer differs: %+v", got)
				}
				if len(got.GoverningLaw) != 1 || got.GoverningLaw[0].LawID != fixture.law || !domainShapeHash.MatchString(got.GoverningLaw[0].ContentHash) {
					t.Fatalf("missing or foreign governing law identity: %+v", got.GoverningLaw)
				}
				if got.ProductID != fixture.product || got.HomeProjectID != fixture.home.HomeProjectID || got.HomeLocatorID != fixture.home.HomeLocatorID || got.Selector != selector || got.KnowledgeCommit != headCommit(t, fixture.home.RepoPath) {
					t.Fatalf("wrong result identity: %+v", got)
				}
				if got.ReferenceVerification != "unverified" || got.ExtractorVerification != "unverified" || !domainShapeHash.MatchString(got.SnapshotContentHash) || !domainShapeHash.MatchString(got.RegistryContentHash) || !domainShapeHash.MatchString(got.PackageMapContentHash) {
					t.Fatal("declarations became verification or lost source pins", got)
				}
				bytes, err := json.Marshal(got)
				if err != nil || len(bytes) > DomainShapeResultMaxBytes {
					t.Fatalf("result bytes %d: %v", len(bytes), err)
				}
				t.Logf("golden %s %s: %d serialized bytes (bound %d)", fixture.product, fact.ID, len(bytes), DomainShapeResultMaxBytes)
				again, err := s.QueryDomainShape(ctx, fixture.product, selector)
				second, _ := json.Marshal(again)
				if err != nil || string(bytes) != string(second) {
					t.Fatal("same revision did not reproduce exact result bytes", err)
				}
			})
		}
	}
	got, err := s.QueryDomainShape(ctx, productA, DomainShapeSelector{Subject: "invariant-b-0"})
	if err != nil || len(got.Facts) != 0 || len(got.Roots) != 0 || len(got.GoverningLaw) != 0 {
		t.Fatal("foreign exact subject leaked", got, err)
	}
	tx, err := beginReadTx(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := selectedDomainShapeTx(ctx, tx, a, productB, DomainShapeSelector{Path: "src"}); err == nil {
		t.Fatal("tx core admitted foreign Product/home pair")
	}
}

func TestDomainShapeReadSelectorsAndTypedSubjects(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	home, product := domainShapeProjectionFixture(t, s, "a")
	for _, selector := range []DomainShapeSelector{
		{}, {Path: "src", Subject: "thing"}, {Path: "src", DomainID: "fixture-law", Subject: "thing"},
		{Path: "../src"}, {Path: "/src"}, {Path: "src/./file"}, {Path: "src\\file"}, {Path: "a\x00b"},
		{Path: strings.Repeat("a", footprintMaxPath+1)}, {DomainID: strings.Repeat("a", maxManifestID+1)},
		{Subject: " "}, {Subject: "a\nb"}, {Subject: strings.Repeat("é", DomainFactMaxTextBytes)}, {Subject: "\xff"},
	} {
		if _, err := s.QueryDomainShape(ctx, product, selector); err == nil {
			t.Fatalf("invalid selector admitted: %+v", selector)
		}
	}
	if _, err := s.QueryDomainShape(ctx, "", DomainShapeSelector{Path: "src"}); err == nil {
		t.Fatal("empty Product admitted")
	}
	var missing *Store
	if _, err := missing.QueryDomainShape(ctx, product, DomainShapeSelector{Path: "src"}); err == nil {
		t.Fatal("missing store admitted")
	}
	for subject, ids := range map[string][]string{
		"database-a": {"data_object-a", "state_authority-a"}, "cache-a": {"state_authority-a"},
		"state_rows": {"data_object-a"}, "CACHE_LIMIT": {"config_coupling-a"}, "account state": {"state_authority-a"},
		"data_object-a": {"data_object-a"}, "State_rows": {}, "state_": {},
	} {
		got, err := s.QueryDomainShape(ctx, product, DomainShapeSelector{Subject: subject})
		if err != nil {
			t.Fatal(err)
		}
		actual := []string{}
		for _, fact := range got.Facts {
			actual = append(actual, fact.Fact.ID)
		}
		if !reflect.DeepEqual(actual, ids) {
			t.Fatalf("subject %q: %v, want %v", subject, actual, ids)
		}
	}
	if _, err := s.QueryDomainShape(ctx, product, DomainShapeSelector{DomainID: "unknown"}); err == nil {
		t.Fatal("unknown Domain admitted")
	}
	got, err := s.QueryDomainShape(ctx, product, DomainShapeSelector{DomainID: "domain-b"})
	if err != nil || len(got.Facts) != 0 || len(got.Roots) != 1 || got.Roots[0].Role != "mixed" {
		t.Fatal("Domain read inferred fact ownership from applicability", got, err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		t.Fatal(err)
	}
}

func TestDomainShapeReadLongestRootRolesAndScopedLaw(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	home, product := domainShapeProjectionFixture(t, s, "a")
	scopes := KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"fixture-law", "domain-b"}}
	writeManifestFixture(t, home.RepoPath,
		manifestFixture{ID: "CD-0001", Kind: "decision", Path: ".concord/docs/decisions/CD-0001-synthetic.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Synthetic law a", Summary: "Synthetic owner invariant", Scopes: scopes},
		manifestFixture{ID: "CD-0003", Kind: "decision", Path: ".concord/docs/decisions/CD-0003-root.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Synthetic root law", Summary: "Synthetic root law", RootHomed: true, Scopes: scopes})
	writeKnowledgeFile(t, home.RepoPath, knowledgePackageMapPath, `{"schema_version":"1.1","packages":[{"path":"src","role":"domain","owner_domain_id":"fixture-law"},{"path":"src/wiring","role":"composition"},{"path":"testsupport","role":"test_support"},{"path":"legacy","role":"mixed","domain_ids":["fixture-law","domain-b"]}]}`)
	writeKnowledgeFile(t, home.RepoPath, "src/wiring/entry.go", "package synthetic\n")
	fact, _ := domainFactFixture(DomainFactInvariant)
	fact.ID, fact.OwnerDomainID, fact.OwningRoot = "mixed-observation", "fixture-law", "legacy"
	fact.Invariant.AppliesToDomainIDs = []string{"fixture-law"}
	writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+fact.ID+".json", string(marshalDomainFact(t, fact)))
	commitKnowledgeRepo(t, home.RepoPath, "nested roles and scoped law")
	for _, test := range []struct {
		path, role string
		facts      int
		owners     []string
	}{
		{"src/file.go", "domain", 4, []string{"fixture-law"}},
		{"src/wiring/file.go", "composition", 0, []string{}},
		{"testsupport/file.go", "test_support", 0, []string{}},
		{"legacy/file.go", "mixed", 1, []string{"domain-b", "fixture-law"}},
		{"src-neighbor/file.go", "", 0, []string{}},
	} {
		got, err := s.QueryDomainShape(ctx, product, DomainShapeSelector{Path: test.path})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Facts) != test.facts || !reflect.DeepEqual(got.DomainIDs, test.owners) {
			t.Fatalf("%s: %+v", test.path, got)
		}
		if test.role != "" && (len(got.Roots) != 1 || got.Roots[0].Role != test.role) {
			t.Fatalf("%s role: %+v", test.path, got.Roots)
		}
		if len(test.owners) != 0 && (len(got.GoverningLaw) != 1 || got.GoverningLaw[0].LawID != "CD-0001") {
			t.Fatal("selected law includes parent placement alone or loses the direct home", got.GoverningLaw)
		}
		if test.role == "mixed" && (got.Facts[0].Fact.Standing != "observed" || len(got.Facts[0].Fact.GoverningLawIDs) != 0 || got.Roots[0].OwnerDomainID != "") {
			t.Fatal("mixed observation acquired exclusive ownership or law standing", got)
		}
	}
	root := composeWorkingTreeManifest(t, home.RepoPath).DomainRegistry.RootDomainID
	got, err := s.QueryDomainShape(ctx, product, DomainShapeSelector{DomainID: root})
	if err != nil || len(got.GoverningLaw) != 1 || got.GoverningLaw[0].LawID != "CD-0003" {
		t.Fatal("root law is not separately retrievable", got.GoverningLaw, err)
	}
}

func TestDomainShapeReadExactByteBoundAndNoPartialResult(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	home, product := domainShapeProjectionFixture(t, s, "a")
	facts := []DomainFact{}
	for i := 0; i < 40; i++ {
		fact, _ := domainFactFixture(DomainFactInvariant)
		fact.ID, fact.OwnerDomainID = fmt.Sprintf("bounded-%02d", i), "fixture-law"
		fact.Invariant.AppliesToDomainIDs = []string{"fixture-law"}
		fact.Invariant.Statement = "Synthetic."
		facts = append(facts, fact)
		writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+fact.ID+".json", string(marshalDomainFact(t, fact)))
	}
	commitKnowledgeRepo(t, home.RepoPath, "bounded result fixture")
	selector := DomainShapeSelector{DomainID: "fixture-law"}
	got, err := s.QueryDomainShape(ctx, product, selector)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	remaining := DomainShapeResultMaxBytes - len(raw)
	if remaining < 1 {
		t.Fatalf("fixture too large: %d", len(raw))
	}
	for i := range facts {
		padding := min(remaining, DomainFactMaxTextBytes-len(facts[i].Invariant.Statement)-1)
		facts[i].Invariant.Statement += strings.Repeat("x", padding)
		remaining -= padding
		writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+facts[i].ID+".json", string(marshalDomainFact(t, facts[i])))
	}
	if remaining != 0 {
		t.Fatal("insufficient valid padding capacity", remaining)
	}
	commitKnowledgeRepo(t, home.RepoPath, "exact result bound")
	got, err = s.QueryDomainShape(ctx, product, selector)
	raw, _ = json.Marshal(got)
	if err != nil || len(raw) != DomainShapeResultMaxBytes || len(got.Facts) != 44 {
		t.Fatalf("exact-bound result: %d bytes, %d facts, %v", len(raw), len(got.Facts), err)
	}
	t.Logf("exact-bound call: %d serialized bytes", len(raw))
	facts[0].Invariant.Statement += "x"
	writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+facts[0].ID+".json", string(marshalDomainFact(t, facts[0])))
	commitKnowledgeRepo(t, home.RepoPath, "one byte over result bound")
	got, err = s.QueryDomainShape(ctx, product, selector)
	assertFailureKind(t, err, KindLimitExceeded)
	if !reflect.DeepEqual(got, DomainShapeResult{}) {
		t.Fatal("overflow returned partial facts", got)
	}
	if _, err := s.QueryDomainShape(ctx, product, DomainShapeSelector{Subject: facts[0].ID}); err != nil {
		t.Fatal("narrow selection should remain available", err)
	}
}
