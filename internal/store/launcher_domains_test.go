package store

import (
	"context"
	"testing"
)

func TestQueryLauncherDomainsAggregatesHierarchyRelationsAndOverlaps(t *testing.T) {
	ctx := context.Background()
	s, _ := seedOverlapProjection(t, "nav-left", "nav-right", true)
	result, err := s.QueryLauncherDomains(ctx, LauncherProductRequest{Product: "product", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Registry == nil || result.Registry.RootDomainID != "root" {
		t.Fatalf("registry watermark missing: %#v", result.Registry)
	}
	if len(result.Domains) == 0 {
		t.Fatalf("domain hierarchy empty: %#v", result.Domains)
	}
	homeFound := false
	for _, domain := range result.Domains {
		if domain.HomeDomain {
			homeFound = true
		}
	}
	if !homeFound {
		t.Fatalf("home Domain not marked: %#v", result.Domains)
	}
	if len(result.Overlaps) != 1 || result.Overlaps[0].FromWorkID != "nav-left" || result.Overlaps[0].ToWorkID != "nav-right" || result.Overlaps[0].ResolutionState != "absent" {
		t.Fatalf("unresolved overlap not surfaced: %#v", result.Overlaps)
	}

	absent, err := s.QueryLauncherDomains(ctx, LauncherProductRequest{Product: "missing-product", Limit: 20})
	if err == nil {
		t.Fatalf("absent registry produced a page: %#v", absent)
	}
	assertFailureKind(t, err, KindDomainRegistryAbsent)
}
