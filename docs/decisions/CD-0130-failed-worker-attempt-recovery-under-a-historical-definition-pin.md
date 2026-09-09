# CD-0130: Failed worker attempt recovery under a historical definition pin

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** Failed worker attempt recovery for workflow definitions released before `record_worker_failure`
- **Approval:** The operator approved this Product law addition for the
  historical-pin recovery repair.
- **Related:** CD-0013, CD-0112, CD-0115, CD-0124
- **Preserves:** Released definition pins, worker attempt identity, action
  guards, workflow steps, and operator authority

## Context

Historical workflow definitions do not declare `record_worker_failure`. A
failed dispatched attempt blocks other delivery actions, but the pinned action
set provides no route to record that failure. The work cannot continue through
its approved workflow.

Changing the historical definition would change its digest and violate
CD-0115. Moving the work to a later definition would also replace its released
pin. Failed attempt closure is an engine concern, not a change to step policy.

## Decision

### D1. The engine owns failed attempt closure

The engine resolves the current `record_worker_failure` action when the pinned
definition does not declare it and the current step holds an unrecorded failed
attempt. The attempt must follow the latest action start for that step.

### D2. Recovery uses the current action contract

Recovery uses the current closed payload, `work_transition` capability, hold
execution mode, identity checks, epoch checks, and failed-state check. It does
not advance the workflow step.

### D3. Released definition pins stay unchanged

The engine does not add the action to the historical definition. The definition
version, content, and digest stay unchanged. Work continues under its pinned
step graph.

### D4. All action surfaces agree

Action resolution, preflight, event folding, and `next_valid_intents` use the
same recovery condition. After the failure is recorded, the recovery action is
not available for that attempt.

### D5. Existing refusals stay active

The engine refuses recovery without a failed current-step attempt. It also
refuses a wrong attempt identity, wrong epoch, stale dispatch, unauthorized
actor, duplicate record, stale work version, and terminal work.

## Verification

- A break-fix version-4 instance records a failed attempt and then admits
  `record_delivery`.
- The work pin advertises recovery before the record and removes it after the
  record.
- Existing worker-result refusal tests continue to pass.
- The released definition digest table stays unchanged.
