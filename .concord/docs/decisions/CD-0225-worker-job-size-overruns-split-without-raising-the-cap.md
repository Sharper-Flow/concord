# CD-0225: Worker-job size overruns split without raising the cap

- **Status:** Superseded by [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
- **Date:** 2026-10-15
- **Scope:** Production-size bounds on editing jobs, result measurement, split lineage, and acceptance.
- **Amends:** [CD-0205](CD-0205-worker-jobs-bind-dispatch-and-separate-local-acceptance-from-delivery.md) D1-D4 for size-bound jobs and split debt; [CD-0137](CD-0137-iterative-correction-preserves-result-disposition.md) D1 for the replaced-overrun disposition; [CD-0164](CD-0164-correction-attempt-counting-population.md) D1-D2 and D4 for overrun accounting; and [CD-0148](CD-0148-approved-fresh-worker-retries.md) for split progress.
- **Preserves:** [CD-0017](CD-0017-typed-workers-and-model-routing.md) D4, [CD-0115](CD-0115-a-released-workflow-definition-never-changes-content-in-place.md), CD-0164's every-dispatch count and three-attempt threshold, and [CD-0201](CD-0201-workflow-action-admission-is-derived-once-and-every-reachable-state-stays-live.md).
- **Approval:** The operator accepted this record as written on 2026-10-15. Implementation requires a separate assignment.

## Context

A recorded worker job binds an objective and its acceptance oracle, but no production-size limit.
Text and list bounds constrain transport, not the engineering delta.
Increasing a limit after an overrun makes the original boundary meaningless.

[The assignment proposal](https://github.com/Sharper-Flow/concord/blob/a98dfe5db3fa2af53bd2d82829aec840e94e64c4/.concord/docs/research/worker-assignment-proposal.md), lines 112-116, correctly separates transport bounds from decomposition.
This decision preserves that distinction and adds a separate production-size contract.
Size requires a smaller partition when its cap cannot hold the work.
Size alone does not establish behavioral boundaries, settle interfaces, or justify incomplete acceptance.

## Decision

### D1. The recorded job owns an immutable cap and measurement basis

Every editing job under a new size-capable definition records `production_size_max`, a positive integer at most 2147483647.
The coordinator chooses the cap before dispatch.
No universal numeric cap, file-count limit, or limit on the total jobs in a work item is introduced.
Read-only jobs have no production-size cap or production-size claim.

The job digest also binds a measurement basis: an immutable Git base commit and an exact path inventory with production, test, documentation, or generated classifications.
The base is the clean integration foundation of the job, not an arbitrary recent commit.
At a job's first dispatch, it equals the admitted clean `subject_commit` before that job's authoring starts.
A job recorded before its prerequisites complete must record a fresh revision with the current foundation before dispatch.
For correction of the same unresolved obligation, keep its original base and measure its retained draft plus new repair cumulatively.
The correction's dispatch subject may contain that job's recorded unaccepted draft above the base, but no unrelated unaccepted changes.
The host observes ancestry and retained draft subjects. The store validates the job/attempt lineage, and independent review checks the actual delta attribution.
A revision cannot hide its own unaccepted work behind a later base. Accepted prerequisite work and pending job changes must remain distinguishable.
While a correction retains that base, the coordinator must serialize other jobs that would change its measured paths until the unresolved obligation is satisfied or partitioned.
Unrelated accepted work may change other paths, but cannot enter this correction's measured delta. This constraint avoids subtracting foreign changes through an unverifiable line-attribution heuristic.
The inventory covers the explicit edit paths known before dispatch.
For a newly authorized path discovered during execution, the coordinator supplies a supplemental classification through the existing result-acceptance action.
That disposition atomically records the classification with the acceptance, bound to the exact job revision, attempt, base, and delivered tree.
It does not change the job digest, path authority, cap, or an existing classification. A refused acceptance records neither effect.
The default for a new source path is production. A test, documentation, or generated exclusion requires the accepting coordinator's recorded classification review, distinct from the executor.
Later independent review checks those exclusions against the actual delta before delivery. It is not a prerequisite for the acceptance that supplies its review anchor.
The supplemental record is measurement content, never permission to edit a path.
Generated exclusions require their owning generator reference.

Production size is added plus deleted physical lines in production-classified paths between the bound base and the delivered tree.
Count blank lines and comments. Count renames as deletion plus addition.
Use Git's Myers diff with rename detection, external diffs, and text conversion disabled.
Tests, documentation, and verified generated outputs do not enter this count. Their source inputs still count when classified as production.
A production binary or an unmeasurable delta refuses acceptance as unavailable evidence, not zero lines.
This cap measures review size, not complexity, effort, or semantic correctness.

A job revision, renamed job, contract revision, retry, or split descendant cannot raise the original cap or reclassify existing production paths to evade it.
Later jobs with genuinely different obligations may choose their own caps.
The coordinator and independent review judge that distinction.

### D2. The host observes size and acceptance refuses an overrun

The result carries the reported production size, exact job binding, base, and delivered tree.
The trusted host observes the same Git delta through the existing worktree observation boundary.
Its sealed observation carries per-path added and deleted counts, so the store can apply recorded classifications without invoking Git inside a transaction.
The store requires the report and observation to agree before local acceptance or combined acceptance with delivery.
Missing, stale, mismatched, dirty, or incomplete observations do not establish a count.
Git inspection occurs outside the transaction. The transaction revalidates its sealed authority and subject bindings.

A count equal to the cap admits the size condition. A count greater than the cap refuses acceptance with typed size-overrun details.
The refusal names the job revision, cap, observed count, subject, and split recovery.
A valid overrun report remains a report. It does not become malformed output or an accepted result.
The completed attempt stays awaiting disposition. Rejection and failed-attempt recovery retain their distinct owners.
No overrun raises a cap, satisfies a job, records delivery, or resets a correction window.

### D3. The coordinator records a complete split under the original cap

The coordinator records the split through the existing worker-job authoring action, with a closed split variant.
One atomic record binds the predecessor revision and digest to two or more immutable successor jobs and their digests.
When a completed overrun attempt awaits disposition, the same record dispositions it as replaced by the split, never accepted or rejected.
This disposition satisfies no job and resets no counter. Existing rejection or terminal-failure evidence remains unchanged.
The replaced disposition is once-only and retains the exact completed report and overrun evidence. It cannot replace an existing acceptance or rejection.
Each replaced overrun joins CD-0164 D1's nonprogress population once, even if the same attempt also reported `no_ship`.
It opens unresolved predecessor debt under CD-0205 D4, even without a failure or rejection.
The same dispatch-count comparator as rejection applies to its recovery. Repeated overrun-and-split attempts therefore reach the existing escalation wall.
Successors are bounded by the existing job-reference collection limit. A larger partition uses further recorded splits.
There is no child workflow, second scheduler, or worker-written Concord state.

Each successor keeps a cap no greater than the predecessor's original cap.
The split retains the original overrun base, tree, and an evidenced allocation of its authored changes to successors.
Before a successor's first dispatch, authorized integration isolates its assigned delta from the retained draft on a clean baseline containing only already accepted prerequisite work.
Undelivered predecessor changes cannot remain in that baseline. Retain their artifacts before removing them from the execution candidate.
At that first dispatch, the successor's base equals the admitted subject. Later corrections follow D1.
Later successors record fresh revisions after prerequisite acceptance, so earlier accepted changes in a shared path do not count twice.
The immutable split mapping and allocated obligations still bind those revisions. Independent review checks that no assigned change disappeared behind a base update.
This is a preparation obligation, not permission for an automatic destructive reset or deletion of retained work.
The split records the full disposition of predecessor scope, predicates, checks, prerequisites, stopping obligations, and reserved integration.
Every predecessor oracle owner, case, and control appears unchanged in at least one successor's closed oracle graph.
Controls retain their recipe pins, arguments, expected results, and evidence roles. New coverage may strengthen them.
The coordinator records the semantic partition and its evidence. The store checks reference coverage and exact retained content, not prose equivalence.

The predecessor becomes replaced by that split, not satisfied or deleted, and cannot dispatch again.
Delivery requires every required successor leaf to be satisfied and the complete integrated result to meet the predecessor obligation.
An incomplete split, missing control, cyclic lineage, stale predecessor, or concurrent active predecessor attempt refuses without effects.
Existing prerequisite references to the predecessor resolve only after the complete split satisfies that predecessor obligation.

For split lineages only, this rule amends CD-0205 D4's byte-identical whole-job satisfaction requirement.
Failed-job and replaced-overrun debt remain attached to the predecessor until every required descendant discharges its assigned obligation and qualifying integration evidence covers their union.
This rule amends CD-0164 D2: no partial descendant acceptance is productive while that predecessor debt remains unresolved.
The existing integration-evidence binding atomically records the derived discharge of split predecessor debt when the full condition first becomes true.
That event opens a new correction window only if it discharges the last unresolved job obligation. It requires no separate agent bookkeeping call.
Its source evidence, accepted leaves, and predecessor are recorded exactly once. Rebinding the same evidence or reading a satisfied split resets nothing.
Unrelated acceptances, partial descendant acceptance, new names, and split recording never reset the window.
Every actual dispatch still counts. Exact failed-retry approval, fresh fencing, the three-attempt escalation threshold, and existing failure recovery remain unchanged.
A split cannot manufacture a retry approval or continue an immutable failed attempt.

At escalation, the closed basis vocabulary gains `split_leaf_progress` for forward progress through a recorded split only.
The latest result disposition must accept an under-cap required successor leaf with no unresolved blocking findings and no later failed or rejected attempt.
That acceptance supplies one single-use basis for the next never-dispatched required successor, bound to the same predecessor lineage and retained obligations.
It cannot fund a retry, a previously dispatched leaf, a newly invented obligation, or another split record.
The dispatch consumes the exact acceptance reference atomically and retains all counters and predecessor debt.
A split record alone provides no basis. Splitting an unproductive or failed leaf provides no basis.
A failure or rejection stops this forward-progress route. Further attempts require CD-0148's existing applicable recovery and convergence basis.
This amendment avoids trapping a successful multi-leaf partition behind its predecessor's unresolved debt without refunding failed attempts.

### D4. Change the existing contract and store owners

Implementation must extend the worker-job authoring source in `contracts/agent-tool-surface-payloads.schema.json`, packet and report schema sources, and their generated projections.
`internal/store/worker_jobs.go` owns the recorded cap, inventory, digest, split lineage, and readiness joins.
`internal/store/worker_lanes.go` owns reported measurement shape and exact attempt binding.
The existing worktree observation owner in `internal/store/worktrees.go` supplies producer-owned size evidence.
`internal/store/workflow_admission_delivery.go` and shared result admission enforce size, supplemental classifications, leaf satisfaction, and integrated predecessor debt.
The existing correction-admission owner derives and consumes the split-progress basis through the single dispatch admission.
The adapter projects these contracts and observations. It does not decide decomposition or acceptance.
`scripts/generate-agent-lanes.py` states the cap, report, and split boundary without prescribing the worker's editing method.

Released workflow definitions and historical event bytes remain immutable.
New definitions and explicit event upcasts introduce the requirement. Historical absence remains absence, never an invented cap, count, or split satisfaction.
Implementation must prove a reachable recovery for every new refusal before it enables the new definitions.

## Alternatives considered

- Raise the cap after an overrun. Rejected: the limit would govern no result.
- Use transport length or file count as production size. Rejected: neither measures the authored delta.
- Trust the worker's count alone. Rejected: a reported number is not a producer observation.
- Reset the base after authoring. Rejected: the same work would disappear from measurement.
- Delete the predecessor and create unrelated jobs. Rejected: this loses acceptance obligations and failed-job debt.
- Accept successor reports as whole-work delivery. Rejected: local success proves no integrated result.

## Consequences

The coordinator pays for a partition when a job exceeds its cap, rather than changing the cap to fit the result.
Deletion and rename-heavy work can reach the cap despite reducing code. That cost follows the chosen review-size unit.
Splits add durable lineage, but retain one work aggregate and the existing authority and correction owners.
After escalation, a first failed leaf has no findings-convergence basis. A first rejection also lacks a prior comparable finding set.
Those cases require the existing approved contract-supersession approach-change route before another attempt. A split never supplies that approval or refunds the failure.
This proposal creates no runtime behavior until the operator accepts it and a separately authorized implementation passes verification.

## Verification

- `python3 scripts/check-doc-contract.py` checks this outline and prose when the record is registered. Draft review must call its `check_record` function with proposed-record metadata before registration.
- `python3 scripts/check-knowledge-closure.py` reports an unregistered draft as unprocessed. Acceptance must register the approved revision and pass the strict closure check without claiming implementation coverage.
- `go test ./internal/store` must cover counts below, at, and above the cap, report/observation disagreement, missing classifications, binary changes, renames, generated exclusions, and changed subjects.
- `go test ./internal/store` must prove cap inheritance across revisions and split descendants, full oracle coverage, atomic split refusal, cycle refusal, original-base accounting, and prerequisite resolution after complete split satisfaction.
- `go test ./internal/store` must prove that partial split acceptance preserves debt, complete integrated satisfaction closes it, all dispatches count, and retries retain exact approval and fresh fencing.
- `go test ./internal/store` must prove that a completed overrun splits without rejection, supplemental classifications preserve the dispatched digest, and successful leaves reset no counter before the last predecessor debt discharges.
- `go test ./internal/store` must prove that the final integration binding discharges split debt and opens exactly one new window only when no unresolved obligation remains.
- `go test ./internal/store` must bound repeated overrun-and-split dispositions, count each distinct attempt once, and retain debt for a replaced overrun with no preceding failure or rejection.
- `go test ./internal/store` must reject stale ordinary bases and successors whose baseline hides allocated work, while excluding accepted prerequisite changes from a later job's size.
- `go test ./internal/store` must admit a cumulative fix-forward correction with its original base and retained draft, while refusing unrelated unaccepted changes and a base that hides that draft.
- `go test ./internal/store` must refuse overlapping job integration into a cumulative correction's measured paths until its obligation is satisfied or partitioned.
- `go test ./internal/store` must refuse reused split-progress bases, retries funded by leaf acceptance, invented obligations, and progress credit after failure or rejection.
- `bun test adapter/opencode/packet.test.ts adapter/opencode/dispatch.test.ts` must prove exact cap and result projection, whole-value refusal, and unchanged historical absence.
- `python3 scripts/generate-agent-lanes.py --check` and `python3 scripts/check-agent-contracts.py` must pass after regeneration. Independent review must verify the partition and classifications, not infer them from green schema checks.
