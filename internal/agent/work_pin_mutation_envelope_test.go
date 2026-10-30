package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// The mutation result envelope is where a caller learns the pinned
// definition's declared obligations (CON-412): every workflow action mutation
// stamps work_pins onto its result through the same ReadWorkPin path the
// continuity read renders. The envelope must carry the sorted exact
// obligation IDs of the pinned definition and the definition identity the
// registry verifies, not a stale or partial copy.
func TestMutationEnvelopePublishesPinnedObligationsAndDefinitionIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, service, grant, _ := mutationDispatchFixture(t, []agentcontract.Capability{"work_define", "work_transition"})
	seedCurrentWorkflowDomainFixture(t, s)
	scopeVersion, _, err := s.ScopeVersion(ctx, "project-1")
	if err != nil {
		t.Fatal(err)
	}
	env := mutationEnvelope(grant, scopeVersion)
	workID := captureCompositionWork(t, ctx, s, service, env, "Mutation envelope obligations", "bug", "workflow.break_fix", "obligation-envelope")
	version := agentCompositionWorkVersion(t, s, workID)

	response, _ := ekbDispatch(t, s, service, env, workID, version, "record_diagnosis", map[string]any{
		"reproduction": "The bounded defect reproduction statement.",
		"searched":     "The bounded backlog search statement.",
		"outcome":      "none_found",
		"root_cause":   "The bounded root cause statement.",
	}, nil, "obligation-envelope-diagnosis")
	if response.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("record_diagnosis: %+v", response.Error)
	}
	var payload struct {
		WorkPins []store.WorkPin `json:"work_pins"`
	}
	if err := json.Unmarshal(response.Result, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.WorkPins) != 1 {
		t.Fatalf("mutation envelope work pins=%d, want the pinned work item", len(payload.WorkPins))
	}
	pin := payload.WorkPins[0]
	registered, err := store.BuiltinWorkflowDefinitionForRef("workflow.break_fix")
	if err != nil {
		t.Fatal(err)
	}
	// The obligation list is the definition's own root/step/rigor
	// declaration set, sorted exactly.
	want := map[string]struct{}{}
	for _, kind := range registered.Definition.RequiredEvidenceKinds {
		want[string(kind)] = struct{}{}
	}
	for _, step := range registered.Definition.StepGraph.Steps {
		for _, kind := range step.RequiredEvidenceKinds {
			want[string(kind)] = struct{}{}
		}
	}
	for _, rule := range registered.Definition.RigorRules {
		for _, kind := range rule.RequiredEvidenceKinds {
			want[string(kind)] = struct{}{}
		}
	}
	wantIDs := make([]string, 0, len(want))
	for id := range want {
		wantIDs = append(wantIDs, id)
	}
	sort.Strings(wantIDs)
	if pin.Obligations == nil || !reflect.DeepEqual(*pin.Obligations, wantIDs) {
		t.Fatalf("mutation envelope obligations=%v, want the sorted declarations %v", pin.Obligations, wantIDs)
	}
	if pin.WorkflowDefinitionDigest == nil || *pin.WorkflowDefinitionDigest != registered.Digest {
		t.Fatalf("mutation envelope definition digest=%v, want the pinned %s", pin.WorkflowDefinitionDigest, registered.Digest)
	}
	if err := store.BuiltinWorkflowRegistry().Verify("workflow.break_fix", *pin.WorkflowDefinitionDigest); err != nil {
		t.Fatalf("mutation envelope definition identity does not verify: %v", err)
	}
	// The published result schema declares obligations a required property of
	// work_pin, so the envelope literally carries them, not only the struct.
	var raw map[string]any
	if err := json.Unmarshal(response.Result, &raw); err != nil {
		t.Fatal(err)
	}
	pins, _ := raw["work_pins"].([]any)
	if len(pins) != 1 {
		t.Fatalf("mutation envelope raw work_pins=%d", len(pins))
	}
	first, _ := pins[0].(map[string]any)
	obligations, _ := first["obligations"].([]any)
	if len(obligations) != len(wantIDs) {
		t.Fatalf("mutation envelope raw obligations=%v, want %v", obligations, wantIDs)
	}
}
