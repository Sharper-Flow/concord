package store

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The pinned definition owns an instance's step vocabulary. These tests pin
// that invariant from both directions: every persisted current_step is a step
// its definition declares, and no call site outside the owning file can write
// one.

func stepFixtureActor() WorkflowActor {
	return WorkflowActor{PrincipalRef: "principal-1", ClientRef: "client-1", AgentRef: "agent-1", SessionRef: "session-1", ActorClass: ActorAgent}
}

// seedStepWork creates one work item, in a Project, that the workflow
// initializer can pin a definition to.
func seedStepWork(t *testing.T, s *Store, workID string) {
	t.Helper()
	ctx := context.Background()
	if err := ApplyOperation(ctx, s, Operation{Events: []Event{locatorProductEvent("product-s"), locatorProjectEvent("project-s"), locatorMembershipEvent("product-s", "project-s")}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProduct, "product-s"): 0, VersionRef(SubjectProject, "project-s"): 0}}); err != nil {
		t.Fatal(err)
	}
	err := ApplyOperation(ctx, s, Operation{Events: []Event{
		{EventID: workID + "-create", Kind: "work.created", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(1, 0).UTC(), PayloadVersion: 2, Payload: jsonRaw(`{"work_kind":"task","title":"Step work","priority":1}`)},
		{EventID: workID + "-membership", Kind: "work.memberships_replaced", SubjectType: SubjectWorkItem, SubjectID: workID, Actor: "operator", OccurredAt: time.Unix(2, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(`{"memberships":[{"project_id":"project-s","role":"primary"}],"expected_version":1,"resulting_version":2}`)},
	}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): 0}})
	if err != nil {
		t.Fatal(err)
	}
}

func initializeStepWorkflow(t *testing.T, s *Store, workID string, definition WorkflowDefinition) {
	t.Helper()
	registered, err := BuiltinWorkflowRegistry().Register(definition)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Transact(ctx, func(tx *Transaction) error {
		return InitializeWorkflowTx(ctx, tx, WorkflowInitializationRequest{WorkID: workID, Definition: registered, Actor: stepFixtureActor(), Now: time.Unix(5, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
}

func readInstanceStep(t *testing.T, s *Store, workID string) string {
	t.Helper()
	var step string
	if err := s.db.QueryRow(`SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&step); err != nil {
		t.Fatal(err)
	}
	return step
}

// A newly initialized instance occupies its definition's declared start step.
// It never holds a placeholder that no definition declares, because a step
// outside the definition's vocabulary refuses every action.
func TestWorkflowInstanceStartsAtItsDefinitionStartStep(t *testing.T) {
	want := map[string]string{
		"workflow.implementation":     "proposal",
		"workflow.break_fix":          "reproduce",
		"workflow.research":           "frame",
		"workflow.architecture_spike": "frame",
		"workflow.ops_runbook":        "plan",
		"workflow.static_analysis":    "scope",
		"workflow.generic_one_off":    "define",
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		s := openTemp(t)
		workID := "work-" + strings.ReplaceAll(definition.Ref, ".", "-")
		seedStepWork(t, s, workID)
		initializeStepWorkflow(t, s, workID, definition)
		got := readInstanceStep(t, s, workID)
		if got != want[definition.Ref] {
			t.Errorf("%s current_step = %q, want %q", definition.Ref, got, want[definition.Ref])
		}
		if workflowStep(definition, got) == nil {
			t.Errorf("%s persisted step %q is not declared by its own definition", definition.Ref, got)
		}
		s.Close()
	}
}

// The step a superseded contract returns to is derived from the definition,
// never named by a literal. Every family declares approve_contract on exactly
// one step, so the derivation is total.
func TestWorkflowContractStepIsDerivedFromTheDefinition(t *testing.T) {
	want := map[string]string{
		"workflow.implementation":     "planning",
		"workflow.break_fix":          "planning",
		"workflow.research":           "frame",
		"workflow.architecture_spike": "frame",
		"workflow.ops_runbook":        "plan",
		"workflow.static_analysis":    "scope",
		"workflow.generic_one_off":    "define",
	}
	for _, definition := range BuiltinWorkflowDefinitions() {
		got, err := workflowDefinitionContractStep(definition)
		if err != nil {
			t.Errorf("%s contract step: %v", definition.Ref, err)
			continue
		}
		if got != want[definition.Ref] {
			t.Errorf("%s contract step = %q, want %q", definition.Ref, got, want[definition.Ref])
		}
	}
}

// Re-pinning a definition before execution starts is re-initialization: the
// instance takes the new definition's start step, so the pair stays coherent.
func TestRepinWritesTheNewDefinitionStartStep(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	seedStepWork(t, s, "work-repin")
	var generic, implementation WorkflowDefinition
	for _, definition := range BuiltinWorkflowDefinitions() {
		switch definition.Ref {
		case "workflow.generic_one_off":
			generic = definition
		case "workflow.implementation":
			implementation = definition
		}
	}
	initializeStepWorkflow(t, s, "work-repin", generic)
	if got := readInstanceStep(t, s, "work-repin"); got != "define" {
		t.Fatalf("initial step = %q, want define", got)
	}
	registered, err := BuiltinWorkflowRegistry().Register(implementation)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		return RepinWorkflowTx(ctx, transaction, WorkflowRepinRequest{WorkID: "work-repin", EventID: "work-repin-repin", Definition: registered, Actor: stepFixtureActor(), Now: time.Unix(9, 0).UTC()})
	}); err != nil {
		t.Fatal(err)
	}
	if got := readInstanceStep(t, s, "work-repin"); got != "proposal" {
		t.Fatalf("re-pinned step = %q, want proposal", got)
	}
}

// startStepFixtureAction records one action start on the instance's current
// step, which is the boundary after which the pinned definition is fixed.
func startStepFixtureAction(t *testing.T, s *Store, workID, actionID string) {
	t.Helper()
	ctx := context.Background()
	actorRef, err := WorkflowActorRef(stepFixtureActor())
	if err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.db.QueryRow(`SELECT version FROM work_items WHERE id=?`, workID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	payload := `{"work_id":"` + workID + `","expected_version":` + strconv.FormatInt(version, 10) +
		`,"resulting_version":` + strconv.FormatInt(version+1, 10) +
		`,"step_id":"` + readInstanceStep(t, s, workID) + `","action_id":"` + actionID +
		`","attempt_epoch":1,"accepted_inputs_digest":"sha256:` + strings.Repeat("a", 64) +
		`","idempotency_identity":"` + workID + `:start","actor_ref":"` + actorRef + `"}`
	if err := s.Transact(ctx, func(transaction *Transaction) error {
		tx, err := transactionSQL(transaction, "workflow_start_test")
		if err != nil {
			return err
		}
		if err := enterFold(ctx, tx); err != nil {
			return err
		}
		defer func() { _ = leaveFold(ctx, tx) }()
		_, err = applyWorkflowOperationTx(ctx, tx, Operation{Events: []Event{
			{EventID: workID + "-started", Kind: WorkflowActionStarted, SubjectType: SubjectWorkItem, SubjectID: workID, Actor: actorRef, OccurredAt: time.Unix(10, 0).UTC(), PayloadVersion: 1, Payload: jsonRaw(payload)},
		}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectWorkItem, workID): version}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Re-pinning is re-initialization, so it is only available before the work
// starts. Once an action begins, the pinned definition is the law the started
// action was authorized against, and changing it would rewrite that authority
// underneath a live attempt. This gate is the sole guard on the transition.
func TestRepinIsRefusedAfterAnActionStarts(t *testing.T) {
	s := openTemp(t)
	defer s.Close()
	seedStepWork(t, s, "work-started")
	var research, implementation WorkflowDefinition
	for _, definition := range BuiltinWorkflowDefinitions() {
		switch definition.Ref {
		case "workflow.research":
			research = definition
		case "workflow.implementation":
			implementation = definition
		}
	}
	initializeStepWorkflow(t, s, "work-started", implementation)
	startStepFixtureAction(t, s, "work-started", "record_proposal")
	registered, err := BuiltinWorkflowRegistry().Register(research)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err = s.Transact(ctx, func(transaction *Transaction) error {
		return RepinWorkflowTx(ctx, transaction, WorkflowRepinRequest{WorkID: "work-started", EventID: "work-started-repin", Definition: registered, Actor: stepFixtureActor(), Now: time.Unix(11, 0).UTC()})
	})
	if err == nil {
		t.Fatal("re-pin after an action start was accepted, want refusal")
	}
	if !strings.Contains(err.Error(), "definition cannot change after execution starts") {
		t.Fatalf("re-pin refusal = %v, want the execution-started refusal", err)
	}
	if got := readInstanceStep(t, s, "work-started"); got != "proposal" {
		t.Fatalf("refused re-pin moved the step to %q, want proposal", got)
	}
}

// current_step is written from exactly one file. Reading it anywhere is
// harmless; a second writer is how a step that no definition declares reaches
// storage, and an instance holding such a step accepts no action at all. The
// boundary is structural rather than a review convention.
func TestCurrentStepIsWrittenOnlyByItsOwningFile(t *testing.T) {
	const owner = "workflow_step.go"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == owner || name == "schema.go" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, "SET current_step") || strings.Contains(line, "INSERT INTO workflow_instances") {
				offenders = append(offenders, name+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(offenders) != 0 {
		t.Fatalf("current_step is written outside %s:\n%s", owner, strings.Join(offenders, "\n"))
	}
}
