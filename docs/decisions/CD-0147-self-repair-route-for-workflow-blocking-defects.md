# CD-0147: Self-repair route for workflow-blocking defects

- **Status:** Accepted
- **Date:** 2026-09-14
- **Scope:** Code-first repair when a Concord defect blocks its own typed recording operation
- **Approval:** The operator approved this bounded conduct route.
- **Related:** CD-0013, CD-0112, CD-0122, CD-0144, and CD-0041
- **Preserves:** Typed refusals, contract authority, repository evidence, lifecycle gates, and host-owned authority

## Context

A defect in Concord can refuse the typed operation that would record its own
repair. A session can then mistake the refusal for a missing capability and
invent a route. Some invented routes can claim authority that the record does
not carry.

CD-0122 permits authorized repository repair when a Concord defect blocks safe
normal recording or execution. This decision declares the conduct required for
the narrower case where the blocked operation would record that repair.

## Decision

### D1. Use code-first repair only for the bounded case

An authorized session may implement the repair before recording delivery when
all five conditions hold:

1. The defect is in Concord and a typed operation refuses the record of its own repair.
2. The operator approved code-first ordering for the item.
3. The item was captured before the repair code landed.
4. The delivery is reconciled afterward: the contract is superseded at its checkpoint, every contract-required evidence kind is bound, and the lifecycle reaches a terminal state.
5. The session claims no authority, verdict, or completion beyond what the record carries.

The session must record the delivery and its evidence after the repair. An
unreconciled delivery is not a delivery under this route.

### D2. Treat a typed refusal as a route signal

A refusal that names a remedy is evidence that the remedy exists. The session
must use the named typed route when it becomes available after the repair.

A claim that a restart is required needs evidence that the repair requires a
reload. A refusal alone does not prove that a restart is required.

### D3. Add no capability or bypass

This decision declares conduct. It adds no operation, flag, admission path,
operator override, or exception to a gate. Existing refusals remain active.

This route does not amend CD-0144 or CD-0041. It does not authorize fabricated
workflow state, skipped contract supersession, missing evidence, or a terminal
claim without the required record.

## Consequences

An authorized code-first repair can reach the typed recording route without
weakening the refusal that exposed the defect. Later sessions can distinguish a
reconciled repair from an unreconciled bypass by reading the record.

The route applies only when all five conditions hold. A session that cannot meet
one condition stays outside this route and makes no claim from it.

## Verification

- The knowledge record resolves this decision through the composed knowledge index.
- Repository validators check the decision path, content hash, evidence paths, and product-root law fields.
- The decision changes no engine code, gate, admission rule, or workflow step.
