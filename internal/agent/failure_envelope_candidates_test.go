package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

// An ambiguous scope refusal promises the caller the enumerated identities it
// may choose among, and the envelope validator holds the producer to that
// promise. The store's check_mandated_laws boundary mints the refusal with its
// candidates, so the failure-to-envelope mapping must carry them at
// construction: a refusal that arrives with candidates and reaches the caller
// without any is the failure a cross-Product work item met in practice, where
// the typed refusal the core decided became an adapter operation_conflict with
// no deliverable envelope behind it.
func TestCheckMandatedLawsAmbiguousScopeCarriesCandidates(t *testing.T) {
	t.Parallel()
	failure := &store.Failure{
		Kind:           store.KindAmbiguousScope,
		Op:             "check_mandated_laws",
		Detail:         "workflow resolves to multiple canonical Git law homes",
		RetrySafe:      false,
		RecoveryAction: "resolve one Product knowledge home",
		CandidateIDs:   []string{"concord-project/locator-a", "toolbox-project/locator-b"},
	}
	out := failureEnvelope(NewBase("law-home-ambiguous", "concord_work_transition", "workflow_action"), failure)
	if out.Error == nil {
		t.Fatalf("failureEnvelope produced no typed error")
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("an ambiguous scope carrying its candidates must validate, got %v", err)
	}
	if out.Error.Kind != "ambiguous_scope" {
		t.Fatalf("error kind = %q, want ambiguous_scope", out.Error.Kind)
	}
	if want := []string{"concord-project/locator-a", "toolbox-project/locator-b"}; !reflect.DeepEqual(out.Error.Candidates, want) {
		t.Fatalf("candidates = %v, want %v", out.Error.Candidates, want)
	}
	if _, err := json.Marshal(out); err != nil {
		t.Fatalf("the delivered refusal must marshal for the adapter: %v", err)
	}
}

// The validator's requirement stays. The repair makes the producers honest; it
// must not make the detection quieter, because a candidate-less ambiguous
// scope is still one no caller can act on.
func TestEnvelopeStillRefusesACandidateLessAmbiguousScope(t *testing.T) {
	t.Parallel()
	failure := &store.Failure{
		Kind:           store.KindAmbiguousScope,
		Op:             "check_mandated_laws",
		Detail:         "workflow resolves to multiple canonical Git law homes",
		RetrySafe:      false,
		RecoveryAction: "resolve one Product knowledge home",
	}
	out := failureEnvelope(NewBase("law-home-bare", "concord_work_transition", "workflow_action"), failure)
	if err := out.Validate(); err == nil {
		t.Fatalf("an ambiguous scope naming no candidates must still be refused at the envelope boundary")
	}
}
