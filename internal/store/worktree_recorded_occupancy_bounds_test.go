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

func TestRecordedOccupancyReleaseBounds(t *testing.T) {
	for _, tc := range []struct {
		name, marker, second string
		scalar, repeat, pass bool
	}{
		{"empty-marker-scalar", "", "", true, false, false},
		{"empty-marker-row-set", "", "", false, false, false},
		{"recorded-marker-scalar", "ses-other", "", true, false, false},
		{"recorded-marker-row-set", "ses-other", "", false, false, false},
		{"scalar-repeat", "absent", "ses-unrecorded", true, true, true},
		{"row-set-repeat", "absent", "ses-unrecorded", false, true, false},
		{"scalar-distinct", "absent", "ses-different", true, true, false},
		{"row-set-distinct", "absent", "ses-different", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := recordedOccupancyFixture(t, tc.marker)
			defer s.Close()
			event := appendRecordedOccupancyRelease(t, s, "release-first", "ses-unrecorded", tc.scalar, false)
			if tc.repeat {
				event = appendRecordedOccupancyRelease(t, s, "release-second", tc.second, tc.scalar, false)
			}
			if tc.pass {
				for range 2 {
					if err := RebuildFromLog(context.Background(), s); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				assertRecordedOccupancyRefusal(t, s, event)
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
			event := appendRecordedOccupancyRelease(t, s, "absent-row-release", second, false, false)
			if second == "ses-recorded" {
				assertRecordedOccupancyRefusal(t, s, event)
			} else if err := RebuildFromLog(context.Background(), s); err != nil {
				t.Fatalf("a recorded session consumed the unrecorded-session bound: %v", err)
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

func TestRecordedOccupancyVacateSharesReleaseBound(t *testing.T) {
	for _, mode := range []string{"vacate-then-row-set", "release-then-same-vacate", "release-then-different-vacate"} {
		t.Run(mode, func(t *testing.T) {
			s := recordedOccupancyFixture(t, "absent")
			defer s.Close()
			var event Event
			if mode == "vacate-then-row-set" {
				appendRecordedOccupancyVacate(t, s, "first-vacate", "ses-unrecorded")
				event = appendRecordedOccupancyRelease(t, s, "repeated-row-set", "ses-unrecorded", false, false)
			} else {
				appendRecordedOccupancyRelease(t, s, "first-release", "ses-unrecorded", false, false)
				session := "ses-unrecorded"
				if mode == "release-then-different-vacate" {
					session = "ses-different"
				}
				event = appendRecordedOccupancyVacate(t, s, "later-vacate", session)
			}
			if mode == "release-then-same-vacate" {
				if err := RebuildFromLog(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			} else {
				assertRecordedOccupancyRefusal(t, s, event)
			}
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
			assertRecordedOccupancyRefusal(t, s, event)
		})
	}
}

func TestRecordedOccupancyLatestCreationMarkerGoverns(t *testing.T) {
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
	event := appendRecordedOccupancyRelease(t, s, "latest-marker-release", "ses-unrecorded", true, false)
	assertRecordedOccupancyRefusal(t, s, event)
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
