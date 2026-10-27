package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		"sqlite-installation-key":        DomainFactInvariant,
		"sqlite-typed-constraints":       DomainFactInvariant,
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

// The committed corpus places many laws on durable-authority's parent. Fact
// reads for the SQLite mechanism must stay inside the result bound, holding
// only law homed to, applicable to, or cited by the selected Domain.
func TestCommittedSQLiteMechanismShapeFitsBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := repositoryRootForTest(t)
	runner := CommittedReadGitRunner{}
	head, err := runner.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(head))
	manifest, shards, missing, err := readKnowledgeManifestShardsRunner(ctx, runner, root, commit, manifestSharedHomeRole)
	if err != nil || missing {
		t.Fatalf("committed manifest missing=%v: %v", missing, err)
	}
	architecture, err := prepareKnowledgeArchitecture(shards, manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &newDomainShapeProjection(commit, manifest, architecture).Snapshot
	if err := reconstructDomainShapeReadingSnapshot(ctx, runner, &dispatchReadingWitness{sourceRepo: root}, shards, snapshot); err != nil {
		t.Fatal(err)
	}
	selector := DomainShapeSelector{Path: "internal/store/internal/sqlitedb/snapshot.go"}
	selection := selectDomainShape(*snapshot, KnowledgeHome{HomeProjectID: "concord", HomeLocatorID: "main"}, "concord", selector)
	if !slices.Equal(selection.DomainIDs, []string{"durable-authority"}) || len(selection.Facts) == 0 {
		t.Fatalf("mechanism path selects Domains %v with %d facts", selection.DomainIDs, len(selection.Facts))
	}
	selection.GoverningLaw = domainShapeManifestGoverningLaw(manifest, selection)
	if _, err := boundDomainShapeResult(selection); err != nil {
		t.Fatal(err)
	}
}
