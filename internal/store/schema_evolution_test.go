package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestApplyRejectsNewerPayloadBeforeMutation(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	event := workCreatedEvent("work-newer", "event-newer")
	event.PayloadVersion = workCreatedCurrentVersion() + 1
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}})
	assertFailureKind(t, err, KindUnsupportedPayloadVersion)
	assertTableCount(t, s, "effect_receipts", 0)
	assertTableCount(t, s, "work_items", 0)
}

func TestEventKindRegistryIsClosedAndComplete(t *testing.T) {
	t.Parallel()
	if err := validateEventKindRegistry(eventKindRegistry); err != nil {
		t.Fatal(err)
	}
	for kind, registration := range eventKindRegistry {
		if registration.Authority != EventAppendAuthorityGeneric && registration.Authority != EventAppendAuthorityWorkflow {
			t.Fatalf("%s registration has invalid authority %d", kind, registration.Authority)
		}
		if registration.ValidatePayload == nil || registration.Fold == nil || registration.CurrentVersion < 1 {
			t.Fatalf("%s registration is incomplete: %+v", kind, registration)
		}
		event := Event{EventID: "registry-" + kind, Kind: kind, SubjectType: SubjectWorkItem, SubjectID: "work", Actor: "actor:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: registration.CurrentVersion + 1, Payload: []byte(`{}`)}
		var failure *Failure
		if err := validateRegisteredEvent(event); !errors.As(err, &failure) || failure.Kind != KindUnsupportedPayloadVersion {
			t.Fatalf("%s newer payload version error = %v, want %s", kind, err, KindUnsupportedPayloadVersion)
		}
	}
}

func TestValidateEventKindRegistryRejectsIncompleteRegistration(t *testing.T) {
	t.Parallel()
	valid := eventKindRegistry["product.created"]
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
		{"invalid current version", "__test_invalid_version__", func() EventKindRegistration {
			registration := valid
			registration.CurrentVersion = 0
			return registration
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := make(map[string]EventKindRegistration, len(eventKindRegistry)+1)
			for kind, registration := range eventKindRegistry {
				registry[kind] = registration
			}
			registry[tc.key] = tc.registration

			if err := validateEventKindRegistry(registry); err == nil {
				t.Fatalf("validateEventKindRegistry() accepted malformed %q registration", tc.key)
			}
		})
	}
}

func TestIntentRevisionPreservesExternalRefAndNormalizesShape(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	seedSchemaEvolutionBase(t, s)
	create := Event{EventID: "intent-shape-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "intent-shape", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: CurrentEventPayloadVersion("work.created"), Payload: []byte(`{"work_kind":"task","title":"Original","value_statement":"Original statement","priority":2,"tags":["alpha"],"external_ref":"tracker:issue-42"}`)}
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
}

func TestNoPersistentPointInTimeTableOrSnapshot(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
