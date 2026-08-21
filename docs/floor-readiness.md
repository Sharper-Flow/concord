# Concord first-usable floor readiness

The tracked [`floor-readiness.v1.json`](./floor-readiness.v1.json) is the
authorizing record of Concord's distance from the first-usable floor. The
companion [`floor-readiness.schema.json`](../contracts/floor-readiness.schema.json)
is the closed JSON Schema contract, and `scripts/check-floor-readiness.py`
validates the manifest on every CI run through `scripts/check-json.py`.

The floor itself is defined in two documents:
[`priorities.md`](./priorities.md) under *First-usable floor* (the six
usability-floor conditions) and [`rollout-plan.md`](./rollout-plan.md) under
*Release-evidence bar* (the release/install/privacy/Linux amd64 bar this
manifest names `fc7`). A condition may carry its own `source`, overriding the
manifest-level default.

The manifest copies its conditions and never redefines them. That was previously
an assertion; the checker now enforces it, requiring each condition's title to
equal the first sentence of the corresponding item in its source section, one to
one and in order. A dropped, added, reordered, or reworded condition fails, and a
source section that cannot be resolved fails rather than passing vacuously.

## Why it is a manifest

The floor is the gate on migration, and until this manifest existed nothing
recorded distance from it. Prose could not: a checklist that asserts its own
authority drifts silently, which is the failure
[`feature-inventory.md`](./feature-inventory.md) avoids by declaring itself
non-authorizing.

Authority here comes from mechanical checking rather than assertion. The
validator rejects a satisfied item whose evidence does not resolve to an
executable check, an outstanding item with no tracking issue, and an
unmeasured item with no reason. A floor condition cannot silently carry no
items at all. The first run of the validator against the first draft of the
manifest rejected a fabricated decision-record path; the schema-2.0 tightening
now rejects a satisfied item whose evidence is anything other than an anchor
that resolves and — for executable anchors — that a required workflow
actually invokes.

## Contract

The manifest has only `schema_version`, `source`, `conditions`, and `items`.
Unknown fields, duplicate keys, duplicate IDs, and duplicate evidence entries
are invalid.

A condition has a closed `fcN` identifier, an ordinal that must agree with that
identifier, and a title. Ordinals are contiguous from 1. Every declared
condition must carry at least one item.

An item has an identifier prefixed by its condition, the condition it belongs
to, a title, a requirement, and a state. The requirement is written so it can be
checked rather than agreed with. State-dependent fields are exclusive:

| State | Requires | Forbids |
|---|---|---|
| `satisfied` | at least one executable anchor that resolves and — for `validator` anchors — is invoked by a required workflow | `issue`, `reason` |
| `outstanding` | a public tracking issue number | `evidence`, `reason` |
| `unmeasured` | a reason | `evidence`, `issue` |
| `out_of_scope` | a reason | `evidence`, `issue` |

A satisfied item's `evidence` is a bounded array of anchor objects. Each
anchor carries a closed `{kind, value}` pair from the closed set `{go_test,
scenario, validator, generated}` and is resolved by the shared
`scripts/evidence_anchors.py` machinery — the same machinery the
law-coverage plane uses, so the proof layer is single-sourced. A `go_test`
anchor names `<package>.<TestName>` and resolves when the test is declared in
that package (a required CI step runs `go test ./...`). A `scenario` anchor
names a corpus id and resolves when the id appears in a corpus file under
`scenarios/` and nothing in `internal/` defers it. A `validator` anchor names
`scripts/(check|test)-<name>.py` and resolves when the script exists and a
required workflow (`.github/workflows/ci.yml` or
`.github/workflows/release.yml`) invokes it directly or via
`scripts/check-json.py`. A `generated` anchor names `<path>#<symbol>` and
resolves when the symbol lives in a file whose leading comment carries the
generator marker.

Paths are not evidence: a satisfied item cannot be established from a cited
repository path alone. Issue numbers are validated as bounded integers, not
resolved over the network; CI performs no lookups.

## Reading the states

`outstanding` and `unmeasured` are different claims and the distinction is the
point of the instrument.

**`outstanding`** means the requirement has been checked and is not met. The
gap is understood and tracked.

**`unmeasured`** means nobody has established whether the requirement is met.
It is not a softer form of outstanding. A condition full of `unmeasured` items
is not close to satisfied — it is unassessed, which is a worse position to be in
without knowing it.

**`out_of_scope`** means the requirement does not apply and records why.
Omission is never an acceptable substitute, because a silently dropped item
understates the gap.

Running the checker prints the tally:

```sh
python3 scripts/check-floor-readiness.py
```

## What this is not

It is an instrument, not a plan. It records state and does not sequence work,
assign effort, set dates, or authorize implementation. Nothing about the
manifest constitutes a replacement-readiness claim: a claim requires every
condition satisfied, and satisfaction is judged against
[`priorities.md`](./priorities.md), not against this file.

It does not replace [`feature-inventory.md`](./feature-inventory.md). That
document remains a non-authorizing capability-evidence map organized by
lineage. This manifest is the authorizing readiness record and cites evidence
rather than surveying capability.

It does not change the development-authority model in
[`development-authority.md`](./development-authority.md); public issues and
pull requests continue to own planned work and merge evidence.
