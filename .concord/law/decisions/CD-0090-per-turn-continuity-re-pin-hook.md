---
{
  "id": "CD-0090",
  "title": "A per-turn runtime continuity hook re-pins orchestrator context",
  "authority": {
    "tier": "legislated",
    "legislated_by": "work-8d7bd788b2890355460bb5b7",
    "contract_version": 1
  },
  "home_domain_id": "agent-adapter",
  "applies_to_domain_ids": []
}
---
# CD-0090: A per-turn runtime continuity hook re-pins orchestrator context

- **Status:** Accepted under operator approval.
- **Approval date:** 2026-08-30.
- **Approval:** Operator-approved ADV change `addOrchestratorContinuityHook`
  (proposal, agreement, design, and task graph approved in sequence; design
  validated by independent researcher report
  `addOrchestratorContinuityHook|change:researcher:orchestrator-continuity|adv-researcher|1`).
- **Scope:** Orchestrator prompt continuity; adapter plugin surface;
  `ContinuitySnapshot` payload; agent-tool surface version.
- **Related:** CD-0016, CD-0031, CD-0043, CD-0072, CD-0084, CD-0098,
  CD-0104, CD-0242.
- **Approval:** Directory-based work resolution: Concord (CON) issue CON-910
  (2026-10-13), under the Snowball coordinator's operator-delegated approval;
  managed-parent exclusion: coordinator-approved 2026-10-17.

## Context

CD-0016 requires that workflow position, authority, and evidence never travel
by summary. Three moments matter:

| Moment | Mechanism | State before this decision |
|---|---|---|
| Managed entry | CD-0031 session-boot packet through work-start preparation | Covered |
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

### D2. One bounded re-pin surface is added

The adapter plugin entry (`concord-plugin.ts`) registers
`experimental.chat.system.transform`. Each turn it:

1. adds no block for a turn without a session identity or for a session under
   a managed parent; otherwise it reads from the host the directory the
   session runs in;
2. spawns the internal read-only `continuity-block` verb with that directory.
   The verb resolves the directory through its active claimed worktree to the
   work item, runs `ReadWorkflowContinuity`, and renders through
   `sessionboot.Build` — the same render path as session boot, so boot and
   re-pin bytes agree. A directory with no active claimed worktree renders
   nothing;
3. adds one sentinel-delimited block to `system[0]`, then replaces that block
   in place on later state changes; it never duplicates the block;
4. emits nothing and never throws on failure, empty work, or absent identity.

The hook never resolves work from the launcher identity environment.
`work_start` moves the session directory and leaves that environment
unchanged, so the environment names the launch item after a move. Native
subagent prompts remain host-owned; the hook creates no subagent packet.

The render path is pinned to `sessionboot.Build`. The envelope render stamps
wall-clock `ObservedAt` and is nondeterministic; using it would break
byte-stability. Spawns are gated by a bounded time window and keyed by the
session and its directory.

### D3. Byte-stability is the cache-safety mechanism

Unchanged snapshot → unchanged bytes → no prompt-prefix invalidation. A state
change invalidates the prefix once per change, bounded by state-change
frequency, not turn frequency. Injection that varied per turn inside the cached
prefix would re-bill the prefix as cache creation on every call and is
forbidden.

### D4. `StepActions` is state projection, not methodology

`ContinuitySnapshot` gains `step_actions` — the current step's `actions[]` from
the verified workflow definition. This is the same class of content as the boot
packet's workflow-step line: durable-state projection, not methodology prose.

`ManifestDigest` is the machine identity for skew checks.

### D6. Projection size never strands continuity

The continuity read returns the inline payload whenever the complete result
fits the unchanged 51,200-byte envelope limit. Otherwise it returns the
CD-0016 lossless projection pages. Each page carries the largest whole UTF-8
fragment that keeps the complete result within that limit. Page metadata and
the signed envelope frame have fixed, tested bounds, so every page carries at
least one code point and the read always advances.

Boot and per-turn re-pin carry the inline payload when the boot packet fits
its separate 65,536-byte cap. Otherwise they carry a deterministic projection
reference. The reference names the exact work, projection digest, length, and
canonical continuity read. It claims no complete contract, question, intent
set, or evidence. It changes no reading obligation.

## Invariants

1. The adapter plugin hook holds no authority: no approval, gating, or payload
   validation. It transports bytes the core derived.
2. The re-pin render path is `sessionboot.Build`, never the envelope render.
3. Injection is byte-stable across turns while the snapshot is unchanged.
4. Failure adds no block and never throws; existing system bytes stay unchanged.
5. `concord.ts` stays transport-only (TS6); the hook lives in the plugin entry.

## Verification

- Verb determinism: two invocations for one session directory produce
  identical bytes, and a directory that resolves no work prints nothing
  (`cmd/concord` tests).
- Directory resolution: an active claim resolves its work item; a reclaimed
  claim or an unknown path resolves no work (`internal/store` tests).
- Step join: `StepActions` equals the current step's `actions[]` for a fixture
  definition; empty when no workflow is in flight (`internal/store` tests).
- Hook: determinism, sentinel-replacement-not-append, failure no-op, spawn-window gate,
  re-resolution after a `work_start` move, no block for a managed-parent
  session, and no fallback to the launcher identity environment (adapter tests).
- Cache safety mechanism: adapter tests prove byte-stability for unchanged
  state and sentinel replacement for changed state. Live cache-meter
  measurement is deferred until after deployment and is not claimed here.
  Procedure: run one orchestrator session for at least five turns without a
  state change, then run `cache-stats summary`; pass when `cache_creation`
  equals the baseline after turn one.
- Scenario corpus: post-boundary re-pin entry under `.concord/scenarios/`.
