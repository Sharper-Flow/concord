---
{
  "id": "CD-0242",
  "title": "Knowledge-first workflows keep current state, not history",
  "authority": {
    "tier": "derived"
  },
  "home_domain_id": "product-root:concord",
  "applies_to_domain_ids": [],
  "law_relations": [
    {
      "kind": "refines",
      "target_id": "CD-0002"
    },
    {
      "kind": "refines",
      "target_id": "CD-0008"
    },
    {
      "kind": "refines",
      "target_id": "CD-0012"
    },
    {
      "kind": "refines",
      "target_id": "CD-0013"
    },
    {
      "kind": "refines",
      "target_id": "CD-0036"
    },
    {
      "kind": "refines",
      "target_id": "CD-0173"
    },
    {
      "kind": "refines",
      "target_id": "CD-0213"
    },
    {
      "kind": "refines",
      "target_id": "CD-0218"
    },
    {
      "kind": "refines",
      "target_id": "CD-0220"
    },
    {
      "kind": "refines",
      "target_id": "CD-0229"
    },
    {
      "kind": "refines",
      "target_id": "CD-0230"
    },
    {
      "kind": "refines",
      "target_id": "PM1"
    },
    {
      "kind": "refines",
      "target_id": "PM3"
    },
    {
      "kind": "refines",
      "target_id": "PM9"
    },
    {
      "kind": "refines",
      "target_id": "TS7"
    },
    {
      "kind": "refines",
      "target_id": "compaction-retention-policy"
    },
    {
      "kind": "refines",
      "target_id": "workflow-engine-contract"
    },
    {
      "kind": "refines",
      "target_id": "PM5"
    },
    {
      "kind": "refines",
      "target_id": "PM8"
    },
    {
      "kind": "refines",
      "target_id": "PM10"
    },
    {
      "kind": "refines",
      "target_id": "TS8"
    },
    {
      "kind": "refines",
      "target_id": "CD-0178"
    },
    {
      "kind": "refines",
      "target_id": "CD-0217"
    }
  ],
  "product_wide_rationale": "Knowledge-first workflows replace the event-sourced state authority, the stage-exit and execution-loop rules, evidence binding, resumption, recovery, and the cutover path for every Domain, so the change reaches the whole Product rather than one child Domain."
}
---
# CD-0242: Knowledge-first workflows keep current state, not history

- **Status:** Accepted
- **Date:** 2026-10-21
- **Scope:** Ownership of durable and current state, workflow stage exits, the
  execution loop, invalidation, evidence, completion, knowledge use, law-edit
  governance, resumption, recovery, and the cutover from the event-sourced
  engine
- **Implements:** [Concord (CON) issue 1275](https://linear.app/sharper-flow/issue/CON-1275)
- **Approval:** The operator approved D1–D14, the amendment map, and the
  quoted `goals.md` and `priorities.md` edits as exact text on 2026-10-21 for
  [Concord (CON) issue 1275](https://linear.app/sharper-flow/issue/CON-1275).
  The change is serious under P12: it affects more than one Domain, touches
  data safety, and changes operator approval points.

## Context

Recovering current work must not depend on reconstructing an event history.
A completed action cannot stand in for a judgment of what remains, and retry
or correction must not introduce an unrelated prerequisite.

Product knowledge and reported delivery links hold outcomes, and a shared local store holds
resumable current work. The operator accepted the loss of exact workflow
history, deterministic replay, and old-schema compatibility. The operator
required that a fresh session resume in-progress work without that history.

## Decision

### D1. Each kind of state has one owner

Linear owns planning. The Product's Git knowledge home owns law, the Domain
registry, specifications and promoted findings. The local SQLite store owns current setup, current work,
coordination, approvals, and unresolved effects. A per-execution
artifact directory owns working evidence files for the life of that execution.
The worktree owns code progress, both committed and uncommitted.

Neither store mirrors another owner. The SQLite store copies no Linear backlog.
A derived index of Git knowledge would be rebuilt from Git, never a second
authority; none exists. Law resolves through the committed law folder, and
historical note locators are retained rows that restore as current state
([PM6 §3](../specs/canonical-git-note-placement.md#3-historical-locator)).

### D2. The store keeps current state and finite snapshots, not history

Each mutation writes current rows in one transaction, checked by an expected
version and recorded by its idempotency key. No event log, fold, upcaster, or
replay exists. An idempotency record stays while its request can still be
retried or reconciled.

Ordinary actions own replacement of their current records, including delivery
links after completion, under
[workflow-engine contract §4.2](../specs/workflow-engine-contract.md#42-ordinary-retry-and-record-correction).
Record-only corrections hold the step and lifecycle. Only the ordinary current
records are retained, without parallel correction state.

Recovery uses verified snapshots, tested restore, and reconciliation of current
rows with Git, worktrees, artifacts, and external effects. Snapshots have a
documented finite count and age. Snapshot cleanup never removes the newest
verified snapshot or one an unfinished recovery needs.

Each retained category names its consumer, how a consumer establishes
applicability, and the condition that makes removal safe. A category without a
consumer is not stored. [PM6 §1](../specs/canonical-git-note-placement.md#1-decision)
governs completion and disposal of workflow step records without publication,
closure hashes or deferred terminal cleanup. Terminal work identity and reported
delivery links remain; operations and effect receipts keep their retry and
reconciliation consumers. A consumer of a removed artifact receives a typed absence, never a
reference that claims the artifact is still inspectable.

### D3. Stage exits are the unit of enforcement

A workflow is a sequence of stages. Each stage declares its required result,
the revisions and candidate it binds, its judge, its evidence kinds, and its
permitted recovery. One admission function decides every advance. It refuses
when an exit is missing, rejected, stale, or recorded by an actor without
authority, and the refusal names the requirement and an admitted next action.

The core decides structural requirements. A semantic judgment, such as whether
an architecture meets its criteria, needs an attributed reviewed-step result. Schema validity is not semantic adequacy. A common requirement has one implementation that every type reuses.

### D4. Types compose shared stages, and rules set the minimum

The workflow types are change, fix, research, and removal. For
architecture-bearing change work the dependency order is: accepted
criteria, applicable design approval, validated architecture, delivery plan,
execution loop, required preview approval, delivery.

D9's preview and early visual-design approvals are ordinary questions Main asks
the operator and records in the existing work record. They are not
machine-enforced stage exits.

The single template contract sets each type's mandatory steps, copied into each
item's plan, without a work-item size classification.
Main may add research, architecture, or planning depth. No agent may waive a
mandatory stage or an operator approval. Current validated knowledge may
satisfy a requirement without a new document. The fix type keeps diagnosis and
regression proof. Research may end with evidenced findings instead of
implementation proof.

Acceptance criteria state the observable outcome and its constraints.
Architecture states responsibilities, boundaries, interfaces, data flow,
invariants, and failure behavior, and cites the contract's Domain and law
scope. The plan connects remaining requirements to an order and a proof
strategy and details only the next assignment.

### D5. Execution is a loop that Main controls

Main owns the plan and every progression. Each implementation assignment
carries one focused outcome. Main may run independent assignments concurrently
when their interfaces and host worktree placement are settled; dependent assignments
run in sequence. Implement builds and verifies. Main integrates the results
into one candidate and obtains independent Review through the host's native
Task. Review inspects the accumulated solution, may make corrective changes,
verifies the corrected candidate, and reports.

Main records a reviewed attempt through `record_review`, naming `step_id`,
`note`, `reviewer`, `result`, `commit`, and the actual native Task result `link`.
It uses the same step-attempt writer as `complete_step`. The linked review
result accounts for its outputs and findings; Concord does not maintain a
separate typed review report. An intermediate review may record a fixed result.
The final review stays open until a clean pass is recorded on the candidate.
The recorded tuple does not execute Review.

Implement cannot accept its own result. Review's corrective edits belong to its
pass and need no further review. No separate final evaluator, and no executing
actor distinctness beyond these rules, is required.

### D6. Native Task belongs to the host

The host owns native Task execution. Concord neither authorizes, packages,
starts, reads back, reconciles, nor recovers subagent runs. It retains no lane
registry, dispatch window, packet, worker report envelope, worker job, or
producer-identity attestation. No compatibility launcher replaces these owners.
Workflow stages own subject-bound results, not subagent execution.

Main obtains independent review and records its actual outcome. A partial
result remains progress with its remaining obligations. No attempt count,
correction count, convergence basis, escalation wall, or reset exists.
Removal of execution attestation never permits fabricated review or proof,
skipped review stages, or weaker required CI.

Main sizes each assignment by complexity and relatedness. No production-line
cap, size measurement, or mandatory split applies. Scope, prerequisite,
ownership, and safety checks remain.

### D7. A revision invalidates only what depended on it

A review or proof about an earlier subject stays true of that subject and does
not authorize a changed one. A changed candidate or actual recorded input needs
the proof applicable to that subject; Git reconciliation does not relabel
prior evidence. Law or Domain registry amendment or deletion alone does not
invalidate proof.

A changed requirement first re-evaluates the existing candidate. Code changes
are required only when the candidate no longer satisfies it. A contract version
change, a relaunch, or a corrected admission request invalidates nothing by
itself. No step requires a no-op commit or repeated production.

A revision within unchanged approved scope has a supported route. A change to
the approved outcome, a security boundary, or another operator decision needs
its approval. A revision never erases an unresolved finding.

### D8. Evidence is captured by its producer and bound to its subject

The coordinator binds ordinary evidence through `bind_evidence`, with an
`immutable_subject_ref`. The stored binding requires a `producer_watermark`.
A caller submitting the locator in the `bind_evidence` call, in top-level
`evidence` or declared fields, may omit the producer fields; the authenticated
binding operation supplies its principal, operation ID, and request ID. A caller may instead name an earlier
producer operation. A binding is an attributed claim, not
attestation that a test ran. Concord does not inspect Git or GitHub to validate
it, execute repository tests, or issue native verification receipts.
Independent Review judges the adequacy of evidence. Required CI remains
external and mandatory. The verified snapshot route remains mandatory before
retiring stored execution data.

### D9. Verification owns proof; completion records delivery

Required plan steps include independent Review and applicable verification.
Completion requires a recorded confirmation for the active contract version and,
for change, fix and removal, a recorded delivery assertion. Research does not
require a delivery assertion. Completion does not recheck criterion proof,
Review reports or acceptance evidence. Its terminal mutation follows
[workflow-engine contract §7](../specs/workflow-engine-contract.md#7-ordered-completion-gate).

A change under rule U needs the operator's approval of a development preview
of the final candidate before it is pushed to a shared remote or merged. A
later change to the approved experience needs renewed approval. Substantial
visual changes get early operator approval of a wireframe or mockup. Backend
work has no preview gate.

Completion records the existing typed reported-delivery assertion and its links
under [PM6 §1](../specs/canonical-git-note-placement.md#1-decision), with no Git or
GitHub verification of that assertion. It requires no completion receipt,
publication intent, reviewed closure hash, knowledge-publication judgement or
post-delivery workflow step retention. Product-law changes still use their
own reviewed enactment route; lessons use CD-0026's independent reviewed PR.

### D10. Knowledge is an input, and a law edit changes only what it changes

A definition resolves applicable law, Domain architecture, specifications, and
findings before criteria are accepted. Reusing a knowledge entry requires its
demonstrated applicability and proportionate validation of its consequential
facts. A native Task report or a provisional finding never becomes law by being
recorded.

An agent corrects a factual documentation error inside its work item without
an operator prompt when the correction changes neither intended behavior nor
an operator decision. Such a correction is not a law change under P12.

A law edit triggers reassessment only of the obligations whose meaning it
changes. It does not by itself require approvals, additional reviewers, a
restart, or blanket evidence invalidation. Approval follows the substantive
P12 consequence. Every law change is listed in the pull request and the
completion report with its records, its change in meaning or the absence of
one, and its reason.

Workflow contracts store no law or registry freshness pins, and current reads
report no compatible-amendment hash pairs. Current readable law context and
same-operation snapshot consistency remain required. Concord retains no
owning-start law inventory, enactment before-state or per-item law attribution.
Law publication and review follow CD-0222 D1; a contract does not retain a
law-change payload or a separate authoring reason or removal list.

### D11. A fresh session resumes from current state

Given a Linear issue key, a session resolves the work item, its Project, and its
active execution. Host shell and Git tools inspect the current checkout;
Concord stores no worktree locator or inspection metadata. The session reports
committed and uncommitted changes, completed exits, remaining obligations, unresolved findings, evidence
locations, and the next admitted actions. It needs neither the predecessor
conversation nor any historical record.

Host tools own worktree placement and recovery under CD-0088 D1, not a
Concord occupancy or restore operation. A session whose workflow authority was
replaced cannot record a result. Workflow authority, overlap detection,
dependencies, and cross-Project handoffs stay atomic in the shared store.
Uncommitted bytes are recoverable only from a backup.

### D12. Every refusal states an action the caller may take

One typed recovery vocabulary runs from the core through the adapter, the tool
schemas, and agent instructions unchanged. It describes an admitted ordinary
action, not a separate correction route or retry-count budget. Per-operation
seconds budgets and diagnostic attribution remain required. A correctable
pre-effect input refusal names the field, the supported bound, and the corrected request. That
correction needs no contract revision, operator decision, new session, or
release. Malformed input, missing context, unmet requirements, denied
authority, host failure, and unknown effects stay distinct. A correction hint
never authorizes a retry of a possibly effective operation or bypasses a
denial.

A recovery hint must satisfy the named action's preconditions that the core
can decide from recorded state. It must not require discarding retained work
that no recorded result has judged. When no admitted recovery exists, the
hint says so and names the maintainer follow-up. It must not prescribe an
unchanged request, invented revision, or qualification of an earlier candidate
as though that would review the retained descendant.

### D13. The cutover is one documented migration with a verified snapshot

One migration route inventories what survives and what is discarded, migrates
setup, knowledge homes, and active work, preserves
every worktree and its uncommitted files, reconciles unresolved operations,
verifies the target, and retires old tables and code. It reports progress and
recovers from interruption without an old-schema execution fallback.

Active work continues under the new model from verified current state. The
cutover needs a verified recovery snapshot and the operator's approval of its
destructive operation. Tooling explains missing or incompatible data and its
migration route.

The verified-snapshot migration preserves active work, knowledge and
worktrees while removing retired execution state. Supported historical reads report truthful absence; migration
invents no successful outcome or subagent provenance.

### D14. Generated contracts derive from one source

Derived schemas and projections are generated from one canonical source. No
released workflow-definition version is retained for history, and no
hand-maintained version, pin, or catalog list duplicates that source.
Identities that active work needs are derived at the integration boundary.
Integrated subjects are revalidated, and earlier evidence is never relabeled.

## Alternatives considered

- Keep the event log and add pruning. Rejected: pruning breaks replay, so the
  log would keep the cost of history without its guarantee.
- Keep a historical interpreter beside the new engine. Rejected: a second live
  route doubles admission and recovery and keeps the compatibility cost.
- Keep a fresh final evaluator after a repairing review. Rejected by the
  operator: Review's verified repair plus Main's oversight and the preview
  gate replace it. This removes one independent pair of eyes.
- Keep line caps as a backstop. Rejected by the operator: agent-judged chunk
  size replaces a mechanical ceiling.

## Consequences

The operator accepts these losses: exact workflow traces, deterministic replay
of completed executions, repair of derived tables by replay, retrospective
diagnosis beyond retained current state and external delivery records, mechanical size ceilings, and a final
independent reviewer. No loss of Product knowledge, referenced research,
active code progress, coordination, or fresh-session resumption is accepted.

## Verification

- The implementing runtime change binds the executable anchors for D2 to D13,
  except D9's preview and early visual-design approvals. Those use the agent's
  ordinary question and existing work record, not an executable preview gate.
  Classified contract consent does not prove approval of the shown experience.
  Planned anchors include the stage admission refusal tests, the
  resume-without-history and reviewer-repair-to-delivery acceptance fixtures,
  the administrative-recovery fixture, and the cutover interruption and
  snapshot-restore tests. Until that change lands, the committed law-folder read is the executable
  anchor for this record's law registration.
- Committed law-folder snapshot validation binds the authored metadata and
  the document bytes into one content digest.
