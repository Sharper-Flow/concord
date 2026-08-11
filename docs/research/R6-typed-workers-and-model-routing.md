# R6: Typed Workers and Model Routing — Research Findings

> **Status:** Research complete; awaiting decision record and operator acceptance.
> **Question:** How should Concord execute and model-route typed worker lanes —
> research, implementation, review, verification, ops — without recreating nested
> workflow authority or delegating to unregistered generic host agents?
> **Issue:** [#57](https://github.com/Sharper-Flow/concord/issues/57).
> **Date:** 2026-08-11.

## Summary

Issue #57 requires every sub-agent used by future Concord runtime orchestration to be
a typed, versioned Concord-owned definition, and asks where worker execution and model
routing live. This research traced the accepted law, compared three ownership options,
and established OpenCode host feasibility from product documentation and source.

Three findings carry the decision:

1. **The two concerns are separable, and only one is scarce.** A worker *contract*
   (lane identity, packet schema, report schema, budgets, evidence obligations,
   authority boundary) is ordinary Concord law — the same shape as the workflow,
   tool-surface, and continuity contracts already accepted. A worker *process* (spawn,
   model selection, session lifecycle) is host capability that OpenCode already
   provides with documented mechanisms. Concord should own the first and delegate the
   second; owning both buys no guarantee the host cannot provide, and owning neither
   forfeits the evidence boundary issue #57 exists to close.
2. **Model identity is available as a fact, not a claim.** OpenCode records the
   executing model per session and per assistant message, so a Concord adapter can
   read back which model actually ran and record it as durable evidence rather than
   trusting a routing request. This converts model routing from a configuration hope
   into a verifiable dispatch/result pair.
3. **The forward-link boundary survives.** A worker attempt is a bounded execution
   attempt inside one workflow step — the same position
   `workflow.action_started`/`workflow.action_checkpointed` already model — not a
   child workflow. R1 and CD-0013 prohibit nested child execution; they do not
   prohibit a step's effect being produced by a delegated session, provided durable
   workflow authority (step transition, verdict, completion) never moves into the
   worker. The prohibition binds authority, not labor.

Evidence labels used throughout: **(i)** documented product behavior or accepted
Concord law, **(ii)** published research or production-system practice, **(iii)**
inference or tertiary source.

---

## 1. Current-law trace

| Law | What it fixes | Consequence for workers |
|---|---|---|
| CD-0005 D1–D2 | Eight end-to-end jobs; at most nine always-visible tools | The tool surface is closed; workers are *callers* of the surface, never new tools. A worker lane does not expand D1. |
| CD-0005 D6 | One `concord.ts` adapter invokes the short-lived Go CLI; no plugin domain logic | The adapter boundary is thin transport. Worker dispatch must not put Concord domain semantics into TypeScript; the adapter may carry host mechanics (spawn, model pin) because those are host concerns, not Product truth. |
| CD-0005 D7/D8 | Strict result envelope; generated versioned manifest; fail-closed negotiation | Worker packet/report schemas belong in the same generated-contract regime (TS8 evolution), not ad-hoc markdown. |
| R1 | Forward-linked successors; no nested child execution; parent never waits on child completion | A worker must not be modeled as a child workflow. It is an attempt inside one step of the owning workflow; the workflow — not the worker — records step transitions. |
| CD-0013 §D5 | Evaluator distinctness is evaluated against the *executing* identity (`actor_ref`: principal, client, agent, session), never the authenticating principal alone | The executing-identity tuple already exists durably. Model identity extends it: the same structural argument that forbids self-review by one actor forbids same-model review where the law requires reviewer distinctness. |
| CD-0016 | Pinned durable projections over lossy summaries; boundaries never cross mid-effect | Worker handoff context at a lane boundary is exactly CD-0016's pinned checkpoint projection; lane dispatch consumes `concord_work_trace.continuity` rather than a prose summary. |
| capability-placement.md | Native ownership vs orchestration is decided per capability | Worker *contracts and evidence* are Product truth → Concord-owned. Worker *process/model execution* is host capability → adapter-orchestrated. |

**Gap confirmed.** No accepted law assigns models, defines worker lanes, requires
reviewer/model distinctness, or records executing-model identity. CD-0005 governs the
tool surface an agent calls; it is silent on which agent definition or model runs.
That silence is the gap #57 closes.

## 2. Options compared

### Option A — Concord-owned typed workers (full ownership)

Concord owns a closed worker registry, dispatch contract, *and* the execution
mechanism; the host adapter is a thin launcher.

- **Authority:** strongest single-owner guarantees. **(i)**
- **Recovery:** Concord must own session/process failure semantics it cannot observe
  (provider rate limits, context truncation, host restarts). **(iii)**
- **Cost/context:** Concord re-implements model inventory, authentication, and
  fallback that the host already maintains. Duplicated capability, duplicated
  failure surface. **(i)**
- **Failure mode:** recreates predecessor orchestration complexity; the registry
  drifts toward nested authority (the R1 violation the option was meant to respect).
- **Verdict:** rejected. Buys guarantees the host already provides; pays for them
  with a second model-routing system to keep correct forever.

### Option B — External sessions claim workflow phases

Concord stays a pure coordination layer; OpenCode sessions advertise capability and
lease phases; Concord records who executed.

- **Authority:** weakest. The packet, prompt, model, and report contract run outside
  Concord's evidence boundary; "who executed" is recorded but "under which contract"
  is not enforceable. **(iii)**
- **Recovery:** operator/session management becomes part of correctness — the exact
  generic-agent drift (#57's motivation) returns through the leasing door. **(iii)**
- **Verdict:** rejected. It preserves CD-0005 by declining to answer the issue.

### Option C — Hybrid: Concord owns lane contracts; host owns process/model dispatch

Concord defines closed lane contracts (identifier, version, purpose, capability set,
packet schema, report schema, budgets, evidence obligations, lifecycle states) and
their generated validators. The OpenCode adapter resolves each lane to a concrete
agent definition and model, performs dispatch through documented host mechanisms, and
reads back the executing model identity for evidence. Worker runs are bounded
execution attempts of one workflow step; durable workflow authority never leaves
Concord.

- **Authority:** contract, packet, result, and evidence stay inside Concord law;
  only labor is delegated, and delegated labor is verified by readback. **(i)**
- **Recovery:** session/process failures are host-observable and surface as typed
  attempt failures on the owning step — the failure semantics already exist in
  CD-0013's attempt model. **(i)**
- **Cost/context:** model inventory, authentication, rate limits, and fallback stay
  where they already live (host configuration); Concord carries a small routing
  policy, not a routing system. **(i)**
- **Failure mode:** the Concord/host boundary must be precise. If the adapter ever
  validates *results* (domain semantics in TypeScript), CD-0005 D6 is violated; the
  adapter validates *envelopes and identity only*.
- **Verdict:** recommended. It is the only option that answers #57 without opening
  R1.

## 3. OpenCode host feasibility

All mechanisms below are documented OpenCode behavior or confirmed source. **(i)**

| Need | Mechanism | Evidence |
|---|---|---|
| Launch a named agent on a pinned model | `opencode run --agent <name> --model <provider/model>`; HTTP API session create with `agent` + `model`; JS SDK `session.create` + per-prompt `model` | OpenCode docs: CLI `run` flags; SDK docs; server session routes |
| Pin a model per agent definition | `model: provider/model-id` in agent markdown frontmatter or config | OpenCode agents docs § Model |
| Prevent parent-model leakage | Documented inheritance: an unpinned subagent uses the *invoking primary's* model | OpenCode agents docs § Model; task tool source |
| Block unregistered agents at dispatch | Declarative `task` permission keyed on `subagent_type` (gate runs before child session creation); plugin `tool.execute.before` hook can throw to abort execution | task tool source permission gate; plugins docs hook + block-by-throw example |
| Read back executing model | Session/message records carry model identity; assistant messages carry `cost` and `tokens` | session processor source; CLI `stats`; v2 message-shape spec |
| Fallback on unavailable model | No documented built-in per-request fallback in OpenCode core; community plugin pattern (`fallback_models` tuple) exists and is in production use locally | insufficient core documentation; plugin pattern **(iii)** |

**Consequence for the design:** the adapter must *require* a pinned model on every
registered lane definition. The documented inheritance rule means an unpinned worker
silently runs on whatever model the operator's primary session happens to use —
invisible same-model review. Pinning is not a preference; it is the distinctness
control. **(i)**

**Fallback boundary:** because OpenCode core has no documented per-request fallback,
the routing policy must produce a **typed blocked/fallback outcome** when the
preferred model is unavailable — never a silent substitution. Whether the adapter
implements fallback itself or drives a routing plugin is an implementation choice the
CD must not fix prematurely; the *outcome shape* (requested class, resolved model or
typed refusal) is law. **(i/iii)**

## 4. Model routing policy shape

- **Bind lanes to capability classes, not vendor IDs.** A lane contract names a
  capability class (for example `review-deep`, `implement-general`,
  `verify-fast`) with context-window and cost ceilings; host configuration resolves
  the class to a concrete preferred/fallback model pair. Vendor identifiers in
  durable law would rot every provider release; capability classes in law plus
  host-resolved models keep both sides versioned independently. **(ii)** — this
  mirrors the capability-vs-implementation split already accepted for tools in
  capability-placement.md. **(i)**
- **Reviewer distinctness extends CD-0013 §D5.** Where a workflow requires an
  independent evaluator, implementation and review resolving to the same model
  identity is a structural rejection, evaluated against recorded model identity —
  the same "executing, not authenticating" principle, one dimension deeper.
  Distinctness is required only where the owning workflow declares it; it is not a
  global tax on every lane. **(i)**
- **Durable evidence per attempt:** requested capability class, resolved
  provider/model, worker-contract digest, routing-policy version, packet and report
  schema versions, and the readback model identity. A mismatch between resolved and
  readback model is a typed dispatch failure, not a warning. **(i)**

## 5. Same-model vs cross-model review — insufficient evidence

The issue's analysis asks for a measured synthetic comparison of same-model vs
cross-model implementation/review on representative Concord scenarios. **No such
measurement exists yet**, and producing one requires the lane contracts this spike
exists to authorize — a circularity the spike must not fake. Recorded as an explicit
insufficient-evidence finding with the measurement protocol that resolves it:

1. Select N ≥ 20 synthetic scenario tasks from `scenarios/` with known-defect seeds.
2. Run each twice: same-model implement+review; cross-model implement+review, holding
   the implementation model constant.
3. Score defect-detection recall and false-positive rate per reviewer verdict.
4. Adopt cross-model distinctness as required only if recall improves materially;
   otherwise record distinctness as advisory policy with the measured basis.

Until that measurement lands, the CD should make reviewer/model distinctness
**structurally available and workflow-declared** rather than universally mandatory:
the mechanism is justified by CD-0013 §D5's executing-identity principle alone, but
its *mandatory scope* deserves the measured basis. **(iii)**

## 6. Behavioral evaluation (promptfoo) applicability

Promptfoo is an open-source CLI/library for evaluating prompts and models with
declarative test cases, matrix comparison across models, CI integration, and local
execution. **(i)** — promptfoo.dev documentation.

Boundary, matching the issue's acceptance additions: promptfoo (or equivalent)
evaluates **lane prompt behavior** once Concord-owned lane prompts exist — does the
research lane produce sourced findings, does the reviewer lane return the typed
report schema. Deterministic authority — packet/result schema validation, registry
rejection of unknown type/version/digest, dispatch fencing, evidence recording —
remains in Go tests. Prompt output is a heuristic surface; schema and state are the
structural surface. Behavioral evals never complete gates. **(i/iii)**

## 7. Insufficient-evidence findings

1. Same-model vs cross-model measured benefit — §5; protocol defined, measurement
   deferred to the implementation that creates lanes.
2. OpenCode core per-request model fallback semantics — no documented behavior found;
   fallback is designed as a typed outcome regardless of which mechanism produces it.
3. Cost ceilings per lane in absolute terms — depends on operator model inventory;
   the law fixes *where ceilings live* (lane contract + host resolution), not numbers.
4. Whether reviewer distinctness should be globally mandatory — awaiting §5
   measurement; default is workflow-declared.

## 8. Recommendation and falsifiers

**Adopt Option C.** A new decision record (CD-0017) should:

- register a canonical, versioned Concord lane registry with generated packet/report
  schemas and validators, evolved through the accepted TS8 mechanism;
- fix dispatch authority: the OpenCode adapter resolves lane → agent definition →
  pinned model, dispatches through documented host mechanisms, and records readback
  model identity; unknown type/version/digest fails closed before work begins;
- fix the worker authority boundary: a worker run is a bounded execution attempt of
  one step; workers never record step transitions, verdicts, or completion;
- fix the routing policy shape: capability classes in law, concrete models in host
  configuration, typed fallback/blocked outcomes, no silent substitution;
- make reviewer/model distinctness structurally available and workflow-declared,
  with the §5 measurement protocol as the path to any mandatory scope;
- cite #42/CD-0016: lane dispatch consumes pinned continuity projections; this spike
  does not absorb context continuity.

**Falsifiers that would reopen this direction:**

| Signal | Response |
|---|---|
| OpenCode removes or breaks agent/model pinning or model-identity readback | Re-evaluate host boundary; consider adapter-owned spawn or a second host |
| Measured §5 protocol shows cross-model review provides no material benefit | Narrow distinctness to advisory; simplify registry |
| Lane contracts drift toward nested authority (workers recording step effects) | Stop; the R1 boundary is being violated by construction, not by accident |
| Adapter accumulates domain semantics (result validation beyond envelope/identity) | CD-0005 D6 violation; refactor to keep Go the sole domain authority |
| Host fallback behavior proves unreliable in production evidence | Revisit owning fallback inside the adapter with a bounded mechanism |

## Sources

Concord law (this repository):

- `docs/decisions/CD-0005-concord-agent-tool-surface.md` — D1–D9, invariants.
- `docs/research/R1-workflow-composition.md` — forward-linked successors.
- `docs/decisions/CD-0013-workflow-engine-mechanism.md` — attempt model, §D5
  executing-identity distinctness.
- `docs/decisions/CD-0016-context-continuity.md` — pinned projections for handoff.
- `docs/capability-placement.md` — ownership-vs-orchestration split.

OpenCode documentation and source (public):

- Agents docs (frontmatter, `mode`, `model` pinning, subagent model inheritance) —
  <https://opencode.ai/docs/agents/> and
  <https://github.com/sst/opencode/blob/dev/packages/web/src/content/docs/agents.mdx>
- CLI docs (`run --agent --model`, `models`, `stats`) —
  <https://github.com/sst/opencode/blob/dev/packages/web/src/content/docs/cli.mdx>
- SDK docs (`session.create`, per-prompt model) —
  <https://github.com/sst/opencode/blob/dev/packages/web/src/content/docs/sdk.mdx>
- Plugins docs (`tool.execute.before`, block-by-throw) —
  <https://github.com/sst/opencode/blob/dev/packages/web/src/content/docs/plugins.mdx>
- Task tool permission gate (`ctx.ask` before child session) —
  <https://github.com/sst/opencode/blob/dev/packages/opencode/src/tool/task.ts>
- Session processor (`assistantMessage.cost`, `.tokens`) —
  <https://github.com/sst/opencode/blob/dev/packages/opencode/src/session/processor.ts>
- v2 message shape (`Assistant.usage`, `Turn.request.model`) —
  <https://github.com/sst/opencode/blob/dev/packages/opencode/specs/v2/message-shape.md>

Evaluation tooling:

- Promptfoo documentation — <https://promptfoo.dev/docs/intro/>;
  <https://github.com/promptfoo/promptfoo>

**Stated limitations.** OpenCode core fallback behavior was assessed from
documentation absence plus community plugin patterns, not from a core specification;
the design therefore fixes fallback *outcome shape* rather than mechanism. The
community model-fallback plugin pattern is cited as practice **(iii)**, not as
product documentation. No measured same/cross-model comparison exists yet by
construction (§5); no claim in this file depends on one.
