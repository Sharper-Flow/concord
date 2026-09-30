# CD-0081: The registry gains a design worker lane

- **Status:** Accepted
- **Date:** 2026-08-29
- **Scope:** The closed lane registry; the lane evidence obligation
  vocabulary; the generated lane projections; the installer agent file list;
  the advisory lane evals; issue
  [#572](https://github.com/Sharper-Flow/concord/issues/572)
- **Approval:** Operator directed the change on 2026-08-29 and selected the
  `visual_artifacts` obligation route over reusing the implement set
- **Related:** CD-0017 (amends the closed lane set), CD-0056 (amends the D2
  vocabulary), CD-0058 (model resolution stays with the host), CD-0070 (the
  new lane definition inherits `hidden: true` by generation)
- **Preserves:** the worker authority boundary in full;
  `agent-lane-packet.v1` and `agent-lane-report.v1` at `schema_version` 1.0;
  the four pre-existing lane digests
- **Supersedes:** nothing

## Context

The registry holds four lanes: research, implement, review, verify. Visual
UI and UX work has no typed worker. Routing that work through `implement`
hides the difference between general engineering and design judgment.

Implement's evidence contract also cannot express visual evidence. Its
obligations are `files_touched`, `verification_commands`, and
`unresolved_issues`. None of them records what the worker inspected.

Issue #572 records the operator direction. The operator chose a new
obligation token over reusing the implement set, so a design completion must
cite visual evidence, not only commands.

## Decision

### D1. The registry admits a fifth lane

The closed ordered set becomes research, implement, design, review, verify.
The `design` lane declares:

- purpose: implement visual UI and UX changes for one approved bounded task
  and report design evidence;
- capability class `design`;
- capabilities `read_repository`, `edit_scoped_files`,
  `run_targeted_checks`, `report_evidence`;
- budgets matching the implement ceilings: `cost_usd_max` 4,
  `context_tokens_max` 48000, `time_seconds_max` 1800;
- obligations `files_touched`, `verification_commands`, `visual_artifacts`,
  `unresolved_issues`.

The lane binds the registered packet and report schemas at version 1.0 and
the standard dispatched, completed, failed lifecycle.

### D2. The obligation vocabulary gains `visual_artifacts`

The CD-0056 D2 closed enum gains `visual_artifacts`. The token joins
`contracts/agent-lanes.schema.json`,
`contracts/agent-lane-report.schema.json`, the Go vocabulary slice, and the
`worker-complete` CLI spec. `scripts/check-agent-contracts.py` keeps proving
the two enums identical and every lane's obligations a subset.

### D3. The manifest digest moves and the existing lane digests do not

The manifest digest covers the whole manifest, so admitting a lane moves it.
A lane digest covers only its own definition, so the four pre-existing
digests stay byte-identical. No persisted worker event can name the design
lane, so the registry needs no legacy digest entry for it.

### D4. Model resolution stays with the host

CD-0058 applies unchanged. The repository declares no model for the lane. A
host that wants a specific model configures `agent.concord-design.model`
through its own routing chain, exactly as for the other four lanes.

## Invariants

1. The registry is the closed ordered set research, implement, design,
   review, verify.
2. A design completion that leaves `visual_artifacts` undischarged is
   refused under CD-0056 D4.
3. The four pre-existing lane digests equal their CD-0056 values.
4. The generated `concord-design.md` stays hidden from the operator agent
   cycle under CD-0070.

## Consequences

- Every lane projection regenerates: Go, TypeScript, agent definitions,
  docs, manifest digest, eval packet digests, knowledge index.
- The installer ships `concord-design.md`.
- The advisory evals gain two design packets, one bounded and one
  authority-refusing.
- Consuming projects regenerate their lane definitions to receive the lane.

## Verification

- `scripts/generate-agent-lanes.py --check` passes on the five-lane
  registry.
- `scripts/check-agent-contracts.py` proves the obligation join on the
  extended vocabulary.
- `go test ./internal/store ./cmd/concord` passes with the design lane
  registered and its digest pinned.
- CI passes.
