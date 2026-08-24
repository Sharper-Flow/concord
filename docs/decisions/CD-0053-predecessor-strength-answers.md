# CD-0053: The three un-mandated predecessor strengths are answered

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** The three features CD-0019 D3 left "not separately
  preservation-mandated" — the gated human-checkpoint lifecycle, the
  agreement-gate contract-before-work discipline, and standalone wisdom
  capture; closes the CD-0019 D3 research questions and satisfies the #295
  cutover-blocking criterion
- **Approval:** Operator accepted the drafted decision as written on 2026-08-21,
  approving all three answers on the merge grant for PR #299; the public record is
  [issue #295 comment](https://github.com/Sharper-Flow/concord/issues/295#issuecomment-5375357551)
- **Related:** CD-0019 D1–D3, CD-0006, CD-0009, CD-0012, CD-0026, CD-0035,
  CD-0037, [`priorities.md`](../priorities.md) Priority 3 and §Workflow
  versatility, [`workflows.md`](../workflows.md), issue #295
- **Preserves:** The CD-0019 mandate method (D2 research-informed shapes);
  the closed workflow registry; the knowledge-home authority
- **Supersedes:** nothing; answers questions CD-0019 deliberately left open

## Context

CD-0019 D1 preservation-mandated six predecessor features and D3 explicitly
declined to mandate three more: the gated human-checkpoint lifecycle, the
agreement-gate contract-before-work discipline, and standalone wisdom
capture. D3 said each might be "absorbed into one of the six, redesigned, or
dropped — a per-feature research question, not pre-decided here."

Issue #295 made those answers cutover-blocking: the first Product migration
cannot proceed until each is answered in writing — preserved-with-shape
(citing the Concord mechanism), absorbed (citing where), or dropped (citing
the reason). The operator's framing, recorded on #295: these three are
"precisely the mechanisms behind the predecessor's does-not-miss quality."
They are the machinery behind the pauses at the right moment, the contract
before the code, and the lessons that outlive any single change.

The research CD-0019 D2 required is complete: each capability's Concord
shape now exists, is structurally enforced, and carries executable evidence.
What was missing is the record that closes the question.

## Decision

### D1. Gated human-checkpoint lifecycle: **absorbed**

The predecessor's strength was never the number seven — it was that a human
holds authority at consequence boundaries and cannot be routed around by an
agent mid-flight. Concord preserves that property as typed machinery rather
than as a fixed gate sequence:

- **Approval-consequence summaries (CD-0037):** a consequential operation
  that lacks approval is refused with `approval_required`, and the refusal
  carries a core-derived consequence summary — never caller prose — so the
  operator approves against what the system will do, not what the agent says
  it will do. Scenario `AJ8-approval-required` binds this end to end.
- **Approval routes on product-changing work:** the workflow registry
  structurally classifies every definition with `changes_product_truth`, and
  product-changing definitions must carry an approval route
  (`TestProductChangingDefinitionsHaveApprovalRoute`).
  A definition that changes Product truth cannot exist without the human
  checkpoint; it is unrepresentable, not remembered.
- **One type among many:** the seven-gate implementation change remains a
  workflow type, not the system shape (`priorities.md` §Workflow versatility).
  Research and ops runbooks get purpose-built types whose *own* definitions
  declare where their consequence boundaries sit.

What is deliberately not preserved: the fixed sequence as a universal
ceremony. Forcing a seven-gate lifecycle onto an ops runbook or a research
pack was the predecessor's own acknowledged friction, and CD-0006 already
rejected it.

### D2. Agreement-gate contract-before-work: **preserved-with-shape**

The predecessor's agreement gate made work state its objectives, acceptance
criteria, and constraints before execution, and held execution to them. The
failure mode it prevented — work drifting from what was agreed because
nothing structural compared delivery against intent — is exactly what
Concord's outcome contract makes unrepresentable:

- **The typed outcome contract (CD-0012):** every work item carries premise,
  required end-state, and candidate set, each with its own revision
  authority. A weaker delivered end-state fails completion
  (`WF03-vacuous-end-state`, `WF04-weaker-delivery`); a changed premise
  supersedes the item rather than silently re-scoping it
  (`WF11-end-state-supersession-audit`).
- **Governing requirements at capture (CD-0035):** law captured with work is
  bound at capture time and governs completion, rather than being re-argued
  at a gate.
- **Planning requires the contract (`WF02-planning-requires-outcome`):**
  execution authority does not exist until the typed contract does.

The shape is stronger than the predecessor's in the specific way Concord's
law demands: the agreement is enforced at completion by the completion gate
comparing delivery against contract, not only at a human checkpoint reading
prose.

### D3. Standalone wisdom capture: **preserved-with-shape**

The predecessor promoted per-change wisdom into durable, searchable project
learnings. Concord's shape already shipped as CD-0026 and is not standalone —
it is part of the knowledge authority:

- **Lessons are a first-class knowledge kind**, published through
  `concord_work_compact.lesson_publish` with operator approval (D7's accepted
  durable reader), landing in the Product's git knowledge home and indexed by
  the knowledge manifest
  (`TestManifestRebuildIndexesDecisionSpecLessonAndQ10Proof`).
- **Deliberately not carried:** unpromoted per-change wisdom — the transient
  working layer. CD-0009 already fixes active working context as bounded and
  non-durable; the predecessor's two-tier split is collapsed to one durable
  tier (publish or discard), which removes the promotion decision from the
  author's workflow and puts it in the archive surface where approval lives.

### D4. These answers are cutover-blocking per #295, and now satisfied

Issue #295's strength-harvest criterion requires each D3 question answered in
writing before the first cutover. This record is that answer. The shadow
evaluation of the first Product (Corded, per operator direction 2026-08-21)
must still demonstrate the machinery catches real cases — the answers here
state what exists and what evidence binds it; shadow tests whether it
composes under real work.

## Consequences

- CD-0019 D3's three open questions are closed. No predecessor strength
  remains un-mandated-but-live: six features were preservation-mandated with
  Concord shapes, and these three are now answered absorbed or
  preserved-with-shape, each citing its mechanism and evidence.
- The 7-gate lifecycle as a universal ceremony is formally dropped; its
  discipline survives as typed approval steps bound to workflow definitions.
- The migration import design may rely on: approval/consequence machinery
  carrying checkpoint discipline, the outcome contract carrying agreements,
  and lesson publication carrying durable wisdom — so imported work carries
  its contract, and imported learnings land as lessons.
- Shadow exit criteria for Corded should include, explicitly: one approval
  refused with a correct consequence summary, one completion failed against
  a weaker delivered end-state, and one lesson published through the archive
  surface.

## Rejected alternatives

**Preserve the seven-gate lifecycle as the system shape.** Rejected: it is
the predecessor's acknowledged over-application — ceremony applied to work
kinds that needed only their own consequence boundaries. CD-0006 and
`priorities.md` §Workflow versatility already decided this; reversing it to
honour nostalgia would reintroduce known friction.

**Drop the agreement discipline as "just planning."** Rejected: the
predecessor's does-not-miss quality depends on delivery being compared to
intent structurally. CD-0012 exists precisely because prose agreements
drift. Dropping it would remove the only mechanism that fails work for being
weaker than agreed.

**Carry wisdom as standalone capture.** Rejected: a second durable-knowledge
authority beside the git knowledge home violates CD-0002's single-derivation
invariant and CD-0019's own warning against cargo-culting predecessor
shapes. The lesson kind inside the knowledge authority is the same capability
with one owner.

**Defer the answers until after shadow.** Rejected: #295 makes them
cutover-blocking precisely because shadow needs to know what to look for.
Answering after shadow would mean shadow evaluated an unspecified system.

## Verification

- Each D-section cites a mechanism that exists and evidence that executes in
  the required workflow: `AJ8-approval-required` (scenario anchor),
  workflow-registry approval-route tests (go_test anchors),
  `WF02`/`WF03`/`WF04`/`WF11` (scenario anchors), lesson publication and
  manifest rebuild tests (go_test anchors) — all already bound in
  `docs/floor-readiness.v1.json` and `docs/law-coverage.v1.json`.
- `docs/concord-knowledge-index.v1.json` records CD-0053 once;
  `docs/law-coverage.v1.json` (generated from shards under `docs/knowledge/coverage/`)
  declares its coverage.
- `python3 scripts/check-json.py`, `check-doc-links.py`,
  `check-knowledge-index.py`, `check-floor-readiness.py` pass.
