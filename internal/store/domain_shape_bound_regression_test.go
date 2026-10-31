package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCommittedAgentSurfaceAndMixedRootShapeFitBound(t *testing.T) {
	ctx := context.Background()
	root := repositoryRootForTest(t)
	runner := runGitRunner{}
	head, err := runner.Run(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(string(head))
	manifest, shards, missing, err := readKnowledgeManifestShardsRunner(ctx, runner, root, commit)
	if err != nil || missing {
		t.Fatalf("committed manifest missing=%v: %v", missing, err)
	}
	architecture, err := prepareKnowledgeArchitecture(shards, manifest, true)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := &newDomainShapeProjection(commit, manifest, architecture).Snapshot
	if err := populateDomainShapeCorpusSnapshot(ctx, root, shards, snapshot); err != nil {
		t.Fatal(err)
	}
	selectors := []DomainShapeSelector{
		{DomainID: "agent-surface"},
		{Path: "internal/store/worker_diff_read.go"},
	}
	for _, domain := range manifest.DomainRegistry.Domains {
		if domain.ParentDomainID == "agent-surface" {
			selectors = append(selectors, DomainShapeSelector{DomainID: domain.DomainID})
		}
	}
	for _, selector := range selectors {
		t.Run(selector.DomainID+selector.Path, func(t *testing.T) {
			selection := selectDomainShape(*snapshot, KnowledgeHome{HomeProjectID: "concord", HomeLocatorID: "main"}, "concord", selector)
			selection.GoverningLaw = domainShapeCorpusGoverningLaw(manifest, selection)
			raw, _ := json.Marshal(selection)
			facts, _ := json.Marshal(selection.Facts)
			laws, _ := json.Marshal(selection.GoverningLaw)
			roots, _ := json.Marshal(selection.Roots)
			t.Logf("result_bytes=%d facts=%d/%d laws=%d/%d roots=%d/%d domains=%v", len(raw), len(selection.Facts), len(facts), len(selection.GoverningLaw), len(laws), len(selection.Roots), len(roots), selection.DomainIDs)
			if _, err := boundDomainShapeResult(selection); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDomainShapeMixedRootLawIDsPreserveCompleteContext(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	home, product := domainShapeProjectionFixture(t, s, "a")
	fixtures := make([]manifestFixture, 200)
	for i := range fixtures {
		id := fmt.Sprintf("CD-%04d", i+1)
		fixtures[i] = manifestFixture{ID: id, Kind: "decision", Path: ".concord/docs/decisions/" + id + "-synthetic.md", Status: "accepted", Date: "2026-08-10T00:00:00Z", Title: "Synthetic governing law " + id, Summary: "Synthetic bounded context", Scopes: KnowledgeRecordScopes{Mode: "explicit", DomainIDs: []string{"fixture-law", "domain-b"}}}
	}
	writeManifestFixture(t, home.RepoPath, fixtures...)
	commit := commitKnowledgeRepo(t, home.RepoPath, "synthetic complete mixed-root law context")
	manifest := composeWorkingTreeManifest(t, home.RepoPath)
	want := make([]string, len(manifest.Records))
	for i, record := range manifest.Records {
		want[i] = record.ID
	}
	for _, selector := range []DomainShapeSelector{{DomainID: "fixture-law"}, {Path: "legacy/file.go"}} {
		shape, err := s.QueryDomainShape(ctx, product, selector)
		if err != nil {
			t.Fatal(err)
		}
		if shape.KnowledgeCommit != commit || !reflect.DeepEqual(shape.GoverningLaw, want) || !reflect.DeepEqual(shape.DomainIDs, []string{"domain-b", "fixture-law"}) {
			t.Fatalf("law revisions or mixed-root ownership were lost: %+v", shape)
		}
		raw, err := json.Marshal(shape)
		if err != nil || len(raw) > DomainShapeResultMaxBytes {
			t.Fatalf("result bytes=%d: %v", len(raw), err)
		}
		t.Logf("selector=%+v result_bytes=%d laws=%d", selector, len(raw), len(shape.GoverningLaw))
	}
	footprint, err := s.QueryDomainFootprint(ctx, DomainFootprintRequest{Product: product, Paths: []string{"legacy/file.go"}, Limit: 1})
	if err != nil || len(footprint.Shapes) != 1 || !reflect.DeepEqual(footprint.Shapes[0].GoverningLaw, want) || len(footprint.Laws) != 1 || footprint.NextCursor == nil {
		t.Fatalf("minimum-page footprint lost complete shape context or pagination: %+v %v", footprint, err)
	}
}
