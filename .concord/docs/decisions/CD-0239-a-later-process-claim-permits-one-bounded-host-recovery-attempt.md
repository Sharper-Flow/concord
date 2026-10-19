# CD-0239: A later process claim permits one bounded host-recovery attempt

- **Status:** Accepted
- **Date:** 2026-10-19
- **Scope:** The convergence bases that admit a dispatch behind the
  three-attempt escalation wall
- **Amends:** [CD-0148](CD-0148-approved-fresh-worker-retries.md) (the closed
  basis families and the recorded basis tokens);
  [CD-0173](CD-0173-escalation-wall-stays-visible-gate-counts-product.md) D1
  (the pin reason for a basis that depends on the dispatching host);
  [CD-0205](CD-0205-worker-jobs-bind-dispatch-and-separate-local-acceptance-from-delivery.md)
  D7 (attribution and retry authority);
  [CD-0201](CD-0201-workflow-action-admission-is-derived-once-and-every-reachable-state-stays-live.md)
  its equal-answer consequence (equal history and equal host inputs);
  [CD-0178](CD-0178-each-repository-gets-its-own-coordinator-session.md) D3
  (a same-session landing records a replacement host process);
  [the workflow-engine contract](../workflow-engine-contract.md)
  §12.1 Correction escalation wall (the closed basis families and recorded facts)
- **Preserves:** [CD-0164](CD-0164-correction-attempt-counting-population.md)
  D1 to D4: every dispatch counts, no failure kind is exempt, the limit stays
  three, and only a productive acceptance opens a new window; CD-0148's
  single-use basis and its fresh fenced attempt; CD-0205 D7's rule that a
  bare cancellation proves no cause
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

The closed basis families of CD-0148 gain `host_changed`. The store derives it
for an escalated correction when all of these hold:

1. The latest `dispatch_worker` action completion on the work item names a
   failed attempt.
2. Its `worker.failed` terminal record carries recorded attribution with
   observer `host_adapter`, phase `host_execution`, and a host event tagged
   `terminal` or `cancellation`. A source tagged `abandonment` does not
   qualify; the recorded failure kind does not matter.
3. The active worktree claim for the dispatching session and Project carries
   a recorded host process claim. The claimed process is live with its recorded
   start time and holds a live Concord host lease. The session-to-process
   association is caller-supplied, not an authenticated host identity.
4. That process started more than one minute after the failure record's
   `occurred_at`. The minute absorbs the error of converting the `/proc` start time
   to wall-clock time.
5. No dispatch has consumed a `host_changed` basis in the current
   nonprogress window, which only a productive acceptance closes.

A failure in any other phase or from any other observer never derives the
basis. That includes an invalid worker report, which the adapter records with
observer `host_adapter` and a report phase. The cause the attribution names
does not matter: a bare cancellation keeps cause `unknown`, and the basis
claims no cause. An existing `approach_changed` or `findings_shrinking` basis
takes precedence; the new basis changes neither comparison.

### D2. The basis follows the existing basis rules

The basis follows CD-0148's single-use and fencing rules and CD-0164's count.
It is derived in the transaction that folds the dispatch, and the dispatch
consumes it. It admits exactly one fresh fenced attempt and opens no window.
Only one `host_changed` dispatch is permitted per nonprogress window, even
after another qualifying process claim. Further dispatches need another basis family.
As with the other bases at the wall, the basis admits dispatch without an
exact retry approval or a retry approval challenge. Below the wall, each pin
keeps its existing approval policy unchanged, including CD-0148's distinction
between historical exact-approval pins and `consequence_v1` pins.
All other dispatch prerequisites, including disposition of the failed
attempt, remain in force.

Dispatch completion records the token `host_changed`, the sequence of the
failure record it was derived from, and the host process id and start time
that met condition 3.

### D3. Host observation is a loader input, and a landing refreshes it

The current host observation is an input to the single admission loader
(CD-0201 D1), not a separate admission route. The dispatch fold supplies it.
A site without that observation derives no `host_changed` basis. The work pin
therefore retains `escalated_retry_requires_convergence` unless a findings or
approach basis exists. Equal history and equal host inputs give equal answers.

CD-0201 D2's abstract liveness checks count no `host_changed` exit without the
required host observation. Contract supersession remains their counted exit.
The dispatch fold's `missing_evidence` refusal names host recovery only as a
conditional route for an eligible host execution failure and an unused host
basis: repair the fault, start the host more than one minute after the failure,
resume the item, and dispatch once. It does not assert those conditions hold.

A same-session resume landing from a different process appends a new landing
event carrying that identity. Its fold updates the occupancy row instead of
returning the older process as already recorded. A legacy occupancy without
process identity receives the same refresh. The core validates liveness
and derives the start time as on the existing landing route (CD-0178 D3).
The caller reads the host lease set before the dispatch transaction opens
(CD-0179 D3). The wall-start comparison uses the existing core `/proc`
observation and its one-minute skew bound.

### D4. Attribution is an eligibility condition, not an authority

Attribution still supplies no retry authority by itself. The store reads the
recorded observer and phase of an authenticated terminal record only as
condition 2 of D1. The admission comes from the store-derived basis, which also
needs the live leased process identity and time conditions of D1.

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

A work item whose latest failure meets D1 can leave the wall with a qualifying
recorded process claim and no contract change. It gets at most one additional
counted attempt from this family before productive acceptance. File edits and
asserted causes alone supply no basis; a process predating the failure cannot
meet the time condition.

The skew bound requires starting the host more than one minute after the
failure. Waiting in a host started earlier does not change its start time.
A genuine restart is evidence that new host code can run, not proof that the fault is
fixed. A host restarted without a repair may spend the single additional
attempt and fail again. Repeated restarts cannot create an unbounded loop.
The landing and host-lease CLI verbs are not signed producer attestations.
They supply a recorded process claim that the core checks against the live
process and lease; they prove neither that this session runs in that process
nor that it loaded repaired plugin code. An ordinary authorized agent with
shell access can invoke those verbs and arrange a qualifying process claim
without repairing or restarting its actual host. This decision permits that
agent to spend at most one counted recovery attempt, not to accept a result
or reset the window. The operator approves that bounded allowance, not a
guarantee of host repair or authenticated host identity.

Recorded events do not change. Failures with absent or `legacy_unavailable`
attribution derive no host basis. An existing failure with the recorded
observer, phase, and terminal source required by D1 can qualify without a
backfill. Rebuild uses the recorded basis facts, not the current host.

## Verification

- `internal/store.TestHostChangedConvergenceAdmitsOneDispatchFromARestartedHost`
  must prove an insufficient restart interval refuses with `missing_evidence`,
  a restart beyond the interval admits one dispatch, and the recorded basis
  names the failure sequence and process identity.
- `internal/store.TestHostChangedConvergenceNeverFollowsAWorkerAttributedFailure`
  must prove a worker-reported failure and an invalid report derive no basis
  from a qualifying process claim.
- `internal/store.TestHostChangedConvergenceNeedsARestartAfterEachFailure`
  must prove the admitted attempt counts, and neither the same host nor another
  restart derives a second host basis before productive acceptance.
- `internal/store.TestClaimLandingRefreshesRestartedHostIdentity` must prove the
  replacement process gets a recorded landing, same-process replay appends
  nothing, and rebuild preserves the refreshed identity.
- `internal/agent.TestEscalatedHostRetryReadsHostLeaseBeforeAdmission`
  must prove the public boundary reads the lease before preflight, exposes
  conditional host recovery in its refusal, and replays one admitted dispatch
  without an approval ask.
- `python3 scripts/check-knowledge-closure.py` and
  `python3 scripts/check-doc-contract.py` must pass after approval and registration.
