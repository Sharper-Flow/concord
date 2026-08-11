# Scenarios

Synthetic, machine-readable acceptance scenarios live here. They describe
accepted jobs and query behavior; they are not runtime storage fixtures.

| Corpus | Authority | Coverage |
|---|---|---|
| [`workflow-engine.v1.json`](./workflow-engine.v1.json) | CD-0013 | 47 workflow-engine conformance scenarios; assertions target authoritative state, projections, events, typed errors, and effects. Validated by [`workflow-engine-scenarios.schema.json`](../contracts/workflow-engine-scenarios.schema.json). |
| [`launcher-portfolio.v1.json`](./launcher-portfolio.v1.json) | C14/C18/CD-0014 | S1 Product-row corpus, typed coverage states, launcher read triggers, first-run behavior, and no-effect session assertions. |
