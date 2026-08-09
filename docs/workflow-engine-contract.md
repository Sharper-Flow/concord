# Concord Workflow Engine Contract

> **Status:** Implementation contract for accepted CD-0013, v1.
> **Authority:** [`CD-0013`](./decisions/CD-0013-workflow-engine-mechanism.md),
> amended only by a later accepted CD-NNNN.
> **Scope:** Go registry, event-folded workflow projections, outcome evaluation,
> completion, conditions, impact propagation, and the reserved `workflow_action`
> dispatch path.
> **Conformance carriers:** [`workflow-engine.v1.json`](../scenarios/workflow-engine.v1.json),
> [`workflow-definition.schema.json`](../contracts/workflow-definition.schema.json),
> [`workflow-outcome.schema.json`](../contracts/workflow-outcome.schema.json).

This document fills the implementation-design boundary left by CD-0013. It does
not add a workflow family, predicate kind, authority, or agent tool. V15 creates
the ten projections enumerated by CD-0013 D2, matching the accepted correction in
the decision's cost section.

## 1. Common representation and bounds

All identifiers are UTF-8 strings with 2–128 characters unless a narrower pattern
is stated. Timestamps are RFC3339 UTC strings, maximum 64 characters. Digests are
lower-case `sha256:` followed by 64 hexadecimal characters. Event payloads are
objects, never free-form maps; unknown keys are rejected before append.

| Value | Type/bound |
|---|---|
| `work_id`, `step_id`, `action_id`, `checkpoint_id`, `condition_id`, `edge_id` | identifier, 2–128 chars |
| `ref` | lower-case registry/reference string, 2–128 chars |
| `version`, `contract_version`, `attempt_epoch`, `request_id` sequence | positive integer, max 2,147,483,647 |
| evidence/reference list | unique array, 0–32 items, each identifier |
| candidate list | unique array, 1–100 items |
| `actor_ref` | `actor:` plus lower-case content-hash reference |
| `payload_version` | integer exactly `1` for v1 emission |

The event envelope is the existing `domain_events` row plus a typed payload:
`event_id`, `sequence`, `subject_type=work_item`, `subject_ref=work_id`,
`kind`, `payload_version`, `actor`, `occurred_at`, and `payload`. `event_id` and
`sequence` are store-assigned. The typed payload must contain `work_id` and may
not repeat envelope authority fields except where the event family explicitly
requires an actor reference.

## 2. Definition registry

### 2.1 Go-side shape

The registry is a process-owned, immutable-after-registration map keyed by
`(ref, version)`. The value is equivalent to the strict manifest in
[`workflow-definition.schema.json`](../contracts/workflow-definition.schema.json):

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
planning. SQLite does not persist the definition body. Active work never adopts a
newer version. Idle work may adopt the newest version only through an explicit
planning operation that records a new `workflow.definition_selected` event.

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
| `blocked` | `awaiting_condition` | a typed external condition is registered | internal-inline |
| `awaiting_condition` | `running` | owning resolver supplies valid evidence | internal-inline |
| `running` | `verifying` | all execution steps are complete and verdict is requested | internal-inline |
| `verifying` | `completed` | ordered completion gate succeeds | one internal transaction |
| `running`, `blocked`, `awaiting_condition`, `verifying` | `cancelled` | explicit authorized cancellation | fenced when an attempt is owned |
| `planned`, `ready`, `running`, `verifying` | `superseded` | operator supersedes the contract/work item | internal-inline with successor link |

An action failure appends `workflow.action_failed`; it does not invent a state.
Recoverable failures leave the instance in `running` or `blocked` according to the
definition edge. A completed, cancelled, or superseded instance is immutable.
Internal SQLite-only actions are one domain transaction and emit no redundant
fence event. Cross-authority and external-effect actions claim an epoch in
`durable_operations`, then append start/checkpoint/result under the current epoch.

## 4. Event families

Every row has `payload_version=1`, `work_id`, and the common envelope. The following
are the complete v1 family payloads; omitted keys are rejected.

`workflow.condition_cancelled` is the typed representation required by CD-0013 D10's
“resolved or canceled” completion clause. It adds no await type or authority:
operator cancellation is explicit, evidenced, and folded beside the two resolver
states.

| Event | Exact payload fields and bounds |
|---|---|
| `workflow.definition_selected` | `work_id`, `ref`, `version`, `digest`, `work_kind` (closed family enum) |
| `workflow.contract_approved` | `work_id`, `contract_version`, `premise` (1–4096), `outcome_kind`, `outcome_payload` (strict outcome schema), `required_evidence` (0–7 unique), `route_conventions` (0–16 unique refs), `spec_mandate` (0–32 unique refs), `rigor_class` (1–64) |
| `workflow.contract_superseded` | `work_id`, `previous_contract_version`, `new_contract_version`, `supersede_reason` (1–4096), `audit_evidence` (1–32 evidence refs) |
| `workflow.candidate_set_revised` | `work_id`, `contract_version`, `candidate_kind` (closed subject enum), `candidate_ref`, `added` (0–100 refs), `removed` (0–100 refs); `added` and `removed` are disjoint and not both empty |
| `workflow.actor_recorded` | `work_id`, `actor_ref`, `principal_ref`, `client_ref`, `agent_ref`, `session_ref`, `actor_class` (`agent` or `operator`) |
| `workflow.action_started` | `work_id`, `step_id`, `action_id`, `attempt_epoch`, `accepted_inputs_digest`, `idempotency_identity` (2–128), `actor_ref` |
| `workflow.action_checkpointed` | `work_id`, `step_id`, `step_kind`, `attempt_epoch`, `checkpoint_payload` (typed action checkpoint, max 16 KiB), `resume_cursor` (0–2048), `actor_ref`, `request_id` |
| `workflow.action_completed` | `work_id`, `step_id`, `attempt_epoch`, `result_evidence_refs` (0–32), `changed_refs` (0–32), `actor_ref` |
| `workflow.action_failed` | `work_id`, `step_id`, `attempt_epoch`, `failure_kind` (closed TS7 error kind), `recoverable` (boolean), `actor_ref` |
| `workflow.evidence_bound` | `work_id`, `evidence_kind` (verification/review/approval/commit/durable_note/native_run/artifact), `immutable_subject_ref`, `producer_id`, `producer_run_ref`, `producer_watermark`, `observed_at` |
| `workflow.verdict_recorded` | `work_id`, `contract_version`, `predicate_id`, `verdict_kind` (`ok`, `outcome_mismatch`, `insufficient_evidence`), `verdict_actor_ref`, `evaluation_evidence` (1–32), `incomparable_with_approved` (boolean) |
| `workflow.premise_confirmed` | `work_id`, `contract_version`, `confirming_actor_ref` |
| `workflow.successor_linked` | `work_id`, `successor_work_id`, `relation_kind` (`forward_link`) |
| `workflow.impact_declared` | `work_id`, `edge_id`, `edge_kind` (`modifies`, `depends_on`, `forward_link`), `edge_class` (`hard`, `soft`, `none`), `target_work_id`, `target_kind`, `severity` (`breaking`, `non-breaking`, `informational`) |
| `workflow.impact_notice_recorded` | `work_id`, `notice_id`, `source_contract_version`, `entity_kind`, `entity_ref`, `target_work_id`, `edge_id`, `old_hash` (nullable digest), `new_hash` (nullable digest), `severity` |
| `workflow.condition_added` | `work_id`, `condition_id`, `await_type` (`pr_merge`, `ci_result`, `timer`, `human_approval`, `remote_work_state`), `await_ref`, `resolution_authority` |
| `workflow.condition_resolved` | `work_id`, `condition_id`, `resolution_evidence` (1–32 evidence refs), `resolved_by_event` |
| `workflow.condition_cancelled` | `work_id`, `condition_id`, `cancellation_authority` (`operator`), `cancellation_evidence` (1–32 evidence refs), `cancelled_by_event` |
| `workflow.completed` | `work_id`, `terminal_state` (`completed`, `cancelled`, `superseded`), `final_verdict_kind`, `verdict_actor_ref`, `premise_confirmed` (boolean), `evidence_count` (0–32), `changed_refs_digest` (digest) |

Upcasters are registered by `(kind, payload_version)`, ordered, deterministic,
side-effect-free, and run before folding. A newer-than-supported version fails
closed and does not mutate any projection.

## 5. V15 projections, constraints, and folds

V15 creates the ten D2 projections below. Every table has `fold_guard=workflow`
semantics: direct writes are refused; only event application inside
`ApplyOperationTx` or `RebuildFromLog` may mutate it. `work_id` references the
existing `work_items(id)` key; actor references point to `workflow_actors`.

### 5.1 Tables

| Table | Columns (types) | Keys, foreign keys, and closed checks |
|---|---|---|
| `workflow_instances` | `work_id TEXT`, `definition_ref TEXT`, `definition_version INTEGER`, `definition_digest TEXT`, `current_step TEXT`, `instance_state TEXT`, `execution_actor_ref TEXT NULL`, `started_at TEXT NULL`, `completed_at TEXT NULL`, `last_checkpoint_at TEXT NULL` | PK `work_id`; FK work; FK actor nullable; checks positive version, digest, state enum |
| `workflow_contracts` | `work_id TEXT`, `contract_version INTEGER`, `premise TEXT`, `outcome_kind TEXT`, `outcome_payload TEXT`, `consequence_class TEXT`, `required_evidence TEXT`, `route_conventions TEXT`, `approved_at TEXT`, `approved_by TEXT`, `superseded_by INTEGER NULL`, `spec_mandate TEXT` | PK `(work_id,contract_version)`; FK work and approved actor; self-FK superseded contract; checks nonempty premise, closed outcome/evidence/consequence |
| `workflow_candidate_sets` | `work_id TEXT`, `contract_version INTEGER`, `candidate_kind TEXT`, `candidate_ref TEXT`, `candidate_role TEXT`, `candidate_scope TEXT`, `recorded_at TEXT`, `recorded_by TEXT` | PK `(work_id,contract_version,candidate_kind,candidate_ref)`; composite FK contract; FK actor; checks closed candidate kind/role |
| `workflow_actors` | `actor_ref TEXT`, `principal_ref TEXT`, `client_ref TEXT`, `agent_ref TEXT`, `session_ref TEXT`, `actor_class TEXT`, `first_seen_at TEXT` | PK `actor_ref`; UNIQUE tuple `(principal_ref,client_ref,agent_ref,session_ref)`; checks nonempty tuple and `actor_class IN ('agent','operator')` |
| `workflow_checkpoints` | `work_id TEXT`, `checkpoint_id TEXT`, `step_id TEXT`, `step_kind TEXT`, `attempt_epoch INTEGER`, `accepted_inputs_digest TEXT`, `result_evidence_refs TEXT`, `resume_cursor TEXT`, `idempotency_identity TEXT`, `actor_ref TEXT`, `request_id TEXT`, `recorded_at TEXT` | PK `(work_id,checkpoint_id)`; UNIQUE `(work_id,step_id,attempt_epoch)` and `(work_id,idempotency_identity)`; FK work/actor; closed step-kind check |
| `workflow_external_conditions` | `work_id TEXT`, `condition_id TEXT`, `await_type TEXT`, `await_ref TEXT`, `resolution_authority TEXT`, `condition_state TEXT`, `resolution_evidence TEXT NULL`, `resolved_by_event TEXT NULL`, `cancellation_authority TEXT NULL`, `cancellation_evidence TEXT NULL`, `cancelled_by_event TEXT NULL`, `recorded_at TEXT`, `resolved_at TEXT NULL`, `cancelled_at TEXT NULL` | PK `(work_id,condition_id)`; FK work; checks await type closed, state `open|resolved|cancelled`, and exactly one complete resolution/cancellation shape |
| `workflow_impact_edges` | `work_id TEXT`, `edge_id TEXT`, `edge_kind TEXT`, `edge_class TEXT`, `target_work_id TEXT`, `target_kind TEXT`, `severity TEXT`, `recorded_at TEXT` | PK `(work_id,edge_id)`; FK source/target work; closed edge/class/severity checks; hard `depends_on` and `forward_link` cycle check in transaction |
| `workflow_impact_notices` | `notice_id TEXT`, `source_work_id TEXT`, `source_contract_version INTEGER`, `entity_kind TEXT`, `entity_ref TEXT`, `target_work_id TEXT`, `edge_id TEXT`, `old_hash TEXT NULL`, `new_hash TEXT NULL`, `severity TEXT`, `recorded_at TEXT` | PK `notice_id`; UNIQUE `(source_work_id,source_contract_version,entity_kind,entity_ref,target_work_id,severity)`; `notice_id` must equal the deterministic derivation in §9; FK source/target work and source edge; closed severity |
| `workflow_decision_records` | `work_id TEXT`, `question TEXT`, `options_considered TEXT`, `decision TEXT`, `rationale TEXT`, `consequences TEXT`, `inputs TEXT`, `poc_findings TEXT`, `supersedes TEXT NULL`, `superseded_by TEXT NULL`, `recorded_at TEXT` | PK `(work_id,question)`; FK work; checks decision enum and required nonempty fields |
| `workflow_premise_confirmations` | `work_id TEXT`, `contract_version INTEGER`, `confirmed_by TEXT`, `confirmed_at TEXT` | PK `(work_id,contract_version)`; composite FK contract; FK actor; actor class must be `operator` |

JSON-encoded columns above are typed arrays or the referenced strict schema, not
arbitrary JSON objects. SQLite `CHECK` constraints validate the closed scalar
values; application validation validates the bounded array payload before the
transaction. No projection stores workflow definition authority.

### 5.2 Fold rules and rebuild

| Event family | Fold effect |
|---|---|
| definition selected | upsert the one instance definition pin; reject a change once an action has started |
| contract approved/superseded | insert the immutable contract version; mark only the previous version superseded |
| candidate set revised | insert added candidates and delete only the named removed candidates for that contract; premise and outcome bytes are untouched |
| actor recorded | insert once; a different tuple for an existing `actor_ref` is event poison |
| action started/checkpointed/completed/failed | update current step/state/checkpoint timestamps and insert/update checkpoint rows; `record_decision` extracts its typed decision-record checkpoint into `workflow_decision_records`; idempotency replay is a no-op |
| evidence bound | append to existing evidence authority and make the binding visible to completion; it is not copied into a workflow-only authority |
| verdict recorded | retain verdict in the event fold for the subject; expose it through the instance's verification view; no mutable verdict table is added |
| premise confirmed | insert operator confirmation |
| successor linked | fold the existing `forward_link` relation; workflow state remains independent |
| impact declared/notice recorded | insert or deduplicate typed edge/notice rows |
| condition added | insert an `open` condition with its owning `resolution_authority` |
| condition resolved | transition `open → resolved`; verify evidence belongs to the stored authority and fill resolution fields |
| condition cancelled | transition `open → cancelled`; require operator authority and cancellation evidence; fill cancellation fields; a resolved or cancelled condition cannot transition again |
| completed | set terminal state and completed timestamp only after the completion transaction's prior clauses pass |

`RebuildFromLog` clears, in one transaction, all ten tables in dependency order:
`workflow_premise_confirmations`, `workflow_impact_notices`,
`workflow_impact_edges`, `workflow_external_conditions`,
`workflow_checkpoints`, `workflow_candidate_sets`, `workflow_contracts`,
`workflow_decision_records`, `workflow_instances`, and `workflow_actors`.
It then replays the retained event log through the same upcasters and fold
handlers. A poison event quarantines the affected subject sub-log and leaves its
last known-good state explicitly degraded; it never deletes history. The live and
rebuilt byte representations must match. `ReconstructSubjectAt(work_id, seq)`
uses the same fold over the prefix through `seq` and is read-only.

## 6. Outcome evaluation and strength

The approved predicate is immutable. The evaluator receives the approved
predicate, delivered predicate/verdict, ground-truth resolver, immutable evidence
bindings, and the executing actor. It returns `satisfied`, `verdict_kind`, and
`incomparable_with_approved`.

Before evaluation, the submitted predicate is validated against the pinned
definition's `outcome_schema`, not only against the generic outcome union. The
validator rejects a predicate kind outside `allowed_kinds`, an outcome token not
in `allowed_outcome_tokens`, and any architecture-spike outcome without its bound
decision record. This check runs for all seven families; the generic union is only
the syntax check, while the selected definition is the authority for meaning.

1. **`exists`:** resolve every approved subject on the named ground-truth surface.
   Unknown/unreadable is not absent. A delivered `exists(S,D)` is strengthening
   when the surface is identical and `D ⊇ S`; it passes only when every `S` exists.
   A different surface or unresolved identity is incomparable.
2. **`absent`:** resolve every subject on the named ground-truth surface and apply
   every `distinguish_from` rule. A delivered absence is strengthening when its
   absent subject set contains the approved set and each approved identity is
   truly absent, not merely archived, relocated, renamed, or disabled. Relocation
   is therefore `outcome_mismatch`, never success. Different surface or changed
   distinction rules is incomparable.
3. **`outcome`:** evaluate the recorded token. It passes iff the token is a member
   of the approved `allowed` set. `no_change` is a valid research result. A spike's
   `accepted_decision` or `insufficient_evidence` token additionally requires the
   strict, reviewer-validated, operator-accepted decision record in §6.1. A token
   outside the set is weaker/mismatched.
4. **`check`:** resolve the registered evaluator against the exact immutable
   subject and compare its closed result with `expected_result`. The evaluator
   owns behavioral strength. It must return `stronger_or_equal`, `weaker`, or
   `incomparable`; `incomparable` is surfaced as `outcome_mismatch`, with no retry
   authority. A changed immutable subject is stale evidence, not a new result.

For all four kinds, unreadable data produces an undetermined result and cannot be
treated as a pass. `outcome_mismatch` is the required typed semantic refusal for a
weaker or incomparable delivery; the TS7 envelope amendment required by CD-0013
D10 ships with the later surface-version change, not this documentation pass.

### 6.1 Architecture-spike record

`accepted_decision` and `insufficient_evidence` require a bound decision record
containing framed questions, options with source-backed evidence, decision,
rationale, consequences, inputs, POC findings (or an explicit no-POC value),
supersession position, reviewer actor, operator acceptance, and for
`insufficient_evidence`, recorded unknowns plus what would be required to decide.
An unaccepted record does not satisfy the predicate or unblock a dependent Epic.

## 7. Ordered completion gate

`CompleteWorkflowTx` executes the following ordered clauses in one SQLite
transaction. It locks the work item, checks the pinned definition and expected
version, and rolls back on the first refusal. The typed refusal and recovery action
are stable:

| Order | Clause | Exact refusal | Recovery action |
|---:|---|---|---|
| 1 | Every required evidence binding exists, resolves to its immutable subject, and belongs to the approved contract | `missing_evidence` | `provide_evidence` |
| 2 | Every declared condition is resolved or explicitly cancelled by its owning authority | `not_terminal` | `reread_entities` |
| 3 | Every `modifies` edge is within the declared scope and the spec mandate is satisfied | `invariant_violation` | `reread_entities` |
| 4 | Verdict is present, its actor tuple is complete, and it is distinct from the executing actor | `unauthorized` for same/partial actor; `missing_evidence` for absent verdict | `contact_operator` / `provide_evidence` |
| 5 | Premise is confirmed by an operator-typed actor | `approval_required` | `request_approval` |
| 6 | The predicate is strengthening/satisfied and no declared blocking staleness rule drifted | `outcome_mismatch` or `stale_requires_review` | `contact_operator` / `refresh_context` |
| 7 | Contract, verdict, evidence bindings, impact notices, terminal metadata, and `workflow.completed` append commit atomically | `operation_conflict` | `reconcile_operation` |

The same transaction performs the reverse-edge impact scan and notice deduplication
before appending `workflow.completed`. A response is not terminal until the commit
returns success. A failed commit produces no terminal projection mutation.

## 8. Actor tuple and evaluator distinctness

The authenticated mutation context supplies `(principal_ref, client_ref, agent_ref,
session_ref)`. The engine validates all four are nonempty, canonicalizes each as a
bounded reference, and hashes these exact UTF-8 bytes with SHA-256:
`actor-v1\0principal_ref=<byte-length>:<value>|client_ref=<byte-length>:<value>|agent_ref=<byte-length>:<value>|session_ref=<byte-length>:<value>|`, where `\0` is one U+0000 NUL byte; the two literal characters backslash-zero are invalid.
`actor_ref=actor:<64 lowercase hex characters>` is the immutable identity. The
event stores the tuple once in `workflow.actor_recorded`; later events carry only
`actor_ref`.

`principal_ref` alone never establishes distinctness. A verdict is refused when its
`agent_ref` and `session_ref` both equal the executing actor's values. An operator
verdict is distinct from an agent executor but must still carry a complete tuple.
Any empty or partial tuple is refused. Actor rows and tuple fields cannot be
updated or deleted; a changed tuple receives a new actor reference.

## 9. Impact edges and notices

`modifies`, hard/soft `depends_on`, and `forward_link` are typed edges. `modifies`
is not cycle-checked. Hard `depends_on` and `forward_link` use the existing
in-transaction recursive cycle check. At completion, reverse edges are selected in
the bounded affected closure and the engine emits one notice per identity:

```text
(source_work_id, source_contract_version, entity_kind, entity_ref,
 target_work_id, severity)
```

The notice row additionally stores `edge_id`, `old_hash`, `new_hash`, and
`recorded_at`. `notice_id` is deterministic: concatenate the prefix `notice-v1\0`
with the six fields in the displayed order, each encoded as
`field_name=<UTF-8-byte-length>:<UTF-8-value>|`, then set
`notice_id = "notice:" + hex(SHA-256(canonical_bytes))`. The event carries
`notice_id`; the fold recomputes it and rejects a mismatch as
`invariant_violation` before inserting. The unique constraint is the identity
above; repeating completion or replaying its idempotency key emits no second
notice. Hard `depends_on` plus

For WF29's six values (`work-alpha`, `1`, `spec`, `spec:one`, `work-child`,
`breaking`), this exact encoding yields
`notice:3443b8a55c9f3fce4d6188b08b07a45f117cf0c31d8d88481b386f2cfd30ba9e`.
`breaking` blocks downstream execution at its consequential boundary. Hard
non-breaking and all soft edges warn. A contract revision emits `breaking` only
when an active hard dependent consumed the superseded contract; otherwise it warns.

## 10. External condition resolution

The engine exposes no wait daemon and performs no polling. Resolution is explicit
on request or at a consequential boundary:

```go
type ConditionResolver interface {
    Resolve(ctx context.Context, condition ExternalCondition,
        now time.Time) (Resolution, error)
}
```

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
`workflow.condition_added` and identifies the sole authority permitted to emit
`workflow.condition_resolved`; cancellation is not provider resolution and can
only be emitted by the operator with `cancellation_authority=operator` and its
own approval/evidence binding.

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
action `contact_operator`. The current TS8 surface remains `1.0.0`; shipping this
engine requires the explicit TS8 MAJOR amendment as surface `2.0.0`, with the
manifest, envelope schema, compatibility matrix, migration guidance, and operator
acceptance required by CD-0005 D11. No other error kind is added or renamed here.

The corpus records this boundary explicitly: `surface_version=1.0.0`,
`engine_status=specification_only`, and `outcome_mismatch` appears only in the
pending-amendment list for `2.0.0`. The checker compares every scenario's expected
error kind with the current envelope enum plus that explicit list. It fails on an
undeclared kind, and it fails if `engine_status` becomes `engine_shipped` while any
pending amendment remains. The pending list is therefore a bounded pre-shipping
declaration, not a permanent exemption.

## 12. Built-in family graphs and actions

These v1 built-ins fill CD-0013's per-family graph boundary. Action IDs are stable
data, not caller-invented commands. Every terminal path declares the universal
`record_verdict → confirm_premise → complete` sequence; no family can reach
`complete` through an undeclared action.

| Family and source | Ordered graph and declared actions at each step |
|---|---|
| **Implementation** — [`workflows.md` §1](./workflows.md#1-the-shift-from-one-workflow-to-a-plurality) and [`feature-inventory.md` §1.6](./feature-inventory.md#16-durable-execution-safety-substrate) | `proposal[record_proposal] → discovery[record_discovery] → design[record_design] → planning[approve_contract] → execution[start_execution, checkpoint_execution, bind_evidence, declare_impact, link_successor] → acceptance[record_verdict, confirm_premise] → release[complete]`. This is exactly proposal, discovery, design, planning, execution, acceptance, release; no shortened alias is permitted. |
| **Break-fix / RCA** — [`workflows.md` §3](./workflows.md#3-complete-work-kind-taxonomy) and §4 | `reproduce[record_reproduction] → diagnose[record_root_cause] → repair[start_repair, checkpoint_repair, bind_evidence, link_successor] → verify[record_verdict, confirm_premise] → complete[complete]`. Completion verb: reproduced defect no longer reproduces. |
| **Research / investigation** — [`workflows.md` §4](./workflows.md#4-example-workflow-types) and CD-0009 D3 | `frame[frame_research, approve_contract] → investigate[record_finding, revise_candidates, bind_evidence] → findings[record_report, link_successor] → conclude[record_conclusion, record_verdict, confirm_premise] → complete[complete]`. Completion verb: findings recorded; `no_change` is valid. |
| **Architecture spike** — [`architecture-spike.md` §2](./architecture-spike.md#2-shape) | `frame[frame_question, approve_contract] → research[record_research, bind_evidence] → options[record_option] → poc_optional[start_poc, checkpoint_poc, discard_poc] → decision_record[record_decision] → review[record_verdict] → acceptance[accept_decision, confirm_premise] → complete[complete]`. The optional POC edge skips only `poc_optional`; completion still requires the accepted decision record, verdict, premise confirmation, and `complete`. |
| **Ops runbook** — [`workflows.md` §4](./workflows.md#4-example-workflow-types) and [`managed-resource-inventory.md` §3](./managed-resource-inventory.md#3-stage-rule) | `plan[approve_contract] → approval[approve_operation] → execute[start_run, checkpoint_run, bind_evidence, add_condition, resolve_condition, cancel_condition] → health[record_health, record_verdict] → rollback_optional[rollback_run] → cleanup[cleanup_run, confirm_premise] → complete[complete]`. Rollback is optional recovery, not a second completion path. |
| **Static analysis** — [`workflows.md` §4–§5](./workflows.md#5-replacing-the-ad-hoc-analysis-skills) | `scope[approve_contract, declare_scope] → analyze[run_analysis, checkpoint_analysis] → report[record_report, bind_evidence] → review[record_verdict, confirm_premise] → complete[complete]`. Completion verb: report exists over the declared surface. |
| **Generic one-off** — [`workflows.md` §2.1a](./workflows.md#21a-outcome-contract) and CD-0013 D14 | `define[approve_contract] → execute[start_action, checkpoint_action, bind_evidence, link_successor] → verify[record_verdict, confirm_premise] → complete[complete]`. It may use any operator-authored allowed predicate without copying another family's semantics. |

### 12.1 Action-to-event mapping

The dispatcher validates every action above against its root `action_definitions`
entry. The following table is exhaustive; an action not listed is invalid. An
internal action uses `workflow.action_completed` plus the listed semantic event;
a fenced action first emits `workflow.action_started` and may emit
`workflow.action_checkpointed`, then emits `workflow.action_completed` or
`workflow.action_failed`. No event family is invented for a graph step.

| Action IDs | Semantic event mapping |
|---|---|
| `record_proposal`, `record_discovery`, `record_design`, `frame_research`, `record_finding`, `record_reproduction`, `record_root_cause`, `record_option`, `record_health`, `declare_scope`, `record_conclusion` | `workflow.action_completed`; immutable artifact/evidence is carried by `result_evidence_refs` and `changed_refs` |
| `approve_contract` | `workflow.contract_approved` plus its required approval evidence binding |
| `start_execution`, `start_repair`, `start_run`, `start_poc`, `start_action` | fenced `workflow.action_started` → `workflow.action_completed`/`workflow.action_failed` |
| `checkpoint_execution`, `checkpoint_repair`, `checkpoint_run`, `checkpoint_poc`, `checkpoint_analysis`, `checkpoint_action` | `workflow.action_checkpointed`; the checkpoint payload is the typed resume boundary |
| `bind_evidence` | `workflow.evidence_bound` |
| `revise_candidates` | `workflow.candidate_set_revised` |
| `declare_impact` | `workflow.impact_declared` |
| `link_successor` | `workflow.successor_linked` and the existing forward-link relation event |
| `record_verdict` | `workflow.verdict_recorded` |
| `confirm_premise` | `workflow.premise_confirmed` |
| `complete` | `workflow.completed`, only after the ordered gate in §7 |
| `frame_question` | `workflow.action_completed`; the framed question is part of the action result and later decision-record evidence |
| `record_research` | `workflow.action_completed` plus bound research evidence |
| `record_decision` | `workflow.action_checkpointed` with the typed decision-record payload, then `workflow.action_completed`; the accepted record is bound in the outcome payload |
| `accept_decision` | `workflow.evidence_bound` for operator acceptance plus `workflow.action_completed` |
| `discard_poc` | `workflow.action_completed` with a discard artifact reference; no POC bytes become workflow authority |
| `approve_operation` | approval evidence binding plus `workflow.action_completed` |
| `add_condition` | `workflow.condition_added` |
| `resolve_condition` | `workflow.condition_resolved` |
| `cancel_condition` | `workflow.condition_cancelled` |
| `rollback_run` | fenced `workflow.action_started` → `workflow.action_completed`/`workflow.action_failed`, with rollback evidence |
| `cleanup_run` | `workflow.action_completed` with cleanup evidence |
| `run_analysis` | fenced `workflow.action_started` → `workflow.action_completed`/`workflow.action_failed`; report evidence follows in `record_report` |
| `record_report` | `workflow.action_completed` plus `workflow.evidence_bound` for the report's immutable subject |

`action_definitions` carry closed payload field definitions for each ID. The
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
storage, or a new family. Implementers must refuse unknown values until those
registries are separately supplied.
