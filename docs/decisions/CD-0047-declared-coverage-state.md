# CD-0047: Coverage is declared, and an undeclared gap is a failure

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** What counts as proof that accepted law is implemented; what counts
  as proof that store mechanism is reachable; the shared state vocabulary both
  use; issue #219
- **Approval:** Operator accepted the drafted decision on 2026-08-20; the public
  record is [issue #219](https://github.com/Sharper-Flow/concord/issues/219)
- **Related:** CD-0026 (learning capture and the drift audit), CD-0021 (floor
  condition scope), CD-0010 (pre-readiness development authority), CD-0013 D11
  (reserved workflow action surface),
  [`floor-readiness.md`](../floor-readiness.md),
  [`predecessor-operational-coverage.md`](../predecessor-operational-coverage.md)
- **Preserves:** the floor-readiness state vocabulary and its state-conditional
  obligations; the knowledge-index sha256 record integrity; CD-0013 D11's
  reserved surface; the closed-registry and digest mechanisms
- **Supersedes:** nothing

## Context

Two mechanisms in this repository state a purpose and check something adjacent
to it.

`scripts/check-knowledge-index.py:339-341` says it fires "before the recorded
law and the current implementation drift apart silently." The check eleven
lines below is `target.is_file()`. Path existence. An unrelated or vacuous file
satisfies the same structural shape. The `sha256` pin beside it proves the
document has not changed, which is a different proposition: code drifts away
from a frozen document and every hash still verifies.

The result is measurable. Of the 41 CD records this decision inherits, one
asserts its own body content —
CD-0014, via `internal/launcher/dependency_inventory_test.go:545-573`. The
other 40 can be rewritten, or contradicted by implementation, with the full
suite green. `docs/workflow-engine-contract.md` documents `edge_class`,
`severity`, and `edge_kind` vocabularies that
`contracts/workflow-engine-scenarios.schema.json` closes independently, and
nothing compares them. `docs/terminal-launcher-contract.md` is excluded from
knowledge-index record paths, so it carries no hash either.

The second mechanism is absent rather than weak. `go run
golang.org/x/tools/cmd/deadcode -test=false ./cmd/...` reports 128 functions
unreachable from the CLI entrypoint, 111 of them in `internal/store`.
`cmd/concord` is the complete entry set — the OpenCode adapter reaches Go only
by executing the binary — so those functions cannot be invoked by any operator
or agent action.

Some of that is correct. CD-0013 D11 keeps `workflow_action` reserved until the
engine ships, so `WorkflowActionAvailable` being unreachable is accepted law
working as intended. Some of it is not: `Backup`, `VerifyBackup`, and
`RestoreBackup` are unreachable while #187 requires a black-box acceptance path
covering backup and restore. Nothing distinguishes the two, so neither is
visible.

Both gaps have one cause. CD-0010 forbids Concord from coordinating its own
development before replacement readiness, so the system that would meter
work-in-flight is the system being built. Law accretes at decision speed,
mechanism accretes at implementation speed, and neither is metered against
reachable product.

This decision does not treat the resulting inventory as waste. Most store
mechanism not reachable from the product is exercised by store tests — built
and proven, not connected. The defect is that the disconnection is undeclared,
so its size is discoverable only by someone who thinks to measure it.

## Decision

### D1. An undeclared gap is a failure; a declared gap is not

For each governed plane the validator establishes the complete subject set
itself, rather than iterating whatever the manifest happens to list. Every
subject in that set must carry a declared state. A subject the manifest does
not mention is a finding.

This is the property neither existing mechanism has. `check-knowledge-index.py`
iterates the records the manifest declares, so a CD that is simply absent is
invisible. A coverage manifest that can omit its hard cases proves nothing, and
would be a third mechanism whose stated purpose exceeds its guarantee.

The inverse is equally a finding. A declared exception that has become covered
is stale, and stale exceptions silently widen over time.

### D2. One state vocabulary, reused from the floor manifest

Both planes use the state vocabulary and state-conditional obligations already
accepted in `contracts/floor-readiness.schema.json`: `satisfied`,
`outstanding`, `unmeasured`, `out_of_scope`, with `evidence` required when
satisfied and forbidden otherwise, `issue` required when outstanding, and
`reason` required for `unmeasured` and `out_of_scope`.

The planes are not symmetrical and this decision does not pretend otherwise —
what differs is how a `satisfied` claim is proved, not what the states mean.
Inventing a second enum for the same idea would reproduce, in the mechanism
built to close drift, the drift it exists to close.

`out_of_scope` is what makes reserved mechanism legal. CD-0013 D11's reserved
surface is declared, not excused. `outstanding` is what makes an unbound
decision legal while it is being bound: prose-only law stays legal when it is
declared prose-only. The failure mode is the undeclared case, never the case
itself.

### D3. Law evidence is a typed anchor, never a path

A `satisfied` law record cites one or more anchors from a closed set. Each
anchor must resolve, and each executable anchor must be reached by a required
check:

| Kind | Value | Resolution |
|---|---|---|
| `go_test` | `package.TestName` | the test exists and its package is covered by `go test ./...` |
| `scenario` | corpus scenario ID | the ID exists in a `scenarios/` corpus **and no harness defers it** |
| `validator` | `scripts/check-*.py` | the script exists and CI invokes it, directly or nested through `check-json.py` |
| `generated` | symbol in a `DO NOT EDIT` artifact | the symbol exists in an artifact carrying the generator marker |

A scenario that sits in a corpus while a harness records it as deferred is
executed by nothing. Accepting one would let presence stand in for enforcement
inside the mechanism built to separate them, so the resolver reads the same
deferral registry the corpus test reads. Three `AJ8` scenarios are deferred
today, and one of them was proposed as evidence during the initial seeding.

A repository path is not an anchor. Paths are what the current drift audit
accepts, and accepting them here would restate the defect in new syntax.

The set is closed and stays minimal: a kind with no use is vocabulary nobody
checks. A drafted `schema_pointer` kind was removed before this decision landed
because no record needed it, and it can return with its first real use.

### D4. Reachability is computed; only exceptions are declared

Law binding is a human claim — which test proves which decision cannot be
derived. Reachability is not: `deadcode` derives it from a real callgraph in
roughly eleven seconds, and does so correctly for the method plus
tx-scoped-core pairs throughout `internal/store` that defeat textual analysis.

So the code plane declares only its exceptions. The validator runs the
analysis, subtracts the declared set, and fails on any remainder. There is no
per-symbol manifest, no maintenance burden as the store grows, and no way for
the manifest to disagree with the code.

An exception may cover several symbols under one reason where a single accepted
decision governs them, because that is how the reasons actually cluster.

`deadcode` runs as a pinned `go run`, adding no module dependency —
the precedent `.github/workflows/ci.yml:56` already sets for `govulncheck`.

### D5. Declaring is not binding, and this decision only requires declaring

Landing this mechanism does not require binding every CD record. It requires
every record to carry an honest state, which for most of them is `outstanding`
against #219 or `unmeasured` with a reason.

Binding law to tests before the first-usable floor is reached would freeze
decisions at their least-informed moment, and CD-0010 means the floor is the
event that lets Concord meter itself at all. The manifest is what stops the gap
growing while that work proceeds; closing the gap is incremental and follows
the floor rather than preceding it.

## Consequences

1. The size of both gaps becomes a CI-visible number rather than the result of
   an audit someone chose to run.
2. Adding an exported function that no CLI path reaches requires declaring why,
   at the moment it is added, when the reason is still known.
3. Accepting a new CD requires declaring how it will be proved, or declaring
   that it is not yet proved and naming the issue.
4. `deadcode` output is pinned by version. A toolchain upgrade that changes the
   analysis surfaces as a manifest diff, which is the correct place to see it.
5. #187 and #181 become instances of this mechanism rather than separate
   arguments about evidence strength.
6. The initial seeding is a large, mostly mechanical diff. It is also the first
   honest inventory of both gaps, and reviewing it is the point rather than a
   cost of it.

## Rejected alternatives

**Bind every CD record before landing the mechanism.** Rejected under D5.
The mechanism's value is that it stops accretion; gating it on the retrofit
delays the stop and freezes law at its least-informed moment.

**A per-symbol reachability manifest, symmetrical with the law manifest.**
Rejected under D4. It would require an entry per exported function, drift from
the code on every refactor, and restate a fact the toolchain derives.

**Separate state vocabularies per plane.** Rejected under D2. Two enums for one
idea is the drift this decision exists to close, appearing inside its own
mechanism.

**Strengthen `check-knowledge-index.py` in place.** Rejected: its job is record
integrity — that the manifest describes real, unmodified documents — and it
does that job correctly. Coverage is a different proposition and conflating
them would leave neither clearly owned.

**Extend the sha256 pin to cover implementation files.** Rejected: it would
freeze implementation against documents rather than proving correspondence, and
would fail on every unrelated edit while still not proving the document is true.

## Verification

- The law manifest is schema-validated, and every anchor kind in D3 resolves.
- A test asserts an unresolvable anchor of each kind is rejected.
- A test asserts a CD record absent from the manifest is a finding (D1).
- A test asserts a declared exception that has become covered is a finding (D1).
- The reachability step fails on an undeclared unreachable function, and on a
  declared exception that is now reachable.
- Both validators run in `.github/workflows/ci.yml`.
