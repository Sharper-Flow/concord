# CD-0034: Host prompt provenance is declared at dispatch

- **Status:** Accepted
- **Date:** 2026-08-16
- **Scope:** Worker dispatch evidence; adapter; issue #103
- **Related:** CD-0017 (D1, D5, Invariant 8), issue #103

## Context

A dispatched worker's behavior is shaped by more than the lane contract. On a
realistic host, unversioned surfaces inject prompt content into every agent
call: model-family behavioral hints, always-on instruction files carrying the
operator's routing and safety policy, output-voice overlays,
predecessor-coupled instruction files that retire on migration. Only the lane
contract is versioned and digest-pinned. The rest can change without any
contract version moving — delegation-boundary drift arriving through prompt
content instead of packet shape.

## Decision

**Declared: host injection is permitted only when recorded.** The worker
dispatch evidence (payload version 3) carries `host_provenance`: an ordered
manifest of the injection surfaces the adapter can bind, each with its kind,
path, and content hash, plus a total digest binding the manifest. The
adapter enumerates:

- the lane agent definition file actually loaded for the dispatch;
- the `AGENTS.md` chain at spawn cwd (bounded);
- instruction files declared through `CONCORD_HOST_INSTRUCTIONS`
  (colon-separated paths, bounded).

Surfaces the adapter cannot enumerate — provider behavioral hints,
output-voice overlays, MCP tool-definition prompt content — are recorded by
name as `unenumerated` sources: visible as present-but-unbound rather than
silently absent.

A difference in host provenance between two runs is visible in dispatch
evidence. Go tests own provenance shape validation and the v3 gate at the CLI
boundary.

**Migration (predecessor-coupled instruction files).** Retiring an
instruction file changes the manifest and therefore the total digest. Lane
behavior change at migration is thus recorded, never silent: the last
dispatch under the old provenance and the first under the new bound the
transition in evidence.

**Provenance recorded before declaration.** The documented cutover migrates retained evidence, and provenance recorded before declaration is unknown by construction, stamped as an `unenumerated` source with a zero digest rather than fabricated ([CD-0242 D13](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d13-the-cutover-is-one-documented-migration-with-a-verified-snapshot)). The CLI
refuses v3 dispatch evidence without `host_provenance` — injection is
permitted only when recorded, and the emitter gate is the enforcement point.

## Rejected alternatives

- **Exclusive** (suppress injection or fail closed): on this host the
  injected content includes the operator's own safety law — test throttling,
  trunk-worktree isolation, transaction discipline. Suppressing it makes
  workers less safe, not more; and the enumerability limits mean exclusivity
  could not be verified anyway.
- **Tolerated** (injection out of scope, evals advisory-only): accepts
  invisible drift with no way to notice it.

## Verification

- `internal/store/worker_lanes_test.go`: provenance validation in
  `internal/store.TestWorkerHostProvenanceValidation`
  (digest shape, closed kinds, bounded sources, unenumerated carries no
   hash and its path is its name, enumerated must carry a hash, no
   duplicates); dispatch with provenance writes durable evidence in
   `internal/store.TestWorkerDispatchCarriesProvenanceIntoDurableEvidence`.
- `adapter/opencode/dispatch.test.ts`: provenance is deterministic for the
  same inputs and changes when an enumerated source's content changes
  (bun, local — CI validates the generated contracts, as the repo already
  holds).
- CLI: `worker-dispatch` v3 requires `host_provenance`; help text declares
  the field.
