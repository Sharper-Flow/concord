# CD-0005: Concord Minimal Agent Tool Surface

**Status:** Accepted
**Date:** 2026-08-06
**Decision owners:** Operator + agent
**Scope:** Canonical agent jobs, current generated OpenCode surface,
context/authorization, transport, results, digest-bound evolution, and deterministic
pre-go-live evidence.

## Context

Concord must give local agents a complete Product-memory surface without repeating
Advance's command/tool sprawl, target-path plumbing, repair internals, false success,
or unbounded context. Storage tables and Go CLI commands are internal boundaries; they
do not automatically become agent tools.

The decision sequence TS1–TS9 was completed and operator-approved individually. Its
linked artifacts remain the detailed binding contracts; this record provides the
accepted architecture index and invariants.

## Decision

CD-0042 identifies the current path by the generated manifest digest.

### D1. Canonical jobs and evaluation

Concord supports eight end-to-end jobs: orient/choose work, explain blockage,
capture need, transition with evidence, relate/scope, compact/reconcile, retrieve
durable knowledge, and coordinate native operational work. Tool-neutral
scenarios judge resulting state, communication, prohibited effects, and invariants;
AJ5 includes version-pinned Domain-overlap resolution under CD-0222.

Binding: [`../specs/agent-tool-surface-jobs.md`](../specs/agent-tool-surface-jobs.md) and
[`../../.concord/scenarios/agent-jobs.v1.json`](../../scenarios/agent-jobs.v1.json).

### D2. Surface budget and granularity

At most nine always-visible domain tools. Merge/split follows intent, authority,
consequence, transaction, result, and recovery boundaries—not entities or CRUD.
Static v1 exposure; no discovery meta-tool.

Binding: [`../specs/agent-tool-surface-budget.md`](../specs/agent-tool-surface-budget.md).

### D3. Three read tools

```text
concord_product_view   resolve | snapshot
concord_work_browse    list | ready | blocked | scope
concord_work_trace     history | relations
```

Reads are strict, bounded, cursor-based, authority/freshness-aware, and never repair
or mutate.

Binding: [`../specs/agent-read-tool-contract.md`](../specs/agent-read-tool-contract.md).

### D4. Four mutation tools

```text
concord_work_define       capture | revise_intent
concord_work_transition   lifecycle | workflow_action
concord_work_relate       membership/link/supersession intents
concord_work_compact      publish | reconcile
```

One domain intent per call; no generic patch/mixed batch. SQLite effects are atomic.
Git/external effects use ordered durable recovery. Native systems execute their own
operations.

Binding: [`../specs/agent-mutation-tool-contract.md`](../specs/agent-mutation-tool-contract.md).

### D5. Context, authorization, and retry identity

Trusted clients inject a hidden typed envelope. Core-issued grants bind principal,
client, scope, capability, expiry, and revocation. Core re-resolves scope every call.
Mutations require expected versions and durable idempotency. No path/trust
booleans. Operator decisions follow CD-0243 D2 in conversation.

Binding: [`../specs/agent-call-context-contract.md`](../specs/agent-call-context-contract.md).

### D6. OpenCode adapter and transport

One globally installed `concord.ts` module exposes the generated current tool set and invokes the
short-lived Go CLI over argv + JSON stdin/stdout. No plugin hooks, MCP daemon, FFI,
TypeScript domain logic, or provider proxy. Signed client bootstrap protects grant
issuance.

The current adapter exposes the generated current tool set, including the accepted
`concord_work_relate.resolve_overlap` operation, and binds the exact manifest digest.
Pre-go-live clients do not negotiate a surface version or range.

Binding: [`../specs/agent-adapter-transport-contract.md`](../specs/agent-adapter-transport-contract.md).

### D7. Result/error/evidence envelope

Every tool returns strict `ok|pending|partial|error`. Core and adapter origins are
disjoint. Authority, freshness, source versions, pagination, omissions, warnings,
evidence, effect certainty, and recovery are structural. No success boolean or raw
process exhaust. Serialized output caps at 51,200 bytes.

Binding: [`../specs/agent-result-envelope.md`](../specs/agent-result-envelope.md) and
[`../../contracts/agent-tool-envelope.schema.json`](../../../contracts/agent-tool-envelope.schema.json).

### D8. Evolution and digest identity

One schema-validated language-neutral manifest generates/verifies Go, TypeScript,
schemas, tests, and docs. Grants, sessions, invocations, and envelopes bind its
exact digest and fail closed on mismatch. There is no pre-go-live surface version,
range negotiation, alias, deprecation window, migration path, or old-client runtime
branch. Durable Product/event history remains interpretable when current readers
require it.

Binding: [`../specs/agent-tool-surface-evolution.md`](../specs/agent-tool-surface-evolution.md).

### D9. Deterministic pre-go-live evidence gate

Deterministic PM1/TS1 oracles, strict schemas, authority and transaction proofs,
negative probes, generated drift checks, and conformance gate the current path.
Supported-model trials and production telemetry may inform research but do not gate a
pre-go-live release. Surface changes require named deterministic evidence and
operator acceptance when authority or consequence changes. The first-go-live
decision must define supported populations and re-accept any future measurement
gate.

Binding: [`../specs/agent-tool-surface-measurement.md`](../specs/agent-tool-surface-measurement.md).

## Invariants

1. The generated current manifest defines one static tool surface; nine remains the hard cap.
2. Agent surface derives from accepted jobs, never storage/CLI inventory.
3. Go core owns all domain semantics and authorization.
4. Adapter owns only host context, transport, and schema registration.
5. Scope/identity use stable IDs; no repeated filesystem target plumbing.
6. Mutation retries reuse one durable idempotency identity.
7. No false cross-authority atomicity or false top-level success.
8. Reads/output remain bounded and authority/freshness explicit.
9. Surface changes are generated, digest-bound, deterministically evidenced, and
operator-accepted when authority or consequence changes.

## Consequences

### Positive

- Small selection surface with complete Product-memory coverage.
- Structural scope/auth/retry safety without model-visible plumbing.
- One Go domain authority and one thin OpenCode adapter.
- Honest recovery from partial git/transport outcomes.
- Surface identity and change pressure are governed before implementation drift.

### Cost

- Canonical manifest/code generation and deterministic conformance evidence are mandatory.
- PM1/TS1 deterministic evidence is substantial before release.
- New Product domains do not gain tools automatically; they must pass the current
  TS9 evidence contract.

## Rejected alternatives

- CRUD/table/CLI-command tool generation.
- One mega `query`/`invoke` or generic JSON patch.
- Advance command-for-command surface.
- Repeated `target_path`/trust arguments.
- Generic plugin domain layer, MCP daemon, FFI, or TypeScript core.
- Natural-language errors, silent truncation, bare async IDs, or success booleans.
- Permanent aliases, runtime-hidden registries, or usage-only pruning.

## Implementation acceptance

Implementation must satisfy each linked contract, PM1/TS1 deterministic scenarios,
TS6 transport/grant failure tests, TS7 schema/negative probes, TS8 exact
manifest-digest binding, and TS9 deterministic authority/transaction/conformance
evidence. A passing schema without state/authority evidence is insufficient.

## Supersession

CD-0005 does not supersede CD-0002/CD-0003 storage/core decisions. It defines the
agent boundary above them. Any change to D1–D9 follows TS8/TS9 and records explicit
operator acceptance.
