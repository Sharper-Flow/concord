package store

import (
	"context"
	"encoding/json"
	"testing"
)

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
