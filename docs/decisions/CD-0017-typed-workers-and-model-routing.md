# CD-0017: Typed Workers and Model Routing

**Status:** Accepted 2026-08-11; amended 2026-08-11 (fallback resolution semantics: D2/D3/D5/D8 clarified, D9 routing-policy record added, Invariant 7 replaced — operator-accepted); amended 2026-08-15 (issue #106: undeclared-role terminal evidence; `resolved ≠ readback` scope restated)
**Type:** Architecture decision (spike outcome)
**Spike:** [`../research/R6-typed-workers-and-model-routing.md`](../research/R6-typed-workers-and-model-routing.md)
**Issue:** [#57](https://github.com/Sharper-Flow/concord/issues/57)
**Amends:** CD-0005 (adds the worker-lane dimension above the D6 adapter boundary; D1–D9 unchanged)
**Preserves:** R1 (forward-linked composition), CD-0013 (attempt model, §D5 executing-identity distinctness), CD-0016 (pinned continuity projections)
**Cites:** #42/CD-0016 (context continuity — separate decision; lane dispatch consumes its pinned projections)

## Context

Concord has accepted workflow, tool-surface, and context-continuity law, but no law
governs *who executes a lane of work or on which model*. Issue #57 requires every
sub-agent used by future Concord runtime orchestration to be a typed, versioned
Concord-owned definition, because predecessor experience showed generic host agents
drift on policy, authority, evidence, and result shape precisely at the delegation
boundary.

R6 traced the accepted law, compared three ownership options, and established
OpenCode host feasibility from product documentation. It also separated two concerns
the predecessor bundled: the **worker contract** (lane identity, packet/report
schemas, budgets, evidence obligations, authority boundary) — ordinary Product law —
and the **worker process** (spawn, model resolution, fallback) — host capability the
host already provides and observes better than Concord can.

Operational history on the host confirms the split: multi-provider fallback chains
are routine production behavior, and a past failover-observability gap had to be
closed with durable logging before a rollback trigger was actionable. Fallback that
is not recorded is indistinguishable from silent substitution.

## Decision

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

The OpenCode adapter resolves each lane to a concrete agent definition and loads the
host routing policy before dispatch through documented host mechanisms (agent
definitions, `opencode run --agent --model`, or SDK session creation). The adapter
validates **envelopes and identity only**; all domain semantics remain in the Go
core per CD-0005 D6. Host configuration chooses the preferred model and ordered
fallback chain for the lane's capability class. The adapter verifies model
availability, records the selected resolution, and confirms executing identity by
readback.

### D3. Model routing binds capability classes, not vendor identifiers

A lane contract names a **capability class** with context-window and cost ceilings.
Host configuration resolves the class to a concrete preferred model plus an ordered
fallback chain (for example through the operator's model-routing plugin). Provider
releases then change host configuration, never durable law.

Preferred-model unavailability, rate limiting, budget exhaustion, or provider
failure produces one of two typed outcomes on the owning workflow attempt: a
**recorded fallback resolution** (a declared fallback model resolved by host
configuration per D9 and confirmed by readback — a legal outcome), or a **blocked**
outcome (the declared resolution set is exhausted — a typed failure). The
`resolved_model` recorded at dispatch is whichever member of the declared
resolution set the host resolved to. Silent substitution — a model that ran but is
neither declared in the resolution set nor matched by readback — is the prohibited
defect, and whatever model actually ran must appear in evidence per D5.

### D4. Worker authority boundary

A worker run is a **bounded execution attempt of one workflow step** — the position
`workflow.action_started`/`workflow.action_checkpointed` already model. Workers
never record step transitions, verdicts, or completion, and never spawn nested
workflow authority. Durable workflow authority remains with the owning Concord
workflow; a worker result can complete a gate only when the owning workflow
transition explicitly permits that typed effect. The owning actor uses the declared
`accept_worker_result` action, bound to the exact completed attempt and its current
step epoch. The workflow fold rechecks successful model readback and requires the
owning actor to differ from the recorded executor before advancing. Workers cannot
invoke another advancing action once a worker attempt is dispatched in the current
step window. This preserves R1 and CD-0013 by construction: the prohibition binds
authority, not labor.

### D5. Dispatch and result evidence identity

Every worker attempt records durably:

- requested lane identifier, version, and contract digest;
- requested capability class, routing-policy version, and routing-policy digest;
- resolved provider/model at dispatch, its **resolution role**
  (`preferred` | `fallback`), and — when the role is fallback — a typed
  **fallback reason**;
- **readback executing-model identity** read from the host after the run;
- packet and report schema versions.

The typed dispatch failure is `resolved ≠ readback`, or `resolved ∉` the declared
resolution set. A fallback event recorded through readback — `resolved` is a
declared fallback member and `readback == resolved` — is legal evidence; an
unrecorded model change is a defect.

*(Amended 2026-08-15, issue #106 — two clarifications the mechanism can now
back.)*

*First: an executing model outside the declared resolution set, and an
exhausted resolution chain where no model ran, are now recordable as terminal
evidence. The dispatch payload's resolution role gains `undeclared`, legal
only together with a forced terminal failure: `model_identity_mismatch` with
the undeclared model recorded exactly as read back, or
`routing_policy_exhausted` with no model. Such attempts are born `failed`,
can never bind a completion, and exist so the prohibited outcome D5 names is
durable evidence rather than an adapter-level rejection that is then
discarded.*

*Second: what `resolved ≠ readback` actually detects. Against the current
host's single-shot `opencode run`, resolution and execution arrive as one
observation — a fallback presents as a new assistant message carrying the
fallback model, with the typed reason on a separate status action — so
`resolved` and `readback` derive from the same reading and the comparison
guards **attempt binding across the two CLI invocations** (crossed attempt
ids, replay, adapter defects), not host-side substitution. A host that
silently substitutes the model after dispatch is not detectable by this
clause on the single-shot path. If a session-attached dispatch
(`--attach`) ever makes resolution and execution separately observable,
this clause should be revisited; until then no reader may infer a
substitution guarantee.*

`worker.completed` and `worker.failed` bind to the dispatched attempt's exact work
item and make one transition from `dispatched`. Both terminal states are immutable;
a later terminal event cannot replace failure with success or success with failure.

### D6. Reviewer/model distinctness is structurally available and workflow-declared

CD-0013 §D5 evaluates evaluator distinctness against the *executing* identity. This
decision extends that principle one dimension: where a workflow declares independent
evaluation, implementation and review resolving to the **same readback model
identity** is a structural rejection. The check evaluates actual (readback)
identities so fallback-induced collisions are caught.

Distinctness is available to every workflow and declared per workflow; it is not
globally mandatory. R6 §5 records the measurement protocol (same-model vs
cross-model review on seeded-defect synthetic scenarios) whose result decides any
mandatory scope.

### D7. Behavioral evaluation boundary

Lane prompt behavior is evaluated with a prompt-evaluation harness (promptfoo or
equivalent) once lane prompts exist. Deterministic authority — registry validation,
packet/report schemas, dispatch fencing, evidence recording, distinctness rejection —
remains in Go tests. Behavioral evals never complete gates and never substitute for
schema or state checks.

### D8. Relationship to host model-routing infrastructure

The operator's existing model-routing TUI/plugin (primary-plus-ordered-fallback per
agent) is the **accepted host mechanism** for D3 fallback chains. Concord lane
definitions become routing targets of that infrastructure; Concord does not
re-implement provider fallback, and the infrastructure does not learn Concord law.
The host mechanism provides **recovery** (preferred → ordered fallback), not merely
evidence. The legal resolution set — the members a host chain may resolve to for a
capability class without producing silent substitution — is declared in the
Concord-owned routing-policy record (D9), kept consistent with the host chain. The
host owns runtime fallback mechanics; Concord owns the evidence boundary that
decides which resolutions are legal to record. The boundary is the same one
capability-placement.md draws: provider mechanics are orchestrated, Product truth
is owned.

### D9. Routing-policy record

A canonical routing-policy record lives in `contracts/`, generated and
digest-pinned under the same regime as the lane registry (D1). Each entry binds a
capability class to an ordered resolution set `[preferred, fallback…]` where
`preferred` is the first model in the host-loaded policy for the lane's capability
class. A dispatch pins
`(capability_class, routing_policy_version, routing_policy_digest)`, and
`resolved_model` must be a member of the declared set. Provider releases change the
routing-policy record and host configuration, not the lane registry (D3).

## Invariants

1. Every orchestration dispatch structurally references one registered lane
   type/version/digest.
2. Generic host agents (for example `general`, `explore`) are rejected for Concord
   lane dispatch unless executing under a registered lane contract producing its
   typed report.
3. Unknown type/version/digest fails closed before authority changes.
4. Worker runs never record workflow step transitions, verdicts, or completion.
5. Every lane definition declares a capability class; model resolution is owned by
   the validated host routing policy, not by the lane registry.
6. Readback executing-model identity appears in every worker attempt's evidence.
7. A fallback produces a typed outcome — preferred resolution, recorded fallback
   resolution (`resolved ∈` declared set, `readback == resolved`), or blocked —
   with a recorded actual model. Silent substitution (`resolved ∉` declared set,
   or `readback ≠ resolved`) is a defect.
8. Lane contracts and schemas evolve only through the accepted versioning mechanism.
9. No Concord self-hosting claim occurs before this decision is implemented and
   included in replacement-readiness evidence.

## Consequences

### Positive

- Delegation-boundary drift (#57's motivation) is closed structurally: contract,
  packet, result, and evidence stay inside Concord law while labor is delegated and
  verified by readback.
- Best-fit model routing per lane without vendor rot in durable law.
- R1/CD-0013 preserved: no nested workflow authority is created.
- CD-0016's clean-restart path is unblocked: a typed registry exists for restart
  injection to target.
- Host fallback investment is reused, not duplicated.

### Cost

- A canonical lane manifest joins the generation regime; every lane change is a
  versioned contract change.
- The adapter gains dispatch/readback mechanics while staying envelope-thin; the
  boundary must be defended in review.
- Reviewer distinctness adds a scheduling constraint: a distinct model must be
  available, or the attempt blocks with a typed outcome.

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

Per issue #57 and its accepted additions:

- Canonical lane registry with generated schemas and validators; deterministic
  rejection tests for unknown type/version/digest and for generic-agent dispatch.
- Dispatch/readback evidence recorded per attempt; resolved-vs-readback mismatch is
  a typed failure.
- Preferred-model unavailability produces a typed fallback/blocked outcome.
- Reviewer/model distinctness rejection is deterministic where declared.
- Research, implementation, and review lanes complete an end-to-end synthetic
  workflow with typed evidence.
- Worker execution demonstrably creates no nested workflow authority and weakens no
  CD-0013 completion rule.
- Lane prompt behavioral evals exist; deterministic authority remains in Go tests.

## Supersession

CD-0017 does not supersede CD-0005, CD-0013, or CD-0016; it amends CD-0005's scope
by adding the worker-lane dimension above the adapter boundary. Changes to D1–D8
follow the accepted decision-supersession path and record explicit operator
acceptance.
