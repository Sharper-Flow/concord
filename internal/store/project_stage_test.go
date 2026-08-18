package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestProjectCreatedV1UpcastsToInheritedStageWithoutReinterpretation(t *testing.T) {
	s := openTemp(t)
	event := projectCreatedEvent("project-stage-v1", "project-stage-v1")
	upcasted, err := upcastEvent(event)
	if err != nil {
		t.Fatal(err)
	}
	if upcasted.PayloadVersion != 2 {
		t.Fatalf("upcast version=%d, want 2", upcasted.PayloadVersion)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(upcasted.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["stage_maturity_override"]) != "null" || string(fields["stage_audience_commitment_override"]) != "null" {
		t.Fatalf("v1 defaults=%s", upcasted.Payload)
	}
	if err := ApplyOperation(context.Background(), s, Operation{
		Events: []Event{
			productCreatedEvent("project-stage-product", "project-stage-product-created"),
			event,
			operationEvent("project-stage-membership", "product_project.added", SubjectProduct, "project-stage-product", map[string]any{
				"product_id": "project-stage-product", "project_id": event.SubjectID, "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
			}),
		},
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "project-stage-product"): 0, VersionRef(SubjectProject, event.SubjectID): 0},
	}); err != nil {
		t.Fatal(err)
	}
	var maturity, audience string
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(stage_maturity_override,''),COALESCE(stage_audience_commitment_override,'') FROM projects WHERE id=?`, event.SubjectID).Scan(&maturity, &audience); err != nil {
		t.Fatal(err)
	}
	if maturity != "" || audience != "" {
		t.Fatalf("v1 Project stage=%q/%q, want inherited null pair", maturity, audience)
	}

	invalid := event
	invalid.EventID = "project-stage-v1-invalid"
	invalid.Payload = []byte(`{"display_name":"Project","stage_maturity_override":"alpha"}`)
	if _, err := upcastEvent(invalid); err == nil {
		t.Fatal("v1 payload with a partial stage override was silently reinterpreted")
	}
}

func TestProjectStageChangeEventValidatesReplaysAndClearsInheritance(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		productCreatedEvent("stage-product", "stage-product-created"),
		projectCreatedEvent("stage-project", "stage-project-created"),
		operationEvent("stage-membership", "product_project.added", SubjectProduct, "stage-product", map[string]any{
			"product_id": "stage-product", "project_id": "stage-project", "role": "primary", "reason": "test", "expected_version": 1, "resulting_version": 2,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "stage-product"): 0, VersionRef(SubjectProject, "stage-project"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{projectStageChangedEvent("stage-project", "stage-set", "beta", "public")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "stage-project"): 1}}); err != nil {
		t.Fatal(err)
	}
	assertProjectStage(t, s, "stage-project", "beta", "public", 2)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	assertProjectStage(t, s, "stage-project", "beta", "public", 2)

	if _, err := s.ChangeProjectStage(ctx, ProjectStageChange{ProjectID: "stage-project", ExpectedVersion: 2}); err != nil {
		t.Fatal(err)
	}
	assertProjectStage(t, s, "stage-project", "", "", 3)
	if err := RebuildFromLog(ctx, s); err != nil {
		t.Fatal(err)
	}
	assertProjectStage(t, s, "stage-project", "", "", 3)

	beforeEvents := countProjectStageEvents(t, s)
	if _, err := s.ChangeProjectStage(ctx, ProjectStageChange{ProjectID: "stage-project", ExpectedVersion: 3, StageMaturityOverride: "alpha"}); err == nil {
		t.Fatal("partial operator stage pair was accepted")
	}
	if got := countProjectStageEvents(t, s); got != beforeEvents {
		t.Fatalf("invalid stage write appended event: before=%d after=%d", beforeEvents, got)
	}
	if _, err := s.DatabaseForTesting().Exec(`UPDATE projects SET stage_maturity_override='alpha' WHERE id='stage-project'`); err == nil {
		t.Fatal("direct Project stage mirror write bypassed fold guard")
	}
}

func TestProjectStageChangedRejectsPartialPairDuringReplay(t *testing.T) {
	s := openTemp(t)
	seedSchemaEvolutionBase(t, s)
	invalid := operationEvent("partial-stage", "project.stage_changed", SubjectProject, "schema-project", map[string]any{
		"stage_maturity_override": "alpha",
	})
	err := ApplyOperation(context.Background(), s, Operation{Events: []Event{invalid}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, "schema-project"): 1}})
	assertFailureKind(t, err, KindInvalidPayload)
	if got := countProjectStageEvents(t, s); got != 0 {
		t.Fatalf("partial stage event persisted: %d", got)
	}
}

func TestProjectCreationWritesStageOnlyThroughVersionedEvent(t *testing.T) {
	s := openTemp(t)
	result, err := s.CreateProductWithProject(context.Background(), ProductCreation{
		ProductID: "operator-stage-product", DisplayName: "Operator Product", StageMaturity: "prototype", StageAudienceCommitment: "operator_only",
		ProjectID: "operator-stage-project", ProjectDisplayName: "Operator Project", ProjectStageMaturityOverride: "beta", ProjectStageAudienceCommitmentOverride: "public", Role: "primary",
	})
	if err != nil || len(result.EventIDs) != 3 {
		t.Fatalf("create result=%#v err=%v", result, err)
	}
	assertProjectStage(t, s, "operator-stage-project", "beta", "public", 1)
	var payloadVersion int
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version FROM domain_events WHERE kind='project.created' AND subject_id=?`, "operator-stage-project").Scan(&payloadVersion); err != nil {
		t.Fatal(err)
	}
	if payloadVersion != 2 {
		t.Fatalf("project.created payload version=%d, want v2", payloadVersion)
	}
}

func assertProjectStage(t *testing.T, s *Store, id, wantMaturity, wantAudience string, wantVersion int64) {
	t.Helper()
	var maturity, audience string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT COALESCE(stage_maturity_override,''),COALESCE(stage_audience_commitment_override,''),version FROM projects WHERE id=?`, id).Scan(&maturity, &audience, &version); err != nil {
		t.Fatal(err)
	}
	if maturity != wantMaturity || audience != wantAudience || version != wantVersion {
		t.Fatalf("Project stage=%q/%q version=%d, want %q/%q version=%d", maturity, audience, version, wantMaturity, wantAudience, wantVersion)
	}
}

func countProjectStageEvents(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE kind='project.stage_changed'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
