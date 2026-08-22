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
through whatever model the host selects — Concord no longer asserts a model
identifier on dispatch (CD-0058).

## Running

Requires a host `opencode` on `PATH` and credentials for the configured models. No
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
authority. Under CD-0058 there is no longer an in-repo drift validator for the
harness, so a lane addition, retirement, or digest change can drift from the
configuration without failing CI. The harness itself is updated by hand and is
treated as advisory infrastructure rather than a Concord machine check.
