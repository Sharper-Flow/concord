# Concord Workflow Engine Contract

> **Status:** Implementation contract for accepted CD-0013, v1.
> **Authority:** [`CD-0013`](./decisions/CD-0013-workflow-engine-mechanism.md),
> amended only by a later accepted CD-NNNN.
> **State and definitions ([CD-0242](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
> D2, D4, D6, and D14):** the engine keeps current workflow state and finite
> snapshots with no event log, fold, replay, or rebuild. Attempts carry their
> own authority with no escalation wall. Families compose shared stages under
> the decision-model minimum, and generated definitions derive from one source.
> **Scope:** Go registry, current workflow state, outcome evaluation,
> completion, conditions, impact propagation, and the reserved `workflow_action`
> dispatch path.
> **Conformance carriers:** [`workflow-engine.v1.json`](../scenarios/workflow-engine.v1.json),
> [`workflow-definition.schema.json`](../../contracts/workflow-definition.schema.json),
> [`workflow-outcome.schema.json`](../../contracts/workflow-outcome.schema.json).

This document fills the implementation-design boundary left by CD-0013. It does
not add a workflow family, predicate kind, authority, or agent tool. The
workflow tables are current state written one mutation at a time;

CD-0015 adds the law boundary without adding a workflow tool.
Only contracts approved through surface `2.2.0` carry the internal persisted
law-boundary marker: for those contracts, `spec_mandate` is the bounded
referenced-law set and `law_modifies` is an optional bounded subset. Planning
checks the accepted Git-derived `law_subjects` and `law_relations` projection;
an explicit conflict may pass planning only when the endpoint whose record
declares it is in `law_modifies`. Completion reruns the check without that
exception, so only a Git-authorized law delta can resolve the conflict.

CD-0036 records each current contract's bounded `law_revisions` pins as
`(law_id, content_hash)`. A changed hash under the same accepted ID is a
compatible amendment. A new accepted ID that supersedes a mandated old ID is
a breaking cutover: the old consumer receives typed `stale_law_revision` and
workflow-authority actions, claims, worker-result acceptance, and completion
stop until one operator-approved `supersede_contract` action atomically installs
a fully supplied successor contract, or the work reaches a terminal
cancellation/supersession. Commit OIDs remain audit context only; recovery pins
the accepted successor law and preserves the current nonterminal workflow step.

CD-0016 governs V22 context-continuity projections and the two closed workflow
events `workflow.context_checkpointed` and `workflow.context_boundary_crossed`.
`checkpoint_context` and `cross_context_boundary` remain actions on the
existing workflow-action route. The canonical `C19.Continuity` read has a pinned projection derived from
durable workflow state and never from advisory summary text.
The two actions belong to every built-in definition and step through one owning
definition builder, with strict payload fields and no approval. The explicit
continuity read does not automatically inject pinned context into current
OpenCode calls; typed-agent injection on restart belongs to issue #120.

[CD-0243 D7](decisions/CD-0243-four-roles-and-six-operator-approval-classes.md#d7-the-remaining-owners-use-the-same-approval-boundary)
qualifies every operator-consent clause in this record, including tables and
acceptance criteria. Other admission, safety, evidence, and historical-pin
requirements remain required.

## Context

The workflow engine carries a work item from an approved contract to a
completed outcome. The binding inputs are CD-0013 as amended by CD-0015,
CD-0016, and CD-0036, the typed carriers it names, and the scenario corpus
with the two schemas it cites. This record fills the implementation boundary
CD-0013 left open: the common representation, the definition registry and
digest check, the instance state machine, current workflow state, outcome
evaluation, the ordered completion gate, the actor tuple and verdict
authority, impact edges and notices, external condition refusal,
dispatch, and the built-in family graphs.

## Contract

For definitions declaring `approval_policy=consequence_v1`, CD-0013 D11 qualifies every operator-only contract, premise, verdict, correction, supersession, and completion clause in this contract, including their actor mappings. Engineering records a valid contract and coordinator confirmation; review records verdicts without consent, and Implement cannot accept its own result ([CD-0242 D5](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)); coordinators record completion against those verdicts. No synthetic operator or approval evidence is created. Decision-bearing checkpoints retain human authority. Generated definitions derive from one source, and every evidence, recovery, source, and liveness obligation remains required ([CD-0242 D14](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

The binding contract is sections 1 through 12: the common representation and
bounds, the definition registry, the instance state machine, current workflow
state, outcome evaluation and strength, the
ordered completion gate, the actor tuple and verdict authority, impact
edges and notices, external condition refusal, dispatch, and the built-in
family graphs and actions. Section 13 records implementation-deferred items
and carries no obligation.

## 1. Common representation and bounds

All identifiers are UTF-8 strings with 2–128 characters unless a narrower pattern
is stated. Timestamps are RFC3339 UTC strings, maximum 64 characters. Digests are
lower-case `sha256:` followed by 64 hexadecimal characters. Recorded mutation
payloads are typed objects, never free-form maps; unknown keys are refused
([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

| Value | Type/bound |
|---|---|
| `work_id`, `step_id`, `action_id`, `checkpoint_id`, `condition_id`, `edge_id` | identifier, 2–128 chars |
| `ref` | lower-case registry/reference string, 2–128 chars |
| `version`, `contract_version`, `attempt_epoch`, `request_id` sequence | positive integer, max 2,147,483,647 |
| evidence/reference list | unique array, 0–32 items, each identifier |
| candidate list | unique array, 1–100 items |
| `actor_ref` | `actor:` plus lower-case content-hash reference |
| `payload_version` | integer exactly `1` for v1 emission |

## 2. Definition registry

### 2.1 Go-side shape

The registry is a process-owned, immutable-after-registration map keyed by
`(ref, version)`. The value is equivalent to the strict manifest in
[`workflow-definition.schema.json`](../../contracts/workflow-definition.schema.json):

```go
type WorkflowDefinition struct {
    Ref                   string
    Version               int64
    WorkKind              WorkKind
    StepGraph             StepGraph
    AvailableActions      []ActionID
    RequiredEvidenceKinds []EvidenceKind
    OutcomeSchema         OutcomeSchema
    RigorRules            []RigorRule
    StalenessRules        []StalenessRule
    CompositionRules      CompositionRules
}

type RegisteredDefinition struct {
    Definition WorkflowDefinition
    Digest     Digest // sha256 of canonical definition bytes
}
type DefinitionRegistry interface {
    Register(WorkflowDefinition) (RegisteredDefinition, error)
    Lookup(ref string, version int64) (RegisteredDefinition, bool)
    Verify(ref string, version int64, digest Digest) error
}
```

`StepGraph` is a finite directed graph. Every step names its step kind and closed
action IDs; every edge names `forward`, `retry`, `optional`, or `failure`. The
graph has one start step, at least one terminal step, no missing endpoints, and no
edge that escapes the graph. Cycles are permitted only through `retry` edges.

### 2.2 Digest and registration

The digest input is the manifest without `digest`, serialized as UTF-8 JSON with
the schema's property order, no insignificant whitespace, normalized integer
values, and arrays in their declared order. Strings are not case-folded. The
registry computes `sha256(canonical_bytes)` and stores it as `sha256:<hex>`.

Registration refuses:

| Refusal | Condition |
|---|---|
| `invalid_definition` | schema, graph, action, evidence, predicate, or composition validation fails |
| `definition_version_conflict` | `(ref, version)` exists with another digest |
| `definition_version_not_monotonic` | a new version is not greater than the highest registered version for `ref` |
| `definition_digest_mismatch` | supplied digest differs from recomputed digest |
| `definition_action_or_step_unknown` | graph references an action not in `available_actions` |

The selected `ref`, `version`, and `digest` are written to the work item at
planning. Active work never adopts a
newer version. Idle work may adopt the newest version only through an explicit
planning operation that writes the new selection.

### 2.3 Availability and drift check

`WorkflowActionAvailable(work_id)` performs this sequence before authorizing any
action:

1. Read the instance's pinned `(ref, version, digest)`.
2. Look up exactly that registry entry.
3. Recompute its canonical digest and compare it with both the registered digest
   and the pinned digest.
4. Verify the graph, action list, and payload schema are still supported by this
   binary.
5. Return `true` only when all comparisons pass.

Missing registration, unsupported definition, or any digest mismatch returns
`false` and the existing closed `invariant_violation` refusal with recovery action
`reread_entities`. No new refusal kind is introduced for registry availability or
drift. No fallback definition, newer version, or response-wording path is accepted.

## 3. Workflow instance state machine

`instance_state` is closed:
`planned`, `ready`, `running`, `blocked`, `awaiting_condition`, `verifying`,
`completed`, `cancelled`, `superseded`.

| From | To | Operation | Authority/fence |
|---|---|---|---|
| absent | `planned` | definition selected and capture/planning recorded | internal-inline |
| `planned` | `ready` | contract, spec mandate, and definition approved | internal-inline; operator approval evidence |
| `ready` | `running` | first action claimed | internal-inline for SQLite-only; fenced for cross-authority/external |
| `running` | `running` | action checkpoint or successful action | inline or current `attempt_epoch` fence |
| `running` | `blocked` | unresolved dependency or condition discovered | internal-inline |
| `blocked` | `awaiting_condition` | a typed external condition is registered (no current definition declares the condition action, [CD-0242 D14](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)) | internal-inline |
| `awaiting_condition` | `running` | owning resolver supplies valid evidence (no resolver exists; CD-0234 D1) | internal-inline |
| `running` | `verifying` | all execution steps are complete and verdict is requested | internal-inline |
| `verifying` | `completed` | ordered completion gate succeeds | one internal transaction |
| `running`, `blocked`, `awaiting_condition`, `verifying` | `cancelled` | explicit authorized cancellation | fenced when an attempt is owned |
| `planned`, `ready`, `running`, `verifying` | `superseded` | operator-approved `supersede_contract` supersedes the contract | internal-inline with successor link |

An action failure records its typed failure result; it does not invent a state.
Recoverable failures leave the instance in `running` or `blocked` according to the
definition edge. A completed, cancelled, or superseded instance is immutable.
Internal SQLite-only actions are one domain transaction.
Cross-authority and external-effect actions claim an epoch in
`durable_operations`, then record start, checkpoint, and result under the
current epoch.

## 4. Typed mutation records

The store keeps current state and finite snapshots ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Each
mutation writes current rows in one transaction checked by an expected version
and recorded by its idempotency key. The names below identify the typed
mutation records that the §12 action tables map actions to; omitted and unknown keys are
refused. Only `CompleteWorkflowTxWithRegistry` records `workflow.completed`,
after the ordered gate in §7; every other mutation route refuses it
structurally. `workflow.staleness_observed` records an external observation
and does not advance the work-item version.

Every mutating workflow payload also carries `expected_version` and
`resulting_version`, consecutive positive integers checked by the mutation;
the rows below do not repeat this pair.

`workflow.condition_cancelled` is the typed representation required by
CD-0013 D10's “resolved or canceled” completion clause. It adds no await type
or authority: operator cancellation is explicit, evidenced, and recorded
beside the two resolver states.

| Record | Exact payload fields and bounds |
|---|---|
| `workflow.definition_selected` | `work_id`, `ref`, `version`, `digest`, `work_kind` (closed family enum) |
| `workflow.contract_approved` | `work_id`, `contract_version`, `premise` (1–4096), `outcome_kind`, `outcome_payload` (strict outcome schema), `required_evidence` (0–7 unique), `route_conventions` (0–16 unique refs), `spec_mandate` (0–32 unique refs), `law_revisions` (one `{law_id, content_hash}` pin per mandated ID ), `rigor_class` (1–64), `consequence_class` (`internal_sqlite`, `cross_authority`, or `external_effect`) |
| `workflow.contract_superseded` | `work_id`, `previous_contract_version`, optional unique `predecessor_contract_versions` (1–32), `new_contract_version`, `supersede_reason` (1–4096), `audit_evidence` (1–32 evidence refs), optional `approval_ref` with the exact `approval_operation_digest`, `approval_scope_json`, `approval_versions_json`, and `approval_consequence` binding, and optional `successor_contract` containing the fully validated contract fields and one current `law_revisions` pin per mandated law; recovery installs this successor and marks every named predecessor superseded in one transaction |
| `workflow.candidate_set_revised` | `work_id`, `contract_version`, `candidate_kind` (exactly `work_item`, `product`, or `project`), `candidate_ref`, `added` (0–100 refs), `removed` (0–100 refs); `added` and `removed` are disjoint and not both empty |
| `workflow.actor_recorded` | `work_id`, `actor_ref`, `principal_ref`, `client_ref`, `agent_ref`, `session_ref`, `actor_class` (`agent` or `operator`) |
| `workflow.action_started` | `work_id`, `step_id`, `action_id`, `attempt_epoch`, `accepted_inputs_digest`, `idempotency_identity` (2–128), `actor_ref` |
| `workflow.action_checkpointed` | `work_id`, `step_id`, `step_kind` (`internal_sqlite`, `cross_authority`, `external_effect`, or the existing definition-schema `human_checkpoint`), `attempt_epoch`, `checkpoint_payload` (typed action checkpoint, max 16 KiB), `resume_cursor` (0–2048), `actor_ref`, `request_id` |
| `workflow.action_completed` | `work_id`, optional `action_id`, `step_id`, `attempt_epoch`, `result_evidence_refs` (0–32), `changed_refs` (0–32), `actor_ref`, optional `worker_attempt_id`. `worker_attempt_id` is allowed only for `accept_worker_result`, `record_worker_failure`, and `dispatch_worker`. It rejects undeclared current-step actions and invalid worker-result records. |
| `workflow.proposal_recorded` | `work_id`, `problem` (1–4096), `affected` (1–16 unique entries of 1–256), `stakes` (1–2048), `user_outcomes` (1–16 unique entries of 1–512), `constraints` (0–16 unique entries of 1–512, omitted when absent), `open_questions` (0–16 unique entries of 1–512, omitted when absent), `out_of_scope` (0–16 unique entries of 1–512, omitted when absent; CD-0202) |
| `workflow.design_recorded` | `work_id`, `approach` (2–4096), `decisions` (1–16 typed decisions with `id`, `question` (1–512), `choice` (1–1024), `rationale` (1–1024), and `rejected` (0–8 strings)), `touched_refs` (1–64 references) |
| `workflow.backlog_alignment_recorded` | `work_id`, `searched` (2–4096, the search performed and its scope), `outcome` (`related_found` or `none_found`), `related_ids` (1–64 unique work ids, present only when `outcome` is `related_found`; the mutation refuses `related_found` with no ids and `none_found` with ids, and every id must name a real work item) |
| `workflow.action_failed` | `work_id`, `step_id`, `attempt_epoch`, `failure_kind` (closed TS7 error kind), `recoverable` (boolean), `actor_ref` |
| `workflow.evidence_bound` | `work_id`, `evidence_kind` (verification/review/approval/commit/durable_note/native_run/artifact), `immutable_subject_ref`, `producer_id`, `producer_run_ref`, `producer_watermark`, `observed_at`. For `evidence_kind=native_run` the `immutable_subject_ref` is the record's `xobs:` observation and the attributed record must be verified: an unverified report stays readable but cannot satisfy completion (CD-0040 D9), so the binding fails closed until a verification record answers the observation. |
| `workflow.staleness_observed` | `rule_id`, `severity` (`warning` or `block`), `drifted` (boolean), `observed_at` (RFC3339), `accepted_inputs_digest` (SHA-256); the latest record for each rule is the staleness authority used by completion and reads, and exact action retries retain the first observation time |
| `workflow.native_run_recorded` | `work_id`, `run_id` (1–128), `native_subject_ref` (1–2048 opaque non-secret), `subject_digest` (core-derived), `phase` (`start`/`health`/`rollback`/`cleanup`, fixed by the action ID), `status` (closed per-phase vocabulary), `evidence_ref`, `evidence_digest`, `asserted_at` (RFC3339, within the two-minute report skew bound), `reporting_authority_ref` (the authenticated actor's client, never caller input), `actor_ref`, and the embedded CD-0040 capture component: `capture_method`, the closed `observed_universe` structure (one item, exactly scoped, complete by closed-structure digest — validated by the shared D4 rules when recorded and read), the derived content-hashed `freshness_policy_ref`/`divergence_policy_ref` from the reviewed per-kind register, and the `observation_id` the run's phases share. The mutation registers that observation in `external_observations` as its shared provenance home, so one verification record answers every phase row and the attributed read (`NativeRunReport`) carries the derived verification state. |
| `workflow.verdict_recorded` | `work_id`, `contract_version`, `predicate_id`, `verdict_kind` (`ok`, `outcome_mismatch`, `insufficient_evidence`), `verdict_actor_ref`, `evaluation_evidence` (1–32), `incomparable_with_approved` (boolean) |
| `workflow.premise_confirmed` | `work_id`, `contract_version`, `confirming_actor_ref` |
| `workflow.successor_linked` | `work_id`, `successor_work_id`, `relation_kind` (`forward_link`) |
| `workflow.impact_declared` | `work_id`, `edge_id`, `edge_kind` (`modifies`, `depends_on`, `forward_link`), `edge_class` (`hard`, `soft`, `none`), `target_work_id`, `target_kind` (exactly `work_item`, matching the target-work foreign key), `severity` (`breaking`, `non-breaking`, `informational`) |
| `workflow.impact_notice_recorded` | `work_id`, `notice_id`, `source_contract_version`, `entity_kind`, `entity_ref`, `target_work_id`, `edge_owner_work_id`, `edge_id`, `old_hash` (nullable digest), `new_hash` (nullable digest), `severity` |
| `workflow.condition_added` | `work_id`, `condition_id`, `await_type` (`pr_merge`, `ci_result`, `timer`, `human_approval`, `remote_work_state`), `await_ref`, `resolution_authority` |
| `workflow.condition_resolved` | `work_id`, `condition_id`, `resolution_evidence` (1–32 evidence refs), `resolved_by_event`; the stored `resolution_authority` is exactly `durable_operation:<op_id>`, and every evidence ref must be present in that completed operation's authoritative `evidence_refs` for this work item |
| `workflow.condition_cancelled` | `work_id`, `condition_id`, `cancellation_authority` (`operator`), `cancellation_evidence` (1–32 evidence refs), `cancelled_by_event`; the recording actor must resolve to an `operator` actor tuple, and every cancellation evidence ref must be present in the stored durable authority operation |
| `workflow.completed` | `work_id`, `terminal_state` (`completed`, `cancelled`, `superseded`), `final_verdict_kind`, `verdict_actor_ref`, `premise_confirmed` (boolean), `evidence_count` (non-negative, derived from the item's bound evidence history), `changed_refs_digest` (digest), `impact_verdict` (`breaking` or `non-breaking`), `warnings` (0–16 staleness-rule IDs) |

## 5. V15 projections, constraints, and state tables

The tables below are the workflow engine's current-state tables.
Each mutation writes current rows in one transaction checked by an expected version and recorded by its idempotency key ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). Direct writes are refused; only
`ApplyOperationTx` and `CompleteWorkflowTxWithRegistry` mutate these tables.
`work_id` references the
existing `work_items(id)` key; actor references point to `workflow_actors`.

### 5.1 Tables

| Table | Columns (types) | Keys, foreign keys, and closed checks |
|---|---|---|
| `workflow_instances` | `work_id TEXT`, `definition_ref TEXT`, `definition_version INTEGER`, `definition_digest TEXT`, `current_step TEXT`, `instance_state TEXT`, `execution_actor_ref TEXT NULL`, `started_at TEXT NULL`, `completed_at TEXT NULL`, `last_checkpoint_at TEXT NULL` | PK `work_id`; FK work; FK actor nullable; checks positive version, digest, state enum |
| `workflow_contracts` | `work_id TEXT`, `contract_version INTEGER`, `premise TEXT`, `outcome_kind TEXT`, `outcome_payload TEXT`, `consequence_class TEXT`, `required_evidence TEXT`, `route_conventions TEXT`, `approved_at TEXT`, `approved_by TEXT`, `superseded_by INTEGER NULL`, `spec_mandate TEXT`, `definition_ref TEXT`, `definition_version INTEGER`, `definition_digest TEXT` | PK `(work_id,contract_version)`; FK work and approved actor; self-FK superseded contract; checks nonempty premise, closed outcome/evidence/consequence; every contract approval records the exact selected definition pin |
| `workflow_contract_law_revisions` | `work_id TEXT`, `contract_version INTEGER`, `law_id TEXT`, `content_hash TEXT` | PK `(work_id,contract_version,law_id)`; FK contract; reverse index by law ID; current rows written only by the contract-approval mutation ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)) |
| `workflow_candidate_sets` | `work_id TEXT`, `contract_version INTEGER`, `candidate_kind TEXT`, `candidate_ref TEXT`, `candidate_role TEXT`, `candidate_scope TEXT`, `recorded_at TEXT`, `recorded_by TEXT` | PK `(work_id,contract_version,candidate_kind,candidate_ref)`; composite FK contract; FK actor; checks closed candidate kind/role |
| `workflow_backlog_alignment` | `work_id TEXT`, `related_work_id TEXT NULL`, `searched TEXT`, `outcome TEXT`, `recorded_at TEXT`, `recorded_by TEXT` | PK `(work_id,related_work_id)`; FK work for both ids; checks closed outcome, a `none_found` work records no match rows, and no self-match; the declaration row carries a null `related_work_id`. Work removal clears rows naming the removed item in either column, because both foreign keys are `RESTRICT`. It does not reuse `workflow_candidate_sets`, whose composite FK binds to `workflow_contracts(work_id,contract_version)` and which therefore cannot hold a row before planning approves a contract |
| `workflow_actors` | `actor_ref TEXT`, `principal_ref TEXT`, `client_ref TEXT`, `agent_ref TEXT`, `session_ref TEXT`, `actor_class TEXT`, `first_seen_at TEXT` | PK `actor_ref`; UNIQUE tuple `(principal_ref,client_ref,agent_ref,session_ref)`; checks nonempty tuple and `actor_class IN ('agent','operator')` |
| `workflow_checkpoints` | `work_id TEXT`, `checkpoint_id TEXT`, `step_id TEXT`, `step_kind TEXT`, `attempt_epoch INTEGER`, `accepted_inputs_digest TEXT`, `result_evidence_refs TEXT`, `resume_cursor TEXT`, `idempotency_identity TEXT`, `actor_ref TEXT`, `request_id TEXT`, `recorded_at TEXT` | PK `(work_id,checkpoint_id)`; UNIQUE `(work_id,step_id,attempt_epoch)` and `(work_id,idempotency_identity)`; FK work/actor; closed step-kind check |
| `workflow_external_conditions` | `work_id TEXT`, `condition_id TEXT`, `await_type TEXT`, `await_ref TEXT`, `resolution_authority TEXT`, `condition_state TEXT`, `resolution_evidence TEXT NULL`, `resolved_by_event TEXT NULL`, `cancellation_authority TEXT NULL`, `cancellation_evidence TEXT NULL`, `cancelled_by_event TEXT NULL`, `recorded_at TEXT`, `resolved_at TEXT NULL`, `cancelled_at TEXT NULL` | PK `(work_id,condition_id)`; FK work; checks await type closed, state `open|resolved|cancelled`, and exactly one complete resolution/cancellation shape |
| `workflow_impact_edges` | `work_id TEXT`, `edge_id TEXT`, `edge_kind TEXT`, `edge_class TEXT`, `target_work_id TEXT`, `target_kind TEXT`, `severity TEXT`, `recorded_at TEXT` | PK `(work_id,edge_id)`; FK source/target work; closed edge/class/severity checks; hard `depends_on` and `forward_link` cycle check in transaction |
| `workflow_impact_notices` | `notice_id TEXT`, `source_work_id TEXT`, `source_contract_version INTEGER`, `entity_kind TEXT`, `entity_ref TEXT`, `target_work_id TEXT`, `edge_owner_work_id TEXT`, `edge_id TEXT`, `old_hash TEXT NULL`, `new_hash TEXT NULL`, `severity TEXT`, `recorded_at TEXT` | PK `notice_id`; UNIQUE `(source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,severity)`; `notice_id` must equal the deterministic derivation in §9; FK source/target/edge-owner work and dependent-owned edge; closed severity |
| `workflow_decision_records` | `work_id TEXT`, `question TEXT`, `options_considered TEXT`, `decision TEXT`, `rationale TEXT`, `consequences TEXT`, `inputs TEXT`, `poc_findings TEXT`, `supersedes TEXT NULL`, `superseded_by TEXT NULL`, `recorded_at TEXT` | PK `(work_id,question)`; FK work; checks decision enum and required nonempty fields |
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
transaction. Contract rows retain the exact definition pin that authorized each
approval, so a contract from another definition is never treated as compatible
([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

`workflow.evidence_bound` does not create a second evidence authority. Its
`producer_run_ref` must identify an existing `durable_operations` row with
`work_id` equal to the recorded subject, `principal_ref` equal to `producer_id`,
`request_id` equal to `producer_watermark`, and a completed result whose
authoritative `evidence_refs` contains the same immutable subject. Completion
reads these bindings from current rows and therefore cannot pass on
workflow-only metadata or an operation from another work item
([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

A green verify-tier run records its own authority. Releasing the lease commits
a completed `durable_operations` row with `op_id`
`worktree_verify:sha256:<digest>`. The lease keeps its work identity, and the
durable operation stores the same `work_id`, so the operation reference does not
repeat that identity. The row carries the requesting principal, request
identity, and run reference in `evidence_refs`. `bind_evidence` names that row
as its producer. The acceptance checkpoint's verification requirement then
resolves against the core run that produced it. A failed or mutated run records
no authority. The gate keeps refusing unbound verification.

Native oracle preparation retains a qualified readiness lease but creates no
green durable operation and discharges no candidate verification predicate.
Execution publishes verification authority only for a qualified pass.
Its bounded `result_json` is byte-identical to the green operation's
`result_payload`; neither JSON record contains raw stream bodies.

An oracle receipt joins retained execution evidence and creates no producer
authority. The candidate and pinned recipe remain separate identities.
The `go_top_level_v1` family supports pure-Go top-level tests only.
Preparation derives a nonempty, build-selected witness set and compiles the
pinned harness without running its binary, initialization, or `TestMain`.
Execution stages the candidate with the exact pinned test and fixture bytes.
It preserves production Go/module inputs and uses the declared contained cwd.
Undeclared non-Go inputs and unresolved embed targets refuse preparation or
execution; only declared pinned fixtures and explicit compiler inputs enter.
The producer pins the installed toolchain and native target, with
`GOTOOLCHAIN=local`, `GOPROXY=off`, `GOFLAGS=-mod=readonly`, `CGO_ENABLED=0`, and
`GOWORK=off`. Each lease uses a private disposable `GOCACHE`.
That family does not install or download missing dependencies. Target runners
materialize verified cached dependencies into per-run scratch under CD-0205 D1.
Unsupported recipes and unverified inputs remain unavailable. Ordinary shell
output supplies no native fallback. No aggregate scratch-quota guarantee is implied.
A pass requires actual run and terminal pass events for every selected case
witness, not exit zero alone, a skipped test, or package-level success.
For Go, selected case events come from the process that links the candidate.
The producer does not authenticate those events against candidate code.
Independent review judges candidate code for harness subversion, including
process exit, output forgery, and initialization effects.
Independent review still judges the semantic adequacy of those witnesses.

Before test-program launch, the native owner proves the persisted dispatch's
work, attempt, epoch, packet digest, exact job revision, control, and candidate.
It joins the completed dispatch, attempt rows, preceding start, and job record
through the existing authority; `in_flight` or `dispatched` is required.
The completion builder records `worker_subject_commit` beside the packet
digest from the same tx-scoped core `subject_commit` view admitted at dispatch.
The workflow reader retains that recorded value without current derivation.
The integrated work-context view exposes only `subject_commit`; no parallel
`candidate_subject` property or independent subject reader survives.
Current clean `HEAD`, current core subject, and recorded dispatch subject must
be nonempty and equal. A completion that cannot join a current candidate refuses
native oracle execution without packet-body retention or an inferred value.
A forged attempt, another job's digest, terminal state, or mismatched candidate
binding refuses with zero test-program launches. Acquisition and release also
recheck applicability. Caller assertions and today's context supply no missing
historical authorization, and preparation requires no worker authorization.

The existing lease retains a canonical producer plan and SHA-256 digest in
separate columns; `command_json` remains a bare argument array.
Resume compares plan bytes and digest as well as work, Project, and request
identity. Completed replay returns retained metadata without another effect.
The lease stores one stdout `BLOB` and one stderr `BLOB`, at most 2 MiB each.
The producer finalizes lengths, hashes, completeness, and bounded JSON before
the SQL-only release transaction stores streams and metadata atomically.
No raw stream body enters durable operations or mutation idempotency records.
Overflow, capture loss, mutation, or conversion failure cannot qualify a pass.
A finalized incomplete diagnostic prefix remains explicitly incomplete.
Abandonment before committed release promises no retained output or reference.
An additive migration preserves legacy NULLs without fabricated empty streams.

`worktree_inspect` mode `oracle_output` reads a retained same-work/Project run
without requiring an active tree and returns at most 16 KiB of raw page bytes.
The reader verifies retained length and SHA-256 before returning bounded base64
data within the unchanged response envelope. The read creates no mutation,
lease, native effect, or idempotency page record. Work removal deletes the
lease and its streams; worktree reclamation alone does not erase access.

### 5.2 Current-row effects

Workflow state is current rows written by each mutation in one transaction, and recovery uses verified snapshots plus reconciliation of current rows with Git, worktrees, artifacts, and external effects ([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

| Record | Current-row effect |
|---|---|
| definition selected | upsert the one instance definition pin; reject a change once an action has started |
| contract approved/superseded | insert the immutable contract version and its recorded law and definition pins; mark every exact predecessor named by a recovery record superseded |
| candidate set revised | insert added candidates and delete only the named removed candidates for that contract; premise and outcome bytes are untouched |
| backlog alignment recorded | insert the declaration row and, when the outcome is `related_found`, one row per named work id; the alignment action records the candidate set only and creates no relation, so work-to-work relation creation stays with `resolve_overlap`, capture's `raised_from` provenance, and the workflow `link_successor` action |
| actor recorded | insert once; a different tuple for an existing `actor_ref` is refused |
| action started/checkpointed/completed/failed | update current step/state/checkpoint timestamps and insert/update checkpoint rows; `record_decision` extracts its typed decision-record checkpoint into `workflow_decision_records`; a repeated idempotency key returns the stored result without a second effect |
| evidence bound | write to the existing evidence authority and make the binding visible to completion; it is not copied into a workflow-only authority |
| verdict recorded | retain the verdict for the subject and expose it through the instance's verification view |
| premise confirmed | insert operator confirmation |
| successor linked | write the existing `forward_link` relation; workflow state remains independent |
| impact declared/notice recorded | insert or deduplicate typed edge/notice rows |
| condition added | insert an `open` condition with its owning `resolution_authority` |
| condition resolved | transition `open → resolved`; require a work-item-bound durable operation authority and verify every evidence ref belongs to its completed authoritative result before filling resolution fields |
| condition cancelled | transition `open → cancelled`; require the recording actor to resolve to an operator actor tuple, not merely a payload string, and verify every cancellation evidence ref against the stored durable authority operation; a resolved or cancelled condition cannot transition again |
| completed | set terminal state and completed timestamp only after the completion transaction's prior clauses pass |
| work item terminal (`work.transitioned` to `completed` or `cancelled`, `work.superseded`) | close a live instance in the same transaction: `superseded` mirrors the lifecycle, `cancelled` and completed-by-lifecycle both record `cancelled` because the completion gate never ran; an instance already terminal keeps its record |

## 6. Outcome evaluation and strength

The approved predicate is immutable. Review, the loop's verdict actor, is the
evaluator: for each approved predicate it applies the rules below to the
delivery and its bound evidence, and records `verdict_kind` and
`incomparable_with_approved`. Implement cannot accept its own result, and no
separate final evaluator or further actor distinctness exists
([CD-0242 D5](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls),
which governs [CD-0235](./decisions/CD-0235-the-independent-verdict-is-the-outcome-strength-evaluation.md) D1).

At contract approval and again at completion, each approved predicate is
validated against the pinned definition's `outcome_schema`, not only against the
generic outcome union. The validator rejects a predicate kind outside
`allowed_kinds` and an outcome token not in `allowed_outcome_tokens`. An inline
decision record is validated whenever one is supplied. This check runs for all seven families; the generic union is only
the syntax check, while the selected definition is the authority for meaning.

1. **`exists`:** resolve every approved subject on the named ground-truth surface.
   Unknown/unreadable is not absent. A delivered `exists(S,D)` is strengthening
   when the surface is identical and `D ⊇ S`; it passes only when every `S` exists.
   A different surface or unresolved identity is incomparable.
2. **`absent`:** resolve every subject on the named ground-truth surface and apply
   every `distinguish_from` rule. A delivered absence is strengthening when its
   absent subject set contains the approved set and each approved identity is
   truly absent, not merely archived, relocated, renamed, or disabled. Relocation
   is therefore weaker, never success. Different surface or changed
   distinction rules is incomparable.
3. **`outcome`:** evaluate the recorded token. It passes iff the token is a member
   of the approved `allowed` set. `no_change` is a valid research result. A spike's
   `accepted_decision` or `insufficient_evidence` token additionally requires the
   bound decision record in §6.1, and the record's decision token must sit in the
   approved `allowed` set. A token outside the set is weaker/mismatched.
4. **`check`:** evaluate the named check against the exact immutable subject
   and compare its closed result with `expected_result`. A changed immutable
   subject is stale evidence, not a new result.

Review owns behavioral strength. It assesses each criterion on the final
candidate as `supported`, `unproved`, or `mismatch`, and unreadable data is
never `supported`. Only a `supported` assessment that cites a passing producer
receipt can become criterion proof, so a weaker or incomparable delivery has no
proof and completion refuses it at clause 4 with `missing_evidence`
([CD-0235 D2](./decisions/CD-0235-the-independent-verdict-is-the-outcome-strength-evaluation.md#d2-completion-refuses-on-the-recorded-verdict)).

Job-oracle controls use exit-status results and retained execution references
(CD-0205, CD-0056). Their receipts are reported evidence, not per-predicate
evaluation, review verdicts, criterion discharge, or completion authority.
Earlier-subject receipts are regression baselines. The oracle does not supply
the deferred registered evaluator catalog or change the closed predicate union.

### 6.1 Architecture-spike record

`accepted_decision` and `insufficient_evidence` require a bound decision record
containing framed questions, options with source-backed evidence, decision,
rationale, consequences, inputs, proof of concept (POC) findings (or an explicit no-POC value),
supersession position, reviewer actor, operator acceptance, and for
`insufficient_evidence`, recorded unknowns plus what would be required to decide.
The verdict actor reads the latest accepted row in
`workflow_decision_records` — the row `record_decision` writes and
`accept_decision` accepts — and records `ok` only when its decision token sits
inside the approved `allowed` set. A lifecycle completion outside the workflow
gate reads the same row and refuses with `decision_record_required` when the
row is missing or its token is outside the pinned definition's allowed tokens. Contract approval validates the predicate kind and its allowed
tokens, and validates an inline record only when one is supplied. An unaccepted
record does not satisfy the predicate or unblock a dependent managed work unit.

## 7. Ordered completion gate

`CompleteWorkflowTxWithRegistry` executes the following ordered clauses in one SQLite
transaction. It locks the work item, checks the pinned definition and expected
version, and rolls back on the first refusal. The typed refusal and recovery action
are stable:

| Order | Clause | Exact refusal | Recovery action |
|---:|---|---|---|
| 1 | Every required evidence binding exists, resolves to its immutable subject, and belongs to the approved contract | `missing_evidence` | `provide_evidence` |
| 2 | Every declared condition is resolved or explicitly cancelled by its owning authority | `not_terminal` | `reread_entities` |
| 3 | Every `modifies` edge is within the declared scope and the spec mandate is satisfied | `invariant_violation` | `reread_entities` |
| 4 | Every approved predicate has a criterion-proof association on the current candidate, its actor tuple is complete, and it is recorded by Main, not the executing Implement actor ([CD-0242 D5](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)) | `unauthorized` for same/partial actor; `missing_evidence` for absent verdict | `contact_operator` / `provide_evidence` |
| 5 | Premise is confirmed with the authority the contract's decision fact requires: a verified operator for decision-bearing work, and under `consequence_v1` an authenticated non-worker coordinator for engineering work (CD-0013 D11) | `approval_required` or `missing_evidence` | `request_approval` / record the coordinator confirmation |
| 6 | Every recorded verdict answers an approved predicate, and no declared blocking staleness rule drifted | `outcome_mismatch` or `stale_requires_review` | `contact_operator` / `refresh_context` |
| 7 | The gate reads, impact notices, terminal metadata, lifecycle change, and the completion record commit in one transaction | `operation_conflict` | `reconcile_operation` |

The same transaction performs the reverse-edge impact scan and notice deduplication
before writing the completion record. A response is not terminal until the commit
returns success. A failed commit produces no terminal state mutation.

The shipped engine evaluates clause 6 from the pinned definition and the
recorded verdict rows. It never records completion on a weaker,
incomparable, stale, or otherwise refused outcome.

### 7.1 Acceptance recovery

The engine admits `record_verdict` on the terminal workflow step before the
instance reaches a terminal state. The route exists only after a definition-backed
verification step and only for an active-contract predicate whose latest eligible
verdict is missing, non-`ok`, or incomparable. An `ok` comparable verdict cannot
be replaced. The action holds the current step and preserves attempts and evidence.

At a nonterminal human checkpoint that declares `confirm_premise`, the operator
question maps `Revise` to `supersede_contract`. The action requires a complete
consecutive successor contract. It records the successor and keeps the current
checkpoint. Prior verdicts remain eligible only across every consecutive contract
version where predicate identity, kind, and canonical payload are unchanged.
Prior premise confirmations and verdicts for changed or removed predicates do not
satisfy the successor contract.

## 8. Actor tuple and verdict recording

The authenticated mutation context supplies `(principal_ref, client_ref, agent_ref,
session_ref)`. The engine validates all four are nonempty, canonicalizes each as a
bounded reference, and hashes these exact UTF-8 bytes with SHA-256:
`actor-v1\0principal_ref=<byte-length>:<value>|client_ref=<byte-length>:<value>|agent_ref=<byte-length>:<value>|session_ref=<byte-length>:<value>|`, where `\0` is one U+0000 NUL byte; the two literal characters backslash-zero are invalid.
`actor_ref=actor:<64 lowercase hex characters>` is the immutable identity. The
actor row stores the tuple once; later records carry only
`actor_ref`.

`principal_ref` alone never establishes authority. Implement cannot accept its
own result: a verdict is refused when its
`agent_ref` and `session_ref` both equal the executing actor's values
([CD-0242 D5](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).
No other actor distinctness is required. Every verdict actor must still carry a
complete tuple; any empty or partial tuple is refused. Actor rows and tuple fields cannot be
updated or deleted; a changed tuple receives a new actor reference.

Every `record_verdict` entry names an approved predicate and 1 to 32 producer
receipt references. The core records the association as `ok` only when each
receipt proves the criterion on the current candidate; a caller-supplied
verdict kind is refused as an unknown field, and an unapproved predicate
refuses with `outcome_mismatch`. A refusal leaves no recorded change, durable
operation, or work-version update
([CD-0242 D14](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source));
no released definition version is retained for history, and no replay or
rebuild exists
([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

For `confirm_premise`, the invoking agent tuple is never converted to an
operator by changing `actor_class`, and `confirming_actor_ref` is not accepted
from the workflow payload. The exact signed approval assertion must carry the
no operator identity fields. The core authenticates the invoking client against
the trusted-client key and policy, binds the assertion to the exact challenge,
request digest, scope, work version, contract version, and `workflow_action`
consequence, then consumes the assertion and approval record. The operator actor
is deterministic core output derived from the consumed approval ref, consumed
challenge ref, authenticated client, and canonical trusted-client policy digest;
it is an approval-authority tuple, not a named-human identity. If its actor row
is absent, the operator actor row is written in the same transaction as the
recorded results; an existing row must match exactly. Replayed,
mismatched, skipped-question, or payload-selected identities cannot create or
use an operator actor. Idempotent replay returns the
stored result without asking again, consuming approval again, or recording the
actor again.

### 8.1 Operator question before approval

The bounded workflow read projection exposes `operator_question` only for the
next declared human-checkpoint action. It contains the stable `prompt` and
`header`, a closed `choices` array (`id`, label, description, and action mapping),
`allow_multiple=false`, `allow_custom=false`, bounded premise and contract
summaries, and `decision_context_digest`. The digest is SHA-256 over the exact
work ID and version, pinned definition ref/version/digest, contract version,
action ID, premise bytes, outcome kind, and stored outcome-payload bytes.

The model calls the built-in question UI using that projection. The question is
semantic input only; the custom Concord tool never calls the question service.
`confirm_premise` requires `selected_choice=confirm` and the matching digest.
`revise` and `stop` are closed choices that route to the declared revision or
cancellation action and are rejected if smuggled through `confirm_premise`.
Only after core validates this selection and returns the exact approval challenge
does the adapter call `ToolContext.ask`. `ask` is authorization, not semantic
input. Its metadata includes work, action, contract version, selected choice, and
bounded premise summary. A recorded operator therefore means approval authority,
not an identified individual human.

## 9. Impact edges and notices

`modifies`, hard/soft `depends_on`, and `forward_link` are typed edges. `modifies`
is not cycle-checked. Hard `depends_on` and `forward_link` use the existing
in-transaction recursive cycle check. At completion, reverse edges are selected in
the bounded affected closure and the engine emits one notice per identity:

```text
(source_work_id, source_contract_version, entity_kind, entity_ref,
 target_work_id, severity)
```

The notice row additionally stores `edge_owner_work_id`, `edge_id`, `old_hash`,
`new_hash`, and `recorded_at`. `edge_owner_work_id` is the dependent work that
declared the inbound edge. `notice_id` is deterministic: concatenate the prefix `notice-v1\0`
with the six fields in the displayed order, each encoded as
`field_name=<UTF-8-byte-length>:<UTF-8-value>|`, then set
`notice_id = "notice:" + hex(SHA-256(canonical_bytes))`. The event carries
`notice_id`; the store recomputes it and rejects a mismatch as
`invariant_violation` before inserting. The unique constraint is the identity
above; repeating completion or replaying its idempotency key emits no second
notice. Completion queries inbound `depends_on` edges; it does not require a spec
mandate or a forward edge from the source. It records `entity_kind=work_item` and
`entity_ref=<completed source work>`. The explicit completion verdict supplies the
notice severity. Hard `depends_on` plus `breaking` blocks downstream execution at
its consequential boundary. Soft plus breaking, and every non-breaking notice,
warn without blocking. The static edge severity remains readable for compatibility
but does not decide completion impact.

For WF29's six values (`work-alpha`, `1`, `work_item`, `work-alpha`,
`work-child`, `breaking`), this exact encoding yields
`notice:8ea37498e2890b3fef5b65132eb0dfc3e7e4297ce6bb521ea4406c0366535503`.
A contract revision uses the same reverse-edge direction and emits `breaking`
when an active hard dependent consumed the superseded contract. It emits
`non-breaking` for every other inbound dependent.

## 10. External conditions

The engine exposes no wait daemon, performs no polling, and has no condition
resolver (CD-0234 D1). No retained family declares an action that
opens, resolves, or cancels a condition. Recorded conditions are current rows
that reads serve
([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
A consequential action on work with an open condition refuses with
`not_terminal`. The table states the owning-authority evidence a resolver added by
a later decision must require.

`await_type` is closed to:

| Type | Required owning-authority evidence |
|---|---|
| `pr_merge` | provider PR ref, merged state, merge commit OID, and provider observation/run ref |
| `ci_result` | provider run ref, checked commit OID, terminal conclusion, and provider observation/run ref |
| `timer` | stored RFC3339 deadline, trusted current time at explicit check, and comparison evidence; no daemon |
| `human_approval` | exact approval ref, operation digest, scope, consequence, approving actor, and approval evidence |
| `remote_work_state` | target work ID, expected version, owning lifecycle event, observed terminal state, and evidence |

Heuristics may locate candidate PRs or runs but cannot resolve a condition. Failed,
ambiguous, stale, or unreadable authority leaves the condition open and returns a
typed problem. Conditions participate in ordinary `blocks` relations and inherit
CD-0008 unreadable-record semantics. `resolution_authority` is immutable from
its recorded addition, has the exact form `durable_operation:<op_id>`, and
must point to a completed durable operation for the same work item. Every
resolution or cancellation evidence ref must be present in that operation's
authoritative `evidence_refs`; actor class alone never establishes evidence
authority. Cancellation is not provider resolution and can only be emitted by
the operator with `cancellation_authority=operator` plus evidence from the
stored durable authority operation.

## 11. `workflow_action` dispatch

`concord_work_transition.workflow_action` is the only advancement surface. The
dispatcher performs this order:

1. Check `WorkflowActionAvailable`; refuse structurally until the pinned digest is
   registered and verified.
2. Validate the closed action ID and payload against the pinned definition's action
   entry. Unknown fields, missing fields, wrong bounds, or action-not-in-current-step
    return the existing `invalid_transition` refusal with recovery action
    `reread_entities`.
3. Validate work ID, expected work version, authenticated actor tuple, and current
   step. Stale versions return `version_conflict`; an incomplete actor returns
   `unauthorized`.
4. Resolve required evidence and the existing `ApprovalCheck` chain. Missing or
   invalid approval returns `approval_required` or `approval_invalid`.
5. Claim/check the durable idempotency identity. A replay of the same operation
   returns the original envelope without a second effect; a conflicting payload
   returns `idempotency_conflict`.
6. Execute the graph action inline for `internal_sqlite`, or claim the current
   attempt epoch for `cross_authority`/`external_effect` before side effects.
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

### Scenarios that bind production routes

WF09, WF22, and WF25 each bind their criterion to a production route.

- WF09 sends `approve_contract`, the only declared action that writes an
  approved end-state, at the `execution` step. The step does not declare the
  action, so the graph's declared-action check refuses it with
  `invalid_transition` before actor authorization, and no contract event is
  written.
- WF22 commits `checkpoint_context`, abandons an uncommitted `record_proposal`
  transaction, and reads the resume
  state through the continuity read (CD-0016). Its resume step is `proposal`,
  a step of the pinned `workflow.implementation` definition.
- WF25 leaves a compaction publication claim pending, then repeats the
  publication under a new idempotency key without an approval. The agent
  boundary returns `approval_required`, and the attempt epoch does not advance.
  Compaction requires an approval on every publication, so WF25 covers
  durable-operation claims only. The fenced-start takeover of a held attempt
  is a separate route for criterion 25, covered by its own tests.

## 12. Built-in family graphs and actions

Every family has typed
action execution modes. The
`accept_worker_result`, `record_worker_failure`, and `dispatch_worker` set lands
on each dispatchable, non-terminal step. The generated lane-step join decides
which step kinds receive that set. `record_delivery` remains the session exit
after a fenced start with no later dispatch. The table lists family actions;
definition composition adds the worker-action set. Break-fix, and the `larger`
and `crucial` implementation compositions, carry a mandatory `refine`
external-effect step between the producing step and its verdict step
(CD-0138 D1; CD-0229 D5).
The refine step requires an artifact
evidence binding and records the scope, report, and evidence supplied by an
external analysis authority. Concord does not implement the scanner.

The refine exit is gated
(CD-0192, amending CD-0138 D3). `record_delivery` at the refine step admits the
advance only when, in the current refine epoch, a bound verification evidence
names a completed `worktree.verify` durable operation for the work item whose
lease recorded outcome `completed`, exit code 0, no tracked-file change, and an
acquire after the current refine start. When the work item's Project declares
`.concord/tooling.v1.json` on its default ref, the run's argv must equal the
whitespace-split invocation of at least one declared tool; the manifest is read
from the default ref outside the store transaction, never from the working
tree. A Project that declares no manifest passes on any green run. The refusal
names the declared tool ids and invocations, or states that the Project
declares none.
The tooling schema and `scripts/check-project-tooling.py` forbid
shell quoting and shell operators in an invocation, so the whitespace split is
the exact argv.

| Family and source | Ordered graph and family actions at each step |
|---|---|
| **Implementation** — [`workflows.md` §1](./workflows.md#1-the-shift-from-one-workflow-to-a-plurality) and [`feature-inventory.md` §1.6](./feature-inventory.md#16-durable-execution-safety-substrate) | `proposal[record_proposal] → alignment[record_alignment] → discovery[record_discovery] → design[record_design] → planning[approve_contract] → execution[start_execution, checkpoint_execution, bind_evidence, declare_impact, link_successor, record_delivery] → refine[start_refine, checkpoint_refine, bind_evidence, record_delivery] → acceptance[record_verdict, confirm_premise] → release[complete]`. Refine is mandatory at the `larger` and `crucial` sizes and requires artifact evidence at that step (CD-0138 D1; CD-0229 D5); the `small` composition walks define, implement, review, and ship. The CD-0156 alignment step follows proposal: record_alignment is the step's only advance exit, so the backlog search cannot be skipped, and the worker-action set stays off the step so no dispatched attempt can accept its way past the search. |
| **Break-fix / RCA** — [`workflows.md` §3](./workflows.md#3-complete-work-kind-taxonomy) and §4 | `reproduce[record_reproduction] → alignment[record_alignment] → diagnose[record_root_cause] → planning[approve_contract] → repair[start_repair, checkpoint_repair, bind_evidence, link_successor, record_delivery] → refine[start_refine, checkpoint_refine, bind_evidence, record_delivery] → verify[bind_evidence, record_verdict, confirm_premise] → complete[complete]`. Refine is mandatory and requires artifact evidence at that step (CD-0138 D1). Verify carries the hold-mode `bind_evidence` route (CD-0124), so a green verify-tier run at `worktree_verify:sha256:<digest>` can be named as the verification producer. The CD-0156 alignment step follows reproduce on the same terms. |
| **Research / investigation** — [`workflows.md` §4](./workflows.md#4-example-workflow-types) and CD-0215 D3 | `frame[frame_research, approve_contract] → investigate[record_finding, revise_candidates, bind_evidence] → findings[record_report, link_successor] → conclude[record_conclusion, record_verdict, confirm_premise] → complete[complete]`. Completion verb: findings recorded; `no_change` is valid. |
| **Architecture spike** — [`architecture-spike.md` §2](./architecture-spike.md#2-shape) | `frame[frame_question, approve_contract] → research[record_research, bind_evidence] → options[record_option] → poc_optional[start_poc, checkpoint_poc, discard_poc, record_delivery] → decision_record[record_decision] → review[record_verdict, accept_decision] → acceptance[confirm_premise] → complete[complete]`. The optional POC edge skips only `poc_optional`; completion still requires the accepted decision record, verdict, premise confirmation, and `complete`. |
| **Ops runbook** — [`workflows.md` §4](./workflows.md#4-example-workflow-types) and [`managed-resource-inventory.md` §3](./managed-resource-inventory.md#3-stage-rule) | `plan[approve_contract, resolve_condition, cancel_condition] → approval[approve_operation, resolve_condition, cancel_condition] → execute[start_run, checkpoint_run, bind_evidence, add_condition, resolve_condition, cancel_condition, record_delivery] → health[record_health, record_verdict, resolve_condition, cancel_condition] → rollback_optional[rollback_run, resolve_condition, cancel_condition, record_delivery] → cleanup[cleanup_run, confirm_premise, resolve_condition, cancel_condition] → complete[complete, resolve_condition, cancel_condition]`. Rollback is optional recovery, not a second completion path. |
| **Static analysis** — [`workflows.md` §4–§5](./workflows.md#5-coordinating-external-analysis-tools) | `scope[approve_contract, declare_scope] → analyze[run_analysis, checkpoint_analysis, record_delivery] → report[record_report, bind_evidence] → review[record_verdict, confirm_premise] → complete[complete]`. Completion verb: report exists over the declared surface. |
| **Generic one-off** — [`workflows.md` §2.1a](./workflows.md#21a-outcome-contract) and CD-0013 D14 | `define[approve_contract] → execute[start_action, checkpoint_action, bind_evidence, link_successor, record_delivery] → verify[record_verdict, confirm_premise] → complete[complete]`. It may use any operator-authored allowed predicate without copying another family's semantics. |

### 12.1 Action-to-event mapping

The dispatcher validates every action above against its root `action_definitions`
entry. Each entry carries one closed `execution_mode`: `advance`, `hold`,
`fenced`, or `checkpoint`. The following table is exhaustive; an action not listed is invalid. An
internal action records its listed result;
a fenced action records its start under the current attempt epoch and then its
terminal result
([CD-0242 D2](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
No result record is invented for a graph step.

| Action IDs | Semantic event mapping |
|---|---|
| `record_discovery`, `frame_research`, `record_finding`, `record_reproduction`, `record_root_cause`, `record_option`, `record_health`, `declare_scope`, `record_conclusion` | `workflow.action_completed`; immutable artifact/evidence is carried by `result_evidence_refs` and `changed_refs` |
| `record_proposal` | `workflow.proposal_recorded` plus `workflow.action_completed` for a definition that declares the proposal document; otherwise `workflow.action_completed` alone (CD-0242 D14) |
| `record_design` | `workflow.design_recorded` plus `workflow.action_completed`; the typed design record is the approved change's decision record, not lane methodology |
| `record_alignment` | `workflow.backlog_alignment_recorded` plus `workflow.action_completed`; the payload records the search scope, the closed `related_found`/`none_found` outcome, and the related work ids exactly when the search found them (CD-0156) |
| `approve_contract` | `workflow.contract_approved` plus its required approval evidence binding |
| `start_execution`, `start_repair`, `start_refine`, `start_run`, `start_poc`, `start_action` | fenced `workflow.action_started` → `workflow.action_completed`/`workflow.action_failed` |
| `checkpoint_execution`, `checkpoint_repair`, `checkpoint_refine`, `checkpoint_run`, `checkpoint_poc`, `checkpoint_analysis`, `checkpoint_action` | `workflow.action_checkpointed`; the committed checkpoint fixes the attempt boundary, and the recorded current step is the resume point. `resume_cursor` may be empty, and no read depends on it. |
| `bind_evidence` | `workflow.evidence_bound`; the producer names a completed durable operation, for example a green verify-tier run at `worktree_verify:sha256:<digest>` |
| `revise_candidates` | `workflow.candidate_set_revised` |
| `declare_impact` | `workflow.impact_declared` |
| `link_successor` | `workflow.successor_linked` and the existing forward-link relation event |
| `record_verdict` | `workflow.verdict_recorded`; the verdict is recorded by an actor other than the executing Implement actor (CD-0242 D5; CD-0109), or by the operator's signed identity strictly when the item exited an external-effect step through `record_delivery` (CD-0116) |
| `confirm_premise` | `workflow.premise_confirmed` |
| `complete` | `workflow.completed`, only after the ordered gate in §7 |
| `frame_question` | `workflow.action_completed`; the framed question is part of the action result and later decision-record evidence |
| `record_research` | `workflow.action_completed` plus bound research evidence |
| `record_decision` | `workflow.action_checkpointed` with the typed decision-record payload, then `workflow.action_completed`, which advances to `review`; the accepted record is bound in the outcome payload |
| `accept_decision` | `workflow.evidence_bound` for operator acceptance plus `workflow.action_completed`, which advances `review` to `acceptance` |
| `record_delivery` | `workflow.action_completed`, which advances the external-effect step when its fenced start ran and no lane attempt was dispatched in that attempt; a dispatched attempt cannot exit through delivery |
| `discard_poc` | `workflow.action_completed` with a discard artifact reference; no POC bytes become workflow authority |
| `approve_operation` | approval evidence binding plus `workflow.action_completed` |
| `add_condition` | `workflow.condition_added` |
| `resolve_condition` | `workflow.condition_resolved` |
| `cancel_condition` | `workflow.condition_cancelled` |
| `rollback_run` | fenced `workflow.action_started` → `workflow.action_completed`/`workflow.action_failed`, with rollback evidence |
| `cleanup_run` | `workflow.action_completed` with cleanup evidence |
| `run_analysis` | fenced `workflow.action_started` → `workflow.action_completed`/`workflow.action_failed`; report evidence follows in `record_report` |
| `record_report` | `workflow.action_completed` plus `workflow.evidence_bound` for the report's immutable subject |
| `dispatch_worker` | fenced `workflow.action_started` then `workflow.action_completed` v2, bound to the authorized attempt and canonical worker-packet digest |
| `accept_worker_result` | `workflow.evidence_bound` naming the accepted attempt as its `immutable_subject_ref`, with the lane's capability class as the evidence kind, then `workflow.action_completed` v2 bound to the exact completed attempt and current step epoch; admission rechecks dispatch order, work ownership, lifecycle, model readback, and non-self-acceptance (CD-0242 D5) before advancing. The verdict on the next step cites the attempt id. |
| `record_worker_failure` | `workflow.action_completed` v2 in hold mode, bound to the exact failed attempt and current step epoch; admission rechecks dispatch order, work ownership, failed lifecycle, non-self-acceptance (CD-0242 D5), and prior recording. A fresh fenced start opens the recovery attempt. |

`record_worker_job` requires the immutable owner/case/control oracle in the
recorded job digest (CD-0205 D1). Every admitted dispatch carries that same
oracle. Structure and retained readiness references join existing contract,
Domain, Project, and law authority; preparation resolves pinned recipes
before authorization. Supported Go and target-repository runners use the
same native preparation, job, dispatch, and receipt authority joins.
Preparation consumes recipe-commit bytes and supplies no verification pass.
Execution consumes candidate-commit production inputs with pinned harness
and runner inputs. Exact dependency pins, cached-byte integrity, input
digests, and runtime policy remain the producer's obligations under CD-0205 D1.
Its shared evidence limits apply to every recipe family.
Unsupported or unavailable inputs refuse without a worker attempt or a
readiness exception.
`worker.completed` admits oracle receipts, ranked-finding ties, and closed
resolution claims; no worker record invents them after the fact.
Local job acceptance holds the step. Whole-work delivery still requires the
complete parent admission, integration evidence, review verdicts, and
operator confirmation; an oracle pass supplies none of those authorities.

### 12.2 Action definitions and composition

Each launch is one admitted request with its own scope, assignment, and subject bindings ([CD-0242 D6](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)). A launch confirmed never started leaves the
assignment and its valid proof unchanged, and a launch with an unknown effect
is reconciled before another launch for that assignment.

`action_definitions` carry closed payload field definitions and execution modes
for each ID. Every registered definition declares an explicit `execution_mode`
for every action; the registry infers no mode from an action ID, and an action
the definition does not declare has no execution mode. The
definition validator rejects duplicate action IDs, graph references not present
in the root action list, action definitions missing from that list, undeclared
step endpoints, undeclared start/terminal nodes, and non-retry cycles. The runtime
also requires the action's current step and rejects a terminal transition unless
the graph declares `complete` on that path.

The family defaults and allowed predicate forms are exactly those in CD-0013 D14:
implementation/check, break-fix/absent, research/outcome, architecture-spike/
outcome, ops-runbook/check, static-analysis/check, and generic outcome-or-check.
Database, configuration, infrastructure, options research, evidence research, and
RCA are represented by the documented family whose shape they use; they do not add
an eighth family.

Composition is forward-link only. A spike cannot nest under a spike; an
implementation change cannot nest under research; a generic one-off may link to
any family. A successor retains independent authority and recovery.

## 13. Remaining implementation-deferred items

The following list matches CD-0013's deferred list exactly. The built-in v1 graph
and action table above is the contract's chosen implementation fill; these items
remain deferred for extensions, catalogs, and runtime policy not required to
interpret the v1 carriers:

1. **Per-family action enumeration.** Each family's available action list and the
   exact step graph are implementation work, not part of this decision. The
   decision fixes the shape; the implementation fills it.
2. **Registered check catalog.** The `check` predicate's evaluator catalog is
   implementation work; each registered check must bind an immutable subject
   authority and a closed expected-result set.
3. **Ground-truth surface registry.** The set of valid `surface` values for
   `exists`/`absent` predicates and their authorities is implementation work.
4. **Audience commitment storage.** Runtime audience band storage and the
   effective-rigor evaluation routine are implementation work.

The contract does not invent the deferred check catalog, surface registry, audience
storage, or a new family. Until a decision supplies the check catalog or the
surface registry, no registry resolves `check_ref` and `surface`
values; approval checks their grammar and the verdict actor interprets them
(CD-0235 D3). Implementers must refuse every other unknown value.

## Acceptance criteria

- Given a work item whose planning omits the required end-state
  When approve_contract is dispatched
  Then the engine refuses with invalid_input, records no contract, and the
  instance keeps its current step and state.

- Given an approved outcome over two capabilities and a Review that
  assesses the delivery over one of them as mismatch
  When complete is dispatched
  Then the engine refuses with missing_evidence and records no
  completion.

- Given a completion verdict recorded by the executing Implement actor
  When record_verdict is dispatched
  Then the engine refuses the verdict: Implement cannot accept its own
  result ([CD-0242 D5](./decisions/CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).

- Given a workflow action whose pinned definition digest is absent from the
  registry
  When the action is dispatched
  Then the dispatcher refuses with invariant_violation and uses no fallback
  definition.

- Given a completion that satisfies the ordered completion gate
  When the completion transaction commits
  Then contract, verdict, evidence, notices, and the completion record commit
  together or not at all.

## Verification

All five criteria are proved by scenarios of `.concord/scenarios/workflow-engine.v1.json`,
executed against production dispatch by
`TestWorkflowScenarioCorpusExecutesExactProductionActions`
(`internal/store/workflow_conformance_test.go`).

- Criterion 1 is proved by the bound `WF02-planning-requires-outcome`
  scenario, which dispatches approve_contract without an end-state and asserts
  an invalid_input refusal from the required-field check, no recorded
  contract, and the instance's current step and state unchanged.
- Criterion 2 is proved by the bound `WF04-weaker-delivery` scenario, whose
  Review assesses the delivery as `mismatch`, so no criterion proof exists,
  and which asserts a missing_evidence refusal with no completion record
  (CD-0235 D2).
- Criterion 3 is proved by the bound `WF13-verdict-actor-distinctness`
  scenario, whose verdict is recorded by the executing Implement actor and
  asserts the refusal (CD-0242 D5).
- Criterion 4 is proved by the bound `WF37-action-availability-before-register`
  scenario, which dispatches before the definition is registered and asserts
  an invariant_violation refusal with no fallback.
- Criterion 5 is proved by the bound `WF19-completion-one-transaction`
  scenario, which fails a completion inside its transaction and asserts that
  no completion record, notice, terminal metadata, or lifecycle change
  remains.
