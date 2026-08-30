# Design — addOrchestratorContinuityHook

Validated by independent researcher (report `addOrchestratorContinuityHook|change:researcher:orchestrator-continuity|adv-researcher|1`). Overall verdict: **ENDORSE, no blockers**, with four corrections incorporated below and one discovery claim retracted.

## Retraction

Discovery claimed the installer's `concord-plugin.ts` was missing from repo and
release, with a hand-placed stand-in live. False on all counts:
`adapter/opencode/concord-plugin.ts` ships in-repo (2026-08-30 17:37); the
installed tree is v4.10.3 (the audit read a stale v4.9.4 dir); the live
registered entry is `~/.config/opencode/tools/concord-plugin.ts`
(`opencode.jsonc:53`). O3 shrinks to removing the unregistered leftover
`~/.config/opencode/concord-adapter.ts`.

## Architecture

### 1. Core read verb (Go)

Thin read-only internal CLI verb reusing the boot path exactly:
`store.ReadWorkflowContinuity(ContinuityRequest{Work, Limit: 1})` →
`sessionboot.Build(productID, snapshot)` (`cmd/concord/session.go:113-130`).
Extract `deriveSessionBoot` into an exported helper shared by both callers.

**Render-path constraint (validator trap):** the verb must stay on the
`sessionboot.Build` path. The envelope render path stamps `time.Now()` into
`ObservedAt` (`runtime.go:1795`) — that path is nondeterministic and would
break byte-stability. `sessionboot.Build` is a pure `json.Marshal` with sorted
map keys and no clock (`sessionboot.go:30-49`, `runtime.go:1774-1788`).

Identity: the launcher sets env vars (`model.go:925-937`), the session reads
them (`session.go:156`), and OpenCode inherits `os.Environ()`
(`session.go:198-199`); in-host consumption is proven
(`adapter/opencode/concord.ts:158`). Product-only sessions (empty work id)
skip the block (`session.go:179-191`).

### 2. Adapter hook (TypeScript, existing `concord-plugin.ts`)

Registers `experimental.chat.system.transform` in the already-shipped,
already-registered entry module (`adapter/opencode/concord-plugin.ts`). The
hook is real and per-turn — confirmed in `@opencode-ai/plugin` typings
(`dist/index.d.ts:265-270`, `output.system: string[]`) and in the
predecessor's production use (`~/dev/advance/plugin/src/index.ts:1321`,
per-turn reread at 1326-1327, byte tracking at 1378). The `experimental.*`
naming risk is absorbed by the pinned host per CD-0084.

- Spawn the core verb via the existing `Bun.spawn` runner pattern
  (`adapter/opencode/dispatch.ts:92`), TTL-gated ≈10s, keyed by identity.
- **Sentinel replacement, never append**: `<!-- concord:continuity:v1 -->…<!-- /concord:continuity:v1 -->`
  replaces its prior occurrence in `output.system[0]` in place. Deterministic
  render → byte-identical prompt across turns while state is unchanged.
- Failure, empty work, absent identity (manual session) → emit nothing, never
  throw. Manual sessions keep pull-only continuity, as today.
- `PendingMessages > 0` renders a peer line.
- TS6 transport-only discipline: the hook lives in `concord-plugin.ts`, not
  `concord.ts` (`terminal-launcher-contract.md:464`).

### 3. Leftover cleanup (O3, reduced)

Remove the unregistered `~/.config/opencode/concord-adapter.ts`. The
installer's registration path is already correct and needs no new consistency
test.

## Cache-safety analysis (AC3)

Unchanged snapshot → unchanged bytes → no prefix invalidation. A state change
invalidates once per change — the predecessor's active-change economics,
bounded by state-change frequency, not turn frequency. Verification:
scripted N-turn workload with no state change, hook on vs off,
`cache-stats summary`; pass = `cache_creation` equal to baseline after the
first turn.

## Scope resolution: A + B (recommendation)

B lands as an additive extension that cannot weaken A's criteria.

- `ContinuitySnapshot` gains `StepActions []string`, resolved during
  `ReadWorkflowContinuity` — no new lookup: the read already carries
  ref+version+digest (`workflow_continuity.go:113`) and calls
  `verifyReadWorkflowDefinition` → `registry.Lookup(ref, version)` + `Verify`
  (`workflow_read.go:43-53`); the step's actions are
  `Definition.StepGraph.Steps[].Actions` (`workflow_registry.go:125-128`).
- **Naming correction (validator):** `available_actions` is per-definition
  (`workflow-definition.schema.json:17`); steps carry `actions[]` (line 55).
  The join reads the current step's `actions[]`.
- Generated contracts regenerate (`scripts/generate-agent-contracts.py`); both
  `continuity_snapshot` and `session_boot_packet` are
  `additionalProperties:false` (`payloads.schema.json:1466, 5654`), so the
  field must land in the schema before regen. Lockstep is enforced in both
  directions (`authority.go:224`, `envelope.go:336`, `concord.ts:146`).
- **Surface version recording (validator caution):** no machine-read "2.4.0"
  field exists; the surface version lives in CD prose (CD-0016:56, CD-0024:46)
  and the `ManifestDigest` is the machine identity. The change records 2.4.0
  in the CD amendment text and relies on ManifestDigest for skew checks.
- CD-0043: step/action listing is durable-state projection, same class as the
  boot packet's workflow-step line — not host methodology. Provenance
  discipline binds only if methodology prose is injected later.

## Decision-record amendments required (validator)

No CD forbids the hook, but two recorded consequences contradict it and must
be superseded by amendment in this change:

- **CD-0031:65** — "OpenCode host hooks and plugins are unnecessary."
- **CD-0016:68** — "does not add a generic host hook."

Plus a CD-0034-style record of the new injection surface (injection is
permitted only when recorded).

## LBP decisions

1. Hook = runtime surface, not authority; core derives and validates.
2. Reuse `sessionboot.Build`; never the envelope render (clock trap).
3. Sentinel replacement over append — cache economics.
4. Pull-on-transform over push-on-event — the system transform is the only
   per-turn interception point; a compaction lifecycle event, if one appears,
   is an optimization for refreshing the TTL cache, not a dependency.
5. Verb stays internal; TS1–TS9 tool budget untouched.
6. Supersede by amendment, not silent contradiction — CD-0031/CD-0016
   consequences are updated in the same change that falsifies them.

## Failure modes

| Condition | Behavior |
|---|---|
| Core binary missing | No block; one stderr warning; session proceeds |
| DB locked / unreachable | No block; session proceeds |
| No in-flight work (product-only session) | No block |
| Identity absent (manual session) | No block; pull-only, as today |
| Render throws | Swallow; previous block, if any, stands |

## Implementation strategy (refined into tasks at prep)

1. Core: export `deriveSessionBoot`; add internal continuity verb + tests.
2. Core (B): `StepActions` join via existing registry lookup; schema field;
   contract regen; tests.
3. Adapter: hook in `concord-plugin.ts`, TTL cache, sentinel render,
   fixture-snapshot tests (determinism asserted).
4. Decision records: amend CD-0031 + CD-0016; add CD-0034-style surface
   record; record surface 2.4.0.
5. Cleanup: remove unregistered `~/.config/opencode/concord-adapter.ts`.
6. Verification: cache-meter measurement; scenario corpus entry.
