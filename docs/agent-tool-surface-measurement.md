# Concord Agent Tool Surface Evidence Contract (TS9)

> **Status:** Accepted and binding until superseded.
> **Decision:** TS9, amended by CD-0042.
> **Current boundary:** Concord is pre-go-live. This document defines deterministic
> evidence for the current path, not a supported-model release gate.

## 1. Pre-go-live authority

Before go-live, a surface change is accepted only when deterministic evidence shows
that the current generated manifest, schemas, and runtime preserve the required
Product outcome. Tool count, call count, model preference, benchmark scores, and
production-like telemetry cannot authorize a change.

The authoritative evidence contract is:

- PM1 query scenarios and TS1 end-to-end scenarios with named initial state,
  final-state, communication, and prohibited-effect oracles;
- strict generated manifest, payload, envelope, and scenario schemas;
- core authority and authorization proofs;
- SQLite transaction and idempotency proofs for durable effects;
- negative probes for unknown fields, variants, stale versions, missing approval,
  mismatched digests, malformed output, and false success; and
- generated drift checks, repository validators, and conformance.

Every scenario must assert authoritative state or effects rather than response
wording. Hard authority, transaction, recovery, and prohibited-effect failures
block the change. The TS1 corpus includes the named
`AJ5-resolve-domain-overlap` real-dispatch proof for operator-approved,
version-pinned overlap resolution.

## 2. Deterministic evidence planes

The PM1/TS1 plane owns job correctness. It records the explicit scenario, fixture,
request, resulting state, communication, authority, effects, and recovery. It does
not infer success from prompts or model prose.

The operational plane may record bounded structural facts such as tool/operation,
manifest digest, outcome, authority, retries, idempotency replays, approvals,
bytes, pagination, and latency. It must not infer intent, satisfaction, reasoning,
or task success. It retains no prompts, transcripts, secrets, raw process exhaust,
or Product identifiers beyond the approved bounded evidence artifact.

## 3. Change evidence

An addition, removal, split, merge, description change, or operation consequence
change must name:

1. the unmet or corrected intent;
2. the deterministic scenario and oracle;
3. the authority, transaction, result, and recovery boundary;
4. the current manifest digest and generated artifacts;
5. positive, negative, strict-schema, and conformance evidence; and
6. operator acceptance when Product authority or consequence changes.

No model trial is required to release a pre-go-live change. Supported-model trials,
cross-model comparisons, and production telemetry may be run as research and may
inform a future design review, but they are not release authority before go-live.

## 4. First-go-live measurement law

The first go-live decision must define the supported model and client populations,
the population identity and privacy boundary for measurement, the supported
contract identity, and the measurement evidence required for future surface
changes. It must explicitly re-accept any measurement gate before that gate can
block a supported release.

Until then, TS9 has no paired-trial threshold, deprecation gate, production-volume
quota, exception process, or hypothetical runner pipeline. CD-0024's exception is
historical evidence only; it is not a precedent or a gate requiring another
exception.

## 5. Review triggers and falsifiers

Review TS9 when a named deterministic scenario exposes an unmet Product intent,
authority/recovery boundary, schema defect, or conformance regression; when a
concrete supported population exists after go-live; or when a real client requires
a different measurement method.

Reopen this contract when deterministic evidence cannot distinguish a safe current
path, authoritative population identity cannot be preserved, or the first-go-live
measurement law proves incomplete. Any amendment keeps scenario-first evaluation,
strict schemas, explicit population identity once applicable, no heuristic success
authority, and operator acceptance for Product consequences.
