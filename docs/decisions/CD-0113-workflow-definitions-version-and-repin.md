# CD-0113: Workflow definitions carry shipped versions and orphaned pins re-pin

- **Status:** Accepted
- **Date:** 2026-09-06
- **Scope:** the versioning of builtin workflow definitions across releases,
  the registry's compiled history, and the one-time re-pin of instance pins
  that the CD-0112 rollout orphaned
- **Approval:** The operator approved the repair contract in Concord
  work-787b91ae08b27e677be778b7 on 2026-09-06, after the v7.6.0 upgrade left
  55 live workflow instances refusing definition verification.
- **Related:** CD-0013, CD-0112
- **Extends:** CD-0013 (workflow engine mechanism) with definition version
  discipline for the builtin registry

## Context

Every builtin workflow definition was authored at Version 1. The registry keys
entries by ref and version, and verification refuses a pinned digest that
differs from the digest computed for that key.

v7.6.0 shipped the CD-0112 definitions as changed content at the same Version
1. After the upgrade, every instance pinned by an older binary refused with
`registered or pinned workflow definition drift`. Fifty-five live items could
not read continuity and could not advance. An older implementation pin in the
same database, divergent from the v7.5.x digest, shows that the pattern
predates CD-0112.

The fold guard says a definition cannot change after execution starts. The
builtin registry changed definitions in place, with no version bump and no
migration for the pins the change orphaned. The guarantee the pin exists for
was broken by the release that carried the change.

Post-upgrade captures pinned the changed content at Version 1, so two digest
populations shared one version. Version history alone cannot separate them.

## Decision

### D1. Definition content changes carry a new version

The authored builtin definitions are Version 2. The registry also compiles the
released Version-1 definitions, reconstructed to the vocabulary they shipped
with, so a pin on any shipped version verifies forever. New captures pin the
highest version. `Register` keeps its monotonic-version and same-key-digest
guards.

### D2. The pins CD-0112 orphaned re-pin once

Migration `repin_orphaned_workflow_definition_pins` runs under the fold guard.
It moves a row to its ref's Version-2 digest when the row matches no shipped
digest population and its current step is a step of the Version-2 definition
for its ref. A row the current definition cannot place stays on its recorded
digest, so verification keeps refusing it by name. A valid historical pin stays
attached to the definition it was pinned under.

### D3. A shipped digest fixture holds the registry to its versions

The digest fixture test pins every shipped ref-and-version digest to the same
literals the migration carries. A definition whose content changes while its
version stays fixed computes a digest outside the pinned set, and the fixture
fails. The migration and the fixture share their literals, so neither can
drift from the registry silently.

## Acceptance Criteria

```gherkin
Scenario: A historical pin verifies
  Given the registry with the released Version-1 definitions and the Version-2 definitions
  And a workflow instance pinned to a released Version-1 digest
  When the core verifies the pin
  Then the pin verifies against the Version-1 definition

Scenario: An orphaned pin re-pins
  Given a store that holds an instance whose digest matches no shipped population
  And whose current step is a step of the Version-2 definition
  When the re-pin migration runs
  Then the instance carries its ref's Version-2 digest

Scenario: An unplaceable pin stays
  Given a store that holds an instance whose current step is not a Version-2 step
  When the re-pin migration runs
  Then the instance keeps its recorded digest
  And verification refuses the instance by name

Scenario: Content changes without a version bump fail
  Given a definition whose computed digest the fixture pins
  When its content changes and its version stays fixed
  Then the fixture test fails
```

## Consequences

All seven builtin families author at Version 2, and new captures pin Version 2.
The fifty-five orphaned instances verify again once the migration runs: the
historical population against Version 1, the re-pinned population against
Version 2. The conformance corpus re-pins its scenario digests. An instance
whose step the current definition cannot place stays visibly refused rather
than silently moved. Future definition changes bump the version, compile the
prior version into the registry, and add fixture literals; they carry no
migration, because a pin on a shipped version never drifts again.
