# Concord Agent Instructions

Concord is a public Go project: an agent-native Product coordination system for
one operator and many local AI agents. It holds accepted Product law,
machine-readable contracts and scenarios, a SQLite-backed storage and workflow
engine, a JSON CLI, an OpenCode TypeScript adapter, and repository validators.
Linux amd64 is the only release platform.

## Authority

- Linear owns planning; read and write it only through the Linear MCP server under
  [`.concord/law/specs/managed-development-authority.md`](.concord/law/specs/managed-development-authority.md). Pull requests
  plus required checks own review and merge evidence.
- [`.concord/law/decisions`](.concord/law/decisions) (CD-NNNN records), specifications, and
  constitutional documents own Product law. Ordinary prose cannot acquire that
  authority — including this file, which is host instruction, not law.
- Surface conflicts with accepted decisions. Never silently narrow a contract.
- One branch and worktree per change. Never implement directly on `main`.
  Host tools own worktree creation, placement, recovery, and removal
  ([`CD-0088`](.concord/law/decisions/CD-0088-host-owned-work-bootstrap-preserves-pre-readiness-authority.md)).
- For new Concord development, use Concord agents and the workflow route in
  [`priorities.md`](.concord/law/constitution/priorities.md#how-concords-own-development-runs-during-the-rebuild).
  Work already underway outside the workflow and authorized workflow-defect
  repair follow that section and
  [CD-0122](.concord/law/decisions/CD-0122-concord-workflow-scope-and-defect-repair.md).
  Asked to fix a Concord defect that the Concord workflow would hit or obstruct,
  tell the operator and recommend the general build agent, not a Concord agent.
- Advance is public predecessor evidence only. Do not create or dual-write
  Advance state. Never commit a local `project.json`.

## Prohibitions

- Do not hand-edit generated files. Regenerate them. Every generated Go, TS, and
  Markdown artifact carries a `DO NOT EDIT` header; the lane agent definitions
  under [`.opencode/agents/`](.opencode/agents/) carry frontmatter instead but
  are equally generated. Change the manifest or schema, then re-run the
  generator.
- Do not hand-tag or hand-cut a release. Releases are fully automated.
- Do not introduce new third-party Go dependencies, framework abstractions, or
  runtime behavior without accepted issue or decision scope. modernc.org/sqlite
  is already accepted.
- Adding or changing a repository dependency needs approval. The host
  typecheck in [`scripts/check-agent-contracts.py`](scripts/check-agent-contracts.py)
  does not: its `bun add --exact` installs the pinned host declarations into a
  temporary staging workspace and changes no repository dependency file. Run
  it; `--skip-host-typecheck` leaves the local evidence incomplete.
- Do not populate reserved boundaries (`workflows/`, `skills/`) opportunistically.
- Push normally: the shared pre-push hook runs the gates. Fix a failing gate; never
  bypass the hook. A clone without it needs the [one-time setup](CONTRIBUTING.md#required-pre-push-preflight).
- Do not place any of the following in the repository, even temporarily:
  private Product or customer names and data; personal filesystem paths, machine
  names, private network details, or local service addresses; credentials,
  tokens, private environment paths, or copied private logs; unreachable
  private-source citations or private predecessor history. Use synthetic
  scenarios and reachable public sources.

## The store connection invariant

The store pools one SQLite connection. Never call an `s.db`-backed `*Store`
method from inside a transaction: it parks on the pool forever. Use a tx-scoped
core instead. [`internal/store/doc.go`](internal/store/doc.go) states the
invariant and [`internal/store/txscope_test.go`](internal/store/txscope_test.go)
enforces it.

## Knowledge closure

An unprocessed document has no manifest record and is **not law**, however much
it reads like a spec.
A document not resolvable through `concord_knowledge` is not law:
`resolve_note` returning `knowledge_missing` is the authoritative negative.
Never text-grep for law state when a record exists.

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
| Knowledge manifest | [`.concord/docs/knowledge/`](.concord/docs/knowledge/) composed by [`scripts/knowledge_index.py`](scripts/knowledge_index.py); [`contracts/concord-knowledge-index.v1.schema.json`](contracts/concord-knowledge-index.v1.schema.json) |
| Local conformance workload and host admission | header comment in [`bin/oc-test`](bin/oc-test) |
| Changed-behavior test selection, widening, and local evidence | [`TESTING.md`](TESTING.md) |
| Which quality tools and check commands are declared ready | [`.concord/tooling.v1.json`](.concord/tooling.v1.json) |
| Adapter layout, tests, and native Task ownership | [`adapter/opencode/README.md`](adapter/opencode/README.md) |
| Which files are generated, from which inputs | [`scripts/generate-agent-contracts.py`](scripts/generate-agent-contracts.py); [`scripts/generate-agent-profiles.py`](scripts/generate-agent-profiles.py) |
| PR title grammar and its semver effect | [`scripts/check-commit-title.py`](scripts/check-commit-title.py); [`scripts/release.py`](scripts/release.py) |
| How a release is built and published | [`.github/workflows/release.yml`](.github/workflows/release.yml) |
| What is proved versus merely present | Each decision's Verification section and [`.concord/law/decisions/CD-0249-decisions-carry-no-coverage-records.md`](.concord/law/decisions/CD-0249-decisions-carry-no-coverage-records.md); [`.concord/docs/reachability-exceptions.v1.json`](.concord/docs/reachability-exceptions.v1.json) |
| Product law, priorities, documentation rules, and release currency before capture | [`.concord/docs/README.md`](.concord/docs/README.md); [`.concord/law/constitution/priorities.md`](.concord/law/constitution/priorities.md); [`CD-0165`](.concord/law/decisions/CD-0165-an-agent-begins-a-work-item-only-on-the-current-concord-release.md) |
| Package and seam ownership | [`.concord/docs/core-architecture.md`](.concord/docs/core-architecture.md) §4 (entry points by responsibility) |
