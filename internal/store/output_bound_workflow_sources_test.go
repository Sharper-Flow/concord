package store

// Workflow-family rows for the structural output bound ledger: the
// workflow_, continuity_, and work_context_ definitions plus work_pin,
// work_pin_evidence, operator_question, work_liveness, and
// work_size_summary. Every finite row cites a real full-string producer
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
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
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
	contractApprovedAt := pkTimeLayoutWidthBound("internal/store/workflow.go", "foldWorkflowContractApproved", "time.RFC3339Nano")
	boundaryRecordedAt := pkTimeLayoutWidthBound("internal/store/workflow.go", "foldWorkflowContextBoundaryCrossed", "time.RFC3339Nano")
	handoffRecordedAt := pkTimeLayoutWidthBound("internal/store/project_handoffs.go", "recordProjectHandoffCore", "time.RFC3339Nano")
	observationRecordedAt := pkTimeLayoutWidthBound("internal/store/work_observations.go", "foldWorkObservationRecorded", "time.RFC3339Nano")
	sizeRecordedAt := pkTimeLayoutWidthBound("internal/store/work_size.go", "foldWorkSized", "time.RFC3339Nano")
	// advanceWorkEffectSeqTx retains the lexical maximum in
	// work_items.last_progress_at. deriveWorkLivenessQ reads that column;
	// workLivenessFromCounts copies it to LastProgress without reformatting.
	lastProgressAt := pkTimeLayoutWidthBound("internal/store/work_effect_seq.go", "advanceWorkEffectSeqTx", "time.RFC3339Nano")

	// Step identity and workflow references are admitted through anchored
	// regexp.MustCompile variables at definition registration; the pattern
	// helper compiles the live pattern and derives its largest accepted
	// character count.
	stepIDPattern := goPatternBound("internal/store/workflow_registry.go", "workflowIDPattern")
	workflowRefPattern := goPatternBound("internal/store/workflow_registry.go", "workflowRefPattern")

	// Raw event ids: Event.validate admits any caller-supplied non-empty id
	// (internal/store/events.go:164-180), so the digest-suffixed ids the
	// agent boundary mints are one finite alternative beside an unbounded
	// raw identity that projects into the resolved views verbatim.
	eventIDRaw := unboundedOutput("Event.validate admits any caller-supplied non-empty event id with no length bound (internal/store/events.go:164-180 checks presence only); the agent boundary mints digest-suffixed ids (internal/agent/mutations.go:1552-1553), but the raw identity is a valid durable event id and the resolved views project it verbatim", readSizeRefusalPath, mutationResultPath)

	// Guard constants cited by more than one row.
	overlapDetailItems := goConstantBound("internal/store/workflow_domain_overlap.go", "maxWorkflowOverlapDetailItems", boundItems)
	handoffIDMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffIDMax", boundBytes)
	handoffJobMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffJobMax", boundBytes)
	handoffActionMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffActionMax", boundBytes)
	handoffListMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffListMax", boundItems)
	handoffEntryMax := goConstantBound("internal/store/project_handoffs.go", "projectHandoffEntryMax", boundBytes)
	requiredReadingMax := goConstantBound("internal/store/work_context.go", "WorkContextRequiredReadingMax", boundItems)
	viewFindingsMax := goConstantBound("internal/store/work_context.go", "WorkContextViewFindingsMax", boundItems)
	domainIDMaxBytes := goConstantBound("internal/store/work_context.go", "workContextDomainIDMaxBytes", boundBytes)
	projectRefMaxBytes := goConstantBound("internal/store/work_context.go", "workContextProjectIDMaxBytes", boundBytes)
	readingReasonMaxBytes := goConstantBound("internal/store/work_context.go", "workContextReadingMaxBytes", boundBytes)
	rationaleMaxBytes := goConstantBound("internal/store/work_context.go", "workContextRationaleMaxBytes", boundBytes)
	repoPathMaxBytes := goConstantBound("internal/store/work_context.go", "workContextPathMaxBytes", boundBytes)
	lawRefMaxBytes := goConstantBound("internal/store/work_context.go", "workContextLawIDMaxBytes", boundBytes)
	findingStatementMax := goConstantBound("internal/store/worker_context_findings.go", "workerContextFindingStatementMaxBytes", boundBytes)
	findingSubjectMax := goConstantBound("internal/store/worker_context_findings.go", "workerContextFindingSubjectRefMaxBytes", boundBytes)
	findingEvidenceRefCount := goConstantBound("internal/store/worker_context_findings.go", "workerContextFindingEvidenceRefMaxCount", boundItems)
	findingEvidenceRefMax := goConstantBound("internal/store/worker_context_findings.go", "workerContextFindingEvidenceRefMaxBytes", boundBytes)
	oracleReceiptsMax := goConstantBound("internal/store/worker_oracle_findings.go", "oracleRetainedReceiptsMax", boundItems)
	criterionBindingsMax := goConstantBound("internal/store/knowledge_manifest.go", "maxCriterionBindings", boundItems)
	observationWindow := goConstantBound("internal/store/workflow_continuity.go", "ContinuityObservationWindow", boundItems)
	bindingMaxLawAdditions := goConstantBound("internal/store/workflow_architecture_binding.go", "workflowArchitectureBindingMaxLawAdditions", boundItems)
	bindingMaxObligations := goConstantBound("internal/store/workflow_architecture_binding.go", "workflowArchitectureBindingMaxObligations", boundItems)

	// The closed evidence-kind enum: a declared slice whose members are
	// string constants, and the same typed constant vocabulary the shared
	// helper resolves. The slice helper counts the declared members; the
	// typed-vocabulary helper measures the longest member.
	evidenceKindSlice := goSliceBound("internal/store/workflow_registry.go", "workflowEvidenceKinds")
	evidenceKindLongest := pkTypedConstVocabularyLongestBound("EvidenceKind", "internal/store/workflow_registry.go")

	// The workflowList member guard: every list the fold admits passes
	// workflowString(value, 128) inside workflowList, so each member is at
	// most 128 bytes.
	workflowListMember := goCallBound("internal/store/workflow.go", "workflowList", "workflowString", "", 1, boundBytes)
	// Uniquely citable guard call: exactly one matching callee per function.
	outcomeSubjects := goCallBound("internal/store/workflow.go", "validateWorkflowOutcome", "workflowList", "", 1, boundItems)

	// Inline comparison guards: the helper selects the unambiguous upper
	// guard over the exact expression, evaluating its operand from live
	// source. workflowEvidenceRef uses the admission-predicate form
	// (len(value) >= 1 && len(value) <= 2048); every other site uses the
	// refusal form (expression > N beside a lower guard the helper never
	// selects).
	continuityPageLimit := goInlineBound("internal/store/workflow_continuity.go", "readWorkflowContinuityForRegistry", "req.Limit", boundItems)
	observationRefBound := goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "utf8.RuneCountInString(ref)", boundCharacters)
	observationTagBound := goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "utf8.RuneCountInString(tag)", boundCharacters)
	productIdentityBound := goInlineBound("internal/store/workflow_continuity.go", "readWorkflowContinuityForRegistry", "len(out.ProductIdentity)", boundItems)
	stepActionCount := goInlineBound("internal/store/workflow_registry.go", "ValidateWorkflowDefinition", "len(step.Actions)", boundItems)
	commitOIDBound := goInlineBound("internal/store/work_context.go", "workContextCommitOID", "len(oid)", boundBytes)
	drivingSessionCount := goInlineBound("internal/store/workpin.go", "scanWorkPinDrivingSessions", "len(sessions)", boundItems)
	evidenceRefBound := goInlineBound("internal/store/workflow.go", "workflowEvidenceRef", "len(value)", boundBytes)
	readPageLimit := goInlineBound("internal/store/workflow_read.go", "readWorkflowProjectionTx", "request.Limit", boundItems)
	distinguishFromCount := goInlineBound("internal/store/workflow.go", "validateWorkflowOutcome", "len(v.Distinguish)", boundItems)
	outcomeAllowedCount := goInlineBound("internal/store/workflow.go", "validateWorkflowOutcome", "len(v.Allowed)", boundItems)

	// Final full-string writer guards. The SQLite length(TEXT) CHECKs are
	// NUL-prefix constraints and never bounded the complete stored text, so
	// every projection column cites the Go admission guard its one fold
	// applies before writing: a workflowString or workflowList call argument
	// resolved through the fold's own body, the inline comparison the
	// validation helper carries, or the deterministic digest identity the
	// writer derives.
	contractRequiredEvidence := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContractApproved", "workflowList", "p.RequiredEvidence", 1, boundItems)
	contractRouteConventions := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContractApproved", "workflowList", "p.RouteConventions", 1, boundItems)
	contractSpecMandate := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContractApproved", "workflowList", "p.SpecMandate", 1, boundItems)
	contractLawModifies := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContractApproved", "workflowList", "p.LawModifies", 1, boundItems)
	actorRefDerived := workflowDigestRefCharactersBound("internal/store/workflow.go", "DeriveWorkflowActorRef")
	sessionRefGuard := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowActorRecorded", "workflowString", "p.SessionRef", 1, boundBytes)
	checkpointActiveUnit := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowString", "p.ActiveUnit", 1, boundBytes)
	checkpointHypothesis := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowString", "p.Hypothesis", 1, boundBytes)
	checkpointDiagnosis := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowString", "p.Diagnosis", 1, boundBytes)
	checkpointStrategy := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowString", "p.Strategy", 1, boundBytes)
	checkpointTouchedRefs := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowList", "p.TouchedRefs", 1, boundItems)
	checkpointEvidenceRefs := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowList", "p.EvidenceRefs", 1, boundItems)
	checkpointPendingQuestions := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowList", "p.PendingQuestions", 1, boundItems)
	checkpointPendingDecisions := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextCheckpointed", "workflowList", "p.PendingDecisions", 1, boundItems)
	boundarySummary := pkGuardArgBound("internal/store/workflow.go", "foldWorkflowContextBoundaryCrossed", "workflowString", "p.Summary", 1, boundBytes)
	observationStatementGuard := goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "utf8.RuneCountInString(statement)", boundCharacters)
	observationRefsCount := goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "len(refs)", boundItems)
	observationTagsCount := goInlineBound("internal/store/work_observations.go", "validateObservationBounds", "len(tags)", boundItems)
	designApproachGuard := goInlineBound("internal/store/workflow_design.go", "validateWorkflowDesignContent", "len(p.Approach)", boundBytes)
	designDecisionsCount := goInlineBound("internal/store/workflow_design.go", "validateWorkflowDesignContent", "len(p.Decisions)", boundItems)
	designTouchedRefsGuard := goCallBound("internal/store/workflow_design.go", "validateWorkflowDesignContent", "workflowList", "", 1, boundItems)
	proposalProblemGuard := pkGuardArgBound("internal/store/workflow.go", "validateWorkflowProposalContent", "validWorkflowProseItem", "content.Problem", 1, boundCharacters)
	proposalConstraintsCount := goInlineBound("internal/store/workflow.go", "validateWorkflowProposalContent", "len(content.Constraints)", boundItems)
	proposalUserOutcomesCount := goInlineBound("internal/store/workflow.go", "validateWorkflowProposalContent", "len(content.UserOutcomes)", boundItems)
	linearIssueKeyGuard := goInlineBound("internal/store/linear_issue_link.go", "validateLinearIssueLink", "len(link.HumanKey)", boundBytes)

	// Registry declaration guard: one work pin intent's required fields are
	// the Required subset of one action's declared payload, and the Payload
	// surface's declaration guard gates definition admission directly.
	// (workflow_registry.go:463; the sibling PublicPayload refusal at :472
	// is nil-check-conjoined and stays an honest named gap.)
	declaredPayloadFields := goInlineBound("internal/store/workflow_registry.go", "ValidateWorkflowDefinition", "len(action.Payload.Fields)", boundItems)

	// The approved contract's outcome predicates: the live approve_contract
	// write admits at most 8 through this action-input shape and the fold
	// re-admits the same 1..8 for replayed events (workflow.go:996-998);
	// the projection's ordinal CHECK is a bare integer comparison the DDL
	// parser cannot read.
	contractOutcomePredicates := maxBound(
		inputBound("/$defs/work_transition_action_variant_approve_contract/properties/fields/oneOf/0/properties/outcome_predicates/maxItems"),
		inputBound("/$defs/work_transition_action_variant_approve_contract/properties/fields/oneOf/1/properties/outcome_predicates/maxItems"))

	// The delivery assertion artifact writers. The asserted artifact is the
	// action request's delivery_artifact, admitted by the published input
	// node the record_delivery and accept_worker_result variants inline;
	// the correction's merge evidence is re-validated at the fold against
	// the published reference definition (validateMergeEvidenceAdmission,
	// internal/store/workflow_delivery_correction.go:368).
	deliveryArtifactInputs := maxBound(
		inputBound("/$defs/work_transition_action_variant_record_delivery/properties/fields/properties/delivery_artifact/maxLength"),
		inputBound("/$defs/work_transition_action_variant_accept_worker_result/properties/fields/properties/delivery_artifact/maxLength"),
	)
	publishedReference := schemaBound("contracts/agent-tool-surface-payloads.schema.json", "/$defs/reference/maxLength")

	// Published contract nodes outside the payload document's output roots.
	manifestRecordTitle := schemaBound("contracts/concord-knowledge-index.v1.schema.json", "/$defs/record/properties/title/maxLength")

	// Contract premise: the fold's single premise guard is
	// workflowString(p.Premise, WorkflowPremiseMaxLength) at
	// workflow.go:984; the constant lives in workflow_registry.go beside the
	// round-trip rule, outside the fold's const scope, so the row cites the
	// live constant and names the guard site.
	workflowPremiseMax := goConstantBound("internal/store/workflow_registry.go", "WorkflowPremiseMaxLength", boundBytes)
	manifestCriterionPredicate := schemaBound("contracts/concord-knowledge-index.v1.schema.json", "/$defs/criterionBinding/properties/predicate_id/maxLength")

	// Law context Domain identity: the manifest parse validates each
	// registry Domain record before prepareDomainProjection copies the
	// validated fields into the projection (ParseKnowledgeManifest ->
	// validateKnowledgeDomainRegistry -> validateKnowledgeDomainRecord,
	// knowledge_manifest.go:616,709 -> projection insert :502), so the
	// full-string guards are the writer chain's bounds.
	domainNameGuard := goInlineBound("internal/store/knowledge_manifest.go", "validateKnowledgeDomainRecord", "utf8.RuneCountInString(domain.Name)", boundCharacters)
	domainPurposeGuard := goInlineBound("internal/store/knowledge_manifest.go", "validateKnowledgeDomainRecord", "utf8.RuneCountInString(domain.Purpose)", boundCharacters)

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

	// Domain overlap: the detail lists are hard-truncated to the shared
	// detail constant with the truncation flag; recovery actions are the
	// declared four-element slice; overlap classes are one fixed class plus
	// three conditional single-element appends.
	registerOutputBound("/$defs/continuity_domain_overlap/properties/overlap_classes/maxItems",
		workflowAppendChainItemsBound("internal/store/workflow_domain_overlap.go", "workflowDomainOverlapPair", "classes"))
	registerOutputBound("/$defs/continuity_domain_overlap/properties/recovery_actions/maxItems",
		goSliceBound("internal/store/workflow_domain_overlap.go", "workflowOverlapRecoveryActions"))
	for _, detail := range []string{"shared_affected_domain_ids", "shared_domain_modifications", "shared_law_ids", "shared_relation_tuples"} {
		registerOutputBound("/$defs/continuity_domain_overlap/properties/"+detail+"/maxItems", overlapDetailItems)
	}

	// Operator question: both projections render the one fixed three-choice
	// composite literal at the single construction site.
	threeChoiceQuestion := workflowCompositeFieldItemsBound("internal/store/workflow_operator.go", "workflowOperatorQuestion", "Choices")
	registerOutputBound("/$defs/continuity_operator_decision/properties/choices/maxItems", threeChoiceQuestion)
	registerOutputBound("/$defs/operator_question/properties/choices/maxItems", threeChoiceQuestion)

	// Pending project handoff: the snapshot rides the fold-admitted record
	// whose validator holds the named constants.
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/artifact_refs/items/maxLength", handoffEntryMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/artifact_refs/maxItems", handoffListMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/blockers/items/maxLength", handoffEntryMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/blockers/maxItems", handoffListMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/bounded_job/maxLength", handoffJobMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/changes/items/maxLength", handoffEntryMax)
	registerOutputBound("/$defs/continuity_pending_project_handoff/properties/changes/maxItems", handoffListMax)
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
	registerOutputBound("/$defs/continuity_snapshot/properties/observations/items/properties/recorded_at/maxLength", observationRecordedAt)
	registerOutputBound("/$defs/continuity_snapshot/properties/observations/items/properties/refs/items/maxLength", observationRefBound)
	registerOutputBound("/$defs/continuity_snapshot/properties/observations/items/properties/refs/maxItems", observationRefsCount)
	registerOutputBound("/$defs/continuity_snapshot/properties/observations/items/properties/statement/maxLength", observationStatementGuard)
	registerOutputBound("/$defs/continuity_snapshot/properties/observations/items/properties/tags/items/maxLength", observationTagBound)
	registerOutputBound("/$defs/continuity_snapshot/properties/observations/items/properties/tags/maxItems", observationTagsCount)
	registerOutputBound("/$defs/continuity_snapshot/properties/observations/maxItems", observationWindow)
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/active_verify_leases/maxItems",
		workflowSQLLimitItemsBound("internal/store/worktrees.go", "heldWorktreeVerifyLeasesByOwnerTx"))
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/compatible_law_amendments/maxItems",
		contractSpecMandate) // at most one amendment per mandated law (workflow_law_revision.go:438-462)
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/product_identity/maxItems", productIdentityBound)
	// Genuinely unbounded: the ready worker-job array. The job-recorded fold
	// admits one row per event with no per-work job cap and the revision
	// read applies no LIMIT, and the byte fitter never shrinks this array.
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/ready_worker_jobs/maxItems",
		unboundedOutput("worker.job_recorded folds one revision row per recorded event with no per-work job cap (internal/store/worker_jobs.go:240-301), readWorkerJobRevisions reads every recorded revision with no LIMIT (internal/store/worker_jobs.go:520-527), and the continuity snapshot appends one packet per ready view (internal/store/workflow_continuity.go:515-526); FitContinuity shrinks only the observation window and the boundary page (internal/agent/runtime.go), so an over-envelope read refuses whole instead of clipping this array", readSizeRefusalPath))
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/spec_mandate/maxItems",
		contractSpecMandate)
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/step_actions/maxItems", stepActionCount)
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/unresolved_overlaps/maxItems", overlapDetailItems)
	registerOutputBound("/$defs/continuity_snapshot/properties/pinned/properties/workflow_step/maxLength", stepIDPattern)

	// Work context domain groups: one group per distinct Domain across the
	// readings and the finding slots; the partition members ride the view
	// bounds they partition.
	domainGroupDefs := []string{"work_context_domain_group", "work_context_domain_group2", "work_context_domain_group3"}
	for _, def := range domainGroupDefs {
		registerOutputBound("/$defs/"+def+"/properties/domain_id/maxLength", domainIDMaxBytes)
		registerOutputBound("/$defs/"+def+"/properties/finding_ids/maxItems", viewFindingsMax)
		registerOutputBound("/$defs/"+def+"/properties/required_reading_ordinals/maxItems", requiredReadingMax)
	}

	// Work context finding view: the shared report wire bounds both the
	// declaration and the worker-report origin.
	registerOutputBoundWithTwin("/$defs/work_context_finding_view/properties/domain_id/maxLength", domainIDMaxBytes)
	registerOutputBoundWithTwin("/$defs/work_context_finding_view/properties/evidence_refs/items/maxLength", findingEvidenceRefMax)
	registerOutputBoundWithTwin("/$defs/work_context_finding_view/properties/evidence_refs/maxItems", findingEvidenceRefCount)
	registerOutputBoundWithTwin("/$defs/work_context_finding_view/properties/product_wide_rationale/maxLength", rationaleMaxBytes)
	registerRetainedTwinBound("/$defs/work_context_finding_view/properties/source_event_id/maxLength", eventIDRaw)
	registerOutputBoundWithTwin("/$defs/work_context_finding_view/properties/statement/maxLength", findingStatementMax)
	registerOutputBoundWithTwin("/$defs/work_context_finding_view/properties/subject_ref/maxLength", findingSubjectMax)

	// Required readings and their closed source union.
	registerOutputBound("/$defs/work_context_reading/properties/domain_id/maxLength", domainIDMaxBytes)
	registerOutputBound("/$defs/work_context_reading/properties/product_wide_rationale/maxLength", rationaleMaxBytes)
	registerOutputBound("/$defs/work_context_reading/properties/reason/maxLength", readingReasonMaxBytes)
	registerOutputBound("/$defs/work_context_reading_source_knowledge/properties/law_id/maxLength", lawRefMaxBytes)
	registerOutputBound("/$defs/work_context_reading_source_knowledge/properties/source_id/maxLength", projectRefMaxBytes)
	registerOutputBound("/$defs/work_context_reading_source_repository_file/properties/path/maxLength", repoPathMaxBytes)
	registerOutputBound("/$defs/work_context_reading_source_repository_file/properties/project_id/maxLength", projectRefMaxBytes)

	// Research finding embed: the ids ride the admitted selection shape the
	// declaration fold re-admits with ValidReference; the statement, the
	// citation list, and the linked source descriptors are unbounded at the
	// store: the research write checks presence only and the CLI route
	// applies no schema, so the resolved view embeds them verbatim.
	registerOutputBoundWithTwin("/$defs/work_context_research_finding/properties/finding_id/maxLength",
		inputBound("/$defs/work_context_research_finding/properties/finding_id/maxLength"))
	registerOutputBoundWithTwin("/$defs/work_context_research_finding/properties/pack_id/maxLength",
		inputBound("/$defs/work_context_research_finding/properties/pack_id/maxLength"))
	registerRetainedTwinBound("/$defs/work_context_research_finding/properties/source_ids/items/maxLength",
		unboundedOutput("the research write checks source_id non-empty only (internal/store/research_operations.go:133-138) and active_research_sources CHECKs length(source_id) > 0 only (internal/store/schema.go:645); the resolved work-context view embeds the finding's source ids verbatim (internal/store/work_context_research.go:125-129)", readSizeRefusalPath, mutationResultPath))
	registerRetainedTwinBound("/$defs/work_context_research_finding/properties/source_ids/maxItems",
		unboundedOutput("the finding citation loop admits any number of source_ids, foreign-key-checked per row only (internal/store/research_mutations.go:474-478), with no count cap on active_research_finding_sources; the view embeds the recorded citation list verbatim", readSizeRefusalPath, mutationResultPath))
	registerRetainedTwinBound("/$defs/work_context_research_finding/properties/statement/maxLength",
		unboundedOutput("the research write checks the finding statement non-empty only (internal/store/research_operations.go:127-132) and active_research_findings CHECKs length(statement) > 0 only (internal/store/schema.go:630); store admission carries no upper text guard, and the projection rebuild snapshot/restore (internal/store/research_rebuild.go:18,44) plus copyResearchRevisionContent (internal/store/research_operations.go:364) carry existing rows forward without revalidation, so the resolved view embeds the recorded statement verbatim (internal/store/work_context_research.go:124)", readSizeRefusalPath, mutationResultPath))

	// Research finding view: both oneOf arms carry the same provenance
	// derivations, so one wildcard row covers the repeated arms; only the
	// resolved arm carries sources. The view id is the fixed
	// research:<seq>:<ordinal> Sprintf construction.
	for _, arm := range []string{"0", "1"} {
		registerRetainedTwinBound("/$defs/work_context_research_finding_view/oneOf/"+arm+"/properties/selection_event_id/maxLength", eventIDRaw)
	}
	registerOutputBoundWithTwin("/$defs/work_context_research_finding_view/oneOf/*/properties/view_id/maxLength",
		workflowSprintfFieldCharactersBound("internal/store/work_context_research.go", "readWorkContextResearchSelections", "ViewID"))
	registerRetainedTwinBound("/$defs/work_context_research_finding_view/oneOf/0/properties/sources/maxItems",
		unboundedOutput("revision sources are unbounded rows of active_research_sources per pack and revision with non-empty CHECKs only (internal/store/schema.go:633-650), and the resolved view embeds them verbatim (internal/store/work_context_research.go:125-129)", readSizeRefusalPath, mutationResultPath))

	// Research selection: the declaration's admitted shape, re-admitted by
	// the fold with ValidReference at the same numbers.
	registerOutputBound("/$defs/work_context_research_selection/properties/domain_id/maxLength", domainIDMaxBytes)
	registerOutputBound("/$defs/work_context_research_selection/properties/finding_id/maxLength",
		inputBound("/$defs/work_context_research_selection/properties/finding_id/maxLength"))
	registerOutputBound("/$defs/work_context_research_selection/properties/pack_id/maxLength",
		inputBound("/$defs/work_context_research_selection/properties/pack_id/maxLength"))
	registerOutputBound("/$defs/work_context_research_selection/properties/product_wide_rationale/maxLength",
		inputBound("/$defs/work_context_research_selection/properties/product_wide_rationale/maxLength"))

	// Work context view: the read parents' retained twin and the two packet
	// variants render one shared assembly, so each row cites the same
	// source; the unversioned view is output only through its twin. The subject
	// commit rides the inline 40..64 lowercase-hex guard.
	viewDefs := []string{"retained_work_context_view", "work_context_view2", "work_context_view3"}
	for _, def := range viewDefs {
		registerOutputBound("/$defs/"+def+"/properties/domain_groups/maxItems",
			sumBound(requiredReadingMax, viewFindingsMax)) // one group per distinct Domain across <=32 readings + <=32 finding slots
		registerOutputBound("/$defs/"+def+"/properties/findings/maxItems", viewFindingsMax)
		registerOutputBound("/$defs/"+def+"/properties/oracle_preparations/maxItems",
			pkProductBound(
				goConstantBound("internal/store/worker_oracle.go", "OracleControlsMax", boundItems),
				goConstantBound("internal/store/worker_oracle.go", "OracleReadinessRefsMax", boundItems),
			)) // one deduplicated preparation per readiness reference across the selected job's controls (readSelectedOraclePreparations); the cross-control dedup only shrinks
		registerOutputBound("/$defs/"+def+"/properties/oracle_receipts/maxItems", oracleReceiptsMax)
		registerOutputBound("/$defs/"+def+"/properties/required_reading/maxItems", requiredReadingMax)
		registerOutputBound("/$defs/"+def+"/properties/subject_commit/maxLength", commitOIDBound)
	}
	registerOutputBound("/$defs/retained_work_context_view/properties/research_findings/maxItems", viewFindingsMax)
	registerOutputBound("/$defs/work_context_view3/properties/research_findings/maxItems", viewFindingsMax)

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
	registerOutputBound("/$defs/work_pin/allOf/0/then/properties/next_valid_intents/maxItems",
		workflowEmptyAssignmentItemsBound("internal/store/workpin.go", "workPinOutsideRepairDispositionTx", "pin.NextValidIntents"))
	registerOutputBound("/$defs/work_pin/properties/driving_sessions/maxItems", drivingSessionCount)
	registerOutputBound("/$defs/work_pin/properties/linear_issue_key/maxLength", linearIssueKeyGuard)
	registerOutputBound("/$defs/work_pin/properties/next_valid_intents/items/properties/required_fields/maxItems",
		declaredPayloadFields) // one intent's required fields are the Required subset of one action's declared payload; the sibling PublicPayload refusal (workflow_registry.go:472) sits inside a nil-check conjunction the inline verifier cannot confirm and is named in the report
	registerOutputBound("/$defs/work_pin/properties/next_valid_intents/maxItems",
		workflowIntentChainItemsBound(
			goInlineBound("internal/store/workflow_registry.go", "ValidateWorkflowDefinition", "len(step.Actions)", boundItems),
			[]string{"workPinIntents", "workPinWithoutAction", "workPinEscalatedRetryIntents"},
			workflowIntentChainSite{file: "internal/store/workpin.go", function: "workPinIntents", target: "intents", baseLoopOK: true},
			workflowIntentChainSite{file: "internal/store/workpin.go", function: "workPinStepIntentsTx", target: "pin.NextValidIntents"},
			workflowIntentChainSite{file: "internal/store/workpin.go", function: "workPinRecoveryIntentsTx", target: "pin.NextValidIntents"},
			workflowIntentChainSite{file: "internal/store/workpin.go", function: "workPinCorrectionRequestIntentsTx", target: "pin.NextValidIntents"},
		)) // the step-action loop is the guard-capped base; the recovery, correction, and checkpoint routes each append at most one fixed intent; the outside-repair path rebuilds the pin to one intent (workpin.go:256)
	registerOutputBound("/$defs/work_pin/properties/obligations/items/maxLength", evidenceKindLongest)
	registerOutputBound("/$defs/work_pin/properties/obligations/maxItems", evidenceKindSlice)
	registerOutputBound("/$defs/work_pin/properties/outside_repair_route/maxItems",
		workflowReturnSliceItemsBound("internal/store/outside_repair.go", "outsideRepairRouteNames"))
	registerOutputBound("/$defs/work_pin/properties/outstanding_evidence_kinds/maxItems", evidenceKindSlice)
	registerOutputBound("/$defs/work_pin/properties/step/maxLength", stepIDPattern)
	registerOutputBound("/$defs/work_pin/properties/verdict_evidence/maxItems",
		workflowSQLLimitItemsBound("internal/store/workpin.go", "workPinVerdictEvidenceTx"))
	registerOutputBound("/$defs/work_pin/properties/verified_criteria/maxItems", contractOutcomePredicates) // one criterion per approved contract predicate
	registerOutputBound("/$defs/work_pin/properties/workflow_type/maxLength", workflowRefPattern)

	// Work pin evidence: the evidence_bound fold's inline closed vocabulary
	// and the shared evidence reference guard.
	registerOutputBound("/$defs/work_pin_evidence/properties/evidence_kind/maxLength",
		workflowContainsLiteralLongestBound("internal/store/workflow.go", "foldWorkflowEvidenceBound", "slices.Contains"))
	registerOutputBound("/$defs/work_pin_evidence/properties/immutable_subject_ref/maxLength", evidenceRefBound)

	// Work size summary.
	registerOutputBound("/$defs/work_size_summary/properties/recorded_at/maxLength", sizeRecordedAt)

	// Workflow contract: the projection columns carry the admission CHECKs.
	registerOutputBound("/$defs/workflow_contract/properties/approved_at/maxLength", contractApprovedAt)
	registerOutputBound("/$defs/workflow_contract/properties/approved_by/maxLength",
		actorRefDerived) // event.Actor is the workflow_actors row the fold requires
	// The decision fact enters only through the approve and supersede
	// fields, which a consequence_v1 pin requires and a legacy pin refuses;
	// the fold re-admits the same canonical set
	// (validateWorkflowDecisionDomains) for replayed events.
	registerOutputBound("/$defs/workflow_contract/properties/decision_domains/maxItems", maxBound(
		inputBound("/$defs/work_transition_action_variant_approve_contract/properties/fields/oneOf/1/properties/decision_domains/maxItems"),
		inputBound("/$defs/work_transition_action_variant_supersede_contract/properties/fields/oneOf/1/properties/decision_domains/maxItems")))
	registerOutputBound("/$defs/workflow_contract/properties/law_modifies/maxItems", contractLawModifies)
	registerOutputBound("/$defs/workflow_contract/properties/law_revisions/maxItems",
		contractSpecMandate) // pins correspond one-to-one with spec_mandate (workflow_law_revision.go:165-167)
	registerOutputBound("/$defs/workflow_contract/properties/outcome_predicates/items/properties/outcome_payload/maxLength",
		goConstantBound("internal/store/workflow_outcome.go", "maxWorkflowPredicateBytes", boundBytes)) // the fold stores the raw admitted predicate text (workflow.go:1097), whose only length guard is DecodeWorkflowPredicate's 64 KiB input refusal (workflow_outcome.go:74)
	registerOutputBound("/$defs/workflow_contract/properties/outcome_predicates/maxItems", contractOutcomePredicates)
	registerOutputBound("/$defs/workflow_contract/properties/required_evidence/maxItems", contractRequiredEvidence)
	registerOutputBound("/$defs/workflow_contract/properties/route_conventions/maxItems", contractRouteConventions)
	registerOutputBound("/$defs/workflow_contract/properties/spec_mandate/maxItems", contractSpecMandate)

	// Correction context: strategy, predicate ids, and failure kind ride the
	// admitted request shape the fold re-admits at the same numbers; the
	// diagnosis and failure detail read worker_attempts.failure_detail,
	// whose terminal-at-birth dispatch writer is unguarded.
	registerRetainedTwinBound("/$defs/workflow_correction_context/properties/diagnosis/maxLength",
		unboundedOutput("two writers feed the correction diagnosis: the request_correction fold admits correction_diagnosis at 4096 bytes (internal/store/workflow.go:2189), but the record_worker_failure diagnosis fallback copies worker_attempts.failure_detail verbatim (internal/store/workflow_correction.go:1607-1611) and the terminal-at-birth worker.dispatched path folds payload.terminal_detail into that column unchecked (internal/store/worker_lanes.go:833-854 has no terminal_detail check) while the column carries no length CHECK", readSizeRefusalPath, mutationResultPath))
	registerRetainedTwinBound("/$defs/workflow_correction_context/properties/evidence_refs/maxItems",
		sumBound(
			pkGuardArgBound("internal/store/workflow.go", "validateWorkflowActionCompletedShape", "workflowList", "p.CorrectionEvidenceRefs", 1, boundItems),
			goConstantBound("internal/store/workflow.go", "workflowOperationEvidenceRefBound", boundItems),
		)) // the read unions the fold-admitted correction refs (workflowList 32, rejected at workflow.go:2189) with the result refs (workflowOperationEvidenceRefBound 32, workflow.go:675); a rejected correction can carry both groups, and correction_evidence_refs is written for reject_worker_result and request_correction completions only (workflow_action_guards.go:1688)
	registerRetainedTwinBound("/$defs/workflow_correction_context/properties/failure_detail/maxLength",
		unboundedOutput("the correction read copies worker_attempts.failure_detail verbatim (internal/store/workflow_correction.go:1601-1605); the worker.failed report path bounds Detail at 4096 bytes (internal/store/worker_lanes.go:807), but the terminal-at-birth worker.dispatched path folds payload.terminal_detail into the column unchecked (internal/store/worker_lanes.go:833-854) and the column carries no length CHECK", readSizeRefusalPath, mutationResultPath))
	registerOutputBoundWithTwin("/$defs/workflow_correction_context/properties/failure_kind/maxLength",
		inputBound("/$defs/workflow_correction_context/properties/failure_kind/maxLength")) // the writer admits only the typed failure-kind vocabulary, tighter than this bound
	registerOutputBoundWithTwin("/$defs/workflow_correction_context/properties/predicate_ids/maxItems",
		inputBound("/$defs/workflow_correction_context/properties/predicate_ids/maxItems"))
	registerOutputBoundWithTwin("/$defs/workflow_correction_context/properties/strategy/maxLength",
		inputBound("/$defs/workflow_correction_context/properties/strategy/maxLength"))

	// Delivery assertion: the asserted artifact rides the action input's
	// published delivery_artifact node; the effective artifact also covers
	// the correction's merge evidence, re-validated against the published
	// reference rule.
	registerOutputBound("/$defs/workflow_delivery_assertion/properties/artifact/maxLength", deliveryArtifactInputs)
	registerOutputBound("/$defs/workflow_delivery_assertion/properties/effective_artifact/maxLength",
		maxBound(deliveryArtifactInputs, publishedReference))

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
	registerOutputBoundWithTwin("/$defs/workflow_law_context/properties/domains/maxItems",
		workflowLawContextDomainsItemsBound())
	registerOutputBoundWithTwin("/$defs/workflow_law_context/properties/laws/maxItems",
		sumBound(contractSpecMandate, bindingMaxLawAdditions, bindingMaxObligations)) // the 128-entry ceiling is the documented write-side sum (workflow_law_revision.go:941-943)
	registerRetainedTwinBound("/$defs/workflow_law_context/properties/registry_path/maxLength",
		unboundedOutput("knowledgeRegistryLocator joins the knowledge home's canonical-path locator with one fixed shard file name (internal/store/workflow_law_revision.go:836,849); the home locator's normalized value derives through normalizePath, whose symlink resolution expands the path to filesystem-target text with no length guard (internal/store/project_locator.go:154-161), so the joined locator is byte-ceiling only", readSizeRefusalPath, mutationResultPath))
	registerOutputBound("/$defs/workflow_law_context_criterion/properties/predicate_id/maxLength",
		manifestCriterionPredicate) // the manifest contract bounds criterion predicate ids with a closed pattern
	// Law context Domain identity: the full-string guards sit upstream of
	// the projection in the manifest parse chain (knowledge_manifest.go:
	// 616,709,727); the insert at knowledge_index_projection.go:502 copies
	// the already-validated fields, and rebuilds re-read the same validated
	// registry.
	registerOutputBound("/$defs/workflow_law_context_domain/properties/name/maxLength", domainNameGuard)
	registerOutputBound("/$defs/workflow_law_context_domain/properties/purpose/maxLength", domainPurposeGuard)
	registerRetainedTwinBound("/$defs/workflow_law_context_law/properties/criteria/maxItems",
		criterionBindingsMax) // the law's criterion bindings filtered to this work; the store admits up to 1000 bindings per law record
	registerOutputBoundWithTwin("/$defs/workflow_law_context_law/properties/obligation_ids/maxItems",
		bindingMaxObligations) // the law's obligations are its subset of the binding's verification obligations
	// Genuinely unbounded: the law file locator joins the knowledge home's
	// normalized canonical-path locator, whose value derives through
	// normalizePath — NUL is refused, but symlink resolution expands the
	// path to filesystem-target text no code length bounds — with the
	// record path.
	registerRetainedTwinBound("/$defs/workflow_law_context_law/properties/path/maxLength",
		unboundedOutput("the law locator joins the knowledge home's canonical-path locator, whose normalized value derives through normalizePath: NUL is refused (internal/store/project_locator.go:150-152) but filepath.EvalSymlinks expands symlinked ancestors to filesystem-target text with no length guard (internal/store/project_locator.go:154-161), so the joined locator is byte-ceiling only", readSizeRefusalPath, mutationResultPath))
	registerOutputBoundWithTwin("/$defs/workflow_law_context_law/properties/roles/maxItems",
		workflowRoleMarkItemsBound("internal/store/workflow_law_revision.go", "readWorkflowLawContext", "markRole"))
	registerOutputBoundWithTwin("/$defs/workflow_law_context_law/properties/title/maxLength",
		manifestRecordTitle) // the projection copies the manifest record title verbatim

	// Outcome payloads: subjects ride the fold's list guard and its member
	// bound; the surface and check fields ride the admitted input shape the
	// fold re-admits at the same numbers; the absent distinguish_from list
	// rides the fold's inline comparison guard.
	// The live outcome union reaches output only inside a dispatched worker
	// packet's predicates, which dispatch admission validates against it.
	registerOutputBound("/$defs/workflow_outcome_outcome/properties/allowed/maxItems",
		inputBound("/$defs/workflow_outcome_outcome/properties/allowed/maxItems"))
	registerOutputBound("/$defs/workflow_outcome_absent/properties/distinguish_from/maxItems", distinguishFromCount)
	registerOutputBound("/$defs/workflow_outcome_absent/properties/subjects/maxItems", outcomeSubjects)
	registerOutputBound("/$defs/workflow_outcome_check/properties/check_ref/maxLength",
		inputBound("/$defs/workflow_outcome_check/properties/check_ref/maxLength"))
	registerOutputBound("/$defs/workflow_outcome_check/properties/immutable_subject_ref/maxLength",
		inputBound("/$defs/workflow_outcome_check/properties/immutable_subject_ref/maxLength"))
	registerOutputBound("/$defs/workflow_outcome_exists/properties/subjects/maxItems", outcomeSubjects)
	registerOutputBound("/$defs/workflow_outcome_subject/maxLength", workflowListMember)
	registerOutputBound("/$defs/workflow_outcome_surface/maxLength",
		inputBound("/$defs/workflow_outcome_surface/maxLength"))

	// Retained outcome payloads: the output surfaces replay stored
	// predicates unchanged, so the allowed list rides the retained output
	// def, not the narrowed live authoring def, whose allowed array is
	// input-only once every output use is separated. The fold re-admits the
	// same 1..8 list it enforced at approval (workflow.go:4364).
	registerOutputBound("/$defs/workflow_retained_outcome/properties/allowed/maxItems", outcomeAllowedCount)

	// Retained decision record: the enforcing guards are the inline
	// validateDecisionRecord comparisons and the anchored actor pattern
	// (internal/store/workflow_outcome.go). Element text and the supersedes
	// fields carry no field guard; their one finite ceiling is the
	// whole-predicate decode refusal maxWorkflowPredicateBytes, because a
	// serialized input of N bytes decodes to at most N code points.
	predicateDecodeCeiling := goConstantBound("internal/store/workflow_outcome.go", "maxWorkflowPredicateBytes", boundBytes)
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/consequences/maxItems",
		goInlineBound("internal/store/workflow_outcome.go", "validateDecisionRecord", "len(record.Consequences)", boundItems))
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/consequences/items/maxLength", predicateDecodeCeiling)
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/inputs/maxItems",
		goInlineBound("internal/store/workflow_outcome.go", "validateDecisionRecord", "len(record.Inputs)", boundItems))
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/inputs/items/maxLength", predicateDecodeCeiling)
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/options_considered/maxItems",
		goInlineBound("internal/store/workflow_outcome.go", "validateDecisionRecord", "len(record.OptionsConsidered)", boundItems))
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/options_considered/items/maxLength", predicateDecodeCeiling)
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/poc_findings/maxLength",
		goInlineBound("internal/store/workflow_outcome.go", "validateDecisionRecord", "len(record.POCFindings)", boundBytes))
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/question/maxLength",
		goInlineBound("internal/store/workflow_outcome.go", "validateDecisionRecord", "len(record.Question)", boundBytes))
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/rationale/maxLength",
		goInlineBound("internal/store/workflow_outcome.go", "validateDecisionRecord", "len(record.Rationale)", boundBytes))
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/required_to_decide/maxItems",
		goInlineBound("internal/store/workflow_outcome.go", "validateDecisionRecord", "len(record.RequiredToDecide)", boundItems))
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/required_to_decide/items/maxLength", predicateDecodeCeiling)
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/reviewer_actor_ref/maxLength",
		goPatternBound("internal/store/workflow_outcome.go", "workflowActorRefPattern"))
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/supersedes/maxLength", predicateDecodeCeiling)
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/superseded_by/maxLength", predicateDecodeCeiling)
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/unknowns/maxItems",
		goInlineBound("internal/store/workflow_outcome.go", "validateDecisionRecord", "len(record.Unknowns)", boundItems))
	registerOutputBound("/$defs/workflow_retained_decision_record/properties/unknowns/items/maxLength", predicateDecodeCeiling)

	// Contract premise: the fold's admission guard is
	// workflowString(p.Premise, WorkflowPremiseMaxLength); the cited
	// constant is that guard's capacity, live-read from its declaration.
	registerOutputBound("/$defs/workflow_premise/maxLength", workflowPremiseMax)

	// Proposal record: the projection columns carry the admission CHECKs;
	// the per-item prose rides the admitted record_proposal item shape the
	// fold re-admits with the same rune counts (workflow.go:414-424).
	registerOutputBound("/$defs/workflow_proposal_record/properties/constraints/items/maxLength",
		inputBound("/$defs/workflow_proposal_record/properties/constraints/items/maxLength"))
	registerOutputBound("/$defs/workflow_proposal_record/properties/constraints/maxItems", proposalConstraintsCount)
	registerOutputBound("/$defs/workflow_proposal_record/properties/problem/maxLength", proposalProblemGuard)
	registerOutputBound("/$defs/workflow_proposal_record/properties/user_outcomes/items/maxLength",
		inputBound("/$defs/workflow_proposal_record/properties/user_outcomes/items/maxLength"))
	registerOutputBound("/$defs/workflow_proposal_record/properties/user_outcomes/maxItems", proposalUserOutcomesCount)

	// Workflow read: the ready derivation and the warning union are the
	// genuinely unbounded arrays; the LIMIT-guarded
	// arrays share one raw read-limit comparison.
	registerOutputBound("/$defs/workflow_read/properties/blocking_conditions/maxItems",
		unboundedOutput("deriveWorkflowReady scans every open workflow_external_conditions row with no LIMIT (internal/store/workflow_conditions.go:21) and appends one blocking entry per durable_operation-owned condition (internal/store/workflow_conditions.go:53), while the add_condition fold admits one condition per call with per-field byte caps only (internal/store/workflow.go:3906-3927), so the open-condition count is uncapped", readSizeRefusalPath))
	registerOutputBound("/$defs/workflow_read/properties/candidate_ids/maxItems", readPageLimit)
	registerOutputBound("/$defs/workflow_read/properties/completion_warnings/maxItems",
		unboundedOutput("workflowRecordedStalenessWarnings returns one warning per distinct drifted staleness_rule_id across admitted workflow.staleness_observed events (internal/store/workflow_completion.go:842-855); the rule id is caller text bounded only at 128 bytes per entry (internal/store/workflow.go:3426), the dedup removes only exact duplicates (internal/store/workflow_read.go:439-450), and no count cap exists on the union", readSizeRefusalPath))
	registerOutputBound("/$defs/workflow_read/properties/conditions/maxItems", readPageLimit)
	registerOutputBound("/$defs/workflow_read/properties/impact_notices/maxItems", readPageLimit)
	registerOutputBound("/$defs/workflow_read/properties/stale_law_revision/properties/recovery_actions/maxItems",
		workflowSplitConstItemsBound("internal/store/workflow_law_revision.go", "staleLawRecoveryActions", ","))
	registerOutputBound("/$defs/workflow_read/properties/unreadable_conditions/maxItems",
		unboundedOutput("deriveWorkflowReady scans every open workflow_external_conditions row with no LIMIT (internal/store/workflow_conditions.go:21) and appends one unknown entry per condition whose authority is not a durable_operation of this work (internal/store/workflow_conditions.go:44-52), while the add_condition fold admits one condition per call with per-field byte caps only (internal/store/workflow.go:3906-3927), so the open-condition count is uncapped", readSizeRefusalPath))
	registerOutputBound("/$defs/workflow_read/properties/unresolved_conditions/maxItems", readPageLimit)

	// Self-repair: the shape validator's own constant.
	registerOutputBound("/$defs/workflow_self_repair/properties/evidence_refs/maxItems",
		goConstantBound("internal/store/workflow_self_repair.go", "workflowSelfRepairMaxEvidence", boundItems))
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

// workflowReturnSliceItemsBound counts the elements of the one slice
// composite literal a named function returns.
func workflowReturnSliceItemsBound(file, function string) outputBoundSource {
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
		if len(returns) != 1 || len(returns[0].Results) != 1 {
			return evaluatedBound{}, fmt.Errorf("workflow return slice %s:%s must carry exactly one return with one result", file, function)
		}
		lit, ok := returns[0].Results[0].(*ast.CompositeLit)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("workflow return slice %s:%s returns a non-composite value", file, function)
		}
		if _, ok := lit.Type.(*ast.ArrayType); !ok {
			return evaluatedBound{}, fmt.Errorf("workflow return slice %s:%s returns a non-slice composite", file, function)
		}
		from := fmt.Sprintf("workflow construction %s:%s returns a %d-element slice literal", file, function, len(lit.Elts))
		return workflowItemsEvaluated(int64(len(lit.Elts)), from), nil
	})
}

// workflowEmptyAssignmentItemsBound proves a target is assigned exactly one
// empty composite literal and never appended to, so the produced array is
// empty on every execution of the named function.
func workflowEmptyAssignmentItemsBound(file, function, target string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		assignments, appends := 0, 0
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
					lit, ok := rhs.(*ast.CompositeLit)
					if !ok {
						refusal = fmt.Errorf("workflow empty assignment %s:%s assigns %s from a non-composite value", file, function, target)
						return false
					}
					if len(lit.Elts) != 0 {
						refusal = fmt.Errorf("workflow empty assignment %s:%s assigns %s a non-empty composite", file, function, target)
						return false
					}
					assignments++
				}
			case *ast.CallExpr:
				if ledgerCalledName(node.Fun) == "append" && len(node.Args) > 0 && workflowExprText(node.Args[0]) == target {
					appends++
				}
			}
			return true
		})
		if refusal != nil {
			return evaluatedBound{}, refusal
		}
		if assignments != 1 || appends != 0 {
			return evaluatedBound{}, fmt.Errorf("workflow empty assignment %s:%s assigns %s %d times with %d appends; the empty construction is not the only writer", file, function, target, assignments, appends)
		}
		from := fmt.Sprintf("workflow construction %s:%s assigns %s the empty composite", file, function, target)
		return workflowItemsEvaluated(0, from), nil
	})
}

// workflowRefuseFieldWriters refuses every direct writer of a struct field
// outside the one construction the evaluator verified: an assignment whose
// target names the field, or an append into it. A second writer would grow
// the emitted value past the counted construction.
func workflowRefuseFieldWriters(fn *ast.FuncDecl, field string) error {
	var refusal error
	ast.Inspect(fn, func(n ast.Node) bool {
		if refusal != nil {
			return false
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				text := workflowExprText(lhs)
				if text == field || strings.HasSuffix(text, "."+field) {
					refusal = fmt.Errorf("workflow construction assigns %s outside the verified literal", text)
					return false
				}
			}
		case *ast.CallExpr:
			if ledgerCalledName(node.Fun) == "append" && len(node.Args) > 0 {
				text := workflowExprText(node.Args[0])
				if text == field || strings.HasSuffix(text, "."+field) {
					refusal = fmt.Errorf("workflow construction appends into %s outside the verified literal", text)
					return false
				}
			}
		}
		return true
	})
	return refusal
}

// workflowCompositeFieldItemsBound counts the elements of one composite
// literal a named function builds under a struct-literal field key. The
// field must have no other writer in the function: a selector assignment or
// an append into the field refuses.
func workflowCompositeFieldItemsBound(file, function, field string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		if err := workflowRefuseFieldWriters(fn, field); err != nil {
			return evaluatedBound{}, fmt.Errorf("workflow composite field %s:%s: %w", file, function, err)
		}
		matches := 0
		elements := 0
		ast.Inspect(fn, func(n ast.Node) bool {
			entry, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := entry.Key.(*ast.Ident)
			if !ok || key.Name != field {
				return true
			}
			lit, ok := entry.Value.(*ast.CompositeLit)
			if !ok {
				return true
			}
			matches++
			elements = len(lit.Elts)
			return true
		})
		if matches != 1 {
			return evaluatedBound{}, fmt.Errorf("workflow composite field %s:%s builds %s %d times; the construction is not unique", file, function, field, matches)
		}
		from := fmt.Sprintf("workflow construction %s:%s builds %s as a %d-element composite literal", file, function, field, elements)
		return workflowItemsEvaluated(int64(elements), from), nil
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

// workflowSprintfFieldCharactersBound measures one fmt.Sprintf construction
// a named function builds under a struct-literal field key: the format's
// fixed literal width plus workflowInt64Slot per %d verb — each %d token
// itself prints nothing — a sound over-approximation of any int64 the verbs
// can render. The field must have no other writer in the function.
func workflowSprintfFieldCharactersBound(file, function, field string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		if err := workflowRefuseFieldWriters(fn, field); err != nil {
			return evaluatedBound{}, fmt.Errorf("workflow sprintf field %s:%s: %w", file, function, err)
		}
		matches := 0
		characters := int64(0)
		ast.Inspect(fn, func(n ast.Node) bool {
			entry, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := entry.Key.(*ast.Ident)
			if !ok || key.Name != field {
				return true
			}
			call, ok := entry.Value.(*ast.CallExpr)
			if !ok || !ledgerCalleeMatches(call, "Sprintf") || len(call.Args) == 0 {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			format, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			verbs, literals, err := workflowFormatDigitVerbs(format)
			if err != nil {
				return true
			}
			matches++
			characters = int64(literals) + int64(verbs)*workflowInt64Slot
			return true
		})
		if matches != 1 {
			return evaluatedBound{}, fmt.Errorf("workflow sprintf field %s:%s builds %s %d times from a literal format; the construction is not unique", file, function, field, matches)
		}
		from := fmt.Sprintf("workflow construction %s:%s renders %s from one literal Sprintf format at %d characters", file, function, field, characters)
		return workflowCharactersEvaluated(characters, from), nil
	})
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

// workflowContainsLiteralLongestBound measures the longest string literal in
// the one callee call of a named function that carries a slice-of-string-
// literals argument: the closed vocabulary the call admits.
func workflowContainsLiteralLongestBound(file, function, callee string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		var calls []*ast.CallExpr
		ast.Inspect(fn, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && ledgerCalleeMatches(call, callee) {
				calls = append(calls, call)
			}
			return true
		})
		if len(calls) != 1 {
			return evaluatedBound{}, fmt.Errorf("workflow contains vocabulary %s:%s carries %d %s calls; the citation is ambiguous", file, function, len(calls), callee)
		}
		found := false
		longest := int64(0)
		for _, arg := range calls[0].Args {
			lit, ok := arg.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range lit.Elts {
				str, ok := elt.(*ast.BasicLit)
				if !ok || str.Kind != token.STRING {
					return evaluatedBound{}, fmt.Errorf("workflow contains vocabulary %s:%s carries a non-literal member", file, function)
				}
				value, err := strconv.Unquote(str.Value)
				if err != nil {
					return evaluatedBound{}, fmt.Errorf("workflow contains vocabulary %s:%s carries an unparsable member: %w", file, function, err)
				}
				if r := int64(utf8.RuneCountInString(value)); r > longest {
					longest = r
				}
				found = true
			}
		}
		if !found {
			return evaluatedBound{}, fmt.Errorf("workflow contains vocabulary %s:%s carries no string-literal vocabulary", file, function)
		}
		from := fmt.Sprintf("workflow contains vocabulary %s:%s %s longest member is %d characters", file, function, callee, longest)
		return workflowCharactersEvaluated(longest, from), nil
	})
}

// workflowSQLLimitItemsBound reads the row cap one read query applies: the
// named function must carry exactly one Query* call whose arguments carry
// exactly one plain SQL string literal containing exactly one word-boundary
// LIMIT followed by a decimal number. Parameterized or repeated LIMIT
// clauses refuse.
func workflowSQLLimitItemsBound(file, function string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		_, fn, err := workflowFunctionDecl(rt, file, function)
		if err != nil {
			return evaluatedBound{}, err
		}
		isQuery := func(name string) bool {
			if dot := strings.LastIndex(name, "."); dot >= 0 {
				name = name[dot+1:]
			}
			switch name {
			case "Query", "QueryRow", "QueryContext", "QueryRowContext":
				return true
			}
			return false
		}
		var calls []*ast.CallExpr
		ast.Inspect(fn, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isQuery(ledgerCalledName(call.Fun)) {
				calls = append(calls, call)
			}
			return true
		})
		if len(calls) != 1 || len(calls[0].Args) == 0 {
			return evaluatedBound{}, fmt.Errorf("workflow SQL limit %s:%s must carry exactly one query call", file, function)
		}
		var sqlLiterals []*ast.BasicLit
		for _, arg := range calls[0].Args {
			if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				sqlLiterals = append(sqlLiterals, lit)
			}
		}
		if len(sqlLiterals) != 1 {
			return evaluatedBound{}, fmt.Errorf("workflow SQL limit %s:%s query call carries %d SQL string literals; the query text is absent or ambiguous", file, function, len(sqlLiterals))
		}
		sql, err := strconv.Unquote(sqlLiterals[0].Value)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("workflow SQL limit %s:%s query text is unparsable: %w", file, function, err)
		}
		locations := regexp.MustCompile(`(?i)\bLIMIT\b`).FindAllStringIndex(sql, -1)
		if len(locations) != 1 {
			return evaluatedBound{}, fmt.Errorf("workflow SQL limit %s:%s carries %d LIMIT clauses; the citation is ambiguous", file, function, len(locations))
		}
		tail := strings.TrimSpace(sql[locations[0][1]:])
		value, err := strconv.ParseInt(tail, 10, 64)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("workflow SQL limit %s:%s LIMIT operand %q is not a decimal number", file, function, tail)
		}
		from := fmt.Sprintf("workflow SQL limit %s:%s query applies LIMIT %d", file, function, value)
		return workflowItemsEvaluated(value, from), nil
	})
}

// workflowSplitConstItemsBound counts the segments of one declared string
// constant after splitting on a separator: the fixed list a writer derives
// from the constant.
func workflowSplitConstItemsBound(file, constName, separator string) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		goS, err := rt.loadGo(file)
		if err != nil {
			return evaluatedBound{}, err
		}
		consts, err := rt.ledgerConstScope(goS, nil)
		if err != nil {
			return evaluatedBound{}, err
		}
		def, ok := consts[constName]
		if !ok {
			return evaluatedBound{}, fmt.Errorf("workflow split const %s:%s resolves to no constant", file, constName)
		}
		lit, ok := def.expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return evaluatedBound{}, fmt.Errorf("workflow split const %s:%s is not a string literal", file, constName)
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("workflow split const %s:%s is unparsable: %w", file, constName, err)
		}
		parts := strings.Split(value, separator)
		for _, part := range parts {
			if strings.TrimSpace(part) == "" {
				return evaluatedBound{}, fmt.Errorf("workflow split const %s:%s carries an empty segment", file, constName)
			}
		}
		from := fmt.Sprintf("workflow split const %s:%s splits into %d segments", file, constName, len(parts))
		return workflowItemsEvaluated(int64(len(parts)), from), nil
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

// workflowIntentChainSite is one function of a work-pin intent chain: the
// target the chain builds, plus whether the site's loop append is the
// guard-covered base production rather than a multiplying site.
type workflowIntentChainSite struct {
	file       string
	function   string
	target     string
	baseLoopOK bool
}

// workflowIntentChainItemsBound bounds a pin intent list built across a
// chain of functions: the base source covers the guard-capped step-action
// loop, and every other append site must add one fixed element outside any
// loop. Assignments may replace the target with a listed relabel-or-filter
// call (the base builder, a subset filter, or a one-to-one relabel, each
// verified by reading) or with a same-function alias copy of an admission
// filter; every other assignment shape refuses. The bound is the base plus
// every fixed append, raised to the widest replacement literal.
func workflowIntentChainItemsBound(base outputBoundSource, relabels []string, sites ...workflowIntentChainSite) outputBoundSource {
	return workflowDerived(func(rt *ledgerRuntime, census *boundCensus) (evaluatedBound, error) {
		baseEvaluated, err := rt.evaluateSource(base, census)
		if err != nil {
			return evaluatedBound{}, fmt.Errorf("workflow intent chain: %w", err)
		}
		baseItems, ok := baseEvaluated.capacity(boundItems)
		if !ok {
			return evaluatedBound{}, fmt.Errorf("workflow intent chain: the base guard has no item capacity")
		}
		allowed := map[string]bool{}
		for _, name := range relabels {
			allowed[name] = true
		}
		total := baseItems
		totalFixed := int64(0)
		maxAssigned := int64(0)
		for _, site := range sites {
			siteGo, fn, err := workflowFunctionDecl(rt, site.file, site.function)
			if err != nil {
				return evaluatedBound{}, err
			}
			loopNodes := workflowLoopBodyNodes(fn)
			fixed, loopAppends := 0, 0
			var refusal error
			ast.Inspect(fn, func(n ast.Node) bool {
				if refusal != nil {
					return false
				}
				switch node := n.(type) {
				case *ast.AssignStmt:
					for i, rhs := range node.Rhs {
						if i >= len(node.Lhs) || workflowExprText(node.Lhs[i]) != site.target {
							continue
						}
						switch value := rhs.(type) {
						case *ast.CompositeLit:
							if int64(len(value.Elts)) > maxAssigned {
								maxAssigned = int64(len(value.Elts))
							}
						case *ast.CallExpr:
							if ledgerCalledName(value.Fun) == "append" {
								continue // counted at the call site below
							}
							if ledgerCalledName(value.Fun) == "make" {
								// A zero-length slice construction: make
								// never carries elements, and a length
								// argument other than the zero literal
								// refuses.
								if len(value.Args) != 3 {
									refusal = fmt.Errorf("workflow intent chain %s:%s constructs %s with make in an unsupported shape", site.file, site.function, site.target)
									return false
								}
								length, evalErr := rt.evalNumeric(siteGo, fn, value.Args[1])
								if evalErr != nil || length != 0 {
									refusal = fmt.Errorf("workflow intent chain %s:%s constructs %s with make length %v", site.file, site.function, site.target, value.Args[1])
									return false
								}
								break
							}
							if !allowed[ledgerCalledName(value.Fun)] {
								refusal = fmt.Errorf("workflow intent chain %s:%s assigns %s from unlisted call %s", site.file, site.function, site.target, ledgerCalledName(value.Fun))
								return false
							}
						case *ast.Ident:
							// A same-function alias copy of an admission
							// filter; the subset relationship is the chain's
							// traced responsibility and never grows the list.
						default:
							refusal = fmt.Errorf("workflow intent chain %s:%s assigns %s from an unsupported shape %T", site.file, site.function, site.target, rhs)
							return false
						}
					}
				case *ast.CallExpr:
					if ledgerCalledName(node.Fun) != "append" || len(node.Args) == 0 || workflowExprText(node.Args[0]) != site.target {
						return true
					}
					if loopNodes[node] {
						if !site.baseLoopOK {
							refusal = fmt.Errorf("workflow intent chain %s:%s appends %s inside a loop", site.file, site.function, site.target)
							return false
						}
						loopAppends++
						return true
					}
					if len(node.Args) != 2 || node.Ellipsis != token.NoPos {
						refusal = fmt.Errorf("workflow intent chain %s:%s appends %s with a variadic or multi-element shape", site.file, site.function, site.target)
						return false
					}
					fixed++
				}
				return true
			})
			if refusal != nil {
				return evaluatedBound{}, refusal
			}
			expectedLoop := 0
			if site.baseLoopOK {
				expectedLoop = 1
			}
			if loopAppends != expectedLoop {
				return evaluatedBound{}, fmt.Errorf("workflow intent chain %s:%s carries %d guard-covered loop appends for %s; the base citation changed", site.file, site.function, loopAppends, site.target)
			}
			totalFixed += int64(fixed)
		}
		total += totalFixed
		if maxAssigned > total {
			total = maxAssigned
		}
		from := fmt.Sprintf("workflow intent chain: base %d plus %d fixed single-element appends, at most %d via replacement literals", baseItems, totalFixed, maxAssigned)
		return workflowItemsEvaluated(total, from), nil
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

// TestWorkflowCompositeFieldSecondWriter pins the second-writer refusal on
// synthetic sources: the verified literal alone passes, and an append, a
// selector assignment, or a duplicated key refuses.
func TestWorkflowCompositeFieldSecondWriter(t *testing.T) {
	t.Parallel()
	const good = `package store

type choice struct{ ID string }

type question struct{ Choices []choice }

func build() *question {
	return &question{Choices: []choice{{ID: "confirm"}, {ID: "revise"}, {ID: "stop"}}}
}
`
	const appendWriter = `package store

type choice struct{ ID string }

type question struct{ Choices []choice }

func build() *question {
	out := &question{Choices: []choice{{ID: "confirm"}, {ID: "revise"}}}
	out.Choices = append(out.Choices, choice{ID: "stop"})
	return out
}
`
	const assignWriter = `package store

type choice struct{ ID string }

type question struct{ Choices []choice }

func build() *question {
	out := &question{Choices: []choice{{ID: "confirm"}}}
	out.Choices = []choice{{ID: "revise"}, {ID: "stop"}, {ID: "more"}}
	return out
}
`
	const duplicateKey = `package store

type choice struct{ ID string }

type question struct{ Choices []choice }

func build() *question {
	out := &question{Choices: []choice{{ID: "confirm"}}}
	return &question{Choices: []choice{{ID: "confirm"}, {ID: "revise"}, {ID: "stop"}}}
}
`
	cases := []struct {
		name    string
		source  string
		want    int64
		wantErr bool
	}{
		{name: "verified literal passes", source: good, want: 3},
		{name: "append second writer refuses", source: appendWriter, wantErr: true},
		{name: "selector assignment second writer refuses", source: assignWriter, wantErr: true},
		{name: "duplicated field key refuses", source: duplicateKey, wantErr: true},
	}
	for _, tc := range cases {
		witness := newLedgerRuntime(ledgerRepoRoot(t))
		if err := witness.parseGoSource("witness.go", tc.source); err != nil {
			t.Fatalf("%s: synthetic source does not parse: %v", tc.name, err)
		}
		evaluated, err := witness.evaluateSource(workflowCompositeFieldItemsBound("witness.go", "build", "Choices"), nil)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected the second writer to refuse, got %+v", tc.name, evaluated)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(evaluated.constraints) != 1 || evaluated.constraints[0].value != tc.want {
			t.Fatalf("%s: evaluated %+v, want one %d-item constraint", tc.name, evaluated.constraints, tc.want)
		}
	}
}

// TestWorkflowSprintfFieldSecondWriter pins the second-writer refusal for the
// Sprintf field measure: the single literal-format construction passes and a
// selector assignment refuses.
func TestWorkflowSprintfFieldSecondWriter(t *testing.T) {
	t.Parallel()
	const good = `package store

type view struct{ ViewID string }

func build(seq int64, ordinal int) []view {
	out := make([]view, 0, 4)
	out = append(out, view{ViewID: fmt.Sprintf("research:%d:%d", seq, ordinal)})
	return out
}
`
	const assignWriter = `package store

type view struct{ ViewID string }

func build(seq int64, ordinal int) []view {
	entry := view{ViewID: fmt.Sprintf("research:%d:%d", seq, ordinal)}
	entry.ViewID = fmt.Sprintf("research:%d:%d", seq, ordinal)
	return []view{entry}
}
`
	// The render width derives from the actual format "research:%d:%d":
	// the %d tokens render nothing, so the fixed literal text is
	// "research:" plus the one separating colon = 10 runes, and each of the
	// 2 slots renders at most one signed int64 (workflowInt64Slot).
	formatLiteral := "research:" + ":"
	formatSlots := 2
	want := int64(len(formatLiteral)) + int64(formatSlots)*workflowInt64Slot
	if len(formatLiteral) != 10 || formatSlots != 2 {
		t.Fatalf("format arithmetic drifted: literal %q, slots %d", formatLiteral, formatSlots)
	}
	cases := []struct {
		name    string
		source  string
		want    int64
		wantErr bool
	}{
		{name: "verified sprintf passes", source: good, want: want},
		{name: "selector assignment second writer refuses", source: assignWriter, wantErr: true},
	}
	for _, tc := range cases {
		witness := newLedgerRuntime(ledgerRepoRoot(t))
		if err := witness.parseGoSource("witness.go", tc.source); err != nil {
			t.Fatalf("%s: synthetic source does not parse: %v", tc.name, err)
		}
		evaluated, err := witness.evaluateSource(workflowSprintfFieldCharactersBound("witness.go", "build", "ViewID"), nil)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected the second writer to refuse, got %+v", tc.name, evaluated)
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
