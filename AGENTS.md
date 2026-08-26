# Concord Agent Instructions

Concord is a public Go project: an agent-native Product coordination system for
one operator and many local AI agents. It holds accepted Product law,
machine-readable contracts and scenarios, a SQLite-backed storage and workflow
engine, a Bubble Tea terminal launcher, a JSON CLI, an OpenCode TypeScript
adapter, and repository validators. Linux amd64 is the only release platform.

This file carries what you must decide before acting. Everything else is a
pointer to the artifact that owns the answer, because a prose copy of a
derivable fact drifts from its source and no check catches it.
[`scripts/check-agents-md.py`](scripts/check-agents-md.py) enforces that split.

## Authority

- GitHub Issues own planned work and defects. Pull requests plus required
  checks own review and merge evidence.
- [`docs/decisions/`](docs/decisions/) (CD-NNNN records), specifications, and
  constitutional documents own Product law. Ordinary prose cannot acquire that
  authority — including this file, which is host instruction, not law.
- Surface conflicts with accepted decisions. Never silently narrow a contract.
- One branch and worktree per change. Never implement directly on `main`.
- Concord must not coordinate its own development before replacement readiness
  (CD-0010). See [`docs/development-authority.md`](docs/development-authority.md).
- Advance is public predecessor evidence only. Do not create or dual-write
  Advance state, and do not route Concord work through ADV. A local
  `project.json` may appear; it is ignored by git and must never be committed.
  [`scripts/check-predecessor-independence.py`](scripts/check-predecessor-independence.py)
  enforces this for repository-owned agent surfaces (generated lanes, the lane
  manifest and generator, and the adapter); host configuration outside the
  repository is out of scope, and predecessor citations under `docs/` remain
  permitted.

## Context discipline

Context is the scarcest resource in a session. Spend it on decisions.

- **Be surgical.** Read the smallest artifact that answers the question — a
  symbol before a file, a file before a directory. Reading for orientation is
  how a session runs out of room to think.
- **Delegate exploration.** Send scans, inventories, and "where is X" questions
  to a sub-agent, and state the exact return shape you need. Bring back
  findings, not transcripts. Worker lanes are defined in
  [`.opencode/agents/`](.opencode/agents/).
- **Interrogate the source, not a description of it.** To learn what a
  validator, generator, or command does, run it — `--help`, `--check`, and a
  deliberately failing run are cheaper and more reliable than reading the
  implementation, and far more reliable than trusting prose about it.
- **Verify narrowly, then widely.** Take the smallest test tier that can fail
  for your change, and widen only before pushing.
- **Repair the owning mechanism.** Prefer a change to the thing that is wrong
  over a parallel path, wrapper, or local exception beside it.
- **Do not restate a derivable fact.** If a generator, validator, workflow, or
  `--help` already asserts it, link to that source instead of copying it.

## Prohibitions

- Do not hand-edit generated files. Regenerate them. Every generated Go, TS, and
  Markdown artifact carries a `DO NOT EDIT` header; the lane agent definitions
  under [`.opencode/agents/`](.opencode/agents/) carry frontmatter instead but
  are equally generated. Change the manifest or schema, then re-run the
  generator.
- Do not hand-tag or hand-cut a release. Releases are fully automated.
- Do not weaken, bypass, or special-case a validator to make a check pass.
- Do not introduce new third-party Go dependencies, framework abstractions, or
  runtime behavior without accepted issue or decision scope. Bubble Tea v2,
  bubbles v2, lipgloss v2, and modernc.org/sqlite are already accepted.
- Do not populate reserved boundaries (`workflows/`, `skills/`) opportunistically.
- Do not place any of the following in the repository, even temporarily:
  private Product or customer names and data; personal filesystem paths, machine
  names, private network details, or local service addresses; credentials,
  tokens, private environment paths, or copied private logs; unreachable
  private-source citations or private predecessor history. Use synthetic
  scenarios and reachable public sources.

## The store connection invariant

The store pools one SQLite connection (`SetMaxOpenConns(1)`,
[`internal/store/store.go`](internal/store/store.go)). While any `*sql.Tx` is
open, that transaction holds the pool's only connection: a nested call through
`s.db` — query, exec, or another `BeginTx` — parks on the pool forever. It never
reaches SQLite, so WAL and `busy_timeout` cannot help, and a test exercises it
only by hanging.

Never call an `s.db`-backed `*Store` method from inside a transaction. Extract or
use a tx-scoped core: an `xxxTx` function, or a small queryer interface taking
the tx. Raising the pool size is not the fix — a second connection would read the
pre-transaction snapshot and silently miss uncommitted writes.
[`internal/store/txscope_test.go`](internal/store/txscope_test.go) enforces this structurally.

## Knowledge closure

[Issue #295](https://github.com/Sharper-Flow/concord/issues/295) splits
knowledge into four states: unprocessed, law, out of date, and out of spec.
An unprocessed document has no manifest record and is **not law** for this
Product regardless of how much it reads like a spec; treat it as source
material awaiting formalization. `scripts/check-knowledge-closure.py` is
the inverse-coverage validator: it walks every `*.md` under the manifest's
`knowledge_roots`, lists paths the manifest does not acknowledge, and exits
0 in warn mode (with per-file `unprocessed: <path>` lines) or 1 under
`--strict` for cutover checks. `scripts/check-doc-contract.py` applies the
required outline, Gherkin AC grammar, and STE subset (sentence length,
banned phrases, abbreviation discipline) to records whose kind is in scope;
hard-fail mode is gated by `doc_contract.enforced` in the manifest, which owns
the current setting and its activation evidence. The anti-conflation rule for
agents: a document not resolvable through `concord_knowledge` is not law —
`resolve_note` returning
`knowledge_missing` is the authoritative negative, never text-grep for law
state when a record exists.

## Go style

Go 1.26 with the pinned toolchain from [`go.mod`](go.mod). Keep behavior, types,
tests, and errors local to the package that owns them. Prefer structural
invariants, typed failures, transactions, and deterministic tests over heuristic
inference. Conventional Commit titles are load-bearing for release semver.

## Where to look

| Question | Authoritative source |
|---|---|
| What the CLI accepts, and its JSON-stdin rules | `commandSpecs` in [`cmd/concord/main.go`](cmd/concord/main.go); `concord --help` |
| The verification contract a branch must satisfy | [`.github/workflows/ci.yml`](.github/workflows/ci.yml) |
| Knowledge manifest, unprocessed enumeration, and doc contract | [`docs/concord-knowledge-index.v1.json`](docs/concord-knowledge-index.v1.json); [`contracts/concord-knowledge-index.v1.schema.json`](contracts/concord-knowledge-index.v1.schema.json); [`scripts/check-knowledge-closure.py`](scripts/check-knowledge-closure.py); [`scripts/check-doc-contract.py`](scripts/check-doc-contract.py) |
| Local verification tiers and their throttling | header comment in [`bin/oc-test`](bin/oc-test) |
| Which quality tools and check commands are declared ready | [`.concord/tooling.v1.json`](.concord/tooling.v1.json) |
| Adapter layout, tests, and the `worker-*` boundary | [`adapter/opencode/README.md`](adapter/opencode/README.md) |
| Which files are generated, from which inputs | [`scripts/generate-agent-contracts.py`](scripts/generate-agent-contracts.py); [`scripts/generate-agent-lanes.py`](scripts/generate-agent-lanes.py) |
| PR title grammar and its semver effect | [`scripts/check-commit-title.py`](scripts/check-commit-title.py); [`scripts/release.py`](scripts/release.py) |
| How to move a CD number that collided with another branch | [`scripts/renumber-cd.py`](scripts/renumber-cd.py) `--dry-run` |
| How a release is built and published | [`.github/workflows/release.yml`](.github/workflows/release.yml) |
| Distance from the first-usable floor | [`docs/floor-readiness.md`](docs/floor-readiness.md) |
| What is proved versus merely present | [`docs/law-coverage.v1.json`](docs/law-coverage.v1.json); [`docs/reachability-exceptions.v1.json`](docs/reachability-exceptions.v1.json) |
| Predecessor operational coverage state | [`docs/predecessor-operational-coverage.md`](docs/predecessor-operational-coverage.md) |
| Product law, priorities, and documentation rules | [`docs/README.md`](docs/README.md); [`docs/priorities.md`](docs/priorities.md) |
| Repository layout and component roles | [`docs/core-architecture.md`](docs/core-architecture.md) |
