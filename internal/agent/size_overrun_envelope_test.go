package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/store"
)

func storeSizeOverrunFailure() *store.Failure {
	return &store.Failure{
		Kind:           store.KindInvalidOperation,
		Op:             "workflow_action",
		Detail:         "accept_worker_result: worker job job:a revision 1's observed production size 11 exceeds its cap 10",
		RecoveryAction: store.RecoveryUseDeclaredRoute,
		RecoveryRefs:   []string{"record_worker_job"},
		SizeOverrun:    &store.SizeOverrun{JobID: "job:a", Revision: 1, Cap: 10, Observed: 11, Subject: strings.Repeat("3", 40)},
	}
}

// The overrun refusal reaches the caller with its typed details and the split
// route (CD-0225 D2): the envelope keeps the declared route, so the caller
// splits through record_worker_job without parsing the detail.
func TestSizeOverrunRefusalCarriesTheSplitRoute(t *testing.T) {
	t.Parallel()
	out := failureEnvelope(NewBase("size-overrun-1", "concord_work_transition", "workflow_action"), storeSizeOverrunFailure())
	encoded, err := out.Encode()
	if err != nil {
		t.Fatalf("the overrun refusal cannot be delivered: %v", err)
	}
	var decoded struct {
		Error TypedError `json:"error"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Error
	if got.Kind != "invalid_input" || got.RecoveryAction.Kind != "use_declared_route" || !slices.Equal(got.RecoveryAction.RequiredRefs, []string{"record_worker_job"}) {
		t.Fatalf("overrun error = %s / %s %v, want invalid_input with the record_worker_job route", got.Kind, got.RecoveryAction.Kind, got.RecoveryAction.RequiredRefs)
	}
	want := SizeOverrun{JobID: "job:a", Revision: 1, Cap: 10, Observed: 11, Subject: strings.Repeat("3", 40)}
	if got.SizeOverrun == nil || *got.SizeOverrun != want {
		t.Fatalf("size_overrun = %#v, want %#v", got.SizeOverrun, want)
	}
}

func TestSizeOverrunCouplingRefusesAnUnroutedOverrun(t *testing.T) {
	t.Parallel()
	valid := func() TypedError {
		return TypedError{Kind: "invalid_input", Message: "overrun", RecoveryAction: RecoveryAction{Kind: "use_declared_route", RequiredRefs: []string{"record_worker_job"}}, EffectState: EffectNone,
			SizeOverrun: &SizeOverrun{JobID: "job:a", Revision: 1, Cap: 10, Observed: 11, Subject: strings.Repeat("3", 40)}}
	}
	if err := validateError(valid()); err != nil {
		t.Fatalf("a routed overrun refused: %v", err)
	}
	for name, mutate := range map[string]func(*TypedError){
		"another kind": func(e *TypedError) {
			e.Kind = "limit_exceeded"
			e.RecoveryAction = RecoveryAction{Kind: "reduce_limit"}
		},
		"another route":            func(e *TypedError) { e.RecoveryAction.RequiredRefs = []string{"reject_worker_result"} },
		"an observed count in cap": func(e *TypedError) { e.SizeOverrun.Observed = 10 },
		"a subject that is no oid": func(e *TypedError) { e.SizeOverrun.Subject = "HEAD" },
	} {
		broken := valid()
		mutate(&broken)
		if err := validateError(broken); err == nil || !strings.Contains(err.Error(), "size overrun coupling violated") {
			t.Errorf("%s: validateError = %v, want the size overrun coupling refusal", name, err)
		}
	}
}
