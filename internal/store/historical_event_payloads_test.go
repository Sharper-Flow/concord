package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Durable acceptance tests for the recorded historical payload
// schemas. Every recorded event here is inserted as a raw log row the live
// write route can no longer produce, and every acceptance route runs through
// the recorded-event preparation the rebuild, restore, reconstruction, and
// recovery owners share. No test tags a private shape onto an event by hand:
// recognition must come from the retained bytes alone.

// The recorded worker dispatch history used the lane identity this digest
// pins: research lane version 1 as the lane registry of its release day
// declared it. The exact id, version, and digest must resolve through
// LookupLane; the tests stop rather than substitute the current digest.
const (
	historicalResearchLaneDigest  = "sha256:3969ceda54cc6be1532877e6d5b1dc5530c280ff77835f43286c4a0ad37e861b"
	historicalRoutingPolicyToken  = "routing-v1"
	historicalRoutingPolicyDigest = "sha256:34718d4f686c90b4806533ad1cc9eb1eab7c3cce0f4e732dcdaa70d73aa9f736"

	// The shared decode refusal every strict recorded and live decoder wraps
	// unknown members, wrong types, and trailing structures in.
	histDecodeRefusal = "does not match its registered schema"
)

// requireHistoricalResearchLane refuses the test when the lane registry can
// no longer resolve the recorded historical identity. Substituting the
// current digest would test a different history, so this is a stop
// condition, not a fixture default.
func requireHistoricalResearchLane(t *testing.T) {
	t.Helper()
	lane, err := LookupLane("research", 1, historicalResearchLaneDigest)
	if err != nil {
		t.Fatalf("the lane registry refuses the recorded historical identity research:1 %s: %v — the recorded dispatch history has no historical authority; report the missing registry entry, do not substitute the current digest", historicalResearchLaneDigest, err)
	}
	if lane.CapabilityClass != "research" {
		t.Fatalf("historical research lane resolves with capability class %q, want research", lane.CapabilityClass)
	}
}

// histDispatchEvent builds one released worker.dispatched payload the
// historical writer emitted: the shared lane identity plus the routing
// members the caller names. Packet and report schema are the released 1.0
// pair, and no packet digest exists at any recorded routing version.
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
	return histEventWithPayload(t, Event{
		EventID: eventID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem,
		SubjectID: "work-w", Actor: "host:fixture", OccurredAt: occurredAt, PayloadVersion: version,
	}, payload)
}

// histWorkerFailedEvent builds one released worker.failed payload: the
// abandoned close with the recorded host session observation the writer of
// its day required. observed may be a typed empty array, a valid nonempty
// array, or nil for the JSON null form.
func histWorkerFailedEvent(t *testing.T, eventID, attemptID string, occurredAt time.Time, observed any) Event {
	t.Helper()
	return histEventWithPayload(t, Event{
		EventID: eventID, Kind: WorkerFailed, SubjectType: SubjectWorkItem,
		SubjectID: "work-w", Actor: "host:fixture", OccurredAt: occurredAt, PayloadVersion: 1,
	}, map[string]any{
		"attempt_id": attemptID, "readback_model": "",
		"failure_kind": WorkerFailureAbandoned, "detail": "the host ended the session before a report arrived",
		"observed_session_directories": observed,
	})
}

// histOccupancyReleasedEvent builds one released occupancy release payload:
// the recorded row identity names its session through occupant_session_ref.
func histOccupancyReleasedEvent(eventID, claimOpID, sessionRef string, occurredAt time.Time) Event {
	payload := jsonRaw(`{"set_id":"` + WorktreeSetID("work-w") + `","project_id":"project-w","claim_op_id":"` + claimOpID + `","occupant_session_ref":"` + sessionRef + `"}`)
	return Event{
		EventID: eventID, Kind: "work.worktree_occupancy_released", SubjectType: SubjectWorkItem,
		SubjectID: "work-w", Actor: sessionRef, OccurredAt: occurredAt, PayloadVersion: 1, Payload: payload,
	}
}

// histDeclaredHostProvenance is one valid declared host provenance the v3
// writer recorded alongside the routing members.
func histDeclaredHostProvenance() map[string]any {
	return map[string]any{
		"digest": "sha256:" + strings.Repeat("ab", 32),
		"sources": []any{map[string]any{
			"kind": "agents_md", "path": "AGENTS.md", "sha256": "sha256:" + strings.Repeat("cd", 32),
		}},
	}
}

// histEventWithPayload marshals the fixture map into the event's payload.
func histEventWithPayload(t *testing.T, event Event, payload map[string]any) Event {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event.Payload = raw
	return event
}

// histEventRow is one durable log row: identity, order, header, and bytes.
type histEventRow struct {
	Seq         int64
	EventID     string
	Kind        string
	SubjectType string
	SubjectID   string
	Actor       string
	OccurredAt  string
	Version     int
	Payload     string
}

// histEventRows reads the complete log in stored order.
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

// histProjectionRows renders every projection row the recorded history
// touches, from one database, in a stable order. The live store and the
// restored store must render identically.
func histProjectionRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var out []string
	scan := func(query string) {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			rendered := ""
			for _, value := range values {
				rendered += fmt.Sprintf("%v\x00", value)
			}
			out = append(out, rendered)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	scan(`SELECT work_id,attempt_id,lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,COALESCE(failure_kind,''),COALESCE(failure_detail,''),dispatched_at,COALESCE(failed_at,'') FROM worker_attempts ORDER BY attempt_id`)
	scan(`SELECT worktree_id,session_ref,recorded_at,COALESCE(host_pid,-1),COALESCE(host_pid_start,-1),has_process_identity FROM worktree_occupancy ORDER BY worktree_id,session_ref`)
	scan(`SELECT set_id,project_id,claim_op_id,state FROM worktree_entries ORDER BY set_id,project_id,claim_op_id`)
	scan(`SELECT work_id,run_id,phase,status,event_id,reporting_authority_ref,actor_ref,native_subject_ref,subject_digest,evidence_ref,evidence_digest,asserted_at,recorded_at,capture_method,observed_universe,freshness_policy_ref,divergence_policy_ref,COALESCE(observation_id,''),COALESCE(verification_state,'') FROM workflow_native_runs ORDER BY work_id,run_id,phase`)
	scan(`SELECT id,version FROM work_items ORDER BY id`)
	var observations int
	if err := db.QueryRow(`SELECT count(*) FROM external_observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	out = append(out, fmt.Sprintf("external_observations\x00%d\x00", observations))
	return out
}

// histAttempt is one worker attempt projection row.
type histAttempt struct {
	laneID      string
	laneVersion int64
	laneDigest  string
	class       string
	readback    string
	packet      string
	report      string
	lifecycle   string
	failureKind string
	failedAt    string
}

func histAttemptRow(t *testing.T, db *sql.DB, attemptID string) histAttempt {
	t.Helper()
	var row histAttempt
	err := db.QueryRow(`SELECT lane_id,lane_version,lane_digest,capability_class,readback_model,packet_schema_version,report_schema_version,lifecycle_state,COALESCE(failure_kind,''),COALESCE(failed_at,'') FROM worker_attempts WHERE attempt_id=?`, attemptID).
		Scan(&row.laneID, &row.laneVersion, &row.laneDigest, &row.class, &row.readback, &row.packet, &row.report, &row.lifecycle, &row.failureKind, &row.failedAt)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// A complete synthetic history of recorded payloads — the pre-identity claim
// landing, the occupant_session_ref release, the released native-run report,
// the routing worker dispatches at payload versions 1, 2, and 3 including one
// terminal-at-birth record, and the abandoned worker failures with their
// recorded host session observations — must rebuild twice with the log
// byte-identical, back up through the promotion gate, restore with the
// source and restored inventories separated, and reconstruct. The recorded
// bytes are never rewritten, and the projections hold the recorded
// semantics: routing rows preserve the values carried by forward migrations,
// the legacy occupancy row keeps no process identity, and the old native-run
// report stays unverified with no observation fabricated.
func TestRecordedHistoricalPayloadHistoryRebuildsRestoresAndReconstructs(t *testing.T) {
	t.Parallel()
	requireHistoricalResearchLane(t)
	s, git, _ := worktreeFixture(t)
	siblingProjectFixture(t, s, git)
	ctx := context.Background()

	claimA := baseClaim(git)
	claimA.ExpectedVersion = 3
	claimA.SessionRef = "ses-legacy"
	claimedA, err := s.ClaimWorktree(ctx, claimA)
	if err != nil {
		t.Fatal(err)
	}
	claimB := siblingClaim(git, "wt-op-2", 4)
	claimB.SessionRef = "ses-legacy"
	claimedB, err := s.ClaimWorktree(ctx, claimB)
	if err != nil {
		t.Fatal(err)
	}

	insertHistoricalEvent(t, s, histOccupancyReleasedEvent("hist-release-1", claimedA.Entry.ClaimOpID, "ses-legacy", time.Unix(20, 0).UTC()))
	landing, landingPayload := legacyLandingEvent("hist-landing-1", "project-b", "ses-legacy", claimedB.Entry.Path, nil, time.Unix(21, 0).UTC())
	insertHistoricalEvent(t, s, landing)
	seedRecordedLandingOccupancy(t, s, landing)

	insertHistoricalEvent(t, s, histDispatchEvent(t, "hist-dispatch-v1", "attempt-hist-v1", 1, time.Unix(30, 0).UTC(), map[string]any{
		"routing_policy_version": historicalRoutingPolicyToken,
		"resolved_model":         "openai/gpt-5.6-luna",
	}))
	insertHistoricalEvent(t, s, histDispatchEvent(t, "hist-dispatch-v2", "attempt-hist-v2", 2, time.Unix(31, 0).UTC(), map[string]any{
		"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
		"resolved_model": "openai/gpt-5.6-luna", "resolution_role": "preferred", "fallback_reason": "",
	}))
	insertHistoricalEvent(t, s, histDispatchEvent(t, "hist-dispatch-v2-fallback", "attempt-hist-v2-fallback", 2, time.Unix(32, 0).UTC(), map[string]any{
		"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
		"resolved_model": "zai-coding-plan/glm-5.3", "resolution_role": "fallback", "fallback_reason": "provider_unavailable",
	}))
	insertHistoricalEvent(t, s, histDispatchEvent(t, "hist-dispatch-v3", "attempt-hist-v3", 3, time.Unix(33, 0).UTC(), map[string]any{
		"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
		"resolved_model": "openai/gpt-5.6-luna", "resolution_role": "preferred", "fallback_reason": "",
		"host_provenance": histDeclaredHostProvenance(),
	}))
	insertHistoricalEvent(t, s, histDispatchEvent(t, "hist-dispatch-v3-terminal", "attempt-hist-v3-terminal", 3, time.Unix(34, 0).UTC(), map[string]any{
		"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
		"resolved_model": "anthropic/claude-unexpected", "resolution_role": "undeclared", "fallback_reason": "",
		"terminal": "failed", "terminal_failure_kind": WorkerFailureModelIdentity,
		"terminal_detail": "host resolved a model outside the declared resolution set",
		"host_provenance": histDeclaredHostProvenance(),
	}))
	insertHistoricalEvent(t, s, histDispatchEvent(t, "hist-dispatch-v3-readback", "attempt-hist-v3-readback", 3, time.Unix(35, 0).UTC(), map[string]any{
		"readback_model": "openai/gpt-5.6-luna", "host_provenance": histDeclaredHostProvenance(),
	}))

	insertHistoricalEvent(t, s, histWorkerFailedEvent(t, "hist-failed-empty-observation", "attempt-hist-v1", time.Unix(40, 0).UTC(), []any{}))
	insertHistoricalEvent(t, s, histWorkerFailedEvent(t, "hist-failed-real-observation", "attempt-hist-v2", time.Unix(41, 0).UTC(), []any{
		map[string]any{"session_ref": "ses-legacy", "directory": claimedA.Entry.Path},
	}))

	var workVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-w'`).Scan(&workVersion); err != nil {
		t.Fatal(err)
	}
	nativePayload, nativeDigest := legacyNativeRunWriterPayload(t, "run-hist", func(payload map[string]any) {
		payload["expected_version"] = workVersion
		payload["resulting_version"] = workVersion + 1
	})
	insertHistoricalEvent(t, s, legacyNativeRunEvent("hist-native-run-1", nativePayload))

	before := histEventRows(t, s)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild must fold the complete recorded history: %v", err)
	}
	afterFirst := histEventRows(t, s)
	if !reflect.DeepEqual(before, afterFirst) {
		t.Fatal("the first rebuild changed the event log bytes, versions, or order")
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("repeated rebuild must fold the recorded history again: %v", err)
	}
	afterSecond := histEventRows(t, s)
	if !reflect.DeepEqual(before, afterSecond) {
		t.Fatal("the repeated rebuild changed the event log bytes, versions, or order")
	}
	for _, row := range afterSecond {
		switch row.EventID {
		case "hist-landing-1":
			if row.Payload != string(landingPayload) {
				t.Fatal("the recorded landing bytes changed on replay")
			}
		case "hist-native-run-1":
			if row.Payload != string(nativePayload) {
				t.Fatal("the recorded native-run report bytes changed on replay")
			}
		}
	}

	// Occupancy: the release deleted the recorded session's source row, the
	// legacy landing holds the destination row with no process identity, and
	// nothing else occupies the work item.
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", claimedA.Entry.ClaimOpID); got != "" {
		t.Fatalf("released occupancy row still records %q", got)
	}
	destinationID := worktreeOccupancyID(WorktreeSetID("work-w"), "project-b", claimedB.Entry.ClaimOpID)
	pid, pidStart, hasIdentity, found := legacyOccupancyIdentity(t, s, destinationID)
	if !found {
		t.Fatal("the legacy landing destination row is missing")
	}
	if pid.Valid || pidStart.Valid || hasIdentity != 0 {
		t.Fatalf("destination identity pid=%v start=%v flag=%d, want NULL identity and has_process_identity=0", pid, pidStart, hasIdentity)
	}
	var occupancyRows int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worktree_occupancy`).Scan(&occupancyRows); err != nil {
		t.Fatal(err)
	}
	if occupancyRows != 1 {
		t.Fatalf("worktree_occupancy holds %d rows, want only the legacy destination row", occupancyRows)
	}

	// Rows preserve the values carried by the routing-removal migration.
	for name, want := range map[string]histAttempt{
		"attempt-hist-v1":          {lifecycle: "failed", failureKind: WorkerFailureAbandoned, failedAt: "set", readback: "openai/gpt-5.6-luna"},
		"attempt-hist-v2":          {lifecycle: "failed", failureKind: WorkerFailureAbandoned, failedAt: "set", readback: "openai/gpt-5.6-luna"},
		"attempt-hist-v2-fallback": {lifecycle: "dispatched", readback: "zai-coding-plan/glm-5.3"},
		"attempt-hist-v3":          {lifecycle: "dispatched", readback: "openai/gpt-5.6-luna"},
		"attempt-hist-v3-terminal": {lifecycle: "failed", failureKind: WorkerFailureModelIdentity, failedAt: "set", readback: "anthropic/claude-unexpected"},
		"attempt-hist-v3-readback": {lifecycle: "dispatched", readback: "openai/gpt-5.6-luna"},
	} {
		got := histAttemptRow(t, s.DatabaseForTesting(), name)
		if got.laneID != "research" || got.laneVersion != 1 || got.laneDigest != historicalResearchLaneDigest || got.class != "research" {
			t.Fatalf("attempt %s lane identity %+v, want the recorded research:1 history digest", name, got)
		}
		if got.packet != "1.0" || got.report != "1.0" {
			t.Fatalf("attempt %s records schema %s/%s, want the released 1.0 pair", name, got.packet, got.report)
		}
		if got.readback != want.readback {
			t.Fatalf("attempt %s records readback %q, want the released projection value %q", name, got.readback, want.readback)
		}
		if got.lifecycle != want.lifecycle || got.failureKind != want.failureKind {
			t.Fatalf("attempt %s lifecycle=%s failure=%s, want %s/%s", name, got.lifecycle, got.failureKind, want.lifecycle, want.failureKind)
		}
		if want.failedAt == "set" && got.failedAt == "" {
			t.Fatalf("attempt %s carries no failed_at, want the recorded terminal time", name)
		}
		if want.failedAt != "set" && got.failedAt != "" {
			t.Fatalf("attempt %s records failed_at %q, want none", name, got.failedAt)
		}
	}

	// Native run: the recorded row keeps the old universe and static policy
	// references, binds no observation, and reads unverified.
	var universeText, freshness, divergence, verification, storedDigest string
	var observationID sql.NullString
	if err := s.DatabaseForTesting().QueryRow(`SELECT observed_universe,freshness_policy_ref,divergence_policy_ref,COALESCE(verification_state,''),subject_digest,observation_id FROM workflow_native_runs WHERE work_id='work-w' AND run_id='run-hist' AND phase='start'`).
		Scan(&universeText, &freshness, &divergence, &verification, &storedDigest, &observationID); err != nil {
		t.Fatal(err)
	}
	if freshness != legacyNativeRunFreshnessPolicyRef || divergence != legacyNativeRunDivergencePolicyRef {
		t.Fatalf("native run policy refs freshness=%q divergence=%q, want the recorded static references", freshness, divergence)
	}
	if storedDigest != nativeDigest {
		t.Fatalf("native run subject digest=%q, want the recorded %q", storedDigest, nativeDigest)
	}
	if observationID.Valid {
		t.Fatalf("native run observation id=%q, want NULL", observationID.String)
	}
	if verification != "unverified" {
		t.Fatalf("native run verification state=%q, want unverified", verification)
	}
	var universe map[string]any
	if err := json.Unmarshal([]byte(universeText), &universe); err != nil {
		t.Fatal(err)
	}
	if universe["total"] != "eq(1)" {
		t.Fatalf("native run universe total=%v, want the recorded eq(1) token", universe["total"])
	}

	var workVersionAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-w'`).Scan(&workVersionAfter); err != nil {
		t.Fatal(err)
	}
	if workVersionAfter != workVersion+1 {
		t.Fatalf("work version=%d after replay, want %d", workVersionAfter, workVersion+1)
	}

	// Backup promotion replays the recorded history on a disposable stage,
	// and restore promotes a verified database with separated inventories.
	snapshot := filepath.Join(t.TempDir(), "recorded-history.db")
	manifest, err := Backup(ctx, s, snapshot)
	if err != nil {
		t.Fatalf("backup of the recorded history must pass promotion verification: %v", err)
	}
	var watermark int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT revision FROM store_revision WHERE id=1`).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	if manifest.EventWatermark != watermark {
		t.Fatalf("manifest watermark=%d, want %d", manifest.EventWatermark, watermark)
	}
	destination := filepath.Join(t.TempDir(), "restored.db")
	report, err := RestoreBackup(ctx, snapshot, destination)
	if err != nil {
		t.Fatalf("restore of the recorded history backup: %v", err)
	}
	if report.SourceSnapshot.SnapshotID != manifest.SnapshotID || report.SourceSnapshot.SchemaVersion != manifest.SchemaVersion || report.SourceSnapshot.EventWatermark != manifest.EventWatermark || report.SourceSnapshot.DBChecksum != manifest.DBChecksum {
		t.Fatalf("source snapshot inventory = %+v, want the verified manifest %+v", report.SourceSnapshot, manifest)
	}
	if report.RestoredDatabase.SchemaVersion != CurrentSchemaVersion() || report.RestoredDatabase.EventWatermark != manifest.EventWatermark {
		t.Fatalf("restored database inventory = %+v, want schema %d watermark %d", report.RestoredDatabase, CurrentSchemaVersion(), manifest.EventWatermark)
	}
	restoredChecksum, err := fileSHA256(destination)
	if err != nil || restoredChecksum != report.RestoredDatabase.DBChecksum {
		t.Fatalf("restored checksum=%q err=%v, want the promoted inventory checksum %q", restoredChecksum, err, report.RestoredDatabase.DBChecksum)
	}
	snapshotChecksum, err := fileSHA256(snapshot)
	if err != nil || snapshotChecksum != manifest.DBChecksum {
		t.Fatalf("snapshot checksum after restore=%q err=%v, want the verified %q", snapshotChecksum, err, manifest.DBChecksum)
	}
	if _, err := os.Stat(manifestPath(destination)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore destination manifest stat error = %v", err)
	}

	restored, err := Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if !reflect.DeepEqual(histEventRows(t, restored), afterSecond) {
		t.Fatal("the restored event log differs from the source log")
	}
	wantProjections := histProjectionRows(t, s.DatabaseForTesting())
	gotProjections := histProjectionRows(t, restored.DatabaseForTesting())
	if !reflect.DeepEqual(wantProjections, gotProjections) {
		t.Fatalf("restored projections differ from the source projections:\nsource: %v\nrestored: %v", wantProjections, gotProjections)
	}
}

func TestRecordedRoutingTerminalOutsideMigratedPopulationRefusesUnchanged(t *testing.T) {
	t.Parallel()
	requireHistoricalResearchLane(t)
	s, _, _ := worktreeFixture(t)
	event := histDispatchEvent(t, "exhausted-routing-dispatch", "attempt-exhausted", 3, time.Unix(30, 0).UTC(), map[string]any{
		"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
		"resolved_model": "", "resolution_role": "undeclared", "fallback_reason": "",
		"host_provenance": histDeclaredHostProvenance(), "terminal": "failed",
		"terminal_failure_kind": "routing_policy_exhausted", "terminal_detail": "declared resolution set exhausted; no model ran",
	})
	insertHistoricalEvent(t, s, event)
	before := histEventRows(t, s)
	var failure *Failure
	if err := RebuildFromLog(context.Background(), s); !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || failure.Detail != "recorded payload matches no released schema" {
		t.Fatalf("a routing terminal outside the migrated population must refuse as unrecognized: %v", err)
	}
	if !reflect.DeepEqual(before, histEventRows(t, s)) {
		t.Fatal("recorded routing terminal log changed")
	}
}

// A stranded fold guard on a store whose history carries recorded occupancy
// events recovers: the rebuild replays the recorded release and landing, the
// occupancy state matches the recorded semantics, and the log is untouched.
func TestRecoverFoldGuardReplaysRecordedOccupancyHistory(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	siblingProjectFixture(t, s, git)
	ctx := context.Background()

	claimA := baseClaim(git)
	claimA.ExpectedVersion = 3
	claimA.SessionRef = "ses-legacy"
	claimedA, err := s.ClaimWorktree(ctx, claimA)
	if err != nil {
		t.Fatal(err)
	}
	claimB := siblingClaim(git, "wt-op-2", 4)
	claimB.SessionRef = "ses-legacy"
	claimedB, err := s.ClaimWorktree(ctx, claimB)
	if err != nil {
		t.Fatal(err)
	}
	insertHistoricalEvent(t, s, histOccupancyReleasedEvent("hist-recovery-release", claimedA.Entry.ClaimOpID, "ses-legacy", time.Unix(20, 0).UTC()))
	recoveryLanding, _ := legacyLandingEvent("hist-recovery-landing", "project-b", "ses-legacy", claimedB.Entry.Path, nil, time.Unix(21, 0).UTC())
	insertHistoricalEvent(t, s, recoveryLanding)
	seedRecordedLandingOccupancy(t, s, recoveryLanding)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("the recorded occupancy history must fold before the guard strands: %v", err)
	}

	var events int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	path := s.Path()

	// Strand the guard the way a crashed fold leaves the database: a
	// committed fold scope that was never closed.
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := beginFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	report, err := RecoverFoldGuard(ctx, path)
	if err != nil {
		t.Fatalf("RecoverFoldGuard over the recorded occupancy history: %v", err)
	}
	if !report.Rebuilt || report.Events != events {
		t.Fatalf("recovery report = %+v, want rebuilt with %d events", report, events)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open after recovery: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var guardRows int
	if err := reopened.DatabaseForTesting().QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&guardRows); err != nil {
		t.Fatal(err)
	}
	if guardRows != 0 {
		t.Fatalf("fold guard holds %d rows after recovery, want 0", guardRows)
	}
	var eventsAfter int64
	if err := reopened.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != events {
		t.Fatalf("event log rows = %d after recovery, want %d", eventsAfter, events)
	}
	if got := worktreeOccupancyByEntry(t, reopened, WorktreeSetID("work-w"), "project-w", claimedA.Entry.ClaimOpID); got != "" {
		t.Fatalf("recorded release did not survive recovery: occupancy records %q", got)
	}
	pid, pidStart, hasIdentity, found := legacyOccupancyIdentity(t, reopened, worktreeOccupancyID(WorktreeSetID("work-w"), "project-b", claimedB.Entry.ClaimOpID))
	if !found {
		t.Fatal("the recorded legacy landing row did not survive recovery")
	}
	if pid.Valid || pidStart.Valid || hasIdentity != 0 {
		t.Fatalf("recovered destination identity pid=%v start=%v flag=%d, want NULL identity and has_process_identity=0", pid, pidStart, hasIdentity)
	}
}

// TestRecordedPreparationRefusesUnreleasedOldPayloadVariants holds the
// recorded-event preparation route to the released shapes: one valid record
// per kind is recognized with its typed identity, and every unreleased
// variant — unknown fields, wrong types, nested foreign keys, mixed old and
// new members, trailing data, fabricated later-version members, and
// unsupported versions — refuses with a typed failure.
func TestRecordedPreparationRefusesUnreleasedOldPayloadVariants(t *testing.T) {
	t.Parallel()
	requireHistoricalResearchLane(t)

	// Positive controls: each released shape is recognized, adapts in
	// memory, and keeps its semantics.
	landing, _ := legacyLandingEvent("ok-landing", "project-w", "ses-legacy", "/fixtures/landed", nil, time.Unix(40, 0).UTC())
	landingPrepared, err := prepareRecordedEvent(landing)
	if err != nil {
		t.Fatalf("released five-field landing refused: %v", err)
	}
	if landingPrepared.current.recordedPayloadShape != recordedShapeLegacyClaimLanding {
		t.Fatal("the released landing was not recognized as the legacy recorded shape")
	}
	if landingPrepared.current.PayloadVersion != 2 {
		t.Fatalf("recognized landing version=%d, want the current 2", landingPrepared.current.PayloadVersion)
	}

	nativePayload, _ := legacyNativeRunWriterPayload(t, "run-ok", nil)
	nativePrepared, err := prepareRecordedEvent(legacyNativeRunEvent("ok-native", nativePayload))
	if err != nil {
		t.Fatalf("released native-run report refused: %v", err)
	}
	if nativePrepared.current.recordedPayloadShape != recordedShapeLegacyNativeRun {
		t.Fatal("the released native-run report was not recognized as the legacy recorded shape")
	}

	release := histOccupancyReleasedEvent("ok-release", "wt-op-1", "ses-legacy", time.Unix(20, 0).UTC())
	releasePrepared, err := prepareRecordedEvent(release)
	if err != nil {
		t.Fatalf("released occupancy release refused: %v", err)
	}
	adaptedRelease := string(releasePrepared.current.Payload)
	if !strings.Contains(adaptedRelease, `"session_ref":"ses-legacy"`) || strings.Contains(adaptedRelease, "occupant_session_ref") {
		t.Fatalf("adapted release payload = %s, want the retired member translated to session_ref", adaptedRelease)
	}

	dispatchV2 := histDispatchEvent(t, "ok-dispatch", "attempt-ok", 2, time.Unix(31, 0).UTC(), map[string]any{
		"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
		"resolved_model": "openai/gpt-5.6-luna", "resolution_role": "preferred", "fallback_reason": "",
	})
	dispatchPrepared, err := prepareRecordedEvent(dispatchV2)
	if err != nil {
		t.Fatalf("released routing dispatch refused: %v", err)
	}
	adaptedDispatch := string(dispatchPrepared.current.Payload)
	for _, retired := range []string{"routing_policy", "resolved_model", "resolution_role", "fallback_reason"} {
		if strings.Contains(adaptedDispatch, retired) {
			t.Fatalf("adapted dispatch payload = %s, want every %s member retired in memory", adaptedDispatch, retired)
		}
	}
	if !strings.Contains(adaptedDispatch, historicalResearchLaneDigest) {
		t.Fatalf("adapted dispatch payload = %s, want the recorded lane digest preserved", adaptedDispatch)
	}

	failure := histWorkerFailedEvent(t, "ok-failure", "attempt-ok", time.Unix(40, 0).UTC(), []any{
		map[string]any{"session_ref": "ses-1", "directory": "/fixtures/session"},
	})
	failurePrepared, err := prepareRecordedEvent(failure)
	if err != nil {
		t.Fatalf("released abandoned failure refused: %v", err)
	}
	adaptedFailure := string(failurePrepared.current.Payload)
	if strings.Contains(adaptedFailure, "observed_session_directories") {
		t.Fatalf("adapted failure payload = %s, want the obsolete host observation omitted", adaptedFailure)
	}

	for name, testCase := range map[string]struct {
		event Event
		kind  FailureKind
		text  string
	}{
		// worker.failed: retired-member types, nested keys, and fabricated
		// later-version members.
		"failure observation wrong type": {histWorkerFailedEvent(t, "bad-failure-type", "attempt-x", time.Unix(40, 0).UTC(), "none"), KindInvalidPayload, histDecodeRefusal},
		"failure nested unknown key": {histWorkerFailedEvent(t, "bad-failure-nested", "attempt-x", time.Unix(40, 0).UTC(), []any{
			map[string]any{"session_ref": "ses-1", "directory": "/fixtures/session", "host": "extra"},
		}), KindInvalidPayload, histDecodeRefusal},
		"failure nested missing directory": {histWorkerFailedEvent(t, "bad-failure-missing", "attempt-x", time.Unix(40, 0).UTC(), []any{
			map[string]any{"session_ref": "ses-1"},
		}), KindInvalidPayload, "invalid recorded host session observation"},
		"failure observation null":            {histWorkerFailedEvent(t, "bad-failure-null", "attempt-x", time.Unix(40, 0).UTC(), nil), KindInvalidPayload, "requires the recorded host session observation"},
		"failure fabricated context findings": {withPayloadMember(t, histWorkerFailedEvent(t, "bad-failure-findings", "attempt-x", time.Unix(40, 0).UTC(), []any{}), `"context_findings":[]`), KindInvalidPayload, histDecodeRefusal},
		"failure trailing data":               {withTrailingBytes(t, histWorkerFailedEvent(t, "bad-failure-trailing", "attempt-x", time.Unix(40, 0).UTC(), []any{})), KindInvalidPayload, "payload is not a JSON object"},
		"failure future version":              {withEventVersion(histWorkerFailedEvent(t, "bad-failure-future", "attempt-x", time.Unix(40, 0).UTC(), []any{}), eventKindRegistry[WorkerFailed].CurrentVersion+1), KindUnsupportedPayloadVersion, "supported range is"},

		// worker.dispatched: fabricated later members, resolution identity,
		// and version coupling.
		"dispatch v1 fabricated resolution role": {histDispatchEvent(t, "bad-dispatch-role", "attempt-x", 1, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "resolved_model": "openai/gpt-5.6-luna", "resolution_role": "preferred",
		}), KindInvalidPayload, "later-version members"},
		"dispatch v1 fabricated provenance": {histDispatchEvent(t, "bad-dispatch-provenance", "attempt-x", 1, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "resolved_model": "openai/gpt-5.6-luna", "host_provenance": histDeclaredHostProvenance(),
		}), KindInvalidPayload, "later-version members"},
		"dispatch v1 fabricated actor ref": {histDispatchEvent(t, "bad-dispatch-actor", "attempt-x", 1, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "resolved_model": "openai/gpt-5.6-luna", "lane_actor_ref": "agent/lane:research",
		}), KindInvalidPayload, histDecodeRefusal},
		"dispatch v1 fabricated worker job": {histDispatchEvent(t, "bad-dispatch-job", "attempt-x", 1, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "resolved_model": "openai/gpt-5.6-luna",
			"worker_job": map[string]any{"job_id": "job:1", "revision": 1, "digest": "sha256:" + strings.Repeat("0", 64)},
		}), KindInvalidPayload, histDecodeRefusal},
		"dispatch v2 missing policy digest": {histDispatchEvent(t, "bad-dispatch-nodigest", "attempt-x", 2, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "resolved_model": "openai/gpt-5.6-luna",
			"resolution_role": "preferred", "fallback_reason": "",
		}), KindInvalidPayload, "missing its resolution identity"},
		"dispatch v2 preferred with fallback reason": {histDispatchEvent(t, "bad-dispatch-pref-reason", "attempt-x", 2, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
			"resolved_model": "openai/gpt-5.6-luna", "resolution_role": "preferred", "fallback_reason": "rate_limit",
		}), KindInvalidPayload, "preferred dispatch has fallback or terminal evidence"},
		"dispatch v2 preferred with terminal": {histDispatchEvent(t, "bad-dispatch-pref-terminal", "attempt-x", 2, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
			"resolved_model": "openai/gpt-5.6-luna", "resolution_role": "preferred", "fallback_reason": "",
			"terminal": "failed", "terminal_failure_kind": WorkerFailureModelIdentity,
		}), KindInvalidPayload, "preferred dispatch has fallback or terminal evidence"},
		"dispatch v2 unknown fallback reason": {histDispatchEvent(t, "bad-dispatch-reason", "attempt-x", 2, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
			"resolved_model": "zai-coding-plan/glm-5.3", "resolution_role": "fallback", "fallback_reason": "because",
		}), KindInvalidPayload, "invalid reason"},
		"dispatch v2 unknown role": {histDispatchEvent(t, "bad-dispatch-role2", "attempt-x", 2, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
			"resolved_model": "openai/gpt-5.6-luna", "resolution_role": "speculative", "fallback_reason": "",
		}), KindInvalidPayload, "invalid resolution role"},
		"dispatch routing version wrong type": {histDispatchEvent(t, "bad-dispatch-typenum", "attempt-x", 2, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": 7, "routing_policy_digest": historicalRoutingPolicyDigest,
			"resolved_model": "openai/gpt-5.6-luna", "resolution_role": "preferred", "fallback_reason": "",
		}), KindInvalidPayload, histDecodeRefusal},
		"dispatch v3 fabricated packet digest": {histDispatchEvent(t, "bad-dispatch-packetdigest", "attempt-x", 3, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
			"resolved_model": "openai/gpt-5.6-luna", "resolution_role": "preferred", "fallback_reason": "",
			"host_provenance": histDeclaredHostProvenance(), "packet_digest": "sha256:" + strings.Repeat("0", 64),
		}), KindInvalidPayload, histDecodeRefusal},
		"dispatch trailing data": {withTrailingBytes(t, histDispatchEvent(t, "bad-dispatch-trailing", "attempt-x", 2, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "routing_policy_digest": historicalRoutingPolicyDigest,
			"resolved_model": "openai/gpt-5.6-luna", "resolution_role": "preferred", "fallback_reason": "",
		})), KindInvalidPayload, "payload is not a JSON object"},
		"dispatch future version": {histDispatchEvent(t, "bad-dispatch-future", "attempt-x", eventKindRegistry[WorkerDispatched].CurrentVersion+1, time.Unix(30, 0).UTC(), nil), KindUnsupportedPayloadVersion, "supported range is"},

		// Claim landing: mixed old and new members and wrong types.
		"landing fabricated host identity":    {withPayloadMember(t, landing, `"host_pid":7`), KindInvalidPayload, "matches no released schema"},
		"landing fabricated claim generation": {withPayloadMember(t, landing, `"claim_op_id":"wt-op-1"`), KindInvalidPayload, "matches no released schema"},
		"landing session wrong type":          {withReplacedPayloadText(t, landing, `"session_ref":"ses-legacy"`, `"session_ref":7`), KindInvalidPayload, histDecodeRefusal},
		"landing trailing data":               {withTrailingBytes(t, landing), KindInvalidPayload, "payload is not a JSON object"},

		// Occupancy release: mixed identities and missing identity.
		"release mixed session identities": {withPayloadMember(t, release, `"session_ref":"ses-legacy"`), KindInvalidPayload, "matches no released schema"},
		"release null occupant":            {withReplacedPayloadText(t, release, `"occupant_session_ref":"ses-legacy"`, `"occupant_session_ref":null`), KindInvalidPayload, "missing required identity"},
		"release occupant wrong type":      {withReplacedPayloadText(t, release, `"occupant_session_ref":"ses-legacy"`, `"occupant_session_ref":7`), KindInvalidPayload, histDecodeRefusal},
		"release missing set identity":     {withReplacedPayloadText(t, release, `"set_id":"wts:work-w"`, `"set_id":""`), KindInvalidPayload, "missing required identity"},
		"release future version":           {withEventVersion(release, eventKindRegistry["work.worktree_occupancy_released"].CurrentVersion+1), KindUnsupportedPayloadVersion, "supported range is"},

		// Native run: mixed universes, nested members, and trailing data.
		"native fabricated observation identity": {withPayloadMember(t, legacyNativeRunEvent("bad-native-obs", nativePayload), `"observation_id":"xobs:0123456789abcdef"`), KindInvalidPayload, "matches no released schema"},
		"native current universe shape":          {withReplacedPayloadText(t, legacyNativeRunEvent("bad-native-universe", nativePayload), `"total":"eq(1)"`, `"anchor_token":"sha256:`+strings.Repeat("0", 64)+`"`), KindInvalidPayload, histDecodeRefusal},
		"native total wrong type":                {withReplacedPayloadText(t, legacyNativeRunEvent("bad-native-total", nativePayload), `"total":"eq(1)"`, `"total":1`), KindInvalidPayload, histDecodeRefusal},
		"native nested unknown member":           {withReplacedPayloadText(t, legacyNativeRunEvent("bad-native-nested", nativePayload), `"total":"eq(1)"`, `"total":"eq(1)","structure_digest":"sha256:`+strings.Repeat("0", 64)+`"`), KindInvalidPayload, histDecodeRefusal},
		"native trailing data":                   {withTrailingBytes(t, legacyNativeRunEvent("bad-native-trailing", nativePayload)), KindInvalidPayload, "payload is not a JSON object"},
		"native future version":                  {withEventVersion(legacyNativeRunEvent("bad-native-future", nativePayload), eventKindRegistry[WorkflowNativeRunRecorded].CurrentVersion+1), KindUnsupportedPayloadVersion, "supported range is"},
	} {
		_, err := prepareRecordedEvent(testCase.event)
		var failure *Failure
		if !failureAs(err, &failure) {
			t.Fatalf("%s must refuse, got err=%v", name, err)
		}
		if failure.Kind != testCase.kind {
			t.Fatalf("%s refusal kind=%s, want %s", name, failure.Kind, testCase.kind)
		}
		if !strings.Contains(failure.Detail, testCase.text) {
			t.Fatalf("%s refusal detail=%q, want it to name %q", name, failure.Detail, testCase.text)
		}
	}
}

// withPayloadMember appends one member to a closed payload object.
func withPayloadMember(t *testing.T, event Event, member string) Event {
	t.Helper()
	if !strings.HasSuffix(string(event.Payload), "}") {
		t.Fatal("fixture payload must be a JSON object")
	}
	return withEventPayload(event, strings.TrimSuffix(string(event.Payload), "}")+","+member+"}")
}

// withReplacedPayloadText swaps one member's exact recorded text.
func withReplacedPayloadText(t *testing.T, event Event, from, to string) Event {
	t.Helper()
	if !strings.Contains(string(event.Payload), from) {
		t.Fatalf("fixture payload %s must carry %s", event.Payload, from)
	}
	return withEventPayload(event, strings.Replace(string(event.Payload), from, to, 1))
}

// withTrailingBytes appends a second JSON value after the payload object.
func withTrailingBytes(t *testing.T, event Event) Event {
	t.Helper()
	return withEventPayload(event, string(event.Payload)+` {"x":1}`)
}

func withEventPayload(event Event, payload string) Event {
	event.Payload = jsonRaw(payload)
	return event
}

func withEventVersion(event Event, version int) Event {
	event.PayloadVersion = version
	return event
}

// Every recorded old shape stays refused on every live write route: the
// operation commits no event and no projection, and a landing request that
// names header version 1 is judged by the current fold — it cannot gain the
// recorded legacy identity by carrying an old header.
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
	countEvents := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var versionBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-w'`).Scan(&versionBefore); err != nil {
		t.Fatal(err)
	}

	nativePayload, _ := legacyNativeRunWriterPayload(t, "run-live", func(payload map[string]any) {
		payload["expected_version"] = versionBefore
		payload["resulting_version"] = versionBefore + 1
	})
	for name, testCase := range map[string]struct {
		event Event
		text  string
	}{
		"occupancy release occupant_session_ref":     {histOccupancyReleasedEvent("live-old-release", claimed.Entry.ClaimOpID, "ses-held", time.Unix(20, 0).UTC()), histDecodeRefusal},
		"worker failed observed_session_directories": {histWorkerFailedEvent(t, "live-old-failure", "attempt-live", time.Unix(40, 0).UTC(), []any{}), histDecodeRefusal},
		"worker dispatched routing members": {histDispatchEvent(t, "live-old-dispatch", "attempt-live", 1, time.Unix(30, 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "resolved_model": "openai/gpt-5.6-luna",
		}), histDecodeRefusal},
		"worker dispatched digestless readback": {histDispatchEvent(t, "live-digestless-dispatch", "attempt-live", 3, time.Unix(30, 0).UTC(), map[string]any{
			"readback_model": "", "host_provenance": histDeclaredHostProvenance(),
		}), "packet_digest must be a sha256 digest"},
		"native run map universe": {legacyNativeRunEvent("live-old-native", nativePayload), histDecodeRefusal},
	} {
		before := countEvents(t)
		operationErr := ApplyOperation(ctx, s, Operation{Events: []Event{testCase.event}})
		var failure *Failure
		if !failureAs(operationErr, &failure) {
			t.Fatalf("live %s err=%v, want a typed refusal", name, operationErr)
		}
		if failure.Kind != KindInvalidPayload {
			t.Fatalf("live %s refusal kind=%s, want %s", name, failure.Kind, KindInvalidPayload)
		}
		if !strings.Contains(failure.Detail, testCase.text) {
			t.Fatalf("live %s refusal detail=%q, want it to name %q", name, failure.Detail, testCase.text)
		}
		if after := countEvents(t); after != before {
			t.Fatalf("live %s committed %d events, want none", name, after-before)
		}
	}

	// A header version 1 landing request reaches only the current fold: the
	// recorded legacy identity is unreachable from any write route, so the
	// refusal is the current missing-host-pid rule and the append commits
	// nothing.
	landing, _ := legacyLandingEvent("live-old-landing", "project-w", "ses-held", claimed.Entry.Path, nil, time.Unix(22, 0).UTC())
	before := countEvents(t)
	landingErr := ApplyOperation(ctx, s, Operation{Events: []Event{landing}})
	var failure *Failure
	if !failureAs(landingErr, &failure) || !strings.Contains(failure.Detail, "missing host_pid") {
		t.Fatalf("header-1 landing err=%v, want the current missing host_pid refusal", landingErr)
	}
	if after := countEvents(t); after != before {
		t.Fatalf("header-1 landing committed %d events, want none", after-before)
	}
	if got := worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", claimed.Entry.ClaimOpID); got != "ses-held" {
		t.Fatalf("occupancy after the refused landing records %q, want the claim's session untouched", got)
	}
	var versionAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-w'`).Scan(&versionAfter); err != nil {
		t.Fatal(err)
	}
	if versionAfter != versionBefore {
		t.Fatalf("work version=%d after the refused operations, want %d", versionAfter, versionBefore)
	}
	var attempts int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM worker_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("worker_attempts holds %d rows after the refused operations, want 0", attempts)
	}
}

// TestRecordedEarlyWorkerTerminalHistoryRebuildsAndRecords covers the early
// released worker terminal forms as corpus witnesses: the v0.37.2 completion
// and basic failure pair, and the four completion v2 generations the registry
// accepted between v0.61.0 and v11.52.0. Each row carries only the members
// its generation's writer emitted — the three-field v1 completion, the
// reported-evidence v2 base, then the directory, base-comparison, and
// predicate-ids generations — pinned to the recorded research lane the
// dispatches already use. The log must rebuild byte-identically and the
// attempts must fold to their terminal states.
func TestRecordedEarlyWorkerTerminalHistoryRebuildsAndRecords(t *testing.T) {
	t.Parallel()
	requireHistoricalResearchLane(t)
	s, _, _ := worktreeFixture(t)
	ctx := context.Background()
	lane, err := LookupLane("research", 1, historicalResearchLaneDigest)
	if err != nil {
		t.Fatal(err)
	}
	laneEvidence := func(withPredicates bool) []any {
		entries := make([]any, 0, len(lane.EvidenceObligations))
		for _, obligation := range lane.EvidenceObligations {
			entry := map[string]any{"obligation": obligation, "detail": "discharged " + obligation}
			if withPredicates {
				entry["predicate_ids"] = []any{"predicate:" + obligation}
			}
			entries = append(entries, entry)
		}
		return entries
	}
	completed := func(eventID, attemptID string, version int, at time.Time, members map[string]any) Event {
		payload := map[string]any{
			"attempt_id": attemptID, "readback_model": "openai/gpt-5.6-luna", "report_schema_version": "1.0",
		}
		for member, value := range members {
			payload[member] = value
		}
		return histEventWithPayload(t, Event{EventID: eventID, Kind: WorkerCompleted, SubjectType: SubjectWorkItem,
			SubjectID: "work-w", Actor: "worker:cli", OccurredAt: at, PayloadVersion: version}, payload)
	}
	hist2 := []string{"attempt-hist2-v1", "attempt-hist2-v2-base", "attempt-hist2-v2-dir", "attempt-hist2-v2-cmp", "attempt-hist2-v2-pred", "attempt-hist2-fail"}
	for i, attempt := range hist2 {
		insertHistoricalEvent(t, s, histDispatchEvent(t, "hist2-dispatch-"+attempt, attempt, 1, time.Unix(50+int64(i), 0).UTC(), map[string]any{
			"routing_policy_version": historicalRoutingPolicyToken, "resolved_model": "openai/gpt-5.6-luna",
		}))
	}
	insertHistoricalEvent(t, s, completed("hist2-complete-v1", hist2[0], 1, time.Unix(60, 0).UTC(), nil))
	insertHistoricalEvent(t, s, completed("hist2-complete-v2-base", hist2[1], 2, time.Unix(61, 0).UTC(), map[string]any{
		"evidence_origin": WorkerEvidenceReported, "evidence": laneEvidence(false),
	}))
	insertHistoricalEvent(t, s, completed("hist2-complete-v2-dir", hist2[2], 2, time.Unix(62, 0).UTC(), map[string]any{
		"worker_directory": "worker-hist2-directory", "evidence_origin": WorkerEvidenceReported, "evidence": laneEvidence(false),
	}))
	insertHistoricalEvent(t, s, completed("hist2-complete-v2-cmp", hist2[3], 2, time.Unix(63, 0).UTC(), map[string]any{
		"worker_directory": "worker-hist2-directory", "evidence_origin": WorkerEvidenceReported, "evidence": laneEvidence(false),
		"base_comparison": map[string]any{"checks": []any{map[string]any{"command": "go test ./...", "branch_result": "pass", "base_result": "pass"}}},
	}))
	insertHistoricalEvent(t, s, completed("hist2-complete-v2-pred", hist2[4], 2, time.Unix(64, 0).UTC(), map[string]any{
		"worker_directory": "worker-hist2-directory", "evidence_origin": WorkerEvidenceReported, "evidence": laneEvidence(true),
	}))
	insertHistoricalEvent(t, s, histEventWithPayload(t, Event{EventID: "hist2-failed-v1", Kind: WorkerFailed, SubjectType: SubjectWorkItem,
		SubjectID: "work-w", Actor: "worker:cli", OccurredAt: time.Unix(65, 0).UTC(), PayloadVersion: 1}, map[string]any{
		"attempt_id": hist2[5], "readback_model": "openai/gpt-5.6-luna",
		"failure_kind": WorkerFailureModelIdentity, "detail": "resolved model differs from host readback model",
	}))

	before := histEventRows(t, s)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild must fold the early worker terminal history: %v", err)
	}
	if !reflect.DeepEqual(before, histEventRows(t, s)) {
		t.Fatal("the first rebuild changed the event log bytes, versions, or order")
	}
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("repeated rebuild must fold the early worker terminal history again: %v", err)
	}
	if !reflect.DeepEqual(before, histEventRows(t, s)) {
		t.Fatal("the repeated rebuild changed the event log bytes, versions, or order")
	}
	for _, attempt := range hist2[:5] {
		if got := histAttemptRow(t, s.DatabaseForTesting(), attempt); got.lifecycle != "completed" {
			t.Fatalf("attempt %s lifecycle=%s, want completed", attempt, got.lifecycle)
		}
	}
	if got := histAttemptRow(t, s.DatabaseForTesting(), hist2[5]); got.lifecycle != "failed" || got.failureKind != WorkerFailureModelIdentity {
		t.Fatalf("attempt %s lifecycle=%s failure=%s, want failed/%s", hist2[5], got.lifecycle, got.failureKind, WorkerFailureModelIdentity)
	}
}
