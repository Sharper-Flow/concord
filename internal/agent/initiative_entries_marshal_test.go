package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// Issue #884: the initiative entries read succeeds at Dispatch but its
// Envelope fails to marshal — the root oneOf rejects the envelope. Every
// read on this surface must survive the wire marshal its callers run.
func TestInitiativeEntriesEnvelopeMarshals(t *testing.T) {
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []Capability{"product_read", "work_initiative"})
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	create := InvokeRequest{Tool: "concord_work_initiative", Operation: "create", Input: json.RawMessage(`{"title":"Initiative","value_statement":"Coordinate work","project_ids":["project-1"],"idempotency_key":"initiative-marshal"}`)}
	created, err := Dispatch(ctx, s, service, create, env)
	if err != nil || created.Outcome != OutcomeOK {
		t.Fatalf("create response=%+v err=%v", created, err)
	}
	initiativeID := (*created.ChangedRefs)[0].ID
	add := InvokeRequest{Tool: "concord_work_initiative", Operation: "add_entry", Input: json.RawMessage(`{"initiative_work_id":"` + initiativeID + `","child_work_id":"work-1","expected_version":2,"position":0,"idempotency_key":"initiative-marshal-add"}`)}
	if _, err := Dispatch(ctx, s, service, add, env); err != nil {
		t.Fatal(err)
	}

	read, err := Dispatch(ctx, s, service, InvokeRequest{Tool: "concord_work_initiative", Operation: "entries", Input: json.RawMessage(`{"initiative_work_id":"` + initiativeID + `"}`)}, env)
	if err != nil || read.Outcome != OutcomeOK {
		t.Fatalf("entries response=%+v err=%v", read, err)
	}
	if _, err := json.Marshal(read); err != nil {
		t.Fatalf("entries envelope must marshal on the wire: %v", err)
	}
}
