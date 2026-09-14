package store

import (
	"context"
	"errors"
	"testing"
)

func externalRefWorkCreatedEvent(eventID, workID, externalRef, raisedFrom string) Event {
	event := operationEvent(eventID, "work.created", SubjectWorkItem, workID, map[string]any{
		"work_kind": "task", "title": workID, "priority": 1,
		"external_ref": externalRef, "raised_from_work_id": raisedFrom,
	})
	event.PayloadVersion = 2
	return event
}

func externalRefMembershipEvent(eventID, workID string) Event {
	return operationEvent(eventID, "work_project.added", SubjectWorkItem, workID, map[string]any{
		"work_id": workID, "project_id": "project", "role": "secondary", "reason": "test",
		"expected_version": 1, "resulting_version": 2,
	})
}

func TestWorkCreatedExternalRefCollisionCarriesTypedOwner(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "external-setup")
	first := externalRefWorkCreatedEvent("external-first", "work-first", "tracker:42", "")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{first, externalRefMembershipEvent("external-first-membership", "work-first")}, ExpectedVersions: workVersion("work-first", 0)}); err != nil {
		t.Fatal(err)
	}

	second := externalRefWorkCreatedEvent("external-second", "work-second", "tracker:42", "")
	err := ApplyOperation(ctx, s, Operation{Events: []Event{second}, ExpectedVersions: workVersion("work-second", 0)})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict {
		t.Fatalf("collision error = %v, want %s", err, KindProjectionConflict)
	}
	if failure.ExternalRefConflict == nil || failure.ExternalRefConflict.ExistingWorkID != "work-first" || failure.ExternalRefConflict.ExternalRef != "tracker:42" {
		t.Fatalf("collision payload = %+v", failure.ExternalRefConflict)
	}
	var count int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM work_items WHERE id='work-second'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("refused collision created %d work rows", count)
	}
}

func TestWorkCreatedExternalRefAcknowledgementRecordsRaisedFrom(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "raised-setup")
	first := externalRefWorkCreatedEvent("raised-first", "work-first", "tracker:43", "")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{first, externalRefMembershipEvent("raised-first-membership", "work-first")}, ExpectedVersions: workVersion("work-first", 0)}); err != nil {
		t.Fatal(err)
	}
	second := externalRefWorkCreatedEvent("raised-second", "work-second", "tracker:43", "work-first")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{second, externalRefMembershipEvent("raised-second-membership", "work-second")}, ExpectedVersions: workVersion("work-second", 0)}); err != nil {
		t.Fatal(err)
	}
	var from, to, kind string
	if err := s.DatabaseForTesting().QueryRow(`SELECT work_id_from,work_id_to,kind FROM relations WHERE work_id_from='work-second'`).Scan(&from, &to, &kind); err != nil {
		t.Fatal(err)
	}
	if from != "work-second" || to != "work-first" || kind != "raised_from" {
		t.Fatalf("raised_from relation = %s -> %s (%s)", from, to, kind)
	}
	var asOf int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT max(seq) FROM domain_events`).Scan(&asOf); err != nil {
		t.Fatal(err)
	}
	reconstructed, err := ReconstructSubjectAt(ctx, s, VersionRef(SubjectWorkItem, "work-second"), asOf, PurposeAudit)
	if err != nil {
		t.Fatalf("reconstruct acknowledged work: %v", err)
	}
	if len(reconstructed.Relations) != 1 || reconstructed.Relations[0].Target != "work-first" || reconstructed.Relations[0].Kind != "raised_from" {
		t.Fatalf("reconstructed relations = %+v", reconstructed.Relations)
	}
}

func TestWorkCreatedExternalRefRejectsWrongAcknowledgement(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "wrong-setup")
	first := externalRefWorkCreatedEvent("wrong-first", "work-first", "tracker:45", "")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{first, externalRefMembershipEvent("wrong-first-membership", "work-first")}, ExpectedVersions: workVersion("work-first", 0)}); err != nil {
		t.Fatal(err)
	}
	second := externalRefWorkCreatedEvent("wrong-second", "work-second", "tracker:45", "wrong-setup")
	err := ApplyOperation(ctx, s, Operation{Events: []Event{second}, ExpectedVersions: workVersion("work-second", 0)})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != KindProjectionConflict {
		t.Fatalf("wrong acknowledgement error = %v, want %s", err, KindProjectionConflict)
	}
	if failure.ExternalRefConflict == nil || failure.ExternalRefConflict.ExistingWorkID != "work-first" {
		t.Fatalf("wrong acknowledgement payload = %+v", failure.ExternalRefConflict)
	}
}

func TestWorkCreatedExternalRefCollisionIgnoresTerminalOwner(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	seedWork(t, s, "terminal-setup")
	first := externalRefWorkCreatedEvent("terminal-first", "work-first", "tracker:44", "")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{first, externalRefMembershipEvent("terminal-first-membership", "work-first")}, ExpectedVersions: workVersion("work-first", 0)}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{workTransitionEvent("terminalize-first", "work-first", "needed", "completed", 2, 3)}, ExpectedVersions: workVersion("work-first", 2)}); err != nil {
		t.Fatal(err)
	}
	second := externalRefWorkCreatedEvent("terminal-second", "work-second", "tracker:44", "")
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{second, externalRefMembershipEvent("terminal-second-membership", "work-second")}, ExpectedVersions: workVersion("work-second", 0)}); err != nil {
		t.Fatal(err)
	}
}
