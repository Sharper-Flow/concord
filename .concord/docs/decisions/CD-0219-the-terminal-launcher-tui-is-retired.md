# CD-0219: The interactive terminal launcher is retired

- **Status:** Accepted
- **Date:** 2026-10-09
- **Scope:** The interactive terminal launcher TUI — the `concord launcher`
  verb, the bare-invocation TUI route, and the internal/launcher tree with
  its Bubble Tea renderer — together with the launcher contracts, the launcher
  portfolio scenario, and the obligations that retire with that surface
- **Approval:** The operator pre-approved this law change for Linear
  [Concord (CON) issue 908](https://linear.app/sharper-flow/issue/CON-908) through the Snowball
  coordinator (operator-delegated) on 2026-10-09 at 21:54 Eastern Daylight Time (EDT).
  The public pull request
  carries its delivery evidence. This record creates no managed workflow
  approval record. Its authority tier is `derived` under CD-0159 D3.
- **Amended by:** [CD-0242](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md) D2
- **Related:** CD-0014, CD-0031, CD-0048, CD-0078, CD-0083, CD-0108, CD-0222,
  CD-0163, CD-0176, C14, C17
- **Amends:** CD-0014 (the rendering stack retires with the TUI); CD-0108 D1
  through D3 (the interactive remake clauses retire; D4 stands); CD-0163
  (the placement boundary re-homes to the session entry route)
- **Amended:** Concord (CON) issue CON-910 (2026-10-13) removes the session entry route this
  record preserved, under the Snowball coordinator's operator-delegated
  approval. D4's kept clauses bind the surviving managed surfaces.
- **Preserves:** CD-0078 D1 through D3, CD-0093, CD-0031, CD-0090, CD-0163
  D1, CD-0176 D3, CD-0098, the store-write-free managed-entry boundary, and
  the verified worktree claim, occupancy, landing, and vacate rules

## Context

Operation Snowball simplifies by deletion. The interactive terminal launcher
is deleted end to end: internal/launcher, the `concord launcher` verb, the
bare-invocation TUI route, and the `charm.land` Bubble Tea, bubbles, and
lipgloss modules. Bare `concord` prints usage.

The entry-route duties this record preserved retired with the entry route.
The launch-layer removal takes `concord zl`, the argv[0] forwarding,
`--resume-last`, `--project`, the `concord session` boot command, the
host-registered opener, and the operator repair launcher. The host starts
and resumes operator sessions, and a host-started session enters managed
work through `concord_work_start` (CD-0098).

## Decision

### D1. The interactive TUI and its dependencies are removed

The internal/launcher tree, the `concord launcher` verb, the bare-invocation
TUI route, the launcher `--list` verb, and the `charm.land` Bubble Tea,
bubbles, and lipgloss modules are removed from the repository and the module
graph. The entry route is non-interactive; bare `concord` prints usage and no
renderer ships.

### D2. The launcher contracts and scenario retire with the surface

`terminal-launcher-contract.md`, `terminal-launcher-replacement-contract.md`,
`terminal-launcher-entry-contract.md`, and
.concord/scenarios/launcher-portfolio.v1.json are deleted. Their
surviving statements keep their owning records: the identity-only handoff
(CD-0031, CD-0163), the no-durable-write boundary (CD-0108 D4), and the bounded
store reads (the store query contracts).

### D3. Obligations whose subject was the TUI are vacated

- CD-0048: the S2 answer-stack clauses are vacated with the screen.
  The store's current-state and query contracts remain unchanged
  ([CD-0242 D2](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d2-the-store-keeps-current-state-and-finite-snapshots-not-history)).
  This deletion introduces no recency column.
- CD-0083: the no-color render anchors and the operator screen-reader
  obligation are vacated with the render surface; the C14 §9
  screen-reader/no-color render condition retires with them. CD-0014's
  validation-failure falsifier retires with CD-0014's rendering decision.
- CD-0222: the Domain graph claims no launcher consumer; the registry
  authoring obligation and the adopted repository navigation stand.
- The launcher-rendered floor items `fc1-portfolio-visibility`,
  `fc1-full-product-scope`, and `fc1-domain-navigation` are out of scope with
  the surface; the `fc1-operator-work-capture` item stands, and
  `fc1-session-handoff` is unmeasured pending the live host-started
  managed-entry witness.

### D4. Kept clauses keep their surviving owners

The session entry route is removed. CD-0078 D1 through D3,
CD-0163 D1, CD-0176 D3, CD-0093, and CD-0031 bind host-started managed
sessions through work start, session-prepare, and the per-turn hook. The
context, identity, registry-probe, and terminal-placement proofs live with
the surfaces that own those behaviors. The worktree claim, occupancy,
landing, and vacate rules are untouched: verified placement, collision
protection, and release-under-live-work refusals keep their owners.

## Alternatives considered

- Delete the verb and keep the TUI. Rejected: the renderer, the store read
  adapter, and the launcher query family exist only for the TUI; a surface
  without its entry path is unreachable code.
- Delete CD-0108, CD-0163, and CD-0176. Rejected: they carry kept
  session-entry law.

## Acceptance criteria

```gherkin
Given the retired launcher artifacts are deleted
When the law planes validate
Then no registered record or coverage anchor cites a retired artifact
And every kept entry-route clause keeps a surviving owner
```

```gherkin
Given the retired surface is gone
When a coverage record names a vacated obligation
Then the record carries an out-of-scope or unmeasured state with its reason
And no record claims satisfaction that the surviving surface does not prove
```

## Consequences

The released binary sheds the Charm dependency surface, the launcher tests,
and the launcher query family. Operator acceptance of the TUI has no subject
after deletion and does not become a new acceptance gate for the entry route.
Coverage for the vacated records carries out-of-scope or unmeasured states
rather than satisfied ones, and the floor manifest records the retired
items the same way. `priorities.md` and `design-constraints.md` own the
constitutional primary-surface direction. This retirement waives no
first-usable floor criterion.

## Verification

- `python3 scripts/check-knowledge-index.py` proves every record matches its
  document and no retired artifact keeps a record.
- `python3 scripts/check-law-coverage.py` proves every satisfied anchor
  resolves and vacated records carry their stated states.
- `python3 scripts/check-doc-links.py` proves no document links a retired
  artifact.
- `python3 scripts/check-floor-readiness.py` proves the retired floor items
  carry out-of-scope reasons and the kept items keep resolving anchors.
- `python3 scripts/check-doc-contract.py`,
  `python3 scripts/check-knowledge-closure.py`, and
  `python3 scripts/check-cd-allocation.py --no-fetch` pass.
- `go test ./internal/store/ -run
  TestResolveContinuityWorkByDirectory` proves the directory binding that
  CD-0093 and CD-0176 keep, and `go test ./cmd/concord/ -run
  TestSessionPrepare` proves the context and probe obligations that CD-0031
  and CD-0189 keep.
