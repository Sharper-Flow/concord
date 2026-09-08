package agent

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// A mutation input schema and the Go struct that decodes it cross a join no
// compiler checks: the generated contract publishes what callers may send, and
// the strict decoder refuses anything the struct lacks. The revise_intent
// evidence gap (#918) shipped because nothing bound the two declarations. The
// assertions below prove the binding for every contract mutation operation, so
// a schema field the decoder lacks — or a decoder field the schema refuses —
// fails here without anyone having to think of a value that exposes it.

// transportOnlyInputFields names schema properties the runtime reads from the
// envelope rather than the operation input, so no decoding struct carries
// them. Every entry is a claim about the transport; keep this list empty of
// anything a decoder should own.
var transportOnlyInputFields = map[string]bool{
	"requested_budget_seconds": true,
}

func TestMutationInputSchemasBindDecodingStructs(t *testing.T) {
	decodeTargets := map[string]any{
		"concord_domain.observation_dismiss":             domainObservationDismissInput{},
		"concord_domain.observation_record":              domainObservationRecordInput{},
		"concord_work_compact.lesson_publish":            lessonPublishInput{},
		"concord_work_compact.publish":                   compactPublishInput{},
		"concord_work_compact.reconcile":                 compactReconcileInput{},
		"concord_work_define.capture":                    captureMutationInput{},
		"concord_work_define.observation_record":         observationRecordInput{},
		"concord_work_define.research_finding_record":    researchFindingMutation{},
		"concord_work_define.research_freshness_set":     researchFreshnessMutation{},
		"concord_work_define.research_pack_create":       researchPackCreateMutation{},
		"concord_work_define.research_revision_append":   researchRevisionMutation{},
		"concord_work_define.research_source_record":     researchSourceMutation{},
		"concord_work_define.revise_intent":              reviseMutationInput{},
		"concord_work_initiative.add_entry":              initiativeEntryMutationInput{},
		"concord_work_initiative.change_requiredness":    initiativeRequirednessInput{},
		"concord_work_initiative.create":                 initiativeCreateMutationInput{},
		"concord_work_initiative.remove_entry":           initiativeRemoveEntryMutationInput{},
		"concord_work_initiative.reorder_entry":          initiativeReorderEntryInput{},
		"concord_work_initiative.revise_narrative":       initiativeNarrativeMutationInput{},
		"concord_work_relate.link":                       linkMutationInput{},
		"concord_work_relate.message_send":               messageSendInput{},
		"concord_work_relate.message_withdraw":           messageWithdrawInput{},
		"concord_work_relate.resolve_overlap":            resolveOverlapMutationInput{},
		"concord_work_relate.resource_claim":             resourceClaimInput{},
		"concord_work_relate.resource_release":           resourceReleaseInput{},
		"concord_work_relate.restore_superseded":         restoreMutationInput{},
		"concord_work_relate.set_memberships":            membershipsMutationInput{},
		"concord_work_relate.supersede":                  supersedeMutationInput{},
		"concord_work_relate.unlink":                     unlinkMutationInput{},
		"concord_work_transition.lifecycle":              lifecycleMutationInput{},
		"concord_work_transition.workflow_action":        actionMutationInput{},
		"concord_work_transition.worktree_audit_reclaim": worktreeAuditReclaimInput{},
		"concord_work_transition.worktree_claim":         worktreeClaimInput{},
		"concord_work_transition.worktree_destroy":       worktreeDestroyInput{},
		"concord_work_transition.worktree_reclaim":       worktreeReclaimInput{},
		"concord_work_transition.worktree_verify":        worktreeVerifyInput{},
	}
	for _, entry := range ContractOperations {
		if entry.Kind != OperationKind("mutation") {
			continue
		}
		t.Run(entry.ID, func(t *testing.T) {
			target, bound := decodeTargets[entry.ID]
			if !bound {
				t.Fatalf("mutation operation has no decoding struct bound in this test")
			}
			rule, ok := GeneratedPayloadRules[entry.InputSchema]
			if !ok {
				t.Fatalf("input schema %q is not in the generated payload rules", entry.InputSchema)
			}
			structTags := jsonTagNames(reflect.TypeOf(target))
			schemaProperties := map[string]bool{}
			for _, name := range rule.Properties {
				schemaProperties[name] = true
			}
			for _, tag := range structTags {
				if !schemaProperties[tag] {
					t.Errorf("decoder field %q is not a property of schema %q, so the strict schema refuses input the decoder expects", tag, entry.InputSchema)
				}
			}
			var missing []string
			for _, name := range rule.Properties {
				if transportOnlyInputFields[name] {
					continue
				}
				if !structTagsMap(structTags)[name] {
					missing = append(missing, name)
				}
			}
			if len(missing) != 0 {
				sort.Strings(missing)
				t.Errorf("schema %q properties the decoder lacks (strict decoding refuses them): %s", entry.InputSchema, strings.Join(missing, ", "))
			}
		})
	}
	// Every bound target must correspond to a contract mutation, so a renamed
	// operation cannot leave a stale entry behind.
	boundOperations := map[string]bool{}
	for _, entry := range ContractOperations {
		if entry.Kind == OperationKind("mutation") {
			boundOperations[entry.ID] = true
		}
	}
	for id := range decodeTargets {
		if !boundOperations[id] {
			t.Errorf("test binds %q, which is not a contract mutation operation", id)
		}
	}
}

// jsonTagNames returns the `json:"name"` tag of every struct field, skipping
// embedded non-struct fields. A `json:"-"` tag never appears because the
// decoders carry no such fields.
func jsonTagNames(value reflect.Type) []string {
	var names []string
	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		names = append(names, strings.Split(tag, ",")[0])
	}
	return names
}

func structTagsMap(names []string) map[string]bool {
	result := make(map[string]bool, len(names))
	for _, name := range names {
		result[name] = true
	}
	return result
}

// TestReviseIntentEvidenceFixtureDecodes is the fixture entry the #918 gap
// lacked: a revise_intent input carrying the evidence field the published
// schema declares, strict-decoded through the same path the runtime uses.
func TestReviseIntentEvidenceFixtureDecodes(t *testing.T) {
	raw := json.RawMessage(`{"work_id":"work-1","expected_version":4,"title":"Need revised","value_statement":"Revised value","kind":"task","priority":3,"tags":[],"reason":"clarified","evidence":[{"kind":"commit","authority":"git","locator_kind":"commit","locator":"commit:7b83cbf41af2f9fa7990294a41a50cb75a1d6d1e"}],"idempotency_key":"revise-fixture-1"}`)
	var in reviseMutationInput
	if err := decodeOperationInput(raw, &in); err != nil {
		t.Fatalf("revise_intent evidence fixture failed strict decoding: %v", err)
	}
	if len(in.Evidence) != 1 || in.Evidence[0].Locator != "commit:7b83cbf41af2f9fa7990294a41a50cb75a1d6d1e" {
		t.Fatalf("revise_intent evidence fixture lost its locator: %+v", in.Evidence)
	}
}
