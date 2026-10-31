---
{
  "id": "CD-0093",
  "title": "Session context resolves in the claimed work directory",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "agent-adapter",
  "applies_to_domain_ids": []
}
---
# CD-0093: Session context resolves in the claimed work directory

- **Status:** Accepted
- **Date:** 2026-09-01
- **Scope:** The directory a managed session resolves; what `session-prepare`
  probes, verifies, and refuses; the claimed directory that governs context,
  identity, and registry verification
- **Approval:** The operator raised the question on 2026-09-01 after a
  launcher-started session ran against the wrong repository. The pull request is
  the public record.
- **Related:** CD-0078, CD-0079, CD-0031, CD-0049, CD-0088, issues #661, #664,
  and the closed #426, #428, #430, #466
- **Approval:** Session-preparation directory discipline: Concord (CON)
  issue CON-910 (2026-10-13), under the Snowball coordinator's
  operator-delegated approval.
- **Preserves:** CD-0078 D1's host ownership of placement, CD-0031's
  core-derived session boot, CD-0049 D4's refusal of a degraded start, and
  CD-0098 D4's session-prepare directory refusal

## Context

A host-started operator session enters managed work through
`concord_work_start`, which claims the work's canonical worktree and moves the
calling session into it (CD-0098). The claimed worktree, not a launch
selection, is where the session's context, identity check, and registry probe
must run.

The substitution defect this record closed did not begin as a managed-session
question. A session asserted an agent identity against one registry while the
host executed another, because the two steps ran in different directories.
Issues #426, #428, #430, and #466 closed earlier instances of that class, and
issue #664 recorded the cost: the refusal arrived only after the session had
already started.

One claimed directory governs the
context read, the agent definition resolution, and the registry probe, and
that directory is established before any of them runs.

## Decision

### D1. Session context resolves from the active claim and its directory

A managed session's work resolves from the session's active claim and the
directory it runs in. The continuity read binds the directory to the work's
claimed worktree. There is no launcher directory selection, no fallback
directory, and no second placement resolver. Cross-repository placement stays
with CON-771's typed session placement detail and its registered-checkout
restore.

### D2. One claimed directory governs identity, verification, and context

The claimed worktree directory is the only directory `session-prepare` uses.
Agent definition resolution, the calling-host registry probe, and the context
read all take it. The probe is one bounded bare probe under the calling
host's inherited environment (CD-0189). None of the three treats
an inherited launch working directory as work authority.

The ordering is part of the decision. The claimed directory is established by
the verified move before identity verification runs, because a verification
that runs first constrains the wrong registry.

### D3. Resolution fails closed

`session-prepare` refuses a directory other than the active claimed worktree
with a typed diagnostic (CD-0098 D4). An unreadable registry document, a
failed probe, or an interrupted probe refuses before managed action. No
managed action starts on a fallback directory or a degraded probe.

An agent handle the claimed directory's registry does not register refuses on
the same terms. CD-0049 D4 already admits no degraded start for agent
identity, and #664 records the cost of learning about a substituted executor
after the session is running rather than before.

### D4. Retired: the configured launch host command

session-prepare requires one bare probe of the calling
host in the claimed directory. A probe refusal stays typed.

### D5. Retired: the per-Project launch environment gap

A host that prepares a per-Project environment does so for its own sessions
before managed entry.
The store stays out of the host path, and no Concord surface derives a
per-Project host environment.

## Consequences

- A managed session operates on the repository directory its claim verified,
  so the agent's file tools reach the claimed tree.
- A Project with no registered canonical path cannot claim a worktree, which
  surfaces incomplete Project registration at claim time (CD-0008), not at a
  launch.
- The agent substitution class #426, #428, #430, and #466 closed stays
  closed, because the verified move and the probe share one claimed
  directory.
- CD-0079 answers the directory-to-Project question with the read-only
  `project-resolve` verb. This record opens no operator read path.

## Rejected alternatives

**Verify in one directory and act in another.** Rejected because it asserts a
property of a registry that never governs the session, which is
indistinguishable from not verifying at all.

**Fall back to an inherited working directory.** Rejected because a managed
session's directory is its verified claim, and an inherited directory is the
substitution class this record closes.

**A second placement resolver in the adapter.** Rejected because verified
placement has one owner (CD-0098). Cross-repository answers are CON-771's
typed placement detail and its registered-checkout restore, not a new
resolver.

## Verification

- `go test ./internal/store/ -run TestResolveContinuityWorkByDirectory` proves
  D1: the continuity read binds the session's directory to its active claim.
- `go test ./cmd/concord/ -run TestSessionPrepare` proves D2 and D3:
  `session-prepare` probes the calling host once in the claimed directory and
  refuses a directory other than the active claimed worktree.
- `go test ./cmd/concord/ -run TestDeterministicHostRegistryRefusalsExit2`
  proves D3: probe and registry refusals are typed and deterministic.
- `python3 scripts/check-json.py` and `python3 scripts/check-doc-links.py`
  pass.
