package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// These recorded values seed the one-time projection migration, not a replay.
const (
	historicalResearchLaneDigest  = "sha256:3969ceda54cc6be1532877e6d5b1dc5530c280ff77835f43286c4a0ad37e861b"
	historicalRoutingPolicyToken  = "routing-v1"
	historicalRoutingPolicyDigest = "sha256:34718d4f686c90b4806533ad1cc9eb1eab7c3cce0f4e732dcdaa70d73aa9f736"
)

func histDispatchEvent(t *testing.T, eventID, attemptID string, version int, occurredAt time.Time, members map[string]any) Event {
	t.Helper()
	payload := map[string]any{
		"attempt_id": attemptID, "lane_id": "research", "lane_version": 1,
		"lane_digest": historicalResearchLaneDigest, "capability_class": "research",
		"packet_schema_version": "1.0", "report_schema_version": "1.0",
	}
	for member, value := range members {
		payload[member] = value
	}
	return Event{EventID: eventID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "host:fixture", OccurredAt: occurredAt, PayloadVersion: version, Payload: mustJSONValue(payload)}
}

func histDeclaredHostProvenance() map[string]any {
	return map[string]any{"digest": "sha256:" + strings.Repeat("ab", 32), "sources": []any{map[string]any{"kind": "agents_md", "path": "AGENTS.md", "sha256": "sha256:" + strings.Repeat("cd", 32)}}}
}

type histEventRow struct {
	Seq                                                      int64
	EventID, Kind, SubjectType, SubjectID, Actor, OccurredAt string
	Version                                                  int
	Payload                                                  string
}

func histEventRows(t *testing.T, s *Store) []histEventRow {
	t.Helper()
	rows, err := s.DatabaseForTesting().Query(`SELECT seq,event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload FROM domain_events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []histEventRow
	for rows.Next() {
		var row histEventRow
		var payload []byte
		if err := rows.Scan(&row.Seq, &row.EventID, &row.Kind, &row.SubjectType, &row.SubjectID, &row.Actor, &row.OccurredAt, &row.Version, &payload); err != nil {
			t.Fatal(err)
		}
		row.Payload = string(payload)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLiveWriteRoutesRefuseRecordedOldShapesAndCommitNothing(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()
	claim := baseClaim(git)
	claim.SessionRef = "ses-held"
	claimed, err := s.ClaimWorktree(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	before := workerProjectionSnapshot(t, s)
	version := readWorkVersion(t, s, "work-w")
	native := legacyNativeRunWriterPayload(t, "run-live", nil)
	cases := map[string]Event{
		"occupancy release occupant_session_ref":     {Kind: "work.worktree_occupancy_released", Payload: jsonRaw(`{"set_id":"worktree-set:work-w","project_id":"project-w","claim_op_id":"wt-op-1","occupant_session_ref":"ses-held"}`)},
		"worker failed observed_session_directories": {Kind: WorkerFailed, Payload: jsonRaw(`{"attempt_id":"attempt-live","failure_kind":"abandoned","detail":"host ended","readback_model":"","observed_session_directories":[]}`)},
		"worker dispatched routing members":          histDispatchEvent(t, "live-old-dispatch", "attempt-live", 1, time.Unix(30, 0).UTC(), map[string]any{"routing_policy_version": historicalRoutingPolicyToken, "resolved_model": "openai/test-model"}),
		"worker dispatched digestless readback":      histDispatchEvent(t, "live-digestless-dispatch", "attempt-live", 3, time.Unix(30, 0).UTC(), map[string]any{"readback_model": "", "host_provenance": histDeclaredHostProvenance()}),
		"native run map universe":                    legacyNativeRunEvent("live-old-native", native),
	}
	for name, event := range cases {
		event.EventID = "refused-" + strings.ReplaceAll(name, " ", "-")
		event.SubjectType, event.SubjectID, event.Actor = SubjectWorkItem, "work-w", "host:fixture"
		event.OccurredAt = time.Unix(40, 0).UTC()
		event.PayloadVersion = eventKindRegistry[event.Kind].CurrentVersion
		count := countRows(t, s, "domain_events")
		err := ApplyOperation(ctx, s, Operation{Events: []Event{event}})
		var failure *Failure
		if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
			t.Fatalf("live %s = %v, want invalid_payload", name, err)
		}
		if countRows(t, s, "domain_events") != count || workerProjectionSnapshot(t, s) != before || readWorkVersion(t, s, "work-w") != version {
			t.Fatalf("live %s refusal changed state", name)
		}
	}
	landing := Event{EventID: "live-missing-pid", Kind: "work.session_claim_landed", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "ses-held", OccurredAt: time.Unix(42, 0).UTC(), PayloadVersion: eventKindRegistry["work.session_claim_landed"].CurrentVersion,
		Payload: mustJSONValue(map[string]any{"work_id": "work-w", "project_id": "project-w", "session_ref": "ses-held", "claim_op_id": claimed.Entry.ClaimOpID, "source_directories": []string{}, "landed_directory": claimed.Entry.Path, "host_pid": 0, "host_pid_start": 0})}
	count := countRows(t, s, "domain_events")
	err = ApplyOperation(ctx, s, Operation{Events: []Event{landing}})
	var failure *Failure
	if !failureAs(err, &failure) || !strings.Contains(failure.Detail, "missing host_pid") {
		t.Fatalf("missing-pid landing = %v", err)
	}
	if countRows(t, s, "domain_events") != count || readWorkVersion(t, s, "work-w") != version {
		t.Fatal("refused landing changed state")
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", claimed.Entry.ClaimOpID); got != "ses-held" {
		t.Fatalf("refused landing changed occupancy to %q", got)
	}
	assertFoldGuardEmpty(t, s)
}
