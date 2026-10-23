# CD-0210: Outside defect repair has an explicit disposition and reconciliation

- **Status:** Accepted
- **Date:** 2026-10-07
- **Scope:** Outside-repair disposition, reconciliation, and host-owned
  repair session entry
- **Related:** CD-0122, CD-0183, CD-0111, CD-0104, CD-0189,
  [Concord (CON) issue 869](https://linear.app/sharper-flow/issue/CON-869)
- **Amends:** CD-0183 D4 only for the explicit outside-repair reconciliation
- **Extends:** CD-0122 D2 with typed entry and reconciliation routes
- **Preserves:** CD-0122 D1 and D3, ordinary managed completion gates,
  repository review and merge evidence, and host-owned permissions and roles
- **Approval:** The operator approved this law change on 2026-10-07,
  relayed by the Snowball coordinator. The public pull request carries its
  delivery evidence. This repair creates no managed workflow approval record.
- **Amended:** Concord (CON) issue CON-910 (2026-10-13) removes the operator repair command and
  its launcher, states the repair setup as host obligations, and re-scopes
  session entry as host-owned, under the Snowball coordinator's
  operator-delegated approval. D1 and D2 are unchanged.

**Approval amendment:** [CD-0243 D7](CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain unchanged.

## Context

CD-0122 D2 permits outside repair when a Concord defect blocks safe managed
execution or recording. The repair still owes an isolated branch and worktree,
a public pull request, and required checks. It receives no workflow authority.

CD-0183 D4 refuses lifecycle completion while a workflow remains live. This
protects managed completion, but leaves an outside repair without a declared
closure route. Managed continuity also keeps prompting actions on the broken
workflow. A typed disposition must separate the repair from those prompts.

## Decision

### D1. An operator-approved disposition holds live work for outside repair

`concord_work_transition.outside_repair` records a typed active disposition on
one `needed` or `in_progress` work item. The one-use operator approval binds
the work, its expected version, the reason, and the exact operation. The
approval consumption and disposition commit in one durable transaction.

The disposition preserves the workflow's current step, instance state, and
historical evidence. Continuity names the outside-repair route and suppresses
step actions and managed next intents. Managed actions and ordinary lifecycle
advancement cannot move the held work. Resuming it requires D2's declared
reconciliation action. A duplicate active disposition refuses without effect.

The disposition asserts no worker dispatch, verdict, premise confirmation, or
managed evidence. It admits historical repairs whose pull requests already
merged. Those repairs use the same approval and reconciliation gates, not
backdated workflow events.

Existing execution-safety admission still applies. Hold and completion require
stopped execution. They neither abandon live workers nor release claims beneath
running work. Unused dispatch authorizations cannot start managed execution
while the disposition holds. Failed terminal attempts remain historical facts.

### D2. One reconcile action closes or resumes the held work

`concord_work_transition.outside_repair_reconcile` accepts `completed` or
`resume`. Its one-use approval binds the work version, reason, mode, and,
for completion, the exact pull-request selectors and release tag.

Completion requires independently fetched evidence from the registered
Project's GitHub repository:

- Every selected pull request merged and names the work's confirmed Linear
  key when the work has one.
- Every required check passed on the immutable pull-request head. The
  boundary resolves the complete required set from branch protection and
  effective rulesets. It authenticates each check and its run rather than
  assigning a commit identifier to a check by inference.
- A published, non-draft, non-prerelease release names a tag that resolves
  to an immutable commit. That commit contains every selected merge.
- The recorded receipt includes native identifiers, URLs, commit identifiers,
  required results, merge and publication times, and observation provenance.

The external reads occur before the write transaction. The transaction
rechecks the work version and consumes the exact approval before it records
the receipt. Missing evidence, a failed required check, a foreign repository,
or a release without a selected merge refuses without effect.

The collector cannot certify required-workflow or deployment rules without
their separate native receipts. Such rules refuse with missing evidence.
Support for those receipts is a named follow-up, not permission to omit them.

This reconciliation is the sole amendment to CD-0183 D4. It closes the
lifecycle and enqueues the Linear status update atomically. A retained workflow
instance receives the distinct terminal state `outside_repair`, not a managed
`completed` verdict. Its step and history remain intact. The route records no
`workflow.completed`, fabricated dispatch, or managed evidence.

Resume clears the active disposition without changing the lifecycle or
workflow step. The recorded reconciliation advances the work version and
preserves its history. Managed admission and prompts then follow the unchanged
workflow. An item without an active disposition cannot reconcile.

### D3. The host opens an unpinned repair session in an isolated ordinary worktree

Concord provides no repair command or launcher. The hold, the approvals,
and the reconciliation of D1 and D2 are untouched. The host owns the repair
setup and session start, under the approved host operational procedure:

- The coordinator or operator creates the repair worktree outside the default
  checkout and outside the Concord managed worktree root, on a new branch
  from a freshly fetched default branch; it refuses an existing path or
  branch and never overwrites or removes one; it checks the new tree is
  clean and reports unexpected content.
- It starts a new unmanaged agent session there (never a reused managed
  worker session), with no inherited work selection, occupancy, or pin, and
  gives it the bounded defect plus the hold/work reference.
- The repair publishes through ordinary PR, checks, and release;
  `outside_repair_reconcile` closes the hold.

The session receives no managed boot packet, workflow pin, or inherited
managed selection, and Concord grants the repair session no workflow
authority. The host owns the repair agent and denies it Concord workflow
write tools. Concord ships no repair-agent profile. No Concord mechanism
enforces these setup guards; they rest on the host procedure. An authorized
coordinator reconciles later through D2.

## Alternatives considered

- Keep the route as prose only. Rejected: continuity still prompts the broken
  workflow and live work cannot close from ordinary repository evidence.
- Let ordinary lifecycle completion bypass the workflow gate. Rejected: it
  would weaken managed completion beyond the outside-repair disposition.
- Fabricate managed dispatch, review, or completion events after merge.
  Rejected: outside work did not produce those records.
- Launch the repair through a managed claim and pin. Rejected: it returns the
  session to the workflow that obstructs the repair.

## Acceptance criteria

```gherkin
Given a live work item and its exact one-use operator approval
When outside_repair records the active disposition
Then continuity offers zero managed step actions and zero managed next intents
And the workflow step and historical evidence remain unchanged
```

```gherkin
Given an active disposition and authenticated merged pull requests
And every required check passed on each immutable head
And a published release contains every selected merge
When outside_repair_reconcile records completed with the exact operator approval
Then the lifecycle is completed and the Linear status update is enqueued atomically
And the retained workflow instance is outside_repair without a workflow.completed event
```

```gherkin
Given an active disposition and an incomplete or mismatched external receipt
When outside_repair_reconcile requests completed
Then the work version and all completion projections remain unchanged
```

```gherkin
Given an active disposition and its exact operator approval
When outside_repair_reconcile records resume
Then managed admission resumes at the unchanged workflow step and lifecycle
```

```gherkin
Given an active operator-approved disposition
When the host creates the isolated repair worktree and starts an unmanaged repair session there
Then the session directory is outside the default checkout and the managed worktree root on a new branch
And the session receives no managed selection, occupancy, or workflow pin
```

## Consequences

Outside repairs retain ordinary pull-request, check, and release evidence.
Reconciliation records the outside outcome without pretending the managed
workflow ran. This repair itself uses CD-0122 D2 and cannot use its new route
before release.

The distinct terminal instance state requires a breaking schema migration.
CD-0111 D3's maintenance boundary and compatibility floor apply. Older live sessions
cannot cross that boundary merely because the repair is outside the workflow.
No release installation or session restart belongs to this decision's delivery.

## Verification

- `internal/store.TestOutsideRepairTypedHoldBaseReproduction` proves the
  missing typed disposition on the base and its accepted fold on this branch.
- `internal/store.TestOutsideRepairHoldRequiresApprovalNotCompletionEvidence`
  proves approval binding, suppression, and log rebuild.
- `internal/store.TestOutsideRepairCentralAdmissionSuppressesAllActionsAndLifecycleBypasses`
  proves managed admission remains held.
- `internal/store.TestOutsideRepairReconcileBothLiveLifecyclesAndReplay` proves
  atomic closure, unchanged history, and replay agreement.
- `internal/store.TestOutsideRepairResumePreservesStateAndAllowsAnotherHold`
  proves resume without changing the workflow step.
- `internal/store.TestOutsideRepairDoesNotChangeOrdinaryCompletion` preserves
  the ordinary completion gate.
- `internal/agent.TestOutsideRepairReconcileCompletedClosesFromBoundaryEvidence`
  proves the declared boundary and recorded receipt.
- The removed operator repair command is one of the refused entries in
  `go test ./cmd/concord/ -run TestRemovedLaunchEntriesRefuseWithoutExecuting`,
  which asserts exit 2, no stdin read, and no authority storage for it.
- `cmd/concord.TestDistinctReleasedCoresCoexistOnOneStore` proves compatible
  release coexistence and the new incompatible-floor boundary.
- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-law-coverage.py`, and
  `python3 scripts/check-migration-compatibility.py` validate the law artifacts
  and breaking migration declaration.
