package store

// Contract writes preserve the exact decision-domain fact and version-bound
// confirmation attribution, both at initial approval and at ordinary revision.

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func authoredDecisionDomainSets() [][]string {
	classes := []string{"increased_spending", "legal", "product", "production_data", "security", "ui_ux"}
	sets := make([][]string, 0, 1<<len(classes))
	for mask := 0; mask < 1<<len(classes); mask++ {
		set := []string{}
		for index, class := range classes {
			if mask&(1<<index) != 0 {
				set = append(set, class)
			}
		}
		sets = append(sets, set)
	}
	return sets
}

func TestDecisionFactWriteBoundary(t *testing.T) {
	t.Parallel()

	t.Run("all canonical decision facts persist exact event and stored bytes on genuine initial writes", func(t *testing.T) {
		t.Parallel()
		for _, set := range authoredDecisionDomainSets() {
			workID := "decision-fact-initial-" + factWriteSetLabel(set)
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			// The genuine local selection starts before the initial contract:
			// the prefix folds the contract as the work's first one, carrying
			// the exact fact at the current event payload version.
			initialVersion := readWorkVersion(t, s, workID)
			advanceResolutionPrefix(t, s, registry, workID, owner, set, false)
			ownerRef := ownerRefForTest(t, owner)
			expected := workflowJSON(set)
			if stored := storedDecisionDomains(t, s, workID, 1); stored != expected {
				t.Fatalf("the initial contract for %s stored decision_domains %q, want the exact canonical bytes %q", expected, stored, expected)
			}
			wantEvent := resolutionEvent("resolution-contract-"+workID, WorkflowContractApproved, workID, ownerRef, initialVersion+3, map[string]any{
				"contract_version": 1, "premise": "deliver the checked change", "outcome_kind": "check",
				"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
				"required_evidence": []string{"verification"}, "route_conventions": []string{}, "spec_mandate": []string{},
				"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite", "decision_domains": set,
			})
			wantDigest, err := effectPayloadDigest(wantEvent.Payload)
			if got := factWriteEventDigestByID(t, s, wantEvent.EventID); err != nil || got != wantDigest {
				t.Fatalf("initial contract receipt digest=%q, want exact declared fact %s in %q, err=%v", got, expected, wantDigest, err)
			}
			assertFactWriteProjectedDomains(t, s, registry, workID, 1, set)
			var rowRef, rowDigest, approvedBy string
			if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest,approved_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&rowRef, &rowDigest, &approvedBy); err != nil {
				t.Fatal(err)
			}
			if rowRef != registered.Definition.Ref || rowDigest != registered.Digest {
				t.Fatalf("the initial contract pinned %s %s, want the registered %s %s", rowRef, rowDigest, registered.Definition.Ref, registered.Digest)
			}
			if approvedBy != ownerRef {
				t.Fatalf("the initial contract recorded approved_by %q, want the authoring owner %s", approvedBy, ownerRef)
			}
			s.Close()
		}
	})

	t.Run("missing null and broken initial facts refuse typed with no contract event version or guard effect", func(t *testing.T) {
		t.Parallel()
		for _, testCase := range []struct {
			name     string
			fact     any
			present  bool
			wantKind FailureKind
			required bool
		}{
			{name: "nonstring string", fact: "product", present: true, wantKind: KindInvalidPayload},
			{name: "nonstring object", fact: map[string]any{"product": true}, present: true, wantKind: KindInvalidPayload},
			{name: "unknown member", fact: []string{"preference", "economics"}, present: true, wantKind: KindInvalidPayload},
			{name: "duplicate member", fact: []string{"product", "product"}, present: true, wantKind: KindInvalidPayload},
			{name: "noncanonical order", fact: []string{"product", "preference"}, present: true, wantKind: KindInvalidPayload},
			{name: "over the closed set", fact: []string{"preference", "product", "ui_ux", "preference"}, present: true, wantKind: KindInvalidPayload},
			// Neither missing nor null declares an engineering fact.
			{name: "missing member", present: false, required: true},
			{name: "explicit null", fact: nil, present: true, required: true},
		} {
			workID := "decision-fact-initial-refuse-" + strings.ReplaceAll(testCase.name, " ", "-")
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			baselineVersion := readWorkVersion(t, s, workID)
			baselineEvents := approvalEventCount(t, s, workID)
			event := factWriteInitialContractEvent(t, s, workID, ownerRefForTest(t, owner), testCase.fact, testCase.present)
			operation := Operation{Events: []Event{event}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baselineVersion}}
			err := tryAppendWorkflowEventsWithRegistry(s, registry, operation)
			var failure *Failure
			if testCase.required {
				if err == nil {
					s.Close()
					t.Fatalf("the %s initial contract under the consequence_v1 pin was accepted without any typed refusal; required decision-fact presence must refuse it before any write", testCase.name)
				}
				if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload || !strings.Contains(failure.Detail, "absence is a legacy shape") {
					s.Close()
					t.Fatalf("the %s initial contract returned %v, want a typed refusal", testCase.name, err)
				}
			} else if err == nil || !failureAs(err, &failure) || failure.Kind != testCase.wantKind {
				s.Close()
				t.Fatalf("the %s initial contract returned %v, want the typed %s refusal", testCase.name, err, testCase.wantKind)
			}
			assertFactWriteInitialNoEffect(t, s, workID, baselineVersion, baselineEvents)
			s.Close()
		}
	})

	t.Run("all canonical decision facts persist on ordinary updates without mutating the predecessor", func(t *testing.T) {
		t.Parallel()
		for _, set := range authoredDecisionDomainSets() {
			workID := "decision-fact-successor-write-" + factWriteSetLabel(set)
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			advanceResolutionPrefix(t, s, registry, workID, owner, []string{}, false)
			ownerRef := ownerRefForTest(t, owner)
			baselineVersion := readWorkVersion(t, s, workID)
			predecessorPayload := factWriteEventDigestByID(t, s, "resolution-contract-"+workID)
			event := factWriteContractUpdateEvent(t, s, workID, ownerRef, set, true)
			if err := factWriteApplyUpdateEvent(t, s, registry, event); err != nil {
				t.Fatal(err)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(event.Payload, &envelope); err != nil {
				t.Fatal(err)
			}
			if member, present := factWritePayloadMember(t, string(envelope["contract"])); !present || member != workflowJSON(set) {
				t.Fatalf("ordinary update input fact=%q, present=%v; want %s", member, present, workflowJSON(set))
			}
			if want, err := effectPayloadDigest(event.Payload); err != nil || factWriteEventDigestByID(t, s, event.EventID) != want {
				t.Fatalf("ordinary update receipt did not preserve submitted bytes: %v", err)
			}
			expected := workflowJSON(set)
			if stored := storedDecisionDomains(t, s, workID, 2); stored != expected {
				t.Fatalf("the complete successor for %s stored decision_domains %q, want the exact canonical bytes %q", expected, stored, expected)
			}
			if stored := storedDecisionDomains(t, s, workID, 1); stored != "[]" {
				t.Fatalf("the superseded predecessor for %s stored %q, want its recorded [] fact immutable", expected, stored)
			}
			var supersededBy int64
			if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil {
				t.Fatal(err)
			}
			if supersededBy != 2 {
				t.Fatalf("the superseded predecessor for %s names superseded_by=%d, want the successor version 2", expected, supersededBy)
			}
			if factWriteEventDigestByID(t, s, "resolution-contract-"+workID) != predecessorPayload {
				t.Fatalf("the successor fold for %s rewrote the predecessor contract event bytes", expected)
			}
			assertFactWriteProjectedDomains(t, s, registry, workID, 2, set)
			if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
				t.Fatalf("the successor fold for %s left the work at version %d, want the one derived advance from %d", expected, version, baselineVersion)
			}
			s.Close()
		}
	})

	t.Run("broken successor facts refuse atomically beside a live confirmation", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-broken-successor"
		s, registry, owner, baselineVersion := seedFactWriteConfirmedEngineering(t, workID)
		ownerRef := ownerRefForTest(t, owner)
		for _, fact := range []struct {
			name  string
			value any
		}{
			{name: "unknown member", value: []string{"preference", "economics"}},
			{name: "duplicate member", value: []string{"product", "product"}},
			{name: "noncanonical order", value: []string{"product", "preference"}},
			{name: "over the closed set", value: []string{"preference", "product", "ui_ux", "preference"}},
			{name: "nonstring string", value: "product"},
			{name: "nonstring object", value: map[string]any{"ui_ux": true}},
		} {
			baselineEvents := approvalEventCount(t, s, workID)
			event := factWriteContractUpdateEvent(t, s, workID, ownerRef, fact.value, true)
			err := factWriteApplyUpdateEvent(t, s, registry, event)
			var failure *Failure
			if err == nil || !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Fatalf("the successor with the %s fact returned %v, want the typed invalid-payload refusal", fact.name, err)
			}
			assertFactWritePredecessorPreserved(t, s, workID, baselineVersion, baselineEvents, ownerRef)
		}
	})

	t.Run("a missing registry refuses a complete successor before any write", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-wrong-pin"
		definition := approvalRouteDefinition(t)
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		advanceResolutionPrefix(t, s, registry, workID, owner, []string{}, false)
		ownerRef := ownerRefForTest(t, owner)
		baselineVersion := readWorkVersion(t, s, workID)
		baselineEvents := approvalEventCount(t, s, workID)
		event := factWriteContractUpdateEvent(t, s, workID, ownerRef, []string{"product"}, true)
		// The empty registry carries nothing that could fall back to the builtin.
		missingErr := factWriteApplyUpdateEvent(t, s, NewWorkflowDefinitionRegistry(), event)
		var missingFailure *Failure
		if missingErr == nil || !failureAs(missingErr, &missingFailure) {
			t.Fatalf("the complete successor under a registry missing the pinned definition returned %v, want a typed refusal", missingErr)
		}
		if contracts := decisionContractCount(t, s, workID); contracts != 1 {
			t.Fatalf("the refused successor writes left %d contract rows, want the active predecessor alone", contracts)
		}
		if stored := storedDecisionDomains(t, s, workID, 1); stored != "[]" {
			t.Fatalf("the refused successor writes changed the predecessor fact to %q, want [] preserved", stored)
		}
		var superseded any
		if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&superseded); err != nil {
			t.Fatal(err)
		}
		if superseded != nil {
			t.Fatalf("the refused successor writes named superseded_by %v, want the predecessor left active", superseded)
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion {
			t.Fatalf("the refused successor writes advanced the work version to %d", version)
		}
		if events := approvalEventCount(t, s, workID); events != baselineEvents {
			t.Fatalf("the refused successor writes left %d events (baseline %d), want rollback with no append", events, baselineEvents)
		}
		var pinRef, pinDigest string
		if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest FROM workflow_instances WHERE work_id=?`, workID).Scan(&pinRef, &pinDigest); err != nil {
			t.Fatal(err)
		}
		if pinRef != registered.Definition.Ref || pinDigest != registered.Digest {
			t.Fatalf("the refused successor writes moved the pin to %s %s, want the registered %s %s", pinRef, pinDigest, registered.Definition.Ref, registered.Digest)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("engineering to decision revision over a confirmed predecessor preserves attribution and refuses stale authority", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-revise-e2d"
		s, registry, definition, owner := seedApprovalItemAtAcceptance(t, workID, []string{})
		ownerRef := ownerRefForTest(t, owner)
		if _, err := confirmResolutionPremise(t, s, registry, definition, workID, owner, nil, nil); err != nil {
			t.Fatalf("the executing coordinator's engineering confirmation refused: %v", err)
		}
		if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != ownerRef {
			t.Fatalf("the engineering confirmation row = %q (present %v), want the executing coordinator %s", confirmed, ok, ownerRef)
		}
		confirmID := factWriteLatestEventID(t, s, workID, WorkflowPremiseConfirmed)
		confirmEvent := factWriteEventDigestByID(t, s, confirmID)
		confirmActor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed)
		contractID := factWriteLatestEventID(t, s, workID, WorkflowContractApproved)
		contractEvent := factWriteEventDigestByID(t, s, contractID)
		baselineVersion := readWorkVersion(t, s, workID)
		if err := factWriteApplyUpdateEvent(t, s, registry, factWriteContractUpdateEvent(t, s, workID, ownerRef, []string{"product"}, true)); err != nil {
			t.Fatal(err)
		}
		if stored := storedDecisionDomains(t, s, workID, 2); stored != `["product"]` {
			t.Fatalf("the decision successor stored decision_domains %q, want the exact [\"product\"] fact", stored)
		}
		if stored := storedDecisionDomains(t, s, workID, 1); stored != "[]" {
			t.Fatalf("the superseded engineering predecessor stored %q, want its recorded [] fact immutable", stored)
		}
		var supersededBy int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&supersededBy); err != nil {
			t.Fatal(err)
		}
		if supersededBy != 2 {
			t.Fatalf("the successful revision names superseded_by=%d, want the successor version 2", supersededBy)
		}
		if factWriteEventDigestByID(t, s, contractID) != contractEvent {
			t.Fatal("the revision rewrote the predecessor contract event bytes")
		}
		if factWriteEventDigestByID(t, s, confirmID) != confirmEvent {
			t.Fatal("the revision rewrote the predecessor confirmation event bytes")
		}
		if actor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed); actor != confirmActor {
			t.Fatalf("the revision moved the confirmation attribution to %q, want %s preserved", actor, confirmActor)
		}
		if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != ownerRef {
			t.Fatalf("the revision moved the confirmation row to %q (present %v), want the executing coordinator preserved", confirmed, ok)
		}
		if _, inherited := approvalPremiseConfirmationRow(t, s, workID, 2); inherited {
			t.Fatal("the decision successor inherited a confirmation row without a fresh confirmation")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
			t.Fatalf("the revision left the work at version %d, want the one derived advance from %d", version, baselineVersion)
		}
		// A confirmation naming the superseded predecessor version refuses
		// through the real constructor seam before any fold.
		_, staleVersionErr := confirmResolutionPremise(t, s, registry, definition, workID, owner, nil, map[string]json.RawMessage{"contract_version": json.RawMessage(`1`)})
		var staleVersionFailure *Failure
		if staleVersionErr == nil || !failureAs(staleVersionErr, &staleVersionFailure) || staleVersionFailure.Kind != KindInvalidPayload || !strings.Contains(staleVersionFailure.Detail, "supersession invalidated") {
			t.Fatalf("the stale-version confirmation returned %v, want the supersession-invalidated refusal", staleVersionErr)
		}
		if _, inherited := approvalPremiseConfirmationRow(t, s, workID, 2); inherited {
			t.Fatal("the read-only stale-authority checks recorded a successor confirmation")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
			t.Fatalf("the stale-authority checks advanced the work version to %d", version)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("decision to engineering revision over a confirmed predecessor preserves attribution and refuses stale authority", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-revise-d2e"
		s, registry, definition, owner := seedApprovalItemAtAcceptance(t, workID, []string{"product"})
		base := factWriteActiveContract(t, s, workID)
		if _, err := confirmResolutionPremise(t, s, registry, definition, workID, owner, nil, nil); err != nil {
			t.Fatalf("the executing coordinator's decision confirmation refused: %v", err)
		}
		if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, base); !ok || confirmed != ownerRefForTest(t, owner) {
			t.Fatalf("the decision confirmation row = %q (present %v), want the executing coordinator %s", confirmed, ok, ownerRefForTest(t, owner))
		}
		confirmID := factWriteLatestEventID(t, s, workID, WorkflowPremiseConfirmed)
		confirmEvent := factWriteEventDigestByID(t, s, confirmID)
		confirmActor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed)
		contractID := factWriteLatestEventID(t, s, workID, WorkflowContractApproved)
		contractEvent := factWriteEventDigestByID(t, s, contractID)
		baselineVersion := readWorkVersion(t, s, workID)
		if err := factWriteApplyUpdateEvent(t, s, registry, factWriteContractUpdateEvent(t, s, workID, ownerRefForTest(t, owner), []string{}, true)); err != nil {
			t.Fatal(err)
		}
		if stored := storedDecisionDomains(t, s, workID, base+1); stored != "[]" {
			t.Fatalf("the engineering successor stored decision_domains %q, want the exact [] fact", stored)
		}
		if stored := storedDecisionDomains(t, s, workID, base); stored != `["product"]` {
			t.Fatalf("the superseded decision predecessor stored %q, want its recorded fact immutable", stored)
		}
		var supersededBy int64
		if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=?`, workID, base).Scan(&supersededBy); err != nil {
			t.Fatal(err)
		}
		if supersededBy != base+1 {
			t.Fatalf("the successful revision names superseded_by=%d, want the successor version %d", supersededBy, base+1)
		}
		if factWriteEventDigestByID(t, s, contractID) != contractEvent {
			t.Fatal("the revision rewrote the predecessor contract event bytes")
		}
		if factWriteEventDigestByID(t, s, confirmID) != confirmEvent {
			t.Fatal("the revision rewrote the predecessor confirmation event bytes")
		}
		if actor := latestApprovalEventActor(t, s, workID, WorkflowPremiseConfirmed); actor != confirmActor {
			t.Fatalf("the revision moved the confirmation attribution to %q, want %s preserved", actor, confirmActor)
		}
		if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, base); !ok || confirmed != ownerRefForTest(t, owner) {
			t.Fatalf("the revision moved the confirmation row to %q (present %v), want the executing coordinator preserved", confirmed, ok)
		}
		if _, inherited := approvalPremiseConfirmationRow(t, s, workID, base+1); inherited {
			t.Fatal("the engineering successor inherited the predecessor's confirmation row")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
			t.Fatalf("the revision left the work at version %d, want the one derived advance from %d", version, baselineVersion)
		}
		// A confirmation naming the superseded predecessor version refuses
		// through the real constructor seam before any fold.
		_, staleVersionErr := confirmResolutionPremise(t, s, registry, definition, workID, owner, nil, map[string]json.RawMessage{"contract_version": json.RawMessage(strconv.FormatInt(base, 10))})
		var staleVersionFailure *Failure
		if staleVersionErr == nil || !failureAs(staleVersionErr, &staleVersionFailure) || staleVersionFailure.Kind != KindInvalidPayload || !strings.Contains(staleVersionFailure.Detail, "supersession invalidated") {
			t.Fatalf("the stale-version confirmation returned %v, want the supersession-invalidated refusal", staleVersionErr)
		}
		if _, inherited := approvalPremiseConfirmationRow(t, s, workID, base+1); inherited {
			t.Fatal("the read-only stale-authority checks recorded a successor confirmation")
		}
		if version := readWorkVersion(t, s, workID); version != baselineVersion+1 {
			t.Fatalf("the stale-authority checks advanced the work version to %d", version)
		}
		assertNoFoldGuardRows(t, s)
	})

	t.Run("the copied decision-fact declaration selects on the new policy and keeps every legacy declaration byte-identical", func(t *testing.T) {
		t.Parallel()

		declaredApprove := currentActionDefinition("approve_contract", true)
		declaredApprove.Payload.Fields = append(append(append([]WorkflowPayloadField{}, declaredApprove.Payload.Fields...), workflowLawChangeFields()...), workflowDecisionFactField())
		newPolicy := WorkflowDefinition{
			ActionDefinitions: []WorkflowActionDefinition{declaredApprove}}
		fact := workflowDecisionFactField()
		if fact.Name != "decision_domains" || fact.ValueType != PayloadStringList || !fact.Required {
			t.Fatalf("the copied decision-fact field = %+v, want the required decision_domains string list", fact)
		}
		if fact.MinItems == nil || *fact.MinItems != 0 || fact.MaxItems == nil || *fact.MaxItems != int64(len(workflowDecisionDomainSet)) {
			t.Fatalf("the copied decision-fact bounds = %v..%v, want 0..%d", fact.MinItems, fact.MaxItems, len(workflowDecisionDomainSet))
		}
		if !reflect.DeepEqual(fact.Enum, workflowDecisionDomainSet) {
			t.Fatalf("the copied decision-fact enum = %v, want the canonical closed set %v", fact.Enum, workflowDecisionDomainSet)
		}

		// Declaration shape: each contract-authoring declaration appends the
		// one fact field and keeps every declared field in order. The initial
		// selection answers from the pinned closed action, never from the
		// global builtin policy.
		initial, initialErr := workflowDecisionFactInitialActionDefinition(newPolicy)
		if initialErr != nil {
			t.Fatalf("the initial declaration selection refused the pinned closed action: %v", initialErr)
		}
		update := WorkflowContractUpdatePayload(newPolicy)
		for _, selected := range []struct {
			name    string
			payload WorkflowPayloadDefinition
			builtin []WorkflowPayloadField
		}{
			{name: "the initial approve_contract declaration", payload: initial.Payload, builtin: builtinActionPolicies["approve_contract"].Payload.Fields},
			{name: "the contract update declaration", payload: update, builtin: BuiltinWorkflowContractUpdateFields()},
		} {
			fields := selected.payload.Fields
			if !reflect.DeepEqual(fields, factWriteExpectedSelection(selected.builtin)) {
				t.Fatalf("%s fields differ from the released fields plus the law-change inputs and canonical decision fact", selected.name)
			}
			for index, field := range selected.builtin {
				if !reflect.DeepEqual(fields[index], field) {
					t.Fatalf("%s altered declared field %d (%s)", selected.name, index, field.Name)
				}
			}
			appended := fields[len(fields)-1]
			if appended.Name != "decision_domains" || !appended.Required {
				t.Fatalf("%s appends %s (required %v), want the required decision_domains fact last", selected.name, appended.Name, appended.Required)
			}
		}

		// Empty-list bounds and the closed enum through the shared payload
		// value validator: the empty engineering list stays legal and every
		// member answers to the canonical set.
		factField := update.Fields[len(update.Fields)-1]
		for _, probe := range []struct {
			raw  string
			want bool
		}{
			{`[]`, true},
			{`["legal", "product"]`, true},
			{`["increased_spending", "legal", "product", "production_data", "security", "ui_ux"]`, true},
			{`["preference"]`, false},
			{`null`, false},
			{`"product"`, false},
			{`["economics"]`, false},
			{`["preference", "product", "ui_ux", "preference"]`, false},
		} {
			if got := validateWorkflowPayloadValue(factField, json.RawMessage(probe.raw)); got != probe.want {
				t.Fatalf("the decision-fact declaration answers %s with %v, want %v", probe.raw, got, probe.want)
			}
		}

		// Fresh backing storage: the selected field list never aliases the
		// builtin policy slice, and the fact enum never aliases the canonical
		// set. The alias probe runs on its own selected field, so the shared
		// enum clone the checks below read stays canonical.
		builtinFields := builtinActionPolicies["approve_contract"].Payload.Fields
		if reflect.ValueOf(builtinFields).Pointer() == reflect.ValueOf(initial.Payload.Fields).Pointer() {
			t.Fatal("the selected initial declaration shares the builtin policy backing storage")
		}
		aliasProbe := WorkflowContractUpdatePayload(newPolicy).Fields[len(update.Fields)-1]
		aliasProbe.Enum[0] = "mutated"
		if workflowDecisionDomainSet[0] != "increased_spending" {
			t.Fatalf("the fact enum aliases the canonical set: %v", workflowDecisionDomainSet)
		}

		// Legacy declaration identity: a legacy pin resolves the initial
		// declaration byte-identically to its own pinned action, and a
		// definition that declares no initial action answers the zero action
		// with no error instead of the global builtin declaration.
		legacyPinned := WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{currentActionDefinition("approve_contract", true)}}
		legacy, legacyErr := workflowDecisionFactInitialActionDefinition(legacyPinned)
		if legacyErr != nil || !reflect.DeepEqual(legacy, currentActionDefinition("approve_contract", true)) {
			t.Fatalf("the legacy initial declaration = %+v (err %v), want the pinned legacy action unchanged", legacy, legacyErr)
		}
		absent, absentErr := workflowDecisionFactInitialActionDefinition(WorkflowDefinition{})
		if absentErr != nil || !reflect.DeepEqual(absent, WorkflowActionDefinition{}) {
			t.Fatalf("the absent initial selection = %+v (err %v), want the zero action with no fabricated declaration", absent, absentErr)
		}
		if legacy := WorkflowContractUpdatePayload(WorkflowDefinition{}); !legacy.Closed || !reflect.DeepEqual(legacy.Fields, BuiltinWorkflowContractUpdateFields()) {
			t.Fatalf("legacy contract update declaration = %+v, want the closed base fields", legacy)
		}

		// The update declaration requires the explicit predicate array and
		// the policy's canonical fact, with no paired outcome fields.
		canonical := update.Fields
		var canonicalFact *WorkflowPayloadField
		for index := range canonical {
			switch canonical[index].Name {
			case "decision_domains":
				canonicalFact = &canonical[index]
			case "outcome_kind", "outcome_payload":
				t.Fatalf("the canonical projection kept the paired authoring field %s", canonical[index].Name)
			}
		}
		if canonicalFact == nil {
			t.Fatal("the canonical projection dropped the copied decision-fact declaration")
		}
		if !canonicalFact.Required || !reflect.DeepEqual(canonicalFact.Enum, workflowDecisionDomainSet) {
			t.Fatalf("the canonical projection restated the fact as %+v, want the owner's required closed declaration", *canonicalFact)
		}
		for _, field := range canonical {
			if field.Name == "outcome_predicates" && !field.Required {
				t.Fatal("the canonical projection left outcome_predicates optional")
			}
		}

		// Unrelated actions and policy-independent base fields carry no fact.
		for _, actionID := range []string{"record_verdict", "confirm_premise", "record_alignment"} {
			for _, field := range currentActionDefinition(actionID, true).Payload.Fields {
				if field.Name == "decision_domains" {
					t.Fatalf("the unrelated action %s declares the decision fact", actionID)
				}
			}
		}
		for _, field := range BuiltinWorkflowContractUpdateFields() {
			if field.Name == "decision_domains" {
				t.Fatal("the base contract update fields carry a policy-specific decision fact")
			}
		}
		for _, action := range BuiltinWorkflowRecoveryActionDefinitions() {
			for _, field := range action.Payload.Fields {
				if field.Name == "decision_domains" {
					t.Fatalf("the legacy canonical publication for %s carries an undeclared decision fact", action.ID)
				}
			}
		}
	})

	t.Run("submitted JSON null stays absent regardless of whitespace", func(t *testing.T) {
		definition := approvalRouteDefinition(t)
		for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(" \nnull\t")} {
			_, err := workflowSubmittedDecisionDomains(definition, map[string]json.RawMessage{"decision_domains": raw})
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Fatalf("submitted decision_domains %q returned %v, want invalid_payload", raw, err)
			}
		}
	})

	t.Run("the initial approve constructor emits and folds every canonical decision fact exactly", func(t *testing.T) {
		t.Parallel()
		for _, set := range authoredDecisionDomainSets() {
			workID := "decision-fact-initial-ctor-" + factWriteSetLabel(set)
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			ownerRef := ownerRefForTest(t, owner)
			baseline := readWorkVersion(t, s, workID)
			events, constructErr := factWriteConstructInitialContract(t, s, definition, workID, ownerRef, factWriteApproveFields(t, workID, set, true), baseline)
			if constructErr != nil {
				s.Close()
				t.Fatalf("the initial approve constructor for %s refused the complete decision fact: %v", workflowJSON(set), constructErr)
			}
			if len(events) != 1 {
				s.Close()
				t.Fatalf("the initial approve constructor for %s returned %d events, want the one contract event", workflowJSON(set), len(events))
			}
			expected := workflowJSON(set)
			if member, present := factWritePayloadMember(t, string(events[0].Payload)); !present || member != expected {
				s.Close()
				t.Fatalf("the initial approve constructor event for %s recorded decision_domains %q (present %v), want the exact canonical member %q", workflowJSON(set), member, present, expected)
			}
			appendWorkflowEventsWithRegistry(t, s, registry, Operation{
				Events:           events,
				ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): baseline},
			})
			if stored := storedDecisionDomains(t, s, workID, 1); stored != expected {
				t.Fatalf("the folded initial contract for %s stored decision_domains %q, want the exact canonical bytes %q", expected, stored, expected)
			}
			assertFactWriteProjectedDomains(t, s, registry, workID, 1, set)
			s.Close()
		}
	})

	t.Run("the initial approve constructor refuses absent and broken decision facts typed before any effect", func(t *testing.T) {
		t.Parallel()
		for _, factCase := range factWriteBrokenFacts() {
			workID := "decision-fact-initial-ctor-refuse-" + strings.ReplaceAll(factCase.name, " ", "-")
			definition := approvalRouteDefinition(t)
			registry, registered := newResolutionRegistry(t, definition)
			s, owner := seedResolutionSelection(t, workID, registry, registered)
			ownerRef := ownerRefForTest(t, owner)
			baselineVersion := readWorkVersion(t, s, workID)
			baselineEvents := approvalEventCount(t, s, workID)
			_, constructErr := factWriteConstructInitialContract(t, s, definition, workID, ownerRef, factWriteApproveFields(t, workID, factCase.fact, factCase.present), baselineVersion)
			var failure *Failure
			if constructErr == nil || !failureAs(constructErr, &failure) || failure.Kind != KindInvalidPayload {
				s.Close()
				t.Fatalf("the initial approve constructor with the %s fact returned %v, want the typed invalid-payload refusal", factCase.name, constructErr)
			}
			assertFactWriteInitialNoEffect(t, s, workID, baselineVersion, baselineEvents)
			s.Close()
		}
	})

	t.Run("ordinary updates refuse missing and null facts beside the confirmed predecessor", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-successor-absent"
		s, registry, owner, baselineVersion := seedFactWriteConfirmedEngineering(t, workID)
		ownerRef := ownerRefForTest(t, owner)
		for _, factCase := range []struct {
			name    string
			fact    any
			present bool
		}{
			{name: "missing member", present: false},
			{name: "explicit null", fact: nil, present: true},
		} {
			baselineEvents := approvalEventCount(t, s, workID)
			event := factWriteContractUpdateEvent(t, s, workID, ownerRef, factCase.fact, factCase.present)
			err := factWriteApplyUpdateEvent(t, s, registry, event)
			var failure *Failure
			if err == nil {
				t.Fatalf("the %s successor folded with no typed refusal; an omission must not bypass the constructor's required decision fact", factCase.name)
			}
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Fatalf("the %s successor returned %v, want the typed absent-fact refusal", factCase.name, err)
			}
			assertFactWritePredecessorPreserved(t, s, workID, baselineVersion, baselineEvents, ownerRef)
		}
	})

	t.Run("the declaration consumers agree with the decision-fact owner", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-consumer-new-policy"
		s, registry, definition, _ := seedApprovalItemAtAcceptance(t, workID, []string{})
		ctx := context.Background()

		// The dispatch answer must come from the pin itself, never from the
		// selector under test or the builtin policy.
		pinnedInitial := workflowActionDefinitionByID(definition, "approve_contract")
		expectedInitial := pinnedInitial
		// The composed head declares the fact; the selection keeps it in
		// place and appends the law-change inputs it does not declare.
		expectedInitial.Payload.Fields = append(slices.Clone(pinnedInitial.Payload.Fields), workflowLawChangeFields()...)
		_, initialAction, err := WorkflowActionDefinitionFor(ctx, s, registry, workID, "approve_contract")
		if err != nil {
			t.Fatalf("dispatch refused the declared initial action: %v", err)
		}
		if !reflect.DeepEqual(initialAction, expectedInitial) {
			t.Fatalf("dispatch resolved the declared initial action %+v, want the pinned declaration with the law-change inputs appended", initialAction)
		}
		assertFactWriteFieldsCarryFact(t, "the contract update declaration", WorkflowContractUpdatePayload(definition).Fields, BuiltinWorkflowContractUpdateFields())

		// Closed payload validation admits the new-policy initial action only
		// with the required fact and refuses every absent or broken shape.
		completeRaw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, []string{"product"}, true))
		if err := validateWorkflowActionPayload(definition, "approve_contract", completeRaw); err != nil {
			t.Fatalf("closed validation refused the initial action payload carrying the required fact: %v", err)
		}
		missingRaw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, nil, false))
		var payloadFailure *Failure
		if err := validateWorkflowActionPayload(definition, "approve_contract", missingRaw); err == nil || !failureAs(err, &payloadFailure) || payloadFailure.Kind != KindInvalidPayload {
			t.Fatalf("closed validation answered %v for the initial action payload missing the required fact, want the typed invalid-payload refusal", err)
		}
		brokenRaw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, []string{"product", "product"}, true))
		if err := validateWorkflowActionPayload(definition, "approve_contract", brokenRaw); err == nil {
			t.Fatal("closed validation accepted an initial action payload with a duplicate fact member")
		}
		unorderedRaw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, []string{"product", "preference"}, true))
		if err := validateWorkflowActionPayload(definition, "approve_contract", unorderedRaw); err == nil {
			t.Fatal("closed validation accepted an initial action payload with a noncanonical fact order")
		}

	})

	t.Run("the optional contract update input validates decision facts at schema and store boundaries", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-update-input"
		s, registry, owner, baselineVersion := seedFactWriteConfirmedEngineering(t, workID)
		ownerRef := ownerRefForTest(t, owner)
		inputFor := func(event Event) map[string]any {
			var input map[string]any
			if err := json.Unmarshal(event.Payload, &input); err != nil {
				t.Fatal(err)
			}
			delete(input, "resulting_version")
			input["work_id"], input["idempotency_key"] = workID, "decision-fact-update-input"
			return input
		}
		for _, factCase := range factWriteBrokenFacts() {
			event := factWriteContractUpdateEvent(t, s, workID, ownerRef, factCase.fact, factCase.present)
			if err := payloadschema.Validate("work_define_revise_input", factWriteAuthoringJSON(t, inputFor(event))); err == nil {
				t.Fatalf("update schema admitted %s decision fact", factCase.name)
			}
			baselineEvents := approvalEventCount(t, s, workID)
			if err := factWriteApplyUpdateEvent(t, s, registry, event); !hasFailureKind(err, KindInvalidPayload) {
				t.Fatalf("store update with %s decision fact = %v, want invalid_payload", factCase.name, err)
			}
			assertFactWritePredecessorPreserved(t, s, workID, baselineVersion, baselineEvents, ownerRef)
		}
		event := factWriteContractUpdateEvent(t, s, workID, ownerRef, []string{"ui_ux"}, true)
		input := inputFor(event)
		if err := payloadschema.Validate("work_define_revise_input", factWriteAuthoringJSON(t, input)); err != nil {
			t.Fatalf("update schema refused the complete decision fact: %v", err)
		}
		delete(input, "contract")
		if err := payloadschema.Validate("work_define_revise_input", factWriteAuthoringJSON(t, input)); err != nil {
			t.Fatalf("contract was not optional in revise_intent: %v", err)
		}
		for _, value := range []any{nil, map[string]any{"unknown_field": true}} {
			input["contract"] = value
			if err := payloadschema.Validate("work_define_revise_input", factWriteAuthoringJSON(t, input)); err == nil {
				t.Fatalf("update schema admitted an invalid complete contract: %v", value)
			}
		}
		if err := factWriteApplyUpdateEvent(t, s, registry, event); err != nil {
			t.Fatalf("store update refused the complete decision fact: %v", err)
		}
	})

	t.Run("the initial approve intent advertises the required fact only beside the new policy", func(t *testing.T) {
		t.Parallel()
		definition := approvalRouteDefinition(t)
		legacy := definition
		legacy.ActionDefinitions = slices.Clone(definition.ActionDefinitions)
		for index := range legacy.ActionDefinitions {
			if legacy.ActionDefinitions[index].ID != "approve_contract" {
				continue
			}
			action := legacy.ActionDefinitions[index]
			fields := make([]WorkflowPayloadField, 0, len(action.Payload.Fields))
			for _, field := range action.Payload.Fields {
				if field.Name == "decision_domains" {
					continue
				}
				fields = append(fields, field)
			}
			action.Payload.Fields = fields
			legacy.ActionDefinitions[index] = action
		}
		for _, pinCase := range []struct {
			name       string
			definition WorkflowDefinition
			want       bool
		}{
			{name: "consequence_v1", definition: definition, want: true},
			{name: "legacy", definition: legacy, want: false},
		} {
			advertised := false
			for _, step := range pinCase.definition.StepGraph.Steps {
				for _, intent := range workPinIntents(pinCase.definition, step.ID, 1) {
					if intent.ActionID != "approve_contract" {
						continue
					}
					advertised = true
					if got := slices.Contains(intent.RequiredFields, "decision_domains"); got != pinCase.want {
						t.Fatalf("the %s approve_contract intent at %s projects required fields %v, want decision_domains advertised %v", pinCase.name, step.ID, intent.RequiredFields, pinCase.want)
					}
				}
			}
			if !advertised {
				t.Fatalf("the %s definition advertises no approve_contract step intent", pinCase.name)
			}
		}
	})

	t.Run("a distinct pinned closed initial declaration survives discovery validation and advertisement", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-pinned-closed-initial"

		// The local pinned definition replaces the route's approve_contract
		// action with the distinct closed declaration, so only the pinned
		// action can answer discovery: its metadata, required_evidence
		// requiredness, premise minimum, and closed PublicPayload all differ
		// from the global builtin policy.
		definition := approvalRouteDefinition(t)
		pinned := factWritePinnedApproveAction(false)
		replaced := false
		for index := range definition.ActionDefinitions {
			if definition.ActionDefinitions[index].ID != "approve_contract" {
				continue
			}
			definition.ActionDefinitions[index] = pinned
			replaced = true
		}
		if !replaced {
			t.Fatal("the approval route declares no approve_contract action to pin")
		}
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		defer s.Close()
		ownerRef := ownerRefForTest(t, owner)
		factWriteSeedAtPlanning(t, s, registry, workID, ownerRef, true)

		approveStep := ""
		for _, step := range definition.StepGraph.Steps {
			if slices.Contains(step.Actions, "approve_contract") {
				approveStep = step.ID
			}
		}
		var currentStep string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
			t.Fatal(err)
		}
		if currentStep != approveStep {
			t.Fatalf("the seed parked the instance at %q, want the pinned approve step %q", currentStep, approveStep)
		}

		_, discovered, err := WorkflowActionDefinitionFor(context.Background(), s, registry, workID, "approve_contract")
		if err != nil {
			t.Fatalf("discovery refused the pinned closed initial declaration: %v", err)
		}
		if discovered.Consequence != pinned.Consequence || discovered.ExecutionMode != pinned.ExecutionMode || discovered.RequiredCapability != pinned.RequiredCapability {
			t.Errorf("discovery resolved metadata %+v, want the pinned action's %+v", WorkflowActionDefinition{ID: discovered.ID, Consequence: discovered.Consequence, ExecutionMode: discovered.ExecutionMode, RequiredCapability: discovered.RequiredCapability}, WorkflowActionDefinition{ID: pinned.ID, Consequence: pinned.Consequence, ExecutionMode: pinned.ExecutionMode, RequiredCapability: pinned.RequiredCapability})
		}
		if !discovered.Payload.Closed {
			t.Errorf("discovery resolved an open initial payload for the pinned closed declaration")
		}
		if count := countDecisionDomainFields(discovered.Payload.Fields); count != 1 {
			t.Errorf("discovery resolved %d decision_domains fields, want exactly the one canonical fact", count)
		}
		wantPinned := append(slices.Clone(pinned.Payload.Fields), workflowLawChangeFields()...)
		if !reflect.DeepEqual(discovered.Payload.Fields, wantPinned) {
			t.Errorf("discovery resolved fields %+v, want the pinned declaration with its fact kept and the law-change inputs appended", discovered.Payload.Fields)
		}
		if discovered.PublicPayload == nil || !discovered.PublicPayload.Closed {
			t.Errorf("discovery dropped the pinned closed PublicPayload: %+v", discovered.PublicPayload)
		}
		expectedPublicFields := append(slices.Clone(pinned.PublicPayload.Fields), workflowLawChangeFields()...)
		var wantRequired []string
		for _, field := range expectedPublicFields {
			if field.Required {
				wantRequired = append(wantRequired, field.Name)
			}
		}
		if public := publicWorkflowActionPayload(discovered); !reflect.DeepEqual(public, WorkflowPayloadDefinition{Closed: true, Fields: expectedPublicFields}) {
			t.Errorf("the public projection resolved %+v, want the pinned public fields with the one canonical fact appended", public)
		}

		// Closed validation answers from the pinned declaration, so its
		// required_evidence requiredness and premise minimum stay enforced.
		completeRaw := factWriteAuthoringJSON(t, factWritePinnedApprovePayload(workID, "decide the pinned approval contract route for the witnessed change", []string{"verification"}, []string{"product"}, true))
		if err := validateWorkflowActionPayload(definition, "approve_contract", completeRaw); err != nil {
			t.Errorf("closed validation refused the complete pinned payload: %v", err)
		}
		missingEvidenceRaw := factWriteAuthoringJSON(t, factWritePinnedApprovePayload(workID, "decide the pinned approval contract route for the witnessed change", nil, []string{"product"}, true))
		var missingEvidenceFailure *Failure
		if err := validateWorkflowActionPayload(definition, "approve_contract", missingEvidenceRaw); err == nil || !failureAs(err, &missingEvidenceFailure) || missingEvidenceFailure.Kind != KindInvalidPayload {
			t.Errorf("closed validation answered %v for the payload missing the pinned required_evidence, want the typed invalid-payload refusal", err)
		}
		shortPremiseRaw := factWriteAuthoringJSON(t, factWritePinnedApprovePayload(workID, "decide the route", []string{"verification"}, []string{"product"}, true))
		var shortPremiseFailure *Failure
		if err := validateWorkflowActionPayload(definition, "approve_contract", shortPremiseRaw); err == nil || !failureAs(err, &shortPremiseFailure) || shortPremiseFailure.Kind != KindInvalidPayload {
			t.Errorf("closed validation answered %v for the below-minimum pinned premise, want the typed invalid-payload refusal", err)
		}

		preflight := func(raw json.RawMessage) error {
			return WorkflowActionPreflightWithRegistry(context.Background(), s, registry, WorkflowActionPreflightRequest{
				WorkID: workID, ExpectedVersion: readWorkVersion(t, s, workID), ActionID: "approve_contract", Payload: raw, Actor: owner,
			})
		}
		if err := preflight(missingEvidenceRaw); err == nil {
			t.Errorf("preflight admitted the payload missing the pinned required_evidence")
		} else {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Errorf("preflight answered %v for the payload missing the pinned required_evidence, want the typed invalid-payload refusal", err)
			}
		}
		if err := preflight(shortPremiseRaw); err == nil {
			t.Errorf("preflight admitted the below-minimum pinned premise")
		} else {
			var failure *Failure
			if !failureAs(err, &failure) || failure.Kind != KindInvalidPayload {
				t.Errorf("preflight answered %v for the below-minimum pinned premise, want the typed invalid-payload refusal", err)
			}
		}
		if err := preflight(completeRaw); err != nil {
			t.Errorf("preflight refused the complete pinned payload beside the active contract: %v", err)
		}

		// The work pin advertises the pinned requiredness beside the fact, so
		// the operator supplies both on the first attempt.
		snapshot := approvalContinuitySnapshot(t, s, registry, workID)
		if snapshot.WorkPin == nil {
			t.Fatal("the continuity snapshot carried no work pin at the pinned approve step")
		}
		advertised := false
		for _, intent := range snapshot.WorkPin.NextValidIntents {
			if intent.ActionID != "approve_contract" {
				continue
			}
			advertised = true
			if !reflect.DeepEqual(intent.RequiredFields, wantRequired) {
				t.Errorf("the work pin's approve_contract intent projects required fields %v, want the pinned requiredness and the fact: %v", intent.RequiredFields, wantRequired)
			}
		}
		if !advertised {
			t.Errorf("the work pin advertised no approve_contract intent at the pinned approve step")
		}
	})

	t.Run("an open pinned initial declaration refuses discovery preflight and advertisement", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-open-initial"

		// No selector may synthesize a usable shape around an open initial
		// declaration.
		definition := approvalRouteDefinition(t)
		for index := range definition.ActionDefinitions {
			if definition.ActionDefinitions[index].ID != "approve_contract" {
				continue
			}
			definition.ActionDefinitions[index].Payload.Closed = false
		}
		registry, registered := newResolutionRegistry(t, definition)
		s, owner := seedResolutionSelection(t, workID, registry, registered)
		defer s.Close()
		ownerRef := ownerRefForTest(t, owner)
		factWriteSeedAtPlanning(t, s, registry, workID, ownerRef, false)

		baselineVersion := readWorkVersion(t, s, workID)
		baselineEvents := approvalEventCount(t, s, workID)

		_, _, discoverErr := WorkflowActionDefinitionFor(context.Background(), s, registry, workID, "approve_contract")
		var discoverFailure *Failure
		if discoverErr == nil || !failureAs(discoverErr, &discoverFailure) || discoverFailure.Kind != KindInvalidDefinition {
			t.Errorf("discovery answered %v for the open pinned initial declaration, want the typed invalid-definition refusal", discoverErr)
		}
		preflightErr := WorkflowActionPreflightWithRegistry(context.Background(), s, registry, WorkflowActionPreflightRequest{
			WorkID: workID, ExpectedVersion: baselineVersion, ActionID: "approve_contract", Actor: owner,
		})
		var preflightFailure *Failure
		if preflightErr == nil || !failureAs(preflightErr, &preflightFailure) || preflightFailure.Kind != KindInvalidDefinition {
			t.Errorf("preflight answered %v for the open pinned initial declaration, want the typed invalid-definition refusal", preflightErr)
		}

		for _, step := range definition.StepGraph.Steps {
			intents := workPinIntents(definition, step.ID, baselineVersion)
			if workPinContainsAction(intents, "approve_contract") {
				t.Errorf("the pure intents at %s advertise approve_contract beside the open pinned initial declaration", step.ID)
			}
			for _, actionID := range step.Actions {
				if actionID == "approve_contract" {
					continue
				}
				if !workPinContainsAction(intents, actionID) {
					t.Errorf("the pure intents at %s dropped the unrelated declared action %s", step.ID, actionID)
				}
			}
		}

		// The actual work pin offers the same answer as the pure intents.
		snapshot := approvalContinuitySnapshot(t, s, registry, workID)
		if snapshot.WorkPin == nil {
			t.Fatal("the continuity snapshot carried no work pin at the planning step")
		}
		if workPinContainsAction(snapshot.WorkPin.NextValidIntents, "approve_contract") {
			t.Errorf("the work pin advertised approve_contract beside the open pinned initial declaration: %+v", snapshot.WorkPin.NextValidIntents)
		}

		// The missing-action probe stays unregistered: neither validation nor
		// the pure intents may fabricate a declaration for the absent action.
		missing := definition
		keptActions := make([]WorkflowActionDefinition, 0, len(definition.ActionDefinitions))
		for _, action := range definition.ActionDefinitions {
			if action.ID == "approve_contract" {
				continue
			}
			keptActions = append(keptActions, action)
		}
		missing.ActionDefinitions = keptActions
		undeclaredRaw := factWriteAuthoringJSON(t, map[string]any{"undeclared_member": true})
		if err := validateWorkflowActionPayload(missing, "approve_contract", undeclaredRaw); err != nil {
			t.Errorf("closed validation answered %v for a definition that declares no approve_contract action, want the generic no-op nil answer instead of a fabricated declaration", err)
		}
		for _, step := range definition.StepGraph.Steps {
			if workPinContainsAction(workPinIntents(missing, step.ID, baselineVersion), "approve_contract") {
				t.Errorf("the pure intents at %s fabricated an approve_contract advertisement for a definition that declares no such action", step.ID)
			}
		}

		// The refusals are reads: the work version and its event rows did
		// not move.
		if version := readWorkVersion(t, s, workID); version != baselineVersion {
			t.Errorf("the open-initial refusals advanced the work version from %d to %d", baselineVersion, version)
		}
		if events := approvalEventCount(t, s, workID); events != baselineEvents {
			t.Errorf("the open-initial refusals left %d events (baseline %d), want no append", events, baselineEvents)
		}
	})

	t.Run("a pinned closed initial declaration that already carries the fact stays single canonical and clone-only", func(t *testing.T) {
		t.Parallel()
		const workID = "decision-fact-pinned-fact-declared"
		definition := approvalRouteDefinition(t)
		pinned := factWritePinnedApproveAction(true)
		replaced := false
		for index := range definition.ActionDefinitions {
			if definition.ActionDefinitions[index].ID != "approve_contract" {
				continue
			}
			definition.ActionDefinitions[index] = pinned
			replaced = true
		}
		if !replaced {
			t.Fatal("the approval route declares no approve_contract action to pin")
		}
		factIndex := -1
		for index, field := range pinned.Payload.Fields {
			if field.Name == "decision_domains" {
				factIndex = index
			}
		}
		publicFactIndex := -1
		for index, field := range pinned.PublicPayload.Fields {
			if field.Name == "decision_domains" {
				publicFactIndex = index
			}
		}
		if factIndex < 0 || publicFactIndex < 0 {
			t.Fatalf("the pinned action authored no declared decision fact (payload %d, public %d)", factIndex, publicFactIndex)
		}
		registry, registered := newResolutionRegistry(t, definition)
		s, _ := seedResolutionSelection(t, workID, registry, registered)
		defer s.Close()

		_, first, err := WorkflowActionDefinitionFor(context.Background(), s, registry, workID, "approve_contract")
		if err != nil {
			t.Fatalf("discovery refused the pinned fact-declaring initial declaration: %v", err)
		}
		_, second, err := WorkflowActionDefinitionFor(context.Background(), s, registry, workID, "approve_contract")
		if err != nil {
			t.Fatalf("the repeated discovery refused the pinned fact-declaring initial declaration: %v", err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Errorf("the repeated discovery resolved %+v, want the first answer %+v", second, first)
		}
		if count := countDecisionDomainFields(first.Payload.Fields); count != 1 {
			t.Errorf("discovery resolved %d decision_domains fields, want exactly the pinned declaration", count)
		}
		if factIndex >= len(first.Payload.Fields) || first.Payload.Fields[factIndex].Name != "decision_domains" {
			t.Errorf("discovery moved the declared fact from position %d: %+v", factIndex, first.Payload.Fields)
		} else if !reflect.DeepEqual(first.Payload.Fields[factIndex], workflowDecisionFactField()) {
			t.Errorf("discovery restated the declared fact as %+v, want the canonical declaration", first.Payload.Fields[factIndex])
		}
		for index, field := range pinned.Payload.Fields {
			if index >= len(first.Payload.Fields) || !reflect.DeepEqual(first.Payload.Fields[index], field) {
				t.Errorf("discovery altered pinned field %d (%s)", index, field.Name)
			}
		}
		publicOK := first.PublicPayload != nil
		if !publicOK {
			t.Errorf("discovery dropped the pinned closed PublicPayload entirely")
		}
		if publicOK {
			public := publicWorkflowActionPayload(first)
			if !public.Closed {
				t.Errorf("the public projection lost the pinned closed shape")
			}
			if count := countDecisionDomainFields(public.Fields); count != 1 {
				t.Errorf("the public projection resolved %d decision_domains fields, want exactly the pinned declaration", count)
			}
			if publicFactIndex >= len(public.Fields) || public.Fields[publicFactIndex].Name != "decision_domains" {
				t.Errorf("the public projection moved the declared fact from position %d: %+v", publicFactIndex, public.Fields)
			}
			if !reflect.DeepEqual(publicWorkflowActionPayload(second), public) {
				t.Errorf("the repeated public projection resolved %+v, want the first answer %+v", publicWorkflowActionPayload(second), public)
			}
		}

		// The registry lookup deep-clones before discovery, so only a direct
		// selector call can expose selector aliasing: this call mutates the
		// premise bound, evidence enum, and public fields of the storage the
		// selector itself returned, and holds the input definition bytes and
		// the canonical fact set fixed. Every mutation is pointer-guarded, so
		// a failing assertion never writes through a shared slice.
		inputBefore, inputErr := json.Marshal(definition)
		if inputErr != nil {
			t.Fatal(inputErr)
		}
		canonicalBefore := append([]string(nil), workflowDecisionDomainSet...)
		selected, selectErr := workflowDecisionFactInitialActionDefinition(definition)
		if selectErr != nil {
			t.Fatalf("the direct selector refused the pinned fact-declaring definition: %v", selectErr)
		}
		if !reflect.DeepEqual(selected, first) {
			t.Errorf("the direct selector resolved %+v, want the discovered answer %+v", selected, first)
		}
		locate := func(fields []WorkflowPayloadField, name string) int {
			for index := range fields {
				if fields[index].Name == name {
					return index
				}
			}
			return -1
		}
		selectedPremise := locate(selected.Payload.Fields, "premise")
		selectedEvidence := locate(selected.Payload.Fields, "required_evidence")
		selectedFact := locate(selected.Payload.Fields, "decision_domains")
		if selectedPremise < 0 || selectedEvidence < 0 || selectedFact < 0 ||
			selected.Payload.Fields[selectedPremise].MinLength == nil ||
			len(selected.Payload.Fields[selectedEvidence].Enum) == 0 ||
			len(selected.Payload.Fields[selectedFact].Enum) == 0 {
			t.Fatalf("the direct selection lost the pinned members (premise %d, evidence %d, fact %d)", selectedPremise, selectedEvidence, selectedFact)
		}
		if reflect.ValueOf(selected.Payload.Fields).Pointer() == reflect.ValueOf(pinned.Payload.Fields).Pointer() {
			t.Errorf("the direct selection shares the pinned field backing storage")
		} else {
			*selected.Payload.Fields[selectedPremise].MinLength = 7
			selected.Payload.Fields[selectedEvidence].Enum[0] = "mutated"
		}
		if reflect.ValueOf(selected.Payload.Fields[selectedFact].Enum).Pointer() == reflect.ValueOf(workflowDecisionDomainSet).Pointer() {
			t.Errorf("the direct selection's fact enum aliases the canonical set")
		} else {
			selected.Payload.Fields[selectedFact].Enum[0] = "mutated"
		}
		switch {
		case selected.PublicPayload == nil:
			t.Errorf("the direct selection dropped the pinned closed PublicPayload")
		case len(selected.PublicPayload.Fields) == 0:
			t.Errorf("the direct selection carried no public fields")
		case reflect.ValueOf(selected.PublicPayload.Fields).Pointer() == reflect.ValueOf(pinned.PublicPayload.Fields).Pointer():
			t.Errorf("the direct selection shares the pinned PublicPayload backing storage")
		default:
			selected.PublicPayload.Fields[0].Name = "mutated"
		}
		inputAfter, inputErr := json.Marshal(definition)
		if inputErr != nil {
			t.Fatal(inputErr)
		}
		if string(inputAfter) != string(inputBefore) {
			t.Errorf("the direct selection mutated the input definition bytes")
		}
		if !reflect.DeepEqual(workflowDecisionDomainSet, canonicalBefore) {
			t.Errorf("the direct selection mutated the canonical decision-domain set: %v", workflowDecisionDomainSet)
		}

		// The selection is clone-only: mutating only the returned action and
		// its nested bounds, enums, and PublicPayload must not reach the
		// pinned definition, the canonical set, or the global builtin
		// storage.
		builtinBefore := builtinActionPolicies["approve_contract"]
		if publicOK {
			first.PublicPayload.Fields[0].Name = "mutated"
			if pinned.PublicPayload.Fields[0].Name != "premise" {
				t.Errorf("the returned PublicPayload aliases the pinned definition's PublicPayload")
			}
		}
		if factIndex < len(first.Payload.Fields) && first.Payload.Fields[factIndex].Name == "decision_domains" {
			returnedFact := first.Payload.Fields[factIndex]
			if len(returnedFact.Enum) != 0 {
				returnedFact.Enum[0] = "mutated"
				if workflowDecisionDomainSet[0] != "increased_spending" {
					t.Errorf("the returned fact enum aliases the canonical set: %v", workflowDecisionDomainSet)
				}
				if pinned.Payload.Fields[factIndex].Enum[0] != "increased_spending" {
					t.Errorf("the returned fact enum aliases the pinned declaration's enum")
				}
			}
			if returnedFact.MinItems == nil {
				t.Errorf("the returned fact carries no declared minimum bound")
			} else {
				*returnedFact.MinItems = 7
				if pinned.Payload.Fields[factIndex].MinItems == nil || *pinned.Payload.Fields[factIndex].MinItems != 0 {
					t.Errorf("the returned fact bounds alias the pinned declaration")
				}
			}
		}
		if reflect.ValueOf(first.Payload.Fields).Pointer() == reflect.ValueOf(pinned.Payload.Fields).Pointer() {
			t.Errorf("the selection shares the pinned definition's field backing storage")
		}
		if publicOK && reflect.ValueOf(first.PublicPayload.Fields).Pointer() == reflect.ValueOf(pinned.PublicPayload.Fields).Pointer() {
			t.Errorf("the selection shares the pinned PublicPayload's field backing storage")
		}
		if reflect.ValueOf(first.Payload.Fields).Pointer() == reflect.ValueOf(builtinActionPolicies["approve_contract"].Payload.Fields).Pointer() {
			t.Errorf("the selection shares the builtin policy's field backing storage")
		}
		if !reflect.DeepEqual(builtinActionPolicies["approve_contract"], builtinBefore) {
			t.Errorf("the selection mutated the global builtin approve_contract policy")
		}

		// A declared wrong-bounds fact is still a declaration: the selector
		// replaces it once, at its position, in both payload shapes,
		// selection over its own answer is a fixpoint, and the local
		// declaration bytes stay untouched.
		wrongAction := factWritePinnedApproveAction(false)
		wrongFact := WorkflowPayloadField{Name: "decision_domains", ValueType: PayloadStringList, Required: true, MinItems: workflowInt(2), MaxItems: workflowInt(3), Enum: []string{"economics"}}
		insertAt := func(fields []WorkflowPayloadField, fact WorkflowPayloadField, index int) []WorkflowPayloadField {
			grown := make([]WorkflowPayloadField, 0, len(fields)+1)
			grown = append(grown, fields[:index]...)
			grown = append(grown, fact)
			return append(grown, fields[index:]...)
		}
		payloadWithout := slices.Clone(wrongAction.Payload.Fields)
		payloadWithout = payloadWithout[:len(payloadWithout)-1]
		publicWithout := slices.Clone(wrongAction.PublicPayload.Fields)
		publicWithout = publicWithout[:len(publicWithout)-1]
		mid := len(payloadWithout) / 2
		wrongAction.Payload.Fields = insertAt(payloadWithout, wrongFact, mid)
		wrongAction.PublicPayload.Fields = insertAt(publicWithout, wrongFact, 1)
		wantPayload := append(insertAt(payloadWithout, workflowDecisionFactField(), mid), workflowLawChangeFields()...)
		wantPublic := append(insertAt(publicWithout, workflowDecisionFactField(), 1), workflowLawChangeFields()...)
		wrongDefinition := WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{wrongAction}}
		wrongBefore, wrongErr := json.Marshal(wrongDefinition)
		if wrongErr != nil {
			t.Fatal(wrongErr)
		}
		wrongSelected, wrongSelectErr := workflowDecisionFactInitialActionDefinition(wrongDefinition)
		if wrongSelectErr != nil {
			t.Fatalf("the selector refused the local wrong-bounds declaration: %v", wrongSelectErr)
		}
		if count := countDecisionDomainFields(wrongSelected.Payload.Fields); count != 1 {
			t.Errorf("the selection kept %d decision_domains fields beside the wrong-bounds declaration, want exactly one", count)
		}
		if !reflect.DeepEqual(wrongSelected.Payload.Fields, wantPayload) {
			t.Errorf("the selection resolved %+v, want the wrong-bounds declaration replaced in place with the canonical fact", wrongSelected.Payload.Fields)
		}
		if wrongSelected.PublicPayload == nil || !reflect.DeepEqual(wrongSelected.PublicPayload.Fields, wantPublic) {
			t.Errorf("the selection resolved public fields %+v, want the wrong-bounds public declaration replaced in place", wrongSelected.PublicPayload)
		}
		again, againErr := workflowDecisionFactInitialActionDefinition(WorkflowDefinition{ActionDefinitions: []WorkflowActionDefinition{wrongSelected}})
		if againErr != nil {
			t.Fatalf("the selector refused its own selected declaration: %v", againErr)
		}
		if !reflect.DeepEqual(again, wrongSelected) {
			t.Errorf("selecting the selected action resolved %+v, want the same answer %+v", again, wrongSelected)
		}
		wrongAfter, wrongErr := json.Marshal(wrongDefinition)
		if wrongErr != nil {
			t.Fatal(wrongErr)
		}
		if string(wrongAfter) != string(wrongBefore) {
			t.Errorf("the selection mutated the local declaration bytes")
		}
	})
}

func assertFactWriteProjectedDomains(t *testing.T, s *Store, registry DefinitionRegistry, workID string, version int64, want []string) {
	t.Helper()
	contract, _, err := workflowCompletionContract(context.Background(), s.DatabaseForTesting(), registry, workID)
	if err != nil {
		t.Fatalf("read the registered active contract: %v", err)
	}
	if contract.Version != version || !contract.DecisionDomainsPresent || !reflect.DeepEqual(contract.DecisionDomains, want) {
		t.Fatalf("active contract v%d projects decision_domains %v (present %v), want v%d with the exact present fact %v", contract.Version, contract.DecisionDomains, contract.DecisionDomainsPresent, version, want)
	}
}

// factWriteSetLabel names a canonical decision-domain set for work item IDs.
func factWriteSetLabel(set []string) string {
	if len(set) == 0 {
		return "engineering"
	}
	return strings.Join(set, "-")
}

// factWriteInitialContractEvent authors the genuine initial contract event at
// its current payload version with an optional decision fact member: present
// false omits the member, a nil fact authors the explicit null, and typed Go
// values author the exact bytes the fold must store or refuse. The event
// folds through the registered fold; no projection column is touched.
func factWriteInitialContractEvent(t *testing.T, s *Store, workID, ownerRef string, fact any, present bool) Event {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	payload := map[string]any{
		"contract_version": int64(1), "premise": "deliver the checked change",
		"outcome_kind":      "check",
		"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	}
	if present {
		payload["decision_domains"] = fact
	}
	return resolutionEvent("decision-fact-initial-contract-"+workID, WorkflowContractApproved, workID, ownerRef, version, payload)
}

func factWriteContractUpdateEvent(t *testing.T, s *Store, workID, actorRef string, fact any, present bool) Event {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	contract := pContractSuccessor(t, s, workID, "The reclassified accepted change.")
	delete(contract, "decision_domains")
	if present {
		contract["decision_domains"] = fact
	}
	var kind string
	if err := s.db.QueryRow(`SELECT kind FROM work_items WHERE id=?`, workID).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	return Event{EventID: "decision-fact-update-" + workID, Kind: "work.intent_revised", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: time.Unix(12, version).UTC(), PayloadVersion: 1, Payload: factWriteAuthoringJSON(t, map[string]any{
		"title": "Updated contract", "value_statement": "Retain the declared decision fact", "kind": kind, "tags": []string{},
		"reason": "Reclassify the delivered work", "expected_version": version, "resulting_version": version + 1, "contract": contract,
	})}
}

func factWriteApplyUpdateEvent(t *testing.T, s *Store, registry DefinitionRegistry, event Event) error {
	t.Helper()
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), event.SubjectID)
	if err != nil {
		return err
	}
	var payload workIntentPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	return ApplyOperation(ctx, s, Operation{
		Events: []Event{event}, Registry: registry,
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, event.SubjectID): payload.ExpectedVersion},
	})
}

// factWriteActiveContract reads the work item's active contract version.
func factWriteActiveContract(t *testing.T, s *Store, workID string) int64 {
	t.Helper()
	version, err := activeWorkflowContractVersion(context.Background(), s.DatabaseForTesting(), workID, "workflow_decision_fact_write_test")
	if err != nil {
		t.Fatal(err)
	}
	return version
}

// factWriteLatestEventID names the latest recorded event of one of the kinds
// for one work item, so its bytes can be compared across a later fold.
func factWriteLatestEventID(t *testing.T, s *Store, workID string, kinds ...string) string {
	t.Helper()
	var eventID string
	query := `SELECT event_id FROM effect_receipts WHERE subject_id=? AND kind IN (?` + strings.Repeat(",?", len(kinds)-1) + `) ORDER BY effect_seq DESC LIMIT 1`
	args := []any{workID}
	for _, kind := range kinds {
		args = append(args, kind)
	}
	if err := s.DatabaseForTesting().QueryRow(query, args...).Scan(&eventID); err != nil {
		t.Fatalf("read the latest %v event: %v", kinds, err)
	}
	return eventID
}

// factWriteEventDigestByID reads the immutable payload identity of one effect.
func factWriteEventDigestByID(t *testing.T, s *Store, eventID string) string {
	t.Helper()
	var raw []byte
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_digest FROM effect_receipts WHERE event_id=?`, eventID).Scan(&raw); err != nil {
		t.Fatalf("read event %s: %v", eventID, err)
	}
	return string(raw)
}

// seedFactWriteConfirmedEngineering seeds the consequence_v1 route to the
// acceptance step with an explicit engineering fact and records the executing
// coordinator's confirmation through the constructor+fold seam. It returns the baseline work version.
func seedFactWriteConfirmedEngineering(t *testing.T, workID string) (*Store, DefinitionRegistry, WorkflowActor, int64) {
	t.Helper()
	s, registry, definition, owner := seedApprovalItemAtAcceptance(t, workID, []string{})
	if _, err := confirmResolutionPremise(t, s, registry, definition, workID, owner, nil, nil); err != nil {
		t.Fatalf("the executing coordinator's confirmation refused on the confirmed seed: %v", err)
	}
	if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != ownerRefForTest(t, owner) {
		t.Fatalf("the confirmed seed's confirmation row = %q (present %v), want the executing coordinator", confirmed, ok)
	}
	return s, registry, owner, readWorkVersion(t, s, workID)
}

// assertFactWriteInitialNoEffect holds the refusal invariant for an initial
// decision-fact write: no contract row, no event movement, no version
// advance, and no stranded write guard.
func assertFactWriteInitialNoEffect(t *testing.T, s *Store, workID string, baselineVersion int64, baselineEvents int) {
	t.Helper()
	if version := readWorkVersion(t, s, workID); version != baselineVersion {
		t.Fatalf("the refused initial fact write advanced the work version from %d to %d", baselineVersion, version)
	}
	if events := approvalEventCount(t, s, workID); events != baselineEvents {
		t.Fatalf("the refused initial fact write left %d events (baseline %d), want rollback with no append", events, baselineEvents)
	}
	if contracts := decisionContractCount(t, s, workID); contracts != 0 {
		t.Fatalf("the refused initial fact write left %d contract rows, want none", contracts)
	}
	assertNoFoldGuardRows(t, s)
}

// assertFactWritePredecessorPreserved holds the atomic-refusal invariant for a
// complete successor write over a confirmed engineering predecessor: the
// active predecessor keeps its fact and its open supersession slot, the work
// version and its event rows did not move, the live confirmation row stands,
// and no consent challenge, approval-kind evidence, or write guard row exists.
func assertFactWritePredecessorPreserved(t *testing.T, s *Store, workID string, baselineVersion int64, baselineEvents int, confirmedBy string) {
	t.Helper()
	if version := readWorkVersion(t, s, workID); version != baselineVersion {
		t.Fatalf("the refused successor write advanced the work version from %d to %d", baselineVersion, version)
	}
	if events := approvalEventCount(t, s, workID); events != baselineEvents {
		t.Fatalf("the refused successor write left %d events (baseline %d), want rollback with no append", events, baselineEvents)
	}
	if contracts := decisionContractCount(t, s, workID); contracts != 1 {
		t.Fatalf("the refused successor write left %d contract rows, want the active predecessor alone", contracts)
	}
	if stored := storedDecisionDomains(t, s, workID, 1); stored != "[]" {
		t.Fatalf("the refused successor write changed the predecessor fact to %q, want [] preserved", stored)
	}
	var superseded any
	if err := s.DatabaseForTesting().QueryRow(`SELECT superseded_by FROM workflow_contracts WHERE work_id=? AND contract_version=1`, workID).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if superseded != nil {
		t.Fatalf("the refused successor write named superseded_by %v, want the predecessor left active", superseded)
	}
	activeVersion, err := activeWorkflowContractVersion(context.Background(), s.DatabaseForTesting(), workID, "workflow_decision_fact_write_test")
	if err != nil {
		t.Fatal(err)
	}
	if activeVersion != 1 {
		t.Fatalf("the refused successor write left active contract version %d, want 1", activeVersion)
	}
	if confirmed, ok := approvalPremiseConfirmationRow(t, s, workID, 1); !ok || confirmed != confirmedBy {
		t.Fatalf("the refused successor write moved the confirmation row to %q (present %v), want %q preserved", confirmed, ok, confirmedBy)
	}
	assertNoFoldGuardRows(t, s)
}

// factWriteBrokenFacts enumerates the absent and broken decision-fact shapes
// both contract constructors must refuse under consequence_v1: the two
// required-presence shapes and every wire shape outside the canonical set.
func factWriteBrokenFacts() []struct {
	name    string
	fact    any
	present bool
} {
	return []struct {
		name    string
		fact    any
		present bool
	}{
		{name: "missing member", present: false},
		{name: "explicit null", fact: nil, present: true},
		{name: "nonstring string", fact: "product", present: true},
		{name: "nonstring object", fact: map[string]any{"product": true}, present: true},
		{name: "unknown member", fact: []string{"preference", "economics"}, present: true},
		{name: "duplicate member", fact: []string{"product", "product"}, present: true},
		{name: "noncanonical order", fact: []string{"product", "preference"}, present: true},
		{name: "over the closed set", fact: []string{"preference", "product", "ui_ux", "preference"}, present: true},
	}
}

// factWriteApproveAuthoring composes the initial approve_contract authoring
// fields the constructor and the closed payload validator consume: the valid
// flat planning shape beside an optional decision fact member.
func factWriteApproveAuthoring(workID string, fact any, present bool) map[string]any {
	payload := map[string]any{
		"contract_version": int64(1), "premise": "deliver the checked change",
		"outcome_predicates": []map[string]any{{
			"predicate_id": "predicate:primary", "ordinal": 0, "outcome_kind": "check",
			"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
		}},
		"required_evidence": []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "law_modifies": []string{},
		"rigor_class": "prototype_internal",
	}
	if present {
		payload["decision_domains"] = fact
	}
	return payload
}

// factWriteApproveFields decodes the authoring fields into the raw field map
// the approve_contract constructor consumes.
func factWriteApproveFields(t *testing.T, workID string, fact any, present bool) map[string]json.RawMessage {
	t.Helper()
	raw := factWriteAuthoringJSON(t, factWriteApproveAuthoring(workID, fact, present))
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

// factWriteAuthoringJSON encodes one composed authoring payload.
func factWriteAuthoringJSON(t *testing.T, authoring map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(authoring)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// factWritePayloadMember reads one raw payload object's decision_domains
// member bytes; present reports whether the member exists.
func factWritePayloadMember(t *testing.T, raw string) (member string, present bool) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("decode the recorded payload: %v", err)
	}
	if value, ok := fields["decision_domains"]; ok {
		return string(value), true
	}
	return "", false
}

// factWriteConstructInitialContract drives the actual approve_contract
// constructor in one short transaction and rolls the transaction back before
// any store-backed assertion, because the store pools one connection.
func factWriteConstructInitialContract(t *testing.T, s *Store, definition WorkflowDefinition, workID, ownerRef string, fields map[string]json.RawMessage, expected int64) ([]Event, error) {
	t.Helper()
	ctx, err := s.EstablishKnowledgeSourceSetProof(t.Context(), workID)
	if err != nil {
		return nil, err
	}
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	events, constructErr := workflowApproveContractEvents(ctx, tx, definition, WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: expected, ActionID: "approve_contract",
		OperationID: "decision-fact-initial-" + workID, Now: time.Unix(12, 0).UTC(),
	}, ownerRef, fields, "decision-fact-initial-"+workID, expected)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	return events, constructErr
}

// factWriteExpectedSelection states the selected payload in order, independent
// of the production selector: released fields, law-change inputs, decision fact.
func factWriteExpectedSelection(released []WorkflowPayloadField) []WorkflowPayloadField {
	fields := append([]WorkflowPayloadField{}, released...)
	fields = append(fields, workflowLawChangeFields()...)
	return append(fields, workflowDecisionFactField())
}

// assertFactWriteFieldsCarryFact holds the declaration-owner agreement: the
// projected contract-authoring fields keep every released field in order,
// add the law-change inputs, and append the required closed decision fact.
func assertFactWriteFieldsCarryFact(t *testing.T, label string, fields, released []WorkflowPayloadField) {
	t.Helper()
	if !reflect.DeepEqual(fields, factWriteExpectedSelection(released)) {
		t.Fatalf("%s fields differ from the released fields plus the law-change inputs and canonical decision fact", label)
	}
	for index, field := range released {
		if !reflect.DeepEqual(fields[index], field) {
			t.Fatalf("%s altered released field %d (%s)", label, index, field.Name)
		}
	}
	fact := fields[len(fields)-1]
	if fact.Name != "decision_domains" || !fact.Required || fact.ValueType != PayloadStringList {
		t.Fatalf("%s appends %+v, want the required decision_domains string list", label, fact)
	}
	if fact.MinItems == nil || *fact.MinItems != 0 || fact.MaxItems == nil || *fact.MaxItems != int64(len(workflowDecisionDomainSet)) {
		t.Fatalf("%s restates the fact bounds %+v, want 0..%d", label, fact, len(workflowDecisionDomainSet))
	}
	if !reflect.DeepEqual(fact.Enum, workflowDecisionDomainSet) {
		t.Fatalf("%s restates the fact enum %v, want the canonical closed set %v", label, fact.Enum, workflowDecisionDomainSet)
	}
}

// factWritePinnedApproveAction authors the distinct closed approve_contract
// action on fresh storage — distinct premise minimum, required_evidence
// requiredness, metadata, and a closed PublicPayload — so no field list,
// bound pointer, or enum aliases the builtin policy or the canonical set.
// alreadyDeclaresFact inserts the canonical fact at a middle index in both
// payload shapes instead of leaving its declaration to selection.
func factWritePinnedApproveAction(alreadyDeclaresFact bool) WorkflowActionDefinition {
	distinctPremise := func() WorkflowPayloadField {
		return WorkflowPayloadField{Name: "premise", ValueType: PayloadString, Required: true, NonBlank: true, MinLength: workflowInt(24), MaxLength: workflowInt(4096)}
	}
	distinctEvidence := func() WorkflowPayloadField {
		return WorkflowPayloadField{Name: "required_evidence", ValueType: PayloadStringList, Required: true, MinItems: workflowInt(1), MaxItems: workflowInt(7), Enum: []string{"verification", "review"}}
	}
	base := currentActionDefinition("approve_contract", true).Payload.Fields
	payloadFields := make([]WorkflowPayloadField, 0, len(base)+1)
	for _, field := range base {
		switch field.Name {
		case "premise":
			field = distinctPremise()
		case "required_evidence":
			field = distinctEvidence()
		}
		payloadFields = append(payloadFields, field)
	}
	publicFields := []WorkflowPayloadField{distinctPremise(), distinctEvidence()}
	if alreadyDeclaresFact {
		index := len(payloadFields)/2 + 1
		grown := make([]WorkflowPayloadField, 0, len(payloadFields)+1)
		grown = append(grown, payloadFields[:index]...)
		grown = append(grown, workflowDecisionFactField())
		grown = append(grown, payloadFields[index:]...)
		payloadFields = grown
		publicFields = []WorkflowPayloadField{distinctPremise(), workflowDecisionFactField(), distinctEvidence()}
	} else {
		payloadFields = append(payloadFields, workflowDecisionFactField())
		publicFields = append(publicFields, workflowDecisionFactField())
	}
	return WorkflowActionDefinition{
		ID: "approve_contract", Consequence: ActionInternalSQLite,
		ExecutionMode: ActionHold, RequiredCapability: "work_transition",
		Payload:       WorkflowPayloadDefinition{Closed: true, Fields: payloadFields},
		PublicPayload: &WorkflowPayloadDefinition{Closed: true, Fields: publicFields},
	}
}

// factWritePinnedApprovePayload varies the premise and required_evidence
// presence so the pinned premise minimum and requiredness stay observable
// beside an optional decision fact member.
func factWritePinnedApprovePayload(workID, premise string, evidence []string, fact any, present bool) map[string]any {
	payload := factWriteApproveAuthoring(workID, fact, present)
	payload["premise"] = premise
	if evidence != nil {
		payload["required_evidence"] = evidence
	} else {
		delete(payload, "required_evidence")
	}
	return payload
}

// factWriteSeedAtPlanning folds the planning prefix and, when withContract,
// the initial contract event. No approve_contract action is folded, so the
// pin keeps that route open.
func factWriteSeedAtPlanning(t *testing.T, s *Store, registry DefinitionRegistry, workID, ownerRef string, withContract bool) {
	t.Helper()
	version := readWorkVersion(t, s, workID)
	events := []Event{
		workflowActionCompletedFixture("decision-fact-planning-proposal-"+workID, workID, ownerRef, version, "proposal", "record_proposal"),
		workflowActionCompletedFixture("decision-fact-planning-discovery-"+workID, workID, ownerRef, version+1, "discovery", "record_discovery"),
		workflowActionCompletedFixture("decision-fact-planning-design-"+workID, workID, ownerRef, version+2, "design", "record_design"),
	}
	if withContract {
		events = append(events, resolutionEvent("decision-fact-planning-contract-"+workID, WorkflowContractApproved, workID, ownerRef, version+3, map[string]any{
			"contract_version": 1, "premise": "deliver the checked change under the pinned approval contract",
			"outcome_kind":      "check",
			"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:workflow", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
			"required_evidence": []string{"verification"}, "route_conventions": []string{},
			"spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
			"decision_domains": []string{"product"},
		}))
	}
	appendWorkflowEventsWithRegistry(t, s, registry, Operation{
		Events:           events,
		ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version},
	})
}

// countDecisionDomainFields counts the decision_domains declarations in one
// field list.
func countDecisionDomainFields(fields []WorkflowPayloadField) int {
	count := 0
	for _, field := range fields {
		if field.Name == "decision_domains" {
			count++
		}
	}
	return count
}
