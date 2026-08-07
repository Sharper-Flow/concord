# Concord Agent Instructions

Concord is a public, pre-runtime Go project. The repository currently contains
accepted Product law, machine-readable contracts/scenarios, repository validators,
and a deliberately tiny CLI boundary. Runtime storage, adapters, workflow execution,
installation, releases, and self-hosting do not exist yet.

## Authority and workflow

- GitHub Issues own planned work and defects.
- Pull requests plus required checks own review and merge evidence.
- `docs/decisions/`, specifications, and constitutional documents own Product law.
- Use one branch/worktree per change. Never implement directly on `main`.
- Advance is public predecessor evidence only. Do not create or dual-write Advance
  state, add `project.json`, or route Concord work through ADV.
- Concord must not coordinate its own development before replacement readiness.
- Surface conflicts with accepted decisions; never silently narrow the contract.

Start with:

1. `docs/README.md`
2. `docs/priorities.md`
3. `docs/development-authority.md`
4. The applicable `docs/decisions/CD-NNNN-*.md` records
5. The linked public GitHub issue

## Verification before push

Run in CI order:

```sh
python3 scripts/check-public-content.py
python3 scripts/check-doc-links.py
python3 scripts/check-json.py
test -z "$(gofmt -l .)"
go mod tidy
go vet ./...
go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
```

CI runs `git diff --exit-code` in its clean checkout after `go mod tidy` to reject
uncommitted module changes. Locally, inspect and stage intentional `go.mod` or
`go.sum` changes rather than expecting that CI-only command to pass in a dirty tree.

`bin/oc-test` runs those commands in three tiers and adds host admission control when a
compatible throttle is installed, so concurrent agent sessions cannot saturate one
machine:

| Tier | Runs | Throttled |
|---|---|---|
| `bin/oc-test targeted -- <args>` | `go test` with your arguments | no |
| `bin/oc-test smoke` | `gofmt`, `go vet`, `go test ./...` | yes |
| `bin/oc-test full` | the ordered gate above, minus `govulncheck` | yes, plus bounded `go test -p` workers and a wall-clock bound |

`full` scopes its module check to `git diff --exit-code -- go.mod go.sum`, so it works in
a dirty tree. Override `OC_TEST_GO_WORKERS` (default 4) or `OC_TEST_FULL_TIMEOUT`
(default `20m`) when needed. When no throttle is installed the tier warns and runs
unthrottled. CI does not use the wrapper; it calls the same commands natively.

For tight Go TDD loops, use `targeted` on the smallest package/test first, then
`bin/oc-test full` before push.

## Public-content boundary

`scripts/check-public-content.py` scans tracked and untracked text. Do not place these
in the repository, even temporarily:

- private Product/customer names or data;
- personal filesystem paths, machine names, private network details, or local
  service addresses;
- credentials, tokens, private environment paths, or copied private logs;
- unreachable private-source citations or private predecessor history.

Use synthetic scenarios and reachable public sources. Do not weaken or bypass the
validator to make a check pass.

## Documentation rules

- `docs/priorities.md` is the canonical priority/operating-envelope source.
- `docs/decisions/` contains binding `CD-NNNN` records until superseded.
- Active research packs are SQLite working context under CD-0009, never Git
  knowledge. Do not add research-pack content or runtime research output to docs.
- Relative links and heading anchors are enforced. Update callers when moving or
  renaming files/headings.
- Add new constitutional documents to the companion table in `docs/README.md`.
- Decisions/specs/lessons require their accepted durable form; ordinary prose cannot
  silently acquire authority.

## Repository boundaries

| Path | Current role |
|---|---|
| `cmd/concord/` | Minimal CLI (`--version` only). |
| `internal/version/` | Development version value. |
| `contracts/` | Public schemas/contracts. |
| `scenarios/` | Synthetic acceptance scenarios. |
| `docs/` | Constitutional Product law and design evidence. |
| `adapter/opencode/` | Reserved adapter boundary; no implementation yet. |
| `workflows/` | Reserved workflow-definition boundary. |
| `skills/` | Conditional/deferred skill boundary. |

Do not populate reserved boundaries opportunistically. Do not introduce third-party
Go dependencies, SQLite code, framework abstractions, release automation, or runtime
behavior without the applicable accepted issue/decision scope.

## Go style

- Go 1.26 with the pinned toolchain from `go.mod`.
- Standard library first; no current third-party runtime dependencies.
- Keep behavior, types, tests, and errors local to the package that owns them.
- Prefer structural invariants, typed failures, transactions, and deterministic tests
  over heuristic inference.
- Use Conventional Commit and PR titles.
