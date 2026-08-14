# Concord Agent Tool Measurement, Pruning, and Expansion Gate (TS9)

> **Status:** **Accepted — binding until superseded.**
> **Accepted by operator:** 2026-08-06.
> **Decision:** TS9; binding launch/maintenance evidence contract for the accepted
> surface. CD-0023 records one narrow 3.0.0 Epic exception.
> **Binding inputs:** accepted PM1 corpus and TS1–TS8 job, budget, tool, context,
> adapter, envelope, and evolution contracts.
> **Does not decide:** model/provider support list, release automation, Product fields
> (C14), resources (C15), or any future surface change before its evidence exists.

## 1. Decision

Concord shipped the v2 eight-tool static surface only after a recorded launch
baseline. The current 3.0.0 surface is governed by CD-0023's narrow exception.
After launch, surface review is **triggered by evidence**, never by a
calendar cleanup ritual.

No tool/operation is added, split, merged, removed, aliased, or hidden through
discovery unless:

1. a named scenario establishes the problem and outcome oracle;
2. current and candidate manifests run against the same corpus/model configuration;
3. hard correctness and authority boundaries pass;
4. comparative success/selection/context evidence supports the change; and
5. TS8 version/deprecation plus operator acceptance are satisfied.

Tool count, low usage, fewer calls, schema tokens, or platform precedent alone never
authorize a surface change.

### CD-0023 Epic exception

CD-0023 is the sole exception to the paired supported-model-trial requirement: the
2.3.0-to-3.0.0 Epic reachability change may ship on its named accepted outcome,
deterministic agent-boundary evidence, generated contract drift checks, fail-closed
major bootstrap, durable replay proof, migration guidance, and explicit operator
approval. It makes no model-selection or task-success claim and does not authorize a
second exception. Any later model-visible surface change must first establish this
runner and evaluate the 3.0.0 baseline under the normal sections 4.1–4.3 rules.

## 2. Two evidence planes

### 2.1 Authoritative evaluation plane

PM1's 22 query scenarios and TS1's 21 end-to-end scenarios own task/job correctness.
The runner knows the explicit `query_id`/`job_id`, initial state, final-state oracle,
required communication, prohibited effects, and shared invariants. This plane can
say whether an agent job succeeded.

### 2.2 Operational telemetry plane

Production/runtime telemetry owns only structurally observed call facts:

- surface/adapter/core version and manifest digest;
- tool + operation;
- TS7 outcome, authority, error/recovery kind, and adapter reason;
- calls, same-key retries, idempotent replays, and operator permission prompts;
- input-schema/prompt tokens exposed by client;
- output bytes, pagination/continuation, warning/omission counts;
- latency and transport failure class; and
- explicit changed/durable-operation reference counts.

It does **not** infer user intent, job family, satisfaction, omitted requirement,
model reasoning, or task success from prompts/transcripts. Heuristics may rank traces
for human review but cannot authorize addition/removal or produce success rates.

## 3. Population identity and privacy

Every reported metric names:

```text
corpus + version
candidate manifest digest
model/provider/version + sampling settings
adapter/core versions
client type
run count or production-call population
inclusion/exclusion rules
time/build boundary when applicable
numerator and denominator
```

An **eligible invocation** is every attempted Concord call reaching the adapter with
a recognized tool and selected/attempted surface version—including schema rejection,
bootstrap/spawn failure, core error, timeout, cancellation, and unknown outcome. An
**outcome-known invocation** is eligible and has one validated core/adapter envelope.
Unknown/no-envelope attempts are reported separately, never silently excluded.

- Usage share: schema-valid invocations on one negotiated surface/client population.
- Error rate: `outcome=error` divided by outcome-known invocations.
- Transport-failure rate: `origin=adapter` errors divided by eligible invocations.
- Retry rate: logical mutation intents with >1 attempt divided by distinct logical
  mutation intents.

For retry aggregation, adapter/core groups attempts temporarily by a keyed hash of
TS5's idempotency tuple, increments aggregate buckets, then discards the hash. Durable
evidence retains only counts—no idempotency key, entity ID, or linkable trace ID.

Telemetry stores no prompts, natural-language arguments, work/Product/Project IDs,
grant/approval secrets, evidence contents, repository paths, raw stdout/stderr, or
session transcripts. Aggregation uses tool/operation/outcome dimensions only.
Operational telemetry remains outside Product memory; durable surface decisions keep
only the bounded aggregate evidence artifact.

## 4. Launch baseline

### 4.1 Deterministic conformance

Required before any model trial:

- 100% PM1/TS1 initial-state, schema, final-state, communication, prohibited-effect,
  authority, idempotency, pagination, and recovery assertions pass;
- every supported tool/operation has positive and rejection-path coverage;
- zero prohibited effects, silent truncations, false authority, or false `ok`;
- Go, TypeScript, manifest, JSON Schema, and docs digest/drift checks pass; and
- TS8 adapter↔core compatibility matrix passes.

Any failure blocks release. Model success cannot compensate.

### 4.2 Supported-model trials

For each declared supported model/configuration:

- run every PM1/TS1 scenario at least 10 times and at least 50 trials per PM1 query/
  TS1 job family, adding balanced repetitions when needed;
- hold prompt, sampling settings, fixture, judge version, and paired trial schedule
  constant; vary only the declared current/candidate surface artifacts and record
  both manifest digests/descriptions;
- report exact counts and two-sided Wilson 95% intervals for absolute success and
  selection rates overall and by query/job family;
- require at least 95% point task success overall and 90% within each PM1 query/TS1
  job family;
- require at least 95% correct first-domain-tool selection overall;
- require trials containing one or more wrong-domain calls at or below 5% of all
  trials;
- require zero prohibited effects across all trials; and
- publish calls/job, retries, operator interventions, schema tokens, output bytes,
  and latency without allowing those efficiency metrics to offset correctness.

A model/configuration failing the floor is unsupported for that surface release; the
surface is not weakened silently to make its score pass.

### 4.3 Paired candidate comparison

Predeclare the primary endpoint and practical threshold. Current and candidate
surfaces use matched scenario/model/configuration trials.

- Minimum 100 paired observations per affected family; increase using a predeclared
  80%-power calculation when margin/discordance requires more.
- Binary task success uses a one-sided 95% bound on paired candidate-minus-current
  difference (stratified matched-pair bootstrap, 10,000 seeded resamples).
- Non-inferiority: lower bound ≥ -5 percentage points overall and ≥ -10 in every
  family.
- Material success improvement: lower bound > 0 and point improvement ≥10 points in
  affected family.
- Material selection improvement: lower bound > 0 and wrong-domain-trial reduction
  ≥5 points.
- Material efficiency improvement: ≥10% predeclared schema-token, call, or output
  reduction with no correctness/authority regression.
- Inconclusive bound: keep current surface or collect more trials; never choose by
  tool-count preference.

Tie-break order: hard correctness/authority → task success → correct selection →
context/output → calls/retries → latency → tool count.

### 4.4 Post-cutover advisory telemetry

Initial release/cutover is decided by deterministic and supported-model scenario
evaluation above—not production-call volume. After cutover, Concord may report
eligible, schema-valid, outcome-known, unknown, total-attempt, and ephemeral-distinct
logical-mutation populations from real use. Any rate names its exact denominator;
small populations are marked `insufficient_population`.

This telemetry is advisory stewardship evidence. It does not retroactively declare
initial usability or infer end-to-end job success.

## 5. Evidence artifact

Every launch or surface-change comparison persists one bounded artifact containing:

- question/change being evaluated;
- current and candidate manifest digests/versions;
- corpus/scenario additions and oracle;
- model/configuration and exact populations;
- hard-oracle results;
- absolute success/selection counts and Wilson intervals, plus paired difference
  bounds/margins for candidate comparisons;
- calls/retries/context/output/latency/intervention distributions;
- bounded production aggregates when available;
- authority/consequence review;
- regressions, uncertainty, and unsupported populations;
- decision, operator approval, migration, and TS8 version/deprecation plan.

Raw prompts/transcripts are not required or retained by this artifact.

## 6. Expansion gate

A new tool or operation requires all of:

1. **Unmet intent:** at least three independent concrete occurrences (different work
   or sessions) of the same accepted Product job that cannot be completed safely by
   the current surface. One occurrence is sufficient only when an executable
   reproduction or causal trace names the violated security/authority/data-loss/
   irreversible hard boundary; the new scenario must fail current surface and the
   candidate must pass with zero prohibited effects.
2. **New scenario:** a tool-neutral scenario reproduces the gap, fails current
   surface, and defines final state/communication/prohibited-effect oracles.
3. **Boundary proof:** the proposed operation has a distinct intent plus authority,
   consequence, transaction, result, or recovery boundary; it is not an endpoint,
   table, CLI command, or convenience alias.
4. **Candidate evidence:** all existing scenarios still pass; new scenario passes;
   §4.3 paired non-inferiority holds; zero prohibited effects remain.
5. **Selection/context benefit:** added selection/schema cost is measured. A new
   tool must meet §4.3's success/selection practical threshold or close the proven
   hard safety hole—not merely reduce one valid call.
6. **Budget:** candidate remains under TS2's nine-tool cap. Exceeding it reopens TS2;
   no automatic tenth tool.
7. **Evolution:** TS8 major-version, compatibility, migration, and approval gates
   pass.

## 7. Removal gate

Low usage never proves a tool unnecessary; rare approval, recovery, and compaction
operations may be critical. Removal requires all of:

1. no unique PM1/TS1 or newly accepted scenario requires its intent/boundary;
2. a concrete rerouting/merge candidate preserves authority, consequence,
   transaction, recovery, and strict schema boundaries;
3. all current scenarios pass under the candidate with zero prohibited effects;
4. §4.3 paired non-inferiority holds;
5. neighboring-tool selection does not regress by 5 points and calls/schema/output
   do not regress by 10% unless a predeclared correctness gain outweighs it;
6. production usage, when available, is reported with its exact denominator and any
   small population marked insufficient—advisory, never required or decisive;
7. every supported durable operation/in-flight trace remains recoverable; and
8. TS8 deprecation/migration/operator gates pass.

An operation can be removed even when used if its intent is fully and more reliably
represented elsewhere. An unused operation cannot be removed if it owns a necessary
safety or recovery boundary.

## 8. Split, merge, description, and discovery gates

### Split

Requires repeated wrong-variant selection or mostly irrelevant schema fields in a
named scenario family, plus §4.3 material selection or success improvement without
crossing TS2 budget or harming others.

### Merge

Requires tools/operations to be always chained for the same intent in scenarios and
observed traces, with one authority/consequence/result/recovery family, §4.3
non-inferiority, and ≥10% schema-token or call reduction. Reduced count alone is
insufficient.

### Description/schema refinement

Classify every description/schema refinement under TS8 as PATCH, MINOR, or MAJOR
before implementation; do not assume strict-schema work is MINOR. Prefer a compatible
description/schema fix before changing tool boundaries. Rerun paired trials because
descriptions affect selection. A fix cannot hide a real authority/recovery distinction.

### Progressive discovery

Requires a static-surface failure scenario and a candidate comparison that includes
discovery calls, meta-tool schema tokens, latency, failures, versioning, and context.
It must meet §4.3's material success/selection threshold after all added cost, with
non-inferior calls/schema/latency or an explicitly approved tradeoff. Tool-count
growth or fashion is not evidence.

## 9. Review triggers

Run the applicable gate when any occurs:

- three independent unmet-intent or confusion incidents;
- one security/authority/data-loss/irreversible-consequence incident;
- a candidate tool count reaches TS2's cap;
- a major OpenCode/model/tool-schema behavior change;
- a concrete second client;
- a TS8 deprecation/removal proposal;
- post-cutover telemetry reveals a repeated named failure pattern; or
- deterministic/model acceptance regresses.

Do not run periodic pruning merely because time passed. Accumulated telemetry without
a trigger remains observability, not a mandate to churn the surface.

## 10. Rejected evidence

- Tool call share without scenario coverage/population definition.
- One anecdote without the executable/causal hard-boundary proof required above.
- LLM classification of prompts as the sole job-success oracle.
- Comparing different models/prompts/fixtures/judges as if only surface changed.
- Average-only metrics hiding job-family failure.
- Fewer tools/calls/tokens with lower correctness.
- Statistical significance without a practical non-inferiority/effect margin.
- A 0-call operation treated as removable despite missing opportunity denominator.
- Benchmark traces or raw prompts retained in Product memory.
- Calendar-based cleanup or permanent experimental aliases.

## 11. Falsifiers

Reopen TS9 when:

- ten runs/scenario cannot produce stable candidate ordering;
- declared 95%/90%/5% floors reject otherwise demonstrably safe supported models or
  admit repeated user-visible failures;
- production aggregation cannot establish population identity without retaining
  prohibited content;
- absolute Wilson or paired-bootstrap bounds mis-rank candidates;
- three-occurrence trigger is too insensitive for recurring non-severe gaps;
- production populations cannot be compared without misleading aggregation; or
- a second client needs a different but population-comparable measurement method.

Any amendment keeps deterministic hard oracles, explicit populations, no heuristic
success authority, safety exceptions, scenario-first expansion/removal, and operator
acceptance.
