package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestCommittedUniqueJSONFactFitsBound(t *testing.T) {
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
	for _, selector := range []DomainShapeSelector{
		{Path: "internal/payloadschema/unique_json.go"},
		{Subject: "agent-unique-json"},
	} {
		selection := selectDomainShape(*snapshot, KnowledgeHome{HomeProjectID: "concord", HomeLocatorID: "main"}, "concord", selector)
		if !slices.Equal(selection.DomainIDs, []string{"agent-surface"}) || len(selection.Roots) != 1 || selection.Roots[0].Path != "internal/payloadschema" {
			t.Fatalf("selector %+v has wrong ownership: %+v", selector, selection.Roots)
		}
		found := false
		for _, item := range selection.Facts {
			if item.Fact.ID != "agent-unique-json" {
				continue
			}
			fact := item.Fact
			if fact.Kind != DomainFactInvariant || fact.Standing != "observed" || fact.OwnerDomainID != "agent-surface" || fact.OwningRoot != "internal/payloadschema" || fact.Invariant == nil || !slices.Equal(fact.Invariant.AppliesToDomainIDs, []string{"agent-surface"}) {
				t.Fatalf("wrong JSON admission fact: %+v", fact)
			}
			found = true
		}
		if !found {
			t.Fatalf("selector %+v omitted agent-unique-json", selector)
		}
		selection.GoverningLaw = domainShapeManifestGoverningLaw(manifest, selection)
		bounded, err := boundDomainShapeResult(selection)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(bounded)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("selector=%+v result_bytes=%d", selector, len(raw))
	}
}
