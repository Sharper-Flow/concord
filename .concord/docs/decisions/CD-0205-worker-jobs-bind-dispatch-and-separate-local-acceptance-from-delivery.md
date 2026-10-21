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
- **Amended by:** [CD-0239](CD-0239-a-later-process-claim-permits-one-bounded-host-recovery-attempt.md)
  D4 (the recorded observer and phase are an eligibility condition of the
  `host_changed` basis)
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D6, D14
- **Amended:** 2026-10-19, approved by the operator as exact text for
  [Concord (CON) issue 1221](https://linear.app/sharper-flow/issue/CON-1221).
  D4 adds the checks repair and amends CD-0148 and CD-0164: the escalation
  wall admits the one confirming review a checks repair funds, without a
  convergence basis. The change is serious under P12: an accepted ship
  verdict on that review closes a failed job's correction window, the wall
  admits a dispatch no basis bought, and the repair reason is a
  caller-supplied claim that grants authority. The Domain test counts one
  child Domain, `workflow-engine`. It changes no UI/UX.
- **Approval:** The operator approved contract version 1 of
  `work-e46f4308fb5dad191fcbe259`. That contract names CD-0164 and CD-0198 as
  modified law and adds this record.
  On 2026-10-07, the operator approved the carried edits under CD-0122 D2.
  The outside repair also serves the approved contract's explicit verification
  job binding, which the implementation-only restriction did not supply.

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

For `approval_policy=consequence_v1`, failed retry and correction require exact transactional state/evidence bindings, not operator approval. Parent-contract, immutable job/oracle, preparation, packet, receipt, delivery, integration, independence, and convergence obligations remain unchanged. Premise confirmation remains required with CD-0013 D11's class-appropriate actor. Historical pins retain their original signature requirements.


### D1. A worker-job revision is an immutable record under the work item

`record_worker_job` records one revision of a bounded job under the work
aggregate. The caller writes the objective, the stopping condition, the path
scope, the relevant predicates, the checks, the prerequisites, the unresolved
references, and the reserved integration work. The caller also states
readiness and binds its evidence.

An oracle-capable revision carries `acceptance_oracle` as immutable job
content in the same digest. Its closed graph names behavioral owners and
obligations, finite cases, and executable controls. Each job predicate has
an owner and a control; case/control references are reciprocal and resolve.
Owners name member Projects, contained source paths, entry points, approved
affected Domains, and any governing law at the contract's pinned hash.
Controls pin repository-file recipes, exact argument vectors, contained
working directories, the expected `pass` result, evidence roles, and retained
readiness references. The source union is the existing work-context union.
The store validates structure and authority joins, not semantic completeness.
An oracle beyond 32 KiB refuses whole; packet and result bounds do not change.

The core derives the parent contract version, the primary Project scope, the
next revision number, and the digest. A recording can name only predicates
that the active approved contract approved. A recorded revision never changes.

A revision is ready for dispatch when it is the latest revision of its job,
is not satisfied, has readiness evidence, has no unresolved references, and
each prerequisite revision is satisfied.

Oracle readiness references support harness availability and a nonempty
selector. Native preparation resolves the exact pinned recipe, cwd, declared
case witnesses, and runtime environment. The producer and generated schema
declare the closed set of supported recipe families, materializers, and runners.
Changes to the supported set require an approved design and executable tests
for the obligations below. Concrete members belong to code and schema, not law.
The Project keeps its own language and tests. No unrelated module is required
to satisfy a native recipe.

Preparation materializes the recipe commit, not the candidate commit. Go
preparation compiles without launching its test program. Target-runner
preparation proves the exact selected witness population or check availability
without invoking acceptance bodies. Collection may import code and run
configuration or registration hooks from the pinned recipe snapshot. These
actions grant neither verification evidence nor a worker attempt.

Each materializer owns the dependency manifest, lockfile, and cache-integrity
proof for its inputs. Candidate dependency inputs must equal the pinned
recipe bytes. A recipe with no dependencies must declare that state;
missing or unequal declared inputs produce `unavailable`, not a download
or readiness exception. Every runner pins its interpreter, executable inputs,
collection behavior, exact witness population, and result grammar.

The producer excludes ambient credentials, runner overrides, plugin autoload,
and user configuration. Only the closed materializer and runner contracts may
add declared environment inputs.
The producer resolves installed exact runtime versions and fingerprints their
executables and consumed dependency inputs. It materializes dependencies from
existing caches into per-run scratch with downloads disabled (offline mode)
for that materialization. Lock verification and dependency-integrity checks are required.
An offline or frozen option alone is not evidence of cached-byte integrity.
Dependency materialization disables build and lifecycle scripts.
A missing artifact, corrupt cache entry, unsupported runtime, or unverified
dependency produces `unavailable`, not a download or readiness exception.
Private dependency copies or a post-run integrity check must keep a run from
changing shared dependency inputs without detection.

Execution materializes the candidate commit and replaces harness, fixture,
runner configuration, and support inputs with the pinned recipe bytes.
The producer verifies subject blob identity and rechecks pinned file digests.
It fingerprints consumed source, runtime, dependencies, and executable inputs
before and after execution. A changed input cannot produce a qualified pass.

The preparation lease supplies readiness only, never verification authority.
Preparation precedes the ready job record; the native owner derives a digest
over the exact owner, case, and control bundle except its readiness references.
The recorded job must match that bundle and parent authority exactly.
Dispatch requires preparation bound to the same candidate and runtime environment.
A readiness refusal creates no attempt and spends no dispatch budget.
Unsupported recipes or unavailable inputs require explicit preparation recovery,
not an invented reference, automatic execution, or a readiness exception.

The following evidence limits apply to every recipe family, including Go:

- Same-UID changes between digest observations can escape those observations.
- Native test and collection processes have no enforced network isolation.
- `PATH`, `LANG`, and `LC_ALL` remain inherited environment inputs.
- Candidate initialization or shared-interpreter code can forge reporter output.

Native observations do not attest protection against hostile candidate code.
Independent semantic review and the existing acceptance authorities remain required.

### D2. A job-capable dispatch binds one ready revision end to end

The worker-job lifecycle applies from `workflow.implementation` version 24 and
`workflow.break_fix` version 21. Job-capable versions retain the declared recovery
routes and declare `record_worker_job` at each dispatch step that accepts
worker results and declares `record_delivery` as its explicit phase exit.

At such a step, a dispatch of any admitted lane must carry one
ready revision in `inputs.worker_job`, with the recorded content. The core
refuses a packet without it, and a packet with other content. `inputs.task`
stays the complete parent premise.

The `dispatch_worker` completion records the binding. The dispatch evidence
must carry exactly that binding. The report must claim exactly that revision.
A satisfied revision refuses a new dispatch (except the D4 confirming
review), on resume and on the first ask.

The host derives canonical report identity from the authorized packet. Worker
echoes cannot select another job, revision, attempt, or subject. The store
refuses a canonical report whose binding differs from its dispatch.

On attribution-capable definition versions, reported predicate ties are
subsets of the exact dispatched job's predicates. The complete parent
predicates remain context and whole-work obligations. A report cannot use
parent context to claim another job's predicate. Dispatches without a job
keep their declared predicate vocabulary.

Attribution records reference the existing core-observed delivery or native
run. They neither copy a subject commit into another owner nor infer a final
subject from a dispatch commit. A missing historical observation stays
missing.

Native oracle execution joins that completion to its exact `worker_attempts`
row, preceding action start and epoch, canonical packet digest, and recorded
job revision under its digest. The selected control must belong to that job.
Only `in_flight` and `dispatched` attempts admit a new oracle execution.
Missing, `completed`, or `failed` attempts refuse before test-program launch.
The completion records `worker_subject_commit` from the same tx-scoped
`subject_commit` view admitted with the packet (CD-0067 D2).
The integrated current view has one subject field, `subject_commit`, not a
second `candidate_subject` field or independently derived candidate owner.
Execution requires current clean `HEAD`, current core `subject_commit`, and
the recorded dispatch commit to be nonempty and equal.
A missing or mismatched value refuses with zero test-program launches.
A path identity, caller assertion, or current context cannot replace the
recorded commit. Completions recorded before the field refuse execution; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: a recorded absence stays absent, with no copy or inference filling it.
The producer rechecks applicability at lease acquisition, before test launch,
and at release; changed authorization cannot produce qualified pass evidence.
A finalized native replay returns retained evidence without another launch.
Managed lanes gain no Concord tools; the authenticated native host owns this
execution through the existing verify operation and its capability grant.
This observation grants neither semantic acceptance nor independent authority.
No new dispatch window, authorization row, or retry budget is implied.

An earlier definition version refuses `inputs.worker_job`. A step without
`record_worker_job` also refuses it. The lane-step join keeps its existing
capability limits. Verification requires nonempty recorded checks. The worker
receives that job's objective, checks, and stopping condition, not an instruction
to execute the complete parent premise. The dispatch binds the job to its exact
attempt and lane. Review and verification remain nonproducing capabilities.

Every admitted lane receives the same recorded oracle in `inputs.worker_job`.
Typed `oracle_receipt` members of reported evidence bind controls and cases
to the exact candidate subject, pinned recipe, result, and retained execution
references (CD-0056). The recipe identifies the harness, not the candidate;
the candidate's changed harness does not replace the pinned recipe. Earlier
receipts remain visible as regression baselines, not current-subject proof.
Independent evidence, per-predicate verdicts, and operator authority remain
required by their existing owners.

Amended 2026-10-13 for the delivery observation and the review subject
([Linear](https://linear.app/sharper-flow/issue/CON-887/one-shared-typed-work-context-packet-per-work-item-read-and-extended)):
a completed implementation-lane completion carries the store's own clean or
dirty `observed_delivery` when the store observed its claimed worktree
(CD-0067 D2); an authorization with no recorded worktree binding keeps
explicit absence. The
observation is the delivery's producer-owned Git identity, not a verdict: it
grants no semantic acceptance, no integration proof, no delivery assertion,
and no review qualification by itself. A failed attempt records no delivery
observation, probes no Git, and retains its consumed frontier.

A review dispatch retains the newest accepted implementation delivery on its
claimed worktree as the delivery anchor. The join runs through acceptance,
the exact completed attempt, and the dispatch identity. The anchor must carry
a clean core observation. The review subject is the shared qualified subject
the packet carries, and it must equal the live clean `HEAD`.

A review subject equal to the anchor's observed commit admits. Otherwise it
admits only when the core observed strict descent from that commit and no
editing dispatch on this work item is newer than the anchor's dispatch.
An editing dispatch names any lane with `edit_scoped_files`, whatever the
attempt's state: in flight, failed, rejected, completed but unaccepted, or
abandoned. The core observes descent through the existing pre-transaction
freshness preparation, outside any SQL transaction. The transaction
revalidates the sealed result and claim identity through SQL only.
The anchor never replaces the review subject. These controls do not establish
per-worker attribution of the anchor-to-subject delta (CD-0220 D5).

An accepted delivery recorded under an earlier contract version of the same
work item may host the review under the active contract. The judging
contract is the packet's own binding, which the admission admits only at the
active contract; the accepted delivery's own contract version stays readable
through the existing records. Rejected, failed, or unaccepted results cannot
replace the anchor. A delivery from another work item, Project, or worktree
cannot host the review, and a cancelled or superseded work item cannot review.
Historical completions without the observation refuse the review. The
approved remedy is a fresh implementation attempt, never a fabricated
observation. This join resets no correction budget, refunds no attempt, and
opens no approval challenge; the walls of CD-0164 and CD-0148 hold exactly
as before.

### D3. Local acceptance holds the step and asserts no delivery

An accept of a job-bound attempt that carries no delivery assertion is local
acceptance. It records the disposition of the dispatched revision, marks that
revision satisfied, and holds the step. The core derives the disposition from
the dispatch record. The caller never supplies it.

Whole-work delivery keeps its two routes: `record_delivery`, and the accept
that carries `delivery_artifact` and `delivery_state`. Both routes run one
complete delivery admission — the same function, not two. That admission
includes the fenced start, the refine proof, the declared tooling, the review
debt (including any unconfirmed checks repair), the verdicts, the operator
checkpoint, and — where the work has recorded
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

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: each launch is one admitted request with its own scope, assignment,
and subject bindings; no attempt count, correction count, convergence basis,
or escalation wall exists; and each terminal result is accepted or rejected
once.

### D5. The combined accept keeps CD-0198 D4 for delivery (amends CD-0198 D4)

On a job-capable pin the payload-blind admission of `accept_worker_result` is
local acceptance, so it does not apply the delivery admission. An accept that
carries the delivery fields applies the one delivery admission at its guard,
before any effect, with the same integration evidence, fenced start, refine
proof, review debt, verdict, and operator gates as `record_delivery`. At a
refine step, a plain accept of an attempt without a job still refuses. That
accept would assert neither a job disposition nor a delivery.

### D6. Historical pins and histories keep their behavior

[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)
governs: derived definitions are generated from one canonical source, no
released workflow-definition version is retained for history, and no
hand-maintained version, pin, or catalog list duplicates that source.

### D7. Failure attribution preserves source and authority

One typed attribution block extends existing terminal and coordinator
correction records. It separates the producer observation from core-derived
identity. The producer names its observed phase, source, error or check,
diagnosis, and causal basis. The core derives identity from the existing
dispatch, job, result, and native-run records.

Cause is unknown, a hypothesis, or a source-backed assertion. A citation alone
proves no cause. Bare cancellation proves the terminal message, not a provider
fault. A prior retry observation does not establish terminal causality. A
typed provider error records the observed fault and retains its actual source
identity and code where available.

Worker observations remain worker claims. A schema failure cannot invent an
implementation cause. Native evidence retains its native authority. A
coordinator diagnosis creates neither native evidence nor independent
verification.

Coordinator integration attribution names the failed standalone verification
run by its verify-lease or operation reference. The core resolves the run's
observed subject from the lease result, and clean unchanged runs only. The
lease records, at acquire time and in its own insert transaction, the highest
committed store sequence then visible. No Git read and no second read path
supplies it. The input population is exactly the accepted job revisions of
this work item whose acceptance store sequence is at or below that watermark,
with their digests and result references. The coordinator supplies only the
run reference, never a population, a commit, or a sequence. A mutated or
aborted run, or a run without a clean subject, cannot anchor integration
attribution and refuses with a typed cause. A lease row without the watermark
reports the population as unavailable, and no timestamp backfill ever supplies
it. A worker count, predicate union, mutable latest-job read, or timestamps
alone cannot replace that population.

The authenticated worker-evidence route binds the canonical producer-
observation digest with a versioned assertion. The core derives identity
separately. Changed content or mismatched source joins refuse before the
terminal write commits. Historical signature bytes remain exact.

An attribution block is at most 8192 UTF-8 bytes in canonical JSON. It carries
at most eight sources. Each locator is at most 512 bytes, each observed
message 2048 bytes, and diagnosis 1024 bytes. Overflow refuses whole.
Attribution cannot truncate a source into a different claim.

Attribution supplies no verification evidence, effect, retry, delivery, or
acceptance authority. [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget) governs: no attempt count, exact retry approval, convergence basis, or escalation wall exists; each launch carries its own authority, and independent verdicts and the sole delivery admission keep their owners.

Existing attempt and terminal-record reads expose attribution with its source
version. An attribution absence reads as an explicit unavailable origin. That
origin is separate from completion evidence origin. No read invents a job,
subject, source, or cause; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: no upcaster or fold exists. Admission validates the same retained structural joins without current-host reads.

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

A coordinator records a bounded job before a worker
dispatch on a new pin. The adapter selects the one ready revision from the
work pin and refuses zero or several ready revisions. A successful job gets a
disposition while the parent work stays open. No job holds an attempt budget or correction window; [CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget) governs: each launch is one admitted request with its own scope, assignment, and subject bindings. Whole-work delivery still requires the
complete delivery admission.

## Verification

```gherkin
Scenario: A dispatch binds only the recorded ready revision
  Given a break-fix version-21 item at repair with one recorded ready job
  When a dispatch carries no job, an unrecorded job, a changed digest, or changed content
  Then the core refuses each dispatch
  And a report that names another revision refuses

Scenario: Local acceptance holds the step without delivery
  Given a job-bound attempt that completed at a job-capable step
  When the coordinator accepts it without delivery fields
  Then the revision is satisfied
  And the step does not change
  And no delivery is asserted

Scenario: Forged oracle execution cannot launch a test program
  Given two persisted job authorizations with distinct attempts and packet digests
  When execution supplies an invented attempt or another job's packet digest
  Then execution refuses and the test-program launch count is zero
  And no init or test marker exists

Scenario: Missing candidate authorization refuses oracle execution
  Given a live job authorization without a provable dispatch-time candidate binding
  When the host requests oracle execution
  Then execution refuses and the test-program launch count is zero
  And no candidate identity is fabricated

Scenario: A dispatch for an older candidate cannot execute the current `HEAD`
  Given attempt A bound to a recorded commit different from current clean `HEAD`
  When the host requests execution with attempt A's valid job and packet digest
  Then execution refuses and the test-program launch count is zero
  And no init or test marker exists

Scenario: Current core candidate must agree with the dispatch and `HEAD`
  Given current clean `HEAD` equals the recorded dispatch commit
  And the current core subject_commit names a different commit
  When the host requests native oracle execution
  Then execution refuses and the test-program launch count is zero

Scenario: Review admits a descendant subject without a newer editing dispatch
  Given accepted implementation delivery A has a clean core observation
  And the qualified subject B equals the live clean HEAD
  And the core observed B as a strict descendant of A
  And no editing dispatch is newer than A's dispatch
  When the coordinator dispatches independent review of B
  Then review admits with subject B and delivery anchor A
  And A's completion and acceptance records remain unchanged

Scenario: A newer editing dispatch blocks review beyond the delivery anchor
  Given accepted implementation delivery A has a clean core observation
  And the qualified subject B equals the live clean HEAD
  And the core observed B as a strict descendant of A
  And an editing dispatch on this work item is newer than A's dispatch
  When the coordinator dispatches independent review of B
  Then the review dispatch refuses regardless of the editing attempt's state
  And the action records no durable effect

Scenario: Review refuses a subject outside the delivery anchor's descendants
  Given accepted implementation delivery A has a clean core observation
  And the qualified subject S equals the live clean HEAD
  And S differs from A and is not a descendant of A
  When the coordinator dispatches independent review of S
  Then the review dispatch refuses
  And the action records no durable effect

Scenario: Terminal acceptance refuses an unobserved subject change
  Given the current subject pin is A and the clean worktree HEAD is B
  And A differs from B
  When the operator confirms the premise or completes the work
  Then the action refuses with stale_requires_review
  And the action records no durable effect

Scenario: A launch carries its own authority with no correction window
  Given a job-bound attempt that failed or whose result was rejected
  When the coordinator admits the next launch for that assignment
  Then no attempt count, correction window, convergence basis, or escalation wall gates it
  And its scope, prerequisite, ownership, and safety checks refuse unsafe admission

Scenario: Stale job authority is refused after supersession
  Given a revision recorded under contract version 1
  When the operator supersedes the contract and a dispatch selects that revision
  Then the revision is not ready and the core refuses the dispatch
  And a new revision under the active contract is required

Scenario: Every reachable state stays live on the new versions
  Given each registered definition version
  When the admission model explores local acceptance as a held successor
  Then each reachable state reaches the terminal state
  And the recorded state equals the model after a local acceptance

Scenario: A report cannot tie evidence outside its dispatched job
  Given a job-bound dispatch whose parent declares a predicate outside the job
  When its canonical report ties evidence to that predicate on an
  attribution-capable pin
  Then completion refuses with no result credit

Scenario: Bare cancellation proves no provider fault
  Given a terminal host observation that contains only Task cancelled
  When the terminal record carries attribution
  Then its causal basis remains unknown

Scenario: A prior retry is not a terminal cause
  Given a prior retry observation and a later cancelled Task
  When the terminal record carries both observations
  Then neither source becomes a proven terminal cause

Scenario: Integration correction cites the run and its watermark population
  Given a standalone verification run of the integrated work that failed on a
  clean subject
  When the coordinator records integration correction
  Then attribution references that run and exactly the accepted input
  revisions at or below its lease watermark
  And a job accepted after the run started stays outside the population

Scenario: Unfit runs cannot anchor integration attribution
  Given a mutated or aborted verification run, or a run without a clean
  subject
  When a correction names that run as the integration anchor
  Then the anchor refuses with a typed cause and records no attribution

Scenario: A lease without a watermark reports the population unavailable
  Given a historical lease row without the acquire-time watermark
  When the coordinator attributes integration through that run
  Then the input population reports unavailable
  And no timestamp backfill supplies it

Scenario: Attribution joins read from current rows
  Given recorded attribution with its source joins
  When the store reads it back
  Then recorded source joins and unavailable origins read unchanged, with no rebuild
```

- `go test ./internal/store/ -run 'TestWorkerJobDispatchRequiresRecordedRevision|TestWorkerJobRecordingBindsApprovedContractPredicates|TestWorkerJobRecordingAuthorityAndReadinessGates'` proves the first scenario.
- `go test ./internal/store/ -run 'TestWorkerJobLocalAcceptAtRefineHoldsWithoutDelivery|TestWorkerJobDispatchRequiresRecordedRevision'` proves the second scenario.
- `go test ./internal/store/ -run 'TestCombinedAcceptanceRequiresIntegration|TestCombinedAcceptanceAdmitsBehindIntegrationEvidence|TestIntegrationCoverageRequiresEveryRequiredProject|TestIntegrationToolingRequiresDeclaredQualifyingRuns'` proves the delivery-integration half: both routes share one admission, and absent, partial, wrong-Project, stale, failed, dirty, and undeclared-tool integration all refuse while a fully proven combined route stays reachable and atomic.
- The failed-job correction-window and rewritten-revision probes bind the
  CD-0242 D6 anchor: no attempt count, correction window, or convergence basis
  exists, and each launch is admitted on its own scope and safety checks. Until
  that change lands, `python3 scripts/check-knowledge-index.py` is the
  executable anchor for this record's law registration.
- `go test ./internal/store/ -run 'TestReviewWorkerJobPredicateShape|TestReviewUnsatisfiedJobPhaseExit|TestReviewJobAuthoritySurvivesSupersession'`
  proves the closure and authority halves: `record_delivery` refuses behind
  unsatisfied jobs and unbound integration evidence, stale contract authority
  refuses dispatch, and the predicate grammar admits the report schema's short
  predicate ids.
- `go test ./internal/store/ -run 'TestWitnessUnrelatedLocalAcceptHoldsSameStepWall|TestWitnessCombinedAcceptRejectsPreResultIntegration'`
  proves a held local acceptance resets no same-step hold and a combined
  acceptance refuses integration evidence acquired before the final job
  completed; the rewritten-revision correction-authority half binds the
  CD-0242 D6 anchor: no correction authority or window exists.
- `go test ./internal/store/ -run 'TestReachableAdmissionStateReachesTerminal|TestWellFormedAdmissionStateHasNonContinuityExit|TestAdmissionConformanceLocalJobAcceptHolds'` proves the liveness scenario. The model derives the worker-job readiness, satisfaction, unresolved-obligation and integration dimensions, and the conformance check exercises the real delivery admission from the held state: `record_delivery` refuses without integration evidence and admits behind it.
- `bun test adapter/opencode/packet.test.ts adapter/opencode/dispatch_route_end_to_end.test.ts` proves the adapter selects the one ready revision and that the real route holds the step until delivery.
- `TestOwnerOracleGraph`, `TestOwnerOracleAuthorAuthorityJoins`, and `TestOwnerOracleImmutableRevisionAndReplay` cover oracle structure and authority joins; the implementing change binds the current-state anchor for immutable recorded revisions (CD-0242 D2).
- `TestOwnerOracleRequiredOnOracleCapablePin`, `TestOwnerOracleLegacyPinStaysOracleFree`, and `TestOwnerOracleJobDebt` cover version enforcement and retained obligations.
