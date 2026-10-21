# CD-0130: Failed worker attempt recovery under a historical definition pin

- **Status:** Accepted
- **Date:** 2026-09-09
- **Scope:** Failed worker attempt recovery for workflow definitions released before `record_worker_failure`
- **Approval:** The operator approved this Product law addition for the
  historical-pin recovery repair.
- **Related:** CD-0013, CD-0112, CD-0115, CD-0124
- **Preserves:** Released definition pins, worker attempt identity, action
  guards, workflow steps, and operator authority
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D14

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

[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) governs: definitions are generated from one canonical source, and no released definition version is retained for history.

### D2. Recovery uses the current action contract

[CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: recovery works from current rows and verified snapshots, not from a retained historical version.

### D3. Released definition pins stay unchanged

[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) governs: no released workflow-definition version is retained for history and no hand-maintained pin duplicates that source.

### D4. All action surfaces agree

Action resolution, preflight, admission, and `next_valid_intents` use the
same recovery condition; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: every surface reads current rows, with no event folding. After the failure is recorded, the recovery action is
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
- The implementing change binds the current-state anchor: definitions generate
  from one canonical source with no retained released version (CD-0242 D14).
