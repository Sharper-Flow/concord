#!/usr/bin/env python3
"""Prove that a candidate core restores an event log written by every earlier release.

Every published, nondraft release of the repository is a writer. For each one
the gate downloads the published standalone binary, verifies it against the
release's asset digest and checksum manifest, and runs a pinned recipe through
that binary's own commands in a disposable root. It then takes an SQLite Online
Backup snapshot after the writer exits and restores the snapshot with the
candidate's `restore` command, then snapshots and restores that result again. Restore migrates a clean stage and rebuilds its
projections from the log (internal/store/backup.go verifyRestoredStage). The
gate then requires the source event rows to survive byte for byte, the
recipe's effects to be present with their values, and the second restore to
reproduce the first. A candidate migration may append events after
the writer's watermark (a locator renormalization does); those are reported,
and the writer's events must remain the unchanged prefix.

Assets are fetched by the asset IDs read at enumeration, so a replaced asset
is refused rather than verified. A missing artifact, refused recipe step,
exhausted budget, or incomplete
population is a named `unknown`, never a skip.

A passing case emits one writer-produced witness per source event row to
`witnesses.jsonl`: the row's exact bytes with the tag, verified asset digest,
recipe hash, and snapshot checksum that produced it. A row is emitted only
after it survived both candidate restores, and the recipe is a closed list of
domain commands, so no imported or synthesized row can become a witness.
scripts/release-restore-inventory/ binds each one to its own release's
registered shape or older accepted version. Exit status: 0 when every writer
passes and shape coverage is closed, 3 when every writer passes but coverage is
held, 1 otherwise. Coverage closes only when the committed shape census
inventories every writer outside the approved exclusion, witnesses every shape
and every older accepted version it writes, and binds every writer row, so a
gate run alone is never
acceptance.

The v0.1.0-v0.7.1 writers run the source-proven bootstrap-only variant:
their complete whole-file owner fingerprints (scripts/release_restore_legacy.py)
select the operator product-create/project-locator-add pair before any
execution, the invoke phase registers and invokes nothing, and the case
report carries the proof's variant, source commit, fingerprint, and
component owner hashes. A drifted, disconnected, or unbound legacy source
holds by name; it never becomes bootstrap-only.

Writer processes get a constructed environment: synthetic HOME, XDG, TMPDIR,
and CONCORD_DB_PATH roots, and no inherited variable. Earlier releases predate
the installed-store refusal (CD-0240), so an inherited CONCORD_DB_PATH would
send them to the live store.

The caller owns candidate provenance: it builds the candidate from the target
commit and passes both. The gate binds the candidate's bytes by hashing them
before the first case and refusing a change by the end of the run.
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
from release_restore_invoke import InvokeError, invoke_plan, run_invoke_phase, tag_facts
from release_restore_legacy import LEGACY_VARIANT, LegacyProofError, select_variant
from release_restore_population import (
    GateError, Writer, enumerate_writers, gh_release_page, gh_release_total, git_tags, list_releases, semver,
)

FULL_SHA_RE = re.compile(r"\A[0-9a-f]{40}\Z")
REPOSITORY_RE = re.compile(r"\A[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
NETWORK_SECONDS = 120
RECIPE_ID = "domain-v2"
CENSUS_DIR = Path(__file__).resolve().parents[1] / "cmd" / "concord" / "testdata" / "release_restore"
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
    return sum(path.stat().st_size for path in root.rglob("*") if path.is_file()) if root.exists() else 0


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


def witnesses(snapshot: Path, deadline: float, provenance: dict) -> list[dict]:
    """One witness per writer event row, with the provenance that produced it."""
    _, rows = event_rows(snapshot, deadline, ["seq", "event_id", "kind", "payload_version", "payload"])
    return [{"writer": provenance, "seq": seq, "event_id": event_id, "kind": kind, "payload_version": payload_version,
             "payload": payload.decode() if isinstance(payload, bytes) else payload}
            for seq, event_id, kind, payload_version, payload in rows]


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
            "event_watermark": snapshot.execute("SELECT COALESCE(MAX(seq),0) FROM domain_events").fetchone()[0],
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
    if manifest["event_watermark"] < 1:
        raise CaseFailure("fail", "snapshot", "writer journal is empty")
    guard()
    manifest["db_checksum"] = sha256_file(destination)
    Path(f"{destination}.manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    return manifest


def event_rows(path: Path, deadline: float, columns: list[str] | None = None) -> tuple[list[str], list[tuple]]:
    db = connect(path, deadline)
    try:
        columns = columns or [row[1] for row in db.execute("PRAGMA table_info(domain_events)")]
        return columns, db.execute(f"SELECT {', '.join(columns)} FROM domain_events ORDER BY seq").fetchall()
    except sqlite3.Error:
        checkpoint(deadline)
        raise
    finally:
        db.close()


def effects(path: Path, deadline: float) -> list[str]:
    """The recipe effects absent from a restored store, by projection."""
    db = connect(path, deadline)
    try:
        return [name for name, query, want in EFFECTS if db.execute(query).fetchall() != [want]]
    except sqlite3.Error:
        checkpoint(deadline)
        raise
    finally:
        db.close()


def restore(candidate: Path, snapshot: Path, restored: Path, case: Path, deadline: float, streams: Path, label: str, guard) -> dict:
    manifest = json.loads(Path(f"{snapshot}.manifest.json").read_text())
    request = json.dumps({"source": str(snapshot), "destination": str(restored)})
    rc, out = run_bounded([str(candidate), "restore"], sandbox_env(case / label), request, deadline, streams, label, guard)
    if rc != 0:
        raise CaseFailure("fail", label, f"candidate restore exited {rc}")
    report = json.loads(out)
    inventory = report.get("restored_database") or {}
    if report.get("source_snapshot", {}).get("db_checksum") != manifest["db_checksum"] or inventory.get("event_watermark", -1) < manifest["event_watermark"]:
        raise CaseFailure("fail", label, "restore report disagrees with the snapshot inventory")
    if sha256_file(snapshot) != manifest["db_checksum"]:
        raise CaseFailure("fail", "conservation", "restore modified the source snapshot")
    columns, source_events = event_rows(snapshot, deadline)
    try:
        _, restored_events = event_rows(restored, deadline, columns)
    except sqlite3.Error as error:
        raise CaseFailure("fail", "conservation", f"restored event log lacks a source column: {error}") from None
    if restored_events[:len(source_events)] != source_events:
        raise CaseFailure("fail", "conservation", "restored event rows differ from the snapshot's bytes")
    missing = effects(restored, deadline)
    if missing:
        raise CaseFailure("fail", "effects", f"{label} lacks the recipe's {', '.join(missing)}")
    return inventory | {"appended_events": len(restored_events) - len(source_events)}


def invoke_runner(binary: Path, env: dict[str, str], streams: Path, deadline: float, guard):
    """A `run(verb, request) -> (rc, out, err)` for the invoke phase, confined like every writer step."""
    calls = iter(range(1 << 30))

    def run(verb: str, request: dict) -> tuple[int, str, str]:
        label = f"invoke-{next(calls)}-{verb}"
        rc, out = run_bounded([str(binary), verb], env, json.dumps(request), deadline, streams, label, guard)
        return rc, out, (streams / f"{label}.stderr").read_text(errors="replace")

    return run


def run_writer_case(repository: str, writer: Writer, candidate: Path, work: Path, streams: Path, case_seconds: int,
                    wall_deadline: float, scratch: Scratch, transfer: Transfer) -> dict:
    started = time.monotonic()
    repo = Path(__file__).resolve().parents[1]
    steps = recipe_for(writer.tag)
    # No hash claims a plan when the wall is exhausted or its source is unreadable.
    row = dataclasses.asdict(writer) | {"recipe": RECIPE_ID, "recipe_sha256": "", "streams": str(streams), "witnesses": []}
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
        if record["outcome"] != "bootstrap-only":
            row["invoke"] = {"outcome": record["outcome"], "plan": record["plan"]}
        snapshot = case / "snapshot.db"
        manifest = snapshot_fixture(store, snapshot, writer.tag, room, guard, deadline)
        row["source"] = {key: manifest[key] for key in ("schema_version", "event_watermark", "db_checksum")}
        row["restored"] = restore(candidate, snapshot, case / "restored.db", case, deadline, streams, "restore", guard)
        again = case / "restored-snapshot.db"
        snapshot_fixture(case / "restored.db", again, f"{writer.tag}-restored", room, guard, deadline)
        row["rebuilt"] = restore(candidate, again, case / "rebuilt.db", case, deadline, streams, "rebuild", guard)
        if row["rebuilt"]["appended_events"] or row["rebuilt"]["event_watermark"] != row["restored"]["event_watermark"]:
            raise CaseFailure("fail", "rebuild", "a second restore of the restored store changed its event log")
        row["witnesses"] = witnesses(snapshot, deadline, {"tag": writer.tag, "asset_digest": asset_digest, "recipe": RECIPE_ID,
                                                          "recipe_sha256": row["recipe_sha256"], "snapshot_sha256": manifest["db_checksum"]})
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


def shape_coverage(census: Path, writers: list[Writer]) -> dict:
    """Close coverage only when the committed census (scripts/release-restore-inventory/)
    inventories every writer outside the approved exclusion, witnesses every shape and older
    accepted version, and binds every writer-produced row."""
    try:
        rows = {row["tag"]: row for row in json.loads((census / "writers.json").read_text(encoding="utf-8"))["rows"]}
        census_coverage = json.loads((census / "coverage.json").read_text(encoding="utf-8"))
        shapes = census_coverage["shapes"]
        written = {tag for shape in shapes for tag in shape["writers"]}
        gaps = {
            "uninventoried writers": [w.tag for w in writers if w.tag not in rows],
            "refused writers": [tag for tag, row in rows.items() if row["status"] != "inventoried" and not row.get("excluded")],
            "inventoried writers without shapes": [tag for tag, row in rows.items() if row["status"] == "inventoried" and tag not in written],
            "unwitnessed shapes": [f"{shape['kind']} v{shape['payload_version']}" for shape in shapes if not shape["covered"]],
            "unwitnessed older versions": [f"{older['kind']} v{older['payload_version']}" for older in census_coverage["older_versions"] if not older["covered"]],
            "unbound writer witnesses": list(census_coverage["unbound_writer_witnesses"]),
        }
    except (OSError, ValueError, KeyError, TypeError) as error:
        return {"status": "held", "detail": f"the shape census at {census} is unreadable: {error}"}
    gaps = {name: items for name, items in gaps.items() if items}
    if gaps:
        return {"status": "held", "detail": "; ".join(f"{len(items)} {name}, first {items[0]}" for name, items in gaps.items())}
    return {"status": "closed", "writers": len(rows), "shapes": len(shapes)}


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
    emitted = [witness for row in rows for witness in row.pop("witnesses") if row["status"] == "pass"]
    (output / "witnesses.jsonl").write_text("".join(json.dumps(w, sort_keys=True) + "\n" for w in emitted), encoding="utf-8")
    rows += unknown
    coverage = shape_coverage(Path(args.census_dir), writers)
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
        "counts": counts, "result": result, "witnesses": len(emitted),
        "coverage": coverage,
        "rows": rows,
    }
    return report, 1 if result != "pass" else 0 if coverage["status"] == "closed" else 3


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = parser.add_subparsers(dest="command", required=True)
    run = sub.add_parser("run")
    run.add_argument("--repository", required=True)
    run.add_argument("--target-sha", required=True)
    run.add_argument("--candidate", required=True)
    run.add_argument("--candidate-version", required=True)
    run.add_argument("--output-dir", required=True)
    run.add_argument("--census-dir", default=str(CENSUS_DIR))
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
    print(json.dumps({key: report.get(key) for key in ("result", "counts", "coverage", "detail") if key in report}))
    return status


if __name__ == "__main__":
    sys.exit(main())
