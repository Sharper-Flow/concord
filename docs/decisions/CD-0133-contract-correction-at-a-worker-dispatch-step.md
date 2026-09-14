# CD-0133: Contract correction at a worker-dispatch step

- **Status:** Accepted
- **Date:** 2026-09-10
- **Scope:** Operator-approved contract correction before worker dispatch and after a recorded worker failure
- **Approval:** The operator approved this bounded correction route.
- **Related:** CD-0013, CD-0059, CD-0115, CD-0130
- **Refines:** CD-0128
- **Preserves:** Released workflow definitions, definition digests, worker attempt identity, and operator authority

## Context

An operator can change a requirement before a worker runs. A failed worker can
also leave a contract that needs correction. Requiring a worker failure before
every correction would force execution against a requirement the operator has
already rejected.

Changing the pinned definition would change its digest. The engine must add the
correction route without changing the definition or bypassing worker-attempt
guards.

## Decision

### D1. Admit correction at a worker-dispatch step

`supersede_contract` is available at a non-terminal worker-dispatch step with one
active contract before any worker dispatch in the current attempt. It is also
available after the worker's failure is recorded. The action still requires
the verified operator approval identity, current versions, and audit evidence.

A dispatch authorization closes the pre-dispatch route even before a worker
report exists. A live worker, an unaccepted result, or an unrecorded failure does
not become permission to replace the contract.

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

### D5. Keep correction reachable when contracts overlap

An unresolved Domain overlap does not hide an otherwise admissible contract
correction. Action discovery and mutation preflight agree on that recovery
route. Correcting a contract does not itself resolve compatibility with other
work or permit execution through an unresolved overlap.

### D6. Correct the contract and design through one approval

The correction payload may include `design_record`: an approach, typed decisions,
and touched references under the existing design bounds. It replaces an existing
typed design, not a missing design step. The approval binds the whole payload,
including the replacement design and expected versions.

The core records the supersession and replacement design atomically. Without a
replacement, an earlier design remains historical and cannot authorize worker
dispatch. A later approved correction can supply the replacement. Work-version
order determines validity, not timestamps. Replay preserves the same distinction.

The work pin and dispatch guard use the same current-design predicate. No new
late-step action or workflow-definition exception is required. Worker fences and
ordinary overlap checks remain in force.

## Verification

- The contract-correction checkpoint remains available on human checkpoints.
- Correction before a fenced start and after a start with no dispatch reaches the public signed-approval route.
- A worker-dispatch step can expose the correction route without a definition digest change.
- A pending dispatch authorization, live worker, or unrecorded worker failure keeps the correction route closed.
- A changed successor payload cannot reuse an approval, and a stale work version cannot change the contract.
- A repeated approved request replays without another contract revision or worker attempt.
- Overlap recovery permits contract correction but preserves ordinary execution refusal.
- Post-dispatch advance refusal names the accepted-result and failed-attempt routes.
- The work pin includes the correction action at the current expected version.
- The work pin omits refused advances while a dispatch holds the step, and restores them after a fresh start.
- A replacement design cannot reuse an approval issued for different content.
- Contract and design replacement replay without another design record.
- An invalidated design blocks dispatch until an approved replacement is current.
