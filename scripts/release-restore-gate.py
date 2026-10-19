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

TAG_RE = re.compile(r"\Av(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
FULL_SHA_RE = re.compile(r"\A[0-9a-f]{40}\Z")
REPOSITORY_RE = re.compile(r"\A[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
PER_PAGE = 100
STREAM_LIMIT = 64 * 1024 * 1024
NETWORK_SECONDS = 120
POLL_SECONDS = 0.05
RECIPE_ID = "bootstrap-v1"
# Strict-semver tags that carry no GitHub release by a recorded ruling. The
# entry binds the tag object and commit, so a retargeted tag is unknown again.
SETTLED_UNPUBLISHED = {
    "v8.28.5": ("e92fd8721495219a9d5a1817e2c8264615325d4d", "7193e6260b142ca86817a0b0128d2898da30ca8c"),
}
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


class GateError(Exception):
    """The population or candidate cannot be trusted; no writer result is meaningful."""


class Transfer:
    """The run's shared download allowance, reserved before each asset is fetched."""

    def __init__(self, limit: int) -> None:
        self.limit, self.used, self.lock = limit, 0, threading.Lock()

    def reserve(self, size: int) -> None:
        with self.lock:
            if self.used + size > self.limit:
                raise CaseFailure("unknown", "transfer", f"download of {size} bytes exceeds the {self.limit} byte transfer budget")
            self.used += size


class CaseFailure(Exception):
    def __init__(self, status: str, stage: str, detail: str) -> None:
        super().__init__(detail)
        self.status, self.stage, self.detail = status, stage, detail


@dataclasses.dataclass(frozen=True)
class Writer:
    tag: str
    release_id: int
    tag_object: str
    commit: str
    asset_id: int
    asset_size: int
    asset_digest: str
    checksum_asset_id: int
    checksum_size: int


def semver(tag: str) -> tuple[int, int, int]:
    match = TAG_RE.match(tag)
    if not match:
        raise GateError(f"tag {tag!r} is not a strict release version")
    return tuple(int(part) for part in match.groups())  # type: ignore[return-value]


def run_json(argv: list[str]) -> object:
    completed = subprocess.run(argv, capture_output=True, text=True, timeout=NETWORK_SECONDS)
    if completed.returncode != 0:
        raise GateError(f"{argv[:3]} exited {completed.returncode}: {completed.stderr.strip()[-400:]}")
    return json.loads(completed.stdout)


def gh_release_total(repository: str) -> int:
    owner, name = repository.split("/")
    query = "query($o:String!,$n:String!){repository(owner:$o,name:$n){releases{totalCount}}}"
    document = run_json(["gh", "api", "graphql", "-f", f"query={query}", "-F", f"o={owner}", "-F", f"n={name}"])
    return int(document["data"]["repository"]["releases"]["totalCount"])  # type: ignore[index]


def gh_release_page(repository: str, page: int) -> list[dict]:
    batch = run_json(["gh", "api", f"repos/{repository}/releases?per_page={PER_PAGE}&page={page}"])
    if not isinstance(batch, list) or any(not isinstance(entry, dict) for entry in batch):
        raise GateError(f"release page {page} is not a list of objects")
    return batch


def list_releases(total: int, fetch_page) -> list[dict]:
    """Read release pages until the reported total is accounted for exactly."""
    releases: list[dict] = []
    page = 1
    while len(releases) < total:
        batch = fetch_page(page)
        if not batch:
            break
        releases.extend(batch)
        page += 1
    if len(releases) != total:
        raise GateError(f"release pages returned {len(releases)} of {total} releases")
    return releases


def git_tags(repository: str) -> dict[str, tuple[str, str]]:
    """Strict-semver tags as {tag: (tag object, peeled commit)} from the public remote."""
    completed = subprocess.run(
        ["git", "ls-remote", "--tags", f"https://github.com/{repository}.git"],
        capture_output=True, text=True, timeout=NETWORK_SECONDS,
    )
    if completed.returncode != 0:
        raise GateError(f"git ls-remote exited {completed.returncode}: {completed.stderr.strip()[-400:]}")
    objects: dict[str, str] = {}
    peeled: dict[str, str] = {}
    for line in completed.stdout.splitlines():
        oid, ref = line.split("\t")
        name = ref.removeprefix("refs/tags/")
        target = peeled if name.endswith("^{}") else objects
        target[name.removesuffix("^{}")] = oid
    return {tag: (oid, peeled.get(tag, oid)) for tag, oid in objects.items() if TAG_RE.match(tag)}


def candidate_rerun(tag: str, tags: dict[str, tuple[str, str]], candidate_version: str, target_sha: str) -> bool:
    """An idempotent release rerun: the candidate's own tag already names the target commit."""
    return tag == candidate_version and tags.get(tag, ("", ""))[1] == target_sha


def enumerate_writers(releases: list[dict], tags: dict[str, tuple[str, str]], candidate_version: str, target_sha: str):
    """Join releases to tags. Returns (writers, unknown rows, unavailable rows, excluded tags)."""
    candidate = semver(candidate_version)
    writers: list[Writer] = []
    unknown: list[dict] = []
    seen: set[str] = set()
    excluded: list[str] = []
    for release in releases:
        if release.get("draft"):
            continue
        tag = release.get("tag_name")
        if not isinstance(tag, str) or tag in seen:
            raise GateError(f"release {release.get('id')} has a missing or duplicate tag {tag!r}")
        seen.add(tag)
        if semver(tag) >= candidate:
            if candidate_rerun(tag, tags, candidate_version, target_sha):
                excluded.append(tag)
                continue
            raise GateError(f"published release {tag} is not earlier than candidate {candidate_version}")
        if tag not in tags:
            unknown.append({"tag": tag, "status": "unknown", "stage": "population", "detail": "release has no tag"})
            continue
        assets = {asset.get("name"): asset for asset in release.get("assets", [])}
        binary, checksum = assets.get(f"concord-{tag}"), assets.get(f"concord-{tag}.sha256")
        if binary is None or checksum is None:
            unknown.append({"tag": tag, "status": "unknown", "stage": "artifact", "detail": "binary or checksum asset is absent"})
            continue
        writers.append(Writer(tag, int(release["id"]), tags[tag][0], tags[tag][1], int(binary["id"]), int(binary["size"]),
                              str(binary.get("digest") or ""), int(checksum["id"]), int(checksum["size"])))
    unavailable: list[dict] = []
    for tag in sorted(set(tags) - seen, key=semver):
        if candidate_rerun(tag, tags, candidate_version, target_sha):
            excluded.append(tag)
        elif SETTLED_UNPUBLISHED.get(tag) == tags[tag]:
            unavailable.append({"tag": tag, "status": "artifact_unavailable", "tag_object": tags[tag][0], "commit": tags[tag][1]})
        else:
            unknown.append({"tag": tag, "status": "unknown", "stage": "population", "detail": "tag has no published release"})
    writers.sort(key=lambda writer: semver(writer.tag))
    return writers, unknown, unavailable, excluded


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
    out_path, err_path = stdout_path or streams / f"{label}.stdout", streams / f"{label}.stderr"
    with out_path.open("wb") as out, err_path.open("wb") as err:
        child = subprocess.Popen(argv, env=env, stdin=subprocess.PIPE, stdout=out, stderr=err, start_new_session=True)
        try:
            try:
                child.stdin.write(stdin.encode())  # type: ignore[union-attr]
                child.stdin.close()  # type: ignore[union-attr]
            except BrokenPipeError:
                pass
            while child.poll() is None:
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


def download_asset(repository: str, asset_id: int, size: int, destination: Path, deadline: float, streams: Path, transfer: Transfer) -> None:
    """Fetch one asset by its enumerated ID with an allowlisted authentication environment."""
    transfer.reserve(size)
    auth = {key: os.environ[key] for key in ("PATH", "HOME", "XDG_CONFIG_HOME", "GH_TOKEN", "GITHUB_TOKEN", "GH_HOST") if key in os.environ}
    destination.parent.mkdir(parents=True, exist_ok=True)
    rc, _ = run_bounded(["gh", "api", "-H", "Accept: application/octet-stream", f"repos/{repository}/releases/assets/{asset_id}"],
                        auth, "", min(deadline, time.monotonic() + NETWORK_SECONDS), streams, f"download-{asset_id}",
                        stdout_path=destination, limit=size + STREAM_LIMIT)
    if rc != 0:
        raise CaseFailure("unknown", "download", f"asset {asset_id} download exited {rc}")
    if destination.stat().st_size != size:
        raise CaseFailure("fail", "download", f"asset {asset_id} has {destination.stat().st_size} bytes, enumerated {size}")


def verify_writer_asset(repository: str, writer: Writer, case: Path, deadline: float, streams: Path, transfer: Transfer) -> Path:
    name = f"concord-{writer.tag}"
    binary, checksums = case / "asset" / name, case / "asset" / f"{name}.sha256"
    download_asset(repository, writer.checksum_asset_id, writer.checksum_size, checksums, deadline, streams, transfer)
    download_asset(repository, writer.asset_id, writer.asset_size, binary, deadline, streams, transfer)
    actual = sha256_file(binary)
    listed = {parts[1].lstrip("*"): parts[0] for parts in (line.split() for line in checksums.read_text().splitlines()) if len(parts) == 2}
    if listed.get(name) != actual or (writer.asset_digest and writer.asset_digest != f"sha256:{actual}"):
        raise CaseFailure("fail", "checksum", f"binary sha256 {actual} disagrees with the checksum manifest or asset digest")
    binary.chmod(0o755)
    rc, version = run_bounded([str(binary), "--version"], sandbox_env(case), "", deadline, streams, "version")
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
                    wall_deadline: float, case_disk: int, transfer: Transfer) -> dict:
    started = time.monotonic()
    row = dataclasses.asdict(writer) | {"recipe": RECIPE_ID, "streams": str(streams)}
    case = work / writer.tag

    def guard() -> None:
        if tree_bytes(case) > case_disk:
            raise CaseFailure("unknown", "disk", f"case scratch exceeds its {case_disk} byte share")

    try:
        if started >= wall_deadline:
            raise CaseFailure("unknown", "wall", "global budget exhausted before the case started")
        deadline = min(started + case_seconds, wall_deadline)
        binary = verify_writer_asset(repository, writer, case, deadline, streams, transfer)
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
    return row


def run_gate(args: argparse.Namespace, releases: list[dict], tags: dict[str, tuple[str, str]]) -> tuple[dict, int]:
    wall_deadline = time.monotonic() + args.wall_seconds
    candidate = Path(args.candidate).resolve()
    candidate_sha256 = sha256_file(candidate)
    rc = subprocess.run([str(candidate), "--version"], capture_output=True, text=True, env={"PATH": "/usr/bin:/bin"}, timeout=NETWORK_SECONDS)
    if rc.returncode != 0 or rc.stdout.strip() != args.candidate_version:
        raise GateError(f"candidate reports {rc.stdout.strip()!r}, want {args.candidate_version}")
    writers, unknown, unavailable, excluded = enumerate_writers(releases, tags, args.candidate_version, args.target_sha)
    output = Path(args.output_dir).resolve()
    population = [dataclasses.asdict(writer) for writer in writers] + unknown + unavailable
    transfer = Transfer(args.transfer_bytes)
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = [pool.submit(run_writer_case, args.repository, writer, candidate, output / "cases", output / "streams" / writer.tag,
                               args.case_seconds, wall_deadline, args.disk_bytes // args.workers, transfer) for writer in writers]
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
                    "disk_bytes": args.disk_bytes, "transfer_bytes": args.transfer_bytes, "transferred_bytes": transfer.used},
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
    try:
        if not REPOSITORY_RE.match(args.repository) or not FULL_SHA_RE.match(args.target_sha):
            raise GateError("repository must be owner/name and target sha a full commit")
        if not 1 <= args.workers <= 4 or min(args.wall_seconds, args.case_seconds, args.disk_bytes, args.transfer_bytes) < 1:
            raise GateError("workers must be 1-4 and every budget positive")
        if output.exists() and any(output.iterdir()):
            raise GateError(f"output directory {output} is not empty")
        output.mkdir(parents=True, exist_ok=True)
        releases = list_releases(gh_release_total(args.repository), lambda page: gh_release_page(args.repository, page))
        report, status = run_gate(args, releases, git_tags(args.repository))
    except (GateError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        report, status = {"schema": "concord.release-restore-gate.v1", "result": "unknown", "stage": "population", "detail": str(error)}, 1
    output.mkdir(parents=True, exist_ok=True)
    (output / "report.json").write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(json.dumps({key: report.get(key) for key in ("result", "counts", "coverage", "detail") if key in report}))
    return status


if __name__ == "__main__":
    sys.exit(main())
