package store

import (
	"testing"
	"time"
)

func TestNativeRunStatusVocabularyCoversEveryDeclaredPair(t *testing.T) {
	actor := WorkflowActor{PrincipalRef: "principal:native-vocab", ClientRef: "client:native-vocab", AgentRef: "agent:native-vocab", SessionRef: "session:native-vocab", ActorClass: ActorAgent}
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	for phase, statuses := range map[string][]string{
		"start":    {"started", "failed_to_start"},
		"health":   {"healthy", "degraded", "failed"},
		"rollback": {"rolled_back", "partially_rolled_back", "rollback_failed"},
		"cleanup":  {"cleaned", "cleanup_failed"},
	} {
		for _, status := range statuses {
			if !NativeRunStatusAllowed(phase, status) {
				t.Errorf("pair %s/%s is not allowed", phase, status)
			}
			wantFailure := phase == "rollback" || status == "failed_to_start" || status == "failed" || status == "cleanup_failed"
			if got := NativeRunStatusIsFailure(phase, status); got != wantFailure {
				t.Errorf("failure classification %s/%s=%t, want %t", phase, status, got, wantFailure)
			}
			if _, err := buildNativeRunEvent("vocab-"+phase+"-"+status, "vocab-work", actor, now, 1, phase, "run", "native://run", status, "evidence://run", "sha256:"+repeatHex("a"), now.Format(time.RFC3339Nano)); err != nil {
				t.Errorf("build pair %s/%s: %v", phase, status, err)
			}
		}
	}
}

func TestNativeRunStatusVocabularyRejectsWrongPhaseAndUnknownStatus(t *testing.T) {
	actor := WorkflowActor{PrincipalRef: "principal:native-vocab-invalid", ClientRef: "client:native-vocab-invalid", AgentRef: "agent:native-vocab-invalid", SessionRef: "session:native-vocab-invalid", ActorClass: ActorAgent}
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ phase, status string }{{"start", "healthy"}, {"health", "unknown"}} {
		if NativeRunStatusAllowed(tc.phase, tc.status) {
			t.Fatalf("invalid pair %s/%s is allowed", tc.phase, tc.status)
		}
		if _, err := buildNativeRunEvent("invalid-"+tc.phase, "vocab-work", actor, now, 1, tc.phase, "run", "native://run", tc.status, "evidence://run", "sha256:"+repeatHex("a"), now.Format(time.RFC3339Nano)); err == nil {
			t.Fatalf("invalid pair %s/%s was accepted", tc.phase, tc.status)
		}
	}
}
