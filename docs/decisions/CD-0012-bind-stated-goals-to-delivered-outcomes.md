# CD-0012: Bind Stated Goals to Delivered Outcomes

**Status:** Proposed — not binding until operator acceptance
**Date:** 2026-08-08
**Decision owner:** Operator
**Scope:** The outcome contract carried by a work item, its revision authorities, the
refusal of weakened deliveries, the handling of work discovered mid-execution, and the
verification boundary at completion.
**Amends:** CD-0006 D5 and D10 by extending the approved-mandate pattern from specs
authorized for modification to end-state required for delivery. Amends the value-statement
invariant in [`workflows.md`](../workflows.md) §2.1 by giving it a binding counterpart.
**Research:** [`R5-goal-to-outcome-binding.md`](../research/R5-goal-to-outcome-binding.md)
**Issue:** <https://github.com/Sharper-Flow/concord/issues/22>

## Context

[`priorities.md`](../priorities.md) §2 names *intent fidelity* — "the delivered outcome
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

A wrong candidate list is not a wrong premise. Treating them as one thing either freezes
detail that research must be free to revise, or licenses replacing the deliverable
whenever the detail moves.

Concord already holds most of the parts. CD-0006 D10 established the pattern: planning
approves a **spec mandate** listing exactly which specs a change may modify, execution is
bounded by it, and "completion verifies delivered changes match the approved mandate: no
silent scope expansion or reduction." CD-0006 D5 places conflict resolution and scope
decisions at planning under human authority. CD-0006 R1 provides forward-linked successor
work. CD-0009 D8 forbids automatic conversion of a research conclusion into a binding
decision. [`specs-as-laws.md`](../specs-as-laws.md) forbids silent scope *contraction*
under spec-law pressure.

What is missing is the object. D10's mandate delimits *which specs may be touched*, not
*what end-state must exist*. Scope substitution and verb dilution are ungoverned because
they are a different direction from the contraction that specs-as-laws prevents.

R5 establishes that three of the four mechanisms are documented failure classes with
named causes, that goal drift is measured and mitigated only statistically by prompt
discipline, and that competence and goal-fidelity are formally separable — so passing
process steps competently does not imply delivering the requested thing. R5 also
establishes, through §5, that a contract binding an objective *without* a revision channel
for its instances is a net regression rather than an improvement. That counter-evidence,
not the supporting evidence, determines the shape below.

## Decisions

### D1. The outcome contract has three parts with three revision authorities

Every work item carries an **outcome contract** composed of three separable parts. Each
part has a different mutability rule, and separating them is the point.

| Part | Content | Revision authority | Effect of revision |
|---|---|---|---|
| **Premise** | Why this work exists, in the operator's terms. Durable. | Operator only | Not an edit. A changed premise supersedes the item with a successor. |
| **Required end-state** | One or more falsifiable postconditions over the world after delivery, carrying the operative verb. | Operator only, at approval | Not an edit. See D6. |
| **Candidate set** | The specific instances the end-state ranges over. | The executing agent, during execution | Appends an event. Premise and required end-state are untouched and no re-approval occurs. |

The candidate set is deliberately the mutable part. R5 §5.1 shows that a contract which
forbids in-place revision of the instance list converts a recoverable framing error into a
structurally guaranteed wrong outcome, because the executing party is forbidden from
acting on the discovery that the list is wrong. Research must remain free to invalidate
every instance without touching the reason the work exists.

R5 §4.1 records that no established practice enforces this split. It is Concord-original,
adopted because the counter-evidence requires it, and it is not adopted consensus.

### D2. The end-state mandate is approved at planning, beside the spec mandate

The required end-state joins the **approved change contract** at the same moment and under
the same authority as CD-0006 D10's spec mandate. It is not a capture-time field.

- At capture, a work item carries its prose value statement per
  [`workflows.md`](../workflows.md) §2.1 and its premise. The end-state is frequently not
  yet knowable, and requiring it there manufactures the acceptance-criteria theatre that
  R5 §5.3 documents.
- At planning approval, the operator approves the required end-state together with the
  spec mandate. Both are recorded before execution begins, with provenance.
- At completion, the delivered outcome is verified against the approved end-state, exactly
  as D10 verifies delivered changes against the approved spec mandate.

This decision adds no agent tool. Verification attaches to the existing lifecycle
transition to `completed`. A separate outcome-verification or completion tool is a shape
already rejected by [`agent-mutation-tool-contract.md`](../agent-mutation-tool-contract.md)
§6, and the TS2 nine-tool ceiling is unaffected. The `capture` payload is unchanged, so
no MAJOR agent-surface version bump follows from this decision.

### D3. A delivered end-state may only be stronger than the approved one, never weaker

Adopt Meyer's redefinition rule for postconditions directly: under redefinition a
postcondition may only be strengthened, never weakened. Applied to work items, a delivery
whose end-state is weaker than the approved end-state **fails**; a delivery whose end-state
is stronger passes.

This gives verb dilution a structural refusal rather than a reviewer's opinion. Where the
approved end-state asserts that named paths are absent, a delivery that relocates them
under an archive is strictly weaker and is refused at completion, regardless of how well
it is argued, documented, or tested.

The rule is established practice, not a Concord invention. Concord's contribution is
applying it to work items rather than to routines.

### D4. An absent or vacuous end-state is a defect, not an approval

Two refusals at approval time:

- **Omission.** A postcondition that is not stated defaults to trivially true and
  guarantees nothing. An approved change contract carrying no required end-state is
  refused. Silence is not a weak contract; it is the absence of one.
- **Vacuity.** A postcondition already satisfied by the state of the world before
  execution is refused. A predicate that cannot fail cannot bind.

### D5. Absence is a first-class end-state form

An end-state may assert non-existence. This is required by the motivating case — a removal
goal is unexpressible otherwise — and R5 §3.2 records that EARS, the closest established
requirement syntax, has no negation pattern and therefore cannot carry it.

R5 §4.2 records the honest position, which this decision adopts verbatim:

> No single, widely-named requirements formalism is dedicated to expressing absence or
> removal as a checkable postcondition. The established mechanisms are distributed across
> negative postconditions (Design by Contract, JML), negative assertions in test
> frameworks, and forbidden-pattern or drift checks in continuous integration and
> policy-as-code. This decision adopts that composite and does not claim a single
> canonical source.

An absence postcondition names the ground-truth surface it is evaluated against.
Consistent with [`design-constraints.md`](../design-constraints.md) §17, that surface is
the resource's own facts, not a status field elsewhere in the system.

### D6. Work discovered mid-execution forward-links; it never substitutes

Work discovered during execution that is not the approved end-state creates **forward-linked
successor work** under CD-0006 R1. It does not replace the current item's end-state, and
the current item does not close as complete by delivering it.

Not all displacement is wrong — R5 §5.2 shows that suppressing legitimate redirection is
its own failure, and that an agent which cannot surface a better target will either bury
the finding or smuggle it. Forward-linking is the channel that lets the better target be
surfaced without being substituted.

Where the operator does want the current item redirected, that is a **legislative moment**,
and it mirrors the three options in [`specs-as-laws.md`](../specs-as-laws.md) §4:

| Option | Meaning |
|---|---|
| **(a) Clarify** | The discovery is already within the approved end-state; no contract change. |
| **(b) Revise the end-state** | The operator changes what must be delivered. The prior contract is superseded, not edited, and the item's history retains both. |
| **(c) Consciously defer** | The operator keeps the approved end-state and records the discovery as separate work. |

Never silent, and never the agent's decision. This extends CD-0009 D8 — "no automatic
conversion of a research conclusion into a binding decision" — from research conclusions
to in-flight discoveries generally.

### D7. Verification is independent of the executing agent

The end-state check is authored at approval by the approving authority and evaluated by a
party that is not the executing agent. The executing agent may not author, re-select,
weaken, or disable the check that governs its own completion.

R5 §1.5 documents the failure this closes: an agent whose verification layer was
self-authored reported inert work as delivered, because "the verification layer that was
supposed to catch exactly this kind of gap was itself checking the wrong thing, silently,
by design." R5 §3.1 records the same lesson from contract practice — an assertion the
executing party may disable is not a control.

Evidence binding follows CD-0008's immutable-subject rule; this decision introduces no
separate evidence authority.

### D8. Route conventions that determine the operative verb are declared at approval

Where the delivery route relies on a pre-existing convention that determines the operative
verb, the approved contract names that convention. Adopting an undeclared convention that
weakens the verb is already refused by D3; D8 makes the reliance visible **before**
execution rather than discovered at completion.

This is deliberately the lightest of the eleven decisions. It adds a declaration, not a
review step, and it is bounded to conventions that bear on the verb — not to every
practice a route touches. The posture follows
[`architecture-spike.md`](../architecture-spike.md) §4.1: the rule is structural rather
than a matter of intent or review vigilance.

### D9. Predicate satisfaction is necessary but never sufficient

The premise travels beside the required end-state permanently, in every read surface that
shows one of them. Acceptance requires the operator to confirm the premise is satisfied,
not only that the postconditions passed.

This is the anti-Goodhart clause, and it is load-bearing. R5 §5.4 establishes that a
checkable proxy displaces the objective it stands for, and that this bites precisely here:
"named paths are absent" is satisfiable by relocation. A design in which predicate
satisfaction alone closes the item has rebuilt verb dilution one level up, at the predicate
rather than the prose.

### D10. Proportional rigor scales the evidence depth, never the contract's existence

Under CD-0006 R2, declared maturity and audience commitment govern how much proof an
end-state assertion requires. They never govern whether an outcome contract exists. Every
work item has one. No stage is exempt, consistent with
[`priorities.md`](../priorities.md) §2: "No stage is an evidence exemption."

CD-0006 R2's global floor already includes "no silent weakening." D3 is the structural
enforcement of that floor clause for outcomes.

### D11. Every workflow type carries an outcome contract; the verb differs by type

The contract is universal; what the verb asserts is type-specific. This decision does not
create an eighth gate, and it does not force one workflow shape onto another —
[`priorities.md`](../priorities.md) §2 governs quality by attributes rather than by gate
count, and that is unchanged.

| Workflow type | Required end-state asserts |
|---|---|
| Implementation change | The capability exists and the specified behavior holds. |
| Architecture spike | An accepted binding decision record exists. The verb is already fixed by CD-0009 D2 and [`architecture-spike.md`](../architecture-spike.md) §1.1: a spike must resolve to a decision. `insufficient evidence` is a satisfying decision outcome. |
| Research / investigation | Findings are recorded. A `no change` conclusion **satisfies** this end-state; it is not a failure, and D3 does not convert it into one. |
| Ops runbook | The operational postcondition holds, with the step evidence the type requires. |
| Break-fix | The reproduced defect no longer reproduces. |
| Static analysis | The report exists over the declared surface. |
| Generic one-off | An operator-authored end-state. The generic type is not an exemption. |

The research row is the one that would break if stated carelessly. A research workflow's
freedom to conclude `no change` is preserved exactly, because its required end-state is
about recorded findings, not about a change being made.

## Consequences

### Positive

- *Intent fidelity* and *no silent drift* acquire a mechanism instead of remaining
  aspirational attributes.
- Verb dilution becomes a typed refusal grounded in established contract practice, rather
  than something a reviewer must notice.
- A wrong candidate set stops being an identity crisis. Research can invalidate every
  instance without escalation, and without the premise being quietly discarded alongside.
- Displacement becomes visible. A better target discovered mid-flight becomes linked work
  with its own record instead of an undocumented substitution.
- The pattern is not new machinery. It is CD-0006 D10 applied to a second object, using
  the same approval moment, the same authority, and the same completion check.
- No new agent tool, no capture-payload change, and therefore no agent-surface MAJOR bump.

### Cost

- Planning approval carries more content, and the operator must author end-state
  assertions rather than approve prose. This is real recurring cost on the operator, who is
  the only person in the system.
- Postconditions can be written badly. A vacuous or over-narrow assertion is now a
  first-class way to make a bad delivery look rigorous. D4 refuses the obviously vacuous
  cases and D9 refuses predicate-only acceptance, but neither eliminates the risk, and R5
  §5.3 documents that this failure mode is common wherever checkable criteria are required.
- Absence assertions depend on naming the right ground-truth surface. A wrongly scoped
  surface produces a confidently green check over the wrong territory.
- The premise / candidate-set split has no prior art. R5 §4.1 states this plainly. If it is
  wrong, it is wrong in a way no existing system has already discovered for us.
- Strengthen-only comparison between two end-states requires an ordering. For absence and
  existence assertions this is mechanical; for richer behavioral assertions it may require
  operator judgment, and the deferred questions below do not pretend otherwise.

## Required conformance scenarios

The implementation acceptance suite must exercise each of the following. Scenario carriers
follow the existing corpus conventions; this decision does not itself modify
[`agent-jobs.v1.json`](../../scenarios/agent-jobs.v1.json).

1. A change contract approved with no required end-state is refused at approval (D4).
2. A change contract whose required end-state already holds before execution is refused as
   vacuous (D4).
3. A delivery asserting a weaker end-state than approved is refused at completion, with a
   typed outcome-mismatch result rather than a generic error (D3, TS7).
4. A delivery asserting a stronger end-state than approved is accepted (D3).
5. An absence end-state over a named ground-truth surface is satisfied by removal and
   refused when the subject is relocated rather than removed (D3, D5).
6. Candidate-set revision during execution succeeds without re-approval, appends an event,
   and leaves premise and required end-state byte-identical (D1).
7. Premise revision is refused as an in-place edit and is representable only as
   supersession with a successor item (D1).
8. Work discovered mid-execution that lies outside the approved end-state produces
   forward-linked successor work and does not close the current item (D6).
9. End-state revision by the operator supersedes the prior contract, retains both in
   history, and records which option was chosen, by whom, and when (D6), matching the audit
   shape in [`specs-as-laws.md`](../specs-as-laws.md) §6.
10. An executing agent attempting to author, replace, or disable its own end-state check is
    refused (D7).
11. A research work item concluding `no change` satisfies its required end-state and
    completes (D11).
12. An architecture spike concluding `insufficient evidence` satisfies its required
    end-state and completes (D11).
13. Completion where all postconditions pass but the operator has not confirmed premise
    satisfaction does not reach an accepted terminal state (D9).
14. Contract and terminal metadata commit together; no observer sees an item terminal with
    an unresolved outcome verdict (PM4 invariant 7).

## Deferred to implementation design

These are named rather than silently decided by implication.

1. **Predicate representation.** Whether an end-state assertion is a typed structure, a
   named executable check, or a bounded expression language. D3 requires only that two
   assertions be comparable for strength.
2. **Event and projection shape.** Which domain events carry contract approval, candidate-set
   revision, and outcome verdict, and whether a typed projection is warranted. Bound by
   CD-0002 and PM3; the one-transaction rule applies.
3. **Strength ordering for behavioral assertions.** Mechanical for existence and absence;
   open for richer assertions. Until resolved, comparison in ambiguous cases surfaces to
   the operator rather than defaulting to pass.
4. **Cross-item outcome attribution.** When one item's end-state is delivered by another
   item's work, whether that is PM4 supersession, a typed relation, or neither.
5. **Downstream notice class.** Whether an end-state revision emits a `breaking` notice
   under CD-0006 R3 to work that declared a dependency on the original outcome.

## Supersession and amendments

This decision **extends** CD-0006 D10 to a second object and does not weaken it. The spec
mandate remains exactly as accepted; the end-state mandate is its sibling, approved at the
same moment under the same authority.

This decision **amends**:

- [`workflows.md`](../workflows.md) §2.1 — the one-sentence value statement remains, and
  gains a binding counterpart. The value statement answers why the work matters; the
  required end-state answers what must be true when it is done. Neither replaces the other,
  and D9 requires both to travel together.
- [`design-constraints.md`](../design-constraints.md) §10 — completion criteria declared by
  a workflow type must include an outcome contract.
- [`feature-inventory.md`](../feature-inventory.md) §3.16 — the value-statement invariant
  entry gains the outcome-contract counterpart.

This decision **does not** alter: the
[`specs-as-laws.md`](../specs-as-laws.md) contraction policy or the user-is-legislator
principle; CD-0006 D5's planning-owns-conflicts rule; CD-0006 R1 forward-linking; CD-0006
R2 obligation bands; CD-0005 and TS2's nine-tool ceiling; CD-0002 or PM3 storage authority;
or Priority 2's governance by attributes rather than gate count.

An unaccepted decision does not bind. Per
[`architecture-spike.md`](../architecture-spike.md) §5, operator acceptance is what makes
this record binding on downstream work.
