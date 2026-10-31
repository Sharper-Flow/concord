# CD-0129: Investigation gates premise confirmation, open questions gate contract approval

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** The two workflow surfaces that carry an investigation
  obligation: coordinator premise confirmation, and contract approval while a
  plan holds recorded open questions
- **Related:** CD-0030, CD-0113, and issues #903, #1010
- **Preserves:** Contract authority, architecture binding at approval,
  evidence provenance, and the recorded facts the store keeps
  ([CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2)

## Context

A coordinator can spend an operator decision on a question its own
investigation should settle. An operator question must not spend a decision
on an investigation the agent can settle.

## Decision

### D1. Premise confirmation requires investigation

`confirm_premise` requires a recorded investigation artifact and the acceptance
deliverables. It records the authenticated coordinator, with no semantic choice
or human decision-context digest. Operator decisions follow CD-0243 in
conversation; the step machine projects no operator question or withholding.
The question-projection and selection requirements and tests are retired.

### D2. An artifact names what it compared against

An investigation artifact is a work observation whose refs resolve in the
projections they name: one ref is a current Domain of the item's Product in
the Domain registry. When the Product holds another work item, one ref names
a work item other than the artifact's own. A ref that names nothing cannot satisfy the obligation, and the
artifact's own work id never counts, because the observation query is
already keyed by it.

### D3. Open questions require bound research at contract approval

`approve_contract` refuses while the item's latest durable context
checkpoint records pending questions and no research pack revision is bound
to the item. The trigger is recorded state that `checkpoint_context`
already requires, not a planner judgement at gate time, because a planner
that decides research is unnecessary would otherwise pass unchecked.
Contract approval admits when the questions close or a revision binds.

### D4. Architecture stays where it already was

The architecture obligation stays at approval for Product-changing work,
where the complete architecture binding is already refused when absent.
This record adds no approval gate for architecture.

## Verification

```text
go test ./internal/store/ -run 'TestRecordedInvestigationArtifactRequiresResolvableDomainAndWorkRefs|TestPendingQuestionsRequireBoundResearchRevision|TestEngineeringConfirmation'
go test ./internal/store/ ./internal/agent/...
cd adapter/opencode && bun test dispatch_route_end_to_end
```
