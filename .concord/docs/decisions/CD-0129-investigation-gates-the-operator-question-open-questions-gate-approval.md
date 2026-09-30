# CD-0129: Investigation gates the operator question, open questions gate approval

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** The two workflow surfaces that carry an investigation
  obligation: the pending operator decision, and contract approval while a
  plan holds recorded open questions
- **Approval:** The operator approved the contract in Concord
  work-ef019d2c52d7241977e90ef1 on 2026-09-09, and chose the operator
  question as the obligated surface.
- **Related:** CD-0030, CD-0113, CD-0117, and issues #903, #1010
- **Amends:** Nothing. This record adds the obligation the earlier
  investigation contract omitted.
- **Preserves:** Contract authority, architecture binding at approval,
  event history, evidence provenance, and released definition pins

## Context

A coordinator could spend an operator decision on a question its own
investigation should have settled. No workflow surface obligated that
investigation. The first attempt at this law gated `approve_contract` on a
recorded investigation artifact, and its ref grammar matched nothing in the
Product: it demanded refs prefixed `domain:` while Domain registry ids are
bare, and it demanded the item's own work id while the observation query is
already keyed by it (issue #1010). Architecture was already mandatory at
approval for Product-changing work, so a second approval gate refused every
contract in the Product without adding an investigation.

## Decision

### D1. The operator question is the obligated surface

`workflowOperatorQuestionTx` is the single path that produces a pending
operator decision. It withholds the question while the work item carries no
recorded investigation artifact. The question returns when one exists. The
gate binds to no single action: every approval-required question the path
produces takes it.

### D2. An artifact names what it compared against

An investigation artifact is a work observation whose refs resolve in the
projections they name: one ref is a current Domain of the item's Product in
the Domain registry, and one ref is a work item other than the artifact's
own. A ref that names nothing cannot satisfy the obligation, and the
artifact's own work id never counts, because the observation query is
already keyed by it.

### D3. Open questions require bound research at approval

`approve_contract` refuses while the item's latest durable context
checkpoint records pending questions and no research pack revision is bound
to the item. The trigger is recorded state that `checkpoint_context`
already requires, not a planner judgement at gate time, because a planner
that decides research is unnecessary would otherwise pass unchecked.
Approval admits when the questions close or a revision binds.

### D4. Architecture stays where it already was

The architecture obligation stays at approval for Product-changing work,
where the complete architecture binding is already refused when absent.
This record adds no approval gate for architecture.

## Verification

```text
go test ./internal/store/ -run 'TestOperatorQuestionWithheldForAnyApprovalRequiredAction|TestRecordedInvestigationArtifactRequiresResolvableDomainAndWorkRefs|TestPendingQuestionsRequireBoundResearchRevision'
go test ./internal/store/ ./internal/agent/...
cd adapter/opencode && bun test dispatch_route_end_to_end
```

All green on the delivery branch. The three adapter version-skew failures
reproduce on `main` unchanged and are out of scope.
