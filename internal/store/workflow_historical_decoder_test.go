package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
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

	// Every registered pin must resolve its declared refs for replay. Live
	// declarations are checked separately against the public document below.
	for _, definition := range builtinWorkflowDefinitionsWithHistory() {
		for _, action := range definition.ActionDefinitions {
			for _, field := range action.Payload.Fields {
				for _, ref := range []string{field.SchemaRef, field.ItemRef} {
					if ref == "" {
						continue
					}
					if !payloadschema.HasIn(workflowReplayPayloadSchemaDocument(), ref) {
						t.Errorf("definition %s v%d action %s field %s declares schema %q, which the replay frozen decoder does not carry", definition.Ref, definition.Version, action.ID, field.Name, ref)
					}
				}
			}
		}
	}
}

func TestRetiredDecisionDeclarationResolvesOnlyFrozenDecoder(t *testing.T) {
	definition, err := BuiltinWorkflowDefinitionForRef("workflow.architecture_spike")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(historicalDecisionFields())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflowActionPayload(definition.Definition, "record_decision", raw); err == nil {
		t.Error("retired decision record stayed authorable through the live document")
	}
	if err := validateWorkflowActionPayloadForReplay(definition.Definition, "record_decision", raw); err != nil {
		t.Fatalf("frozen decoder rejected the recorded decision shape: %v", err)
	}
}

// TestRetainedPinClassificationCoversEveryRegisteredHistoryVersion pins the
// retained-pin identity itself: exactly the registered (ref, version) entries
// below a family head are retained, and every current head is not.
func TestRetainedPinClassificationCoversEveryRegisteredHistoryVersion(t *testing.T) {
	current := map[string]bool{}
	for _, definition := range builtinWorkflowFrozenHeads() {
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

// TestCurrentAndRecoveryDeclarationsResolvePublicDocument keeps the
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
// public document admits the 2.0 packet identity, the frozen
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
	head, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", builtinWorkflowCurrentVersions["workflow.implementation"])
	if !ok {
		t.Fatal("the implementation head is not registered")
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

// frozenWorkerPacketDigest is the canonical-JSON digest of worker_packet as
// the frozen decoder first publishes it. The definition equals the v13.3.1
// and v13.4.0 released definitions except for description annotations, cut once
// before first publication. Any later rewrite of these bytes is a replay break
// the witness refuses before it can land.
const frozenWorkerPacketDigest = "sha256:33ba1533be5063aca5530c88af9d3eeef20b54b1f2260c683753a64e053e7109"

// TestFrozenWorkerPacketBaselineStaysByteExact pins the frozen 1.0/1.1
// worker_packet baseline to the decoder's first-published bytes: it grows
// only by addition through the owning freeze, and the versioned additions
// must not have moved one byte of the frozen baseline. It also pins the
// versioned identities: the frozen worker_packet2, work_context_view2, and
// work_context_domain_group2 the freeze carried equal the live published
// definitions, so the versioned identity replays exactly the shape it
// records.
func TestFrozenWorkerPacketBaselineStaysByteExact(t *testing.T) {
	frozen := deepCopyJSONDocument(t, payloadschema.HistoricalDocument())
	defs := frozen["$defs"].(map[string]any)
	packet, ok := defs["worker_packet"].(map[string]any)
	if !ok {
		t.Fatal("the frozen decoder carries no worker_packet definition")
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	var canonical map[string]any
	if json.Unmarshal(raw, &canonical) != nil {
		t.Fatal(err)
	}
	ordered, err := json.Marshal(canonical)
	if err != nil {
		t.Fatal(err)
	}
	// Canonical JSON sorts keys, so the digest is stable across projections.
	sum := sha256.Sum256(ordered)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if digest != frozenWorkerPacketDigest {
		t.Fatalf("the frozen worker_packet baseline drifted: computed %s, frozen %s", digest, frozenWorkerPacketDigest)
	}
	// The versioned identities the freeze carried are the published shapes:
	// the packet, the view it anchors, and the nav-free group the view
	// resolves, so the versioned identity replays exactly the shape it
	// records, transitively.
	liveDefs := deepCopyJSONDocument(t, payloadschema.Document())["$defs"].(map[string]any)
	for _, name := range []string{"worker_packet2", "work_context_view2", "work_context_domain_group2", "worker_packet3", "work_context_view3", "work_context_domain_group3"} {
		versioned, ok := defs[name].(map[string]any)
		if !ok {
			t.Fatalf("the frozen decoder carries no versioned %s definition", name)
		}
		live, ok := liveDefs[name]
		if !ok {
			t.Fatalf("the public authoring document carries no versioned %s definition", name)
		}
		if !reflect.DeepEqual(versioned, live) {
			t.Fatalf("the frozen %s definition drifted from the published identity", name)
		}
	}
	if ref := defs["worker_packet2"].(map[string]any)["properties"].(map[string]any)["inputs"].(map[string]any)["properties"].(map[string]any)["work_context"].(map[string]any)["$ref"]; ref != "#/$defs/work_context_view2" {
		t.Fatalf("the frozen worker_packet2 anchors %q, want the versioned work_context_view2", ref)
	}
	if ref := defs["worker_packet3"].(map[string]any)["properties"].(map[string]any)["inputs"].(map[string]any)["properties"].(map[string]any)["work_context"].(map[string]any)["$ref"]; ref != "#/$defs/work_context_view3" {
		t.Fatalf("the frozen worker_packet3 anchors %q, want the research-enabled work_context_view3", ref)
	}
	// The versioned identity carries no retired conditional: its
	// schema_version is the 2.0 identity alone, so the 1.0 worker_job branch
	// the frozen worker_packet keeps has nothing to select here.
	if _, dead := defs["worker_packet2"].(map[string]any)["allOf"]; dead {
		t.Fatal("the versioned worker_packet2 carries the retired 1.0 conditional")
	}
}

// TestDerivedFailureHeadsReplayVersionedPacket is the replay witness for the
// versioned packet2 closure: the derived-failure heads (implementation v29,
// break_fix v26) declare the versioned packet2 decoder, so a recorded
// dispatch_worker payload carrying the packet2.0 identity replays through
// the frozen decoder's worker_packet2 definition; the retained v28/v25 pins
// keep the released worker_packet declaration, so their 1.1 history replays
// through the byte-exact frozen baseline unchanged, and each pin refuses the
// other generation's identity on its own declaration.
func TestDerivedFailureHeadsReplayVersionedPacket(t *testing.T) {
	head, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 29)
	if !ok {
		t.Fatal("implementation v29 is not registered")
	}
	retained, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 28)
	if !ok {
		t.Fatal("implementation v28 is not registered")
	}
	if head.Definition.Version != 29 || !pinIsBelowHead(t, retained.Definition) {
		t.Fatal("the witness pins are not one derived-failure head and one retained pin")
	}
	headRef, retainedRef := dispatchPacketSchemaRef(t, head.Definition), dispatchPacketSchemaRef(t, retained.Definition)
	if headRef != "worker_packet2" {
		t.Fatalf("implementation v29 dispatch packet schema ref = %q, want the versioned worker_packet2", headRef)
	}
	if retainedRef != "worker_packet" {
		t.Fatalf("implementation v28 dispatch packet schema ref = %q, want the released worker_packet", retainedRef)
	}
	breakFixHead, ok := BuiltinWorkflowRegistry().Lookup("workflow.break_fix", 26)
	if !ok {
		t.Fatal("break_fix v26 is not registered")
	}
	if dispatchPacketSchemaRef(t, breakFixHead.Definition) != "worker_packet2" {
		t.Fatal("break_fix v26 dispatch packet schema ref is not the versioned worker_packet2")
	}

	modern := json.RawMessage(`{"attempt_id":"attempt-witness","worker_packet":` + string(discriminatingWorkerPacket("2.0")) + `}`)
	historic := json.RawMessage(`{"attempt_id":"attempt-witness","worker_packet":` + string(discriminatingWorkerPacket("1.1")) + `}`)
	if err := validateWorkflowActionPayloadForReplay(head.Definition, "dispatch_worker", modern); err != nil {
		t.Errorf("replay refused the recorded packet2.0 history the derived-failure head declared: %v", err)
	}
	if err := validateWorkflowActionPayloadForReplay(head.Definition, "dispatch_worker", historic); err == nil {
		t.Error("replay admitted a retired 1.1 packet identity the versioned worker_packet2 declaration never accepted")
	}
	if err := validateWorkflowActionPayloadForReplay(retained.Definition, "dispatch_worker", historic); err != nil {
		t.Errorf("replay refused the recorded 1.1 history the retained v28 pin declared: %v", err)
	}
	if err := validateWorkflowActionPayloadForReplay(retained.Definition, "dispatch_worker", modern); err == nil {
		t.Error("replay admitted the packet2.0 identity through the released worker_packet declaration the frozen baseline never froze")
	}
	// The live role answers to the published document for every pin: the
	// derived-failure head publishes the versioned identity, and the
	// released surface keeps the 2.0-only worker_packet gate it shipped.
	if err := validateWorkflowActionPayload(head.Definition, "dispatch_worker", modern); err != nil {
		t.Errorf("live validation refused the current packet2.0 packet on the derived-failure head: %v", err)
	}
	if err := validateWorkflowActionPayload(retained.Definition, "dispatch_worker", modern); err != nil {
		t.Errorf("live validation refused the published 2.0 packet on the retained v28 pin: %v", err)
	}
}

// dispatchPacketSchemaRef reads the dispatch_worker packet field's declared
// schema identity off one definition.
func dispatchPacketSchemaRef(t *testing.T, definition WorkflowDefinition) string {
	t.Helper()
	for _, action := range definition.ActionDefinitions {
		if action.ID != "dispatch_worker" {
			continue
		}
		for _, field := range action.Payload.Fields {
			if field.Name == "worker_packet" {
				return field.SchemaRef
			}
		}
	}
	t.Fatal("the definition declares no dispatch_worker worker_packet field")
	return ""
}

// pinnedDispatchPacketSchemaRef names the packet schema the work's pinned
// definition declares for its dispatch_worker worker_packet field.
func pinnedDispatchPacketSchemaRef(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var ref string
	var version int64
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_version FROM workflow_instances WHERE work_id=?`, workID).Scan(&ref, &version); err != nil {
		t.Fatalf("read pinned definition for %s: %v", workID, err)
	}
	registered, ok := BuiltinWorkflowRegistry().Lookup(ref, version)
	if !ok {
		t.Fatalf("pinned definition %s@%d is not registered", ref, version)
	}
	return dispatchPacketSchemaRef(t, registered.Definition)
}

// fullWorkContextPacket2 builds one packet2 carrying a fully populated
// work-context view — readings, findings, and the domain groups the versioned
// view resolves — so the nested closure, not the packet root alone, decides
// the verdict. navigationGroup carries the retired repository navigation
// cards; navFreeGroup carries exactly the current group shape.
func fullWorkContextPacket2(schemaVersion string, group map[string]any) json.RawMessage {
	view := map[string]any{
		"source_event_frontier": 7,
		"required_reading": []map[string]any{{
			"domain_id": "registry:root",
			"reason":    "the root domain governs every finding in this view",
			"source": map[string]any{
				"kind": "repository_file", "project_id": "project-fixture",
				"path": "docs/root.md", "commit_oid": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		}},
		"findings": []map[string]any{{
			"finding_id": "finding:7:0", "kind": "observation",
			"statement":     "the packet2 work-context closure carries the current view",
			"subject_ref":   "context:packet2-closure",
			"evidence_refs": []string{"evidence:packet2-closure"},
			"domain_id":     "registry:root",
			"origin":        "declaration", "status": "reported",
			"source_event_id": "event:packet2-closure", "source_event_seq": 7, "ordinal": 0,
		}},
		"domain_groups": []map[string]any{group},
	}
	packet := map[string]any{
		"schema_version": schemaVersion, "attempt_id": "attempt-packet2-closure",
		"lane_id": "implement", "lane_version": 1,
		"lane_digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"work_id":     "work-packet2-closure", "step_id": "execution",
		"inputs": map[string]any{
			"task": "the bounded packet2 closure task",
			"binding": map[string]any{
				"objective_source": "contract_premise", "work_version": 1,
				"contract_version": 1, "assigned_result": "changed_files",
			},
			"report_protocol": "concord-worker-result-v1",
			"work_context":    view,
		},
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		panic(err)
	}
	return raw
}

// TestPacket2WorkContextClosureParityAcrossLiveAndReplay proves the
// versioned packet2 closure preserves value and refusal parity transitively:
// a full representative packet2 whose work-context view carries the current
// nav-free domain group is admitted by the live worker_packet2 and replays
// through the frozen worker_packet2; the same packet whose group carries the
// retired repository navigation cards is refused by both, with the refusal
// naming the retired member; and the frozen 1.0/1.1 worker_packet baseline
// still decodes a navigation-bearing group, so the released histories keep
// their own decoder unchanged. A root-and-version-only witness cannot see
// any of this; the nested shape is the witness.
func TestPacket2WorkContextClosureParityAcrossLiveAndReplay(t *testing.T) {
	head, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 29)
	if !ok {
		t.Fatal("implementation v29 is not registered")
	}
	retained, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 28)
	if !ok {
		t.Fatal("implementation v28 is not registered")
	}
	if !pinIsBelowHead(t, retained.Definition) {
		t.Fatal("the baseline witness pin is not a retained pin")
	}
	navFreeGroup := map[string]any{
		"domain_id":                 "registry:root",
		"required_reading_ordinals": []int{0},
		"finding_ids":               []string{"finding:7:0"},
	}
	navigationGroup := map[string]any{
		"domain_id":                 "registry:root",
		"required_reading_ordinals": []int{0},
		"finding_ids":               []string{"finding:7:0"},
		"domain_cards": []map[string]any{{
			"kind": "repository_file", "project_id": "project-fixture",
			"path": "docs/root.md", "commit_oid": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
	}
	wrap := func(packet json.RawMessage) json.RawMessage {
		return json.RawMessage(`{"attempt_id":"attempt-packet2-closure","worker_packet":` + string(packet) + `}`)
	}

	current := wrap(fullWorkContextPacket2("2.0", navFreeGroup))
	if err := validateWorkflowActionPayload(head.Definition, "dispatch_worker", current); err != nil {
		t.Errorf("live worker_packet2 refused the current nav-free work-context closure: %v", err)
	}
	if err := validateWorkflowActionPayloadForReplay(head.Definition, "dispatch_worker", current); err != nil {
		t.Errorf("replay worker_packet2 refused the current nav-free work-context closure: %v", err)
	}

	retired := wrap(fullWorkContextPacket2("2.0", navigationGroup))
	if err := validateWorkflowActionPayload(head.Definition, "dispatch_worker", retired); err == nil {
		t.Error("live worker_packet2 admitted the retired repository navigation cards")
	} else if !strings.Contains(err.Error(), "domain_cards") {
		t.Errorf("the retired-navigation refusal does not name domain_cards: %v", err)
	}
	if err := validateWorkflowActionPayloadForReplay(head.Definition, "dispatch_worker", retired); err == nil {
		t.Error("replay worker_packet2 admitted the retired repository navigation cards")
	} else if !strings.Contains(err.Error(), "domain_cards") {
		t.Errorf("the retired-navigation replay refusal does not name domain_cards: %v", err)
	}

	// The released baseline keeps its own decoder: a 1.1 packet whose view
	// carries the navigation-bearing group replays through the frozen
	// worker_packet exactly as its recorded writes answered, and the live
	// retired gate above changed nothing about it.
	historicRetained := wrap(fullWorkContextPacket2("1.1", navigationGroup))
	if err := validateWorkflowActionPayloadForReplay(retained.Definition, "dispatch_worker", historicRetained); err != nil {
		t.Errorf("replay worker_packet refused the released navigation-bearing history: %v", err)
	}
}

// researchContextDeclarationPayload builds a record_work_context payload
// with the given research selection entries beside an otherwise empty
// declaration. plain skips the member entirely: the retained heads' own
// recorded shape never carried it, and their closed payloads refuse the
// member name itself, whatever its value.
func researchContextDeclarationPayload(selections string) json.RawMessage {
	if selections == "" {
		return json.RawMessage(`{"required_reading":[],"finding_refs":[],"context_findings":[]}`)
	}
	return json.RawMessage(`{"required_reading":[],"finding_refs":[],"context_findings":[],"research_finding_refs":` + selections + `}`)
}

// fullResearchContextPacket3 builds one packet3 whose work-context view
// carries a resolved research entry and a group whose id namespace mixes the
// generic and research view ids, so the nested research closure — not the
// packet root alone — decides the verdict.
func fullResearchContextPacket3() json.RawMessage {
	view := map[string]any{
		"source_event_frontier": 9,
		"required_reading":      []map[string]any{},
		"findings":              []map[string]any{},
		"domain_groups": []map[string]any{{
			"domain_id":                 "registry:root",
			"required_reading_ordinals": []int{},
			"finding_ids":               []string{"research:9:0"},
		}},
		"research_findings": []map[string]any{{
			"selection": map[string]any{
				"pack_id": "pack-packet3-closure", "revision": 1,
				"finding_id": "f-packet3-closure", "domain_id": "registry:root",
			},
			"view_id":             "research:9:0",
			"selection_event_id":  "event:packet3-closure",
			"selection_event_seq": 9,
			"ordinal":             0,
			"required":            true,
			"state":               "resolved",
			"finding": map[string]any{
				"pack_id": "pack-packet3-closure", "revision": 1,
				"finding_id": "f-packet3-closure", "kind": "observation",
				"statement":  "the packet3 research closure carries the selected finding",
				"confidence": "high", "freshness": "current", "status": "active",
				"scopes": map[string]any{"mode": "home"},
			},
			"sources": []map[string]any{},
		}},
	}
	packet := map[string]any{
		"schema_version": "2.0", "attempt_id": "attempt-packet3-closure",
		"lane_id": "implement", "lane_version": 1,
		"lane_digest": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"work_id":     "work-packet3-closure", "step_id": "execution",
		"inputs": map[string]any{
			"task": "the bounded packet3 closure task",
			"binding": map[string]any{
				"objective_source": "contract_premise", "work_version": 1,
				"contract_version": 1, "assigned_result": "changed_files",
			},
			"report_protocol": "concord-worker-result-v1",
			"work_context":    view,
		},
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		panic(err)
	}
	return raw
}

// TestResearchContextHeadsDeclareSelectionAndPacket3Closure is the S4
// publication witness: the promoted heads (implementation v32, break_fix v29,
// research v19) append the optional research_finding_refs action input to
// record_work_context and anchor dispatch_worker's packet at the frozen
// worker_packet3 closure, while the retained v31/v28/v18 pins keep the
// packet2 closure and structurally refuse the selection field — the same
// recorded payload replays under its own pin and refuses under the research
// closure, and a research-bearing packet is admitted only through packet3.
func TestResearchContextHeadsDeclareSelectionAndPacket3Closure(t *testing.T) {
	type family struct {
		ref             string
		headVersion     int64
		retainedVersion int64
	}
	for _, fam := range []family{
		{"workflow.implementation", 32, 31},
		{"workflow.break_fix", 29, 28},
		{"workflow.research", 19, 18},
	} {
		head, ok := BuiltinWorkflowRegistry().Lookup(fam.ref, fam.headVersion)
		if !ok {
			t.Fatalf("%s v%d is not registered", fam.ref, fam.headVersion)
		}
		retained, ok := BuiltinWorkflowRegistry().Lookup(fam.ref, fam.retainedVersion)
		if !ok {
			t.Fatalf("%s v%d is not registered", fam.ref, fam.retainedVersion)
		}
		if got := dispatchPacketSchemaRef(t, head.Definition); got != "worker_packet3" {
			t.Fatalf("%s v%d dispatch packet schema ref = %q, want the research-context worker_packet3", fam.ref, fam.headVersion, got)
		}
		if got := dispatchPacketSchemaRef(t, retained.Definition); got != "worker_packet2" {
			t.Fatalf("%s v%d dispatch packet schema ref = %q, want the retained worker_packet2", fam.ref, fam.retainedVersion, got)
		}
		selectionField := func(definition WorkflowDefinition) *WorkflowPayloadField {
			for _, action := range definition.ActionDefinitions {
				if action.ID != "record_work_context" {
					continue
				}
				for index := range action.Payload.Fields {
					if action.Payload.Fields[index].Name == "research_finding_refs" {
						return &action.Payload.Fields[index]
					}
				}
			}
			return nil
		}
		declared := selectionField(head.Definition)
		if declared == nil {
			t.Fatalf("%s v%d record_work_context declares no research_finding_refs field", fam.ref, fam.headVersion)
		}
		if declared.Required || declared.ValueType != PayloadArray || declared.ItemRef != "work_context_research_selection" {
			t.Fatalf("%s v%d research_finding_refs declaration = %+v, want an optional array of work_context_research_selection", fam.ref, fam.headVersion, declared)
		}
		if declared.MinItems == nil || *declared.MinItems != 0 || declared.MaxItems == nil || *declared.MaxItems != WorkContextFindingRefsMax {
			t.Fatalf("%s v%d research_finding_refs bounds = %+v, want 0..%d", fam.ref, fam.headVersion, declared, WorkContextFindingRefsMax)
		}
		if selectionField(retained.Definition) != nil {
			t.Fatalf("%s v%d record_work_context must keep its closed payload without the selection field", fam.ref, fam.retainedVersion)
		}
		// Replay parity: the retained pin replays its own recorded
		// declaration shape and refuses the selection the new heads author;
		// the promoted head replays both the plain and the selection
		// declaration it ships.
		plain := researchContextDeclarationPayload("")
		selected := researchContextDeclarationPayload(`[{"pack_id":"pack-packet3-closure","revision":1,"finding_id":"f-packet3-closure","domain_id":"registry:root"}]`)
		if err := validateWorkflowActionPayloadForReplay(retained.Definition, "record_work_context", plain); err != nil {
			t.Errorf("%s v%d replay refused its own plain declaration: %v", fam.ref, fam.retainedVersion, err)
		}
		if err := validateWorkflowActionPayloadForReplay(retained.Definition, "record_work_context", selected); err == nil {
			t.Errorf("%s v%d replay admitted the research selection its closed payload never declared", fam.ref, fam.retainedVersion)
		}
		if err := validateWorkflowActionPayloadForReplay(head.Definition, "record_work_context", selected); err != nil {
			t.Errorf("%s v%d replay refused the research selection it declares: %v", fam.ref, fam.headVersion, err)
		}
		// Transport parity: a research-bearing packet is admitted live and on
		// replay only through the packet3 closure; the retained packet2 pin
		// refuses it structurally.
		researchPacket := json.RawMessage(`{"attempt_id":"attempt-packet3-closure","worker_packet":` + string(fullResearchContextPacket3()) + `}`)
		if err := validateWorkflowActionPayloadForReplay(head.Definition, "dispatch_worker", researchPacket); err != nil {
			t.Errorf("%s v%d replay refused the research-bearing packet3: %v", fam.ref, fam.headVersion, err)
		}
		if err := validateWorkflowActionPayloadForReplay(retained.Definition, "dispatch_worker", researchPacket); err == nil {
			t.Errorf("%s v%d replay admitted a research-bearing packet through the frozen packet2 closure", fam.ref, fam.retainedVersion)
		}
	}
	// The frozen closure resolves transitively: the new view anchors the
	// research-enabled group, and the old frozen group keeps the generic-only
	// id pattern. The byte-equality loop above proves the frozen identities
	// equal the published shapes, so the live gates decide the same verdicts.
	frozenDefs := deepCopyJSONDocument(t, payloadschema.HistoricalDocument())["$defs"].(map[string]any)
	group3, ok := frozenDefs["work_context_domain_group3"].(map[string]any)
	if !ok {
		t.Fatal("the frozen decoder carries no work_context_domain_group3 definition")
	}
	pattern, _ := group3["properties"].(map[string]any)["finding_ids"].(map[string]any)["items"].(map[string]any)["pattern"].(string)
	if pattern != "^(finding|research):[0-9]+:[0-9]+$" {
		t.Fatalf("the frozen group3 id pattern = %q, want the closed finding|research namespace", pattern)
	}
	for _, name := range []string{"work_context_domain_group", "work_context_domain_group2"} {
		old, ok := frozenDefs[name].(map[string]any)
		if !ok {
			t.Fatalf("the frozen decoder lost %s", name)
		}
		oldPattern, _ := old["properties"].(map[string]any)["finding_ids"].(map[string]any)["items"].(map[string]any)["pattern"].(string)
		if oldPattern != "^finding:[0-9]+:[0-9]+$" {
			t.Fatalf("the frozen %s id pattern = %q, want the unchanged generic-only namespace", name, oldPattern)
		}
	}
}
