# CD-0133: Contract correction at a worker-dispatch step

- **Status:** Accepted
- **Date:** 2026-09-10
- **Scope:** Operator-approved contract correction after a failed worker dispatch
- **Approval:** The operator approved this bounded correction route.
- **Related:** CD-0013, CD-0059, CD-0115, CD-0130
- **Preserves:** Released workflow definitions, definition digests, worker attempt identity, and operator authority

## Context

A failed worker attempt can leave a work item on an external-effect step whose
contract needs correction. The correction route currently accepts only a human
checkpoint, so the failed attempt can strand the workflow.

Changing the pinned definition would change its digest. The engine must add the
correction route without changing the definition or bypassing worker-attempt
guards.

## Decision

### D1. Admit correction at a worker-dispatch step

`supersede_contract` is available at a non-terminal worker-dispatch step when
the step has a recorded failed worker attempt. The action still requires the
verified operator approval identity.

### D2. Preserve the pinned definition

The route uses the existing contract-correction action. It does not add an
action, step, or worker route to the pinned workflow definition.

### D3. Keep post-dispatch refusals actionable

An advance that bypasses a dispatched worker attempt remains refused. Its
remediation names both accepted completed attempts and recorded failed attempts.

### D4. State only admissible routes in the work pin

The work pin exposes the contract-correction action with the current work
version, alongside the declared step actions. While a worker dispatch in the
current attempt holds the step, the pin omits every advancing step action except
`accept_worker_result`, because the fold refuses those advances. One predicate
owns that fact for both surfaces, so the pin cannot drift from the fold.

## Verification

- The contract-correction checkpoint remains available on human checkpoints.
- A worker-dispatch step can expose the correction route without a definition digest change.
- A live worker dispatch closes the correction route, and a bare worker failure keeps it closed.
- Post-dispatch advance refusal names the accepted-result and failed-attempt routes.
- The work pin includes the correction action at the current expected version.
- The work pin omits refused advances while a dispatch holds the step, and restores them after a fresh start.
