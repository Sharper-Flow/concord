# Contributing to Concord

Concord is maintainer-led. Issues and pull requests are welcome, while the
operator retains final authority over accepted Product law and releases.

## Before changing code or Product law

Open an issue or proposal first for consequential changes to Product purpose,
authority, workflows, contracts, public policy, or supported platforms. Small
bug fixes, documentation corrections, and maintenance changes may begin with a
pull request when their scope is clear.

## Development flow

1. Create a focused branch and worktree from `main`; do not develop directly on
   the default branch.
2. Use a Conventional Commit-style title for commits (for example,
   `fix: reject invalid cursor`).
3. Keep changes focused, explain any contract or decision impact, and open a
   pull request against `main`.
4. Address review feedback and required checks before merge. Maintainers merge
   approved changes.

Link each Concord work pull request (`work/work-*` branch) to its Linear issue
with a non-closing `Related to <issue key>` line in the body; other branches
need no link. Never put a closing word (`Fixes`, `Closes`,
`complete`, and Linear's other closing magic words) directly before an issue
key, not even to disclaim completion: Linear ignores the negation and moves
the issue at merge. `verify-pr-linear-link` refuses the wording on every pull
request, because issue status changes only through the Linear MCP server or
the operator (CD-0213 D5).

## Verification

Run `go test` on each changed package during development. Run the relevant
repository checks before opening a pull request:

```sh
go test ./path/to/changed/package
```

The CI workflow runs the repository validators, formatting, module tidiness,
vetting, and race-detector test run. The individual commands remain available:

```sh
gofmt -l .
python3 scripts/check-doc-links.py
python3 scripts/check-public-content.py
python3 scripts/check-json.py
bin/oc-test conformance
```

## Required pre-push preflight

The repository ships a [Lefthook](https://github.com/evilmartians/lefthook)
`pre-push` preflight (`lefthook.yml`): the cheap CI-owned gates — Go
formatting, vet, build, compile-only tests, lint, the JSON contract check,
reachability, and complexity — run before a push leaves the machine. A gate
whose globs match none of the pushed files is skipped, so a Python-only push
skips the Go gates. Preflight
is not full-suite proof: the Go test suite, the adapter suite, the script
battery, the race detector, and acceptance run only in CI. Use the existing
[advisory script selector](TESTING.md#selection-recipes-and-widening) for focused
local feedback; it is not a hook gate.

Before the first push from a clone, install the hook from its clean, merged
checkout of `main`:

```sh
go install github.com/evilmartians/lefthook/v2@v2.1.14
lefthook install
test -x "$(git rev-parse --git-path hooks/pre-push)"
```

- `go install` puts the `lefthook` binary in `$(go env GOPATH)/bin`; that
  directory must be on `PATH`, or the installed hook cannot find the binary.
- Git hooks live in the shared common git directory, so linked worktrees use
  the same installed hook as the main checkout.
- `lefthook install` refuses to run while `core.hooksPath` points to a
  custom location, and preserves an unrelated existing hook by renaming it
  to `<hook>.old` instead of deleting it.
- `lefthook.yml` pins `min_version: 2.1.14`; an older installed binary fails
  instead of guessing at newer settings.

This clone setup step owns hook installation, not the Concord release installer.
The hook remains in the clone's common Git directory across Concord upgrades.
Repeat the setup after a fresh clone or host rebuild, or if the hook is removed. The
`no_auto_install: true` setting means a manual preflight does not repair a missing
hook. `assert_lefthook_installed: true` makes an installed hook fail when it cannot
find Lefthook; it cannot detect a missing hook.

Push normally and let the installed hook run the selected gates. Each gate
enters the host admission route (`oc-test-gate`'s smoke admission lane)
through `bin/oc-test preflight-gate`, so concurrent pushes from linked
worktrees queue their gate work instead of overlapping it; on a host without
`oc-test-gate` the gate still runs, with a warning. The smoke lane is
separate from the heavy lane that full-tier suites use, so full-tier local
work does not block a push's gates; another smoke-class run — another
push's gates or a `bin/oc-test smoke` run — still shares that lane. A gate
can still be refused admission (exit 75): inside a synchronous tool call
the host waits up to its default 30 s (`OC_WORKLOAD_GATE_SYNC_WAIT_MS`)
for the lane before refusing, and the push fails closed with the host's
remediation — never with a skipped gate. Recovery is to rerun the push
itself through the host's long-running command route, which waits for
admission within its budget; each gate rejoins the lane per gate, so a
contended smoke lane lengthens the run by up to one smoke-lane wait per
matched gate, within that budget. Fix a failing gate before retrying a
push; do not bypass the hook. CI remains the verification owner. To diagnose
a failure before retrying a push, run the same gates manually:

```sh
bin/oc-test preflight
```

Do not include credentials, private paths, private fixtures, or generated local
state. See [SECURITY.md](SECURITY.md) for vulnerability reporting.
