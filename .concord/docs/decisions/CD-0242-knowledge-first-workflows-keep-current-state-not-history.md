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

Concord derives all state from an append-only event log and replays it to
rebuild projections. Workflows are versioned action lists with retry budgets,
production-size caps, and a separate verdict actor. A completed action can
stand in for a judgment of what remains. Recovery has repeatedly created a new,
unrelated prerequisite.

The operator approved a knowledge-first replacement: Product knowledge and
completion receipts hold durable outcomes, and a shared local store holds
resumable current work. The operator accepted the loss of exact workflow
history, deterministic replay, and old-schema compatibility. The operator
required that a fresh session resume in-progress work without that history.

## Decision

### D1. Each kind of state has one owner

Linear owns planning. The Product's Git knowledge home owns law, the Domain
registry, specifications, promoted findings, and one completion receipt per
finished execution. The local SQLite store owns current setup, current work,
coordination, approvals, research, and unresolved effects. A per-execution
artifact directory owns working evidence files for the life of that execution.
The worktree owns code progress, both committed and uncommitted.

Neither store mirrors another owner. The SQLite store copies no Linear backlog.
A derived index of Git knowledge is rebuilt from Git.

### D2. The store keeps current state and finite snapshots, not history

Each mutation writes current rows in one transaction, checked by an expected
version and recorded by its idempotency key. No event log, fold, upcaster, or
replay exists. An idempotency record stays while its request can still be
retried or reconciled.

Recovery uses verified snapshots, tested restore, and reconciliation of current
rows with Git, worktrees, artifacts, and external effects. Snapshots have a
documented finite count and age. Snapshot cleanup never removes the newest
verified snapshot or one an unfinished recovery needs.

Each retained category names its consumer, how a consumer establishes
applicability, and the condition that makes removal safe. A category without a
consumer is not stored. Terminal cleanup runs only after verified publication,
release of active references and claims, and reconciliation of outstanding
effects. A consumer of a removed artifact receives a typed absence, never a
reference that claims the artifact is still inspectable.

### D3. Stage exits are the unit of enforcement

A workflow is a sequence of stages. Each stage declares its required result,
the revisions and candidate it binds, its judge, its evidence kinds, and its
permitted recovery. One admission function decides every advance. It refuses
when an exit is missing, rejected, stale, or recorded by an actor without
authority, and the refusal names the requirement and an admitted next action.

The core decides structural requirements. A semantic judgment, such as whether
an architecture meets its criteria, needs an attributed review bound to its
exact inputs. Schema validity is not semantic adequacy. A common requirement
has one implementation that every family reuses.

### D4. Families compose shared stages, and rules set the minimum

The live families are implementation, break-fix, and research. For
architecture-bearing implementation work the dependency order is: accepted
criteria, applicable design approval, validated architecture, delivery plan,
execution loop, required preview approval, delivery.

D9's preview and early visual-design approvals are ordinary questions Main asks
the operator and records in the existing work record. They are not
machine-enforced stage exits.

Rules derived from change type and risk set each execution's mandatory stages.
Main may add research, architecture, or planning depth. No agent may waive a
mandatory stage or an operator approval. Current validated knowledge may
satisfy a requirement without a new document. Break-fix keeps diagnosis and
regression proof. Research may end with evidenced findings instead of
implementation proof.

Acceptance criteria state the observable outcome and its constraints.
Architecture states responsibilities, boundaries, interfaces, data flow,
invariants, and failure behavior, and cites the contract's Domain and law
scope. The plan connects remaining requirements to an order and a proof
strategy and details only the next assignment.

### D5. Execution is a loop that Main controls

Main owns the plan and every progression. Each implementation assignment
carries one focused outcome. Main may run independent assignments of one work
item as one concurrent group when their interfaces are settled and each member
runs in its own claimed member worktree; dependent assignments run in sequence.
Implement builds and verifies; Main integrates the group's results into one
candidate and dispatches Review; Review inspects the accumulated solution, may
commit corrective changes, verifies the corrected candidate, and reports.
Workers do not dispatch workers.

A review report names its input and output candidates, the criteria and
architecture revisions it judged, its findings, its remaining obligations, and
one disposition: continue, correction required, revision required, or ready
for final proof. Main records its acceptance of the disposition. A new
assignment group needs an accepted disposition of continue bound to the current
integrated candidate; members of one group need no Review between them. A late
or duplicate member result never replaces a newer accepted candidate. Leaving execution needs an accepted disposition of ready for final
proof bound to the current candidate. An open in-scope blocking finding
blocks both.

Implement cannot accept its own result. Review's corrective edits belong to its
pass and need no further review. No separate final evaluator, and no executing
actor distinctness beyond these rules, is required.

### D6. Attempts carry their own authority, not a budget

Authorization, actual start, terminal result, and result acceptance are
distinct recorded facts. Each launch is one admitted request with its own
scope, assignment, and subject bindings. No attempt count, correction count,
convergence basis, escalation wall, or reset exists.

A launch confirmed never started leaves the assignment and its valid proof
unchanged. A launch with an unknown effect is reconciled before another launch
for that assignment. Each terminal result is accepted or rejected once. A
useful partial result is accepted as progress with its remaining obligations.

Main sizes each assignment by complexity and relatedness. No production-line
cap, size measurement, or mandatory split applies. Scope, prerequisite,
ownership, and safety checks remain.

### D7. A revision invalidates only what depended on it

Each exit, review, and proof records the digests of the revisions, candidate,
and inputs it depended on. It is stale exactly when one of those digests has
changed. The dependency order is criteria, architecture, plan, assignment, and
separately candidate to proof. A record about an earlier subject stays true of
that subject and authorizes nothing about a changed one.

A changed requirement first re-evaluates the existing candidate. Code changes
are required only when the candidate no longer satisfies it. A contract
version change, a relaunch, or a corrected admission request invalidates
nothing by itself. No step requires a no-op commit or repeated production.

A revision within unchanged approved scope has a supported route. A change to
the approved outcome, a security boundary, or another operator decision needs
its approval. A revision never erases an unresolved finding.

### D8. Evidence is captured by its producer and bound to its subject

A verification receipt records what ran, the subject commit and tree, the
relevant inputs and environment, the outcome, the producer, and the artifact
location and digest. The tool captures these facts; agents do not re-enter
them. Consumers reference one receipt. A new session reuses applicable
receipts. A worker's statement that a check passed is not a receipt.

Failure and unknown outcomes are recorded as such. A receipt never claims a
fact that was not captured.

### D9. Completion needs proof, approvals, delivery, and publication

Completion requires a passing receipt for every accepted criterion on the
final candidate, the accepted ready disposition on that candidate, no open
in-scope blocking finding, every applicable approval, and delivery evidence.

A change under rule U needs the operator's approval of a development preview
of the final candidate before it is pushed to a shared remote or merged. A
later change to the approved experience needs renewed approval. Substantial
visual changes get early operator approval of a wireframe or mockup. Backend
work has no preview gate.

Closure publishes the changed Product knowledge, or a reviewed judgment that
no knowledge changed, and a concise receipt in the canonical knowledge home.
The receipt links the delivered subject, the accepted outcome, consequential
decisions, and the proof that substantiates acceptance.

### D10. Knowledge is an input, and a law edit changes only what it changes

A definition resolves applicable law, Domain architecture, specifications, and
findings before criteria are accepted. Reusing a knowledge entry requires its
demonstrated applicability and proportionate validation of its consequential
facts. A worker report or a provisional finding never becomes law by being
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

### D11. A fresh session resumes from current state

Given a Linear issue key, a session resolves the work item, its Project, its
active execution, and its worktree. It reports committed and uncommitted
changes, completed exits, remaining obligations, unresolved findings, evidence
locations, and the next admitted actions. It needs neither the predecessor
conversation nor any historical record.

Ownership passes only through the liveness-checked occupancy route. A resume
never takes work from a live session. A session whose authority was replaced
cannot record a result. Claims, overlap detection, dependencies, and
cross-Project handoffs stay atomic in the shared store. A missing worktree is
rebuilt from surviving Git references; uncommitted bytes are recoverable only
from a backup.

### D12. Every refusal states an action the caller may take

One typed recovery vocabulary runs from the core through the adapter, the tool
schemas, and agent instructions unchanged. A correctable pre-effect input
refusal names the field, the supported bound, and the corrected request. That
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
setup, knowledge homes, research and its consumers, and active work, preserves
every worktree and its uncommitted files, reconciles unresolved operations,
verifies the target, and retires old tables and code. It reports progress and
recovers from interruption without an old-schema execution fallback.

Active work continues under the new model from verified current state. The
cutover needs a verified recovery snapshot and the operator's approval of its
destructive operation. Tooling explains missing or incompatible data and its
migration route.

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
diagnosis beyond the retained receipt, mechanical size ceilings, and a final
independent reviewer. No loss of Product knowledge, referenced research,
active code progress, coordination, or fresh-session resumption is accepted.

## Verification

- The implementing runtime change binds the executable anchors for the runtime
  obligations in D2 to D13.
  Planned anchors include the stage admission refusal tests, the
  resume-without-history and reviewer-repair-to-delivery acceptance fixtures,
  the administrative-recovery fixture, and the cutover interruption and
  snapshot-restore tests. Until that change lands, `python3 scripts/check-knowledge-index.py` is the executable
  anchor for this record's law registration.
  D9's preview and early visual-design approvals use the agent's ordinary
  question and existing work record, not an executable preview gate.
  Classified contract consent does not prove approval of the shown experience.
- `python3 scripts/check-knowledge-index.py` composes the record shards and
  checks each shard's recorded document sha256 against the document bytes.
- `python3 scripts/check-knowledge-closure.py --strict` proves this document
  is registered and leaves no knowledge document unprocessed.
- `python3 scripts/check-doc-contract.py` checks the required decision
  outline, the anchor discipline of this section, and accepted-law citation
  targets.
