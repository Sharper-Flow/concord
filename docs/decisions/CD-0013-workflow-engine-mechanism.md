# CD-0013: Workflow Engine Mechanism and Contract

**Status:** Accepted
**Date:** 2026-08-08
**Decision owner:** Operator
**Accepted by operator:** 2026-08-08
**Reviewer:** Independent review per [`architecture-spike.md`](../architecture-spike.md) §5; five rounds, approved.
**Scope:** Workflow engine authority, definition registry, instance lifecycle, progression,
evidence binding, outcome verification, impact propagation, and the durable shapes a
workflow engine must add to the existing storage spine.
**Amends:** CD-0006 D3 (engine authority), D4 (rigor), D5 (spec-law), D10 (spec mandate);
CD-0008 D2 (evidence), D4 (checkpoints), D5 (external conditions), D6 (schema evolution);
CD-0009 D1/D2/D6 (research ownership); CD-0012 D1–D11 (outcome contract).
**Issue:** <https://github.com/Sharper-Flow/concord/issues/28>
**Historical surface note:** CD-0042 supersedes the pre-go-live agent-surface
version, negotiation, migration, and deprecation requirements described in D11.
Those passages remain historical evidence for the workflow-engine amendment, not
current compatibility policy.

## Context

CD-0006 D3 commits Concord to a full workflow coordination engine with native-effect
preservation. CD-0008 commits to checkpoint-and-resume, attempt fencing, immutable
evidence binding, typed external conditions, and schema evolution. CD-0009 commits
research ownership semantics. CD-0012 commits a three-part outcome contract with
independent evaluator authority and a strengthening-only delivery comparison.

The accepted law already defines:

- seven required workflow families with documented shapes and completion verbs;
- composition as forward-linked successors with no nested child execution;
- one durable transaction per logical step;
- attempt-epoch fencing for cross-authority and external-effect steps;
- an outcome contract with distinct operator and executing-agent revision authorities;
- typed evidence bound to immutable subjects;
- cross-workflow impact propagation through declared `modifies` and `depends_on` edges
  with breaking/non-breaking notices at consequential boundaries.

The accepted law does **not** yet:

- name a predicate representation that two workflows can compare for strength;
- name the actor identity used to enforce evaluator distinctness;
- name the durable shape a workflow instance, definition, contract, evidence, and
  checkpoint occupy in SQLite;
- name the event families a workflow engine must produce so `RebuildFromLog` remains
  total reconstruction authority;
- name the typed edges (`modifies`, hard/soft `depends_on`, `forward_link`) and how
  they surface as impact notices.

CD-0007 §D6 fixes the required workflow catalog at implementation, break-fix/RCA,
research/investigation, architecture spike, ops runbook, static-analysis family, and
generic one-off, plus the equivalence of database/configuration/infrastructure work
to implementation or ops.

This decision resolves the design choices still named as deferred by CD-0012 and adds
the implementation contract the engine needs.

## Decisions

### D1. Workflow definitions are code-defined, not stored authority

A workflow definition is a typed Go value in a registered registry. The registry
records:

- `ref` — canonical identifier string;
- `version` — strictly monotonic integer;
- `digest` — content hash of the canonical definition;
- `work_kind` — one closed set covering all seven required families;
- `step_graph` — directed graph of typed steps and edges;
- `available_actions` — closed enumeration of action references;
- `required_evidence_kinds` — closed evidence kinds per step;
- `outcome_schema` — required end-state predicate schema for this family;
- `rigor_rules` — required evidence per maturity × audience band;
- `staleness_rules` — declared inputs whose drift blocks or warns;
- `composition_rules` — forward-link successors and forbidden compositions.

SQLite persists only the selected `ref`, `version`, `digest`, and `instance_state`.
A workflow instance never re-reads or re-interprets the definition; it trusts the
digest pinned at planning. Active executions stay pinned to their accepted version
even if the registry registers a newer one (CD-0008 D6).

The generic one-off family inherits the same contract; only the values differ.

### D2. Workflow state lives on the work-item subject

A workflow instance is bound to one work item and inherits the work-item subject
type `work_item` already used by PM4 transitions and relations. No new subject
authority is introduced. Workflow projections are typed tables event-folded from
the existing `domain_events` log. `RebuildFromLog` must be able to reconstruct the
entire workflow state from the log.

The projections defined by this decision:

- `workflow_instances(work_id, definition_ref, definition_version, definition_digest,
  current_step, instance_state, execution_actor_ref, started_at, completed_at,
  last_checkpoint_at)`;
- `workflow_contracts(work_id, contract_version, premise, outcome_kind, outcome_payload,
  consequence_class, required_evidence, route_conventions, approved_at,
  approved_by, superseded_by)`;
- `workflow_candidate_sets(work_id, contract_version, candidate_kind, candidate_ref,
  candidate_role, candidate_scope, recorded_at, recorded_by)`;
- `workflow_actors(actor_ref, principal_ref, client_ref, agent_ref, session_ref,
  actor_class, first_seen_at)`, where `actor_ref` is the content hash of the tuple
  and rows are immutable once written;
- `workflow_checkpoints(work_id, checkpoint_id, step_id, step_kind, attempt_epoch,
  accepted_inputs_digest, result_evidence_refs, resume_cursor, idempotency_identity,
  actor_ref, request_id, recorded_at)`, preserving CD-0008 D4's required checkpoint
  identity: `actor_ref` carries and strengthens `principal_ref`, `request_id` is
  retained verbatim, and the event envelope supplies the recorded time;
- `workflow_external_conditions(work_id, condition_id, await_type, await_ref,
  resolution_authority, resolution_evidence, resolved_by_event, recorded_at,
  resolved_at)`;
- `workflow_impact_edges(work_id, edge_id, edge_kind, edge_class, target_work_id,
  target_kind, severity, recorded_at)`;
- `workflow_impact_notices(notice_id, source_work_id, source_contract_version,
  entity_kind, entity_ref, target_work_id, edge_owner_work_id, edge_id, old_hash,
  new_hash, severity, recorded_at)`;
- `workflow_decision_records(work_id, question, options_considered, decision,
  rationale, consequences, inputs, poc_findings, supersedes, superseded_by,
  recorded_at)`;
- `workflow_premise_confirmations(work_id, contract_version, confirmed_by,
  confirmed_at)`.

Each projection is reconstructed from the event log; none of them is writable by any
path other than `ApplyOperation`/`ApplyOperationTx`/fold. `fold_guard` enforces this.

### D3. Event families added

The workflow engine emits the following new typed events, all with
`subject_type=work_item`:

- `workflow.definition_selected` — `ref`, `version`, `digest`, `work_kind`;
- `workflow.contract_approved` — `premise`, `outcome_kind`, `outcome_payload`,
  `required_evidence`, `route_conventions`, `spec_mandate`, `rigor_class`;
- `workflow.contract_superseded` — `previous_contract_version`,
  `new_contract_version`, `supersede_reason`, `audit_evidence`;
- `workflow.candidate_set_revised` — `candidate_kind`, `candidate_ref`, `added`,
  `removed`;
- `workflow.actor_recorded` — `actor_ref`, `principal_ref`, `client_ref`,
  `agent_ref`, `session_ref`, `actor_class`;
- `workflow.action_started` — `step_id`, `action_id`, `attempt_epoch`,
  `accepted_inputs_digest`, `idempotency_identity`, `actor_ref`;
- `workflow.action_checkpointed` — `step_id`, `step_kind`, `attempt_epoch`,
  `checkpoint_payload`, `resume_cursor`, `actor_ref`, `request_id`;
- `workflow.action_completed` v2 — `step_id`, `attempt_epoch`,
  `result_evidence_refs`, `changed_refs`, `actor_ref`, and optional
  `worker_attempt_id`; v1 upcasts with no worker attempt identity. V2 built-in
  external-effect steps advance a dispatched worker attempt only through the
  declared `accept_worker_result` action, whose actor differs from the executor;
- `workflow.action_failed` — `step_id`, `attempt_epoch`, `failure_kind`,
  `recoverable`, `actor_ref`;
- `workflow.evidence_bound` — `evidence_kind`, `immutable_subject_ref`,
  `producer_id`, `producer_run_ref`, `producer_watermark`, `observed_at`;
- `workflow.verdict_recorded` — `predicate_id`, `verdict_kind`,
  `verdict_actor_ref`, `evaluation_evidence`, `incomparable_with_approved`;
- `workflow.premise_confirmed` — `contract_version`, `confirming_actor_ref`;
- `workflow.successor_linked` — `successor_work_id`, `relation_kind`;
- `workflow.impact_declared` — `edge_kind`, `edge_class`, `target_work_id`,
  `severity`;
- `workflow.impact_notice_recorded` — `source_contract_version`, `entity_kind`,
  `entity_ref`, `target_work_id`, `edge_owner_work_id`, `edge_id`, `old_hash`,
  `new_hash`, `severity`;
- `workflow.condition_added` — `await_type`, `await_ref`,
  `resolution_authority`;
- `workflow.condition_resolved` — `condition_id`, `resolution_evidence`,
  `resolved_by_event`;
- `workflow.completed` — `terminal_state`, `final_verdict_kind`,
  `verdict_actor_ref`, `premise_confirmed`, `evidence_count`,
  `changed_refs_digest`, `impact_verdict` (`breaking` or `non-breaking`).

Each event records `payload_version=1` on first emission; upcasters govern future
schema evolution under CD-0008 D6.

The R3 correction raises `workflow.completed` and
`workflow.impact_notice_recorded` to payload version 2. Historical completion
v1 events upcast to `impact_verdict=non-breaking`. Historical notice v1 events
upcast `edge_owner_work_id` to their former source-owned edge. New completion
actions must supply the verdict explicitly.

### D4. Outcome predicates use a closed union, never an expression language

A required end-state is one of four closed predicate kinds:

```text
exists:
  surface: <ground-truth authority>
  subjects: [<stable ids>]

absent:
  surface: <ground-truth authority>
  subjects: [<stable ids>]
  distinguish_from:
    - archived
    - relocated
    - renamed
    - disabled

outcome:
  allowed:
    - <workflow outcome token>

check:
  check_ref: <registered evaluator>
  immutable_subject_ref: <commit/artifact/version>
  expected_result: <closed value>
```

`outcome` resolves research to `no_change` and architecture spikes to
`accepted_decision` or `insufficient_evidence`. The generic one-off family permits
both `outcome` and `check`. Implementation, break-fix, ops runbook, and static
analysis use only `exists`, `absent`, and `check`.

An `outcome` token is never self-satisfying. The contract's `required_evidence` must
name the artifact that makes the token true, and completion refuses until that
evidence is bound. For an architecture spike this is mandatory and specific: both
`accepted_decision` and `insufficient_evidence` require a bound, reviewer-validated,
operator-accepted decision record carrying every field required by
[`architecture-spike.md`](../architecture-spike.md) §3 — framed questions, options
considered, decision, rationale, consequences, inputs, proof of concept (POC) findings where one was
built, and supersession position. `insufficient_evidence` additionally requires the
recorded unknowns and what would be required to decide. An unaccepted record does
not satisfy the predicate and does not unblock dependent Epic entries.

Predicates are not nested. Strength comparison is mechanical for `exists`/`absent`
(set-theoretic inclusion), trivial for `outcome` (membership in allowed set), and
delegated to a registered `check` evaluator for behavioral checks. A `check` whose
evaluator returns `incomparable_with_approved=true` causes completion to surface
to the operator instead of passing (CD-0012 D4).

The registry is closed. Adding a new predicate kind requires an accepted CD-NNNN.

### D5. Actor identity is the existing authenticated tuple

Evaluator distinctness and authorship fencing reuse the agent authority tuple
already bound by every workflow mutation:

- `principal_ref` — the human authenticating the invocation;
- `client_ref` — the registered trusted client key/version;
- `agent_ref` — the OpenCode session agent identity;
- `session_ref` — the durable session identity from grant bootstrap.

Concord is permanently single-operator (CD-0006 D8), so `principal_ref` is not a
distinguishing identity and must never carry this check alone.

The existing `domain_events` row records one `actor` string, which is insufficient
for this comparison. The tuple is therefore persisted explicitly: a
`workflow.actor_recorded` event writes an immutable `workflow_actors` row whose
`actor_ref` is the content hash of the tuple, and every event and projection that
must support the distinctness check carries that `actor_ref` — including
`workflow_instances.execution_actor_ref`, `workflow_checkpoints.actor_ref`, and the
`verdict_actor_ref` on `workflow.verdict_recorded`. A rebuild therefore reconstructs
the full comparison from the log alone.

Distinctness is evaluated against the executing identity, not the authenticating
human:

- A verdict whose `agent_ref` and `session_ref` both equal the recorded executing
  values is refused.
- A verdict recorded through the operator confirmation path is always distinct from
  an agent executor, because the operator is not the executing agent.
- A verdict with an empty or partial actor tuple is refused.

This preserves CD-0012 D7 — the executing agent cannot evaluate its own delivery —
without making operator evaluation of agent-run work impossible.

### D6. Evidence, approvals, and external conditions reuse existing machinery

- Evidence uses the immutable-subject binding from CD-0008 D2 with the closed kinds
  the envelope schema already enumerates — `verification`, `review`, `approval`,
  `commit`, `durable_note`, `native_run`, and `artifact`. This decision adds no
  evidence kind, and the engine records cross-step attestations as ordinary bindings
  of those kinds. Two drifts between CD-0008 D2's prose and that enum are noted and
  deliberately left for a separate reconciliation: the prose names
  "durable-knowledge" where the enum uses `durable_note`, and the prose omits
  `artifact` entirely. This decision resolves neither by implication.
- Approvals use the existing `ApprovalCheck{OperationDigest, Scope, Versions,
  Consequence}` chain with `Consequence=workflow_action` already reserved in the
  schema.
- External conditions use a `workflow_external_conditions` projection (D2). No
  resolver exists in the runtime today; the engine must add one bound to the closed
  `await_type` set — `pr_merge`, `ci_result`, `timer`, `human_approval`,
  `remote_work_state` — where resolution is an attributable event carrying the
  owning authority's evidence. Evaluation happens on explicit request and at
  consequential boundaries only. No wait authority is created outside that
  resolver.
- `durable_operations` and the attempt-epoch fence remain the only step-claim
  authority; workflow events fold the result, not duplicate the state.

### D7. Impact propagation uses typed edges and notices

The `modifies`, `depends_on` (hard/soft), and `forward_link` relations become typed
edges backed by `workflow_impact_edges`. The fold for `relation.added` is extended
to admit these kinds. PM4's acyclic-governing-graph invariant applies to
`forward_link` and hard `depends_on`, using the same in-transaction recursive cycle
check the existing relation folds already perform; `modifies` is not a governing
graph and is not cycle-checked.

At completion, the engine computes the reverse-edge set and writes one
`workflow.impact_notice_recorded` event per notice identity. CD-0006 R3 keys notices
by entity; a notice must additionally identify the dependent it informs and the
source contract state that produced it, so the identity is:

```text
(source_work_id, source_contract_version, entity_kind, entity_ref,
 target_work_id, severity)
```

Every component of that identity, plus `edge_id`, `old_hash`, and `new_hash`, is
carried on the event, so the projection is fully derivable by replay and its unique
constraint matches the identity exactly. Recomputing the same completion produces
the same identity and therefore no duplicate row. `edge_owner_work_id` records the
dependent that declared the inbound edge. Completion notices use
`entity_kind=work_item` and `entity_ref=<completed source work>`; they do not depend
on a spec mandate. The completion event owns the `breaking|non-breaking` verdict.
The edge's `hard|soft` class controls blocking; its stored severity remains legacy
declaration metadata and does not classify the delivered change.

Hard `depends_on` + `breaking` blocks downstream execution at the consequential
boundary; hard `depends_on` + `non-breaking` and all soft edges warn only. End-state
revision through `workflow.contract_superseded` reuses the same machinery and emits a
`breaking` notice when an active `hard depends_on` was on the prior contract and a
`non-breaking` notice for every other inbound dependent.

### D8. Composition is forward-link only

Forward-linked successors use the existing `relation.added` event with a new
closed kind `forward_link`; no parent waiting, no nested child execution. A
spike never nests under another spike. An implementation change does not nest
under a research workflow. The generic one-off type may forward-link to any other
family.

### D9. Spec mandate extends, not replaces, CD-0006 D10

The approved contract carries:

- `spec_mandate` — exact spec IDs the change is authorized to modify
  (CD-0006 D10);
- `outcome_kind` + `outcome_payload` — the CD-0012 outcome;
- `required_evidence` — closed evidence kinds required at completion;
- `route_conventions` — declared conventions that determine the operative verb
  (CD-0012 D8);
- `rigor_class` — the maturity × audience composition from CD-0006 D4.

Modifications to specs outside the mandate route back to planning under CD-0006 D10;
modifications to the outcome fail authorship fencing under CD-0012 D7.

### D10. Completeness gate enforces CD-0012 D9

A workflow reaches `terminal` only when:

1. All required evidence is bound and verified.
2. All declared external conditions are resolved or canceled.
3. All `modifies` edges match their declared scope and the spec mandate is satisfied.
4. The verdict is recorded by a distinct actor with a non-empty tuple.
5. The premise is confirmed by an operator-typed actor.
6. The contract, verdict, evidence, and terminal transition commit in one
   transaction (PM4 invariant 7 extension).

Each clause is a typed refusal at completion. Refusals reuse the existing TS7 closed
error kinds wherever one already fits — `missing_evidence`, `unauthorized`,
`approval_required`, `approval_invalid`, `invariant_violation`, `not_terminal`,
`stale_requires_review`, `version_conflict` — each paired with the recovery action
the envelope schema already requires for it.

CD-0012 D3 requires a *typed* outcome mismatch rather than a generic error, and the
closed error enum in `contracts/agent-tool-envelope.schema.json` does not contain one
today. This decision therefore amends the envelope contract to add exactly one error
kind, `outcome_mismatch`, paired with recovery `contact_operator`: a weaker or
incomparable delivery is resolved by the operator either strengthening the delivery
or superseding the contract, never by agent retry. That amendment is a MAJOR
tool-surface change under TS8; D11 records the process it must follow. No other error
kind is added and no existing kind changes meaning.

### D11. Public surface for workflow actions stays reserved

`concord_work_transition.workflow_action` already exists in the agent manifest,
capability map, consequence class, and envelope schema. Until this engine ships,
`WorkflowActionAvailable()` returns `false` and the operation is refused
structurally (CD-0005 D8 evolution rule). When the engine ships:

- `WorkflowActionAvailable()` returns `true` only when the selected definition's
  digest matches a registered entry.
- The dispatcher validates the action payload against the definition's
  `available_actions`, the running actor tuple, and the current step.

`workflow_action` remains the only surface for advancing a workflow. Free-form
advancement is not accepted.

Adding the `outcome_mismatch` error discriminant required by D10 is the shipped **MAJOR**
surface change under TS8's evolution rules, because it extends a closed error enum
that clients match exhaustively. The shipping change carries every TS8 MAJOR
requirement, without substitution:

1. named failing/passing TS1 scenario or TS9 removal evidence;
2. canonical manifest, schema, and docs updated in one change, with a new digest;
3. migration guidance and replacement mapping for the prior surface version;
4. old/new adapter↔core compatibility tests across the supported version matrix;
5. durable-operation and in-flight-work replay across the version change; and
6. explicit operator acceptance.

Regenerated Go and TypeScript artifacts, updated envelope fixtures, signed version
negotiation against the new supported range, and the bounded deprecation window
follow from those requirements. No alias, shim, or silent dual-acceptance is
introduced.

### D12. Staleness is structurally evaluated at consequential boundaries

The terminal completion gate checks declared `staleness_rules`. Drift in a declared
input produces one of:

- `warning` — recorded on `workflow.completed` and visible in the next read;
- `block` — completion refused until the input is re-verified.

No polling, no timer daemon, no heuristic authority (CD-0008 D5). Drift is recorded
through `workflow.evidence_bound` re-execution or `workflow.impact_notice_recorded`
on the changed upstream entity.

### D13. Reconstruction is total from the event log

The workflow projections join the existing rebuild path, `RebuildFromLog(ctx, store)`
in `internal/store/operation.go`. Every workflow projection table is added to the set
that rebuild clears before replay, and every new event family gains a fold handler, so
a rebuilt database matches the live write path byte-for-byte.
`ReconstructSubjectAt(ctx, store, subject, asOfSeq, purpose)` in
`internal/store/reconstruction.go` reconstructs one work item's workflow state at any
past event sequence for diagnosis (CD-0008 D6).

Active research remains the only state permitted to survive rebuild through byte-equal
snapshot and restore rather than replay, because CD-0009 makes it direct-table working
context rather than event authority. Workflow state takes no such exemption.

### D14. The seven families each bind to a concrete predicate form

| Family | Default predicate form | Allowed predicates | Completion verdict |
|---|---|---|---|
| Implementation | `check` (registered behavior) | `exists`, `absent`, `check` | `verdict_kind ∈ {ok, outcome_mismatch}` |
| Break-fix / RCA | `absent` (defect no longer reproduces) | `exists`, `absent`, `check` | `verdict_kind ∈ {ok, outcome_mismatch}` |
| Research | `outcome` | `outcome` | `verdict_kind ∈ {ok, outcome_mismatch}` |
| Architecture spike | `outcome` bound to an accepted decision record | `outcome` | `verdict_kind ∈ {ok, insufficient_evidence}` |
| Ops runbook | `check` (step evidence) | `exists`, `absent`, `check` | `verdict_kind ∈ {ok, outcome_mismatch}` |
| Static analysis | `check` (report exists over declared surface) | `exists`, `absent`, `check` | `verdict_kind ∈ {ok, outcome_mismatch}` |
| Generic one-off | `outcome` or `check` (operator-authored) | `exists`, `absent`, `outcome`, `check` | `verdict_kind ∈ {ok, outcome_mismatch, insufficient_evidence}` |

Adding a family or a default predicate form requires an accepted CD-NNNN.

## Required conformance scenarios

The implementation acceptance suite must exercise:

### Outcome contract (CD-0012 binding)

1. Capture accepts a work item without a required end-state; planning records one
   beside the spec mandate; completion verifies against the recorded approved
   contract.
2. A change contract with no required end-state is refused at planning.
3. A change contract whose required end-state already holds before execution is
   refused as vacuous.
4. A weaker delivery is refused with `outcome_mismatch`.
5. A stronger delivery is accepted.
6. An absence end-state is satisfied by removal; relocation is refused.
7. Candidate-set revision appends an event and leaves premise + end-state
   byte-identical.
8. Premise revision is represented as supersession only.
9. An execution-time write to the approved required end-state is refused.
10. Mid-execution discovery outside the approved end-state forward-links as successor
    work and does not close the current item.
11. End-state supersession re-enters planning and records an audit shape matching
    `specs-as-laws.md` §6.
12. The executing agent cannot author, replace, or disable its own end-state check.
13. A completion verdict whose actor equals the executing actor is refused; a
    verdict with no actor is refused.
14. A delivery route relying on an undeclared convention is refused at approval.
15. The lowest maturity × audience band still requires an approved contract; only
    proof depth differs.
16. Research `no_change` satisfies the required end-state.
17. Architecture spike `insufficient_evidence` satisfies the required end-state.
18. Completion where all postconditions pass but the premise is unconfirmed does
    not reach an accepted terminal state.
19. Contract + verdict + evidence + terminal metadata commit in one transaction.

### Checkpoint and fencing (CD-0008 binding)

20. Internal SQLite-only actions commit inline as one domain operation with no
    separate workflow fence event.
21. Two agents claim the same external step; only the current attempt epoch
    completes.
22. A committed checkpoint is the resume boundary; an uncommitted step is the
    resume point.
23. A retry with the same idempotency identity produces no second effect.
24. A step completed by a stale attempt_epoch is refused.
25. Operator takeover of a stuck attempt succeeds only with a valid approval_ref.

### External conditions

26. Each closed `await_type` evaluates through the resolver this decision requires
    the engine to add, on explicit request and at consequential boundaries only,
    and resolves only with its owning authority's evidence.
27. Conditions participate in ordinary `blocks` relations and inherit CD-0008 D3
    unreadable-record semantics.
28. No polling, no timer daemon, no automatic downstream rewrite.

### Impact propagation

29. Each inbound hard or soft `depends_on` edge produces exactly one notice per
    declared identity — `(source_work_id, source_contract_version, entity_kind,
    entity_ref, target_work_id, severity)` — at completion. The notice records
    the dependent as `edge_owner_work_id`, and recomputing the same completion
    produces no duplicate row.
30. Hard `depends_on` + `breaking` blocks downstream execution at the consequential
    boundary; everything else warns.
31. End-state revision emits a breaking notice when an active hard dependent
    consumed the superseded contract; otherwise warns.

### Composition

32. A workflow creates a forward-linked successor and finishes; both retain
    independent authority and recovery.
33. A spike does not nest under another spike; a change does not nest under
    research.
34. A generic one-off forwards to any other family without copying semantics.

### Reconstruction

35. Rebuilding the workflow projections from the event log produces the same state
    byte-for-byte as the live write path.
36. `ReconstructSubjectAt(work_id, sequence)` reconstructs a workflow's past
    state for diagnosis.

### Workflow_action surface

37. `WorkflowActionAvailable()` returns `false` until the engine registers and
    verifies a definition's digest; the operation is refused structurally
    otherwise.
38. Each action validates its payload against the registered `available_actions`,
    the actor tuple, and the current step.
39. TS7 envelopes emit `error` with the closed recovery_action set on refused
    actions; no fallback path exists.

### Staleness

40. Drift in a declared `staleness_rule` input blocks completion until the input
    is re-verified or warned only per rule severity.
41. Drift detected outside the consequential-boundary scan is recorded on
    `workflow.completed` and surfaced through the next read; no daemon re-scans.

### Retained CD-0008 obligations

This engine's acceptance suite retains, and does not replace, the accepted CD-0008
obligations that remain unproven:

42. Ten worktrees share one Product truth; no copied or branched database appears.
43. An unreadable possible blocker never yields `ready`, `no conflict`, or
    `release-safe`.
44. An unrelated unreadable record does not block an independently provable
    operation.
45. Projection corruption rebuilds from events; event poison isolates the affected
    subject and preserves history, and the two remain distinguishable.
46. Older supported events upcast deterministically; a newer-than-supported event
    fails closed before any projection mutation.
47. Evidence bound to one commit cannot authorize a changed commit.

## Required artifacts

This decision produces:

- `docs/workflow-engine-contract.md` — implementation contract binding this
  decision to concrete Go/storage/agent/CLI behavior;
- `contracts/workflow-definition.schema.json` — closed definition schema for
  generated manifests;
- `contracts/workflow-outcome.schema.json` — closed outcome payload schema;
- `scenarios/workflow-engine.v1.json` — the conformance scenario carriers listed
  above;
- typed Go built-ins for each of the seven families;
- generated manifest extensions for `workflow_action`.

## Consequences

### Positive

- The workflow engine gains one durable authority (event log), one definition
  authority (typed Go registry), one predicate authority (closed union), and one
  evaluator authority (authenticated tuple) — no second authority is introduced.
- CD-0012 deferred items 1–5 collapse to design choices this decision makes
  concrete; the remaining open items (cross-item outcome attribution, downstream
  notice class for end-state revisions) become behavior of the impact-propagation
  machinery.
- The seven required families become first-class workflow definitions generated
  from Go, exposed through the reserved `workflow_action` operation, and backed by
  the existing `durable_operations` claim/complete fence.
- Reconstruction remains total from the event log; workflow state never lives
  outside the existing fold guard.

### Cost

- A migration (V15) adds ten new projections and approximately sixteen event
  families; upcaster scaffolding for `payload_version=1` must ship with the engine.
- The workflow-action dispatcher reuses agent authority; an upgrade to
  `concord_work_transition.workflow_action` is required under TS8 once the engine
  ships.
- Definition manifests become durable artifacts; drift detection between the Go
  registry and the registered definition digest must run at every
  `WorkflowActionAvailable` call.

### What this forecloses

- A separate workflow DSL is rejected.
- A non-event-folded workflow state is rejected.
- A second evaluator authority (e.g. a human-only completion bypass) is rejected.
- A timer-driven or polling-driven staleness resolver is rejected.
- A change to the closed predicate union without an accepted CD-NNNN is rejected.

## Deferred to implementation design

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

## Supersession and amendments

This decision extends:

- CD-0006 D3 by fixing the engine's mechanical shape and definition authority;
- CD-0008 D2, D4, D5, D6 by fixing workflow event families, evidence binding,
  external conditions, and reconstruction;
- CD-0009 by aligning research ownership with the new outcome and projection
  shapes;
- CD-0012 by collapsing deferred items 1–4 into concrete predicate, actor, and
  event choices;
- CD-0005 by keeping `workflow_action` reserved until the engine ships.

It does **not** weaken any accepted decision. A revised definition form, predicate
kind, event family, or actor tuple requires an accepted CD-NNNN.

## Relationship to existing artifacts

| Artifact | Change |
|---|---|
| `docs/decisions/CD-0006-concord-root-product-policy.md` | Extends D3, D4, D5, D10 with engine-shaped definitions, rigor, and spec mandate enforcement. |
| `docs/decisions/CD-0008-concord-mechanism-hardening.md` | Extends D2, D4, D5, D6 with workflow evidence, checkpoints, conditions, and reconstruction. |
| `docs/decisions/CD-0009-active-research-context.md` | Aligns research with the new outcome predicate form. |
| `docs/decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md` | Collapses deferred items 1–4; preserves the contract, evaluator distinctness, and strengthening-only comparison. |
| `docs/decisions/CD-0005-concord-agent-tool-surface.md` | Keeps `workflow_action` reserved; promotes `WorkflowActionAvailable` to registered-engine check. |
| `docs/workflows.md` | Resolves the seven-family shape with a single machine contract. |
| `docs/architecture-spike.md` | Anchors the architecture-spike family to `outcome` predicate and the decision record schema. |
| `docs/priorities.md` | New decision-tracker row; no priority reordering. |
| `docs/rollout-plan.md` | New entry condition: workflow engine shipped before any non-trivial advancement through `workflow_action`. |
| `docs/README.md` | Companion-table row; status prose adjusted. |
