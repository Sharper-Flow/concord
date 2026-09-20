package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// CD-0156: Concord mandates the backlog check. The alignment step is spliced
// into workflow.implementation between proposal and discovery, and into
// workflow.break_fix between reproduce and diagnose. The step declares one
// advance action, record_alignment, so the single forward edge cannot be
// crossed without the search, and the search becomes structural rather than
// discretionary.

// alignmentAdvanceExits returns every action on the alignment step whose
// execution mode advances the step.
func alignmentAdvanceExits(t *testing.T, definition WorkflowDefinition, stepID string) []string {
	t.Helper()
	step := workflowStep(definition, stepID)
	if step == nil {
		t.Fatalf("%s has no %s step", definition.Ref, stepID)
	}
	var exits []string
	for _, actionID := range step.Actions {
		mode, ok := workflowActionExecutionMode(definition, actionID)
		if !ok {
			t.Fatalf("%s action %s declares no execution mode", definition.Ref, actionID)
		}
		if mode == ActionAdvance {
			exits = append(exits, actionID)
		}
	}
	return exits
}

func TestAlignmentStepHasOnlyRecordAlignmentAsItsAdvanceExit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ref         string
		afterStep   string
		nextStep    string
		wantVersion int64
	}{
		{"workflow.implementation", "proposal", "discovery", 13},
		{"workflow.break_fix", "reproduce", "diagnose", 11},
	}
	for _, testCase := range cases {
		registered, err := BuiltinWorkflowDefinitionForRef(testCase.ref)
		if err != nil {
			t.Fatalf("%s does not resolve: %v", testCase.ref, err)
		}
		definition := registered.Definition
		if definition.Version != testCase.wantVersion {
			t.Fatalf("%s resolved version %d, want %d", testCase.ref, definition.Version, testCase.wantVersion)
		}
		if got := workflowNextStep(definition, testCase.afterStep); got != "alignment" {
			t.Fatalf("%s routes %s to %q, want alignment", testCase.ref, testCase.afterStep, got)
		}
		if got := workflowNextStep(definition, "alignment"); got != testCase.nextStep {
			t.Fatalf("%s routes alignment to %q, want %s", testCase.ref, got, testCase.nextStep)
		}
		for _, edge := range definition.StepGraph.Edges {
			if edge.From == "alignment" && edge.Kind != WorkflowEdgeForward {
				t.Fatalf("%s alignment carries a %s edge to %s; the step owns exactly one forward exit", testCase.ref, edge.Kind, edge.To)
			}
		}
		exits := alignmentAdvanceExits(t, definition, "alignment")
		if len(exits) != 1 || exits[0] != "record_alignment" {
			t.Fatalf("%s alignment advance exits are %v, want exactly [record_alignment]", testCase.ref, exits)
		}
		if !definitionStepAllows(definition, "alignment", "record_alignment") {
			t.Fatalf("%s alignment does not admit record_alignment", testCase.ref)
		}
		for _, withheld := range []string{"dispatch_worker", "accept_worker_result", "record_worker_failure"} {
			if definitionStepAllows(definition, "alignment", withheld) {
				t.Fatalf("%s alignment admits %s; a dispatched attempt could accept its way past the search", testCase.ref, withheld)
			}
		}
	}
}

// TestRecordAlignmentRefusesInconsistentOutcomeAndRelatedIds proves the guard
// refuses a payload whose outcome contradicts its related_ids list at the
// action boundary, and that a consistent payload records the search and
// advances the workflow.
func TestRecordAlignmentRefusesInconsistentOutcomeAndRelatedIds(t *testing.T) {
	t.Parallel()
	s := openTemp(t)
	const workID = "alignment-guard-work"
	seedWork(t, s, workID)
	actor := WorkflowActor{PrincipalRef: "principal:alignment", ClientRef: "client:alignment", AgentRef: "agent:alignment", SessionRef: "session:alignment", ActorClass: ActorAgent}
	if _, err := BuiltinWorkflowDefinitionForRef("workflow.implementation"); err != nil {
		t.Fatal(err)
	}
	registered, err := BuiltinWorkflowDefinitionForRef("workflow.implementation")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := initializeWorkflowRawTx(context.Background(), tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: actor, Now: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	version := int64(4)
	alignmentWorkflowAction := func(t *testing.T, version int64, operationID string, payload json.RawMessage) error {
		t.Helper()
		tx, err := s.DatabaseForTesting().BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := enterFold(context.Background(), tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		_, err = applyWorkflowActionRawTx(context.Background(), tx, BuiltinWorkflowRegistry(), WorkflowActionExecutionRequest{
			WorkID: workID, ExpectedVersion: version, ActionID: "record_alignment", Payload: payload, Actor: actor,
			AcceptedInputsDigest: "sha256:alignment", IdempotencyIdentity: operationID, OperationID: operationID,
			PrincipalRef: actor.PrincipalRef, Tool: "concord_work_transition", IdempotencyKey: operationID,
			RequestID: "request:" + operationID, ContractDigest: testManifestDigest, Now: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC),
		})
		_ = leaveFold(context.Background(), tx)
		if err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit()
	}
	searched := "Searched the Product backlog for open work duplicating the approved objective."
	emptyPayload := json.RawMessage(`{}`)
	proposalPayload := json.RawMessage(`{"problem":"The bounded problem statement.","affected":["The affected system."],"stakes":"The bounded stakes statement.","user_outcomes":["The expected user outcome."]}`)
	version = issue31WorkflowActionWithPayload(t, s, workID, version, "record_proposal", "alignment-proposal", actor, proposalPayload)
	var step string
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "alignment" {
		t.Fatalf("after record_proposal the workflow sits at %q, want alignment", step)
	}
	for _, testCase := range []struct {
		name    string
		payload json.RawMessage
	}{
		{"related_found without ids", json.RawMessage(`{"searched":` + jsonString(t, searched) + `,"outcome":"related_found"}`)},
		{"none_found with ids", json.RawMessage(`{"searched":` + jsonString(t, searched) + `,"outcome":"none_found","related_ids":["work-duplicate"]}`)},
		{"missing outcome", json.RawMessage(`{"searched":` + jsonString(t, searched) + `}`)},
		{"undeclared outcome", json.RawMessage(`{"searched":` + jsonString(t, searched) + `,"outcome":"duplicates_found"}`)},
		{"empty payload", emptyPayload},
	} {
		if err := alignmentWorkflowAction(t, version, "alignment-refuse-"+jsonString(t, testCase.name), testCase.payload); err == nil {
			t.Fatalf("%s: record_alignment accepted an inconsistent payload %s", testCase.name, testCase.payload)
		}
		var rows int
		if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_backlog_alignment`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows != 0 {
			t.Fatalf("%s: refused payload left %d projection rows", testCase.name, rows)
		}
		var current string
		if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&current); err != nil {
			t.Fatal(err)
		}
		if current != "alignment" {
			t.Fatalf("%s: refused payload moved the workflow to %q", testCase.name, current)
		}
	}
	seedWork(t, s, "work-duplicate-a")
	seedWork(t, s, "work-duplicate-b")
	consistent := json.RawMessage(`{"searched":` + jsonString(t, searched) + `,"outcome":"related_found","related_ids":["work-duplicate-a","work-duplicate-b"]}`)
	if err := alignmentWorkflowAction(t, version, "alignment-accept", consistent); err != nil {
		t.Fatalf("record_alignment refused a consistent payload: %v", err)
	}
	if err := s.DatabaseForTesting().QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	if step != "discovery" {
		t.Fatalf("after record_alignment the workflow sits at %q, want discovery", step)
	}
	var searchedRecorded, outcome string
	var related []string
	rows, err := s.DatabaseForTesting().Query(`SELECT related_work_id,searched,outcome FROM workflow_backlog_alignment WHERE work_id=? ORDER BY related_work_id`, workID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var relatedID *string
		if err := rows.Scan(&relatedID, &searchedRecorded, &outcome); err != nil {
			t.Fatal(err)
		}
		if relatedID != nil {
			related = append(related, *relatedID)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if outcome != "related_found" || searchedRecorded != searched {
		t.Fatalf("projection recorded searched=%q outcome=%q", searchedRecorded, outcome)
	}
	if len(related) != 2 || related[0] != "work-duplicate-a" || related[1] != "work-duplicate-b" {
		t.Fatalf("projection related ids are %v", related)
	}
	var events int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM domain_events WHERE subject_id=? AND kind=?`, workID, WorkflowBacklogAlignmentRecorded).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("record_alignment appended %d typed events, want 1", events)
	}
	if err := RebuildFromLog(context.Background(), s); err != nil {
		t.Fatalf("rebuild workflow event log: %v", err)
	}
	var rebuilt int
	if err := s.DatabaseForTesting().QueryRow(`SELECT count(*) FROM workflow_backlog_alignment WHERE work_id=?`, workID).Scan(&rebuilt); err != nil {
		t.Fatal(err)
	}
	if rebuilt != 3 {
		t.Fatalf("rebuilt projection holds %d rows, want 3 (declaration plus two related ids)", rebuilt)
	}
}

// TestPriorPinnedDefinitionVersionsReplayUnchanged proves every pinned prior
// definition still verifies against its recorded digest and that the frozen
// pre-CD-0156 versions carry no alignment step, so every pinned in-flight
// item replays its original graph unchanged.
func TestPriorPinnedDefinitionVersionsReplayUnchanged(t *testing.T) {
	t.Parallel()
	current := map[string]int64{}
	for _, definition := range BuiltinWorkflowDefinitions() {
		current[definition.Ref] = definition.Version
	}
	for pin, digest := range workflowDefinitionVersionPins {
		ref, version := pin[0], pin[1]
		entry, ok := builtinWorkflowRegistry.Lookup(ref, pinVersion(t, version))
		if !ok {
			t.Fatalf("%s version %s is not registered", ref, version)
		}
		if err := builtinWorkflowRegistry.Verify(ref, pinVersion(t, version), digest); err != nil {
			t.Fatalf("%s version %s pin does not verify: %v", ref, version, err)
		}
		if pinVersion(t, version) >= current[ref] {
			continue
		}
		definition := entry.Definition
		if stepDeclaresAction(definition, "alignment", "record_alignment") {
			t.Fatalf("%s version %s declares the alignment step; a pinned in-flight item would replay a changed graph", ref, version)
		}
		for _, step := range definition.StepGraph.Steps {
			if step.ID == "alignment" {
				t.Fatalf("%s version %s declares an alignment step with no record_alignment action", ref, version)
			}
		}
	}
	predecessors := map[string]WorkflowDefinition{
		"workflow.implementation": implementationPreAlignmentV12(),
		"workflow.break_fix":      breakFixPreAlignmentV10(),
	}
	for ref, definition := range predecessors {
		pin := [2]string{ref, versionString(definition.Version)}
		digest, held := workflowDefinitionVersionPins[pin]
		if !held {
			t.Fatalf("%s version %d is a CD-0156 predecessor without a digest pin", ref, definition.Version)
		}
		if err := builtinWorkflowRegistry.Verify(ref, definition.Version, digest); err != nil {
			t.Fatalf("%s version %d does not verify: %v", ref, definition.Version, err)
		}
	}
}

// TestWorkRemovalClearsBacklogAlignmentAtBothEnds proves removal succeeds for
// the item that recorded the search and for an item the search named. Both
// columns of workflow_backlog_alignment carry a RESTRICT foreign key, so a
// projection delete that covered only work_id would leave the related-item row
// behind and the final work-item delete would fail.
func TestWorkRemovalClearsBacklogAlignmentAtBothEnds(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct{ name, removed, survivor string }{
		{"searching item", "align-searcher", "align-related"},
		{"related item", "align-related", "align-searcher"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			s := openTemp(t)
			seedWork(t, s, "align-searcher")
			seedWork(t, s, "align-related")
			seedBacklogAlignmentRows(t, s, "align-searcher", "align-related")
			request := removalTestRequest()
			request.WorkID = testCase.removed
			request.OperationID = "remove-align-" + testCase.removed
			request.IdempotencyKey = "remove-align-key-" + testCase.removed
			if _, err := s.ShelveWork(ctx, request); err != nil {
				t.Fatalf("removing %s left a backlog alignment row behind: %v", testCase.removed, err)
			}
			var rows int
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM workflow_backlog_alignment WHERE work_id=? OR related_work_id=?`, testCase.removed, testCase.removed).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 0 {
				t.Fatalf("removing %s left %d alignment rows naming it", testCase.removed, rows)
			}
			if err := RebuildFromLog(ctx, s); err != nil {
				t.Fatalf("replay after removing %s: %v", testCase.removed, err)
			}
			var survivors int
			if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT count(*) FROM work_items WHERE id=?`, testCase.survivor).Scan(&survivors); err != nil {
				t.Fatal(err)
			}
			if survivors != 1 {
				t.Fatalf("replay dropped the surviving item %s", testCase.survivor)
			}
		})
	}
}

// seedBacklogAlignmentRows writes the declaration row and one related-item row
// a related_found search produces, so a removal test holds a row at each end of
// the foreign-key pair.
func seedBacklogAlignmentRows(t *testing.T, s *Store, workID, relatedID string) {
	t.Helper()
	ctx := context.Background()
	const actorRef = "actor:0000000000000000000000000000000000000000000000000000000000000001"
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_actors(actor_ref,principal_ref,client_ref,agent_ref,session_ref,actor_class,first_seen_at) VALUES(?,?,?,?,?,'agent','2026-09-19T00:00:00Z')`, actorRef, "principal:alignment-removal", "client:alignment-removal", "agent:alignment-removal", "session:alignment-removal"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	searched := "Searched open work for items covering the same surface."
	for _, related := range []any{nil, relatedID} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO workflow_backlog_alignment(work_id,related_work_id,searched,outcome,recorded_at,recorded_by) VALUES(?,?,?,'related_found','2026-09-19T00:00:00Z',?)`, workID, related, searched, actorRef); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := leaveFold(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
