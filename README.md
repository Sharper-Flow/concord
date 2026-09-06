# Concord

*Organized Product Development at Chaotic Speed.*

Concord is a Product-law-first, agent-native coordination system for one
operator and many concurrent local AI agents working on one machine. One local
SQLite authority holds accepted Product law, work, workflow state, evidence,
knowledge, and research. Every agent reads and changes that state through a
small typed tool surface, under per-call authorization.

The failure Concord prevents is quiet contradiction: many agents produce
individually clean changes that jointly enact a different Product than the one
the accepted law describes. Concord binds Product-changing work to canonical
Domains that own the law, detects concurrent Domain overlap, and refuses the
overlap until the operator resolves it against pinned versions.

## Status

The [replacement-readiness floor](docs/floor-readiness.md) is satisfied: the
authorizing manifest records every condition satisfied, with none outstanding
([issue #685](https://github.com/Sharper-Flow/concord/issues/685)). The alpha
and beta maturity rungs hold their manifests with no outstanding item. Concord
coordinates its own development under
[CD-0089](docs/decisions/CD-0089-concord-development-coordination.md): GitHub
issues remain authority for planning, and pull requests plus required checks
remain authority for review and merge.

Releases publish automatically on every merged pull request. Published
releases support Linux amd64 only.

## What Concord provides

### One state authority

- One local SQLite store is the sole authority: an append-only event log plus
  typed projections for Products, Projects, work, workflow state, knowledge,
  and research
  ([CD-0002](docs/decisions/CD-0002-concord-state-authority.md),
  [CD-0011](docs/decisions/CD-0011-retain-sqlite-after-conformance.md)).
- Acknowledged operations — approvals, workflow dispatch, terminal
  transitions — survive power loss
  ([CD-0050](docs/decisions/CD-0050-durable-acknowledgement.md)).
- A ten-process conformance harness falsifies the durability and latency
  claims on every release candidate.

### Architecture-bound Product law

- Canonical Domains own the law and bind Product-changing work to exact
  Domain and law footprints
  ([CD-0041](docs/decisions/CD-0041-architecture-bound-product-law.md)).
- Durable knowledge is manifest-primary: a document outside the knowledge
  manifest is source material, not law, whatever it reads like.
- Lessons publish from finished work into the Git knowledge home under
  operator approval
  ([CD-0026](docs/decisions/CD-0026-learning-capture.md)).

### A workflow engine with a completion gate

- Workflow definitions are code-defined, versioned, and digest-pinned; state
  is work-item events with typed projections
  ([CD-0013](docs/decisions/CD-0013-workflow-engine-mechanism.md)).
- Completion is one transaction that binds evidence, external conditions,
  verdict, and premise confirmation. A delivered outcome weaker than the
  approved one fails, and work discovered mid-execution forward-links rather
  than substitutes
  ([CD-0012](docs/decisions/CD-0012-bind-stated-goals-to-delivered-outcomes.md)).

### Continuity for every session

- Durable checkpoints and boundaries back each call with re-derived pinned
  state; summary prose is never an authority source
  ([CD-0016](docs/decisions/CD-0016-context-continuity.md)).
- Launcher-started sessions receive a core-derived continuity boot packet
  before OpenCode starts ([CD-0031](docs/decisions/CD-0031-core-derived-session-boot.md)).

### A closed, typed agent surface

- At most nine always-visible domain tools, behind strict
  `ok|pending|partial|error` envelopes with bounded output.
- Per-call capability authorization, per-operation seconds budgets that
  refuse before any effect, and operation-bound approvals with typed
  consequence summaries ([CD-0038](docs/decisions/CD-0038-per-operation-seconds-budgets.md),
  [CD-0037](docs/decisions/CD-0037-core-derived-approval-consequence-summaries.md)).
- Typed worker lanes with closed packet and report contracts; the host
  resolves the executing model, and Concord records the readback as evidence
  ([CD-0017](docs/decisions/CD-0017-typed-workers-and-model-routing.md),
  [CD-0058](docs/decisions/CD-0058-no-model-routing.md)).
- Worker evidence is a signed `worker-evidence-v1` assertion from a key held
  in the OS Secret Service, bound to the exact attempt
  ([CD-0044](docs/decisions/CD-0044-worker-evidence-caller-authentication.md)).

### A work vocabulary that records intent

- Five-state work lifecycle, typed relations, and atomic supersession.
- Durable resource claims that are records of intent, not locks
  ([CD-0028](docs/decisions/CD-0028-resource-claims.md)).
- Peer messages addressed to work and delivered at the next call
  ([CD-0029](docs/decisions/CD-0029-peer-messages.md)).
- Lightweight mid-execution observations, visible at resume
  ([CD-0030](docs/decisions/CD-0030-mid-execution-observations.md)).
- Versioned research packs with findings, sources, and freshness, bound into
  workflow steps that fail closed on stale revisions
  ([CD-0009](docs/decisions/CD-0009-active-research-context.md),
  [CD-0025](docs/decisions/CD-0025-research-surface.md)).

### Sessions and worktrees

- One canonical worktree per work item, with tiered authority: read-only
  inspect, exclusive-lease verify, typed take-over, and destroy for merged
  terminal work ([CD-0096](docs/decisions/CD-0096-in-session-worktree-retargeting.md)).
- `concord launcher`: browse the portfolio, launch and resume agent sessions,
  and pass a prompt through
  ([CD-0108](docs/decisions/CD-0108-the-launcher-is-the-zlauncher-replacement.md)).

### Migration from the predecessor

- One Product at a time migrates from the installed predecessor while both
  systems stay writable; harvest is idempotent and keeps predecessor identity
  as provenance
  ([CD-0097](docs/decisions/CD-0097-bounded-parallel-predecessor-migration.md)).

## Install a release

The installer requires:

- Linux amd64;
- Python 3;
- `git`, `opencode`, `secret-tool`, `gnome-keyring-daemon`, `busctl`,
  `dbus-run-session`, and `systemctl`;
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

On a headless host with no login collection, the installer creates an
unencrypted, user-scoped `gnome-keyring-daemon` collection. The user-account boundary
protects its files. No private key enters a file outside Secret Service,
process arguments, logs, or installer output.

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
records or keys. The
[OpenCode adapter guide](adapter/opencode/README.md) documents the exact
bootstrap commands and closed command vocabulary.

## Build and verify from source

Source development uses the Go toolchain pinned in [`go.mod`](go.mod) and
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

CI runs commands natively rather than through `bin/oc-test` and adds the
production-like acceptance conformance run. The nightly workflow adds
`govulncheck`. See [AGENTS.md](AGENTS.md) for the exact ordered gate and
focused-test guidance.

## Repository map

| Path | Purpose |
|---|---|
| `cmd/concord/` | Go CLI boundary: launcher and strict JSON commands. |
| `internal/store/` | SQLite authority, workflow engine, knowledge, research, and generated lane/routing registry. |
| `internal/launcher/` | Framework-independent launcher model plus Bubble Tea and store adapters. |
| `internal/agent/` | Authorization, invoke dispatch, envelopes, payload validation, and generated tool contracts. |
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

Concord coordinates its own development under
[CD-0089](docs/decisions/CD-0089-concord-development-coordination.md):

1. Start from a public issue.
2. Start a Concord session with the issue link; Concord captures the item and
   claims its canonical worktree.
3. Follow accepted decisions and linked acceptance scenarios.
4. Open a pull request with local evidence.
5. Merge only after required checks pass.

Every accepted decision lives under [`docs/decisions/`](docs/decisions/) and
binds until superseded. Advance is predecessor evidence — not a dependency,
development authority, or state store for Concord. Its recorded state-model
failures are Concord's founding anti-pattern evidence, and
[CD-0097](docs/decisions/CD-0097-bounded-parallel-predecessor-migration.md)
defines the bounded migration path that replaces it.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) before opening a pull request.

## Security

Report suspected vulnerabilities privately through GitHub Security Advisories,
not a public issue or pull request. Only the latest tagged release is supported
for security fixes. See [SECURITY.md](SECURITY.md).

## License

Concord is released under the [MIT License](LICENSE).
