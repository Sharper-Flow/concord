#!/usr/bin/env python3
"""Integrate one reviewed, non-divergent Go change into an unoccupied rapid ref.

The coordinating agent supplies a JSON request with target_ref, target_tip,
candidate, expires_at (timezone required), declaration_ref, review_ref,
review_digest (sha256), reviewed_commit, eligibility_ref, eligible_paths and a
conventional title.
Those references retain the existing operator declaration, independent review
and D4 classification; this helper does not grant or authenticate them. The
coordinator must establish them before invoking it. --review preserves the
original review artifact, without converting it to workflow evidence.

Use an existing host-isolated, clean candidate worktree. Only classified Go
paths in internal/ are supported; all other changes take the standard route.
The target must be an ancestor of the candidate: the resulting merge tree is
therefore exactly the reviewed and tested tree. Divergent integration requires
new review and is not supported here. Neither worktree creation nor checkout is
performed. Executable commit hooks require the standard merge route instead
of being bypassed by plumbing. Pre-push hooks and main CI remain unchanged.

Gate commands come from the base's CI verify-go job, not a local mirror.
Full outputs, commands, subjects and digests are retained in a fresh external
--evidence-dir; archive it with the owning evidence record. A successful result
asserts phase-local integration only. It does not assert workflow admission,
completion, main delivery, release or installation. Installed subject-bound
evidence retains its original subject; it is never relabelled for the merge.
"""

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shlex
import subprocess
import sys

import git_environment
import yaml


class Refused(RuntimeError):
    pass


def digest(data):
    return "sha256:" + hashlib.sha256(data).hexdigest()


def integrate(worktree, request, review, evidence):
    env = git_environment.sanitized_environment()

    def git(*args, input=None):
        result = subprocess.run(["git", "-C", str(worktree), *args], input=input,
                                env=env, capture_output=True, text=True, timeout=30)
        if result.returncode:
            raise Refused(f"git {args!r} failed (exit {result.returncode}):\n{result.stdout}{result.stderr}")
        return result.stdout.strip()

    required = {"target_ref", "target_tip", "candidate", "expires_at", "declaration_ref",
                "review_ref", "review_digest", "reviewed_commit", "eligibility_ref", "eligible_paths", "title"}
    if not isinstance(request, dict) or set(request) != required:
        raise Refused("request requires exactly: " + ", ".join(sorted(required)))
    if any(not isinstance(request[k], str) or not request[k].strip() or "\n" in request[k]
           for k in required - {"eligible_paths"}):
        raise Refused("request references must be nonempty single-line strings")
    target, base, candidate = (request[k] for k in ("target_ref", "target_tip", "candidate"))
    if not target.startswith("refs/heads/rapid/"):
        raise Refused("target must name one explicit refs/heads/rapid/ branch, never main")
    git("check-ref-format", target)
    for oid in (base, candidate):
        if not re.fullmatch(r"[0-9a-f]{40}", oid) or git("rev-parse", oid + "^{commit}") != oid:
            raise Refused("target tip and candidate must be exact commit OIDs")
    expiry = dt.datetime.fromisoformat(request["expires_at"])
    if expiry.tzinfo is None:
        raise Refused("phase expiry requires a timezone")

    def current():
        if dt.datetime.now(dt.timezone.utc) >= expiry:
            raise Refused("rapid phase expired; no integration permitted")
        if git("rev-parse", "--verify", target) != base:
            raise Refused("target tip changed; prepare and review integration again")
        if git("rev-parse", "HEAD") != candidate or git("status", "--porcelain", "--untracked-files=all"):
            raise Refused("candidate worktree must remain clean at the exact reviewed commit")
        if "branch " + target in git("worktree", "list", "--porcelain").splitlines():
            raise Refused("rapid target is checked out; refuse to invalidate its index or worktree")

    current()
    if request["reviewed_commit"] != candidate:
        raise Refused("independent review must identify the exact candidate commit")
    review_bytes = review.read_bytes()
    if not review_bytes or digest(review_bytes) != request["review_digest"]:
        raise Refused("independent review artifact must match its exact nonempty digest")
    if git("rev-parse", "--git-dir") == git("rev-parse", "--git-common-dir"):
        raise Refused("candidate requires an existing isolated linked worktree")
    if git("symbolic-ref", "HEAD") == target:
        raise Refused("candidate and rapid target must be distinct")
    symbolic = subprocess.run(["git", "-C", str(worktree), "symbolic-ref", "-q", target],
                              env=env, capture_output=True, timeout=30)
    if symbolic.returncode != 1:
        raise Refused("rapid target must be a direct ref")
    if git("merge-base", base, candidate) != base or base == candidate:
        raise Refused("only a non-divergent candidate strictly beyond the target is supported")
    changed = git("diff", "--name-only", "--no-renames", base, candidate).splitlines()
    paths = request["eligible_paths"]
    if not isinstance(paths, list) or not paths or sorted(paths) != sorted(changed):
        raise Refused("eligible_paths must classify the complete candidate delta")
    for path in paths:
        parts = PurePosixPath(path).parts
        if len(parts) < 3 or parts[0] != "internal" or ".." in parts or not path.endswith(".go"):
            raise Refused("only explicitly eligible internal Go paths are supported; use the standard route")
        if parts[1] in {"store", "agent", "receipt", "hostlease", "sessionboot", "payloadschema"}:
            raise Refused("safety and admission owners are unsupported by this bounded route")
        entry = git("ls-tree", candidate, "--", path)
        if not entry:
            raise Refused("deleted Go paths require the standard route: " + path)
        mode = entry.split()[0]
        if mode not in ("100644", "100755"):
            raise Refused("eligible paths must be ordinary Go files, not symlinks or submodules")
    hooks = Path(git("rev-parse", "--path-format=absolute", "--git-path", "hooks"))
    for name in ("pre-merge-commit", "pre-commit", "prepare-commit-msg", "commit-msg", "post-commit"):
        if os.access(hooks / name, os.X_OK):
            raise Refused("executable commit hooks require the standard merge route: " + name)
    workflow = yaml.safe_load(git("show", base + ":.github/workflows/ci.yml"))
    job = workflow["jobs"]["verify-go"]
    if "env" in job or "defaults" in job or "container" in job or "services" in job:
        raise Refused("verify-go job has unsupported local execution semantics")
    steps = job["steps"]
    if any("if" in step or "env" in step or "working-directory" in step or "shell" in step
           for step in steps if "run" in step):
        raise Refused("verify-go step has unsupported execution semantics")
    commands = [step["run"] for step in steps if "run" in step]
    if not commands or any(not isinstance(cmd, str) or not cmd.strip() for cmd in commands):
        raise Refused("verify-go declares no executable local gate")
    evidence = evidence.resolve()
    common = Path(git("rev-parse", "--path-format=absolute", "--git-common-dir")).resolve()
    roots = [common] + [
        Path(entry[9:]).resolve()
        for entry in git("worktree", "list", "--porcelain", "-z").split("\0")
        if entry.startswith("worktree ")]
    if any(evidence == root or root in evidence.parents for root in roots):
        raise Refused("evidence directory must be outside the repository and Git directory")
    evidence.mkdir()  # Exclusive: never overwrite or reuse another run's records.
    (evidence / "review").write_bytes(review_bytes)
    (evidence / "request.json").write_text(json.dumps(request, sort_keys=True) + "\n")
    tree = git("rev-parse", candidate + "^{tree}")
    gates = []

    def gate(name, argv):
        current()
        output = evidence / (name + ".log")
        failure = None
        code = None
        try:
            with output.open("wb") as stream:
                with subprocess.Popen(argv, cwd=worktree, env=env, stdout=stream,
                                      stderr=subprocess.STDOUT) as child:
                    try:
                        code = child.wait(timeout=300)
                    except subprocess.TimeoutExpired:
                        # Give the wrapper/host gate its cancellation signal;
                        # subprocess.run's timeout kills only the wrapper.
                        child.terminate()
                        try:
                            child.wait(timeout=10)
                        except subprocess.TimeoutExpired:
                            child.kill()
                            child.wait()
                        raise
        except (OSError, subprocess.SubprocessError) as error:
            failure = str(error)
        row = {"name": name, "argv": argv, "exit_code": code, "failure": failure,
               "file": output.name, "digest": digest(output.read_bytes()),
               "candidate": candidate, "tree": tree}
        gates.append(row)
        (evidence / "gates.json").write_text(json.dumps(gates, sort_keys=True) + "\n")
        if failure or code != 0:
            raise Refused(f"{name} failed (exit {code}, error {failure}); output retained in {output}")
        current()

    gate("title", ["python3", "scripts/check-commit-title.py", request["title"]])
    # Each step gets its own strict shell, as in CI; do not join shell state.
    shell = "\n".join("bash -euo pipefail -c " + shlex.quote(cmd) for cmd in commands)
    gate("verify-go", ["bin/oc-test", "broad", "--", "bash", "-euo", "pipefail", "-c", shell])
    packages = sorted({"./" + str(PurePosixPath(path).parent) for path in paths})
    gate("targeted-tests", ["bin/oc-test", "broad", "--", "go", "test", "-timeout=5m", *packages])
    message = request["title"] + "\n\nPhase-local integration only.\n"
    for name, value in {"Target": target, "Base": base, "Candidate": candidate, "Tree": tree,
                        "Declaration": request["declaration_ref"], "Expiry": request["expires_at"],
                        "Review": request["review_ref"], "Review-Digest": digest(review_bytes),
                        "Eligibility": request["eligibility_ref"]}.items():
        message += f"Rapid-{name}: {value}\n"
    for row in gates:
        message += f"Rapid-Gate-{row['name']}: {row['digest']}\n"
    merge = git("commit-tree", tree, "-p", base, "-p", candidate, input=message)
    current()
    git("update-ref", "--no-deref", "-m", "rapid phase integration", target, merge, base)
    receipt = {"state": "phase_integrated", "target_ref": target, "base": base,
               "candidate": candidate, "tree": tree, "merge_commit": merge, "gates": gates}
    (evidence / "integration.json").write_text(json.dumps(receipt, sort_keys=True) + "\n")
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--worktree", type=Path, required=True)
    parser.add_argument("--request", type=Path, required=True)
    parser.add_argument("--review", type=Path, required=True)
    parser.add_argument("--evidence-dir", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = integrate(args.worktree, json.loads(args.request.read_text()), args.review, args.evidence_dir)
    except (Refused, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError,
            git_environment.GitEnvironmentError, yaml.YAMLError) as error:
        print(f"rapid integration failed: {error}; inspect target ref and retained evidence before retry", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
