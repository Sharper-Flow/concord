# CD-0115: A released workflow definition never changes content in place

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** the versioning discipline of the builtin workflow definitions:
  what a release may do to a definition an instance has pinned
- **Approval:** The operator approved the law-only successor contract in
  Concord work-1dd312bb395cc5aa474cb2d9 on 2026-09-06, after two drift
  incidents left live instances refusing definition verification.
- **Related:** CD-0013, CD-0112, and issues #861, #863
- **Extends:** CD-0013 (workflow engine mechanism) at the registry's version
  discipline; the mechanism shipped in #863

## Context

A workflow instance pins the definition it was created under: ref, version,
and digest. Verification refuses a read or a dispatch when the pinned digest
differs from the digest the registry computes for that ref and version.

Twice, a release changed a pinned definition's content while the version
stayed fixed. CD-0112 moved every version-1 digest at the v7.6.0 release, and
an older implementation pin in the same store proves the pattern predates it.
Each time, every instance pinned by an earlier binary refused with
`registered or pinned workflow definition digest drifted`. After v7.6.0, 158
live instances could not read continuity and could not advance.

Issue #863 repaired the damage and shipped the mechanism: the version-1
definitions are frozen in `internal/store/workflow_registry_versions.go`, the
changed graphs registered as version 2, and
`internal/store/workflow_definition_version_pins_test.go` pins the digest of
every registered version. A rule that a test enforces but no accepted record
states is a convention, not law. This record states it.

## Decision

### D1. A released definition's content is frozen

A builtin workflow definition that a release shipped is immutable at its
version. A content change ships as a new version number in the same release
that carries the change. No release edits a released definition in place.

### D2. The registry compiles every released version

The builtin registry registers every version of every family that any
instance may pin, for as long as any store may hold such an instance. A pin
on any released version verifies forever, and new captures pin the highest
version.

### D3. The version pins are the enforcement evidence

`workflow_definition_version_pins_test.go` holds the digest of every
registered version and fails when a computed digest drifts from its pin. A
definition change adds a new row to that table with a new version; it never
rewrites an existing row.

## Acceptance Criteria

```gherkin
Scenario: Content changes ship as a new version
  Given a builtin definition released at version N
  When a change must alter its content
  Then the release registers the changed content at version N+1
  And the version pins table gains a row for version N+1

Scenario: A released version verifies forever
  Given a workflow instance pinned to a released version and digest
  When the core verifies the pin on any later release
  Then the pin verifies against the registry's copy of that version

Scenario: An in-place edit fails
  Given a builtin definition released at a pinned digest
  When its content changes and its version stays fixed
  Then the version pins test fails
```

## Consequences

A definition change carries its version bump, its registry history, and its
pins-table row in one release; a reviewer sees all three or none. The three
families no instance ever pinned keep version 1 for their current content, as
#863 shipped them. A store that holds an instance no release can verify keeps
refusing it by name, so the gap stays visible instead of silent.
