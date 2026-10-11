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

// releasedFrozenWorkerPacketDigest is the canonical-JSON digest of the frozen
// historical decoder's worker_packet definition as released. The definition
// is the only decoder for every released 1.0/1.1 recorded packet, so any
// rewrite of its bytes is a replay break the witness refuses before it can
// land.
const releasedFrozenWorkerPacketDigest = "sha256:f2efb64652898438478e8f2093e97dcd76add600f23f022a345d4cc1861fccf9"

// TestFrozenWorkerPacketBaselineStaysByteExact pins the frozen 1.0/1.1
// worker_packet baseline to its released bytes: the historical decoder grows
// only by addition through the owning freeze, and the versioned additions
// must not have moved one byte of the released baseline. It also pins the
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
	if digest != releasedFrozenWorkerPacketDigest {
		t.Fatalf("the frozen worker_packet baseline drifted: computed %s, released %s", digest, releasedFrozenWorkerPacketDigest)
	}
	// The versioned identities the freeze carried are the published shapes:
	// the packet, the view it anchors, and the nav-free group the view
	// resolves, so the versioned identity replays exactly the shape it
	// records, transitively.
	liveDefs := deepCopyJSONDocument(t, payloadschema.Document())["$defs"].(map[string]any)
	for _, name := range []string{"worker_packet2", "work_context_view2", "work_context_domain_group2"} {
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
	// The versioned identity carries no retired conditional: its
	// schema_version is the 2.0 identity alone, so the 1.0 worker_job branch
	// the released worker_packet keeps has nothing to select here.
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
