# CD-0140: Capture refuses a colliding external reference

- **Status:** Accepted
- **Date:** 2026-09-12
- **Scope:** Work creation folds and capture surfaces
- **Approval:** The operator approved this bounded collision gate.
- **Related:** CD-0002, CD-0013, CD-0035, CD-0050
- **Preserves:** Terminal work reuse, one-way Linear import, and domain-overlap detection

## Context

Work captures can carry an external reference from an issue tracker or another
source. The store had no common refusal when two live work items carried the
same reference, so a duplicate could be created without a declared origin.

Every creation route emits `work.created`. A gate in one command handler would
leave another route able to bypass the collision check.

## Decision

### D1. Read live collisions in the creation fold

`foldWorkCreated` reads the exact external reference on its existing transaction
and matches only work items whose `terminal_time IS NULL`. An empty reference
skips the read. The fold returns `projection_conflict` with an
`external_ref_conflict` payload that carries the existing work ID and reference.

### D2. Require an explicit origin for a legitimate duplicate

The optional `raised_from_work_id` capture field must name the item returned by
the collision read. The fold records the `raised_from` relation with the new
work item in the same transaction. Any other value refuses the capture.

### D3. Keep creation routes on one rule

Agent capture and host bootstrap publish `raised_from_work_id` to the shared
creation event. Initiative creation and Linear import continue to emit their
existing creation events and retain their existing import behavior.

## Verification

- Store tests prove live collisions refuse with the typed owner and reference.
- Store tests prove a matching acknowledgement records `raised_from` atomically.
- Store tests prove terminal owners do not block a later capture.
- Contract checks prove the capture and host bootstrap fields are published.
