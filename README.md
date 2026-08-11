# Concord

Concord is a Product-first, agent-native coordination system for one operator and
many local AI agents. The repository contains the accepted constitutional design,
public contracts/scenarios, repository safety gates, a Go CLI boundary, and a
release-ready Linux amd64 distribution path.

## Status and support

Linux amd64 is the only planned v1 platform. Concord is
pre-replacement-readiness: it must not self-host its own development before the
accepted replacement-readiness floor is proven, so development stays
GitHub-native (issues, pull requests, worktrees) in the meantime.

Start with:

- [Documentation index](docs/README.md)
- [Canonical priorities](docs/priorities.md)
- [Public provenance](docs/provenance.md)
- [Development authority](docs/development-authority.md)
- [Installation and upgrades](docs/installation.md)
- [Agent instructions](AGENTS.md)

## Build and verify

```sh
go run ./cmd/concord --version
go test -race ./...
go vet ./...
python3 scripts/check-doc-links.py
python3 scripts/check-public-content.py
python3 scripts/check-json.py
python3 scripts/check-agent-contracts.py
python3 scripts/test-release.py
python3 scripts/test-installer.py
```

Before pushing, run the complete ordered gate in [AGENTS.md](AGENTS.md). The CLI
is built with the Go toolchain; repository validation also requires Python 3.

`bin/oc-test` groups those checks into four tiers and applies host admission
control when a compatible throttle is installed, so several agents running local
suites at once cannot saturate one machine:

```sh
bin/oc-test targeted -- -run TestRunVersion ./cmd/concord
bin/oc-test smoke
bin/oc-test full
bin/oc-test conformance   # long ten-process SQLite harness
```

`targeted` is unthrottled, `smoke` runs the Go sweep, `full` reproduces the
ordered gate with bounded workers and a wall-clock bound, and `conformance`
runs the long ten-process SQLite acceptance harness. The wrapper is optional:
continuous integration calls the same commands natively, and the tiers still
run, unthrottled and with a warning, when no throttle is installed.

## Repository map

| Path | Purpose |
|---|---|
| `cmd/concord/` | Go CLI boundary (launcher + JSON commands). |
| `internal/` | Storage, workflow engine, launcher, and agent tool surface. |
| `contracts/` | Public machine-readable contracts and schemas. |
| `scenarios/` | Synthetic conformance scenarios. |
| `docs/` | Accepted Product law and design evidence. |
| `adapter/opencode/` | OpenCode custom-tool adapter (TypeScript). |
| `workflows/` | Reserved workflow-definition boundary. |
| `skills/` | Conditional/deferred skill boundary. |

## Development

Development is GitHub-native until Concord proves replacement readiness:

1. Start from a public issue.
2. Create an isolated branch/worktree.
3. Follow accepted decisions and linked acceptance scenarios.
4. Open a pull request with local evidence.
5. Merge only after required checks pass.

The first runtime milestone is the
[storage-spine acceptance slice](docs/storage-spine-slice.md). Advance is predecessor
evidence—not a dependency, development authority, or state store for Concord.

## Contributing and security

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. Report
security vulnerabilities privately through [SECURITY.md](SECURITY.md), not in a
public issue.

## License

Concord is released under the [MIT License](LICENSE).
