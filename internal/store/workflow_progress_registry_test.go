package store

import (
	"context"
	"testing"
	"time"
)

const progressCustomRef = "workflow.progress_custom"

func seedRegistryWorkPin(t *testing.T, s *Store, workID string) (DefinitionRegistry, RegisteredDefinition) {
	t.Helper()
	current, err := BuiltinWorkflowDefinitionForRef("workflow.research")
	if err != nil {
		t.Fatal(err)
	}
	definition := current.Definition
	definition.Ref = progressCustomRef
	definition.Version = 1
	registry := NewWorkflowDefinitionRegistry()
	registered, err := registry.Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	version := readWorkVersion(t, s, workID)
	selection := Operation{Events: []Event{
		workflowEventWithActor("progress-owner-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{
			"work_id": workID, "expected_version": version, "resulting_version": version + 1,
			"actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef,
			"agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent",
		}),
		workflowTypedEvent("progress-selection-"+workID, WorkflowDefinitionSelected, workID, ownerRef, time.Unix(9, 0).UTC(), version+1, map[string]any{
			"ref": registered.Definition.Ref, "version": registered.Definition.Version, "digest": registered.Digest,
			"work_kind": string(registered.Definition.WorkKind),
			"work_id":   workID, "expected_version": version, "resulting_version": version + 1,
		}),
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}}
	appendWorkflowEventsWithRegistry(t, s, registry, selection)
	return registry, registered
}

func readRegistryThreadedPin(t *testing.T, s *Store, registry DefinitionRegistry, workID string) (WorkPin, error) {
	t.Helper()
	var pin WorkPin
	err := s.Transact(context.Background(), func(tx *Transaction) error {
		var err error
		pin, _, err = ReadMutationWorkPinTxForRegistry(context.Background(), tx, registry, workID, nil)
		return err
	})
	return pin, err
}

func TestCallerRegistryWorkPinKeepsVerifiedIdentity(t *testing.T) {
	t.Parallel()
	const workID = "caller-registry-work-pin"
	s := openTemp(t)
	defer s.Close()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	registry, registered := seedRegistryWorkPin(t, s, workID)
	pin, err := readRegistryThreadedPin(t, s, registry, workID)
	if err != nil {
		t.Fatal(err)
	}
	if pin.WorkflowDefinitionDigest == nil || *pin.WorkflowDefinitionDigest != registered.Digest || pin.WorkflowDefinitionVersion == nil || *pin.WorkflowDefinitionVersion != registered.Definition.Version {
		t.Fatalf("caller registry pin lost its verified definition identity: %+v", pin)
	}
	if pin.Obligations == nil || len(pin.NextValidIntents) == 0 {
		t.Fatalf("caller registry pin lost obligations or declared intents: %+v", pin)
	}
	if _, err := ReadWorkPin(context.Background(), s, workID); err == nil {
		t.Fatal("the builtin registry must not silently resolve the caller's custom definition")
	}
}

func TestCallerRegistryWorkPinRefusesUnknownCurrentDefinition(t *testing.T) {
	t.Parallel()
	const workID = "caller-registry-unknown-definition"
	s := openTemp(t)
	defer s.Close()
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	registry, _ := seedRegistryWorkPin(t, s, workID)
	if err := s.Transact(context.Background(), func(tx *Transaction) error {
		q, err := transactionSQL(tx, "registry_pin_test")
		if err != nil {
			return err
		}
		if err := enterFold(context.Background(), q); err != nil {
			return err
		}
		if _, err := q.Exec(`UPDATE workflow_instances SET definition_ref='workflow.missing' WHERE work_id=?`, workID); err != nil {
			return err
		}
		return leaveFold(context.Background(), q)
	}); err != nil {
		t.Fatal(err)
	}
	_, err := readRegistryThreadedPin(t, s, registry, workID)
	var failure *Failure
	if !failureAs(err, &failure) || failure.Kind != KindInvariantViolation {
		t.Fatalf("unknown current definition error=%v, want the typed pin refusal", err)
	}
}
