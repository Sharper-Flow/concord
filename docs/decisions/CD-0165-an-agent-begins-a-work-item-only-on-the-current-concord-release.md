# CD-0165: An agent begins a work item only on the current Concord release

- **Status:** Accepted
- **Date:** 2026-09-21
- **Scope:** Agents that develop the Concord Product, the release currency they
  check before capture or resume, and the comparison that decides it
- **Approval:** The operator approved this decision on work item
  `work-471434ead56d66cf09071a66` in session chat on 2026-09-21.
- **Related:** CD-0111, CD-0088, CD-0098

## Context

Concord develops itself on this host. Sessions run for hours and pin the
release they boot on, under CD-0111 D1. The installer moves the `current`
root on every merged pull request, so a long session drifts behind `current`
while it works.

The version string does not track behavior. Releases v11.9.2 and v11.9.3
carry one manifest digest and one store schema version. Releases v11.3.2 and
v11.5.3 diverge from v11.9.3 on both. A currency rule built on the version
string would refuse sessions that hold identical behavior and admit sessions
that do not. `concord host-leases` emits the manifest digest and the schema
version for every live session, so the measured predicate already has a
surface.

An agent that records workflow state through a stale surface meets refusals
that look like absent capabilities. Its conduct corpus and operation manifest
are not the ones this repository ships. The operator asked for law that binds
agents to a current installation before work begins.

## Decision

### D1. The currency predicate is digest and schema against the current root

Before capture or resume of a work item for this Product, an agent compares
its held release with the installed `current` root. The comparison uses the
release manifest digest and the store schema version. It never uses the
version string. A session is current when both values match the release the
`current` root names. `concord host-leases` and the `current` symlink carry
the whole comparison, so no new surface is required.

### D2. The obligation binds at the item boundary only

The check fires before the agent captures or resumes an item. It never fires
while an item is in progress. A session that began an item on release N
completes that item on release N, under CD-0111 D1. This decision interrupts
no running work.

### D3. A stale agent does not begin the item

An agent that fails the predicate does not capture or resume the item. It
reports the two digests and the two schema versions, and it names the remedy.
The remedy belongs to the operator, whose normal path is a fresh session that
boots on the `current` root. This is a conduct obligation, not a typed core
refusal, so CD-0111 D4 stays untouched.

### D4. Currency is never measured against a remote latest

The comparison is local: the held release against the installed `current`
root. Concord releases on every merge, so a rule against the latest published
release marks a session stale minutes after boot and makes every long session
non-compliant. CD-0111 D2 keeps retained releases on disk for live sessions;
this decision adds no rule that contradicts it.

## Acceptance Criteria

```gherkin
Scenario: a session that holds the current release begins an item
  Given a session whose held release matches the current root on digest and schema version
  When the agent checks currency before capture
  Then the agent captures the work item

Scenario: a session on a divergent release is held at the boundary
  Given a session whose held release differs from the current root on manifest digest or schema version
  When the agent checks currency before capture
  Then the agent does not capture the item
  And the agent reports both digests and both schema versions

Scenario: the version string decides nothing
  Given a session that holds release v11.9.2 while the current root is v11.9.3
  And both releases carry one manifest digest and one schema version
  When the agent checks currency before capture
  Then the predicate reports current

Scenario: work in progress is never interrupted
  Given an agent inside an accepted work item
  When a newer release installs on the host
  Then no currency check fires
  And the agent completes the item on the release it started with
```

## Verification

- The record passes `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-knowledge-index.py`, and
  `python3 scripts/check-cd-allocation.py`.
- The predicate values are read from `concord host-leases` and the `current`
  root. The decision changes no code.
- `docs/decisions/CD-0111-a-release-never-requires-a-session-restart.md` is
  unchanged by this decision.
