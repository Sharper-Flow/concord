# CD-0111: A release never requires a session restart

- **Status:** Accepted
- **Date:** 2026-09-05
- **Scope:** The adapter-to-core binding a running session holds, the installer's
  retention of release directories, and the moment a breaking store migration
  applies
- **Approval:** The operator approved this decision for issue #841 on work item
  `work-c128cbe6b07ea2d61c7d105a`.
- **Related:** CD-0005, CD-0027, CD-0096, CD-0105, issue #841, issue #722
- **Amends:** `docs/agent-tool-surface-evolution.md`, acceptance criterion 3

## Context

Concord releases on every merged pull request. Thirty-two releases shipped in
the three days before this decision. The installer moves the `concord` launcher
and the `current` root to the new release and deletes the prior release
directory in the same transaction.

The adapter is plugin code loaded once per OpenCode process. It carries its
release manifest digest as a compiled constant and resolves the core binary
through `PATH` on every call. After an install, an old adapter meets a new
core, and the core refuses every call with `manifest_mismatch`. That refusal is
correct under CD-0005. The recovery it names is not: the surface-evolution
document and issue #841 both send the operator to restart the session, which
loses the session context mid-item.

The store already admits an older binary through a schema compatibility floor
(issue #722). An additive migration recorded by a newer binary does not refuse
an older one. Only a breaking migration closes that door. Two of the last eight
schema-touching merges were breaking.

## Decision

### D1. A session keeps the pair it started with

The adapter invokes the core binary at its own release path, never through
`PATH`. The release path is a generated constant beside the manifest digest.
A session that started on release N runs every call against the core of
release N until the session ends.

The digest check stays exact and fails closed. It never admits a second digest.

### D2. The installer retains a release a live session references

The installer removes a release directory only when no live host session
references it. The host owns session liveness. The installer reads the live
session set from the host and refuses the removal while any session holds the
release. The removal is retried at the next install.

An installer with no host observation removes nothing but the release it
replaced in its own transaction, and only when that release is older than the
one before it.

### D3. A breaking migration applies only through an explicit upgrade

The core never applies a breaking migration when it opens the store. An
additive migration still applies at open, under the compatibility floor of
issue #722.

A breaking migration applies through `concord upgrade`. The command refuses
while any live session holds a release older than the migration. The refusal
names each session and the release it holds. The operator chooses when to run
it.

### D4. The typed refusal never asks for a restart

No core refusal and no adapter refusal names a session restart as its
recovery. `manifest_mismatch` remains the refusal for a digest the core does
not recognize. Under D1 it is reachable only through a defect, and its recovery
action is `contact_operator` with the two digests.

## Acceptance Criteria

```gherkin
Scenario: a release installs while a session runs
  Given a session that started on release N
  When release N+1 installs on the same host
  And the session records a workflow action
  Then the core of release N accepts the action
  And the session is never told to restart

Scenario: the installer keeps a referenced release
  Given a live session that holds release N
  When the installer commits release N+1
  Then the release N directory remains on disk
  And the installer removes it at a later install once no session holds it

Scenario: a breaking migration waits for the operator
  Given release N+1 carries a breaking store migration
  And a live session holds release N
  When a session on release N+1 opens the store
  Then the store opens without the breaking migration
  When the operator runs concord upgrade
  Then the command refuses and names the session on release N
```

## Verification

- Adapter tests prove the core path is the generated release constant and
  that no call resolves `concord` through `PATH`.
- Installer tests prove retention under a live-session observation and
  bounded removal without one.
- Store tests prove open never applies a breaking migration and `concord
  upgrade` refuses under a live older session.
- The surface-evolution acceptance criterion names `contact_operator` as the
  mismatch recovery.
- `python3 scripts/check-doc-contract.py` and
  `python3 scripts/check-knowledge-index.py` pass.
