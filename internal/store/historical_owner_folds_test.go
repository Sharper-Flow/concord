package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/hostlease"
)

// Migration fixtures retain source bytes without running a historical fold.
func insertHistoricalEvent(t *testing.T, s *Store, event Event) {
	t.Helper()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO domain_events(event_id,kind,subject_type,subject_id,actor,occurred_at,payload_version,payload) VALUES(?,?,?,?,?,?,?,?)`, event.EventID, event.Kind, string(event.SubjectType), event.SubjectID, event.Actor, event.OccurredAt.Format(time.RFC3339Nano), event.PayloadVersion, event.Payload); err != nil {
		t.Fatal(err)
	}
}

func storedEventPayload(t *testing.T, s *Store, eventID string) []byte {
	t.Helper()
	var payload []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload FROM domain_events WHERE event_id=?`, eventID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func legacyNativeRunWriterPayload(t *testing.T, runID string, mutate func(map[string]any)) []byte {
	t.Helper()
	const subjectRef = "/service/checkout"
	sum := sha256.Sum256([]byte(subjectRef))
	digest := "sha256:" + hex.EncodeToString(sum[:])
	payload := map[string]any{
		"work_id": "work-w", "run_id": runID, "native_subject_ref": subjectRef, "subject_digest": digest, "phase": "start", "status": "started",
		"evidence_ref": "run-legacy-evidence", "evidence_digest": "legacy-evidence-digest", "asserted_at": time.Unix(50, 0).UTC().Format(time.RFC3339Nano),
		"reporting_authority_ref": "client-legacy", "actor_ref": "agent:legacy", "capture_method": "trusted_client_report",
		"observed_universe":    map[string]any{"shape": "item", "applied_scope": subjectRef, "anchor": digest, "coverage": "complete", "observed_count": 1, "observed_refs": []string{runID}, "total": "eq(1)", "completion_evidence": "closed_structure_digest", "canonical_identity_key": "work-w/" + runID, "omissions": []string{}},
		"freshness_policy_ref": "policy/native-run-report/freshness@cd-0040", "divergence_policy_ref": "policy/native-run-report/divergence@cd-0040", "expected_version": 2, "resulting_version": 3,
	}
	if mutate != nil {
		mutate(payload)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func legacyNativeRunEvent(eventID string, payload []byte) Event {
	return Event{EventID: eventID, Kind: WorkflowNativeRunRecorded, SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "agent:legacy", OccurredAt: time.Unix(51, 0).UTC(), PayloadVersion: 1, Payload: payload}
}

func jsonMember(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestCurrentShapeClaimLandingGenerationCheckPreserved(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claim := baseClaim(git)
	claim.SessionRef = "ses-current"
	claimed, err := s.ClaimWorktree(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: "stale-generation-landing", Kind: "work.session_claim_landed", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "ses-current", OccurredAt: time.Unix(41, 0).UTC(), PayloadVersion: eventKindRegistry["work.session_claim_landed"].CurrentVersion,
		Payload: mustJSONValue(sessionClaimLandedPayload{WorkID: "work-w", ProjectID: "project-w", ClaimOpID: "wt-op-stale", SessionRef: "ses-current", SourceDirectories: []string{}, LandedDirectory: claimed.Entry.Path, HostPID: 7, HostPIDStart: 999})}
	before := currentEffectRows(t, s)
	err = ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "claim generation the landed path no longer carries") {
		t.Fatalf("stale-generation landing = %v", err)
	}
	if len(currentEffectRows(t, s)) != len(before) {
		t.Fatal("refused generation wrote an event")
	}
}

func TestLiveClaimLandingMissingHostPIDStillRefused(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claim := baseClaim(git)
	claim.SessionRef = "ses-current"
	claimed, err := s.ClaimWorktree(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{Kind: "work.session_claim_landed", SubjectType: SubjectWorkItem, SubjectID: "work-w", Payload: mustJSONValue(sessionClaimLandedPayload{WorkID: "work-w", ProjectID: "project-w", ClaimOpID: claimed.Entry.ClaimOpID, SessionRef: "ses-current", SourceDirectories: []string{}, LandedDirectory: claimed.Entry.Path})}
	err = foldSessionClaimLanded(context.Background(), nil, event)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "missing host_pid") {
		t.Fatalf("missing-pid fold = %v", err)
	}
	_, err = s.RecordWorktreeClaimLanding(context.Background(), WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-current", LandedDirectory: claimed.Entry.Path, Now: time.Unix(42, 0).UTC()})
	failure = nil
	if !failureAs(err, &failure) || !strings.Contains(failure.Detail, "landing requires the host process pid") {
		t.Fatalf("missing-pid live landing = %v", err)
	}
}

func TestLiveOccupancyReleaseEmitsVersionTwo(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claim := baseClaim(git)
	claim.SessionRef = "ses-claim"
	if _, err := s.ClaimWorktree(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	start, err := hostlease.ProcessStart(pid)
	if err != nil {
		t.Fatal(err)
	}
	insertWorktreeOccupantWithIdentity(t, s, "work-w", "ses-ended", pid, start-1)
	seedWorktreeLifecycle(t, s)
	if _, err := s.db.Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM worktree_occupancy WHERE session_ref='ses-claim'; DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReclaimWorktree(context.Background(), WorktreeReclaimRequest{WorkID: "work-w", ProjectID: "project-w", DefaultRef: "origin/main", PrincipalRef: "principal-1", RequestID: "req-release-version", ExpectedVersion: 4, Now: time.Unix(25, 0).UTC(), Runner: git}); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT payload_version FROM effect_receipts WHERE kind='work.worktree_occupancy_released' AND event_id=?`, occupancyReleasedEventID("work-w", "project-w", claim.OpID, "ses-ended", 0)).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("occupancy release version=%d, want 2", version)
	}
}

func TestLiveClaimLandingEmitsVersionTwo(t *testing.T) {
	t.Parallel()
	s, git, _ := worktreeFixture(t)
	claim := baseClaim(git)
	claim.SessionRef = "ses-live"
	claimed, err := s.ClaimWorktree(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordWorktreeClaimLanding(context.Background(), WorktreeClaimLandingRequest{WorkID: "work-w", SessionRef: "ses-live", LandedDirectory: claimed.Entry.Path, Now: time.Unix(30, 0).UTC(), HostPID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := s.db.QueryRow(`SELECT payload_version FROM effect_receipts WHERE kind='work.session_claim_landed' AND subject_id='work-w'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("claim landing version=%d, want 2", version)
	}
}
