package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// Independent regression probes invoke the production host publisher
// (adapter/opencode/concord.ts publishedRequestSchema) and the host validator
// (adapter/opencode/core-runtime.ts validateAgainstSchema) through Bun, exactly as
// the host runtime does, and the core boundary is exercised through the
// owning core validators with the live builtin registry's own declarations.
//
// The Unicode probe also covers workflow reference list items, which the
// store admits through ValidReference by UTF-8 bytes.

// outsideRepairProbeScript is the bridge the tests drive: it imports the
// production host publisher and validator by absolute path, resolves the
// published work_transition fields node for one action from whichever shape
// the publisher emits (per-action closed branches, the compact factored
// document with local references and a grouped action union, a
// discriminator-shared union, or allOf if/then conditions), and validates
// probe values against the resolved published nodes with the host's own
// validator. Nothing here rebuilds or approximates the publication.
func outsideRepairProbeScript(repoRoot string) string {
	return fmt.Sprintf(`import { publishedRequestSchema } from %q;
import { payloadSchemas } from %q;
import { validateAgainstSchema } from %q;
const schema = publishedRequestSchema("concord_work_transition");
const resolveBranch = (b) => (b && typeof b.$ref === "string") ? schema.$defs?.[b.$ref.replace("#/$defs/", "")] : b;
const operationBranches = (schema.oneOf ?? []).map(resolveBranch).filter((b) => typeof b?.properties?.operation?.const === "string");
const branches = (schema.oneOf ?? []).map(resolveBranch).filter((b) => b?.properties?.operation?.const === "workflow_action");
if (branches.length === 0) throw new Error("published concord_work_transition names no workflow_action operation branch");
const fieldsFor = (action) => {
  for (const branch of operationBranches) {
    if (branch.properties.operation.const === action && branch.properties.input?.properties) return branch.properties.input;
  }
  for (const branch of branches) {
    const input = branch?.properties?.input;
    if (input?.properties?.action_id?.const === action && input?.properties?.fields) return input.properties.fields;
  }
  for (const branch of branches) {
    const input = branch?.properties?.input;
    const discriminator = input?.properties?.action_id;
    if (Array.isArray(discriminator?.enum) && discriminator.enum.includes(action) && input?.properties?.fields) return input.properties.fields;
  }
  for (const branch of branches) {
    const input = branch?.properties?.input;
    for (const member of input?.oneOf ?? []) {
      const variant = resolveBranch(member);
      if (variant?.properties?.action_id?.const === action && variant?.properties?.fields) return variant.properties.fields;
    }
  }
  for (const branch of branches) {
    const input = branch?.properties?.input;
    for (const member of input?.allOf ?? []) {
      const trigger = member?.if?.properties?.action_id;
      if ((trigger?.const === action || (Array.isArray(trigger?.enum) && trigger.enum.includes(action))) && member?.then?.properties?.fields) return member.then.properties.fields;
    }
  }
  if (branches.length === 1) return branches[0]?.properties?.input?.properties?.fields ?? null;
  return null;
};
const propertyNode = (node, name) => {
  if (!node || typeof node !== "object") return null;
  if (typeof node.$ref === "string") {
    node = schema.$defs?.[node.$ref.replace("#/$defs/", "")];
    if (!node || typeof node !== "object") return null;
  }
  if (node.properties && node.properties[name]) return node.properties[name];
  for (const keyword of ["oneOf", "anyOf"]) {
    for (const branch of node[keyword] ?? []) {
      const found = propertyNode(resolveBranch(branch), name);
      if (found) return found;
    }
  }
  for (const member of node.allOf ?? []) {
    const found = propertyNode(member?.then ?? member, name);
    if (found) return found;
  }
  return null;
};
// validateNode validates a resolved published node. The compact publication
// interning leaves local references inside the node; the document's own
// $defs table travels with it so the host validator resolves them. On a
// fully inlined publication the added table is empty and inert.
const validateNode = (node, value, failures) => validateAgainstSchema({ ...node, $defs: { ...(node.$defs ?? {}), ...(schema.$defs ?? {}) } }, value, failures);
const probes = await new Response(Bun.stdin).json();
const results = [];
for (const probe of probes) {
  const failures = [];
  const fields = fieldsFor(probe.action);
  let admit = false;
  if (probe.mode === "definition") {
    const document = { $defs: payloadSchemas, $ref: "#/$defs/" + probe.definition };
    admit = validateAgainstSchema(document, probe.value, failures);
  } else if (!fields) {
    failures.push("published schema resolves no fields node for " + probe.action);
  } else if (probe.mode === "field") {
    const node = propertyNode(fields, probe.field);
    if (!node) {
      failures.push("published schema resolves no " + probe.field + " property for " + probe.action);
    } else {
      admit = validateNode(node, probe.value, failures);
    }
  } else {
    admit = validateNode(fields, probe.fields, failures);
  }
  results.push({ id: probe.id, admit: !!admit, failures: failures.slice(0, 4) });
}
console.log(JSON.stringify({ results }));
`, filepath.Join(repoRoot, "adapter", "opencode", "concord.ts"), filepath.Join(repoRoot, "adapter", "opencode", "generated-contracts.ts"), filepath.Join(repoRoot, "adapter", "opencode", "core-runtime.ts"))
}

// outsideRepairPublication is one published-boundary verdict: whether the
// host validator admitted the probe, and the failures it reported when it
// refused.
type outsideRepairPublication struct {
	Admit    bool
	Failures []string
}

// outsideRepairPublishedVerdicts invokes the production host publisher and
// validator through Bun once, feeding every probe on stdin, and returns the
// verdicts keyed by probe id. The test skips when Bun is unavailable, the
// same admission the release-pairs session test takes.
func outsideRepairPublishedVerdicts(t *testing.T, probes []map[string]any) map[string]outsideRepairPublication {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skipf("bun is unavailable for the host publication probe: %v", err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "adapter", "opencode", "concord.ts")); err != nil {
		t.Fatalf("the worktree publishes no adapter at %s: %v", repoRoot, err)
	}
	scriptPath := filepath.Join(t.TempDir(), "outside_repair_publication_probe.ts")
	if err := os.WriteFile(scriptPath, []byte(outsideRepairProbeScript(repoRoot)), 0o644); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(probes)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, bun, scriptPath)
	command.Dir = repoRoot
	command.Stdin = strings.NewReader(string(encoded))
	var stdout, stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("the host publication probe failed: %v\nstderr: %s", err, stderr.String())
	}
	var report struct {
		Results []struct {
			ID       string   `json:"id"`
			Admit    bool     `json:"admit"`
			Failures []string `json:"failures"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &report); err != nil {
		t.Fatalf("the host publication probe returned no verdict document: %v\nstdout: %s", err, stdout.String())
	}
	verdicts := make(map[string]outsideRepairPublication, len(report.Results))
	for _, result := range report.Results {
		verdicts[result.ID] = outsideRepairPublication{Admit: result.Admit, Failures: result.Failures}
	}
	if len(verdicts) != len(probes) {
		t.Fatalf("the host publication probe returned %d verdicts for %d probes", len(verdicts), len(probes))
	}
	return verdicts
}

// outsideRepairActionPayload returns the live builtin registry's payload
// declaration for one action of the implementation family — the same
// declaration the published variants generate from, read from the registry
// itself so the probe judges the shape each tree actually enforces.
func outsideRepairActionPayload(t *testing.T, actionID string) (WorkflowPayloadDefinition, WorkflowDefinition) {
	t.Helper()
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range registered.Definition.ActionDefinitions {
		if action.ID == actionID {
			return action.Payload, registered.Definition
		}
	}
	t.Fatalf("the builtin implementation definition declares no %s action", actionID)
	return WorkflowPayloadDefinition{}, WorkflowDefinition{}
}

// outsideRepairActionField returns one declared payload field of an action.
func outsideRepairActionField(t *testing.T, actionID, fieldName string) WorkflowPayloadField {
	t.Helper()
	payload, _ := outsideRepairActionPayload(t, actionID)
	for _, field := range payload.Fields {
		if field.Name == fieldName {
			return field
		}
	}
	t.Fatalf("the %s payload declaration carries no %s field", actionID, fieldName)
	return WorkflowPayloadField{}
}

func outsideRepairRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// TestOutsideRepairUnicodeParity pins the approved UTF-8 byte representation
// of workflow string bounds at both admission boundaries, on boundary
// fixtures: record_alignment.searched declares a two-unit floor and a
// 4096-unit ceiling on every tree under comparison, so the two-byte
// one-code-point "é" and the 4096-code-point 8192-byte all-"é" string sit
// exactly on the divergent bounds. Core validation
// (validateWorkflowPayloadValue) and the production host publication must
// both admit the two-byte string at its two-byte floor and both refuse the
// 8192-byte string over its 4096-byte ceiling. At the base commit all four
// behavioral assertions fail: the core's rune counting refuses "é" and admits
// the 8192-byte string, and the base publication's code-point-equal bounds
// refuse and admit the same two strings with it.
func TestOutsideRepairUnicodeParity(t *testing.T) {
	t.Parallel()
	twoByteOneRune := "é"
	if utf8.RuneCountInString(twoByteOneRune) != 1 || len(twoByteOneRune) != 2 {
		t.Fatalf("fixture %q is not one code point of two bytes", twoByteOneRune)
	}
	maxBoundMultibyte := strings.Repeat("é", 4096)
	if len(maxBoundMultibyte) != 8192 || utf8.RuneCountInString(maxBoundMultibyte) != 4096 {
		t.Fatal("fixture is not 4096 code points of 8192 bytes")
	}
	searched := outsideRepairActionField(t, "record_alignment", "searched")
	if searched.MinLength == nil || *searched.MinLength != 2 || searched.MaxLength == nil || *searched.MaxLength != 4096 {
		t.Fatalf("the declared searched bounds are min=%v max=%v; the probe needs the declared floor 2 and ceiling 4096", searched.MinLength, searched.MaxLength)
	}
	fixtures := []struct {
		id            string
		value         string
		wantAdmitted  bool
		coreFailure   string
		publishedSide string
	}{
		{
			id: "min-bound-two-bytes-one-rune", value: twoByteOneRune, wantAdmitted: true,
			coreFailure:   "core validation refuses the two-byte one-code-point string its own two-byte floor admits",
			publishedSide: "refused a core-admitted two-byte string at the two-byte floor",
		},
		{
			id: "min-control-two-ascii", value: "vv", wantAdmitted: true,
			coreFailure:   "core validation refuses a two-byte ASCII string at the floor",
			publishedSide: "refused the two-byte ASCII control at the floor",
		},
		{
			id: "max-4096-code-points-8192-bytes", value: maxBoundMultibyte, wantAdmitted: false,
			coreFailure:   "core validation admits a string whose 8192 UTF-8 bytes exceed the declared 4096-byte ceiling",
			publishedSide: "admitted an 8192-byte string over the 4096-byte ceiling",
		},
		{
			id: "max-control-4096-ascii", value: strings.Repeat("v", 4096), wantAdmitted: true,
			coreFailure:   "core validation refuses the 4096-byte ASCII string its own ceiling admits",
			publishedSide: "refused the 4096-byte ASCII control at the ceiling",
		},
	}
	probes := make([]map[string]any, 0, len(fixtures))
	for _, fixture := range fixtures {
		probes = append(probes, map[string]any{"id": fixture.id, "mode": "field", "action": "record_alignment", "field": "searched", "value": fixture.value})
	}
	// The same unit fault at list-item level: approve_contract
	// route_conventions items are workflow references, which the store
	// admits through ValidReference at 2 to 128 UTF-8 bytes.
	routeConventions := outsideRepairActionField(t, "approve_contract", "route_conventions")
	itemFixtures := []struct {
		id           string
		value        string
		wantAdmitted bool
	}{
		{id: "reference-two-bytes-one-rune", value: twoByteOneRune, wantAdmitted: true},
		{id: "reference-64-runes-128-bytes", value: strings.Repeat("é", 64), wantAdmitted: true},
		{id: "reference-65-runes-130-bytes", value: strings.Repeat("é", 65), wantAdmitted: false},
	}
	for _, fixture := range itemFixtures {
		probes = append(probes, map[string]any{"id": fixture.id, "mode": "field", "action": "approve_contract", "field": "route_conventions", "value": []any{fixture.value}})
	}
	published := outsideRepairPublishedVerdicts(t, probes)
	for _, fixture := range fixtures {
		if coreAdmits := validateWorkflowPayloadValue(searched, outsideRepairRaw(t, fixture.value)); coreAdmits != fixture.wantAdmitted {
			t.Errorf("unicode fixture %s: %s", fixture.id, fixture.coreFailure)
		}
		verdict, ok := published[fixture.id]
		if !ok {
			t.Fatalf("the host publication probe returned no verdict for %s", fixture.id)
		}
		if verdict.Admit != fixture.wantAdmitted {
			t.Errorf("unicode fixture %s: the published searched bounds %s; failures: %v", fixture.id, fixture.publishedSide, verdict.Failures)
		}
	}
	for _, fixture := range itemFixtures {
		if coreAdmits := validateWorkflowPayloadValue(routeConventions, outsideRepairRaw(t, []any{fixture.value})); coreAdmits != fixture.wantAdmitted {
			t.Errorf("reference item fixture %s: core validation admits=%v, want %v", fixture.id, coreAdmits, fixture.wantAdmitted)
		}
		verdict, ok := published[fixture.id]
		if !ok {
			t.Fatalf("the host publication probe returned no verdict for %s", fixture.id)
		}
		if verdict.Admit != fixture.wantAdmitted {
			t.Errorf("reference item fixture %s: the published route_conventions items admit=%v, want %v; failures: %v", fixture.id, verdict.Admit, fixture.wantAdmitted, verdict.Failures)
		}
	}
}

// TestOutsideRepairOutcomeKindParity pins the payload-only equality between
// an outcome predicate's declared outcome_kind and its outcome_payload kind,
// on the wire forms that carry it: one outcome_predicates item on
// approve_contract, the canonical outcome_predicates array on
// supersede_contract, and the retained-pin outcome pair the engine recovery
// declaration still enforces for replay. The equality depends on no live
// context, so both admission boundaries must hold it. Core validation
// refuses the mismatched item through the generated items def
// (payloadschema.Validate, the same def the payload validators bind), the
// canonical mismatched array through the successor-contract payload
// validator over a pinned canonical head, and the mismatched pair through
// the same validator's engine tier; the production host publication must
// refuse the canonical forms beside them. At the base commit all mismatch
// assertions fail: the base items def published outcome_kind and
// outcome_payload independently and the base published successor fields
// carried the same independence, so the publication admitted both mismatched
// forms, and the base core payload validators admitted them too — the base
// fold refused the mismatch only after the payload boundaries accepted it.
func TestOutsideRepairOutcomeKindParity(t *testing.T) {
	t.Parallel()
	const itemsRef = "workflow_action_outcome_predicates"
	if !payloadschema.HasIn(payloadschema.Document(), itemsRef) {
		t.Fatalf("the core schema registry carries no %s def; the item probe cannot run", itemsRef)
	}
	checkPayload := func() map[string]any {
		return map[string]any{"kind": "check", "check_ref": "check:outside-repair", "immutable_subject_ref": "commit:outside-repair", "expected_result": "pass"}
	}
	predicate := func(kind string) map[string]any {
		return map[string]any{"predicate_id": "predicate:outside-repair", "ordinal": 0, "outcome_kind": kind, "outcome_payload": checkPayload()}
	}
	items := func(kind string) []any { return []any{predicate(kind)} }
	// supersedeFields builds the canonical successor contract: the explicit
	// outcome_predicates array is the one authorable outcome form
	// (CD-0218 D1/D2), required and per-kind closed.
	supersedeFields := func(kind string) map[string]any {
		return map[string]any{
			"contract_version": 2, "premise": "outside-repair successor premise", "outcome_predicates": items(kind),
			"required_evidence": []any{}, "route_conventions": []any{}, "spec_mandate": []any{}, "law_modifies": []any{},
			"rigor_class": "prototype_internal", "supersede_reason": "outside-repair supersede reason", "audit_evidence": []any{"evidence:outside-repair"},
			"decision_domains": []any{},
		}
	}
	// retainedPinFields builds the legacy outcome pair the engine recovery
	// declaration still carries for recorded history: a retained pin that
	// replays under the paired shape answers to the kind-match rule.
	retainedPinFields := func(kind string) map[string]any {
		fields := supersedeFields(kind)
		delete(fields, "outcome_predicates")
		fields["outcome_kind"] = kind
		fields["outcome_payload"] = checkPayload()
		delete(fields, "decision_domains")
		return fields
	}
	probes := []map[string]any{
		{"id": "approve-item-mismatch", "mode": "field", "action": "approve_contract", "field": "outcome_predicates", "value": items("exists")},
		{"id": "approve-item-control", "mode": "field", "action": "approve_contract", "field": "outcome_predicates", "value": items("check")},
		{"id": "supersede-array-mismatch", "mode": "fields", "action": "supersede_contract", "fields": supersedeFields("exists")},
		{"id": "supersede-array-control", "mode": "fields", "action": "supersede_contract", "fields": supersedeFields("check")},
	}
	published := outsideRepairPublishedVerdicts(t, probes)
	require := func(id string) outsideRepairPublication {
		verdict, ok := published[id]
		if !ok {
			t.Fatalf("the host publication probe returned no verdict for %s", id)
		}
		return verdict
	}
	// The mismatched approve outcome item: outcome_kind "exists" beside a
	// check outcome_payload.
	if err := payloadschema.Validate(itemsRef, outsideRepairRaw(t, items("exists"))); err == nil {
		t.Error("core validation admits an approve outcome_predicates item whose outcome_kind and outcome_payload.kind disagree")
	} else if itemControl := payloadschema.Validate(itemsRef, outsideRepairRaw(t, items("check"))); itemControl != nil {
		t.Fatalf("core validation refuses the matching-kind control item: %v", itemControl)
	}
	if verdict := require("approve-item-mismatch"); verdict.Admit {
		t.Errorf("the published approve_contract outcome_predicates admit a mismatched item; failures: %v", verdict.Failures)
	}
	if verdict := require("approve-item-control"); !verdict.Admit {
		t.Errorf("the published approve_contract outcome_predicates refuse the matching-kind control item: %v", verdict.Failures)
	}
	// The canonical supersede array: one item whose outcome_kind "exists"
	// disagrees with its check outcome_payload, against the pinned
	// canonical head the live authoring selector resolves.
	if err := validateWorkflowContractRecoveryPayload(currentWorkflowDefinition(t), outsideRepairRaw(t, supersedeFields("exists"))); err == nil {
		t.Error("the successor-contract payload validator admits an outcome_predicates item whose outcome_kind and outcome_payload.kind disagree")
	} else if arrayControl := validateWorkflowContractRecoveryPayload(currentWorkflowDefinition(t), outsideRepairRaw(t, supersedeFields("check"))); arrayControl != nil {
		t.Fatalf("the successor-contract payload validator refuses the matching-kind control array: %v", arrayControl)
	}
	if verdict := require("supersede-array-mismatch"); verdict.Admit {
		t.Errorf("the published supersede_contract fields admit a mismatched outcome item; failures: %v", verdict.Failures)
	}
	if verdict := require("supersede-array-control"); !verdict.Admit {
		t.Errorf("the published supersede_contract fields refuse the matching-kind control array: %v", verdict.Failures)
	}
	// The retained-pin outcome pair keeps its kind-match rule on the engine
	// tier: the pair fields left canonical authoring, so the published
	// canonical variant carries no pair leg and the equality is held by the
	// recovery declaration replay reads.
	if err := validateWorkflowContractRecoveryPayload(WorkflowDefinition{}, outsideRepairRaw(t, retainedPinFields("exists"))); err == nil {
		t.Error("the engine recovery validator admits a retained-pin outcome pair whose outcome_kind and outcome_payload.kind disagree")
	} else if pairControl := validateWorkflowContractRecoveryPayload(WorkflowDefinition{}, outsideRepairRaw(t, retainedPinFields("check"))); pairControl != nil {
		t.Fatalf("the engine recovery validator refuses the matching-kind retained-pin control pair: %v", pairControl)
	}
}

// TestOutsideRepairRecordVerdictGroupSubsets supplements the original
// record_verdict behavioral comparison with the canonical batch contract:
// the verdicts batch is the one declared shape, and the entry group
// {predicate_id, verdict_kind, evaluation_evidence} lives inside each batch
// entry. The complete batch admits on both admission boundaries, and every
// proper subset of the entry group is judged on both boundaries in every
// position it can appear: inside the entry the required predicate and kind
// splits the subsets into legal minimal entries and refusals, and riding at
// the top level — beside the batch or alone — every member refuses, because
// the single form the engine declaration still names for replay carries no
// canonical admission anywhere. At the base commit every refusal assertion
// fails: the base core declared no cross-field rule and the base publication
// was the union merge of the wire shapes, so both
// admitted each complete-plus-partial payload.
func TestOutsideRepairRecordVerdictGroupSubsets(t *testing.T) {
	t.Parallel()
	payload, definition := outsideRepairActionPayload(t, "record_verdict")
	declared := map[string]bool{}
	for _, field := range payload.Fields {
		declared[field.Name] = true
	}
	if !declared["verdicts"] {
		t.Fatalf("the record_verdict payload declaration carries no verdicts member; the batch probes cannot run")
	}
	if !payloadschema.HasIn(payloadschema.Document(), "workflow_verdict_batch_entry") {
		t.Fatalf("the core schema registry carries no workflow_verdict_batch_entry def; the entry probes cannot run")
	}
	entryValue := func(member string) any {
		switch member {
		case "predicate_id":
			return "predicate:outside-repair-subset"
		case "receipt_refs":
			return []any{"worktree_verify:outside-repair-subset"}
		}
		t.Fatalf("unknown entry member %s", member)
		return nil
	}
	entryGroup := []string{"predicate_id", "receipt_refs"}
	completeBatch := func() map[string]any {
		verdict := map[string]any{}
		for _, member := range entryGroup {
			verdict[member] = entryValue(member)
		}
		return map[string]any{"contract_version": 1, "verdicts": []any{verdict}}
	}
	entryCarrying := func(subset []string) map[string]any {
		verdict := map[string]any{}
		for _, member := range subset {
			verdict[member] = entryValue(member)
		}
		return map[string]any{"contract_version": 1, "verdicts": []any{verdict}}
	}
	core := func(fields map[string]any) error {
		return validateWorkflowActionPayload(definition, "record_verdict", outsideRepairRaw(t, fields))
	}
	// Every proper subset of the entry group with at least one member.
	var properSubsets [][]string
	for size := 1; size < len(entryGroup); size++ {
		properSubsets = append(properSubsets, outsideRepairSubsets(entryGroup, size)...)
	}
	probes := []map[string]any{
		{"id": "batch-complete-control", "mode": "fields", "action": "record_verdict", "fields": completeBatch()},
	}
	for _, subset := range properSubsets {
		label := strings.Join(subset, "+")
		probes = append(probes,
			map[string]any{"id": "entry-carrying-" + label, "mode": "fields", "action": "record_verdict", "fields": entryCarrying(subset)},
			map[string]any{"id": "batch-plus-" + label, "mode": "fields", "action": "record_verdict", "fields": batchPlusTopLevel(subset, completeBatch, entryValue)},
			map[string]any{"id": "top-level-" + label, "mode": "fields", "action": "record_verdict", "fields": topLevelOnly(subset)},
		)
	}
	published := outsideRepairPublishedVerdicts(t, probes)
	require := func(id string) outsideRepairPublication {
		verdict, ok := published[id]
		if !ok {
			t.Fatalf("the host publication probe returned no verdict for %s", id)
		}
		return verdict
	}
	if err := core(completeBatch()); err != nil {
		t.Fatalf("core payload validation refuses the complete batch: %v", err)
	}
	if verdict := require("batch-complete-control"); !verdict.Admit {
		t.Errorf("the published record_verdict fields refuse the complete batch: %v", verdict.Failures)
	}
	for _, subset := range properSubsets {
		label := strings.Join(subset, "+")
		// The entry's own schema carries the discipline inside the batch:
		// predicate_id and receipt_refs are required, so a subset that
		// includes both is a legal minimal entry both boundaries admit,
		// and a subset that omits either is a partial entry both refuse.
		entryErr := core(entryCarrying(subset))
		entryVerdict := require("entry-carrying-" + label)
		if slices.Contains(subset, "predicate_id") && slices.Contains(subset, "receipt_refs") {
			if entryErr != nil {
				t.Errorf("core payload validation refuses the legal minimal entry {%s}: %v", label, entryErr)
			}
			if !entryVerdict.Admit {
				t.Errorf("the published record_verdict fields refuse the legal minimal entry {%s}; failures: %v", label, entryVerdict.Failures)
			}
		} else {
			if entryErr == nil {
				t.Errorf("core payload validation admits a batch entry carrying the partial entry group {%s}", label)
			}
			if entryVerdict.Admit {
				t.Errorf("the published record_verdict fields admit a batch entry carrying the partial entry group {%s}; failures: %v", label, entryVerdict.Failures)
			}
		}
		// The remnant rides beside the batch: the closed canonical field
		// list refuses every entry-level field at the top level.
		if err := core(batchPlusTopLevel(subset, completeBatch, entryValue)); err == nil {
			t.Errorf("core payload validation admits the complete batch beside the partial entry group {%s}", label)
		}
		if verdict := require("batch-plus-" + label); verdict.Admit {
			t.Errorf("the published record_verdict fields admit the complete batch beside the partial entry group {%s}; failures: %v", label, verdict.Failures)
		}
		// The remnant alone: the single form left the canonical declared
		// set, so both boundaries refuse it together.
		if err := core(topLevelOnly(subset)); err == nil {
			t.Errorf("core payload validation admits the partial entry group {%s} alone", label)
		}
		if verdict := require("top-level-" + label); verdict.Admit {
			t.Errorf("the published record_verdict fields admit the partial entry group {%s} alone; failures: %v", label, verdict.Failures)
		}
	}
}

// batchPlusTopLevel clones the complete batch and adds the given entry-group
// members at the top level, where the canonical declaration declares none of
// them.
func batchPlusTopLevel(subset []string, completeBatch func() map[string]any, entryValue func(string) any) map[string]any {
	fields := completeBatch()
	for _, member := range subset {
		fields[member] = entryValue(member)
	}
	return fields
}

// topLevelOnly carries the given entry-group members alone at the top level,
// the single-form position the canonical declaration retired.
func topLevelOnly(subset []string) map[string]any {
	fields := map[string]any{"contract_version": 1}
	for _, member := range subset {
		switch member {
		case "predicate_id":
			fields[member] = "predicate:outside-repair-subset"
		case "verdict_kind":
			fields[member] = "ok"
		case "receipt_refs":
			fields[member] = []any{"worktree_verify:outside-repair-subset"}
		}
	}
	return fields
}

// outsideRepairSubsets returns every subset of the given size of the names,
// in a deterministic order.
func outsideRepairSubsets(names []string, size int) [][]string {
	if size <= 0 || size > len(names) {
		return nil
	}
	var result [][]string
	var walk func(start int, chosen []string)
	walk = func(start int, chosen []string) {
		if len(chosen) == size {
			result = append(result, append([]string{}, chosen...))
			return
		}
		for index := start; index <= len(names)-(size-len(chosen)); index++ {
			walk(index+1, append(chosen, names[index]))
		}
	}
	walk(0, nil)
	return result
}
