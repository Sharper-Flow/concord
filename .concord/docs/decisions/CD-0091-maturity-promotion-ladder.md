# CD-0091: Maturity and audience-commitment promotion ladder

- **Status:** Accepted
- **Date:** 2026-08-31
- **Scope:** The evidence bar that promotes a Product's `maturity` and
  `audience_commitment` fields above their current rung; issue #639
- **Approval:** The operator approved the finding and the decision contract in
  issue #639 on 2026-08-31.
- **Related:** CD-0007, CD-0010, CD-0089, `priorities.md` First-usable floor,
  `rollout-plan.md` §3 and §4, `product-data-model.md`, and
  `floor-readiness.md`
- **Preserves:** The first-usable floor as the replacement-readiness bar; the
  demand-driven correction model of `rollout-plan.md` §4; and the maturity and
  audience vocabulary of `product-data-model.md`

## Context

Concord documents the floor it has reached but not the ground above it.
`floor-readiness.md` defines replacement readiness for one operator and many
agents on one machine. No document defines what promotes a Product past that
floor.

`product-data-model.md` gives the vocabulary. The `maturity` field ranks
`prototype`, `alpha`, `beta`, `production`, and `deprecated`. The
`audience_commitment` field ranks `operator_only`, `limited`, and `public`. No
record states the evidence bar for any rung, so a field is a self-assessment
with no falsifier. A Product can read `production` while its core path carries
open defects, and no check notices.

`rollout-plan.md` §4 defers the question until "a later decision accepts a
bounded need." This decision accepts that need and defines the ladder.

## Decision

### D1. Promotion is an evidence claim, not a declaration

A Product may set `maturity` or `audience_commitment` above its current rung only
when the change that sets the field cites evidence meeting the target rung's bar
in D3 or D4. A field set without that evidence is drift, in the same sense that a
replacement-ready claim without floor evidence is drift.

### D2. Retired: the per-rung readiness manifest

### D3. The maturity rungs and their evidence bars

Each rung adds to the rung below it.

- **alpha**: the replacement-ready floor is met, and the core mutation and
  migration path carries no open defect classified as bootstrap-blocking.
- **beta**: alpha, and migration is forward-verified across at least one
  released schema change, and a documented crash-recovery procedure has a
  passing test.
- **production**: beta, and a defect-rate bar over a stated observation window is
  met, and a compatibility guarantee covers the command grammar and the typed
  tool surface, and `reachability-exceptions.v1.json` is empty or each exception
  is justified.

### D4. The audience-commitment rungs and their evidence bars

Each rung adds to the rung below it.

- **operator_only**: the default. It promises nothing beyond the author and
  needs no evidence.
- **limited**: a named non-author relies on the Product. It requires a support
  channel of record and a compatibility statement for the surfaces that party
  touches.
- **public**: limited, and the CD-0007 public release bar is met, and a privacy
  review is recorded, and a deprecation policy states how breaking changes reach
  dependents.

### D5. The ladder governs a claim, not a schedule

The ladder governs a Product's own maturity and audience claim. It imposes no
global adoption sequence, calendar, migration, or retirement on any Product. A
Product at `prototype` and `operator_only` owes nothing until it claims a higher
rung. This preserves `rollout-plan.md` §4.

### D6. Concord's current rung is recorded, not advanced

This decision does not promote Concord. Concord stays at `prototype` and
`operator_only` until the operator chooses to pursue a higher rung.

## Consequences

- A maturity or audience field above its proved rung is a defect in the change
  that sets it.
- No mechanism activates for a Product that claims no higher rung, so the
  decision adds no cost to prototype work.
- Concord gains a defined finish line for each maturity claim, which its own
  maturity field can later be measured against.

## Rejected alternatives

**Promotion by judgment alone.** Rejected because a field with no falsifier
drifts from reality exactly as the first-usable floor did before its manifest.

**A fixed calendar or global adoption sequence.** Rejected because it contradicts
the demand-driven model `rollout-plan.md` §4 preserves.

**A separate heavyweight certification document per rung.** Rejected because
the rung bars in D3 and D4 already state what each claim must prove.

## Verification

- This record exists with status `accepted` and is indexed once in
  `concord-knowledge-index.v1.json`.
- This decision is prose-only governance: it governs how a Product is
  matured, not how it behaves at runtime.
- `rollout-plan.md` §4 points at this decision as the accepted bounded need,
  without restating the ladder.
- Concord's `maturity` remains `prototype` and its `audience_commitment` remains
  `operator_only`.
- `python3 scripts/check-json.py`, `check-doc-links.py`,
  `check-knowledge-index.py`, and `check-public-content.py` pass.
