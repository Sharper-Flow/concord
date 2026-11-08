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
	if len(details) < 3 || !strings.Contains(details[0], "SEARCH wp USING COVERING INDEX work_projects_by_project (project_id=?)") {
		t.Fatalf("cancelled closes must start with project membership: %s", plan)
	}
	instanceSeek := -1
	for i, detail := range details {
		if strings.Contains(detail, "SEARCH wi USING INDEX sqlite_autoindex_workflow_instances_1 (work_id=?)") {
			instanceSeek = i
		}
	}
	if instanceSeek <= 0 {
		t.Fatalf("cancelled closes must probe instances by work ID: %s", plan)
	}
}
