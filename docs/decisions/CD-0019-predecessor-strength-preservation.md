# CD-0019 — Predecessor-strength preservation and research-informed redesign

**Status:** Accepted under operator approval.
**Date:** 2026-08-13.
**Approval:** Operator direction, 2026-08-13.
**Type:** Preservation and method decision.
**Relates:** [`../specs-as-laws.md`](../specs-as-laws.md), [`../self-documentation.md`](../self-documentation.md), [`../concord-knowledge-index.md`](../concord-knowledge-index.md), [`../advance-predecessor-lessons.md`](../advance-predecessor-lessons.md), CD-0009, CD-0010.

## Context

Advance (the predecessor) has stabilized after Temporal removal and tool-count
reduction, but is not Concord's end state. Several existing constitutional docs
already carry ADV strengths forward in part — specs-as-laws, self-documentation,
the knowledge index.

What is not yet captured as law is two things the operator directed on 2026-08-13:

1. **Which predecessor features are preservation-mandated** — Concord must have
   them in some shape, because they are what keep a large project well-documented
   and known.
2. **That each preserved feature's Concord shape is research-informed, not
   inherited** — the move from Advance to Concord optimizes shapes against
   emerging best practice and the realities of an agent-native, SQLite-backed,
   event/contract/scenario system, rather than cargo-culting ADV's exact forms.

## Decision

### D1. Six predecessor features are preservation-mandated

Concord must carry each forward, in some shape:

| # | Feature | Why it matters | Existing partial treatment |
|---|---|---|---|
| 1 | Specs — queryable capability law | Behavioral contract that prevents drift; legislator-visible law | [`specs-as-laws.md`](../specs-as-laws.md) |
| 2 | Per-change narrative artifacts — problem/decision/outcome trail | Each change leaves a coherent WHY, not just a git diff | [`self-documentation.md`](../self-documentation.md) §1.2 |
| 3 | Knowledge index — concepts → docs → code map | Navigable map of what is where | [`concord-knowledge-index.md`](../concord-knowledge-index.md) |
| 4 | Conformance — claims-vs-reality check | Automated proof stated behavior matches delivered behavior | `internal/portfolio`, `internal/workflowcorpus` read projections |
| 5 | Triage — issue ↔ work reconciliation + portfolio balance | Overlap coalescing and what-matters-next surfacing | GitHub Issues as work authority |
| 6 | Reflection — post-completion learning | Durable lessons that fit in no single change | not yet identified; research under D2 |

Presence is mandated; exact shape is not. Each may be redesigned, consolidated,
or restructured as long as the capability survives.

### D2. Each preserved feature is research-informed, not inherited

Before a feature's Concord shape is fixed, three inputs are gathered:

1. **ADV-current** — what the predecessor does today, what works, what is brittle.
2. **State of the art** — emerging research and best shapes elsewhere (spec-driven
   development, ADRs/decision logs, knowledge graphs, contract/scenario testing,
   portfolio triage, learning loops).
3. **Concord-fit** — the shape that lands cleanly on SQLite authority, event/lifecycle
   semantics, contract/scenario foundations, and the agent-native operating envelope.

Output per feature: a target Concord shape plus the gap from where Concord is now.
A feature is not shape-fixed by copying ADV; it is shape-fixed by this three-input
method.

### D3. Not separately mandated

Three ADV features are not separately preservation-mandated: the gated
human-checkpoint lifecycle, the agreement-gate contract-before-work discipline,
and standalone wisdom capture. Not rejected — they may be absorbed into one of the
six, redesigned, or dropped. That is a per-feature research question (D2), not
pre-decided here.

### D4. Mandates and methods; does not authorize implementation

Under CD-0010, each feature's research and shape-fixing proceeds as GitHub Issues,
CD-0009 research packs, and subsequent shape-fixing decision records. This CD
establishes the preservation mandate and research-informed method only. No feature
is implemented by virtue of this decision.

### D5. Relationship to existing docs

[`specs-as-laws.md`](../specs-as-laws.md), [`self-documentation.md`](../self-documentation.md),
and [`concord-knowledge-index.md`](../concord-knowledge-index.md) are partial
treatments already in force; this decision is the umbrella mandate and method
binding them under one preservation + research-informed frame.
[`advance-predecessor-lessons.md`](../advance-predecessor-lessons.md) captures
failure modes to prevent; this decision captures strengths to preserve. The two
are complementary.

## Rationale

Prevents two opposite failure modes in the ADV→Concord transition:

- **Loss** — rewriting without a mandate drops the documentation/spec machinery
  that keeps a large project known.
- **Cargo-cult** — copying ADV shapes with better modern alternatives, or shapes
  that fit ADV's single-operator-checkpoint model but not Concord's agent-native
  envelope.

A uniform research-informed method (D2) keeps feature work consistent and
evidence-backed.

## Consequences

### Positive

- Six documentation/spec strengths guaranteed to survive the rewrite.
- Each lands in a shape informed by current best practice and Concord's actual
  architecture.
- Existing partial treatments gain an umbrella mandate.

### Cost

- Each feature carries a research obligation before shape-fixing; "just port it"
  is not an available shortcut.
- Shape-fixing is sequenced per feature, not batched.

## Supersession

None. Does not supersede CD-0009, CD-0010, or any existing constitutional doc;
mandates and methods alongside them.
