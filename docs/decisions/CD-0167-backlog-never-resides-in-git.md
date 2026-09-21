# CD-0167: Backlog never resides in git

- **Status:** Accepted
- **Date:** 2026-09-21
- **Scope:** Planning-backlog storage for every Product mode and session
- **Approval:** The operator approved this Product boundary.
- **Amends:** CD-0155 D2's local-only `todo.md` route
- **Preserves:** CD-0121's Product-scoped planning authority, a local-only
  Product's Concord planning authority, explicit Product resolution, and the
  CD-0155 refusal without a Product record

## Context

Active work resides in git. A planning backlog is not active work. A backlog
committed to a repository turns planning records into repository content, and
review, history, and public-content rules then govern a list that only the
planning authority should own.

The store already holds this boundary structurally. `databasePath()` refuses
a database override that resolves inside a git repository or worktree, and
the default path is one installation-wide file outside any repository. A
local-only Product's backlog never resides in git today.

One accepted route contradicts the boundary. CD-0155 D2 sends a foreign
session that finds work for a local-only Product to an appended `todo.md`
entry at the Project's recorded canonical path, committed with a path-scoped
commit. No code implements that route. It is the only accepted law that
writes a planning backlog into a repository.

## Decision

### D1. A backlog never resides in a git repository

A Product's planning backlog lives in the Product's confirmed Linear
destination or in Concord's store. No route may commit, append, or sync a
backlog entry into a repository. Concord's store stays structurally outside
git.

### D2. Foreign sessions record under the owning Product's scope

CD-0155 D2's local-only bullet is superseded. A foreign session that finds
work for a local-only Product records it as a Concord work item scoped to the
owning Product's Projects through the existing cross-Product recording
capability. Ownership stays correct because the item carries the owning
Product, never the serving session's Product. A missing Product record,
Project scope, or Product mode still refuses before any work item or backlog
entry is created.

This decision adds no typed operation, schema, or validator. The scoped
recording route stays policy until its own delivery adds implementation
evidence.

### D3. A local-only Product keeps its backlog

A `local_only` Product keeps Concord planning authority for planned work and
defects, including unstarted items. A missing Linear connection never removes
backlog capability and never changes the planning owner.

### D4. GitHub Issues is not a backlog destination

GitHub retains existing issue identity under the cutover policy. New planned
work for a Linear-enabled Product belongs in Linear. No mode adds GitHub
Issues as a backlog destination.

## Rejected alternatives

**Remove local-only backlog capability.** Requiring a Linear connection
before any Product can hold unstarted work revives the mandatory-provider
boundary CD-0121 rejected and leaves a fresh Product unable to record a need.

**Keep the `todo.md` route.** It is the one accepted route that commits a
backlog into a repository, and no code implements it.

**Send foreign-session work to the serving Product.** CD-0155 D1 already
refuses false ownership. Correct scoping answers the same concern without a
second record.

## Consequences

Git holds active work and nothing else. A foreign session records
other-Product work under the owning Product's scope in the shared store, so
cross-Product discovery cannot lose work and cannot create false ownership.
Local-only Products keep their planning authority. The scoped recording route
needs implementation evidence before any runtime claim.

## Verification

- Review the [development-authority contract](../development-authority.md)
  for the git-backlog prohibition, the scoped foreign-session route, and the
  preserved local-only authority.
- Run the document and knowledge validators.
- Any runtime routing implementation must prove owning-Product scoping, the
  missing-Product refusal, and the absence of any repository write in its own
  implementation tests.
