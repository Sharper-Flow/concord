# CD-0090: A per-turn runtime continuity hook re-pins orchestrator context

- **Status:** Accepted under operator approval.
- **Approval date:** 2026-08-30.
- **Approval:** Operator-approved ADV change `addOrchestratorContinuityHook`
  (proposal, agreement, design, and task graph approved in sequence; design
  validated by independent researcher report
  `addOrchestratorContinuityHook|change:researcher:orchestrator-continuity|adv-researcher|1`).
- **Scope:** Orchestrator prompt continuity; adapter plugin surface;
  `ContinuitySnapshot` payload; agent-tool surface version.
- **Related:** CD-0016, CD-0031, CD-0034, CD-0043, CD-0072, CD-0084.
- **Supersedes:** nothing. Amends the recorded consequences of CD-0016
  (§Consequences, "does not add a generic host hook") and CD-0031
  (§Consequences, "OpenCode host hooks and plugins are unnecessary").

## Context

CD-0016 requires that workflow position, authority, and evidence never travel
by summary. Three moments matter:

| Moment | Mechanism | State before this decision |
|---|---|---|
| Session boot | CD-0031 session-boot packet | Covered |
| Any `concord_*` call | CD-0016 pinned projection, re-derived per call | Covered |
| Mid-session with no Concord call | none | Uncovered |

The uncovered window is reached in practice. Host compaction fires mid-session
during stretches of file editing and shell work with no `concord_*` call. After
it does, the boot packet sits behind the compaction boundary and nothing
re-derives the projection, so workflow position is carried by the summary alone
— the condition CD-0016 forbids.

The predecessor enforced this half with per-turn prompt injection. Its design
bundled the injection with authority-bearing workflow state. Concord keeps the
injection and refuses the bundled authority.

## Decision

### D1. Runtime hooks and authority hooks are different classes

A hook that **decides** — approving a contract, gating a transition, validating
a payload — is authority and stays in the core, reached by routing. A hook that
**re-asserts** state the core already derived is runtime and may live in the
adapter plugin, because the states it governs (prompt bytes, cache prefix,
turn boundaries) never route to the core at all.

CD-0072's conclusion that no Concord surface could hold an interception hook
was written before the adapter shipped a plugin entry module. That premise no
longer holds; CD-0072's actual exclusion (raw file-write trunk interception)
remains unchanged and is not reopened here.

### D2. One bounded re-pin surface is added

The adapter plugin entry (`concord-plugin.ts`) registers
`experimental.chat.system.transform`. Each turn it:

1. resolves the session's work from the launcher identity environment;
2. spawns the internal read-only `continuity-block` verb, which runs
   `ReadWorkflowContinuity` and renders through `sessionboot.Build` — the same
   render path as session boot, so boot and re-pin bytes agree;
3. adds one sentinel-delimited block to `system[0]`, then replaces that block
   in place on later state changes; it never duplicates the block;
4. emits nothing and never throws on failure, empty work, or absent identity.

The render path is pinned to `sessionboot.Build`. The envelope render stamps
wall-clock `ObservedAt` and is nondeterministic; using it would break
byte-stability. Spawns are gated by a bounded time window and keyed by identity.

### D3. Byte-stability is the cache-safety mechanism

Unchanged snapshot → unchanged bytes → no prompt-prefix invalidation. A state
change invalidates the prefix once per change, bounded by state-change
frequency, not turn frequency. Injection that varied per turn inside the cached
prefix would re-bill the prefix as cache creation on every call and is
forbidden.

### D4. `StepActions` is state projection, not methodology

`ContinuitySnapshot` gains `step_actions` — the current step's `actions[]` from
the verified workflow definition. This is the same class of content as the boot
packet's workflow-step line: durable-state projection. CD-0043 D2 provenance
binding applies only if methodology prose is ever injected; none is.

### D5. Agent-tool surface version 2.4.0

The `step_actions` addition is recorded as surface version 2.4.0. No
machine-read version field exists; the `ManifestDigest` remains the machine
identity for skew checks. This record is the durable prose location of the
version number.

## Invariants

1. The adapter plugin hook holds no authority: no approval, gating, or payload
   validation. It transports bytes the core derived.
2. The re-pin render path is `sessionboot.Build`, never the envelope render.
3. Injection is byte-stable across turns while the snapshot is unchanged.
4. Failure adds no block and never throws; existing system bytes stay unchanged.
5. `concord.ts` stays transport-only (TS6); the hook lives in the plugin entry.

## Verification

- Verb determinism: two invocations from one snapshot produce identical bytes
  (`cmd/concord` tests).
- Step join: `StepActions` equals the current step's `actions[]` for a fixture
  definition; empty when no workflow is in flight (`internal/store` tests).
- Hook: determinism, sentinel-replacement-not-append, failure no-op, spawn-window gate
  (adapter tests).
- Cache safety mechanism: adapter tests prove byte-stability for unchanged
  state and sentinel replacement for changed state. Live cache-meter
  measurement is deferred until after deployment and is not claimed here.
  Procedure: run one orchestrator session for at least five turns without a
  state change, then run `cache-stats summary`; pass when `cache_creation`
  equals the baseline after turn one.
- Scenario corpus: post-boundary re-pin entry under `scenarios/`.
