package store

import (
	"strings"
	"testing"
)

// rebuildClearListExemptions names the tables that hold a foreign key to
// work_items yet must never join the clear list: they are direct-table
// authority the event log cannot restore, so the rebuild snapshots and
// restores them instead of clearing them (research_rebuild.go,
// runtime_authority_rebuild.go). A table here that stops referencing
// work_items, or stops existing, fails the test so the exemption cannot
// outlive its reason.
var rebuildClearListExemptions = append([]string{
	"active_research_packs",
	"active_research_consumers",
}, operationalRebuildTables...)

// TestLinearOutboxIsNeverCleared pins the one direct-authority table that has
// no foreign key at all: a queued row is pending Linear work no event can
// restore, and nothing blocks its rows, so the rebuild must never clear,
// snapshot, or otherwise touch it.
func TestLinearOutboxIsNeverCleared(t *testing.T) {
	t.Parallel()
	for _, table := range replayProjectionClearTables {
		if table == "linear_outbox" {
			t.Fatal("linear_outbox is in the clear list: a rebuild would drop queued Linear operations the event log cannot restore")
		}
	}
	for _, table := range operationalRebuildTables {
		if table == "linear_outbox" {
			t.Fatal("linear_outbox is in the snapshot set: it holds no foreign key and must simply stay untouched")
		}
	}
}

// rebuildClearTableSet indexes the clear list for membership checks.
func rebuildClearTableSet() map[string]bool {
	set := make(map[string]bool, len(replayProjectionClearTables))
	for _, table := range replayProjectionClearTables {
		set[table] = true
	}
	return set
}

// The clear list is a hand list. This derives its obligations from SQLite's
// own foreign-key metadata on a migrated store and refuses drift in either
// direction: a projection table whose foreign key to work_items is missing
// from the list (the DELETE FROM work_items then fires the foreign key and
// the rebuild refuses), and a listed table cleared before another listed
// table it references. Foreign keys declared DEFERRABLE are exempt from the
// coverage rule: SQLite checks them at commit, and the replay restores every
// referenced projection before the rebuild commits.
func TestRebuildClearListCoversWorkItemReferences(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	db := s.DatabaseForTesting()

	// Read every edge before judging any: the store pool holds one
	// connection, and a second query while a result set is open parks
	// forever.
	createSQL := map[string]string{}
	tables := []string{}
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, sql string
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatal(err)
		}
		createSQL[name] = sql
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()

	references := map[string]map[string]bool{}
	for _, table := range tables {
		edges, err := db.Query(`SELECT "table" FROM pragma_foreign_key_list(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		references[table] = map[string]bool{}
		for edges.Next() {
			var referenced string
			if err := edges.Scan(&referenced); err != nil {
				t.Fatal(err)
			}
			references[table][referenced] = true
		}
		if err := edges.Err(); err != nil {
			t.Fatal(err)
		}
		edges.Close()
	}

	position := map[string]int{}
	seen := map[string]bool{}
	for i, table := range replayProjectionClearTables {
		if seen[table] {
			t.Errorf("%s appears twice in the clear list", table)
		}
		seen[table] = true
		position[table] = i
	}
	workItemsPos, listed := position["work_items"]
	if !listed {
		t.Fatal("work_items is absent from the clear list, so the rebuild never clears it")
	}

	exempt := map[string]bool{}
	for _, table := range rebuildClearListExemptions {
		exempt[table] = true
	}

	for _, table := range tables {
		referencesWorkItems := references[table]["work_items"]
		_, exempted := exempt[table]
		if referencesWorkItems && exempted {
			if _, inList := position[table]; inList {
				t.Errorf("%s is exempt direct-table authority but appears in the clear list", table)
			}
			continue
		}
		if referencesWorkItems && strings.Contains(createSQL[table], "DEFERRABLE") {
			continue // checked at commit, after the replay restored work_items
		}
		if !referencesWorkItems {
			continue
		}
		pos, inList := position[table]
		if !inList {
			t.Errorf("%s holds a foreign key to work_items but is absent from the clear list", table)
			continue
		}
		if pos >= workItemsPos {
			t.Errorf("%s is cleared after work_items (positions %d, %d)", table, pos, workItemsPos)
		}
	}

	for _, table := range rebuildClearListExemptions {
		if _, exists := createSQL[table]; !exists {
			t.Errorf("exempt table %s no longer exists; retire the exemption", table)
			continue
		}
		if !references[table]["work_items"] && !references[table]["products"] && !references[table]["projects"] {
			t.Errorf("exempt table %s no longer references a cleared projection root; retire the exemption", table)
		}
	}

	for table, pos := range position {
		for referenced := range references[table] {
			if referenced == table {
				continue
			}
			refPos, referencedListed := position[referenced]
			if !referencedListed {
				continue
			}
			if refPos <= pos {
				t.Errorf("%s references %s but is cleared after it (positions %d, %d)", table, referenced, pos, refPos)
			}
		}
	}
}
