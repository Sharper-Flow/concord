# CD-0052: `implements` declares fulfilment-link semantics, display-only

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** The work-relation kind `implements`; issue #273
- **Approval:** Operator accepted the drafted decision as written on 2026-08-21; the
  public record is
  [issue #273 comment](https://github.com/Sharper-Flow/concord/issues/273#issuecomment-5370947874)
- **Related:** CD-0018 (relation-kind declaration precedent), CD-0041
  (architecture bindings own the work-to-Domain dimension),
  [`workflow-engine-contract.md`](../workflow-engine-contract.md)
- **Preserves:** the closed relation vocabulary in the fold grammar, the
  schema CHECKs, and the agent-facing payload enum; the cycle exemption
- **Supersedes:** nothing; gives undefined vocabulary its first semantics

## Context

`implements` is one of the four original relation kinds (issue #11, PM4
lifecycle) — `parent`, `blocks`, `supersedes`, `implements` — and has been
cycle-exempt since introduction. Unlike every kind added later, it never
received a semantic definition: no decision record defines it, no research
report defines it, no scenario exercises it, no contract documents it, and no
code consumes it beyond display edge listings. Issue #273 investigated the
history and found intent but no law.

The investigation supports one coherent reading. `work_items.kind` includes
`decision`: decision records are work items. A directional edge
`task implements decision` is a many-to-one fulfilment link — this work
delivers that accepted law or goal. That shape explains the original
cycle exemption (rejection is pointless for a non-hierarchical, non-blocking
linkage where many items may point at one target) and completes the original
four-kind set: hierarchy (`parent`), dependency (`blocks`), replacement
(`supersedes`), fulfilment (`implements`).

CD-0041's `workflow_architecture_bindings` own the work-to-Domain dimension.
The work-to-decision-record dimension this kind addresses remains otherwise
unowned.

## Decision

### D1. Semantics

`B implements A` records that work item B fulfils work item A, where A is
typically a `decision`-kind item. The edge is directional, non-blocking, and
non-hierarchical: it never removes a target from the ready query, participates
in no PM1.Q5 `blocks` exclusion, and asserts no parent-child structure. It is
cycle-exempt by design — the exemption is the structural signature of a
many-to-one linkage, not an oversight. It carries the common relation evidence
(`actor`, `reason`, expected/resulting versions) via the existing
`work_relate.link` and `work_relate.unlink` operations, with no
special-cased composite path.

### D2. Display-only until a consumer exists

No ranking, gating, attention tier, or workflow state may read this kind.
It renders in existing relation edge listings and nothing else. A future
consumer must define its read surface in its own accepted record and cite this
one; until then, building on `implements` behaviour beyond display is
unsupported.

### D3. Removal is off the table

The kind sits in the public read-side relation vocabulary (schema CHECK on
`relations`) and the closed agent-facing `relation_kind` payload enum.
Removing it is a schema migration across the closed vocabulary for zero
behavioural gain while nothing writes it. The declaration above is the
cheapest honest state and is reversible if a consumer lands.

## Consequences

- The vocabulary is fully specified: every legal relation kind now has
  documented semantics.
- The cycle exemption is law, not accident — and already tested as such.
- No code changes. No store, schema, contract, or generated-file movement.

## Verification

- `internal/store.TestRelationKindsEnforceTheirOwnGraphRules` exercises the
  per-kind cycle rejection and the `implements` cycle exemption (two-node
  cycle permitted).
- Law coverage for this record is `satisfied` on that anchor.
- Validators: `check-knowledge-index.py`, `check-law-coverage.py`,
  `check-json.py`, `check-doc-links.py`, `check-public-content.py`.
