---
{
  "id": "CD-0093",
  "title": "Session context resolves in the host-placed work directory",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "agent-adapter",
  "applies_to_domain_ids": []
}
---
# CD-0093: Session context resolves in the host-placed work directory

- **Status:** Accepted
- **Date:** 2026-09-01
- **Scope:** The directory a managed session resolves; what `session-prepare`
  probes, verifies, and refuses; the host-placed directory that governs context,
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
  core-derived session boot, and CD-0049 D4's refusal of a degraded start

## Context

A host-started operator session enters the work directory through host tools
(CD-0088 D1). The active work directory is where the session's context,
identity check, and registry probe must run.

The substitution defect this record closed did not begin as a managed-session
question. A session asserted an agent identity against one registry while the
host executed another, because the two steps ran in different directories.
Issues #426, #428, #430, and #466 closed earlier instances of that class, and
issue #664 recorded the cost: the refusal arrived only after the session had
already started.

One work directory governs the
context read, the agent definition resolution, and the registry probe, and
that directory is established before any of them runs.

## Decision

### D1. Session preparation resolves the current Project directory

Session preparation resolves the current directory to a registered Project,
checks that the requested work belongs to that Project, and verifies its Product
scope. It requires a host-created worktree, not the main checkout. No Concord
claim or occupancy row is required. Host tools own placement; preparation neither
selects another directory nor creates a checkout.

The caller supplies the work identity explicitly; the directory establishes
Project scope, not worktree ownership or a directory-derived work identity.
Preparation reads and verifies existing state without recording a write.

### D2. One work directory governs identity, verification, and context

The current worktree directory is the only directory `session-prepare` uses.
Agent definition resolution, the calling-host registry probe, and the context
read all take it. The probe is one bounded bare probe under the calling
host's inherited environment (CD-0189). None of the three treats
an inherited launch working directory as work authority.

The ordering is part of the decision. The work directory is established by
the host before identity verification runs, because a verification
that runs first constrains the wrong registry.

### D3. Resolution fails closed

`session-prepare` refuses an unresolved Project, the main checkout, a Project
outside the work's memberships, or a mismatched Product with a typed diagnostic.
An unreadable registry document, a
failed probe, or an interrupted probe refuses before managed action. No
managed action starts on a fallback directory or a degraded probe.

An agent handle the current directory's registry does not register refuses on
the same terms. CD-0049 D4 already admits no degraded start for agent
identity, and #664 records the cost of learning about a substituted executor
after the session is running rather than before.

### D4. Probe the calling host

session-prepare requires one bare probe of the calling
host in the current work directory. A probe refusal stays typed.

### D5. The host owns its launch environment

A host that prepares a per-Project environment does so for its own sessions
before managed entry.
The store stays out of the host path, and no Concord surface derives a
per-Project host environment.

## Consequences

- A managed session operates in the host-placed directory whose Project and work
  scope preparation verified; identity and context use that same directory.
- The agent substitution class #426, #428, #430, and #466 closed stays
  closed, because the identity check and the probe share one active
  directory.
- CD-0079 answers the directory-to-Project question with the read-only
  `project-resolve` verb. This record opens no operator read path.

## Rejected alternatives

**Verify in one directory and act in another.** Rejected because it asserts a
property of a registry that never governs the session, which is
indistinguishable from not verifying at all.

**Fall back to an inherited working directory.** Rejected because a managed
session's directory must resolve to the requested work's Project. An unchecked
directory admits the substitution class this record closes.

**A second placement resolver in the adapter.** Rejected because host placement
has one owner (CD-0088 D1).

## Verification

- `TestSessionPrepareMultiProject` and
  `TestSessionPrepareRefusesForeignScopeBeforeCallbacks` cover D1: host-created
  checkouts resolve their Project without claim rows; foreign work and Product
  scope refuse before identity callbacks.
- `TestSessionPrepareProbesTheCallingHostOnce` covers D2 and D3: preparation
  probes the calling host once in the current work directory and refuses an
  unregistered agent handle.
- `go test ./cmd/concord/ -run TestDeterministicHostRegistryRefusalsExit2`
  proves D3: probe and registry refusals are typed and deterministic.
- `python3 scripts/check-json.py` and `python3 scripts/check-doc-links.py`
  pass.
