# Concord

Concord is a Product-first, agent-native coordination system for one operator and
many local AI agents. The repository currently contains the accepted constitutional
design, public contracts/scenarios, repository safety gates, and a minimal Go CLI
boundary. Runtime storage, adapters, workflow execution, installation, and releases
are not implemented yet.

## Status and support

This is a public pre-runtime scaffold. Linux amd64 is the only planned
v1 platform. Concord must not self-host Concord development before the accepted
replacement-readiness floor is proven; use GitHub Issues, pull requests, and
worktrees in the meantime.

Start with:

- [Documentation index](docs/README.md)
- [Canonical priorities](docs/priorities.md)
- [Public provenance](docs/provenance.md)
- [Development authority](docs/development-authority.md)
- [Agent instructions](AGENTS.md)

## Build and verify

```sh
go run ./cmd/concord --version
go test -race ./...
go vet ./...
python3 scripts/check-doc-links.py
python3 scripts/check-public-content.py
python3 scripts/check-json.py
```

Before pushing, run the complete ordered gate in [AGENTS.md](AGENTS.md). The scaffold
CLI has no dependencies beyond the Go toolchain; repository validation also requires
Python 3.

## Repository map

| Path | Purpose |
|---|---|
| `cmd/concord/` | Minimal Go CLI boundary. |
| `contracts/` | Public machine-readable contracts. |
| `scenarios/` | Synthetic conformance scenarios. |
| `docs/` | Accepted Product law and design evidence. |
| `adapter/opencode/` | Reserved OpenCode adapter boundary. |
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

`bin/oc-test` groups the same checks into three tiers and applies host
admission control when a compatible throttle is installed, so several agents
running local suites at once cannot saturate one machine:

```sh
bin/oc-test targeted -- -run TestRunVersion ./cmd/concord
bin/oc-test smoke
bin/oc-test full
```

`targeted` is unthrottled, `smoke` runs the Go sweep, and `full` reproduces the
continuous-integration checks with bounded workers and a wall-clock bound. The
wrapper is optional: continuous integration calls the same commands natively,
and the tiers still run, unthrottled and with a warning, when no throttle is
installed.

## Contributing and security

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. Report
security vulnerabilities privately through [SECURITY.md](SECURITY.md), not in a
public issue.

## License

Concord is released under the [MIT License](LICENSE).
