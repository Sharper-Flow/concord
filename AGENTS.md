# Concord Agent Instructions

Concord is a public Go project: an agent-native Product coordination system for
one operator and many local AI agents. The repository holds accepted Product
law, machine-readable contracts/scenarios, a SQLite-backed storage and workflow
engine, a Bubble Tea terminal launcher, a short-lived JSON CLI, an OpenCode
TypeScript custom-tool adapter, and repository validators. Linux amd64 is the
only release platform.

## Authority and workflow

- GitHub Issues own planned work and defects.
- Pull requests plus required checks own review and merge evidence.
- `docs/decisions/` (CD-NNNN records), specifications, and constitutional
  documents own Product law.
- Use one branch/worktree per change. Never implement directly on `main`.
- Advance is public predecessor evidence only. Do not create or dual-write
  Advance state, and do not route Concord work through ADV. A local
  `project.json` may appear (ADV state); it is **not** gitignored — never
  commit it.
- Concord must not coordinate its own development before replacement readiness
  (CD-0010).
- Surface conflicts with accepted decisions; never silently narrow the contract.

Start with `docs/README.md`, `docs/priorities.md`, `docs/development-authority.md`,
the applicable `docs/decisions/CD-NNNN-*.md`, and the linked public issue.

## CLI surface (`cmd/concord`)

Small but no longer `--version`-only:

- `concord --version`, `concord --help`
- `concord launcher` — interactive Bubble Tea TUI; TTY-only, does **not** read
  JSON stdin.
- `concord session` — internal TTY bootstrap invoked by the launcher; derives and
  validates the selected work's CD-0016 continuity packet before starting OpenCode.
- JSON-stdin commands: `grant`, `invoke`, worker evidence (`worker-dispatch`,
  `worker-complete`, `worker-fail`), and operator setup (`client register`
  / `policy-update` / `key-rotate` / `revoke`, `product create`, `project
  create`, `product project-add`, `project locator-add` / `update` / `remove`).

The `worker-*` verbs record CD-0017 worker attempt evidence and are not agent
callable. The OpenCode adapter appends them through `adapter/opencode/dispatch.ts`
after a lane spawn; nothing else should call them. Under CD-0044 each write
carries a signed `worker-evidence-v1` assertion bound to the exact attempt, and
the signing client must hold the `worker_evidence` capability, so a direct
invocation without that credential fails before any event is appended.

JSON command rules (see `commandSpecs` in `cmd/concord/main.go`):

- Reads exactly one strict JSON object from stdin; rejects unknown fields and
  trailing bytes.
- Hard cap 65536 bytes on input and output.
- Hyphenated (`client-register`) and two-word (`client register`) forms are both
  accepted; no other aliases.
- `CONCORD_DB_PATH` overrides the SQLite path but is refused inside any git
  repo or worktree. Without the override the database lives at
  `$XDG_DATA_HOME/concord/concord.db`, else `~/.local/share/concord/concord.db`
  (`store.DefaultPath`).

## Verification before push

CI order (`.github/workflows/ci.yml`):

```sh
python3 scripts/check-public-content.py
python3 scripts/check-doc-links.py
python3 scripts/check-json.py
python3 scripts/check-predecessor-coverage.py
CONCORD_REQUIRE_BUN=1 python3 scripts/check-agent-contracts.py
python3 scripts/check-tx-scope.py
python3 scripts/check-store-boundary.py
python3 scripts/test-release.py && python3 scripts/test-installer.py && python3 scripts/test-commit-title.py && python3 scripts/test-tx-scope.py && python3 scripts/test-store-boundary.py && python3 scripts/test-predecessor-coverage.py && python3 scripts/test-floor-readiness.py && python3 scripts/test-knowledge-index.py
test -z "$(gofmt -l .)"
go mod tidy
git diff --exit-code            # CI's clean checkout; locally scope to -- go.mod go.sum
go vet ./...
go test -race ./...
CONCORD_CONFORMANCE_LONG=1 go test -count=1 -run '^TestTenProcessAcceptanceConformance$' -v ./internal/store
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
```

`bin/oc-test` wraps these with host admission control when `oc-test-gate` is
installed, so concurrent agent sessions cannot saturate one machine:

| Tier | Runs | Throttled |
|---|---|---|
| `targeted -- <args>` | `go test` with your args (defaults to `./...`) | no |
| `smoke` | `gofmt`, `go vet`, `go test ./...` | yes |
| `conformance` | long ten-process SQLite harness (`TestTenProcessConformance`, `internal/store`) | yes |
| `full` | validators + gofmt + `go mod tidy` + `git diff --exit-code -- go.mod go.sum` + `go vet` + `go test -race`, bounded workers and wall-clock | yes |

`full` does **not** run `govulncheck` or the long conformance harness. Override
`OC_TEST_GO_WORKERS` (default 4) or `OC_TEST_FULL_TIMEOUT` (default `20m`).
Without `oc-test-gate`, tiers warn and run unthrottled. CI does not use the
wrapper. For tight Go TDD loops, `targeted` the smallest package first, then
`full` before push.

Adapter tests (`adapter/opencode/*.test.ts`) use `bun:test` — run them with
`bun test adapter/opencode`. There is no `package.json`. CI installs Bun and
runs the whole adapter suite through `scripts/check-agent-contracts.py`, which
also build-checks the generated TypeScript and the hand-written `concord.ts`.

Bun stays optional locally: without it the checker falls back to a
generated-file marker scan that cannot observe a behavioural change to
`concord.ts` or `dispatch.ts`. `CONCORD_REQUIRE_BUN=1` turns that fallback into
a failure, and CI sets it, so removing the toolchain step fails the required
check instead of silently reducing it to a marker scan.

A second CI workflow (`.github/workflows/pr-title.yml`) validates the
pull-request title with `scripts/check-commit-title.py`. The repository squashes
with `squash_merge_commit_title=PR_TITLE`, so the title becomes the commit
subject verbatim and is read by `scripts/release.py`. The checker imports that
module's grammar rather than restating it, and closes the type vocabulary —
`release.py` parses any identifier as a type, so `feature:` would otherwise
parse cleanly and bump nothing.

## Generated code — do not hand-edit

Two generators produce every file carrying a `DO NOT EDIT` header.

`scripts/generate-agent-contracts.py` produces:

- `internal/agent/generated_contracts.go`, `internal/agent/generated_payload_schemas.go`
- `adapter/opencode/generated-contracts.ts`, `adapter/opencode/generated-contract-tests.ts`
- `docs/generated-agent-tool-surface.md`

Inputs: `contracts/agent-tool-surface.v1.json` (manifest),
`contracts/agent-tool-surface.schema.json` (IR),
`contracts/agent-tool-surface-payloads.schema.json`. A pinned digest lives in
`contracts/agent-tool-surface.digest`.

`scripts/generate-agent-lanes.py` produces:

- `internal/store/generated_agent_lanes.go`, `internal/store/generated_routing_policy.go`
- `adapter/opencode/generated-agent-lanes.ts`
- `contracts/agent-lanes.digest`, `contracts/routing-policy.digest`
- `docs/agent-lanes-contract.md`, `docs/routing-policy-contract.md`
- `adapter/opencode/agents/concord-{lane}.md` lane agent definitions

Inputs: `contracts/agent-lanes.v1.json` (lane manifest) and
`contracts/routing-policy.v1.json` (routing policy), each with its schema and
pinned digest.

`scripts/check-agent-contracts.py` (own CI step) re-runs both generators in
`--check` mode, diffs the outputs, and — when bun is installed — build-checks
the generated TypeScript. Regenerate rather than patching when you change a
manifest or schema. `scripts/test-agent-contracts.py` covers manifest tamper
rejection (local `python3` unittest; not part of CI).

`docs/concord-knowledge-index.v1.json` is validated by
`scripts/check-knowledge-index.py` and `docs/floor-readiness.v1.json` by
`scripts/check-floor-readiness.py`. `scripts/check-json.py` (CI) nests those
two plus `check-agent-contracts.py`, `check-tx-scope.py`, `check-store-boundary.py`, and
`check-lane-evals.py` (adapter lane evals).

`docs/predecessor-operational-coverage.md` is validated by
`scripts/check-predecessor-coverage.py`, which runs as its own CI step. It parses
the section tables and fails when a covered outcome names no existing repository
path, an excluded outcome carries no reason, a state falls outside the closed
vocabulary, or the stated tally disagrees with the rows it summarises. Editing
the table is how coverage state changes; the validator keeps the claim honest.

`docs/floor-readiness.v1.json` is the authorizing record of distance from the
first-usable floor. Editing it is how readiness state changes — a satisfied item
requires an existing evidence path, an outstanding item requires a tracking
issue, and `unmeasured` is a distinct state from incomplete. See
[`docs/floor-readiness.md`](docs/floor-readiness.md).

## Release flow

Releases are fully automated (`.github/workflows/release.yml`): on a green CI
run on `main`, `scripts/release.py` computes the next version from Conventional
Commit history, builds the Linux amd64 `CGO_ENABLED=0` binary with the version
ldflag, packages adapter + skills + installer, generates an SBOM, attests
provenance, tags, and publishes the GitHub Release. Do not hand-tag or hand-cut
releases. Conventional Commit titles are load-bearing for semver — not just
style, and `.github/workflows/pr-title.yml` enforces them on the title that
becomes the squashed subject.

## Public-content boundary

`scripts/check-public-content.py` scans tracked and untracked text. Do not place
in the repository, even temporarily:

- private Product/customer names or data;
- personal filesystem paths, machine names, private network details, or local
  service addresses;
- credentials, tokens, private environment paths, or copied private logs;
- unreachable private-source citations or private predecessor history.

Use synthetic scenarios and reachable public sources. Do not weaken or bypass
the validator to make a check pass.

## Documentation rules

- `docs/priorities.md` is the canonical priority/operating-envelope source.
- `docs/decisions/` contains binding `CD-NNNN` records until superseded.
- Active research packs are SQLite working context under CD-0009, never Git
  knowledge. Accepted, decision-bound research reports do live in Git under
  `docs/research/` (`R*.md`, referenced from the companion table). Do not add
  active research-pack content or runtime research output to docs.
- Relative links and heading anchors are enforced
  (`scripts/check-doc-links.py`). Update callers when moving or renaming
  files/headings.
- Add new constitutional documents to the companion table in `docs/README.md`.
- Decisions/specs/lessons require their accepted durable form; ordinary prose
  cannot silently acquire authority.

## Repository boundaries

| Path | Role |
|---|---|
| `cmd/concord/` | CLI entrypoint (see CLI surface above). |
| `internal/version/` | Version value (ldflag-overridden at release). |
| `internal/store/` | SQLite authority: schema, events, workflow engine, knowledge index, research, lifecycle, membership, generated lane/routing registry. |
| `internal/launcher/` | Framework-agnostic launcher model; `render/bubbletea` is the TUI adapter; `storeport` is the store bridge. |
| `internal/agent/` | Agent tool surface: grants, invoke dispatch, envelopes, semver, payload validation, generated contracts. |
| `internal/portfolio/`, `internal/workflowcorpus/` | Read projections and conformance coverage. |
| `contracts/` | Public JSON schemas + manifests (generated-code inputs). |
| `scenarios/` | Synthetic acceptance scenarios and fixtures. |
| `adapter/opencode/` | Implemented TypeScript custom-tool adapter (`concord.ts` hand-written + generated contracts); `agents/` holds generated lane definitions and `evals/` the advisory CD-0017 D7 prompt-eval harness. |
| `docs/` | Constitutional Product law and design evidence. |
| `workflows/`, `skills/` | Reserved (README only); `skills/` is packaged into releases. |
| `scripts/` | Validators, codegen, release/install tooling, and their tests. |

Do not populate reserved boundaries opportunistically. Do not introduce new
third-party Go dependencies, framework abstractions, or runtime behavior without
the applicable accepted issue/decision scope. (Bubble Tea v2, bubbles v2,
lipgloss v2, and modernc.org/sqlite are already accepted.)

## Go style

- Go 1.26 with the pinned toolchain from `go.mod`.
- The store pools one SQLite connection (`SetMaxOpenConns(1)`, `internal/store/store.go`). While any `*sql.Tx` is open, that transaction holds the pool's only connection: a nested call through `s.db` (query, exec, or another `BeginTx`) parks on the pool forever — it never reaches SQLite, so WAL and `busy_timeout` cannot help, and a test exercises it only by hanging. Never call an `s.db`-backed `*Store` method from inside a transaction; extract or use a tx-scoped core (`xxxTx` function or a small queryer interface taking the tx). Raising the pool size is not the fix — a second connection would read the pre-transaction snapshot and silently miss uncommitted writes. `scripts/check-tx-scope.py` (CI) enforces this textually: `s.db.` access after transaction acquisition in one function is a finding.
- Keep behavior, types, tests, and errors local to the package that owns them.
- Prefer structural invariants, typed failures, transactions, and deterministic
  tests over heuristic inference.
- Use Conventional Commit messages and PR titles (load-bearing for release
  semver).
