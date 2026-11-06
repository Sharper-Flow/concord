---
{
  "id": "CD-0012",
  "title": "Bind stated goals to delivered outcomes",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": []
}
---
# CD-0012: Bind Stated Goals to Delivered Outcomes

**Status:** Accepted
**Date:** 2026-08-08
**Decision owner:** Operator
**Accepted by operator:** 2026-08-08
**Reviewer:** Independent review; three rounds, approved.
**Scope:** Stated goals, independent Review, discovered work and revision authority.
**Amends:** CD-0006 D5 and D10 while preserving the spec mandate.
**Amended:** 2026-10-18. [CD-0236](CD-0236-vacuity-is-judged-at-review.md) places vacuity judgment at Review.
[CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D5 amends
D7: Main's review loop replaces a distinct final evaluator. [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
D2 and D14 govern current state and generated definitions:
the store keeps current state in current rows, and no historical pins are retained.
**Research:** [`R5-goal-to-outcome-binding.md`](https://github.com/Sharper-Flow/concord/blob/a98dfe5db3fa2af53bd2d82829aec840e94e64c4/.concord/docs/research/R5-goal-to-outcome-binding.md)
**Issue:** <https://github.com/Sharper-Flow/concord/issues/22>

## Context

[`priorities.md`](../constitution/priorities.md) §2 names *intent fidelity* — "the delivered outcome
matches the stated intent" — and *no silent drift* as two of Concord's nine quality
attributes. Neither has a mechanism. A work item can satisfy every step of its workflow
type and still deliver something other than what was asked, because nothing in the
lifecycle compares the delivered outcome to the stated goal.

Four distinct mechanisms produce this result. They are routinely conflated, and
conflating them is itself a failure:

1. **Candidate-set error.** The specific instances a work item was framed around turn out
   to be mostly wrong. This is benign and expected; research exists to catch it.
2. **Goal displacement.** Research or design surfaces a problem that appears more
   important than the framed one, and the work delivers that instead. The original goal is
   never revised or withdrawn — it is orphaned, and the item closes as complete.
3. **Verb dilution.** The delivered action is structurally weaker than the stated one. A
   goal to *remove* is satisfied by *archiving*, which removes nothing, while the goal
   sentence still reads true on inspection.
4. **Unchallenged inherited convention.** A pre-existing convention is adopted as the
   delivery route without anyone asking whether that convention deserves to exist, and the
   convention silently determines the operative verb.

A wrong candidate list is not a wrong stated goal. Treating them as one thing either freezes
detail that research must be free to revise, or licenses replacing the deliverable
whenever the detail moves.

Concord already holds most of the parts. CD-0006 D10 established the pattern: planning
approves a **spec mandate** listing exactly which specs a change may modify, and
execution is bounded by it. CD-0006 D5 places conflict resolution and scope
decisions at planning under human authority. CD-0006 R1 provides forward-linked successor
work. Research conclusions do not automatically become binding decisions.
[`specs-as-laws.md`](../../docs/specs-as-laws.md) forbids silent scope *contraction*
under spec-law pressure.

D10's mandate delimits which specs may be touched. The plan states the intended
outcome, and independent Review judges the delivered work.

R5 establishes that three of the four mechanisms are documented failure classes with
named causes, that goal drift is measured and mitigated only statistically by prompt
discipline, and that competence and goal-fidelity are formally separable — so passing
process steps competently does not imply delivering the requested thing. R5 also
establishes, through §5, that a contract binding an objective *without* a revision channel
for its instances is a net regression rather than an improvement. That counter-evidence,
not the supporting evidence, determines the shape below.

## Decisions

A work item's recorded intent text states why the work exists. The authenticated
non-worker coordinator records current-version premise confirmation. Operator
decisions remain conversational under CD-0243.

### D1. The work keeps its stated goal

The item's recorded intent text states why the work exists, and an ordinary
intent revision replaces it in one transaction. Research remains free to revise
its working list of candidates without touching the stated goal: nothing
stores the working list, so no revision of it needs recording or re-approval.

### D6. Work discovered mid-execution forward-links; it never substitutes

Work discovered outside the agreed outcome creates forward-linked successor
work under CD-0006 R1. It does not silently replace the current work, and its
delivery does not complete the current item. An
explicit redirection follows the approval classes in CD-0243 D2. A new
execution-time law conflict returns to planning under CD-0006 D5. Contract
revision does not itself enact law or resolve a conflict. Research conclusions
do not automatically become binding decisions for in-flight discoveries.

An in-scope discovery may be clarified without a contract change. A redirection
records the complete next contract version through the ordinary intent update;
execution follows that recorded version after required conflict resolution and
operator decisions. Deferral keeps the agreed outcome and records separate work.

### D7. Verification is independent of the executing agent

Main obtains independent native Task Review of the accumulated solution.
Implement cannot accept its own result. Review's step record names the reviewer,
result, commit and actual result link under CD-0242 D5.

### D9. Premise confirmation is recorded

The authenticated coordinator records confirmation of the active contract's
stated goal. Completion retains that terminal fact.

### D10. Proportional rigor scales the evidence depth

Declared maturity and audience commitment govern the depth of proof under
CD-0006 R2. No stage is an evidence exemption.

### D11. Every workflow type carries a contract

Change, fix, research and removal work each carry a contract. Research may
conclude that no change is needed.

## Consequences

Forward links make discoveries visible without substituting another outcome.
Independent Review judges the delivery; the recorded step does not replace the
review itself. Surface changes remain subject to TS8.

## Required conformance scenarios

- An intent revision appends a retained contract version without changing an
  earlier version or creating a successor.
- An out-of-scope discovery is forward-linked and does not substitute for the
  current work.
- An independent Review may correct its candidate; a final review completes
  only with a clean pass.
- A work item at the lowest maturity and audience bands still requires an
  approved contract; only the proof depth differs from the highest bands.
- Terminal metadata, lifecycle change, reported delivery links and the
  completion record commit together or not at all.

## Supersession and amendments

The spec mandate, law-conflict resolution, forward-linking, operator approval
boundaries, storage authority and TS8 surface evolution rules remain in force.
