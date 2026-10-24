# Scenarios

Synthetic, machine-readable acceptance scenarios live here. They describe
accepted jobs and query behavior; they are not runtime storage fixtures.

| Corpus | Authority | Coverage |
|---|---|---|
| [`workflow-engine.v1.json`](./workflow-engine.v1.json) | CD-0013; CD-0242 D2/D14 | Two current-state workflow scenarios: capture without an outcome contract and refusal of condition actions without writes. Validated by [`workflow-engine-scenarios.schema.json`](../../contracts/workflow-engine-scenarios.schema.json). |
| [`adapter-continuity.v1.json`](./adapter-continuity.v1.json) | CD-0090 | Per-turn continuity re-pin resolved from the session directory: byte-stability across turns, in-place sentinel replacement, failure-as-absence, TTL spawn gating, post-boundary re-pin without a tool call, work_start retarget to the landed item's block, and lane-session absence. Validated by [`adapter-continuity-scenarios.schema.json`](../../contracts/adapter-continuity-scenarios.schema.json). |
