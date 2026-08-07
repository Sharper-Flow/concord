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

Run the relevant checks before opening a pull request:

```sh
gofmt -l .
python3 scripts/check-doc-links.py
python3 scripts/check-public-content.py
python3 scripts/check-json.py
```

Do not include credentials, private paths, private fixtures, or generated local
state. See [SECURITY.md](SECURITY.md) for vulnerability reporting.
