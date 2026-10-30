package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestGoverningOverlapCoreRequiresPreservedScope(t *testing.T) {
	for _, kind := range []string{ResolutionMergedInto, ResolutionSupersedes} {
		for _, path := range []string{"entry", "fold", "legacy_from", "legacy_to"} {
			t.Run(kind+"/"+path, func(t *testing.T) {
				ctx := context.Background()
				s, actor := seedOverlapProjection(t, "governed-left", "governed-right", false)
				terminated := "governed-left"
				if kind == ResolutionSupersedes {
					terminated = "governed-right"
				}
				overlapFixtureExec(t, s, `UPDATE work_items SET intent_json=json_set(intent_json,'$.governing_requirements',json('["audit_required"]')) WHERE id=?`, terminated)
				want := KindUnauthorized
				if path == "legacy_from" || path == "legacy_to" {
					unknown := "governed-left"
					if path == "legacy_to" {
						unknown = "governed-right"
					}
					overlapFixtureExec(t, s, `UPDATE work_items SET intent_json=json_remove(intent_json,'$.governing_requirements') WHERE id=?`, unknown)
					want = KindMissingEvidence
				}
				fromVersion, toVersion := readWorkVersion(t, s, "governed-left"), readWorkVersion(t, s, "governed-right")
				var before int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&before); err != nil {
					t.Fatal(err)
				}
				var err error
				if path == "fold" {
					payload := map[string]any{"work_id": "governed-left", "expected_version": fromVersion, "resulting_version": fromVersion + 1, "to_work_id": "governed-right", "to_expected_version": toVersion, "to_resulting_version": toVersion + 1, "from_contract_version": 1, "to_contract_version": 1, "resolution_kind": kind, "reason": "settle overlap"}
					event := operationEvent("governed-fold", WorkflowOverlapResolved, SubjectWorkItem, "governed-left", payload)
					err = applyWorkflowTestOperation(ctx, s, Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "governed-left"): fromVersion, VersionRef(SubjectWorkItem, "governed-right"): toVersion}})
				} else {
					err = s.Transact(ctx, func(tx *Transaction) error {
						_, err := ResolveWorkflowDomainOverlapTx(ctx, tx, WorkflowDomainOverlapResolutionRequest{EventID: "governed-entry", FromWorkID: "governed-left", ToWorkID: "governed-right", FromExpectedVersion: fromVersion, ToExpectedVersion: toVersion, FromContractVersion: 1, ToContractVersion: 1, ResolutionKind: kind, Reason: "settle overlap", Actor: actor})
						return err
					})
				}
				assertFailureKind(t, err, want)
				var after, resolutions int
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM effect_receipts`).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_overlap_resolutions`).Scan(&resolutions); err != nil {
					t.Fatal(err)
				}
				if before != after || resolutions != 0 || readWorkVersion(t, s, "governed-left") != fromVersion || readWorkVersion(t, s, "governed-right") != toVersion {
					t.Fatal("scope refusal did not roll back")
				}
				assertFoldGuardEmpty(t, s)
			})
		}
	}
}

func TestGoverningCaptureFactsSurviveRevisionAndLegacyStaysUnknown(t *testing.T) {
	for _, declared := range [][]string{nil, {}, {"audit_required"}} {
		raw, _ := json.Marshal(declared)
		t.Run(string(raw), func(t *testing.T) {
			s := openTemp(t)
			seedGovernedProject(t, s, "prod", "proj")
			created := workCreatedEvent("governed-capture", "governed-create")
			var payload map[string]any
			if err := json.Unmarshal(created.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if declared != nil {
				payload["governing_requirements"] = declared
			}
			created.Payload, _ = json.Marshal(payload)
			membership := operationEvent("governed-member", "work.memberships_replaced", SubjectWorkItem, "governed-capture", map[string]any{"memberships": []map[string]string{{"project_id": "proj", "role": "primary"}}, "expected_version": 1, "resulting_version": 2})
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{created, membership}, ExpectedVersions: workVersion("governed-capture", 0)}); err != nil {
				t.Fatal(err)
			}
			revised := operationEvent("governed-revise", "work.intent_revised", SubjectWorkItem, "governed-capture", map[string]any{"title": "Revised", "value_statement": "preserve captured scope", "kind": "task", "priority": 0, "reason": "clarify intent", "expected_version": 2, "resulting_version": 3})
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{revised}, ExpectedVersions: workVersion("governed-capture", 2)}); err != nil {
				t.Fatal(err)
			}
			var intent string
			if err := s.DatabaseForTesting().QueryRow(`SELECT intent_json FROM work_items WHERE id='governed-capture'`).Scan(&intent); err != nil {
				t.Fatal(err)
			}
			var projected workIntentProjection
			if err := json.Unmarshal([]byte(intent), &projected); err != nil {
				t.Fatal(err)
			}
			if declared == nil {
				if projected.GoverningRequirements != nil {
					t.Fatal("invented legacy requirement facts")
				}
			} else if projected.GoverningRequirements == nil || !NewGoverningRequirementScope(nil, declared).Equal(NewGoverningRequirementScope(nil, *projected.GoverningRequirements)) {
				t.Fatalf("lost genuine declaration: %s", intent)
			}
		})
	}
}

func TestGoverningCaptureRevalidatesProjectRequirements(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	seedGovernedProject(t, s, "prod", "proj")
	planned := NewGoverningRequirementScope(nil, nil)
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{declareAuditRequirementEvent("late-requirement")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "proj"): 1}}); err != nil {
		t.Fatal(err)
	}
	err := s.Transact(ctx, func(tx *Transaction) error {
		return ValidateCaptureGoverningRequirementsTx(ctx, tx, []string{"proj"}, planned)
	})
	assertFailureKind(t, err, KindVersionConflict)
}
