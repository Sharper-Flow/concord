# CD-0034: Host prompt provenance is declared at dispatch

- **Status:** Accepted
- **Date:** 2026-08-16
- **Scope:** Worker dispatch evidence; adapter; issue #103
- **Related:** CD-0017 (D1, D5, D7, Invariant 8), issue #103
- **Supersedes:** nothing

## Context

A dispatched worker's behavior is shaped by more than the lane contract. On a
realistic host, unversioned surfaces inject prompt content into every agent
call: model-family behavioral hints, always-on instruction files carrying the
operator's routing and safety policy, output-voice overlays,
predecessor-coupled instruction files that retire on migration. Only the lane
contract is versioned and digest-pinned. The rest can change without any
contract version moving — delegation-boundary drift arriving through prompt
content instead of packet shape, and invisible to D7 evaluation.

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

**Consequences for D7.** A prompt evaluation is reproducible from its
evidence only when both digests match: the lane contract digest and the host
provenance digest of the run under test. A green eval proves nothing about a
later run whose provenance differs — and now the difference is visible in
dispatch evidence rather than inferable only from drift. Deterministic
authority stays in Go tests (provenance shape validation, the v3 gate at the
CLI boundary, upcast behavior); behavioral evaluation stays outside, as D7
already holds.

**Migration (predecessor-coupled instruction files).** Retiring an
instruction file changes the manifest and therefore the total digest. Lane
behavior change at migration is thus recorded, never silent: the last
dispatch under the old provenance and the first under the new bound the
transition in evidence.

**History.** Legacy v1/v2 dispatch evidence upcasts with an honest marker:
provenance recorded-before-declaration is unknown by construction, stamped as
an `unenumerated` source with a zero digest rather than fabricated. The CLI
refuses v3 dispatch evidence without `host_provenance` — injection is
permitted only when recorded, and the emitter gate is the enforcement point.

## Rejected alternatives

- **Exclusive** (suppress injection or fail closed): on this host the
  injected content includes the operator's own safety law — test throttling,
  trunk-worktree isolation, transaction discipline. Suppressing it makes
  workers less safe, not more; and the enumerability limits mean exclusivity
  could not be verified anyway.
- **Tolerated** (injection out of scope, evals advisory-only): accepts
  invisible drift; a green eval would prove nothing about any later run with
  no way to notice. D7's harness would measure an unreproducible composite.

## Verification

- `internal/store/worker_undeclared_test.go`: provenance validation
  (digest shape, closed kinds, bounded sources, unenumerated carries no
  hash and its path is its name, enumerated must carry a hash, no
  duplicates); v3 dispatch with
  provenance folds and the evidence bytes persist.
- `adapter/opencode/dispatch.test.ts`: provenance is deterministic for the
  same inputs and changes when an enumerated source's content changes
  (bun, local — CI validates the generated contracts, as the repo already
  holds).
- CLI: `worker-dispatch` v3 requires `host_provenance`; help text declares
  the field.
