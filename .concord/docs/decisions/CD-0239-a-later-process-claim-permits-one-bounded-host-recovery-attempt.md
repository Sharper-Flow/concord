# CD-0239: A later process claim permits one bounded host-recovery attempt

- **Status:** Accepted
- **Date:** 2026-10-19
- **Scope:** Host process claims, landing refresh, and failure attribution at
  dispatch admission. [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
  governs: no convergence basis, escalation wall, or attempt count exists;
  each launch carries its own authority.
- **Amends:** [CD-0201](CD-0201-workflow-action-admission-is-derived-once-and-every-reachable-state-stays-live.md)
  its equal-answer consequence (equal history and equal host inputs);
  [CD-0178](CD-0178-each-repository-gets-its-own-coordinator-session.md) D3
  (a same-session landing records a replacement host process).
  [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
  supersedes the counted-attempt relations this record made: no basis family,
  escalation wall, retry approval, or counted exit exists.
- **Preserves:** CD-0205 D7's rule that a bare cancellation proves no cause;
  [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
  governs: no dispatch count, three-attempt limit, or nonprogress window
  exists.
- **Approval:** The operator approved the exact text of this record and of
  the amendments it makes on 2026-10-19 (CD-0230 D4), for
  [Concord (CON) issue 1188](https://linear.app/sharper-flow/issue/CON-1188).
- **P12 declaration:** Serious. It removes behavior the operator sees: a host
  failure at the escalation wall no longer needs an operator-approved contract
  supersession for one more attempt, which narrows an operator approval point
  (CD-0230 D5). It touches security: a caller-supplied process claim, which an
  authorized agent with shell access can arrange, authorizes that attempt. The
  Domain test counts one child Domain, `workflow-engine`; CD-0148 and CD-0178
  are root-homed and no amended law declares applicability. It changes no
  UI/UX.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D6

## Context

A worker attempt can fail because the host killed it, not because the worker
or the contract was wrong. Three such failures escalate the correction like
any other three failures (CD-0164 D1). CD-0148 then admits a dispatch only
behind a findings basis or a changed approach. A host failure carries no
findings, so the only exit is an operator-approved contract supersession,
although the contract was not wrong.

The adapter can record `Task cancelled` with observer `host_adapter`, phase
`host_execution`, and cause `unknown`. A cancellation does not establish its
cause. A repair to a host plugin can require a host restart without changing
the work contract or its prompt files.

The store records the process identity of the host session that occupies a
worktree: the process id supplied by the adapter and its start time read from
`/proc` by the core (CD-0178 D3).

## Decision

### D1. A third basis: a later recorded process claim

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: each launch is one admitted request with its own scope and subject
bindings, no attempt count, convergence basis, or escalation wall exists, and
a launch with an unknown effect is reconciled before another launch for that
assignment.

### D2. The basis follows the existing basis rules

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: authorization, actual start, terminal result, and result acceptance
are distinct recorded facts, and no basis token, retry approval, or count
gates a launch; scope, prerequisite, ownership, and safety checks remain.

### D3. Host observation is a loader input, and a landing refreshes it

The current host observation is an input to the single admission loader
(CD-0201 D1), not a separate admission route. The dispatch fold supplies it.
[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: no `host_changed` or other convergence basis exists, and no pin
advertises an escalated retry; equal history and equal host inputs give equal
answers.

CD-0201's liveness checks keep their owners, and contract supersession keeps
its own exit; no counted `host_changed` exit or conditional host-recovery
route exists.

A same-session resume landing from a different process appends a new landing
event carrying that identity. Its fold updates the occupancy row instead of
returning the older process as already recorded. A legacy occupancy without
process identity receives the same refresh. The core validates liveness
and derives the start time as on the existing landing route (CD-0178 D3).
The caller reads the host lease set before the dispatch transaction opens
(CD-0179 D3).

### D4. Attribution is an eligibility condition, not an authority

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: recorded observations grant no launch authority; each launch is one
admitted request, and attribution records what its producer observed without
counting attempts or opening windows.

## Alternatives considered

- Compare the host prompt provenance digest of the failed and the next
  dispatch. Rejected: the digest binds prompt files, not host code. It misses a
  plugin repair, and an unrelated file edit or a worktree move changes it.
- Exempt host failures from the count. Rejected: CD-0164 rejects exemptions,
  because a broken host could then loop dispatches without bound.
- Accept any new host process, without the time condition. Rejected: a second
  process that was already running can carry the same fault.
- Leave the wall as it is. Rejected: the only exit is a contract supersession
  that the contract does not need, so an operator approves a false change.

## Consequences

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: no escalation wall, convergence basis, or counted attempt exists; a
recorded process claim authorizes no launch, and each launch is admitted on
its own scope and safety checks. A genuine restart is evidence that new host
code can run, not proof that the fault is fixed.

The landing and host-lease CLI verbs are not signed producer attestations.
They supply a recorded process claim that the core checks against the live
process and lease; they prove neither that this session runs in that process
nor that it loaded repaired plugin code. An ordinary authorized agent with
shell access can invoke those verbs and record a process claim without
repairing or restarting its actual host. [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: that record authorizes no launch, no result acceptance, and no
window reset; launches are admitted on their own scope, prerequisite,
ownership, and safety checks.

Recorded events do not change. [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: no host basis is derived from any recorded failure, and a recorded
failure authorizes no launch.

## Verification

- The `host_changed` basis admission probes bind the CD-0242 D6 anchor: no
  convergence basis, escalation wall, or attempt count exists, and each
  launch is admitted on its own scope and safety checks. Until that change
  lands, `python3 scripts/check-knowledge-index.py` is the executable anchor
  for this record's law registration.
- `internal/store.TestClaimLandingRefreshesRestartedHostIdentity` must prove the
  replacement process gets a recorded landing, same-process replay appends
  nothing, and rebuild preserves the refreshed identity.
- `internal/agent.TestEscalatedHostRetryReadsHostLeaseBeforeAdmission`
  must prove the public boundary reads the lease before preflight; the
  conditional host-recovery route its refusal names binds the CD-0242 D6
  anchor, and until that change lands `python3 scripts/check-knowledge-index.py`
  is the executable anchor for this record's law registration.
- `python3 scripts/check-knowledge-closure.py` and
  `python3 scripts/check-doc-contract.py` must pass after approval and registration.
