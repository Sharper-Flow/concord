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
that carries `delivery_artifact` and `delivery_state`. Both routes run one
complete delivery admission — the same function, not two. That admission
includes the fenced start, the refine proof, the declared tooling, the review
debt, the verdicts, the operator checkpoint, and — where the work has recorded
worker jobs — the satisfied required jobs and the integration evidence. Local
acceptances and their count prove no integration and no delivery:
`record_delivery` refuses while a required job's latest revision is
unsatisfied, and refuses without qualifying core-owned worktree verification
evidence: a green, unchanged-tree verify run of every required Project, bound
after the required job acceptances, after every required job's recorded
result population, and after the phase start, in log order. The result
population anchors the combined route too: a run acquired before the final
pending job completed did not observe the integrated whole, whatever its
binding order. A
binding that merely mentions an acceptance, the acceptance count, or
timestamps alone prove nothing. The combined accept counts its own accepting
revision as satisfied for the population check — its pending disposition
derives from the accepting attempt's exact dispatched revision and report —
but that pending satisfaction never supplies integration, so a combined
acceptance behind missing integration evidence refuses exactly as
`record_delivery` does.

Each job-capable step also declares `record_delivery`. A held step therefore
keeps its exit, and no new phase-exit action is necessary.

### D4. Only the satisfying acceptance closes a failed job's window (refines CD-0164 D1-D2)

CD-0164 D1 holds: each dispatched attempt counts, whatever its failure kind.
CD-0164 D2 changes for job-bound failures.

A job-bound attempt that fails, or whose completed result is rejected, makes
its recorded revision unresolved. While a revision is unresolved, an accepted
result opens a new window only when its recorded disposition names that job
and carries the same recorded obligation. When several revisions are
unresolved, the window opens when the last of them is satisfied.

An acceptance of an unrelated job does not change the window, the count, or
the exact retry approval. The same is true for an acceptance without a job, a
new job name, a lane change, and a contract supersession. Recording a new
revision does not reset the count. An acceptance satisfies an unresolved
revision only when it discharges the recorded obligation itself: the accepted
revision must carry the same objective, stopping condition, scope, predicates,
checks, prerequisites, unresolved references, and reserved integration work as
the unresolved revision. Job identity alone is not satisfaction — a rewritten
revision with an unrelated objective carries a different obligation and leaves
the failed revision's window open. The satisfying routes are to retry the
unresolved revision itself under the CD-0148 exact approval, or to re-record
the same obligation as a new revision and accept that.

A recorded revision also keeps the parent contract authority it was recorded
under. After a contract supersession, a revision recorded under the superseded
contract is no longer ready and the core refuses its dispatch until a new
revision is recorded under the active contract.

A failure without a job binding keeps CD-0164 D2 unchanged. The three-attempt
limit, the two comparators of CD-0164 D4, and the approval wall of CD-0148 do
not change.

### D5. The combined accept keeps CD-0198 D4 for delivery (amends CD-0198 D4)

On a job-capable pin the payload-blind admission of `accept_worker_result` is
local acceptance, so it does not apply the delivery admission. An accept that
carries the delivery fields applies the one delivery admission at its guard,
before any effect, with the same integration evidence, fenced start, refine
proof, review debt, verdict, and operator gates as `record_delivery`. At a
refine step, a plain accept of an attempt without a job still refuses. That
accept would assert neither a job disposition nor a delivery.

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
  Given a job-bound attempt that failed or whose result was rejected
  When the coordinator accepts a different job, or a rewritten revision of the same job
  Then the correction count and the retry approval do not change
  And only an acceptance carrying the same recorded obligation closes the window

Scenario: Stale job authority is refused after supersession
  Given a revision recorded under contract version 1
  When the operator supersedes the contract and a dispatch selects that revision
  Then the revision is not ready and the core refuses the dispatch
  And a new revision under the active contract is required

Scenario: Every reachable state stays live on the new versions
  Given each registered definition version
  When the admission model explores local acceptance as a held successor
  Then each reachable state reaches the terminal state
  And the real fold equals the model after a local acceptance
```

- `go test ./internal/store/ -run 'TestWorkerJobDispatchRequiresRecordedRevision|TestWorkerJobRecordingBindsApprovedContractPredicates|TestWorkerJobRecordingAuthorityAndReadinessGates'` proves the first scenario.
- `go test ./internal/store/ -run 'TestWorkerJobLocalAcceptAtRefineHoldsWithoutDelivery|TestWorkerJobDispatchRequiresRecordedRevision'` proves the second scenario.
- `go test ./internal/store/ -run 'TestCombinedAcceptanceRequiresIntegration|TestCombinedAcceptanceAdmitsBehindIntegrationEvidence|TestIntegrationCoverageRequiresEveryRequiredProject|TestIntegrationToolingRequiresDeclaredQualifyingRuns'` proves the delivery-integration half: both routes share one admission, and absent, partial, wrong-Project, stale, failed, dirty, and undeclared-tool integration all refuse while a fully proven combined route stays reachable and atomic.
- `go test ./internal/store/ -run 'TestUnrelatedAcceptedJobPreservesCorrectionWindow|TestSatisfyingAcceptedJobResetsCorrectionWindow|TestPartialSatisfactionPreservesOtherUnresolvedJobs'` proves the third scenario's identity half.
- `go test ./internal/store/ -run 'TestReviewRejectedJobWindow|TestReviewRewrittenRevisionResetsFailedJob|TestReviewWorkerJobPredicateShape|TestReviewUnsatisfiedJobPhaseExit|TestReviewJobAuthoritySurvivesSupersession'` proves the third and fourth scenarios' closure, integration, and authority halves. A rejected result joins the unresolved window. A rewritten revision cannot discharge it. `record_delivery` refuses behind unsatisfied jobs and unbound integration evidence. Stale contract authority refuses dispatch. The predicate grammar admits the report schema's short predicate ids.
- `go test ./internal/store/ -run 'TestWitnessUnrelatedLocalAcceptHoldsSameStepWall|TestWitnessRewrittenJobKeepsExactRetryBinding|TestWitnessCombinedAcceptRejectsPreResultIntegration'` proves the exact retry-binding half the review probes established: a held local acceptance resets no same-step wall, a rewritten revision consumes no correction authority, and a combined acceptance refuses integration evidence acquired before the final job completed.
- `go test ./internal/store/ -run 'TestReachableAdmissionStateReachesTerminal|TestWellFormedAdmissionStateHasNonContinuityExit|TestAdmissionConformanceLocalJobAcceptHolds'` proves the liveness scenario. The model folds the worker-job readiness, satisfaction, unresolved-obligation and integration dimensions, and the conformance replay exercises the real delivery admission from the held state: `record_delivery` refuses without integration evidence and admits behind it.
- `bun test adapter/opencode/packet.test.ts adapter/opencode/dispatch_route_end_to_end.test.ts` proves the adapter selects the one ready revision and that the real route holds the step until delivery.
