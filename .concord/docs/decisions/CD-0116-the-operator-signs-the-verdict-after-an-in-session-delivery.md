# CD-0116: the operator signs the verdict after a definition-backed exit

- **Status:** Accepted
- **Date:** 2026-09-08
- **Scope:** operator authority for verdicts and completion after a recorded workflow exit
- **Approval:** The operator approved the same-session amendment on 2026-09-08
  in work-14241c17803dadaa82a498c5, recorded in
  [issue #923](https://github.com/Sharper-Flow/concord/issues/923).
- **Related:** CD-0013, CD-0109, CD-0112, and issues #801, #881, #888, #923
- **Amends:** CD-0013 D5 and CD-0109's evaluator rule by admitting verified
  operator authority after a definition-backed advancing exit
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D5, D6

## Context

A coordinator can author an action start, dispatch a worker, and accept the
worker result within one session. Dispatch rotates the executing lease to
the worker. The event log still records the coordinator's earlier execution.
An unsigned verdict from that coordinator is self-evaluation, regardless of
the current lease holder.

Approval admission and self-evaluation refusal must use the same recorded
authority. A check that consults only the current lease can miss the very
caller whom the historical-execution guard refuses. Requiring a new session
or another worktree hides this disagreement instead of resolving it.

The operator owns the acceptance decision. A verified operator identity is
distinct from both the coordinator and the worker. The host can request
approval and resubmit the exact operation within the coordinator's session.
The worker report, operator decision, and verification results remain
different facts.

## Decision

D1 and D2's operator-substitution route applies to exits recorded under the consequence approval policy. [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls) governs: no separate evaluator or model distinctness exists; the executing coordinator's verdict still needs the operator approval D1 and D2 bind, and D3's worker boundary and D4's evidence gates stand.


### D1. Verified operator authority is available after a definition-backed exit

`record_verdict` and `complete` accept the verified operator identity after an
advancing action exits the step before the verdict step; the declaring actions
come from the one canonical source
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).
This includes `record_delivery`, `accept_worker_result`, research `record_report`,
and architecture `record_decision`.

A completed worker attempt alone is not an accepted delivery. An action with a
matching name in another workflow family does not satisfy the exit. Without a
definition-backed exit, the operator evaluation route refuses.

The existing host approval mechanism binds the operation digest, expected
versions, scope, session, and worktree. The operator is the recorded actor of
the verdict and of completion, and neither the caller identity nor recorded
facts are rewritten
([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

### D2. Approval admission and refusal share execution authority

The agent surface and store derive evaluation authority from the current
lease and the recorded execution and acceptance facts
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
A coordinator
that executed a step or accepted a worker result requires operator approval,
even after the lease rotates to a lane.
A read failure refuses the operation instead of implying independence.

[CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)
governs: no separate evaluator or model-distinctness requirement exists, and
an unsigned self-evaluation remains forbidden.

### D3. Worker authority ends at the report

A recorded worker actor cannot submit operator decisions or complete work.
An operator identity must not relabel a worker as a coordinator. The agent
surface refuses a worker before it requests approval, and the store refuses
the worker even if the request carries an operator identity.

The broader host-to-worker session binding remains the concern of
[issue #881](https://github.com/Sharper-Flow/concord/issues/881). This decision
does not grant unbound child sessions any new authority.

### D4. Operator acceptance does not create verification results

Every evidence, predicate, premise, and version gate remains in force.
Operator approval does not assert that an absent test passed. Deferred scope
requires an explicit contract revision, not a passing verdict on an unrun
check. A worktree move preserves identity and cannot clear execution history.

## Acceptance Criteria

```gherkin
Scenario: The coordinator completes after lane delivery
  Given a coordinator started the external-effect step and dispatched a lane
  And the lane completed with verification evidence
  And the coordinator accepted the worker result
  When that coordinator requests a verdict and supplies verified operator approval
  Then the verdict records the operator as its actor
  And the coordinator can confirm the premise and complete with verified operator approval

Scenario: In-session delivery retains the operator route
  Given an external-effect step exited through record_delivery
  When its coordinator submits a verdict with verified operator approval
  Then the verdict records the operator as its actor
  And completion accepts that operator authority

Scenario: Approval is exact
  Given an operator challenge for a coordinator's verdict
  When the signature is absent or the submitted version or verdict differs
  Then the operation refuses
  And the work version remains unchanged

Scenario: Worker authority does not expand
  Given a recorded worker actor
  When the worker submits an operator-authorized verdict
  Then the operation refuses
  And the work version remains unchanged

Scenario: A definition-backed exit is required
  Given a work item without an advancing exit into its pinned verdict step
  When operator evaluation admission runs
  Then the operation refuses

Scenario: A research report permits operator evaluation
  Given a pinned research workflow recorded its report action
  When the coordinator submits a verdict with verified operator approval
  Then the verdict records the operator as its actor

Scenario: An architecture decision permits operator evaluation
  Given a pinned architecture workflow recorded its decision action
  When the coordinator submits a verdict with verified operator approval
  Then the verdict records the operator as its actor

Scenario: Review reports and Main accepts the disposition
  Given an accepted lane result with bound evidence
  When Review submits its report citing that evidence
  Then Main records its acceptance of the disposition
```

## Consequences

The coordinator can remain in one session through acceptance and completion.
The approval path and the refusal use one evaluation-authority query instead
of competing lease and history checks. The shared query is transaction-scoped
inside the mutation that calls it, preserving the single-connection store
invariant ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
