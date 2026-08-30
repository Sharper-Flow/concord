# Agreement — addOrchestratorContinuityHook

## Objectives

- **O1 — Close the CD-0016 window.** A turn that begins after a compaction
  boundary, with no intervening `concord_*` call, carries pinned continuity
  (work identity, workflow step, approved contract reference) derived from
  durable state.
- **O2 — Carry the peer signal.** `PendingMessages` from `ContinuitySnapshot`
  surfaces in the same derived block when nonzero.
- **O3 — Reconcile plugin entry ownership.** The hook lives in the entry module
  the installer registers (`concord-plugin.ts` per `ADAPTER_FILES` /
  `PLUGIN_ENTRY_FILE`), retiring the hand-placed `~/.config/opencode/concord-adapter.ts`
  stand-in. Installer registration, shipped file, and hook presence stay
  consistency-tested.
- **O4 — Adapter owns no authority.** All derivation, validation, and rendering
  happens in the core (`ReadWorkflowContinuity` → `sessionboot.Build` →
  `Validate` path, `cmd/concord/session.go:130`); the adapter transports.

## Acceptance criteria

- **AC1:** After a context boundary with no intervening `concord_*` call, the
  orchestrator prompt contains pinned continuity content. Evidence: adapter test
  rendering hook output from a fixture `ContinuitySnapshot`; scenario coverage.
- **AC2:** The injected payload is produced by the core's existing continuity
  read path — not adapter-composed. The adapter contains no approval, gating,
  or validation logic beyond transport.
- **AC3:** Cache safety is measured, not assumed. With the hook active,
  `opencode-cache-meter` shows no full-prefix `cache_creation` regression on a
  repeated-turn workload versus baseline. Placement is outside the cached
  prefix, or content is byte-stable across turns while state is unchanged.
- **AC4:** Failure degrades to absence. Continuity read failure or no in-flight
  work injects nothing and never throws; the session proceeds.
- **AC5:** Per-turn cost stays bounded. The hook reuses the child-spawn pattern
  (`dispatch.ts` `Bun.spawn` of the concord binary) with an explicit bound —
  debounce or digest-gated emission — so no unbounded per-turn latency.
- **AC6:** `PendingMessages > 0` renders in the injected block.

## Constraints

1. The adapter never owns validation, authorization, or approval.
2. CD-0016 holds: summaries stay advisory and never carry authority.
3. Prompt-cache prefix safety is a hard requirement (toolbox ADR 0013 economics).
4. If derived instruction content is added later, CD-0043 D2 provenance
   discipline (enumerated path, kind, content hash) governs.

## Avoidances

- No write-interception or trunk firewall — CD-0072 Invariant 1 forbids it;
   separate concern, separate decision.
- No new durable state; injection is derived, never stored.
- Output bounding, history sanitization, and session metering are named
   follow-ups, not this change.
- **Scope boundary:** Scope B (joining `available_actions` into the continuity
  read for derived orchestrator instruction) is decided at the design gate. It
  may extend this change only if it does not weaken any criterion above; if it
  does, this agreement reopens.
