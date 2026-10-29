# CD-0198: Small items batch, merge, and derive routine workflow actions

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** The typed workflow action surface of `workflow.implementation` and
  `workflow.break_fix`: the `record_verdict` payload, the three diagnosis record
  actions, the fenced step starts, the delivery assertion's entry route, and the
  worker-failure record with its retry dispatch
- **Related:** CD-0012, CD-0166, CD-0173, CD-0183, CD-0192
- **Approval:** The operator approved the work item contract that carries this
  record on 2026-09-30
  ([Concord (CON) issue 767](https://linear.app/sharper-flow/issue/CON-767/decide-which-coordinator-workflow-actions-merge-or-become-core-derived)).

Operator consent is limited to the six classes in [CD-0243 D6](CD-0243-four-roles-and-six-operator-approval-classes.md#d6-existing-approval-requirements-follow-these-classes). Failure records and other non-consent checks remain required.

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
it. The verdict cannot come from the executing agent: Implement cannot accept
its own result
([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).
The human checkpoint at the verdict step stays. Removing any of the four is out
of scope, as are the tool-schema refusal fixes that CON-408 owns.

## Decision

For `approval_policy=consequence_v1`, the contract remains mandatory but human signature follows recorded decision domains under CD-0013 D11. Batched verdict recording still requires recorded verdict authority and evidence.
Implement cannot accept its own result ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)). Engineering premise confirmation uses its
real coordinator identity, and a combined action cannot accept its own result
or silently omit a predicate.

All five mechanisms retain the four floors.

### D1. One record_verdict call carries a verdict for every predicate it judges

The `record_verdict` payload gains a batched form that carries one or more
verdict objects. Each object names one approved predicate of the active contract
and carries its own `verdict_kind`, its own evaluation evidence, and its own
incomparable flag. Duplicate predicate ids refuse. One call records one verdict
per approved predicate in one transaction
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

The evidence floor holds because the batch changes how many
calls carry verdicts, not who produces them. Every verdict in the batch carries
its recorded actor, and completion still refuses a verdict the executing actor
recorded: Implement cannot accept its own result
([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).
The batch records evaluations of
predicates the approving authority authored at planning, and authors nothing, so
authorship fencing is untouched. The completion gate keeps demanding a verdict
for every approved predicate, so a predicate the call omits stays missing and
named, never silently excused.

The `record_verdict` payload contract declares the batched form in the one
canonical source. Generated contracts and the identities active work needs derive
from that source at the integration boundary
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

### D2. The three diagnosis records merge into one typed action on break_fix

The diagnosis step's advance action is `record_diagnosis`. Its payload carries a reproduction section,
an alignment-search section, and a root-cause section. Each section is required
and carries its payload fields and consistency guards,
including the CD-0156 D3 `searched`, `outcome`, and `related_ids` fields with
their guards. The diagnosis step admits `record_diagnosis` alone as its advance action, so its
single forward edge cannot be crossed without all three sections recorded.

The step-shape obligations of CD-0156 apply to `workflow.implementation`, which
has the alignment step and `record_alignment`. The floor holds against CD-0156 D3 because
passing the diagnosis step stays the proof the search happened, and the section
enum separates checked-and-found-nothing from never-checked. The
action writes the same `workflow_backlog_alignment` rows from its alignment
section
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)),
and CD-0156 D5 keeps its force: the action records the
candidate set and creates no relation.

The search section is required in the same recorded act as the analysis, so no
analysis can be recorded without a search beside it. The diagnosis step and its
action are declared in the one canonical source the generated contracts derive from
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

### D3. The start of repair and refine derives from the first action at the step

Recording the first workflow action at the `repair` or `refine` step derives the step-start
fact in the same transaction, and the first action recorded at the step wins
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
The derivation mechanism of CD-0183 D2 for `execution_started_at` answers
from current rows.

The derived start keeps every duty the callable start carried. It opens the step
epoch. It binds the step fence from the first action's own actor. The delivery
admission guard keeps requiring a start at the delivery-bearing step, and the
CD-0192 D2 epoch bound reads the derived refine start, so a verify run acquired
before it stays stale. The one documented cutover migrates live instances and
derives each retained start fact there
([CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)).

On both families, delivery admission and refine-proof guards read the derived
step-start fact declared in the one canonical source
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

### D4. An accepted report that carries the delivery artifact records the delivery

`accept_worker_result` gains optional delivery fields. When an accept at a
delivery-bearing step carries `delivery_artifact`, it first reuses the complete
delivery-admission mechanism that `record_delivery` uses. The admission checks
the fenced step start, the refine-exit proof, and the post-rejection review gate
before any acceptance, delivery record, or step advance. The acceptance records
the same delivery a `record_delivery` call records, with `delivery_state`
`asserted`, and advances the step in the same transaction, so the separate call
at that step disappears
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

CD-0192 D1 holds because both entry routes run the same refine-exit proof guard.
An acceptance that exits refine requires bound verification evidence in the
current refine epoch. That evidence names a completed green `worktree_verify`
run for this item, acquired after the refine start, with exit code zero and no
tracked-file change. When the default ref declares tooling, the run's argv must
match a declared invocation. Both routes resolve tooling from the same default
ref and use the same admission implementation. A missing, stale, failed, or
undeclared run refuses the whole acceptance-and-delivery operation without
acceptance, delivery, or advance. Recording the delivery alone is not proof
that admission ran
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

The floor holds against CD-0166 because the assertion changes its entry route,
not its owner. CD-0166 D1 holds because `accept_worker_result` is a coordinator
action, so the coordinating agent still owns the truth of the assertion, and the
core still observes nothing. CD-0166 D2 holds because the delivery gate keeps
`record_delivery` as its declared exit and runs no accept. CD-0166 D3 holds
because the unreconciled refusal and the parked read read the recorded
delivery, and the derived record is one
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
The CD-0166 D6 post-rejection refusal names the
recorded delivery of a step, and a delivery derived at the refused step refuses
with it.

The `accept_worker_result` payload and its recording on both families reuse the
complete delivery-admission mechanism. Regression checks on both routes must prove refusal for missing, stale,
failed, dirty, and undeclared verification runs, and for a missing start or
unsatisfied post-rejection review. A valid bound run permits the combined route
and records acceptance, delivery, and advance atomically. Generated contracts
derive from the one canonical source
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

### D5. The worker-failure record derives, and a rejection can carry its retry dispatch

Two reductions share the correction route. First, when a dispatched attempt
reaches its terminal failed lifecycle at a workflow step, recording the
terminal result derives the workflow failure record from the attempt's own
terminal result
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
The correction
diagnosis fills from the failure detail the attempt already carries, and the
strategy fills from the standing default, which are the values the correction
context applies today when the caller omits them. The record stops being a
callable action and the coordinator call disappears.

Where an attempt carries typed attribution, the terminal result owns the typed
observation. Its existing derived workflow failure fact references that
attribution. Derivation neither copies a second causal record nor creates
another correction record. Recording commits the existing pair once, and no
rebuild or second derivation exists
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Born-failed dispatch and abandonment keep their existing terminal
representations and liveness gates. Attribution does not fabricate model
readback or another terminal outcome. A failed or uncertain evidence write
remains a refusal with its actual effect state. No successful attribution
write or replacement window is inferred.

Rejection and retry still share the sole dispatch admission. Attribution
changes no approval, fence, or rejection-only escape; no attempt count, limit,
or convergence basis exists
([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).
The correction diagnosis and strategy retain their existing source and
defaults.

Second, `reject_worker_result` gains an optional retry dispatch in its payload.
The rejection records the dispatch in the same transaction
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
The retry mints a fresh fenced attempt identity, and the dispatch binds the
same packet rules every dispatch binds. No escalation wall exists, each launch
carries its own authority, and its scope and safety checks remain, so a
rejection may carry the dispatch and one without the dispatch payload still
records
([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

The floor holds because the correction context reads the derived record exactly
as it reads a recorded one, and
the dispatch rules keep their single source in the dispatch packet.

The terminal result's derived failure record and `reject_worker_result`'s optional
dispatch are declared in the one canonical source of the families that declare the actions
([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

## Alternatives considered

- Remove contract approval or the verdict checkpoint to save the most calls.
  Refused: the objective bounds both out, and [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)
  and CD-0012 D9 make them load-bearing.
- Replace the small records with one free-form progress action. Refused: a
  generic call cannot carry typed payloads, so every per-action guard weakens
  into prose, and the verb dilution CD-0012 D3 refuses follows.
- Let confirm_premise carry the verdicts beside the operator confirmation.
  Refused: it blurs the verdict into the approval act and hides which actor
  recorded it, and the executor cannot accept its own result
  ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).
- Derive the delivery assertion from an observed merge instead of the
  coordinator's accept. Refused: CD-0166 D1 sets the boundary that the
  coordinating agent owns the assertion and the core observes nothing.
- Derive the worker-failure record with no correction context at all. Refused:
  the retry fences read that context, and dropping it deletes validation rather
  than a call
  ([CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)).

## Consequences

- A small break_fix item spends about 6 to 9 fewer coordinator calls, and no
  accepted floor loses a check.
- The verdict, delivery, and failure records keep their fields, so receipts
  and read surfaces read them unchanged
  ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- Each agent-visible typed action surface carries the generated-manifest, digest, schema, scenario, and
  classification cost that agent-tool-surface-evolution attaches, and the
  generated contracts derive from the one canonical source
  ([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).
- A reader that navigates break_fix steps uses the diagnosis step. The CD-0156 step-shape tests apply to
  `workflow.implementation`.
- The start of repair and refine is a derived fact, so a derivation defect
  would misplace the fence or the refine epoch. Verification must
  prove the derived start matches the first recorded action at the step
  ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
- The batched verdict payload and the merged diagnosis payload grow, and the
  recording is one transaction, so a malformed section refuses the whole call
  and the caller retries the call rather than one section.

## Verification

```gherkin
Scenario: One derived failure fact references the terminal attribution
  Given one terminal attempt with a typed attribution record
  When the store records the attempt's derived failure fact
  Then exactly one derived workflow failure fact references the same
  attribution
  And the correction context reads that one derived record
```

- `python3 scripts/check-doc-contract.py` proves this record carries the current
  decision outline and passes the writing rules.
- `python3 scripts/check-knowledge-index.py` and
  `python3 scripts/check-knowledge-closure.py` prove this record registers with
  a current content hash and no unprocessed document remains.
- `python3 scripts/check-law-coverage.py` proves the coverage record's state
  obligations hold, and `python3 scripts/update-issue-state.py` proves the
  outstanding pointer names a live issue.
