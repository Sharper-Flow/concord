package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
)

// Direct transaction callbacks isolate the selected-pack owner check from
// workflow admission. Synthetic retained reference bytes isolate the shared
// reader's independence from the author's grant; they prove no live admission.

func researchContextSelection(packID string) []string {
	return []string{packID}
}

func researchContextBinding(packID string) researchBindingInput {
	return researchBindingInput{PackID: packID, Revision: 1, UseRole: "context", Required: true}
}

// researchContextCallbackFixture builds one scope fixture and the direct
// transaction-callback pair for it: the runtime value and invocation the
// live workflow_action mutation route assembles, with the byte-oriented
// envelope researchScopeEnvelope builds.
func researchContextCallbackFixture(t *testing.T, capabilities []Capability, products []string) (researchScopeEnv, runtime, Invocation) {
	t.Helper()
	f := researchScopeFixture(t, capabilities, products)
	env, err := researchScopeEnvelope(f)
	if err != nil {
		t.Fatal(err)
	}
	inv := Invocation{ClientRef: "client-1", PrincipalRef: "human-1", SessionRef: "session-1", AgentRef: "agent-1", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: ManifestDigest, RequiredCapability: Capability("work_transition"), ProductID: "product-a", ProjectID: "project-a"}
	return f, runtime{Store: f.store, Authority: f.service, Envelope: env}, inv
}

func researchContextAuthorizeTx(t *testing.T, f researchScopeEnv, rt runtime, inv Invocation, bindings []researchBindingInput, selections []string) error {
	t.Helper()
	before := make(map[string]researchScopeEffects)
	for _, packID := range append([]string{researchScopePackA, researchScopePackB}, combinedResearchPackIDs(bindings, selections)...) {
		if _, seen := before[packID]; seen {
			continue
		}
		var count int
		if err := f.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM active_research_packs WHERE pack_id=?`, packID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			before[packID] = researchScopeSnapshot(t, f.store, packID)
		}
	}
	err := f.store.Transact(context.Background(), func(tx *store.Transaction) error {
		return rt.authorizeWorkflowActionTx(context.Background(), tx, probedHost(), inv, bindings, selections)
	})
	if err != nil {
		for packID, snapshot := range before {
			researchScopeAssertNoEffects(t, snapshot, researchScopeSnapshot(t, f.store, packID))
		}
	}
	return err
}

// researchContextRequireRefusal asserts the callback refused with one typed
// kind: the runtime failure kinds the action boundary maps, or the store
// failure kinds the identity lookups return.
func researchContextRequireRefusal(t *testing.T, err error, wantKind string) {
	t.Helper()
	if err == nil {
		t.Fatal("selection authorization passed, want a typed refusal")
	}
	var rtFail *runtimeFailure
	if errors.As(err, &rtFail) {
		if rtFail.kind != wantKind {
			t.Fatalf("refusal kind %q, want %q (error: %v)", rtFail.kind, wantKind, err)
		}
		return
	}
	var failure *store.Failure
	if errors.As(err, &failure) {
		if string(failure.Kind) != wantKind {
			t.Fatalf("refusal kind %q, want %q (error: %v)", failure.Kind, wantKind, err)
		}
		return
	}
	t.Fatalf("refusal is not a typed failure: %v", err)
}

// researchContextDeclareRetainedSelection inserts synthetic retained reference
// bytes for reader isolation only. This fixture exercises neither live
// admission nor the production fold or authoritative workflow append route.
func researchContextDeclareRetainedSelection(t *testing.T, f researchScopeEnv, packIDs []string) {
	t.Helper()
	var stepID, workflowRef, workflowDigest string
	var definitionVersion int64
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT current_step,definition_ref,definition_digest,definition_version FROM workflow_instances WHERE work_id=?`, researchScopeConsumer).Scan(&stepID, &workflowRef, &workflowDigest, &definitionVersion); err != nil {
		t.Fatal(err)
	}
	var attemptEpoch int64
	if err := f.store.DatabaseForTesting().QueryRow(`SELECT COALESCE(MAX(attempt_epoch),1) FROM durable_operations WHERE work_id=?`, researchScopeConsumer).Scan(&attemptEpoch); err != nil {
		t.Fatal(err)
	}
	version := workVersion(t, f.store, researchScopeConsumer)
	selections := make([]map[string]any, 0, len(packIDs))
	for _, packID := range packIDs {
		selections = append(selections, map[string]any{"pack_id": packID, "revision": 1, "finding_id": "f-" + packID, "domain_id": "retained-scope"})
	}
	payload := map[string]any{
		"work_id": researchScopeConsumer, "expected_version": version, "resulting_version": version + 1,
		"step_id": stepID, "attempt_epoch": attemptEpoch,
		"required_reading": []any{}, "finding_refs": []any{}, "context_findings": []any{},
		"research_finding_refs": selections,
		"workflow_ref":          workflowRef, "workflow_definition_version": definitionVersion, "workflow_definition_digest": workflowDigest,
		"actor_ref":  store.DeriveWorkflowActorRef("human-1", "client-1", "agent-1", "session-1"),
		"request_id": "request:research-context-retained",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.store.DatabaseForTesting().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seq := agentSeedReceipt(t, tx, "sc-rc-declare-retained", store.WorkflowWorkContextRecorded, researchScopeConsumer, "operator", 1, raw)
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO work_context_state(work_id,source_frontier,declaration_seq,declaration_id,declaration_payload) VALUES(?,?,?,?,?) ON CONFLICT(work_id) DO UPDATE SET source_frontier=excluded.source_frontier,declaration_seq=excluded.declaration_seq,declaration_id=excluded.declaration_id,declaration_payload=excluded.declaration_payload`, researchScopeConsumer, seq, seq, "sc-rc-declare-retained", string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestResearchContextSelectionOwnerScope(t *testing.T) {
	// The callback checks the combined referenced-owner set independently of
	// workflow admission and leaves durable state unchanged on refusal.
	t.Run("same_product_selection_succeeds", func(t *testing.T) {
		f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a"})
		if err := researchContextAuthorizeTx(t, f, rt, inv, nil, researchContextSelection(researchScopePackA)); err != nil {
			t.Fatalf("same-Product selection refused: %v", err)
		}
	})
	t.Run("selection_only_cross_product_denied_without_cross_scope", func(t *testing.T) {
		f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a"})
		err := researchContextAuthorizeTx(t, f, rt, inv, nil, researchContextSelection(researchScopePackB))
		researchContextRequireRefusal(t, err, "unauthorized")
	})
	t.Run("covered_cross_product_denied_without_cross_scope", func(t *testing.T) {
		f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a", "product-b"})
		err := researchContextAuthorizeTx(t, f, rt, inv, nil, researchContextSelection(researchScopePackB))
		researchContextRequireRefusal(t, err, "unauthorized")
	})
	t.Run("selection_denied_outside_grant_even_with_cross_scope", func(t *testing.T) {
		f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition", "cross_scope"}, []string{"product-a"})
		err := researchContextAuthorizeTx(t, f, rt, inv, nil, researchContextSelection(researchScopePackB))
		researchContextRequireRefusal(t, err, "unauthorized")
	})
	t.Run("allowed_cross_product_with_matching_grant_and_capability", func(t *testing.T) {
		f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition", "cross_scope"}, []string{"product-a", "product-b"})
		if err := researchContextAuthorizeTx(t, f, rt, inv, nil, researchContextSelection(researchScopePackB)); err != nil {
			t.Fatalf("allowed cross-Product selection refused: %v", err)
		}
	})
	t.Run("mixed_allowed_and_denied_selections_reject_whole", func(t *testing.T) {
		f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a"})
		err := researchContextAuthorizeTx(t, f, rt, inv, nil, []string{researchScopePackA, researchScopePackB})
		researchContextRequireRefusal(t, err, "unauthorized")
	})
	t.Run("binding_and_foreign_selection_reject_whole", func(t *testing.T) {
		f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a"})
		err := researchContextAuthorizeTx(t, f, rt, inv, []researchBindingInput{researchContextBinding(researchScopePackA)}, researchContextSelection(researchScopePackB))
		researchContextRequireRefusal(t, err, "unauthorized")
	})
	t.Run("missing_owner_scope", func(t *testing.T) {
		t.Run("absent_pack", func(t *testing.T) {
			f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a"})
			err := researchContextAuthorizeTx(t, f, rt, inv, nil, researchContextSelection("pack-absent"))
			researchContextRequireRefusal(t, err, string(store.KindProjectionNotFound))
		})
		t.Run("membershipless_owner", func(t *testing.T) {
			f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a"})
			pack := "pack-scope-orphan"
			seedResearchScopePack(t, f.store, pack, "owner-none", "Orphan question", "Orphan finding")
			// A missing membership is corrupt retained state, not a legal
			// work creation: isolate the fault after the fixture passes
			// admission, exactly as the retained scope suite does.
			if _, err := f.store.DatabaseForTesting().Exec(`INSERT INTO write_guard(active) VALUES(1); DELETE FROM work_projects WHERE work_id='owner-none'; DELETE FROM write_guard`); err != nil {
				t.Fatal(err)
			}
			err := researchContextAuthorizeTx(t, f, rt, inv, nil, researchContextSelection(pack))
			researchContextRequireRefusal(t, err, "unknown_scope")
		})
	})

	// Decode controls: the shared closed selection shape refuses malformed
	// input before any owner lookup, and can never reduce to an empty owner
	// set. Absent and empty array members select nothing; every other
	// action keeps its retained shape.
	t.Run("malformed_selection_input_refuses", func(t *testing.T) {
		entry := func(body string) string {
			return `{"research_finding_refs":[{"pack_id":"` + researchScopePackA + `","revision":1,"finding_id":"f-1","domain_id":"scope"` + body + `}]}`
		}
		for _, test := range []struct {
			name    string
			action  string
			payload string
		}{
			{"invalid_json", "record_work_context", `{"research_finding_refs":`},
			{"array_payload", "record_work_context", `[]`},
			{"string_payload", "record_work_context", `"invalid"`},
			{"number_payload", "record_work_context", `1`},
			{"null_payload", "record_work_context", `null`},
			{"empty_payload", "record_work_context", ``},
			{"null_selection", "record_work_context", `{"research_finding_refs":null}`},
			{"whitespace_null_selection", "record_work_context", "{\"research_finding_refs\": \n null \t}"},
			{"null_entry", "record_work_context", `{"research_finding_refs":[null]}`},
			{"object_instead_of_array", "record_work_context", `{"research_finding_refs":{"pack_id":"pack-scope-a"}}`},
			{"unknown_member", "record_work_context", entry(`,"revision_extra":2`)},
			{"missing_pack", "record_work_context", `{"research_finding_refs":[{"revision":1,"finding_id":"f-1","domain_id":"scope"}]}`},
			{"empty_pack", "record_work_context", `{"research_finding_refs":[{"pack_id":"","revision":1,"finding_id":"f-1","domain_id":"scope"}]}`},
			{"zero_revision", "record_work_context", `{"research_finding_refs":[{"pack_id":"pack-scope-a","revision":0,"finding_id":"f-1","domain_id":"scope"}]}`},
			{"missing_finding", "record_work_context", `{"research_finding_refs":[{"pack_id":"pack-scope-a","revision":1,"domain_id":"scope"}]}`},
			{"missing_domain", "record_work_context", `{"research_finding_refs":[{"pack_id":"pack-scope-a","revision":1,"finding_id":"f-1"}]}`},
		} {
			t.Run(test.name, func(t *testing.T) {
				packs, err := researchContextSelectionPacks(test.action, json.RawMessage(test.payload))
				if err == nil {
					t.Fatalf("malformed selection %s produced packs %v and no refusal", test.name, packs)
				}
			})
		}
		t.Run("empty_array_selects_nothing", func(t *testing.T) {
			packs, err := researchContextSelectionPacks("record_work_context", json.RawMessage(`{"research_finding_refs":[]}`))
			if err != nil || len(packs) != 0 {
				t.Fatalf("empty selection array packs=%v err=%v, want none", packs, err)
			}
		})
		t.Run("absent_member_selects_nothing", func(t *testing.T) {
			packs, err := researchContextSelectionPacks("record_work_context", json.RawMessage(`{}`))
			if err != nil || len(packs) != 0 {
				t.Fatalf("absent selection member packs=%v err=%v, want none", packs, err)
			}
		})
		t.Run("unrelated_action_ignores_member", func(t *testing.T) {
			for _, payload := range []string{
				`{"research_finding_refs":[{"pack_id":"pack-scope-a","revision":1,"finding_id":"f-1","domain_id":"scope"}]}`,
				`{"research_finding_refs":null}`,
				`[]`,
			} {
				packs, err := researchContextSelectionPacks("record_proposal", json.RawMessage(payload))
				if err != nil || len(packs) != 0 {
					t.Fatalf("unrelated action packs=%v err=%v, want none", packs, err)
				}
			}
		})
	})
	t.Run("normalized_fields_preserve_selected_owner", func(t *testing.T) {
		selection := `[{"pack_id":"pack-scope-b","revision":1,"finding_id":"f-pack-scope-b","domain_id":"scope"}]`
		for _, test := range []struct{ name, fields string }{
			{"object", `{"research_finding_refs":` + selection + `}`},
			{"legacy_field_list", `[{"name":"research_finding_refs","value":` + selection + `}]`},
		} {
			t.Run(test.name, func(t *testing.T) {
				payload, err := workflowActionFields(json.RawMessage(test.fields))
				if err != nil {
					t.Fatal(err)
				}
				packs, err := researchContextSelectionPacks("record_work_context", payload)
				if err != nil || len(packs) != 1 || packs[0] != researchScopePackB {
					t.Fatalf("normalized selection packs=%v err=%v, want [%s]", packs, err, researchScopePackB)
				}
				f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a", "product-b"})
				researchContextRequireRefusal(t, researchContextAuthorizeTx(t, f, rt, inv, nil, packs), "unauthorized")
			})
		}
	})
	t.Run("normalization_refuses_ambiguous_fields", func(t *testing.T) {
		for _, fields := range []string{
			`{"research_finding_refs":[],"research_finding_refs":[]}`,
			`[{"name":"research_finding_refs","value":[]},{"name":"research_finding_refs","value":[]}]`,
			`{"research_finding_refs":[{"pack_id":"pack-scope-a","pack_id":"pack-scope-b"}]}`,
		} {
			if _, err := workflowActionFields(json.RawMessage(fields)); err == nil {
				t.Fatalf("ambiguous fields passed normalization: %s", fields)
			}
		}
	})

	// One referenced-owner set: the binding and selection identity lists
	// deduplicate before the owner lookup, first-seen order kept.
	t.Run("combined_pack_ids_dedupe_first_seen_order", func(t *testing.T) {
		combined := combinedResearchPackIDs(
			[]researchBindingInput{researchContextBinding("pack-b"), researchContextBinding("pack-a")},
			[]string{"pack-a", "pack-c", "pack-b"},
		)
		if len(combined) != 3 || combined[0] != "pack-b" || combined[1] != "pack-a" || combined[2] != "pack-c" {
			t.Fatalf("combined pack identities = %v, want [pack-b pack-a pack-c]", combined)
		}
	})
	t.Run("dedupe_strings_keeps_first_occurrence", func(t *testing.T) {
		kept := dedupeStrings([]string{"owner-a", "owner-b", "owner-a", "owner-c", "owner-b"})
		if len(kept) != 3 || kept[0] != "owner-a" || kept[1] != "owner-b" || kept[2] != "owner-c" {
			t.Fatalf("deduped owners = %v, want [owner-a owner-b owner-c]", kept)
		}
	})
	t.Run("duplicate_references_share_one_owner_set", func(t *testing.T) {
		f, rt, inv := researchContextCallbackFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a"})
		if err := researchContextAuthorizeTx(t, f, rt, inv, []researchBindingInput{researchContextBinding(researchScopePackA)}, []string{researchScopePackA, researchScopePackA}); err != nil {
			t.Fatalf("duplicated in-scope references refused: %v", err)
		}
	})

	// Exact replay: the replay window short-circuits before the action
	// transaction's authorization callback, so an exact replay of an allowed
	// binding-only declaration neither re-runs the owner check nor duplicates
	// its effect,
	// even after the pack owner's scope moved out of the grant. A fresh
	// declaration of the same binding refuses, proving the moved owner would
	// refuse if the replay re-authorized.
	t.Run("exact_replay_no_duplicate_effect", func(t *testing.T) {
		f := researchScopeFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a"})
		input := researchScopeBindingAction(t, f, "record_proposal", "rc-replay-bind", []map[string]any{researchScopeBinding(researchScopePackA, 1, true)})
		first := researchScopeInvoke(t, f, "workflow_action", input)
		if first.Outcome != OutcomeOK {
			t.Fatalf("allowed declaration failed: %+v", first.Error)
		}
		researchScopeMoveWorkToProjectB(t, f.store, researchScopeOwnerA)
		afterMove := researchScopeSnapshot(t, f.store, researchScopePackA)
		replay := researchScopeInvoke(t, f, "workflow_action", input)
		if replay.Outcome != OutcomeOK || !replay.Replayed || !bytes.Equal(first.Result, replay.Result) {
			t.Fatalf("exact replay outcome=%s replayed=%v error=%+v", replay.Outcome, replay.Replayed, replay.Error)
		}
		researchScopeAssertNoEffects(t, afterMove, researchScopeSnapshot(t, f.store, researchScopePackA))
		fresh := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_work_context", "rc-replay-fresh", []map[string]any{researchScopeBinding(researchScopePackA, 1, true)}))
		researchScopeAssertRefused(t, fresh, "unauthorized", researchScopeMarkerA)
	})

	// CD-0216 D3: the shared context reader does not reauthorize retained
	// selections against the original author's grant. After the authoring
	// client's grant becomes unavailable, the store read the work pin and
	// continuity share still returns the retained entries: the bound
	// selection resolves with its native content, and the unbound foreign
	// selection reads as the typed fail-closed missing entry.
	t.Run("retained_reads_survive_unavailable_author_grant", func(t *testing.T) {
		f := researchScopeFixture(t, []Capability{"product_read", "work_transition"}, []string{"product-a"})
		bound := researchScopeInvoke(t, f, "workflow_action", researchScopeBindingAction(t, f, "record_proposal", "rc-retained-bind", []map[string]any{researchScopeBinding(researchScopePackA, 1, true)}))
		if bound.Outcome != OutcomeOK {
			t.Fatalf("in-scope binding failed: %+v", bound.Error)
		}
		researchContextDeclareRetainedSelection(t, f, []string{researchScopePackA, researchScopePackB})
		policy := store.TrustedClientRecord{ClientRef: "client-1", Status: "active", PrincipalRef: "human-1", CapabilitiesJSON: `[]`, ProductScopeJSON: `[]`, ProjectScopeJSON: `[]`, AgentScopeJSON: `["agent-1"]`}
		if err := f.store.UpdateTrustedClientPolicy(context.Background(), "client-1", policy, fixedTime().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		pin, err := store.ReadWorkPin(context.Background(), f.store, researchScopeConsumer)
		if err != nil {
			t.Fatalf("retained read refused after the author grant became unavailable: %v", err)
		}
		if pin.WorkContext == nil || len(pin.WorkContext.ResearchFindings) != 2 {
			t.Fatalf("retained read lost the selections: %+v", pin.WorkContext)
		}
		resolved := pin.WorkContext.ResearchFindings[0]
		if resolved.Selection.PackID != researchScopePackA || resolved.Selection.FindingID != "f-"+researchScopePackA || resolved.Selection.Revision != 1 {
			t.Fatalf("bound entry lost its selection references: %+v", resolved.Selection)
		}
		if resolved.State != store.WorkContextResearchStateResolved || !resolved.Required || resolved.Finding == nil || resolved.Sources == nil || len(*resolved.Sources) != 0 {
			t.Fatalf("bound entry state=%q required=%t finding=%v sources=%v, want resolved, required, native finding, empty sources", resolved.State, resolved.Required, resolved.Finding, resolved.Sources)
		}
		missing := pin.WorkContext.ResearchFindings[1]
		if missing.Selection.PackID != researchScopePackB || missing.State != store.WorkContextResearchStateMissing || !missing.Required || missing.Finding != nil || missing.Sources != nil {
			t.Fatalf("unbound entry state=%q required=%t finding=%v sources=%v, want the typed fail-closed missing entry", missing.State, missing.Required, missing.Finding, missing.Sources)
		}
	})
}
