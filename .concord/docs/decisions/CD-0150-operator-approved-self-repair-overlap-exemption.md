# CD-0150: Operator-approved self-repair bypasses overlap admission

- **Status:** Accepted
- **Date:** 2026-09-14
- **Scope:** Concord workflow defects that block the operation needed for their repair
- **Approval:** The operator approved the explicit `self_repair` classification and its code-first delivery.
- **Related:** CD-0222, CD-0144, and CD-0148
- **Amends:** CD-0145 D1 and CD-0147 D3
- **Preserves:** Typed refusals, visible overlap detail, ordinary overlap admission, contract authority, and all non-overlap gates
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D13

## Context

CD-0145 blocks concurrent Product writes until their overlap has a resolution.
A Concord defect can also prevent that resolution or worker dispatch. CD-0147
permits code-first repair in this case, but its D3 adds no gate exception.

The repair route needs durable authority that is narrower than a general
override. It must identify the typed refusal, require an operator decision, and
leave the conflicting work visible.

## Decision

This operator-derived exemption remains available only under its original signed authority. A routine supersession under `approval_policy=consequence_v1` grants no exemption, even when its descriptive `self_repair` fields are valid. New-policy work resolves its actual Domain intersection through the dedicated version-pinned overlap operation. Neither classification nor unsigned supersession acquires a standing compatibility waiver.


### D1. The active contract owns the classification

A `self_repair` classification has these fields:

1. `refusal_kind` names one closed typed-error kind.
2. `blocked_operation` names the operation that the refusal blocked.
3. `evidence_refs` contains 1–32 unique references to the refusal evidence.

Only the Concord Product can use this classification. A signed operator
decision must approve the exact successor contract through
`supersede_contract`. The supersession mutation verifies the operator identity
and the Product registry key before it stores the classification
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
The operator decision
is the authority. The evidence references remain its auditable support.

### D2. The exemption changes only overlap admission

The Domain-overlap execution guard admits work with an active `self_repair`
classification. It does not resolve, dismiss, or rewrite an overlap. It does
not bypass lifecycle, contract, evidence, worker, worktree, or completion
checks.

Unclassified work continues to use the CD-0145 shared-write predicate and its
operator-approved resolution choices.

### D3. Read surfaces keep the conflict visible

Workflow reads, work pins, and continuity include the active classification.
The overlap read surfaces continue to report every unresolved intersection for
classified work.

### D4. Prior contracts stay unclassified

The cutover migration stores `null` for existing contracts, and nothing infers
self-repair authority from prior data
([CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)).

## Consequences

- Concord can repair a workflow gate that blocks its own repair route.
- The operator approves the exact refusal, operation, evidence, and contract.
- Ordinary concurrent Product writes keep their existing protection.
- Read surfaces distinguish an exemption from an overlap resolution.

## Verification

- Store tests prove the Product and operator restrictions.
- Store tests prove classified admission and ordinary-work refusal.
- Agent tests prove signed contract supersession and visible overlap detail.
- Migration tests prove that existing contracts stay unclassified
  ([CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)).
- Generated payload checks prove the closed `self_repair` schema.
