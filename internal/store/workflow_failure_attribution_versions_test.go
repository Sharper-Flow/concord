package store

import (
	"encoding/json"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/testutil/schemawire"
)

// workflowFailureObservationInput is one direct producer observation exactly
// as a correction caller authors it: observer, phase, cause, basis, sources,
// diagnosis — and nothing else. The recorded block's origin, core-derived
// identity, population, sequence, and commit members are core-derived and
// are never authorable here.
func workflowFailureObservationInput(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"observer": WorkerFailureObserverHostAdapter,
		"phase":    WorkerFailurePhaseHostExecution,
		"cause":    WorkerFailureCauseUnknown,
		"basis":    WorkerFailureBasisUnknown,
		"sources": []map[string]any{{
			"kind":    WorkerFailureSourceHostEvent,
			"locator": "evt-cancel-0001",
			"digest":  testDigestA,
		}},
		"diagnosis": "the child session ended before the report readback",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// workflowFailureCorrectionPayload builds one correction action payload with
// the action's required fields plus the supplied extra members.
func workflowFailureCorrectionPayload(t *testing.T, actionID string, extra map[string]any) json.RawMessage {
	t.Helper()
	fields := map[string]any{
		"attempt_id":    "attempt-attribution",
		"attempt_epoch": 1,
		"diagnosis":     "the report readback failed",
		"strategy":      "reproduce the failure and re-dispatch",
	}
	switch actionID {
	case "reject_worker_result":
		fields["predicate_ids"] = []string{"predicate:readback"}
		fields["evidence_refs"] = []string{"evidence:evt-cancel-0001"}
	case "request_correction":
		delete(fields, "attempt_id")
		delete(fields, "attempt_epoch")
		fields["predicate_ids"] = []string{"predicate:readback"}
		fields["evidence_refs"] = []string{"evidence:evt-cancel-0001"}
	}
	for name, value := range extra {
		fields[name] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// workflowFailureDefinitionByRefVersion resolves one registered builtin
// definition or fails the test.
func workflowFailureDefinitionByRefVersion(t *testing.T, ref string, version int64) WorkflowDefinition {
	t.Helper()
	entry, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("%s version %d is not registered", ref, version)
	}
	return entry.Definition
}

// TestWorkflowFailureAttributionGateMatchesPromotedHeads pins the private
// version gate the terminal wiring consumes: exactly implementation 31,
// break_fix 28, and research 18 and above carry the failure-attribution
// semantics, and every released version below — and every retired family —
// stays outside the gate.
func TestWorkflowFailureAttributionGateMatchesPromotedHeads(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ref     string
		version int64
		want    bool
	}{
		{"workflow.implementation", 30, false},
		{"workflow.implementation", 31, true},
		{"workflow.break_fix", 27, false},
		{"workflow.break_fix", 28, true},
		{"workflow.research", 17, false},
		{"workflow.research", 18, true},
		{"workflow.architecture_spike", 17, false},
		{"workflow.ops_runbook", 18, false},
		{"workflow.static_analysis", 15, false},
		{"workflow.generic_one_off", 16, false},
	}
	for _, tc := range cases {
		definition := workflowFailureDefinitionByRefVersion(t, tc.ref, tc.version)
		if got := workflowFailureAttributionActive(definition); got != tc.want {
			t.Errorf("%s version %d gate = %v, want %v", tc.ref, tc.version, got, tc.want)
		}
	}
	if workflowFailureAttributionActive(WorkflowDefinition{Ref: "workflow.implementation"}) {
		t.Error("an unregistered definition ref activates the gate")
	}
}

// TestWorkflowFailureAttributionCorrectionResolverMatchesPinVersion pins the
// version-aware correction resolution: the promoted heads declare the
// optional failure_attribution object field, every released predecessor
// resolves the payload it shipped, and the engine law list stays classic.
func TestWorkflowFailureAttributionCorrectionResolverMatchesPinVersion(t *testing.T) {
	t.Parallel()
	declaresAttribution := func(action WorkflowActionDefinition) *WorkflowPayloadField {
		for index := range action.Payload.Fields {
			if action.Payload.Fields[index].Name != "failure_attribution" {
				continue
			}
			return &action.Payload.Fields[index]
		}
		return nil
	}
	promoted := []struct {
		ref     string
		version int64
	}{
		{"workflow.implementation", 31},
		{"workflow.break_fix", 28},
		{"workflow.research", 18},
	}
	for _, tc := range promoted {
		definition := workflowFailureDefinitionByRefVersion(t, tc.ref, tc.version)
		for _, actionID := range []string{"reject_worker_result", "request_correction"} {
			field := declaresAttribution(workflowCorrectionActionDefinitionFor(definition, actionID))
			if field == nil {
				t.Fatalf("%s v%d %s resolves no failure_attribution field", tc.ref, tc.version, actionID)
			}
			if field.ValueType != PayloadObject || field.Required || field.SchemaRef != "worker_failure_observation" {
				t.Fatalf("%s v%d %s failure_attribution = %+v, want an optional object declaring worker_failure_observation", tc.ref, tc.version, actionID, field)
			}
		}
		request := workflowFailureDefinitionByRefVersion(t, tc.ref, tc.version)
		var requestField *WorkflowPayloadField
		for index := range request.ActionDefinitions {
			if request.ActionDefinitions[index].ID != "request_correction" {
				continue
			}
			requestField = declaresAttribution(request.ActionDefinitions[index])
		}
		if tc.ref == "workflow.research" {
			// Research declares request_correction on no version: the
			// delivery gate correction composes onto implementation and
			// break_fix only, so the version-aware resolver owns research's
			// payload and the in-definition restatement does not apply.
			if requestField != nil {
				t.Fatalf("%s v%d declares request_correction in-definition; the graph changed under its chain", tc.ref, tc.version)
			}
			continue
		}
		if requestField == nil {
			t.Fatalf("%s v%d declares request_correction without the failure_attribution field", tc.ref, tc.version)
		}
	}
	released := []struct {
		ref     string
		version int64
	}{
		{"workflow.implementation", 30},
		{"workflow.implementation", 22},
		{"workflow.break_fix", 27},
		{"workflow.break_fix", 14},
		{"workflow.research", 17},
		{"workflow.research", 9},
	}
	for _, tc := range released {
		definition := workflowFailureDefinitionByRefVersion(t, tc.ref, tc.version)
		for _, actionID := range []string{"reject_worker_result", "request_correction"} {
			if field := declaresAttribution(workflowCorrectionActionDefinitionFor(definition, actionID)); field != nil {
				t.Fatalf("%s v%d %s declares failure_attribution; a released pin changed under its pin", tc.ref, tc.version, actionID)
			}
		}
	}
	for _, action := range BuiltinWorkflowRecoveryActionDefinitions() {
		if action.ID != "reject_worker_result" && action.ID != "request_correction" {
			continue
		}
		if field := declaresAttribution(action); field != nil {
			t.Fatalf("engine recovery law for %s declares failure_attribution; the engine list is version-free", action.ID)
		}
	}
}

// TestWorkflowFailureAttributionPublishedVariantCarriesObservation pins the
// authoring projection's owner list: the canonical recovery list carries the
// attribution variants beside the classic ones, so publication offers the
// field while the store validates each call against the pinned version.
func TestWorkflowFailureAttributionPublishedVariantCarriesObservation(t *testing.T) {
	t.Parallel()
	variants := map[string]int{}
	attributionVariants := map[string]int{}
	for _, action := range BuiltinWorkflowCanonicalRecoveryActionDefinitions() {
		if action.ID != "reject_worker_result" && action.ID != "request_correction" {
			continue
		}
		variants[action.ID]++
		for _, field := range action.Payload.Fields {
			if field.Name == "failure_attribution" {
				attributionVariants[action.ID]++
			}
		}
	}
	for _, actionID := range []string{"reject_worker_result", "request_correction"} {
		if variants[actionID] < 2 {
			t.Fatalf("%s publishes %d canonical variants, want the classic and attribution pair", actionID, variants[actionID])
		}
		if attributionVariants[actionID] != 1 {
			t.Fatalf("%s carries %d attribution variants, want exactly one", actionID, attributionVariants[actionID])
		}
	}
}

// TestWorkflowFailureAttributionReleasedPinsRefuseObservationInput holds the
// freeze boundary at the payload layer: a correction payload carrying the
// observation refuses on every released pin, through the same live-document
// validation admission runs.
func TestWorkflowFailureAttributionReleasedPinsRefuseObservationInput(t *testing.T) {
	t.Parallel()
	observation := workflowFailureObservationInput(t)
	released := []struct {
		ref     string
		version int64
	}{
		{"workflow.implementation", 30},
		{"workflow.break_fix", 27},
		{"workflow.research", 17},
	}
	for _, tc := range released {
		definition := workflowFailureDefinitionByRefVersion(t, tc.ref, tc.version)
		for _, actionID := range []string{"reject_worker_result", "request_correction"} {
			payload := workflowFailureCorrectionPayload(t, actionID, map[string]any{"failure_attribution": observation})
			if err := validateWorkflowActionPayload(definition, actionID, payload); err == nil {
				t.Fatalf("%s v%d admitted failure_attribution on %s; a released pin changed behavior", tc.ref, tc.version, actionID)
			}
		}
	}
}

// TestWorkflowFailureAttributionPromotedPinsAdmitObservationInput proves the
// promoted heads admit the direct observation as an optional input and refuse
// the recorded block's core-derived members at the same boundary. The
// generated document must carry the declared schemas first; a missing
// definition names the regeneration instead of failing silently.
func TestWorkflowFailureAttributionPromotedPinsAdmitObservationInput(t *testing.T) {
	t.Parallel()
	document := payloadschema.Document()
	for _, name := range []string{"worker_failure_observation", "worker_failure_source", "worker_failure_host_observation"} {
		if !payloadschema.HasIn(document, name) {
			t.Fatalf("the generated payload contract declares no %s; run the agent-contract generation before this check", name)
		}
	}
	observation := workflowFailureObservationInput(t)
	promoted := []struct {
		ref     string
		version int64
	}{
		{"workflow.implementation", 31},
		{"workflow.break_fix", 28},
		{"workflow.research", 18},
	}
	for _, tc := range promoted {
		definition := workflowFailureDefinitionByRefVersion(t, tc.ref, tc.version)
		for _, actionID := range []string{"reject_worker_result", "request_correction"} {
			without := workflowFailureCorrectionPayload(t, actionID, nil)
			if err := validateWorkflowActionPayload(definition, actionID, without); err != nil {
				t.Fatalf("%s v%d refused %s without the optional member: %v", tc.ref, tc.version, actionID, err)
			}
			with := workflowFailureCorrectionPayload(t, actionID, map[string]any{"failure_attribution": observation})
			if err := validateWorkflowActionPayload(definition, actionID, with); err != nil {
				t.Fatalf("%s v%d refused the failure_attribution observation on %s: %v", tc.ref, tc.version, actionID, err)
			}
			for _, coreDerived := range []string{"origin", "identity", "input_results", "seq", "commit"} {
				corePayload := workflowFailureCorrectionPayload(t, actionID, map[string]any{
					"failure_attribution": observation,
					coreDerived:           "core-derived",
				})
				if err := validateWorkflowActionPayload(definition, actionID, corePayload); err == nil {
					t.Fatalf("%s v%d admitted the core-derived member %s on %s", tc.ref, tc.version, coreDerived, actionID)
				}
			}
		}
	}
}

// TestWorkflowFailureAttributionFullBlockStaysWithinCoreBound pins the core
// block bound the schema declaration must support: the full recorded block —
// origin, producer observation, and the core-derived identity in both
// variants, the integration one naming exact accepted input results —
// validates, and every serialized shape stays inside the 8192-byte bound the
// declaration is sized against.
func TestWorkflowFailureAttributionFullBlockStaysWithinCoreBound(t *testing.T) {
	t.Parallel()
	attempt := workerFailureAttributionFixture()
	if err := ValidateWorkerFailureAttribution(attempt); err != nil {
		t.Fatalf("the full attempt block refused: %v", err)
	}
	integration := &WorkerFailureAttribution{
		Origin: WorkerFailureAttributionRecorded,
		Observation: func() *WorkerFailureObservation {
			observation := workerFailureObservationFixture()
			observation.Observer = WorkerFailureObserverCoordinator
			observation.Phase = WorkerFailurePhaseIntegration
			observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceNativeRun, Locator: "run-combined-1", Digest: testDigestH}}
			return observation
		}(),
		Identity: workerFailureIntegrationIdentityFixture(),
	}
	if err := ValidateWorkerFailureAttribution(integration); err != nil {
		t.Fatalf("the full integration block refused: %v", err)
	}
	unavailable := &WorkerFailureAttribution{
		Origin: WorkerFailureAttributionRecorded,
		Observation: func() *WorkerFailureObservation {
			observation := workerFailureObservationFixture()
			observation.Observer = WorkerFailureObserverCoordinator
			observation.Phase = WorkerFailurePhaseIntegration
			observation.Sources = []WorkerFailureSource{{Kind: WorkerFailureSourceNativeRun, Locator: "run-combined-1", Digest: testDigestH}}
			return observation
		}(),
		Identity: &WorkerFailureIdentity{
			Kind: WorkerFailureIdentityIntegration,
			Integration: &WorkerFailureIntegrationIdentity{
				NativeRun:         WorkerFailureSubjectRef{Kind: WorkerFailureSubjectNativeRun, Locator: "run-combined-1", Digest: testDigestH},
				InputsUnavailable: true,
			},
		},
	}
	if err := ValidateWorkerFailureAttribution(unavailable); err != nil {
		t.Fatalf("the explicit inputs_unavailable integration block refused: %v", err)
	}
	for name, block := range map[string]*WorkerFailureAttribution{"attempt": attempt, "integration": integration, "unavailable": unavailable} {
		encoded, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > WorkerFailureAttributionMaxBlockBytes {
			t.Fatalf("the %s full block serializes to %d bytes, past the %d-byte core bound", name, len(encoded), WorkerFailureAttributionMaxBlockBytes)
		}
	}
}

// TestWorkflowCorrectionAttributionFitsDerivedActionBudget composes the
// declared maxima of the reject correction fields — every existing member at
// its maximum beside the 8192-byte observation block the store bounds — and
// proves the whole-action budget holds the composition without a limit
// increase. A missing generated definition names the regeneration.
func TestWorkflowCorrectionAttributionFitsDerivedActionBudget(t *testing.T) {
	t.Parallel()
	defs, _ := payloadschema.Document()["$defs"].(map[string]any)
	if defs == nil {
		t.Fatal("the generated payload contract carries no $defs")
	}
	opt := schemawire.Options{
		Defs: defs,
		RefBounds: map[string]int{
			"worker_failure_observation": WorkerFailureAttributionMaxBlockBytes,
		},
		FindingRefMax: 2 + len("finding:") + 19 + 1 + 2,
	}
	for _, name := range []string{"work_transition_action_variant_reject_worker_result", "work_transition_action_variant_request_correction"} {
		composed, err := schemawire.Max(defs[name], name, opt)
		if err != nil {
			t.Fatalf("the %s derivation hit an unbudgeted member: %v", name, err)
		}
		if composed > maxWorkflowActionPayloadBytes {
			t.Fatalf("the %s composed declared maxima need %d bytes, above the %d-byte whole-action budget", name, composed, maxWorkflowActionPayloadBytes)
		}
	}
	// The dispatch walk resolves the worker packet through its context
	// aggregate bound, exactly as the request derivation does, so the
	// dispatch and correction shapes keep one shared construction; the
	// ceilings' exact rounding derives from the widest admitted action
	// shape in the agent package's widest-shape test.
	packetOpt := schemawire.Options{
		Defs: defs,
		PathBounds: map[string]int{
			"worker_packet2/inputs/worker_job/worker_packet_worker_job/acceptance_oracle": OracleCombinedMaxBytes,
			"worker_packet2/inputs/design_record/workflow_design_record/recorded_at":      32,
			"worker_packet2/inputs/proposal_record/workflow_proposal_record/recorded_at":  32,
		},
		Excludes: map[string]bool{
			"worker_packet2/inputs/work_context": true,
			"worker_packet2/inputs/checkpoint":   true,
			"worker_packet2/inputs/correction":   true,
		},
		FindingRefMax: 2 + len("finding:") + 19 + 1 + 2,
	}
	packetParts, err := schemawire.Max(defs["worker_packet2"], "worker_packet2", packetOpt)
	if err != nil {
		t.Fatalf("the packet derivation hit an unbudgeted member: %v", err)
	}
	// The research-context packet3 identity derives through the
	// same excludes: its research closure sits inside the excluded
	// work_context member, so both identities compose identically and the
	// retained packet2 evidence stays.
	packet3Opt := schemawire.Options{
		Defs: defs,
		PathBounds: map[string]int{
			"worker_packet3/inputs/worker_job/worker_packet_worker_job/acceptance_oracle": OracleCombinedMaxBytes,
			"worker_packet3/inputs/design_record/workflow_design_record/recorded_at":      32,
			"worker_packet3/inputs/proposal_record/workflow_proposal_record/recorded_at":  32,
		},
		Excludes: map[string]bool{
			"worker_packet3/inputs/work_context": true,
			"worker_packet3/inputs/checkpoint":   true,
			"worker_packet3/inputs/correction":   true,
		},
		FindingRefMax: 2 + len("finding:") + 19 + 1 + 2,
	}
	packet3Parts, err := schemawire.Max(defs["worker_packet3"], "worker_packet3", packet3Opt)
	if err != nil {
		t.Fatalf("the packet3 derivation hit an unbudgeted member: %v", err)
	}
	if packet3Parts != packetParts {
		t.Fatalf("the packet3 derivation = %d bytes, want the packet2 derivation %d", packet3Parts, packetParts)
	}
	packet4Opt := workerPacketCapacityOptions(defs, "worker_packet4", "worker_packet_checks_repair_job", packetOpt.FindingRefMax)
	packet4Parts, err := schemawire.Max(defs["worker_packet4"], "worker_packet4", packet4Opt)
	if err != nil {
		t.Fatalf("the packet4 derivation hit an unbudgeted member: %v", err)
	}
	// The size-capable packet5 identity mirrors the checks-repair packet4
	// shape beside the recorded production-size members: the measurement
	// basis's base commit and the direct-caller allowance's knowledge
	// content hash are anchored scalars the walker bounds at their exact
	// quoted serializations.
	packet5Opt := workerPacketCapacityOptions(defs, "worker_packet5", "worker_packet5_worker_job", packetOpt.FindingRefMax)
	packet5Opt.PathBounds["worker_packet5/inputs/worker_job/worker_packet5_worker_job/measurement_basis/worker_job_measurement_basis/base_commit"] = 71
	packet5Opt.PathBounds["worker_packet5/inputs/worker_job/worker_packet5_worker_job/direct_caller_allowance/worker_job_direct_caller_allowance/law_bindings/items/worker_oracle_law_binding/source/work_context_reading_source_knowledge/content_hash"] = 71
	packet5Parts, err := schemawire.Max(defs["worker_packet5"], "worker_packet5", packet5Opt)
	if err != nil {
		t.Fatalf("the packet5 derivation hit an unbudgeted member: %v", err)
	}
	dispatchOpt := schemawire.Options{
		Defs: defs,
		RefBounds: map[string]int{
			"worker_failure_observation": WorkerFailureAttributionMaxBlockBytes,
			"worker_packet2":             packetParts + DispatchContextAggregateMaxBytes - 1,
			"worker_packet3":             packet3Parts + DispatchContextAggregateMaxBytes - 1,
			"worker_packet4":             packet4Parts + DispatchContextAggregateMaxBytes - 1,
			"worker_packet5":             packet5Parts + DispatchContextAggregateMaxBytes - 1,
		},
		FindingRefMax: 2 + len("finding:") + 19 + 1 + 2,
	}
	dispatch, err := schemawire.Max(defs["work_transition_action_variant_dispatch_worker"], "work_transition_action_variant_dispatch_worker", dispatchOpt)
	if err != nil {
		t.Fatalf("the dispatch derivation hit an unbudgeted member: %v", err)
	}
	for _, name := range []string{"work_transition_action_variant_reject_worker_result", "work_transition_action_variant_request_correction"} {
		composed, err := schemawire.Max(defs[name], name, opt)
		if err != nil {
			t.Fatal(err)
		}
		if composed >= dispatch {
			t.Fatalf("the %s composed maxima %d bytes reach the dispatch composition %d bytes; the correction attribution input must stay narrower than the dispatch composition this check pins", name, composed, dispatch)
		}
	}
}
