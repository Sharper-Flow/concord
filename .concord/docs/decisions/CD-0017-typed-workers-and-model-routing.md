# CD-0017: Typed workers and model evidence

**Status:** Accepted 2026-08-11.
**Type:** Architecture decision (spike outcome)
**Spike:** [`../research/R6-typed-workers-and-model-routing.md`](https://github.com/Sharper-Flow/concord/blob/a98dfe5db3fa2af53bd2d82829aec840e94e64c4/.concord/docs/research/R6-typed-workers-and-model-routing.md)
**Issue:** [#57](https://github.com/Sharper-Flow/concord/issues/57)
**Amends:** CD-0005 (adds the worker-lane dimension above the D6 adapter boundary; D1–D9 unchanged)
**Preserves:** R1 (forward-linked composition), CD-0013 (attempt model, §D5 actor identity), CD-0016 (pinned continuity projections)
**Cites:** #42/CD-0016 (context continuity — separate decision; lane dispatch consumes its pinned projections)
**Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D5, D6

## Context

Issue #57 requires every sub-agent used by Concord runtime orchestration to be
a typed, versioned Concord-owned definition. Generic host agents can drift on
policy, authority, evidence, and result shape at the delegation boundary.

R6 separates the **worker contract** (lane identity, packet/report schemas,
budgets, evidence obligations, authority boundary) from the **worker process**
(spawn and model resolution). The contract is Product law. The host owns the
process, and Concord records the executing identity through readback.

## Decision

For `approval_policy=consequence_v1`, CD-0013 D11 and CD-0059 D2 replace failed-retry consent with exact transactional attempt, epoch, contract, and job binding. The fresh attempt, capability, signed evidence, and worker boundary requirements remain; [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) and [D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget) govern the loop and each launch, so no model-distinctness, convergence, or chained approval requirement is retained.


### D1. Concord owns a canonical lane registry

Every worker lane used by Concord runtime orchestration is a closed, typed, versioned
Concord-owned definition: lane identifier, version, purpose, capability set, input
packet schema, output/report schema, budgets (cost, context, time), evidence
obligations, and lifecycle states. The registry lives in `contracts/` as a
language-neutral manifest that generates Go, TypeScript, JSON schemas, tests, and
docs, and evolves through the accepted TS8 mechanism. No permanent or simultaneous
aliases.

Unknown lane type, version, or digest **fails closed before work begins**.

### D2. Host owns process and model dispatch

The OpenCode adapter dispatches each registered lane through documented host
mechanisms. It selects no model and omits `--model`. OpenCode resolves the
executing model from host configuration. The adapter validates envelopes and
identity only; domain semantics remain in the Go core under CD-0005 D6.

Lane definitions are model-neutral. Capability classes describe lane intent;
they do not select a model or define a legal resolution set. Every attempt
records the actual executing-model identity reported by the host under D5.

### D3. Capability classes describe lane intent

A lane contract names a **capability class** with context-window and cost
ceilings. The class is a model-neutral label, not a runtime routing input.
Provider releases change host configuration, never durable lane law.

### D4. Worker authority boundary

A worker run is a **bounded execution attempt of one workflow step** — the position
`workflow.action_started`/`workflow.action_checkpointed` already model. Workers
never record step transitions, verdicts, or completion, and never spawn nested
workflow authority. Durable workflow authority remains with the owning Concord
workflow; a worker result can complete a gate only when the owning workflow
transition explicitly permits that typed effect. The owning actor uses the declared
`accept_worker_result` action, bound to the exact completed attempt and its current
step epoch. Acceptance rechecks successful model readback before advancing. [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) governs: the owning workflow advances through Main's recorded review acceptance, with no owner-versus-executor distinctness beyond the loop. Workers cannot
invoke another advancing action once a worker attempt is dispatched in the current
step window. This preserves R1 and CD-0013 by construction: the prohibition binds
authority, not labor.

### D5. Dispatch and result evidence identity

Every worker attempt records durably:

- requested lane identifier, version, and contract digest;
- executing-model identity read from the host after the run;
- packet and report schema versions.

Concord records which model executed the attempt. It asserts no intended
model and no legal resolution set, so it claims no substitution detection.

A readback that yields no executing-model identity, or more than one, is not
an adapter error to discard. The adapter records exactly one failed attempt in
one event: a dispatch born `failed`, with an empty `readback_model` and a kind
of `model_readback_missing` or `model_readback_ambiguous`, carrying the
refusing predicate, the export digest, and the export byte count. One event is
one transaction, so no `dispatched` attempt can outlive a stopped process. The
schema admits an empty `readback_model` only under those two failed kinds. The
attempt is never retried; a new attempt is a new decision.

The dispatch event also records `worker_worktree_identity`, the sha256 of the
canonical session worktree path the core admitted under CD-0102 D7. Later
worker evidence binds to that claim without a machine path in a public event.

[CD-0242 D6](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d6-attempts-carry-their-own-authority-not-a-budget) governs: a retry is one newly admitted launch with its own authorization and subject bindings; no attempt count, chained approval prerequisite, or resume of the failed attempt exists.

`worker.completed` and `worker.failed` bind to the dispatched attempt's exact work
item and make one transition from `dispatched`. Both terminal states are immutable;
a later terminal event cannot replace failure with success or success with failure.

### D6. Reviewer/model distinctness is structurally available and workflow-declared

[CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) governs: Main's dispatch of Review carries the loop's distinctness, and no model-identity distinctness requirement exists beyond it.

### D7. Behavioral evaluation boundary

Lane prompt behavior is evaluated with a prompt-evaluation harness (promptfoo or
equivalent) once lane prompts exist. Deterministic authority — registry validation,
packet/report schemas, dispatch fencing, evidence recording, distinctness rejection —
remains in Go tests. Behavioral evals never complete gates and never substitute for
schema or state checks.

## Invariants

1. Every orchestration dispatch structurally references one registered lane
   type/version/digest.
2. Generic host agents (for example `general`, `explore`) are rejected for Concord
   lane dispatch unless executing under a registered lane contract producing its
   typed report.
3. Unknown type/version/digest fails closed before authority changes.
4. Worker runs never record workflow step transitions, verdicts, or completion.
5. Every lane definition declares a model-neutral capability class, not a model pin.
6. Readback executing-model identity appears in every worker attempt's evidence.
7. Missing or ambiguous executing-model readback produces one attempt born `failed`
   with its typed failure and no executing-model value.
8. Lane contracts and schemas evolve only through the accepted versioning mechanism.
9. No Concord self-hosting claim occurs before this decision is implemented and
   included in replacement-readiness evidence.

## Consequences

### Positive

- Delegation-boundary drift (#57's motivation) is closed structurally: contract,
  packet, result, and evidence stay inside Concord law while labor is delegated and
  verified by readback.
- R1/CD-0013 preserved: no nested workflow authority is created.
- CD-0016's clean-restart path is unblocked: a typed registry exists for restart
  injection to target.

### Cost

- A canonical lane manifest joins the generation regime; every lane change is a
  versioned contract change.
- The adapter gains dispatch/readback mechanics while staying envelope-thin; the
  boundary must be defended in review.
- Review is Main's dispatched loop stage: [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) governs, so no distinct-model availability constraint or typed blocking outcome exists.

## Rejected alternatives

- **Concord-owned worker execution** (Option A): duplicates host model inventory,
  authentication, and fallback; drifts the registry toward nested authority.
- **External sessions lease phases** (Option B): leaves packet, prompt, model, and
  report contract outside the evidence boundary; declines the issue.
- **Vendor/model identifiers in lane contracts:** rot every provider release;
  capability classes carry the durable intent.
- **Globally mandatory cross-model review:** awaits the R6 §5 measured basis;
  structurally available and workflow-declared until then.
- **Adapter-side result validation beyond envelope/identity:** CD-0005 D6 violation.

## Implementation acceptance

- Canonical lane registry with generated schemas and validators; deterministic
  rejection tests for unknown type/version/digest and for generic-agent dispatch.
- Actual executing-model readback recorded per attempt; missing or ambiguous
  readback produces a typed failed attempt under D5.
- No reviewer/model distinctness rejection is required: [CD-0242 D5](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) governs, and the implementing change instead proves Review's disposition and Main's acceptance record as current rows.
- Research, implementation, and review lanes complete an end-to-end synthetic
  workflow with typed evidence.
- Worker execution demonstrably creates no nested workflow authority and weakens no
  CD-0013 completion rule.
- Lane prompt behavioral evals exist; deterministic authority remains in Go tests.

## Supersession

CD-0017 does not supersede CD-0005, CD-0013, or CD-0016. It adds the worker-lane
dimension above CD-0005's adapter boundary. A compatible amendment keeps this ID
and publishes a new content hash under CD-0036 D2. A breaking replacement uses
an accepted new law ID and the CD-0036 cutover. The operator approves the law delta.
