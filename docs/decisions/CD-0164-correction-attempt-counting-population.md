# CD-0164: Correction attempts count every dispatch

- **Status:** Accepted
- **Date:** 2026-09-20
- **Scope:** The counting population behind the worker-dispatch correction
  bound; the escalation comparators in
  `internal/store/workflow_correction.go`
- **Approval:** The operator approved the objective for
  `work-c51d029c14ade1c6625a35ff` at its repair step on 2026-09-20.
- **Related:** CD-0130, CD-0133, CD-0137, CD-0148
- **Refines:** CD-0137 D3, which bounds one correction sequence at three
  worker attempts but does not name the counting population
- **Preserves:** The three-attempt limit, the operator approval wall, the
  escalated retry route, and the immutability of attempt history

## Context

CD-0137 D3 bounds one correction sequence at three worker attempts. The
implementation counts dispatched attempts in
`workflowCorrectionAttemptCount` (`internal/store/workflow_correction.go`,
lines 314-320), but no accepted decision states that population. In
particular, no decision states whether an attempt that never returned a
judgeable result consumes the bound.

A lane that treats transport and infrastructure failures as free retries
could loop dispatches without an operator. The count query carries no
failure-kind filter today. The bound holds only while an accepted decision
names the population and forbids exemptions.

Two comparators escalate corrections in the same file. The failure and
rejection path escalates on the dispatch count at line 565. The verification
path escalates on the request count at lines 371 and 529. No decision
reconciles them.

## Decision

### D1. The counting population is every dispatch

One correction sequence counts every dispatched worker attempt on the work
item since the last accepted worker result. The count joins the worker
attempt projection to the dispatch events, and an accepted result closes the
window. Every dispatched attempt consumes the bound regardless of failure
kind: worker error, abandoned, fallback blocked, model identity mismatch, and
the model readback kinds all count. No failure kind is exempt.

### D2. An accepted result resets the count

`accept_worker_result` opens a new window. A correction that starts after an
accepted result counts only the dispatches recorded after that acceptance.

### D3. The count survives contract supersession

The counting window is scoped to the work item's attempt history, not to a
contract version. Operator-approved supersession leaves the counted
dispatches in place, and the successor contract inherits the bound.

### D4. The two comparators count different populations

The failure and rejection path escalates when the dispatch count reaches the
limit (line 565, counted by lines 314-320, `count >= limit`). The
verification path escalates when the correction request count passes the
limit (lines 371 and 529, `count > limit`): it counts
`request_correction` actions since the last healthy verdict, plus the
pending request. The populations differ on purpose. An engine-dispatched
attempt may never return a judgeable result, so the failure path must count
dispatches. A verification correction exists only after a judgeable verdict,
so the verification path counts requests. This decision names both
populations and fixes each to its comparator.

## Consequences

- An infrastructure failure consumes the bound. Three transport failures
  escalate exactly like three worker errors.
- An escalated correction admits a dispatch only behind the operator
  approval wall (lines 649-651), and the escalated retry route in
  `internal/agent/mutations.go` stays unchanged.
- The limit stays three. Raising it, exempting a failure kind, or resetting
  a counter requires a new decision.

## Rejected alternatives

- **Count only attempts that returned a judgeable result.** Infrastructure
  failures would become free retries, and a broken host could loop
  dispatches without an operator.
- **Scope the count to the active contract version.** A successor contract
  would erase the counted attempts mid-sequence and grant a fresh bound for
  the same defect.
- **Share one comparator for both paths.** The failure path has no verdicts
  to count when a worker fails early, and the verification path has no
  dispatches of its own to count. Each path counts the population it can
  observe.

## Verification

- `internal/store.TestInfrastructureFailureKindConsumesCorrectionAttemptBound`
  proves a `fallback_blocked` failure consumes the bound, the third such
  failure escalates, and the work pin removes `dispatch_worker`.
- `internal/store.TestAcceptedWorkerResultResetsCorrectionAttemptCount`
  proves an accepted result opens a fresh window and the next failure counts
  one.
- `internal/store.TestCorrectionAttemptCountSurvivesContractSupersession`
  proves the counted dispatches survive an operator-approved supersession
  and still escalate under the successor.
