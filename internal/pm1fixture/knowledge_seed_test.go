package pm1fixture

import "testing"

func TestKnowledgeSeedsHistoricalLocatorWithoutPublication(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	s, err := OpenTemp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := Seed(t.Context(), s, c); err != nil {
		t.Fatal(err)
	}
	knowledge, err := SeedKnowledge(t.Context(), s, c, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var notePath, terminalState, homeProjectID, homeLocatorID string
	if err := s.DatabaseForTesting().QueryRow(`SELECT note_path,terminal_state,home_project_id,home_locator_id FROM archived_work WHERE id='work-done'`).Scan(&notePath, &terminalState, &homeProjectID, &homeLocatorID); err != nil {
		t.Fatal(err)
	}
	if notePath != ".concord/docs/work/2026-08-03-auth-release.md" || terminalState != "completed" || homeProjectID != knowledge.Home.HomeProjectID || homeLocatorID != knowledge.Home.HomeLocatorID {
		t.Fatalf("historical locator=%q/%q/%q/%q", notePath, terminalState, homeProjectID, homeLocatorID)
	}
	var publications, guards int
	if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT count(*) FROM durable_operations WHERE workflow_type_ref='concord.pm6.compaction'),(SELECT count(*) FROM write_guard)`).Scan(&publications, &guards); err != nil || publications != 0 || guards != 0 {
		t.Fatalf("publication operations=%d write guards=%d err=%v", publications, guards, err)
	}
}
