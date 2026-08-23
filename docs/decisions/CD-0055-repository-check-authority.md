# CD-0055: Repository checks are structural first, and textual guards are declared exceptions

- **Status:** Accepted
- **Date:** 2026-08-21
- **Scope:** what authority a repository check may hold; when a hand-written
  textual guard is legal; the audit of the existing Go-source checks; issue #302
- **Approval:** Operator accepted the drafted decision on 2026-08-21; the public
  record is [issue #302](https://github.com/Sharper-Flow/concord/issues/302)
- **Related:** CD-0047 (declared coverage and the `deadcode` precedent), CD-0014
  D7 (toolchain-parser AST checks in the test suite),
  [`priorities.md`](../priorities.md) P1,
  [`capability-placement.md`](../capability-placement.md) §3
- **Preserves:** the coordination plane's prior exclusion of heuristic
  authority; the invariants `internal/store/txscope_test.go` and
  `internal/store/boundary_test.go` guard; CD-0047's computed reachability
- **Supersedes:** nothing

## Context

The predecessor's validator layer accreted heuristic scanners — code-smell
detection, architectural-consistency inference, textual workflow guards — whose
maintenance tax exceeded their invariant value. The defect is not any one tool;
it is three properties arriving together:

1. the check approximates an invariant that types, tests, or generated digests
   could own;
2. its false positives are appeased — code is reshaped to satisfy the scanner —
   rather than answered structurally;
3. the check grows toward a second implementation of the language (a lexer, a
   parser, a type system) that nobody trusts and everyone works around.

Concord's coordination plane already rejects this pattern.
[`priorities.md`](../priorities.md) P1: "Semantic analysis may suggest duplicate
or conflicting law for operator review; it never owns persistence, supersession,
or blocking." [`capability-placement.md`](../capability-placement.md) §3 keeps
analysis tooling an external native authority. What that law does not cover is
the repository's own CI plane — the validators and tests that gate every merge —
which is where the same accretion would recur, one pragmatic escape hatch at a
time.

The current inventory of Go-source analysis in required checks:

| Check | Shape |
|---|---|
| `scripts/check-reachability.py` | version-pinned `deadcode` callgraph over `./cmd/...` with a declared-exceptions manifest (CD-0047 D4) |
| `internal/launcher` dependency-inventory tests | AST analysis through the toolchain's own parser (CD-0014 D7) |
| `scripts/check-tx-scope.py` | textual guard over the one-connection pool invariant |
| `scripts/check-store-boundary.py` | hand-written Go comment/string masking plus SQL regex parsing |

## Decision

### D1. Structural first

An invariant that Go types, tests, generated contracts with pinned digests, or
schema validation can own is owned there. A repository check that restates such
an invariant is a second opinion with merge-blocking authority — the failure
mode in miniature.

### D2. Real tool over hand-rolled

Where an invariant genuinely needs static analysis, the check wraps a pinned
real tool and declares only its exceptions — the CD-0047 D4 precedent. A check
must not hand-write a lexer or parser for a language the toolchain already
parses: every new escape hatch then grows the hand-rolled front end, which is
the accretion curve.

### D3. Textual guard as a declared exception

A hand-written textual guard is legal only when all of the following hold:

- the failure class is invisible at the boundary it guards — hang, deadlock,
  silent corruption; no error surfaces where the mistake is made;
- the invariant is inexpressible in the structural mechanisms of D1 today;
- the guard stays small and documents its false-positive shape;
- the guard names the structural end-state that retires it.

A textual guard missing any of these is a finding, not a guard.

### D4. Heuristic judgment warns; it never blocks

A check whose verdict is heuristic — similarity, smell, pattern suspicion — may
run and report, and may never be a required check. This is the
coordination-plane rule applied to the repository's own plane, so the two planes
cannot drift apart as the validator set grows. D3 and D4 are distinct: a
deterministic textual rule over source text is a D3 guard with narrow legality;
a probabilistic or judgment-shaped verdict is D4 and never blocks.

### D5. The existing checks are audited against this policy

- `check-reachability.py` is the D2 model and stands.
- The launcher dependency-inventory tests are D2's toolchain-backed shape in
  its structural home — the language's own parser, executed as tests — and
  stand.
- `check-tx-scope.py` was retained under D3 until the structural end-state it
  named existed. That end-state is `internal/store/txscope_test.go`: a
  function receiving a live transaction handle may not hold a `*Store`, every
  `Transact` closure delegates to one transaction-scoped core, and no nil check
  is written against an interface-typed handle, which cannot fire. The guard is
  deleted under D3, not extended.
- `check-store-boundary.py` is replaced under D2, not extended. Its invariant —
  raw SQLite stays inside `internal/store` — is real, but its implementation is
  a growing hand-rolled Go/SQL front end: the D2 violation at accretion scale.
  No new lexical rule is added to it after this decision; the replacement
  deletes the lexer. Tracked in #302.

## Consequences

1. A proposed check names its authority class — structural, real-tool,
   textual-guard, or warn-only — and a check that cannot is a review finding.
2. `check-store-boundary.py` shrinks monotonically from acceptance until its
   replacement lands.
3. Appeasing a guard by reshaping code, rather than improving structure, is
   evidence the guard exceeded its class; the response is to fix or retire the
   guard, not to appease it.
4. This decision governs the repository validator plane only. The coordination
   plane's prior exclusion of heuristic authority is unchanged and remains the
   stronger rule.
5. This record is law-coverage `outstanding` against #302 until the
   store-boundary replacement lands and proves D5; its anchors then become the
   surviving checks themselves.

## Rejected alternatives

**Ban textual checks entirely.** Rejected: the one-connection pool invariant
qualifies under every D3 condition today, and pretending otherwise would lose a
guard whose failure mode is an invisible hang.

**Grow `check-store-boundary.py` behind better discipline.** Rejected: the
discipline is what failed in the predecessor; the shape itself is the defect.

**Adopt a general linting framework for all invariants.** Rejected as
overcorrection: a framework answers no specific invariant by itself and adds a
dependency surface to audit; D2's per-invariant pinning keeps each tool's
purpose and version visible.

**Reduce `check-tx-scope.py` to warn-only.** Rejected: it is a deterministic
textual rule with narrow legality under D3, not a heuristic under D4, and a
warn-only guard over an invisible hang is noise nobody reads.

## Verification

- The retained checks remain required and green in `.github/workflows/ci.yml`:
  `check-reachability.py` and the launcher dependency-inventory tests. The
  transaction-scope invariant is required and green as `TestTxScope` in the Go
  suite, which `verify` runs.
- The store-boundary replacement lands behind #302, preserves every current
  finding class through the tool or tests, and deletes the hand-rolled lexer.
- This record's law-coverage state is `outstanding` against #302 until then.
