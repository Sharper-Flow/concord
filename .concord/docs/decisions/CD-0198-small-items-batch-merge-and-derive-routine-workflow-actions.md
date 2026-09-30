# CD-0198: Small items batch, merge, and derive routine workflow actions

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** The typed workflow action surface of `workflow.implementation` and
  `workflow.break_fix`: the `record_verdict` payload, the three diagnosis record
  actions, the fenced step starts, the delivery assertion's entry route, and the
  worker-failure record with its retry dispatch
- **Amends:** CD-0156 D1 and D2 at their `workflow.break_fix` placement only
- **Related:** CD-0012, CD-0166, CD-0173, CD-0183, CD-0192
- **Approval:** The operator approved the work item contract that carries this
  record on 2026-09-30
  ([Concord (CON) issue 767](https://linear.app/sharper-flow/issue/CON-767/decide-which-coordinator-workflow-actions-merge-or-become-core-derived)).

## Context

Research issue
[Concord (CON) issue 766](https://linear.app/sharper-flow/issue/CON-766/which-coordinator-calls-and-workflow-actions-can-a-small-work-item)
measured what a completed break_fix item costs in coordinator calls per workflow
action. The query read `workflow.action_completed` events over 341 items
completed since 2026-09-15. `record_verdict` averaged 4.49 calls per item
against contracts that average 4.83 approved predicates, the three diagnosis
records averaged 2.53 together, `start_refine` 0.90, `start_repair` 0.75,
`record_delivery` 1.52, and the two correction records 0.81. A small item pays
most of its coordination cost in calls that record facts the store could carry
in fewer, larger, or derived records.

CON-766 ranked five candidates that answer this: one `record_verdict` call
carries a verdict per approved predicate, the three diagnosis records merge into
one typed action, the start of repair and refine derives from the first action
at the step, an accepted report that carries the delivery artifact records the
delivery, and the worker-failure record derives while a rejection can carry its
retry dispatch. Each candidate changes the typed workflow action surface, so
each needs a decision record that argues its evidence floor against CD-0012 D7,
CD-0156, CD-0183 D2, CD-0166, and CD-0192 D1, and names one implementation item
per adopted candidate.

Four floors bound every candidate. Contract approval stays an approved,
operator-signed act. Evidence stays durably bound before the gates that require
it. The verdict stays independent of the executing agent under CD-0012 D7. The
human checkpoint at the verdict step stays. Removing any of the four is out of
scope, as are the tool-schema refusal fixes that CON-408 owns.

## Decision

This record adopts all five candidates. None removes one of the four floors,
and each names one implementation item here.

### D1. One record_verdict call carries a verdict for every predicate it judges

The `record_verdict` payload gains a batched form that carries one or more
verdict objects. Each object names one approved predicate of the active contract
and carries its own `verdict_kind`, its own evaluation evidence, and its own
incomparable flag. Duplicate predicate ids refuse. The fold appends one typed
verdict event per predicate in the same transaction.

The evidence floor holds against CD-0012 D7 because the batch changes how many
calls carry verdicts, not who produces them. Every verdict in the batch carries
its recorded actor, and the completion transition still refuses a verdict whose
actor is the executing actor of the item. The batch records evaluations of
predicates the approving authority authored at planning, and authors nothing, so
authorship fencing is untouched. The completion gate keeps demanding a verdict
for every approved predicate, so a predicate the call omits stays missing and
named, never silently excused.

Implementation item: [CON-772](https://linear.app/sharper-flow/issue/CON-772/batch-per-predicate-verdicts-in-one-atomic-record-verdict-call),
`work-bd63058d9040b113a411577d`. Extend the `record_verdict` payload contract to the
batched form, and raise every workflow family that declares the action to its
next definition version with prior versions pinned.

### D2. The three diagnosis records merge into one typed action on break_fix

`record_reproduction`, `record_alignment`, and `record_root_cause` fold into one
advance action, `record_diagnosis`. Its payload carries a reproduction section,
an alignment-search section, and a root-cause section. Each section is required
and carries the payload fields and consistency guards its source action carried,
including the CD-0156 D3 `searched`, `outcome`, and `related_ids` fields with
their guards. The reproduce, alignment, and diagnose steps fold into one
diagnosis step whose advance actions admit `record_diagnosis` alone, so its
single forward edge cannot be crossed without all three sections recorded.

This amends CD-0156 D1 and D2 at their `workflow.break_fix` placement only. The
alignment step leaves the break_fix graph, and the step-shape obligations of
CD-0156 narrow to `workflow.implementation`, which keeps the alignment step and
`record_alignment` unchanged. The floor holds against CD-0156 D3 because
passing the diagnosis step stays the proof the search happened, and the section
enum still separates checked-and-found-nothing from never-checked. The
projection fold writes the same `workflow_backlog_alignment` rows from the
alignment section, and CD-0156 D5 keeps its force: the merged action records the
candidate set and creates no relation.

CD-0156 D2 placed the search before the analysis so a duplicate is found before
effort is spent on it. The merged action turns that ordering from a graph edge
into a payload duty: the search section is required in the same recorded act as
the analysis, so no analysis can be recorded without a search beside it. The
effort saving of an earlier search narrows, and this record accepts that cost
because the evidence floor and the mandatory record both survive.

Implementation item: [CON-776](https://linear.app/sharper-flow/issue/CON-776/merge-break-fix-reproduction-alignment-and-root-cause-into-record),
`work-3d04acde6b5f5f90debdcc91`. Fold the reproduce, alignment, and diagnose steps of
`workflow.break_fix` into one diagnosis step with `record_diagnosis` as its only
advance action, on the next definition version with prior versions pinned.

### D3. The start of repair and refine derives from the first action at the step

`start_repair` and `start_refine` stop being callable actions. The fold that
applies the first workflow action at the `repair` or `refine` step derives the
step-start fact in the same transaction, and the first start in log order wins.
This follows the mechanism CD-0183 D2 accepted for `execution_started_at`, so
the change extends an accepted derivation instead of inventing a second start
fact, and RebuildFromLog derives the same value.

The derived start keeps every duty the callable start carried. It opens the step
epoch. It binds the step fence from the first action's own actor. The delivery
admission guard keeps requiring a start at the delivery-bearing step, and the
CD-0192 D2 epoch bound reads the derived refine start, so a verify run acquired
before it stays stale. Live instances backfill from the lowest-sequence
qualifying action, the migration pattern CD-0183 D2 used.

Implementation item: [CON-773](https://linear.app/sharper-flow/issue/CON-773/derive-repair-and-refine-start-facts-from-their-first-workflow-action),
`work-2126c8ce04a66f64f73d0cbd`. Derive the step-start fact in the action fold on both
families, remove the two callable starts, and repoint the delivery admission and
refine-proof guards at the derived fact, on the next definition version with
prior versions pinned.

### D4. An accepted report that carries the delivery artifact records the delivery

`accept_worker_result` gains optional delivery fields. When an accept at a
delivery-bearing step carries `delivery_artifact`, it first reuses the complete
delivery-admission mechanism that `record_delivery` uses. The admission checks
the fenced step start, the refine-exit proof, and the post-rejection review gate
before any acceptance, delivery event, or step advance. The fold appends the same
typed delivery event a `record_delivery` call appends, with `delivery_state`
`asserted`, and advances the step, so the separate call at that step disappears.

CD-0192 D1 holds because both entry routes run the same refine-exit proof guard.
An acceptance that exits refine requires bound verification evidence in the
current refine epoch. That evidence names a completed green `worktree_verify`
run for this item, acquired after the refine start, with exit code zero and no
tracked-file change. When the default ref declares tooling, the run's argv must
match a declared invocation. Both routes resolve tooling from the same default
ref and use the same admission implementation. A missing, stale, failed, or
undeclared run refuses the whole acceptance-and-delivery operation without
acceptance, delivery, or advance. Appending a delivery event alone is not proof
that admission ran.

The floor holds against CD-0166 because the assertion changes its entry route,
not its owner. CD-0166 D1 holds because `accept_worker_result` is a coordinator
action, so the coordinating agent still owns the truth of the assertion, and the
core still observes nothing. CD-0166 D2 holds because the delivery gate keeps
`record_delivery` as its declared exit and runs no accept. CD-0166 D3 holds
because the unreconciled refusal and the parked read read delivery events, and
the derived event is one. The CD-0166 D6 post-rejection refusal names the
recorded delivery of a step, and a delivery derived at the refused step refuses
with it.

Implementation item: [CON-774](https://linear.app/sharper-flow/issue/CON-774/let-accepted-worker-results-assert-delivery-through-the-existing),
`work-f052c024a1075d56e030cade`. Extend the `accept_worker_result` payload and fold on both
families, reusing the complete delivery-admission mechanism on the new entry
route. Regression checks on both routes must prove refusal for missing, stale,
failed, dirty, and undeclared verification runs, and for a missing start or
unsatisfied post-rejection review. A valid bound run permits the combined route
and records acceptance, delivery, and advance atomically. Raise the definition
version with prior versions pinned.

### D5. The worker-failure record derives, and a rejection can carry its retry dispatch

Two reductions share the correction route. First, when a dispatched attempt
reaches its terminal failed lifecycle at a workflow step, the fold records the
workflow failure record from the attempt's own terminal event. The correction
diagnosis fills from the failure detail the attempt already carries, and the
strategy fills from the standing default, which are the values the correction
context applies today when the caller omits them. The record stops being a
callable action and the coordinator call disappears.

Second, `reject_worker_result` gains an optional retry dispatch in its payload.
The fold appends the rejection and the dispatch in one transaction. The retry
mints a fresh fenced attempt identity, and the dispatch binds the same packet
rules every dispatch binds. When the rejection is the attempt that reaches the
CD-0173 escalation wall, the combined call refuses and a rejection without the
dispatch payload still records, so the wall keeps its operator-owned escape.

The floor holds because the correction context, the attempt count, the limit,
and the wall read the derived record exactly as they read the recorded one, and
the dispatch rules keep their single source in the dispatch packet.

Implementation item: [CON-775](https://linear.app/sharper-flow/issue/CON-775/derive-worker-failure-records-and-combine-result-rejection-with-retry),
`work-1b0708f1de8cd8328b920e38`. Derive the failure record in the attempt terminal fold, and
extend `reject_worker_result` with the optional dispatch, on the next definition
version of the families that declare the actions, with prior versions pinned.

## Alternatives considered

- Remove contract approval or the verdict checkpoint to save the most calls.
  Refused: the objective bounds both out, and CD-0012 D7 and D9 make them
  load-bearing.
- Replace the small records with one free-form progress action. Refused: a
  generic call cannot carry typed payloads, so every per-action guard weakens
  into prose, and the verb dilution CD-0012 D3 refuses follows.
- Let confirm_premise carry the verdicts beside the operator confirmation.
  Refused: it blurs the independent verdict into the approval act and hides
  which actor produced which verdict.
- Derive the delivery assertion from an observed merge instead of the
  coordinator's accept. Refused: CD-0166 D1 sets the boundary that the
  coordinating agent owns the assertion and the core observes nothing.
- Derive the worker-failure record with no correction context at all. Refused:
  the escalation wall and the retry fences read that context, and dropping it
  deletes validation rather than a call.

## Consequences

- A small break_fix item spends about 6 to 9 fewer coordinator calls, and no
  accepted floor loses a check.
- The verdict, delivery, and failure events keep their kinds and payload
  fields, so projections, receipts, and read surfaces fold unchanged.
- Each implementation item changes an agent-visible typed action surface, so
  each carries the generated-manifest, digest, schema, scenario, and
  classification cost that agent-tool-surface-evolution attaches, plus a
  definition version bump with prior versions pinned per CD-0115.
- The alignment step leaves the break_fix graph: a reader that navigates steps
  must use the merged diagnosis step, and the CD-0156 step-shape tests narrow to
  `workflow.implementation`.
- The start of repair and refine becomes a derived fact, so a derivation defect
  would misplace the fence or the refine epoch. The implementation item must
  prove the derived start matches the old event order under rebuild.
- The batched verdict payload and the merged diagnosis payload grow, and the
  fold is one transaction, so a malformed section refuses the whole call and the
  caller retries the call rather than one section.
- Each implementation item named above carries its own verification contract
  when its work dispatches. This record decides and implements nothing.

## Verification

- `python3 scripts/check-doc-contract.py` proves this record carries the current
  decision outline and passes the writing rules.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove this record registers with
  a current content hash and no unprocessed document remains.
- `python3 scripts/check-law-coverage.py` proves the coverage record's state
  obligations hold, and `python3 scripts/update-issue-state.py` proves the
  outstanding pointer names a live issue.
- `python3 scripts/check-cd-allocation.py --no-fetch` proves the CD-0198
  identifier allocates once.
