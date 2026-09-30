# CD-0030: Mid-execution observations

- **Status:** Accepted
- **Date:** 2026-08-15
- **Scope:** Work-item events; agent tool surface; continuity snapshot
- **Related:** CD-0012 (D6), CD-0016 (continuity), CD-0018 (capture,
  `raised_from`), CD-0025 (surface pattern), issue #99, R8
- **Supersedes:** nothing

## Context

An agent that discovers, mid-execution, work outside the approved end-state had
every obligation satisfied at completion and no natural act for recording the
finding. The existing recording acts — capture, `link_successor`, `raised_from`
— are work-management decisions requiring work-hood before observation. The
silent drop (R8's five-siblings scenario) is the reverse of the drift CD-0012
D6 guards: not delivering something other than promised, but learning something
and having no cheap channel to say it.

## Decision

**D1. Observations are a first-class, non-authoritative record on the work
item.** `work.observation_recorded` persists a bounded statement (1–512
characters), optional refs (≤16), and optional tags (≤8), any time the item is
nonterminal, through `concord_work_define.observation_record`. Observations
satisfy nothing: no evidence kind, no gate, no workflow action reads them as
authority. They are the durable form of "I noticed something."

**D2. Visibility is read-time, not enforced.** The continuity snapshot carries
the un-promoted observations (bounded, newest first), so a resuming session and
the operator see them at the boundary where the next decision is made. No
completion gate exists; an item closing with un-promoted observations is
checkable after the fact — a review signal, not a refusal.

**D3. Promotion is unchanged and separate.** Turning an observation into work
remains the deliberate CD-0018 path: capture with a `raised_from` edge to the
observing work, or any warranted relation. Observations are the substrate;
promotion is a later decision by anyone.

**D4. Terminal items stop recording but keep their observations.** The fold
refuses observation events on terminal work (the discovery channel belongs to
active work), while existing rows persist for audit and promotion.

## Rejected alternatives

Recorded in R8: a completion declaration gate with coherence refusal (operator
direction: avoid gates, fix the channel), a divergence-only gate (unsound —
self-report is the failing channel), non-blocking omission surfacing alone, and
no mechanism.

## Verification

- `internal/store/work_observations_test.go`: record + rebuild determinism,
  terminal refusal, bounds, idempotent replay.
- `internal/agent/observation_dispatch_test.go`: the surface op through real
  dispatch, continuity carries un-promoted observations, and the
  non-authority property — an observation cannot satisfy evidence, approve,
  or transition.
