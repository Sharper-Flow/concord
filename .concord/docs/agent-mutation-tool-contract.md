# Concord Agent Mutation-Tool Contract (TS4)

> **Status:** **Accepted — binding until superseded.**
> **Amended:** 2026-10-18.
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS4; binding input to CD-0005 §4 and TS5–TS9.
> **Binding inputs:** accepted PM3–PM6, compaction-retention-policy domain operations, TS1 jobs/scenarios,
> TS2 budget/granularity, TS3 read surface, capability-placement native-authority
> rule, and the Advance postmortem.
> **Does not decide:** request context/authorization/idempotency envelope (TS5),
> transport (TS6), shared result/error schema (TS7), surface evolution (TS8),
> measurement evidence (TS9), workflow-type registration/gate vocabulary, or C15.
> **Amended direction:** CD-0222 requires typed architecture binding for
> Product-changing work; CD-0213 removes the Initiative route.
> **Amended by [CD-0242](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D4, and D6:** each
> mutation writes current rows in one transaction; no event append or replay
> exists, and a task bigger than its size reruns the decision model instead of
> creating a successor item.
> CD-0042 makes the generated current manifest the only pre-go-live surface
> identity; this contract does not authorize partial Domain writes or any planning write.

## Context

TS1's mutation jobs need a bounded mutation surface. The binding inputs are
the accepted PM3 through PM6 and compaction-retention-policy domain operations, TS1 jobs and scenarios, TS2
budget and granularity, the TS3 read surface, the capability-placement
native-authority rule, and the Advance postmortem. This record fixes the four
always-visible mutation tools, their operation tables, the batch boundary,
and the inline-versus-durable execution rule. CD-0222 requires typed
architecture binding for Product-changing work; CD-0213 removes planning writes; CD-0042 makes the
generated manifest the only pre-go-live surface identity.
## Contract

Approval prerequisites in the operation tables follow CD-0013 D11. Routine overlap answers, delivery correction, same-authorized-Product memberships, ordinary completion, and local compaction publication/reconciliation require no consent. `merged_into`, `supersedes`, cancellation, removal, authority-expanding Product or client-policy changes, explicit cross-Product mutations, outside repair and both reconciliation modes, and physical research-pack deletion retain operator approval as listed gaps without a new class assignment. A publication intent replaces approval as the exact content/home lock. Workflow action consent remains definition-pinned. All version, authorization, evidence, native proof, atomicity, and durable-operation checks remain required.


The binding contract is sections 1 through 4: the four-tool decision with the
native-authority rule, each tool's operation table with core effects, the
batch boundary of one domain intent per call, and the inline-versus-durable
execution rule with the compaction recovery contract. Sections 5 through 7
record scenario mapping including the native-run evidence law, rejected
shapes, and falsifiers, and carry no obligation.
## 1. Decision

Concord exposes **four always-visible mutation tools**:

1. `concord_work_define` — capture one work item or replace its mutable intent;
2. `concord_work_transition` — perform one accepted lifecycle or workflow action
   with required evidence/approval;
3. `concord_work_relate` — change work membership or typed work relations while
   preserving graph/supersession invariants;
4. `concord_work_compact` — publish or reconcile the one canonical durable note for
   terminal work through PM6's ordered git→SQLite protocol.

Together with TS3's four reads, Concord v1 has **eight always-visible tools**, below
TS2's cap of nine. This count follows the accepted boundaries; it is not a target
that permits collapsing future authority or consequence boundaries.

External native authorities—GitHub, CI, databases, cloud providers, service
managers, and git itself—retain execution authority. Concord records work intent,
approval, references, evidence, and status; it does not expose a generic external
`execute` tool or proxy provider APIs. An ops runbook uses these four Concord tools
for durable coordination and the accepted native tool for the real effect.

## 2. Tool contracts

### 2.1 `concord_work_define`

**Use when:** an agent must capture a new need or clarify the mutable intent of an
existing nonterminal item. Do not use it for lifecycle, scope, relation, evidence,
or terminal-note changes.

| Operation | Required domain input | Core effect |
|---|---|---|
| `capture` | title, value statement, work kind, at least one Project membership, the governing requirements the work carries when its target scope declares any, optional workflow type/priority/tags/external reference | Create one `work_item` plus its complete initial membership set in one SQLite transaction. |
| `revise_intent` | work ID, expected version, complete replacement intent block, reason | Replace only the closed mutable intent block in one recorded mutation. Omitted fields are intentionally absent because the block is complete—not patch semantics. |

The mutable intent block is closed: title, value statement, work kind, priority,
tags, and accepted workflow-type reference. Identity, lifecycle,
memberships, relations, evidence, versions, authority, and compaction locator cannot
appear in it. A governing-law conflict returns a typed conflict and no mutation;
the agent cannot silently shrink the accepted scope.

CD-0035 makes that sentence enforceable at `capture`. Requirements are declared
against a Project, and the core refuses when the requirement set a capture declares
does not cover the ones its target scope carries. The refusal is a set difference,
not a reading of the instruction: `invariant_violation` naming the omitted
requirements in `violations`, `contact_operator` recovery, the operator's three
`options`, and no mutation. Enumeration confers no authority — a caller can match the
applicable set or fail, and the only path to a reduced set is an operator-approved
scope cut bound to the challenge the refusal mints.

`capture` is one item only. Multiple discovered needs become separate explicit
calls so each receives independent identity, value, scope, and conflict handling.

### 2.2 `concord_work_transition`

**Use when:** an agent must change canonical work lifecycle or perform one action
offered by the work item's accepted workflow definition.

| Operation | Required domain input | Core effect |
|---|---|---|
| `lifecycle` | work ID, expected version, target `needed|in_progress|completed|cancelled`, reason, required evidence/approval | Validate PM4 transition and workflow obligations; write the current rows atomically. |
| `workflow_action` | work ID, expected version, stable action ID offered by the current workflow version, required fields/evidence/approval | Execute the workflow-defined domain action atomically or reject it; action ID is authoritative data, not a caller-invented command. |

`superseded` is excluded from lifecycle input. The `supersede` operation
retired: contract supersession goes through the operator-approved
`supersede_contract` action, and an overlap resolution that terminalizes a
work item goes through `concord_work_relate.resolve_overlap` with `supersedes`
or `merged_into`, which commits the relation and the terminal transition
together. A task that proves bigger than its size reruns the decision model,
adds the newly required steps, and continues without redoing completed work or
creating a successor item ([CD-0242](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D4, D6). Reopening
superseded work has no operation; a superseded work item keeps its terminal
record.

`workflow_action` does not decide workflow registration or gate vocabulary. It is a
stable indirection to an action already declared by the work item's pinned workflow
version. The core—not tool prose—validates whether the action is currently offered
and which evidence/approval it requires.

### 2.3 `concord_work_relate`

**Use when:** an agent must change which Projects one work item touches or resolve
one concurrent Domain overlap.

| Operation | Required domain input | Core effect |
|---|---|---|
| `set_memberships` | work ID, expected version, complete resulting Project membership set with optional singular primary | Validate at least one membership, uniqueness, Project identity, and primary cardinality; replace the set atomically and return bounded Product-scope impact. |
| `resolve_overlap` | both work IDs/versions, both workflow-contract versions, resolution kind `compatible_with\|depends_on\|blocks\|merged_into\|supersedes`, reason, and an operator approval for `merged_into` or `supersedes` | Resolve one concurrent Domain overlap and create its typed relation atomically; `supersedes` and `merged_into` commit the terminal transition in the same transaction. |

Membership is complete-set replacement because add/remove patches can transiently
violate at-least-one and primary-cardinality invariants or depend on call order.
The rule binds every writer of work membership, not only the agent surface. A
writer that writes one membership row per Project in separate mutations
charges one version per Project, where replacement charges one for the whole set. Such a writer places
its work items at versions no agent call can produce, so a test fixture reading
one exercises arithmetic the product never performs. Test fixtures write
`work.memberships_replaced` for that reason.

Relation operations remain singular because each edge has its own graph oracle and
version conflict. `depends_on` is an inverse read; `blocks` is written only by
`resolve_overlap`. The other relation kinds have their own writers outside this
tool: capture writes `raised_from` through `raised_from_work_id`, and the
workflow `link_successor` action writes `forward_link`.

### 2.4 `concord_work_compact`

**Use when:** an agent must publish or recover durable Product knowledge for one
terminal work item. Do not use it to complete lifecycle, prune projections, or
write arbitrary files.

| Operation | Required domain input | Core/cross-authority effect |
|---|---|---|
| `publish` | terminal work ID/version; exact proposed note content; resolved home/locator; operator approval over content and home | Follow PM6: validate eligibility/home, commit to git, verify commit/path/work ID/hash, then record the SQLite compaction link. |
| `reconcile` | durable operation reference and expected operation version (or terminal work/version for orphan discovery); expected proof/recovery intent | Compare-and-swap the durable step, find and verify an orphan/partial publication by work ID, then complete the missing link or return a typed non-destructive recovery outcome without creating a second note. |

Git and SQLite are never described as one atomic transaction. Before the first
external effect, the core creates a durable operation record identifying the pinned
terminal work version, approval reference bound to the exact content and home,
idempotency identity, current step, and recovery state. The PM6 home lock remains
held through git verification and SQLite linkage; the core revalidates the pinned
terminal version before recording that link.

- If `publish` completes inside the accepted execution budget, it returns final
  success inline.
- If a git/host step remains pending or the caller budget ends, it returns the same
  durable operation reference with structural `pending|partial|failed` state.
- `reconcile` resumes or explains that operation; it never restarts from scratch.
- SQLite never records an authoritative locator before git proof.
- The publication phases are executed from one declared sequence rather than from
  the order statements happen to appear in, and the sequence is checked as it
  runs. A partial outcome reports the steps that actually completed, so an
  operator recovering from an interrupted publication is told how far the
  cross-authority effect really got.

compaction-retention-policy pruning/backfill maintenance is not agent-exposed in v1: no accepted TS1
scenario requires an agent batch tool. Native bounded maintenance may exist behind
operator/internal CLI control and can be promoted only through TS8/TS9 evidence.

## 3. Batch boundary

V1 mutation tools accept one work item, one lifecycle/workflow action, one relation,
or one compaction operation per call.

The only collection-valued mutation is `set_memberships`, which supplies the
complete resulting membership set for **one** work item and commits as one invariant.
It is not a batch.

No v1 mutation accepts arrays of work IDs, heterogeneous operation lists, arbitrary
transactions, or "continue on error." This avoids false top-level success and keeps
expected versions, approvals, evidence, idempotency, and recovery attributable to
one domain intent. A future batch requires a named failing scenario and per-item
typed outcomes; reducing calls alone is insufficient.

## 4. Inline versus durable execution

- SQLite-only define, transition, and relation operations are short, single-domain
  transactions and complete inline or fail without an effect.
- Compaction crosses git/SQLite authority and may return a durable operation as
  defined above.
- External ops execute through their native tools. Concord's corresponding work
  transition completes only after native evidence is supplied; Concord never
  returns success merely because a provider call was launched.
- A durable operation reference is not a generic job queue ID. It names one typed
  operation, authority step, work item, and recovery contract.

TS7 owns the common result/error fields. TS4 requires that routine inline success
include changed references/new versions/next valid intents and that nonterminal
durable results include current step and a safe next action—never a bare "started."

## 5. Scenario mapping

| Tool | TS1 scenarios |
|---|---|
| `concord_work_define` | AJ3 capture and spec conflict |
| `concord_work_transition` | AJ4 start, complete, missing evidence, stale version; AJ8 approval/evidence recording around native execution |
| `concord_work_relate` | AJ5 operator-approved Domain-overlap resolution |
| `concord_work_compact` | AJ6 publication and partial reconciliation |

Native-run actions (`start_run`, `record_health`, `rollback_run`, `cleanup_run`)
belong only to the archived `workflow.ops_runbook` family (CD-0218 D1).
Admission refuses them, and no route records a new native-run report. Reports
recorded under CD-0039 stay readable (CD-0218 D4): each
`workflow.native_run_recorded` record carries the reporting client, subject,
evidence, and both times alongside the status. The adapter never derives
outcomes from provider output.

Every native-run record embeds the shared CD-0040 capture component and carries
its verification participation: reads return the record unverified, diverged,
or verified — never a bare status — and binding it as completion evidence
requires the attributed record verified first. The generic external-observation
variant of `concord_work_define.observation_record` (CD-0040 D10) records
captures of, and verifications against, state outside Concord with the same
shared component: exact provenance, an examined universe whose completeness is
earned (witness, anchor, reconciled totals, zero unresolved omissions), and
content-hashed freshness/divergence policy references derived from the
reviewed per-kind register. Reporting and verifying authorities derive from the
authenticated trusted client. External observations are non-authoritative:
they may only supply or withhold a precondition checked by another operation;
they never satisfy evidence, approval, transition, verdict, or completion
except through the verified `native_run` evidence path above.

AJ8 native execution/rollback/reclamation is deliberately not claimed as a Concord
mutation. Its Concord-visible intent/evidence/lifecycle uses define/transition;
the accepted native authority performs and proves the real operation.

## 6. Rejected shapes

- Generic `create_entity`, `update`, patch, field mask, JSON merge-patch, SQL, or
  arbitrary command dispatcher.
- Separate tools for start, pause, complete, cancel, reopen, link, unlink, approve,
  evidence, archive, or repair.
- Direct `superseded` lifecycle patch separate from its canonical relation.
- Client-sequenced membership add/remove/promote calls.
- One mixed mutation batch or a global success boolean hiding per-item failure.
- A generic Concord cloud/database/GitHub/system executor.
- Pretending git+SQLite or Concord+provider effects share one transaction.
- Returning a bare asynchronous ID with no typed authority, step, or recovery path.
- Agent-exposed pruning, rebuild, destructive repair, or backfill without an
  accepted deterministic scenario and TS8/TS9 evidence.

## 7. Falsifiers and amendment rule

Reopen TS4 when:

- an accepted TS1 mutation scenario cannot be expressed without client-sequenced
  invariant maintenance;
- supported agents repeatedly confuse two mutation tools or operation variants;
- `revise_intent` full replacement causes demonstrated legitimate partial-edit
  failures that a stricter domain operation would solve;
- single-relation calls create measured call overhead without independent graph or
  version semantics;
- compaction cannot recover every PM6 interruption from its durable operation;
- a recurring native operation lacks an authoritative tool and requires new Concord
  execution ownership; or
- a homogeneous batch is required by a named scenario and can preserve per-item
  authorization, version, idempotency, evidence, and result truth.

Any added mutation must name its domain invariant, authority/consequence boundary,
retry identity, successful state oracle, and recovery path. An existing CLI command
or storage method is not sufficient evidence.

## Acceptance criteria

- Given a valid capture with Project membership
  When `concord_work_define.capture` commits
  Then the work item and its complete membership set
  commit in one SQLite transaction.

- Given a transition carrying a stale expected version
  When any mutation tool dispatches it
  Then the core refuses before approval consumption or any effect and returns
  the current version.

- Given a completion without its required evidence
  When `concord_work_transition.lifecycle` attempts it
  Then the core blocks the terminal transition with a typed error and no
  mutation.

- Given two overlapping Product-changing items
  When `concord_work_relate.resolve_overlap` resolves one
  Then one typed relation of the resolved kind is created atomically with the
  resolution; `supersedes` and `merged_into` terminalize the predecessor in
  the same transaction, a resolution that would close a cycle or add a
  second successor is refused, and `merged_into` or `supersedes` demands
  the operator approval pinned to both work and contract versions.

## Verification

All four criteria are proved by scenarios of `.concord/scenarios/agent-jobs.v1.json`,
executed by `TestAgentJobsCorpus`
(`internal/agent/agent_jobs_corpus_test.go`), with dispatch-layer proofs in
`internal/agent/mutation_dispatch_test.go`.

- Criterion 1 is proved by the bound `AJ3-capture-work` scenario and
  `TestDispatchCaptureCreatesWorkAndMembershipsAtomically`
  (`internal/agent/runtime_test.go`).
- Criterion 2 is proved by the bound `AJ4-stale-version` scenario.
- Criterion 3 is proved by the bound `AJ4-completion-missing-evidence`
  scenario.
- Criterion 4 is proved by the bound `AJ5-resolve-domain-overlap` scenario,
  which drives the real `concord_work_relate.resolve_overlap` dispatch, and
  by `internal/store.TestOverlapTerminalResolutionRejectsCycle`,
  `internal/store.TestOverlapTerminalResolutionRejectsSecondSuccessor`, and
  `internal/store.TestOverlapBlocksRemovesWorkFromReady`.
