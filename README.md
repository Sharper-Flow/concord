# Concord

Concord is a Product-first coordination scaffold for one operator and local AI
agents. The public repository currently contains the constitutional design,
contracts, scenarios, and a minimal Go CLI boundary. Runtime storage, adapters,
workflows, installation, and releases are not implemented yet.

## Status and support

This is a pre-readiness development candidate. Linux amd64 is the only planned
v1 platform. Concord must not self-host Concord development before the accepted
replacement-readiness floor is proven; use GitHub Issues, pull requests, and
worktrees in the meantime.

Start with the [documentation index](docs/README.md), [provenance record](docs/provenance.md),
and [development authority](docs/development-authority.md).

## Build and verify

```sh
go run ./cmd/concord --version
python3 scripts/check-doc-links.py
python3 scripts/check-public-content.py
python3 scripts/check-json.py
```

The scaffold has no runtime dependencies beyond Go and Python 3 for repository
validation.

## Contributing and security

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request. Report
security vulnerabilities privately through [SECURITY.md](SECURITY.md), not in a
public issue.

## License

Concord is released under the [MIT License](LICENSE).
