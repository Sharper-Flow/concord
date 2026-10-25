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
	request := compactionRequest(home, commit, path, "no-version-claim")
	note, err := VerifyCommittedNote(ctx, home.RepoPath, commit, path, request.ExpectedHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := PublishCompactionLink(ctx, s, request); err != nil {
		t.Fatal(err)
	}
	var payloadVersion int
	var digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version,payload_digest FROM effect_receipts WHERE event_id=? AND kind='compaction_link.published' AND subject_id=?`, request.EventID, request.WorkID).Scan(&payloadVersion, &digest); err != nil {
		t.Fatal(err)
	}
	expected := compactionLinkPayload{
		ID: note.ID, Type: note.Kind, Title: note.Title,
		CompletedAt: note.CompletedAt, OutcomeTag: note.OutcomeTag,
		LessonTags: note.LessonTags, TerminalState: note.TerminalState,
		Priority: note.Priority, Summary: note.Summary, SuccessorID: note.SuccessorID,
		ProductIDs: note.ProductIDs, ProjectIDs: note.ProjectIDs,
		DomainIDs: []string{"auth"}, TagIDs: note.TagIDs,
		HomeProjectID: home.HomeProjectID, HomeLocatorID: home.HomeLocatorID,
		NotePath: note.NotePath, CommitOID: note.CommitOID, ContentHash: note.ContentHash,
		Reason: request.Reason, ExpectedVersion: 3,
	}
	expectedRaw := mustJSONValue(expected)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(expectedRaw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, claimed := fields["resulting_version"]; claimed || string(fields["expected_version"]) != "3" {
		t.Fatalf("compaction expectation carries a version change: %s", expectedRaw)
	}
	want, err := effectPayloadDigest(expectedRaw)
	if payloadVersion != 3 || err != nil || digest != want {
		t.Fatalf("compaction receipt version=%d digest=%q, want version 3 and exact payload %q with expected_version 3 and no resulting_version: %v", payloadVersion, digest, want, err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='owner'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("work version after compaction = %d, want the unchanged 3", version)
	}
}
