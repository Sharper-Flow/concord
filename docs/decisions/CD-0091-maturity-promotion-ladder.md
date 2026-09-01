# CD-0091: Maturity and audience-commitment promotion ladder

- **Status:** Accepted
- **Date:** 2026-08-31
- **Scope:** The evidence bar that promotes a Product's `maturity` and
  `audience_commitment` fields above their current rung; issue #639
- **Approval:** The operator approved the finding and the decision contract in
  issue #639 on 2026-08-31.
- **Related:** CD-0007, CD-0010, CD-0089, `priorities.md` First-usable floor,
  `rollout-plan.md` §3 and §4, `product-data-model.md`, and
  `floor-readiness.v1.json`
- **Preserves:** The first-usable floor as the replacement-readiness bar; the
  demand-driven correction model of `rollout-plan.md` §4; the maturity and
  audience vocabulary of `product-data-model.md`; and the floor-readiness
  manifest mechanism

## Context

Concord documents the floor it has reached but not the ground above it.
`floor-readiness.v1.json` proves replacement readiness for one operator and many
agents on one machine, and `check-floor-readiness.py` enforces it. No document
defines what promotes a Product past that floor.

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
when a recorded evidence manifest for the target rung is satisfied. A field set
without that manifest is drift, in the same sense that a replacement-ready claim
without floor evidence is drift.

### D2. Each rung reuses the floor-readiness mechanism

A per-rung manifest reuses the `floor-readiness` schema shape. It carries
conditions with contiguous ordinals and items in the states `satisfied`,
`outstanding`, `unmeasured`, or `out_of_scope`. Each `satisfied` item binds an
executable anchor that a required workflow invokes. The rung validator either
extends `check-floor-readiness.py` or is a sibling that reuses its schema. This
decision adds no new verification style.

### D3. The maturity rungs and their evidence bars

Each rung adds to the rung below it.

- **alpha**: the replacement-ready floor is met, and the core mutation and
  migration path carries no open defect classified as bootstrap-blocking.
- **beta**: alpha, and every `law-coverage.v1.json` entry is proved with no
  `outstanding` state, and migration is forward-verified across at least one
  released schema change, and a documented crash-recovery procedure has a
  passing test.
- **production**: beta, and a defect-rate bar over a stated observation window is
  met, and a compatibility guarantee covers the command grammar and the typed
  tool surface, and `reachability-exceptions.v1.json` is empty or each exception
  is justified.

### D4. The audience-commitment rungs and their evidence bars

Each rung adds to the rung below it.

- **operator_only**: the default. It promises nothing beyond the author and
  needs no manifest.
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
`operator_only`. The first rung manifest, for alpha, is the next artifact, and
the operator authors it when the operator chooses to pursue alpha. Until then no
rung manifest exists, because a Product at `prototype` owes none.

## Consequences

- A maturity or audience field above its proved rung becomes a detectable defect
  once the target rung is pursued and its manifest exists.
- The ladder mechanism matches the floor mechanism, so a reader learns one
  pattern rather than two.
- No mechanism activates for a Product that claims no higher rung, so the
  decision adds no cost to prototype work.
- Concord gains a defined finish line for each maturity claim, which its own
  maturity field can later be measured against.

## Rejected alternatives

**Promotion by judgment alone.** Rejected because a field with no falsifier
drifts from reality exactly as the first-usable floor did before its manifest.

**A fixed calendar or global adoption sequence.** Rejected because it contradicts
the demand-driven model `rollout-plan.md` §4 preserves.

**A separate heavyweight certification document per rung.** Rejected because the
floor-readiness manifest already models graded, evidence-bound conditions, and a
second mechanism would drift from the first.

**Build the alpha manifest and its validator now.** Rejected because Concord owes
no manifest at `prototype`, so the validator would guard data that does not
exist. The manifest is authored when a rung is pursued, per D6.

## Verification

- This record exists with status `accepted` and is indexed once in
  `concord-knowledge-index.v1.json`.
- The coverage shard records this decision as prose-only governance, because it
  governs how a Product is matured, not how it behaves at runtime.
- `rollout-plan.md` §4 points at this decision as the accepted bounded need,
  without restating the ladder.
- Concord's `maturity` remains `prototype` and its `audience_commitment` remains
  `operator_only`.
- `python3 scripts/check-json.py`, `check-doc-links.py`, `check-doc-contract.py`,
  `check-knowledge-index.py`, `check-knowledge-closure.py`, and
  `check-public-content.py` pass.
