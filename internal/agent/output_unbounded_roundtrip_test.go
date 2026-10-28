package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/store"
)

// The workflow_read producers for classified conditions (blocking and
// unreadable) and completion warnings have no finite cardinality guard:
// deriveWorkflowReady scans every open condition and the persisted staleness
// arm returns one warning per distinct drifted rule id. These tests hold the
// real dispatch boundary to that: the producer's whole result publishes whole
// at any count, readiness derives from every open condition, and only a
// result too large for the 51200-byte envelope refuses, as the typed
// limit_exceeded byte refusal, never a field-schema failure.

const (
	// unboundedWitnessBlocking is the classified-condition count the
	// fixed-projection witness builds. At the maximum published identifier
	// width the complete workflow member alone exceeds the envelope ceiling,
	// so no event page limit can fit it and the read must answer the typed
	// byte refusal. The witness never relies on an oversized single history
	// event.
	unboundedWitnessBlocking = 280
	unboundedWitnessIDWidth  = 128
)

// unboundedCondition pairs one recorded condition with the durable operation
// its resolution authority names.
type unboundedCondition struct {
	id, opID string
}

// unboundedConditionAt derives a distinct condition identity. width pads the
// published identifier to its schema maximum; the authority stays inside the
// fold's 128-byte resolution_authority guard.
func unboundedConditionAt(index, width int) unboundedCondition {
	id := fmt.Sprintf("condition:c%03d", index)
	if width > len(id) {
		id += strings.Repeat("w", width-len(id))
	}
	suffix := strings.TrimPrefix(id, "condition:")
	if len(suffix) > 107 {
		suffix = suffix[:107]
	}
	return unboundedCondition{id: id, opID: "op-" + suffix}
}

// seedWorkflowConditionAuthority records the completed durable operation one
// condition's resolution authority names, through the production receipt
// shape the fold verifies against.
func seedWorkflowConditionAuthority(t *testing.T, s *store.Store, workID, opID, evidenceRef string) {
	t.Helper()
	var typeRef string
	var typeDigest string
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT definition_ref,definition_digest,current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&typeRef, &typeDigest, &step); err != nil {
		t.Fatal(err)
	}
	at := fixedTime().Format(time.RFC3339Nano)
	evidence, err := json.Marshal([]string{evidenceRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO durable_operations(op_id,attempt_epoch,work_id,workflow_type_ref,workflow_type_digest,step_id,step_kind,accepted_inputs_digest,accepted_scope_snapshot,result_kind,result_payload,evidence_refs,principal_ref,request_id,observed_at,completed_at,contract_digest) VALUES(?,1,?,?,?,?,'external_effect',?,?,'completed','{}',?,'principal/operator',?,?,?,?)`,
		opID, workID, typeRef, typeDigest, step, agentcontract.ManifestDigest, "{}", string(evidence), "req-"+opID, at, at, agentcontract.ManifestDigest); err != nil {
		t.Fatalf("seed the condition authority %s: %v", opID, err)
	}
}

// seedWorkflowConditions supplies the current rows the read producer classifies.
func seedWorkflowConditions(t *testing.T, s *store.Store, workID string, conditions []unboundedCondition) {
	t.Helper()
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	for _, condition := range conditions {
		if _, err := tx.Exec(`INSERT INTO workflow_external_conditions(work_id,condition_id,await_type,await_ref,resolution_authority,condition_state,recorded_at) VALUES(?,?,'ci_result',?,?,'open',?)`,
			workID, condition.id, "await:"+strings.TrimPrefix(condition.id, "condition:"), "durable_operation:"+condition.opID, fixedTime().Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("seed current condition %s: %v", condition.id, err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM write_guard`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// seedDistinctStalenessWarnings records count drifted staleness observations
// with distinct caller-supplied rule ids — the persisted warning arm whose
// producer the published 32-item cap cannot bound.
func seedDistinctStalenessWarnings(t *testing.T, s *store.Store, workID string, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		ruleID := fmt.Sprintf("staleness:w%02d", i)
		payload, err := json.Marshal(map[string]any{"staleness_rule_id": ruleID, "observed_drift": map[string]any{"severity": "warning", "drifted": true}})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AppendWorkflowStalenessObservation(context.Background(), s, fmt.Sprintf("staleness-observed-w%02d", i), workID, "operator", agentcontract.ManifestDigest, payload, fixedTime()); err != nil {
			t.Fatalf("record staleness observation %s: %v", ruleID, err)
		}
		ids = append(ids, ruleID)
	}
	sort.Strings(ids)
	return ids
}

// classifiedReadFixture is one break-fix workflow whose open-condition and
// warning populations are current store state.
type classifiedReadFixture struct {
	store    *store.Store
	service  *Service
	grant    Authority
	blocking []string
	unknown  string
	warnings []string
}

// buildClassifiedReadFixture builds the fixture: blocking conditions with
// live authority receipts, an optional condition whose authority receipt is
// removed after seeding (the unreadable-authority shape the ready
// derivation must classify without rewriting), and distinct persisted
// warnings.
func buildClassifiedReadFixture(t *testing.T, blockingCount, idWidth, warningCount int, withUnknown bool) classifiedReadFixture {
	t.Helper()
	const workID = "work-1"
	s, service, _, _, _ := workflowEngineFixture(t, "")
	conditions := make([]unboundedCondition, 0, blockingCount+1)
	blocking := make([]string, 0, blockingCount)
	for i := 0; i < blockingCount; i++ {
		condition := unboundedConditionAt(i, idWidth)
		conditions = append(conditions, condition)
		blocking = append(blocking, condition.id)
	}
	unknown := ""
	if withUnknown {
		unknownCondition := unboundedCondition{id: "condition:unknown-authority", opID: "op-unknown-authority"}
		conditions = append(conditions, unknownCondition)
		unknown = unknownCondition.id
	}
	for _, condition := range conditions {
		seedWorkflowConditionAuthority(t, s, workID, condition.opID, "evidence:"+condition.opID)
	}
	warnings := seedDistinctStalenessWarnings(t, s, workID, warningCount)
	seedWorkflowConditions(t, s, workID, conditions)
	if withUnknown {
		if _, err := s.DatabaseForTesting().Exec(`DELETE FROM durable_operations WHERE op_id=?`, "op-unknown-authority"); err != nil {
			t.Fatal(err)
		}
	}
	grant, err := service.Authorize(context.Background(), Invocation{ClientRef: "client-session-exec-aaaa", PrincipalRef: "human-1", SessionRef: "session-exec-aaaa", AgentRef: "agent-exec", Directory: "/repo", Worktree: "/repo-wt", ManifestDigest: agentcontract.ManifestDigest, RequiredCapability: "product_read", ProductID: "product-1", ProjectID: "project-1"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(blocking)
	return classifiedReadFixture{store: s, service: service, grant: grant, blocking: blocking, unknown: unknown, warnings: warnings}
}

// dispatchUnboundedHistory runs the real concord_work_trace.history
// dispatch. A non-nil error means the boundary refused outside the typed
// envelope, which the callers report as the failure mode it is.
func dispatchUnboundedHistory(t *testing.T, fixture classifiedReadFixture) (agentcontract.Envelope, error) {
	const input = `{"work_id":"work-1","limit":1}`
	t.Helper()
	scopeVersion, _, err := fixture.store.ScopeVersion(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	return Dispatch(context.Background(), fixture.store, fixture.service, InvokeRequest{Tool: "concord_work_trace", Operation: "history", Input: json.RawMessage(input)}, mutationEnvelope(fixture.grant, scopeVersion))
}

// fixtureStoreWorkflow reads the store projection beneath the agent envelope,
// where no published cap exists, to separate producer completeness from
// representation refusal.
func fixtureStoreWorkflow(t *testing.T, fixture classifiedReadFixture) *store.WorkflowReadProjection {
	t.Helper()
	out, err := fixture.store.QueryQ7(context.Background(), store.Q7Request{Work: "work-1"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Workflow == nil {
		t.Fatal("store projection omitted the workflow member")
	}
	return out.Workflow
}

type unboundedWorkflowMembers struct {
	Ready      bool `json:"ready"`
	Conditions []struct {
		ID string `json:"id"`
	} `json:"conditions"`
	UnresolvedConditions []string `json:"unresolved_conditions"`
	UnreadableConditions []string `json:"unreadable_conditions"`
	BlockingConditions   []string `json:"blocking_conditions"`
	CompletionWarnings   []string `json:"completion_warnings"`
}

type unboundedReadPage struct {
	Entries  []json.RawMessage         `json:"entries"`
	Workflow *unboundedWorkflowMembers `json:"workflow"`
}

func requireSortedEqual(t *testing.T, got, want []string, what string) {
	t.Helper()
	if !sort.StringsAreSorted(got) {
		t.Fatalf("%s = %v, want deterministic identifier order", what, got)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// assertConditionWindow holds the LIMIT-bounded display arrays to their
// producer contract: a prefix of the sorted condition population in order,
// with unresolved_conditions tracking the same window.
func assertConditionWindow(t *testing.T, members *unboundedWorkflowMembers, all []string) {
	t.Helper()
	ids := make([]string, 0, len(members.Conditions))
	for _, condition := range members.Conditions {
		ids = append(ids, condition.ID)
	}
	if len(ids) == 0 || len(ids) > len(all) {
		t.Fatalf("published %d conditions against %d recorded conditions, want a non-empty prefix window", len(ids), len(all))
	}
	if !slices.Equal(ids, all[:len(ids)]) {
		t.Fatalf("published conditions %v are not the sorted prefix of the %d recorded conditions", ids, len(all))
	}
	if !slices.Equal(members.UnresolvedConditions, ids) {
		t.Fatalf("unresolved_conditions %v do not match the published condition window %v", members.UnresolvedConditions, ids)
	}
}

func TestWorkflowReadRoundTripsClassifiedConditionsAndWarningsWhole(t *testing.T) {
	t.Parallel()
	t.Run("at the legacy caps 100 classified conditions and 32 warnings stay publishable", func(t *testing.T) {
		t.Parallel()
		fixture := buildClassifiedReadFixture(t, 100, 0, 32, false)
		projection := fixtureStoreWorkflow(t, fixture)
		if projection.Ready || len(projection.BlockingConditions) != 100 {
			t.Fatalf("store producer = ready %v with %d blocking conditions, want not-ready with all 100", projection.Ready, len(projection.BlockingConditions))
		}
		envelope, err := dispatchUnboundedHistory(t, fixture)
		if err != nil {
			t.Fatalf("at-cap history read failed outside the typed envelope: %v", err)
		}
		if envelope.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("at-cap workflow read refused: %+v", envelope.Error)
		}
		var page unboundedReadPage
		if err := json.Unmarshal(envelope.Result, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) != 1 {
			t.Fatalf("entries = %d, want the one limit-1 page entry with the workflow member intact", len(page.Entries))
		}
		if page.Workflow == nil {
			t.Fatal("history page omitted the workflow member")
		}
		if page.Workflow.Ready {
			t.Fatal("workflow read reported ready with every recorded condition open")
		}
		requireSortedEqual(t, page.Workflow.BlockingConditions, fixture.blocking, "blocking_conditions")
		if len(page.Workflow.UnreadableConditions) != 0 {
			t.Fatalf("unreadable_conditions = %v, want none", page.Workflow.UnreadableConditions)
		}
		requireSortedEqual(t, page.Workflow.CompletionWarnings, fixture.warnings, "completion_warnings")
		all := append([]string{}, fixture.blocking...)
		assertConditionWindow(t, page.Workflow, all)
	})

	t.Run("beyond the caps 101 classified conditions and 33 warnings round-trip whole", func(t *testing.T) {
		t.Parallel()
		fixture := buildClassifiedReadFixture(t, 101, 0, 33, true)
		// The store producer carries the whole population: 101 classified
		// blocking conditions, one unreadable authority, 33 distinct
		// warnings, and readiness derived from every open condition.
		projection := fixtureStoreWorkflow(t, fixture)
		if projection.Ready || len(projection.BlockingConditions) != 101 || !slices.Equal(projection.UnreadableConditions, []string{fixture.unknown}) || len(projection.CompletionWarnings) != 33 {
			t.Fatalf("store producer incomplete: ready %v blocking %d unreadable %v warnings %d", projection.Ready, len(projection.BlockingConditions), projection.UnreadableConditions, len(projection.CompletionWarnings))
		}
		envelope, err := dispatchUnboundedHistory(t, fixture)
		if err != nil {
			t.Fatalf("beyond-cap history read failed outside the typed envelope (a redundant output cap refusing the whole result): %v", err)
		}
		if envelope.Outcome != agentcontract.OutcomeOK {
			t.Fatalf("workflow read refused a whole producer result: %+v", envelope.Error)
		}
		var page unboundedReadPage
		if err := json.Unmarshal(envelope.Result, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) != 1 {
			t.Fatalf("entries = %d, want the one limit-1 page entry with the complete workflow member", len(page.Entries))
		}
		if page.Workflow == nil {
			t.Fatal("history page omitted the workflow member")
		}
		if page.Workflow.Ready {
			t.Fatal("workflow read reported ready with 102 open conditions")
		}
		requireSortedEqual(t, page.Workflow.BlockingConditions, fixture.blocking, "blocking_conditions")
		requireSortedEqual(t, page.Workflow.UnreadableConditions, []string{fixture.unknown}, "unreadable_conditions")
		requireSortedEqual(t, page.Workflow.CompletionWarnings, fixture.warnings, "completion_warnings")
		// Readiness derives from all conditions: the classified arrays
		// partition the whole open population, blocking plus unreadable.
		classified := append(append([]string{}, page.Workflow.BlockingConditions...), page.Workflow.UnreadableConditions...)
		sort.Strings(classified)
		all := append(append([]string{}, fixture.blocking...), fixture.unknown)
		sort.Strings(all)
		requireSortedEqual(t, classified, all, "blocking + unreadable classified conditions")
		assertConditionWindow(t, page.Workflow, all)
	})
}

// TestWorkflowReadClassifiesUnknownAuthorityWithoutRewritingIt holds the
// unknown-authority classification on the same published read: an open
// condition whose authority operation is no longer readable reports
// unreadable beside the blocking whole, and the read resolves nothing.
func TestWorkflowReadClassifiesUnknownAuthorityWithoutRewritingIt(t *testing.T) {
	t.Parallel()
	fixture := buildClassifiedReadFixture(t, 3, 0, 0, true)
	var eventsBefore int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	envelope, err := dispatchUnboundedHistory(t, fixture)
	if err != nil {
		t.Fatalf("unknown-authority history read failed outside the typed envelope: %v", err)
	}
	if envelope.Outcome != agentcontract.OutcomeOK {
		t.Fatalf("unknown-authority workflow read refused: %+v", envelope.Error)
	}
	var page unboundedReadPage
	if err := json.Unmarshal(envelope.Result, &page); err != nil {
		t.Fatal(err)
	}
	if page.Workflow == nil {
		t.Fatal("history page omitted the workflow member")
	}
	if page.Workflow.Ready {
		t.Fatal("workflow read reported ready with an unresolvable open condition")
	}
	requireSortedEqual(t, page.Workflow.BlockingConditions, fixture.blocking, "blocking_conditions")
	requireSortedEqual(t, page.Workflow.UnreadableConditions, []string{fixture.unknown}, "unreadable_conditions")
	var eventsAfter int
	if err := fixture.store.DatabaseForTesting().QueryRow(`SELECT last FROM store_sequences WHERE name='effect'`).Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("published read moved event history: before %d after %d", eventsBefore, eventsAfter)
	}
}

// TestWorkflowReadLargeFixedProjectionRefusesTypedBytesNotFieldSchema builds
// the large fixed-projection witness: 280 maximal-width blocking conditions
// whose complete published workflow member exceeds the envelope ceiling at
// the smallest event page. The refusal must be the unchanged typed
// limit_exceeded byte refusal naming result_envelope_bytes — never a
// field-schema failure and never a shrunk or paged result.
func TestWorkflowReadLargeFixedProjectionRefusesTypedBytesNotFieldSchema(t *testing.T) {
	t.Parallel()
	fixture := buildClassifiedReadFixture(t, unboundedWitnessBlocking, unboundedWitnessIDWidth, 0, false)
	projection := fixtureStoreWorkflow(t, fixture)
	if len(projection.BlockingConditions) != unboundedWitnessBlocking {
		t.Fatalf("store producer holds %d blocking conditions, want %d", len(projection.BlockingConditions), unboundedWitnessBlocking)
	}
	envelope, err := dispatchUnboundedHistory(t, fixture)
	if err != nil {
		t.Fatalf("oversized fixed projection must refuse as a typed envelope, not a transport fault: %v", err)
	}
	if envelope.Outcome != agentcontract.OutcomeError || envelope.Error == nil {
		t.Fatalf("outcome %s error %+v, want the typed byte refusal", envelope.Outcome, envelope.Error)
	}
	if envelope.Error.Kind != "limit_exceeded" {
		t.Fatalf("refusal kind %q, want limit_exceeded; any other kind means a field-schema cap fired before the byte ceiling", envelope.Error.Kind)
	}
	if envelope.Error.RecoveryAction.Kind != "reduce_limit" {
		t.Fatalf("recovery action %+v, want reduce_limit", envelope.Error.RecoveryAction)
	}
	if !strings.Contains(envelope.Error.Message, "fixed projection") {
		t.Fatalf("refusal message %q, want the fixed-projection byte account", envelope.Error.Message)
	}
	details := envelope.Error.Details
	if details["reason"] != "result_envelope_bytes" {
		t.Fatalf("refusal reason %v, want result_envelope_bytes", details["reason"])
	}
	if details["maximum_bytes"] != agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("refusal ceiling %v, want the agent envelope constant %d", details["maximum_bytes"], agentcontract.MaxResultEnvelopeBytes)
	}
	serialized, ok := details["serialized_bytes"].(int)
	if !ok || serialized <= agentcontract.MaxResultEnvelopeBytes {
		t.Fatalf("serialized_bytes %v, want an observed size above %d", details["serialized_bytes"], agentcontract.MaxResultEnvelopeBytes)
	}
	if len(envelope.Result) != 0 {
		t.Fatalf("refused envelope still carries %d result bytes", len(envelope.Result))
	}
	after := fixtureStoreWorkflow(t, fixture)
	if len(after.BlockingConditions) != unboundedWitnessBlocking {
		t.Fatalf("refused read changed the durable producer: %d blocking conditions remain", len(after.BlockingConditions))
	}
}
