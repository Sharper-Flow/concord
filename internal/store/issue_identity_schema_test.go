package store

import (
	"strings"
	"testing"
)

func TestRecordedIssueIdentitySchema(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	db := s.DatabaseForTesting()
	rows, err := db.Query(`SELECT name FROM pragma_table_info('linear_issue_links')`)
	if err != nil {
		t.Fatal(err)
	}
	columns := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{"work_id", "remote_issue_uuid", "human_key", "url", "created_at", "updated_at"}
	if strings.Join(columns, ",") != strings.Join(want, ",") {
		t.Fatalf("linear_issue_links columns = %v, want exactly the retained identity %v", columns, want)
	}
	var references int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_list('linear_issue_links') WHERE "table"='work_items'`).Scan(&references); err != nil {
		t.Fatal(err)
	}
	if references != 0 {
		t.Fatal("recorded issue identity must not depend on a work_items row")
	}
}
