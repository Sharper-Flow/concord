package store

import (
	"context"
	"strings"
	"testing"
)

func TestWorkPinCancelledClosesPlanSeeksProjectInstances(t *testing.T) {
	s := openTemp(t)
	rows, err := s.DatabaseForTesting().QueryContext(context.Background(), `EXPLAIN QUERY PLAN `+workPinCancelledInstanceClosesSQL, "project")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(details, " | ")
	t.Logf("query plan: %s", plan)
	want := []string{
		"SEARCH wp USING COVERING INDEX work_projects_by_project (project_id=?)",
		"SEARCH wi USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)",
		"SEARCH w USING INDEX sqlite_autoindex_work_items_1 (id=?)",
	}
	if len(details) != len(want) {
		t.Fatalf("cancelled closes must read project memberships, then key-probe instances and work items: %s", plan)
	}
	for i := range want {
		if !strings.Contains(details[i], want[i]) {
			t.Fatalf("cancelled closes must read project memberships, then key-probe instances and work items: %s", plan)
		}
	}
}
