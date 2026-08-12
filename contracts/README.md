# Contracts

Machine-readable public contracts live here. The bootstrap candidate includes
the versioned agent-tool envelope schema plus the accepted CD-0013 workflow
definition and outcome schemas.

| Contract | Authority | Role |
|---|---|---|
| [`workflow-definition.schema.json`](./workflow-definition.schema.json) | CD-0013 D1 | Strict v1 registry manifest shape. |
| [`workflow-outcome.schema.json`](./workflow-outcome.schema.json) | CD-0013 D4 and CD-0012 | Strict discriminated union for `exists`, `absent`, `outcome`, and `check`. |
| [`workflow-engine-scenarios.schema.json`](./workflow-engine-scenarios.schema.json) | CD-0013 conformance corpus | Strict carrier shape for the 47 scenario records and closed assertions. |
| [`workflow-engine.fixtures.json`](./workflow-engine.fixtures.json) | Contract checker | Positive/negative schema and graph-validation fixtures. |
| [`agent-lanes.v1.json`](./agent-lanes.v1.json) + [`agent-lanes.schema.json`](./agent-lanes.schema.json) | CD-0017 D1/D2 | Closed typed worker lanes with pinned preferred models. |
| [`routing-policy.v1.json`](./routing-policy.v1.json) + [`routing-policy.schema.json`](./routing-policy.schema.json) | CD-0017 amendment D3/D5/D9 | Digest-pinned capability-class resolution sets and typed fallback policy. |
