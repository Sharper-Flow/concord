<picture>
  <source media="(prefers-color-scheme: dark)" srcset="brand/github/concord-readme-banner-dark.svg">
  <source media="(prefers-color-scheme: light)" srcset="brand/github/concord-readme-banner-light.svg">
  <img alt="Concord — Product-first agent coordination for professionals."
       src="brand/github/concord-readme-banner-light.svg">
</picture>

# Concord

*Organized Product Development at Chaotic Speed.*

Concord helps one operator coordinate several AI coding agents on one machine.
This page is for people who want to know whether this approach fits their work
and how to start.

When agents make independent changes in related parts of one project, each
change can pass review while the combined result breaks an approved requirement.
Concord records approved requirements, checks for overlapping work, and compares
the delivered result with the approved goal before it marks work complete.
Read the decisions on [overlapping work](docs/decisions/CD-0041-architecture-bound-product-law.md)
and [delivered outcomes](docs/decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md)
for the design details.

Concord keeps approvals, work state, evidence, knowledge, and research in one
local SQLite database. Agents change that state through typed, authorized tools.
The [core architecture](docs/core-architecture.md) describes these boundaries.

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

Use `concord --version` to print the version, `concord --help` to print usage,
and `concord launcher` to open the interactive launcher.

## Build and verify from source

Source development uses the Go toolchain pinned in [`go.mod`](go.mod) and
Python 3 for repository validators. The [CI workflow](.github/workflows/ci.yml)
shows the repository checks, and [`bin/oc-test`](bin/oc-test) documents local
test tiers.

```sh
go run ./cmd/concord --version
bin/oc-test targeted -- -run TestRunVersion ./cmd/concord
bin/oc-test full
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
