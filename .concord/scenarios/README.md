# Scenarios

Synthetic, machine-readable acceptance scenarios live here. They describe
accepted jobs and query behavior; they are not runtime storage fixtures.

| Corpus | Authority | Coverage |
|---|---|---|
| [`workflow-engine.v1.json`](./workflow-engine.v1.json) | CD-0013; CD-0242 D2/D14 | Two current-state workflow scenarios: capture without an outcome contract and refusal of condition actions without writes. Validated by [`workflow-engine-scenarios.schema.json`](../../contracts/workflow-engine-scenarios.schema.json). |
