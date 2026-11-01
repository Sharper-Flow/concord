"""Fixed-input, branch-only WAL diagnosis. No test execution in this helper."""

import hashlib
from datetime import datetime
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from urllib.request import Request, urlopen


BASE = "a8b034bdc99948c23d8938d183e95d2946434501"
REPORTING = "b26f46da96bf804d00cda4a1d70d1043969dc093"
PATCH_HASH = "2b6310fd1f36c97fd43d010cc0ae1b72e8ea741060cbb3cfa8e0e49a0408c14a"
NAMES_HASH = "c0eb090db11fa12a56e53eda96e1251cdead7ffa79c16a6c2f1e30979ebb5f38"
PATTERN_HASH = "8f417de0f016a1a7ccd81179e991e0e81158e96e3de618c145025b2645b5ce83"
PATHS = ["internal/store/backup.go", "internal/store/backup_readonly_test.go"]
POSTIMAGES = [
    "cdb775ba9c11b56f6c0dad35e7e9ff45f5c615ba",
    "17426d019048e705957ecfa330a10518025085db",
]
POISON = {
    "CONCORD_SELECTED_PRODUCT_ID": "poison-product",
    "CONCORD_SELECTED_WORK_ID": "poison-work",
    "CONCORD_PRODUCT_ID": "poison-product",
    "CONCORD_DB_PATH": "/nonexistent/poison.db",
    "CONCORD_SELECTED_PROJECT_ID": "poison-project",
    "CONCORD_HOST_INSTRUCTIONS": "/nonexistent/poison-instructions",
    "CONCORD_CLIENT_REF": "poison-client",
    "CONCORD_SOURCE": "/nonexistent/poison-source",
    "CONCORD_BIN": "/nonexistent/poison-concord",
    "CONCORD_FUTURE_INPUT": "poison-future",
    "CONCORD_TEST_SUBPROCESS": "poison-no-bypass",
}


def require(condition, message):
    if not condition:
        raise SystemExit(message)


def command(cwd, *argv, data=None):
    return subprocess.run(
        argv, cwd=cwd, input=data, stdout=subprocess.PIPE, check=True, timeout=60
    ).stdout


def digest(data):
    return hashlib.sha256(data).hexdigest()


def inputs(repo):
    names = Path(__file__).with_name("con-1610-names.txt").read_bytes()
    require(digest(names) == NAMES_HASH, "frozen names hash mismatch")
    names = names.decode().splitlines()
    require(len(names) == len(set(names)) == 306, "expected 306 distinct names")
    # This is byte reconstruction of the frozen literal, not shard discovery.
    pattern = ("^(" + "|".join(names) + ")$").encode()
    require(digest(pattern) == PATTERN_HASH, "frozen literal pattern hash mismatch")
    patch = command(repo, "git", "diff", "--full-index", BASE, REPORTING, "--", *PATHS)
    require(digest(patch) == PATCH_HASH, "frozen reporting patch hash mismatch")
    return pattern, patch


def hosted_root():
    require(os.environ.get("GITHUB_ACTIONS") == "true", "hosted execution only")
    require(os.environ.get("GITHUB_REPOSITORY") == "Sharper-Flow/concord", "wrong repository")
    require(os.environ.get("GITHUB_EVENT_NAME") == "push", "push event required")
    require(os.environ.get("GITHUB_REF") == "refs/heads/diag/con-1610-wal-allocation-2", "wrong branch")
    require(os.environ.get("GITHUB_RUN_ATTEMPT") == "1", "reruns are not admitted")
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    require(event.get("created") is True and event.get("deleted") is False, "branch creation only")
    require(os.environ.get("RUNNER_OS") == "Linux", "Linux required")
    require(os.environ.get("RUNNER_ARCH") == "X64", "X64 required")
    return Path(os.environ["GITHUB_WORKSPACE"])


def prepare(root):
    subject = root / "subject"
    output = root / "diagnosis"
    output.mkdir(exist_ok=False)
    pattern, patch = inputs(subject)
    require(command(subject, "git", "rev-parse", "HEAD").decode().strip() == BASE, "wrong subject base")
    require(not command(subject, "git", "status", "--porcelain=v1"), "subject must start clean")
    command(subject, "git", "apply", "--check", "-", data=patch)
    command(subject, "git", "apply", "--index", "-", data=patch)
    require(command(subject, "git", "diff", "--cached", "--full-index") == patch, "unexpected subject delta")
    for path, blob in zip(PATHS, POSTIMAGES):
        require(command(subject, "git", "hash-object", path).decode().strip() == blob, "wrong postimage")
    (output / "pattern.txt").write_bytes(pattern)
    (output / "reporting-only.patch").write_bytes(patch)
    identity = {
        "harness_commit": os.environ["GITHUB_SHA"],
        "harness_tree": command(root, "git", "rev-parse", "HEAD^{tree}").decode().strip(),
        "subject_base_commit": BASE,
        "reporting_commit_two_paths_only": REPORTING,
        "reporting_patch_sha256": digest(patch),
        "effective_subject_tree": command(subject, "git", "write-tree").decode().strip(),
        "subject_status": command(subject, "git", "status", "--porcelain=v1").decode(),
        "names_sha256": NAMES_HASH,
        "pattern_sha256": digest(pattern),
        "requested_argv": ["go", "test", "-timeout=15m", "-run", pattern.decode(), "./internal/store"],
        "effective_argv": ["go", "test", "-json", "-timeout=15m", "-run", pattern.decode(), "./internal/store"],
        "run_url": f"{os.environ['GITHUB_SERVER_URL']}/{os.environ['GITHUB_REPOSITORY']}/actions/runs/{os.environ['GITHUB_RUN_ID']}",
        "run_attempt": 1,
        "claim": "Instrumented original source, not clean original or parent candidate; diagnosis only.",
    }
    (output / "identity.json").write_text(json.dumps(identity, indent=2) + "\n")
    print("Frozen source, reporting delta, effective tree and literal 306-name selection verified.")


def optional_observation(probe):
    try:
        value = probe()
    except (OSError, AttributeError) as error:
        return {"status": "unavailable", "reason": f"{type(error).__name__} (errno={getattr(error, 'errno', None)})"}
    if not value:
        return {"status": "unavailable", "reason": "probe returned no data"}
    return {"status": "observed", "value": value}


def record_runtime(root):
    subject = root / "subject"
    for key, value in POISON.items():
        require(os.environ.get(key) == value, f"wrong poison declaration: {key}")
    go = json.loads(command(subject, "go", "env", "-json", "GOVERSION", "GOOS", "GOARCH", "CGO_ENABLED"))
    require(go == {"GOVERSION": "go1.26.10", "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "1"}, "unexpected Go runtime")
    bun = command(subject, "bun", "--version").decode().strip()
    require(bun == "1.4.0", "unexpected Bun version")
    for key in ("GOFLAGS", "GODEBUG"):
        require(not os.environ.get(key), f"unexpected implicit Go input: {key}")
    command(subject, "go", "clean", "-testcache")
    run_id = int(os.environ["GITHUB_RUN_ID"])
    # Public read only; no token, retry or permission expansion on lookup failure.
    request = Request(
        f"https://api.github.com/repos/Sharper-Flow/concord/actions/runs/{run_id}/attempts/1/jobs",
        headers={"Accept": "application/vnd.github+json", "User-Agent": "concord-wal-diagnosis"},
    )
    with urlopen(request, timeout=15) as response:
        jobs = json.load(response)
    require(jobs["total_count"] == len(jobs["jobs"]) == 1, "expected one diagnostic job")
    job = jobs["jobs"][0]
    require(job["name"] == "wal-diagnosis" and job["run_id"] == run_id and job["run_attempt"] == 1, "wrong admission job")
    require(job["head_sha"] == os.environ["GITHUB_SHA"] and job["status"] == "in_progress", "wrong admission candidate/status")
    started = datetime.fromisoformat(job["started_at"].replace("Z", "+00:00"))
    setup_elapsed = time.time() - started.timestamp()
    require(0 <= setup_elapsed <= 90, "setup exceeded 90s; shard execution is not admitted")
    runtime = {
        "go_environment": go,
        "go_version": command(subject, "go", "version").decode().strip(),
        "bun_version": bun,
        "poison_environment": POISON,
        "test_cache_expiration_command": ["go", "clean", "-testcache"],
        "setup_elapsed_seconds_at_admission": setup_elapsed,
        "admission_job": {key: job[key] for key in ("id", "html_url", "started_at", "run_id", "run_attempt", "head_sha")},
        "runner": {key: os.environ.get(key) for key in (
            "RUNNER_OS", "RUNNER_ARCH", "ImageOS", "ImageVersion", "GO_SETUP_CACHE_HIT", "GOMAXPROCS"
        )},
        "cpu_count": optional_observation(os.cpu_count),
        "cpu_affinity": optional_observation(lambda: sorted(os.sched_getaffinity(0))),
        # Raw local controller files do not establish effective CPU capacity.
        "cpu_max": optional_observation(lambda: Path("/sys/fs/cgroup/cpu.max").read_text().strip()),
        "cpu_v1_quota_us": optional_observation(lambda: Path("/sys/fs/cgroup/cpu/cpu.cfs_quota_us").read_text().strip()),
        "cpu_v1_period_us": optional_observation(lambda: Path("/sys/fs/cgroup/cpu/cpu.cfs_period_us").read_text().strip()),
        "implicit_go_input_sha256": {key: digest(os.environ.get(key, "").encode()) for key in ("GOFLAGS", "GODEBUG")},
        "original_image_cpu_cache": "not recorded; exact reproduction cannot be claimed",
    }
    (root / "diagnosis" / "runtime.json").write_text(json.dumps(runtime, indent=2) + "\n")
    print(json.dumps(runtime, indent=2))


def main():
    require(len(sys.argv) == 2 and sys.argv[1] in ("check", "prepare", "runtime", "finish"), "use check, prepare, runtime or finish")
    if sys.argv[1] == "check":
        inputs(Path(__file__).resolve().parents[2])
        print("PASS: frozen names, literal pattern and two-path reporting patch; no Go/test execution")
        return
    root = hosted_root()
    if sys.argv[1] == "prepare":
        prepare(root)
    elif sys.argv[1] == "runtime":
        record_runtime(root)
    else:
        subject = root / "subject"
        patch = command(subject, "git", "diff", "HEAD", "--full-index")
        status = command(subject, "git", "status", "--porcelain=v1").decode()
        (root / "diagnosis" / "status-after.txt").write_text(status)
        (root / "diagnosis" / "diff-after.patch").write_bytes(patch)
        require(digest(patch) == PATCH_HASH, "subject changed during execution")
        require(status.splitlines() == ["M  " + path for path in PATHS], "unexpected subject files after execution")


if __name__ == "__main__":
    main()
