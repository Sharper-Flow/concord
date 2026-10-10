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

// TestRetainedDefinitionPayloadValidationResolvesFrozenHistoricalDecoder
// proves the S0 separation at the engine boundary: a retained pin's declared
// schema refs resolve through the frozen historical decoder document, current
// heads and recovery declarations resolve through the public authoring
// document, and the classified path accepts and refuses exactly what the
// frozen decoder accepts and refuses.
func TestRetainedDefinitionPayloadValidationResolvesFrozenHistoricalDecoder(t *testing.T) {
	retained := retainedDefinitionWithSchemaRef(t)
	if !workflowDefinitionIsHistorical(retained) {
		t.Fatal("a retained registry pin does not classify as historical")
	}
	if _, ok := BuiltinWorkflowRegistry().Lookup(retained.Ref, retained.Version); !ok {
		t.Fatal("the synthesized retained pin is not registered")
	}

	valid := json.RawMessage(`{"value":{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":[]}}`)
	invalid := json.RawMessage(`{"value":{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":"not-a-list"}}`)
	if err := validateWorkflowActionPayload(retained, "probe", valid); err != nil {
		t.Errorf("retained-pin validation refused a payload the frozen decoder accepts: %v", err)
	}
	if err := validateWorkflowActionPayload(retained, "probe", invalid); err == nil {
		t.Error("retained-pin validation accepted a payload the frozen decoder refuses")
	}
	// The declared schema resolves the field value itself, so the direct
	// decoder corpus is the field object without the payload wrapper.
	fieldValid := json.RawMessage(`{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":[]}`)
	fieldInvalid := json.RawMessage(`{"id":"design:record","question":"which owner","choice":"the fold owner","rationale":"one authority","rejected":"not-a-list"}`)
	if err := payloadschema.ValidateInDocument(payloadschema.HistoricalDocument(), "workflow_design_decision", fieldValid); err != nil {
		t.Errorf("frozen decoder disagrees with the classified engine path on the valid corpus: %v", err)
	}
	if err := payloadschema.ValidateInDocument(payloadschema.HistoricalDocument(), "workflow_design_decision", fieldInvalid); err == nil {
		t.Error("frozen decoder disagrees with the classified engine path on the invalid corpus")
	}

	// Current heads and the engine recovery declarations keep resolving
	// through the public authoring document.
	for _, definition := range BuiltinWorkflowDefinitions() {
		if workflowDefinitionIsHistorical(definition) {
			t.Fatalf("current head %s v%d classifies as historical", definition.Ref, definition.Version)
		}
	}
	for _, definition := range recoveryDefinitionWithSchemaRefs(t) {
		if workflowDefinitionIsHistorical(definition) {
			t.Fatalf("the engine recovery declaration %s classifies as historical", definition.ActionDefinitions[0].ID)
		}
	}

	// Every declared schema ref of every registered definition resolves in
	// its classified document.
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		document := workflowPayloadSchemaDocumentFor(definition)
		for _, action := range definition.ActionDefinitions {
			for _, field := range action.Payload.Fields {
				for _, ref := range []string{field.SchemaRef, field.ItemRef} {
					if ref == "" {
						continue
					}
					if !payloadschema.HasIn(document, ref) {
						t.Errorf("definition %s v%d action %s field %s declares schema %q, which its classified document does not carry", definition.Ref, definition.Version, action.ID, field.Name, ref)
					}
				}
			}
		}
	}
}

// TestRetainedPinClassificationCoversEveryRegisteredHistoryVersion pins the
// classification itself: exactly the registered (ref, version) entries below
// a family head are historical, and every current head is not.
func TestRetainedPinClassificationCoversEveryRegisteredHistoryVersion(t *testing.T) {
	current := map[string]bool{}
	for _, definition := range BuiltinWorkflowDefinitions() {
		current[definition.Ref+"/"+strconv.FormatInt(definition.Version, 10)] = true
	}
	historyCount := 0
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		key := definition.Ref + "/" + strconv.FormatInt(definition.Version, 10)
		if current[key] {
			if workflowDefinitionIsHistorical(definition) {
				t.Errorf("current head %s classifies as historical", key)
			}
			continue
		}
		historyCount++
		if !workflowDefinitionIsHistorical(definition) {
			t.Errorf("retained pin %s classifies as current", key)
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

// TestHistoricalEngineCrossFieldRulesStayVersionInvariant keeps the global
// engine cross-field rules outside the schema documents: a retained pin's
// payload answers to the same engine declaration a current pin answers to,
// so a refusal cannot depend on which document resolved the refs.
// record_alignment declares the combinations; implementation v14 first
// carried it, so a retained pin and the current head run the same corpus.
func TestHistoricalEngineCrossFieldRulesStayVersionInvariant(t *testing.T) {
	head, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 27)
	if !ok {
		t.Fatal("implementation v27 is not registered")
	}
	retained, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 14)
	if !ok {
		t.Fatal("implementation v14 is not registered")
	}
	if workflowDefinitionIsHistorical(head.Definition) || !workflowDefinitionIsHistorical(retained.Definition) {
		t.Fatal("the witness pins classify backwards")
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
