# CD-0242: Knowledge-first workflows keep current state, not history

- **Status:** Proposed
- **Date:** 2026-10-21
- **Scope:** Ownership of durable and current state, workflow stage exits, the
  execution loop, invalidation, evidence, completion, knowledge use, law-edit
  governance, resumption, recovery, and the cutover from the event-sourced
  engine
- **Implements:** [Concord (CON) issue 1275](https://linear.app/sharper-flow/issue/CON-1275)
- **Supersedes:** listed in the amendment map of Consequences
- **Approval:** Pending operator approval of exact text. The change is serious
  under P12: it affects more than one Domain, touches data safety, and changes
  operator approval points.

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

### D5. Execution is a sequential loop that Main controls

Main owns the plan and every progression. Within one work item, one
implementation assignment runs at a time. Main assigns; Implement builds and
verifies; Main dispatches Review; Review inspects the accumulated solution,
may commit corrective changes, verifies the corrected candidate, and reports.
Workers do not dispatch workers.

A review report names its input and output candidates, the criteria and
architecture revisions it judged, its findings, its remaining obligations, and
one disposition: continue, correction required, revision required, or ready
for final proof. Main records its acceptance of the disposition. A new
assignment needs an accepted disposition of continue bound to the current
candidate. Leaving execution needs an accepted disposition of ready for final
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

### Amendment map

This record supersedes these records in full:

| Record | Governing decision |
|---|---|
| CD-0115, released definitions never change in place | D14 |
| CD-0148, approved fresh worker retries | D6 |
| CD-0164, correction attempt counting | D6 |
| CD-0225, size overruns split without raising the cap | D6 |
| CD-0226, review findings carry fixes with cross-family checks | D5 |
| CD-0237, resize successor lineage | D4, D6 |
| CD-0238, point-in-time reconstruction outside the core | D2 |

Each listed clause is replaced by one sentence that states the current rule
and cites the governing decision here. The rest of each record stands.

| Record | Replaced clauses | Governing decision |
|---|---|---|
| CD-0002 | I2, I3, I5, I6; tier table; section 4 log survivor | D1, D2 |
| Core architecture | invariants 1, 3, 4, 7, 11; section 1 log discipline; section 3 migration row | D2, D6, D14 |
| CD-0008 | D6 | D2, D13, D14 |
| CD-0012 | D7, the executing-agent exclusion | D5 |
| CD-0013 | D1, D2, D3, D5, D10 clause 4, D11, D12, D13, D14 | D2, D3, D5, D7, D14 |
| CD-0017 | D5 retry limit and approval prerequisite; D6 model distinctness | D5, D6 |
| CD-0036 | D1 revision identity for staleness; D2; D3 strict quiescence | D7, D10 |
| CD-0130 | D1 to D3 | D2, D14 |
| CD-0137 | D1 to D4 | D5, D6, D7 |
| CD-0173 | D1 | D6 |
| CD-0197 | D1 verdict vocabulary; D4; D6 | D5 |
| CD-0205 | D4; D6 | D6, D14 |
| CD-0213 | D8 replay readability | D2 |
| CD-0218 | preamble pins; D2; D4; D5 | D2, D4, D13, D14 |
| CD-0220 | D3, for the review lane | D5 |
| CD-0229 | D3 to D6 | D4, D5, D6, D14 |
| CD-0230 | D4 factual corrections; D5 table gains the rule U preview point | D9, D10 |
| CD-0235 | D1, D2 | D5 |
| CD-0239 | D1, D2, D4 | D6 |
| PM1 query contract | Q7 history | D2 |
| PM3 domain schema | section 1 log authority; section 3.1; section 3.1b | D2 |
| PM9 process exhaust | section 1 | D2, D8, D9 |
| TS7 result envelope | replay-dependent recovery text | D2, D12 |
| Compaction retention policy | sections 1, 4, 7, 8 | D2 |
| Workflows and the workflow engine contract | frozen history; retry escalation; rebuild | D2, D4, D6, D14 |
| Design constraints, product-memory recovery, evidence store, membership, managed resources, installation, tool-surface evolution, floor readiness | append-only and replay statements | D2 |

These stand and are not retired: CD-0028 resource claims, CD-0102 native Task
dispatch, CD-0130 D5 refusal set, CD-0153 content staleness, CD-0173 D2,
CD-0178 D3 and CD-0179 D2 to D3 host process identity, CD-0201 one admission
derivation, CD-0215 and CD-0216 research, compaction retention section 6, and
PM6 note publication.

`goals.md` changes, quoted exactly:

- Goal B, "Who picks the size": "A defined decision model sets the minimum
  steps. The agent may add steps, never remove one."
- Appendix A2 row 5: "Review fixes problems inside the change's scope and
  verifies its fixes; file follow-up items for the rest | Same | Same; the
  coordinator runs more review until review reports no high or medium
  problem | Review fixes; the coordinator ends the loop".
- Appendix A2 row 8: "Run the decision model again, add the newly required
  steps, and continue without redoing completed work | Same | Same | Decision
  model; coordinator".

`priorities.md` changes follow D2 for its append-only and SQLite authority
lines, D4 for its size lines, and D5 for "a party other than the author
reviews every change", which gains: "A reviewer's own corrective edits are
verified, not reviewed again."

## Verification

Anchors are bound when the implementing change lands:

- the stage admission refusal tests for D3, D5, and D7;
- the resume-without-history acceptance fixture for D2 and D11;
- the reviewer-repair-to-delivery acceptance fixture for D5 and D9;
- the administrative-recovery acceptance fixture for D6, D7, and D12;
- the cutover interruption and snapshot-restore tests for D2 and D13;
- `python3 scripts/check-doc-contract.py` and
  `python3 scripts/check-knowledge-index.py` for the record and its shard.
