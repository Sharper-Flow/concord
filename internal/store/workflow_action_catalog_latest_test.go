package store

import (
	"reflect"
	"testing"
)

func TestLatestCatalogActionUsesLatestContainingCatalog(t *testing.T) {
	catalogs := builtinWorkflowActionCatalogs()
	for _, catalog := range catalogs {
		for id := range catalog.revisions {
			want, err := catalog.action(id)
			if err != nil {
				t.Fatal(err)
			}
			for _, later := range catalogs {
				if later.Version <= catalog.Version {
					continue
				}
				if _, declared := later.revisions[id]; declared {
					want, err = later.action(id)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := latestCatalogAction(id)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("latest action %s = %+v, %v; want %+v", id, got, err, want)
			}
		}
	}
	if _, err := latestCatalogAction("unknown_action"); err == nil {
		t.Fatal("unknown action resolved through the catalog")
	}
}
