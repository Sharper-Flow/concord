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
population is a named `unknown`, never a skip. Exit status: 0 when every writer
passes and shape coverage is closed, 3 when every writer passes but coverage is
held, 1 otherwise. Coverage stays held until the closed historical-shape
inventory is bound, so a bootstrap-only run is never acceptance.

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
import signal
import sqlite3
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from pathlib import Path

from release_restore_population import (
    NETWORK_SECONDS, GateError, Writer, enumerate_writers, gh_release_page, gh_release_total, git_tags, list_releases,
)

FULL_SHA_RE = re.compile(r"\A[0-9a-f]{40}\Z")
REPOSITORY_RE = re.compile(r"\A[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
STREAM_LIMIT = 64 * 1024 * 1024
POLL_SECONDS = 0.05
RECIPE_ID = "bootstrap-v1"
PRODUCT = {
    "product_id": "gate-product", "display_name": "Gate Product", "stage_maturity": "prototype",
    "stage_audience_commitment": "operator_only", "project_id": "gate-project",
    "project_display_name": "Gate Project", "role": "primary", "reason": "release restore gate",
}
LOCATOR = {
    "project_id": "gate-project", "locator_id": "gate-remote", "kind": "git_remote",
    "value": "https://example.invalid/gate/project.git", "expected_version": 1,
}
RECIPE = (("product-create", PRODUCT), ("project-locator-add", LOCATOR))
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


class CaseFailure(Exception):
    def __init__(self, status: str, stage: str, detail: str) -> None:
        super().__init__(detail)
        self.status, self.stage, self.detail = status, stage, detail


def sandbox_env(root: Path) -> dict[str, str]:
    env = {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "HOME": str(root / "home"), "TMPDIR": str(root / "tmp"),
           "CONCORD_DB_PATH": str(root / "store" / "concord.db")}
    for name in ("DATA", "STATE", "CONFIG", "CACHE"):
        env[f"XDG_{name}_HOME"] = str(root / "home" / name.lower())
    for value in (env["HOME"], env["TMPDIR"], str(root / "store"), *(env[f"XDG_{n}_HOME"] for n in ("DATA", "STATE", "CONFIG", "CACHE"))):
        Path(value).mkdir(parents=True, exist_ok=True)
    return env


def run_bounded(argv: list[str], env: dict[str, str] | None, stdin: str, deadline: float, streams: Path, label: str,
                guard=None, stdout_path: Path | None = None, limit: int = STREAM_LIMIT) -> tuple[int, str]:
    """Run one child in its own process group and kill the whole group when it ends.

    The deadline, the stream limit, and the caller's guard are checked while the
    child runs, so a runaway child is stopped rather than measured afterward.
    """
    streams.mkdir(parents=True, exist_ok=True)
    in_path, out_path, err_path = streams / f"{label}.stdin", stdout_path or streams / f"{label}.stdout", streams / f"{label}.stderr"
    in_path.write_text(stdin, encoding="utf-8")
    with in_path.open("rb") as feed, out_path.open("wb") as out, err_path.open("wb") as err:
        child = subprocess.Popen(argv, env=env, stdin=feed, stdout=out, stderr=err, start_new_session=True)
        try:
            # WNOWAIT leaves the exited leader unreaped, so its group ID cannot be
            # reused before the group is killed below.
            while os.waitid(os.P_PID, child.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT) is None:
                if time.monotonic() >= deadline:
                    raise CaseFailure("unknown", label, "case budget exhausted")
                if out_path.stat().st_size + err_path.stat().st_size > limit:
                    raise CaseFailure("unknown", label, f"streams exceeded their {limit} byte budget")
                if guard:
                    guard()
                time.sleep(POLL_SECONDS)
        finally:
            try:
                os.killpg(child.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            child.wait()
    if out_path.stat().st_size + err_path.stat().st_size > limit:
        raise CaseFailure("unknown", label, f"streams exceeded their {limit} byte budget")
    return child.returncode, "" if stdout_path else out_path.read_text(errors="replace")


def tree_bytes(root: Path) -> int:
    return sum(path.stat().st_size for path in root.rglob("*") if path.is_file()) if root.exists() else 0


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def download_asset(repository: str, asset_id: int, size: int, destination: Path, deadline: float, streams: Path,
                   transfer: Transfer, guard, room: int) -> None:
    """Fetch one asset by its enumerated ID with an allowlisted authentication environment."""
    if size > room:
        raise CaseFailure("unknown", "disk", f"asset {asset_id} of {size} bytes exceeds the case's remaining scratch")
    transfer.reserve(size)
    auth = {key: os.environ[key] for key in ("PATH", "HOME", "XDG_CONFIG_HOME", "GH_TOKEN", "GITHUB_TOKEN", "GH_HOST") if key in os.environ}
    destination.parent.mkdir(parents=True, exist_ok=True)
    rc, _ = run_bounded(["gh", "api", "-H", "Accept: application/octet-stream", f"repos/{repository}/releases/assets/{asset_id}"],
                        auth, "", min(deadline, time.monotonic() + NETWORK_SECONDS), streams, f"download-{asset_id}",
                        guard, stdout_path=destination, limit=size + STREAM_LIMIT)
    if rc != 0:
        raise CaseFailure("unknown", "download", f"asset {asset_id} download exited {rc}")
    if destination.stat().st_size != size:
        raise CaseFailure("fail", "download", f"asset {asset_id} has {destination.stat().st_size} bytes, enumerated {size}")


def verify_writer_asset(repository: str, writer: Writer, case: Path, deadline: float, streams: Path, transfer: Transfer, guard, room) -> Path:
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
    return binary


def snapshot_fixture(source: Path, destination: Path, writer: str) -> dict:
    """Online Backup snapshot of an exited writer's store, with its PM10 manifest."""
    reader = sqlite3.connect(f"file:{source}?mode=ro", uri=True)
    target = sqlite3.connect(destination)
    try:
        reader.backup(target)
    finally:
        target.close()
        reader.close()
    snapshot = sqlite3.connect(f"file:{destination}?mode=ro", uri=True)
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
    finally:
        snapshot.close()
    if manifest["integrity_check"] != "ok" or manifest["quick_check"] != "ok" or foreign:
        raise CaseFailure("fail", "snapshot", "writer store fails SQLite integrity verification")
    if manifest["event_watermark"] < 1:
        raise CaseFailure("fail", "snapshot", "writer journal is empty")
    manifest["db_checksum"] = sha256_file(destination)
    Path(f"{destination}.manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    return manifest


def event_rows(path: Path, columns: list[str] | None = None) -> tuple[list[str], list[tuple]]:
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    try:
        columns = columns or [row[1] for row in db.execute("PRAGMA table_info(domain_events)")]
        return columns, db.execute(f"SELECT {', '.join(columns)} FROM domain_events ORDER BY seq").fetchall()
    finally:
        db.close()


def effects(path: Path) -> list[str]:
    """The recipe effects absent from a restored store, by projection."""
    db = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    try:
        return [name for name, query, want in EFFECTS if db.execute(query).fetchall() != [want]]
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
    columns, source_events = event_rows(snapshot)
    try:
        _, restored_events = event_rows(restored, columns)
    except sqlite3.Error as error:
        raise CaseFailure("fail", "conservation", f"restored event log lacks a source column: {error}") from None
    if restored_events[:len(source_events)] != source_events:
        raise CaseFailure("fail", "conservation", "restored event rows differ from the snapshot's bytes")
    missing = effects(restored)
    if missing:
        raise CaseFailure("fail", "effects", f"{label} lacks the recipe's {', '.join(missing)}")
    return inventory | {"appended_events": len(restored_events) - len(source_events)}


def run_writer_case(repository: str, writer: Writer, candidate: Path, work: Path, streams: Path, case_seconds: int,
                    wall_deadline: float, scratch: Scratch, transfer: Transfer) -> dict:
    started = time.monotonic()
    row = dataclasses.asdict(writer) | {"recipe": RECIPE_ID, "streams": str(streams)}
    case = work / writer.tag

    def room() -> int:
        return scratch.allowance() - tree_bytes(case) - tree_bytes(streams)

    def guard() -> None:
        if room() < 0:
            raise CaseFailure("unknown", "disk", f"case scratch and streams exceed their {scratch.allowance()} byte share")

    try:
        if started >= wall_deadline:
            raise CaseFailure("unknown", "wall", "global budget exhausted before the case started")
        deadline = min(started + case_seconds, wall_deadline)
        binary = verify_writer_asset(repository, writer, case, deadline, streams, transfer, guard, room)
        env = sandbox_env(case)
        for verb, request in RECIPE:
            rc, _ = run_bounded([str(binary), verb], env, json.dumps(request), deadline, streams, verb, guard)
            if rc != 0:
                raise CaseFailure("unknown", verb, f"writer refused recipe step {verb} with exit {rc}")
        snapshot = case / "snapshot.db"
        manifest = snapshot_fixture(Path(env["CONCORD_DB_PATH"]), snapshot, writer.tag)
        row["source"] = {key: manifest[key] for key in ("schema_version", "event_watermark", "db_checksum")}
        row["restored"] = restore(candidate, snapshot, case / "restored.db", case, deadline, streams, "restore", guard)
        again = case / "restored-snapshot.db"
        snapshot_fixture(case / "restored.db", again, f"{writer.tag}-restored")
        row["rebuilt"] = restore(candidate, again, case / "rebuilt.db", case, deadline, streams, "rebuild", guard)
        if row["rebuilt"]["appended_events"] or row["rebuilt"]["event_watermark"] != row["restored"]["event_watermark"]:
            raise CaseFailure("fail", "rebuild", "a second restore of the restored store changed its event log")
        guard()
        if time.monotonic() > deadline:
            raise CaseFailure("unknown", "budget", "case finished after its budget")
        row |= {"status": "pass", "stage": "complete", "detail": ""}
    except CaseFailure as failure:
        row |= {"status": failure.status, "stage": failure.stage, "detail": failure.detail}
    except (OSError, ValueError, KeyError, AttributeError, TypeError, sqlite3.Error, subprocess.SubprocessError) as error:
        row |= {"status": "unknown", "stage": "harness", "detail": f"{type(error).__name__}: {error}"}
    row |= {"disk_bytes": tree_bytes(case), "seconds": round(time.monotonic() - started, 3)}
    if row["status"] == "pass":
        shutil.rmtree(case, ignore_errors=True)
    else:
        shutil.rmtree(case / "asset", ignore_errors=True)
    scratch.retain(tree_bytes(case) + tree_bytes(streams))
    return row


def run_gate(args: argparse.Namespace, releases: list[dict], tags: dict[str, tuple[str, str]], wall_deadline: float | None = None) -> tuple[dict, int]:
    wall_deadline = wall_deadline or time.monotonic() + args.wall_seconds
    output = Path(args.output_dir).resolve()
    candidate = Path(args.candidate).resolve()
    candidate_sha256 = sha256_file(candidate)
    try:
        rc, version = run_bounded([str(candidate), "--version"], sandbox_env(output / "candidate"), "",
                                  min(wall_deadline, time.monotonic() + NETWORK_SECONDS), output / "streams" / "candidate", "version")
    except CaseFailure as failure:
        raise GateError(f"candidate preflight: {failure.detail}") from None
    if rc != 0 or version.strip() != args.candidate_version:
        raise GateError(f"candidate reports {version.strip()!r}, want {args.candidate_version}")
    writers, unknown, unavailable, excluded = enumerate_writers(releases, tags, args.candidate_version, args.target_sha)
    population = [dataclasses.asdict(writer) for writer in writers] + unknown + unavailable
    transfer, scratch = Transfer(args.transfer_bytes), Scratch(args.disk_bytes, args.workers)
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = [pool.submit(run_writer_case, args.repository, writer, candidate, output / "cases", output / "streams" / writer.tag,
                               args.case_seconds, wall_deadline, scratch, transfer) for writer in writers]
        rows = [future.result() for future in futures]
    if sha256_file(candidate) != candidate_sha256:
        raise GateError("the candidate binary changed during the run")
    rows += unknown
    counts = {status: sum(row["status"] == status for row in rows) for status in ("pass", "fail", "unknown")}
    result = "fail" if counts["fail"] else "unknown" if counts["unknown"] or not writers or len(rows) != len(writers) + len(unknown) else "pass"
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
        "coverage": {"status": "held", "detail": "the closed historical-shape inventory is not bound; a bootstrap recipe is not acceptance"},
        "rows": rows,
    }
    return report, 3 if result == "pass" else 1


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
        releases = list_releases(gh_release_total(args.repository), lambda page: gh_release_page(args.repository, page))
        report, status = run_gate(args, releases, git_tags(args.repository), wall_deadline)
    except (GateError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        report, status = {"schema": "concord.release-restore-gate.v1", "result": "unknown", "stage": "population", "detail": str(error)}, 1
    output.mkdir(parents=True, exist_ok=True)
    (output / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({key: report.get(key) for key in ("result", "counts", "coverage", "detail") if key in report}))
    return status


if __name__ == "__main__":
    sys.exit(main())
