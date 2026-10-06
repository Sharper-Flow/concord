# CD-0205: Worker jobs bind dispatch and separate local acceptance from delivery

- **Status:** Accepted
- **Date:** 2026-10-05
- **Scope:** The worker-job revision record, its binding through the packet,
  the dispatch evidence, the report, and the acceptance; the step hold of a
  local acceptance; the correction window of a failed job
- **Refines:** CD-0164 D1 and D2, which count every dispatch and reset the
  count on any accepted result
- **Amends:** CD-0198 D4 at a job-capable refine step: a plain accept of a
  job-bound attempt is local acceptance, not a refusal
- **Preserves:** CD-0067, the core-authorized packet; CD-0115, released
  definition content and digests; CD-0148, the exact retry approval and fresh
  fencing; CD-0201, the single admission derivation and the liveness law
- **Related:** CD-0137, CD-0192
- **Approval:** The operator approved contract version 1 of
  `work-e46f4308fb5dad191fcbe259`. That contract names CD-0164 and CD-0198 as
  modified law and adds this record.

## Context

A dispatch binds an attempt identity and the complete work contract. It binds
no bounded job. The coordinator can only describe the job of an attempt in
checkpoint prose, which no check reads.

At a refine step, an ordinary accept must assert delivery (CD-0198 D4). A
successful partial job therefore cannot get its own disposition without a
whole-work delivery assertion.

CD-0164 D2 resets the correction count on any accepted result. With local
acceptance, an unrelated successful job would then erase the budget of a failed
job.

## Decision

### D1. A worker-job revision is an immutable record under the work item

`record_worker_job` records one revision of a bounded job under the work
aggregate. The caller writes the objective, the stopping condition, the path
scope, the relevant predicates, the checks, the prerequisites, the unresolved
references, and the reserved integration work. The caller also states
readiness and binds its evidence.

The core derives the parent contract version, the primary Project scope, the
next revision number, and the digest. A recording can name only predicates
that the active approved contract approved. A recorded revision never changes.

A revision is ready for dispatch when it is the latest revision of its job,
is not satisfied, has readiness evidence, has no unresolved references, and
each prerequisite revision is satisfied.

### D2. A job-capable dispatch binds one ready revision end to end

The worker-job lifecycle applies to `workflow.implementation` version 23 and
`workflow.break_fix` version 20. These versions declare `record_worker_job` at
each step where an implementation or design lane can dispatch.

At such a step, a dispatch of an implementation or design lane must carry one
ready revision in `inputs.worker_job`, with the recorded content. The core
refuses a packet without it, and a packet with other content. `inputs.task`
stays the complete parent premise.

The `dispatch_worker` completion records the binding. The dispatch evidence
must carry exactly that binding. The report must claim exactly that revision.
A satisfied revision refuses a new dispatch, on resume and on the first ask.

An earlier definition version refuses `inputs.worker_job`. A lane of another
capability class refuses it at every version.

### D3. Local acceptance holds the step and asserts no delivery

An accept of a job-bound attempt that carries no delivery assertion is local
acceptance. It records the disposition of the dispatched revision, marks that
revision satisfied, and holds the step. The core derives the disposition from
the dispatch record. The caller never supplies it.

Whole-work delivery keeps its two routes: `record_delivery`, and the accept
that carries `delivery_artifact` and `delivery_state`. Each route runs the
complete delivery admission. That admission includes the fenced start, the
refine proof, the declared tooling, the review debt, the verdicts, and the
operator checkpoint. Local acceptances and their count prove no integration
and no delivery.

Each job-capable step also declares `record_delivery`. A held step therefore
keeps its exit, and no new phase-exit action is necessary.

### D4. Only the satisfying acceptance closes a failed job's window (refines CD-0164 D1-D2)

CD-0164 D1 holds: each dispatched attempt counts, whatever its failure kind.
CD-0164 D2 changes for job-bound failures.

A job-bound attempt that fails makes its job unresolved. While a job is
unresolved, an accepted result opens a new window only when its recorded
disposition names that job. When several jobs are unresolved, the window opens
when the last of them is satisfied.

An acceptance of an unrelated job does not change the window, the count, or
the exact retry approval. The same is true for an acceptance without a job, a
new job name, a lane change, and a contract supersession. Recording a new
revision does not reset the count. An accepted result for any revision of the
same job identity satisfies the job, because the identity owns the
obligation.

A failure without a job binding keeps CD-0164 D2 unchanged. The three-attempt
limit, the two comparators of CD-0164 D4, and the approval wall of CD-0148 do
not change.

### D5. The combined accept keeps CD-0198 D4 for delivery (amends CD-0198 D4)

On a job-capable pin the payload-blind admission of `accept_worker_result` is
local acceptance, so it does not apply the delivery admission. An accept that
carries the delivery fields applies the same delivery derivation at its guard,
before any effect. At a refine step, a plain accept of an attempt without a job
still refuses. That accept would assert neither a job disposition nor a
delivery.

### D6. Historical pins and histories keep their behavior

The new versions do not change a released definition. No item moves to a new
version without its own revision. The core never creates a job from a task
string or from checkpoint prose. A history without a job binding folds and
counts as it did before. The packet, the report, the dispatch evidence, and
the completion carry the binding as an optional member, and every reader
treats its absence as no job.

## Alternatives considered

- Infer the job from checkpoint prose. Rejected: no check can read prose as
  authority.
- Make each job a separate work item. Rejected: the parent contract would lose
  the integration obligation.
- Advance the step on each accepted job. Rejected: one success would erase the
  remaining work.
- Add a caller-selected advance flag to the accept. Rejected: a second exit
  owner beside delivery admission.
- Count only failed or judgeable attempts. Rejected: CD-0164 D1 forbids free
  retries.
- Give each job its own free retry budget. Rejected: renaming a job would
  restore the budget.
- Change the released definitions in place. Rejected: CD-0115 forbids it.

## Consequences

A coordinator records a bounded job before an implementation or design
dispatch on a new pin. The adapter selects the one ready revision from the
work pin and refuses zero or several ready revisions. A successful job gets a
disposition while the parent work stays open. An unrelated success no longer
restores the budget of a failed job. Whole-work delivery still requires the
complete delivery admission.

## Verification

```gherkin
Scenario: A dispatch binds only the recorded ready revision
  Given a break-fix version-20 item at repair with one recorded ready job
  When a dispatch carries no job, an unrecorded job, a changed digest, or changed content
  Then the core refuses each dispatch
  And a report that names another revision refuses

Scenario: Local acceptance holds the step without delivery
  Given a job-bound attempt that completed at a job-capable step
  When the coordinator accepts it without delivery fields
  Then the revision is satisfied
  And the step does not change
  And no delivery is asserted

Scenario: An unrelated accepted job keeps the failed job's window
  Given a job-bound attempt that failed
  When the coordinator accepts a different job
  Then the correction count and the retry approval do not change

Scenario: Every reachable state stays live on the new versions
  Given each registered definition version
  When the admission model explores local acceptance as a held successor
  Then each reachable state reaches the terminal state
  And the real fold equals the model after a local acceptance
```

- `go test ./internal/store/ -run 'TestWorkerJobDispatchRequiresRecordedRevision|TestWorkerJobRecordingBindsApprovedContractPredicates|TestWorkerJobRecordingAuthorityAndReadinessGates'` proves the first scenario.
- `go test ./internal/store/ -run 'TestWorkerJobLocalAcceptAtRefineHoldsWithoutDelivery|TestWorkerJobDispatchRequiresRecordedRevision'` proves the second scenario.
- `go test ./internal/store/ -run 'TestUnrelatedAcceptedJobPreservesCorrectionWindow|TestSatisfyingAcceptedJobResetsCorrectionWindow|TestPartialSatisfactionPreservesOtherUnresolvedJobs'` proves the third scenario.
- `go test ./internal/store/ -run 'TestReachableAdmissionStateReachesTerminal|TestWellFormedAdmissionStateHasNonContinuityExit|TestAdmissionConformanceLocalJobAcceptHolds'` proves the fourth scenario.
- `bun test adapter/opencode/packet.test.ts adapter/opencode/dispatch_route_end_to_end.test.ts` proves the adapter selects the one ready revision and that the real route holds the step until delivery.
