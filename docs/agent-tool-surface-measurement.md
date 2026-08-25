# Concord Agent Tool Surface Evidence Contract (TS9)

> **Status:** Accepted and binding until superseded.
> **Decision:** TS9, amended by CD-0042.
> **Current boundary:** Concord is pre-go-live. This document defines deterministic
> evidence for the current path, not a supported-model release gate.

## Context

Surface changes need one evidence authority before any supported-model
population exists. The binding inputs are the accepted PM1 and TS1 scenario
corpora, the strict generated schemas, and CD-0024's historical exception
recorded as evidence only. This record defines the deterministic evidence
contract for the pre-go-live path: what authorizes a change, what the
evidence planes record, and what the first go-live decision must later
define.
## Contract

The binding contract is sections 1 through 3: the pre-go-live authority rule
that only deterministic scenario evidence authorizes a surface change, the
deterministic evidence planes and their boundaries, and the six-element
change-evidence requirement. Section 4 records the first-go-live measurement
law future decisions must define; section 5 records review triggers and
falsifiers, and carries no obligation.
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

## Acceptance criteria

- Given a scenario in the PM1 or TS1 corpus
  When it is evaluated
  Then it asserts authoritative state or effects rather than response
  wording, and its prohibited-effect assertions are actively probed.

- Given a surface change candidate
  When it is judged
  Then tool count, call count, model preference, benchmark scores, and
  telemetry hold no release authority before go-live.

- Given a change that alters Product authority or consequence
  When it lands
  Then it records explicit operator acceptance beside its deterministic
  evidence.

- Given the operational evidence plane
  When it records a surface run
  Then it retains no prompts, transcripts, secrets, raw process exhaust, or
  Product identifiers beyond the approved bounded evidence artifact.

## Verification

The evidence contract is proved by the validators and corpus runner that
enforce it, so every criterion carries a typed exemption in the record naming
the enforcing mechanism.

- Criterion 1 is proved by the absent-probe guard of
  `TestAgentJobsCorpus` (`internal/agent/agent_jobs_corpus_test.go`), whose
  own rejection test is `TestEvaluateAbsentRequiresProbe`.
- Criterion 2 is a law about what cannot authorize; it is enforced
  structurally by the corpus being the only accepted evidence path in CI
  (`.github/workflows/ci.yml`), with no model-trial gate in any workflow.
- Criterion 3 is proved by the recorded operator acceptance on the
  domain-overlap change (issue #195, cited in TS1's approved amendments).
- Criterion 4 is proved by the operational-plane boundary checks in
  `scripts/check-public-content.py` and the corpus runner's recorded
  measurements, which carry typed structural facts only.
