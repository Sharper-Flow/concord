# CD-0043: Lane methodology is host-owned and provenance-recorded

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** Ownership of worker lane methodology; the legal channel by which it
  reaches a dispatch; the `skills/` boundary; issue #210
- **Approval:** Operator accepted the drafted decision as written on 2026-08-20; the
  public record is
  [issue #210 comment](https://github.com/Sharper-Flow/concord/issues/210#issuecomment-5359122989)
- **Related:** CD-0017 (D1, D4, D7), CD-0034,
  [`capability-placement.md`](../capability-placement.md) §3–§4,
  [`predecessor-operational-coverage.md`](../predecessor-operational-coverage.md) §3–§4
- **Preserves:** The closed lane registry and its digest; `agent-lane-packet.v1` and
  `agent-lane-report.v1`; CD-0034 provenance recording; CD-0017 D7's evaluation
  boundary
- **Supersedes:** nothing

## Context

The lane registry is structurally complete. Four lanes are closed, versioned, and
digest-pinned; each pins a model; packet and report schemas are strict; dispatch is
fenced; the worker authority boundary is proved by negative tests; a validator fails
on drift between the registry and its evaluation harness.

The generated lane agent definitions are deliberately thin. They restate the lane
purpose, the packet and report contract, the refusal boundary, and the evidence
obligations. They carry no procedure — no review dimensions, no verification rubric,
no inspection method for a rendered surface.

That absence was intended. What was never decided is where the procedure lives
instead.

[`predecessor-operational-coverage.md`](../predecessor-operational-coverage.md)
defers it three times. The quality-scanner row, the visual-review row, and the
opportunity-scouting row each hand methodology to "a skill" without naming an owner.
A fourth row already excludes behavioural methodology as host-owned. `skills/` ships
in every release containing one README that defers indefinitely.

So the repository states that a capability exists somewhere else, ships an empty
directory shaped like that somewhere else, and has no check that could notice the
difference. `check-predecessor-coverage.py` validates that a covered outcome names an
existing path and an excluded outcome carries a reason. It cannot detect a reason
pointing at an unbuilt surface.

## Decision

### D1. Lane methodology is never Concord durable state

Concord owns the lane contract, the evidence boundary, and the provenance record. It
does not own the procedure a worker follows inside its lane.

A lane definition declares what a worker is for, what it must produce, what it may
not do, and which model runs it. How a reviewer decides what to look at, how a
verifier chooses which commands constitute proof, how an implementer sequences a
bounded task — these are methodology. They are volatile, host-coupled, and carry no
Product truth. Placing them in durable coordination state would make Concord the
owner of prose it cannot verify and would age exactly as
[`capability-placement.md`](../capability-placement.md) §5 warns.

This extends the split CD-0017 D4 already draws. That decision separated worker
labor from workflow authority. This one separates worker method from worker contract.

### D2. `CONCORD_HOST_INSTRUCTIONS` is the sole legal channel

Methodology reaches a worker only as a host prompt surface, through the enumerable
instruction path CD-0034 declared. The adapter binds each file with its kind, path,
and content hash, and folds it into the `host_provenance` total digest.

Methodology delivered through any other route is invisible drift. CD-0034 already
refuses v3 dispatch evidence without `host_provenance`, so the emitter gate is the
enforcement point and no new mechanism is required.

Surfaces the adapter cannot enumerate remain recorded by name as `unenumerated`.
Methodology deliberately placed in such a surface is therefore visible as
present-but-unbound rather than silently absent — a weaker record, and a reason to
prefer the enumerable path.

### D3. `skills/` stays reserved

Concord ships no skill. `skills/README.md` records this as a closed reason rather
than an open deferral. The directory stays packaged so a future accepted decision can
populate it without changing release shape, but its emptiness is now a decided state
rather than an unfinished one.

### D4. The coverage rows name the owner

The rows that defer methodology are amended to state that the deferred capability is
host-owned. The row already excluding behavioural methodology is the governing
precedent, not an exception to it. A reader can then tell, from the coverage record
alone, that Concord owes nothing further.

## Consequences

- Methodology is not versioned with the lane registry. It is hashed per dispatch.
- A methodology change moves the `host_provenance` digest while the lane digest
  holds. This is the visibility CD-0034 was accepted to produce, now carrying real
  content rather than only operator safety policy.
- A D7 evaluation baseline is reproducible only against both digests. Methodology
  authored after a baseline invalidates that baseline by construction, so the
  baseline in issue #212 must be taken after methodology is in place.
- Lane behaviour is not reproducible from the release artifact alone. Reproducing a
  run requires the recorded provenance manifest as well as the release. This is the
  accepted cost of D1.
- `contracts/agent-lanes.v1.json` and `contracts/agent-lane-packet.schema.json` are
  unchanged by this decision. No version moves and no digest churns.

## Rejected alternatives

**Concord-shipped skills.** Populating `skills/` would make lane behaviour
reproducible from the release artifact alone, which is a real gain. Rejected because
it contradicts the accepted exclusion of behavioural methodology as host-owned, and
because it requires a skill-versioning regime — authorship rules, digest pinning,
compatibility with a lane registry that versions independently — with no current
consumer to justify it. A later decision may reopen this if reproducibility from the
artifact becomes a stated requirement.

**A `methodology` field in the lane packet.** Rejected because it bumps a closed
schema to carry content the provenance manifest already binds, and because packet
content is per-attempt while methodology is per-configuration. The two have different
lifetimes and should not share a field.

**Fatten the generated lane definitions.** Rejected because it puts hand-authored
prose under `DO NOT EDIT` codegen. The generator would either overwrite the prose or
stop being the source of truth for the file.

**Split by shape.** Lane-invariant methodology as Concord skills, host-specific
policy as host instructions. Rejected because it creates two owners for one prompt
surface and needs a further rule to keep the boundary stable, with no current case
requiring the split.

## Verification

- `docs/decisions/CD-0043-host-owned-lane-methodology.md` exists with status
  `accepted` and is indexed exactly once in `docs/concord-knowledge-index.v1.json`.
- `skills/` contains only `README.md`.
- `contracts/agent-lane-packet.schema.json` gains no property.
- `contracts/agent-lanes.v1.json` keeps its current version, and
  `contracts/agent-lanes.digest` is unchanged.
- `python3 scripts/check-json.py`, `python3 scripts/check-doc-links.py`, and
  `python3 scripts/check-public-content.py` pass.
