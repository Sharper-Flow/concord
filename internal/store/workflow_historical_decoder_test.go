package store

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// retainedDefinitionWithSchemaRef returns a synthesized workflow definition
// that carries a retained (non-head) registry pin identity and one action
// whose field answers to a frozen historical schema. The classification keys
// on the (ref, version) pin, so the synthesized payload exercises exactly the
// historical resolution the engine must keep independent of the public
// authoring document.
func retainedDefinitionWithSchemaRef(t *testing.T) WorkflowDefinition {
	t.Helper()
	head, err := BuiltinWorkflowDefinitionForRef("workflow.architecture_spike")
	if err != nil {
		t.Fatal(err)
	}
	retained := head.Definition
	var found bool
	for version := retained.Version - 1; version >= 1 && !found; version-- {
		if entry, ok := BuiltinWorkflowRegistry().Lookup(retained.Ref, version); ok {
			retained = entry.Definition
			found = true
		}
	}
	if !found {
		t.Fatal("architecture_spike registers no retained version below its head")
	}
	return WorkflowDefinition{
		Ref:     retained.Ref,
		Version: retained.Version,
		ActionDefinitions: []WorkflowActionDefinition{{
			ID: "probe",
			Payload: WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{
				{Name: "value", ValueType: PayloadObject, Required: true, SchemaRef: "workflow_design_decision"},
			}},
		}},
	}
}

// pinIsBelowHead reports whether the definition's registry pin sits below its
// family's current head: the retained-pin identity the role-split witnesses
// need, read from the registry itself.
func pinIsBelowHead(t *testing.T, definition WorkflowDefinition) bool {
	t.Helper()
	head, err := BuiltinWorkflowDefinitionForRef(definition.Ref)
	if err != nil {
		t.Fatalf("no current head for %s: %v", definition.Ref, err)
	}
	return definition.Version < head.Definition.Version
}

// TestRetainedDefinitionPayloadValidationResolvesFrozenHistoricalDecoder
// proves the S2 invocation-role split at the engine boundary: a LIVE payload
// resolves its declared schema refs through the public authoring document
// whatever pin its definition carries (a retained pin dispatching live
// carries the current published packet), a RECORDED payload replays through
// the frozen historical decoder document whatever pin it records, and the
// engine cross-field rules answer the same on both sides.
func TestRetainedDefinitionPayloadValidationResolvesFrozenHistoricalDecoder(t *testing.T) {
	retained := retainedDefinitionWithSchemaRef(t)
	if !pinIsBelowHead(t, retained) {
		t.Fatal("the witness pin is not a retained registry pin below its head")
	}
	if _, ok := BuiltinWorkflowRegistry().Lookup(retained.Ref, retained.Version); !ok {
		t.Fatal("the synthesized retained pin is not registered")
	}

	valid := json.RawMessage(`{"value":{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":[]}}`)
	invalid := json.RawMessage(`{"value":{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":"not-a-list"}}`)
	if err := validateWorkflowActionPayload(retained, "probe", valid); err != nil {
		t.Errorf("retained-pin live validation refused a payload the public document accepts: %v", err)
	}
	if err := validateWorkflowActionPayload(retained, "probe", invalid); err == nil {
		t.Error("retained-pin live validation accepted a payload the public document refuses")
	}
	// The declared schema resolves the field value itself, so the direct
	// decoder corpus is the field object without the payload wrapper. The
	// replay role validates the same recorded corpus through the frozen
	// decoder, and the frozen verdict matches the public verdict while the
	// two documents carry the same bytes.
	fieldValid := json.RawMessage(`{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":[]}`)
	fieldInvalid := json.RawMessage(`{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":"not-a-list"}`)
	if err := payloadschema.ValidateInDocument(payloadschema.HistoricalDocument(), "workflow_design_decision", fieldValid); err != nil {
		t.Errorf("frozen decoder disagrees with the classified engine path on the valid corpus: %v", err)
	}
	if err := payloadschema.ValidateInDocument(payloadschema.HistoricalDocument(), "workflow_design_decision", fieldInvalid); err == nil {
		t.Error("frozen decoder disagrees with the classified engine path on the invalid corpus")
	}

	// The live role resolves every declared schema ref of every registered
	// definition through the public authoring document, and the replay role
	// resolves them all through the frozen decoder — the new-pin replay
	// obligation: every current pin becomes retained later, so the frozen
	// document must already carry what a current definition declares, not
	// only what the S0 baseline carried.
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		for _, action := range definition.ActionDefinitions {
			for _, field := range action.Payload.Fields {
				for _, ref := range []string{field.SchemaRef, field.ItemRef} {
					if ref == "" {
						continue
					}
					if !payloadschema.HasIn(workflowLivePayloadSchemaDocument(), ref) {
						t.Errorf("definition %s v%d action %s field %s declares schema %q, which the live public document does not carry", definition.Ref, definition.Version, action.ID, field.Name, ref)
					}
					if !payloadschema.HasIn(workflowReplayPayloadSchemaDocument(), ref) {
						t.Errorf("definition %s v%d action %s field %s declares schema %q, which the replay frozen decoder does not carry", definition.Ref, definition.Version, action.ID, field.Name, ref)
					}
				}
			}
		}
	}
}

// TestRetainedPinClassificationCoversEveryRegisteredHistoryVersion pins the
// retained-pin identity itself: exactly the registered (ref, version) entries
// below a family head are retained, and every current head is not.
func TestRetainedPinClassificationCoversEveryRegisteredHistoryVersion(t *testing.T) {
	current := map[string]bool{}
	for _, definition := range BuiltinWorkflowDefinitions() {
		current[definition.Ref+"/"+strconv.FormatInt(definition.Version, 10)] = true
	}
	historyCount := 0
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		key := definition.Ref + "/" + strconv.FormatInt(definition.Version, 10)
		if current[key] {
			if pinIsBelowHead(t, definition) {
				t.Errorf("current head %s reads as below its own head", key)
			}
			continue
		}
		historyCount++
		if !pinIsBelowHead(t, definition) {
			t.Errorf("retained pin %s does not read as below its head", key)
		}
	}
	if historyCount == 0 {
		t.Fatal("the registry carries no retained versions; the classification witness is vacuous")
	}
}

// TestCurrentAndRecoveryDeclarationsResolvePublicDocument keeps the CON-412
// authoring invariant: every schema ref a current definition or engine
// recovery declaration declares still resolves in the public authoring
// document the published variants build from.
func TestCurrentAndRecoveryDeclarationsResolvePublicDocument(t *testing.T) {
	definitions := BuiltinWorkflowDefinitions()
	definitions = append(definitions, recoveryDefinitionWithSchemaRefs(t)...)
	for _, definition := range definitions {
		for _, action := range definition.ActionDefinitions {
			for _, field := range action.Payload.Fields {
				for _, ref := range []string{field.SchemaRef, field.ItemRef} {
					if ref == "" {
						continue
					}
					if !payloadschema.HasIn(payloadschema.Document(), ref) {
						t.Errorf("current declaration %s/%s field %s names schema %q, which the public authoring document does not carry", definition.Ref, action.ID, field.Name, ref)
					}
				}
			}
		}
	}
	if len(definitions) == 0 {
		t.Fatal("no current definitions declared")
	}
}

// recoveryDefinitionWithSchemaRefs collects the engine recovery action
// definitions the preflight substitutes for old pins.
func recoveryDefinitionWithSchemaRefs(t *testing.T) []WorkflowDefinition {
	t.Helper()
	names := []string{"record_worker_failure", "reject_worker_result", "request_correction"}
	definitions := make([]WorkflowDefinition, 0, len(names))
	for _, name := range names {
		var action WorkflowActionDefinition
		switch name {
		case "record_worker_failure":
			action = workerFailureRecoveryActionDefinition()
		case "reject_worker_result":
			action = workflowCorrectionActionDefinition()
		case "request_correction":
			action = workflowCorrectionRequestActionDefinition()
		}
		if action.ID != name {
			t.Fatalf("recovery action %s resolved to %q", name, action.ID)
		}
		definitions = append(definitions, WorkflowDefinition{Ref: "engine.recovery", Version: 1, ActionDefinitions: []WorkflowActionDefinition{action}})
	}
	return definitions
}

// retainedDefinitionWithWorkerPacket returns a synthesized retained-pin
// definition whose one action carries a worker_packet schema ref, the
// transport schema the live/replay role split must discriminate on: the
// public document admits the 2.0 packet identity CON-899 shipped, the frozen
// decoder carries only the 1.0/1.1 identities its recorded writes answered
// to.
func retainedDefinitionWithWorkerPacket() WorkflowDefinition {
	return WorkflowDefinition{
		Ref:     "workflow.implementation",
		Version: 1,
		ActionDefinitions: []WorkflowActionDefinition{{
			ID: "probe",
			Payload: WorkflowPayloadDefinition{Closed: true, Fields: []WorkflowPayloadField{
				{Name: "value", ValueType: PayloadObject, Required: true, SchemaRef: "worker_packet"},
			}},
		}},
	}
}

// discriminatingWorkerPacket builds one minimal packet the worker_packet
// schema accepts at the given schema_version, so the enum tail alone decides
// the verdict between the two documents.
func discriminatingWorkerPacket(schemaVersion string) json.RawMessage {
	return json.RawMessage(`{"schema_version":"` + schemaVersion + `","attempt_id":"attempt-probe","lane_id":"implement","lane_version":1,"lane_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","work_id":"work-probe","step_id":"execution","inputs":{"task":"the bounded task","binding":{"objective_source":"contract_premise","work_version":1,"contract_version":1,"assigned_result":"changed_files"},"report_protocol":"concord-worker-result-v1"}}`)
}

// TestLiveRetainedPinAdmitsModernPacketReplayStaysFrozen is the S2
// live/replay witness: a LIVE dispatch on a retained pin carries the current
// published 2.0 packet and the public document admits it; the same recorded
// packet replayed against the frozen decoder refuses, the original historic
// 1.1 packet replays exactly, and a mutation of the public document's packet
// enum cannot move the frozen verdict — the frozen history is not derived
// from the mutable public document.
func TestLiveRetainedPinAdmitsModernPacketReplayStaysFrozen(t *testing.T) {
	definition := retainedDefinitionWithWorkerPacket()
	wrap := func(packet json.RawMessage) json.RawMessage {
		return json.RawMessage(`{"value":` + string(packet) + `}`)
	}
	modern := wrap(discriminatingWorkerPacket("2.0"))
	historic := wrap(discriminatingWorkerPacket("1.1"))
	futurePacket := discriminatingWorkerPacket("9.9")
	future := wrap(futurePacket)
	// The live document is the current published contract: it admits the
	// 2.0 packet identity it publishes and has moved past the 1.1 identity
	// only the frozen decoder still decodes.
	if err := validateWorkflowActionPayload(definition, "probe", modern); err != nil {
		t.Errorf("live validation refused the current published 2.0 packet on a retained pin: %v", err)
	}
	if err := validateWorkflowActionPayload(definition, "probe", historic); err == nil {
		t.Error("live validation admitted the retired 1.1 packet identity the published contract no longer carries")
	}
	if err := validateWorkflowActionPayloadForReplay(definition, "probe", historic); err != nil {
		t.Errorf("replay validation refused the original historic 1.1 packet through the frozen decoder: %v", err)
	}
	if err := validateWorkflowActionPayloadForReplay(definition, "probe", modern); err == nil {
		t.Error("replay validation admitted the 2.0 packet identity the frozen decoder never froze")
	}

	// The public document is the mutable side: a hypothetical future packet
	// identity admitted there changes no frozen verdict, because the replay
	// role resolves the frozen decoder alone.
	mutated := deepCopyJSONDocument(t, payloadschema.Document())
	defs := mutated["$defs"].(map[string]any)
	packet, ok := defs["worker_packet"].(map[string]any)
	if !ok {
		t.Fatal("the public document carries no worker_packet definition")
	}
	properties := packet["properties"].(map[string]any)
	schemaVersion := properties["schema_version"].(map[string]any)
	enum, ok := schemaVersion["enum"].([]any)
	if !ok || len(enum) == 0 {
		t.Fatal("the public worker_packet carries no readable schema_version enum")
	}
	properties["schema_version"] = map[string]any{"type": "string", "enum": append(append([]any{}, enum...), "9.9")}
	if err := payloadschema.ValidateInDocument(mutated, "worker_packet", futurePacket); err != nil {
		t.Fatalf("the mutated public document refused its own future packet identity: %v", err)
	}
	if err := validateWorkflowActionPayloadForReplay(definition, "probe", future); err == nil {
		t.Error("a mutation of the public document's packet enum changed the frozen replay verdict")
	}
	if err := payloadschema.ValidateInDocument(payloadschema.HistoricalDocument(), "worker_packet", future); err == nil {
		t.Error("the frozen decoder admitted the future packet identity the public mutation introduced")
	}
}

// deepCopyJSONDocument copies one parsed schema document the way the
// payloadschema test helpers do, so a test can mutate its own replica
// without touching the shared parsed documents.
func deepCopyJSONDocument(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var replica map[string]any
	if err := json.Unmarshal(raw, &replica); err != nil {
		t.Fatal(err)
	}
	return replica
}

// TestHistoricalEngineCrossFieldRulesStayVersionInvariant keeps the global
// engine cross-field rules outside the schema documents: a retained pin's
// payload answers to the same engine declaration a current pin answers to,
// so a refusal cannot depend on which document resolved the refs.
// record_alignment declares the combinations; implementation v14 first
// carried it, so a retained pin and the current head run the same corpus.
func TestHistoricalEngineCrossFieldRulesStayVersionInvariant(t *testing.T) {
	head, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 29)
	if !ok {
		t.Fatal("implementation v29 is not registered")
	}
	retained, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 14)
	if !ok {
		t.Fatal("implementation v14 is not registered")
	}
	if pinIsBelowHead(t, head.Definition) || !pinIsBelowHead(t, retained.Definition) {
		t.Fatal("the witness pins are not one current head and one retained pin")
	}
	corpus := []json.RawMessage{
		json.RawMessage(`{"searched":"the registry","outcome":"related_found"}`),
		json.RawMessage(`{"searched":"the registry","outcome":"none_found","related_ids":["work:other"]}`),
		json.RawMessage(`{"searched":"the registry","outcome":"related_found","related_ids":["work:other"]}`),
	}
	for _, definition := range []WorkflowDefinition{retained.Definition, head.Definition} {
		for _, raw := range corpus {
			err := validateWorkflowActionPayload(definition, "record_alignment", raw)
			invalid := err != nil
			if invalid != (string(raw) != `{"searched":"the registry","outcome":"related_found","related_ids":["work:other"]}`) {
				t.Errorf("record_alignment corpus verdict diverged on %s through %s v%d: %v", raw, definition.Ref, definition.Version, err)
			}
		}
	}
	if len(workflowActionCrossField("record_alignment").Combinations) != 2 {
		t.Fatal("record_alignment lost its engine cross-field declaration")
	}
}
