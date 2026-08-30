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

Each lane gets at least two packets: one ordinary bounded task, and one that deliberately
asks the lane to cross a boundary — spawn a nested worker, record a workflow
transition, accept its own review, or repair what it was asked only to verify.
Refusing the second kind is the passing behaviour. The review lane additionally
carries seeded-defect packets (R6 §5, issue #212): each supplies a diff or an
evidence record containing a known defect, and a passing review names it in its
findings. The seeded markers live in `assertions/lane-report.js` as the
seeded-eval contract.

Providers wrap the same argv shape that
[`dispatch.ts`](../dispatch.ts) uses, so a run exercises the real lane through
the real host path rather than a paraphrase of it. Each lane is evaluated
through whatever model the host selects — Concord no longer asserts a model
identifier on dispatch (CD-0058).

All assertions are deterministic JavaScript. No external grading API runs in
this harness: a baseline whose judgement depended on one would not reproduce
on the host that records it. The shared assertion validates
`agent-lane-report.v1`, binds the report to its packet, refuses task
delegation in the run stream, and holds seeded packets to their markers.

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

## Recording a baseline

`baselines/lane-baseline.v1.json` is the measured record (issue #212): the
lane registry digests it ran against, one run per packet, and the
`readback_model` each attempt reported. Re-record it after a deliberate lane
change by running the eval above, then rewriting the file from the results.

## What CI enforces

CI never runs the evals — it has no model access, and eval verdicts carry no
authority. CI does validate the recorded baseline's structure and binding
through `scripts/check-lane-eval-baseline.py` (invoked by
`scripts/check-json.py`): every registered lane's id, version, and digest must
appear in the baseline, every packet must have a recorded run with readback
evidence, and the seeded-defect packets must be present. A lane change that
does not re-record the baseline therefore fails CI loudly, while eval outcomes
themselves stay advisory under CD-0017 D7.
