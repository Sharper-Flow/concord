package store

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestUpgradePreservesRetainedWork(t *testing.T) {
	db := openMigratedTo(t, filepath.Join(t.TempDir(), "store.db"), 199)
	retirementUpgradeExec(t, db, `INSERT INTO write_guard(active) VALUES(1)`)
	retirementUpgradeExec(t, db, `INSERT INTO work_items(id,kind,title,lifecycle,version,created_at,updated_at,intent_json) VALUES('work-bootstrap','task','Bootstrap','needed',2,'now','now','{"title":"Bootstrap","kind":"task"}')`)
	retirementUpgradeExec(t, db, `DELETE FROM write_guard`)
	const query = `SELECT id,kind,title,lifecycle,version,created_at,updated_at,intent_json FROM work_items WHERE id='work-bootstrap'`
	before := retirementUpgradeRows(t, db, query)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if got := retirementUpgradeRows(t, db, query); !reflect.DeepEqual(got, before) {
		t.Fatalf("upgrade changed work: got %v, want %v", got, before)
	}
}
