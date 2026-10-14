package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

// The released payload shapes these tests
// record are the bytes the historical writers emitted: the five-field claim
// landing from before CD-0178 D3 introduced host process identity, and the
// native-run report the current capture component replaced. Prerequisites are
// complete synthetic histories; the historical event itself is inserted the
// way its writer recorded it, as a raw log row the live route can no longer
// produce, and replay folds it through the recorded-event route. No recorded
// row is ever rewritten.

// legacyLandingEvent builds one released five-field landing event.
func legacyLandingEvent(eventID, projectID, sessionRef, landedDirectory string, sources []string, at time.Time) (Event, []byte) {
	payload, err := json.Marshal(legacySessionClaimLandedPayload{
		WorkID: "work-w", ProjectID: projectID, SessionRef: sessionRef,
		SourceDirectories: sources, LandedDirectory: landedDirectory,
	})
	if err != nil {
		panic(err)
	}
	return Event{
		EventID: eventID, Kind: "work.session_claim_landed",
		SubjectType: SubjectWorkItem, SubjectID: "work-w",
		Actor: sessionRef, OccurredAt: at, PayloadVersion: 1, Payload: payload,
	}, payload
}

// insertHistoricalEvent records one event row the way its historical writer
// left it: never folded live, never rewritten afterwards.
func insertHistoricalEvent(t *testing.T, s *Store, event Event) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`,
		event.EventID, event.Kind, string(event.SubjectType), event.SubjectID, event.Actor, event.OccurredAt.Format(time.RFC3339Nano), event.PayloadVersion, event.Payload); err != nil {
		t.Fatal(err)
	}
}

// storedEventPayload returns the durable payload bytes of one event row.
func storedEventPayload(t *testing.T, s *Store, eventID string) []byte {
	t.Helper()
	var payload []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, eventID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

// legacyOccupancyIdentity reads one occupancy row's process identity. found
// is false when the row does not exist.
func legacyOccupancyIdentity(t *testing.T, s *Store, worktreeID string) (pid sql.NullInt64, pidStart sql.NullInt64, hasIdentity int, found bool) {
	t.Helper()
	err := s.DatabaseForTesting().QueryRow(`SELECT host_pid, host_pid_start, has_process_identity FROM worktree_occupancy WHERE worktree_id=? AND session_ref=?`, worktreeID, "ses-legacy").Scan(&pid, &pidStart, &hasIdentity)
	if err == sql.ErrNoRows {
		return sql.NullInt64{}, sql.NullInt64{}, 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return pid, pidStart, hasIdentity, true
}

// A landing recorded before host process identity existed replays as a legacy
// occupancy row: the destination row holds the session with NULL pid and
// start and has_process_identity=0, the session's other rows clear, the
// recorded event bytes stay untouched, and a repeated rebuild agrees.
func TestLegacySessionClaimLandingReplaysAsProcessIdentityFreeRow(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	siblingProjectFixture(t, s, git)
	ctx := context.Background()

	reqA := baseClaim(git)
	reqA.SessionRef = "ses-legacy"
	reqA.ExpectedVersion = 3
	claimedA, err := s.ClaimWorktree(ctx, reqA)
	if err != nil {
		t.Fatal(err)
	}
	reqB := siblingClaim(git, "wt-op-2", 4)
	reqB.SessionRef = "ses-legacy"
	claimedB, err := s.ClaimWorktree(ctx, reqB)
	if err != nil {
		t.Fatal(err)
	}

	landing, payload := legacyLandingEvent("legacy-landing-1", "project-b", "ses-legacy", claimedB.Entry.Path, []string{claimedA.Entry.Path}, time.Unix(40, 0).UTC())
	insertHistoricalEvent(t, s, landing)
	seedRecordedLandingOccupancy(t, s, landing)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild must fold the released five-field landing: %v", err)
	}

	destinationID := worktreeOccupancyID(WorktreeSetID("work-w"), "project-b", claimedB.Entry.ClaimOpID)
	pid, pidStart, hasIdentity, found := legacyOccupancyIdentity(t, s, destinationID)
	if !found {
		t.Fatal("the legacy landing must record the destination occupancy row")
	}
	if pid.Valid || pidStart.Valid || hasIdentity != 0 {
		t.Fatalf("destination identity pid=%v start=%v flag=%d, want NULL identity and has_process_identity=0", pid, pidStart, hasIdentity)
	}
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", claimedA.Entry.ClaimOpID) != "" {
		t.Fatal("the legacy landing must clear the session's source row")
	}
	if string(storedEventPayload(t, s, "legacy-landing-1")) != string(payload) {
		t.Fatal("replay must leave the recorded landing bytes untouched")
	}

	// A second rebuild folds the same bytes to the same projection state.
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("repeated rebuild must agree: %v", err)
	}
	if _, _, _, found := legacyOccupancyIdentity(t, s, destinationID); !found {
		t.Fatal("the repeated rebuild must hold the same legacy row")
	}
}

// A released landing at a path with no active worktree row refuses, and a
// refused replay leaves the recorded bytes and the prior projection intact.
func TestLegacySessionClaimLandingAbsentPathStillRefuses(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()

	claim := baseClaim(git)
	claim.SessionRef = "ses-legacy"
	if _, err := s.ClaimWorktree(ctx, claim); err != nil {
		t.Fatal(err)
	}

	absent, payload := legacyLandingEvent("legacy-landing-absent", "project-w", "ses-legacy", t.TempDir()+"/nowhere", nil, time.Unix(40, 0).UTC())
	insertHistoricalEvent(t, s, absent)
	err := RebuildFromLog(ctx, s)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindProjectionNotFound || !strings.Contains(failure.Detail, "landed path has no active worktree row") {
		t.Fatalf("absent-path landing err=%v, want %s naming the unresolved path", err, KindProjectionNotFound)
	}
	if string(storedEventPayload(t, s, "legacy-landing-absent")) != string(payload) {
		t.Fatal("a refused replay must leave the recorded bytes untouched")
	}
	if worktreeOccupancyByEntry(t, s, WorktreeSetID("work-w"), "project-w", claim.OpID) != "ses-legacy" {
		t.Fatal("a refused replay must leave the prior occupancy state intact")
	}
}

// A current-shape v1 landing recorded against a claim generation the landed
// path no longer carries keeps its generation refusal through the shared
// transfer owner.
func TestCurrentShapeClaimLandingGenerationCheckPreserved(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()

	claim := baseClaim(git)
	claim.SessionRef = "ses-legacy"
	claimed, err := s.ClaimWorktree(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	staleGeneration := Event{
		EventID: "stale-generation-landing", Kind: "work.session_claim_landed",
		SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "ses-legacy",
		OccurredAt: time.Unix(41, 0).UTC(), PayloadVersion: 1,
		Payload: jsonRaw(`{"work_id":"work-w","project_id":"project-w","claim_op_id":"wt-op-stale","session_ref":"ses-legacy","source_directories":[],"landed_directory":"` + claimed.Entry.Path + `","host_pid":7,"host_pid_start":999}`),
	}
	insertHistoricalEvent(t, s, staleGeneration)
	err = RebuildFromLog(ctx, s)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "claim generation the landed path no longer carries") {
		t.Fatalf("stale-generation landing err=%v, want a claim-generation refusal", err)
	}
}

// The live refusals stay exactly where they were: an untagged landing without
// a host pid refuses at the fold, the five-field shape without the recorded
// shape tag refuses the same way, and the live route still refuses a landing
// request without a host pid.
func TestLiveClaimLandingMissingHostPIDStillRefused(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()

	claim := baseClaim(git)
	claim.SessionRef = "ses-legacy"
	claimed, err := s.ClaimWorktree(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}

	untaggedLegacy, _ := legacyLandingEvent("untagged-landing", "project-w", "ses-legacy", claimed.Entry.Path, nil, time.Unix(40, 0).UTC())
	err = foldSessionClaimLanded(ctx, nil, untaggedLegacy)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "missing host_pid") {
		t.Fatalf("untagged five-field fold err=%v, want the missing host_pid refusal", err)
	}

	untaggedCurrent := untaggedLegacy
	untaggedCurrent.Payload = jsonRaw(`{"work_id":"work-w","project_id":"project-w","claim_op_id":"` + claimed.Entry.ClaimOpID + `","session_ref":"ses-legacy","source_directories":[],"landed_directory":"` + claimed.Entry.Path + `","host_pid":0,"host_pid_start":0}`)
	err = foldSessionClaimLanded(ctx, nil, untaggedCurrent)
	failure = nil
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "missing host_pid") {
		t.Fatalf("current-shape fold without pid err=%v, want the missing host_pid refusal", err)
	}

	_, err = s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-legacy", LandedDirectory: claimed.Entry.Path, Now: time.Unix(42, 0).UTC()})
	failure = nil
	if !failureAs(err, &failure) || !strings.Contains(failure.Detail, "landing requires the host process pid") {
		t.Fatalf("live landing without pid err=%v, want the live pid refusal", err)
	}
}

// The released landing validator admits only the exact five-field shape: the
// later process-identity and claim-generation members refuse as unknown, and
// a required member or a well-formed type cannot be dropped.
func TestLegacySessionClaimLandingValidatorRejectsLaterMembers(t *testing.T) {
	t.Parallel()
	valid, _ := legacyLandingEvent("v", "project-w", "ses-legacy", "/fixtures/landed", nil, time.Unix(40, 0).UTC())
	if err := validateLegacySessionClaimLandedPayload(valid); err != nil {
		t.Fatalf("released five-field landing err=%v, want acceptance", err)
	}

	for name, payload := range map[string]string{
		"host identity member":    `{"work_id":"work-w","project_id":"project-w","session_ref":"ses-legacy","source_directories":[],"landed_directory":"/fixtures/landed","host_pid":7,"host_pid_start":9}`,
		"claim generation member": `{"work_id":"work-w","project_id":"project-w","claim_op_id":"wt-op-1","session_ref":"ses-legacy","source_directories":[],"landed_directory":"/fixtures/landed"}`,
		"wrong type":              `{"work_id":"work-w","project_id":"project-w","session_ref":7,"source_directories":[],"landed_directory":"/fixtures/landed"}`,
		"missing landed path":     `{"work_id":"work-w","project_id":"project-w","session_ref":"ses-legacy","source_directories":[]}`,
	} {
		event := valid
		event.EventID = "invalid-" + strings.ReplaceAll(name, " ", "-")
		event.Payload = jsonRaw(payload)
		if err := validateLegacySessionClaimLandedPayload(event); err == nil {
			t.Fatalf("%s must refuse, want a typed refusal", name)
		}
	}
}

// legacyNativeRunWriterPayload marshals the released native-run report the
// way its writer emitted it, including the map-form universe, and returns the
// subject digest that writer derived. The mutation hook derives every
// negative case from the same emitted form.
func legacyNativeRunWriterPayload(t *testing.T, runID string, mutate func(map[string]any)) ([]byte, string) {
	t.Helper()
	const subjectRef = "/service/checkout"
	sum := sha256.Sum256([]byte(subjectRef))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	payload := map[string]any{
		"work_id": "work-w", "run_id": runID, "native_subject_ref": subjectRef,
		"subject_digest": digest, "phase": "start", "status": "started",
		"evidence_ref": "run-legacy-evidence", "evidence_digest": "legacy-evidence-digest",
		"asserted_at":             time.Unix(50, 0).UTC().Format(time.RFC3339Nano),
		"reporting_authority_ref": "client-legacy", "actor_ref": "agent:legacy",
		"capture_method": "trusted_client_report",
		"observed_universe": map[string]any{
			"shape": "item", "applied_scope": subjectRef, "anchor": digest,
			"coverage": "complete", "observed_count": 1, "observed_refs": []string{runID},
			"total": "eq(1)", "completion_evidence": "closed_structure_digest",
			"canonical_identity_key": "work-w/" + runID, "omissions": []string{},
		},
		"freshness_policy_ref":  legacyNativeRunFreshnessPolicyRef,
		"divergence_policy_ref": legacyNativeRunDivergencePolicyRef,
		"expected_version":      2, "resulting_version": 3,
	}
	if mutate != nil {
		mutate(payload)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw, digest
}

func legacyNativeRunEvent(eventID string, payload []byte) Event {
	return Event{
		EventID: eventID, Kind: WorkflowNativeRunRecorded,
		SubjectType: SubjectWorkItem, SubjectID: "work-w",
		Actor: "agent:legacy", OccurredAt: time.Unix(51, 0).UTC(), PayloadVersion: 1, Payload: payload,
	}
}

// jsonMember renders one JSON-decoded value so fixture maps compare without
// importing reflection into the assertion style.
func jsonMember(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(encoded)
}

// The released native-run validator admits only the emitted writer form: no
// fabricated observation identity, no current-policy relabeling, no current
// universe members, and the original bounds and vocabulary.
func TestLegacyNativeRunValidatorAcceptsOnlyRecordedWriterForm(t *testing.T) {
	t.Parallel()
	validPayload, _ := legacyNativeRunWriterPayload(t, "run-legacy", nil)
	valid := legacyNativeRunEvent("legacy-native-valid", validPayload)
	if err := validateLegacyNativeRunPayload(valid); err != nil {
		t.Fatalf("released native-run report err=%v, want acceptance", err)
	}

	universeMutation := func(apply func(universe map[string]any)) func(map[string]any) {
		return func(payload map[string]any) {
			universe, ok := payload["observed_universe"].(map[string]any)
			if !ok {
				t.Fatal("fixture universe must be a JSON object")
			}
			apply(universe)
		}
	}
	cases := map[string]struct {
		mutate  func(map[string]any)
		refusal string
	}{
		"fabricated observation identity": {func(payload map[string]any) { payload["observation_id"] = "xobs:0123456789abcdef" }, "unknown field"},
		"current policy reference": {func(payload map[string]any) {
			payload["freshness_policy_ref"] = "native_run@sha256:00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
		}, "recorded static references"},
		"current capture identity": {func(payload map[string]any) { payload["capture_method"] = "core_probe" }, "trusted-client report"},
		"current universe member": {universeMutation(func(u map[string]any) {
			u["structure_digest"] = "sha256:00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
		}), "unknown field"},
		"wrong total type":     {universeMutation(func(u map[string]any) { u["total"] = 1 }), "cannot unmarshal"},
		"other total token":    {universeMutation(func(u map[string]any) { u["total"] = "eq(2)" }), "recorded writer form"},
		"other observed count": {universeMutation(func(u map[string]any) { u["observed_count"] = 2 }), "recorded writer form"},
		"other observed ref":   {universeMutation(func(u map[string]any) { u["observed_refs"] = []string{"other"} }), "recorded writer form"},
		"missing omissions":    {universeMutation(func(u map[string]any) { delete(u, "omissions") }), "recorded writer form"},
		"foreign anchor": {universeMutation(func(u map[string]any) {
			u["anchor"] = "sha256:00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
		}), "its own report"},
		"status outside vocabulary": {func(payload map[string]any) { payload["phase"] = "deploy"; payload["status"] = "ok" }, "phase vocabulary"},
		"unparsable asserted time":  {func(payload map[string]any) { payload["asserted_at"] = "not-a-time" }, "RFC3339"},
	}
	for name, testCase := range cases {
		payload, _ := legacyNativeRunWriterPayload(t, "run-legacy", testCase.mutate)
		err := validateLegacyNativeRunPayload(legacyNativeRunEvent("legacy-native-"+strings.ReplaceAll(name, " ", "-"), payload))
		if err == nil {
			t.Fatalf("%s must refuse, want a typed refusal", name)
		}
		if testCase.refusal != "" && !strings.Contains(err.Error(), testCase.refusal) {
			t.Fatalf("%s err=%v, want a refusal naming %q", name, err, testCase.refusal)
		}
	}

	trailingPayload, _ := legacyNativeRunWriterPayload(t, "run-legacy", nil)
	trailingPayload = append(trailingPayload, []byte(` {"x":1}`)...)
	if err := validateLegacyNativeRunPayload(legacyNativeRunEvent("legacy-native-trailing", trailingPayload)); err == nil {
		t.Fatal("trailing data must refuse")
	}
}

// A released native-run report folds to the schema-42 migration result: the
// row keeps the old universe JSON and static policy references, binds no
// observation, reads unverified, and no external observation is fabricated.
// The recorded bytes stay untouched and a repeated rebuild agrees.
func TestLegacyNativeRunFoldPersistsRecordedRowWithoutObservation(t *testing.T) {
	t.Parallel()
	s, _, _ := worktreeFixture(t)
	ctx := context.Background()

	const runID = "run-legacy"
	payload, subjectDigest := legacyNativeRunWriterPayload(t, runID, nil)
	insertHistoricalEvent(t, s, legacyNativeRunEvent("legacy-native-run-1", payload))
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("rebuild must fold the released native-run report: %v", err)
	}

	var status, eventID, freshness, divergence, captureMethod, subjectDigestStored, universeText, verificationState string
	var observationID sql.NullString
	if err := s.DatabaseForTesting().QueryRow(`SELECT status,event_id,freshness_policy_ref,divergence_policy_ref,capture_method,subject_digest,observed_universe,verification_state,observation_id FROM workflow_native_runs WHERE work_id='work-w' AND run_id=? AND phase='start'`, runID).
		Scan(&status, &eventID, &freshness, &divergence, &captureMethod, &subjectDigestStored, &universeText, &verificationState, &observationID); err != nil {
		t.Fatal(err)
	}
	if status != "started" || eventID != "legacy-native-run-1" {
		t.Fatalf("row status=%q event=%q, want the recorded report", status, eventID)
	}
	if subjectDigestStored != subjectDigest {
		t.Fatalf("subject digest=%q, want the recorded %q", subjectDigestStored, subjectDigest)
	}
	if freshness != legacyNativeRunFreshnessPolicyRef || divergence != legacyNativeRunDivergencePolicyRef {
		t.Fatalf("policy refs freshness=%q divergence=%q, want the recorded static references", freshness, divergence)
	}
	if captureMethod != string(CaptureTrustedClientReport) {
		t.Fatalf("capture method=%q, want the recorded trusted-client report", captureMethod)
	}
	if observationID.Valid {
		t.Fatalf("observation id=%q, want NULL", observationID.String)
	}
	if verificationState != "unverified" {
		t.Fatalf("verification state=%q, want unverified", verificationState)
	}
	var storedUniverse map[string]any
	if err := json.Unmarshal([]byte(universeText), &storedUniverse); err != nil {
		t.Fatal(err)
	}
	expectedUniverse := map[string]any{
		"shape": "item", "applied_scope": "/service/checkout",
		"anchor":                 subjectDigest,
		"coverage":               "complete",
		"observed_count":         float64(1),
		"observed_refs":          []any{runID},
		"total":                  "eq(1)",
		"completion_evidence":    "closed_structure_digest",
		"canonical_identity_key": "work-w/" + runID,
		"omissions":              []any{},
	}
	if universeText != jsonMember(expectedUniverse) {
		t.Fatalf("stored universe bytes=%q, want the released map encoding %q", universeText, jsonMember(expectedUniverse))
	}
	if len(storedUniverse) != len(expectedUniverse) {
		t.Fatalf("stored universe members=%d, want %d: %v", len(storedUniverse), len(expectedUniverse), storedUniverse)
	}
	for key, want := range expectedUniverse {
		got, exists := storedUniverse[key]
		if !exists {
			t.Fatalf("stored universe is missing member %q", key)
		}
		if jsonMember(want) != jsonMember(got) {
			t.Fatalf("stored universe member %q=%v, want %v", key, got, want)
		}
	}
	var observations int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM external_observations WHERE work_id='work-w'`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if observations != 0 {
		t.Fatalf("external observations=%d, want none fabricated", observations)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT version FROM work_items WHERE id='work-w'`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("work version=%d err=%v, want 3", version, err)
	}
	if string(storedEventPayload(t, s, "legacy-native-run-1")) != string(payload) {
		t.Fatal("replay must leave the recorded report bytes untouched")
	}

	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatalf("repeated rebuild must agree: %v", err)
	}
	var same string
	if err := s.DatabaseForTesting().QueryRow(`SELECT observed_universe FROM workflow_native_runs WHERE work_id='work-w' AND run_id=? AND phase='start'`, runID).Scan(&same); err != nil || same != universeText {
		t.Fatalf("repeated rebuild universe err=%v text=%q, want the same row", err, same)
	}
}

// The live writer emits payload version 2 and its event still passes the
// live validator, so the new-write epoch is unambiguous while the current
// validation stays the only admission a live append can reach.
func TestLiveNativeRunWriterEmitsVersionTwo(t *testing.T) {
	t.Parallel()
	event, err := buildNativeRunEvent("live-native-run", "work-w", WorkflowActor{PrincipalRef: "principal-1", ClientRef: "client-live"}, time.Unix(60, 0).UTC(), 2, "start", "run-live", "/service/checkout", "started", "evidence-live", "digest-live", time.Unix(60, 0).UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if event.PayloadVersion != 2 {
		t.Fatalf("live native-run writer version=%d, want 2", event.PayloadVersion)
	}
	if err := validateNativeRunPayload(event, nativeRunPayload{}); err != nil {
		t.Fatalf("live native-run event err=%v, want acceptance", err)
	}
}

// The live occupancy release writer emits payload version 2. The release
// rides the reclaim's process-end proof: the row records this test process at
// a start time the kernel no longer proves, so the reclaim releases it.
func TestLiveOccupancyReleaseEmitsVersionTwo(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()

	claim := baseClaim(git)
	claim.SessionRef = "ses-claim"
	if _, err := s.ClaimWorktree(ctx, claim); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	pidStart, err := hostlease.ProcessStart(pid)
	if err != nil {
		t.Fatalf("the test process is not observable: %v", err)
	}
	insertWorktreeOccupantWithIdentity(t, s, "work-w", "ses-ended", pid, pidStart-1)
	seedWorktreeLifecycle(t, s)
	vacateHistoricalSession(t, s, "vacate-claim", "ses-claim")

	if _, err := s.ReclaimWorktree(ctx, WorktreeReclaimRequest{
		WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main",
		PrincipalRef: "principal-1", RequestID: "req-release-version", ExpectedVersion: 4,
		Now: time.Unix(25, 0).UTC(), Runner: git,
	}); err != nil {
		t.Fatalf("reclaim releasing the ended row: %v", err)
	}
	var releasedVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version FROM domain_events WHERE kind='work.worktree_occupancy_released' AND json_extract(payload,'$.session_ref')='ses-ended'`).Scan(&releasedVersion); err != nil {
		t.Fatalf("released event err=%v, want the recorded process-end release", err)
	}
	if releasedVersion != 2 {
		t.Fatalf("occupancy release version=%d, want 2", releasedVersion)
	}
}

// vacateHistoricalSession records one released v1 vacate event, the way the
// pre-CD-0178 writer recorded it, to clear one session's occupancy rows.
func vacateHistoricalSession(t *testing.T, s *Store, eventID, sessionRef string) {
	t.Helper()
	entries, err := s.WorktreeEntries(context.Background(), "work-w")
	if err != nil || len(entries) != 1 {
		t.Fatalf("worktree entries=%v err=%v, want one fixture entry", entries, err)
	}
	payload := jsonRaw(`{"work_id":"work-w","project_id":"` + entries[0].ProjectID + `","session_ref":"` + sessionRef + `","source_directory":"` + entries[0].Path + `"}`)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{
		EventID: eventID, Kind: "work.session_vacated", SubjectType: SubjectWorkItem, SubjectID: "work-w",
		Actor: sessionRef, OccurredAt: time.Unix(22, 0).UTC(), PayloadVersion: 1, Payload: payload,
	}}}); err != nil {
		t.Fatal(err)
	}
}

// The live claim-landing writer emits payload version 2.
func TestLiveClaimLandingEmitsVersionTwo(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	ctx := context.Background()

	claim := baseClaim(git)
	claim.SessionRef = "ses-live"
	claimed, err := s.ClaimWorktree(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordWorktreeClaimLanding(ctx, WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-live", LandedDirectory: claimed.Entry.Path, Now: time.Unix(30, 0).UTC(), HostPID: 1}); err != nil {
		t.Fatalf("live landing: %v", err)
	}
	var landingVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version FROM domain_events WHERE kind='work.session_claim_landed' AND subject_id='work-w'`).Scan(&landingVersion); err != nil {
		t.Fatal(err)
	}
	if landingVersion != 2 {
		t.Fatalf("claim landing version=%d, want 2", landingVersion)
	}
}
