package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func routingPolicyTestDocument(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("../../contracts/routing-policy.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func writeRoutingPolicyTestDocument(t *testing.T, document map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routing-policy.json")
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

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
			if lane.CapabilityClass == policy.CapabilityClass && policy.PreferredModel == "" {
				t.Fatalf("lane %s has an empty policy preferred model", lane.ID)
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

func TestRoutingPolicyHostOverrideBindsDigestAndDispatchValidation(t *testing.T) {
	document := routingPolicyTestDocument(t)
	for _, raw := range document["policies"].([]any) {
		entry := raw.(map[string]any)
		entry["preferred_model"] = "host/preferred"
		entry["resolution_set"] = []any{"host/preferred", "host/fallback"}
	}
	t.Setenv(routingPolicySourceEnv, writeRoutingPolicyTestDocument(t, document))
	s := openTemp(t)
	defer s.Close()
	if got := LoadedRoutingPolicyManifestDigest(); got == RoutingPolicyManifestDigest {
		t.Fatalf("host policy digest = default digest %q", got)
	}
	lane := BuiltinLaneDefinitions()[0]
	policy, err := LookupRoutingPolicy(lane.CapabilityClass, "routing-v1", LoadedRoutingPolicyManifestDigest())
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkerDispatchIdentity(lane, policy, "host/preferred", WorkerResolutionPreferred, ""); err != nil {
		t.Fatalf("host policy dispatch validation failed: %v", err)
	}
	if err := ValidateWorkerDispatchIdentity(lane, policy, "openai/gpt-5.6-luna", WorkerResolutionPreferred, ""); !hasFailureKind(err, KindRoutingPolicyInvalid) {
		t.Fatalf("default model accepted by host policy: %v", err)
	}
}

func TestRoutingPolicySetButMissingPathFailsTyped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-routing-policy.json")
	t.Setenv(routingPolicySourceEnv, path)
	_, err := LoadRoutingPolicyRegistry()
	if !hasFailureKind(err, KindUnavailable) || !strings.Contains(err.Error(), path) {
		t.Fatalf("missing host policy error = %v, want typed path-bearing unavailable failure", err)
	}
}

func TestRoutingPolicyMalformedAndIncompleteHostPoliciesNameFailures(t *testing.T) {
	t.Run("missing field", func(t *testing.T) {
		document := map[string]any{"schema_version": "1.0", "registry": "routing_policy", "version": "routing-v1"}
		t.Setenv(routingPolicySourceEnv, writeRoutingPolicyTestDocument(t, document))
		_, err := LoadRoutingPolicyRegistry()
		if !hasFailureKind(err, KindRoutingPolicyInvalid) || !strings.Contains(err.Error(), "policies") {
			t.Fatalf("malformed policy error = %v, want policies field", err)
		}
	})
	t.Run("missing capability class", func(t *testing.T) {
		document := routingPolicyTestDocument(t)
		document["policies"] = document["policies"].([]any)[:3]
		t.Setenv(routingPolicySourceEnv, writeRoutingPolicyTestDocument(t, document))
		_, err := LoadRoutingPolicyRegistry()
		if !hasFailureKind(err, KindRoutingPolicyInvalid) || !strings.Contains(err.Error(), "verification") {
			t.Fatalf("incomplete policy error = %v, want verification class", err)
		}
	})
	t.Run("unknown capability class", func(t *testing.T) {
		document := routingPolicyTestDocument(t)
		entry := document["policies"].([]any)[0].(map[string]any)
		entry["capability_class"] = "unknown"
		t.Setenv(routingPolicySourceEnv, writeRoutingPolicyTestDocument(t, document))
		_, err := LoadRoutingPolicyRegistry()
		if !hasFailureKind(err, KindRoutingPolicyInvalid) || !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("unknown class policy error = %v, want unknown class", err)
		}
	})
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
		{name: "fallback without reason", model: "zai-coding-plan/glm-5.3", role: WorkerResolutionFallback, reason: ""},
		{name: "preferred marked fallback", model: preferredModelForLane(lane), role: WorkerResolutionFallback, reason: "rate_limit"},
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
	if err := ValidateWorkerCompletion(preferredModelForLane(lane), "zai-coding-plan/glm-5.3"); !hasFailureKind(err, KindModelIdentityMismatch) {
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
