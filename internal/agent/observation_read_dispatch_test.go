package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

func TestWorkTraceObservationsReadsAllRowsForRequestedWork(t *testing.T) {
	ctx := context.Background()
	s, service, grant := claimsFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	events := make([]store.Event, 0, 12)
	for i := 0; i < 12; i++ {
		id := "obs:" + string([]byte{'0' + byte(i/10), '0' + byte(i%10)}) + "00000000000000"
		if len(id) != 20 {
			t.Fatalf("observation id=%q has length %d", id, len(id))
		}
		payload, _ := json.Marshal(map[string]any{
			"observation_id": id,
			"statement":      "Observation " + string(rune('a'+i)),
			"refs":           []string{},
			"tags":           []string{},
		})
		events = append(events, store.Event{EventID: "observation-read-" + string(rune('a'+i)), Kind: "work.observation_recorded", SubjectType: store.SubjectWorkItem, SubjectID: "work-contender", Actor: "operator", OccurredAt: fixedTime().Add(time.Duration(i) * time.Second), PayloadVersion: 1, Payload: payload})
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events}); err != nil {
		t.Fatal(err)
	}

	response, err := Dispatch(ctx, s, service, InvokeRequest{
		Tool:      "concord_work_trace",
		Operation: "observations",
		Input:     json.RawMessage(`{"work_id":"work-contender","page":{"cursor":null,"limit":64}}`),
	}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("observation read failed: %+v", response.Error)
	}
	var page struct {
		Observations []store.WorkObservation `json:"observations"`
	}
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Observations) != 12 {
		t.Fatalf("observation count=%d, want 12", len(page.Observations))
	}
	for index, observation := range page.Observations {
		if observation.WorkID != "work-contender" {
			t.Fatalf("observation %d work_id=%q, want work-contender", index, observation.WorkID)
		}
		if index > 0 {
			previous := page.Observations[index-1]
			if previous.RecordedAt < observation.RecordedAt || (previous.RecordedAt == observation.RecordedAt && previous.ObservationID < observation.ObservationID) {
				t.Fatalf("observations are not newest-first: previous=%+v current=%+v", previous, observation)
			}
		}
	}
}

func TestWorkTraceExternalObservationsReadPreservesD9StateWithoutEvents(t *testing.T) {
	ctx := context.Background()
	s, service, grant := claimsFixture(t)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	captured := dispatchObservation(t, s, service, grant, scopeVersion, externalCaptureInput("external-read-capture", "xobs:0123456789abcdef"))
	if captured.Outcome != OutcomeOK {
		t.Fatalf("external capture failed: %+v", captured.Error)
	}
	if len(*captured.NextValidIntents) != 1 || (*captured.NextValidIntents)[0].Operation != "external_observations" || (*captured.NextValidIntents)[0].QueryID != "CD-0040.R1" {
		t.Fatalf("external capture next intent=%+v, want external_observations/CD-0040.R1", captured.NextValidIntents)
	}
	verified := dispatchObservation(t, s, service, grant, scopeVersion, externalVerificationInput("external-read-verification", "xobs:0123456789abcdef"))
	if verified.Outcome != OutcomeOK {
		t.Fatalf("external verification failed: %+v", verified.Error)
	}

	eventsBefore := scalarRow(t, s, `SELECT count(*) FROM domain_events`)
	response, err := Dispatch(ctx, s, service, InvokeRequest{
		Tool:      "concord_work_trace",
		Operation: "external_observations",
		Input:     json.RawMessage(`{"work_id":"work-holder","limit":64}`),
	}, mutationEnvelope(grant, scopeVersion))
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("external observation read failed: %+v", response.Error)
	}
	var page struct {
		Observations []store.ExternalObservationRow `json:"external_observations"`
	}
	if err := json.Unmarshal(response.Result, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Observations) != 1 {
		t.Fatalf("external observation count=%d, want 1", len(page.Observations))
	}
	observation := page.Observations[0]
	if observation.VerificationState != store.VerificationVerified {
		t.Fatalf("verification_state=%q, want verified", observation.VerificationState)
	}
	if observation.FreshnessState != string(store.VerificationVerified) {
		t.Fatalf("freshness_state=%q, want verified", observation.FreshnessState)
	}
	if eventsAfter := scalarRow(t, s, `SELECT count(*) FROM domain_events`); eventsAfter != eventsBefore {
		t.Fatalf("external observation read changed domain_events from %d to %d", eventsBefore, eventsAfter)
	}
}
