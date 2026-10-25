# CD-0130: Failed worker attempt recovery

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** Failed worker attempt recovery for workflow definitions released before `record_worker_failure`
- **Approval:** The operator approved this Product law addition for the
  historical-pin recovery repair.
- **Related:** CD-0013, CD-0112, CD-0124
- **Preserves:** Worker attempt identity, action
  guards, workflow steps, and operator authority.

## Context

Definitions recorded before `record_worker_failure` do not declare it. A
failed dispatched attempt blocks other delivery actions, but the step's
declared action set provides no route to record that failure. The work cannot
continue through its approved workflow.

The closure needs no definition change: Failed attempt closure is an engine concern, not a change to step policy.

## Decision

### D2. Recovery uses the current action contract

Closing a failed attempt uses the current action contract and current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### D4. All action surfaces agree

Action resolution, preflight, admission, and `next_valid_intents` use the
same recovery condition; every surface reads current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). After the failure is recorded, the recovery action is
not available for that attempt.

### D5. Existing refusals stay active

The engine refuses recovery without a failed current-step attempt. It also
refuses a wrong attempt identity, wrong epoch, stale dispatch, unauthorized
actor, duplicate record, stale work version, and terminal work.

## Verification

- A break-fix instance records a failed attempt and then admits
  `record_delivery`.
- The work pin advertises recovery before the record and removes it after the
  record.
- Existing worker-result refusal tests continue to pass.
- The implementing change binds the current-state anchor: definitions generate
  from one canonical source with no retained released version (CD-0242 D14).
