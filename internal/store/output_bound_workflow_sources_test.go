package store

// Workflow-family rows for the structural output bound ledger: the
// workflow_ and continuity_ definitions plus work_pin,
// work_pin_evidence and work_liveness. Every finite row cites a real full-string producer
// bound traced to its writer: a store guard constant, a workflowString or
// workflowList call argument resolved through its fold, an inline
// comparison guard, an anchored pattern constant, a declared slice literal,
// the deterministic digest identity a writer mints, the admitted input
// shape a fold re-admits at the same numbers, or a family-local derived
// evaluator over one exact live construction. SQLite length(TEXT) CHECKs
// are NUL-prefix constraints and bound no complete output, so no row cites
// one as a finite source; projections whose writers carry no full-string
// guard are registered honestly unbounded with a typed refusal. The derived
// evaluators below each inspect one AST shape, refuse every unsupported
// writer, and none derives a filesystem length. No row freezes a numeric
// copy of its own output bound, and no row cites an output-only definition
// as its own source.

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
)

func init() {
	// The typed whole-result refusals every byte-ceiling-only row names:
	// the shared read envelope budget owner and the shared mutation
	// envelope check, both in internal/agent.
	const readSizeRefusalPath = "internal/agent/runtime.go:readSizeRefusal"
	const mutationResultPath = "internal/agent/mutations.go:mutationResult"

	// The RFC3339Nano timestamps written into recorded_at, approved_at,
	// and last_progress_at columns. The shared bounded-layout
	// helper verifies each cited writer carries exactly one Format layout
	// and derives the maximum render width from its tokens and the stdlib
	// time representation, without assuming a four-digit year or bounded zone.
	contractApprovedAt := pkTimeLayoutWidthBound("internal/store/workflow.go", "persistWorkflowContractApprovalTx", "time.RFC3339Nano")
	boundaryRecordedAt := pkTimeLayoutWidthBound("internal/store/workflow.go", "foldWorkflowContextBoundaryCrossed", "time.RFC3339Nano")
	handoffRecordedAt := pkTimeLayoutWidthBound("internal/store/project_handoffs.go", "recordProjectHandoffCore", "time.RFC3339Nano")
	// advanceWorkEffectSeqTx retains the lexical maximum in
	// work_items.last_progress_at. deriveWorkLivenessQ reads that column;
	// workLivenessFromCounts copies it to LastProgress without reformatting.
	lastProgressAt := pkTimeLayoutWidthBound("internal/store/work_effect_seq.go", "advanceWorkEffectSeqTx", "time.RFC3339Nano")

	// Step identities are admitted through an anchored
	// regexp.MustCompile variable at definition registration; the pattern
	// helper compiles the live pattern and derives its largest accepted
	// character count.
	stepIDPattern := goPatternBound("internal/store/workflow_action_policy.go", "workflowIDPattern")

	// Guard constants cited by more than one row.
	handoffIDMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffIDMax", boundBytes)
	handoffJobMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffJobMax", boundBytes)
	handoffActionMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffActionMax", boundBytes)
	handoffListMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffListMax", boundItems)
	handoffEntryMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffEntryMax", boundBytes)
	bindingMaxLawAdditions := goConstantBound("internal/store/workflow_architecture_binding.go", "workflowArchitectureBindingMaxLawAdditions", boundItems)
	bindingMaxObligations := goConstantBound("internal/store/workflow_architecture_binding.go", "workflowArchitectureBindingMaxObligations", boundItems)

	// Inline comparison guards: the helper selects the unambiguous upper
	// guard over the exact expression, evaluating its operand from live
	// source. workflowEvidenceRef uses the admission-predicate form
	// (len(value) >= 1 && len(value) <= 2048); every other site uses the
	// refusal form (expression > N beside a lower guard the helper never
	// selects).
	continuityPageLimit := goInlineBound("internal/store/workflow_continuity.go", "ReadWorkflowContinuity", "req.Limit", boundItems)
	productIdentityBound := goInlineBound("internal/store/workflow_continuity.go", "continuityReadIdentityTx", "len(out.ProductIdentity)", boundItems)
	stepActionCount := workPinIntentItemsBound()
	drivingSessionCount := goInlineBound("internal/store/workpin.go", "scanWorkPinDrivingSessions", "len(sessions)", boundItems)

	// Final full-string writer guards. The SQLite length(TEXT) CHECKs are
	// NUL-prefix constraints and never bounded the complete stored text, so
	// every projection column cites the Go admission guard its one fold
	// applies before writing: a workflowString or workflowList call argument
	// resolved through the fold's own body, the inline comparison the
	// validation helper carries, or the deterministic digest identity the
	// writer derives.
	contractLegislativeSubjects := goCallBound("internal/store/workflow.go", "validLegislativeSubjects", "workflowList", "", 1, boundItems)
	contractRouteConventions := pkGuardArgBound("internal/store/workflow.go", "normalizeWorkflowContractApproval", "workflowList", "p.RouteConventions", 1, boundItems)
	contractSpecMandate := pkGuardArgBound("internal/store/workflow.go", "normalizeWorkflowContractApproval", "workflowList", "p.SpecMandate", 1, boundItems)
	contractLawModifies := pkGuardArgBound("internal/store/workflow.go", "normalizeWorkflowContractApproval", "workflowList", "p.LawModifies", 1, boundItems)
	actorRefDerived := workflowDigestRefCharactersBound("internal/store/workflow.go", "DeriveWorkflowActorRef")
	sessionRefGuard := pkGuardArgBound("internal/store/workflow.go", "persistWorkflowActorTuple", "workflowString", "p.SessionRef", 1, boundBytes)
	checkpointActiveUnit := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowString", "p.ActiveUnit", 1, boundBytes)
	checkpointHypothesis := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowString", "p.Hypothesis", 1, boundBytes)
	checkpointDiagnosis := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowString", "p.Diagnosis", 1, boundBytes)
	checkpointStrategy := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowString", "p.Strategy", 1, boundBytes)
	checkpointTouchedRefs := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowList", "p.TouchedRefs", 1, boundItems)
	checkpointEvidenceRefs := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowList", "p.EvidenceRefs", 1, boundItems)
	checkpointPendingQuestions := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowList", "p.PendingQuestions", 1, boundItems)
	checkpointPendingDecisions := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowList", "p.PendingDecisions", 1, boundItems)
	boundarySummary := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextBoundaryCrossed", "workflowString", "p.Summary", 1, boundBytes)
	designApproachGuard := goInlineBound("internal/store/workflow_design.go", "validateWorkflowDesignContent", "len(p.Approach)", boundBytes)
	designDecisionsCount := goInlineBound("internal/store/workflow_design.go", "validateWorkflowDesignContent", "len(p.Decisions)", boundItems)
	designTouchedRefsGuard := goCallBound("internal/store/workflow_design.go", "validateWorkflowDesignContent", "workflowList", "", 1, boundItems)
	proposalProblemGuard := pkGuardArgBound("internal/store/workflow.go", "validateWorkflowProposalContent", "validWorkflowProseItem", "content.Problem", 1, boundCharacters)
	proposalConstraintsCount := goInlineBound("internal/store/workflow.go", "validateWorkflowProposalContent", "len(content.Constraints)", boundItems)
	proposalUserOutcomesCount := goInlineBound("internal/store/workflow.go", "validateWorkflowProposalContent", "len(content.UserOutcomes)", boundItems)
	linearIssueKeyGuard := goInlineBound("internal/store/linear_issue_link.go", "validateLinearIssueLink", "len(link.HumanKey)", boundBytes)

	declaredPayloadFields := requiredIntentFieldsBound()

	// The delivery assertion artifact writer uses record_delivery's admitted
	// delivery_artifact input.

	// The committed law read admits each law title at maxManifestTitle code points.
	manifestRecordTitle := goConstantBound("internal/store/knowledge_record.go", "maxManifestTitle", boundCharacters)

	// Contract premise: the fold's single premise guard is
	// workflowString(p.Premise, WorkflowPremiseMaxLength) at
	// workflow.go:984; the constant lives in workflow_registry.go beside the
	// round-trip rule, outside the fold's const scope, so the row cites the
	// live constant and names the guard site.
	workflowPremiseMax := goConstantBound("internal/store/workflow_action_policy.go", "WorkflowPremiseMaxLength", boundBytes)

	// Law context Domain identity: the committed law read validates each
	// registry Domain record through the retained chain
	// (validateKnowledgeDomainRegistry -> validateKnowledgeDomainRecord in
	// knowledge_record.go), so the full-string guards are the writer
	// chain's bounds.
	domainNameGuard := goInlineBound("internal/store/knowledge_record.go", "validateKnowledgeDomainRecord", "utf8.RuneCountInString(domain.Name)", boundCharacters)
	domainPurposeGuard := goInlineBound("internal/store/knowledge_record.go", "validateKnowledgeDomainRecord", "utf8.RuneCountInString(domain.Purpose)", boundCharacters)

	// Continuity boundary: the summary column carries the fold's own CHECK,
	// and recorded_at rides the timestamp format.
	registerOutputBound("/$defs/continuity_boundary/properties/recorded_at/maxLength", boundaryRecordedAt)
	registerOutputBound("/$defs/continuity_boundary/properties/summary/maxLength", boundarySummary)

	// Context checkpoint: the read scans the latest projection row, whose
	// columns carry the fold admission bounds as CHECKs.
	registerOutputBound("/$defs/continuity_checkpoint/properties/active_unit/maxLength", checkpointActiveUnit)
	registerOutputBound("/$defs/continuity_checkpoint/properties/diagnosis/maxLength", checkpointDiagnosis)
	registerOutputBound("/$defs/continuity_checkpoint/properties/evidence_refs/maxItems", checkpointEvidenceRefs)
	registerOutputBound("/$defs/continuity_checkpoint/properties/hypothesis/maxLength", checkpointHypothesis)
	registerOutputBound("/$defs/continuity_checkpoint/properties/pending_decisions/maxItems", checkpointPendingDecisions)
	registerOutputBound("/$defs/continuity_checkpoint/properties/pending_questions/maxItems", checkpointPendingQuestions)
	registerOutputBound("/$defs/continuity_checkpoint/properties/strategy/maxLength", checkpointStrategy)
	registerOutputBound("/$defs/continuity_checkpoint/properties/touched_refs/maxItems", checkpointTouchedRefs)

	// Pending project handoff: the snapshot rides the fold-admitted record
	// whose validator holds the named constants.
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/artifact_refs/items/maxLength", handoffEntryMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/artifact_refs/maxItems", handoffListMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/blockers/items/maxLength", handoffEntryMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/blockers/maxItems", handoffListMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/bounded_job/maxLength", handoffJobMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/changes/items/maxLength", handoffEntryMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/changes/maxItems", handoffListMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/consumed_at/maxLength", handoffRecordedAt)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/consumed_by_session_ref/maxLength",
		sessionRefGuard) // the value is grant.SessionRef of the consuming session
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/handoff_id/maxLength", handoffIDMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/next_action/maxLength", handoffActionMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/recorded_at/maxLength", handoffRecordedAt)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/source_session_ref/maxLength",
		sessionRefGuard) // the value is grant.SessionRef from the workflow_actors row
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/verification/items/maxLength", handoffEntryMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/verification/maxItems", handoffListMax)

	// Continuity projection page and pinned step identity: pattern-admitted
	// step ids.
	registerOutputBound("/$defs/continuity_projection_page/properties/workflow_step/maxLength", stepIDPattern)

	// Continuity snapshot windows and pinned fields.
	registerOutputBound("/$defs/continuity_snapshot/properties/boundaries/properties/items/maxItems", continuityPageLimit)
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/product_identity/maxItems", productIdentityBound)
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/spec_mandate/maxItems",
		contractSpecMandate)
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/step_actions/maxItems", stepActionCount)
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/workflow_step/maxLength", stepIDPattern)

	// Work liveness: read-time evidence assembled from one state-switch
	// assignment plus at most two conditional single-element appends; each
	// element is a fixed prose format with one int64 count. last_progress
	// rides the current work row's timestamp format.
	registerOutputBound("/$defs/work_liveness/properties/evidence/items/maxLength",
		workflowStringConstructionCharactersBound("internal/store/work_removal.go", "workLivenessFromCounts"))
	registerOutputBound("/$defs/work_liveness/properties/evidence/maxItems",
		workflowAppendChainItemsBound("internal/store/work_removal.go", "workLivenessFromCounts", "out.Evidence"))
	registerOutputBound("/$defs/work_liveness/properties/last_progress/maxLength", lastProgressAt)

	// Work pin.
	registerOutputBound("/$defs/work_pin/properties/driving_sessions/maxItems", drivingSessionCount)
	registerOutputBound("/$defs/work_pin/properties/linear_issue_key/maxLength", linearIssueKeyGuard)
	registerOutputBound("/$defs/work_pin/properties/next_valid_intents/items/properties/required_fields/maxItems",
		declaredPayloadFields)
	registerOutputBound("/$defs/work_pin/properties/next_valid_intents/maxItems",
		workPinIntentItemsBound())

	// Workflow contract: the projection columns carry the admission CHECKs.
	registerOutputBound("/$defs/workflow_contract/properties/approved_at/maxLength", contractApprovedAt)
	registerOutputBound("/$defs/workflow_contract/properties/approved_by/maxLength",
		actorRefDerived) // event.Actor is the workflow_actors row the fold requires
	// The decision fact enters through approval and ordinary contract updates,
	// which a consequence_v1 pin requires and a legacy pin refuses;
	// the fold re-admits the same canonical set
	// (validateWorkflowDecisionDomains) for replayed events.
	registerOutputBound("/$defs/workflow_contract/properties/decision_domains/maxItems", maxBound(
		inputBound("/$defs/work_transition_action_variant_approve_contract/properties/fields/properties/decision_domains/maxItems"),
		inputBound("/$defs/workflow_contract_update/properties/decision_domains/maxItems")))
	registerOutputBound("/$defs/workflow_contract/properties/law_modifies/maxItems", contractLawModifies)
	registerOutputBound("/$defs/workflow_contract/properties/legislative_subjects/maxItems", contractLegislativeSubjects)
	registerOutputBound("/$defs/workflow_contract/properties/route_conventions/maxItems", contractRouteConventions)
	registerOutputBound("/$defs/workflow_contract/properties/spec_mandate/maxItems", contractSpecMandate)

	registerOutputBound("/$defs/workflow_plan_attempt/properties/note/maxLength",
		goInlineBound("internal/store/workflow_step_plan.go", "validateStepPlanAttemptsShape", "len(attempt.Note)", boundBytes))

	// Resume read (C19.Resume): the read returns the stored plan exactly as
	// written, so the stored-plan shape guard bounds the steps and, because
	// the frontier selects among those steps, the derived next-step ids too.
	// The opening steps are fixed slice literals.
	registerOutputBound("/$defs/resume_read_document/properties/steps/maxItems",
		goInlineBound("internal/store/workflow_step_plan.go", "validateStepPlanShape", "len(plan.Steps)", boundItems))
	registerOutputBound("/$defs/resume_read_document/properties/next_steps/maxItems",
		goInlineBound("internal/store/workflow_step_plan.go", "validateStepPlanShape", "len(plan.Steps)", boundItems)) // one id per stored step at most
	registerOutputBound("/$defs/resume_read_document/properties/opening_steps/maxItems",
		workflowReturnLiteralsMaxItemsBound("internal/store/workflow_resume_read.go", "workflowOpeningSteps"))

	// Design decision: the admitted record_design shape, re-admitted by the
	// fold with the same numbers (internal/store/workflow_design.go:23-28).
	registerOutputBound("/$defs/workflow_design_decision/properties/choice/maxLength",
		inputBound("/$defs/workflow_design_decision/properties/choice/maxLength"))
	registerOutputBound("/$defs/workflow_design_decision/properties/question/maxLength",
		inputBound("/$defs/workflow_design_decision/properties/question/maxLength"))
	registerOutputBound("/$defs/workflow_design_decision/properties/rationale/maxLength",
		inputBound("/$defs/workflow_design_decision/properties/rationale/maxLength"))
	registerOutputBound("/$defs/workflow_design_decision/properties/rejected/items/maxLength",
		inputBound("/$defs/workflow_design_decision/properties/rejected/items/maxLength"))
	registerOutputBound("/$defs/workflow_design_decision/properties/rejected/maxItems",
		inputBound("/$defs/workflow_design_decision/properties/rejected/maxItems"))

	// Design record: the projection columns carry the admission CHECKs.
	registerOutputBound("/$defs/workflow_design_record/properties/approach/maxLength", designApproachGuard)
	registerOutputBound("/$defs/workflow_design_record/properties/decisions/maxItems", designDecisionsCount)
	registerOutputBound("/$defs/workflow_design_record/properties/touched_refs/maxItems", designTouchedRefsGuard)

	// Law context.
	registerRetainedTwinBound("/$defs/workflow_law_context/properties/domains/maxItems",
		workflowLawContextDomainsItemsBound())
	registerRetainedTwinBound("/$defs/workflow_law_context/properties/laws/maxItems",
		sumBound(contractSpecMandate, bindingMaxLawAdditions, bindingMaxObligations)) // the 128-entry ceiling is the documented write-side sum (workflow_law_revision.go:941-943)
	registerRetainedTwinBound("/$defs/workflow_law_context/properties/registry_path/maxLength",
		unboundedOutput("knowledgeRegistryLocator joins the knowledge home's canonical-path locator with one fixed shard file name (internal/store/workflow_law_revision.go:836,849); the home locator's normalized value derives through normalizePath, whose symlink resolution expands the path to filesystem-target text with no length guard (internal/store/project_locator.go:154-161), so the joined locator is byte-ceiling only", readSizeRefusalPath, mutationResultPath))
	// Law context Domain identity: the full-string guards sit in the
	// committed law read (knowledge_record.go,
	// validateKnowledgeDomainRegistry -> validateKnowledgeDomainRecord), so
	// the already-validated fields are the writer chain's bounds.
	registerOutputBound("/$defs/workflow_law_context_domain/properties/name/maxLength", domainNameGuard)
	registerOutputBound("/$defs/workflow_law_context_domain/properties/purpose/maxLength", domainPurposeGuard)
	registerRetainedTwinBound("/$defs/workflow_law_context_law/properties/obligation_ids/maxItems",
		bindingMaxObligations) // the law's obligations are its subset of the binding's verification obligations
	// Genuinely unbounded: the law file locator joins the knowledge home's
	// normalized canonical-path locator, whose value derives through
	// normalizePath — NUL is refused, but symlink resolution expands the
	// path to filesystem-target text no code length bounds — with the
	// record path.
	registerRetainedTwinBound("/$defs/workflow_law_context_law/properties/path/maxLength",
		unboundedOutput("the law locator joins the knowledge home's canonical-path locator, whose normalized value derives through normalizePath: NUL is refused (internal/store/project_locator.go:150-152) but filepath.EvalSymlinks expands symlinked ancestors to filesystem-target text with no length guard (internal/store/project_locator.go:154-161), so the joined locator is byte-ceiling only", readSizeRefusalPath, mutationResultPath))
	registerRetainedTwinBound("/$defs/workflow_law_context_law/properties/roles/maxItems",
		workflowRoleMarkItemsBound("internal/store/workflow_law_revision.go", "readWorkflowLawContext", "markRole"))
	registerRetainedTwinBound("/$defs/workflow_law_context_law/properties/title/maxLength",
		manifestRecordTitle) // the law context copies the committed law title verbatim

	// Contract premise: the fold's admission guard is
	// workflowString(p.Premise, WorkflowPremiseMaxLength); the cited
	// constant is that guard's capacity, live-read from its declaration.
	registerOutputBound("/$defs/workflow_premise/maxLength", workflowPremiseMax)

	// Retained proposal rows are checked by the proposal fold's prose guard.
	registerOutputBound("/$defs/workflow_proposal_record/properties/constraints/items/maxLength",
		proposalListItemBound("Constraints"))
	registerOutputBound("/$defs/workflow_proposal_record/properties/constraints/maxItems", proposalConstraintsCount)
	registerOutputBound("/$defs/workflow_proposal_record/properties/problem/maxLength", proposalProblemGuard)
	registerOutputBound("/$defs/workflow_proposal_record/properties/user_outcomes/items/maxLength",
		proposalListItemBound("UserOutcomes"))
	registerOutputBound("/$defs/workflow_proposal_record/properties/user_outcomes/maxItems", proposalUserOutcomesCount)

}

// proposalListItemBound follows the grouped lists through the fold validator's
// nested iteration to its per-item rune guard.
func proposalListItemBound(field string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, _ *boundCensus) (evaluatedBound, error) {
		const file = "internal/store/workflow.go"
		goS, fn, err := workflowFunctionDecl(rt, file, "validateWorkflowProposalContent")
		if err != nil {
			return evaluatedBound{}, err
		}
		if err := reconcileGoUnit(boundCharacters, rt.inferFromCallee(file, "validWorkflowProseItem")); err != nil {
			return evaluatedBound{}, err
		}
		var limit ast.Expr
		matches := 0
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			loop, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}
			group, ok := loop.X.(*ast.CompositeLit)
			if !ok {
				return true
			}
			contains := false
			for _, item := range group.Elts {
				if sel, ok := item.(*ast.SelectorExpr); ok && sel.Sel.Name == field {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "content" {
						contains = true
					}
				}
			}
			if !contains {
				return true
			}
			ast.Inspect(loop.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if ok && ledgerCalleeMatches(call, "validWorkflowProseItem") && len(call.Args) == 2 && ledgerExprText(call.Args[0]) == "value" {
					matches++
					limit = call.Args[1]
				}
				return true
			})
			return true
		})
		if matches != 1 {
			return evaluatedBound{}, fmt.Errorf("proposal %s has %d grouped item guards", field, matches)
		}
		value, err := rt.evalNumeric(goS, fn, limit)
		if err != nil {
			return evaluatedBound{}, err
		}
		return workflowCharactersEvaluated(value, "proposal fold guards each "+field+" item with validWorkflowProseItem"), nil
	})
}

// TestOutputBoundProposalListItemSource proves proposalListItemBound refuses
// once a list field leaves validateWorkflowProposalContent's grouped per-item
// prose guard, so the workflow_proposal_record item rows cite a live guard.
func TestOutputBoundProposalListItemSource(t *testing.T) {
	root := ledgerRepoRoot(t)
	const file = "internal/store/workflow.go"
	source, err := os.ReadFile(root + "/" + file)
	if err != nil {
		t.Fatal(err)
	}
	const group = "for _, values := range [][]string{content.UserOutcomes, content.Constraints, content.OpenQuestions, content.OutOfScope}"
	text := strings.Replace(string(source), group, "for _, values := range [][]string{content.UserOutcomes, content.OpenQuestions, content.OutOfScope}", 1)
	if text == string(source) {
		t.Fatal("proposal list group not found")
	}
	rt := newLedgerRuntime(root)
	if err := rt.parseGoSource(file, text); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.evaluateSource(proposalListItemBound("Constraints"), nil); err == nil {
		t.Fatal("removed proposal list guard was accepted")
	}
}

// ---------------------------------------------------------------------------
// Family-local derived sources
//
// Each constructor below returns one derived source whose evaluator inspects
// one exact live AST shape and refuses every unsupported writer, including
// any second writer of the counted field. None of them reads a filesystem
// limit.
// ---------------------------------------------------------------------------

// workflowDerived wraps one evaluator as a derived output bound source.
func workflowDerived(derive func(*ledgerRuntime, *boundCensus) (evaluatedBound, error)) outputBoundSource {
	return outputBoundSource{kind: "derived", derive: derive}
}

// workflowInt64Slot is the render width of one %d verb over an int64:
// the sign plus the 19 digits of the minimum int64.
const workflowInt64Slot = 20

// workflowExprText renders one identifier or selector chain. Any other
// expression renders as "" so target comparisons fail closed.
func workflowExprText(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		prefix := workflowExprText(e.X)
		if prefix == "" {
			return ""
		}
		return prefix + "." + e.Sel.Name
	case *ast.ParenExpr:
		return workflowExprText(e.X)
	default:
		return ""
	}
}

// workflowFunctionDecl loads one Go file and resolves the named function,
// which must be declared exactly once with a body.
func workflowFunctionDecl(rt *ledgerRuntime, file, function string) (*goSource, *ast.FuncDecl, error) {
	goS, err := rt.loadGo(file)
	if err != nil {
		return nil, nil, err
	}
	fns := ledgerFuncDeclsNamed(goS.file, function)
	if len(fns) != 1 || fns[0].Body == nil {
		return nil, nil, fmt.Errorf("workflow derived source: function %s must be declared exactly once with a body in %s", function, file)
	}
	return goS, fns[0], nil
}

// workflowLoopBodyNodes marks every statement inside a for or range loop of
// the function, so construction counters can refuse loop-multiplied appends.
func workflowLoopBodyNodes(fn *ast.FuncDecl) map[ast.Node]bool {
	nodes := map[ast.Node]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		switch l := n.(type) {
		case *ast.ForStmt:
			ast.Inspect(l.Body, func(m ast.Node) bool { nodes[m] = true; return true })
		case *ast.RangeStmt:
			ast.Inspect(l.Body, func(m ast.Node) bool { nodes[m] = true; return true })
		}
		return true
	})
	return nodes
}

func workflowItemsEvaluated(value int64, from string) evaluatedBound {
	return evaluatedBound{constraints: []boundConstraint{{unit: boundItems, value: value, from: from}}, kind: "derived", describe: from}
}

// TestOutputBoundWorkPinIntentSource proves the registered work_pin
// next_valid_intents source evaluates to a finite capacity within
// agentcontract.MaxNextValidIntents.
func TestOutputBoundWorkPinIntentSource(t *testing.T) {
	const pattern = "/$defs/work_pin/properties/next_valid_intents/maxItems"
	rt := newLedgerRuntime(ledgerRepoRoot(t))
	for _, row := range ledgerRegistrations {
		if row.pattern == pattern {
			bound, err := rt.evaluateSource(row.source, nil)
			if err != nil {
				t.Fatalf("work pin intent source: %v", err)
			}
			if capacity, ok := bound.capacity(boundItems); !ok || capacity > agentcontract.MaxNextValidIntents {
				t.Fatalf("work pin capacity = %d (finite=%t), envelope limit = %d", capacity, ok, agentcontract.MaxNextValidIntents)
			}
			return
		}
	}
	t.Fatal("work pin intent source is not registered")
}

// TestOutputBoundContinuityReachableFrontier drives a live workflow through
// an inserted-step type switch and proves the reachable continuity snapshot's
// step_actions stays within its schema maxItems.
func TestOutputBoundContinuityReachableFrontier(t *testing.T) {
	ctx := t.Context()
	s := openTemp(t)
	defer s.Close()
	seedStepWork(t, s, "work")
	if err := s.Transact(ctx, func(tx *Transaction) error {
		scope := newWriteScope(tx.tx)
		if err := scope.enter(ctx); err != nil {
			return err
		}
		if _, err := tx.tx.ExecContext(ctx, `INSERT INTO workflow_instances(work_id,instance_state) VALUES('work','intake')`); err != nil {
			return err
		}
		return scope.close(ctx)
	}); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `SELECT version FROM work_items WHERE id='work'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	seq := 0
	apply := func(action string, payload any) {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		seq++
		request := WorkflowActionExecutionRequest{WorkID: "work", ExpectedVersion: version, ActionID: action, Payload: raw, Actor: stepFixtureActor(), OperationID: fmt.Sprintf("frontier-op-%d", seq), IdempotencyIdentity: fmt.Sprintf("frontier-key-%d", seq), PrincipalRef: "principal-1", RequestID: fmt.Sprintf("frontier-request-%d", seq), AcceptedInputsDigest: testDigest("inputs"), ContractDigest: testManifestDigest, Now: time.Unix(100, 0).UTC()}
		var result WorkflowActionExecutionResult
		err = s.Transact(ctx, func(tx *Transaction) error {
			var err error
			result, err = ApplyWorkflowActionTx(ctx, tx, request)
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		version = result.ResultingVersion
	}
	apply("record_intake", map[string]any{"outcome": "proceed", "open_questions_found": false})
	apply("pick_type", map[string]any{"type": "change", "reason": "initial pick"})
	change := stepPlanTemplateOrFatal(t, WorkflowStepPlanChange)
	research := stepPlanTemplateOrFatal(t, WorkflowStepPlanResearch)
	for _, step := range research.Steps {
		apply("insert_step", map[string]any{"id": step.ID, "after": []string{change.Steps[0].ID}, "reason": "additional work"})
	}
	apply("record_plan", json.RawMessage(testWorkflowPlanDocument))
	apply("complete_step", map[string]any{"step_id": change.Steps[0].ID, "note": "done"})
	apply("pick_type", map[string]any{"type": "research", "reason": "switch type"})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var snapshot ContinuitySnapshot
	if err := readContinuityInstanceTx(ctx, tx, "work", &snapshot); err != nil {
		t.Fatal(err)
	}
	doc, err := newLedgerRuntime(ledgerRepoRoot(t)).loadDoc(payloadSchemaFile)
	if err != nil {
		t.Fatal(err)
	}
	value, ok := resolveSchemaPointer(doc, "/$defs/continuity_snapshot/properties/pinned/properties/step_actions/maxItems")
	if !ok {
		t.Fatal("continuity step_actions bound is missing")
	}
	limit, err := value.(json.Number).Int64()
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(snapshot.StepActions)) > limit {
		node, ok := resolveSchemaPointer(doc, "/$defs/continuity_snapshot/properties/pinned/properties/step_actions")
		if !ok {
			t.Fatal("continuity step_actions schema is missing")
		}
		raw, err := json.Marshal(snapshot.StepActions)
		if err != nil {
			t.Fatal(err)
		}
		var actions any
		if err := json.Unmarshal(raw, &actions); err != nil {
			t.Fatal(err)
		}
		validation := payloadschema.ValidateValue(actions, node.(map[string]any), doc, "$.pinned.step_actions")
		t.Fatalf("reachable continuity step_actions contains %d actions, exceeding schema maxItems %d; payload validation: %v; actions: %v", len(snapshot.StepActions), limit, validation, snapshot.StepActions)
	}
}

// TestOutputBoundIntentCountDirectWriters proves workPinIntentItemsBound
// counts the direct intent writer in internal/agent/mutations.go at its
// literal width and refuses a non-literal producer.
func TestOutputBoundIntentCountDirectWriters(t *testing.T) {
	root := ledgerRepoRoot(t)
	const file = "internal/agent/mutations.go"
	source, err := os.ReadFile(root + "/" + file)
	if err != nil {
		t.Fatal(err)
	}
	const original = `plan.intents = []agentcontract.NextIntent{{Tool: "concord_work_browse", Operation: "list", QueryID: "PM1.Q3", ReasonCode: "refresh_work_version", RequiredFields: []string{"work_ids", "detail"}}}`
	for _, tc := range []struct {
		name, replacement string
		want              int64
		refuse            bool
	}{
		{"larger literal", `plan.intents = []agentcontract.NextIntent{` + strings.Repeat(`{},`, 40) + `}`, 40, false},
		{"unknown producer", `plan.intents = otherIntents`, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := strings.Replace(string(source), original, tc.replacement, 1)
			if text == string(source) {
				t.Fatal("direct intent list not found")
			}
			rt := newLedgerRuntime(root)
			if err := rt.parseGoSource(file, text); err != nil {
				t.Fatal(err)
			}
			bound, err := rt.evaluateSource(workPinIntentItemsBound(), nil)
			if tc.refuse {
				if err == nil {
					t.Fatal("unknown intent list accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := bound.capacity(boundItems); !ok || got != tc.want {
				t.Fatalf("capacity = %d (finite=%t), want %d", got, ok, tc.want)
			}
		})
	}
}

func workflowCharactersEvaluated(value int64, from string) evaluatedBound {
	return evaluatedBound{constraints: []boundConstraint{{unit: boundCharacters, value: value, from: from}}, kind: "derived", describe: from}
}

// workflowAppendChainItemsBound counts a slice a named function builds from
// one composite-literal assignment plus unconditional-or-conditional
// single-element appends. An append inside a loop, a variadic append, an
// append of more than one element, or a reassignment from a non-composite
// value refuses: any of those can multiply the count past this arithmetic.
// The reported bound is the largest assignment plus every append site, a
// sound over-approximation of one execution's final content.
func workflowAppendChainItemsBound(file, function, target string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		loopNodes := workflowLoopBodyNodes(fn)
		initial, appends := -1, 0
		var refusal error
		ast.Inspect(fn, func(n ast.Node) bool {
			if refusal != nil {
				return false
			}
			switch node := n.(type) {
			case *ast.AssignStmt:
				for i, rhs := range node.Rhs {
					if i >= len(node.Lhs) || workflowExprText(node.Lhs[i]) != target {
						continue
					}
					if call, isCall := rhs.(*ast.CallExpr); isCall && ledgerCalledName(call.Fun) == "append" {
						continue // counted at the call site below
					}
					lit, ok := rhs.(*ast.CompositeLit)
					if !ok {
						refusal = fmt.Errorf("workflow append chain %s:%s assigns %s from an unsupported shape", file, function, target)
						return false
					}
					if loopNodes[node] {
						refusal = fmt.Errorf("workflow append chain %s:%s constructs %s inside a loop", file, function, target)
						return false
					}
					if len(lit.Elts) > initial {
						initial = len(lit.Elts)
					}
				}
			case *ast.CallExpr:
				if ledgerCalledName(node.Fun) != "append" || len(node.Args) == 0 || workflowExprText(node.Args[0]) != target {
					return true
				}
				if loopNodes[node] {
					refusal = fmt.Errorf("workflow append chain %s:%s appends %s inside a loop", file, function, target)
					return false
				}
				if len(node.Args) != 2 || node.Ellipsis != token.NoPos {
					refusal = fmt.Errorf("workflow append chain %s:%s appends %s with a variadic or multi-element shape", file, function, target)
					return false
				}
				appends++
			}
			return true
		})
		if refusal != nil {
			return evaluatedBound{}, refusal
		}
		if initial < 0 {
			return evaluatedBound{}, fmt.Errorf("workflow append chain %s:%s never constructs %s", file, function, target)
		}
		value := int64(initial + appends)
		from := fmt.Sprintf("workflow construction %s:%s builds %s from one %d-element composite plus %d single-element appends", file, function, target, initial, appends)
		return workflowItemsEvaluated(value, from), nil
	})
}

// workflowReturnLiteralsMaxItemsBound derives the widest slice composite
// literal among a named function's returns. Every return statement must
// return exactly one unkeyed slice composite literal: while all outputs are
// fixed literals, the widest literal is the emitted count. Any other return,
// or an indexed element that could widen a literal past its element count,
// fails closed instead of being ignored.
func workflowReturnLiteralsMaxItemsBound(file, function string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		var returns []*ast.ReturnStmt
		ast.Inspect(fn, func(n ast.Node) bool {
			if returned, ok := n.(*ast.ReturnStmt); ok {
				returns = append(returns, returned)
			}
			return true
		})
		if len(returns) == 0 {
			return evaluatedBound{}, fmt.Errorf("workflow return literals %s:%s declares no return statements", file, function)
		}
		widest := int64(0)
		for _, returned := range returns {
			if len(returned.Results) != 1 {
				return evaluatedBound{}, fmt.Errorf("workflow return literals %s:%s must return exactly one result per return", file, function)
			}
			lit, ok := returned.Results[0].(*ast.CompositeLit)
			if !ok {
				return evaluatedBound{}, fmt.Errorf("workflow return literals %s:%s returns a non-composite value", file, function)
			}
			if array, isArray := lit.Type.(*ast.ArrayType); !isArray || array.Len != nil {
				return evaluatedBound{}, fmt.Errorf("workflow return literals %s:%s returns a non-slice composite", file, function)
			}
			for _, elt := range lit.Elts {
				if _, keyed := elt.(*ast.KeyValueExpr); keyed {
					return evaluatedBound{}, fmt.Errorf("workflow return literals %s:%s returns an indexed slice literal", file, function)
				}
			}
			if int64(len(lit.Elts)) > widest {
				widest = int64(len(lit.Elts))
			}
		}
		from := fmt.Sprintf("workflow construction %s:%s returns one of %d slice literals, widest %d elements", file, function, len(returns), widest)
		return workflowItemsEvaluated(widest, from), nil
	})
}

// workflowFormatDigitVerbs scans one literal format string and accepts only
// %% and %d. It returns the %d count and the render's fixed literal width:
// every non-verb rune prints as itself, each %% prints one %, and each %d
// token itself renders nothing. Width flags, other verbs, and a trailing
// bare % refuse: the render width would no longer be derivable.
func workflowFormatDigitVerbs(format string) (verbs, literals int, err error) {
	runes := []rune(format)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '%' {
			literals++
			continue
		}
		if i+1 >= len(runes) {
			return 0, 0, fmt.Errorf("workflow format %q carries a trailing bare %%", format)
		}
		switch runes[i+1] {
		case '%':
			literals++
			i++
		case 'd':
			verbs++
			i++
		default:
			return 0, 0, fmt.Errorf("workflow format %q carries unsupported verb %q", format, string(runes[i:i+2]))
		}
	}
	return verbs, literals, nil
}

// workflowStringConstructionCharactersBound measures the largest string
// construction a named function emits: every string literal, plus every
// Sprintf with a literal format at its rune count plus workflowInt64Slot
// per %d verb. A Sprintf with a non-literal format or an unsupported verb
// refuses: the function's render width would no longer be derivable.
func workflowStringConstructionCharactersBound(file, function string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		found := false
		characters := int64(0)
		var refusal error
		ast.Inspect(fn, func(n ast.Node) bool {
			if refusal != nil {
				return false
			}
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(node.Value)
				if err != nil {
					refusal = fmt.Errorf("workflow string construction %s:%s carries an unparsable string literal", file, function)
					return false
				}
				if r := int64(utf8.RuneCountInString(value)); r > characters {
					characters = r
				}
				found = true
			case *ast.CallExpr:
				if !ledgerCalleeMatches(node, "Sprintf") || len(node.Args) == 0 {
					return true
				}
				lit, ok := node.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					refusal = fmt.Errorf("workflow string construction %s:%s carries a Sprintf whose format is not a literal", file, function)
					return false
				}
				format, err := strconv.Unquote(lit.Value)
				if err != nil {
					refusal = fmt.Errorf("workflow string construction %s:%s carries an unparsable Sprintf format", file, function)
					return false
				}
				verbs, literals, err := workflowFormatDigitVerbs(format)
				if err != nil {
					refusal = fmt.Errorf("workflow string construction %s:%s: %w", file, function, err)
					return false
				}
				if r := int64(literals) + int64(verbs)*workflowInt64Slot; r > characters {
					characters = r
				}
				found = true
			}
			return true
		})
		if refusal != nil {
			return evaluatedBound{}, refusal
		}
		if !found {
			return evaluatedBound{}, fmt.Errorf("workflow string construction %s:%s carries no string construction", file, function)
		}
		from := fmt.Sprintf("workflow construction %s:%s renders strings at most %d characters (largest literal or literal-format Sprintf)", file, function, characters)
		return workflowCharactersEvaluated(characters, from), nil
	})
}

// workflowRoleMarkItemsBound counts the distinct constant role arguments a
// named function passes to one marking callee: the closed role set the
// projection deduplicates per subject.
func workflowRoleMarkItemsBound(file, function, marker string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		consts, err := rt.ledgerConstScope(goS, fn)
		if err != nil {
			return evaluatedBound{}, err
		}
		seen := map[string]bool{}
		var refusal error
		ast.Inspect(fn, func(n ast.Node) bool {
			if refusal != nil {
				return false
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || ledgerCalledName(call.Fun) != marker {
				return true
			}
			if len(call.Args) != 2 {
				refusal = fmt.Errorf("workflow role mark %s:%s %s call does not carry two arguments", file, function, marker)
				return false
			}
			ident, ok := call.Args[1].(*ast.Ident)
			if !ok {
				refusal = fmt.Errorf("workflow role mark %s:%s %s role argument is not a constant identifier", file, function, marker)
				return false
			}
			def, ok := consts[ident.Name]
			if !ok {
				refusal = fmt.Errorf("workflow role mark %s:%s role %s resolves to no constant", file, function, ident.Name)
				return false
			}
			lit, ok := def.expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				refusal = fmt.Errorf("workflow role mark %s:%s role %s is not a string literal", file, function, ident.Name)
				return false
			}
			if _, err := strconv.Unquote(lit.Value); err != nil {
				refusal = fmt.Errorf("workflow role mark %s:%s role %s is unparsable: %w", file, function, ident.Name, err)
				return false
			}
			seen[ident.Name] = true
			return true
		})
		if refusal != nil {
			return evaluatedBound{}, refusal
		}
		if len(seen) == 0 {
			return evaluatedBound{}, fmt.Errorf("workflow role mark %s:%s carries no %s call", file, function, marker)
		}
		from := fmt.Sprintf("workflow role mark %s:%s marks %d distinct constant roles", file, function, len(seen))
		return workflowItemsEvaluated(int64(len(seen)), from), nil
	})
}

// workflowLawContextDomainsItemsBound proves the law-context Domain list is
// one fixed home Domain plus the binding's affected Domains, and cites the
// affected list's inline admission guard: one home element plus the guarded
// affected capacity, which the per-Domain dedup can only shrink.
func workflowLawContextDomainsItemsBound() outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, "internal/store/workflow_law_revision.go", "resolveLawContextDomains")
		if err != nil {
			return evaluatedBound{}, err
		}
		matches := 0
		ast.Inspect(fn, func(n ast.Node) bool {
			loop, ok := n.(*ast.RangeStmt)
			if !ok {
				return true
			}
			call, ok := loop.X.(*ast.CallExpr)
			if !ok || ledgerCalledName(call.Fun) != "append" || len(call.Args) != 2 {
				return true
			}
			lit, ok := call.Args[0].(*ast.CompositeLit)
			if !ok || len(lit.Elts) != 1 || call.Ellipsis == token.NoPos {
				return true
			}
			matches++
			return true
		})
		if matches != 1 {
			return evaluatedBound{}, fmt.Errorf("workflow law context domains: resolveLawContextDomains carries %d one-home-plus-affected range sources; the construction is not unique", matches)
		}
		member := goInlineBound("internal/store/workflow_architecture_binding.go", "validateWorkflowArchitectureBindingShape", "len(binding.AffectedDomainIDs)", boundItems)
		evaluated, err := rt.evaluateSource(member, census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("workflow law context domains: %w", err)
		}
		affected, ok := evaluated.capacity(boundItems)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("workflow law context domains: the affected-domain guard has no item capacity")
		}
		value := 1 + affected
		from := fmt.Sprintf("workflow law context domains: one home Domain plus the guarded affected list (%d), deduplicated", affected)
		return workflowItemsEvaluated(value, from), nil
	})
}

// A pin starts empty and adds each catalog action at most once. Direct agent
// intent lists are bounded by their literal constructions.
func workPinIntentItemsBound() outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, _ *boundCensus) (evaluatedBound, error) {
		catalog := builtinWorkflowActionCatalog()
		if len(catalog) == 0 {
			return evaluatedBound{}, fmt.Errorf("workflow action catalog is empty")
		}
		ids := make([]string, 0, 2*len(catalog))
		for id := range catalog {
			ids = append(ids, id, id)
		}
		var pin WorkPin
		if err := workPinAddCatalogIntents(&pin, ids); err != nil {
			return evaluatedBound{}, err
		}
		if len(pin.NextValidIntents) != len(catalog) {
			return evaluatedBound{}, fmt.Errorf("work pin no longer emits each catalog action at most once")
		}
		goS, err := rt.loadGo("internal/store/workpin.go")
		if err != nil {
			return evaluatedBound{}, err
		}
		assignments, empty, appends := 0, 0, 0
		ast.Inspect(goS.file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				if ledgerExprText(lhs) != "pin.NextValidIntents" || i >= len(assign.Rhs) {
					continue
				}
				assignments++
				switch rhs := assign.Rhs[i].(type) {
				case *ast.CompositeLit:
					if len(rhs.Elts) == 0 {
						empty++
					}
				case *ast.CallExpr:
					if ledgerCalledName(rhs.Fun) == "append" && len(rhs.Args) == 2 && ledgerExprText(rhs.Args[0]) == "pin.NextValidIntents" {
						if call, ok := rhs.Args[1].(*ast.CallExpr); ok && ledgerCalledName(call.Fun) == "workPinIntentForAction" {
							appends++
						}
					}
				}
			}
			return true
		})
		if assignments != 2 || empty != 1 || appends != 1 {
			return evaluatedBound{}, fmt.Errorf("work pin intent constructions changed: %d assignments, %d empty, %d appends", assignments, empty, appends)
		}
		goS, err = rt.loadGo("internal/agent/mutations.go")
		if err != nil {
			return evaluatedBound{}, err
		}
		largest, direct := int64(len(catalog)), 0
		var refusal error
		ast.Inspect(goS.file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok {
				return true
			}
			for i, lhs := range assign.Lhs {
				if ledgerExprText(lhs) != "plan.intents" || i >= len(assign.Rhs) {
					continue
				}
				lit, ok := assign.Rhs[i].(*ast.CompositeLit)
				if !ok {
					refusal = fmt.Errorf("agent plan intent assignment is not a literal")
					continue
				}
				direct++
				if count := int64(len(lit.Elts)); count > largest {
					largest = count
				}
			}
			return true
		})
		if refusal != nil {
			return evaluatedBound{}, refusal
		}
		if direct == 0 {
			return evaluatedBound{}, fmt.Errorf("agent direct intent constructions are absent")
		}
		return workflowItemsEvaluated(largest, "distinct live catalog actions and direct agent intent lists"), nil
	})
}

// workflowDigestRefCharactersBound verifies one writer mints a deterministic
// digest identity: the function's single return concatenates one string
// literal prefix with one hex.EncodeToString whose input slices an
// identifier assigned exactly once from one sha256.Sum256 call, so every
// emitted identity carries exactly the prefix plus 64 lowercase hex
// characters. A foreign call in the returned text, a non-digest hex input,
// or an overwritten digest identifier refuses.
func workflowDigestRefCharactersBound(file, function string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		// The hex input must trace to exactly one sha256.Sum256 assignment
		// of its base identifier: a second assignment could overwrite the
		// digest before the return reads it, and an input that is not a
		// slice of a hashed identifier has no fixed width to derive.
		hashedOnce, hashAssignments := map[string]bool{}, map[string]int{}
		ast.Inspect(fn, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			ident, ok := assign.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			hashAssignments[ident.Name]++
			if call, ok := assign.Rhs[0].(*ast.CallExpr); ok && ledgerCalledName(call.Fun) == "sha256.Sum256" {
				hashedOnce[ident.Name] = true
			}
			return true
		})
		var returns []ast.Expr
		ast.Inspect(fn, func(n ast.Node) bool {
			if returned, ok := n.(*ast.ReturnStmt); ok && len(returned.Results) == 1 {
				returns = append(returns, returned.Results[0])
			}
			return true
		})
		if len(returns) != 1 {
			return evaluatedBound{}, fmt.Errorf("digest ref %s:%s carries %d single-result returns; the identity mint is not unique", file, function, len(returns))
		}
		prefix, hexCalls := "", 0
		var refusal error
		ast.Inspect(returns[0], func(n ast.Node) bool {
			if refusal != nil {
				return false
			}
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind != token.STRING {
					refusal = fmt.Errorf("digest ref %s:%s return carries a non-string literal", file, function)
					return false
				}
				text, err := strconv.Unquote(node.Value)
				if err != nil {
					refusal = fmt.Errorf("digest ref %s:%s prefix is unparsable: %w", file, function, err)
					return false
				}
				prefix += text
			case *ast.CallExpr:
				switch ledgerCalledName(node.Fun) {
				case "hex.EncodeToString":
					hexCalls++
					if len(node.Args) != 1 {
						refusal = fmt.Errorf("digest ref %s:%s hex conversion does not carry one argument", file, function)
						return false
					}
					slice, ok := node.Args[0].(*ast.SliceExpr)
					if !ok {
						refusal = fmt.Errorf("digest ref %s:%s hex input does not slice a hashed identifier", file, function)
						return false
					}
					base, ok := slice.X.(*ast.Ident)
					if !ok || !hashedOnce[base.Name] || hashAssignments[base.Name] != 1 {
						refusal = fmt.Errorf("digest ref %s:%s hex input %s does not trace to exactly one sha256 digest assignment", file, function, workflowExprText(slice.X))
						return false
					}
				default:
					refusal = fmt.Errorf("digest ref %s:%s return carries unsupported call %s; the rendered text is not one literal prefix plus one digest", file, function, ledgerCalledName(node.Fun))
					return false
				}
			}
			return true
		})
		if refusal != nil {
			return evaluatedBound{}, refusal
		}
		if hexCalls != 1 {
			return evaluatedBound{}, fmt.Errorf("digest ref %s:%s carries %d hex conversions; the identity mint is not one digest", file, function, hexCalls)
		}
		value := int64(utf8.RuneCountInString(prefix)) + 64
		from := fmt.Sprintf("digest identity %s:%s mints %q plus one traced sha256 hex digest", file, function, prefix)
		return workflowCharactersEvaluated(value, from), nil
	})
}

// TestWorkflowDigestRefCharactersBound pins the deterministic digest identity
// measure: the prefix plus one traced sha256 digest passes at the fixed
// width; a foreign call in the returned text, a hex input that is not a
// traced digest, and an overwritten digest identifier all refuse.
func TestWorkflowDigestRefCharactersBound(t *testing.T) {
	t.Parallel()
	const good = `package store

import (
	"crypto/sha256"
	"encoding/hex"
)

func deriveRef(a, b string) string {
	sum := sha256.Sum256([]byte(a + b))
	return "actor:" + hex.EncodeToString(sum[:])
}
`
	const foreignReturnedCall = `package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func deriveRef(a, b string) string {
	sum := sha256.Sum256([]byte(a + b))
	return "actor:" + strings.ToUpper(hex.EncodeToString(sum[:]))
}
`
	const nonDigestInput = `package store

import "encoding/hex"

func deriveRef(a string) string {
	return "actor:" + hex.EncodeToString([]byte(a))
}
`
	const overwrittenDigest = `package store

import (
	"crypto/sha256"
	"encoding/hex"
)

func deriveRef(a, b string) string {
	sum := sha256.Sum256([]byte(a))
	sum = sha256.Sum256([]byte(b))
	return "actor:" + hex.EncodeToString(sum[:])
}
`
	cases := []struct {
		name    string
		source  string
		want    int64
		wantErr bool
	}{
		{name: "prefix plus one traced digest passes", source: good, want: int64(len("actor:")) + 64},
		{name: "foreign call in the returned text refuses", source: foreignReturnedCall, wantErr: true},
		{name: "non-digest hex input refuses", source: nonDigestInput, wantErr: true},
		{name: "overwritten digest identifier refuses", source: overwrittenDigest, wantErr: true},
	}
	for _, tc := range cases {
		witness := newLedgerRuntime(ledgerRepoRoot(t))
		if err := witness.parseGoSource("witness.go", tc.source); err != nil {
			t.Fatalf("%s: synthetic source does not parse: %v", tc.name, err)
		}
		evaluated, err := witness.evaluateSource(workflowDigestRefCharactersBound("witness.go", "deriveRef"), nil)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected a closed failure, got %+v", tc.name, evaluated)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(evaluated.constraints) != 1 || evaluated.constraints[0].value != tc.want {
			t.Fatalf("%s: evaluated %+v, want one %d-character constraint", tc.name, evaluated.constraints, tc.want)
		}
	}
}
