package store

// Review results retain the coordinator's admitted report and action identity.
// CD-0242 D5-D6 removes worker envelopes, not these subject-bound results.
func init() {
	const (
		review        = "internal/store/review.go"
		oracle        = "internal/store/acceptance_oracle.go"
		finding       = "/$defs/lane_report_oracle_finding/properties/"
		retained      = "/$defs/retained_worker_oracle_finding/properties/"
		lawBinding    = "/$defs/lane_report_oracle_law_binding/properties/"
		resolved      = "/$defs/lane_report_resolved_finding/properties/"
		candidate     = "/$defs/lane_report_review_candidate/properties/"
		candidateView = "/$defs/workflow_review_candidate_view/properties/"
		dependencies  = "/$defs/lane_report_review_dependencies/properties/"
		obligation    = "/$defs/lane_report_review_obligation/properties/"
		accepted      = "/$defs/workflow_review_state/properties/accepted_review/anyOf/1/properties/"
	)
	reference := goInlineBound("internal/store/workflow_registry.go", "ValidReference", "len(value)", boundBytes)
	predicate := goPatternBound(review, "reviewPredicatePattern")
	sha := goPatternBound("internal/store/worktree_reads.go", "worktreeSHAPattern")
	evidenceRef := goInlineBound(review, "validateReviewOracleFinding", "len(ref)", boundBytes)
	evidenceCount := goInlineBound(review, "validateReviewOracleFinding", "len(finding.EvidenceRefs)", boundItems)
	entryPoint := goInlineBound(review, "validateReviewOracleFinding", "len(finding.EntryPoint)", boundBytes)
	clause := goConstantBound(oracle, "oracleDescriptionMaxBytes", boundBytes)
	lineage := workflowSprintfFieldCharactersBound("internal/store/work_context_review.go", "rankedFindingView", "FindingID")
	for _, prefix := range []string{finding, retained} {
		for _, member := range []string{"case_ids", "control_ids", "predicate_ids"} {
			registerOutputBound(prefix+member+"/items/maxLength", inputBound(finding+member+"/items/maxLength"))
			registerOutputBound(prefix+member+"/maxItems", goInlineBound(review, "validateReviewOracleFinding", "len(group.values)", boundItems))
		}
		registerOutputBound(prefix+"owner_id/maxLength", goPatternBound(oracle, "oracleOwnerIDPattern"))
		registerOutputBound(prefix+"failure_family/maxLength", goInlineBound(review, "validateReviewOracleFinding", "len(finding.FailureFamily)", boundBytes))
		registerOutputBound(prefix+"law_bindings/maxItems", goInlineBound(review, "validateReviewOracleFinding", "len(finding.LawBindings)", boundItems))
		registerOutputBound(prefix+"evidence_refs/maxItems", evidenceCount)
		registerOutputBound(prefix+"evidence_refs/items/maxLength", evidenceRef)
		registerOutputBound(prefix+"entry_point/maxLength", entryPoint)
		for _, member := range []string{"continues_finding_id", "variant_of"} {
			registerOutputBound(prefix+member+"/maxLength", lineage)
		}
	}
	registerOutputBound(finding+"entry_point/x-maxBytes", entryPoint)
	registerOutputBound(finding+"evidence_refs/items/x-maxBytes", evidenceRef)
	for _, keyword := range []string{"maxLength", "x-maxBytes"} {
		registerOutputBound(lawBinding+"clause/"+keyword, clause)
		registerOutputBound(resolved+"evidence_refs/items/"+keyword, goInlineBound(review, "validateReviewResolutions", "len(ref)", boundBytes))
	}
	registerOutputBound(resolved+"finding_id/maxLength", lineage)
	registerOutputBound(resolved+"evidence_refs/maxItems", goInlineBound(review, "validateReviewResolutions", "len(resolution.EvidenceRefs)", boundItems))
	registerOutputBound(resolved+"judged_case_ids/maxItems", goInlineBound(review, "validateReviewResolutions", "len(resolution.JudgedCaseIDs)", boundItems))
	registerOutputBound(resolved+"judged_case_ids/items/maxLength", goInlineBound(review, "validateReviewResolutions", "len(id)", boundBytes))
	registerOutputBound(lawBinding+"source/properties/source_id/maxLength", goConstantBound(oracle, "oracleProjectIDMaxBytes", boundBytes))
	registerOutputBound(lawBinding+"source/properties/law_id/maxLength", goConstantBound("internal/store/work_context.go", "workContextLawIDMaxBytes", boundBytes))
	registerOutputBound(lawBinding+"source/properties/content_hash/maxLength", goInlineBound("internal/store/workflow.go", "workflowDigest", "len(value)", boundBytes))
	registerOutputBound("/$defs/worker_oracle_law_binding/properties/clause/maxLength", clause)
	for _, row := range []struct{ member, measurement string }{
		{"assessments", "len(review.Assessments)"},
		{"findings", "len(review.Findings)"},
		{"remaining_obligations", "len(review.RemainingObligations)"},
	} {
		registerOutputBound("/$defs/workflow_review_report/properties/"+row.member+"/maxItems", goInlineBound(review, "validateReviewBlock", row.measurement, boundItems))
	}
	registerOutputBound("/$defs/workflow_review_report/properties/resolved_findings/maxItems", goInlineBound(review, "validateReviewResolutions", "len(resolutions)", boundItems))
	for _, prefix := range []string{candidate, candidateView} {
		registerOutputBound(prefix+"project_id/maxLength", reference)
		registerOutputBound(prefix+"commit/maxLength", sha)
		registerOutputBound(prefix+"tree/maxLength", sha)
	}
	registerOutputBound(dependencies+"criteria/maxItems", goInlineBound(review, "validateReviewAssessments", "len(deps.Criteria)", boundItems))
	registerOutputBound(dependencies+"criteria/items/properties/predicate_id/maxLength", predicate)
	for _, member := range []string{"criteria/items/properties/content_digest", "architecture_digest"} {
		registerOutputBound(dependencies+member+"/maxLength", inputBound(dependencies+member+"/maxLength"))
	}
	registerOutputBound("/$defs/lane_report_review_assessment/properties/predicate_id/maxLength", predicate)
	for _, member := range []string{"obligation_id", "owner_ref", "subject_ref"} {
		registerOutputBound(obligation+member+"/maxLength", reference)
	}
	registerOutputBound(obligation+"input_digest/maxLength", inputBound(obligation+"input_digest/maxLength"))
	neededProof := goInlineBound(review, "validateReviewBlock", "len(obligation.NeededProof)", boundBytes)
	detail := goInlineBound(review, "validateReviewFinding", "len(finding.Detail)", boundBytes)
	for _, keyword := range []string{"maxLength", "x-maxBytes"} {
		registerOutputBound(obligation+"needed_proof/"+keyword, neededProof)
		registerOutputBound("/$defs/lane_report_review_finding/properties/detail/"+keyword, detail)
		registerOutputBound("/$defs/workflow_review_unidentified_finding/properties/detail/"+keyword, detail)
		registerOutputBound("/$defs/lane_report_review_refs/items/"+keyword, reference)
	}
	registerOutputBound("/$defs/lane_report_review_finding/properties/finding_id/maxLength", reference)
	registerOutputBound("/$defs/lane_report_review_refs/maxItems", maxBound(
		goInlineBound(review, "validateReviewAssessments", "len(assessment.ReceiptRefs)", boundItems),
		goInlineBound(review, "validateReviewFinding", "len(finding.EvidenceRefs)", boundItems),
		goInlineBound(review, "validateReviewFinding", "len(finding.ClosureRefs)", boundItems),
	))
	registerOutputBound(accepted+"actor_ref/maxLength", workflowDigestRefCharactersBound("internal/store/workflow.go", "DeriveWorkflowActorRef"))
	registerOutputBound(accepted+"report_ref/maxLength", unboundedOutput(
		"accepted Review retains the action receipt identity; raw Event.validate checks event id presence, not length",
		ledgerReadRefusalPath, ledgerMutationRefusalPath))
}
