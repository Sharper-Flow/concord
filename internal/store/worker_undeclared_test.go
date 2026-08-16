package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Issue #106: the two model-identity outcomes that previously had no
// representable form — an undeclared executing model, and an exhausted
// resolution chain — now land as terminal-at-birth evidence rows.

func undeclaredModelFixture(t *testing.T) (*Store, LaneDefinition, RoutingPolicyDefinition) {
	t.Helper()
	s := openTemp(t)
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("p106"), locatorProjectEvent("pr106"), locatorMembershipEvent("p106", "pr106")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "p106"): 0, VersionRef(SubjectProject, "pr106"): 0}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: "w106-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: "work-106", Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: json.RawMessage(`{"work_kind":"task","title":"106","priority":1}`)},
		{EventID: "w106-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: "work-106", Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"memberships":[{"project_id":"pr106","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, "work-106"): 0}}); err != nil {
		t.Fatal(err)
	}
	lanes := BuiltinLaneDefinitions()
	var lane LaneDefinition
	for _, candidate := range lanes {
		if candidate.ID == "research" {
			lane = candidate
			break
		}
	}
	if lane.ID == "" {
		t.Fatal("research lane missing")
	}
	policy, err := LookupRoutingPolicy(lane.CapabilityClass, "routing-v1", RoutingPolicyManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	return s, lane, policy
}

func appendWorkerEvent(t *testing.T, s *Store, event Event) {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{event}}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestUndeclaredExecutingModelLandsAsTerminalEvidence(t *testing.T) {
	s, lane, _ := undeclaredModelFixture(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	undeclared := "anthropic/claude-unexpected"
	payload, _ := json.Marshal(WorkerDispatchedPayload{
		AttemptID: "attempt:106-undeclared", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: "routing-v1", RoutingPolicyDigest: RoutingPolicyManifestDigest,
		ResolvedModel: undeclared, ResolutionRole: WorkerResolutionUndeclared, PacketSchemaVersion: "1.0", ReportSchemaVersion: "1.0",
		Terminal: "failed", TerminalFailureKind: string(KindModelIdentityMismatch), TerminalDetail: "host resolved a model outside the declared resolution set",
	})
	appendWorkerEvent(t, s, Event{EventID: "w106-undeclared-dispatch", Kind: "worker.dispatched", SubjectType: SubjectWorkItem, SubjectID: "work-106", Actor: "actor:dispatch", OccurredAt: now, PayloadVersion: 2, Payload: payload})

	var lifecycle, failureKind, resolved string
	if err := s.db.QueryRow(`SELECT lifecycle_state, failure_kind, resolved_model FROM worker_attempts WHERE attempt_id='attempt:106-undeclared'`).Scan(&lifecycle, &failureKind, &resolved); err != nil {
		t.Fatalf("evidence row missing: %v", err)
	}
	if lifecycle != "failed" || failureKind != string(KindModelIdentityMismatch) || resolved != undeclared {
		t.Fatalf("row lifecycle=%s kind=%s resolved=%s", lifecycle, failureKind, resolved)
	}

	// The evidence row is never usable: a completion cannot bind.
	completePayload, _ := json.Marshal(WorkerCompletedPayload{AttemptID: "attempt:106-undeclared", ReadbackModel: undeclared, ReportSchemaVersion: "1.0"})
	err := appendWorkerEventErr(t, s, Event{EventID: "w106-undeclared-complete", Kind: "worker.completed", SubjectType: SubjectWorkItem, SubjectID: "work-106", Actor: "actor:dispatch", OccurredAt: now, PayloadVersion: 1, Payload: completePayload})
	if err == nil || !strings.Contains(err.Error(), "already terminal") {
		t.Fatalf("terminal evidence must not bind a completion: %v", err)
	}
}

func TestExhaustedResolutionChainLeavesDurableTrace(t *testing.T) {
	s, lane, _ := undeclaredModelFixture(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	payload, _ := json.Marshal(WorkerDispatchedPayload{
		AttemptID: "attempt:106-exhausted", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: "routing-v1", RoutingPolicyDigest: RoutingPolicyManifestDigest,
		ResolutionRole: WorkerResolutionUndeclared, PacketSchemaVersion: "1.0", ReportSchemaVersion: "1.0",
		Terminal: "failed", TerminalFailureKind: "routing_policy_exhausted", TerminalDetail: "declared resolution set exhausted; no model ran",
	})
	appendWorkerEvent(t, s, Event{EventID: "w106-exhausted-dispatch", Kind: "worker.dispatched", SubjectType: SubjectWorkItem, SubjectID: "work-106", Actor: "actor:dispatch", OccurredAt: now, PayloadVersion: 2, Payload: payload})

	var lifecycle, failureKind string
	if err := s.db.QueryRow(`SELECT lifecycle_state, failure_kind FROM worker_attempts WHERE attempt_id='attempt:106-exhausted'`).Scan(&lifecycle, &failureKind); err != nil {
		t.Fatalf("trace missing: %v", err)
	}
	if lifecycle != "failed" || failureKind != "routing_policy_exhausted" {
		t.Fatalf("row lifecycle=%s kind=%s", lifecycle, failureKind)
	}
}

func TestTerminalDispatchRefusesWrongShapes(t *testing.T) {
	s, lane, policy := undeclaredModelFixture(t)
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	// Declared model cannot ride the undeclared role.
	if err := ValidateWorkerDispatchIdentity(lane, policy, policy.PreferredModel, WorkerResolutionUndeclared, ""); err == nil || !strings.Contains(err.Error(), "outside the declared resolution set") {
		t.Fatalf("declared model on undeclared role must refuse: %v", err)
	}
	// An undeclared model dispatch must fail as model_identity_mismatch.
	badKind, _ := json.Marshal(WorkerDispatchedPayload{
		AttemptID: "attempt:106-badkind", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: "routing-v1", RoutingPolicyDigest: RoutingPolicyManifestDigest,
		ResolvedModel: "mystery/model", ResolutionRole: WorkerResolutionUndeclared, PacketSchemaVersion: "1.0", ReportSchemaVersion: "1.0",
		Terminal: "failed", TerminalFailureKind: "something_else",
	})
	err := appendWorkerEventErr(t, s, Event{EventID: "w106-badkind", Kind: "worker.dispatched", SubjectType: SubjectWorkItem, SubjectID: "work-106", Actor: "actor:dispatch", OccurredAt: now, PayloadVersion: 2, Payload: badKind})
	if err == nil || !strings.Contains(err.Error(), "model_identity_mismatch") {
		t.Fatalf("wrong failure kind must refuse: %v", err)
	}
	// Empty model with mismatch kind must refuse (mismatch needs the model).
	badEmpty, _ := json.Marshal(WorkerDispatchedPayload{
		AttemptID: "attempt:106-badempty", LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: "routing-v1", RoutingPolicyDigest: RoutingPolicyManifestDigest,
		ResolutionRole: WorkerResolutionUndeclared, PacketSchemaVersion: "1.0", ReportSchemaVersion: "1.0",
		Terminal: "failed", TerminalFailureKind: string(KindModelIdentityMismatch),
	})
	err = appendWorkerEventErr(t, s, Event{EventID: "w106-badempty", Kind: "worker.dispatched", SubjectType: SubjectWorkItem, SubjectID: "work-106", Actor: "actor:dispatch", OccurredAt: now, PayloadVersion: 2, Payload: badEmpty})
	if err == nil || !strings.Contains(err.Error(), "requires the undeclared executing model") {
		t.Fatalf("empty-model mismatch must refuse: %v", err)
	}
}

// appendWorkerEventErr is appendWorkerEvent returning the fold error.
func appendWorkerEventErr(t *testing.T, s *Store, event Event) error {
	t.Helper()
	ctx := context.Background()
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	_, foldErr := applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{event}})
	if foldErr != nil {
		_ = tx.Rollback()
		return foldErr
	}
	if err := leaveFold(ctx, tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return nil
}
