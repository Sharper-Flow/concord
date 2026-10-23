package store

// The review-state reader copies terminal review content and accepted-action
// attribution from domain_events. These are producer guards, not the output
// schema's own caps; a retained value can exceed a newer report admission.
func init() {
	const (
		lanes        = "internal/store/worker_lanes.go"
		oracle       = "internal/store/worker_oracle_findings.go"
		reviewShape  = "validateWorkerReviewBlock"
		reportSchema = "contracts/agent-lane-report.schema.json"
		finding      = "/$defs/lane_report_oracle_finding/properties/"
		lawBinding   = "/$defs/lane_report_oracle_law_binding/properties/"
		resolved     = "/$defs/lane_report_resolved_finding/properties/"
		candidate    = "/$defs/lane_report_review_candidate/properties/"
		dependencies = "/$defs/lane_report_review_dependencies/properties/"
		obligation   = "/$defs/lane_report_review_obligation/properties/"
		accepted     = "/$defs/workflow_review_state/properties/accepted_review/anyOf/1/properties/"
	)
	reference := goInlineBound("internal/store/workflow_registry.go", "ValidReference", "len(value)", boundBytes)
	predicate := goPatternBound("internal/store/worker_jobs.go", "workerJobPredicatePattern")
	sha := goPatternBound("internal/store/worktrees.go", "worktreeSHAPattern")
	evidenceRef := goConstantBound(oracle, "oracleFindingEvidenceRefBytes", boundBytes)
	evidenceCount := goConstantBound(oracle, "oracleFindingEvidenceRefsMax", boundItems)
	entryPoint := goConstantBound(oracle, "oracleFindingEntryPointMaxBytes", boundBytes)
	clause := goConstantBound("internal/store/worker_oracle.go", "oracleDescriptionMaxBytes", boundBytes)

	// Case and control references join the dispatched oracle even for a
	// follow_up (validateWorkerOracleFindingJoins). Its identities entered
	// through the recorded job's authoring shape. A follow_up owner does not
	// join: only its store pattern bounds it, which may exceed the output cap.
	for _, row := range []struct{ member, definition, id, constant string }{
		{"case_ids", "worker_oracle_case", "case_id", "oracleFindingCaseMax"},
		{"control_ids", "worker_oracle_control", "control_id", "oracleFindingControlMax"},
	} {
		registerOutputBound(finding+row.member+"/items/maxLength",
			inputBound("/$defs/"+row.definition+"/properties/"+row.id+"/maxLength"))
		registerOutputBound(finding+row.member+"/maxItems", goConstantBound(oracle, row.constant, boundItems))
	}
	registerOutputBound(finding+"owner_id/maxLength", goPatternBound("internal/store/worker_oracle.go", "oracleOwnerIDPattern"))
	registerOutputBound(finding+"failure_family/maxLength", goConstantBound(oracle, "oracleFindingFamilyMaxBytes", boundBytes))
	registerOutputBound(finding+"predicate_ids/items/maxLength", goInlineBound(lanes, "validWorkerPredicateID", "len(id)", boundBytes))
	registerOutputBound(finding+"predicate_ids/maxItems", goConstantBound(oracle, "oracleFindingPredicateMax", boundItems))
	registerOutputBound(finding+"law_bindings/maxItems", goConstantBound(oracle, "oracleFindingLawBindingsMax", boundItems))
	registerOutputBound(finding+"evidence_refs/maxItems", evidenceCount)
	for _, keyword := range []string{"maxLength", "x-maxBytes"} {
		registerOutputBound(finding+"entry_point/"+keyword, entryPoint)
		registerOutputBound(finding+"evidence_refs/items/"+keyword, evidenceRef)
		registerOutputBound(lawBinding+"clause/"+keyword, clause)
		registerOutputBound(resolved+"evidence_refs/items/"+keyword, evidenceRef)
	}
	// Lineage and resolution joins require an existing ranked finding of this
	// work, so the core's identity formatter, not the unbounded digits in the
	// report's reference grammar, owns the maximum.
	for _, member := range []string{"continues_finding_id", "variant_of"} {
		registerOutputBound(finding+member+"/maxLength", workerRankedFindingIDBound())
	}
	registerOutputBound(resolved+"finding_id/maxLength", workerRankedFindingIDBound())
	registerOutputBound(resolved+"evidence_refs/maxItems", evidenceCount)
	registerOutputBound(lawBinding+"source/properties/source_id/maxLength", goConstantBound("internal/store/worker_oracle.go", "oracleProjectIDMaxBytes", boundBytes))
	registerOutputBound(lawBinding+"source/properties/law_id/maxLength", goConstantBound("internal/store/work_context.go", "workContextLawIDMaxBytes", boundBytes))
	registerOutputBound(lawBinding+"source/properties/content_hash/maxLength", goInlineBound("internal/store/workflow.go", "workflowDigest", "len(value)", boundBytes))

	// The terminal payload validator enforces these lists and scalar guards
	// before append/replay; accepted reports retain those values unchanged.
	for _, row := range []struct{ member, measurement string }{
		{"assessments", "len(review.Assessments)"},
		{"findings", "len(review.Findings)"},
		{"remaining_obligations", "len(review.RemainingObligations)"},
	} {
		registerOutputBound("/$defs/lane_report_review_block/properties/"+row.member+"/maxItems", goInlineBound(lanes, reviewShape, row.measurement, boundItems))
	}
	registerOutputBound("/$defs/lane_report_review_block/properties/resolved_findings/maxItems", goConstantBound(oracle, "oracleResolutionsMax", boundItems))
	registerOutputBound(candidate+"project_id/maxLength", reference)
	registerOutputBound(candidate+"commit/maxLength", sha)
	registerOutputBound(candidate+"tree/maxLength", sha)
	registerOutputBound(dependencies+"criteria/maxItems", goInlineBound(lanes, reviewShape, "len(review.Dependencies.Criteria)", boundItems))
	registerOutputBound(dependencies+"criteria/items/properties/predicate_id/maxLength", predicate)
	// Digest claims enter through the host's validateAgentLaneReport input
	// schema, not these output definitions. The store's validDigest guard
	// also requires exactly a SHA-256 hex digest with an optional prefix;
	// core-derived dependencies use reviewDigest/nativeDigest, no wider.
	for _, member := range []string{"criteria/items/properties/content_digest", "architecture_digest"} {
		registerOutputBound(dependencies+member+"/maxLength",
			schemaBound(reportSchema, "/$defs/review_dependencies/properties/"+member+"/maxLength"))
	}
	registerOutputBound("/$defs/lane_report_review_assessment/properties/predicate_id/maxLength", predicate)
	for _, member := range []string{"obligation_id", "owner_ref", "subject_ref"} {
		registerOutputBound(obligation+member+"/maxLength", reference)
	}
	registerOutputBound(obligation+"input_digest/maxLength", schemaBound(reportSchema, "/$defs/review_obligation/properties/input_digest/maxLength"))
	neededProof := goInlineBound(lanes, reviewShape, "len(obligation.NeededProof)", boundBytes)
	detail := goInlineBound(lanes, "validateWorkerReviewFinding", "len(finding.Detail)", boundBytes)
	for _, keyword := range []string{"maxLength", "x-maxBytes"} {
		registerOutputBound(obligation+"needed_proof/"+keyword, neededProof)
		registerOutputBound("/$defs/lane_report_review_finding/properties/detail/"+keyword, detail)
		registerOutputBound("/$defs/lane_report_review_refs/items/"+keyword, reference)
	}
	registerOutputBound("/$defs/lane_report_review_finding/properties/finding_id/maxLength", reference)
	registerOutputBound("/$defs/lane_report_review_refs/maxItems", maxBound(
		goInlineBound(lanes, reviewShape, "len(assessment.ReceiptRefs)", boundItems),
		goInlineBound(lanes, "validateWorkerReviewFinding", "len(finding.EvidenceRefs)", boundItems),
		goInlineBound(lanes, "validateWorkerReviewFinding", "len(finding.ClosureRefs)", boundItems),
	))

	// The action fold requires its actor to equal the recorded actor identity.
	// report_ref instead copies the terminal event id; Event.validate checks
	// presence only, so no finite producer bound can justify its output cap.
	registerOutputBound(accepted+"actor_ref/maxLength", workflowDigestRefCharactersBound("internal/store/workflow.go", "DeriveWorkflowActorRef"))
	registerOutputBound(accepted+"report_ref/maxLength", unboundedOutput(
		"loadWorkflowReviewState (internal/store/workflow_review.go) copies the worker.completed domain_events.event_id into accepted_review.report_ref unchanged; Event.validate (internal/store/events.go) checks only presence, and domain_events.event_id has no length CHECK",
		ledgerReadRefusalPath, ledgerMutationRefusalPath))
}
