# CD-0155: Work for another Product leaves Concord

- **Status:** Accepted
- **Date:** 2026-09-16
- **Scope:** Product identity and routing of work found outside the serving Product
- **Approval:** The operator approved this Product boundary.
- **Amends:** CD-0121's Product-scoped planning boundary for foreign sessions only
- **Preserves:** The serving Product's planning authority, the other Product's recorded mode, and explicit Product resolution

## Context

A Concord session serves one resolved Product. A request, defect, or follow-up
can identify a different Product during discovery. Recording that work in the
serving Product creates a false planning record and can expose another Product's
data to the session.

CD-0121 assigns planning authority by Product mode for a session that serves
that Product. A session that serves a different Product cannot create a Concord
work item in the other Product's local database. This decision defines the
narrow foreign-session route without changing local-only authority for a session
that serves the local-only Product.

## Decision

### D1. A session records only its serving Product's work

Concord resolves the Product served by the session before it records planned
work or a defect. The session may record only work owned by that Product. It
must not create an unscoped or serving-Product work item for another Product.

### D2. Other-Product work follows that Product's recorded mode

When work belongs to another Product, the session routes it by that Product's
recorded planning mode:

- A Linear-enabled Product uses its confirmed Linear destination.
- A local-only Product receives an appended entry in `todo.md` at the Project's
  recorded canonical path. The commit is scoped to that path and leaves
  unrelated modified files untouched.

The local-only route is a foreign-session-only override to CD-0121's local-only
planning clause. A session that serves the local-only Product continues to use
Concord's local planning authority. The destination never comes from the
serving session's mode or repository path.

### D3. Product identity remains explicit

An ambiguous Product match requires an explicit operator choice before routing.
A repository path, Project, or serving session does not authorize a different
Product. Routing requires a Concord Product record for the other Product. If
that record, the destination, or the Product mode is not resolved, the session
reports missing context, refuses, and creates no work item or backlog entry.

### D4. Routing does not create serving-session Concord work

Routing work to another Product does not create a Concord work item for the
serving Product. A later session that serves the other Product may record the
work through that Product's own planning authority.

This decision adds no typed operation, schema, or validator. It changes the
Product routing policy only.

## Rejected alternatives

**Create the item in the serving Product.** This creates false ownership and
can mix planning records between Products.

**Use the serving Product's mode.** This ignores the other Product's authority
and can send work to the wrong provider.

**Create a temporary Concord item before routing.** This leaves a Concord work
record for work that the serving session does not own.

## Consequences

Product identity is a recording boundary, not only a display value. Sessions
must resolve a Concord Product record before they route work. Cross-Product
discovery can produce a Linear issue or a path-scoped `todo.md` commit, but it
cannot produce a serving-Product Concord work item. The local-only route does
not change planning for a session that serves that Product.

## Verification

- Review the [development-authority contract](../development-authority.md) for
  the serving-Product boundary, both recorded-mode routes, and the
  foreign-session-only local-only override.
- Run the document and knowledge validators.
- Any later runtime routing must prove Product-record resolution, destination
  selection, a path-scoped commit for local-only routing, preservation of
  unrelated modified files, and no serving-Product work-item effect in its own
  implementation tests.
