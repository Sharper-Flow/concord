# CD-0013: Workflow Engine Mechanism and Contract

**Status:** Accepted
**Amended:** 2026-10-18.
**Date:** 2026-08-08
**Decision owner:** Operator
**Accepted by operator:** 2026-08-08
**Reviewer:** Independent review per [`architecture-spike.md`](../architecture-spike.md) §5; five rounds, approved.
**Scope:** Workflow engine authority, definition registry, instance lifecycle, progression,
evidence binding, outcome verification, impact propagation, and the durable shapes a
workflow engine must add to the existing storage spine.
**Amends:** CD-0006 D3 (engine authority), D4 (rigor), D5 (spec-law), D10 (spec mandate);
CD-0008 D2 (evidence), D4 (checkpoints), D5 (external conditions), D6 (schema evolution);
CD-0215 D2/D6 (research ownership); CD-0012 D1–D11 (outcome contract).
**Amended:** 2026-10-18. These 2026-10-17 records amend this one:
[CD-0229](CD-0229-workflows-are-composed-from-shared-parts-and-a-decision-model-sets-each-work-items-size.md)
amends this record's D1 (workflows composed from shared parts);
[CD-0230](CD-0230-the-conflict-rules-are-one-checked-table-and-an-agent-changes-law-that-is-not-serious.md)
amends D9 (operator approval only for serious law changes);
[CD-0234](CD-0234-the-engine-has-no-external-condition-resolver.md) amends D6's
external conditions bullet and acceptance criterion 26 (no external-condition
resolver); [CD-0235](CD-0235-the-independent-verdict-is-the-outcome-strength-evaluation.md)
amends D4's strength comparison paragraph (the independent verdict is the outcome
strength evaluation); [CD-0236](CD-0236-vacuity-is-judged-at-review.md) amends
outcome-contract acceptance criterion 3 (vacuity is judged at review); and
[CD-0238](CD-0238-point-in-time-reconstruction-runs-outside-the-core.md) amends D13's
`ReconstructSubjectAt` paragraph and acceptance criterion 36 (point-in-time
reconstruction runs outside the core).
**Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2, D5, D7, D11, D13, D14
**Issue:** <https://github.com/Sharper-Flow/concord/issues/28>
**Historical surface note:** CD-0042 supersedes the pre-go-live agent-surface
version, negotiation, migration, and deprecation requirements described in D11.
Those passages remain historical evidence for the workflow-engine amendment, not
current compatibility policy.

## Context

CD-0006 D3 commits Concord to a full workflow coordination engine with native-effect
preservation. CD-0008 commits to checkpoint-and-resume, attempt fencing, immutable
evidence binding, typed external conditions, and schema evolution. CD-0215 commits
research ownership semantics. CD-0012 commits a three-part outcome contract
and a strengthening-only delivery comparison; [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md)
governs: Review reports one disposition and Main records its acceptance, with
no separate final evaluator.

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
- name the actor identity that records a review disposition; [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) governs: review reports one disposition and Main records its acceptance;
- name the durable shape a workflow instance, definition, contract, evidence, and
  checkpoint occupy in SQLite;
- name how each workflow mutation persists; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: the store keeps current rows, so no event families or replay authority exist;
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

[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) governs: workflow definitions are generated from one canonical source, and no released definition version, hand-maintained pin, or catalog list is retained for history.

### D2. Workflow state lives on the work-item subject

[CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: workflow state lives as current rows on the work item, written in one transaction under an expected version; no event-folded projection or replay exists.

### D3. Each mutation writes current rows

[CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: the engine records each mutation as a current row in one transaction; no typed event families, payload versions, or upcasters exist.

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

Actor identity stays the existing authenticated tuple, and [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) governs: no executing-actor distinctness beyond the sequential loop's rules is required.

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
- External conditions use current condition rows (D2). No
  resolver exists in the runtime today; the engine must add one bound to the closed
  `await_type` set — `pr_merge`, `ci_result`, `timer`, `human_approval`,
  `remote_work_state` — where resolution is an attributable record carrying the
  owning authority's evidence. Evaluation happens on explicit request and at
  consequential boundaries only. No wait authority is created outside that
  resolver.
- `durable_operations` and the attempt-epoch fence remain the only step-claim
  authority; the store records each step result as a current row, not duplicated state.

### D7. Impact propagation uses typed edges and notices

The `modifies`, `depends_on` (hard/soft), and `forward_link` relations become typed
edges backed by `workflow_impact_edges`; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: relation writes are current rows in one transaction, not folded events. PM4's acyclic-governing-graph invariant applies to
`forward_link` and hard `depends_on`, using the same in-transaction recursive cycle
check the existing relation writes already perform; `modifies` is not a governing
graph and is not cycle-checked.

At completion, the engine computes the reverse-edge set and records one
impact-notice row per notice identity. CD-0006 R3 keys notices
by entity; a notice must additionally identify the dependent it informs and the
source contract state that produced it, so the identity is:

```text
(source_work_id, source_contract_version, entity_kind, entity_ref,
 target_work_id, severity)
```

Every component of that identity, plus `edge_id`, `old_hash`, and `new_hash`, is
carried on the notice row, so its unique constraint matches the identity exactly.
Recomputing the same completion produces
the same identity and therefore no duplicate row. `edge_owner_work_id` records the
dependent that declared the inbound edge. Completion notices use
`entity_kind=work_item` and `entity_ref=<completed source work>`; they do not depend
on a spec mandate. The completion record owns the `breaking|non-breaking` verdict.
The edge's `hard|soft` class controls blocking; its stored severity remains legacy
declaration metadata and does not classify the delivered change.

Hard `depends_on` + `breaking` blocks downstream execution at the consequential
boundary; hard `depends_on` + `non-breaking` and all soft edges warn only. End-state
revision through contract supersession reuses the same machinery and records a
`breaking` notice when an active `hard depends_on` was on the prior contract and a
`non-breaking` notice for every other inbound dependent.

### D8. Composition is forward-link only

Forward-linked successors use the closed `forward_link` relation kind; [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: the relation is a current row written in one transaction. No parent waiting, no nested child execution. A
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
4. [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) governs: verdicts are recorded through the loop's review acceptance, with no distinct-actor requirement beyond it.
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

Definitions declaring `approval_policy=consequence_v1` use the following consent contract. [CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) governs: definitions generate from one canonical source, no released-definition version or pin is retained for history, and the identities active work needs are derived at the integration boundary.

This policy is the workflow-action form of CD-0230 D5. Its approval classes are independent of execution consequence and durability. Its decision-bearing classes are product or preference decisions and UI/UX changes; its destructive class is D5's destructive action on Concord-owned state. Destructive or irreversible effects require exact operator approval, but that approval is necessary, not sufficient. CD-0230 D5 requires a verified recovery route before a destructive action runs and requires the agent to stop without one. This paragraph neither establishes that route nor waives that stop. Routine actions never request operator approval. Law changes follow CD-0230 D4 separately, and D5's listed gaps retain approval until their holding records are amended.

Only `approve_contract` and `confirm_premise` are decision-bearing workflow actions. They require operator approval exactly when the contract's recorded `decision_domains` is nonempty. Engineering work still records a valid contract and premise confirmation without a host consent prompt.

`decision_domains` is an explicit, sorted, unique array containing zero to three members from `preference`, `product`, and `ui_ux`. `product` and `preference` represent CD-0230 D5's product-or-preference class; `ui_ux` represents its UI/UX class. An empty array means engineering; absence is not engineering. Mixed work records every applicable member. No title, tag, kind, path, premise text, or `changes_product_truth` flag determines this field.

An authenticated non-worker coordinator declares the field with the contract. Once committed, only attributed contract supersession changes it, with expected predecessor versions, a reason, and audit evidence. A successor invalidates previous premise confirmation. A downgrade does not rewrite prior human authority; a decision-bearing successor requires human premise confirmation before completion.

For this policy, `supersede_contract`, `request_correction`, `record_verdict`, `complete`, failed-worker retry dispatch, and routine acceptance records require no consent. Removed approvals do not remove evidence, version, source, scope, current-step, or recovery checks. Supersession grants no legislative authority or automatic overlap exemption.

[CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) governs: Review inspects the accumulated solution and reports one disposition that Main records; workers gain no completion authority.

An engineering premise confirmation records the actual coordinator actor after investigation and acceptance deliverables pass. Decision-bearing confirmation retains the exact human question, choice, digest, and verified operator identity. For `ui_ux`, human premise confirmation records the operator's review and acceptance of the UI/UX change at the current contract version; contract approval alone does not satisfy that acceptance. Completion requires confirmation at the current contract version, with authority appropriate to that contract. D5 and D10's universal operator assertions are qualified by this paragraph for this policy only.

The shared admission derivation owns class-dependent questions and next actions. Every removed consent gate retains an admitted, evidence-backed exit and terminal reachability. Review is a real prerequisite, not an assumed verdict. No hold loop or fabricated human actor satisfies liveness.

At the negotiated operation surface, removal, cancellation, outside repair and both reconciliation modes, physical research-pack deletion, authority-expanding Product or client-policy changes, and explicit cross-Product mutations retain their operator approvals. Together with `merged_into` and `supersedes`, these points remain CD-0230 D5 gaps pending classification; this policy neither classifies nor reclassifies them. Governing-requirement scope cuts retain approval as product decisions. Destructive worktree destruction or forced occupancy release retains exact approval and remains subject to D5's verified-recovery precondition and stop. Explicit cross-Product mutations retain their capability checks. No approval grants otherwise absent authorization.

Routine overlap answers are `depends_on`, `blocks`, and `compatible_with`, with both work versions and judged contract content pinned. `merged_into` and `supersedes` require approval because they abandon or replace a work identity. Ordinary acceptance-backed completion is not abandonment.

Same-authorized-Product membership replacement, ordinary lifecycle completion, attributed delivery correction, and local canonical-note publication or proof-backed reconciliation are routine. Their existing state, evidence, exact-target, content, home, and durable-intent checks remain required. Clean, merged or unstarted worktree cleanup is routine only under its existing recoverability and occupancy proofs.

Physical research-pack retirement retains exact operator approval for non-dry-run deletion as a listed gap, without a new class assignment. Eligibility and dry-run inspection grant no deletion authority. Host-controlled destructive commands, deployment, release, credential disclosure, and force-push do not become routine through workflow acceptance. Approval never waives CD-0230 D5's verified-recovery precondition or its stop for a destructive action without one.

The typed approval summary shows decision domains, work identity, and proposed or active contract version on decision-bearing challenges. These facts bind to the canonical request and consumption checks. The host renders core facts; neither caller prose nor adapter inference supplies authority.

New live-family definition versions declare this policy and require explicit classification. Retired families remain historical and gain no new authoring route. Changed closed contracts follow the major surface and migration rules; [CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) governs: definitions generate from one canonical source, and no released-definition version or pin is retained for history.

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
5. durable-operation and in-flight-work continuity across the version change,
   bound to current state (CD-0242 D2); and
6. explicit operator acceptance.

Regenerated Go and TypeScript artifacts, updated envelope fixtures, signed version
negotiation against the new supported range, and the bounded deprecation window
follow from those requirements. No alias, shim, or silent dual-acceptance is
introduced.

### D12. Staleness is structurally evaluated at consequential boundaries

[CD-0242 D7](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it) governs: a record is stale exactly when a revision, candidate, or input digest it depended on has changed, checked at the stage's admission. No polling, no timer daemon, no heuristic authority (CD-0008 D5).

### D13. Recovery restores a verified snapshot

[CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: recovery uses verified snapshots with tested restore and reconciles current rows with Git, worktrees, artifacts, and external effects; no replay or rebuild exists.

### D14. The seven families each bind to a concrete predicate form

[CD-0242 D14](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d14-generated-contracts-derive-from-one-source) governs: the families' predicate contract is generated from the one canonical source; adding a family or a default predicate form requires an accepted decision.

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
7. Candidate-set revision records current rows in one transaction and leaves
   premise + end-state byte-identical.
8. Premise revision is represented as supersession only.
9. An execution-time write to the approved required end-state is refused.
10. Mid-execution discovery outside the approved end-state forward-links as successor
    work and does not close the current item.
11. End-state supersession re-enters planning and records an audit shape matching
    `specs-as-laws.md` §6.
12. The executing agent cannot author, replace, or disable its own end-state check.
13. Review's corrective edits are accepted without further review, and Main's
    recorded acceptance of the review disposition, not a distinct verdict actor,
    completes the item.
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

35. A resumed session reads current workflow state from the store with no
    historical record (CD-0242 D2, D11); recovery restores a verified snapshot
    and reconciles current rows.
36. A verified snapshot restore yields the recorded current workflow state;
    the core holds no point-in-time reconstruction (CD-0242 D2).

### Workflow_action surface

37. `WorkflowActionAvailable()` returns `false` until the engine registers and
    verifies a definition's digest; the operation is refused structurally
    otherwise.
38. Each action validates its payload against the registered `available_actions`,
    the actor tuple, and the current step.
39. TS7 envelopes emit `error` with the closed recovery_action set on refused
    actions; no fallback path exists.

### Staleness

40. A stage record is stale at its admission exactly when a revision, candidate,
    or input digest it depended on has changed; a record whose depended-on
    digests are unchanged is not stale.
41. Staleness is evaluated only at the consequential boundary's admission; no
    polling, timer daemon, or heuristic re-scan exists (CD-0008 D5).

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

- `.concord/docs/workflow-engine-contract.md` — implementation contract binding this
  decision to concrete Go/storage/agent/CLI behavior;
- `contracts/workflow-definition.schema.json` — closed definition schema for
  generated manifests;
- `contracts/workflow-outcome.schema.json` — closed outcome payload schema;
- `.concord/scenarios/workflow-engine.v1.json` — the conformance scenario carriers listed
  above;
- typed Go built-ins for each of the seven families;
- generated manifest extensions for `workflow_action`.

## Consequences

### Positive

- The workflow engine gains one state authority (current rows and finite
  snapshots), one definition authority (one generated source), one predicate
  authority (closed union), and one
  evaluator authority (authenticated tuple) — no second authority is introduced.
- CD-0012 deferred items 1–5 collapse to design choices this decision makes
  concrete; the remaining open items (cross-item outcome attribution, downstream
  notice class for end-state revisions) become behavior of the impact-propagation
  machinery.
- The seven required families become first-class workflow definitions generated
  from Go, exposed through the reserved `workflow_action` operation, and backed by
  the existing `durable_operations` claim/complete fence.
- [CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history) governs: recovery uses verified snapshots with tested restore; workflow state lives only as current rows.

### Cost

- [CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot) governs: the cutover is one documented migration with a verified snapshot; no projection or event-family scaffolding ships.
- The workflow-action dispatcher reuses agent authority; an upgrade to
  `concord_work_transition.workflow_action` is required under TS8 once the engine
  ships.
- Definition manifests become durable artifacts; drift detection between the Go
  registry and the registered definition digest must run at every
  `WorkflowActionAvailable` call.

### What this forecloses

- A separate workflow DSL is rejected.
- Workflow state outside current rows is rejected (CD-0242 D2).
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
- CD-0008 D2, D4, D5, D6 by fixing workflow checkpoint and state recording,
  evidence binding, external conditions, and recovery;
- CD-0012 by collapsing deferred items 1–4 into concrete predicate, actor, and
  state-recording choices;
- CD-0005 by keeping `workflow_action` reserved until the engine ships.

It does **not** weaken any accepted decision. A revised definition form, predicate
kind, or actor tuple requires an accepted CD-NNNN.

## Relationship to existing artifacts

### CD-0143 amendment: verdict correction remains engine-owned

The engine admits `request_correction` after a completed and accepted worker
result leaves a current unhealthy verification verdict. A dispatch alone does
not satisfy this boundary. The action requires a diagnosis, strategy, affected
predicate IDs, bound evidence, and exact operator approval. It holds
the current checkpoint, preserves the recorded verdict and results, and returns
the pinned instance to its declared external-effect step. It does not return to
`refine`, rewrite the contract, or alter a definition's generated source.
[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget)
governs: no attempt count, correction count, or correction limit exists, and
each correction launch is admitted on its own scope and safety checks.
Completion
stays fail-closed until the latest approved predicates are healthy.

| Artifact | Change |
|---|---|
| `.concord/docs/decisions/CD-0006-concord-root-product-policy.md` | Extends D3, D4, D5, D10 with engine-shaped definitions, rigor, and spec mandate enforcement. |
| `.concord/docs/decisions/CD-0008-concord-mechanism-hardening.md` | Extends D2, D4, D5, D6 with workflow evidence, checkpoints, conditions, and recovery. |
| `.concord/docs/decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md` | Collapses deferred items 1–4; preserves the contract, Main-recorded review verdicts, and strengthening-only comparison. |
| `.concord/docs/decisions/CD-0005-concord-agent-tool-surface.md` | Keeps `workflow_action` reserved; promotes `WorkflowActionAvailable` to registered-engine check. |
| `.concord/docs/workflows.md` | Resolves the seven-family shape with a single machine contract. |
| `.concord/docs/architecture-spike.md` | Anchors the architecture-spike family to `outcome` predicate and the decision record schema. |
| `.concord/docs/priorities.md` | New decision-tracker row; no priority reordering. |
| `.concord/docs/rollout-plan.md` | New entry condition: workflow engine shipped before any non-trivial advancement through `workflow_action`. |
| `.concord/docs/README.md` | Companion-table row; status prose adjusted. |
