package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCommittedSQLiteMechanismFacts(t *testing.T) {
	t.Parallel()
	root := repositoryRootForTest(t)
	manifest := composeWorkingTreeManifest(t, root)
	read := func(t *testing.T, path string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	packages, err := parseKnowledgePackageMap(read(t, knowledgePackageMapPath), manifest.DomainRegistry)
	if err != nil {
		t.Fatal(err)
	}
	tooling, err := ParseProjectToolingManifest(read(t, projectToolingManifestPath))
	if err != nil || tooling.DomainShape == nil {
		t.Fatalf("missing shape tooling declaration: %v", err)
	}
	refs := DomainFactReferences{
		Registry: manifest.DomainRegistry, Roots: packages.domainFactRoots(),
		ValidationMode: DomainFactUnverifiedReferences,
		ProjectIDs:     map[string]bool{"concord": true}, RequiredCheckIDs: map[string]bool{},
	}
	for _, check := range tooling.DomainShape.RequiredChecks {
		refs.RequiredCheckIDs[check] = true
	}
	for id, kind := range map[string]DomainFactKind{
		"sqlite-single-connection":       DomainFactInvariant,
		"sqlite-busy-timeout":            DomainFactConfigCoupling,
		"sqlite-current-row-write-guard": DomainFactInvariant,
		"sqlite-effect-receipt-order":    DomainFactInvariant,
		"sqlite-finite-snapshot":         DomainFactInvariant,
	} {
		t.Run(id, func(t *testing.T) {
			fact, err := ParseDomainFact(id+".json", read(t, knowledgeFactTreeDir+id+".json"), refs)
			if err != nil {
				t.Fatal(err)
			}
			if fact.Kind != kind || fact.Standing != "observed" || fact.OwnerDomainID != "durable-authority" || fact.OwningRoot != "internal/store/internal/sqlitedb" {
				t.Fatalf("wrong mechanism ownership or standing: %+v", fact)
			}
			if owner := refs.Roots[fact.OwningRoot]; owner.Role != "domain" || len(owner.OwnerDomainIDs) != 1 || owner.OwnerDomainIDs[0] != fact.OwnerDomainID {
				t.Fatalf("mechanism is not exclusively owned: %+v", owner)
			}
		})
	}
}
