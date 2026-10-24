# CD-0239: A later process claim refreshes the recorded host identity

- **Status:** Accepted
- **Date:** 2026-10-19
- **Scope:** Host process claims, landing refresh, and failure attribution at
  dispatch admission. Each launch carries its own authority ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).
- **Preserves:** CD-0205 D7's rule that a bare cancellation proves no cause;
- **Approval:** The operator approved the exact text of this record and of
  the amendments it makes on 2026-10-19 (CD-0230 D4), for
  [Concord (CON) issue 1188](https://linear.app/sharper-flow/issue/CON-1188).
- **Security boundary:** A caller-supplied process claim, which an authorized
  agent with shell access can arrange, feeds a recorded claim the admission checks.
  It authorizes no launch, and each launch is admitted on its own scope and safety checks ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

## Context

A worker attempt can fail because the host killed it, not because the worker
or the contract was wrong. Each launch is one admitted request on its own scope and safety checks ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)). A
launch with an unknown effect is reconciled before another launch for that
assignment.

The adapter can record `Task cancelled` with observer `host_adapter`, phase
`host_execution`, and cause `unknown`. A cancellation does not establish its
cause. A repair to a host plugin can require a host restart without changing
the work contract or its prompt files.

The store records the process identity of the host session that occupies a
worktree: the process id supplied by the adapter and its start time read from
`/proc` by the core (CD-0178 D3).

## Decision

### D1. A later recorded process claim

Each launch is one admitted request with its own scope and subject bindings, and a launch with an unknown effect is reconciled before another launch for that assignment ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

### D2. Recorded facts gate no launch

Authorization, actual start, terminal result, and result acceptance are distinct recorded facts; scope, prerequisite, ownership, and safety checks remain ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

### D3. Host observation is a loader input, and a landing refreshes it

The current host observation is an input to the single admission loader
(CD-0201 D1), not a separate admission route. The dispatch admission reads it
from current rows.
Equal current state and equal host inputs give equal answers ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

CD-0201's liveness checks keep their owners, and contract supersession keeps
its own exit; no counted `host_changed` exit or conditional host-recovery
route exists.

A same-session resume landing from a different process writes the occupancy
row with the new process identity in one transaction instead of returning the
older process as already recorded. A legacy occupancy without
process identity receives the same refresh. The core validates liveness
and derives the start time as on the existing landing route (CD-0178 D3).
The caller reads the host lease set before the dispatch transaction opens
(CD-0179 D3).

### D4. Attribution is an eligibility condition, not an authority

Recorded observations grant no launch authority; each launch is one admitted request, and attribution records what its producer observed ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

## Alternatives considered

- Compare the host prompt provenance digest of the failed and the next
  dispatch. Rejected: the digest binds prompt files, not host code. It misses a
  plugin repair, and an unrelated file edit or a worktree move changes it.
- Exempt host failures from a count. Rejected: no count exists (CD-0242 D6);
  the unknown-effect reconciliation and the scope, prerequisite, ownership,
  and safety checks bound launches.

## Consequences

A recorded process claim authorizes no launch, and each launch is admitted on its own scope and safety checks ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)). A genuine restart is evidence that new host
code can run, not proof that the fault is fixed.

The landing and host-lease CLI verbs are not signed producer attestations.
They supply a recorded process claim that the core checks against the live
process and lease; they prove neither that this session runs in that process
nor that it loaded repaired plugin code. An ordinary authorized agent with
shell access can invoke those verbs and record a process claim without
repairing or restarting its actual host. That record authorizes no launch or result acceptance; launches are admitted on their own scope, prerequisite, ownership, and safety checks ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

Recorded results do not change. A recorded failure authorizes no launch ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

## Verification

- The `host_changed` basis admission probes bind the CD-0242 D6 anchor: no
  convergence basis, escalation wall, or attempt count exists, and each
  launch is admitted on its own scope and safety checks. Until that change
  lands, `python3 scripts/check-knowledge-index.py` is the executable anchor
  for this record's law registration.
- `internal/store.TestClaimLandingRefreshesRestartedHostIdentity` must prove the
  replacement process gets a recorded landing, a repeated landing for the same
  process changes nothing, and the refreshed identity survives restore and
  reconciliation.
- `internal/agent.TestHostRestartDoesNotSupplyRetryAuthority` must prove a
  live host lease supplies no convergence credit or retry consent and leaves
  each dispatch subject to ordinary admission.
- `python3 scripts/check-knowledge-closure.py` and
  `python3 scripts/check-doc-contract.py` must pass after approval and registration.
