#!/usr/bin/env python3
"""Check current recipe effects through two candidate restores for every earlier release.

Every published, nondraft release remains a writer. The gate downloads its
standalone binary by enumerated asset ID, verifies its checksum and identity,
and runs the pinned domain/invoke recipe in a disposable root. It takes an
SQLite Online Backup snapshot after the writer exits, restores that snapshot
with the candidate's `restore`, and snapshots and restores the result again.

The core verifier checks whole-store current-state equality before accepted
migrations. This harness checks source provenance, immutable snapshot bytes,
restore report checksums, and the recipe's expected current effects in the
source and both restored databases. It neither reconstructs history nor
implements a competing store digest. These recipe assertions do not prove
arbitrary migration transformations or the store cutover, which the cutover
mode of `concord upgrade` proves with its own tests. Exit status is 0 when every
writer case passes, and 1 when any writer fails, is unknown, or the population
is incomplete.

Missing artifacts, refused recipe steps and exhausted budgets are named
unknowns, never skips. Source-proven v0.1.0-v0.7.1 bootstrap-only writers retain
their pinned fingerprints, source commit, owner hashes and distinct recipe
hash. An unbound or drifted source never becomes bootstrap-only.

Writer and candidate processes get constructed HOME, XDG, TMPDIR and
CONCORD_DB_PATH roots, not the caller's environment. The caller builds the
candidate from the target commit and supplies both identities. The gate binds
the candidate's bytes before the first case and refuses any change by the end.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import dataclasses
import hashlib
import json
import os
import re
import shutil
import sqlite3
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from pathlib import Path

from release_restore_confine import STOP, CaseFailure, Guard, STREAM_LIMIT, checkpoint, install_interrupt_handlers, run_bounded
from release_restore_invoke import InvokeError, invoke_plan, run_invoke_phase, tag_facts, verify_invoke_snapshot
from release_restore_legacy import LEGACY_VARIANT, LegacyProofError, select_variant
from release_restore_population import (
    GateError, Writer, enumerate_writers, gh_release_page, gh_release_total, git_tags, list_releases, semver,
)

FULL_SHA_RE = re.compile(r"\A[0-9a-f]{40}\Z")
REPOSITORY_RE = re.compile(r"\A[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
NETWORK_SECONDS = 120
RECIPE_ID = "domain-v2"
PRODUCT = {
    "product_id": "gate-product", "display_name": "Gate Product", "stage_maturity": "prototype",
    "stage_audience_commitment": "operator_only", "project_id": "gate-project",
    "project_display_name": "Gate Project", "role": "primary", "reason": "release restore gate",
}
LOCATOR = {
    "project_id": "gate-project", "locator_id": "gate-remote", "kind": "git_remote",
    "value": "https://example.invalid/gate/project.git", "expected_version": 1,
}


def version(table: str, subject: str, key: str = "id") -> dict:
    """A request value the runner fills with the subject's current version, read from the writer's store."""
    return {"$version": [table, key, subject]}


def repository(name: str) -> dict:
    """A request value the runner fills with an existing directory inside the case."""
    return {"$path": name}


PRODUCT_VERSION, PROJECT_VERSION = version("products", "gate-product"), version("projects", "gate-project")
MIRROR = {"project_id": "gate-project", "locator_id": "gate-mirror", "kind": "git_remote"}
HOME = {"product_id": "gate-product", "project_id": "gate-project", "locator_id": "gate-home", "reason": "release restore gate"}
SOURCE = HOME | {"locator_id": "gate-source"}
ENVIRONMENTS = ["development"]
# Each step runs on writers at or after its tag. The domain steps start at the
# first inventoried release; earlier releases run the bootstrap pair only.
DOMAIN = "v0.37.2"
RECIPE = (
    ("v0.1.0", "product-create", PRODUCT),
    ("v0.1.0", "project-locator-add", LOCATOR),
    (DOMAIN, "project-locator-add", MIRROR | {"value": "https://example.invalid/gate/mirror.git", "expected_version": PROJECT_VERSION}),
    (DOMAIN, "project-locator-update", MIRROR | {"value": "https://example.invalid/gate/moved.git", "expected_version": PROJECT_VERSION}),
    (DOMAIN, "project-locator-remove", {"project_id": "gate-project", "locator_id": "gate-mirror", "expected_version": PROJECT_VERSION}),
    (DOMAIN, "project-create", {"project_id": "gate-member", "display_name": "Gate Member", "product_id": "gate-product", "role": "secondary",
                                "reason": "release restore gate", "expected_product_version": PRODUCT_VERSION}),
    (DOMAIN, "product-create", PRODUCT | {"product_id": "gate-other", "display_name": "Gate Other", "project_id": "gate-shared",
                                          "project_display_name": "Gate Shared"}),
    (DOMAIN, "product-project-add", {"product_id": "gate-product", "project_id": "gate-shared", "role": "secondary",
                                     "reason": "release restore gate", "expected_version": PRODUCT_VERSION}),
    ("v4.3.0", "project-locator-add", {"project_id": "gate-project", "locator_id": "gate-home", "kind": "canonical_path",
                                       "value": repository("home"), "expected_version": PROJECT_VERSION}),
    ("v4.3.0", "product-knowledge-home-designate", HOME | {"expected_version": PRODUCT_VERSION}),
    ("v11.59.0", "project-locator-add", {"project_id": "gate-project", "locator_id": "gate-source", "kind": "canonical_path",
                                         "value": repository("source"), "expected_version": PROJECT_VERSION}),
    ("v11.59.0", "product-knowledge-source-register", SOURCE | {"expected_version": PRODUCT_VERSION}),
    ("v11.59.0", "product-knowledge-source-remove", SOURCE | {"expected_version": PRODUCT_VERSION}),
    ("v4.3.0", "product-knowledge-home-clear", {"product_id": "gate-product", "reason": "release restore gate", "expected_version": PRODUCT_VERSION}),
    ("v5.3.0", "product-stage-update", {"product_id": "gate-product", "stage_maturity": "alpha", "stage_audience_commitment": "limited",
                                        "reason": "release restore gate", "expected_version": PRODUCT_VERSION}),
    ("v4.7.0", "resource-create", {
        "event_id": "gate-resource-created", "resource_id": "gate-resource", "product_id": "gate-product", "display_name": "Gate Resource",
        "class": "infrastructure", "kind": "database", "purpose": "release restore gate", "stage_maturity": "prototype",
        "stage_audience_commitment": "operator_only", "environments": ENVIRONMENTS, "locator_absence_reason": "planned",
        "metadata_schema_version": "1", "metadata": {"engine": "sqlite"}, "owner_purpose": "release restore gate",
        "owner_environments": ENVIRONMENTS, "expected_product_version": PRODUCT_VERSION}),
    ("v4.7.0", "resource-share", {"event_id": "gate-resource-shared", "resource_id": "gate-resource", "product_id": "gate-other",
                                  "purpose": "release restore gate", "environments": ENVIRONMENTS,
                                  "expected_resource_version": version("managed_resources", "gate-resource", "resource_id")}),
)


def recipe_for(tag: str) -> list[tuple[str, dict]]:
    """The steps a writer runs: every step whose first tag is at or before the writer's."""
    return [(verb, request) for first, verb, request in RECIPE if semver(first) <= semver(tag)]


def recipe_sha256(steps: list[tuple[str, dict]], plan: tuple[str, ...]) -> str:
    """Bind the domain steps and the tag's actual invoke plan: no variant reuses another's hash."""
    return hashlib.sha256(json.dumps([RECIPE_ID, steps, list(plan)], sort_keys=True).encode()).hexdigest()


def resolve(value, store: Path, case: Path, deadline: float):
    """Fill one recipe request: versions from the writer's own store, paths as case directories."""
    if isinstance(value, dict) and set(value) == {"$version"}:
        table, key, subject = value["$version"]
        db = connect(store, deadline)
        try:
            row = db.execute(f"SELECT version FROM {table} WHERE {key}=?", (subject,)).fetchone()
        finally:
            db.close()
        if row is None:
            raise CaseFailure("unknown", "recipe", f"writer store has no {table} row {subject}")
        return row[0]
    if isinstance(value, dict) and set(value) == {"$path"}:
        path = case / "repositories" / value["$path"]
        path.mkdir(parents=True, exist_ok=True)
        return str(path)
    if isinstance(value, dict):
        return {key: resolve(item, store, case, deadline) for key, item in value.items()}
    if isinstance(value, list):
        return [resolve(item, store, case, deadline) for item in value]
    return value
EFFECTS = (
    ("products", "SELECT display_name FROM products WHERE id='gate-product'", ("Gate Product",)),
    ("projects", "SELECT display_name FROM projects WHERE id='gate-project'", ("Gate Project",)),
    ("product_projects", "SELECT role FROM product_projects WHERE product_id='gate-product' AND project_id='gate-project'", ("primary",)),
    ("project_locators", "SELECT kind, locator_value FROM project_locators WHERE project_id='gate-project' AND locator_id='gate-remote'",
     ("git_remote", "https://example.invalid/gate/project.git")),
)


class Transfer:
    """The run's shared download allowance, reserved before each asset is fetched."""

    def __init__(self, limit: int) -> None:
        self.limit, self.used, self.lock = limit, 0, threading.Lock()

    def reserve(self, size: int) -> None:
        with self.lock:
            if self.used + size > self.limit:
                raise CaseFailure("unknown", "transfer", f"download of {size} bytes exceeds the {self.limit} byte transfer budget")
            self.used += size


class Scratch:
    """The run's disk ceiling: bytes retained by finished cases, and an equal share of the rest per worker."""

    def __init__(self, limit: int, workers: int) -> None:
        self.limit, self.workers, self.retained, self.lock = limit, workers, 0, threading.Lock()

    def allowance(self) -> int:
        with self.lock:
            return (self.limit - self.retained) // self.workers

    def retain(self, size: int) -> None:
        with self.lock:
            self.retained += size


def sandbox_env(root: Path) -> dict[str, str]:
    env = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "HOME": str(root / "home"), "TMPDIR": str(root / "tmp"),
           "CONCORD_DB_PATH": str(root / "store" / "concord.db")}
    for name in ("DATA", "STATE", "CONFIG", "CACHE"):
        env[f"XDG_{name}_HOME"] = str(root / "home" / name.lower())
    for value in (env["HOME"], env["TMPDIR"], str(root / "store"), *(env[f"XDG_{n}_HOME"] for n in ("DATA", "STATE", "CONFIG", "CACHE"))):
        Path(value).mkdir(parents=True, exist_ok=True)
    return env


def tree_bytes(root: Path) -> int:
    """File bytes under root.

    The confined writer may remove its SQLite journal mid-walk after a rollback;
    a path that disappears between the is_file check and stat counts zero rather
    than failing the case at the harness stage.
    """
    if not root.exists():
        return 0
    total = 0
    try:
        for path in root.rglob("*"):
            try:
                if path.is_file():
                    total += path.stat().st_size
            except FileNotFoundError:
                continue
    except FileNotFoundError:
        pass
    return total


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def network_env() -> dict[str, str]:
    """The allowlisted environment `gh` and `git` need to reach GitHub, and nothing else."""
    return {key: os.environ[key] for key in ("PATH", "HOME", "XDG_CONFIG_HOME", "GH_TOKEN", "GITHUB_TOKEN", "GH_HOST") if key in os.environ}


def population_runner(streams: Path, wall_deadline: float, scratch: Scratch):
    """A `run(argv) -> stdout` for the population readers: confined, wall-bounded, interruptible, and inside scratch."""
    calls = iter(range(1 << 30))

    def check() -> None:
        if tree_bytes(streams) > scratch.allowance():
            raise CaseFailure("unknown", "disk", "population reads exceed their scratch share")

    def run(argv: list[str]) -> str:
        label = f"population-{next(calls)}"
        try:
            rc, out = run_bounded(argv, network_env(), "", min(wall_deadline, time.monotonic() + NETWORK_SECONDS), streams, label,
                                  Guard(check, lambda: min(STREAM_LIMIT, scratch.allowance())))
        except CaseFailure as failure:
            raise GateError(f"{argv[:3]}: {failure.detail}") from None
        if rc != 0:
            raise GateError(f"{argv[:3]} exited {rc}: {(streams / f'{label}.stderr').read_text(errors='replace').strip()[-400:]}")
        return out

    return run


def download_asset(repository: str, asset_id: int, size: int, destination: Path, deadline: float, streams: Path,
                   transfer: Transfer, guard, room: int) -> None:
    """Fetch one asset by its enumerated ID with an allowlisted authentication environment."""
    if size > room:
        raise CaseFailure("unknown", "disk", f"asset {asset_id} of {size} bytes exceeds the case's remaining scratch")
    transfer.reserve(size)
    destination.parent.mkdir(parents=True, exist_ok=True)
    rc, _ = run_bounded(["gh", "api", "-H", "Accept: application/octet-stream", f"repos/{repository}/releases/assets/{asset_id}"],
                        network_env(), "", min(deadline, time.monotonic() + NETWORK_SECONDS), streams, f"download-{asset_id}",
                        guard, stdout_path=destination, limit=size + STREAM_LIMIT)
    if rc != 0:
        raise CaseFailure("unknown", "download", f"asset {asset_id} download exited {rc}")
    if destination.stat().st_size != size:
        raise CaseFailure("fail", "download", f"asset {asset_id} has {destination.stat().st_size} bytes, enumerated {size}")


def verify_writer_asset(repository: str, writer: Writer, case: Path, deadline: float, streams: Path, transfer: Transfer, guard, room) -> tuple[Path, str]:
    name = f"concord-{writer.tag}"
    binary, checksums = case / "asset" / name, case / "asset" / f"{name}.sha256"
    download_asset(repository, writer.checksum_asset_id, writer.checksum_size, checksums, deadline, streams, transfer, guard, room())
    download_asset(repository, writer.asset_id, writer.asset_size, binary, deadline, streams, transfer, guard, room())
    actual = sha256_file(binary)
    listed = {parts[1].lstrip("*"): parts[0] for parts in (line.split() for line in checksums.read_text().splitlines()) if len(parts) == 2}
    if listed.get(name) != actual or (writer.asset_digest and writer.asset_digest != f"sha256:{actual}"):
        raise CaseFailure("fail", "checksum", f"binary sha256 {actual} disagrees with the checksum manifest or asset digest")
    binary.chmod(0o755)
    rc, version = run_bounded([str(binary), "--version"], sandbox_env(case), "", deadline, streams, "version", guard)
    if rc != 0 or version.strip() != writer.tag:
        raise CaseFailure("fail", "version", f"writer reports {version.strip()!r}, want {writer.tag}")
    return binary, f"sha256:{actual}"


def connect(path: Path, deadline: float, readonly: bool = True) -> sqlite3.Connection:
    """A connection whose statements abort at the deadline or on a signal; checkpoint() then names why."""
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True) if readonly else sqlite3.connect(path)
    db.set_progress_handler(lambda: int(STOP.is_set() or time.monotonic() >= deadline), 1000)
    return db


def snapshot_fixture(source: Path, destination: Path, writer: str, room=lambda: 1 << 62, guard=lambda: None,
                     deadline: float = float("inf")) -> dict:
    """Online Backup snapshot of an exited writer's store, with its PM10 manifest.

    The copy is refused before it starts when the source would not fit the case's
    remaining scratch, and the case guard runs again once it is written. Every
    backup step and statement stops at the deadline or on a signal.
    """
    if source.stat().st_size > room():
        raise CaseFailure("unknown", "disk", f"snapshot of {source.stat().st_size} bytes exceeds the case's remaining scratch")
    reader, target = connect(source, deadline), connect(destination, deadline, readonly=False)
    try:
        reader.backup(target, pages=256, progress=lambda *_: checkpoint(deadline))
    finally:
        target.close()
        reader.close()
    snapshot = connect(destination, deadline)
    try:
        manifest = {
            "schema_version": snapshot.execute("SELECT COALESCE(MAX(version),0) FROM schema_migrations").fetchone()[0],
            "snapshot_id": f"release-restore-gate-{writer}",
            "created_at": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
            "git_commit_ids": {},
            "binary_version": writer,
            "integrity_check": snapshot.execute("PRAGMA integrity_check").fetchone()[0],
            "foreign_key_check": [],
            "quick_check": snapshot.execute("PRAGMA quick_check").fetchone()[0],
        }
        foreign = snapshot.execute("PRAGMA foreign_key_check").fetchall()
    except sqlite3.Error:
        checkpoint(deadline)
        raise
    finally:
        snapshot.close()
    if manifest["integrity_check"] != "ok" or manifest["quick_check"] != "ok" or foreign:
        raise CaseFailure("fail", "snapshot", "writer store fails SQLite integrity verification")
    guard()
    manifest["db_checksum"] = sha256_file(destination)
    Path(f"{destination}.manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    return manifest


def effects(path: Path, deadline: float) -> list[str]:
    """The recipe effects absent from a store's current tables."""
    db = connect(path, deadline)
    try:
        return [name for name, query, want in EFFECTS if db.execute(query).fetchall() != [want]]
    except sqlite3.Error:
        checkpoint(deadline)
        raise
    finally:
        db.close()


def restore(candidate: Path, snapshot: Path, restored: Path, case: Path, deadline: float, streams: Path, label: str, guard) -> dict:
    """Use the core's source-to-stage verifier, then check expected current effects.

    The core verifies every table before migration. Post-migration assertions
    below cover the recipe only, not arbitrary schema transformations.
    """
    manifest = json.loads(Path(f"{snapshot}.manifest.json").read_text())
    if sha256_file(snapshot) != manifest["db_checksum"]:
        raise CaseFailure("fail", label, "source snapshot checksum disagrees with its manifest")
    request = json.dumps({"source": str(snapshot), "destination": str(restored)})
    rc, out = run_bounded([str(candidate), "restore"], sandbox_env(case / label), request, deadline, streams, label, guard)
    if rc != 0:
        raise CaseFailure("fail", label, f"candidate restore exited {rc}")
    report = json.loads(out)
    source = report.get("source_snapshot") or {}
    inventory = report.get("restored_database") or {}
    if any(source.get(key) != manifest[key] for key in ("db_checksum", "schema_version", "snapshot_id")):
        raise CaseFailure("fail", label, "restore report disagrees with the source snapshot")
    if (type(inventory.get("schema_version")) is not int or inventory["schema_version"] < manifest["schema_version"]
            or inventory.get("db_checksum") != sha256_file(restored)):
        raise CaseFailure("fail", label, "restore report disagrees with the restored database")
    if sha256_file(snapshot) != manifest["db_checksum"]:
        raise CaseFailure("fail", "conservation", "restore modified the source snapshot")
    missing = effects(restored, deadline)
    if missing:
        raise CaseFailure("fail", "effects", f"{label} lacks the recipe's {', '.join(missing)}")
    return inventory


def invoke_runner(binary: Path, env: dict[str, str], streams: Path, deadline: float, guard):
    """A `run(verb, request) -> (rc, out, err)` for the invoke phase, confined like every writer step."""
    calls = iter(range(1 << 30))

    def run(verb: str, request: dict) -> tuple[int, str, str]:
        label = f"invoke-{next(calls)}-{verb}"
        rc, out = run_bounded([str(binary), verb], env, json.dumps(request), deadline, streams, label, guard)
        return rc, out, (streams / f"{label}.stderr").read_text(errors="replace")

    return run


def check_invoke_effects(path: Path, record: dict, deadline: float, status: str, stage: str) -> None:
    db = connect(path, deadline)
    try:
        verify_invoke_snapshot(db, record)
    except InvokeError as error:
        raise CaseFailure(status, stage, error.detail) from None
    finally:
        db.close()


def run_writer_case(repository: str, writer: Writer, candidate: Path, work: Path, streams: Path, case_seconds: int,
                    wall_deadline: float, scratch: Scratch, transfer: Transfer) -> dict:
    started = time.monotonic()
    repo = Path(__file__).resolve().parents[1]
    steps = recipe_for(writer.tag)
    # No hash claims a plan when the wall is exhausted or its source is unreadable.
    row = dataclasses.asdict(writer) | {"recipe": RECIPE_ID, "recipe_sha256": "", "streams": str(streams)}
    case = work / writer.tag

    def room() -> int:
        return scratch.allowance() - tree_bytes(case) - tree_bytes(streams)

    def check() -> None:
        if room() < 0:
            raise CaseFailure("unknown", "disk", f"case scratch and streams exceed their {scratch.allowance()} byte share")

    guard = Guard(check, scratch.allowance)

    try:
        if started >= wall_deadline:
            raise CaseFailure("unknown", "wall", "global budget exhausted before the case started")
        try:
            facts = tag_facts(repo, writer.tag)
            selection = select_variant(repo, writer.tag, writer.commit)
        except (InvokeError, LegacyProofError) as error:
            raise CaseFailure("unknown", f"invoke-{error.stage}", error.detail) from None
        if selection is not None and selection[0] == LEGACY_VARIANT:
            # The complete whole-file proof selected bootstrap-only before any
            # execution: the report carries the proof's variant, source commit,
            # fingerprint, and component owner hashes.
            variant, proof = selection
            facts = {**facts, "bootstrap_only": True}
            row["source_proof"] = {"variant": variant, "commit": proof["commit"],
                                   "fingerprint": proof["fingerprint"], "owners": proof["owners"]}
        plan = invoke_plan(facts)
        row["recipe_sha256"] = recipe_sha256(steps, plan)
        deadline = min(started + case_seconds, wall_deadline)
        binary, asset_digest = verify_writer_asset(repository, writer, case, deadline, streams, transfer, guard, room)
        env = sandbox_env(case)
        store = Path(env["CONCORD_DB_PATH"])
        for step, (verb, request) in enumerate(steps):
            request = resolve(request, store, case, deadline)
            rc, _ = run_bounded([str(binary), verb], env, json.dumps(request), deadline, streams, f"step-{step}-{verb}", guard)
            if rc != 0:
                raise CaseFailure("unknown", verb, f"writer refused recipe step {step} {verb} with exit {rc}")
        try:
            record = run_invoke_phase(invoke_runner(binary, env, streams, deadline, guard), repo, writer.tag, case, store, facts=facts)
        except InvokeError as error:
            raise CaseFailure("unknown", f"invoke-{error.stage}", error.detail) from None
        if record.get("plan") != list(plan):
            raise CaseFailure("unknown", "invoke-plan", "invoke phase plan diverged from the recipe-hashed plan")
        snapshot = case / "snapshot.db"
        manifest = snapshot_fixture(store, snapshot, writer.tag, room, guard, deadline)
        missing = effects(snapshot, deadline)
        if missing:
            raise CaseFailure("unknown", "snapshot-effects", f"writer lacks the recipe's {', '.join(missing)}")
        check_invoke_effects(snapshot, record, deadline, "unknown", "invoke-snapshot-effects")
        if record["outcome"] != "bootstrap-only":
            row["invoke"] = {"outcome": record["outcome"], "plan": record["plan"]}
        row["source"] = {key: manifest[key] for key in ("schema_version", "db_checksum")}
        row["source"]["asset_digest"] = asset_digest
        row["restored"] = restore(candidate, snapshot, case / "restored.db", case, deadline, streams, "restore", guard)
        check_invoke_effects(case / "restored.db", record, deadline, "fail", "effects")
        again = case / "restored-snapshot.db"
        snapshot_fixture(case / "restored.db", again, f"{writer.tag}-restored", room, guard, deadline)
        row["restored_again"] = restore(candidate, again, case / "restored-again.db", case, deadline, streams, "restore-again", guard)
        check_invoke_effects(case / "restored-again.db", record, deadline, "fail", "restore-again-effects")
        if row["restored_again"]["schema_version"] != row["restored"]["schema_version"]:
            raise CaseFailure("fail", "restore-again", "a second restore changed the candidate schema version")
        guard()
        if time.monotonic() > deadline:
            raise CaseFailure("unknown", "budget", "case finished after its budget")
        row |= {"status": "pass", "stage": "complete", "detail": ""}
    except CaseFailure as failure:
        row |= {"status": failure.status, "stage": failure.stage, "detail": failure.detail}
    except sqlite3.Error as error:
        try:
            checkpoint(deadline)
            row |= {"status": "unknown", "stage": "harness", "detail": f"{type(error).__name__}: {error}"}
        except CaseFailure as failure:
            row |= {"status": failure.status, "stage": failure.stage, "detail": failure.detail}
    except (OSError, ValueError, KeyError, AttributeError, TypeError, subprocess.SubprocessError) as error:
        row |= {"status": "unknown", "stage": "harness", "detail": f"{type(error).__name__}: {error}"}
    row |= {"disk_bytes": tree_bytes(case), "seconds": round(time.monotonic() - started, 3)}
    if row["status"] == "pass":
        shutil.rmtree(case, ignore_errors=True)
    else:
        shutil.rmtree(case / "asset", ignore_errors=True)
    scratch.retain(tree_bytes(case) + tree_bytes(streams))
    return row


def run_gate(args: argparse.Namespace, releases: list[dict], tags: dict[str, tuple[str, str]], wall_deadline: float | None = None,
             scratch: Scratch | None = None) -> tuple[dict, int]:
    wall_deadline = wall_deadline or time.monotonic() + args.wall_seconds
    scratch = scratch or Scratch(args.disk_bytes, args.workers)
    output = Path(args.output_dir).resolve()
    candidate = Path(args.candidate).resolve()
    candidate_sha256 = sha256_file(candidate)
    if time.monotonic() >= wall_deadline:
        raise GateError("the wall budget ran out during enumeration")
    transfer = Transfer(args.transfer_bytes)
    preflight = output / "candidate"

    def check() -> None:
        if tree_bytes(preflight) > scratch.allowance():
            raise CaseFailure("unknown", "disk", "candidate preflight exceeds its scratch share")

    try:
        rc, version = run_bounded([str(candidate), "--version"], sandbox_env(preflight), "",
                                  min(wall_deadline, time.monotonic() + NETWORK_SECONDS), preflight / "streams", "version",
                                  Guard(check, scratch.allowance))
    except CaseFailure as failure:
        raise GateError(f"candidate preflight: {failure.detail}") from None
    scratch.retain(tree_bytes(preflight))
    if rc != 0 or version.strip() != args.candidate_version:
        raise GateError(f"candidate reports {version.strip()!r}, want {args.candidate_version}")
    writers, unknown, unavailable, excluded = enumerate_writers(releases, tags, args.candidate_version, args.target_sha)
    population = [dataclasses.asdict(writer) for writer in writers] + unknown + unavailable
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = [pool.submit(run_writer_case, args.repository, writer, candidate, output / "cases", output / "streams" / writer.tag,
                               args.case_seconds, wall_deadline, scratch, transfer) for writer in writers]
        rows = [future.result() for future in futures]
    if sha256_file(candidate) != candidate_sha256:
        raise GateError("the candidate binary changed during the run")
    rows += unknown
    counts = {status: sum(row["status"] == status for row in rows) for status in ("pass", "fail", "unknown")}
    result = "fail" if counts["fail"] else "unknown" if STOP.is_set() or counts["unknown"] or not writers or len(rows) != len(writers) + len(unknown) else "pass"
    report = {
        "schema": "concord.release-restore-gate.v1", "repository": args.repository, "target_sha": args.target_sha,
        "candidate": {"path": str(candidate), "version": args.candidate_version, "sha256": candidate_sha256},
        "recipe": RECIPE_ID,
        "population": {"digest": hashlib.sha256(json.dumps(population, sort_keys=True).encode()).hexdigest(),
                       "writers": len(writers) + len(unknown), "unavailable": unavailable, "excluded_candidate": excluded},
        "budgets": {"workers": args.workers, "wall_seconds": args.wall_seconds, "case_seconds": args.case_seconds,
                    "disk_bytes": args.disk_bytes, "retained_bytes": scratch.retained,
                    "transfer_bytes": args.transfer_bytes, "transferred_bytes": transfer.used},
        "counts": counts, "result": result,
        "rows": rows,
    }
    return report, 1 if result != "pass" else 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run")
    run.add_argument("--repository", required=True)
    run.add_argument("--target-sha", required=True)
    run.add_argument("--candidate", required=True)
    run.add_argument("--candidate-version", required=True)
    run.add_argument("--output-dir", required=True)
    for name, default in (("--workers", 4), ("--wall-seconds", 14400), ("--case-seconds", 120), ("--disk-bytes", 6 << 30), ("--transfer-bytes", 40 << 30)):
        run.add_argument(name, type=int, default=default)
    args = parser.parse_args(argv)
    install_interrupt_handlers()
    output = Path(args.output_dir)
    wall_deadline = time.monotonic() + args.wall_seconds
    try:
        if not REPOSITORY_RE.match(args.repository) or not FULL_SHA_RE.match(args.target_sha):
            raise GateError("repository must be owner/name and target sha a full commit")
        if not 1 <= args.workers <= 4 or min(args.wall_seconds, args.case_seconds, args.disk_bytes, args.transfer_bytes) < 1:
            raise GateError("workers must be 1-4 and every budget positive")
        if output.exists() and any(output.iterdir()):
            raise GateError(f"output directory {output} is not empty")
        output.mkdir(parents=True, exist_ok=True)
        scratch, population = Scratch(args.disk_bytes, args.workers), output / "streams" / "population"
        run = population_runner(population, wall_deadline, scratch)
        releases = list_releases(gh_release_total(args.repository, run), lambda page: gh_release_page(args.repository, page, run))
        tags = git_tags(args.repository, run)
        scratch.retain(tree_bytes(population))
        report, status = run_gate(args, releases, tags, wall_deadline, scratch)
    except (GateError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        report, status = {"schema": "concord.release-restore-gate.v1", "result": "unknown", "stage": "population", "detail": str(error)}, 1
    output.mkdir(parents=True, exist_ok=True)
    (output / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({key: report.get(key) for key in ("result", "counts", "detail") if key in report}))
    return status


if __name__ == "__main__":
    sys.exit(main())
