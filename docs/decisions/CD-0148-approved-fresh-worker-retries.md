# CD-0148: Failed worker retries require exact approval and fresh fencing

- **Status:** Accepted
- **Date:** 2026-09-14
- **Scope:** Worker lane dispatch and workflow correction
- **Amends:** CD-0017, CD-0027
- **Related:** CD-0013, CD-0059, CD-0102

## Context

A failed worker attempt is terminal evidence. A retry that reuses its identity,
epoch, or host session can apply a result to the wrong execution boundary.
Approval that names only the work item does not prove which failed attempt the
operator reviewed. A retry also needs a durable bound to the approved contract.

## Decision

After a worker attempt fails, the agent mutation boundary must challenge for an
exact operator decision. The challenge binds the failed attempt ID, failed
attempt epoch, active contract version, work version, scope, and request digest.
The failed attempt remains immutable and terminal.

Only the matching, unused, unexpired approval can admit `dispatch_worker`. The
retry must use a new attempt ID and the next fenced step epoch. A missing, stale,
expired, or reused approval refuses without a new attempt or dispatch event.
The host deletes any resume task identity when it consumes a dispatch window.

The existing limit of three correction attempts remains in force. Once the limit
is reached, the work pin removes `dispatch_worker` and requires operator
escalation. A worker cannot record workflow transitions, verdicts, completion, or
resume a failed worker session.

## Consequences

- The approval challenge exposes the failed attempt and contract bindings.
- A valid retry creates a distinct worker attempt and step epoch.
- Invalid approval and retry identity changes have no durable workflow effect.
- CD-0027's fresh dispatch path remains stateless and does not become typed restart.

## Verification

- Agent mutation tests prove the challenge, fresh identity, stale approval,
  reused approval, and retry-limit boundaries.
- Store tests prove failed-attempt immutability, fresh attempt identity, fresh
  epoch fencing, and the three-attempt limit.
- Adapter tests prove approval forwarding and removal of a resume task identity.
