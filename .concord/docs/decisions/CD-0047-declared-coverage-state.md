# CD-0047: Coverage is declared, and an undeclared gap is a failure

- **Status:** Accepted
- **Date:** 2026-08-20
- **Scope:** What counts as proof that accepted law is implemented; what counts
  as proof that store mechanism is reachable; the shared state vocabulary;
  removal of dead code, instructions, and outdated documentation
- **Approval:** Operator accepted the drafted decision on 2026-08-20; the public
  record is [issue #219](https://github.com/Sharper-Flow/concord/issues/219).
  The operator approved the exact amendment text for
  [Concord (CON) issue 1164](https://linear.app/sharper-flow/issue/CON-1164).
- **Related:** CD-0026 (learning capture and the drift audit), CD-0021 (floor
  condition scope), CD-0010 (pre-readiness development authority), CD-0013 D11
  (reserved workflow action surface), CD-0036 (law supersession), CD-0055
  (structural enforcement), CD-0222 D3 (law-graph pruning),
  [`floor-readiness.md`](../floor-readiness.md),
  [`predecessor-operational-coverage.md`](../predecessor-operational-coverage.md)
- **Preserves:** the coverage state vocabulary and its state-conditional
  obligations; the knowledge-index sha256 record integrity; CD-0013 D11's
  reserved surface; the closed-registry and digest mechanisms

## Context

Record integrity and implementation evidence answer different questions. A hash
proves that a document has not changed; it does not prove that the implementation
still meets the document. Typed executable anchors carry that second claim.

`cmd/concord` is the runtime entry set. The OpenCode adapter reaches Go through
the binary. Callgraph analysis distinguishes runtime-reachable functions from
test seams, build-time accessors, and unused code; declared exceptions explain
current obligations outside that entry set.

Both planes must inventory their full subject set. Checking only declared rows
hides omissions, while keeping superseded code and prose hides which design is
current. The repository needs evidence for what it keeps and removal of what
no longer serves the accepted design.

## Decision

### D1. An undeclared gap is a failure; a declared gap is not

For each governed plane the validator establishes the complete subject set
itself, rather than iterating whatever the manifest happens to list. Every
subject in that set must carry a declared state. A subject the manifest does
not mention is a finding.

A coverage manifest that can omit its hard cases proves nothing. Record
integrity alone cannot establish the complete subject set.

The inverse is equally a finding. A declared exception that has become covered
is stale, and stale exceptions silently widen over time.

### D2. One state vocabulary

Both planes use one state vocabulary and its state-conditional obligations,
defined once in `scripts/coverage_state.py`: `satisfied`,
`outstanding`, `unmeasured`, `out_of_scope`, with `evidence` required when
satisfied and forbidden otherwise, `issue` required when outstanding, and
`reason` required for `unmeasured` and `out_of_scope`.

The planes are not symmetrical and this decision does not pretend otherwise —
what differs is how a `satisfied` claim is proved, not what the states mean.
Inventing a second enum for the same idea would reproduce, in the mechanism
built to close drift, the drift it exists to close.

`out_of_scope` declares a current obligation outside the analysis entry set.
It does not authorize dead code. `outstanding` is what makes an unbound
decision legal while it is being bound: prose-only law stays legal when it is
declared prose-only. The failure mode is the undeclared case, never the case
itself.

### D3. Law evidence is a typed anchor, never a path

A `satisfied` law record cites one or more anchors from a closed set. Each
anchor must resolve, and each executable anchor must be reached by a required
check:

| Kind | Value | Resolution |
|---|---|---|
| `go_test` | `<package>.TestName` | the test exists and its package is covered by `go test ./...` |
| `scenario` | corpus scenario ID | the ID exists in a `.concord/scenarios/` corpus **and no harness defers it** |
| `validator` | `scripts/check-*.py` | the script exists and CI invokes it, directly or nested through `check-json.py` |
| `generated` | symbol in a `DO NOT EDIT` artifact | the symbol exists in an artifact carrying the generator marker |

A scenario that sits in a corpus while a harness records it as deferred is
executed by nothing. Accepting one would let presence stand in for enforcement
inside the mechanism built to separate them, so the resolver reads the same
deferral registry the corpus test reads.

A repository path is not an anchor. Presence alone does not prove behavior.

The set is closed and stays minimal: a kind with no use is vocabulary nobody
checks. A new kind requires a current use and a resolver.

### D4. Reachability is computed; only exceptions are declared

Law binding is a human claim — which test proves which decision cannot be
derived. Reachability is not: `deadcode` derives it from a real callgraph,
including the method plus
tx-scoped-core pairs throughout `internal/store` that defeat textual analysis.

So the code plane declares only its exceptions. The validator runs the
analysis, subtracts the declared set, and fails on any remainder. There is no
per-symbol manifest, no maintenance burden as the store grows, and no way for
the manifest to disagree with the code.

An exception may cover several symbols under one reason where a single accepted
decision governs them, because that is how the reasons actually cluster.

`deadcode` runs as a pinned `go run`, adding no module dependency, as the
pinned `govulncheck` run in `.github/workflows/nightly.yml` does.

### D5. Declaring is not binding, and this decision only requires declaring

Landing this mechanism does not require binding every CD record. It requires
every record to carry an honest state, which for most of them is `outstanding`
against #219 or `unmeasured` with a reason.

Binding law to tests before the first-usable floor is reached would freeze
decisions at their least-informed moment, and CD-0010 means the floor is the
event that lets Concord meter itself at all. The manifest is what stops the gap
growing while that work proceeds; closing the gap is incremental and follows
the floor rather than preceding it.

### D6. Dead and superseded material has zero tolerance

Concord keeps only material that serves its current accepted design. This rule
covers production code, tests, fixtures, scripts, instructions, generated agents,
documentation, and law records. The change that replaces a mechanism removes
its unused branches, aliases, instructions, and descriptions in the same change.
Git history holds replaced designs; a retained file is not an archive.

A test or fixture stays when it proves current behavior, including rejection of
retired input. Remove obsolete expectations, not assertions that still protect
the current contract. Never delete validation, error handling, or observability
merely to reduce the finding count. Generated artifacts change through their
source and generator, not by hand.

Law keeps one canonical current statement per obligation. A compatible amendment
edits the owning record in place and states only the current rule. A breaking
replacement keeps the old record as `superseded` because CD-0036's
dependence-scoped staleness and
CD-0222 D3 read that supersession (a revision invalidates only what depended on it ([CD-0242 D7](CD-0242-knowledge-first-workflows-keep-current-state-not-history.md#d7-a-revision-invalidates-only-what-depended-on-it))); the record leaves the current-law view, and
its callers and citations move to the successor. A non-law document that claims
to describe the current design but has been fully replaced is deleted with its
manifest and coverage shards. Git history holds its earlier text. Historical
research and lessons that still serve current evidence are not obsolete design
descriptions; their historical text is not rewritten under CD-0222 D1.

Required checks refuse mechanically established gaps: undeclared unreachable Go
functions and packages, unprocessed knowledge documents, missing cited files or
Go tests, and drift from generated registries.
The citation resolver covers accepted law, published knowledge, and shipped
instructions. The installer tests bind the shipped instruction list to
`.concord/instructions/`; the lane generator refuses drift in each agent
definition it projects and any `.opencode/agents/` file it does not project.
These are structural or real-tool
checks under CD-0055, not judgments based on words such as legacy or obsolete.

A declared reachability exception is valid only for a current obligation outside
the CLI callgraph, such as a used test seam, a generator, or accepted audit
reconstruction. A hypothetical user, an old fixture, or the effort of deletion
does not justify retaining dead code. D1 still rejects stale exceptions.

Review decides whether prose describes the current design and whether a retained
mechanism still has a purpose. CI does not claim to infer either from keywords.
An existing cleanup that cannot land safely with the enforcement change receives
an owned Linear follow-up naming the exact finding and its removal end state.
That follow-up does not waive a required check or excuse new dead material.

## Consequences

1. The size of both gaps becomes a CI-visible number rather than the result of
   an audit someone chose to run.
2. Adding an exported function that no CLI path reaches requires declaring why,
   at the moment it is added, when the reason is still known.
3. Accepting a new CD requires declaring how it will be proved, or declaring
   that it is not yet proved and naming the issue.
4. `deadcode` output is pinned by version. A toolchain upgrade that changes the
   analysis surfaces as a manifest diff, which is the correct place to see it.
5. A change that replaces code, an instruction, or a document carries the
   removal of what it replaced; a later cleanup round is not the default path.
6. A citation in current law, published knowledge, or a shipped instruction to
   a deleted file or Go test fails the required doc contract.

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
- `scripts/check-knowledge-closure.py` refuses unprocessed documents in strict
  mode.
- `scripts/check-doc-contract.py` resolves current citations in accepted law,
  published knowledge, and shipped instructions; `scripts/test-doc-contract.py`
  proves the refusal and the passing counterpart for each population.
- `scripts/generate-agent-lanes.py --check` refuses unprojected agent definitions;
  `scripts/test-generate-agent-lanes.py` proves the refusal and the passing
  counterpart in `AgentDirectoryInventoryTests`.
