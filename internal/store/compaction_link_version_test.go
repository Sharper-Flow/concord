package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// compactionLinkBytes encodes one note's link payload at a recorded payload
// version. Version 1 spells its Domains as component_ids. extra sets
// top-level keys after the shape is built, so a case can add, restore, or
// corrupt resulting_version without a version-specific struct.
func compactionLinkBytes(t *testing.T, id string, version int, extra map[string]any) []byte {
	t.Helper()
	base := compactionLinkPayload{
		ID: id, Type: "work_note", Title: "Title " + id, CompletedAt: "2026-08-07T00:00:00Z", OutcomeTag: "shipped",
		LessonTags: []string{"lesson"}, TerminalState: "completed", Priority: 1, Summary: "Summary " + id,
		ProductIDs: []string{"product"}, ProjectIDs: []string{"project"}, DomainIDs: []string{"z-domain", "a-domain"}, TagIDs: []string{"tag"},
		HomeProjectID: "home", HomeLocatorID: "locator", NotePath: ".concord/docs/work/" + id + ".md",
		CommitOID: strings.Repeat("a", 40), ContentHash: "sha256:" + strings.Repeat("b", 64), Reason: "fixture", ExpectedVersion: 3,
	}
	var shape any = base
	if version == 1 {
		shape = compactionLinkPayloadV1{
			ID: base.ID, Type: base.Type, Title: base.Title, CompletedAt: base.CompletedAt, OutcomeTag: base.OutcomeTag,
			LessonTags: base.LessonTags, TerminalState: base.TerminalState, Priority: base.Priority, Summary: base.Summary,
			ProductIDs: base.ProductIDs, ProjectIDs: base.ProjectIDs, ComponentIDs: base.DomainIDs, TagIDs: base.TagIDs,
			HomeProjectID: base.HomeProjectID, HomeLocatorID: base.HomeLocatorID, NotePath: base.NotePath,
			CommitOID: base.CommitOID, ContentHash: base.ContentHash, Reason: base.Reason, ExpectedVersion: base.ExpectedVersion,
		}
	}
	raw, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "resulting_version")
	for key, value := range extra {
		fields[key] = value
	}
	out, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCompactionLinkPublishRecordsNoVersionChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, home, _, commit, path := compactionFixture(t, false)
	if err := PublishCompactionLink(ctx, s, compactionRequest(home, commit, path, "no-version-claim")); err != nil {
		t.Fatal(err)
	}
	var payloadVersion int
	var raw string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version, payload FROM domain_events WHERE kind='compaction_link.published'`).Scan(&payloadVersion, &raw); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatal(err)
	}
	if _, claimed := fields["resulting_version"]; payloadVersion != 3 || claimed || string(fields["expected_version"]) != "3" {
		t.Fatalf("compaction event payload_version=%d payload=%s; want version 3 with expected_version 3 and no resulting_version", payloadVersion, raw)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='owner'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("work version after compaction = %d, want the unchanged 3", version)
	}
}

func TestCompactionLinkReplayRefusesAVersionClaim(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		version int
		extra   map[string]any
		stage   FailureStage
	}{
		{"v1 pair is not consecutive", 1, map[string]any{"resulting_version": 5}, StageUpcast},
		{"v2 pair is not consecutive", 2, map[string]any{"resulting_version": 5}, StageUpcast},
		{"v3 carries resulting_version", 3, map[string]any{"resulting_version": 4}, StageDecode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := openTemp(t)
			if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES('bad-link','compaction_link.published','work_item','owner','operator','2026-08-07T00:00:00Z',?,?)`,
				tc.version, string(compactionLinkBytes(t, "owner", tc.version, tc.extra))); err != nil {
				t.Fatal(err)
			}
			err := RebuildFromLog(context.Background(), s)
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || failure.Stage != tc.stage || failure.EventID != "bad-link" {
				t.Fatalf("rebuild error = %v, want invalid_payload at stage %s for bad-link", err, tc.stage)
			}
		})
	}
}

// compactionVersionStore records one terminal work item per link and appends
// each link at its recorded payload version.
func compactionVersionStore(t *testing.T, versions []int) *Store {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	ids := make([]string, len(versions))
	for i := range versions {
		ids[i] = "link-" + string(rune('a'+i))
	}
	seedResearchWork(t, s, ids...)
	seedEventDerivedLocator(t, s, "home", "locator", t.TempDir())
	for i, id := range ids {
		terminalizeResearchOwner(t, s, id)
		extra := map[string]any{"resulting_version": 4}
		if versions[i] == 3 {
			extra = nil
		}
		event := Event{EventID: "compaction-" + id, Kind: "compaction_link.published", SubjectType: SubjectWorkItem, SubjectID: id, Actor: "test", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), PayloadVersion: versions[i], Payload: compactionLinkBytes(t, id, versions[i], extra)}
		if err := ApplyOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, id): 3}}); err != nil {
			t.Fatalf("append v%d link: %v", versions[i], err)
		}
	}
	return s
}

// compactionProjectionDump renders every row a compaction link can affect.
func compactionProjectionDump(t *testing.T, s *Store) string {
	t.Helper()
	queries := []string{
		`SELECT json_group_array(json_array(id, version, lifecycle)) FROM (SELECT * FROM work_items ORDER BY id)`,
		`SELECT json_group_array(json_array(id,type,title,completed_at,outcome_tag,lesson_tags,terminal_state,priority,summary,successor_work_id,home_project_id,home_locator_id,note_path,commit_oid,content_hash)) FROM (SELECT * FROM archived_work ORDER BY id)`,
		`SELECT json_group_array(json_array(work_id, product_id)) FROM (SELECT * FROM archived_work_products ORDER BY work_id, product_id)`,
		`SELECT json_group_array(json_array(work_id, project_id)) FROM (SELECT * FROM archived_work_projects ORDER BY work_id, project_id)`,
		`SELECT json_group_array(json_array(work_id, domain_id)) FROM (SELECT * FROM archived_work_domains ORDER BY work_id, domain_id)`,
		`SELECT json_group_array(json_array(work_id, tag_id)) FROM (SELECT * FROM archived_work_tags ORDER BY work_id, tag_id)`,
	}
	var dump strings.Builder
	for _, query := range queries {
		var rows string
		if err := s.DatabaseForTesting().QueryRow(query).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		dump.WriteString(rows + "\n")
	}
	return dump.String()
}

func TestMixedCompactionPayloadVersionsRebuildIdenticalRows(t *testing.T) {
	t.Parallel()
	mixed := compactionVersionStore(t, []int{1, 2, 3})
	want := compactionProjectionDump(t, compactionVersionStore(t, []int{3, 3, 3}))
	if got := compactionProjectionDump(t, mixed); got != want {
		t.Fatalf("mixed-version links fold to different rows:\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(want, `"link-c"`) || strings.Count(want, `"a-domain"`) != 3 {
		t.Fatalf("dump does not hold the archived links: %s", want)
	}
	// Rebuild keeps archived rows it already holds, so clear them to make the
	// replay insert every link again.
	if _, err := mixed.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); DELETE FROM archived_work_products; DELETE FROM archived_work_projects; DELETE FROM archived_work_domains; DELETE FROM archived_work_tags; DELETE FROM archived_work; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), mixed); err != nil {
		t.Fatal(err)
	}
	if got := compactionProjectionDump(t, mixed); got != want {
		t.Fatalf("rebuild of mixed-version links changed rows:\n got %s\nwant %s", got, want)
	}
}
