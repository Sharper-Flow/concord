#!/usr/bin/env python3
"""Keep a hook's Git environment out of child Git repositories.

A Git hook runs its children with the repository-local environment
(`git rev-parse --local-env-vars`, plus the pre-receive quarantine variable)
set for the hooking repository. A child that builds its own scratch
repository then has every Git operation redirected into, or refused by, that
outer repository: per git-scm.com/docs/githooks, a hook must clear these
variables before operating on another repository, and `git -C <dir>` does
not override an inherited GIT_DIR.

The namespace is discovered from the installed Git rather than mirrored
here, so a future Git reporting more local variables is covered unchanged.
Discovery itself runs with every `GIT_*` key cleared, so an inherited
poisoned configuration can neither break nor steer it. Only repository-local
and config-injected keys are removed; transport and authentication keys
(GIT_SSH_COMMAND, GIT_TERMINAL_PROMPT, ...) and unrelated keys survive.

Every entry point fails closed: if Git cannot be queried, reports nothing,
or reports names that cannot be Git variables, the caller gets
GitEnvironmentError and must refuse to spawn Git children.
"""

from __future__ import annotations

import os
import re
import subprocess

QUARANTINE_VAR = "GIT_QUARANTINE_PATH"

_DISCOVERY_TIMEOUT_SECONDS = 30
_VARIABLE_NAME_RE = re.compile(r"GIT_[A-Z0-9_]+")
_INDEXED_CONFIG_RE = re.compile(r"GIT_CONFIG_(KEY|VALUE)_[0-9]+")


class GitEnvironmentError(RuntimeError):
    """The local Git environment namespace cannot be established. Fail closed."""


def _discovery_env(environ: dict[str, str]) -> dict[str, str]:
    return {key: value for key, value in environ.items() if not key.startswith("GIT_")}


def local_environment_vars(environ: dict[str, str] | None = None) -> tuple[str, ...]:
    """The installed Git's repository-local variable names, plus quarantine."""
    source = os.environ if environ is None else environ
    try:
        discovered = subprocess.run(
            ["git", "rev-parse", "--local-env-vars"],
            env=_discovery_env(source),
            capture_output=True,
            text=True,
            check=False,
            timeout=_DISCOVERY_TIMEOUT_SECONDS,
        )
    except (OSError, subprocess.SubprocessError) as error:
        raise GitEnvironmentError(f"cannot query git for its local environment variables: {error}") from error
    if discovered.returncode != 0:
        detail = discovered.stderr.strip() or discovered.stdout.strip() or f"exit {discovered.returncode}"
        raise GitEnvironmentError(f"git rev-parse --local-env-vars failed: {detail}")
    names = [line.strip() for line in discovered.stdout.splitlines() if line.strip()]
    if not names:
        raise GitEnvironmentError("git reported an empty local environment namespace")
    malformed = [name for name in names if not _VARIABLE_NAME_RE.fullmatch(name)]
    if malformed:
        raise GitEnvironmentError(f"git reported names that cannot be Git variables: {malformed}")
    if QUARANTINE_VAR not in names:
        names.append(QUARANTINE_VAR)
    return tuple(names)


def sanitized_environment(environ: dict[str, str] | None = None) -> dict[str, str]:
    """A child environment without the repository-local Git namespace.

    Indexed GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n pairs are removed with their
    GIT_CONFIG_COUNT authority, so no injection survives a cleared count.
    """
    source = os.environ if environ is None else environ
    local = set(local_environment_vars(source))
    return {
        key: value
        for key, value in source.items()
        if key not in local and not _INDEXED_CONFIG_RE.fullmatch(key)
    }


def scrub_inherited(environ: dict[str, str] | None = None) -> tuple[str, ...]:
    """Remove the inherited local Git namespace from `environ` in place.

    Suite startup calls this before any Git operation runs, so a hook that
    launched the suite cannot redirect the scratch repositories the suite
    builds. Values a test installs afterwards are that test's own and stay.
    Returns the removed keys, discovery order first.
    """
    target = os.environ if environ is None else environ
    removed: list[str] = []
    for key in local_environment_vars(dict(target)):
        if target.pop(key, None) is not None:
            removed.append(key)
    for key in list(target):
        if _INDEXED_CONFIG_RE.fullmatch(key):
            del target[key]
            removed.append(key)
    return tuple(removed)
