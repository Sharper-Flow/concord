package agent

import (
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// A version conflict promises the caller a live version to re-read, and the
// envelope validator holds the producer to that promise. The agent mapping
// must therefore carry every carrier the store supplies: a refusal that
// arrives with a version and reaches the caller without one is the failure two
// sessions met in practice, where the typed refusal the core decided became a
// transport fault with no operation_id to reconcile against.
func TestVersionConflictMappingCarriesTheLiveVersion(t *testing.T) {
	failure := &store.Failure{
		Kind:           store.KindVersionConflict,
		Op:             "workflow_action_preflight",
		Detail:         "work_item work-1 has version 3, want 1",
		RetrySafe:      false,
		RecoveryAction: "reload the subject and retry with its current version",
		CurrentVersions: []store.SubjectCurrentVersion{
			{SubjectType: store.SubjectWorkItem, SubjectID: "work-1", Version: 3},
		},
	}
	out := failureEnvelope(NewBase("vc-live", "concord_work_transition", "workflow_action"), failure)
	if out.Error == nil {
		t.Fatalf("failureEnvelope produced no typed error")
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("a version conflict carrying its live version must validate, got %v", err)
	}
	if len(out.Error.CurrentVersions) != 1 {
		t.Fatalf("error.current_versions len=%d, want 1", len(out.Error.CurrentVersions))
	}
	if got := out.Error.CurrentVersions[0]; got.EntityKind != "work_item" || got.ID != "work-1" || got.Version != "3" {
		t.Fatalf("error.current_versions[0]=%+v, want work_item/work-1/3", got)
	}
}

// The validator's requirement stays. The repair makes the producers honest; it
// must not make the detection quieter, because a carrier-less version conflict
// is still one no caller can act on.
func TestEnvelopeStillRefusesACarrierLessVersionConflict(t *testing.T) {
	failure := &store.Failure{
		Kind:           store.KindVersionConflict,
		Op:             "research_mutation",
		Detail:         "research pack changed before its write",
		RetrySafe:      false,
		RecoveryAction: "reload the pack and retry",
	}
	out := failureEnvelope(NewBase("vc-bare", "concord_work_define", "research_finding_record"), failure)
	if err := out.Validate(); err == nil {
		t.Fatalf("a version conflict naming no current version must still be refused at the envelope boundary")
	}
}
