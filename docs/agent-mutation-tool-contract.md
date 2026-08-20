# Concord Agent Mutation-Tool Contract (TS4)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS4; binding input to CD-0005 §4 and TS5–TS9.
> **Binding inputs:** accepted PM3–PM7 domain operations, TS1 jobs/scenarios,
> TS2 budget/granularity, TS3 read surface, capability-placement native-authority
> rule, and the Advance postmortem.
> **Does not decide:** request context/authorization/idempotency envelope (TS5),
> transport (TS6), shared result/error schema (TS7), surface evolution (TS8),
> measurement evidence (TS9), workflow-type registration/gate vocabulary, C14, or C15.
> **Amended direction:** CD-0041 requires typed architecture binding for
> Product-changing work uses the direct Initiative route.
> CD-0042 makes the generated current manifest the only pre-go-live surface
> identity; this contract does not authorize partial Domain or Initiative writes.

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
| `capture` | title, value statement, work kind, at least one Project membership, the governing requirements the work carries when its target scope declares any, optional workflow type/priority/tags/external reference | Create one `work_item` plus complete initial membership set and creation event in one SQLite transaction. |
| `revise_intent` | work ID, expected version, complete replacement intent block, reason | Replace only the closed mutable intent block and append one event. Omitted fields are intentionally absent because the block is complete—not patch semantics. |

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
`options`, and no events. Enumeration confers no authority — a caller can match the
applicable set or fail, and the only path to a reduced set is an operator-approved
scope cut bound to the challenge the refusal mints.

`capture` is one item only. Multiple discovered needs become separate explicit
calls so each receives independent identity, value, scope, and conflict handling.

### 2.2 `concord_work_transition`

**Use when:** an agent must change canonical work lifecycle or perform one action
offered by the work item's accepted workflow definition.

| Operation | Required domain input | Core effect |
|---|---|---|
| `lifecycle` | work ID, expected version, target `needed|in_progress|completed|cancelled`, reason, required evidence/approval | Validate PM4 transition and workflow obligations; append event and update projection atomically. |
| `workflow_action` | work ID, expected version, stable action ID offered by the current workflow version, required fields/evidence/approval | Execute the workflow-defined domain action atomically or reject it; action ID is authoritative data, not a caller-invented command. |

`superseded` is excluded from direct lifecycle input. Supersession requires
`concord_work_relate.supersede`, which commits the relation and terminal transition
together. Reopening superseded work also belongs there because the edge must change
in the same transaction.

`workflow_action` does not decide workflow registration or gate vocabulary. It is a
stable indirection to an action already declared by the work item's pinned workflow
version. The core—not tool prose—validates whether the action is currently offered
and which evidence/approval it requires.

### 2.3 `concord_work_relate`

**Use when:** an agent must change which Projects one work item touches or create,
remove, supersede, or restore a typed work relation.

| Operation | Required domain input | Core effect |
|---|---|---|
| `set_memberships` | work ID, expected version, complete resulting Project membership set with optional singular primary | Validate at least one membership, uniqueness, Project identity, and primary cardinality; replace the set atomically and return bounded Product-scope impact. |
| `link` | source/target work IDs and expected versions; kind `parent|blocks|implements` | Validate endpoints, duplicates, self-edge, and applicable cycle rules; create one canonical directed edge and event. |
| `unlink` | canonical relation ID or source/target/kind plus expected versions; reason | Remove one accepted edge and append one event; inverse read names never create mirrored mutations. |
| `resolve_overlap` | both work IDs/versions, both workflow-contract versions, closed resolution kind, reason, and operator approval | Resolve one concurrent Domain overlap and create its typed relation atomically. |
| `supersede` | successor/predecessor IDs, both expected versions, reason/evidence | Create one canonical supersession edge and transition predecessor to `superseded` in one transaction. |
| `restore_superseded` | predecessor ID/version, active supersession relation, current successor ID/version, optional replacement successor ID/version, replacement/removal instruction, reason/evidence | Validate every affected endpoint version, remove or replace the active edge, and transition predecessor to `needed` in one transaction. |

Membership is complete-set replacement because add/remove patches can transiently
violate at-least-one and primary-cardinality invariants or depend on call order.
Relation operations remain singular because each edge has its own graph oracle and
version conflict. `depends_on` is an inverse read; callers create canonical `blocks`.

### 2.4 `concord_work_compact`

**Use when:** an agent must publish or recover durable Product knowledge for one
terminal work item. Do not use it to complete lifecycle, prune projections, or
write arbitrary files.

| Operation | Required domain input | Core/cross-authority effect |
|---|---|---|
| `publish` | terminal work ID/version; exact proposed note content; resolved home/locator; operator approval over content and home | Follow PM6: validate eligibility/home, commit to git, verify commit/path/work ID/hash, then append the SQLite compaction link. |
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

PM7 pruning/backfill maintenance is not agent-exposed in v1: no accepted TS1
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
| `concord_work_relate` | AJ5 dependency, cycle rejection, atomic supersession, operator-approved Domain-overlap resolution |
| `concord_work_compact` | AJ6 publication and partial reconciliation |

Native-run actions (`start_run`, `record_health`, `rollback_run`, `cleanup_run`)
carry typed attributed-report fields per CD-0039: the native authority performs
and proves the operation while Concord folds one `workflow.native_run_recorded`
event per report with the reporting client, subject, evidence, and both times
alongside the status. A report that the approved logical operation did not
complete successfully classifies the action `partial` with
`operation_conflict`/`reconcile_operation`; `ok` is reserved for successful
native predicates. The adapter never derives outcomes from provider output.

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
