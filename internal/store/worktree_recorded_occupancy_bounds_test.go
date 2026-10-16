package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func appendRecordedProcessLanding(t *testing.T, s *Store, id, session string) {
	t.Helper()
	raw, err := json.Marshal(sessionClaimLandedPayload{
		WorkID: "work-w", ProjectID: "project-w", SessionRef: session, ClaimOpID: "wt-op-1",
		LandedDirectory: claimPath(s), HostPID: 12345, HostPIDStart: 67890,
	})
	if err != nil {
		t.Fatal(err)
	}
	insertHistoricalEvent(t, s, Event{EventID: id, Kind: "work.session_claim_landed", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: session, OccurredAt: time.Unix(15, 0).UTC(), PayloadVersion: 2, Payload: raw})
}

// appendRecordedOccupancyReleaseTarget appends one original-v1 release event
// for an explicit worktree target identity.
func appendRecordedOccupancyReleaseTarget(t *testing.T, s *Store, id, setID, projectID, claimOpID, session string, scalar bool) Event {
	t.Helper()
	payload := map[string]any{"set_id": setID, "project_id": projectID, "claim_op_id": claimOpID}
	key := "session_ref"
	if scalar {
		key = "occupant_session_ref"
	}
	payload[key] = session
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event := Event{EventID: id, Kind: "work.worktree_occupancy_released", SubjectType: SubjectWorkItem,
		SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 1, Payload: raw}
	insertHistoricalEvent(t, s, event)
	return event
}

// A recorded row-set release names its own target row: the named row is
// absent, another session's row is a separate target that stays, and the
// retained row survives repeated replay, backup, and restore.
func TestRecordedOccupancyRowSetAbsentTargetKeepsOtherRow(t *testing.T) {
	s := recordedOccupancyFixture(t, "ses-other")
	defer s.Close()
	appendRecordedOccupancyRelease(t, s, "absent-target-release", "ses-unrecorded", false, false)
	assertRecordedOccupancyReplayConverged(t, s, []string{"ses-other"})
}

// A global v1 vacate released every row the vacating session held, including
// the stale sibling-Project row it held through the sibling claim's creation
// marker. The vacate admits against its own empty recorded source, and the
// sibling worktree's recorded row-set release then names a row the vacate
// already deleted and admits on its governing creation's identity, without
// duplicating a release event.
func TestRecordedOccupancyGlobalVacateThenSiblingRowSetRelease(t *testing.T) {
	s := recordedOccupancyFixture(t, "absent")
	defer s.Close()
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		locatorProductEvent("product-other"),
		locatorProjectEvent("project-other"),
		locatorMembershipEvent("product-other", "project-other"),
		operationEvent("other-membership", "work.memberships_replaced", SubjectWorkItem, "work-w", map[string]any{
			"memberships":      []workMembershipPayload{{ProjectID: "project-w", Role: "primary"}, {ProjectID: "project-other", Role: "secondary"}},
			"expected_version": 3, "resulting_version": 4,
		}),
	}}); err != nil {
		t.Fatal(err)
	}
	var creation map[string]any
	if err := json.Unmarshal(storedEventPayload(t, s, "recorded-occupancy-creation"), &creation); err != nil {
		t.Fatal(err)
	}
	creation["project_id"], creation["claim_op_id"], creation["path"] = "project-other", "other-claim", t.TempDir()
	creation["expected_version"], creation["resulting_version"], creation["occupant_session_ref"] = 4, 5, "ses-recorded"
	raw, err := json.Marshal(creation)
	if err != nil {
		t.Fatal(err)
	}
	insertHistoricalEvent(t, s, Event{EventID: "sibling-creation", Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(13, 0).UTC(), PayloadVersion: 1, Payload: raw})
	appendRecordedOccupancyVacate(t, s, "global-vacate", "ses-recorded")
	appendRecordedOccupancyReleaseTarget(t, s, "sibling-row-release", WorktreeSetID("work-w"), "project-other", "other-claim", "ses-recorded", false)
	assertRecordedOccupancyReplayConverged(t, s, nil)
}

func TestRecordedOccupancyReleaseBounds(t *testing.T) {
	for _, tc := range []struct {
		name, marker, second string
		scalar, repeat, pass bool
		wantSessions         []string
	}{
		{"empty-marker-scalar", "", "", true, false, true, nil},
		{"empty-marker-row-set", "", "", false, false, true, nil},
		{"recorded-marker-scalar", "ses-other", "", true, false, false, nil},
		{"recorded-marker-row-set", "ses-other", "", false, false, true, []string{"ses-other"}},
		{"scalar-repeat", "absent", "ses-unrecorded", true, true, true, nil},
		{"row-set-repeat", "absent", "ses-unrecorded", false, true, true, nil},
		{"scalar-distinct", "absent", "ses-different", true, true, true, nil},
		{"row-set-distinct", "absent", "ses-different", false, true, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := recordedOccupancyFixture(t, tc.marker)
			defer s.Close()
			event := appendRecordedOccupancyRelease(t, s, "release-first", "ses-unrecorded", tc.scalar, false)
			if tc.repeat {
				event = appendRecordedOccupancyRelease(t, s, "release-second", tc.second, tc.scalar, false)
			}
			if !tc.pass {
				assertRecordedOccupancyRefusal(t, s, event)
				return
			}
			assertRecordedOccupancyReplayConverged(t, s, tc.wantSessions)
		})
	}
}

// The admission owner derives exclusivity from the recorded event alone: a
// recognized v1 vacate and a scalar-shaped release are exclusive, a
// current-shape v1 release owns only its named row, and any other kind or
// shape combination never admits.
func TestRecordedOccupancyAdmissionOwnerDerivesExclusivityFromEvent(t *testing.T) {
	s := recordedOccupancyFixture(t, "absent")
	defer s.Close()
	ctx := workflowReplayContext(context.Background())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	event := func(kind string, shape recordedPayloadShape) Event {
		return Event{Kind: kind, SubjectType: SubjectWorkItem, SubjectID: "work-w", Seq: 99, replaySourcePayloadVersion: 1, recordedPayloadShape: shape}
	}
	for _, tc := range []struct {
		name  string
		event Event
		want  bool
	}{
		{"vacate-exclusive", event("work.session_vacated", recordedShapeCurrent), true},
		{"vacate-foreign-shape", event("work.session_vacated", recordedShapeLegacyClaimLanding), false},
		{"scalar-shape-exclusive", event("work.worktree_occupancy_released", recordedShapeScalarOccupancyRelease), true},
		{"row-set-current-shape", event("work.worktree_occupancy_released", recordedShapeCurrent), true},
		{"foreign-kind", event("work.other", recordedShapeCurrent), false},
		{"foreign-shape-marker", event("work.worktree_occupancy_released", recordedShapeLegacyClaimLanding), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admitted, err := admitRecordedOccupancyReleaseTx(ctx, tx, tc.event, WorktreeSetID("work-w"), "project-w", "wt-op-1", "ses-x")
			if err != nil {
				t.Fatal(err)
			}
			if admitted != tc.want {
				t.Fatalf("admitted=%t, want %t", admitted, tc.want)
			}
		})
	}
}

func TestRecordedOccupancyLandingAndRepeatedRowSetRelease(t *testing.T) {
	for _, second := range []string{"ses-recorded", "ses-unrecorded"} {
		t.Run(second, func(t *testing.T) {
			s := recordedOccupancyFixture(t, "absent")
			defer s.Close()
			appendRecordedProcessLanding(t, s, "recorded-landing", "ses-recorded")
			appendRecordedOccupancyRelease(t, s, "recorded-row-release", "ses-recorded", false, false)
			appendRecordedOccupancyRelease(t, s, "absent-row-release", second, false, false)
			if second == "ses-recorded" {
				// The already-released named row accepts its own repeat.
				assertRecordedOccupancyReplayConverged(t, s, nil)
				return
			}
			if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatalf("a recorded session must not consume another session's release: %v", err)
			}
		})
	}
}

func TestRecordedOccupancyScalarRefusesDifferentLandingSession(t *testing.T) {
	s := recordedOccupancyFixture(t, "absent")
	defer s.Close()
	appendRecordedProcessLanding(t, s, "recorded-other-landing", "ses-other")
	event := appendRecordedOccupancyRelease(t, s, "scalar-wrong-occupant", "ses-unrecorded", true, false)
	assertRecordedOccupancyRefusal(t, s, event)
}

// A v1 vacate and a recorded row-set release each carry their own committed
// effect: no lifetime budget spans the two kinds, and no release is a
// duplicate of another kind's release.
func TestRecordedOccupancyVacateAndReleaseCarryNoSharedLifetimeBudget(t *testing.T) {
	for _, mode := range []string{"vacate-then-row-set", "release-then-same-vacate", "release-then-different-vacate"} {
		t.Run(mode, func(t *testing.T) {
			s := recordedOccupancyFixture(t, "absent")
			defer s.Close()
			if mode == "vacate-then-row-set" {
				appendRecordedOccupancyVacate(t, s, "first-vacate", "ses-unrecorded")
				appendRecordedOccupancyRelease(t, s, "repeated-row-set", "ses-unrecorded", false, false)
			} else {
				appendRecordedOccupancyRelease(t, s, "first-release", "ses-unrecorded", false, false)
				session := "ses-unrecorded"
				if mode == "release-then-different-vacate" {
					session = "ses-different"
				}
				appendRecordedOccupancyVacate(t, s, "later-vacate", session)
			}
			assertRecordedOccupancyReplayConverged(t, s, nil)
		})
	}
}

func TestRecordedOccupancyVacateBoundsWithRowsElsewhere(t *testing.T) {
	for _, mode := range []string{"second-distinct", "different-recorded-target", "empty-marker"} {
		t.Run(mode, func(t *testing.T) {
			marker := "absent"
			if mode == "empty-marker" {
				marker = ""
			}
			s := recordedOccupancyFixture(t, marker)
			defer s.Close()
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
				locatorProductEvent("product-other"),
				locatorProjectEvent("project-other"),
				locatorMembershipEvent("product-other", "project-other"),
				operationEvent("other-membership", "work.memberships_replaced", SubjectWorkItem, "work-w", map[string]any{
					"memberships":      []workMembershipPayload{{ProjectID: "project-w", Role: "primary"}, {ProjectID: "project-other", Role: "secondary"}},
					"expected_version": 3, "resulting_version": 4,
				}),
			}}); err != nil {
				t.Fatal(err)
			}
			var creation map[string]any
			if err := json.Unmarshal(storedEventPayload(t, s, "recorded-occupancy-creation"), &creation); err != nil {
				t.Fatal(err)
			}
			creation["project_id"], creation["claim_op_id"], creation["path"] = "project-other", "other-claim", t.TempDir()
			creation["expected_version"], creation["resulting_version"], creation["occupant_session_ref"] = 4, 5, "ses-unrecorded"
			raw, err := json.Marshal(creation)
			if err != nil {
				t.Fatal(err)
			}
			insertHistoricalEvent(t, s, Event{EventID: "other-creation", Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(13, 0).UTC(), PayloadVersion: 1, Payload: raw})
			if mode == "different-recorded-target" {
				appendRecordedProcessLanding(t, s, "different-target-landing", "ses-other")
			}
			event := appendRecordedOccupancyVacate(t, s, "vacate-with-stale-row", "ses-unrecorded")
			if mode == "second-distinct" {
				event = appendRecordedOccupancyRelease(t, s, "second-distinct-release", "ses-different", false, false)
			}
			if mode == "different-recorded-target" {
				assertRecordedOccupancyRefusal(t, s, event)
				return
			}
			// The vacate releases every row the session held, including the
			// stale sibling-Project row; the admitted release deletes nothing.
			assertRecordedOccupancyReplayConverged(t, s, nil)
		})
	}
}

// The recorded release stands on its governing creation's identity: a later
// creation's marker change does not revoke it, while a release naming an
// unknown claim generation, or a governing creation whose set and Project
// identity the release never carried, refuses.
func TestRecordedOccupancyMarkerChangeDoesNotRevokeRecordedRelease(t *testing.T) {
	t.Run("marker-change-keeps-release", func(t *testing.T) {
		s := recordedOccupancyFixture(t, "absent")
		defer s.Close()
		var payload map[string]any
		if err := json.Unmarshal(storedEventPayload(t, s, "recorded-occupancy-creation"), &payload); err != nil {
			t.Fatal(err)
		}
		payload["occupant_session_ref"] = ""
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		insertHistoricalEvent(t, s, Event{EventID: "later-creation", Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(15, 0).UTC(), PayloadVersion: 1, Payload: raw})
		appendRecordedOccupancyRelease(t, s, "latest-marker-release", "ses-unrecorded", true, false)
		assertRecordedOccupancyReplayConverged(t, s, nil)
	})
	t.Run("unknown-claim-generation", func(t *testing.T) {
		s := recordedOccupancyFixture(t, "absent")
		defer s.Close()
		event := appendRecordedOccupancyReleaseTarget(t, s, "unknown-claim-generation", WorktreeSetID("work-w"), "project-w", "wt-op-2", "ses-unrecorded", true)
		assertRecordedOccupancyRefusal(t, s, event)
	})
	t.Run("wrong-source-creation-identity", func(t *testing.T) {
		s := recordedOccupancyFixture(t, "absent")
		defer s.Close()
		var creation map[string]any
		if err := json.Unmarshal(storedEventPayload(t, s, "recorded-occupancy-creation"), &creation); err != nil {
			t.Fatal(err)
		}
		creation["project_id"], creation["claim_op_id"], creation["path"] = "project-other", "wt-op-1", t.TempDir()
		creation["expected_version"], creation["resulting_version"] = 3, 4
		delete(creation, "occupant_session_ref")
		raw, err := json.Marshal(creation)
		if err != nil {
			t.Fatal(err)
		}
		insertHistoricalEvent(t, s, Event{EventID: "displaced-creation", Kind: "work.worktree_created", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(15, 0).UTC(), PayloadVersion: 1, Payload: raw})
		event := appendRecordedOccupancyRelease(t, s, "wrong-source-release", "ses-unrecorded", true, false)
		assertRecordedOccupancyRefusal(t, s, event)
	})
}

func TestRecordedOccupancyMissingCreationAndLiveReleaseRefuse(t *testing.T) {
	t.Run("missing-creation", func(t *testing.T) {
		s, _, _ := worktreeFixture(t)
		defer s.Close()
		event := appendRecordedOccupancyRelease(t, s, "missing-claim-release", "ses-unrecorded", true, false)
		assertRecordedOccupancyRefusal(t, s, event)
	})
	for _, mode := range []string{"live", "replay-v2"} {
		t.Run(mode, func(t *testing.T) {
			s := recordedOccupancyFixture(t, "absent")
			defer s.Close()
			before := histEventRows(t, s)
			event := Event{EventID: "live-absent-release", Kind: "work.worktree_occupancy_released", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 2,
				Payload: jsonRaw(`{"set_id":"` + WorktreeSetID("work-w") + `","project_id":"project-w","claim_op_id":"wt-op-1","session_ref":"ses-unrecorded"}`)}
			if mode == "replay-v2" {
				insertHistoricalEvent(t, s, event)
				assertRecordedOccupancyRefusal(t, s, event)
				return
			}
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}}); err == nil {
				t.Fatal("live release admitted an absent target")
			}
			if !reflect.DeepEqual(before, histEventRows(t, s)) {
				t.Fatal("refused live release appended an event")
			}
		})
	}
}

// Current-shape live and replay-v2 row-set releases keep their strict
// missing-row refusal while another session's row stands: the recorded-v1
// admission is never a relaxation of a current writer.
func TestRecordedOccupancyCurrentRowSetAbsentTargetWithOtherRowRefuses(t *testing.T) {
	for _, mode := range []string{"live", "replay-v2"} {
		t.Run(mode, func(t *testing.T) {
			s := recordedOccupancyFixture(t, "ses-other")
			defer s.Close()
			event := Event{EventID: "current-absent-release", Kind: "work.worktree_occupancy_released", SubjectType: SubjectWorkItem, SubjectID: "work-w", Actor: "operator", OccurredAt: time.Unix(20, 0).UTC(), PayloadVersion: 2,
				Payload: jsonRaw(`{"set_id":"` + WorktreeSetID("work-w") + `","project_id":"project-w","claim_op_id":"wt-op-1","session_ref":"ses-unrecorded"}`)}
			if mode == "replay-v2" {
				insertHistoricalEvent(t, s, event)
				assertRecordedOccupancyRefusal(t, s, event)
				return
			}
			before := histEventRows(t, s)
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}}); err == nil {
				t.Fatal("current live release admitted an absent named target")
			}
			if !reflect.DeepEqual(before, histEventRows(t, s)) {
				t.Fatal("refused live release appended an event")
			}
			if got := recordedOccupancySessionRefs(t, s.db); !reflect.DeepEqual(got, []string{"ses-other"}) {
				t.Fatalf("other session's row must survive a refused current release: %v", got)
			}
		})
	}
}

func TestRecordedOccupancyRefusesProcessIdentityDowngrade(t *testing.T) {
	s := recordedOccupancyFixture(t, "ses-recorded")
	defer s.Close()
	if _, err := s.db.Exec(`INSERT INTO fold_guard(active) VALUES(1); UPDATE worktree_occupancy SET host_pid=12345,host_pid_start=67890,has_process_identity=1 WHERE session_ref='ses-recorded'; DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	before, events := histProjectionRows(t, s.db), histEventRows(t, s)
	err := RebuildFromLog(context.Background(), s)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindProjectionConflict || !strings.Contains(failure.Detail, "ses-recorded") {
		t.Fatalf("rebuild must refuse an unrecorded process identity: %v", err)
	}
	if !reflect.DeepEqual(before, histProjectionRows(t, s.db)) || !reflect.DeepEqual(events, histEventRows(t, s)) {
		t.Fatal("refused rebuild downgraded process identity or changed recorded bytes")
	}
}

func TestRecordedOccupancyRebuildKeepsProcessIdentityAndCreationMarker(t *testing.T) {
	for _, session := range []string{"", "ses-recorded"} {
		t.Run(fmt.Sprintf("session-%s", session), func(t *testing.T) {
			s, git, _ := worktreeFixture(t)
			defer s.Close()
			req := baseClaim(git)
			req.SessionRef, req.HostPID = session, os.Getpid()
			if _, err := s.ClaimWorktree(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			var raw []byte
			if err := s.db.QueryRow(`SELECT payload FROM domain_events WHERE kind='work.worktree_created' ORDER BY seq DESC LIMIT 1`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var members map[string]json.RawMessage
			if err := json.Unmarshal(raw, &members); err != nil {
				t.Fatal(err)
			}
			if _, present := members["occupant_session_ref"]; !present {
				t.Fatal("live creation omitted the historical occupancy marker")
			}
			before, events := histProjectionRows(t, s.db), histEventRows(t, s)
			for range 2 {
				if err := RebuildFromLog(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, histProjectionRows(t, s.db)) || !reflect.DeepEqual(events, histEventRows(t, s)) {
					t.Fatal("rebuild changed process identity, occupancy, or recorded bytes")
				}
			}
		})
	}
}
