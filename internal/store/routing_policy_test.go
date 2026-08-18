package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRoutingPolicyRegistryIsDigestPinnedAndMatchesLanePreferences(t *testing.T) {
	policies := BuiltinRoutingPolicies()
	if len(policies) != 4 {
		t.Fatalf("routing policy count = %d, want 4", len(policies))
	}
	digestBytes, err := os.ReadFile("../../contracts/routing-policy.digest")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(digestBytes)) != RoutingPolicyManifestDigest {
		t.Fatalf("routing policy digest file = %q, generated Go = %q", strings.TrimSpace(string(digestBytes)), RoutingPolicyManifestDigest)
	}
	for _, policy := range policies {
		if err := ValidateRoutingPolicyDefinition(policy); err != nil {
			t.Fatalf("ValidateRoutingPolicyDefinition(%s) = %v", policy.CapabilityClass, err)
		}
		if _, err := LookupRoutingPolicy(policy.CapabilityClass, RoutingPolicyVersion, RoutingPolicyManifestDigest); err != nil {
			t.Fatalf("LookupRoutingPolicy(%s) error = %v", policy.CapabilityClass, err)
		}
		for _, lane := range BuiltinLaneDefinitions() {
			if lane.CapabilityClass == policy.CapabilityClass && lane.PinnedModel != policy.PreferredModel {
				t.Fatalf("lane %s pinned model = %q, policy preferred = %q", lane.ID, lane.PinnedModel, policy.PreferredModel)
			}
		}
	}
	if _, err := LookupRoutingPolicy("unknown", RoutingPolicyVersion, RoutingPolicyManifestDigest); !hasFailureKind(err, KindRoutingPolicyNotRegistered) {
		t.Fatalf("unknown policy error = %v", err)
	}
	if _, err := LookupRoutingPolicy("research", RoutingPolicyVersion, "sha256:"+strings.Repeat("0", 64)); !hasFailureKind(err, KindRoutingPolicyDigestMismatch) {
		t.Fatalf("digest mismatch error = %v", err)
	}
}

func TestRecordedFallbackCompletesWhenReadbackMatchesDeclaredResolution(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[2]
	policy, err := LookupRoutingPolicy(lane.CapabilityClass, RoutingPolicyVersion, RoutingPolicyManifestDigest)
	if err != nil {
		t.Fatal(err)
	}
	fallback := policy.ResolutionSet[1]
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerDispatchV2Event("fallback-work", "fallback-dispatch", lane, fallback, WorkerResolutionFallback, "provider_unavailable")}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{workerCompleteEvent("fallback-work", "fallback-complete", "fallback-dispatch", fallback)}}); err != nil {
		t.Fatal(err)
	}
	var state, role, reason, resolved, readback, digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,resolution_role,fallback_reason,resolved_model,readback_model,routing_policy_digest FROM worker_attempts WHERE attempt_id=?`, "fallback-dispatch").Scan(&state, &role, &reason, &resolved, &readback, &digest); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || role != WorkerResolutionFallback || reason != "provider_unavailable" || resolved != fallback || readback != fallback || digest != RoutingPolicyManifestDigest {
		t.Fatalf("fallback projection = %s/%s/%s/%s/%s/%s", state, role, reason, resolved, readback, digest)
	}
	before := fallbackProjectionSnapshot(t, s)
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if after := fallbackProjectionSnapshot(t, s); after != before {
		t.Fatalf("fallback rebuild changed projection: got %s want %s", after, before)
	}
}

func TestRoutingPolicyRejectsSilentSubstitutionAndInvalidResolutionEvidence(t *testing.T) {
	s := openTemp(t)
	lane := BuiltinLaneDefinitions()[0]
	cases := []struct {
		name   string
		model  string
		role   string
		reason string
	}{
		{name: "undeclared model", model: "openai/not-declared", role: WorkerResolutionFallback, reason: "other"},
		{name: "fallback without reason", model: "zai-coding-plan/glm-5.2", role: WorkerResolutionFallback, reason: ""},
		{name: "preferred marked fallback", model: lane.PinnedModel, role: WorkerResolutionFallback, reason: "rate_limit"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			event := workerDispatchV2Event("invalid-"+testCase.name, "invalid-"+testCase.name, lane, testCase.model, testCase.role, testCase.reason)
			if err := ApplyOperation(context.Background(), s, Operation{Events: []Event{event}}); !hasFailureKind(err, KindRoutingPolicyInvalid) {
				t.Fatalf("error = %v, want %s", err, KindRoutingPolicyInvalid)
			}
			if got := countRows(t, s, "worker_attempts"); got != 0 {
				t.Fatalf("invalid dispatch rows = %d, want 0", got)
			}
		})
	}
	if err := ValidateWorkerCompletion(lane.PinnedModel, "zai-coding-plan/glm-5.2"); !hasFailureKind(err, KindModelIdentityMismatch) {
		t.Fatalf("readback mismatch = %v, want %s", err, KindModelIdentityMismatch)
	}
}

func workerDispatchV2Event(workID, eventID string, lane LaneDefinition, model, role, reason string) Event {
	return Event{EventID: eventID, Kind: WorkerDispatched, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "worker:test", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: mustJSONValue(WorkerDispatchedPayload{
		AttemptID: eventID, LaneID: lane.ID, LaneVersion: lane.Version, LaneDigest: lane.Digest,
		CapabilityClass: lane.CapabilityClass, RoutingPolicyVersion: RoutingPolicyVersion, RoutingPolicyDigest: RoutingPolicyManifestDigest,
		ResolvedModel: model, ResolutionRole: role, FallbackReason: reason,
		PacketSchemaVersion: WorkerPacketSchemaVersion, ReportSchemaVersion: WorkerReportSchemaVersion,
	})}
}

func fallbackProjectionSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	var state, role, reason, resolved, readback, digest string
	if err := s.DatabaseForTesting().QueryRow(`SELECT lifecycle_state,resolution_role,fallback_reason,resolved_model,readback_model,routing_policy_digest FROM worker_attempts WHERE attempt_id=?`, "fallback-dispatch").Scan(&state, &role, &reason, &resolved, &readback, &digest); err != nil {
		var failure *Failure
		if errors.As(err, &failure) {
			t.Fatal(failure)
		}
		t.Fatal(err)
	}
	return strings.Join([]string{state, role, reason, resolved, readback, digest}, "|")
}
