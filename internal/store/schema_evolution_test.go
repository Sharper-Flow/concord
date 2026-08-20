package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestWorkCreatedV1UpcasterIsDeterministic(t *testing.T) {
	event := workCreatedEvent("work-v1", "event-v1")
	event.PayloadVersion = 1
	event.Payload = []byte(`{"kind":"task","title":"migrate","priority":2}`)

	first, err := upcastEvent(event)
	if err != nil {
		t.Fatalf("upcastEvent() error = %v", err)
	}
	for i := 0; i < 10; i++ {
		got, err := upcastEvent(event)
		if err != nil {
			t.Fatalf("repeat upcastEvent() error = %v", err)
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("upcast result changed on repeat:\n got %#v\nwant %#v", got, first)
		}
	}
	if first.PayloadVersion != 2 || string(first.Payload) != `{"work_kind":"task","title":"migrate","priority":2}` {
		t.Fatalf("upcast result = version %d payload %s", first.PayloadVersion, first.Payload)
	}
	if string(event.Payload) != `{"kind":"task","title":"migrate","priority":2}` || event.PayloadVersion != 1 {
		t.Fatal("upcast mutated the stored event value")
	}
}

func TestWorkCreatedV1MissingFieldIsInvalidPayload(t *testing.T) {
	for _, payload := range []string{
		`{"kind":"task","priority":2}`,
		`{"kind":"task","title":"missing priority"}`,
	} {
		event := workCreatedEvent("work-v1", "event-v1")
		event.PayloadVersion = 1
		event.Payload = []byte(payload)
		_, err := upcastEvent(event)
		assertFailureKind(t, err, KindInvalidPayload)
	}
}

func TestApplyWorkCreatedV1RetainsStoredBytesAndFoldsAsV2(t *testing.T) {
	s := openTemp(t)
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("product-v1", "product-v1"),
			projectCreatedEvent("project-v1", "project-v1"),
			operationEvent("membership-v1", "product_project.added", SubjectProduct, "product-v1", map[string]any{
				"product_id": "product-v1", "project_id": "project-v1", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-v1"): 0, VersionRef(SubjectProject, "project-v1"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	event := workCreatedEvent("work-v1", "event-v1")
	event.PayloadVersion = 1
	event.Payload = []byte(`{"kind":"task","title":"legacy","priority":2}`)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		event,
		operationEvent("work-membership-v1", "work_project.added", SubjectWorkItem, "work-v1", map[string]any{
			"work_id": "work-v1", "project_id": "project-v1", "role": "secondary", "reason": "test", "expected_version": 1, "resulting_version": 2,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-v1"): 0}}); err != nil {
		t.Fatalf("ApplyOperation() error = %v", err)
	}
	var version int
	var payload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version, payload FROM domain_events WHERE event_id = ?`, event.EventID).Scan(&version, &payload); err != nil {
		t.Fatal(err)
	}
	if version != 1 || payload != string(event.Payload) {
		t.Fatalf("stored event = version %d payload %s, want original v1", version, payload)
	}
	var kind, title string
	var priority int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT kind, title, priority FROM work_items WHERE id = ?`, event.SubjectID).Scan(&kind, &title, &priority); err != nil {
		t.Fatal(err)
	}
	if kind != "task" || title != "legacy" || priority != 2 {
		t.Fatalf("projection = %s/%s/%d, want task/legacy/2", kind, title, priority)
	}
}

func TestApplyRejectsNewerPayloadBeforeMutation(t *testing.T) {
	s := openTemp(t)
	event := workCreatedEvent("work-v3", "event-v3")
	event.PayloadVersion = 3
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
	assertFailureKind(t, err, KindUnsupportedPayloadVersion)
	assertTableCount(t, s, "domain_events", 0)
	assertTableCount(t, s, "work_items", 0)
}

func TestMixedWorkCreatedVersionsRebuildDeterministically(t *testing.T) {
	s := openTemp(t)
	seedSchemaEvolutionBase(t, s)
	legacy := workCreatedEvent("work-legacy", "event-legacy")
	legacy.PayloadVersion = 1
	legacy.Payload = []byte(`{"kind":"task","title":"legacy","priority":2}`)
	current := workCreatedEvent("work-current", "event-current")
	for _, event := range []Event{legacy, current} {
		if err := ApplyOperation(context.Background(), s, Operation{
			Events: []Event{event, operationEvent("membership-"+event.SubjectID, "work_project.added", SubjectWorkItem, event.SubjectID, map[string]any{
				"work_id": event.SubjectID, "project_id": "schema-project", "role": "secondary", "reason": "test", "expected_version": 1, "resulting_version": 2,
			})},
			ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): 0},
		}); err != nil {
			t.Fatal(err)
		}
	}
	want := fullPM4Snapshot(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := fullPM4Snapshot(t, s); got != want {
		t.Fatalf("mixed-version rebuild changed projection:\n%s\nwant\n%s", got, want)
	}
	first := fullPM4Snapshot(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := fullPM4Snapshot(t, s); got != first {
		t.Fatalf("second rebuild changed projection:\n%s\nwant\n%s", got, first)
	}
}

func TestRebuildPoisonFailureHasExactEventContextAndRollsBack(t *testing.T) {
	s := openTemp(t)
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("product-poison", "product-poison"),
			projectCreatedEvent("project-poison", "project-poison"),
			operationEvent("membership-poison", "product_project.added", SubjectProduct, "product-poison", map[string]any{
				"product_id": "product-poison", "project_id": "project-poison", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-poison"): 0, VersionRef(SubjectProject, "project-poison"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	good := workCreatedEvent("work-good", "event-good")
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		good,
		operationEvent("work-membership-poison", "work_project.added", SubjectWorkItem, "work-good", map[string]any{
			"work_id": "work-good", "project_id": "project-poison", "role": "secondary", "reason": "test", "expected_version": 1, "resulting_version": 2,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-good"): 0}}); err != nil {
		t.Fatal(err)
	}
	before := projectionSnapshot(t, s)
	beforeWorkAndRelations := fullPM4Snapshot(t, s)
	poison := workCreatedEvent("work-poison", "event-poison")
	poison.PayloadVersion = 3
	result, err := s.DatabaseForTesting().ExecContext(context.Background(), `
		INSERT INTO domain_events
			(event_id, kind, subject_type, subject_id, actor, occurred_at, payload_version, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		poison.EventID, poison.Kind, poison.SubjectType, poison.SubjectID, poison.Actor,
		poison.OccurredAt.UTC().Format(time.RFC3339Nano), poison.PayloadVersion, string(poison.Payload))
	if err != nil {
		t.Fatalf("insert synthetic poison event: %v", err)
	}
	poisonSeq, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("poison sequence: %v", err)
	}
	err = RebuildFromLog(context.Background(), s)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("RebuildFromLog() error = %v, want *Failure", err)
	}
	if failure.Kind != KindUnsupportedPayloadVersion || failure.EventID != poison.EventID || failure.EventKind != poison.Kind || failure.PayloadVersion != 3 || failure.SubjectType != poison.SubjectType || failure.SubjectID != poison.SubjectID || failure.Sequence != poisonSeq || failure.Stage != "upcast" {
		t.Fatalf("failure context = %+v", failure)
	}
	if got := projectionSnapshot(t, s); got != before {
		t.Fatalf("failed rebuild changed projections:\n%s\nwant\n%s", got, before)
	}
	if got := fullPM4Snapshot(t, s); got != beforeWorkAndRelations {
		t.Fatalf("failed rebuild changed work or relation projections:\n%s\nwant\n%s", got, beforeWorkAndRelations)
	}
	assertTableCount(t, s, "domain_events", 6)
	assertFoldGuardEmpty(t, s)
}

func TestEventKindRegistryIsClosedAndComplete(t *testing.T) {
	if err := validateEventKindRegistry(); err != nil {
		t.Fatal(err)
	}
	for kind, registration := range eventKindRegistry {
		if registration.Authority != EventAppendAuthorityGeneric && registration.Authority != EventAppendAuthorityWorkflow {
			t.Fatalf("%s registration has invalid authority %d", kind, registration.Authority)
		}
		if registration.ValidatePayload == nil || registration.Fold == nil || registration.Upcasters == nil || registration.MinSupported < 1 || registration.MinSupported > registration.CurrentVersion {
			t.Fatalf("%s registration is incomplete: %+v", kind, registration)
		}
		for version := registration.MinSupported; version < registration.CurrentVersion; version++ {
			if registration.Upcasters[version] == nil {
				t.Fatalf("%s registration lacks upcaster from version %d", kind, version)
			}
		}
		event := Event{EventID: "registry-" + kind, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: "work", Actor: "actor:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: registration.CurrentVersion + 1, Payload: []byte(`{}`)}
		var failure *Failure
		if err := validateRegisteredEvent(event); !errors.As(err, &failure) || failure.Kind != KindUnsupportedPayloadVersion {
			t.Fatalf("%s newer payload version error = %v, want %s", kind, err, KindUnsupportedPayloadVersion)
		}
	}
}

func TestValidateEventKindRegistryRejectsIncompleteRegistration(t *testing.T) {
	valid := eventKindRegistry["product.created"]
	versioned := eventKindRegistry["project.created"]
	cases := []struct {
		name         string
		key          string
		registration EventKindRegistration
	}{
		{"empty key", "", valid},
		{"invalid authority", "__test_invalid_authority__", func() EventKindRegistration {
			registration := valid
			registration.Authority = EventAppendAuthorityInvalid
			return registration
		}()},
		{"nil validator", "__test_nil_validator__", func() EventKindRegistration {
			registration := valid
			registration.ValidatePayload = nil
			return registration
		}()},
		{"nil fold", "__test_nil_fold__", func() EventKindRegistration {
			registration := valid
			registration.Fold = nil
			return registration
		}()},
		{"invalid version bounds", "__test_invalid_bounds__", func() EventKindRegistration {
			registration := valid
			registration.MinSupported = 0
			return registration
		}()},
		{"nil upcaster map", "__test_nil_upcasters__", func() EventKindRegistration {
			registration := valid
			registration.Upcasters = nil
			return registration
		}()},
		{"missing required upcaster", "__test_missing_upcaster__", func() EventKindRegistration {
			registration := versioned
			registration.Upcasters = map[int]Upcaster{}
			return registration
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previous, existed := eventKindRegistry[tc.key]
			eventKindRegistry[tc.key] = tc.registration
			defer func() {
				if existed {
					eventKindRegistry[tc.key] = previous
				} else {
					delete(eventKindRegistry, tc.key)
				}
			}()

			if err := validateEventKindRegistry(); err == nil {
				t.Fatalf("validateEventKindRegistry() accepted malformed %q registration", tc.key)
			}
		})
	}
}

func TestWorkflowActionCompletedV1UpcastsWithoutWorkerAttemptIdentity(t *testing.T) {
	event := Event{EventID: "legacy-action-completed", Kind: WorkflowActionCompleted, SubjectType: SubjectWorkItem, SubjectID: "legacy-work", Actor: "actor:legacy", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"legacy-work","expected_version":1,"resulting_version":2,"step_id":"execution","action_id":"record_proposal","attempt_epoch":1,"result_evidence_refs":[],"changed_refs":[],"actor_ref":"actor:legacy"}`)}
	upcasted, err := upcastEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if upcasted.PayloadVersion != 2 {
		t.Fatalf("upcast payload version=%d, want 2", upcasted.PayloadVersion)
	}
	var payload workflowActionCompletedPayload
	if err := json.Unmarshal(upcasted.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WorkerAttemptID != "" {
		t.Fatalf("legacy action completion acquired worker attempt identity %q", payload.WorkerAttemptID)
	}
}

func TestIntentRevisionReplaysDeterministically(t *testing.T) {
	s := openTemp(t)
	seedWork(t, s, "intent-work")
	payload := []byte(`{"title":"Revised","value_statement":"A complete replacement","kind":"task","priority":4,"tags":["durable"],"reason":"clarified","expected_version":2,"resulting_version":3}`)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{{EventID: "intent-revised", Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: "intent-work", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "intent-work"): 2}}); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := s.DatabaseForTesting().QueryRow(`SELECT intent_json FROM work_items WHERE id='intent-work'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := s.DatabaseForTesting().QueryRow(`SELECT intent_json FROM work_items WHERE id='intent-work'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("intent replay changed projection: before=%s after=%s", before, after)
	}
}

func TestIntentRevisionPreservesExternalRefAndNormalizesShape(t *testing.T) {
	s := openTemp(t)
	seedSchemaEvolutionBase(t, s)
	create := Event{EventID: "intent-shape-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "intent-shape", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: []byte(`{"work_kind":"task","title":"Original","value_statement":"Original statement","priority":2,"tags":["alpha"],"external_ref":"tracker:issue-42"}`)}
	membership := Event{EventID: "intent-shape-membership", Kind: "work_project.added", SubjectType: SubjectWorkItem, SubjectID: "intent-shape", Actor: "operator", OccurredAt: time.Unix(1, 1).UTC(), PayloadVersion: 1, Payload: []byte(`{"work_id":"intent-shape","project_id":"schema-project","role":"primary","reason":"test","expected_version":1,"resulting_version":2}`)}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{create, membership}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "intent-shape"): 0}}); err != nil {
		t.Fatal(err)
	}
	revise := Event{EventID: "intent-shape-revise", Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: "intent-shape", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: []byte(`{"title":"Revised","value_statement":"Revised statement","kind":"task","priority":3,"tags":["beta"],"reason":"sharpened","expected_version":2,"resulting_version":3}`)}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{revise}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "intent-shape"): 2}}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.DatabaseForTesting().QueryRow(`SELECT intent_json FROM work_items WHERE id='intent-shape'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var intent map[string]any
	if err := json.Unmarshal([]byte(stored), &intent); err != nil {
		t.Fatal(err)
	}
	wantKeys := []string{"title", "value_statement", "kind", "priority", "urgency", "tags", "component_id", "workflow_type_ref", "external_ref"}
	if len(intent) != len(wantKeys) {
		t.Fatalf("intent_json key set = %d keys (%v), want exactly %d", len(intent), intent, len(wantKeys))
	}
	for _, key := range wantKeys {
		if _, ok := intent[key]; !ok {
			t.Fatalf("intent_json is missing key %q: %s", key, stored)
		}
	}
	for _, forbidden := range []string{"reason", "expected_version", "resulting_version"} {
		if _, ok := intent[forbidden]; ok {
			t.Fatalf("intent_json leaked event field %q: %s", forbidden, stored)
		}
	}
	if intent["external_ref"] != "tracker:issue-42" {
		t.Fatalf("revision dropped the capture-owned external_ref: %s", stored)
	}
	if intent["title"] != "Revised" || intent["priority"] != float64(3) || intent["urgency"] != "standard" {
		t.Fatalf("revision did not replace the mutable block: %s", stored)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var rebuilt string
	if err := s.DatabaseForTesting().QueryRow(`SELECT intent_json FROM work_items WHERE id='intent-shape'`).Scan(&rebuilt); err != nil {
		t.Fatal(err)
	}
	if rebuilt != stored {
		t.Fatalf("intent replay changed shape: before=%s after=%s", stored, rebuilt)
	}
}

func TestSchemaManifestCompatibilityReportsCurrentVersion(t *testing.T) {
	s := openTemp(t)
	compatibility, err := CheckSchemaCompatibility(context.Background(), s.DatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	if compatibility.CurrentVersion != len(migrations) || compatibility.AppliedVersion != len(migrations) || !compatibility.Compatible {
		t.Fatalf("compatibility = %+v", compatibility)
	}
}

func TestReconstructSubjectAtAcceptsOnlyAuditAndDiagnosis(t *testing.T) {
	s := openTemp(t)
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("product-reconstruct", "product-reconstruct"),
			projectCreatedEvent("project-reconstruct", "project-reconstruct"),
			operationEvent("membership-reconstruct", "product_project.added", SubjectProduct, "product-reconstruct", map[string]any{
				"product_id": "product-reconstruct", "project_id": "project-reconstruct", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-reconstruct"): 0, VersionRef(SubjectProject, "project-reconstruct"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	event := workCreatedEvent("work-reconstruct", "event-reconstruct")
	event.PayloadVersion = 1
	event.Payload = []byte(`{"kind":"task","title":"legacy reconstruction","priority":2}`)
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		event,
		operationEvent("work-membership-reconstruct", "work_project.added", SubjectWorkItem, "work-reconstruct", map[string]any{
			"work_id": "work-reconstruct", "project_id": "project-reconstruct", "role": "secondary", "reason": "test", "expected_version": 1, "resulting_version": 2,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-reconstruct"): 0}}); err != nil {
		t.Fatal(err)
	}
	for _, purpose := range []ReconstructionPurpose{PurposeAudit, PurposeDiagnosis} {
		snapshot, err := ReconstructSubjectAt(context.Background(), s, VersionRef(SubjectWorkItem, event.SubjectID), 5, purpose)
		if err != nil {
			t.Fatalf("ReconstructSubjectAt(%q) error = %v", purpose, err)
		}
		if snapshot.Work == nil || snapshot.Work.ID != event.SubjectID || snapshot.Work.Kind != "task" || snapshot.Work.Title != "legacy reconstruction" || snapshot.Work.Priority != 2 || snapshot.AsOfSeq != 5 {
			t.Fatalf("snapshot = %+v", snapshot)
		}
	}
	_, err := ReconstructSubjectAt(context.Background(), s, VersionRef(SubjectWorkItem, event.SubjectID), 5, ReconstructionPurpose("other"))
	assertFailureKind(t, err, KindInvalidOperation)
}

func TestReconstructSubjectAtDoesNotMutateLiveState(t *testing.T) {
	s := openTemp(t)
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("product-reconstruct", "product-reconstruct"),
			projectCreatedEvent("project-reconstruct", "project-reconstruct"),
			operationEvent("membership-reconstruct", "product_project.added", SubjectProduct, "product-reconstruct", map[string]any{
				"product_id": "product-reconstruct", "project_id": "project-reconstruct", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-reconstruct"): 0, VersionRef(SubjectProject, "project-reconstruct"): 0},
	}); err != nil {
		t.Fatal(err)
	}
	event := workCreatedEvent("work-reconstruct", "event-reconstruct")
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{
		event,
		operationEvent("work-membership-reconstruct", "work_project.added", SubjectWorkItem, "work-reconstruct", map[string]any{
			"work_id": "work-reconstruct", "project_id": "project-reconstruct", "role": "secondary", "reason": "test", "expected_version": 1, "resulting_version": 2,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-reconstruct"): 0}}); err != nil {
		t.Fatal(err)
	}
	before := projectionSnapshot(t, s)
	var guardBefore int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&guardBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconstructSubjectAt(context.Background(), s, VersionRef(SubjectWorkItem, event.SubjectID), 5, PurposeDiagnosis); err != nil {
		t.Fatal(err)
	}
	if got := projectionSnapshot(t, s); got != before {
		t.Fatalf("live projection changed:\n%s\nwant\n%s", got, before)
	}
	var guardAfter int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM fold_guard`).Scan(&guardAfter); err != nil {
		t.Fatal(err)
	}
	if guardAfter != guardBefore {
		t.Fatalf("fold guard = %d, want %d", guardAfter, guardBefore)
	}
}

func TestNoPersistentPointInTimeTableOrSnapshot(t *testing.T) {
	s := openTemp(t)
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND (name LIKE '%snapshot%' OR name LIKE '%reconstruct%')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("persistent point-in-time tables = %d", count)
	}
}

func TestWorkCreatedV2FixturePayloadIsCanonical(t *testing.T) {
	event := workCreatedEvent("work-v2", "event-v2")
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["work_kind"]; !ok {
		t.Fatalf("fixture payload = %s, want work_kind", event.Payload)
	}
}

func seedSchemaEvolutionBase(t *testing.T, s *Store) {
	t.Helper()
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("schema-product", "schema-product"),
			projectCreatedEvent("schema-project", "schema-project"),
			operationEvent("schema-membership", "product_project.added", SubjectProduct, "schema-product", map[string]any{
				"product_id": "schema-product", "project_id": "schema-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "schema-product"): 0, VersionRef(SubjectProject, "schema-project"): 0},
	}); err != nil {
		t.Fatal(err)
	}
}
