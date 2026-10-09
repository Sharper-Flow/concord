package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

// CON-412 independent regression probes. This file carries no production
// change and no reconstruction of any publication: the published boundary is
// exercised by invoking the ORIGINAL production host publisher
// (adapter/opencode/concord.ts publishedRequestSchema) and the host validator
// (adapter/opencode/dispatch.ts validateAgainstSchema) through Bun, exactly as
// the host dispatch path does, and the core boundary is exercised through the
// owning core validators with the live builtin registry's own declarations.
//
// The same source runs unchanged at the base commit (the pre-repair
// mechanism) and on the repaired tree; every helper it calls
// (BuiltinWorkflowDefinitionForRef, validateWorkflowPayloadValue,
// validateWorkflowActionPayload, validateWorkflowContractRecoveryPayload,
// payloadschema.Validate/Has) already exists at the base commit, and the Bun
// entry points (publishedRequestSchema, validateAgainstSchema) exist there
// under the same exported names. The probe resolves whichever publication
// shape the production publisher of the current tree emits: the per-action
// closed branches the current publisher emits, or the single shared union
// input the original publisher emitted, without rebuilding either.
//
// At the base commit every failing assertion below fails BEHAVIORALLY — an
// admission verdict that differs from the approved behavior, never a missing
// schema keyword, variant name, or version marker:
//
//   - Unicode: the base core counted Unicode code points
//     (validateWorkflowPayloadValue's len([]rune(text))) and the base
//     publication carried code-point-equal minLength/maxLength, so both
//     admitted a 4096-code-point 8192-byte "searched" string and both refused
//     the two-byte one-code-point "é" at the declared two-unit floor. The
//     approved UTF-8 byte representation admits "é" and refuses the 8192-byte
//     string; both halves fail on the base tree and pass on the repair.
//   - Outcome-kind equality: the base publication published outcome_kind and
//     outcome_payload independently (a kind-agnostic items def and a union
//     outcome_payload), so it admitted outcome_kind "exists" beside a check
//     outcome_payload — an item the core's generated items def and the
//     successor-contract validator refuse on the repair, and which the base
//     fold itself refused at workflow.go's "malformed or mismatched" guard.
//   - record_verdict alternative groups: the base core carried no cross-field
//     declaration and the base publication was the union merge of the wire
//     shapes, so both admitted a complete batch beside every partial entry
//     group. The repair refuses each complete-plus-partial payload at payload
//     validation and at the published closed branches.
//
// The Unicode probe also covers workflow reference list items, which the
// store admits through ValidReference by UTF-8 bytes.
//
// Run on the repaired tree:
//
//	bin/oc-test targeted -- go test -count=1 ./internal/store -run '^TestOutsideRepair' -v
//
// Run unchanged on the base commit: check out the base, copy this file into
// internal/store/, and run the same go test command — the Bun publisher of
// that tree is invoked, and each named behavioral assertion fails there.

// outsideRepairProbeScript is the bridge the tests drive: it imports the
// production host publisher and validator by absolute path, resolves the
// published work_transition fields node for one action from whichever shape
// the publisher emits (per-action closed branches, a discriminator-shared
// union, or allOf if/then conditions), and validates probe values against the
// resolved published nodes with the host's own validator. Nothing here
// rebuilds or approximates the publication.
func outsideRepairProbeScript(repoRoot string) string {
	return fmt.Sprintf(`import { publishedRequestSchema } from %q;
import { validateAgainstSchema } from %q;
const schema = publishedRequestSchema("concord_work_transition");
const branches = (schema.oneOf ?? []).filter((b) => b?.properties?.operation?.const === "workflow_action");
if (branches.length === 0) throw new Error("published concord_work_transition names no workflow_action operation branch");
const fieldsFor = (action) => {
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
  if (node.properties && node.properties[name]) return node.properties[name];
  for (const keyword of ["oneOf", "anyOf"]) {
    for (const branch of node[keyword] ?? []) {
      const found = propertyNode(branch, name);
      if (found) return found;
    }
  }
  for (const member of node.allOf ?? []) {
    const found = propertyNode(member?.then ?? member, name);
    if (found) return found;
  }
  return null;
};
const probes = await new Response(Bun.stdin).json();
const results = [];
for (const probe of probes) {
  const failures = [];
  const fields = fieldsFor(probe.action);
  let admit = false;
  if (!fields) {
    failures.push("published schema resolves no fields node for " + probe.action);
  } else if (probe.mode === "field") {
    const node = propertyNode(fields, probe.field);
    if (!node) {
      failures.push("published schema resolves no " + probe.field + " property for " + probe.action);
    } else {
      admit = validateAgainstSchema(node, probe.value, failures);
    }
  } else {
    admit = validateAgainstSchema(fields, probe.fields, failures);
  }
  results.push({ id: probe.id, admit: !!admit, failures: failures.slice(0, 4) });
}
console.log(JSON.stringify({ results }));
`, filepath.Join(repoRoot, "adapter", "opencode", "concord.ts"), filepath.Join(repoRoot, "adapter", "opencode", "dispatch.ts"))
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
// on the two wire forms that carry it: one outcome_predicates item on
// approve_contract, and the single legacy outcome pair on supersede_contract.
// The equality depends on no live context, so both admission boundaries must
// hold it. Core validation refuses the mismatched item through the generated
// items def (payloadschema.Validate, the same def the payload validators
// bind) and the mismatched pair through the successor-contract payload
// validator (validateWorkflowContractRecoveryPayload); the production host
// publication must refuse both beside it. At the base commit all mismatch
// assertions fail: the base items def published outcome_kind and
// outcome_payload independently and the base published successor fields
// carried the same independence, so the publication admitted both mismatched
// forms, and the base core payload validators admitted them too — the base
// fold refused the mismatch only after the payload boundaries accepted it.
func TestOutsideRepairOutcomeKindParity(t *testing.T) {
	t.Parallel()
	const itemsRef = "workflow_action_outcome_predicates"
	if !payloadschema.Has(itemsRef) {
		t.Fatalf("the core schema registry carries no %s def; the item probe cannot run", itemsRef)
	}
	checkPayload := func() map[string]any {
		return map[string]any{"kind": "check", "check_ref": "check:outside-repair", "immutable_subject_ref": "commit:outside-repair", "expected_result": "pass"}
	}
	predicate := func(kind string) map[string]any {
		return map[string]any{"predicate_id": "predicate:outside-repair", "ordinal": 0, "outcome_kind": kind, "outcome_payload": checkPayload()}
	}
	items := func(kind string) []any { return []any{predicate(kind)} }
	pairFields := func(kind string) map[string]any {
		return map[string]any{
			"contract_version": 2, "premise": "outside-repair successor premise", "outcome_kind": kind, "outcome_payload": checkPayload(),
			"required_evidence": []any{}, "route_conventions": []any{}, "spec_mandate": []any{}, "law_modifies": []any{},
			"rigor_class": "prototype_internal", "supersede_reason": "outside-repair supersede reason", "audit_evidence": []any{"evidence:outside-repair"},
		}
	}
	probes := []map[string]any{
		{"id": "approve-item-mismatch", "mode": "field", "action": "approve_contract", "field": "outcome_predicates", "value": items("exists")},
		{"id": "approve-item-control", "mode": "field", "action": "approve_contract", "field": "outcome_predicates", "value": items("check")},
		{"id": "supersede-pair-mismatch", "mode": "fields", "action": "supersede_contract", "fields": pairFields("exists")},
		{"id": "supersede-pair-control", "mode": "fields", "action": "supersede_contract", "fields": pairFields("check")},
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
	// The mismatched supersede single pair: outcome_kind "exists" beside the
	// same check outcome_payload at the successor field level.
	if err := validateWorkflowContractRecoveryPayload(outsideRepairRaw(t, pairFields("exists"))); err == nil {
		t.Error("the successor-contract payload validator admits an outcome pair whose outcome_kind and outcome_payload.kind disagree")
	} else if pairControl := validateWorkflowContractRecoveryPayload(outsideRepairRaw(t, pairFields("check"))); pairControl != nil {
		t.Fatalf("the successor-contract payload validator refuses the matching-kind control pair: %v", pairControl)
	}
	if verdict := require("supersede-pair-mismatch"); verdict.Admit {
		t.Errorf("the published supersede_contract fields admit a mismatched outcome pair; failures: %v", verdict.Failures)
	}
	if verdict := require("supersede-pair-control"); !verdict.Admit {
		t.Errorf("the published supersede_contract fields refuse the matching-kind control pair: %v", verdict.Failures)
	}
}

// TestOutsideRepairRecordVerdictGroupSubsets supplements the original
// record_verdict behavioral comparison with the complete-plus-partial
// alternatives of its actual multi-member group: the entry group
// {predicate_id, verdict_kind, evaluation_evidence} beside the complete batch
// group {verdicts}. A complete batch beside ANY proper subset of the entry
// group — every one- and two-member subset, not only the single-member cases
// — must be refused by core payload validation and by the production host
// publication. At the base commit every refusal assertion fails: the base
// core declared no cross-field rule and the base publication was the union
// merge of the wire shapes, so both admitted each complete-plus-partial
// payload. A partial entry group on its own is judged only for boundary
// parity (the two admission boundaries must agree); the fold's deeper
// verdict-shape refusal owns that payload's final fate on every tree.
func TestOutsideRepairRecordVerdictGroupSubsets(t *testing.T) {
	t.Parallel()
	payload, definition := outsideRepairActionPayload(t, "record_verdict")
	declared := map[string]bool{}
	for _, field := range payload.Fields {
		declared[field.Name] = true
	}
	for _, member := range []string{"predicate_id", "verdict_kind", "evaluation_evidence", "verdicts"} {
		if !declared[member] {
			t.Fatalf("the record_verdict payload declaration carries no %s member; the group probes cannot run", member)
		}
	}
	entryValue := func(member string) any {
		switch member {
		case "predicate_id":
			return "predicate:outside-repair-subset"
		case "verdict_kind":
			return "ok"
		case "evaluation_evidence":
			return []any{"evidence:outside-repair-subset"}
		}
		t.Fatalf("unknown entry member %s", member)
		return nil
	}
	entryGroup := []string{"predicate_id", "verdict_kind", "evaluation_evidence"}
	completeEntry := func() map[string]any {
		fields := map[string]any{"contract_version": 1}
		for _, member := range entryGroup {
			fields[member] = entryValue(member)
		}
		return fields
	}
	completeBatch := func() map[string]any {
		verdict := map[string]any{}
		for _, member := range entryGroup {
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
		{"id": "entry-complete-control", "mode": "fields", "action": "record_verdict", "fields": completeEntry()},
		{"id": "batch-complete-control", "mode": "fields", "action": "record_verdict", "fields": completeBatch()},
	}
	batchPlusPartial := func(subset []string) map[string]any {
		fields := completeBatch()
		for _, member := range subset {
			fields[member] = entryValue(member)
		}
		return fields
	}
	for _, subset := range properSubsets {
		probes = append(probes, map[string]any{"id": "batch-plus-" + strings.Join(subset, "+"), "mode": "fields", "action": "record_verdict", "fields": batchPlusPartial(subset)})
	}
	partialAlone := func(subset []string) map[string]any {
		fields := map[string]any{"contract_version": 1}
		for _, member := range subset {
			fields[member] = entryValue(member)
		}
		return fields
	}
	for _, subset := range properSubsets {
		probes = append(probes, map[string]any{"id": "partial-alone-" + strings.Join(subset, "+"), "mode": "fields", "action": "record_verdict", "fields": partialAlone(subset)})
	}
	published := outsideRepairPublishedVerdicts(t, probes)
	require := func(id string) outsideRepairPublication {
		verdict, ok := published[id]
		if !ok {
			t.Fatalf("the host publication probe returned no verdict for %s", id)
		}
		return verdict
	}
	if err := core(completeEntry()); err != nil {
		t.Fatalf("core payload validation refuses the complete entry group: %v", err)
	}
	if verdict := require("entry-complete-control"); !verdict.Admit {
		t.Errorf("the published record_verdict fields refuse the complete entry group: %v", verdict.Failures)
	}
	if err := core(completeBatch()); err != nil {
		t.Fatalf("core payload validation refuses the complete batch group: %v", err)
	}
	if verdict := require("batch-complete-control"); !verdict.Admit {
		t.Errorf("the published record_verdict fields refuse the complete batch group: %v", verdict.Failures)
	}
	for _, subset := range properSubsets {
		label := strings.Join(subset, "+")
		if err := core(batchPlusPartial(subset)); err == nil {
			t.Errorf("core payload validation admits the complete batch beside the partial entry group {%s}", label)
		}
		if verdict := require("batch-plus-" + label); verdict.Admit {
			t.Errorf("the published record_verdict fields admit the complete batch beside the partial entry group {%s}; failures: %v", label, verdict.Failures)
		}
		// Boundary parity on the partial group alone: whatever the two
		// boundaries decide, they decide it together.
		coreAdmits := core(partialAlone(subset)) == nil
		if verdict := require("partial-alone-" + label); verdict.Admit != coreAdmits {
			t.Errorf("the partial entry group {%s} alone: core validation admits=%v while the published fields admit=%v; failures: %v", label, coreAdmits, verdict.Admit, verdict.Failures)
		}
	}
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
