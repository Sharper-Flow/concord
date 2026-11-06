---
{
  "id": "CD-0013",
  "title": "Workflow engine mechanism",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "workflow-engine",
  "applies_to_domain_ids": []
}
---
# CD-0013: Workflow Engine Mechanism and Contract

**Status:** Accepted
**Amended:** 2026-10-18.
**Date:** 2026-08-08
**Decision owner:** Operator
**Accepted by operator:** 2026-08-08
**Reviewer:** Independent review; five rounds, approved.
**Scope:** Workflow engine authority, type templates and per-item plans, instance lifecycle, progression,
evidence binding, outcome verification, workflow composition, and the durable shapes a
workflow engine must add to the existing storage spine.
**Amends:** CD-0006 D3 (engine authority), D4 (rigor), D5 (spec-law), D10 (spec mandate);
CD-0008 D2 (evidence), D4 (checkpoints), D6 (schema evolution);
CD-0012 D1–D11 (outcome contract).
**Amended:** 2026-10-18. These 2026-10-17 records amend this one:
[CD-0229](CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md)
amends this record's D1 (plain Go workflow definitions);
[CD-0230](CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md)
amends D9 (law-change approval follows CD-0243 D2).
**Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D5, D7, D11, D13, D14
**Issue:** <https://github.com/Sharper-Flow/concord/issues/28>

## Context

CD-0006 D3 commits Concord to a full workflow coordination engine with native-effect
preservation. CD-0008 commits to current-state resumption, expected versions, immutable
evidence binding and schema evolution. CD-0012 retains the stated goal and
independent Review ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).

The accepted law already defines:

- four workflow types with required template steps copied into per-item plans;
- composition as forward-linked successors with no nested child execution;
- one durable transaction per logical step;
- expected-version and idempotency checks for each atomic mutation;
- an outcome contract with distinct operator and executing-agent revision authorities;
- typed evidence bound to immutable subjects;

The accepted law does **not** yet:

- name the durable shape a workflow instance, definition, contract, evidence, and
  checkpoint occupy in SQLite;
- name how each workflow mutation persists; the store keeps current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history))
- name the typed edges (`modifies`, hard/soft `depends_on`, `forward_link`).

[CD-0218 D1](CD-0218-the-live-workflow-catalog-retains-three-execution-families.md#d1-four-workflow-types-own-new-execution)
sets the workflow types as change, fix, research, and removal.

This decision resolves the design choices still named as deferred by CD-0012 and adds
the implementation contract the engine needs.

## Decisions

### D1. Workflow types use one template contract

The canonical template contract supplies required steps for each type; each work item holds its own plan ([CD-0218 D1](CD-0218-the-live-workflow-catalog-retains-three-execution-families.md#d1-four-workflow-types-own-new-execution)).

### D2. Workflow state lives on the work-item subject

Workflow state lives as current rows on the work item, written in one transaction under an expected version ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### D3. Each mutation writes current rows

The engine records each mutation as a current row in one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### D5. Actor identity is the existing authenticated tuple

Actor identity stays the existing authenticated tuple.

### D6. Evidence reuses existing bindings

- Evidence uses the immutable-subject binding from CD-0008 D2 with the closed kinds
  the envelope schema already enumerates — `verification`, `review`, `approval`,
  `commit`, `durable_note`, `native_run`, and `artifact`. This decision adds no
  evidence kind, and the engine records cross-step attestations as ordinary bindings
  of those kinds. Two drifts between CD-0008 D2's prose and that enum are noted and
  deliberately left for a separate reconciliation: the prose names
  "durable-knowledge" where the enum uses `durable_note`, and the prose omits
  `artifact` entirely. This decision resolves neither by implication.
- Operations and effect receipts retain successful-request retry and reconciliation
  identity. There is no separate step-claim or settlement authority. Nonterminal
  state stays resumable; completion disposes workflow step records under PM6 §1.

### D7. Workflow composition uses typed edges

`workflow_impact_edges` holds the typed edges used by successor composition.
Relation writes commit as current rows in one transaction
([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
PM4's acyclic governing graph combines `forward_link` and hard `depends_on`
edges. The in-transaction recursive check refuses a link that creates a cycle.

### D8. Composition is forward-link only

Forward-linked successors use the closed `forward_link` relation kind; the relation is a current row written in one transaction ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)). No parent waiting, no nested child execution. An
implementation change does not nest under a research workflow.

### D9. Spec mandate extends, not replaces, CD-0006 D10

The approved contract carries:

- `spec_mandate` — exact spec IDs the change is authorized to modify
  (CD-0006 D10);
- `legislative_subjects` — subjects recorded for the exact contract version;
  legislative standing requires the operator-approved contract and the other
  conditions in CD-0159 D2, not subject inclusion alone;

Modifications to specs outside the mandate route back to planning under CD-0006 D10.

### D10. Completion commits terminal state atomically

Completion records reported delivery links and commits terminal metadata and
the lifecycle transition in one transaction (PM4 invariant 7 extension).
The legal terminal action and state, active contract, expected version,
authenticated actor, idempotency and atomic rollback remain required under
[workflow-engine contract §7](../specs/workflow-engine-contract.md#7-ordered-completion-gate).
Completion requires the active contract's recorded premise confirmation and, for
change, fix and removal, a recorded delivery assertion. It does not re-evaluate
evidence, candidate scope or mandates, Review or commit drift.
Required plan steps and applicable approvals retain their own obligations.

D11 and TS8 govern changes to the current generated surface.

### D11. Workflow actions use the current generated agent surface

Definitions generate from one canonical source, and the identities active work needs are derived at the integration boundary ([CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source)).

Operator decisions follow CD-0230 D5 and
[CD-0243 D2](CD-0243-four-roles-and-six-operator-approval-classes.md#d2-the-operator-approves-six-classes-of-decision).
Consent follows the six operator approval classes, not execution consequence,
durability, action name, or a missing classification. Routine engineering and
work management outside those classes need no consent. Serious law and crucial
size alone do not require approval. Approval-boundary changes require the
operator's approval of their exact wording.

Operator approval classes remain conversational under CD-0243: consent is
never inferred from a title, tag, kind, path, or intent text. Every workflow
records a valid contract and premise confirmation.

Contract updates and corrections preserve actor, evidence, subject, version,
source, scope, current-step and recovery checks at their owning actions.
Completion follows D10. Recovery follows CD-0243 D5: the operator may approve
explained irreversible loss, and customer-data loss remains reserved.
Contract updates grant no legislative authority or automatic overlap exemption.
The ordinary update and its version-addressed provenance follow
[workflow-engine contract §4.1](../specs/workflow-engine-contract.md#41-ordinary-contract-updates).

Independent native Task Review inspects the accumulated solution. Main records
its reviewed-step result, reviewer, commit and actual link without Concord
attesting a subagent producer ([CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d5-execution-is-a-loop-that-main-controls)).

A premise confirmation records the authenticated coordinator. A UI/UX change requiring
approval retains the operator's review and acceptance at the current contract
version; contract approval alone does not satisfy that acceptance. Minor
corrections introducing no new design or interaction need no fresh UI/UX
approval.

The shared admission derivation owns questions and next actions. Every reachable state retains an admitted, evidence-backed exit and terminal reachability. Review is a real prerequisite, not an assumed verdict. No hold loop or fabricated human actor satisfies liveness.

At the current operation surface, removal, cancellation, repair,
reconciliation, overlap resolution, membership
changes and delivery correction require operator consent
only when a CD-0243 D2 class applies. Governing-requirement scope cuts remain
product decisions. Granting access remains a security decision. No approval
grants otherwise absent capability or authorization. Existing authentication,
capability, state, evidence and exact-target checks remain required. Workflow acceptance does not authorize host-controlled
destructive commands, deployment, release, credential disclosure, or
force-push; host permissions still govern them.

Type contracts require explicit classification. Each item's plan derives from the single template contract ([CD-0218 D1](CD-0218-the-live-workflow-catalog-retains-three-execution-families.md#d1-four-workflow-types-own-new-execution)).

[CD-0042 D2](CD-0042-pre-go-live-single-path.md#d2-the-current-agent-surface-has-one-identity)
owns the current agent-tool contract identity, its digest binding, and
fail-closed mismatch.

`concord_work_transition.workflow_action` is declared in the current manifest,
capability map, consequence class, and strict envelope schema:

- Admission requires a readable workflow state.
- The dispatcher validates the action payload against its strict schema, the
  action catalog, the running actor tuple, and the current plan frontier.
  Unknown fields and unsupported discriminants fail closed.

`workflow_action` remains the only surface for advancing a workflow. Free-form
advancement is not accepted.

[`agent-tool-surface-evolution.md`](../specs/agent-tool-surface-evolution.md) (TS8)
§2 owns the change rule and required evidence for changes to closed
discriminants, including `outcome_mismatch`, governed before go-live by
[CD-0042 D3–D4](CD-0042-pre-go-live-single-path.md#d3-superseded-development-paths-are-deleted).

### D12. Proof remains bound to its subject

A record about an earlier subject does not authorize a changed one. Applicable
proof stays bound to the candidate and inputs it judges (CD-0242 D7).

### D13. Recovery restores a verified snapshot

Recovery uses verified snapshots with tested restore and reconciles current rows with Git, worktrees, artifacts, and external effects ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

## Required conformance scenarios

The implementation acceptance suite must exercise:

### Outcome contract (CD-0012 binding)

1. An intent revision appends a retained contract version without changing earlier content.
2. Work discovered outside the approved scope forward-links as successor work.
3. Independent Review may correct its candidate; final review requires a clean pass.
4. The lowest maturity × audience band still requires an approved contract;
   only proof depth differs.
5. Premise confirmation and the reported delivery remain terminal facts.
6. Terminal metadata, lifecycle change and delivery links commit atomically.

### Current-state mutations (CD-0008 binding)

20. Internal SQLite-only actions commit inline as one domain operation with no
    separate workflow fence event.
22. A committed checkpoint is the resume boundary; an uncommitted step is the
    resume point.
23. A retry with the same idempotency identity produces no second effect.

### Composition

32. A workflow creates a forward-linked successor and finishes; both retain
    independent authority and recovery.
33. An implementation change does not nest under research.

### Reconstruction

35. A resumed session reads current workflow state from the store with no
    historical record (CD-0242 D2, D11); recovery restores a verified snapshot
    and reconciles current rows.
36. A verified snapshot restore yields the recorded current workflow state;
    the core holds no point-in-time reconstruction (CD-0242 D2).

### Workflow_action surface

37. Workflow actions refuse when the workflow state cannot be read.
38. Each action validates its payload against the action catalog,
    the actor tuple, and the current plan frontier.
39. TS7 envelopes emit `error` with the closed recovery_action set on refused
    actions; no fallback path exists.

### Staleness

Proof remains bound to its recorded subject. A changed candidate needs applicable
proof; no polling, timer daemon, or heuristic re-scan substitutes for it.

### Retained CD-0008 obligations

This engine's acceptance suite retains, and does not replace, the accepted CD-0008
obligations that remain unproven:

42. Ten worktrees share one Product truth; no copied or branched database appears.
43. An unreadable possible blocker never yields `ready`, `no conflict`, or
    `release-safe`.
44. An unrelated unreadable record does not block an independently provable
    operation.
45. Corrupted rows restore from the newest verified snapshot and reconcile; an
    unreadable row yields unknown without deleting recorded state, and the two
    remain distinguishable.
46. A record whose schema the release does not support fails closed before any
    mutation, and derived schemas generate from one canonical source.
47. Evidence bound to one commit cannot authorize a changed commit.

## Required artifacts

This decision produces:

- `.concord/law/specs/workflow-engine-contract.md` — implementation contract binding this
  decision to concrete Go/storage/agent/CLI behavior;
- `contracts/workflow-step-templates.v1.json` — closed template contract for
  the workflow types and the stored step plan schema;
- `.concord/scenarios/workflow-engine.v1.json` — the conformance scenario carriers listed
  above;
- typed Go support for the four workflow types and their plans;
- generated manifest extensions for `workflow_action`.

## Consequences

### Positive

- The workflow engine gains one state authority (current rows and finite
  snapshots) and one template authority (one canonical contract).
- The four workflow types use per-item plans through `workflow_action`, backed by
  ordinary atomic versioned mutations, operations and effect receipts.
- Recovery uses verified snapshots with tested restore; workflow state lives only as current rows ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).

### Cost

- The cutover is one documented migration with a verified snapshot; no projection or event-family scaffolding ships ([CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)).
- The workflow-action dispatcher reuses agent authority; an upgrade to
  `concord_work_transition.workflow_action` is required under TS8 once the engine
  ships.
- Each item's step plan is a durable artifact; every plan write passes the one
  plan-write check before it saves (CON-1574; the workflow-engine-contract §2.3).

### What this forecloses

- A separate workflow DSL is rejected.
- Workflow state outside current rows is rejected (CD-0242 D2).
- A timer-driven or polling-driven staleness resolver is rejected.

## Deferred to implementation design

1. **Per-type action enumeration.** Each type's available action list and the
   exact step graph are implementation work, not part of this decision. The
   decision fixes the shape; the implementation fills it.
2. **Audience commitment storage.** Runtime audience band storage and the
   effective-rigor evaluation routine are implementation work.

## Supersession and amendments

This decision extends:

- CD-0006 D3 by fixing the engine's mechanical shape and definition authority;
- CD-0008 D2, D4, D5, D6 by fixing workflow checkpoint and state recording,
  evidence binding and recovery;
- CD-0012 through actor and state-recording choices;
- CD-0005 by keeping `workflow_action` reserved until the engine ships.

It does not waive other accepted decisions. A revised definition form or actor
tuple requires an accepted CD-NNNN.

## Relationship to existing artifacts

### Ordinary record writes

Retry and correction use the owning action under
[workflow-engine contract §4.2](../specs/workflow-engine-contract.md#42-ordinary-retry-and-record-correction).
Record-only replacements hold step and lifecycle. Actor, version, idempotency,
evidence, subject and approval checks remain required. Native execution stays
host-owned under CD-0242 D6; it supplies no delivery or verdict authority.
Review and premise confirmation retain their own requirements;
completion follows D10.

| Artifact | Change |
|---|---|
| `.concord/law/decisions/CD-0006-concord-root-product-policy.md` | Extends D3, D4, D5, D10 with engine-shaped definitions, rigor, and spec mandate enforcement. |
| `.concord/law/decisions/CD-0008-concord-mechanism-hardening.md` | Extends D2, D4, D5, D6 with workflow evidence, checkpoints, conditions, and recovery. |
| `.concord/law/decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md` | Preserves the stated goal, independent Review and forward-linking. |
| `.concord/law/decisions/CD-0005-concord-agent-tool-surface.md` | Keeps `workflow_action` reserved; promotes `WorkflowActionAvailable` to registered-engine check. |
| `.concord/docs/workflows.md` | Describes the workflow types; each type's required steps live once in the template contract `contracts/workflow-step-templates.v1.json` (CD-0218 D1). |
| `.concord/law/constitution/priorities.md` | New decision-tracker row; no priority reordering. |
| `.concord/docs/rollout-plan.md` | New entry condition: workflow engine shipped before any non-trivial advancement through `workflow_action`. |
| `.concord/docs/README.md` | Companion-table row; status prose adjusted. |
