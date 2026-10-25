import type { AgentLaneReportReview, AgentLaneReportReviewFinding } from "./dispatch"

// Synthetic report claims for schema/transport tests. They grant no subject or
// receipt authority; store integration fixtures must supply observed identities.
export function reviewFindingFixture(overrides: Partial<AgentLaneReportReviewFinding> = {}): AgentLaneReportReviewFinding {
  return { finding_id: "finding:boundary", severity: "P1", confidence: "high", detail: "the bound is not checked at the boundary",
    in_scope: true, blocking: true, status: "open", evidence_refs: [], closure_refs: [], ...overrides }
}

export function reviewBlockFixture(overrides: Partial<AgentLaneReportReview> = {}): AgentLaneReportReview {
  const candidate = { project_id: "project-1", commit: "a".repeat(40), tree: "b".repeat(40) }
  return { disposition: "correction_required", input_candidate: candidate, output_candidate: { ...candidate },
    dependencies: { criteria: [{ predicate_id: "predicate:primary", content_digest: "sha256:" + "c".repeat(64) }], architecture_not_applicable: true },
    assessments: [], findings: [reviewFindingFixture()], remaining_obligations: [], ...overrides }
}
