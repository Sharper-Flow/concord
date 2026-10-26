package store

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDomainFootprintShapeUsesSelectedCoreAndProductHome(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	a, productA := domainShapeProjectionFixture(t, s, "a")
	b, productB := domainShapeProjectionFixture(t, s, "b")
	for _, fixture := range []struct {
		home    KnowledgeHome
		product string
	}{{a, productA}, {b, productB}} {
		if err := s.EnsureKnowledgeIndexFresh(ctx, fixture.home); err != nil {
			t.Fatal(err)
		}
		req := DomainFootprintRequest{Product: fixture.product, Paths: []string{"src/file.go", "legacy/file.go", "cmd/file.go", "testsupport/file.go", "unowned/file.go"}}
		got, err := s.QueryDomainFootprint(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Shapes) != len(req.Paths) {
			t.Fatalf("footprint lost shape selections: %+v", got)
		}
		for i, shape := range got.Shapes {
			direct, err := s.QueryDomainShape(ctx, fixture.product, shape.Selector)
			if err != nil || !reflect.DeepEqual(shape, direct) {
				t.Fatalf("footprint and standalone answers differ: %+v %+v %v", shape, direct, err)
			}
			if shape.ProductID != fixture.product || shape.HomeLocatorID != fixture.home.HomeLocatorID || !reflect.DeepEqual(shape.DomainIDs, got.Paths[i].DomainIDs) {
				t.Fatal("foreign Product or alternate ownership resolver", shape)
			}
			if len(shape.Roots) != 0 && shape.Roots[0].Path != got.Paths[i].Package {
				t.Fatal("footprint and shape selected different roots", shape, got.Paths[i])
			}
			if len(shape.Roots) == 1 && (shape.Roots[0].Role == "composition" || shape.Roots[0].Role == "test_support") && (got.Paths[i].Unmapped || len(shape.DomainIDs) != 0 || len(shape.Facts) != 0) {
				t.Fatal("ownerless role became unmapped or gained behavior ownership", shape, got.Paths[i])
			}
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("footprint %s: %d serialized Store result bytes", fixture.product, len(raw))
		prepared, err := s.prepareDomainFootprint(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := beginReadTx(ctx, s.db)
		if err != nil {
			t.Fatal(err)
		}
		complete, err := prepared.completeTx(ctx, tx)
		_ = tx.Rollback()
		if err != nil || !reflect.DeepEqual(got.Shapes, complete.Shapes) {
			t.Fatal("complete footprint did not use the same selection", err)
		}
		behavior, err := s.QueryDomainFootprint(ctx, DomainFootprintRequest{Product: fixture.product, Behavior: "Synthetic owner"})
		if err != nil || len(behavior.Shapes) != 0 || behavior.PackageMapContentHash == nil {
			t.Fatal("behavior-only read injected a shape overview or lost its map identity", behavior, err)
		}
	}
}

func TestDomainFootprintShapeRefusesOversizedSelection(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	home, product, facts := domainShapeGoldenFixture(t, s, "a")
	for _, fact := range facts {
		if fact.Kind != DomainFactInvariant {
			continue
		}
		for i := 0; i < 10; i++ {
			copy := fact
			copy.ID += strings.Repeat("x", i+1)
			writeKnowledgeFile(t, home.RepoPath, knowledgeFactTreeDir+copy.ID+".json", string(marshalDomainFact(t, copy)))
		}
	}
	commitKnowledgeRepo(t, home.RepoPath, "oversized footprint facts")
	if err := s.EnsureKnowledgeIndexFresh(ctx, home); err != nil {
		t.Fatal(err)
	}
	got, err := s.QueryDomainFootprint(ctx, DomainFootprintRequest{Product: product, Paths: []string{"src/file.go"}})
	assertFailureKind(t, err, KindLimitExceeded)
	if !reflect.DeepEqual(got, DomainFootprintResult{}) {
		t.Fatal("oversized footprint returned a partial selection")
	}
}
