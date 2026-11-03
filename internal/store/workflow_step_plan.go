package store

import (
	"encoding/json"
	"fmt"
	"slices"
)

// The per-item step plan (CON-1574). Each work item's workflow row carries one
// JSON plan copied from contracts/workflow-step-templates.v1.json when the
// driving agent picks the type. The plan is written only when the type is
// picked, when a step is inserted, and when a step completes; every write
// passes ValidateStepPlanWrite, the one function that compares the new plan
// with the old one and refuses a plan that fails its schema, contains a
// cycle, removes a required step, changes a step's after edges, completes a
// step before every step it comes after, or alters a completed step.

// WorkflowStepPlanType names one workflow type a driving agent may pick after
// intake. The template contract owns the closed set.
type WorkflowStepPlanType string

const (
	WorkflowStepPlanChange   WorkflowStepPlanType = "change"
	WorkflowStepPlanFix      WorkflowStepPlanType = "fix"
	WorkflowStepPlanResearch WorkflowStepPlanType = "research"
	WorkflowStepPlanRemoval  WorkflowStepPlanType = "removal"
)

// WorkflowStepPlanTypes is the closed type set in template-contract order.
var WorkflowStepPlanTypes = []WorkflowStepPlanType{WorkflowStepPlanChange, WorkflowStepPlanFix, WorkflowStepPlanResearch, WorkflowStepPlanRemoval}

// WorkflowIntakeOutcome is one of the five intake outcomes. The four non-
// proceed outcomes end or replace the item without picking a type.
type WorkflowIntakeOutcome string

const (
	WorkflowIntakeProceed        WorkflowIntakeOutcome = "proceed"
	WorkflowIntakeClarify        WorkflowIntakeOutcome = "clarify"
	WorkflowIntakeAlreadyHandled WorkflowIntakeOutcome = "already_handled"
	WorkflowIntakeDecline        WorkflowIntakeOutcome = "decline"
	WorkflowIntakeSplitOrMerge   WorkflowIntakeOutcome = "split_or_merge"
)

// WorkflowIntakeChecks are the fourteen decided intake checks in decided
// order. An intake record names which of these found something.
var WorkflowIntakeChecks = []string{
	"freshness", "overtaken", "already_delivered", "still_reproducible", "observed_or_theoretical",
	"defect_or_feature", "duplicate_or_overlap", "right_owner", "conflicts", "value",
	"cost_against_value", "one_outcome_or_several", "checkable_end_state", "blocked",
}

// WorkflowPlanAttempt is one completion of one step. A repeated step appends
// another attempt; completed attempts are never altered.
type WorkflowPlanAttempt struct {
	Note          string `json:"note"`
	CompletedAt   string `json:"completed_at"`
	Actor         string `json:"actor,omitempty"`
	CompletionSeq int64  `json:"completion_seq,omitempty"`
	// Reviewer, Result, Commit and Link carry the concord-review record a
	// reviewed step completes with; they are required on a reviewed step's
	// attempt and absent otherwise.
	Reviewer string `json:"reviewer,omitempty"`
	Result   string `json:"result,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Link     string `json:"link,omitempty"`
}

// WorkflowPlanStep is one step of a stored plan.
type WorkflowPlanStep struct {
	ID       string                `json:"id"`
	Required bool                  `json:"required"`
	After    []string              `json:"after"`
	Skill    *string               `json:"skill,omitempty"`
	Helper   *string               `json:"helper,omitempty"`
	Reviewed bool                  `json:"reviewed"`
	Attempts []WorkflowPlanAttempt `json:"attempts,omitempty"`
}

// WorkflowStepPlan is the stored plan document.
type WorkflowStepPlan struct {
	SchemaVersion string               `json:"schema_version"`
	Type          WorkflowStepPlanType `json:"type"`
	Steps         []WorkflowPlanStep   `json:"steps"`
}

// workflowStepPlanSchemaVersion is the stored plan's manifest shape, enumerated
// by the plan_schema in the template contract.
const workflowStepPlanSchemaVersion = "1.0"

// workflowTemplateStep is one template step as the contract declares it.
type workflowTemplateStep struct {
	ID       string   `json:"id"`
	Skill    *string  `json:"skill"`
	Helper   *string  `json:"helper"`
	Reviewed bool     `json:"reviewed"`
	Actions  []string `json:"actions"`
}

type workflowTemplateType struct {
	ID    string                 `json:"id"`
	Steps []workflowTemplateStep `json:"steps"`
}

type workflowTemplatesContract struct {
	SchemaVersion string                 `json:"schema_version"`
	Types         []workflowTemplateType `json:"types"`
}

// workflowStepTemplates holds the parsed template contract. It is immutable
// data compiled into the binary; templates change only through the law route.
var workflowStepTemplates = parseWorkflowStepTemplates()

func parseWorkflowStepTemplates() map[WorkflowStepPlanType][]workflowTemplateStep {
	var contract workflowTemplatesContract
	if err := json.Unmarshal([]byte(GeneratedWorkflowStepTemplatesDocument), &contract); err != nil {
		panic(fmt.Sprintf("workflow step templates contract is not valid JSON: %v", err))
	}
	if contract.SchemaVersion != workflowStepPlanSchemaVersion {
		panic("workflow step templates contract schema_version is not " + workflowStepPlanSchemaVersion)
	}
	templates := make(map[WorkflowStepPlanType][]workflowTemplateStep, len(contract.Types))
	for _, declared := range contract.Types {
		planType := WorkflowStepPlanType(declared.ID)
		if !slices.Contains(WorkflowStepPlanTypes, planType) {
			panic("workflow step templates contract declares unknown type " + declared.ID)
		}
		if _, duplicate := templates[planType]; duplicate {
			panic("workflow step templates contract declares type " + declared.ID + " twice")
		}
		if len(declared.Steps) < 1 {
			panic("workflow step templates contract declares no steps for type " + declared.ID)
		}
		for _, step := range declared.Steps {
			if !validWorkflowID(step.ID) || len(step.Actions) < 1 || len(step.Actions) > 16 {
				panic("workflow step templates contract declares an invalid step for type " + declared.ID)
			}
			for _, actionID := range step.Actions {
				if _, err := catalogAction(actionID); err != nil {
					panic(fmt.Sprintf("workflow step templates contract step %s.%s names unknown action %s", declared.ID, step.ID, actionID))
				}
			}
		}
		templates[planType] = declared.Steps
	}
	for _, planType := range WorkflowStepPlanTypes {
		if _, ok := templates[planType]; !ok {
			panic("workflow step templates contract is missing type " + string(planType))
		}
	}
	return templates
}

// WorkflowStepPlanTemplate copies a type's required steps into a fresh plan.
// Each step comes after the step the template lists before it; the first step
// comes after nothing.
func WorkflowStepPlanTemplate(planType WorkflowStepPlanType) (WorkflowStepPlan, error) {
	steps, ok := workflowStepTemplates[planType]
	if !ok {
		return WorkflowStepPlan{}, newFailure(KindInvalidOperation, "workflow_step_plan", "workflow type is not declared by the template contract", false, "pick change, fix, research, or removal")
	}
	plan := WorkflowStepPlan{SchemaVersion: workflowStepPlanSchemaVersion, Type: planType, Steps: make([]WorkflowPlanStep, 0, len(steps))}
	for index, template := range steps {
		step := WorkflowPlanStep{ID: template.ID, Required: true, Skill: template.Skill, Helper: template.Helper, Reviewed: template.Reviewed}
		if index > 0 {
			step.After = []string{steps[index-1].ID}
		} else {
			step.After = []string{}
		}
		plan.Steps = append(plan.Steps, step)
	}
	return plan, nil
}

// WorkflowStepPlanTemplateActions returns the actions the template declares on
// a type's step, so admission and the work pin answer from one authority.
func WorkflowStepPlanTemplateActions(planType WorkflowStepPlanType, stepID string) []string {
	for _, template := range workflowStepTemplates[planType] {
		if template.ID == stepID {
			return template.Actions
		}
	}
	return nil
}

// ValidateStepPlanWrite is the one check every plan write passes. old is nil
// when the type is first picked; afterwards old is the stored plan. It refuses
// a write whose new plan fails the plan schema, contains a cycle, removes a
// required step, changes an existing step's after edges, completes a step
// before every step it comes after, or alters a completed step. A repeated
// step appends another attempt instead of altering the completed one.
func ValidateStepPlanWrite(old *WorkflowStepPlan, updated WorkflowStepPlan) error {
	if err := validateStepPlanShape(updated); err != nil {
		return err
	}
	if old == nil {
		return validateStepPlanInitialCopy(updated)
	}
	return validateStepPlanContinuation(*old, updated)
}

// validateStepPlanShape holds the plan schema in code, the way the engine
// holds the workflow-definition schema: bounds, identifiers, after-edge
// references, and acyclicity.
func validateStepPlanShape(plan WorkflowStepPlan) error {
	refuse := func(detail string) error {
		return newFailure(KindInvalidOperation, "workflow_step_plan", detail, false, "reread the plan and resend the write")
	}
	if plan.SchemaVersion != workflowStepPlanSchemaVersion {
		return refuse("plan schema version is not " + workflowStepPlanSchemaVersion)
	}
	if !slices.Contains(WorkflowStepPlanTypes, plan.Type) {
		return refuse("plan type is not declared by the template contract")
	}
	if len(plan.Steps) < 1 || len(plan.Steps) > 64 {
		return refuse("plan step count is out of bounds")
	}
	ids := make(map[string]bool, len(plan.Steps))
	for _, step := range plan.Steps {
		if err := validateStepPlanStepShape(plan.Steps, step, ids, refuse); err != nil {
			return err
		}
	}
	if stepPlanHasCycle(plan.Steps) {
		return refuse("plan contains a cycle")
	}
	return nil
}

func validateStepPlanStepShape(steps []WorkflowPlanStep, step WorkflowPlanStep, ids map[string]bool, refuse func(string) error) error {
	if len(step.ID) < 2 || len(step.ID) > 64 || !validWorkflowID(step.ID) || ids[step.ID] {
		return refuse("plan step id is invalid or duplicated")
	}
	ids[step.ID] = true
	if len(step.After) > 64 {
		return refuse("plan step declares too many after edges")
	}
	edges := make(map[string]bool, len(step.After))
	for _, after := range step.After {
		if edges[after] {
			return refuse("plan step after edge is duplicated")
		}
		edges[after] = true
		if after == step.ID || !ids[after] && !stepIDExists(steps, after) {
			return refuse("plan step after edge names a step the plan does not hold")
		}
	}
	return validateStepPlanAttemptsShape(step, refuse)
}

func validateStepPlanAttemptsShape(step WorkflowPlanStep, refuse func(string) error) error {
	if len(step.Attempts) > 64 {
		return refuse("plan step holds too many attempts")
	}
	for _, attempt := range step.Attempts {
		if len(attempt.Note) < 2 || len(attempt.Note) > 4096 || attempt.CompletedAt == "" || attempt.CompletionSeq < 0 {
			return refuse("plan step attempt is invalid")
		}
		if step.Reviewed && (attempt.Reviewer == "" || (attempt.Result != "pass" && attempt.Result != "fixed") || attempt.Commit == "" || attempt.Link == "") {
			return refuse("a reviewed step's attempt requires its reviewer, result, commit and link")
		}
		if !step.Reviewed && (attempt.Reviewer != "" || attempt.Result != "" || attempt.Commit != "" || attempt.Link != "") {
			return refuse("an unreviewed step's attempt carries review fields")
		}
	}
	return nil
}

func stepIDExists(steps []WorkflowPlanStep, id string) bool {
	return slices.ContainsFunc(steps, func(step WorkflowPlanStep) bool { return step.ID == id })
}

func stepPlanHasCycle(steps []WorkflowPlanStep) bool {
	state := make(map[string]int8, len(steps))
	var visit func(string) bool
	visit = func(id string) bool {
		state[id] = 1
		for _, step := range steps {
			if step.ID != id {
				continue
			}
			for _, after := range step.After {
				switch state[after] {
				case 1:
					return true
				case 0:
					if visit(after) {
						return true
					}
				}
			}
		}
		state[id] = 2
		return false
	}
	for _, step := range steps {
		if state[step.ID] == 0 && visit(step.ID) {
			return true
		}
	}
	return false
}

// validateStepPlanInitialCopy holds the type-pick write: the plan is exactly
// the picked type's template with no completions.
func validateStepPlanInitialCopy(plan WorkflowStepPlan) error {
	template, err := WorkflowStepPlanTemplate(plan.Type)
	if err != nil {
		return err
	}
	if len(plan.Steps) != len(template.Steps) {
		return newFailure(KindInvalidOperation, "workflow_step_plan", "type-pick plan does not copy the type's template", false, "copy the type's required steps unchanged")
	}
	for index, step := range plan.Steps {
		if !workflowPlanStepEqual(step, template.Steps[index]) {
			return newFailure(KindInvalidOperation, "workflow_step_plan", "type-pick plan does not copy the type's template", false, "copy the type's required steps unchanged")
		}
	}
	return nil
}

// workflowPlanStepEqual compares two plan steps field by field, including
// their after edges; the plan step type holds slices, so == cannot compare it.
func workflowPlanStepEqual(left, right WorkflowPlanStep) bool {
	if left.ID != right.ID || left.Required != right.Required || left.Reviewed != right.Reviewed ||
		!equalOptionalString(left.Skill, right.Skill) || !equalOptionalString(left.Helper, right.Helper) ||
		len(left.After) != len(right.After) || len(left.Attempts) != len(right.Attempts) {
		return false
	}
	for index, after := range left.After {
		if right.After[index] != after {
			return false
		}
	}
	for index, attempt := range left.Attempts {
		if right.Attempts[index] != attempt {
			return false
		}
	}
	return true
}

// validateStepPlanContinuation holds the step-insert and step-completion
// writes, and the type switch that adds the new type's missing required
// steps while keeping completed steps.
func validateStepPlanContinuation(old, updated WorkflowStepPlan) error {
	refuse := func(detail string) error {
		return newFailure(KindInvalidOperation, "workflow_step_plan", detail, false, "reread the plan and resend the write")
	}
	switching := updated.Type != old.Type
	if switching {
		if err := validateStepPlanTypeSwitch(old, updated, refuse); err != nil {
			return err
		}
	}
	oldSteps := make(map[string]WorkflowPlanStep, len(old.Steps))
	for _, step := range old.Steps {
		oldSteps[step.ID] = step
	}
	appended := 0
	for _, step := range updated.Steps {
		previous, existed := oldSteps[step.ID]
		if !existed {
			if err := validateStepPlanAddedStep(updated.Type, step, switching, refuse); err != nil {
				return err
			}
			continue
		}
		count, err := validateStepPlanExistingStep(old.Steps, previous, step, refuse)
		if err != nil {
			return err
		}
		appended += count
	}
	if appended > 1 {
		return refuse("a plan write appends at most one attempt")
	}
	for _, step := range old.Steps {
		if step.Required && !stepIDExists(updated.Steps, step.ID) {
			return refuse("a required step is never removed")
		}
		if len(step.Attempts) > 0 && !stepIDExists(updated.Steps, step.ID) {
			return refuse("a completed step is never removed")
		}
	}
	return nil
}

func validateStepPlanTypeSwitch(old, updated WorkflowStepPlan, refuse func(string) error) error {
	template, err := WorkflowStepPlanTemplate(updated.Type)
	if err != nil {
		return err
	}
	for _, step := range old.Steps {
		current, found := stepByID(updated.Steps, step.ID)
		if !found {
			return refuse("a type switch never removes a step")
		}
		if !workflowPlanStepEqual(step, current) {
			return refuse("a type switch never changes an existing step")
		}
	}
	for _, templateStep := range template.Steps {
		if !stepIDExists(old.Steps, templateStep.ID) && !stepIDExists(updated.Steps, templateStep.ID) {
			return refuse("a type switch adds the new type's missing required steps")
		}
	}
	return nil
}

func validateStepPlanAddedStep(planType WorkflowStepPlanType, step WorkflowPlanStep, switching bool, refuse func(string) error) error {
	if len(step.Attempts) != 0 {
		return refuse("a new step must be incomplete")
	}
	if step.Required {
		if !switching {
			return refuse("a required step is added only on a type switch")
		}
		template, err := WorkflowStepPlanTemplate(planType)
		if err != nil {
			return err
		}
		templateStep, found := stepByID(template.Steps, step.ID)
		if !found || step.Reviewed != templateStep.Reviewed || !equalOptionalString(step.Skill, templateStep.Skill) || !equalOptionalString(step.Helper, templateStep.Helper) {
			return refuse("an added required step must come from the picked type's template")
		}
	} else if switching {
		return refuse("a type switch adds only missing required template steps")
	}
	return nil
}

func validateStepPlanExistingStep(oldSteps []WorkflowPlanStep, previous, step WorkflowPlanStep, refuse func(string) error) (int, error) {
	if len(previous.Attempts) > len(step.Attempts) {
		return 0, refuse("a completed step's attempts are never removed or reordered")
	}
	for index, attempt := range previous.Attempts {
		if step.Attempts[index] != attempt {
			return 0, refuse("a completed step's attempts are never altered")
		}
	}
	if !slices.Equal(previous.After, step.After) {
		return 0, refuse("an existing step's after edges never change")
	}
	if previous.Required != step.Required || previous.Reviewed != step.Reviewed || !equalOptionalString(previous.Skill, step.Skill) || !equalOptionalString(previous.Helper, step.Helper) {
		return 0, refuse("an existing step's metadata never changes")
	}
	if len(step.Attempts) > len(previous.Attempts) {
		for _, after := range step.After {
			afterStep, found := stepByID(oldSteps, after)
			if !found || len(afterStep.Attempts) == 0 {
				return 0, refuse("a step completes only after every step it comes after")
			}
		}
		return len(step.Attempts) - len(previous.Attempts), nil
	}
	return 0, nil
}

func equalOptionalString(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func stepByID(steps []WorkflowPlanStep, id string) (WorkflowPlanStep, bool) {
	for _, step := range steps {
		if step.ID == id {
			return step, true
		}
	}
	return WorkflowPlanStep{}, false
}

// workflowPlanStepOpen reports whether a step still needs an attempt. The
// picked type's final template step is the final clean review: it stays open
// until its latest attempt needs no fixes, so the review repeats instead of
// letting a fixed result close the plan.
func workflowPlanStepOpen(plan WorkflowStepPlan, step WorkflowPlanStep) bool {
	if len(step.Attempts) == 0 {
		return true
	}
	template := workflowStepTemplates[plan.Type]
	if len(template) == 0 || !step.Reviewed || step.ID != template[len(template)-1].ID {
		return false
	}
	return step.Attempts[len(step.Attempts)-1].Result != "pass"
}

// WorkflowStepPlanFrontier derives the plan's current frontier: the open
// steps whose after-steps are all complete, in plan order. Nothing stores the
// frontier; every read derives it.
func WorkflowStepPlanFrontier(plan WorkflowStepPlan) []WorkflowPlanStep {
	frontier := make([]WorkflowPlanStep, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		if !workflowPlanStepOpen(plan, step) {
			continue
		}
		ready := true
		for _, after := range step.After {
			afterStep, found := stepByID(plan.Steps, after)
			if !found || len(afterStep.Attempts) == 0 {
				ready = false
				break
			}
		}
		if ready {
			frontier = append(frontier, step)
		}
	}
	return frontier
}

// WorkflowStepPlanComplete reports whether every step in the plan is
// completed and the final clean review needed no fixes. Completion of the
// work item requires this.
func WorkflowStepPlanComplete(plan WorkflowStepPlan) bool {
	for _, step := range plan.Steps {
		if workflowPlanStepOpen(plan, step) {
			return false
		}
	}
	return true
}
