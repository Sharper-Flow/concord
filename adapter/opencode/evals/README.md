# Lane behavioural evals

Prompt-evaluation harness for the Concord worker lanes, required by
[CD-0017](../../../docs/decisions/CD-0017-typed-workers-and-model-routing.md) D7.

## What this is for

Everything that can be decided without a model already lives in Go: registry
validation, packet and report schemas, dispatch fencing, evidence recording, and
distinctness rejection. None of that is repeated here.

What Go cannot decide is whether a lane, given a real packet, actually behaves
like a bounded worker — whether it stays inside its authority when a packet asks
it not to. That is what this harness measures.

**These evals are advisory.** They never complete a gate and never substitute
for a schema or state check. A failing eval is a signal to look at lane prose,
not a broken build.

## Layout

| Path | Role |
|---|---|
| `promptfooconfig.yaml` | Harness configuration: packets, lane providers, assertions. |
| `packets/` | Real `agent-lane-packet.v1` documents, two per lane. |
| `assertions/lane-report.js` | Shared structural check that the returned report satisfies `agent-lane-report.v1` and binds to its packet. |

Each lane gets two packets: one ordinary bounded task, and one that deliberately
asks the lane to cross a boundary — spawn a nested worker, record a workflow
transition, accept its own review, or repair what it was asked only to verify.
Refusing the second kind is the passing behaviour.

Providers wrap the same argv shape that
[`dispatch.ts`](../dispatch.ts) uses, so a run exercises the real lane through
the real host path rather than a paraphrase of it. Each lane is evaluated
through the model the registry pins for it.

## Running

Requires a host `opencode` on `PATH` and credentials for the pinned models. No
`package.json` is added to this repository; promptfoo runs standalone.

```sh
npx promptfoo@0.122.0 eval -c adapter/opencode/evals/promptfooconfig.yaml
```

Validate the configuration without calling any model:

```sh
npx promptfoo@0.122.0 validate config -c adapter/opencode/evals/promptfooconfig.yaml
```

## What CI enforces

CI never runs the evals — it has no model access, and eval verdicts carry no
authority. It does run
[`scripts/check-lane-evals.py`](../../../scripts/check-lane-evals.py), which
fails when the harness stops describing the lanes the registry declares:

- a registered lane with no eval packet
- a packet whose `lane_version` or `lane_digest` has drifted from the registry
- a packet the configuration never references
- a lane whose provider is not pinned to its registered model
- a packet for a lane that is not registered
- a registered lane with no prose file

That keeps the harness honest across lane changes without pretending a model
judgement is a deterministic check.
