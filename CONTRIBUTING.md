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

## Pre-push preflight (optional)

The repository ships a [Lefthook](https://github.com/evilmartians/lefthook)
`pre-push` preflight (`lefthook.yml`): the cheap CI-owned gates — Go
formatting, vet, build, compile-only tests, lint, the JSON contract check,
reachability, complexity, and the script suites your pushed files select —
run before a push leaves the machine. A gate whose globs match none of the
pushed files is skipped, so a Python-only push skips the Go gates. Preflight
is not full-suite proof: the Go test suite, the adapter suite, the race
detector, and acceptance run only in CI.

Install it once from a merged checkout of `main`:

```sh
go install github.com/evilmartians/lefthook/v2@v2.1.14
lefthook install
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

Run the same gates manually, with or without a hook installed:

```sh
bin/oc-test preflight
```

`scripts/check-pre-push.py`, a required CI step, fails when a gate command,
environment, pin, glob routing, or the script-suite battery drifts from the
CI steps that own the same commands.

Do not include credentials, private paths, private fixtures, or generated local
state. See [SECURITY.md](SECURITY.md) for vulnerability reporting.
