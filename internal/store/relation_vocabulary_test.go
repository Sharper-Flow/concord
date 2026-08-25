package store

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"
)

func TestRelationSchemaKindsMatchVocabulary(t *testing.T) {
	s := openTemp(t)
	var createSQL string
	if err := s.DatabaseForTesting().QueryRowContext(context.Background(), `SELECT sql FROM sqlite_master WHERE type='table' AND name='relations'`).Scan(&createSQL); err != nil {
		t.Fatal(err)
	}
	check := regexp.MustCompile(`(?i)kind\s+IN\s*\(([^)]*)\)`).FindStringSubmatch(createSQL)
	if len(check) != 2 {
		t.Fatalf("relations table has no kind CHECK: %s", createSQL)
	}
	values := regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(check[1], -1)
	got := make(map[string]bool, len(values))
	for _, value := range values {
		got[value[1]] = true
	}
	want := make(map[string]bool, len(relationStoredKinds))
	for _, kind := range relationStoredKinds {
		want[kind] = true
	}
	if len(got) != len(want) {
		t.Fatalf("relation CHECK kinds = %v, want %v", got, want)
	}
	for kind := range want {
		if !got[kind] {
			t.Fatalf("relation CHECK omits vocabulary kind %q: got %v", kind, got)
		}
	}
}

func TestRelationIdentityIncludesWorkflowRelationEvents(t *testing.T) {
	s := openTemp(t)
	db := s.DatabaseForTesting()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_items(id,kind,title,lifecycle,priority,version,created_at,updated_at) VALUES
		('collision-from','task','From','needed',1,1,'now','now'),
		('collision-to','task','To','needed',1,1,'now','now'),
		('collision-successor','task','Successor','needed',1,1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES
		('collision-overlap','workflow.overlap_resolved','work_item','collision-from','operator','2026-01-01T00:00:00Z',1,'{}'),
		('collision-successor-event','workflow.successor_linked','work_item','collision-from','operator','2026-01-01T00:00:01Z',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	overlap := Event{EventID: "collision-overlap", Kind: WorkflowOverlapResolved, SubjectID: "collision-from", OccurredAt: relationTestTime("2026-01-01T00:00:00Z")}
	if err := insertRelation(context.Background(), tx, overlap, relationPayload{From: "collision-from", To: "collision-to", Kind: "compatible_with"}); err != nil {
		t.Fatalf("insert overlap relation: %v", err)
	}
	successor := Event{EventID: "collision-successor-event", Kind: WorkflowSuccessorLinked, Seq: 2, SubjectID: "collision-from", OccurredAt: relationTestTime("2026-01-01T00:00:01Z")}
	if err := insertWorkflowForwardRelation(context.Background(), tx, successor, "collision-successor"); err != nil {
		t.Fatalf("insert successor relation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT id,kind FROM relations ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	var kinds []string
	for rows.Next() {
		var id int64
		var kind string
		if err := rows.Scan(&id, &kind); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		kinds = append(kinds, kind)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 || kinds[0] != "compatible_with" || kinds[1] != "forward_link" {
		t.Fatalf("relation identities = %v and kinds = %v, want [1 2] and [compatible_with forward_link]", ids, kinds)
	}
}

// The launcher reads a deliberate display subset of the relation vocabulary
// rather than every kind. The subset is a display choice, but each member must
// still be a real stored kind, or the launcher silently renders nothing for it.
func TestLauncherRelationSubsetIsDrawnFromVocabulary(t *testing.T) {
	source, err := os.ReadFile("launcher_query.go")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`r\.kind IN \(([^)]*)\)`).FindSubmatch(source)
	if len(match) != 2 {
		t.Fatal("launcher_query.go has no relation kind filter")
	}
	stored := make(map[string]bool, len(relationStoredKinds))
	for _, kind := range relationStoredKinds {
		stored[kind] = true
	}
	found := regexp.MustCompile(`'([^']*)'`).FindAllSubmatch(match[1], -1)
	if len(found) == 0 {
		t.Fatalf("launcher relation filter names no kinds: %s", match[1])
	}
	for _, value := range found {
		if kind := string(value[1]); !stored[kind] {
			t.Fatalf("launcher renders relation kind %q, which the vocabulary does not declare", kind)
		}
	}
}

func relationTestTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}
