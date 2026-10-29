# Concord first-usable floor readiness

The tracked [`floor-readiness.v1.json`](./floor-readiness.v1.json) records the
validated state of each first-usable floor condition. The
companion [`floor-readiness.schema.json`](../../contracts/floor-readiness.schema.json)
is the closed JSON Schema contract, and `scripts/check-floor-readiness.py`
validates the manifest on every CI run through `scripts/check-json.py`.

The floor itself is defined in two places: the *First-usable floor* section
below (the six usability-floor conditions) and
[`rollout-plan.md`](./rollout-plan.md) under *Release-evidence bar* (the
release/install/privacy/Linux amd64 bar this manifest names `fc7`). A condition
may carry its own `source`, overriding the manifest-level default.

The manifest copies its conditions and never redefines them. The checker requires each condition's title to
equal the first sentence of the corresponding item in its source section, one to
one and in order. A dropped, added, reordered, or reworded condition fails, and a
source section that cannot be resolved fails rather than passing vacuously.

## First-usable floor

CI validates the manifest, and the alpha rung of
[`maturity-readiness.v1.json`](./maturity-readiness.v1.json) requires the floor
to stay met. Concord's current direction is in [`goals.md`](./goals.md) and
[`priorities.md`](./priorities.md). The manifest copies the six condition titles below.

1. The operator can see, plan, and act across the full Product scope through Product → Domain context. Per CD-0021, "across the full Product scope" means every Product is reachable, not that a result set spans Products. The host starts operator sessions, and agents enter managed work through `concord_work_start` under [CD-0219 D4](./decisions/CD-0219-the-terminal-launcher-tui-is-retired.md#d4-kept-clauses-keep-their-surviving-owners). Planning stays in Linear through the Linear MCP server under [CD-0213 D1](./decisions/CD-0213-linear-mcp-is-the-only-planning-authority.md#d1-linear-is-the-only-planning-authority); managed work creation keeps one write authority.
2. Agents can read, write, and execute within that Product and Domain context through tools.
3. Every supported workflow family (implementation, break-fix, research) has a defined workflow type and completion evidence.
4. Required authority/freshness is explicit and CD-0006 R3's accepted cross-workflow impact policy is enforced.
5. Concord-owned data and workflow boundaries preserve data safety (bounded writer admission, no hand-repair, safe evolution).
6. The Advance surfaces that Concord consumes are stable enough to serve as evidence, and Concord can replace them without losing operational coverage.

## Why it is a manifest

The floor is the gate for a replacement-readiness claim. A checklist that asserts its own
authority drifts silently.

Authority here comes from mechanical checking rather than assertion. The
validator rejects a satisfied item whose evidence does not resolve to an
executable check, an outstanding item with no tracking issue, and an
unmeasured item with no reason. A floor condition cannot silently carry no
items at all. The validator rejects a satisfied item whose evidence is anything other than an anchor
that resolves and — for executable anchors — that a required workflow
actually invokes.

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
manifest alone constitutes a replacement-readiness claim: a claim requires every
condition satisfied, and satisfaction is judged against each condition's
source section, not against the manifest.

This manifest is the authorizing readiness record and cites evidence rather than
surveying capability.

It does not change the development-authority model in
[`managed-development-authority.md`](./managed-development-authority.md): Linear
through the Linear MCP server owns planning, and pull requests with required
checks own review and merge evidence.
