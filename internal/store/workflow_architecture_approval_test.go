package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The contract approval binding tests: a new pin whose closed approve_contract
// declaration requires design_binding records the canonical design identity on
// the approval event itself, an older payload version can never carry the
// member, and a stale or substituted identity refuses with zero effects. The
// binding is event-backed: the reader reports the active contract's recorded
// identity, a rebuild keeps it while the current design moves on, and replay
// never re-derives it from mutable design state. The approval path creates no
// synthetic operator, and the existing post-fold capacity-slot admission keeps
// holding at the approval. Policy-level approval classes are outside this
// store scope.

// approvalBindingPinRef names the synthetic architecture-capable pin. Its
// closed approve_contract payload requires design_binding, the declared
// capability source this store surface admits.
const approvalBindingPinRef = "workflow.test_approval_binding"

// approvalBindingLegacyRef names the synthetic pin that keeps the released
// approve_contract payload shape without the binding member.
const approvalBindingLegacyRef = "workflow.test_approval_legacy"

// approvalBindingDefinition clones the typed-design implementation pin and,
// for the capable pin, adds design_binding to the closed approve_contract
// declaration: the field the pinned declaration requires, not a policy or a
// discriminator. The ref stays outside the released range so the synthetic
// head can never publish. The declared fields are copied first, so the
// registration never aliases the shared builtin policy slice.
func approvalBindingDefinition(t *testing.T, ref string, withBindingField bool) RegisteredDefinition {
	t.Helper()
	base, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 6)
	if !ok {
		t.Fatal("typed design workflow is not registered")
	}
	shape := workflowFixtureShape(base.Definition, 6)
	shape.Ref = ref
	if withBindingField {
		for i := range shape.ActionDefinitions {
			if shape.ActionDefinitions[i].ID != "approve_contract" {
				continue
			}
			fields := make([]WorkflowPayloadField, 0, len(shape.ActionDefinitions[i].Payload.Fields)+1)
			fields = append(fields, shape.ActionDefinitions[i].Payload.Fields...)
			fields = append(fields, WorkflowPayloadField{Name: "design_binding", ValueType: PayloadObject, Required: true})
			shape.ActionDefinitions[i].Payload.Fields = fields
		}
	}
	registered, err := BuiltinWorkflowRegistry().Register(shape)
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

// approvalBindingMalformedDefinition registers a pin whose approve_contract
// names design_binding without the closed required object declaration the
// capability source demands. The name alone grants nothing: the approval
// keeps the released payload shape.
func approvalBindingMalformedDefinition(t *testing.T, ref string, malformed func(field WorkflowPayloadField) ([]WorkflowPayloadField, bool)) RegisteredDefinition {
	t.Helper()
	base, ok := BuiltinWorkflowRegistry().Lookup("workflow.implementation", 6)
	if !ok {
		t.Fatal("typed design workflow is not registered")
	}
	shape := workflowFixtureShape(base.Definition, 6)
	shape.Ref = ref
	for i := range shape.ActionDefinitions {
		if shape.ActionDefinitions[i].ID != "approve_contract" {
			continue
		}
		fields := make([]WorkflowPayloadField, 0, len(shape.ActionDefinitions[i].Payload.Fields)+1)
		fields = append(fields, shape.ActionDefinitions[i].Payload.Fields...)
		declared, closed := malformed(WorkflowPayloadField{Name: "design_binding", ValueType: PayloadObject, Required: true})
		fields = append(fields, declared...)
		shape.ActionDefinitions[i].Payload.Fields = fields
		shape.ActionDefinitions[i].Payload.Closed = closed
	}
	registered, err := BuiltinWorkflowRegistry().Register(shape)
	if err != nil {
		t.Fatal(err)
	}
	return registered
}

// approvalBindingLooseRef, approvalBindingLooseStringRef, and
// approvalBindingLooseOpenRef name the malformed-declaration pins.
const (
	approvalBindingLooseRef       = "workflow.test_approval_loose"
	approvalBindingLooseStringRef = "workflow.test_approval_loose_string"
	approvalBindingLooseOpenRef   = "workflow.test_approval_loose_open"
)

type approvalBindingFixture struct {
	store    *Store
	owner    WorkflowActor
	ownerRef string
	digest   string
	revision int64
}

// seedApprovalBindingFixtureWithDefinition walks the given synthetic pin to
// planning: an optional typed design (a nil plan records the
// architecture-absent legacy design), then the design step. The instance
// waits at planning with no active contract, so the test drives the approval
// through the full action path.
func seedApprovalBindingFixtureWithDefinition(t *testing.T, workID string, definition RegisteredDefinition, plan *WorkflowDesignArchitecture) approvalBindingFixture {
	t.Helper()
	ctx := context.Background()
	s := openTemp(t)
	t.Cleanup(func() { s.Close() })
	seedWork(t, s, workID)
	seedWorkflowLaw(t, s)
	owner := WorkflowActor{PrincipalRef: "principal/operator", ClientRef: "client/concord-1", AgentRef: "agent/owner", SessionRef: "session/" + workID, ActorClass: ActorAgent}
	ownerRef, err := WorkflowActorRef(owner)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		workflowEventWithActor("binding-owner-"+workID, WorkflowActorRecorded, workID, ownerRef, map[string]any{"work_id": workID, "expected_version": 2, "resulting_version": 3, "actor_ref": ownerRef, "principal_ref": owner.PrincipalRef, "client_ref": owner.ClientRef, "agent_ref": owner.AgentRef, "session_ref": owner.SessionRef, "actor_class": "agent"}),
		workflowEvent("binding-definition-"+workID, WorkflowDefinitionSelected, workID, map[string]any{"work_id": workID, "expected_version": 3, "resulting_version": 4, "ref": definition.Definition.Ref, "version": definition.Definition.Version, "digest": definition.Digest, "work_kind": string(definition.Definition.WorkKind)}),
		workflowActionCompletedFixture("binding-proposal-"+workID, workID, ownerRef, 4, "proposal", "record_proposal"),
		workflowActionCompletedFixture("binding-discovery-"+workID, workID, ownerRef, 5, "discovery", "record_discovery"),
	}
	if plan != nil {
		events = append(events, projectionDesignEvent(t, workID, "binding-design-"+workID, 6, "binding", plan))
		events = append(events, workflowActionCompletedFixture("binding-design-step-"+workID, workID, ownerRef, 7, "design", "record_design"))
	} else {
		events = append(events, workflowActionCompletedFixture("binding-design-step-"+workID, workID, ownerRef, 6, "design", "record_design"))
	}
	if err := applyWorkflowTestOperation(ctx, s, Operation{Events: events, ExpectedVersions: workVersion(workID, 2)}); err != nil {
		t.Fatal(err)
	}
	fixture := approvalBindingFixture{store: s, owner: owner, ownerRef: ownerRef}
	if plan != nil {
		current, _, err := readCurrentWorkflowDesign(ctx, s.DatabaseForTesting(), workID)
		if err != nil || current == nil || current.Digest == "" {
			t.Fatalf("seeded design carries no digest: %v", err)
		}
		if current.Revision == nil {
			t.Fatal("seeded architecture design carries no revision")
		}
		fixture.digest = current.Digest
		fixture.revision = *current.Revision
	}
	return fixture
}

// seedApprovalBindingFixture walks the named synthetic pin to planning.
func seedApprovalBindingFixture(t *testing.T, workID, ref string, withBindingField bool, plan *WorkflowDesignArchitecture) approvalBindingFixture {
	t.Helper()
	return seedApprovalBindingFixtureWithDefinition(t, workID, approvalBindingDefinition(t, ref, withBindingField), plan)
}

// approvalBindingRawMemberPayload builds the approve_contract payload with
// the caller's raw binding member value, so a malformed declaration's
// admitted field type can ride a real approval call.
func approvalBindingRawMemberPayload(t *testing.T, premise string, binding any, withMember bool) json.RawMessage {
	t.Helper()
	fields := map[string]any{
		"contract_version":   1,
		"premise":            premise,
		"outcome_predicates": approvalBindingPredicates(),
		"required_evidence":  []string{"verification"},
	}
	if withMember {
		fields["design_binding"] = binding
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// approvalBindingPredicates builds one non-vacuous check predicate at the
// zero position.
func approvalBindingPredicates() []map[string]any {
	return []map[string]any{{
		"predicate_id": "predicate:approval-binding", "ordinal": 0, "outcome_kind": "check",
		"outcome_payload": map[string]any{"kind": "check", "check_ref": "check:approval-binding", "immutable_subject_ref": "commit:" + strings.Repeat("a", 64), "expected_result": "pass"},
	}}
}

// approvalBindingPayload builds the approve_contract action payload. A nil
// binding omits the member, the way a legacy approval calls the same path.
func approvalBindingPayload(t *testing.T, premise string, binding map[string]any) json.RawMessage {
	t.Helper()
	fields := map[string]any{
		"contract_version":   1,
		"premise":            premise,
		"outcome_predicates": approvalBindingPredicates(),
		"required_evidence":  []string{"verification"},
	}
	if binding != nil {
		fields["design_binding"] = binding
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// runApprovalBindingAction drives approve_contract through the full action
// path, so the constructor and the fold own every admission decision.
func runApprovalBindingAction(t *testing.T, fixture approvalBindingFixture, workID string, payload json.RawMessage) error {
	t.Helper()
	ctx := context.Background()
	version := projectionWorkVersion(t, fixture.store, workID)
	tx, err := fixture.store.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := applyWorkflowActionRawTx(ctx, tx, newFoldScope(tx), BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
		WorkID: workID, ExpectedVersion: version, ActionID: "approve_contract", Payload: payload, Actor: fixture.owner,
		AcceptedInputsDigest: testDigest("approval-binding-" + workID), IdempotencyIdentity: "approve-binding-" + workID, OperationID: "approve-binding-" + workID,
		PrincipalRef: fixture.owner.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: "approve-binding-" + workID,
		RequestID: "request:approve-binding-" + workID, ContractDigest: testManifestDigest, Now: time.Unix(40, 0).UTC(),
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// approvalBindingState is the durable surface a refused approval must leave
// untouched.
type approvalBindingState struct {
	version    int64
	events     int64
	contracts  int64
	designRows int64
}

func readApprovalBindingState(t *testing.T, s *Store, workID string) approvalBindingState {
	t.Helper()
	state := approvalBindingState{}
	if err := s.DatabaseForTesting().QueryRow(`SELECT (SELECT version FROM work_items WHERE id=?),(SELECT count(*) FROM domain_events WHERE subject_type='work_item' AND subject_id=?),(SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL),(SELECT count(*) FROM workflow_design_records WHERE work_id=?)`, workID, workID, workID, workID).Scan(&state.version, &state.events, &state.contracts, &state.designRows); err != nil {
		t.Fatal(err)
	}
	return state
}

func requireApprovalBindingUnchanged(t *testing.T, s *Store, workID string, before approvalBindingState) {
	t.Helper()
	if after := readApprovalBindingState(t, s, workID); after != before {
		t.Fatalf("the refused approval moved state: before %+v after %+v", before, after)
	}
}

// approvalBindingEvent reads the work's latest contract approval event from
// the log: the durable surface the binding must live on.
func approvalBindingEvent(t *testing.T, s *Store, workID string) (int, map[string]any) {
	t.Helper()
	var payloadVersion int
	var payload string
	if err := s.DatabaseForTesting().QueryRow(`SELECT payload_version,CAST(payload AS TEXT) FROM domain_events WHERE subject_type='work_item' AND subject_id=? AND kind=? ORDER BY seq DESC LIMIT 1`, workID, WorkflowContractApproved).Scan(&payloadVersion, &payload); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("contract approval payload is not JSON: %v", err)
	}
	return payloadVersion, decoded
}

// requireApprovalBindingRefusal asserts a typed refusal without naming a
// failure kind the store does not pin.
func requireApprovalBindingRefusal(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the store accepted an approval binding it must refuse")
	}
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("refusal is not typed: %v", err)
	}
}

// requireApprovalBindingIdentity asserts the event carries exactly the
// recorded revision and digest pair.
func requireApprovalBindingIdentity(t *testing.T, payload map[string]any, revision int64, digest string) {
	t.Helper()
	member, present := payload["design_binding"]
	if !present {
		t.Fatalf("the approval event carries no design_binding member: %v", payload)
	}
	raw, err := json.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	var bound struct {
		Revision int64  `json:"revision"`
		Digest   string `json:"digest"`
	}
	if err := json.Unmarshal(raw, &bound); err != nil {
		t.Fatalf("design_binding is not the typed revision and digest pair: %v", err)
	}
	if bound.Revision != revision || bound.Digest != digest {
		t.Fatalf("recorded design_binding = {revision:%d digest:%q}, want the exact current identity {revision:%d digest:%q}", bound.Revision, bound.Digest, revision, digest)
	}
}

// approvalBindingForgedApprovalEvent builds one raw approval event at the
// binding-bearing payload version, bypassing the constructor. The fold is the
// material authority the bypass must meet, so the shape is otherwise the
// exact contract the constructor writes.
func approvalBindingForgedApprovalEvent(t *testing.T, fixture approvalBindingFixture, workID string, version int64, binding any, withMember bool) Event {
	t.Helper()
	fields := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 1, "premise": "the fold owns the binding authority",
		"outcome_predicates": approvalBindingPredicates(),
		"required_evidence":  []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "law_revisions": []any{}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	}
	if withMember {
		fields["design_binding"] = binding
	}
	event := workflowEventWithActor("binding-raw-"+workID, WorkflowContractApproved, workID, fixture.ownerRef, fields)
	event.PayloadVersion = 5
	return event
}

// approvalBindingForgedV4Event builds one raw approval event at the last
// historical payload version, bypassing the constructor. The live fold must
// hold a pin that requires the binding to its own admission, not to the
// version the raw event happens to carry.
func approvalBindingForgedV4Event(t *testing.T, fixture approvalBindingFixture, workID string, version int64) Event {
	t.Helper()
	fields := map[string]any{
		"work_id": workID, "expected_version": version, "resulting_version": version + 1,
		"contract_version": 1, "premise": "the fold owns the binding authority",
		"outcome_predicates": approvalBindingPredicates(),
		"required_evidence":  []string{"verification"}, "route_conventions": []string{},
		"spec_mandate": []string{}, "law_revisions": []any{}, "law_boundary_version": 1,
		"rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
	}
	event := workflowEventWithActor("binding-raw-v4-"+workID, WorkflowContractApproved, workID, fixture.ownerRef, fields)
	event.PayloadVersion = 4
	return event
}

// approvalBindingRawForgedEvent builds one raw binding-bearing approval event
// from whole payload bytes, so the recorded member keeps the exact JSON the
// caller wrote: duplicate keys survive into the fold undecoded.
func approvalBindingRawForgedEvent(t *testing.T, fixture approvalBindingFixture, workID string, version int64, rawMember string) Event {
	t.Helper()
	payload := fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"resulting_version":%d,"contract_version":1,"premise":"the fold owns the binding authority","outcome_predicates":[{"predicate_id":"predicate:approval-binding","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:approval-binding","immutable_subject_ref":"commit:%s","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_revisions":[],"law_boundary_version":1,"rigor_class":"prototype_internal","consequence_class":"internal_sqlite","design_binding":%s}`, workID, version, version+1, strings.Repeat("a", 64), rawMember)
	event := workflowEventWithActor("binding-raw-bytes-"+workID, WorkflowContractApproved, workID, fixture.ownerRef, map[string]any{"work_id": workID})
	event.Payload = json.RawMessage(payload)
	event.PayloadVersion = 5
	return event
}

// approvalBindingAliasedEvent builds one raw binding-bearing approval event
// from whole payload bytes with the caller's exact binding member plus extra
// raw members, so an alias key rides the same bytes the fold reads.
func approvalBindingAliasedEvent(t *testing.T, fixture approvalBindingFixture, workID string, version int64, rawBinding, extraMembers string) Event {
	t.Helper()
	payload := fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"resulting_version":%d,"contract_version":1,"premise":"the fold owns the binding authority","outcome_predicates":[{"predicate_id":"predicate:approval-binding","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:approval-binding","immutable_subject_ref":"commit:%s","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_revisions":[],"law_boundary_version":1,"rigor_class":"prototype_internal","consequence_class":"internal_sqlite","design_binding":%s%s}`, workID, version, version+1, strings.Repeat("a", 64), rawBinding, extraMembers)
	event := workflowEventWithActor("binding-alias-"+workID, WorkflowContractApproved, workID, fixture.ownerRef, map[string]any{"work_id": workID})
	event.Payload = json.RawMessage(payload)
	event.PayloadVersion = 5
	return event
}

// recordApprovalBindingDesign appends one more architecture-bearing design
// through the design writer, so the current identity moves past the recorded
// binding.
func recordApprovalBindingDesign(t *testing.T, fixture approvalBindingFixture, workID, statement string) string {
	t.Helper()
	moved := projectionArchitecture()
	moved.Invariants[0].Statement = statement
	version := projectionWorkVersion(t, fixture.store, workID)
	if err := applyProjectionEvents(t, fixture.store, workID, version, projectionDesignEvent(t, workID, "binding-design-move-"+workID, version, "moved", &moved)); err != nil {
		t.Fatal(err)
	}
	current, _, err := readCurrentWorkflowDesign(context.Background(), fixture.store.DatabaseForTesting(), workID)
	if err != nil || current == nil || current.Digest == "" {
		t.Fatalf("moved design carries no digest: %v", err)
	}
	return current.Digest
}

func TestArchitectureApprovalBinding(t *testing.T) {
	ctx := context.Background()

	t.Run("a new pin's exact design binding enters the approval event", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-exact", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-exact"
		if err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bound change", map[string]any{"revision": fixture.revision, "digest": fixture.digest})); err != nil {
			t.Fatalf("the exact current binding must approve: %v", err)
		}
		payloadVersion, payload := approvalBindingEvent(t, fixture.store, workID)
		if payloadVersion != 5 {
			t.Fatalf("approval payload version = %d, want the binding-bearing version 5", payloadVersion)
		}
		requireApprovalBindingIdentity(t, payload, fixture.revision, fixture.digest)
		if payload["contract_version"].(float64) != 1 {
			t.Fatalf("approval contract_version = %v, want 1", payload["contract_version"])
		}
		if state := readApprovalBindingState(t, fixture.store, workID); state.contracts != 1 {
			t.Fatalf("active contracts = %d, want the exact approval to record one", state.contracts)
		}
	})

	t.Run("a stale design binding refuses with zero effects", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-stale", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-stale"
		movedDigest := recordApprovalBindingDesign(t, fixture, workID, "the revised plan the approval must match instead")
		if movedDigest == fixture.digest {
			t.Fatal("the moved design kept the predecessor digest; the fixture cannot present a stale identity")
		}
		before := readApprovalBindingState(t, fixture.store, workID)
		err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bound change", map[string]any{"revision": fixture.revision, "digest": fixture.digest}))
		requireApprovalBindingRefusal(t, err)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a substituted design digest refuses with zero effects", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-sub", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-sub"
		before := readApprovalBindingState(t, fixture.store, workID)
		substituted := map[string]any{"revision": fixture.revision, "digest": "sha256:" + strings.Repeat("f", 64)}
		err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bound change", substituted))
		requireApprovalBindingRefusal(t, err)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a binding pin over an architecture-absent design refuses", func(t *testing.T) {
		fixture := seedApprovalBindingFixture(t, "approval-binding-legacy-design", approvalBindingPinRef, true, nil)
		const workID = "approval-binding-legacy-design"
		before := readApprovalBindingState(t, fixture.store, workID)
		invented := map[string]any{"revision": 1, "digest": "sha256:" + strings.Repeat("e", 64)}
		err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bound change", invented))
		requireApprovalBindingRefusal(t, err)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("rebuild retains the recorded binding while the current design changed", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-rebuild", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-rebuild"
		if err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bound change", map[string]any{"revision": fixture.revision, "digest": fixture.digest})); err != nil {
			t.Fatalf("the exact current binding must approve: %v", err)
		}
		movedDigest := recordApprovalBindingDesign(t, fixture, workID, "the design moved on after the approval recorded its binding")
		if movedDigest == fixture.digest {
			t.Fatal("the moved design kept the recorded digest; the rebuild assertion cannot distinguish event-backed authority from a re-derived one")
		}
		if err := RebuildFromLog(ctx, fixture.store); err != nil {
			t.Fatal(err)
		}
		payloadVersion, payload := approvalBindingEvent(t, fixture.store, workID)
		if payloadVersion != 5 {
			t.Fatalf("rebuilt approval payload version = %d, want the binding-bearing version 5", payloadVersion)
		}
		// The recorded identity, not the moved current identity.
		requireApprovalBindingIdentity(t, payload, fixture.revision, fixture.digest)
	})

	// The fold is the material authority: a raw binding-bearing approval that
	// bypasses the constructor must meet the same admission the constructor
	// performs, and no stored version below the binding can acquire the
	// member.
	t.Run("the fold admits a raw binding-bearing approval with the exact identity", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-raw-exact", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-raw-exact"
		version := projectionWorkVersion(t, fixture.store, workID)
		event := approvalBindingForgedApprovalEvent(t, fixture, workID, version, map[string]any{"revision": fixture.revision, "digest": fixture.digest}, true)
		if err := applyProjectionEvents(t, fixture.store, workID, version, event); err != nil {
			t.Fatalf("the fold must admit the raw approval whose binding names the exact current identity: %v", err)
		}
		payloadVersion, payload := approvalBindingEvent(t, fixture.store, workID)
		if payloadVersion != 5 {
			t.Fatalf("stored raw approval payload version = %d, want 5", payloadVersion)
		}
		requireApprovalBindingIdentity(t, payload, fixture.revision, fixture.digest)
		if state := readApprovalBindingState(t, fixture.store, workID); state.contracts != 1 {
			t.Fatalf("active contracts = %d, want the admitted raw approval to record one", state.contracts)
		}
		// Every admitted binding stays readable: the reader reports the exact
		// recorded identity from the stored event.
		recorded, _, err := workflowContractDesignBindingTx(ctx, fixture.store.DatabaseForTesting(), workID)
		if err != nil {
			t.Fatalf("the reader refused the admitted raw approval: %v", err)
		}
		if recorded == nil || recorded.Revision != fixture.revision || recorded.Digest != fixture.digest {
			t.Fatalf("reader after the admitted raw approval = %+v, want the exact recorded binding", recorded)
		}
	})

	t.Run("a raw approval event with a substituted binding refuses at the fold", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-raw-sub", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-raw-sub"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		event := approvalBindingForgedApprovalEvent(t, fixture, workID, version, map[string]any{"revision": fixture.revision, "digest": "sha256:" + strings.Repeat("f", 64)}, true)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a raw approval event with a stale binding refuses at the fold", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-raw-stale", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-raw-stale"
		movedDigest := recordApprovalBindingDesign(t, fixture, workID, "the design moved before the raw approval arrived")
		if movedDigest == fixture.digest {
			t.Fatal("the moved design kept the predecessor digest; the fixture cannot present a stale identity")
		}
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		event := approvalBindingForgedApprovalEvent(t, fixture, workID, version, map[string]any{"revision": fixture.revision, "digest": fixture.digest}, true)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a raw approval event with malformed binding members refuses", func(t *testing.T) {
		validDigest := "sha256:" + strings.Repeat("a", 64)
		for _, testCase := range []struct {
			name   string
			member any
		}{
			{"string member", "not-a-binding"},
			{"zero revision", map[string]any{"revision": 0, "digest": validDigest}},
			{"negative revision", map[string]any{"revision": -1, "digest": validDigest}},
			{"short digest", map[string]any{"revision": 1, "digest": "sha256:short"}},
			{"unknown member", map[string]any{"revision": 1, "digest": validDigest, "approved": true}},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				plan := projectionArchitecture()
				fixture := seedApprovalBindingFixture(t, "approval-binding-raw-malformed", approvalBindingPinRef, true, &plan)
				const workID = "approval-binding-raw-malformed"
				version := projectionWorkVersion(t, fixture.store, workID)
				before := readApprovalBindingState(t, fixture.store, workID)
				event := approvalBindingForgedApprovalEvent(t, fixture, workID, version, testCase.member, true)
				err := applyProjectionEvents(t, fixture.store, workID, version, event)
				assertFailureKind(t, err, KindInvalidPayload)
				requireApprovalBindingUnchanged(t, fixture.store, workID, before)
			})
		}
	})

	t.Run("a raw approval event without the binding member refuses", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-raw-absent", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-raw-absent"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		event := approvalBindingForgedApprovalEvent(t, fixture, workID, version, nil, false)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a raw approval event with a null binding member refuses", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-raw-null", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-raw-null"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		event := approvalBindingForgedApprovalEvent(t, fixture, workID, version, nil, true)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a raw binding event under a pin that does not declare it refuses", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-raw-undeclared", approvalBindingLegacyRef, false, &plan)
		const workID = "approval-binding-raw-undeclared"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		event := approvalBindingForgedApprovalEvent(t, fixture, workID, version, map[string]any{"revision": fixture.revision, "digest": fixture.digest}, true)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("the reader reports the recorded identity from the approval event alone", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-reader", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-reader"
		if err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bound change", map[string]any{"revision": fixture.revision, "digest": fixture.digest})); err != nil {
			t.Fatalf("the exact current binding must approve: %v", err)
		}
		read := func() (*workflowDesignBinding, int) {
			t.Helper()
			binding, version, err := workflowContractDesignBindingTx(ctx, fixture.store.DatabaseForTesting(), workID)
			if err != nil {
				t.Fatalf("the recorded binding reader refused: %v", err)
			}
			return binding, version
		}
		binding, version := read()
		if binding == nil || version != 5 {
			t.Fatalf("reader = %+v at version %d, want the recorded binding at version 5", binding, version)
		}
		if binding.Revision != fixture.revision || binding.Digest != fixture.digest {
			t.Fatalf("reader binding = {revision:%d digest:%q}, want the exact recorded identity", binding.Revision, binding.Digest)
		}
		movedDigest := recordApprovalBindingDesign(t, fixture, workID, "the design moved on while the recorded identity stands")
		if movedDigest == fixture.digest {
			t.Fatal("the moved design kept the recorded digest; the reader cannot prove it reads the event")
		}
		binding, version = read()
		if binding == nil || version != 5 || binding.Revision != fixture.revision || binding.Digest != fixture.digest {
			t.Fatalf("reader after the design moved = %+v at version %d, want the recorded identity, not the current design", binding, version)
		}
		if err := RebuildFromLog(ctx, fixture.store); err != nil {
			t.Fatal(err)
		}
		binding, version = read()
		if binding == nil || version != 5 || binding.Revision != fixture.revision || binding.Digest != fixture.digest {
			t.Fatalf("reader after rebuild = %+v at version %d, want the same recorded binding from the event", binding, version)
		}
	})

	t.Run("the reader reports no binding for a legacy approval", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-reader-legacy", approvalBindingLegacyRef, false, &plan)
		const workID = "approval-binding-reader-legacy"
		if err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bounded change", nil)); err != nil {
			t.Fatalf("the legacy exact approval must stay admitted: %v", err)
		}
		binding, version, err := workflowContractDesignBindingTx(ctx, fixture.store.DatabaseForTesting(), workID)
		if err != nil || binding != nil || version != 4 {
			t.Fatalf("reader = %+v at version %d err %v, want no binding at the legacy version 4", binding, version, err)
		}
	})

	t.Run("the reader reports the active contract after supersession", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-superseded", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-superseded"
		if err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bound change", map[string]any{"revision": fixture.revision, "digest": fixture.digest})); err != nil {
			t.Fatalf("the exact current binding must approve: %v", err)
		}
		binding, _, err := workflowContractDesignBindingTx(ctx, fixture.store.DatabaseForTesting(), workID)
		if err != nil || binding == nil || binding.Revision != fixture.revision || binding.Digest != fixture.digest {
			t.Fatalf("reader before supersession = %+v err %v, want the recorded binding", binding, err)
		}
		operator := operatorVerdictActor(t, workID)
		if err := runIssue933OperatorAction(t, fixture.store, workID, "supersede_contract", issue1013SuccessorContract(), fixture.owner, operator); err != nil {
			t.Fatalf("the approved supersession route must succeed: %v", err)
		}
		var active int64
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT contract_version FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active != 2 {
			t.Fatalf("active contract version = %d, want the supersession to have installed the successor 2", active)
		}
		read := func() *workflowDesignBinding {
			t.Helper()
			activeBinding, _, readErr := workflowContractDesignBindingTx(ctx, fixture.store.DatabaseForTesting(), workID)
			if readErr != nil {
				t.Fatalf("the reader refused after supersession: %v", readErr)
			}
			return activeBinding
		}
		if activeBinding := read(); activeBinding != nil {
			t.Fatalf("reader after supersession = {revision:%d digest:%q}, want no binding: the active successor is unbound and must not inherit the superseded predecessor's identity", activeBinding.Revision, activeBinding.Digest)
		}
		if err := RebuildFromLog(ctx, fixture.store); err != nil {
			t.Fatal(err)
		}
		if activeBinding := read(); activeBinding != nil {
			t.Fatalf("reader after rebuild = {revision:%d digest:%q}, want no binding from the active-contract join over the log", activeBinding.Revision, activeBinding.Digest)
		}
	})

	t.Run("a native v5 approval without the binding member refuses on a legacy pin", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-legacy-native", approvalBindingLegacyRef, false, &plan)
		const workID = "approval-binding-legacy-native"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		event := approvalBindingForgedApprovalEvent(t, fixture, workID, version, nil, false)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a native v5 approval with a null binding member refuses on a legacy pin", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-legacy-null", approvalBindingLegacyRef, false, &plan)
		const workID = "approval-binding-legacy-null"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		event := approvalBindingForgedApprovalEvent(t, fixture, workID, version, nil, true)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("capability requires the closed required object declaration", func(t *testing.T) {
		validDigest := "sha256:" + strings.Repeat("a", 64)
		typedMember := func() []WorkflowPayloadField {
			return []WorkflowPayloadField{{Name: "design_binding", ValueType: PayloadObject, Required: false}}
		}
		for _, testCase := range []struct {
			name     string
			ref      string
			workID   string
			malform  func(field WorkflowPayloadField) ([]WorkflowPayloadField, bool)
			binding  any
			withMemb bool
		}{
			{"optional member", approvalBindingLooseRef, "approval-binding-loose", func(field WorkflowPayloadField) ([]WorkflowPayloadField, bool) { return typedMember(), true }, map[string]any{"revision": 1, "digest": validDigest}, true},
			{"string member type", approvalBindingLooseStringRef, "approval-binding-loose-string", func(field WorkflowPayloadField) ([]WorkflowPayloadField, bool) {
				return []WorkflowPayloadField{{Name: "design_binding", ValueType: PayloadString, Required: false, MaxLength: workflowInt(256)}}, true
			}, validDigest, true},
			{"open payload", approvalBindingLooseOpenRef, "approval-binding-loose-open", func(field WorkflowPayloadField) ([]WorkflowPayloadField, bool) {
				return []WorkflowPayloadField{field}, false
			}, map[string]any{"revision": 1, "digest": validDigest}, true},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				plan := projectionArchitecture()
				definition := approvalBindingMalformedDefinition(t, testCase.ref, testCase.malform)
				fixture := seedApprovalBindingFixtureWithDefinition(t, testCase.workID, definition, &plan)
				workID := testCase.workID
				if err := runApprovalBindingAction(t, fixture, workID, approvalBindingRawMemberPayload(t, "deliver the bounded change", testCase.binding, testCase.withMemb)); err != nil {
					t.Fatalf("a pin without the closed required object declaration must keep the released approval shape: %v", err)
				}
				payloadVersion, payload := approvalBindingEvent(t, fixture.store, workID)
				if payloadVersion != 4 {
					t.Fatalf("malformed-declaration approval payload version = %d, want the released 4 with no capability", payloadVersion)
				}
				if _, present := payload["design_binding"]; present {
					t.Fatalf("the approval carries a design_binding member on a malformed declaration: %v", payload)
				}
				if state := readApprovalBindingState(t, fixture.store, workID); state.contracts != 1 {
					t.Fatalf("active contracts = %d, want the legacy-shape approval to record one", state.contracts)
				}
			})
		}
	})

	t.Run("a live raw v4 approval refuses under a pin that requires the binding", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-raw-v4", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-raw-v4"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		event := approvalBindingForgedV4Event(t, fixture, workID, version)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a raw approval event with a duplicate binding member refuses at the fold", func(t *testing.T) {
		digest := "sha256:" + strings.Repeat("a", 64)
		for _, testCase := range []struct {
			name   string
			member string
		}{
			{"duplicate revision keys", fmt.Sprintf(`{"revision":1,"revision":2,"digest":%q}`, digest)},
			{"duplicate digest keys", fmt.Sprintf(`{"revision":1,"digest":%q,"digest":%q}`, digest, digest)},
			{"missing digest member", `{"revision":1}`},
			{"extra raw member", fmt.Sprintf(`{"revision":1,"digest":%q,"approved":true}`, digest)},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				plan := projectionArchitecture()
				fixture := seedApprovalBindingFixture(t, "approval-binding-raw-bytes", approvalBindingPinRef, true, &plan)
				const workID = "approval-binding-raw-bytes"
				version := projectionWorkVersion(t, fixture.store, workID)
				before := readApprovalBindingState(t, fixture.store, workID)
				event := approvalBindingRawForgedEvent(t, fixture, workID, version, testCase.member)
				err := applyProjectionEvents(t, fixture.store, workID, version, event)
				assertFailureKind(t, err, KindInvalidPayload)
				requireApprovalBindingUnchanged(t, fixture.store, workID, before)
			})
		}
	})

	t.Run("duplicate identical binding members refuse at the fold", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-raw-dup", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-raw-dup"
		member := fmt.Sprintf(`{"revision":%d,"revision":%d,"digest":%q,"digest":%q}`, fixture.revision, fixture.revision, fixture.digest, fixture.digest)
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		event := approvalBindingRawForgedEvent(t, fixture, workID, version, member)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a substituted lowercase binding behind an uppercase alias refuses", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-alias-binding", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-alias-binding"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		rawBinding := fmt.Sprintf(`{"revision":%d,"digest":"sha256:%s"}`, fixture.revision, strings.Repeat("f", 64))
		alias := fmt.Sprintf(`,"DESIGN_BINDING":{"revision":%d,"digest":%q}`, fixture.revision, fixture.digest)
		event := approvalBindingAliasedEvent(t, fixture, workID, version, rawBinding, alias)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a contract version alias diverges from the bound contract", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-alias-version", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-alias-version"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		rawBinding := fmt.Sprintf(`{"revision":%d,"digest":%q}`, fixture.revision, fixture.digest)
		event := workflowEventWithActor("binding-alias-version-"+workID, WorkflowContractApproved, workID, fixture.ownerRef, map[string]any{"work_id": workID})
		payload := fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"resulting_version":%d,"contract_version":2,"premise":"the fold owns the binding authority","outcome_predicates":[{"predicate_id":"predicate:approval-binding","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:approval-binding","immutable_subject_ref":"commit:%s","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_revisions":[],"law_boundary_version":1,"rigor_class":"prototype_internal","consequence_class":"internal_sqlite","design_binding":%s,"CONTRACT_VERSION":1}`, workID, version, version+1, strings.Repeat("a", 64), rawBinding)
		event.Payload = json.RawMessage(payload)
		event.PayloadVersion = 5
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a work id alias refuses on the native surface", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-alias-work", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-alias-work"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		rawBinding := fmt.Sprintf(`{"revision":%d,"digest":%q}`, fixture.revision, fixture.digest)
		alias := fmt.Sprintf(`,"WORK_ID":%q`, workID)
		event := approvalBindingAliasedEvent(t, fixture, workID, version, rawBinding, alias)
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("duplicate canonical contract versions refuse at the fold", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-dup-version", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-dup-version"
		version := projectionWorkVersion(t, fixture.store, workID)
		before := readApprovalBindingState(t, fixture.store, workID)
		rawBinding := fmt.Sprintf(`{"revision":%d,"digest":%q}`, fixture.revision, fixture.digest)
		event := workflowEventWithActor("binding-dup-version-"+workID, WorkflowContractApproved, workID, fixture.ownerRef, map[string]any{"work_id": workID})
		payload := fmt.Sprintf(`{"work_id":%q,"expected_version":%d,"resulting_version":%d,"contract_version":2,"contract_version":1,"premise":"the fold owns the binding authority","outcome_predicates":[{"predicate_id":"predicate:approval-binding","ordinal":0,"outcome_kind":"check","outcome_payload":{"kind":"check","check_ref":"check:approval-binding","immutable_subject_ref":"commit:%s","expected_result":"pass"}}],"required_evidence":["verification"],"route_conventions":[],"spec_mandate":[],"law_revisions":[],"law_boundary_version":1,"rigor_class":"prototype_internal","consequence_class":"internal_sqlite","design_binding":%s}`, workID, version, version+1, strings.Repeat("a", 64), rawBinding)
		event.Payload = json.RawMessage(payload)
		event.PayloadVersion = 5
		err := applyProjectionEvents(t, fixture.store, workID, version, event)
		assertFailureKind(t, err, KindInvalidPayload)
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("an ambiguous active-contract projection refuses the reader", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-ambiguous", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-ambiguous"
		if err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bound change", map[string]any{"revision": fixture.revision, "digest": fixture.digest})); err != nil {
			t.Fatalf("the exact current binding must approve: %v", err)
		}
		if _, err := fixture.store.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES(1); INSERT INTO workflow_contracts(work_id,contract_version,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class) SELECT work_id,2,premise,consequence_class,required_evidence,route_conventions,approved_at,approved_by,spec_mandate,law_modifies,law_boundary_version,rigor_class FROM workflow_contracts WHERE work_id=? AND contract_version=1; DELETE FROM fold_guard`, workID); err != nil {
			t.Fatal(err)
		}
		var active int64
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active != 2 {
			t.Fatalf("active contracts = %d, want the duplicated projection the reader must refuse", active)
		}
		_, _, err := workflowContractDesignBindingTx(ctx, fixture.store.DatabaseForTesting(), workID)
		assertFailureKind(t, err, KindInvariantViolation)
	})

	t.Run("a legacy pin keeps payload version four with no binding member", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-legacy", approvalBindingLegacyRef, false, &plan)
		const workID = "approval-binding-legacy"
		if err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bounded change", nil)); err != nil {
			t.Fatalf("the legacy exact approval must stay admitted: %v", err)
		}
		payloadVersion, payload := approvalBindingEvent(t, fixture.store, workID)
		if payloadVersion != 4 {
			t.Fatalf("legacy approval payload version = %d, want the released 4", payloadVersion)
		}
		if _, present := payload["design_binding"]; present {
			t.Fatalf("the legacy approval event carries a design_binding member: %v", payload)
		}
		if state := readApprovalBindingState(t, fixture.store, workID); state.contracts != 1 {
			t.Fatalf("active contracts = %d, want the legacy approval to record one", state.contracts)
		}
		var approvedBy string
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT approved_by FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&approvedBy); err != nil {
			t.Fatal(err)
		}
		if approvedBy != fixture.ownerRef {
			t.Fatalf("approved_by = %q, want the approving owner actor %q", approvedBy, fixture.ownerRef)
		}
	})

	t.Run("an undeclared binding member refuses on the legacy pin", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-undeclared", approvalBindingLegacyRef, false, &plan)
		const workID = "approval-binding-undeclared"
		before := readApprovalBindingState(t, fixture.store, workID)
		capability := map[string]any{"revision": fixture.revision, "digest": fixture.digest}
		err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bounded change", capability))
		if err == nil {
			t.Fatal("the legacy pin admitted an undeclared design_binding member")
		}
		assertFailureKind(t, err, KindInvalidPayload)
		var failure *Failure
		if !errors.As(err, &failure) || !strings.Contains(failure.Detail, "not declared") {
			t.Fatalf("refusal detail = %v, want the closed-declaration refusal", err)
		}
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
	})

	t.Run("a v4 approval payload cannot carry the binding", func(t *testing.T) {
		for _, member := range []struct {
			name  string
			value any
		}{
			{"object member", map[string]any{"revision": 1, "digest": "sha256:" + strings.Repeat("d", 64)}},
			{"null member", nil},
		} {
			t.Run(member.name, func(t *testing.T) {
				s := openTemp(t)
				defer s.Close()
				const workID = "approval-binding-forged"
				seedWork(t, s, workID)
				before := readApprovalBindingState(t, s, workID)
				forged := map[string]any{
					"work_id": workID, "expected_version": 2, "resulting_version": 3,
					"contract_version": 1, "premise": "the binding cannot ride the released payload shape",
					"outcome_kind":      "check",
					"outcome_payload":   map[string]any{"kind": "check", "check_ref": "check:approval-binding", "immutable_subject_ref": "commit:" + workID, "expected_result": "pass"},
					"required_evidence": []string{"verification"}, "route_conventions": []string{},
					"spec_mandate": []string{}, "rigor_class": "prototype_internal", "consequence_class": "internal_sqlite",
					"design_binding": member.value,
				}
				event := workflowEventWithActor("binding-forged-"+workID, WorkflowContractApproved, workID, "actor:test", forged)
				event.PayloadVersion = 4
				err := applyProjectionEvents(t, s, workID, 2, event)
				if err == nil {
					t.Fatal("a v4 approval payload admitted the design_binding member")
				}
				assertFailureKind(t, err, KindInvalidPayload)
				requireApprovalBindingUnchanged(t, s, workID, before)
			})
		}
	})

	t.Run("the approval creates no synthetic operator", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-operator", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-operator"
		var actorsBefore int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&actorsBefore); err != nil {
			t.Fatal(err)
		}
		if err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bound change", map[string]any{"revision": fixture.revision, "digest": fixture.digest})); err != nil {
			t.Fatalf("the exact current binding must approve: %v", err)
		}
		var actorsAfter int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors`).Scan(&actorsAfter); err != nil {
			t.Fatal(err)
		}
		if actorsAfter != actorsBefore {
			t.Fatalf("workflow actors = %d after the approval, want the seeded %d with no synthetic addition", actorsAfter, actorsBefore)
		}
		var operators int
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_actors WHERE actor_class='operator'`).Scan(&operators); err != nil {
			t.Fatal(err)
		}
		if operators != 0 {
			t.Fatalf("operator actors = %d, want the binding path to mint none", operators)
		}
		var approvedBy string
		if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT approved_by FROM workflow_contracts WHERE work_id=? AND superseded_by IS NULL`, workID).Scan(&approvedBy); err != nil {
			t.Fatal(err)
		}
		if approvedBy != fixture.ownerRef {
			t.Fatalf("approved_by = %q, want the approving owner actor %q", approvedBy, fixture.ownerRef)
		}
	})

	t.Run("post-fold capacity admission holds at the approval", func(t *testing.T) {
		plan := projectionArchitecture()
		fixture := seedApprovalBindingFixture(t, "approval-binding-capacity", approvalBindingPinRef, true, &plan)
		const workID = "approval-binding-capacity"
		over := strings.Repeat("p", 4087)
		before := readApprovalBindingState(t, fixture.store, workID)
		err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, over, map[string]any{"revision": fixture.revision, "digest": fixture.digest}))
		_ = capacityWritesRefusal(t, err, "task")
		requireApprovalBindingUnchanged(t, fixture.store, workID, before)
		if err := runApprovalBindingAction(t, fixture, workID, approvalBindingPayload(t, "deliver the bounded bound change", map[string]any{"revision": fixture.revision, "digest": fixture.digest})); err != nil {
			t.Fatalf("the bounded approval within the declared slot must commit: %v", err)
		}
		if state := readApprovalBindingState(t, fixture.store, workID); state.contracts != 1 {
			t.Fatalf("active contracts = %d, want the bounded approval to record one", state.contracts)
		}
	})
}
