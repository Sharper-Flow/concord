package store

import (
	"reflect"
	"testing"
)

func TestCatalogActionUsesCurrentCatalog(t *testing.T) {
	catalog := builtinWorkflowActionCatalog()
	for id := range catalog {
		want, err := catalog.action(id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := catalogAction(id)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("current action %s = %+v, %v; want %+v", id, got, err, want)
		}
	}
	if _, err := catalogAction("unknown_action"); err == nil {
		t.Fatal("unknown action resolved through the catalog")
	}
}
