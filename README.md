<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/logos/concord-primary-mono-light.svg">
  <img alt="Concord" src="brand/logos/concord-primary-mono-dark.svg" width="240">
</picture>

# Concord

*Organized Product Development at Chaotic Speed.*

Concord helps one operator coordinate several AI coding agents on one machine.

## What Concord is

Concord is a workflow engine over a durable record. It keeps approved
requirements, work state, evidence, knowledge, and research in one local SQLite
database. Agents reach that state only through typed, authorized operations, and
the engine decides whether each operation is admissible. The
[core architecture](docs/core-architecture.md) describes these boundaries.

## Why it is different

When agents make independent changes in related parts of one project, each
change can pass review while the combined result breaks an approved requirement.
Most agent tooling answers this in the prompt. It assigns roles, orders handoffs,
and describes the process each agent should follow. Those instructions hold while
every agent follows them, and nothing outside the prompt checks the result. When
an agent reports that the work is complete, that report is the only record.

Concord puts the process in state that an agent cannot write around.

- **Completion carries evidence.** A terminal transition refuses until
  verification, review, and commit evidence are bound to the work. An agent
  cannot finish its own work by asserting that it is finished.
- **The goal is fixed before the work starts.** The operator approves the
  outcome predicates first. At acceptance, Concord compares the delivered result
  against those predicates rather than against the agent's account of them.
  ([CD-0012](docs/decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md))
- **Colliding work is refused, not merged later.** Each change declares the
  architectural Domains it writes. A second change into a claimed Domain refuses
  to start until the overlap is resolved.
  ([CD-0041](docs/decisions/CD-0041-architecture-bound-product-law.md))
- **A refusal names its remedy.** Every refusal is typed and states which
  condition failed, so a blocked agent reads the route out instead of guessing.

This carries a real cost. An agent must earn each transition, and a session that
skips a step is refused rather than obeyed.

## Install a release

Concord supports Linux amd64. Read the [full prerequisites](docs/installation.md#prerequisites)
before you install. Run the installer with Python 3.

> **Warning:** On a headless host without a login collection, the installer
> creates an unencrypted, user-scoped keyring collection. The operating-system
> user boundary restricts access, but it does not encrypt the collection.

Download `concord-installer.py` from the
[latest release](https://github.com/Sharper-Flow/concord/releases/latest), then
install that release's tag (replace `vX.Y.Z`):

```sh
python3 concord-installer.py install --version vX.Y.Z
```

The installer verifies the published checksum and bundle before it changes the
operator environment. It does not overwrite user-authored files and can recover
an interrupted operation. Restart OpenCode after installation.

The [installation guide](docs/installation.md) covers artifact verification,
managed paths, repair, upgrade, uninstall, and first-use requirements.

## First use

Complete the [operator bootstrap](adapter/opencode/README.md#operator-bootstrap-cli)
before you use adapter tools:

1. Register the client.
2. Create the Product and its first Project.
3. Add a locator for the Project's Git repository.

Concord does not invent these records or their keys. The bootstrap commands
listed in the adapter guide read one strict JSON object from stdin and write one
bounded JSON result. The launcher uses an interactive TTY instead.

```sh
concord --version
concord --help
concord launcher
```

## Build and verify from source

Source development uses the Go toolchain pinned in [`go.mod`](go.mod). Run
plain `go test` on changed packages during the edit loop. The [CI
workflow](.github/workflows/ci.yml) shows the repository checks, and
[`bin/oc-test`](bin/oc-test) documents the local conformance workload.

```sh
go run ./cmd/concord --version
go test ./cmd/concord
bin/oc-test conformance
bun test adapter/opencode/
```

The adapter test command needs Bun.

## Learn more

- [Installation guide](docs/installation.md)
- [OpenCode adapter guide](adapter/opencode/README.md)
- [Development authority](docs/development-authority.md)
- [Documentation index](docs/README.md)

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## Security

Report suspected vulnerabilities privately through GitHub Security Advisories,
not a public issue or pull request. Only the latest tagged release is supported
for security fixes. See [SECURITY.md](SECURITY.md).

## License

Concord is released under the [MIT License](LICENSE).
