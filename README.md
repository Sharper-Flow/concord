# Concord

Concord is Product-first, agent-native planning and coordination for one
operator and many local AI agents. It combines a SQLite-backed authority and
workflow engine, a strict JSON CLI, an interactive terminal launcher, and an
OpenCode adapter with public contracts, scenarios, and repository validators.

## Status

Concord is pre-replacement-readiness. Until the accepted readiness floor is
proven, GitHub issues, pull requests, and worktrees remain the authority for
Concord's own development; Concord must not coordinate its own development.

Published releases support Linux amd64 only.

## What Concord provides

- A SQLite authority for Products, Projects, work, workflow state, evidence,
  knowledge, research, and relationships.
- A typed agent tool surface with signed grants, strict envelopes, bounded JSON
  input/output, and fail-closed validation.
- `concord launcher`, an interactive Bubble Tea terminal interface.
- An OpenCode TypeScript adapter and generated, model-routed worker lanes.
- Public machine-readable contracts, synthetic acceptance scenarios, and
  validators for Product law and repository claims.

## Install a release

The installer requires:

- Linux amd64;
- Python 3;
- `git`, `opencode`, `secret-tool`, and `gnome-keyring-daemon`;
- a user-session Secret Service at `org.freedesktop.secrets`; and
- `$HOME/.local/bin` on `PATH`.

Download `concord-installer.py` from the
[latest GitHub release](https://github.com/Sharper-Flow/concord/releases/latest),
then install that release's tag (replace `vX.Y.Z`):

```sh
python3 concord-installer.py install --version vX.Y.Z
```

The installer verifies the published checksum and bundle before changing the
operator environment. It refuses to overwrite user-authored files and recovers
interrupted install, upgrade, or uninstall operations. Restart OpenCode after
installation or upgrade.

Inspect or remove a managed installation with:

```sh
python3 concord-installer.py status
python3 concord-installer.py uninstall
```

See [Installing Concord](docs/installation.md) for artifact verification,
managed paths, recovery behavior, and first-use requirements.

## First use

```sh
concord --version
concord --help
concord launcher
```

`concord launcher` requires an interactive TTY. The other command surface uses
one strict JSON object on stdin and one bounded JSON result on stdout.

Before using adapter tools, register the client, Product, Project, and Project
locator through the operator CLI. Concord deliberately does not invent those
records, keys, or grants. The
[OpenCode adapter guide](adapter/opencode/README.md) documents the exact
bootstrap commands and closed command vocabulary.

## Build and verify from source

Source development uses the Go toolchain pinned in `go.mod` (Go 1.26.5) and
Python 3 for repository validators.

```sh
go run ./cmd/concord --version
bin/oc-test targeted -- -run TestRunVersion ./cmd/concord
bin/oc-test full
```

`targeted` runs a focused Go test without host admission control. `full` runs
the bounded pre-push validator, formatting, module, vet, and race-test suite.
Run the expensive ten-process SQLite harness separately:

```sh
bin/oc-test conformance
```

Adapter tests use `bun:test` when Bun is available:

```sh
bun test adapter/opencode
```

CI runs commands natively rather than through `bin/oc-test` and adds
`govulncheck` plus the production-like acceptance conformance run. See
[AGENTS.md](AGENTS.md) for the exact ordered gate and focused-test guidance.

## Repository map

| Path | Purpose |
|---|---|
| `cmd/concord/` | Go CLI boundary: launcher and strict JSON commands. |
| `internal/store/` | SQLite authority, workflow engine, knowledge, research, and generated lane/routing registry. |
| `internal/launcher/` | Framework-independent launcher model plus Bubble Tea and store adapters. |
| `internal/agent/` | Grants, invoke dispatch, envelopes, payload validation, and generated tool contracts. |
| `contracts/` | Public schemas and manifests; inputs for generated contracts. |
| `scenarios/` | Synthetic acceptance scenarios and fixtures. |
| `adapter/opencode/` | OpenCode custom-tool adapter, generated lane agents, tests, and advisory evals. |
| `docs/` | Accepted Product law, decisions, research reports, and design evidence. |
| `scripts/` | Validators, code generators, installer, and release tooling. |
| `workflows/`, `skills/` | Reserved release boundaries; currently README-only in source. |

## Documentation

- [Documentation index](docs/README.md)
- [Canonical priorities](docs/priorities.md)
- [Public provenance](docs/provenance.md)
- [Development authority](docs/development-authority.md)
- [Installation and upgrades](docs/installation.md)
- [Agent instructions](AGENTS.md)

## Development

Development remains GitHub-native until Concord proves replacement readiness:

1. Start from a public issue.
2. Create an isolated branch and worktree from `main`.
3. Follow accepted decisions and linked acceptance scenarios.
4. Open a pull request with local evidence.
5. Merge only after required checks pass.

The first runtime milestone is the
[storage-spine acceptance slice](docs/storage-spine-slice.md). Advance is
predecessor evidence—not a dependency, development authority, or state store
for Concord.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## Security

Report suspected vulnerabilities privately through GitHub Security Advisories,
not a public issue or pull request. Only the latest tagged release is supported
for security fixes. See [SECURITY.md](SECURITY.md).

## License

Concord is released under the [MIT License](LICENSE).
