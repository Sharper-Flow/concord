---
{
  "id": "workflow-engine-contract",
  "title": "Workflow engine contract",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": [],
  "criterion_bindings": [
    {
      "criterion": 1,
      "scenario": "WF37-action-availability-before-register"
    },
    {
      "criterion": 2,
      "scenario": "WF19-completion-one-transaction"
    }
  ]
}
---
# Concord Workflow Engine Contract

> **Status:** Implementation contract for accepted CD-0013, v1.
> **Authority:** [`CD-0013`](../decisions/CD-0013-workflow-engine-mechanism.md),
> amended only by a later accepted CD-NNNN.
> **State and definitions ([CD-0242](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
> D2, D4, D6, and D14):** the engine keeps current workflow state and finite
> snapshots with no event log, fold, replay, or rebuild. Attempts carry their
> own authority with no escalation wall. Families compose shared stages under
> the decision-model minimum, and generated definitions derive from one source.
> **Scope:** Go registry, current workflow state, reviewed steps,
> completion, workflow composition, and the reserved `workflow_action`
> dispatch path.
> **Conformance carriers:** [`workflow-engine.v1.json`](../../scenarios/workflow-engine.v1.json),
> [`workflow-step-templates.v1.json`](../../../contracts/workflow-step-templates.v1.json).

This document fills the implementation-design boundary left by CD-0013. It does
not add a workflow family, authority, or agent tool. The
workflow tables are current state written one mutation at a time;

CD-0015 adds the law boundary without adding a workflow tool.
Only contracts approved through surface `2.2.0` carry the internal persisted
law-boundary marker: for those contracts, `spec_mandate` is the bounded
referenced-law set and `law_modifies` is an optional bounded subset. Planning
checks the laws committed in the law folder and their declared relations;
an explicit conflict may pass planning only when the endpoint whose record
declares it is in `law_modifies`.

CD-0036 binds contracts to law IDs without law or Domain registry freshness
hashes. Later amendments, deletions and registry edits reconcile through Git;
they produce no stale-law refusal or compatible-amendment hash projection.
Current mandate, Domain, modification, addition and obligation membership
checks remain. Same-operation law snapshots remain required. Concord retains
no owning-start inventory, enactment before-state, per-item law attribution or
registry delta. Contract payloads carry no separate law-change reason or removal
list. Independent review and the committed law folder prove publication and
the declared law delta under CD-0222 D1, not a completion enactment gate.

CD-0016 requires explicit current-state reads and forbids automatic per-turn
context injection or authority-bearing summaries.

An explicit work reopen clears an existing terminal workflow instance's completion
metadata in the same transaction. With a plan, the instance returns to `running`,
preserves its plan and prelude state, and admits `insert_step`. Without a plan,
it returns to `intake` and clears its prelude state, allowing a new `record_intake`.
Neither case requires a new capture. PM4 and CD-0146 govern the lifecycle and
Linear collision checks.

[CD-0243 D7](../decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, and evidence requirements remain
required. No released workflow-definition version or historical pin is retained
(CD-0242 D14).

## Context

The workflow engine carries a work item from an approved contract to a
completed outcome. The binding inputs are CD-0013 as amended by CD-0015,
CD-0016, and CD-0036, the typed carriers it names, and the scenario corpus
with the template contract it cites. This record fills the implementation boundary
CD-0013 left open: the common representation, the type templates and step
plans, the instance state machine, current workflow state, reviewed steps,
the ordered completion gate, the actor tuple and
authority, workflow composition, dispatch, and the workflow types.

## Contract

CD-0013 D11 qualifies operator-consent clauses in this contract. The coordinator records the contract, premise confirmation and completion. Main obtains independent native Review and records the reviewed step; Implement cannot accept its own result ([CD-0242 D5](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)). No synthetic operator identity is created. Decision-bearing checkpoints retain human authority through CD-0243's conversational classes. Generated definitions derive from one source, and applicable recovery, source and liveness obligations remain required.

The binding contract is sections 1 through 12: the common representation and
bounds, the type templates and step plans, the instance state machine, current workflow
state, reviewed steps, the
ordered completion gate, the actor tuple, workflow
composition, dispatch, and the built-in
family graphs and actions. Section 13 records implementation-deferred items
and carries no obligation.

## 1. Common representation and bounds

All identifiers are UTF-8 strings with 2–128 characters unless a narrower pattern
is stated. Timestamps are RFC3339 UTC strings, maximum 64 characters. Digests are
lower-case `sha256:` followed by 64 hexadecimal characters. Recorded mutation
payloads are typed objects, never free-form maps; unknown keys are refused
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

| Value | Type/bound |
|---|---|
| `work_id`, `step_id`, `action_id`, `checkpoint_id`, `edge_id` | identifier, 2–128 chars |
| `ref` | lower-case registry/reference string, 2–128 chars |
| `version`, `contract_version`, `attempt_epoch`, `request_id` sequence | positive integer, max 2,147,483,647 |
| evidence/reference list | unique array, 0–32 items, each identifier |
| candidate list | unique array, 1–100 items |
| `actor_ref` | `actor:` plus lower-case content-hash reference |
| `payload_version` | integer exactly `1` for v1 emission |

## 2. Workflow type templates and step plans

### 2.1 One template contract

Each workflow type's ordered required steps — with each step's method skill,
helper, and reviewed flag, and the actions admitted on it — are listed once in
the machine-readable contract
[`contracts/workflow-step-templates.v1.json`](../../../contracts/workflow-step-templates.v1.json).
The plan schema sits in the same contract. Code reads it. Law and docs hold no
hand-written copy of a type's steps. The contract changes only through the law
route. There is no per-kind fixed step chain, no definition registry, and no
definition digest: a copied plan never changes when a template changes.

The closed type set is `change`, `fix`, `research`, and `removal`. Only Change
and Fix carry the six-part architectural plan at their plan step.

### 2.2 Per-item plan

When the driving agent picks the type, Concord copies that type's required
steps into one JSON plan stored on the item's workflow row. Each step records
its id, whether it is required or inserted, the steps it comes `after`, its
skill, its helper, whether it is reviewed, and, once done, its completion: a
note, plus the reviewer, result, commit and link for a reviewed step.
"Current" and "next" are not stored; a read derives them. Concord writes the
plan only when the type is picked, when a step is inserted, and when a step
completes. Nothing is written on a start, checkpoint, retry or read.

### 2.3 One check per plan write

Each plan write is a single row update under the item's expected version.
Before it saves, one function compares the new plan with the old one and
refuses the write if the plan fails its schema, contains a cycle, removes a
required step or changes a step's `after` edges, completes a step before every
step it comes after, or alters a completed step. A repeated step adds another
attempt instead of altering it. A type switch keeps completed steps and adds
the new type's missing required steps; it never removes a step.

## 3. Workflow instance state machine

`instance_state` is closed:
`intake`, `running`, `blocked`, `verifying`,
`completed`, `cancelled`.

| From | To | Operation | Authority/fence |
|---|---|---|---|
| absent | `intake` | capture records the actor tuple and opens the instance with no type and no plan | internal-inline |
| `intake` | `running` | type picked and the plan written | one expected-version-checked transaction |
| `running` | `running` | action checkpoint or successful action | one expected-version-checked transaction |
| `running` | `blocked` | unresolved dependency discovered | internal-inline |
| `running` | `verifying` | execution steps are complete and verification begins | internal-inline |
| `verifying` | `completed` | ordered completion gate succeeds | one internal transaction |
| `running`, `blocked`, `verifying` | `cancelled` | explicit authorized cancellation | one expected-version-checked transaction |
| `completed`, `cancelled` | `running`, or `intake` when no plan exists | explicit work reopen | the reopen's expected-version-checked transaction |

An action failure records its typed failure result; it does not invent a state.
Recoverable failures leave the instance in `running` or `blocked`.
A completed or cancelled instance changes only through an explicit work reopen.
Each action is one expected-version-checked, idempotent domain
transaction with its operation result and effect receipts. There is no separate
step claim, takeover or claim/settle protocol. Native authorities own external
execution. Completion keeps terminal work identity and reported delivery links,
not workflow step records, under [PM6 §1](canonical-git-note-placement.md#1-decision).

## 4. Typed mutation records

The store keeps current state and finite snapshots ([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Each
mutation writes current rows in one transaction checked by an expected version
and recorded by its idempotency key. The names below identify the typed
mutation records that the §12 action tables map actions to; omitted and unknown keys are
refused. Only the completion fold reached through the `complete` action records
`workflow.completed`, after the ordered gate in §7;
ordinary `ApplyOperationTx` refuses it structurally.

Every mutating workflow payload also carries `expected_version` and
`resulting_version`, consecutive positive integers checked by the mutation;
the rows below do not repeat this pair.

| Record | Exact payload fields and bounds |
|---|---|
| `workflow.definition_selected` | `work_id`, `ref`, `version`, `digest`, `work_kind` (closed family enum) |
| `workflow.contract_approved` | `work_id`, `contract_version`, `premise`, `legislative_subjects` (0–800 unique subjects), `route_conventions`, `spec_mandate`, `rigor_class`, and `consequence_class` |
| `workflow.candidate_set_revised` | `work_id`, `contract_version`, `candidate_kind` (exactly `work_item`, `product`, or `project`), `candidate_ref`, `added` (0–100 refs), `removed` (0–100 refs); `added` and `removed` are disjoint and not both empty |
| `workflow.actor_recorded` | `work_id`, `actor_ref`, `principal_ref`, `client_ref`, `agent_ref`, `session_ref`, `actor_class` (`agent` or `operator`) |
| `workflow.action_started` | `work_id`, `step_id`, `action_id`, `attempt_epoch`, `accepted_inputs_digest`, `idempotency_identity` (2–128), `actor_ref` |
| `workflow.action_checkpointed` | `work_id`, `step_id`, `step_kind` (`internal_sqlite`, `cross_authority`, or `external_effect`), `attempt_epoch`, `checkpoint_payload` (typed action checkpoint, max 16 KiB), `resume_cursor` (0–2048), `actor_ref`, `request_id` |
| `workflow.action_completed` | `work_id`, optional `action_id`, `step_id`, `attempt_epoch`, `result_evidence_refs` (0–32), `changed_refs` (0–32), `actor_ref`. It rejects undeclared current-step actions. |
| `workflow.proposal_recorded` | `work_id`, `problem` (1–4096), `affected` (1–16 unique entries of 1–256), `stakes` (1–2048), `user_outcomes` (1–16 unique entries of 1–512), `constraints` (0–16 unique entries of 1–512, omitted when absent), `open_questions` (0–16 unique entries of 1–512, omitted when absent), `out_of_scope` (0–16 unique entries of 1–512, omitted when absent) |
| `workflow.design_recorded` | `work_id`, `approach` (2–4096), `decisions` (1–16 typed decisions with `id`, `question` (1–512), `choice` (1–1024), `rationale` (1–1024), and `rejected` (0–8 strings)), `touched_refs` (1–64 references) |
| `workflow.backlog_alignment_recorded` | `work_id`, `searched` (2–4096, the search performed and its scope), `outcome` (`related_found` or `none_found`), `related_ids` (1–64 unique work ids, present only when `outcome` is `related_found`; the mutation refuses `related_found` with no ids and `none_found` with ids, and every id must name a real work item) |
| `workflow.action_failed` | `work_id`, `step_id`, `attempt_epoch`, `failure_kind` (closed TS7 error kind), `recoverable` (boolean), `actor_ref` |
| `workflow.evidence_bound` | `work_id`, `evidence_kind`, `immutable_subject_ref`, and required `producer_watermark`; the authenticated binding supplies attribution. For `evidence_kind=native_run` the `immutable_subject_ref` is the record's `xobs:` observation and the attributed record must be verified (CD-0040 D9), so the binding fails closed until a verification record answers the observation. |
| `workflow.native_run_recorded` | `work_id`, `run_id` (1–128), `native_subject_ref` (1–2048 opaque non-secret), `subject_digest` (core-derived), `phase` (`start`/`health`/`rollback`/`cleanup`, fixed by the action ID), `status` (closed per-phase vocabulary), `evidence_ref`, `evidence_digest`, `asserted_at` (RFC3339, within the two-minute report skew bound), `reporting_authority_ref` (the authenticated actor's client, never caller input), `actor_ref`, and the embedded CD-0040 capture component: `capture_method`, the closed `observed_universe` structure (one item, exactly scoped, complete by closed-structure digest — validated by the shared D4 rules when recorded and read), the derived content-hashed `freshness_policy_ref`/`divergence_policy_ref` from the reviewed per-kind register, and the `observation_id` the run's phases share. The mutation registers that observation in `external_observations` as its shared provenance home, so one verification record answers every phase row and the attributed read (`NativeRunReport`) carries the derived verification state. |
| `workflow.premise_confirmed` | `work_id`, `contract_version`, `confirming_actor_ref` |
| `workflow.successor_linked` | `work_id`, `successor_work_id`, `relation_kind` (`forward_link`) |
| `workflow.completed` | `work_id`, `terminal_state` (`completed`, `cancelled`), `premise_confirmed` (boolean), `evidence_count` (non-negative, derived from the item's bound evidence history), `changed_refs_digest` (digest), `impact_verdict` (`breaking` or `non-breaking`), `warnings` (0–16 staleness-rule IDs) |

`legislative_subjects` belongs to the exact contract version that recorded it.
Legislative standing also requires the operator-approved contract and the other
conditions in [CD-0159 D2](../decisions/CD-0159-authority-tier-for-product-law.md#d2-the-deliverable-sets-the-tier-not-the-presence-of-an-approval); subject inclusion alone is not approval.

### 4.1 Ordinary contract updates

An authenticated coordinator changes a nonterminal work item's contract through
`concord_work_define.revise_intent`, the ordinary record-update operation. The
request carries the complete replacement intent, the expected work version,
a reason, and an optional complete `contract` object. Omitting `contract`
leaves contract content unchanged. Supplying it appends the next retained
contract version atomically with the intent update. It does not edit an older
version in place or create another work item.

The complete contract passes the existing contract validation, authority,
scope, current-law membership, and definition checks. A refusal leaves the intent, contract,
evidence, and work version unchanged. Operator decisions remain ordinary
questions whose answers are recorded in the work record under
[CD-0243 D2](../decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision).
The update grants no legislative authority, overlap exemption, or approval
waiver. Initial contract recording remains `approve_contract`, which a completed
plan still admits while no active contract exists (§4.2).

Retained contract content, candidate sets, attribution, and evidence remain
addressable by the versions and subjects they originally named. The update
preserves the current workflow step, instance state, checkpoints, and attempts.
Evidence for a different candidate does not prove the current candidate.
Version-bound premise confirmations stay bound to their recorded version.
`confirm_premise` is admitted only at its declared step: while that step is on
the frontier or, once the plan completes, while the active contract's
confirmation is missing (§4.2). A contract update adds the action to no other
step. Unchanged valid proof
needs no replacement merely because a contract version was appended.

Tests of ordinary contract validation, atomic refusals, provenance retention,
unchanged-proof reuse, candidate validity, current law membership, operator
approval boundaries, and completion remain required. Workflow stage and
step-plan policy otherwise remain unchanged.

An update itself never moves the step. The coordinator obtains independent
native Task Review and applicable proof for the exact changed candidate.

### 4.2 Ordinary retry and record correction

Retry reruns the owning action. Current-record replacement applies to
`record_intake`, `record_clarify`, `record_plan`, `record_diagnosis`,
`record_delivery`, `record_review` and `revise_candidates`.
To correct an existing record, the authenticated coordinator invokes its ordinary
owning action with replacement values and the current expected work version.
That write updates the action's current record in one transaction. It applies
the same payload validation, actor and authority checks, evidence and subject
bindings, applicable approvals and
idempotency protections as an ordinary write. Invalid or stale requests leave
all current records and the work version unchanged.

These existing records remain editable after their producing step has passed.
Research control markers (`frame_research`, `record_finding`, `record_report`,
and `record_conclusion`), checkpoints and native jobs are not record edits.
Ordinary `bind_evidence` also remains available after the workflow reaches or
passes a step declaring it, under one active contract and the same evidence,
membership, actor, lifecycle, version and idempotency checks. Prior bindings do
not prevent binding fresh independent evidence.
`confirm_premise` also remains available once the plan completes while the
active contract's confirmation is missing: a completed plan has no frontier
step to declare it, yet completion requires that confirmation, so its
declaring step keeps admitting the action for the active contract under the
same coordinator-actor, contract-version, version and idempotency checks.
This route supplies the confirmation; it never bypasses the completion gate,
which still refuses without the recorded confirmation.
Two more completion prerequisites keep their declaring steps on a completed
plan, under the same coordinator-actor, contract-version, version and
idempotency checks. `approve_contract` remains available while the work holds
no active contract: completion requires one and only `approve_contract` records
the first, so its declaring step keeps admitting the initial approval. The
route closes once a contract exists; later contract versions belong to
`revise_intent`. For non-research plans, `record_delivery` remains available while the delivery
assertion is missing: the natural order records the final review before the
delivery, and completion requires the assertion, so its declaring
step keeps admitting the first delivery recording. Once recorded, this
section's current-record replacement owns delivery corrections. Neither route
bypasses the completion gate, which still refuses a missing prerequisite
where that prerequisite is required.
A record-only replacement holds the current step, instance state and lifecycle;
it does not repeat the original forward transition. In particular,
`record_delivery` replaces a completed item's reported delivery links without
reopening it, rerunning completion or inventing new delivery evidence.
The completed item's identity and terminal state remain intact. This permits
record correction, not new execution on completed work.

The same successful request with the same idempotency identity returns its
original result without another write. A different replacement request uses a
new identity and current expected version. No retry-count budget or attempt
limit gates ordinary retry or repeated correction.
Per-operation seconds budgets and error diagnostics remain required.

The coordinator obtains proof applicable to a changed candidate under CD-0242
D7. A replacement does not relabel earlier evidence or resolve a finding in the
linked Review. The coordinator preserves required Review, verification and
approval for the subject they judge; the core does not compare Review reports
or candidate commits. A corrected link alone does not manufacture proof.

Each action owns its current record; no separate correction operation, state,
route registry, target-event tuple, return to a producer or Review-consumption
binding mediates replacement. The same admission owner serves writes, pins,
liveness and conformance. Checks cover repeated replacement, atomic refusal,
rollback and proof validity without correction-specific route machinery.

Upgrade preserves the effective delivery as the sole current delivery record.
Verified-snapshot upgrade, interruption and rollback proof remain required.

A pre-effect refusal with a known absence of effects permits a corrected
request only within unchanged authority and approved scope. An operator denial
or authority boundary remains binding. A possible or unknown effect requires
reconciliation before another attempt; a later no-effect refusal does not clear
that uncertainty. Correction never bypasses an approval or effect boundary.

## 5. V15 projections, constraints, and state tables

The tables below are the workflow engine's current-state tables.
Each mutation writes current rows in one transaction checked by an expected version and recorded by its idempotency key ([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Direct writes are refused; only
`ApplyOperationTx` and the completion fold mutate these tables.
`work_id` references the
existing `work_items(id)` key; actor references point to `workflow_actors`.

### 5.1 Tables

Each instance carries its own step plan ([section 2](#2-workflow-type-templates-and-step-plans)).

| Table | Columns (types) | Keys, foreign keys, and closed checks |
|---|---|---|
| `workflow_instances` | `work_id TEXT`, `workflow_type TEXT NULL`, `step_plan TEXT NULL`, `instance_state TEXT`, `execution_actor_ref TEXT NULL`, `started_at TEXT NULL`, `completed_at TEXT NULL`, `last_checkpoint_at TEXT NULL` | PK `work_id`; FK work; FK actor nullable; checks type enum or null, plan JSON object or null, state enum |
| `workflow_contracts` | `work_id TEXT`, `contract_version INTEGER`, `premise TEXT`, `legislative_subjects TEXT`, `consequence_class TEXT`, `route_conventions TEXT`, `approved_at TEXT`, `approved_by TEXT`, `superseded_by INTEGER NULL`, `spec_mandate TEXT` | PK `(work_id,contract_version)`; FK work and approved actor; self-FK superseded contract |
| `workflow_candidate_sets` | `work_id TEXT`, `contract_version INTEGER`, `candidate_kind TEXT`, `candidate_ref TEXT`, `candidate_role TEXT`, `candidate_scope TEXT`, `recorded_at TEXT`, `recorded_by TEXT` | PK `(work_id,contract_version,candidate_kind,candidate_ref)`; composite FK contract; FK actor; checks closed candidate kind/role |
| `workflow_backlog_alignment` | `work_id TEXT`, `related_work_id TEXT NULL`, `searched TEXT`, `outcome TEXT`, `recorded_at TEXT`, `recorded_by TEXT` | PK `(work_id,related_work_id)`; FK work for both ids; checks closed outcome, a `none_found` work records no match rows, and no self-match; the declaration row carries a null `related_work_id`. Work removal clears rows naming the removed item in either column, because both foreign keys are `RESTRICT`. It preserves the original search outcome and surviving evidence under [§11.1](#111-retained-alignment-records). It does not reuse `workflow_candidate_sets`, whose composite FK binds to `workflow_contracts(work_id,contract_version)` and which therefore cannot hold a row before planning approves a contract |
| `workflow_actors` | `actor_ref TEXT`, `principal_ref TEXT`, `client_ref TEXT`, `agent_ref TEXT`, `session_ref TEXT`, `actor_class TEXT`, `first_seen_at TEXT` | PK `actor_ref`; UNIQUE tuple `(principal_ref,client_ref,agent_ref,session_ref)`; checks nonempty tuple and `actor_class IN ('agent','operator')` |
| `workflow_checkpoints` | `work_id TEXT`, `checkpoint_id TEXT`, `step_id TEXT`, `step_kind TEXT`, `attempt_epoch INTEGER`, `accepted_inputs_digest TEXT`, `result_evidence_refs TEXT`, `resume_cursor TEXT`, `idempotency_identity TEXT`, `actor_ref TEXT`, `request_id TEXT`, `recorded_at TEXT` | PK `(work_id,checkpoint_id)`; UNIQUE `(work_id,step_id,attempt_epoch)` and `(work_id,idempotency_identity)`; FK work/actor; closed step-kind check |
| `workflow_impact_edges` | `work_id TEXT`, `edge_id TEXT`, `edge_kind TEXT`, `edge_class TEXT`, `target_work_id TEXT`, `target_kind TEXT`, `severity TEXT`, `recorded_at TEXT` | PK `(work_id,edge_id)`; FK source/target work; closed edge/class/severity checks; hard `depends_on` and `forward_link` cycle check in transaction |
| `workflow_decision_records` | `work_id TEXT`, `question TEXT`, `options_considered TEXT`, `decision TEXT`, `rationale TEXT`, `consequences TEXT`, `inputs TEXT`, `poc_findings TEXT`, `supersedes TEXT NULL`, `superseded_by TEXT NULL`, `recorded_at TEXT` | PK `(work_id,question)`; FK work; checks decision enum and required nonempty fields. Retained rows written before the step-plan cutover; they remain readable as recorded decisions, and the cutover's six-part plan record carries new in-flight decisions, so no current action writes new rows |
| `workflow_premise_confirmations` | `work_id TEXT`, `contract_version INTEGER`, `confirmed_by TEXT`, `confirmed_at TEXT` | PK `(work_id,contract_version)`; composite FK contract; FK actor; actor class must be `operator` |

Phase A does not invent a second candidate-role or scope authority. The
`candidate_role` projection column is closed to the single value `include`, and
`candidate_scope` is the canonical empty JSON object `{}`. Both are derived
values because the candidate-set record carries no
role or scope fields. `consequence_class` is not defaulted: it is an explicit
closed field recorded with contract approval. `target_kind` is closed to
`work_item`, matching the `target_work_id` foreign key; product/project targets
are not silently represented by a work foreign key.

JSON-encoded columns above are typed arrays or the referenced strict schema, not
arbitrary JSON objects. SQLite `CHECK` constraints validate the closed scalar
values; application validation validates the bounded array payload before the
transaction. A contract binds to the item whose plan authorized its approval;
contracts carry no definition pin
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

`workflow.evidence_bound` does not create a second evidence authority. Its
`producer_run_ref` must identify an existing `durable_operations` row with
`work_id` equal to the recorded subject, `principal_ref` equal to `producer_id`,
`request_id` equal to `producer_watermark`, and a completed result whose
authoritative `evidence_refs` contains the same immutable subject. Verification
consumes these authoritative bindings, not workflow-only metadata or an
operation from another work item
([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

Evidence bindings are attributed claims, not producer attestations. Independent
Review judges evidence adequacy. Concord runs no repository checks and retains no runner,
materializer, preparation, isolation, verification lease, output stream or
native receipt. It provides no oracle-output reader or live-Git workflow
freshness gate. The verified-snapshot upgrade precedes removal of stored
execution data.

### 5.2 Current-row effects

Workflow state is current rows written by each mutation in one transaction, and recovery uses verified snapshots plus reconciliation of current rows with Git, worktrees, artifacts, and external effects ([CD-0242 D2](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

| Record | Current-row effect |
|---|---|
| intake recorded | insert the intake record row (its outcome and which of the fourteen checks found something) |
| clarify recorded | insert the clarify record row resolving the open questions intake found |
| type picked | write the item's step plan in one row update through the plan-write check (§2.3); the initial pick copies the type's template; a later pick adds the new type's missing required steps only |
| step inserted | add the inserted step through the plan-write check; an existing step never changes |
| step completed | append the step's attempt through the plan-write check; the frontier derives from completions, and no step pointer is stored |
| contract recorded/updated | insert the immutable contract version; an ordinary update atomically makes the next version current under §4.1 |
| candidate set revised | insert added candidates and delete only the named removed candidates for that contract; premise and outcome bytes are untouched |
| backlog alignment recorded | insert the declaration row and, when the outcome is `related_found`, one row per named work id; the alignment action records the candidate set only and creates no relation |
| actor recorded | insert once; a different tuple for an existing `actor_ref` is refused |
| action started/checkpointed/completed/failed | update instance state/checkpoint timestamps and insert/update checkpoint rows; a repeated idempotency key returns the stored result without a second effect |
| evidence bound | write to the existing evidence authority; it is not copied into a workflow-only authority |
| premise confirmed | insert operator confirmation |
| successor linked | write the existing `forward_link` relation; workflow state remains independent |
| completed | retain terminal current-state identity and reported delivery links and dispose workflow step records atomically after the completion transaction's prior clauses pass; no publication, closure hash or deferred cleanup |
| work item terminal (`work.transitioned` to `completed` or `cancelled`) | close a live instance in the same transaction: `cancelled` and completed-by-lifecycle both record `cancelled` because the completion gate never ran; an instance already terminal keeps its record |

## 6. Independent Review

Main obtains independent native Task Review of the candidate and records the
reviewed step's reviewer, result, commit and actual result link. Review assesses
whether the delivery and evidence meet the agreed outcome. Intermediate fixed
attempts are allowed; final review requires a clean pass. The record does not
execute Review or create a separate typed report.

## 7. Ordered completion gate

The `complete` action executes in one SQLite transaction. It locks the work
item, checks that every step in the item's plan is completed and the expected
version, and rolls back on refusal.

Completion requires the active contract's recorded premise confirmation and,
for change, fix and removal, a recorded delivery assertion. It does not recheck
evidence bindings, scope or mandate satisfaction, verification
obligations, criterion proof, verdict health or association, current Review,
staleness rules, or evidence-to-candidate commit drift.
Mandated-law presence is not a completion condition.
Verification, Review, delivery-stage admission and premise confirmation keep
their own requirements. The active contract, legal terminal action and state,
expected version, authenticated actor, and durable operation remain required.
Completion runs no law-attribution or enactment check
([CD-0230 D4](../decisions/CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md#d4-law-changes-follow-the-operators-approval-boundary)).

| Clause | Exact refusal | Recovery action |
|---|---|---|
| The gate reads, terminal metadata, lifecycle change, and the completion record commit in one transaction | `operation_conflict` | `reconcile_operation` |

A response is not terminal until the commit
returns success. A failed commit produces no terminal state mutation.

## 8. Actor tuple

The authenticated mutation context supplies `(principal_ref, client_ref, agent_ref,
session_ref)`. The engine validates all four are nonempty, canonicalizes each as a
bounded reference, and hashes these exact UTF-8 bytes with SHA-256:
`actor-v1\0principal_ref=<byte-length>:<value>|client_ref=<byte-length>:<value>|agent_ref=<byte-length>:<value>|session_ref=<byte-length>:<value>|`, where `\0` is one U+0000 NUL byte; the two literal characters backslash-zero are invalid.
`actor_ref=actor:<64 lowercase hex characters>` is the immutable identity. The
actor row stores the tuple once; later records carry only
`actor_ref`.

`principal_ref` alone never establishes authority. Actor rows and tuple fields
cannot be updated or deleted; a changed tuple receives a new actor reference.

For `confirm_premise`, the invoking agent tuple is never converted to an
operator by changing `actor_class`, and `confirming_actor_ref` is not accepted
from the workflow payload. The core authenticates the invoking client against
the trusted-client key and policy, requires an agent-class non-worker
coordinator, and records that identity as the confirmation's
`confirming_actor_ref` with coordinator authority in the same transaction as
the authorized intent. Replayed, mismatched, or
payload-selected identities cannot substitute another actor. Idempotent replay
returns the stored result without recording the confirmation again.

### 8.1 Operator decisions stay in conversation

The step machine has no operator-specific step kind, question projection,
withholding, semantic choice input, or decision-context digest. Invoking
`confirm_premise` records authenticated coordinator confirmation.

Agents ask operator questions through the host question tool when CD-0243 D2
requires a decision and quote the answer in the ordinary workflow step record.
The quotation supplies context, not authentication or mutation authority.
The core does not interpret it as a grant. No adapter or store question route
substitutes for the host tool.

An upgrade that removes a step kind first takes the existing verified snapshot.
It preserves ordinary checkpoint rows and their constraints, mapping the retired
operator kind to `internal_sqlite` without discarding checkpoint payloads or
attribution.

## 9. Workflow composition

`modifies`, hard/soft `depends_on`, and `forward_link` are typed edges. `modifies`
is not cycle-checked. Hard `depends_on` and `forward_link` use the existing
in-transaction recursive cycle check.

## 11. `workflow_action` dispatch

### 11.1 Retained alignment records

Ordinary work removal drops the removed item's match rows from surviving
alignments ([CD-0142 D4](../decisions/CD-0142-operator-directed-execution-removal.md#d4-absence-survives-reads-recovery-and-renewal)).
It preserves each surviving declaration's search, original outcome, author and
time, other matches, and applicable effect and idempotency evidence.
An absent declaration means no recorded search, not a search whose matches were removed.

Recording guards still refuse `related_found` without match IDs and
`none_found` with match IDs. The initiative cutover still refuses
sole-initiative matches under
[CD-0213 D8](../decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md#d8-retired-planning-kinds-keep-no-history);
the ordinary removal state does not relax that upgrade gate.

`concord_work_transition.workflow_action` is the only advancement surface. The
dispatcher performs this order:

1. Check `WorkflowActionAvailable`; refuse structurally until the item's
   workflow instance holds a readable state: before a type is picked only the
   intake prelude actions are available; after the pick, the plan's derived
   frontier admits its actions ([section 2](#2-workflow-type-templates-and-step-plans)).
2. Validate the closed action ID and payload against the action catalog's
   entry. Existing-record replacement follows §4.2.
   Unknown fields, missing fields, wrong bounds, or an action
   outside its admitted ordinary use return the existing `invalid_transition`
   refusal with recovery action `reread_entities`.
3. Validate work ID, expected work version, authenticated actor tuple, and current
   step. Stale versions return `version_conflict`; an incomplete actor returns
   `unauthorized`.
4. Validate authorization and any evidence the action binds. A cross-scope action
   requires `cross_scope`; otherwise the core returns `unauthorized`.
5. Check the durable idempotency identity. A replay of the same operation
   returns the original envelope without a second effect; a conflicting payload
   returns `idempotency_conflict`.
6. Execute the admitted graph mutation in one SQLite transaction, recording its
   operation result and effect receipts. External execution remains native-owned.
7. Construct the existing TS7 envelope with `outcome`, `authority`, typed evidence,
   warnings, and recovery metadata. Refusals are `error`; no fallback advancement
   path exists.

| Internal condition | TS7 error kind | Required recovery action |
|---|---|---|
| Missing/unsupported registry entry or pinned digest mismatch | `invariant_violation` | `reread_entities` |
| Unknown action, undeclared payload field, missing payload field, or wrong graph step | `invalid_transition` | `reread_entities` |
| Stale expected work version | `version_conflict` | `reread_entities` |
| Incomplete actor tuple | `unauthorized` | `contact_operator` |

Registry-specific availability/drift names and action-payload-specific names are
not envelope kinds and must never escape the core.
CD-0013 D10 adds exactly one new closed kind, `outcome_mismatch`, with recovery
action `contact_operator`. The current generated manifest and envelope schema carry
that classification; no agent-surface version, compatibility matrix, migration
guidance, or old-client path is part of this pre-go-live corpus. No other error kind
is added or renamed here.

The corpus records `engine_status=engine_shipped`. The checker compares every scenario's expected
error kind with the current closed envelope enum; unknown classifications fail
validation rather than being downgraded to prose or generic success.

## 12. Workflow types, prelude, and step admission

Every workflow starts with **intake**: the driving session records the fourteen
decided checks — which of them found something — and one of five outcomes
(`proceed`, `clarify`, `already_handled`, `decline`, `split_or_merge`). **Clarify**
runs only when intake found open questions, and intake's record says so when it
found none. The outcomes `already_handled`, `decline` and `split_or_merge` end or
replace the item without picking a type. Then the type is picked
(`change`, `fix`, `research` or `removal`) and its required steps are copied from
the template contract into the item's plan
([section 2](#2-workflow-type-templates-and-step-plans)).

**Inserted steps.** Between required steps the driving agent may insert a step
into the item's plan, and the engine does not refuse it. An inserted step
anchors itself by declaring its own `after` edges; an existing step's `after`
edges never change. A send-back is a new inserted step after the current one,
never an edge pointing backwards, so the plan stays a DAG.

**No skip move.** After a step completes, the agent either proceeds to a next
step or inserts a step. Nothing skips a required step. Clarify running only when
intake found open questions, the three ending intake outcomes, and the final
clean review repeating until one needs no fixes, are not skips.

**Reviewed steps.** From implementation onward, a step passes only when a
`concord-review` native subagent has passed it or fixed it. The step's
completion record keeps the reviewer, result, commit and link. A final clean
review needs no fixes before completion. Each step names its method skill and
the resume read shows it; no check verifies that a skill was loaded.

**Completion follows the plan.** An item completes only when every required
step in its plan is completed.

### 12.1 Action-to-event mapping

The current action catalog and plan admission govern availability. Except for
plan writes, starts and execution checkpoints, semantic events are
followed by `workflow.action_completed`; this records the action, not completion
of a plan step. `record_review` and `complete_step` use the same step-attempt writer.

| Action IDs | Semantic event mapping |
|---|---|
| `record_intake`, `record_clarify`, `pick_type`, `insert_step`, `complete_step` | `workflow.intake_recorded`, `workflow.clarify_recorded`, `workflow.type_picked`, `workflow.step_inserted`, `workflow.step_completed`, respectively |
| `record_plan`, `record_removal_scope` | `workflow.plan_recorded`, `workflow.removal_scope_recorded`, respectively, plus `workflow.action_completed` |
| `approve_contract`, `revise_candidates`, `record_diagnosis` | `workflow.contract_approved`, `workflow.candidate_set_revised`, `workflow.diagnosis_recorded`, respectively, plus `workflow.action_completed` |
| `record_review` | `workflow.step_completed` with the reviewed attempt |
| `bind_evidence`, `record_report` | `workflow.evidence_bound` plus `workflow.action_completed` |
| `confirm_premise`, `link_successor` | `workflow.premise_confirmed`, `workflow.successor_linked`, respectively, plus `workflow.action_completed` |
| `frame_research`, `record_finding`, `record_conclusion`, `record_delivery` | `workflow.action_completed`; delivery carries its assertion and links |
| `start_execution` | `workflow.action_started` |
| `checkpoint_execution`, `checkpoint_refine`, `checkpoint_repair` | `workflow.action_checkpointed` |
| `complete` | `workflow.completed` and the terminal lifecycle transition, after the completion gate |

Whole-work delivery still requires complete parent admission, integration
evidence, independent Review and applicable approvals; a report supplies none
of those authorities by itself. Required CI remains external and mandatory.

### 12.2 Action definitions and composition

Native Task execution belongs to the host, not workflow action composition
([CD-0242 D6](../decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-native-task-belongs-to-the-host)).

The action catalog carries closed payload fields and execution modes. The
template contract declares actions available on required steps; admission uses
the item's plan frontier. Plan writes pass the check in §2.3, and completion
requires every plan step to be complete.

Required steps derive from the single template contract and are copied into the
item's plan.

Composition across work items is forward-link only. An implementation change
cannot nest under research. A successor retains independent authority and
recovery; shared steps follow CD-0242 D4.

## 13. Remaining implementation-deferred items

The following list matches CD-0013's deferred list exactly. The built-in v1 graph
and action table above is the contract's chosen implementation fill; these items
remain deferred for extensions, catalogs, and runtime policy not required to
interpret the v1 carriers:

1. **Per-family action enumeration.** Each family's available action list and the
   exact step graph are implementation work, not part of this decision. The
   decision fixes the shape; the implementation fills it.
2. **Audience commitment storage.** Runtime audience band storage and the
   effective-rigor evaluation routine are implementation work.

The contract does not invent audience storage or a new family.

## Acceptance criteria

- Given a plan write that removes a required step, changes a step's after
  edges, completes a step before every step it comes after, or alters a
  completed step
  When the write is dispatched
  Then the engine refuses with invalid_input, records no plan change, and the
  stored plan is unchanged.

- Given a completion that satisfies the ordered completion gate
  When the completion transaction commits
  Then terminal metadata, lifecycle change, reported delivery links and the
  completion record commit together or not at all.

## Verification

The two criteria bind scenarios of `.concord/scenarios/workflow-engine.v1.json`,
executed against production dispatch by
`TestWorkflowScenarioCorpusExecutesExactProductionActions`
(`internal/store/workflow_conformance_test.go`).

- Criterion 1 is proved by the bound `WF37-action-availability-before-register`
  scenario, which dispatches before the definition is registered and asserts
  an invariant_violation refusal with no fallback.
- Criterion 2 is proved by the bound `WF19-completion-one-transaction`
  scenario, which fails a completion inside its transaction and asserts that
  no completion record, terminal metadata, or lifecycle change
  remains.
