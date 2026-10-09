#!/usr/bin/env python3
"""Select and admit release verification evidence for a pushed main commit.

Release starts on a push to main. `select` looks up authenticated merge-group
CI evidence for the exact push SHA through the read-only GitHub Actions REST
API (`gh api`) and reports `reuse` only when the observed run matches the
target commit's own CI contract exactly: same repository and head repository,
the CI workflow itself (resolved by path, not by name), event `merge_group`,
the exact target SHA, a completed successful run, one exact attempt whose
every expected job ran exactly once and succeeded, and no superseding rerun
observed after the job population was read. Every miss - absent, ambiguous,
superseded, failed, skipped, neutral, duplicated, truncated, or errored
evidence, or a CI contract shape this verifier does not interpret - selects
`full`, and the Release workflow then calls the reusable CI workflow as the
complete fallback. `gate` evaluates the workflow admission algebra from real
job results; a selected mode alone is never publication evidence.

Failure taxonomy (fail closed everywhere):

- refusal (exit 2, no mode output): the target cannot be proven - bad
  arguments, a checkout that is not the target, a target unreachable from
  `origin/main`, or a CI contract at the target that cannot be read;
- full fallback (exit 0, mode=full): evidence or API miss, recorded as the
  reason and in the receipt;
- admission refusal (gate exit 1): any dependency state other than
  (lookup=success AND mode=reuse AND refresh=success AND fallback=skipped) or
  (lookup=success AND mode=full AND refresh=skipped AND fallback=success).

A failure of the target's own checks is never converted into a reuse miss.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path

import git_environment

try:
    import yaml
except ImportError:  # pragma: no cover - CI runner images carry PyYAML
    yaml = None

PER_PAGE = 100
MAX_PAGES = 10
CI_WORKFLOW_RELPATH = ".github/workflows/ci.yml"
FULL_SHA_RE = re.compile(r"\A[0-9a-f]{40}\Z")
REPOSITORY_RE = re.compile(r"\A[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
JOB_RESULTS = frozenset({"success", "failure", "cancelled", "skipped"})
MODES = frozenset({"reuse", "full"})
MERGE_GROUP = "merge_group"


class EvidenceError(ValueError):
    """The target cannot be proven; publication must be refused."""


class WorkflowYamlError(ValueError):
    """Workflow text is not acceptable strict YAML."""


class UnsupportedContract(ValueError):
    """The CI contract's job population cannot be derived statically."""


class ApiError(RuntimeError):
    """A read-only GitHub API call failed."""


@dataclass(frozen=True)
class NativeRun:
    id: int
    workflow_id: int
    path: str
    event: str
    head_sha: str
    status: str
    conclusion: str | None
    run_attempt: int
    created_at: str
    repository_id: int
    head_repository_id: int
    html_url: str


@dataclass(frozen=True)
class NativeJob:
    id: int
    run_id: int
    run_attempt: int
    head_sha: str
    name: str
    status: str
    conclusion: str | None
    html_url: str


def _positive_int(value: object, label: str) -> int:
    if type(value) is not int or value <= 0:
        raise ApiError(f"{label} must be a positive integer")
    return value


def _nonempty_string(value: object, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ApiError(f"{label} must be a non-empty string")
    return value


def _optional_string(value: object, label: str) -> str | None:
    if value is not None and not isinstance(value, str):
        raise ApiError(f"{label} must be a string or null")
    return value


def _nested_id(document: dict, key: str, label: str) -> int:
    nested = document.get(key)
    if not isinstance(nested, dict):
        raise ApiError(f"{label} must be an object")
    return _positive_int(nested.get("id"), f"{label}.id")


def _native_run(document: object) -> NativeRun:
    if not isinstance(document, dict):
        raise ApiError("workflow run entry must be an object")
    conclusion = _optional_string(document.get("conclusion"), "workflow run conclusion")
    return NativeRun(
        id=_positive_int(document.get("id"), "workflow run id"),
        workflow_id=_positive_int(document.get("workflow_id"), "workflow id"),
        path=_nonempty_string(document.get("path"), "workflow run path"),
        event=_nonempty_string(document.get("event"), "workflow run event"),
        head_sha=_nonempty_string(document.get("head_sha"), "workflow run head SHA"),
        status=_nonempty_string(document.get("status"), "workflow run status"),
        conclusion=conclusion,
        run_attempt=_positive_int(document.get("run_attempt"), "workflow run attempt"),
        created_at=_nonempty_string(document.get("created_at"), "workflow run creation time"),
        repository_id=_nested_id(document, "repository", "workflow run repository"),
        head_repository_id=_nested_id(document, "head_repository", "workflow run head repository"),
        html_url=_nonempty_string(document.get("html_url"), "workflow run URL"),
    )


def _native_job(document: object) -> NativeJob:
    if not isinstance(document, dict):
        raise ApiError("workflow job entry must be an object")
    conclusion = _optional_string(document.get("conclusion"), "workflow job conclusion")
    return NativeJob(
        id=_positive_int(document.get("id"), "workflow job id"),
        run_id=_positive_int(document.get("run_id"), "workflow job run id"),
        run_attempt=_positive_int(document.get("run_attempt"), "workflow job run attempt"),
        head_sha=_nonempty_string(document.get("head_sha"), "workflow job head SHA"),
        name=_nonempty_string(document.get("name"), "workflow job name"),
        status=_nonempty_string(document.get("status"), "workflow job status"),
        conclusion=conclusion,
        html_url=_nonempty_string(document.get("html_url"), "workflow job URL"),
    )


if yaml is not None:

    class _StrictLoader(yaml.SafeLoader):
        """A real-parser loader that refuses duplicate mapping keys."""

        def construct_mapping(self, node, deep: bool = False):
            seen: list = []
            for key_node, _ in node.value:
                key = self.construct_object(key_node, deep=deep)
                try:
                    duplicate = key in seen
                except TypeError:
                    raise WorkflowYamlError(f"unhashable YAML key: {key!r}") from None
                if duplicate:
                    raise WorkflowYamlError(f"duplicate YAML key: {key!r}")
                seen.append(key)
            return super().construct_mapping(node, deep)


def strict_yaml_load(text: str) -> object:
    """Parse YAML with the real parser, refusing malformed text and duplicate keys."""
    if yaml is None:
        raise WorkflowYamlError(
            "PyYAML is required to parse workflow YAML; install PyYAML instead of guessing a structure"
        )
    try:
        return yaml.load(text, Loader=_StrictLoader)
    except yaml.YAMLError as error:
        raise WorkflowYamlError(f"invalid YAML: {error}") from error


def expected_ci_jobs(document: object) -> list[str]:
    """The static CI job population, by display name, or why it cannot be derived.

    A job population is derivable only when every job is unconditional and
    unexpanded: no matrix strategy, no `continue-on-error`, and no `if`
    condition other than `always()`. Anything else returns UnsupportedContract
    so the release takes the complete fallback rather than inferring which
    jobs a run should have carried. Duplicate display names are equally
    ambiguous for name-based matching and select the fallback too.
    """
    jobs = document.get("jobs") if isinstance(document, dict) else None
    if not isinstance(jobs, dict) or not jobs:
        raise EvidenceError(f"{CI_WORKFLOW_RELPATH} declares no jobs mapping")
    names: list[str] = []
    for job_id, job in jobs.items():
        if not isinstance(job, dict):
            raise EvidenceError(f"CI job {job_id!r} is not a mapping")
        unsupported: list[str] = []
        if "strategy" in job:
            unsupported.append("a matrix strategy")
        if job.get("continue-on-error") is not None:
            unsupported.append("continue-on-error")
        condition = job.get("if")
        if condition is not None and str(condition).strip() != "always()":
            unsupported.append(f"an `if: {condition!r}` condition")
        if unsupported:
            raise UnsupportedContract(
                f"CI job {job_id!r} declares {', '.join(unsupported)}; "
                "its job population cannot be derived statically"
            )
        name = job.get("name", job_id)
        if not isinstance(name, str) or not name:
            raise EvidenceError(f"CI job {job_id!r} has no usable check name")
        names.append(name)
    duplicates = sorted({name for name in names if names.count(name) > 1})
    if duplicates:
        raise UnsupportedContract(f"CI jobs share one required name: {', '.join(duplicates)}")
    return names


def _observed_now() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat()


def git(root: Path, *args: str) -> str:
    completed = subprocess.run(["git", "-C", str(root), *args], capture_output=True, text=True)
    if completed.returncode != 0:
        raise EvidenceError(f"git {' '.join(args)} failed: {completed.stderr.strip()}")
    return completed.stdout.strip()


def verify_target_identity(root: Path, target_sha: str) -> None:
    """The checkout is the target, and the target is reachable from main."""
    head = git(root, "rev-parse", "HEAD")
    if head != target_sha:
        raise EvidenceError(f"checked-out HEAD {head} is not the release target {target_sha}")
    git(root, "fetch", "origin", "+refs/heads/main:refs/remotes/origin/main")
    probe = subprocess.run(
        ["git", "-C", str(root), "merge-base", "--is-ancestor", target_sha, "refs/remotes/origin/main"],
        capture_output=True,
    )
    if probe.returncode != 0:
        raise EvidenceError(f"target {target_sha} is not reachable from origin/main")


def gh_api(endpoint: str) -> object:
    try:
        completed = subprocess.run(["gh", "api", endpoint], capture_output=True, text=True)
    except OSError as error:
        raise ApiError(f"cannot run gh api {endpoint!r}: {error}") from error
    if completed.returncode != 0:
        detail = completed.stderr.strip().splitlines()[-1] if completed.stderr.strip() else "no diagnostics"
        raise ApiError(f"gh api {endpoint!r} exited {completed.returncode}: {detail}")
    try:
        return json.loads(completed.stdout)
    except json.JSONDecodeError as error:
        raise ApiError(f"gh api {endpoint!r} returned invalid JSON: {error}") from error


def _paginated(endpoint_base: str, key: str) -> list[dict]:
    """Every entry of a paginated collection, with full total accounting.

    A total that exceeds the bounded page budget, or entries that do not add
    up to the reported total, are reported as ApiError so the caller takes
    the complete fallback instead of trusting a partial population.
    """
    entries: list[dict] = []
    total: int | None = None
    for page in range(1, MAX_PAGES + 1):
        separator = "&" if "?" in endpoint_base else "?"
        document = gh_api(f"{endpoint_base}{separator}per_page={PER_PAGE}&page={page}")
        if not isinstance(document, dict):
            raise ApiError(f"{endpoint_base} page {page} is not an object")
        batch = document.get(key)
        reported = document.get("total_count")
        if not isinstance(batch, list) or type(reported) is not int or reported < 0:
            raise ApiError(f"{endpoint_base} page {page} lacks its collection shape")
        if total is None:
            total = reported
        elif reported != total:
            raise ApiError(f"{endpoint_base} total_count changed from {total} to {reported} on page {page}")
        if any(not isinstance(entry, dict) for entry in batch):
            raise ApiError(f"{endpoint_base} page {page} contains a non-object entry")
        entries.extend(batch)
        if len(entries) > total:
            raise ApiError(f"{endpoint_base} returned more entries than its total_count {total}")
        if len(entries) == total:
            break
        if not batch:
            # An empty page before the reported total means no further page
            # can complete the collection; stop requesting and let the
            # accounting below report the incomplete population.
            break
    if total is None:
        raise ApiError(f"{endpoint_base} returned no first page")
    if total > MAX_PAGES * PER_PAGE:
        raise ApiError(f"{endpoint_base} reports {total} entries beyond the bounded page budget {MAX_PAGES}")
    if len(entries) != total:
        raise ApiError(f"{endpoint_base} returned {len(entries)} of {total} entries; pagination is incomplete")
    return entries


def _repo_id(repository: str) -> int:
    document = gh_api(f"repos/{repository}")
    repo_id = document.get("id") if isinstance(document, dict) else None
    full_name = document.get("full_name") if isinstance(document, dict) else None
    if full_name != repository:
        raise ApiError(f"repos/{repository} did not resolve to this repository's numeric identity")
    return _positive_int(repo_id, f"repos/{repository} id")


def _ci_workflow(repository: str) -> dict:
    document = gh_api(f"repos/{repository}/actions/workflows/ci.yml")
    if not isinstance(document, dict):
        raise ApiError("the CI workflow did not resolve to an object")
    if document.get("path") != CI_WORKFLOW_RELPATH:
        raise ApiError(f"the resolved workflow path {document.get('path')!r} is not {CI_WORKFLOW_RELPATH}")
    _positive_int(document.get("id"), "resolved CI workflow id")
    return document


def _merge_group_runs_for_target(
    runs: list[NativeRun], repo_id: int, workflow_id: int, target_sha: str
) -> list[NativeRun]:
    """Runs that are this repository's CI workflow testing the exact target.

    Status and conclusion are deliberately not filtered here: the latest
    identity-matched run is selected first, so an older success can never
    conceal a newer pending or failed observation for the same SHA.
    """

    return [
        run
        for run in runs
        if run.repository_id == repo_id
        and run.head_repository_id == repo_id
        and run.workflow_id == workflow_id
        and run.path == CI_WORKFLOW_RELPATH
        and run.event == MERGE_GROUP
        and run.head_sha == target_sha
    ]


def _run_satisfies_target(run: NativeRun, target_sha: str) -> str | None:
    """Why this completed-success run still cannot be reused, or None."""
    if run.path != CI_WORKFLOW_RELPATH:
        return f"run path {run.path!r} is not {CI_WORKFLOW_RELPATH}"
    if run.head_sha != target_sha:
        return f"run head_sha {run.head_sha!r} is not the target {target_sha}"
    if run.status != "completed" or run.conclusion != "success":
        return f"run is {run.status}/{run.conclusion}"
    return None


def _attempt_jobs(repository: str, run: NativeRun) -> list[NativeJob]:
    documents = _paginated(f"repos/{repository}/actions/runs/{run.id}/attempts/{run.run_attempt}/jobs", "jobs")
    return [_native_job(document) for document in documents]


def _same_run_identity(left: NativeRun, right: NativeRun) -> bool:
    return (
        left.id == right.id
        and left.workflow_id == right.workflow_id
        and left.path == right.path
        and left.event == right.event
        and left.head_sha == right.head_sha
        and left.repository_id == right.repository_id
        and left.head_repository_id == right.head_repository_id
        and left.run_attempt == right.run_attempt
    )


@dataclass(frozen=True)
class Selection:
    mode: str
    reason: str
    receipt: dict = field(default_factory=dict)


def fallback_selection(target_sha: str, reason: str, observed_at: str, repository_id: int | None = None) -> Selection:
    receipt = {
        "schema_version": "1.0",
        "target_sha": target_sha,
        "route": "full_ci",
        "reason": reason,
        "observed_at": observed_at,
    }
    if repository_id is not None:
        receipt["repository_id"] = repository_id
    return Selection("full", reason, receipt)


def select_evidence(root: Path, repository: str, target_sha: str) -> Selection:
    if not REPOSITORY_RE.match(repository):
        raise EvidenceError(f"--repository {repository!r} is not an owner/name repository identity")
    if not FULL_SHA_RE.match(target_sha):
        raise EvidenceError(f"--target-sha {target_sha!r} is not a full lowercase commit SHA")

    verify_target_identity(root, target_sha)
    try:
        ci_text = git(root, "show", f"{target_sha}:{CI_WORKFLOW_RELPATH}")
        document = strict_yaml_load(ci_text)
    except (EvidenceError, UnicodeDecodeError, WorkflowYamlError) as error:
        raise EvidenceError(f"the CI contract at the target cannot be read: {error}") from error
    try:
        expected_jobs = expected_ci_jobs(document)
    except UnsupportedContract as error:
        return fallback_selection(target_sha, str(error), _observed_now())
    blob_oid = git(root, "rev-parse", f"{target_sha}:{CI_WORKFLOW_RELPATH}")
    observed_at = _observed_now()

    repo_id: int | None = None
    try:
        repo_id = _repo_id(repository)
        workflow = _ci_workflow(repository)
        runs = _paginated(
            f"repos/{repository}/actions/workflows/ci.yml/runs?event={MERGE_GROUP}&head_sha={target_sha}",
            "workflow_runs",
        )
        native_runs = [_native_run(document) for document in runs]
        eligible = _merge_group_runs_for_target(native_runs, repo_id, workflow["id"], target_sha)
        if not eligible:
            return fallback_selection(target_sha, f"no {MERGE_GROUP} run of the CI workflow tested {target_sha}", observed_at, repo_id)
        # The most recent observation wins; an older success must not conceal it.
        run = max(eligible, key=lambda item: (item.created_at, item.id))
        miss = _run_satisfies_target(run, target_sha)
        if miss is not None:
            return fallback_selection(target_sha, f"the latest {MERGE_GROUP} run for the target was rejected: {miss}", observed_at, repo_id)
        jobs = _attempt_jobs(repository, run)
        verdict = _population_verdict(jobs, expected_jobs, run, target_sha)
        if verdict is not None:
            return fallback_selection(target_sha, verdict, observed_at, repo_id)
        # Re-read the run: a rerun that superseded the observed attempt must
        # void the reuse, never ride on the older success.
        current = _native_run(gh_api(f"repos/{repository}/actions/runs/{run.id}"))
        if not _same_run_identity(run, current):
            return fallback_selection(
                target_sha,
                "the re-read run no longer matches the selected run identity or attempt",
                observed_at,
                repo_id,
            )
        if current.status != "completed" or current.conclusion != "success":
            return fallback_selection(
                target_sha,
                f"run is now {current.status}/{current.conclusion}",
                observed_at,
                repo_id,
            )
    except ApiError as error:
        return fallback_selection(target_sha, f"api-error: {error}", observed_at, repo_id)

    return Selection(
        "reuse",
        "exact merge-group evidence verified for the pushed target",
        {
            "schema_version": "1.0",
            "repository_id": repo_id,
            "target_sha": target_sha,
            "route": "merge_group_reuse",
            "source_workflow_id": workflow["id"],
            "source_workflow_path": CI_WORKFLOW_RELPATH,
            "ci_workflow_blob_oid": blob_oid,
            "source_run_id": run.id,
            "source_run_attempt": run.run_attempt,
            "source_run_url": run.html_url,
            "expected_jobs": sorted(expected_jobs),
            "observed_job_ids": sorted(job.id for job in jobs),
            "observed_job_urls": {
                job.name: job.html_url for job in sorted(jobs, key=lambda item: item.name)
            },
            "observed_at": observed_at,
        },
    )


def _population_verdict(jobs: list[NativeJob], expected_jobs: list[str], run: NativeRun, target_sha: str) -> str | None:
    """Why the observed attempt population cannot be reused, or None."""
    expected = set(expected_jobs)
    seen_names: list[str] = []
    seen_ids: set[int] = set()
    for job in jobs:
        if job.id in seen_ids:
            return f"job id {job.id!r} appears twice in attempt {run.run_attempt}"
        seen_ids.add(job.id)
        if job.name not in expected:
            return f"attempt {run.run_attempt} carries unexpected job {job.name!r} outside the target's CI contract"
        if job.run_id != run.id or job.run_attempt != run.run_attempt:
            return f"job {job.name!r} does not belong to run {run.id} attempt {run.run_attempt}"
        if job.head_sha != target_sha:
            return f"job {job.name!r} tested {job.head_sha!r}, not the target {target_sha}"
        if job.status != "completed" or job.conclusion != "success":
            return f"job {job.name!r} is {job.status}/{job.conclusion}"
        seen_names.append(job.name)
    duplicates = sorted({name for name in seen_names if seen_names.count(name) > 1})
    if duplicates:
        return f"attempt {run.run_attempt} reports duplicate owners: {', '.join(duplicates)}"
    missing = sorted(expected - set(seen_names))
    if missing:
        return f"attempt {run.run_attempt} omits expected jobs: {', '.join(missing)}"
    return None


def admit(mode: str, lookup_result: str, refresh_result: str, fallback_result: str) -> tuple[bool, str]:
    """The admission algebra. Job results decide; the mode alone never does."""
    for label, value in (
        ("lookup result", lookup_result),
        ("refresh result", refresh_result),
        ("fallback result", fallback_result),
    ):
        if value not in JOB_RESULTS:
            return False, f"unknown {label}: {value!r}"
    if lookup_result != "success":
        return False, f"verification lookup did not succeed (result={lookup_result})"
    if mode == "reuse":
        if refresh_result != "success":
            return False, f"reuse requires refreshed main checks to succeed (result={refresh_result})"
        if fallback_result != "skipped":
            return False, f"reuse requires the fallback to be skipped (result={fallback_result})"
        return True, "admitted on verified exact merge-group evidence"
    if mode == "full":
        if fallback_result != "success":
            return False, f"full verification requires the fallback to succeed (result={fallback_result})"
        if refresh_result != "skipped":
            return False, f"full verification requires the refresh to be skipped (result={refresh_result})"
        return True, "admitted on a complete full CI fallback"
    return False, f"unknown verification mode: {mode!r}"


def _write_outputs(selection: Selection, output_file: Path | None, github_output: Path | None) -> None:
    payload = dict(selection.receipt)
    payload["mode"] = selection.mode
    payload["reason"] = selection.reason
    if output_file is not None:
        output_file.parent.mkdir(parents=True, exist_ok=True)
        output_file.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    if github_output is not None:
        with github_output.open("a", encoding="utf-8") as handle:
            handle.write(f"mode={selection.mode}\n")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)

    select_command = commands.add_parser("select", help="select reuse or full verification for a pushed target")
    select_command.add_argument("--repository", required=True, help="owner/name repository identity")
    select_command.add_argument("--target-sha", required=True, help="the exact pushed commit SHA")
    select_command.add_argument("--root", type=Path, default=Path.cwd(), help="the target's checkout")
    select_command.add_argument("--output-file", type=Path, help="where to write the admission receipt JSON")
    select_command.add_argument("--github-output", type=Path, help="$GITHUB_OUTPUT to receive the selected mode")

    gate_command = commands.add_parser("gate", help="evaluate the admission algebra from real job results")
    gate_command.add_argument("--mode", required=True, help="selected verification mode")
    gate_command.add_argument("--lookup-result", required=True, help="verify-lookup job result")
    gate_command.add_argument("--refresh-result", required=True, help="refresh-main-checks job result")
    gate_command.add_argument("--fallback-result", required=True, help="ci-fallback job result")

    args = parser.parse_args(argv)

    if args.command == "gate":
        admitted, verdict = admit(args.mode, args.lookup_result, args.refresh_result, args.fallback_result)
        print(f"release admission: {verdict}")
        print(
            "release admission: "
            f"mode={args.mode} lookup={args.lookup_result} "
            f"refresh={args.refresh_result} fallback={args.fallback_result}"
        )
        return 0 if admitted else 1

    try:
        git_environment.scrub_inherited()
        selection = select_evidence(args.root.resolve(), args.repository, args.target_sha)
    except (EvidenceError, git_environment.GitEnvironmentError) as error:
        print(f"release evidence: refusing publication: {error}", file=sys.stderr)
        return 2
    _write_outputs(selection, args.output_file, args.github_output)
    print(f"release evidence: mode={selection.mode}")
    print(f"release evidence: reason={selection.reason}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
